package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/entireio/entire-graph/internal/sem"
	"github.com/entireio/entire-graph/internal/termsafe"
)

// gate — which tests does this change need, and what does no test reach?
//
// The command is the thin shell around sem.Gate: it resolves the repository and the change range,
// builds one snapshot, hands both to the engine, and renders the answer. All judgement lives in
// package sem, so this file stays about argument handling and presentation.

const (
	gateDefaultFormat = "text"
	// Default to the full profile rather than search's "fast". Only the full profile emits TESTS
	// edges (provider.go:6226), and quietly dropping one of the three evidence routes would make
	// the UNCOVERED verdict — the entire point of the command — less trustworthy without saying so.
	gateDefaultProfile = "full"
)

type gateFlags struct {
	Repo         string
	Base         string
	Head         string
	Checkpoint   string
	Format       string
	Profile      string
	Depth        int
	CacheDir     string
	DisableCache bool
	Run          bool
}

func parseGateFlags(args []string) (gateFlags, error) {
	flags := gateFlags{
		Base:    "main",
		Head:    "HEAD",
		Format:  gateDefaultFormat,
		Profile: gateDefaultProfile,
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		value := func() (string, error) {
			index++
			if index >= len(args) {
				return "", fmt.Errorf("%s requires a value", arg)
			}
			return args[index], nil
		}
		var err error
		switch arg {
		case "--repo":
			flags.Repo, err = value()
		case "--base":
			flags.Base, err = value()
		case "--head":
			flags.Head, err = value()
		case "--checkpoint":
			flags.Checkpoint, err = value()
		case "--format":
			flags.Format, err = value()
		case "--profile":
			flags.Profile, err = value()
		case "--cache-dir":
			flags.CacheDir, err = value()
		case "--no-cache":
			flags.DisableCache = true
		case "--run":
			flags.Run = true
		case "--depth":
			parsed, next, parseErr := searchPositiveIntFlag(args, index)
			if parseErr != nil {
				return flags, parseErr
			}
			flags.Depth, index = parsed, next
		default:
			return flags, fmt.Errorf("gate received unexpected argument %q", arg)
		}
		if err != nil {
			return flags, err
		}
	}

	// A checkpoint already names its own range: it resolves to one commit and its first parent.
	// Accepting an explicit range alongside it would let the two disagree with no way to tell
	// which the output described.
	if flags.Checkpoint != "" {
		for _, pair := range []struct{ name, value, standard string }{
			{"--base", flags.Base, "main"},
			{"--head", flags.Head, "HEAD"},
		} {
			if pair.value != pair.standard {
				return flags, fmt.Errorf("gate --checkpoint cannot be combined with %s", pair.name)
			}
		}
	}
	if flags.Format != "text" && flags.Format != "json" {
		return flags, fmt.Errorf("gate --format must be text or json, got %q", flags.Format)
	}
	// The verdict is rendered as prose alongside the analysis. Emitting it beside a JSON document
	// would produce output that is neither valid JSON nor a readable report.
	if flags.Run && flags.Format != "text" {
		return flags, errors.New("gate --run requires --format text")
	}
	if flags.Depth != 0 && flags.Depth != 1 && flags.Depth != 2 {
		return flags, errors.New("gate --depth must be 1 or 2")
	}
	return flags, nil
}

func runGate(ctx context.Context, opts Options, args []string) error {
	flags, err := parseGateFlags(args)
	if err != nil {
		return err
	}
	profile, err := parseProfile(flags.Profile)
	if err != nil {
		return err
	}
	repo, err := resolveRepo(ctx, opts.Env, flags.Repo)
	if err != nil {
		return err
	}
	if err := sem.EnsureGitMetadataSafeForSubprocess(repo); err != nil {
		return err
	}

	var change sem.Result
	if flags.Checkpoint != "" {
		change, err = sem.AnalyzeCheckpoint(ctx, repo, flags.Checkpoint)
	} else {
		// Both revisions are spliced into a git argv, so a value that begins with "-" would stop
		// being a revision and become a flag of the command it lands in. validateRevision is the
		// existing guard for that (root.go:838, CWE-88).
		if err := validateRevision("gate --base", flags.Base); err != nil {
			return err
		}
		if err := validateRevision("gate --head", flags.Head); err != nil {
			return err
		}
		change, err = sem.AnalyzeGitRange(ctx, repo, flags.Base, flags.Head, nil)
	}
	if err != nil {
		return err
	}

	snapshot, _, err := sem.LoadOrBuildProviderSnapshot(ctx, repo, opts.Version, sem.ProviderSnapshotOptions{
		NoNetwork: true,
		Profile:   profile,
	}, resolveCacheDir(flags.CacheDir, opts.Env.PluginDataDir), flags.DisableCache)
	if err != nil {
		return err
	}

	result := sem.Gate(change, snapshot, sem.GateOptions{Depth: flags.Depth})

	if flags.Format == "json" {
		encoder := json.NewEncoder(termsafe.NewJSONWriter(opts.Stdout))
		encoder.SetEscapeHTML(false)
		return encoder.Encode(result)
	}
	writeGateText(opts.Stdout, result)
	if !flags.Run {
		return nil
	}
	return runGateSelection(ctx, opts, repo, result)
}

