package sem

import (
	"reflect"
	"testing"
)

// The anchor is deliberately named `Put` — three characters. searchNamedTestFiles
// requires four before the name route can fire, so every fixture below admits tests
// through resolved edges only, and a route assertion is testing the walk rather than
// the name heuristic. For the same reason no `store/store_test.go` exists: without a
// mirror file the mirror route stays silent too. The routes get their own test.
func reachAnchor() SymbolRecord {
	return coverSymbol("src:Put", "Put", "Store.Put", "method", "store/store.go", 40, 52)
}

func reachIndex(symbols []SymbolRecord) (map[string]SymbolRecord, map[string][]SymbolRecord) {
	byID := make(map[string]SymbolRecord, len(symbols))
	byFile := make(map[string][]SymbolRecord)
	for _, symbol := range symbols {
		byID[symbol.ID] = symbol
		byFile[symbol.FilePath] = append(byFile[symbol.FilePath], symbol)
	}
	return byID, byFile
}

func reachRun(symbols []SymbolRecord, relations []RelationRecord, options gateReachOptions) ([]gateReachedTest, bool, bool) {
	byID, byFile := reachIndex(symbols)
	return gateReach(reachAnchor(), relations, byID, byFile, options)
}

func reachDefaults() gateReachOptions {
	return gateReachOptions{Depth: 2, MaxFanOut: 32}
}

func reachNames(tests []gateReachedTest) []string {
	names := make([]string, 0, len(tests))
	for _, test := range tests {
		names = append(names, test.Symbol.Name)
	}
	return names
}

func reachFind(t *testing.T, tests []gateReachedTest, name string) gateReachedTest {
	t.Helper()
	for _, test := range tests {
		if test.Symbol.Name == name {
			return test
		}
	}
	t.Fatalf("expected %q among reached tests, got %v", name, reachNames(tests))
	return gateReachedTest{}
}

// reachDirectFixture: one test calls the anchor, one unrelated function calls it too.
//
//	flow/flow_test.go  TestPutDirect --> Store.Put
//	api/handler.go     ServeHTTP -----> Store.Put
func reachDirectFixture() ([]SymbolRecord, []RelationRecord) {
	symbols := []SymbolRecord{
		reachAnchor(),
		coverSymbol("test:direct", "TestPutDirect", "TestPutDirect", "function", "flow/flow_test.go", 10, 18),
		coverSymbol("src:serve", "ServeHTTP", "ServeHTTP", "function", "api/handler.go", 30, 44),
	}
	relations := []RelationRecord{
		coverCall("test:direct", "src:Put", 0.9),
		coverCall("src:serve", "src:Put", 0.9),
	}
	return symbols, relations
}

func TestReachFindsTestThatCallsTheAnchorDirectly(t *testing.T) {
	symbols, relations := reachDirectFixture()

	tests, hasCallers, truncated := reachRun(symbols, relations, reachDefaults())

	if !hasCallers {
		t.Error("hasCallers = false, want true — two symbols call the anchor")
	}
	if truncated {
		t.Error("truncated = true, want false — fan-out is far below the cap")
	}
	got := reachFind(t, tests, "TestPutDirect")
	if got.Depth != 1 {
		t.Errorf("depth = %d, want 1", got.Depth)
	}
	if got.Route != "edge" {
		t.Errorf("route = %q, want %q", got.Route, "edge")
	}
	// Nothing sits between a direct caller and the anchor, and the anchor is not
	// carried in the chain, so a depth-1 chain is the test on its own.
	if want := []string{"TestPutDirect"}; !reflect.DeepEqual(got.Chain, want) {
		t.Errorf("chain = %v, want %v", got.Chain, want)
	}
}

// A caller that is not a test must never be selected. Reporting ServeHTTP as
// covering the change would be the exact false assurance this command exists to
// remove.
func TestReachDoesNotSelectANonTestCaller(t *testing.T) {
	symbols, relations := reachDirectFixture()

	tests, _, _ := reachRun(symbols, relations, reachDefaults())

	for _, test := range tests {
		if test.Symbol.Name == "ServeHTTP" {
			t.Fatalf("ServeHTTP is production code and must not be selected; got %+v", tests)
		}
	}
}

