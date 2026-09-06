package sem

import "sort"

// Change-anchored test selection.
//
// `impact` answers what a change affects. `verify` runs a test command the caller supplies.
// Nothing joins them, and nothing is anchored to a change set: today a developer computes a
// blast radius, guesses which tests cover it, and hands that guess to verify.
//
// Gate closes that loop. It takes an entity-level change set, resolves each changed entity to a
// graph symbol, asks which tests reach it, and returns a verdict per changed symbol together with
// the evidence chain that justified each selected test.
//
// The verdict that matters is UNCOVERED: a changed symbol that other code depends on and that no
// test reaches. `search` already finds a covering test for one symbol, but when it finds nothing
// the block is simply absent (search_covertest.go), and absence is indistinguishable from not
// having asked. Gate turns that silence into a reported gap.
//
// The pipeline (resolve -> reach -> attribute -> verdict) is a pure function of the change set and
// the snapshot: no I/O, no clock, no network. Two identical requests produce identical results,
// which is what docs/brain-and-graph-boundaries.md requires of anything living in this package.

// GateSchemaVersion pins the shape of GateResult so a persisted copy can be read back knowing
// which schema wrote it, matching the convention Result already follows.
const GateSchemaVersion = "gate.v1"

// GateVerdict is the per-symbol answer.
type GateVerdict string

const (
	// GateCovered: at least one test reaches this changed symbol within the requested depth.
	GateCovered GateVerdict = "COVERED"
	// GateUncovered: other code depends on this changed symbol, but no test reaches it.
	// This is the finding the command exists to surface.
	GateUncovered GateVerdict = "UNCOVERED"
	// GateIsolated: nothing calls this changed symbol at all — dead code, an entry point, or
	// library API whose callers live outside this repository.
	GateIsolated GateVerdict = "ISOLATED"
)

// Reasons a changed entity could not be tied to exactly one symbol.
//
// All three are reported rather than dropped. A change the graph cannot place is precisely where a
// coverage claim would be silently wrong, so it has to stay visible in the output — the same
// principle as GateUncovered. A tool built to remove silence must not introduce its own.
const (
	GateUnresolvedNotFound  = "not-found"
	GateUnresolvedAmbiguous = "ambiguous"
	GateUnresolvedModule    = "module-scope"
)

// GateOptions configures one analysis.
type GateOptions struct {
	// Depth bounds the inbound call walk. 1 or 2; 0 means the default of 2, matching impact.
	Depth int
	// MaxFanOut caps how many inbound edges are expanded per level. A hub symbol can have
	// thousands; past this bound the result is marked Truncated rather than silently shortened.
	MaxFanOut int
}

const (
	gateDefaultDepth     = 2
	gateDefaultMaxFanOut = 512
)

// withDefaults fills unset options so callers can pass a zero GateOptions.
func (o GateOptions) withDefaults() GateOptions {
	if o.Depth <= 0 {
		o.Depth = gateDefaultDepth
	}
	if o.MaxFanOut <= 0 {
		o.MaxFanOut = gateDefaultMaxFanOut
	}
	return o
}

// GateSelectedTest is one test chosen for one changed symbol, with the evidence that chose it.
type GateSelectedTest struct {
	SymbolID  string `json:"symbol_id"`
	Name      string `json:"name"`
	FilePath  string `json:"file_path"`
	StartLine int    `json:"start_line"`
	// Route records which evidence admitted this test: "edge" (the graph resolved a call or
	// TESTS relation), "mirror" (it lives in the anchor's mirror test file), or "name" (its name
	// names the anchor). Printing it lets a reader weigh a resolved edge against a convention.
	Route string `json:"route"`
	// Depth is how many hops separate the test from the changed symbol.
	Depth int `json:"depth"`
	// Chain is the path from the test down to the changed symbol, as qualified names. This is
	// what makes a selection checkable: every hop can be re-queried with `graph neighbors`.
	Chain []string `json:"chain"`
	// Evidence grades Route: a resolved edge is confirmed structural evidence, a mirror or name
	// match is a heuristic. Route already said which convention fired; Evidence says whether it
	// is a convention at all, which is the part a reader has to act on.
	Evidence GateEvidence `json:"evidence"`
}