// runGateSelection executes the selected tests and prints an adjudicated verdict.
//
// Execution and parsing are verify's, reused wholesale: runVerifyCommands runs the command with
// verify's own timeout policy, and parseVerifyOutput recognises the same nine frameworks. All this
// adds is the selection — verify has always been able to run a command, it just had no way to know
// which one this change needed.
func runGateSelection(ctx context.Context, opts Options, repo string, result sem.GateResult) error {
	command := gateGoTestCommand(gateAllSelectedTests(result))
	if command == "" {
		fmt.Fprint(opts.Stdout, "\nNothing to run: no selected test is executable by go test.\n")
		return nil
	}
	output, exitCode, err := runVerifyCommands(ctx, repo, verifyFlags{Test: command, MaxBytes: verifyDefaultMaxBytes})
	if err != nil {
		return err
	}
	results, _, parsed := parseVerifyOutput(output)
	fmt.Fprintf(opts.Stdout, "\n%s\n", termsafe.Line(gateRunSummary(results, parsed, exitCode)))
	return nil
}

// gateRunSummary states the outcome of running the selection, and never more than was measured.
//
// When no parser recognised the output there are no per-test ids, so it reports the exit code and
// says so rather than inventing counts. Failing names are listed because a bare count would send
// the reader back to raw test output, which is the thing verify exists to spare them.
func gateRunSummary(results verifyResults, parsed bool, exitCode int) string {
	if !parsed || len(results) == 0 {
		outcome := "passed"
		if exitCode != 0 {
			outcome = "failed"
		}
		return fmt.Sprintf("VERDICT: selected tests %s (exit %d; no per-test output recognised)", outcome, exitCode)
	}
	var failed []string
	passed := 0
	for name, status := range results {
		if status == verifyStatusPass {
			passed++
			continue
		}
		failed = append(failed, name)
	}
	sort.Strings(failed)
	summary := fmt.Sprintf("VERDICT: %d selected tests ran, %d passed, %d failed", len(results), passed, len(failed))
	if len(failed) > 0 {
		summary += " \u2014 " + strings.Join(failed, ", ")
	}
	return summary
}