// The one this whole file exists for. The existing covering-test finder stops at
// one hop (search_covertest.go:456), so TestPutViaHelper — which reaches the anchor
// through writeAll — is invisible to it today.
func TestReachFindsTestThroughAnIntermediateCall(t *testing.T) {
	symbols := []SymbolRecord{
		reachAnchor(),
		coverSymbol("src:writeAll", "writeAll", "writeAll", "function", "store/batch.go", 12, 30),
		coverSymbol("test:via", "TestPutViaHelper", "TestPutViaHelper", "function", "flow/flow_test.go", 20, 34),
	}
	relations := []RelationRecord{
		coverCall("src:writeAll", "src:Put", 0.9),
		coverCall("test:via", "src:writeAll", 0.9),
	}

	tests, hasCallers, _ := reachRun(symbols, relations, reachDefaults())

	if !hasCallers {
		t.Error("hasCallers = false, want true")
	}
	got := reachFind(t, tests, "TestPutViaHelper")
	if got.Depth != 2 {
		t.Errorf("depth = %d, want 2", got.Depth)
	}
	// The anchor is excluded: the caller already knows which symbol it asked about.
	want := []string{"TestPutViaHelper", "writeAll"}
	if !reflect.DeepEqual(got.Chain, want) {
		t.Errorf("chain = %v, want %v (test first, intermediates in call order, anchor excluded)", got.Chain, want)
	}
}

// Depth is a bound, not a suggestion. At depth 1 the transitive test must not appear.
func TestReachRespectsDepthOne(t *testing.T) {
	symbols := []SymbolRecord{
		reachAnchor(),
		coverSymbol("src:writeAll", "writeAll", "writeAll", "function", "store/batch.go", 12, 30),
		coverSymbol("test:via", "TestPutViaHelper", "TestPutViaHelper", "function", "flow/flow_test.go", 20, 34),
	}
	relations := []RelationRecord{
		coverCall("src:writeAll", "src:Put", 0.9),
		coverCall("test:via", "src:writeAll", 0.9),
	}

	tests, _, _ := reachRun(symbols, relations, gateReachOptions{Depth: 1, MaxFanOut: 32})

	for _, test := range tests {
		if test.Symbol.Name == "TestPutViaHelper" {
			t.Fatalf("depth 1 must not reach a test two hops away; got %v", reachNames(tests))
		}
	}
}

// A -> B -> A must terminate rather than expand forever, and the anchor must never
// be re-expanded through an edge that points back at it.
func TestReachTerminatesOnACallCycle(t *testing.T) {
	symbols := []SymbolRecord{
		reachAnchor(),
		coverSymbol("src:alpha", "alpha", "alpha", "function", "store/alpha.go", 10, 20),
		coverSymbol("src:beta", "beta", "beta", "function", "store/beta.go", 10, 20),
		coverSymbol("test:cycle", "TestCycleReaches", "TestCycleReaches", "function", "flow/flow_test.go", 40, 52),
	}
	relations := []RelationRecord{
		coverCall("src:alpha", "src:Put", 0.9),
		coverCall("src:beta", "src:alpha", 0.9),
		coverCall("src:alpha", "src:beta", 0.9),
		coverCall("src:Put", "src:alpha", 0.9),
		coverCall("test:cycle", "src:alpha", 0.9),
	}

	done := make(chan struct{})
	var tests []gateReachedTest
	go func() {
		tests, _, _ = reachRun(symbols, relations, reachDefaults())
		close(done)
	}()
	<-done

	reachFind(t, tests, "TestCycleReaches")
}

// A self-edge is not a dependent. A symbol that only calls itself is ISOLATED, and
// reporting it as having callers would turn the headline finding into noise.
func TestReachIgnoresASelfEdgeWhenReportingCallers(t *testing.T) {
	symbols := []SymbolRecord{reachAnchor()}
	relations := []RelationRecord{coverCall("src:Put", "src:Put", 0.9)}

	tests, hasCallers, _ := reachRun(symbols, relations, reachDefaults())

	if hasCallers {
		t.Error("hasCallers = true, want false — a self-edge is not a dependent")
	}
	if len(tests) != 0 {
		t.Errorf("expected no tests, got %v", reachNames(tests))
	}
}

