package sem

import (
	"reflect"
	"testing"
)

// gateFixtureSymbols is the symbol table the resolution tests resolve against.
//
// Store.Put appears twice, in two files, on purpose: that collision is the whole reason
// resolution is keyed by path as well as name. Upstream issue #34 records that compound-v1
// symbol IDs are unstable for same-name and overloaded symbols, so name-only matching would
// silently pick the wrong definition on exactly the repositories this command is for.
func gateFixtureSymbols() []SymbolRecord {
	return []SymbolRecord{
		{ID: "sym-store-put", Kind: "method", Name: "Store.Put", FilePath: "store.go", StartLine: 88, EndLine: 96},
		{ID: "sym-cache-put", Kind: "method", Name: "Store.Put", FilePath: "cache/store.go", StartLine: 12, EndLine: 20},
		{ID: "sym-parse-opts", Kind: "function", Name: "parseOpts", FilePath: "opts.go", StartLine: 12, EndLine: 30},
	}
}

func gateResultWithChange(path string, change EntityChange) Result {
	return Result{
		Base: "main",
		Head: "HEAD",
		Files: []FileChange{
			{Path: path, Status: "M", Changes: []EntityChange{change}},
		},
	}
}

func TestGateResolvesChangedEntityToSymbolInSameFile(t *testing.T) {
	t.Parallel()

	result := gateResultWithChange("store.go", EntityChange{
		Type: "BODY_CHANGED", Kind: "method", Name: "Store.Put", DependentsCount: 12,
	})

	resolution := resolveChangedSymbols(result, gateFixtureSymbols())

	if len(resolution.Unresolved) != 0 {
		t.Fatalf("expected no unresolved changes, got %+v", resolution.Unresolved)
	}
	if len(resolution.Resolved) != 1 {
		t.Fatalf("expected 1 resolved change, got %d", len(resolution.Resolved))
	}
	got := resolution.Resolved[0]
	if got.Symbol.ID != "sym-store-put" {
		t.Errorf("resolved to %q, want %q (the Store.Put defined in the changed file)", got.Symbol.ID, "sym-store-put")
	}
	if got.Path != "store.go" {
		t.Errorf("carried path %q, want %q", got.Path, "store.go")
	}
	if got.Change.DependentsCount != 12 {
		t.Errorf("dependents count %d, want 12", got.Change.DependentsCount)
	}
}

func TestGateReportsUnresolvedChangeExplicitly(t *testing.T) {
	t.Parallel()

	result := gateResultWithChange("store.go", EntityChange{
		Type: "ADDED", Kind: "function", Name: "brandNewSymbol",
	})

	resolution := resolveChangedSymbols(result, gateFixtureSymbols())

	if len(resolution.Resolved) != 0 {
		t.Fatalf("expected no resolved changes, got %+v", resolution.Resolved)
	}
	if len(resolution.Unresolved) != 1 {
		t.Fatalf("expected the change to be reported as unresolved, got %d entries", len(resolution.Unresolved))
	}
	got := resolution.Unresolved[0]
	if got.Change.Name != "brandNewSymbol" {
		t.Errorf("unresolved change name %q, want %q", got.Change.Name, "brandNewSymbol")
	}
	if got.Reason != GateUnresolvedNotFound {
		t.Errorf("reason %q, want %q", got.Reason, GateUnresolvedNotFound)
	}
}

func TestGateReportsAmbiguousChangeExplicitly(t *testing.T) {
	t.Parallel()

	symbols := append(gateFixtureSymbols(), SymbolRecord{
		ID: "sym-store-put-overload", Kind: "method", Name: "Store.Put",
		FilePath: "store.go", StartLine: 120, EndLine: 130,
	})
	result := gateResultWithChange("store.go", EntityChange{
		Type: "SIGNATURE_CHANGED", Kind: "method", Name: "Store.Put",
	})

	resolution := resolveChangedSymbols(result, symbols)

	if len(resolution.Resolved) != 0 {
		t.Fatalf("an ambiguous change must not resolve, got %+v", resolution.Resolved)
	}
	if len(resolution.Unresolved) != 1 {
		t.Fatalf("expected 1 unresolved change, got %d", len(resolution.Unresolved))
	}
	if got := resolution.Unresolved[0].Reason; got != GateUnresolvedAmbiguous {
		t.Errorf("reason %q, want %q", got, GateUnresolvedAmbiguous)
	}
}

