package cli

import (
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func gateTest(name, path string) sem.GateSelectedTest {
	return sem.GateSelectedTest{Name: name, FilePath: path, Route: "edge", Depth: 1}
}

// The existing emitter names a single test (deriveSearchVerifyGo, search_verify.go:919 builds
// " -run '^Name$'"). A change set selects a set, so the pattern has to be an alternation, and it
// has to be stable: the same selection must produce the same command every run.
func TestGateComposesGoCommandForTheSelectedSet(t *testing.T) {
	t.Parallel()

	got := gateGoTestCommand([]sem.GateSelectedTest{
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

	got := gateGoTestCommand([]sem.GateSelectedTest{
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

	got := gateGoTestCommand([]sem.GateSelectedTest{
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

	if got := gateGoTestCommand(nil); got != "" {
		t.Errorf("command %q, want empty", got)
	}
	if got := gateGoTestCommand([]sem.GateSelectedTest{gateTest("helper", "x_test.go")}); got != "" {
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
