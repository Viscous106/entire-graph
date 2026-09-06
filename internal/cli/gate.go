package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
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
	writeGateText(opts.Stdout, result, gateDetectManifests(repo, result))
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
	commands := gateRunnableCommands(gateDetectManifests(repo, result), result)
	if len(commands) == 0 {
		fmt.Fprint(opts.Stdout, "\nNothing to run: no selected test is executable by a known runner.\n")
		return nil
	}
	for _, command := range commands {
		output, exitCode, err := runVerifyCommands(ctx, repo, verifyFlags{Test: command, MaxBytes: verifyDefaultMaxBytes})
		if err != nil {
			return err
		}
		results, _, parsed := parseVerifyOutput(output)
		if len(commands) > 1 {
			fmt.Fprintf(opts.Stdout, "\n%s\n", termsafe.Line(command))
		}
		fmt.Fprintf(opts.Stdout, "\n%s\n", termsafe.Line(gateRunSummary(results, parsed, exitCode)))
	}
	return nil
}

// gateRunnableCommands is the command list --run executes: exactly what the VERIFY lines printed,
// so what is shown and what is run cannot drift apart.
func gateRunnableCommands(manifests []string, result sem.GateResult) []string {
	plans := gateVerifyPlans(manifests, result)
	commands := make([]string, 0, len(plans))
	for _, plan := range plans {
		commands = append(commands, plan.Command)
	}
	return commands
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

// gateToolchain describes how one language's tests are named, selected and run.
//
// gate's ANALYSIS is language-agnostic — reachability, attribution and grading work on anything the
// provider parses semantically, and running against a pytest repository selected Python tests
// through the same three routes. Only the emitted command is language-specific, so that is the one
// place a toolchain table is needed.
type gateToolchain struct {
	name string
	// manifests are the build files whose presence means the repository is run by this toolchain.
	// Without one there is no evidence, and no command is emitted.
	manifests []string
	// sourceSuffix selects which changed symbols this toolchain is responsible for.
	sourceSuffix string
	// runnable reports whether a selected test's name is one the runner can actually be filtered
	// to. A command naming something the runner cannot match selects nothing, and a green run of
	// nothing reads exactly like a green run of everything.
	runnable func(string) bool
	// narrow builds the filtered command for a set of test names, already sorted and deduplicated.
	narrow func(names []string) string
	// widened builds the fallback over the directories holding the changed code.
	widened func(dirs []string) string
}

var gateToolchains = []gateToolchain{
	{
		name:         "go",
		manifests:    []string{"go.mod"},
		sourceSuffix: ".go",
		runnable:     func(name string) bool { return strings.HasPrefix(name, "Test") },
		narrow: func(names []string) string {
			return "go test -run '^(" + strings.Join(names, "|") + ")$' ./..."
		},
		widened: func(dirs []string) string {
			return "go test " + strings.Join(gatePrefixDirs(dirs, "./", "/"), " ")
		},
	},
	{
		name:         "pytest",
		manifests:    []string{"pyproject.toml", "pytest.ini", "tox.ini", "setup.cfg"},
		sourceSuffix: ".py",
		runnable:     func(name string) bool { return strings.HasPrefix(name, "test") },
		// -k matches on test name rather than a ::node id, so a parametrised case or a method on a
		// test class still matches without gate having to reconstruct its full id.
		narrow: func(names []string) string {
			return "python -m pytest -k '" + strings.Join(names, " or ") + "'"
		},
		widened: func(dirs []string) string {
			return "python -m pytest " + strings.Join(gatePrefixDirs(dirs, "", "/"), " ")
		},
	},
}

// gatePrefixDirs renders directory arguments in the shape one runner expects.
func gatePrefixDirs(dirs []string, prefix, suffix string) []string {
	rendered := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		rendered = append(rendered, prefix+dir+suffix)
	}
	return rendered
}

