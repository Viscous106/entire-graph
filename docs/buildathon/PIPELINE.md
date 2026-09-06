# entire graph gate — Full Reference
BTW Buildathon 2026 · Track E2 Graph Intelligence · 6 Sep 2026
Team: Abhinav Singh (OfficialAbhinavSingh) · Yash (Viscous106)
Repo: fork of `entireio/entire-graph` (Go) · 723 files · 8,242 symbols · 10,069 CALLS edges

> Single reference doc. Sections 1–13 = the product. 14–19 = decision record, environment,
> and submission. Yash builds the deck from 1–11.

---

# PART A — THE PRODUCT

## 1. One sentence

`entire graph gate` turns a change into a **verified test plan**: which tests must run,
why each was selected, which changed code **no test can reach**, and — with `--run` —
an adjudicated verdict that closes the loop.

## 2. Problem

You changed a function. The repo has hundreds of tests. Two questions nobody can answer
from a diff:

1. **Which tests can actually break?** Today you guess, or you run everything.
2. **Which changed code is reachable by no test at all?** Today: silence.

The second is the dangerous one. An agent makes a small-looking edit; the blast radius
includes symbols no test touches; CI is green; nothing was ever verified. A diff cannot
show this. A repo-wide coverage report cannot either — it is not scoped to the change.

**User:** a developer or coding agent about to commit a change.

## 3. Why Entire is essential

Three distinct Entire capabilities are load-bearing. Remove any one and the product dies.

| Capability | Role |
|---|---|
| `sem.AnalyzeGitRange` / `AnalyzeCheckpoint` | entity-level change set, not line diffs |
| Graph snapshot: symbols + CALLS relations | transitive reachability — the only source for it |
| `runVerifyCommands` adjudication | verdict as delta (newly-fail vs PRE-EXISTING), not raw output |

Without the graph there is no reachability. Without entity-level change analysis we are
matching text. Without adjudication `--run` is just a shell command.

## 4. What a person actually does

```
1.  make changes, as normal
2.  $ entire graph gate --base main
3.  read: tests to run · reasoning per test · the gaps
4.  $ entire graph gate --base main --run      <- adjudicated verdict
5.  act on the warnings: write the missing test, or accept the risk knowingly
```

Step 4 is the loop closing. Today you copy a command into a shell and lose the connection
between *what changed* and *what passed*.

## 5. Pipeline (LLD)

```
L0  CLI            gate.go: flags, resolveRepo, validateRevision, git-metadata guard
                   REUSED - same preamble as runCommit / runDiff
L1  Change source  sem.AnalyzeGitRange(base,head) | sem.AnalyzeCheckpoint(id)
                   REUSED - both return sem.Result{ Files[].Changes[]EntityChange }
L2  Graph index    snapshot reader -> symbolsByID, symbolsByFile, relations[]
                   REUSED - same path buildImpactResponseFromReader uses. Built ONCE.
L3  Resolution     EntityChange{Name,Kind} --> SymbolRecord                    NEW
                   diff speaks names; the graph speaks symbol IDs
L4  Reachability   transitive inbound CALLS walk, depth <= 2                   NEW
                   lifts the one-hop limit in search_covertest.go
L5  Attribution    reaching symbols -> tests                          REUSED, re-aimed
                   searchTestArtifactPath / searchTestNameShaped / mirror routes
L6  Verdict        COVERED · UNCOVERED · ISOLATED per symbol                   NEW
                   this is the alarm
L7  Command        union of selected tests -> go test -run '^(A|B)$'           NEW
                   existing SearchVerifyCommand is per-file, single-anchor
L8  Execute        runVerifyCommands + renderVerifyVerdict           REUSED wholesale
L9  Render         text + JSON (schema_version, deterministic ordering)
```

**Four new layers, five reused.**