// A module-scope change represents edits outside any named symbol — imports, top-level
// statements, comments. There is no symbol to call, so no test can reach one and asking the
// graph about it is meaningless. It must be reported under its own reason: labelling it
// "not-found" would read as a resolution failure the reader should investigate, when in fact
// nothing is wrong.
func TestGateReportsModuleScopeChangeUnderItsOwnReason(t *testing.T) {
	t.Parallel()

	result := gateResultWithChange("store.go", EntityChange{
		Type: "BODY_CHANGED", Kind: moduleKind, Name: "store.go",
	})

	resolution := resolveChangedSymbols(result, gateFixtureSymbols())

	if len(resolution.Resolved) != 0 {
		t.Fatalf("a module-scope change must not resolve to a symbol, got %+v", resolution.Resolved)
	}
	if len(resolution.Unresolved) != 1 {
		t.Fatalf("expected 1 unresolved entry, got %d", len(resolution.Unresolved))
	}
	if got := resolution.Unresolved[0].Reason; got != GateUnresolvedModule {
		t.Errorf("reason %q, want %q", got, GateUnresolvedModule)
	}
}

// --- L6: verdict ------------------------------------------------------------------------------

func gateReachedTestFixture() gateReachedTest {
	return gateReachedTest{
		Symbol: SymbolRecord{ID: "sym-test-put", Name: "TestStorePut", FilePath: "store_test.go", StartLine: 9},
		Route:  "edge",
		Depth:  1,
		Chain:  []string{"TestStorePut", "Store.Put"},
	}
}

func TestGateVerdictCoveredWhenATestReaches(t *testing.T) {
	t.Parallel()

	got := gateVerdictFor([]gateReachedTest{gateReachedTestFixture()}, true, 12)

	if got != GateCovered {
		t.Errorf("verdict %q, want %q", got, GateCovered)
	}
}

// The alarm. Other code depends on the changed symbol and no test reaches it, so the suite can go
// green without this change ever having been exercised.
func TestGateVerdictUncoveredWhenCallersButNoTestReaches(t *testing.T) {
	t.Parallel()

	got := gateVerdictFor(nil, true, 12)

	if got != GateUncovered {
		t.Errorf("verdict %q, want %q", got, GateUncovered)
	}
}

func TestGateVerdictIsolatedWhenNothingCallsIt(t *testing.T) {
	t.Parallel()

	got := gateVerdictFor(nil, false, 0)

	if got != GateIsolated {
		t.Errorf("verdict %q, want %q", got, GateIsolated)
	}
}

// The graph resolved no inbound call edge, but the change analysis counted dependents by textual
// reference. The two disagree, and the honest reading is that something depends on this symbol
// through a call the static analysis could not resolve. Calling that ISOLATED would tell the
// reader "nothing uses this" on evidence we do not have; UNCOVERED tells them to look.
func TestGateVerdictPrefersUncoveredWhenDependentsDisagreeWithGraph(t *testing.T) {
	t.Parallel()

	got := gateVerdictFor(nil, false, 12)

	if got != GateUncovered {
		t.Errorf("verdict %q, want %q", got, GateUncovered)
	}
}

// --- Gate: orchestration ----------------------------------------------------------------------

func gateSnapshotFixture(relationSet []string) ProviderSnapshot {
	return ProviderSnapshot{
		Header:  SnapshotHeader{Profile: "full", RelationSet: relationSet},
		Symbols: gateFixtureSymbols(),
	}
}

