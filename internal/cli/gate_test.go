package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// gateGoNarrow runs the narrow emitter through the Go toolchain, which is what the four command
// composition tests below are about: alternation, de-duplication, and refusing names the runner
// cannot match.
func gateGoNarrow(t *testing.T, tests []sem.GateSelectedTest) string {
	t.Helper()
	for _, toolchain := range gateToolchains {
		if toolchain.name == "go" {
			return gateNarrowCommand([]sem.GateChangedSymbol{{FilePath: "mux.go", Tests: tests}}, toolchain)
		}
	}
	t.Fatal("no go toolchain registered")
	return ""
}

func gateTest(name, path string) sem.GateSelectedTest {
	return sem.GateSelectedTest{Name: name, FilePath: path, Route: "edge", Depth: 1}
}

// The existing emitter names a single test (deriveSearchVerifyGo, search_verify.go:919 builds
// " -run '^Name$'"). A change set selects a set, so the pattern has to be an alternation, and it
// has to be stable: the same selection must produce the same command every run.
func TestGateComposesGoCommandForTheSelectedSet(t *testing.T) {
	t.Parallel()

	got := gateGoNarrow(t, []sem.GateSelectedTest{
		gateTest("TestServeHTTP", "mux_test.go"),
		gateTest("TestRouteMatch", "mux_test.go"),
	})

	want := `go test -run '^(TestRouteMatch|TestServeHTTP)$' ./...`
	if got != want {
		t.Errorf("command\n got %s\nwant %s", got, want)
	}
}

// One test can be selected by several changed symbols. Running it twice is wasteful and makes the
// command read as though more was verified than actually was.
func TestGateGoCommandDeduplicatesTestNames(t *testing.T) {
	t.Parallel()

	got := gateGoNarrow(t, []sem.GateSelectedTest{
		gateTest("TestRouteMatch", "mux_test.go"),
		gateTest("TestRouteMatch", "mux_test.go"),
	})

	want := `go test -run '^(TestRouteMatch)$' ./...`
	if got != want {
		t.Errorf("command\n got %s\nwant %s", got, want)
	}
}

// `go test -run` only ever matches functions named Test*. Emitting anything else would produce a
// command that silently selects nothing, which reads as "everything passed".
func TestGateGoCommandSkipsNamesGoTestCannotRun(t *testing.T) {
	t.Parallel()

	got := gateGoNarrow(t, []sem.GateSelectedTest{
		gateTest("TestRouteMatch", "mux_test.go"),
		gateTest("helperNotATest", "mux_test.go"),
		gateTest("BenchmarkMatch", "mux_test.go"),
	})

	want := `go test -run '^(TestRouteMatch)$' ./...`
	if got != want {
		t.Errorf("command\n got %s\nwant %s", got, want)
	}
}

// No runnable selection must yield no command at all. Falling back to the whole suite here would
// defeat the point: the caller asked which tests this change needs, and "all of them" is the
// answer they already had.
func TestGateGoCommandIsEmptyWhenNothingIsRunnable(t *testing.T) {
	t.Parallel()

	if got := gateGoNarrow(t, nil); got != "" {
		t.Errorf("command %q, want empty", got)
	}
	if got := gateGoNarrow(t, []sem.GateSelectedTest{gateTest("helper", "x_test.go")}); got != "" {
		t.Errorf("command %q, want empty", got)
	}
}

// --- L8: adjudication -------------------------------------------------------------------------

func TestGateRunSummaryReportsPassAndFailCounts(t *testing.T) {
	t.Parallel()

	got := gateRunSummary(verifyResults{
		"TestRouteMatch": verifyStatusPass,
		"TestServeHTTP":  verifyStatusPass,
	}, true, 0)

	want := "VERDICT: 2 selected tests ran, 2 passed, 0 failed"
	if got != want {
		t.Errorf("summary\n got %s\nwant %s", got, want)
	}
}

// A failing selected test is the whole point of running them, so its name has to appear. A count
// alone would send the reader back to raw output, which is what verify exists to avoid.
func TestGateRunSummaryNamesFailingTests(t *testing.T) {
	t.Parallel()

	got := gateRunSummary(verifyResults{
		"TestServeHTTP":  verifyStatusPass,
		"TestRouteMatch": verifyStatusFail,
		"TestStorePut":   verifyStatusError,
	}, true, 1)

	want := "VERDICT: 3 selected tests ran, 1 passed, 2 failed — TestRouteMatch, TestStorePut"
	if got != want {
		t.Errorf("summary\n got %s\nwant %s", got, want)
	}
}

