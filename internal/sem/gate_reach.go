package sem

import "sort"

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
	if anchor.ID == "" {
		return nil, false, false
	}
	depth := options.Depth
	if depth > gateReachMaxDepth {
		depth = gateReachMaxDepth
	}

	callersOf, anchorHasCallers := gateReachInboundEdges(anchor, relations)
	hasCallers = anchorHasCallers

	// Depth 1 is the existing covering-test gatherer, not a reimplementation of it.
	// It already applies all three routes and the test-admission filter, and reusing
	// it is what keeps this feature additive rather than a second, drifting opinion
	// about what counts as a test.
	admitted := map[string]bool{anchor.ID: true}
	reached := make([]gateReachedTest, 0)
	if depth >= 1 {
		mirrors := gateReachMirrorFiles(anchor, symbolsByFile)
		for _, candidate := range searchCoveringTestCandidates(anchor, relations, symbolsByID, symbolsByFile) {
			if admitted[candidate.symbol.ID] {
				continue
			}
			admitted[candidate.symbol.ID] = true
			reached = append(reached, gateReachedTest{
				Symbol: candidate.symbol,
				Route:  gateReachRoute(candidate, mirrors),
				Depth:  1,
				// The anchor is not in the chain: the caller already knows which
				// symbol it asked about, and repeating it in every entry adds a
				// column of noise to the one field a reader has to check by hand.
				// A depth-1 chain is therefore the test alone.
				Chain: []string{gateReachName(candidate.symbol)},
			})
		}
	}

	// Everything below is the transitive layer. A test that reaches the anchor
	// through an intermediate call is invisible to the one-hop gatherer above, and
	// finding it is the whole reason this file exists.
	//
	// Only NON-test callers are expanded. Walking through a test would attribute
	// whatever calls that test to the anchor, which is not a claim the graph supports.
	frontier := gateReachExpandable(anchor, callersOf[anchor.ID], symbolsByID, admitted)
	for hop := 2; hop <= depth; hop++ {
		var next []gateReachNode
		for _, node := range frontier {
			callers := callersOf[node.id]
			// The cap is per expanded symbol and counted in callers examined, so one
			// hot intermediate cannot silently swallow the level's whole allowance.
			if options.MaxFanOut > 0 && len(callers) > options.MaxFanOut {
				callers = callers[:options.MaxFanOut]
				truncated = true
			}
			for _, callerID := range callers {
				if admitted[callerID] {
					continue
				}
				caller, known := symbolsByID[callerID]
				if !known {
					// An edge from a symbol this snapshot does not carry. It is a
					// real dependent, already counted in hasCallers, but it cannot
					// be walked through and must never be reported as a test.
					continue
				}
				admitted[callerID] = true
				if gateReachIsTest(caller) {
					reached = append(reached, gateReachedTest{
						Symbol: caller,
						// Transitive admission is always by resolved call edge: the
						// mirror and name routes are proximity arguments about the
						// anchor's own file and say nothing about a symbol two hops
						// away.
						Route: "edge",
						Depth: hop,
						Chain: gateReachChain(caller, node.chain),
					})
					continue
				}
				next = append(next, gateReachNode{
					id:    callerID,
					chain: append(append([]string{}, node.chain...), gateReachName(caller)),
				})
			}
		}
		frontier = next
	}

	sort.Slice(reached, func(left, right int) bool {
		if reached[left].Symbol.FilePath != reached[right].Symbol.FilePath {
			return reached[left].Symbol.FilePath < reached[right].Symbol.FilePath
		}
		if reached[left].Symbol.StartLine != reached[right].Symbol.StartLine {
			return reached[left].Symbol.StartLine < reached[right].Symbol.StartLine
		}
		return reached[left].Symbol.ID < reached[right].Symbol.ID
	})
	if len(reached) == 0 {
		return nil, hasCallers, truncated
	}
	return reached, hasCallers, truncated
}

// gateReachMaxDepth is the ceiling the spec puts on options.Depth. Two hops is
// where the evidence still holds: a test that reaches the change through one
// intermediate call genuinely exercises it, while each hop past that weakens the
// claim faster than it adds tests.
const gateReachMaxDepth = 2

// gateReachNode is one frontier entry: the symbol to expand, and the intermediates
// already walked through to reach it, anchor-side first.
type gateReachNode struct {
	id    string
	chain []string
}

// gateReachChain builds a chain that reads test first, then the intermediates in
// the order a reader would follow the calls down toward the anchor.
//
// The anchor itself is excluded: the caller already knows which symbol it asked
// about, so repeating it in every entry is a column of noise in the one field a
// reader has to check by hand.
//
// node.chain is accumulated anchor-side first, so it is reversed here. The chain
// is the part of a selection a human can verify by opening two files, which is the
// only reason to trust the selection at all — so it has to be in call order, not
// in whatever order the walk happened to build it.
func gateReachChain(test SymbolRecord, intermediates []string) []string {
	chain := make([]string, 0, len(intermediates)+1)
	chain = append(chain, gateReachName(test))
	for index := len(intermediates) - 1; index >= 0; index-- {
		chain = append(chain, intermediates[index])
	}
	return chain
}