func TestGateCarriesResolutionAndVerdictIntoTheResult(t *testing.T) {
	t.Parallel()

	result := Result{
		Base: "main", Head: "HEAD",
		Files: []FileChange{{Path: "store.go", Status: "M", Changes: []EntityChange{
			{Type: "BODY_CHANGED", Kind: "method", Name: "Store.Put", DependentsCount: 12},
			{Type: "BODY_CHANGED", Kind: moduleKind, Name: "store.go"},
		}}},
	}

	got := Gate(result, gateSnapshotFixture([]string{"CALLS", "TESTS"}), GateOptions{})

	if got.SchemaVersion != GateSchemaVersion {
		t.Errorf("schema version %q, want %q", got.SchemaVersion, GateSchemaVersion)
	}
	if got.Base != "main" || got.Head != "HEAD" {
		t.Errorf("range %s..%s, want main..HEAD", got.Base, got.Head)
	}
	if len(got.Changed) != 1 {
		t.Fatalf("expected 1 changed symbol, got %d", len(got.Changed))
	}
	// gateReach is stubbed until the reachability layer lands, so no test reaches anything.
	// A symbol with 12 dependents and no reaching test is exactly the alarm case.
	if got.Changed[0].Verdict != GateUncovered {
		t.Errorf("verdict %q, want %q", got.Changed[0].Verdict, GateUncovered)
	}
	if got.Changed[0].FilePath != "store.go" || got.Changed[0].StartLine != 88 {
		t.Errorf("located at %s:%d, want store.go:88", got.Changed[0].FilePath, got.Changed[0].StartLine)
	}
	if len(got.Unresolved) != 1 || got.Unresolved[0].Reason != GateUnresolvedModule {
		t.Errorf("expected the module-scope change reported as unresolved, got %+v", got.Unresolved)
	}
}

// Output order must not depend on map iteration: two identical requests have to produce
// byte-identical results, which is what the provider's determinism rule requires.
func TestGateOrdersChangedSymbolsDeterministically(t *testing.T) {
	t.Parallel()

	result := Result{
		Base: "main", Head: "HEAD",
		Files: []FileChange{
			{Path: "opts.go", Changes: []EntityChange{{Kind: "function", Name: "parseOpts"}}},
			{Path: "store.go", Changes: []EntityChange{{Kind: "method", Name: "Store.Put"}}},
			{Path: "cache/store.go", Changes: []EntityChange{{Kind: "method", Name: "Store.Put"}}},
		},
	}

	want := []string{"cache/store.go", "opts.go", "store.go"}
	for attempt := range 3 {
		got := Gate(result, gateSnapshotFixture([]string{"CALLS"}), GateOptions{})
		if len(got.Changed) != len(want) {
			t.Fatalf("attempt %d: expected %d changed symbols, got %d", attempt, len(want), len(got.Changed))
		}
		for index, path := range want {
			if got.Changed[index].FilePath != path {
				t.Fatalf("attempt %d: position %d is %q, want %q", attempt, index, got.Changed[index].FilePath, path)
			}
		}
	}
}

// TESTS edges are only emitted at the full profile. On a snapshot without them one evidence
// route is missing, so a reader weighing an UNCOVERED verdict has to be told before trusting it.
func TestGateReportsWhenTheTestsRelationIsUnavailable(t *testing.T) {
	t.Parallel()

	result := gateResultWithChange("store.go", EntityChange{Kind: "method", Name: "Store.Put"})

	withTests := Gate(result, gateSnapshotFixture([]string{"CALLS", "TESTS"}), GateOptions{})
	if !withTests.TestsRelationAvailable {
		t.Error("snapshot advertising TESTS reported the relation as unavailable")
	}

	withoutTests := Gate(result, gateSnapshotFixture([]string{"CALLS"}), GateOptions{})
	if withoutTests.TestsRelationAvailable {
		t.Error("snapshot without TESTS reported the relation as available")
	}
}

// The change analysis names a nested entity by its qualified path ("fileRelationScan.relations")
// while the symbol table may carry only the bare name. Matching the bare name alone reported real
// upstream changes as not-found on this repository, so both spellings have to be tried.
func TestGateResolvesEntityNamedByQualifiedName(t *testing.T) {
	t.Parallel()

	symbols := []SymbolRecord{{
		ID: "sym-relations", Kind: "field", Name: "relations",
		QualifiedName: "fileRelationScan.relations",
		FilePath:      "provider.go", StartLine: 4490, EndLine: 4492,
	}}
	result := gateResultWithChange("provider.go", EntityChange{
		Type: "BODY_CHANGED", Kind: "field", Name: "fileRelationScan.relations",
	})

	resolution := resolveChangedSymbols(result, symbols)

	if len(resolution.Resolved) != 1 {
		t.Fatalf("expected the qualified name to resolve, got unresolved %+v", resolution.Unresolved)
	}
	if resolution.Resolved[0].Symbol.ID != "sym-relations" {
		t.Errorf("resolved to %q, want %q", resolution.Resolved[0].Symbol.ID, "sym-relations")
	}
}