// gateGoTestCommand composes one command covering the whole selection.
//
// The existing emitter names a single test (deriveSearchVerifyGo, search_verify.go:919). A change
// set selects a set, so the pattern is an alternation over every selected name, sorted so the same
// selection always renders the same command.
//
// Names go test cannot run are dropped rather than emitted: `-run` only matches functions named
// Test*, so including anything else would produce a command that silently selects nothing — and a
// green run of nothing reads exactly like a green run of everything. When nothing runnable is
// left, no command is emitted at all. Falling back to the whole suite would answer a question the
// caller did not ask; "run everything" is the state they were already in.
func gateGoTestCommand(tests []sem.GateSelectedTest) string {
	seen := map[string]bool{}
	names := make([]string, 0, len(tests))
	for _, test := range tests {
		if !strings.HasPrefix(test.Name, "Test") || seen[test.Name] {
			continue
		}
		seen[test.Name] = true
		names = append(names, test.Name)
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return "go test -run '^(" + strings.Join(names, "|") + ")$' ./..."
}

// gateVerifyCommand chooses between the narrow selection and a widened fallback, and says why
// when it widens.
//
// Returns ("", "") when there is nothing runnable, preserving the existing behaviour that no
// command at all is better than one which silently selects nothing.
func gateVerifyCommand(result sem.GateResult) (command, why string) {
	narrow := gateGoTestCommand(gateAllSelectedTests(result))

	reason := gateWidenReason(result)
	if reason == "" {
		return narrow, ""
	}
	widened := gateWidenedCommand(result)
	if widened == "" {
		return narrow, ""
	}
	return widened, reason
}

// gateWidenReason reports why the narrow command cannot be trusted, or "" when it can.
//
// Partiality is checked first because it is the stronger statement: the input itself was not whole,
// so nothing computed from it is safe to narrow on. Heuristic-only evidence is the second case —
// the analysis was complete, but what it found is a naming convention rather than a resolved call.
func gateWidenReason(result sem.GateResult) string {
	if result.Partial {
		return "analysis over the changed files was incomplete, so a narrow selection could skip what the parser missed"
	}
	for _, changed := range result.Changed {
		if len(changed.Tests) > 0 && changed.Evidence == sem.GateEvidenceHeuristic {
			return "the only evidence for " + changed.Name +
				" is a naming convention, not a resolved call, so a narrow selection asserts more than the graph found"
		}
	}
	return ""
}

// gateWidenedCommand bounds the fallback to the packages the change set touches.
//
// The package holding a change is the smallest scope that still contains what the resolver may
// have missed. Falling straight to the whole repository would discard the part of the analysis
// that did resolve, which is the opposite of degrading gracefully.
func gateWidenedCommand(result sem.GateResult) string {
	seen := map[string]bool{}
	dirs := make([]string, 0, len(result.Changed))
	for _, changed := range result.Changed {
		dir := path.Dir(changed.FilePath)
		if dir == "." || dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		dirs = append(dirs, "./"+dir+"/")
	}
	if len(dirs) == 0 {
		return ""
	}
	sort.Strings(dirs)
	return "go test " + strings.Join(dirs, " ")
}

// gateAllSelectedTests flattens every per-symbol selection into one set for the command.
func gateAllSelectedTests(result sem.GateResult) []sem.GateSelectedTest {
	var all []sem.GateSelectedTest
	for _, changed := range result.Changed {
		all = append(all, changed.Tests...)
	}
	return all
}

func writeGateText(out io.Writer, result sem.GateResult) {
	out = termsafe.NewWriter(out)

	scope := result.Base + " -> " + result.Head
	if result.Checkpoint != "" {
		scope = "checkpoint " + result.Checkpoint
	}
	fmt.Fprintf(out, "CHANGED  %d symbols   (%s)\n", len(result.Changed), termsafe.Line(scope))

	if result.SkippedTestFileChanges > 0 {
		fmt.Fprintf(out, "  (%d change(s) inside test files excluded: a changed test is not something that needs a test)\n",
			result.SkippedTestFileChanges)
	}

	if !result.TestsRelationAvailable {
		fmt.Fprintf(out, "  note: this snapshot carries no TESTS relation (profile %s); "+
			"attribution used call edges and naming conventions only\n", termsafe.Line(result.Profile))
	}

	if result.Partial {
		fmt.Fprint(out, "\nPARTIAL ANALYSIS — verdicts below are computed from an input the parser could not fully read\n")
		for _, gap := range result.AnalysisGaps {
			fmt.Fprintf(out, "  %s   %s   %s\n",
				termsafe.Line(gap.FilePath), termsafe.Line(gap.Code), termsafe.Line(gap.Reason))
		}
	}

	for _, changed := range result.Changed {
		fmt.Fprintf(out, "\n%-9s %s   %s:%d   [%s]\n",
			changed.Verdict, termsafe.Line(changed.Name), termsafe.Line(changed.FilePath), changed.StartLine,
			termsafe.Line(string(changed.Evidence)))
		fmt.Fprintf(out, "  dependents: %d\n", changed.Dependents)

		for _, test := range changed.Tests {
			fmt.Fprintf(out, "  selected:   %s   %s:%d  (%s -> %s, depth %d)\n",
				termsafe.Line(test.Name), termsafe.Line(test.FilePath), test.StartLine,
				termsafe.Line(test.Route), termsafe.Line(string(test.Evidence)), test.Depth)
			if len(test.Chain) > 1 {
				fmt.Fprintf(out, "  evidence:   %s\n", termsafe.Line(strings.Join(test.Chain, " -> ")))
			}
		}
		if changed.Verdict == sem.GateUncovered {
			// The whole reason the command exists. Said plainly, and scoped honestly: the graph
			// cannot see calls made through interfaces, reflection or generated code, so this is
			// a prompt to check rather than proof that nothing tests the symbol.
			fmt.Fprintf(out, "  ! %d dependents and no test reaches this (no path the graph can see)\n",
				changed.Dependents)
		}
		if changed.Truncated {
			fmt.Fprint(out, "  ! fan-out cap reached; the selection above may be incomplete\n")
		}
	}

	if len(result.Unresolved) > 0 {
		fmt.Fprint(out, "\nUNRESOLVED\n")
		for _, unresolved := range result.Unresolved {
			fmt.Fprintf(out, "  %s   %s   (%s)\n",
				termsafe.Line(unresolved.Name), termsafe.Line(unresolved.Path), termsafe.Line(unresolved.Reason))
		}
	}

	if command, why := gateVerifyCommand(result); command != "" {
		fmt.Fprintf(out, "\nVERIFY: %s\n", termsafe.Line(command))
		if why != "" {
			fmt.Fprintf(out, "  widened: %s\n", termsafe.Line(why))
		}
	}
}
