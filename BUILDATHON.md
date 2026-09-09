# entire graph gate

## One-sentence summary

`entire graph gate` turns a change set into a verified test plan: it resolves each changed entity to a graph symbol, walks inbound `CALLS` edges transitively to find which tests reach it, emits the narrowest command for that set in the repository's own test runner (`go test -run`, `python -m pytest -k`), grades how good the evidence behind each answer actually is, and raises `UNCOVERED` for changed symbols that other code depends on but that no test reaches.

**Team:** Abhinav Singh (OfficialAbhinavSingh), Yash Virulkar (Viscous106)
**Fork:** https://github.com/Viscous106/entire-graph
**Entire mirror:** `entire://aws-ap-south-1.entire.io/gh/viscous106/entire-graph`
**Track:** E2 — Graph Intelligence

## Problem, intended user and why it matters

You changed a function. The repository has hundreds of tests. Two questions cannot be answered from a diff:

1. **Which tests can actually break?** Today you guess, or you run everything.
2. **Which changed code is reachable by no test at all?** Today: silence.

The second is the dangerous one. An agent makes a small-looking edit; the blast radius includes symbols no test touches; CI is green; nothing was ever verified. A line diff cannot show this, and a repo-wide coverage report cannot either, because it is not scoped to the change.

**Intended user:** a developer or a coding agent about to commit a change.

The repository already contains the adjacent halves and no join between them. `impact` answers what a change affects. `verify` runs a test command the caller supplies. Nothing anchors a test selection to an entity-level change set (`internal/sem/gate.go:5-18`).

## Selected Entire track and why Entire is essential

Track **E2 — Graph Intelligence**. Three Entire capabilities are load-bearing; remove any one and the command cannot exist.

| Capability | Role | Where used |
| --- | --- | --- |
| `sem.AnalyzeGitRange` / `sem.AnalyzeCheckpoint` | entity-level change set, not line diffs | `internal/cli/gate.go:131`, `internal/cli/gate.go:142` |
| Graph snapshot: symbols + `CALLS` relations | transitive reachability — the only source for it | `internal/cli/gate.go:148`, `internal/sem/gate.go:268-290` |
| `runVerifyCommands` adjudication | verdict as a delta (newly-failing vs pre-existing), not raw output | `internal/cli/verify.go:157`, `internal/cli/verify.go:302` |

Without the graph there is no reachability. Without entity-level change analysis we are matching text against a diff. Without adjudication, running the emitted command is just a shell invocation.

### Prior art inside this repository, and our delta

We checked what Graph already does before building, because the honest question is whether this is new.

`search` already emits a COVERING TEST block (`internal/sem/search_covertest.go`, 886 lines). It is real and it works, with four properties that bound it:

- **One-hop only.** `internal/sem/search_covertest.go:456` — `if relation.ToID != anchor.ID { continue }`. A test that reaches the change through one intermediate call is invisible to it.
- **One test.** `internal/sem/search_covertest.go:48` — `searchCoveringTestLimit = 1`.
- **Silent on absence.** `internal/sem/search_covertest.go:41-43` — "when no candidate clears the bar the section is simply absent." Absence is not a warning, and is indistinguishable from not having asked.
- **Single-test command.** The Go emitter names one test: `internal/sem/search_verify.go:919` builds `" -run " + shellQuote("^"+testName+"$")`. No alternation exists anywhere in the repository.

Our delta, precisely: **change-anchored** instead of query-anchored; **transitive** instead of one-hop; **a set** instead of one; and **loud about gaps** instead of silent. The alternation emitter is `gateGoTestCommand` (`internal/cli/gate.go:178-193`).

## Architecture and main workflow

Nine layers. Four are new, five are reused.