// A changed test function is not something that needs tests written for it, and reporting one as
// ISOLATED buries the real findings under noise. Changes inside test files are counted and
// excluded rather than verdicted.
func TestGateExcludesChangesInsideTestFiles(t *testing.T) {
	t.Parallel()

	symbols := []SymbolRecord{{
		ID: "sym-test-fn", Kind: "function", Name: "TestSomething",
		FilePath: "provider_test.go", StartLine: 15, EndLine: 40,
	}}
	result := gateResultWithChange("provider_test.go", EntityChange{
		Type: "ADDED", Kind: "function", Name: "TestSomething",
	})

	got := Gate(result, ProviderSnapshot{
		Header:  SnapshotHeader{Profile: "full", RelationSet: []string{"CALLS", "TESTS"}},
		Symbols: symbols,
	}, GateOptions{})

	if len(got.Changed) != 0 {
		t.Errorf("a change inside a test file must not get a verdict, got %+v", got.Changed)
	}
	if got.SkippedTestFileChanges != 1 {
		t.Errorf("skipped count %d, want 1", got.SkippedTestFileChanges)
	}
}

// --- Curveball: evidence grading --------------------------------------------------------------
//
// The graph is evidence, not an oracle. A resolved CALLS/TESTS edge is structural evidence that
// the test reaches the change; a mirror filename or a name that mentions the symbol is a naming
// convention that happens to correlate. Flattening the two into one COVERED told a reader that a
// convention match was a proven call. These tests pin the three grades apart.

func TestGateGradesAResolvedEdgeAsConfirmed(t *testing.T) {
	t.Parallel()

	got := gateEvidenceFor([]gateReachedTest{gateReachedTestFixture()})

	if got != GateEvidenceConfirmed {
		t.Errorf("evidence %q, want %q (a resolved edge is structural evidence)", got, GateEvidenceConfirmed)
	}
}

func TestGateGradesAMirrorRouteAsHeuristic(t *testing.T) {
	t.Parallel()

	reached := gateReachedTestFixture()
	reached.Route = "mirror"

	got := gateEvidenceFor([]gateReachedTest{reached})

	if got != GateEvidenceHeuristic {
		t.Errorf("evidence %q, want %q (a mirror filename is a convention, not a call)", got, GateEvidenceHeuristic)
	}
}

func TestGateGradesANameRouteAsHeuristic(t *testing.T) {
	t.Parallel()

	reached := gateReachedTestFixture()
	reached.Route = "name"

	got := gateEvidenceFor([]gateReachedTest{reached})

	if got != GateEvidenceHeuristic {
		t.Errorf("evidence %q, want %q (a name match is a convention, not a call)", got, GateEvidenceHeuristic)
	}
}

// UNCOVERED and ISOLATED both rest on the absence of an edge, and absence is a fact about what the
// resolver followed, not about the repository. Grading it unverified is what turns a false
// assurance into a prompt to check the source.
func TestGateGradesAbsenceOfTestsAsUnverified(t *testing.T) {
	t.Parallel()

	got := gateEvidenceFor(nil)

	if got != GateEvidenceUnverified {
		t.Errorf("evidence %q, want %q (no tests means the claim rests on absence)", got, GateEvidenceUnverified)
	}
}

// The aggregate is the STRONGEST evidence present, because one resolved edge is enough to justify
// COVERED on its own. Grading the set by its weakest member would report a symbol as heuristic
// merely because a convention match tagged along behind a real call.
func TestGateAggregatesToTheStrongestEvidencePresent(t *testing.T) {
	t.Parallel()

	edge := gateReachedTestFixture()
	convention := gateReachedTestFixture()
	convention.Symbol.ID = "sym-test-put-mirror"
	convention.Route = "mirror"

	got := gateEvidenceFor([]gateReachedTest{convention, edge})

	if got != GateEvidenceConfirmed {
		t.Errorf("evidence %q, want %q (one resolved edge justifies the verdict)", got, GateEvidenceConfirmed)
	}
}

// gateCoveredSnapshotFixture is a snapshot where a test genuinely calls the changed symbol, so the
// COVERED path executes and its evidence can be asserted. gateSnapshotFixture carries no relations
// and therefore can only ever produce UNCOVERED.
func gateCoveredSnapshotFixture() ProviderSnapshot {
	symbols := append(gateFixtureSymbols(),
		coverSymbol("sym-test-store-put", "TestStorePut", "TestStorePut", "function", "store_test.go", 9, 20),
	)
	return ProviderSnapshot{
		Header:    SnapshotHeader{Profile: "full", RelationSet: []string{"CALLS", "TESTS"}},
		Symbols:   symbols,
		Relations: []RelationRecord{coverCall("sym-test-store-put", "sym-store-put", 0.9)},
	}
}