// When no parser recognised the output there are no per-test ids to report. Claiming a count we
// did not measure would be worse than saying so and falling back to the exit code.
func TestGateRunSummaryFallsBackToExitCodeWhenUnparsed(t *testing.T) {
	t.Parallel()

	if got, want := gateRunSummary(nil, false, 0), "VERDICT: selected tests passed (exit 0; no per-test output recognised)"; got != want {
		t.Errorf("summary\n got %s\nwant %s", got, want)
	}
	if got, want := gateRunSummary(nil, false, 2), "VERDICT: selected tests failed (exit 2; no per-test output recognised)"; got != want {
		t.Errorf("summary\n got %s\nwant %s", got, want)
	}
}

// --- Curveball: the fallback command ----------------------------------------------------------
//
// A narrow command derived from incomplete evidence is the dangerous output. It looks
// authoritative, and it silently skips whatever the resolver missed. When the evidence behind a
// selection is not structural, or the analysis behind it was not whole, the command has to widen
// and say why — degrading toward running more tests rather than fewer.

// gateResultWith builds a result carrying one changed symbol with the given evidence grade.
func gateResultWith(evidence sem.GateEvidence, partial bool, tests ...sem.GateSelectedTest) sem.GateResult {
	return sem.GateResult{
		Partial: partial,
		Changed: []sem.GateChangedSymbol{{
			Name: "Store.Put", FilePath: "internal/sem/store.go", StartLine: 88,
			Verdict: sem.GateCovered, Evidence: evidence, Tests: tests,
		}},
	}
}

// gateFirstPlan is the single command a one-toolchain result produces, or ("", "") when it produces
// none.
func gateFirstPlan(t *testing.T, result sem.GateResult) (command, why string) {
	t.Helper()
	plans := gateVerifyPlans([]string{"go.mod", "pyproject.toml"}, result)
	if len(plans) == 0 {
		return "", ""
	}
	if len(plans) > 1 {
		t.Fatalf("expected at most one command, got %+v", plans)
	}
	return plans[0].Command, plans[0].Why
}

// POINT 4, the regression that guards everything else. With complete analysis and a resolved edge,
// the command is byte-identical to what the pre-curveball emitter produced. If this test ever
// changes, the revision stopped being additive.
func TestGateVerifyCommandIsUnchangedForFullyResolvedCode(t *testing.T) {
	t.Parallel()

	result := gateResultWith(sem.GateEvidenceConfirmed, false,
		gateTest("TestServeHTTP", "mux_test.go"),
		gateTest("TestRouteMatch", "mux_test.go"),
	)

	command, why := gateFirstPlan(t, result)

	if want := `go test -run '^(TestRouteMatch|TestServeHTTP)$' ./...`; command != want {
		t.Errorf("command\n got %s\nwant %s", command, want)
	}
	if why != "" {
		t.Errorf("a fully resolved selection must widen for no reason, got %q", why)
	}
}

func TestGateVerifyCommandWidensWhenAnalysisIsPartial(t *testing.T) {
	t.Parallel()

	result := gateResultWith(sem.GateEvidenceConfirmed, true, gateTest("TestServeHTTP", "mux_test.go"))

	command, why := gateFirstPlan(t, result)

	if command == `go test -run '^(TestServeHTTP)$' ./...` {
		t.Fatal("a narrow command was emitted from a partial analysis")
	}
	if want := "go test ./internal/sem/"; command != want {
		t.Errorf("command\n got %s\nwant %s", command, want)
	}
	if why == "" {
		t.Error("a widened command must say why it widened")
	}
}

// A selection resting only on a mirror filename or a name match is a convention, not a call. It
// may well be right — but narrowing the suite to it asserts a structural claim the graph never
// made.
func TestGateVerifyCommandWidensWhenEvidenceIsOnlyHeuristic(t *testing.T) {
	t.Parallel()

	result := gateResultWith(sem.GateEvidenceHeuristic, false, gateTest("TestServeHTTP", "mux_test.go"))

	command, why := gateFirstPlan(t, result)

	if want := "go test ./internal/sem/"; command != want {
		t.Errorf("command\n got %s\nwant %s", command, want)
	}
	if why == "" {
		t.Error("a widened command must say why it widened")
	}
}