// GateChangedSymbol is one changed symbol and its verdict.
type GateChangedSymbol struct {
	Name       string             `json:"name"`
	Kind       string             `json:"kind"`
	FilePath   string             `json:"file_path"`
	StartLine  int                `json:"start_line"`
	ChangeType string             `json:"change_type"`
	Dependents int                `json:"dependents"`
	Verdict    GateVerdict        `json:"verdict"`
	Tests      []GateSelectedTest `json:"tests,omitempty"`
	// Truncated marks that the fan-out cap was hit while walking to this symbol, so the test
	// list may be incomplete. Reported, never silent.
	Truncated bool `json:"truncated,omitempty"`
	// Evidence grades the verdict: the strongest evidence behind it when tests were selected,
	// and unverified when the verdict rests on the absence of an edge.
	Evidence GateEvidence `json:"evidence"`
}

// GateUnresolved is a changed entity that could not be tied to exactly one symbol.
type GateUnresolved struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// GateGapInventoryOnly marks a changed file whose language is parsed for inventory only: it has
// file and symbol records but no relations at all, so every reachability answer about it is an
// artifact of the tier rather than a finding about the code.
const GateGapInventoryOnly = "E_INVENTORY_ONLY_LANGUAGE"

// GateAnalysisGap is one reason the analysis behind this change set was incomplete.
type GateAnalysisGap struct {
	FilePath string `json:"file_path,omitempty"`
	Code     string `json:"code"`
	Reason   string `json:"reason"`
}

// GateResult is the whole answer.
type GateResult struct {
	SchemaVersion string `json:"schema_version"`
	Base          string `json:"base"`
	Head          string `json:"head"`
	Checkpoint    string `json:"checkpoint,omitempty"`
	Profile       string `json:"profile"`
	// TestsRelationAvailable is false when the snapshot was built at a profile that does not
	// emit TESTS edges (only the full profile does). Attribution still works through the mirror
	// and name routes, but one evidence route is missing, and a caller reading UNCOVERED needs
	// to know that before trusting it.
	TestsRelationAvailable bool `json:"tests_relation_available"`
	// SkippedTestFileChanges counts changes inside test files. A changed test is not something
	// that needs a test written for it, so verdicting one would bury the real findings in noise.
	// It is counted rather than dropped silently, so the total still reconciles with the diff.
	SkippedTestFileChanges int `json:"skipped_test_file_changes,omitempty"`
	// SkippedNonTestSelections counts symbols that attribution admitted but that are not reportable
	// as tests: helpers defined inside a test file, reached by a naming convention rather than a
	// resolved edge. Counted rather than dropped silently, so a reader can see that the file was
	// considered and what came of it.
	SkippedNonTestSelections int `json:"skipped_non_test_selections,omitempty"`
	// SkippedNonCallableChanges counts changes to entities nothing can call — markdown headings
	// and fenced blocks, YAML and JSON keys. No test can reach one, so verdicting them produces
	// permanent ISOLATED noise that buries the real findings. Counted, not silently dropped.
	SkippedNonCallableChanges int `json:"skipped_non_callable_changes,omitempty"`
	// Partial is true when the analysis behind these verdicts was incomplete over the files this
	// change set touches. It does not mean a verdict is wrong; it means the input the verdict was
	// computed from was not whole, so no verdict here may be read as certain.
	Partial bool `json:"partial"`
	// AnalysisGaps names why, one entry per reason. Partial without a named reason would be an
	// unfalsifiable hedge, which is the same failure as a confident wrong answer.
	AnalysisGaps []GateAnalysisGap   `json:"analysis_gaps,omitempty"`
	Changed      []GateChangedSymbol `json:"changed"`
	Unresolved   []GateUnresolved    `json:"unresolved,omitempty"`
	Warnings     []ProviderWarning   `json:"warnings,omitempty"`
}

// --- the seam between resolution/verdict and reachability/attribution -------------------------
//
// These two types are the contract between internal/sem/gate.go and internal/sem/gate_reach.go.
// They are declared here so the reachability side can be implemented against a fixed shape
// without either file having to be edited by both authors.

type gateReachOptions struct {
	Depth     int
	MaxFanOut int
}

// gateReachedTest is one test symbol found to reach an anchor, with how it was found.
type gateReachedTest struct {
	Symbol SymbolRecord
	Route  string
	Depth  int
	Chain  []string
}