// The grade has to reach the reader, not just exist inside the walk. It is carried per selected
// test, so a reader can weigh one selection against another, and aggregated per changed symbol,
// so the headline verdict states what it rests on.
func TestGateCarriesEvidenceGradeIntoTheResult(t *testing.T) {
	t.Parallel()

	result := gateResultWithChange("store.go", EntityChange{
		Type: "BODY_CHANGED", Kind: "method", Name: "Store.Put", DependentsCount: 12,
	})

	got := Gate(result, gateCoveredSnapshotFixture(), GateOptions{})

	if len(got.Changed) != 1 {
		t.Fatalf("expected 1 changed symbol, got %d", len(got.Changed))
	}
	changed := got.Changed[0]
	if changed.Verdict != GateCovered {
		t.Fatalf("verdict %q, want %q — the fixture has a resolved call from a test", changed.Verdict, GateCovered)
	}
	if changed.Evidence != GateEvidenceConfirmed {
		t.Errorf("symbol evidence %q, want %q", changed.Evidence, GateEvidenceConfirmed)
	}
	if len(changed.Tests) != 1 {
		t.Fatalf("expected 1 selected test, got %d", len(changed.Tests))
	}
	if changed.Tests[0].Evidence != GateEvidenceConfirmed {
		t.Errorf("selected-test evidence %q, want %q", changed.Tests[0].Evidence, GateEvidenceConfirmed)
	}
}

// The alarm case must say what it rests on. UNCOVERED is an inference from the absence of an edge,
// and absence is a fact about what the resolver followed — so it is graded unverified, never
// presented as a confirmed finding about the repository.
func TestGateGradesAnUncoveredVerdictAsUnverified(t *testing.T) {
	t.Parallel()

	result := gateResultWithChange("store.go", EntityChange{
		Type: "BODY_CHANGED", Kind: "method", Name: "Store.Put", DependentsCount: 12,
	})

	got := Gate(result, gateSnapshotFixture([]string{"CALLS", "TESTS"}), GateOptions{})

	if len(got.Changed) != 1 {
		t.Fatalf("expected 1 changed symbol, got %d", len(got.Changed))
	}
	if got.Changed[0].Verdict != GateUncovered {
		t.Fatalf("verdict %q, want %q", got.Changed[0].Verdict, GateUncovered)
	}
	if got.Changed[0].Evidence != GateEvidenceUnverified {
		t.Errorf("evidence %q, want %q", got.Changed[0].Evidence, GateEvidenceUnverified)
	}
}

// --- Curveball: partial analysis --------------------------------------------------------------
//
// A verdict computed over an input the parser could not fully read is not a verdict about the
// repository. The snapshot header already records what the provider could not do; gate has to
// read it and say so, instead of reporting as if the input were whole.

// gatePartialSnapshotFixture is the covered fixture plus one recorded parse failure, on the file
// the change lives in.
func gatePartialSnapshotFixture(failure PartialFailure) ProviderSnapshot {
	snapshot := gateCoveredSnapshotFixture()
	snapshot.Files = []FileRecord{{Path: "store.go", Language: "Go"}}
	snapshot.Header.PartialFailures = []PartialFailure{failure}
	return snapshot
}

func TestGateMarksResultPartialWhenAChangedFileFailedToParse(t *testing.T) {
	t.Parallel()

	result := gateResultWithChange("store.go", EntityChange{
		Type: "BODY_CHANGED", Kind: "method", Name: "Store.Put", DependentsCount: 12,
	})
	snapshot := gatePartialSnapshotFixture(PartialFailure{
		Code: "E_PARSE_FAILED", Severity: "warn", FilePath: "store.go",
		EffectOnCompleteness: "relations from this file are missing",
	})

	got := Gate(result, snapshot, GateOptions{})

	if !got.Partial {
		t.Fatal("a parse failure on a changed file must mark the result partial")
	}
	if len(got.AnalysisGaps) != 1 {
		t.Fatalf("expected the reason to be named, got %d gaps", len(got.AnalysisGaps))
	}
	if got.AnalysisGaps[0].FilePath != "store.go" || got.AnalysisGaps[0].Code != "E_PARSE_FAILED" {
		t.Errorf("gap %+v, want the E_PARSE_FAILED on store.go", got.AnalysisGaps[0])
	}
}