// gateReachInboundEdges inverts the relation list once into callee -> caller ids,
// and reports whether the anchor itself has any inbound call edge.
//
// hasCallers counts the EDGE, not the resolved symbol: an edge from a caller this
// snapshot does not carry still means something depends on the anchor, and
// reporting that symbol as ISOLATED would claim nothing calls it at all.
func gateReachInboundEdges(anchor SymbolRecord, relations []RelationRecord) (map[string][]string, bool) {
	callersOf := make(map[string][]string)
	anchorHasCallers := false
	for index := range relations {
		relation := &relations[index]
		if !searchRelatedCallRelation(relation.Type) && relation.Type != "TESTS" {
			continue
		}
		if relation.FromID == relation.ToID {
			// A self-edge is not a dependent, and expanding one cannot terminate.
			continue
		}
		if relation.ToID == anchor.ID {
			anchorHasCallers = true
		}
		callersOf[relation.ToID] = append(callersOf[relation.ToID], relation.FromID)
	}
	// Relation order in a snapshot follows file-walk order, an artifact of the
	// filesystem rather than a property of the code. Sorting here is what makes the
	// fan-out cap bite on the same symbols every run, and therefore what makes a
	// truncated result reproducible rather than arbitrary.
	for id := range callersOf {
		sort.Strings(callersOf[id])
		callersOf[id] = gateReachDedupe(callersOf[id])
	}
	return callersOf, anchorHasCallers
}

// gateReachDedupe collapses repeated caller ids in a sorted slice. Two call sites
// inside one function are two relation records and one dependent.
func gateReachDedupe(sorted []string) []string {
	if len(sorted) < 2 {
		return sorted
	}
	unique := sorted[:1]
	for _, id := range sorted[1:] {
		if id != unique[len(unique)-1] {
			unique = append(unique, id)
		}
	}
	return unique
}

// gateReachExpandable is the depth-1 frontier: direct callers that are known, not
// already admitted, and not themselves tests.
func gateReachExpandable(
	anchor SymbolRecord,
	callerIDs []string,
	symbolsByID map[string]SymbolRecord,
	admitted map[string]bool,
) []gateReachNode {
	frontier := make([]gateReachNode, 0, len(callerIDs))
	for _, callerID := range callerIDs {
		if callerID == anchor.ID || admitted[callerID] {
			continue
		}
		caller, known := symbolsByID[callerID]
		if !known || gateReachIsTest(caller) {
			continue
		}
		admitted[callerID] = true
		frontier = append(frontier, gateReachNode{id: callerID, chain: []string{gateReachName(caller)}})
	}
	return frontier
}

// gateReachIsTest applies the repository's own test predicates, in the order the
// covering-test gatherer applies them: a test artifact path (lowercased first, as
// searchTestArtifactPath requires) or a test-shaped name.
func gateReachIsTest(symbol SymbolRecord) bool {
	return searchTestArtifactPath(searchLowerPath(symbol.FilePath)) || searchTestNameShaped(symbol.Name)
}

// gateReachMirrorFiles is the set of mirror test files for the anchor, used to
// tell a mirror-route candidate from a name-route one.
func gateReachMirrorFiles(anchor SymbolRecord, symbolsByFile map[string][]SymbolRecord) map[string]bool {
	mirrors := map[string]bool{}
	for _, filePath := range searchMirrorTestFiles(anchor.FilePath, symbolsByFile) {
		mirrors[filePath] = true
	}
	return mirrors
}

// gateReachRoute names why a depth-1 candidate was admitted.
//
// searchCoveringTestCandidates reports only whether a resolved edge admitted the
// candidate, so the two non-edge routes are separated here by the same mirror
// lookup it used: a candidate in a mirror file came in through the mirror route,
// and anything else came in by naming the anchor.
func gateReachRoute(candidate searchCoveringTestCandidate, mirrors map[string]bool) string {
	switch {
	case candidate.edge:
		return "edge"
	case mirrors[candidate.symbol.FilePath]:
		return "mirror"
	default:
		return "name"
	}
}

// gateReachName is the name a chain entry carries. The qualified name is what makes
// a chain checkable — `Store.Put` rather than `Put` — and the bare name is the
// fallback for languages whose provider does not qualify.
func gateReachName(symbol SymbolRecord) string {
	if symbol.QualifiedName != "" {
		return symbol.QualifiedName
	}
	return symbol.Name
}