### Data flow
```
git base..head
      |
      v
  [L1] entity change set ----+
                             |
  graph snapshot --> [L2] ---+--> [L3] resolve --> [L4] reach --> [L5] attribute
                                                                        |
                                            +---------------------------+
                                            v
                                     [L6] verdict per symbol
                                            |
                        +-------------------+-------------------+
                        v                                       v
              [L7] test command                        UNCOVERED / ISOLATED
                        |                                    (the gaps)
                        v
              [L8] adjudicated verdict --> [L9] text | JSON
```

### Verdict semantics (L6)

| Verdict | Meaning | Action |
|---|---|---|
| `COVERED` | >=1 test reaches this changed symbol within depth 2 | run the selected tests |
| `UNCOVERED` | symbol has callers, but no path from any test | write a test, or accept knowingly |
| `ISOLATED` | symbol has no inbound CALLS edges at all | dead code, or an entry point — inspect |

`UNCOVERED` is the product. Everything else is table stakes.

### Two properties that matter to judges

**Determinism.** L3 -> L4 -> L5 -> L6 is a pure function of (change set, graph). No I/O,
no clock, no network. Table-testable with fixtures. Satisfies the repo's determinism rule.

**No regression surface.** We consume `internal/sem` rather than editing existing files,
so the provider and its ~300 existing test files cannot regress.

### Placement correction (verified against source)

`searchTestNameShaped` (`search_covertest.go:587`) and `searchTestArtifactPath`
(`search.go:4873`) are **unexported**. Only renderers are exported
(`RenderSearchCoverageNote`, `RenderSearchVerifyCommand`). L5 is therefore **not reachable
from `internal/cli`**.

**Resolution:** engine lives in a **new file** `internal/sem/gate.go` exporting `sem.Gate(...)`.
Additive — no existing `sem` file is modified, so the no-regression property holds and the
unexported helpers are in scope. `internal/cli/gate.go` is a thin flag shell.

## 6. Verified reuse points

Every claim below was checked against the working tree, not assumed.

| Symbol | Location |
|---|---|
| `sem.AnalyzeGitRange` | `internal/sem/analyze.go:27` |
| `sem.AnalyzeGitRangeWithOptions` | `internal/sem/analyze.go:72` |
| `sem.AnalyzeCheckpoint` | `internal/sem/analyze.go:1159` |
| `buildImpactResponseFromReader` | `internal/cli/impact.go:330` |
| `runVerifyCommands` | `internal/cli/verify.go:157` |
| `renderVerifyVerdict` | `internal/cli/verify.go:302` |
| `searchTestNameShaped` (unexported) | `internal/sem/search_covertest.go:587` |
| `searchTestArtifactPath` (unexported) | `internal/sem/search.go:4873` |
| `buildSearchVerifyCommand` | `internal/sem/search_verify.go:205` |
| covering-test is one-hop | `internal/sem/search_covertest.go:33` — "an inbound CALLS/TESTS edge from a symbol in a test file" |

File sizes: `search_covertest.go` 886 · `impact.go` 977 · `verify.go` 409.

## 7. Build order

| Window | Deliverable |
|---|---|
| -> 11:45 | `gate --base main`. L0–L7. Prints selected tests + reasoning + UNCOVERED/ISOLATED. No `--run`, no `--checkpoint`. |
| 11:45 | Stable commit + **Checkpoint 2** (graded) |
| 12:00 | Curveball. Fresh session. Reconstruct from checkpoints. `graph impact` before touching the affected area. |
| 13:00–14:15 | Curveball response + `--run` (L8 is pure reuse) |
| 14:15–14:45 | `--checkpoint <id>` + UNDECLARED REACH · BUILDATHON.md |
| 14:45 | Final `graph diff` semantic analysis + **Checkpoint 4** |

Ship narrow first. `--run` and `--checkpoint` are both additive.

### The `--checkpoint` lens (afternoon)

L1 already accepts a checkpoint. Today we use it as a change source and discard its **text**.
Adding the claim comparison costs ~40 lines and no new I/O:

```
$ entire graph gate --checkpoint <id>
  Checkpoint claims: "fix token refresh in auth"
  Reach:  internal/sem/   <- never mentioned in the claim
  !! UNDECLARED REACH
  Tests: 12 selected · 2 UNCOVERED · 1 ISOLATED
```

The Curveball generates this scenario for free: the new constraint forces us off the
morning plan, so the pre-noon checkpoint's stated intent no longer matches actual reach.
We catch ourselves with our own tool, on the record. This converts Checkpoints 9 -> 14.

## 8. Rubric mapping

| Criterion | Pts | Evidence |
|---|---|---|
| Problem and innovation | 20 | Reachability-gap alarm (UNCOVERED / ISOLATED) is not in TIA tooling; lifts a real one-hop limit in Graph itself |
| Technical implementation | 25 | 5 of 9 layers reused; zero modification to existing `sem` files; pure deterministic core; fixture tables |
| Curveball response | 15 | New constraint = new verdict category or selection rule. Traversal engine unaffected |
| Use of Checkpoints | 15 | `--checkpoint` consumes stated intent, not just the diff; UNDECLARED REACH |
| Use of Graph | 15 | Transitive CALLS walk, symbol resolution, test attribution — verified by **running** the selected tests |
| Demo and future potential | 10 | Seeded-bug proof, live, in 10 seconds |

Required to show during dev: (a) a graph search or definition lookup, (b) a relationship or
impact analysis before a high-risk change, (c) a final semantic diff of the submission.
Our build hits all three as a by-product of normal work — record evidence as we go.

## 9. Checkpoint plan (15 pts — machine-scored for authenticity)

`entireio/entire-judge` is a real repo: "judge lenses over a submission's brain
(deterministic authenticity/effort + LLM-judged prompting/idea-execution)". Padding is
detectable. Each checkpoint records decisions, **rejected options**, failures, assumptions,
open risks.

1. **Intent & architecture.** Chose gate. Rejected: constraint-memory / "Priors" (prior art
   `drift`, `invariance-ai/gps`, `kgai`; and no mined corpus exists in a fork created at
   09:48 today). Rejected: intent-attestation as headline (verdicts are judgments, not
   deterministically testable — costs Technical). Open risk: L3 resolution.
2. **Last stable pre-Curveball.** What runs, what does not, what is unverified.
3. **Curveball response.** Constraint received, impact analysis run before editing,
   smallest complete response, test result.
4. **Final implementation & verification.** Semantic diff, adjudicated verdict, limits.

## 10. Demo script (3 min)

1. **Problem** — one sentence. "You changed a function. Which tests can break, and what can
   no test reach?"
2. `gate --base main` — selected tests, reasoning per test, the UNCOVERED lines.
3. **Proof** — plant a bug in a selected symbol. `gate --run`. One selected test fails. Run a
   test from *outside* the selection: passes, blind to it. Selection is sound.
4. **Graph evidence** — `graph impact` on the same symbol, cite `file:line`. Verifiable
   against source.
5. **Curveball** — the constraint, the behavior that changed, the test that proves it.
6. **Limits + next step.**

Fallback: keep a screen recording of steps 2–3 locally in case anything is cold on stage.

## 11. Risks

| Risk | Mitigation |
|---|---|
| **L3 name -> symbol resolution.** Overloads and same-name symbols in different files make the mapping ambiguous. Upstream issue #34: "compound-v1 symbol IDs are unstable for same-name/overloaded symbols" | Resolve with `(name, file path)` from the `FileChange` we are already inside. Report unresolvable entries **explicitly** rather than dropping them silently |
| L5 unreachable from `internal/cli` | Engine in new file `internal/sem/gate.go` (see §5) |
| Depth-2 walk explodes on a hub symbol | Cap fan-out per level; report truncation in output, never silently |
| Cold graph query ~30s | Index already warm; warm it again before demo |
| Mirror push may not propagate to GitHub fork | Push to both `origin` and `upstream` before submitting. **Verify, do not assume** |
| Deadline disputed: PDF says 15:00, site says 16:00 | Treat 15:00 as real. Confirm with an organiser |