// Scoping matters. A parse failure in a file nothing changed does not make THIS change set's
// verdicts partial, and marking it so would widen every command on every run until the flag meant
// nothing.
func TestGateIgnoresAParseFailureOutsideTheChangeSet(t *testing.T) {
	t.Parallel()

	result := gateResultWithChange("store.go", EntityChange{
		Type: "BODY_CHANGED", Kind: "method", Name: "Store.Put", DependentsCount: 12,
	})
	snapshot := gatePartialSnapshotFixture(PartialFailure{
		Code: "E_PARSE_FAILED", Severity: "warn", FilePath: "unrelated/other.go",
	})

	got := Gate(result, snapshot, GateOptions{})

	if got.Partial {
		t.Errorf("a failure outside the change set must not mark the result partial, gaps %+v", got.AnalysisGaps)
	}
}

// An inventory-only language has file and symbol records but no relations at all, so every
// reachability answer about it is an artifact of the tier rather than a finding.
func TestGateMarksResultPartialWhenAChangedFileIsInventoryOnly(t *testing.T) {
	t.Parallel()

	result := gateResultWithChange("store.go", EntityChange{
		Type: "BODY_CHANGED", Kind: "method", Name: "Store.Put", DependentsCount: 12,
	})
	snapshot := gateCoveredSnapshotFixture()
	snapshot.Files = []FileRecord{{Path: "store.go", Language: "Ruby"}}
	snapshot.Header.LanguageTiers = map[string]string{"Ruby": "inventory-only"}

	got := Gate(result, snapshot, GateOptions{})

	if !got.Partial {
		t.Fatal("a changed file in an inventory-only language must mark the result partial")
	}
	if len(got.AnalysisGaps) != 1 || got.AnalysisGaps[0].FilePath != "store.go" {
		t.Fatalf("expected one gap naming store.go, got %+v", got.AnalysisGaps)
	}
	if got.AnalysisGaps[0].Code != GateGapInventoryOnly {
		t.Errorf("gap code %q, want %q", got.AnalysisGaps[0].Code, GateGapInventoryOnly)
	}
}

func TestGateIsNotPartialWhenAnalysisIsComplete(t *testing.T) {
	t.Parallel()

	result := gateResultWithChange("store.go", EntityChange{
		Type: "BODY_CHANGED", Kind: "method", Name: "Store.Put", DependentsCount: 12,
	})

	got := Gate(result, gateCoveredSnapshotFixture(), GateOptions{})

	if got.Partial {
		t.Errorf("a complete snapshot must not be marked partial, gaps %+v", got.AnalysisGaps)
	}
	if len(got.AnalysisGaps) != 0 {
		t.Errorf("expected no gaps, got %+v", got.AnalysisGaps)
	}
}

// --- Curveball point 5: a repository whose analysis is genuinely incomplete --------------------
//
// Every fixture above is a hand-built snapshot. This one is a real repository, parsed by the real
// provider, and it is incomplete for a real reason rather than a stipulated one.
//
// The shape is the dangerous one. store_test.go holds TestStorePut, which calls Store.Put, so the
// change IS covered. But the file has a syntax error, and the consequence is asymmetric: the
// provider still records TestStorePut as a symbol (inventory survives) while the CALLS edge from
// it to Store.Put is lost. What remains is the mirror-filename convention — the test happens to
// live in store_test.go next to store.go.
//
// So before the revision this rendered as a plain COVERED backed by a narrow -run command, and the
// structural evidence behind it had actually been destroyed by a parse error the output never
// mentioned. That is precisely "presenting incomplete Graph relationships as certain".
func gateIncompleteAnalysisRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	writeFile(t, repo, "store.go", `package store

type Store struct{}

func (s *Store) Put(k string) error { return nil }

func Save(s *Store, k string) error { return s.Put(k) }
`)
	// Deliberately unparseable. The test it holds genuinely exercises Store.Put.
	writeFile(t, repo, "store_test.go", `package store

func TestStorePut(t *testing.T) {
	s := &Store{}
	if err := s.Put("k"); err != nil {
	func( {{{
`)
	return repo
}