// gateNonCallableKind reports whether an entity kind can never participate in a call relation.
//
// These are the fallback parsers' structural kinds: "section" covers markdown headings and YAML
// and JSON keys, "code_fence" covers fenced blocks, "setting" covers configuration entries. The
// list is a deny-list rather than an allow-list of callable kinds on purpose — the provider emits
// language-specific kinds across more than twenty languages, and a missing entry in an allow-list
// would silently discard real code, which is a far worse failure than letting one heading through.
func gateNonCallableKind(kind string) bool {
	switch kind {
	case "section", "code_fence", "setting":
		return true
	default:
		return false
	}
}

// gateReportableTests keeps only the selections that can actually be checked by a reader.
//
// Attribution admits any symbol whose path is a test artifact, so a fixture helper inside a test
// file arrives looking like a test case. Running against a JavaScript repository selected `faker`
// from `tests/pat-info.test.js` as the covering "test" for a changed handler. Sending a reader
// there to verify a verdict is sending them somewhere that cannot verify anything.
//
// A convention route (mirror, name) must therefore also satisfy the name convention: without it,
// the only claim left is "a symbol in a nearby file", which is evidence about the FILE and not
// about which of its symbols exercises this change — the same bar searchCoveringTestScore applies.
// A resolved edge is kept whatever the name, because the graph saw the call, and a helper that
// really calls the anchor is real evidence that test code exercises it.
func gateReportableTests(tests []gateReachedTest) ([]gateReachedTest, int) {
	reportable := make([]gateReachedTest, 0, len(tests))
	dropped := 0
	for _, test := range tests {
		if test.Route != gateRouteEdge && !searchTestNameShaped(test.Symbol.Name) {
			dropped++
			continue
		}
		reportable = append(reportable, test)
	}
	if len(reportable) == 0 {
		return nil, dropped
	}
	return reportable, dropped
}

// --- Curveball: evidence grading ---------------------------------------------------------------

// GateEvidence grades how much a claim about a changed symbol is actually worth.
type GateEvidence string

// gateRouteEdge is the route label gate_reach.go emits for a resolved relation. The other two
// labels it emits ("mirror", "name") are conventions and share the heuristic grade, so only the
// structural one needs naming here.
const gateRouteEdge = "edge"

const (
	// GateEvidenceConfirmed: a resolved CALLS/ASYNC_CALLS/TESTS edge.
	GateEvidenceConfirmed GateEvidence = "confirmed"
	// GateEvidenceHeuristic: a mirror test file or a name mentioning the symbol.
	GateEvidenceHeuristic GateEvidence = "heuristic"
	// GateEvidenceUnverified: a claim resting on the absence of evidence.
	GateEvidenceUnverified GateEvidence = "unverified"
)

// gateEvidenceForRoute grades a single attribution route.
//
// gate_reach.go admits a test through three routes and records which one fired. Only "edge" is a
// relation the resolver actually followed; "mirror" and "name" are filename and identifier
// conventions that correlate with coverage without demonstrating it.
func gateEvidenceForRoute(route string) GateEvidence {
	if route == gateRouteEdge {
		return GateEvidenceConfirmed
	}
	return GateEvidenceHeuristic
}

// gateEvidenceFor grades the strongest evidence behind a set of selected tests.
//
// The aggregate takes the strongest member, not the weakest: one resolved edge justifies COVERED
// by itself, so a convention match tagging along behind a real call must not downgrade it. An
// empty set is unverified — the claim then rests on the absence of an edge, which is a fact about
// what the resolver followed rather than a fact about the repository.
func gateEvidenceFor(tests []gateReachedTest) GateEvidence {
	if len(tests) == 0 {
		return GateEvidenceUnverified
	}
	for _, test := range tests {
		if gateEvidenceForRoute(test.Route) == GateEvidenceConfirmed {
			return GateEvidenceConfirmed
		}
	}
	return GateEvidenceHeuristic
}

