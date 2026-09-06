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
