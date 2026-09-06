# entire graph gate

## One-sentence summary

`entire graph gate` turns a change set into a verified test plan: it resolves each changed entity to a graph symbol, walks inbound `CALLS` edges transitively to find which tests reach it, emits the narrowest `go test -run '^(A|B)$'` command for that set, and raises `UNCOVERED` for changed symbols that other code depends on but that no test reaches.

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

## Entire Graph findings and verification

Findings from using Graph on this repository during the build:

- **Snapshot scale.** 723 files, 8,242 symbols, 10,069 `CALLS` edges.
- **The one-hop limit is real and load-bearing.** Confirmed at `internal/sem/search_covertest.go:456`. This is what makes L4's transitive walk an addition to Graph rather than a repackaging of `search`.
- **L5 is not reachable from `internal/cli`.** `searchTestNameShaped` (`internal/sem/search_covertest.go:587`) and `searchTestArtifactPath` (`internal/sem/search.go:4873`) are unexported; only renderers are exported (`RenderSearchCoverageNote` at `internal/sem/search_covertest.go:387`, `RenderSearchVerifyCommand` at `internal/sem/search_verify.go:1274`). This finding, from a `graph search` before writing any code, is why the engine lives in `internal/sem` rather than `internal/cli`.
- **`TESTS` edges exist only at profile `full`.** So `gate` defaults to `full`, not `search`'s `fast` (`internal/cli/gate.go:22-28`), and reports `tests_relation_available` when the relation is missing (`internal/sem/gate.go:125-129`, rendered at `internal/cli/gate.go:213-216`).
- **Verified reuse points**, each re-checked against the working tree rather than assumed: `sem.AnalyzeGitRange` (`internal/sem/analyze.go:27`), `sem.AnalyzeGitRangeWithOptions` (`internal/sem/analyze.go:72`), `sem.AnalyzeCheckpoint` (`internal/sem/analyze.go:1159`), `buildImpactResponseFromReader` (`internal/cli/impact.go:330`), `buildSearchVerifyCommand` (`internal/sem/search_verify.go:205`), the depth-bounded frontier shape at `internal/cli/impact.go:504-567`.

Verification performed: `go test ./internal/sem/ -run TestGate` and `go test ./internal/cli/ -run TestGate` both pass. The `sem` suite covers resolution (same-file resolution, not-found, ambiguous, module-scope), each of the three verdicts, the dependents-disagree tie-break, deterministic ordering, and the missing-`TESTS`-relation report. The `cli` suite covers command composition, de-duplication, skipping names `go test -run` cannot match, and the empty case.

## Noon Curveball: what changed and how we adapted

TO BE COMPLETED AFTER 12:00

## Checkpoint links and what each checkpoint proves

Links to be filled in at submission. The four required milestones:

1. **Intent and architecture.** Proves the idea was chosen against alternatives, not defaulted into: 13 candidates scored, `Priors` rejected for prior art and no corpus, intent-attestation demoted from headline to a flag because its verdicts are judgments rather than deterministically testable. Records L3 resolution as the open risk. — *link pending*
2. **Last stable pre-Curveball.** Proves what ran, what did not, and what was unverified at the freeze point. — *link pending*
3. **Curveball response.** Proves the constraint was received, an impact analysis was run before editing the affected area, and the smallest complete response was made and tested. — *link pending*
4. **Final implementation and verification.** Proves the semantic diff of the submission, the adjudicated verdict, and the stated limits. — *link pending*

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
go test ./internal/sem/ -run TestGate
go test ./internal/cli/ -run TestGate
```

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

- **Static call resolution is heuristic.** Interface dispatch, reflection, table-driven registration and generated code can all hide a real test-to-symbol path. `UNCOVERED` means *"no path the graph can see"* — a prompt to check, never a proof that the symbol is untested (`internal/sem/gate.go:243-245`).
- **Test selection is not coverage measurement.** A test that reaches a changed symbol may not assert anything about the changed behaviour. Reachability bounds what *can* break; it does not confirm what *is* checked.
- **Go-only command emission in v1.** `gateGoTestCommand` (`internal/cli/gate.go:178`) emits `go test -run`. Other toolchains get the full analysis — verdicts, evidence chains, gaps — but no runnable command. Names `go test -run` cannot match are dropped rather than emitted, because a command that silently selects nothing reads exactly like a green run of everything (`internal/cli/gate.go:171-177`).
- **`TESTS` edges require profile `full`.** At other profiles one of the three evidence routes is missing; the output says so via `tests_relation_available` rather than degrading quietly.
- **Fan-out is capped** at 512 inbound edges per level (`internal/sem/gate.go:64`). When the cap bites, the result is marked `Truncated` and the text renderer prints a warning (`internal/cli/gate.go:238-240`) — a shortened list that did not say it was shortened would read as "no more tests exist".
- **L4 is the in-flight layer.** `gateReach` (`internal/sem/gate_reach.go`) is the transitive walk and test-attribution step; until it returns selections, every resolved symbol falls through to `UNCOVERED` or `ISOLATED`. L3, L6, L7 and L9 are complete and tested.
- **A pre-existing upstream test fails on our machine**: `TestDoctorWorksOutsideGitRepo`. Outside a git repository, `doctor` reports `repo_root=<temp dir>` where the test expects `<unset>`. Verified unrelated to this work by deleting our files and re-running on a pristine tree. It will appear in `go test ./...` on a comparable machine.

**Next steps:** finish L4 and wire L8 (`--run`, adjudicated verdict — `runVerifyCommands` is pure reuse); add the `--checkpoint` claim-versus-reach lens that raises `UNDECLARED REACH` when a checkpoint's stated intent does not mention a region the change actually reaches; per-language command emitters beyond Go; and a co-change lens for invisible coupling — files that change together with no static `CALLS` edge between them.