// gateGapRelevantPaths is the set of files whose analysis state can actually affect a verdict.
//
// Two kinds of file qualify. First, any file that produced a verdicted symbol: a gap there is a gap
// in the evidence behind an answer being reported. Second, any changed test file, even though its
// own changes are skipped — a test file the parser could not read is the single most dangerous gap
// this command has, because the test that covers the change may be sitting in it unparsed, and the
// resulting UNCOVERED would be an artifact of the parser rather than a finding.
//
// A file whose changes were all excluded as non-callable does NOT qualify. A markdown file is
// inventory-only by definition, so counting it would mark the run partial on every commit that
// touched a doc, and a flag that fires constantly stops carrying information.
func gateGapRelevantPaths(result Result, verdicted []GateChangedSymbol) map[string]bool {
	paths := make(map[string]bool, len(verdicted))
	for _, changed := range verdicted {
		paths[changed.FilePath] = true
	}
	for _, file := range result.Files {
		if searchTestArtifactPath(searchLowerPath(file.Path)) {
			paths[file.Path] = true
		}
	}
	return paths
}

// gateAnalysisGaps names every way the analysis over this change set was incomplete.
//
// Scope is the files that can affect a verdict, not the whole snapshot. A parse failure in a file
// nothing touched does not make these verdicts partial, and marking it so would widen every command
// on every run until the flag stopped meaning anything.
//
// The residual limitation is stated rather than hidden: reachability walks INBOUND edges, so a
// caller in an unparsed file elsewhere can still hide a test. That is what the unverified grade on
// an UNCOVERED verdict already says, and it is why the grade is carried even when Partial is false.
func gateAnalysisGaps(relevant map[string]bool, snapshot ProviderSnapshot) []GateAnalysisGap {
	changed := relevant
	gaps := []GateAnalysisGap{}
	seen := map[string]bool{}

	add := func(path, code, reason string) {
		key := path + "\x00" + code
		if seen[key] {
			return
		}
		seen[key] = true
		gaps = append(gaps, GateAnalysisGap{FilePath: path, Code: code, Reason: reason})
	}

	for _, failure := range snapshot.Header.PartialFailures {
		if changed[failure.FilePath] {
			add(failure.FilePath, failure.Code, gateGapReason(failure.EffectOnCompleteness, failure.Detail))
		}
	}
	for _, warning := range snapshot.Header.Warnings {
		if changed[warning.FilePath] {
			add(warning.FilePath, warning.Code, gateGapReason(warning.EffectOnCompleteness, warning.Detail))
		}
	}
	for _, file := range snapshot.Files {
		if !changed[file.Path] {
			continue
		}
		if snapshot.Header.LanguageTiers[file.Language] == "inventory-only" {
			add(file.Path, GateGapInventoryOnly,
				file.Language+" is parsed for inventory only: no call relations are emitted for it, so reachability cannot be evaluated")
		}
	}

	sort.Slice(gaps, func(left, right int) bool {
		if gaps[left].FilePath != gaps[right].FilePath {
			return gaps[left].FilePath < gaps[right].FilePath
		}
		return gaps[left].Code < gaps[right].Code
	})
	if len(gaps) == 0 {
		return nil
	}
	return gaps
}

// gateGapReason prefers the provider's completeness note and falls back to its free-text detail.
func gateGapReason(effect, detail string) string {
	if effect != "" {
		return effect
	}
	return detail
}

// --- L3: resolution ---------------------------------------------------------------------------

// changedSymbol is one entity change tied to the symbol it names.
type changedSymbol struct {
	Change EntityChange
	Path   string
	Symbol SymbolRecord
}

// unresolvedChange is one entity change that could not be tied to exactly one symbol.
type unresolvedChange struct {
	Change EntityChange
	Path   string
	Reason string
}

type gateResolution struct {
	Resolved   []changedSymbol
	Unresolved []unresolvedChange
}

