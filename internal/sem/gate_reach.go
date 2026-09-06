package sem

// Reachability and test attribution for `gate` — OWNED BY PERSON 2.
//
// This file answers one question: given a changed symbol, which tests reach it?
//
// It is deliberately the only file implementing that. internal/sem/gate.go owns resolution and
// the verdict and does not need editing to complete this work; the two types this signature
// depends on (gateReachOptions, gateReachedTest) are declared there and are fixed.
//
// What to implement:
//
//  1. Walk INBOUND call edges from the anchor, transitively, to options.Depth (max 2).
//     An edge points at the anchor when relation.ToID == anchor.ID; the caller is
//     symbolsByID[relation.FromID]. Accept the call family via searchRelatedCallRelation
//     (search_related.go:113 — CALLS and ASYNC_CALLS) plus the "TESTS" relation, which that
//     helper deliberately excludes; search_covertest.go:460 shows the same combination.
//
//     This transitive walk is the point of the whole feature. The existing covering-test finder
//     stops at one hop (search_covertest.go:456 skips any relation whose ToID is not the anchor),
//     so a test reaching the change through one intermediate call is invisible to it today.
//     internal/cli/impact.go:504-567 is the shape to copy — a depth-bounded frontier over a
//     prebuilt adjacency map — but it is function-local there and cannot be imported.
//
//  2. Admit a reached symbol as a TEST when its path is a test artifact or its name is
//     test-shaped: searchTestArtifactPath(searchLowerPath(symbol.FilePath)) — note the
//     lowercase helper is required first — or searchTestNameShaped(symbol.Name).
//     searchCoveringTestCandidates(anchor, relations, symbolsByID, symbolsByFile)
//     (search_covertest.go:421) already gathers one-hop candidates through three routes
//     (resolved edge, mirror test file, name mention); reuse it for the depth-1 layer rather
//     than reimplementing the mirror and name routes, and add the transitive layer on top.
//
//  3. Set Route to "edge", "mirror" or "name" per the route that admitted the test, Depth to the
//     hop count, and Chain to the qualified names from the test down to the anchor, test first.
//     Chain is what makes a selection checkable by a human, so it must be accurate.
//
//  4. Return hasCallers = true when the anchor has ANY inbound call edge, test or not. That is
//     what separates UNCOVERED (things depend on this and no test reaches it — the alarm) from
//     ISOLATED (nothing calls it at all). Getting this wrong turns the headline finding into
//     noise, so it is worth its own test.
//
//  5. Guard cycles and self-edges: a symbol must never be expanded twice, and A->B->A must
//     terminate. Cap expansion at options.MaxFanOut per level and return truncated = true when
//     the cap bites — a shortened list that does not say it was shortened would read as "no more
//     tests exist", which is exactly the silence this command exists to remove.
//
// Constraints:
//   - PURE. No file reads, no git, no network, no clock, no randomness.
//   - DETERMINISTIC. Never range over a map into output. Sort the returned tests by
//     (FilePath, StartLine, Symbol.ID) before returning; two identical calls must return
//     byte-identical results.
//   - Do not edit gate.go, anything under internal/cli/, or any existing file in this package.
//
// Tests go in gate_reach_test.go, named TestReach*, following the fixture style in
// search_covertest_test.go:15-131 (coverSymbol / coverCall helpers). Include a depth-2 case, a
// cycle case, a fan-out truncation case, and a non-test caller that must not be selected.
// Done when `go test ./internal/sem/ -run TestReach` passes and `gofmt -l -s .` prints nothing.
func gateReach(
	anchor SymbolRecord,
	relations []RelationRecord,
	symbolsByID map[string]SymbolRecord,
	symbolsByFile map[string][]SymbolRecord,
	options gateReachOptions,
) (tests []gateReachedTest, hasCallers bool, truncated bool) {
	return nil, false, false
}