```
L0  CLI            internal/cli/gate.go:112     flags, resolveRepo, validateRevision   REUSED shape
L1  Change source  internal/cli/gate.go:129-143 AnalyzeGitRange | AnalyzeCheckpoint    REUSED
L2  Graph index    internal/cli/gate.go:148     LoadOrBuildProviderSnapshot, built once REUSED
L3  Resolution     internal/sem/gate.go:187     EntityChange -> SymbolRecord           NEW
L4  Reachability   internal/sem/gate_reach.go   transitive inbound CALLS walk, depth<=2 NEW
L5  Attribution    reached symbols -> tests     searchTestArtifactPath / NameShaped    REUSED, re-aimed
L6  Verdict        internal/sem/gate.go:246     COVERED / UNCOVERED / ISOLATED         NEW
L7  Command        internal/cli/gate.go:178     union of tests -> -run '^(A|B)$'       NEW
L8  Adjudication   internal/cli/verify.go:157   runVerifyCommands + renderVerifyVerdict REUSED
L9  Render         internal/cli/gate.go:204     text and JSON, deterministic ordering
```

Verdict semantics (`internal/sem/gate.go:31-40`):

| Verdict | Meaning | Action |
| --- | --- | --- |
| `COVERED` | at least one test reaches this changed symbol within the depth bound | run the selected tests |
| `UNCOVERED` | the symbol has dependents, but no path from any test | write a test, or accept the risk knowingly |
| `ISOLATED` | no inbound call edges at all | dead code, an entry point, or an external-caller API |

`UNCOVERED` is the product. Everything else is table stakes.

**Determinism.** L3 through L6 is a pure function of (change set, snapshot): no I/O, no git, no clock, no network (`internal/sem/gate.go:261-264`). Output ordering is sorted explicitly so map iteration never reaches the output (`internal/sem/gate.go:317-331`). This is what `docs/brain-and-graph-boundaries.md` requires of anything on the provider side.

**No regression surface.** No existing file in `internal/sem` was modified. The engine is a new file, `internal/sem/gate.go`, which is also what puts the unexported helpers `searchTestNameShaped` (`internal/sem/search_covertest.go:587`) and `searchTestArtifactPath` (`internal/sem/search.go:4873`) in scope — they are not reachable from `internal/cli`. `internal/cli/gate.go` is a thin flag-and-render shell, wired at `internal/cli/root.go:104` and described at `internal/cli/help.go:360`.

### Key design decision: resolution keys on (file path, name)

The diff speaks entity names; the graph speaks symbol IDs. Matching on name alone is wrong. The same name recurs across files, and overloads repeat it inside one file — upstream issue **#34** records that compound-v1 symbol IDs are unstable for same-name and overloaded symbols. So a candidate must match on **both** the name and the file the change was found in, and a name that still matches more than once inside that file is reported `ambiguous` rather than resolved arbitrarily (`internal/sem/gate.go:175-229`).

**Report, never drop.** Three reasons are first-class: `not-found`, `ambiguous`, `module-scope` (`internal/sem/gate.go:47-51`). A change the graph cannot place is precisely where a coverage claim would be silently wrong, so it stays visible in the output. This is the same principle as the `UNCOVERED` alarm: a tool built to remove silence must not introduce its own.

Verified against real upstream commits: `forEachRelation` in `internal/sem/provider.go` came back genuinely ambiguous — the rule fires on real code, not only on fixtures.

## What the graph is used for, end to end

The product consumes exactly two graph facts, and there is no second source for either.

**Fact one — what changed, as symbols.** `sem.AnalyzeGitRange` reports `runIndexedStreamingPipeline`
was added, not "lines 14-30 of provider_parallel_stream.go changed". A line diff cannot be joined to
a call graph; an entity change can.

**Fact two — who reaches those symbols.** The inbound `CALLS`/`TESTS` edges. Run the graph directly
and this is the raw input:

```
$ entire graph neighbors --repo . --symbol runIndexedStreamingPipeline --relation CALLS --direction in
forEachRelation                                            internal/sem/provider.go
TestIndexedStreamingPipelineBoundsAndOrdersLargeOutput     internal/sem/provider_parallel_stream_test.go
TestIndexedStreamingPipelineStopsAndJoinsBlockedProducers  internal/sem/provider_parallel_stream_test.go
```

That edge list is the whole input. Gate turns it into a decision:

```
COVERED  runIndexedStreamingPipeline  provider_parallel_stream.go:14  [confirmed]
  selected: TestIndexedStreamingPipelineBoundsAndOrdersLargeOutput  (edge -> confirmed, depth 1)
  selected: TestIndexedStreamingPipelineStopsAndJoinsBlockedProducers  (edge -> confirmed, depth 1)
```

Every element traces back to a graph fact:

| Output | The graph fact behind it |
| --- | --- |
| the symbol is in scope at all | `AnalyzeGitRange` reported it changed in `4afea0a..4172fb3` |
| `COVERED` | a test appears in the inbound `CALLS` list |
| *which* tests | those two names, from the edge list |
| `[confirmed]` | the admitting route was a resolved edge, not a filename convention |
| `go test -run '^(Test…\|Test…)$'` | composed from those names |

Remove the graph and the feature does not degrade — it cannot exist. There is no other way to know
which tests reach a changed symbol.

**What gate adds on top of the edges.** The track requires more than raw graph output, and three
things separate a decision from a dump:

1. **Change-anchored.** `neighbors` requires you to already know the symbol to ask about. Gate
   starts from `--base/--head` and finds the symbols itself.
2. **Transitive and set-level.** The existing covering-test finder stops at one hop
   (`search_covertest.go:456`) and names one test (`searchCoveringTestLimit = 1`). Gate walks two
   hops and returns every test for every changed symbol as one runnable command.
3. **It reports absence.** When the inbound list holds callers but no tests, the existing tooling
   prints nothing, and silence is indistinguishable from never having asked. Gate says:

```
UNCOVERED  result  provider_parallel_stream.go:33  [unverified]
  ! 581 dependents and no test reaches this (no path the graph can see)
```

581 symbols depend on that field and no test reaches it. The fact was in the graph the whole time;
nothing surfaced it.

## Verified on three repositories, three languages

Gate reads a graph, so it is not specific to this repository or to Go. Run against unrelated
projects on the same machine:

| Repository | Language | Verdicts | Evidence | Command emitted |
| --- | --- | --- | --- | --- |
| this fork | Go | 5 changed | 1 `confirmed`, 4 `heuristic` | `go test ./internal/sem/` (widened) |
| `scaler-content` | Python | 53 changed: 41 `COVERED`, 11 `UNCOVERED`, 1 `ISOLATED` | 2 `confirmed`, 39 `heuristic`, 12 `unverified` | `python -m pytest src/scaler_listen/ …` (widened) |
| `githubStats` | JavaScript | 12 changed: 11 `UNCOVERED`, 1 `ISOLATED` | 12 `unverified` | none — no jest/vitest emitter in v1 |

The Python run is the strongest evidence that the Curveball revision was necessary. Its selected
tests break down by attribution route as:

```
mirror: 613     name: 15     edge: 2
```

**628 convention matches against 2 resolved edges.** Before grading landed, all 41 `COVERED`
verdicts rendered identically and every one looked like structural proof. Now the two that are
structural say `confirmed`, the rest say `heuristic`, and the emitted command widens because of it.

The JavaScript run found a real defect in our own attribution, since fixed. `faker`, a fixture
helper in `tests/pat-info.test.js`, was selected as the covering "test" for a changed handler,
because attribution admits any symbol on a test-artifact path. A convention route must now satisfy
the name convention as well, so such a symbol is counted as not reportable rather than presented as
evidence. `pat-info` correctly reads `ISOLATED [unverified]`, and the same fix removed **255**
falsely-reported helper selections on the Python repository — measured, not estimated.

Both defects were found by running the product on real repositories rather than on its own
fixtures, which is why the cross-language runs are part of the submission and not a footnote.

## Entire Graph findings and verification

Findings from using Graph on this repository during the build:

- **Snapshot scale.** 723 files, 8,242 symbols, 10,069 `CALLS` edges.
- **The one-hop limit is real and load-bearing.** Confirmed at `internal/sem/search_covertest.go:456`. This is what makes L4's transitive walk an addition to Graph rather than a repackaging of `search`.
- **L5 is not reachable from `internal/cli`.** `searchTestNameShaped` (`internal/sem/search_covertest.go:587`) and `searchTestArtifactPath` (`internal/sem/search.go:4873`) are unexported; only renderers are exported (`RenderSearchCoverageNote` at `internal/sem/search_covertest.go:387`, `RenderSearchVerifyCommand` at `internal/sem/search_verify.go:1274`). This finding, from a `graph search` before writing any code, is why the engine lives in `internal/sem` rather than `internal/cli`.
- **`TESTS` edges exist only at profile `full`.** So `gate` defaults to `full`, not `search`'s `fast` (`internal/cli/gate.go:22-28`), and reports `tests_relation_available` when the relation is missing (`internal/sem/gate.go:144-148`, rendered at `internal/cli/gate.go:341-343`).
- **Verified reuse points**, each re-checked against the working tree rather than assumed: `sem.AnalyzeGitRange` (`internal/sem/analyze.go:27`), `sem.AnalyzeGitRangeWithOptions` (`internal/sem/analyze.go:72`), `sem.AnalyzeCheckpoint` (`internal/sem/analyze.go:1159`), `buildImpactResponseFromReader` (`internal/cli/impact.go:330`), `buildSearchVerifyCommand` (`internal/sem/search_verify.go:205`), the depth-bounded frontier shape at `internal/cli/impact.go:504-567`.

Verification performed: `go test ./internal/sem/ -run 'TestGate|TestReach'` (45 tests) and `go test ./internal/cli/ -run TestGate` (14 tests) both pass. The `sem` suite covers resolution (same-file, qualified-name, not-found, ambiguous, module-scope), each of the three verdicts, the dependents-disagree tie-break, deterministic ordering, the missing-`TESTS`-relation report, the transitive reachability walk (depth-2, cycles, self-edges, fan-out truncation, non-test callers), and — after the Curveball — evidence grading, partial-analysis detection, the additive-behaviour regression, and a real unparseable-repository fixture. The `cli` suite covers command composition, de-duplication, skipping names `go test -run` cannot match, the empty case, `--run` adjudication, and the widened fallback with its reason.

## Final semantic diff of the submitted implementation

`docs/buildathon/final-semantic-diff.txt`, produced by the graph's own change analysis rather than
a line diff:

```
entire graph diff --repo . --base 3a2a715 --head 3e98d8b --json
```

`3a2a715` is the last upstream commit before any Buildathon work; `3e98d8b` is the submission.

```
files with semantic changes : 16  (9 Go, 7 docs/other)
entity changes in Go code   : 226   (223 added, 3 body_changed)
by kind                     : 130 function, 76 field, 17 type, 2 module, 1 method
```