// resolveChangedSymbols maps the entity changes in a diff result onto snapshot symbols.
//
// The diff speaks entity names; the graph speaks symbol IDs. Matching on name alone is wrong: the
// same name routinely appears in several files, and overloads repeat it inside one file (upstream
// issue #34, "compound-v1 symbol IDs are unstable for same-name/overloaded symbols"). So a
// candidate must match on BOTH the name and the file the change was found in, and a name that
// still matches more than once inside that file is reported ambiguous rather than resolved
// arbitrarily.
//
// Module-scope entities are synthetic: they represent code outside any named symbol, so there is
// no symbol to call and no test that can reach one. They are reported with their own reason
// instead of being mislabelled as missing.
func resolveChangedSymbols(result Result, symbols []SymbolRecord) gateResolution {
	// Index both spellings. The change analysis names a nested entity by its qualified path
	// ("fileRelationScan.relations") while the symbol table may carry only the bare name, so
	// matching one spelling alone reported real upstream changes as not-found. A symbol whose
	// qualified name equals its bare name is indexed once, not twice, so it cannot look ambiguous
	// to itself.
	byPathAndName := map[string][]SymbolRecord{}
	for _, symbol := range symbols {
		byPathAndName[symbol.FilePath+"\x00"+symbol.Name] = append(byPathAndName[symbol.FilePath+"\x00"+symbol.Name], symbol)
		if symbol.QualifiedName != "" && symbol.QualifiedName != symbol.Name {
			key := symbol.FilePath + "\x00" + symbol.QualifiedName
			byPathAndName[key] = append(byPathAndName[key], symbol)
		}
	}

	resolution := gateResolution{}
	for _, file := range result.Files {
		for _, change := range file.Changes {
			if change.Kind == moduleKind {
				resolution.Unresolved = append(resolution.Unresolved, unresolvedChange{
					Change: change,
					Path:   file.Path,
					Reason: GateUnresolvedModule,
				})
				continue
			}
			candidates := byPathAndName[file.Path+"\x00"+change.Name]
			switch len(candidates) {
			case 1:
				resolution.Resolved = append(resolution.Resolved, changedSymbol{
					Change: change,
					Path:   file.Path,
					Symbol: candidates[0],
				})
			case 0:
				resolution.Unresolved = append(resolution.Unresolved, unresolvedChange{
					Change: change,
					Path:   file.Path,
					Reason: GateUnresolvedNotFound,
				})
			default:
				resolution.Unresolved = append(resolution.Unresolved, unresolvedChange{
					Change: change,
					Path:   file.Path,
					Reason: GateUnresolvedAmbiguous,
				})
			}
		}
	}
	return resolution
}

// --- L6: verdict ------------------------------------------------------------------------------

// gateVerdictFor turns reachability output into the answer a reader acts on.
//
// The interesting case is the third one. hasCallers comes from resolved graph edges;
// dependents comes from the change analysis, which counts references textually and so sees uses
// the call resolver could not resolve. When they disagree — no resolved caller, but dependents
// counted — the honest reading is that something uses this symbol through a call static analysis
// missed. Reporting ISOLATED there would assert "nothing uses this" on evidence we do not have,
// so the tie breaks toward UNCOVERED, which asks the reader to look rather than telling them not
// to bother.
//
// Note what UNCOVERED does NOT claim. It means no test reaches this symbol along a path the graph
// can see. Interface dispatch, reflection, table-driven registration and generated code can all
// hide a real test, so this is a prompt to check, never a proof of absence.
func gateVerdictFor(tests []gateReachedTest, hasCallers bool, dependents int) GateVerdict {
	if len(tests) > 0 {
		return GateCovered
	}
	if hasCallers || dependents > 0 {
		return GateUncovered
	}
	return GateIsolated
}

// --- Gate: orchestration ----------------------------------------------------------------------