## 12. Ownership

| Who | Files |
|---|---|
| Abhinav | `internal/sem/gate.go` (L3–L6) |
| Yash | `internal/cli/gate.go` (L0, L7, L9), `BUILDATHON.md`, submission deck |

No file collisions. Both consume the same `sem.Gate(...)` signature — **agree it before
splitting**.

## 13. Open items

- [ ] Restore or accept removal of the `.claude/settings.json` deny block that `entire enable` deleted
- [ ] Record `entire graph verify --record-baseline` **before any edit** — unrecoverable later
- [ ] Assign submission owner and demo owner
- [ ] Confirm real deadline with an organiser
- [ ] Confirm mirror push propagates to the GitHub fork
- [ ] Agree `sem.Gate(...)` signature between Abhinav and Yash

---

# PART B — DECISION RECORD & ENVIRONMENT

## 14. Ideas considered

13 candidates were surfaced and scored against the rubric before choosing.

| Idea | Verdict |
|---|---|
| Priors — mine rejected approaches, scope to graph region, block edits | **Rejected.** Prior art + no corpus |
| Impact-aware review bot | Rejected. Most obvious idea in the track; ~9/15 on Checkpoints |
| Graph test selection | **Absorbed into gate** |
| Onboarding / guided exploration | Rejected. Crowded 2026 lane; scored 51/100 |
| Migration planner | Rejected. Plans are not executable -> weak Technical/Demo |
| Test-gap detector | **Absorbed into gate** as UNCOVERED/ISOLATED |
| attest — claimed intent vs actual change | **Demoted to a flag** (`--checkpoint`) |
| Route-orphan + safe-delete proof | Rejected. Detection exists (`fireharp/coherence`) |
| Refactor-vs-behavior-change classifier | Rejected. Adjacent prior art; fuzzy verdicts |
| Blast-radius edit gate for agents | Rejected. riftmap/drift occupy this |
| Invisible coupling (co-change with no static CALLS edge) | Parked. Interesting, weak checkpoint story. Possible stretch view |
| Checkpoint resumability scorer | Rejected. Barely uses the graph — reads as an E1 project |
| Failing-test auto-triage | Rejected. Narrow, unglamorous |

**Why gate won:** Technical implementation is the largest rubric line (25) and gate owns it —
deep verified reuse, deterministic core, no regression surface. Its verdicts are provable
(the seeded bug is caught or it is not), where attest's verdicts are judgments a judge can
argue with. Highest floor, and the ceiling gap closes once `--checkpoint` lands.

## 15. Prior art checked

| Category | Existing |
|---|---|
| Constraint / decision memory | `dadbodgeoff/drift`, `invariance-ai/gps`, `kgai` |
| Test impact analysis | Launchable, Ekstazi, pytest-testmon, Bazel rdeps, Microsoft TIA |
| Impact / blast radius | CodeScene, Sourcegraph, riftmap, `tirth8205/code-review-graph` |
| Onboarding graphs | CodeGraphContext, CodeGraph, Sourcetrail |
| Architecture rules | ArchUnit, dependency-cruiser, Nx module boundaries |

**Our delta vs TIA tooling:** TIA answers "which tests to run". None of them raise
"this changed symbol is reachable by **no** test" as a first-class alarm scoped to the change,
and none close the loop with an adjudicated verdict tied to the entity change set.

## 16. Entire CLI reference (what we actually use)

