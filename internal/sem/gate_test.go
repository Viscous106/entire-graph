package sem

import "testing"

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