// Gate answers, for one change set, which tests reach each changed symbol and which changed
// symbols nothing reaches.
//
// It is a pure function of (result, snapshot, options): no file reads, no git, no clock, no
// network. Everything it needs was already produced by the change analysis and the provider
// snapshot, which is what lets the whole pipeline be table-tested and what keeps it on the
// provider side of the line drawn in docs/brain-and-graph-boundaries.md.
func Gate(result Result, snapshot ProviderSnapshot, options GateOptions) GateResult {
	options = options.withDefaults()

	symbolsByID := make(map[string]SymbolRecord, len(snapshot.Symbols))
	symbolsByFile := make(map[string][]SymbolRecord, len(snapshot.Files))
	for _, symbol := range snapshot.Symbols {
		symbolsByID[symbol.ID] = symbol
		symbolsByFile[symbol.FilePath] = append(symbolsByFile[symbol.FilePath], symbol)
	}

	gate := GateResult{
		SchemaVersion:          GateSchemaVersion,
		Base:                   result.Base,
		Head:                   result.Head,
		Checkpoint:             result.Checkpoint,
		Profile:                snapshot.Header.Profile,
		TestsRelationAvailable: gateRelationAdvertised(snapshot.Header.RelationSet, "TESTS"),
		Warnings:               snapshot.Header.Warnings,
	}

	resolution := resolveChangedSymbols(result, snapshot.Symbols)

	reach := gateReachOptions{Depth: options.Depth, MaxFanOut: options.MaxFanOut}
	for _, resolved := range resolution.Resolved {
		if searchTestArtifactPath(searchLowerPath(resolved.Path)) {
			gate.SkippedTestFileChanges++
			continue
		}
		if gateNonCallableKind(resolved.Change.Kind) || gateNonCallableKind(resolved.Symbol.Kind) {
			gate.SkippedNonCallableChanges++
			continue
		}
		tests, hasCallers, truncated := gateReach(
			resolved.Symbol, snapshot.Relations, symbolsByID, symbolsByFile, reach,
		)
		// Filtered before the verdict, not after: reporting COVERED on selections we then refuse
		// to show would be the same false assurance the evidence grades exist to remove.
		tests, droppedSelections := gateReportableTests(tests)
		gate.SkippedNonTestSelections += droppedSelections
		gate.Changed = append(gate.Changed, GateChangedSymbol{
			Name:       resolved.Symbol.Name,
			Kind:       resolved.Symbol.Kind,
			FilePath:   resolved.Symbol.FilePath,
			StartLine:  resolved.Symbol.StartLine,
			ChangeType: resolved.Change.Type,
			Dependents: resolved.Change.DependentsCount,
			Verdict:    gateVerdictFor(tests, hasCallers, resolved.Change.DependentsCount),
			Tests:      gateSelectedTests(tests),
			Truncated:  truncated,
			Evidence:   gateEvidenceFor(tests),
		})
	}

	for _, unresolved := range resolution.Unresolved {
		if searchTestArtifactPath(searchLowerPath(unresolved.Path)) {
			gate.SkippedTestFileChanges++
			continue
		}
		if gateNonCallableKind(unresolved.Change.Kind) {
			gate.SkippedNonCallableChanges++
			continue
		}
		gate.Unresolved = append(gate.Unresolved, GateUnresolved{
			Name:   unresolved.Change.Name,
			Kind:   unresolved.Change.Kind,
			Path:   unresolved.Path,
			Reason: unresolved.Reason,
		})
	}

	// Positional order is the honest presentation of a change set — it mirrors how a reader
	// walks a diff — and sorting explicitly keeps map iteration out of the output, so two
	// identical requests render byte-identically.
	sort.Slice(gate.Changed, func(left, right int) bool {
		if gate.Changed[left].FilePath != gate.Changed[right].FilePath {
			return gate.Changed[left].FilePath < gate.Changed[right].FilePath
		}
		if gate.Changed[left].StartLine != gate.Changed[right].StartLine {
			return gate.Changed[left].StartLine < gate.Changed[right].StartLine
		}
		return gate.Changed[left].Name < gate.Changed[right].Name
	})
	sort.Slice(gate.Unresolved, func(left, right int) bool {
		if gate.Unresolved[left].Path != gate.Unresolved[right].Path {
			return gate.Unresolved[left].Path < gate.Unresolved[right].Path
		}
		return gate.Unresolved[left].Name < gate.Unresolved[right].Name
	})

	// Gaps are computed last because relevance depends on which files actually produced a verdict.
	gate.AnalysisGaps = gateAnalysisGaps(gateGapRelevantPaths(result, gate.Changed), snapshot)
	gate.Partial = len(gate.AnalysisGaps) > 0

	return gate
}

// gateRelationAdvertised reports whether the snapshot's header claims to carry a relation type.
// An empty set means the header did not enumerate one, which is not the same as promising the
// relation is absent, so it is read as "not advertised" rather than "known missing".
func gateRelationAdvertised(relationSet []string, relation string) bool {
	for _, candidate := range relationSet {
		if candidate == relation {
			return true
		}
	}
	return false
}

// gateSelectedTests projects reachability output onto the public result shape.
func gateSelectedTests(tests []gateReachedTest) []GateSelectedTest {
	if len(tests) == 0 {
		return nil
	}
	selected := make([]GateSelectedTest, 0, len(tests))
	for _, test := range tests {
		selected = append(selected, GateSelectedTest{
			SymbolID:  test.Symbol.ID,
			Name:      test.Symbol.Name,
			FilePath:  test.Symbol.FilePath,
			StartLine: test.Symbol.StartLine,
			Route:     test.Route,
			Depth:     test.Depth,
			Chain:     test.Chain,
			Evidence:  gateEvidenceForRoute(test.Route),
		})
	}
	return selected
}
