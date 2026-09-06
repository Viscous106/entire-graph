package cli

import (
	"github.com/entireio/entire-graph/internal/sem"
)

// Reasons a changed entity could not be tied to a symbol in the graph.
//
// Both are reported rather than dropped. A change the graph cannot place is
// exactly the case where a coverage claim would be silently wrong, so it has to
// stay visible in the output.
const (
	gateUnresolvedNotFound  = "not-found"
	gateUnresolvedAmbiguous = "ambiguous"
)

// changedSymbol is one entity change tied to the symbol it names.
type changedSymbol struct {
	Change sem.EntityChange
	Path   string
	Symbol sem.SymbolRecord
}

// unresolvedChange is one entity change that could not be tied to exactly one symbol.
type unresolvedChange struct {
	Change sem.EntityChange
	Path   string
	Reason string
}

type gateResolution struct {
	Resolved   []changedSymbol
	Unresolved []unresolvedChange
}

// resolveChangedSymbols maps the entity changes in a diff result onto snapshot symbols.
//
// The diff speaks entity names; the graph speaks symbol IDs. Matching on name alone is
// wrong: the same name routinely appears in several files, and overloads repeat it inside
// one file (upstream issue #34). So a candidate must match on BOTH the name and the file
// the change was found in, and a name that still matches more than once inside that file
// is reported as ambiguous instead of being resolved arbitrarily.
func resolveChangedSymbols(result sem.Result, symbols []sem.SymbolRecord) gateResolution {
	byPathAndName := map[string][]sem.SymbolRecord{}
	for _, symbol := range symbols {
		key := symbol.FilePath + "\x00" + symbol.Name
		byPathAndName[key] = append(byPathAndName[key], symbol)
	}

	resolution := gateResolution{}
	for _, file := range result.Files {
		for _, change := range file.Changes {
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
					Reason: gateUnresolvedNotFound,
				})
			default:
				resolution.Unresolved = append(resolution.Unresolved, unresolvedChange{
					Change: change,
					Path:   file.Path,
					Reason: gateUnresolvedAmbiguous,
				})
			}
		}
	}
	return resolution
}
