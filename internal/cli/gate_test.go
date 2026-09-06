package cli

import (
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// gateFixtureSymbols is the symbol table the L3 resolution tests resolve against.
// Store.Put appears twice, in two files, on purpose: that collision is the whole
// reason resolution is scoped by path rather than by name (upstream issue #34,
// "compound-v1 symbol IDs are unstable for same-name/overloaded symbols").
func gateFixtureSymbols() []sem.SymbolRecord {
	return []sem.SymbolRecord{
		{ID: "sym-store-put", Kind: "method", Name: "Store.Put", FilePath: "store.go", StartLine: 88, EndLine: 96},
		{ID: "sym-cache-put", Kind: "method", Name: "Store.Put", FilePath: "cache/store.go", StartLine: 12, EndLine: 20},
		{ID: "sym-parse-opts", Kind: "function", Name: "parseOpts", FilePath: "opts.go", StartLine: 12, EndLine: 30},
	}
}

func gateResultWithChange(path string, change sem.EntityChange) sem.Result {
	return sem.Result{
		Base: "main",
		Head: "HEAD",
		Files: []sem.FileChange{
			{Path: path, Status: "M", Changes: []sem.EntityChange{change}},
		},
	}
}

func TestGateResolvesChangedEntityToSymbolInSameFile(t *testing.T) {
	result := gateResultWithChange("store.go", sem.EntityChange{
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
	result := gateResultWithChange("store.go", sem.EntityChange{
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
	if got.Reason != gateUnresolvedNotFound {
		t.Errorf("reason %q, want %q", got.Reason, gateUnresolvedNotFound)
	}
}

func TestGateReportsAmbiguousChangeExplicitly(t *testing.T) {
	symbols := append(gateFixtureSymbols(), sem.SymbolRecord{
		ID: "sym-store-put-overload", Kind: "method", Name: "Store.Put",
		FilePath: "store.go", StartLine: 120, EndLine: 130,
	})
	result := gateResultWithChange("store.go", sem.EntityChange{
		Type: "SIGNATURE_CHANGED", Kind: "method", Name: "Store.Put",
	})

	resolution := resolveChangedSymbols(result, symbols)

	if len(resolution.Resolved) != 0 {
		t.Fatalf("an ambiguous change must not resolve, got %+v", resolution.Resolved)
	}
	if len(resolution.Unresolved) != 1 {
		t.Fatalf("expected 1 unresolved change, got %d", len(resolution.Unresolved))
	}
	if got := resolution.Unresolved[0].Reason; got != gateUnresolvedAmbiguous {
		t.Errorf("reason %q, want %q", got, gateUnresolvedAmbiguous)
	}
}
