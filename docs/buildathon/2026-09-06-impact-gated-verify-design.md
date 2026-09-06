# `entire graph gate` — implementation record

**BTW Buildathon 2026 · Track E2 Graph Intelligence · 2026-09-06**
Team: Abhinav Singh (OfficialAbhinavSingh) · Yash Virulkar (Viscous106)
Fork: `Viscous106/entire-graph` · mirror `entire://aws-ap-south-1.entire.io/gh/viscous106/entire-graph`

> **`PIPELINE.md` is the canonical plan** — product framing, layer map, build order, rubric
> mapping, ownership, risks and submission checklist all live there. This file is the
> subordinate engineering record: what has actually been built, what was verified against
> source, and what is still open. Where the two disagree, `PIPELINE.md` wins.

## 1. Verification of `PIPELINE.md` claims

Every reuse point in §6 and the placement correction in §5 was re-checked against the working
tree at `3a2a715`. All accurate:

| Claim | Result |
| --- | --- |
| `searchTestNameShaped` unexported, `search_covertest.go:587` | confirmed |
| `searchTestArtifactPath` unexported, `search.go:4873` | confirmed |
| only renderers exported: `RenderSearchCoverageNote:387`, `RenderSearchVerifyCommand:1274` | confirmed |
| `AnalyzeGitRange:27`, `AnalyzeGitRangeWithOptions:72`, `AnalyzeCheckpoint:1159` | confirmed |
| `buildSearchVerifyCommand:205` | confirmed |
| covering test is one-hop: `if relation.ToID != anchor.ID { continue }` | confirmed, `search_covertest.go:456` |

The one-hop confirmation is the load-bearing one: it is what makes L4's transitive walk a
real addition to Graph rather than a repackaging of `search`.

## 2. Built so far — L3 (resolution)

Change set entities carry names; the graph carries symbol IDs. L3 bridges them.

**Rule.** A candidate must match on **both** the entity name and the file the change was found
in. Name alone is wrong: the same name recurs across files, and overloads repeat it inside one
file — upstream issue **#34** ("compound-v1 symbol IDs are unstable for same-name/overloaded
symbols"). A name still matching more than once inside its own file is reported `ambiguous`
rather than resolved arbitrarily.

**Report, never drop.** Unplaceable changes surface as `not-found`, ambiguous ones as
`ambiguous`. A change the graph cannot place is precisely where a coverage claim would be
silently wrong, so it stays visible. This is the same principle as the `UNCOVERED` alarm: the
product exists to remove silence, so it must not introduce its own.

**Tests** — written first, each watched fail before implementation existed:

| Test | Proves |
| --- | --- |
| `TestGateResolvesChangedEntityToSymbolInSameFile` | `Store.Put` present in two files resolves to the one in the changed file |
| `TestGateReportsUnresolvedChangeExplicitly` | an unplaceable name is reported, not skipped |
| `TestGateReportsAmbiguousChangeExplicitly` | two same-named symbols in one file do not resolve to a guess |

State: 3/3 pass, `gofmt` clean, `go vet` clean.

### Open: L3 is in the wrong package and the wrong lane

It currently sits in `internal/cli/gate.go`. Per `PIPELINE.md` §5 the engine (L3–L6) belongs in
a new `internal/sem/gate.go`, and per §12 those layers are **Abhinav's**. L3 itself does not
need the unexported helpers — only L5 does — but splitting one pure pipeline across two
packages is worse than moving it.

**Resolution required before either of us continues on the engine:** move this logic and its
three tests into `internal/sem/gate.go` (`package cli` → `package sem`), or hand them over as
a starting point. Blocked on the `sem.Gate(...)` signature agreement — `PIPELINE.md` §13.

The types below are a concrete proposal for part of that signature:

```go
const (
    gateUnresolvedNotFound  = "not-found"
    gateUnresolvedAmbiguous = "ambiguous"
)

type changedSymbol    struct { Change sem.EntityChange; Path string; Symbol sem.SymbolRecord }
type unresolvedChange struct { Change sem.EntityChange; Path string; Reason string }
type gateResolution   struct { Resolved []changedSymbol; Unresolved []unresolvedChange }

func resolveChangedSymbols(result sem.Result, symbols []sem.SymbolRecord) gateResolution
```

## 3. Environment findings

- **Pre-existing test failure.** `TestDoctorWorksOutsideGitRepo` fails on this machine on a
  pristine tree with our files deleted: outside a git repo `doctor` reports
  `repo_root=<temp dir>` where the test expects `<unset>`. Not caused by our work. It will
  appear in any suite run a judge performs on a comparable machine, so it belongs in
  `BUILDATHON.md` limitations.
- **`.claude/settings.json`.** `PIPELINE.md` §17 records `entire enable` deleting its
  `permissions.deny` block, listed as the only uncommitted change. **Not reproduced in this
  clone** — the file is tracked and `git status` is clean. The re-clone from the Entire mirror
  appears to have reset it. §13's first open item may already be moot here; confirm on the
  other clone before acting.
- **Token store.** `PIPELINE.md` §17 puts the file-backend token at
  `~/.config/entire/tokens.json` via `~/.zshrc`. A second, repo-local configuration also exists
  at `BTW/.entire/env.sh` pointing at `BTW/.entire/config/tokens.json`. Both work; they are
  different login contexts. Pick one before the demo so the shell on stage is not the one
  without a token.
- Build from source on this machine: `go build ./cmd/entire-graph` succeeds, 61s cold, CGO and
  tree-sitter fine on NixOS.

## 4. Next

L4 (transitive inbound `CALLS` walk, depth ≤ 2, fan-out capped with truncation reported), then
L5, L6. Ordering, ownership and the afternoon items (`--run`, `--checkpoint`, UNDECLARED REACH)
are as scheduled in `PIPELINE.md` §7.