// Widening has to stay bounded. The package holding the change is the smallest scope that still
// contains what the resolver may have missed; falling straight to the whole repository would throw
// away the part of the analysis that did resolve.
func TestGateVerifyCommandWidensToThePackagesThatChanged(t *testing.T) {
	t.Parallel()

	result := gateResultWith(sem.GateEvidenceHeuristic, false, gateTest("TestServeHTTP", "mux_test.go"))
	result.Changed = append(result.Changed, sem.GateChangedSymbol{
		Name: "runGate", FilePath: "internal/cli/gate.go", StartLine: 120,
		Verdict: sem.GateCovered, Evidence: sem.GateEvidenceHeuristic,
	})

	command, _ := gateFirstPlan(t, result)

	if want := "go test ./internal/cli/ ./internal/sem/"; command != want {
		t.Errorf("command\n got %s\nwant %s", command, want)
	}
}

// --- Curveball: rendering the grade -----------------------------------------------------------

func gateRenderText(t *testing.T, result sem.GateResult) string {
	t.Helper()
	var buffer bytes.Buffer
	writeGateText(&buffer, result, []string{"go.mod"})
	return buffer.String()
}

// The grade has to be machine-readable in JSON and legible in text. Before the revision the text
// output hedged in prose ("no path the graph can see") while nothing carried the caveat in a form
// a reader could act on, and the JSON carried none at all.
func TestGateTextShowsTheEvidenceGrade(t *testing.T) {
	t.Parallel()

	got := gateRenderText(t, gateResultWith(sem.GateEvidenceConfirmed, false,
		gateTest("TestServeHTTP", "mux_test.go")))

	if !strings.Contains(got, "confirmed") {
		t.Errorf("text output does not carry the evidence grade:\n%s", got)
	}
}

// An UNCOVERED verdict rests on the absence of an edge. The text must say the claim is unverified,
// not merely hedge about it in prose.
func TestGateTextGradesAnUncoveredVerdictAsUnverified(t *testing.T) {
	t.Parallel()

	result := sem.GateResult{Changed: []sem.GateChangedSymbol{{
		Name: "Store.Put", FilePath: "internal/sem/store.go", StartLine: 88, Dependents: 12,
		Verdict: sem.GateUncovered, Evidence: sem.GateEvidenceUnverified,
	}}}

	got := gateRenderText(t, result)

	if !strings.Contains(got, "unverified") {
		t.Errorf("an UNCOVERED verdict must be graded unverified in text:\n%s", got)
	}
}

// A widened command that does not say why it widened is just a slower command. The reason is what
// lets the reader decide whether to trust it or go and look at the source.
func TestGateTextExplainsAWidenedVerifyCommand(t *testing.T) {
	t.Parallel()

	result := gateResultWith(sem.GateEvidenceConfirmed, true, gateTest("TestServeHTTP", "mux_test.go"))
	result.AnalysisGaps = []sem.GateAnalysisGap{{
		FilePath: "internal/sem/store.go", Code: "E_PARSE_FAILED", Reason: "relations from this file are missing",
	}}

	got := gateRenderText(t, result)

	if !strings.Contains(got, "VERIFY: go test ./internal/sem/") {
		t.Errorf("expected the widened command in the VERIFY line:\n%s", got)
	}
	if !strings.Contains(got, "incomplete") {
		t.Errorf("expected the widening to be explained:\n%s", got)
	}
	if !strings.Contains(got, "E_PARSE_FAILED") {
		t.Errorf("expected the named analysis gap to be reported:\n%s", got)
	}
}