// hasCallers is what separates UNCOVERED from ISOLATED, so it has to be right when
// dependents exist and none of them is a test.
func TestReachReportsCallersWhenDependentsExistButNoTestReaches(t *testing.T) {
	symbols := []SymbolRecord{
		reachAnchor(),
		coverSymbol("src:serve", "ServeHTTP", "ServeHTTP", "function", "api/handler.go", 30, 44),
	}
	relations := []RelationRecord{coverCall("src:serve", "src:Put", 0.9)}

	tests, hasCallers, _ := reachRun(symbols, relations, reachDefaults())

	if !hasCallers {
		t.Error("hasCallers = false, want true — ServeHTTP depends on the anchor")
	}
	if len(tests) != 0 {
		t.Errorf("expected no tests reached, got %v", reachNames(tests))
	}
}

func TestReachReportsNoCallersWhenNothingCallsTheAnchor(t *testing.T) {
	symbols := []SymbolRecord{
		reachAnchor(),
		coverSymbol("src:other", "unrelated", "unrelated", "function", "api/other.go", 10, 20),
	}
	relations := []RelationRecord{coverCall("src:other", "src:elsewhere", 0.9)}

	tests, hasCallers, _ := reachRun(symbols, relations, reachDefaults())

	if hasCallers {
		t.Error("hasCallers = true, want false — nothing calls the anchor")
	}
	if len(tests) != 0 {
		t.Errorf("expected no tests reached, got %v", reachNames(tests))
	}
}

// An edge from a symbol the snapshot does not carry is still a dependent: it is
// evidence something calls the anchor. It just cannot be walked or named a test.
func TestReachCountsAnEdgeFromAnUnknownSymbolAsACaller(t *testing.T) {
	symbols := []SymbolRecord{reachAnchor()}
	relations := []RelationRecord{coverCall("src:notInSnapshot", "src:Put", 0.9)}

	tests, hasCallers, _ := reachRun(symbols, relations, reachDefaults())

	if !hasCallers {
		t.Error("hasCallers = false, want true — an unresolved caller still depends on the anchor")
	}
	if len(tests) != 0 {
		t.Errorf("an unknown symbol must never be reported as a test, got %v", reachNames(tests))
	}
}

// A shortened list that does not say it was shortened reads as "no more tests
// exist", which is the silence this command exists to remove.
func TestReachReportsTruncationWhenFanOutCapBites(t *testing.T) {
	symbols := []SymbolRecord{
		reachAnchor(),
		coverSymbol("src:hub", "hub", "hub", "function", "store/hub.go", 10, 20),
	}
	relations := []RelationRecord{coverCall("src:hub", "src:Put", 0.9)}
	// Many tests all call the one intermediate, so the cap bites at the second hop.
	for index := 0; index < 8; index++ {
		id := "test:fan" + string(rune('a'+index))
		symbols = append(symbols, coverSymbol(
			id, "TestFan"+string(rune('A'+index)), "TestFan"+string(rune('A'+index)),
			"function", "flow/fan_test.go", 10+index*10, 18+index*10,
		))
		relations = append(relations, coverCall(id, "src:hub", 0.9))
	}

	tests, _, truncated := reachRun(symbols, relations, gateReachOptions{Depth: 2, MaxFanOut: 3})

	if !truncated {
		t.Fatal("truncated = false, want true — 8 callers examined under a cap of 3")
	}
	if len(tests) > 3 {
		t.Errorf("returned %d tests, want at most the cap of 3", len(tests))
	}
}

// Relation order in a real snapshot follows filesystem walk order. Two identical
// calls must return byte-identical results, including which entries a truncated
// run dropped — otherwise a truncated result is arbitrary rather than reproducible.
func TestReachIsDeterministicAcrossRelationOrder(t *testing.T) {
	symbols := []SymbolRecord{
		reachAnchor(),
		coverSymbol("src:hub", "hub", "hub", "function", "store/hub.go", 10, 20),
		coverSymbol("test:one", "TestOne", "TestOne", "function", "flow/flow_test.go", 10, 18),
		coverSymbol("test:two", "TestTwo", "TestTwo", "function", "flow/flow_test.go", 20, 28),
		coverSymbol("test:three", "TestThree", "TestThree", "function", "flow/other_test.go", 10, 18),
	}
	forward := []RelationRecord{
		coverCall("src:hub", "src:Put", 0.9),
		coverCall("test:one", "src:Put", 0.9),
		coverCall("test:two", "src:hub", 0.9),
		coverCall("test:three", "src:hub", 0.9),
	}
	reversed := make([]RelationRecord, len(forward))
	for index, relation := range forward {
		reversed[len(forward)-1-index] = relation
	}

	first, firstCallers, firstTruncated := reachRun(symbols, forward, gateReachOptions{Depth: 2, MaxFanOut: 1})
	second, secondCallers, secondTruncated := reachRun(symbols, reversed, gateReachOptions{Depth: 2, MaxFanOut: 1})

	if !reflect.DeepEqual(first, second) {
		t.Errorf("reached tests differ with relation order:\n first  = %+v\n second = %+v", first, second)
	}
	if firstCallers != secondCallers || firstTruncated != secondTruncated {
		t.Errorf("flags differ with relation order: (%v,%v) vs (%v,%v)",
			firstCallers, firstTruncated, secondCallers, secondTruncated)
	}
}