Only three pre-existing entities changed at all, and all three are the wiring a new subcommand
needs: `body_changed function Run` (the dispatch switch), and `body_changed module`
on `internal/cli/help.go` and `internal/cli/help_test.go` (the command's help entry). Every other
one of the 226 Go entity changes is `added`.

That is the independent check on the claim that the Curveball revision was additive. A feature that
had modified the provider would show `body_changed` across `internal/sem/provider*.go`; the graph's
own change analysis shows none.

Note the base ref: `3a2a715` predates all Buildathon work, so this diff covers the whole feature,
including `internal/sem/gate_reach.go` written by the other author. Scoped to the Curveball
revision alone (`git diff --stat 01520bf..3e98d8b -- internal/sem/gate_reach.go`) that file is
untouched, which is what the pre-implementation impact analysis in `curveball-impact.txt`
predicted.

## Noon Curveball: what changed and how we adapted

**The constraint.** Do not present incomplete Graph relationships as certain; identify when analysis
may be partial; offer a safe fallback or verification path; keep existing behaviour working for
fully resolved code; ship a test or fixture representing incomplete analysis.

**The assumption it invalidated.** `gate` assumed **the absence of an edge is a fact about the
code**. It is a fact about what static analysis resolved. That assumption was load-bearing in two
opposite directions. `UNCOVERED` asserted absence as a finding about the repository, when the
evidence only supports "no test reaches this along a path the resolver could follow". And `COVERED`
flattened three unequal evidence classes into one verdict: a resolved `CALLS`/`TESTS` edge, a mirror
test filename, and a name that mentions the symbol. `GateSelectedTest.Route` recorded which route
fired, but `GateVerdict` did not, so a convention match and a resolver-confirmed call rendered
identically. Both are the same error — treating the graph as an oracle rather than as evidence with
a provenance.

The full statement is `docs/buildathon/CURVEBALL.md`, **written before any code changed**.

**Graph analysis ran before implementation, and decided the design.** `entire graph impact` over
`gateVerdictFor`, `gateReach` and `Gate` was captured to `docs/buildathon/curveball-impact.txt` and
committed as `3ce7209` **before the first line of implementation existed**. It settled three things:

- *Ownership.* `gateReach`'s callees include `gateReachRoute`, which already emits
  `edge`/`mirror`/`name`, and its type consumers show it `RETURNS_TYPE gateReachedTest` — a type
  declared on our side of the seam. The evidence classes were already produced and already crossing
  the seam, so grading is a pure function of data `gate.go` already receives. **`gate_reach.go`, the
  other author's file, was never touched.**
- *Regression surface.* `gateVerdictFor` has 5 direct and 5 transitive callers, 8 of them tests.
  That made "additive, not a rewrite" a measured constraint. `gateVerdictFor` ended up unmodified.
- *Precedent for partiality.* `Gate` already called `gateRelationAdvertised` to read
  `snapshot.Header.RelationSet`, so reading `PartialFailures` and `LanguageTiers` extends an
  existing pattern rather than inventing one.

**The revision, additive in five points.**

1. **Every claim is graded.** `confirmed` (a resolved edge), `heuristic` (mirror or name
   convention), `unverified` (a claim resting on absence) — carried per selected test and aggregated
   per changed symbol, in both text and JSON (`internal/sem/gate.go:187-231`).
2. **Partial analysis is detected.** `Header.PartialFailures`, `Header.Warnings` and inventory-only
   language tiers are read and scoped to the files the change set touches; a changed file the parser
   could not fully handle marks the result partial and names the reason
   (`internal/sem/gate.go:255-307`).
3. **The fallback widens.** When analysis is partial, or a symbol's only evidence is heuristic, the
   emitted command widens from `-run '^(A|B)$'` to the changed packages and says why
   (`internal/cli/gate.go:263-316`). A narrow command derived from incomplete evidence is the
   dangerous output: it looks authoritative and silently skips what the resolver missed.
4. **Fully resolved code is unchanged.** Asserted, not assumed, by
   `TestGateFullyResolvedResultIsUnchangedByTheRevision` (`internal/sem/gate_test.go`) and
   `TestGateVerifyCommandIsUnchangedForFullyResolvedCode` (`internal/cli/gate_test.go`).
5. **A fixture for incomplete analysis**, built as a real repository parsed by the real provider.
   Two assumptions were checked against the provider before it was written and one was wrong:
   interface dispatch is **over**-approximated here (`Save -> Putter.Put` *and* `Save -> Store.Put`),
   not missed, so a fixture claiming interface dispatch hides the call would have asserted something
   false. The fixture uses the real incompleteness instead — a test file with a syntax error, where
   the provider still records `TestStorePut` as a symbol while the `CALLS TestStorePut -> Store.Put`
   edge is destroyed, leaving only the mirror-filename convention.

**What it does on real code.** On `4afea0a..4172fb3`, five changed symbols that were five identical
`COVERED` verdicts now separate by evidence class:

```
COVERED   fileRelationScan              provider.go:4486               [heuristic]   24 tests, all mirror
COVERED   runIndexedStreamingPipeline   provider_parallel_stream.go:14 [confirmed]    2 tests, both edge
COVERED   slot                          provider_parallel_stream.go:31 [heuristic]
COVERED   values                        provider_parallel_stream.go:32 [heuristic]
COVERED   result                        provider_parallel_stream.go:33 [heuristic]

VERIFY: go test ./internal/sem/
  widened: the only evidence for fileRelationScan is a naming convention, not a resolved call,
           so a narrow selection asserts more than the graph found
```

One symbol has structural evidence; four rest on filename and identifier conventions. Among
`values`'s selections was `TestParseDiffFlagsRequiresRevisionValues` in `internal/cli/root_test.go`,
admitted because its name contains "values". The tool now says so instead of implying a call.

**Why the new result is safe.** It distinguishes what was *resolved* from what was *inferred from
silence*, and it degrades toward running more tests rather than fewer. A wrong `UNCOVERED` now costs
the reader a source check they were told to make; before, it cost them a false assurance they had no
reason to doubt.

## Checkpoint links and what each checkpoint proves

All four are on `main` in the Entire mirror `entire://aws-ap-south-1.entire.io/gh/viscous106/entire-graph`.
Inspect any of them with `entire checkpoint explain <id>`.

| # | Milestone | Checkpoint | Commit |
| --- | --- | --- | --- |
| 1 | Intent and architecture | `1384714f6910` | `af889cf` — 11:29 |
| 2 | Last stable pre-Curveball | *see note* | `01520bf` — 12:00 |
| 3 | Curveball response | `ccbca9421d4e`, `1e82264bcb5a` | `3ce7209` — 12:28, `2466618` — 12:48 |
| 4 | Final implementation and verification | `1e82264bcb5a` + `entire session attach` | `3774fea` |

**1. Intent and architecture** (`1384714f6910` → `af889cf`). Proves the idea was chosen against
alternatives rather than defaulted into: 13 candidates scored, `Priors` rejected for prior art and
no corpus, intent-attestation demoted from headline to a flag because its verdicts are judgments
rather than deterministically testable. Records L3 resolution as the open risk, which is exactly
where the two real defects later appeared.

**2. Last stable pre-Curveball** (`01520bf`, committed at 12:00 as the Curveball landed). Proves
what ran and what did not at the freeze point: L0–L3, L6–L9 complete, and `gateReach` still the
66-line stub returning `nil, false, false`, so every verdict was `UNCOVERED` or `ISOLATED` and the
`COVERED` path had never executed. **Stated honestly: this commit carries no
`Entire-Checkpoint` trailer.** The session that produced it was not recording at the time, and the
trailer cannot be added afterwards without rewriting the commit. Checkpoints `1384714f6910` (11:29)
and `ccbca9421d4e` (12:28) bracket it, and `docs/buildathon/HANDOVER.md` — committed in
`3ce7209` and written at 12:10, before any Curveball code — states the freeze-point state in full,
including the blocker.

**3. Curveball response** — two checkpoints, in this order, and the order is the point.
`ccbca9421d4e` → `3ce7209` carries `docs/buildathon/CURVEBALL.md` (the invalidated assumption,
written before any code changed) and `docs/buildathon/curveball-impact.txt` (`entire graph impact`
over the three affected symbols). **It contains no implementation.** `1e82264bcb5a` → `2466618` is
the implementation that followed. The graph analysis is therefore provably before the edit, not
reconstructed after it.

**4. Final implementation and verification** (`3774fea`). Proves the shipped state: per-toolchain
command emission, the two noise-filter defects found by running gate on its own commits, and the
non-reportable-selection fix found by running it on a JavaScript repository.

## Setup, run and test instructions

Requires Go (CGO enabled; the tree-sitter dependency needs it) and Git.

```bash
# build
go build -o entire-graph ./cmd/entire-graph

# run: what tests does this change need, and what does no test reach?
entire graph gate --repo . --base main

# machine-readable, schema_version "gate.v1"
entire graph gate --repo . --base main --format json

# other entry points
entire graph gate --repo . --checkpoint <id>      # change set from an Entire Checkpoint
entire graph gate --repo . --base main --depth 1  # 1 or 2; default 2

# tests for this feature
go test ./internal/sem/ -run 'TestGate|TestReach'    # 53 tests
go test ./internal/cli/ -run TestGate                # 23 tests
```

To reproduce the cross-language runs, point `--repo` at any other project:

```bash
entire graph gate --repo /path/to/a/python/project --base HEAD~1
entire graph gate --repo /path/to/a/javascript/project --base HEAD~1
```

Note `--base main` from `main` itself is an empty range and correctly reports `CHANGED 0 symbols`;
use `--base HEAD~1` or an explicit `--base A --head B`.

The exact CI gate this repository requires, in order:

```bash
gofmt -l -s .
go vet ./...
go build ./...
go test ./...
```

## Databricks use, data sources and limitations (if applicable)

Opted in and authenticated, but **not used in the submitted feature**. Nothing in `entire graph gate` reads from or writes to Databricks; the command is local and no-egress, matching Graph itself. The considered-but-unbuilt angle was backfilling `gate` over N upstream commits into a table and using a hosted endpoint for the `--checkpoint` claim-versus-reach judgment. It was kept off the critical path deliberately: the SQL warehouse is 2X-Small and stopped (1-3 minute cold start, per-day Free Edition quota), and a network dependency in the core pipeline would break the determinism property that L3-L6 rests on. No credentials appear anywhere in this repository.

## Known limitations and next steps

Stated plainly, because a coverage tool that overstates itself is worse than none.

- **Static call resolution is heuristic.** Interface dispatch, reflection, table-driven registration and generated code can all hide a real test-to-symbol path. `UNCOVERED` means *"no path the graph can see"* — a prompt to check, never a proof that the symbol is untested (`internal/sem/gate.go:406-409`).
- **Test selection is not coverage measurement.** A test that reaches a changed symbol may not assert anything about the changed behaviour. Reachability bounds what *can* break; it does not confirm what *is* checked.
- **Two runners in v1: Go and pytest.** Commands are derived per toolchain from the changed files (`gateToolchains`, `internal/cli/gate.go`), and a build manifest must exist on disk before one is emitted — the only evidence that a given runner is the one the project uses. jest/vitest, cargo, maven and gradle repositories get the full analysis (verdicts, routes, evidence grades, gaps) and no command, which is deliberate: names a runner cannot match are dropped rather than emitted, because a command that silently selects nothing reads exactly like a green run of everything.
- **A test-file helper is not a covering test.** Attribution admits any symbol on a test-artifact path, so a fixture helper arrives looking like a test case. A convention route (mirror, name) must therefore also satisfy the name convention; a resolved edge is kept regardless, because the graph saw the call. Symbols dropped this way are counted, not hidden.
- **`TESTS` edges require profile `full`.** At other profiles one of the three evidence routes is missing; the output says so via `tests_relation_available` rather than degrading quietly.
- **Fan-out is capped** at 512 inbound edges per level (`internal/sem/gate.go:64`). When the cap bites, the result is marked `Truncated` and the text renderer prints a warning (`internal/cli/gate.go:375-377`) — a shortened list that did not say it was shortened would read as "no more tests exist".
- **Partiality is scoped to the files the change set touches.** Reachability walks *inbound* edges, so a parse failure in an unchanged file elsewhere can still hide a caller. That case is not marked `partial` — but it is not silent either: the verdict still carries `unverified` or `heuristic`, and heuristic-only evidence widens the command on its own. The tool degrades toward running more tests, never fewer.
- **A second pre-existing failure surfaces only in a full-package run**: `TestCollidingRepoKeysDoNotShareCacheEntries` passes under `-run` in isolation but fails as part of `go test ./internal/cli/`, which points at cross-test interference over a shared cache directory rather than at the assertion itself. Verified unrelated to this work by running the full package against `01520bf`, the commit before the Curveball revision, where it fails identically. Not investigated further — it is upstream's and outside this feature.
- **A pre-existing upstream test fails on our machine**: `TestDoctorWorksOutsideGitRepo`. Outside a git repository, `doctor` reports `repo_root=<temp dir>` where the test expects `<unset>`. Verified unrelated to this work by deleting our files and re-running on a pristine tree. It will appear in `go test ./...` on a comparable machine.

**Next steps:** L4 (transitive reachability) and L8 (`--run`, adjudicated verdict) both shipped. Next: widen partiality detection to files holding selected tests, not only changed files; add the `--checkpoint` claim-versus-reach lens that raises `UNDECLARED REACH` when a checkpoint's stated intent does not mention a region the change actually reaches; per-language command emitters beyond Go; and a co-change lens for invisible coupling — files that change together with no static `CALLS` edge between them.