// The widened command is a Go command, so it may only be emitted for Go. Running gate on a Python
// repository produced `go test ./src/scaler_listen/ ./scripts/ ...` — a command that cannot run at
// all. That is worse than the narrow path's failure mode, which the emitter already guards against
// by emitting nothing rather than a command that selects nothing.
func TestGateVerifyCommandEmitsNothingWhenNoGoPackagesChanged(t *testing.T) {
	t.Parallel()

	result := sem.GateResult{
		Changed: []sem.GateChangedSymbol{{
			Name: "warn", FilePath: "scripts/setup-actions.sh", StartLine: 35,
			Verdict: sem.GateCovered, Evidence: sem.GateEvidenceHeuristic,
			Tests: []sem.GateSelectedTest{gateTest("test_a_failed_last_run_warns", "tests/test_health.py")},
		}, {
			Name: "run", FilePath: "src/scaler_listen/export.py", StartLine: 75,
			Verdict: sem.GateCovered, Evidence: sem.GateEvidenceHeuristic,
		}},
	}

	command, _ := gateFirstPlan(t, result)

	if command != "" {
		t.Errorf("emitted %q for a repository with no changed Go packages, want no command", command)
	}
}

// Mixed repositories keep the Go half. Dropping the whole command because one changed file was
// Python would throw away a selection that is still runnable.
func TestGateVerifyCommandWidensOnlyOverGoPackages(t *testing.T) {
	t.Parallel()

	result := sem.GateResult{
		Changed: []sem.GateChangedSymbol{{
			Name: "handler", FilePath: "internal/api/handler.go", StartLine: 10,
			Verdict: sem.GateCovered, Evidence: sem.GateEvidenceHeuristic,
			Tests: []sem.GateSelectedTest{gateTest("TestHandler", "internal/api/handler_test.go")},
		}, {
			Name: "build_report", FilePath: "tools/report.py", StartLine: 4,
			Verdict: sem.GateCovered, Evidence: sem.GateEvidenceHeuristic,
		}},
	}

	command, _ := gateFirstPlan(t, result)

	if want := "go test ./internal/api/"; command != want {
		t.Errorf("command\n got %s\nwant %s", command, want)
	}
}

// --- Per-toolchain command emission -----------------------------------------------------------
//
// gate's analysis is language-agnostic: it selected Python tests on a pytest repository through
// the same routes it uses for Go. Only the emitted command is language-specific, so it is derived
// per toolchain from the changed files and the manifests the snapshot indexed.

func gatePyResult(evidence sem.GateEvidence, tests ...sem.GateSelectedTest) sem.GateResult {
	return sem.GateResult{
		Changed: []sem.GateChangedSymbol{{
			Name: "warn", FilePath: "src/scaler_listen/health.py", StartLine: 35,
			Verdict: sem.GateCovered, Evidence: evidence, Tests: tests,
		}},
	}
}

func TestGateEmitsAPytestCommandForAPythonSelection(t *testing.T) {
	t.Parallel()

	plans := gateVerifyPlans([]string{"pyproject.toml"}, gatePyResult(sem.GateEvidenceConfirmed,
		gateTest("test_a_failed_last_run_warns", "tests/test_health.py"),
		gateTest("test_readme_warns", "tests/test_export.py"),
	))

	if len(plans) != 1 {
		t.Fatalf("expected 1 command, got %d: %+v", len(plans), plans)
	}
	if want := `python -m pytest -k 'test_a_failed_last_run_warns or test_readme_warns'`; plans[0].Command != want {
		t.Errorf("command\n got %s\nwant %s", plans[0].Command, want)
	}
}

func TestGatePytestCommandWidensToTheChangedDirectories(t *testing.T) {
	t.Parallel()

	plans := gateVerifyPlans([]string{"pyproject.toml"}, gatePyResult(sem.GateEvidenceHeuristic,
		gateTest("test_a_failed_last_run_warns", "tests/test_health.py")))

	if len(plans) != 1 {
		t.Fatalf("expected 1 command, got %d", len(plans))
	}
	if want := "python -m pytest src/scaler_listen/"; plans[0].Command != want {
		t.Errorf("command\n got %s\nwant %s", plans[0].Command, want)
	}
	if plans[0].Why == "" {
		t.Error("a widened command must say why")
	}
}