// Results are sorted by (FilePath, StartLine, Symbol.ID) so a reader can diff two
// gate runs and trust the difference.
func TestReachSortsResultsByFileThenLine(t *testing.T) {
	symbols := []SymbolRecord{
		reachAnchor(),
		coverSymbol("test:z", "TestZulu", "TestZulu", "function", "zeta/zeta_test.go", 10, 18),
		coverSymbol("test:a2", "TestAlphaTwo", "TestAlphaTwo", "function", "alpha/alpha_test.go", 90, 98),
		coverSymbol("test:a1", "TestAlphaOne", "TestAlphaOne", "function", "alpha/alpha_test.go", 10, 18),
	}
	relations := []RelationRecord{
		coverCall("test:z", "src:Put", 0.9),
		coverCall("test:a2", "src:Put", 0.9),
		coverCall("test:a1", "src:Put", 0.9),
	}

	tests, _, _ := reachRun(symbols, relations, reachDefaults())

	want := []string{"TestAlphaOne", "TestAlphaTwo", "TestZulu"}
	if got := reachNames(tests); !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// A test admitted because it sits in the anchor's mirror file is reported as
// "mirror", not "edge": there is no resolved call behind it, and labelling it as
// one would overstate the evidence.
func TestReachLabelsTheMirrorRouteDistinctly(t *testing.T) {
	anchor := reachAnchor()
	symbols := []SymbolRecord{
		anchor,
		coverSymbol("test:mirror", "TestMirrorNeighbour", "TestMirrorNeighbour", "function", "store/store_test.go", 10, 18),
	}
	byID, byFile := reachIndex(symbols)

	tests, _, _ := gateReach(anchor, nil, byID, byFile, reachDefaults())

	got := reachFind(t, tests, "TestMirrorNeighbour")
	if got.Route != "mirror" {
		t.Errorf("route = %q, want %q — no call edge exists, only the mirror file", got.Route, "mirror")
	}
	if got.Depth != 1 {
		t.Errorf("depth = %d, want 1", got.Depth)
	}
}

// A resolved TESTS edge counts alongside CALLS. searchRelatedCallRelation excludes
// it deliberately, so it has to be added back here — dropping it would report a
// symbol the graph knows is tested as uncovered.
func TestReachAcceptsAResolvedTestsEdge(t *testing.T) {
	symbols := []SymbolRecord{
		reachAnchor(),
		coverSymbol("test:tests", "TestViaTestsEdge", "TestViaTestsEdge", "function", "flow/flow_test.go", 10, 18),
	}
	relations := []RelationRecord{
		{FromID: "test:tests", ToID: "src:Put", Type: "TESTS", Confidence: 0.95},
	}

	tests, hasCallers, _ := reachRun(symbols, relations, reachDefaults())

	if !hasCallers {
		t.Error("hasCallers = false, want true — a TESTS edge is an inbound edge")
	}
	reachFind(t, tests, "TestViaTestsEdge")
}

// ASYNC_CALLS is in the call family searchRelatedCallRelation accepts, so a test
// that reaches the anchor asynchronously must be selected like any other.
func TestReachAcceptsAnAsyncCallEdge(t *testing.T) {
	symbols := []SymbolRecord{
		reachAnchor(),
		coverSymbol("test:async", "TestAsyncPath", "TestAsyncPath", "function", "flow/flow_test.go", 10, 18),
	}
	relations := []RelationRecord{
		{FromID: "test:async", ToID: "src:Put", Type: "ASYNC_CALLS", Confidence: 0.8},
	}

	tests, hasCallers, _ := reachRun(symbols, relations, reachDefaults())

	if !hasCallers {
		t.Error("hasCallers = false, want true")
	}
	reachFind(t, tests, "TestAsyncPath")
}