// gateDetectManifests reports which build manifests actually exist in the repository.
//
// This is a filesystem question, so it belongs here rather than in sem.Gate, which is pure. Two
// earlier attempts were wrong and are worth recording: the snapshot's file list does not contain
// go.mod at all (the provider emits file records only for languages it parses), and it DOES contain
// package.json and pom.xml belonging to this repository's own test fixtures — so a snapshot-derived
// answer both missed the real toolchain and invented two false ones.
//
// Each changed file's ancestors are walked, the way deriveSearchVerifySuiteCommand does, so a
// module or pytest config in a parent directory still identifies the toolchain.
func gateDetectManifests(repo string, result sem.GateResult) []string {
	seen := map[string]bool{}
	dirs := map[string]bool{"": true}
	for _, changed := range result.Changed {
		for dir := path.Dir(changed.FilePath); dir != "." && dir != "/" && dir != ""; dir = path.Dir(dir) {
			dirs[dir] = true
		}
	}
	for _, toolchain := range gateToolchains {
		for _, name := range toolchain.manifests {
			for dir := range dirs {
				if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(dir), name)); err == nil {
					seen[name] = true
					break
				}
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// gateVerifyPlan is one runnable command and, when it was widened, the reason it was.
type gateVerifyPlan struct {
	Toolchain string
	Command   string
	Why       string
}

// gateVerifyPlans derives one command per toolchain the change set touches.
//
// A mixed repository gets one command each rather than a single command that is wrong for one of
// them; a repository with no matching manifest gets none, because the presence of a manifest is the
// only evidence that a given runner is the one this project uses.
func gateVerifyPlans(manifests []string, result sem.GateResult) []gateVerifyPlan {
	plans := make([]gateVerifyPlan, 0, len(gateToolchains))
	for _, toolchain := range gateToolchains {
		if !gateHasManifest(manifests, toolchain.manifests) {
			continue
		}
		changed := gateChangedFor(result, toolchain)
		if len(changed) == 0 {
			continue
		}
		reason := gateWidenReason(result, changed)
		if reason == "" {
			if command := gateNarrowCommand(changed, toolchain); command != "" {
				plans = append(plans, gateVerifyPlan{Toolchain: toolchain.name, Command: command})
			}
			continue
		}
		if command := gateWidenedCommand(changed, toolchain); command != "" {
			plans = append(plans, gateVerifyPlan{Toolchain: toolchain.name, Command: command, Why: reason})
		}
	}
	return plans
}

// gateHasManifest reports whether any of a toolchain's manifests was indexed.
func gateHasManifest(present, wanted []string) bool {
	for _, candidate := range wanted {
		for _, name := range present {
			if name == candidate {
				return true
			}
		}
	}
	return false
}

// gateChangedFor selects the changed symbols a toolchain is responsible for.
func gateChangedFor(result sem.GateResult, toolchain gateToolchain) []sem.GateChangedSymbol {
	changed := make([]sem.GateChangedSymbol, 0, len(result.Changed))
	for _, symbol := range result.Changed {
		if strings.HasSuffix(symbol.FilePath, toolchain.sourceSuffix) {
			changed = append(changed, symbol)
		}
	}
	return changed
}

// gateNarrowCommand composes one command covering the whole selection for a toolchain.
func gateNarrowCommand(changed []sem.GateChangedSymbol, toolchain gateToolchain) string {
	seen := map[string]bool{}
	names := make([]string, 0)
	for _, symbol := range changed {
		for _, test := range symbol.Tests {
			if !toolchain.runnable(test.Name) || seen[test.Name] {
				continue
			}
			seen[test.Name] = true
			names = append(names, test.Name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return toolchain.narrow(names)
}

// gateWidenReason reports why the narrow command cannot be trusted, or "" when it can.
//
// Partiality is checked first because it is the stronger statement: the input itself was not whole,
// so nothing computed from it is safe to narrow on. Heuristic-only evidence is the second case —
// the analysis was complete, but what it found is a naming convention rather than a resolved call.
func gateWidenReason(result sem.GateResult, scope []sem.GateChangedSymbol) string {
	if result.Partial {
		return "analysis over the changed files was incomplete, so a narrow selection could skip what the parser missed"
	}
	for _, changed := range scope {
		if len(changed.Tests) > 0 && changed.Evidence == sem.GateEvidenceHeuristic {
			return "the only evidence for " + changed.Name +
				" is a naming convention, not a resolved call, so a narrow selection asserts more than the graph found"
		}
	}
	return ""
}

// gateWidenedCommand bounds the fallback to the Go packages the change set touches.
//
// The package holding a change is the smallest scope that still contains what the resolver may
// have missed. Falling straight to the whole repository would discard the part of the analysis
// that did resolve, which is the opposite of degrading gracefully.
//
// Only Go files contribute a directory. This is a `go test` command, and gate analyses every
// language the provider parses: run against a Python repository, an unfiltered version emitted
// `go test ./src/... ./scripts/`, a command that cannot run at all. That is a worse failure than
// the narrow emitter's, which already refuses to name tests `go test -run` cannot match. A mixed
// repository still keeps its Go half rather than losing the command entirely.
func gateWidenedCommand(changed []sem.GateChangedSymbol, toolchain gateToolchain) string {
	seen := map[string]bool{}
	dirs := make([]string, 0, len(changed))
	for _, changed := range changed {
		dir := path.Dir(changed.FilePath)
		if dir == "." || dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}
	if len(dirs) == 0 {
		return ""
	}
	sort.Strings(dirs)
	return toolchain.widened(dirs)
}

func writeGateText(out io.Writer, result sem.GateResult, manifests []string) {
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

	if result.SkippedNonTestSelections > 0 {
		fmt.Fprintf(out, "  (%d symbol(s) in test files not reportable as tests: helpers matched by convention, not by a resolved call)\n",
			result.SkippedNonTestSelections)
	}

	if result.SkippedNonCallableChanges > 0 {
		fmt.Fprintf(out, "  (%d change(s) to entities nothing can call excluded: headings, fenced blocks, config keys)\n",
			result.SkippedNonCallableChanges)
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

	for _, plan := range gateVerifyPlans(manifests, result) {
		fmt.Fprintf(out, "\nVERIFY: %s\n", termsafe.Line(plan.Command))
		if plan.Why != "" {
			fmt.Fprintf(out, "  widened: %s\n", termsafe.Line(plan.Why))
		}
	}
}