// A repository holding both languages gets one command per toolchain, not a single command that is
// wrong for one of them.
func TestGateEmitsOneCommandPerToolchainInAMixedRepository(t *testing.T) {
	t.Parallel()

	result := sem.GateResult{
		Changed: []sem.GateChangedSymbol{
			{
				Name: "handler", FilePath: "internal/api/handler.go", StartLine: 10,
				Verdict: sem.GateCovered, Evidence: sem.GateEvidenceConfirmed,
				Tests: []sem.GateSelectedTest{gateTest("TestHandler", "internal/api/handler_test.go")},
			},
			{
				Name: "warn", FilePath: "tools/health.py", StartLine: 4,
				Verdict: sem.GateCovered, Evidence: sem.GateEvidenceConfirmed,
				Tests: []sem.GateSelectedTest{gateTest("test_warn", "tools/test_health.py")},
			},
		},
	}

	plans := gateVerifyPlans([]string{"go.mod", "pyproject.toml"}, result)

	if len(plans) != 2 {
		t.Fatalf("expected one command per toolchain, got %d: %+v", len(plans), plans)
	}
	if want := `go test -run '^(TestHandler)$' ./...`; plans[0].Command != want {
		t.Errorf("go command\n got %s\nwant %s", plans[0].Command, want)
	}
	if want := `python -m pytest -k 'test_warn'`; plans[1].Command != want {
		t.Errorf("pytest command\n got %s\nwant %s", plans[1].Command, want)
	}
}

// Without a manifest there is no evidence the repository is run by that toolchain, so no command is
// emitted — the same rule the narrow Go emitter already follows.
func TestGateEmitsNoCommandWithoutAManifest(t *testing.T) {
	t.Parallel()

	result := gatePyResult(sem.GateEvidenceConfirmed, gateTest("test_warn", "tests/test_health.py"))

	if plans := gateVerifyPlans(nil, result); len(plans) != 0 {
		t.Errorf("expected no command without a manifest, got %+v", plans)
	}
}

// --run has to execute the same commands the VERIFY lines printed. It was wired to the Go-only
// emitter, so on a pytest repository it would have printed a pytest command and then run a Go one.
func TestGateRunExecutesEveryPlannedCommand(t *testing.T) {
	t.Parallel()

	result := sem.GateResult{
		Changed: []sem.GateChangedSymbol{
			{
				Name: "handler", FilePath: "internal/api/handler.go", StartLine: 10,
				Verdict: sem.GateCovered, Evidence: sem.GateEvidenceConfirmed,
				Tests: []sem.GateSelectedTest{gateTest("TestHandler", "internal/api/handler_test.go")},
			},
			{
				Name: "warn", FilePath: "tools/health.py", StartLine: 4,
				Verdict: sem.GateCovered, Evidence: sem.GateEvidenceConfirmed,
				Tests: []sem.GateSelectedTest{gateTest("test_warn", "tools/test_health.py")},
			},
		},
	}

	got := gateRunnableCommands([]string{"go.mod", "pyproject.toml"}, result)

	want := []string{`go test -run '^(TestHandler)$' ./...`, `python -m pytest -k 'test_warn'`}
	if len(got) != len(want) {
		t.Fatalf("commands %v, want %v", got, want)
	}
	for index, command := range want {
		if got[index] != command {
			t.Errorf("command %d\n got %s\nwant %s", index, got[index], command)
		}
	}
}

// Manifest detection is a filesystem question, so it lives here rather than in the pure engine.
// It walks the changed file's ancestors the way the existing suite derivations do, so a Go module
// or a pytest config in a parent directory still identifies the toolchain.
func TestGateDetectsManifestsOnDisk(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	for _, name := range []string{"go.mod", "tools/py/pyproject.toml"} {
		full := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	result := sem.GateResult{Changed: []sem.GateChangedSymbol{
		{Name: "handler", FilePath: "internal/api/handler.go"},
		{Name: "warn", FilePath: "tools/py/health.py"},
	}}

	got := gateDetectManifests(repo, result)

	want := []string{"go.mod", "pyproject.toml"}
	if len(got) != len(want) {
		t.Fatalf("manifests %v, want %v", got, want)
	}
	for index, name := range want {
		if got[index] != name {
			t.Errorf("manifest %d is %q, want %q", index, got[index], name)
		}
	}
}

// A repository with no recognised manifest yields none, so no command is emitted for it.
func TestGateDetectsNoManifestsInABareTree(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	result := sem.GateResult{Changed: []sem.GateChangedSymbol{{Name: "x", FilePath: "src/x.py"}}}

	if got := gateDetectManifests(repo, result); len(got) != 0 {
		t.Errorf("manifests %v, want none", got)
	}
}
