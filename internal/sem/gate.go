package sem

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
}

// GateUnresolved is a changed entity that could not be tied to exactly one symbol.
type GateUnresolved struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
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
	TestsRelationAvailable bool                `json:"tests_relation_available"`
	Changed                []GateChangedSymbol `json:"changed"`
	Unresolved             []GateUnresolved    `json:"unresolved,omitempty"`
	Warnings               []ProviderWarning   `json:"warnings,omitempty"`
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
	byPathAndName := map[string][]SymbolRecord{}
	for _, symbol := range symbols {
		key := symbol.FilePath + "\x00" + symbol.Name
		byPathAndName[key] = append(byPathAndName[key], symbol)
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