```bash
# evidence
entire graph search   --repo . --query "..." --format json
entire graph impact   --repo . --symbol X --format json     # blast radius, co-change, types
entire graph neighbors --repo . --symbol X --direction in   # callers
entire graph diff     --base main --head HEAD --json        # entity-level semantic diff
entire graph commit   <sha> --json
entire graph checkpoint <id> --json                         # graph <-> checkpoint bridge

# proof
entire graph verify --repo . --test "go test ./..." --record-baseline /tmp/base.json
entire graph verify --repo . --test "go test ./..." --pre-edit-baseline /tmp/base.json

# intent
entire checkpoint list
entire checkpoint explain <id> [--full]
entire search "<query>" --json --compact
```

`impact` output includes direct + transitive callers (depth<=2), callees, type consumers
(USES_TYPE / PARAM_TYPE / RETURNS_TYPE), data flows, historic co-change files, and siblings.
Graph is deterministic and **no-egress** — local CLI only, no MCP server, no HTTP API, no SDK.
Consume it by shelling out and parsing `--format json` / NDJSON.

## 17. Environment gotchas

- **No OS keyring on this machine.** `entire login` saves nothing without
  `ENTIRE_TOKEN_STORE=file`. Already appended to `~/.zshrc` (backup `~/.zshrc.bak-btw`);
  token at `~/.config/entire/tokens.json`, mode 0600. A non-interactive shell will not
  source `.zshrc` — set it explicitly:
  `export PATH="$HOME/.local/bin:$PATH" ENTIRE_TOKEN_STORE=file`
- **`entire graph init-agents` has already been run.** Rerunning regenerates
  `.entire/graph-agent.md` in full and discards manual edits. Do not rerun.
- **`entire enable` deleted** the `"permissions": {"deny": ["Read(./.entire/metadata/**)"]}`
  block from `.claude/settings.json`. Reproduced on both clones — tool behavior, not a fluke.
  Currently the only uncommitted change. Decide before first commit.
- **Two people, one repo.** Coordinate file ownership (§12) or you will collide.
- Versions: `entire` 0.10.5 · graph plugin 0.4.0 · Git 2.55.0.

## 18. Databricks (opted in, wired, unused)

Separate ₹50,000 award, separate 100-pt rubric, does **not** affect the Entire score.
Workspace authenticated. `export DATABRICKS_CONFIG_PROFILE=dbc-37e693a6-3ac2`
(passing `--profile` via a shell variable gets mangled into one argument and errors).

Available with no API key: hosted LLM endpoints (`databricks-llama-4-maverick`,
`gpt-oss-120b`, `meta-llama-3-3-70b-instruct`, others) and embeddings (`bge-large-en`,
`gte-large-en`). SQL warehouse `de92eaa24a5276e4` is 2X-Small and **STOPPED** — cold start
1–3 min, and Free Edition quota is per-day, so do not start it to idle. 0 of 3 Apps used.

**Only if the Entire path is green and time remains.** A credible angle: backfill gate over
N upstream commits into a table, and use a hosted model for the `--checkpoint` claim-vs-reach
judgment. Do not put a network dependency on the critical path before 11:45.

**Credential rule:** nothing credential-shaped reaches the repo, `BUILDATHON.md`, a notebook,
a screenshot, or a checkpoint. Checkpoints capture agent session context — a secret pasted
into a chat message can end up committed to the mirror.

## 19. Submission package

Due **15:00 IST** (treat as real until an organiser confirms otherwise).

- [ ] Selected track: E2 — Graph Intelligence
- [ ] GitHub fork URL + final commit SHA (push to `origin` **and** `upstream`)
- [ ] Entire mirror / project URL
- [ ] Links to all four required Checkpoints
- [ ] Setup, run and test instructions
- [ ] Working demo or fallback recording
- [ ] `BUILDATHON.md` in repo root — sections: name · one-sentence summary · problem, user,
      why it matters · track and why Entire is essential · architecture and workflow ·
      Graph findings and verification · Curveball: what changed and how we adapted ·
      checkpoint links and what each proves · setup/run/test · Databricks (if opted in) ·
      known limitations and next steps
- [ ] Final `entire graph diff` semantic analysis recorded
- [ ] Demo owner can sign in and run the critical path