func gateIncompleteAnalysisChanges() Result {
	return Result{
		Base: "HEAD~1", Head: "HEAD",
		Files: []FileChange{
			{Path: "store.go", Status: "M", Changes: []EntityChange{
				{Type: "BODY_CHANGED", Kind: "method", Name: "Put", DependentsCount: 1},
			}},
			{Path: "store_test.go", Status: "M", Changes: []EntityChange{
				{Type: "BODY_CHANGED", Kind: "function", Name: "TestStorePut"},
			}},
		},
	}
}

func TestGateMarksARealUnparseableRepositoryAsPartial(t *testing.T) {
	t.Parallel()

	snapshot, err := BuildProviderSnapshot(t.Context(), gateIncompleteAnalysisRepo(t), "test-version")
	if err != nil {
		t.Fatal(err)
	}

	got := Gate(gateIncompleteAnalysisChanges(), snapshot, GateOptions{})

	if !got.Partial {
		t.Fatalf("a repository with a parse error on a changed file must be partial, gaps %+v", got.AnalysisGaps)
	}
	found := false
	for _, gap := range got.AnalysisGaps {
		if gap.FilePath == "store_test.go" && gap.Code == "E_PARSE_ERROR" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the parse error on store_test.go to be named, got %+v", got.AnalysisGaps)
	}
}

// The verdict must not be presented as certain. Whatever it says, the grade has to record that the
// structural evidence is not there — the surviving attribution is a filename convention.
func TestGateDoesNotPresentAnIncompleteVerdictAsConfirmed(t *testing.T) {
	t.Parallel()

	snapshot, err := BuildProviderSnapshot(t.Context(), gateIncompleteAnalysisRepo(t), "test-version")
	if err != nil {
		t.Fatal(err)
	}

	got := Gate(gateIncompleteAnalysisChanges(), snapshot, GateOptions{})

	if len(got.Changed) != 1 {
		t.Fatalf("expected the one non-test change to be verdicted, got %+v", got.Changed)
	}
	changed := got.Changed[0]
	if changed.Evidence == GateEvidenceConfirmed {
		t.Errorf("verdict %q was graded %q, but the CALLS edge from the test was lost to the parse error",
			changed.Verdict, changed.Evidence)
	}
	for _, test := range changed.Tests {
		if test.Evidence == GateEvidenceConfirmed {
			t.Errorf("selected test %q graded confirmed via route %q, but no resolved edge survived",
				test.Name, test.Route)
		}
	}
}

// --- Curveball point 4: fully resolved code behaves exactly as before --------------------------

// The revision is only safe if it is additive. On complete analysis with a resolved edge, every
// field that existed before the curveball must be identical to what it was: same verdict, same
// selected test, same route, same depth, same chain. Only the new Evidence field is added, and
// Partial stays false so no command widens.
//
// This is asserted rather than assumed, because "additive" is exactly the kind of claim that
// quietly stops being true.
func TestGateFullyResolvedResultIsUnchangedByTheRevision(t *testing.T) {
	t.Parallel()

	result := gateResultWithChange("store.go", EntityChange{
		Type: "BODY_CHANGED", Kind: "method", Name: "Store.Put", DependentsCount: 12,
	})

	got := Gate(result, gateCoveredSnapshotFixture(), GateOptions{})

	if got.Partial {
		t.Errorf("complete analysis must not be partial, gaps %+v", got.AnalysisGaps)
	}
	if len(got.Changed) != 1 {
		t.Fatalf("expected 1 changed symbol, got %d", len(got.Changed))
	}

	want := GateChangedSymbol{
		Name: "Store.Put", Kind: "method", FilePath: "store.go", StartLine: 88,
		ChangeType: "BODY_CHANGED", Dependents: 12, Verdict: GateCovered,
		Tests: []GateSelectedTest{{
			SymbolID: "sym-test-store-put", Name: "TestStorePut", FilePath: "store_test.go",
			StartLine: 9, Route: "edge", Depth: 1, Chain: []string{"TestStorePut"},
			Evidence: GateEvidenceConfirmed,
		}},
		Evidence: GateEvidenceConfirmed,
	}
	if !reflect.DeepEqual(got.Changed[0], want) {
		t.Errorf("changed symbol\n got %+v\nwant %+v", got.Changed[0], want)
	}
}
