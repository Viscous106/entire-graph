# Checkpoint — Noon Curveball adaptation

**Track E2 · `entire graph gate` · 2026-09-06, post-noon session.**

## What the curveball invalidated

`gate` assumed **the absence of an edge is a fact about the code**. It is a fact about what static
analysis resolved. That assumption was load-bearing in two opposite directions: `UNCOVERED`
asserted absence as a finding about the repository, and `COVERED` flattened three unequal evidence
classes — a resolved `CALLS`/`TESTS` edge, a mirror test filename, a name that mentions the symbol
— into one verdict. `GateSelectedTest.Route` recorded which route fired, but `GateVerdict` did not,
so a convention match and a resolver-confirmed call rendered identically.

Full statement in `docs/buildathon/CURVEBALL.md`, written before any code changed.

## How the adaptation was sequenced

1. Reconstructed from `docs/buildathon/HANDOVER.md`, `CURVEBALL.md` and checkpoint `be08c1b`.
2. Cleared the blocker: the teammate's L4/L5 reachability (`9c2ceb5`) was on the remote but not in
   local history, so every verdict was `UNCOVERED` and the `COVERED` path had never executed.
3. **Ran `entire graph impact` over `gateVerdictFor`, `gateReach` and `Gate`, and committed it
   (`3ce7209`) before writing any implementation code.** Captured in
   `docs/buildathon/curveball-impact.txt`.
4. Implemented the five-point revision, strict TDD.

The impact analysis was not ceremony — it decided the design:

- **It answered the ownership question.** `gateReach`'s callees include `gateReachRoute`, which
  already emits `edge`/`mirror`/`name`, and its type consumers show it `RETURNS_TYPE
  gateReachedTest`, a type declared on our side of the seam. The evidence classes were already
  being produced and already crossing the seam, so grading is a pure function of data `gate.go`
  already receives. **`internal/sem/gate_reach.go` was never touched** — no teammate coordination
  was needed, which the analysis established before the first edit rather than after a conflict.
- **It sized the regression surface.** `gateVerdictFor` has 5 direct callers and 5 transitive: 8 of
  them tests. That made "additive, not a rewrite" a measured constraint rather than an intention,
  and `gateVerdictFor` was in the end not modified at all.
- **It found the precedent for partiality.** `Gate` already called `gateRelationAdvertised` to read
  `snapshot.Header.RelationSet`. Reading `PartialFailures` and `LanguageTiers` extends that pattern
  instead of inventing one.

## The five points, as shipped

| # | Point | Where |
| --- | --- | --- |
| 1 | Grade every claim `confirmed` / `heuristic` / `unverified`, per test and per symbol, in text and JSON | `gateEvidenceFor`, `gateEvidenceForRoute`, `GateEvidence` (`internal/sem/gate.go`) |
| 2 | Detect partial analysis from `PartialFailures`, `Warnings`, inventory-only tiers, scoped to changed files | `gateAnalysisGaps`, `GateResult.Partial`, `AnalysisGaps` |
| 3 | Widen the command to the changed packages, and say why | `gateVerifyCommand`, `gateWidenReason`, `gateWidenedCommand` (`internal/cli/gate.go`) |
| 4 | Fully resolved code behaves exactly as before | `TestGateFullyResolvedResultIsUnchangedByTheRevision`, `TestGateVerifyCommandIsUnchangedForFullyResolvedCode` |
| 5 | Fixture representing incomplete analysis | `gateIncompleteAnalysisRepo` + 2 tests (`internal/sem/gate_test.go`) |

## What the fixture actually demonstrates

Two assumptions were checked against the provider before the fixture was written, and one was
wrong:

- A syntactically broken Go file **does** emit `E_PARSE_ERROR` carrying its `FilePath`.
- Interface dispatch is **not** a missing edge in this provider — it is *over*-approximated
  (`Save -> Putter.Put` **and** `Save -> Store.Put`). A fixture claiming interface dispatch hides
  the call would have asserted something false, so it was not written that way.

The fixture uses the real incompleteness instead, and the failure it captures is asymmetric: in a
test file with a syntax error, the provider still records `TestStorePut` as a symbol while the
`CALLS TestStorePut -> Store.Put` edge is lost. The surviving attribution is the mirror filename.
Before the revision that rendered as a plain `COVERED` behind a narrow `-run` command — verified by
disabling the new code and watching the fixture print `verdict "COVERED" was graded "confirmed"`.

## Evidence it works on real code

`gate --repo . --base 4afea0a --head 4172fb3`, five changed symbols that were five identical
`COVERED`s before:

```
COVERED   fileRelationScan              provider.go:4486               [heuristic]   (24 tests, all mirror)
COVERED   runIndexedStreamingPipeline   provider_parallel_stream.go:14 [confirmed]   (2 tests, both edge)
COVERED   slot                          provider_parallel_stream.go:31 [heuristic]
COVERED   values                        provider_parallel_stream.go:32 [heuristic]
COVERED   result                        provider_parallel_stream.go:33 [heuristic]

VERIFY: go test ./internal/sem/
  widened: the only evidence for fileRelationScan is a naming convention, not a resolved call,
           so a narrow selection asserts more than the graph found
```

One symbol has structural evidence; four rest on filename and identifier conventions. Among
`values`'s selections was `TestParseDiffFlagsRequiresRevisionValues` in `internal/cli/root_test.go`
— admitted because its name contains "values". The tool now says so instead of implying a call.

## Verification

`gofmt -l -s .`, `go vet ./...`, `go build ./...` clean. 45 `TestGate*`/`TestReach*` in
`internal/sem`, 14 `TestGate*` in `internal/cli`. Every new test was watched failing on its
assertion before implementation; the two fixture tests, which passed on first run because the
implementation preceded them, were re-verified by disabling the implementation and confirming both
fail with the expected messages.

## Known limitation, stated rather than hidden

Partiality is scoped to files the change set touches, per point 2. Reachability walks *inbound*
edges, so a parse failure in an unchanged file elsewhere can still hide a caller. That case is not
marked `Partial`, but it is not silent either: the verdict still carries `unverified` or
`heuristic`, and heuristic-only evidence widens the command on its own. The tool degrades toward
running more tests, never fewer.
