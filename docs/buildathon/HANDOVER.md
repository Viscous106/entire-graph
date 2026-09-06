# Session handover — 12:10 IST, 2026-09-06

## What this project is

`entire graph gate` — a new subcommand for this fork of `entireio/entire-graph`.
BTW Buildathon 2026, **Track E2 (Graph Intelligence)**. Submission 15:00 IST.

It takes a change set (`--base`/`--head`, or `--checkpoint <id>`), resolves each changed entity to a
graph symbol, walks inbound `CALLS` edges transitively to find which tests reach it, emits the
narrowest `go test -run '^(A|B)$'` for that set, and — the headline — raises **UNCOVERED** for a
changed symbol that has dependents but that no test reaches.

Verdicts: `COVERED` / `UNCOVERED` / `ISOLATED`.

**The delta over what the repo already had**, all verified against source:
`search` already finds a covering test, but it is query-anchored, one-hop
(`search_covertest.go:456`), names a single test (`searchCoveringTestLimit = 1`), and is silent
when it finds nothing (`search_covertest.go:41`). The Go emitter names one test, no alternation
(`search_verify.go:919`). We made it change-anchored, transitive, set-level, and loud about gaps.

## Where the code is

| Layer | File | Owner | State |
| --- | --- | --- | --- |
| L0 flags, L7 command, L8 `--run`, L9 render | `internal/cli/gate.go` | us | done |
| L3 resolution, L6 verdict, orchestration | `internal/sem/gate.go` | us | done |
| L4 reachability, L5 attribution | `internal/sem/gate_reach.go` | **the other author** | see blocker |
| wiring | `internal/cli/root.go`, `help.go`, `help_test.go` | us | done |

20 test functions, every one written RED before GREEN. `gofmt -l -s .`, `go vet ./...`,
`go build ./...` all clean.

## BLOCKER — read this first

`internal/sem/gate_reach.go` in the working tree is still the **66-line stub returning
`nil, false, false`**, and `gate_reach_test.go` does not exist locally.

The real implementation is commit **`9c2ceb5`**, present on both `origin/main` and `upstream/main`
but **not in local history**. Our local `c52611f` was committed on top of `af889cf` instead, so the
push was rejected as non-fast-forward.

Until this is merged, **every verdict is UNCOVERED or ISOLATED and the COVERED path has never
executed.** Fix before anything else:

```sh
export ENTIRE_TOKEN_STORE=file ENTIRE_TOKEN_STORE_PATH=/home/viscous/Viscous/BTW/.entire/config/tokens.json
export PATH="/home/viscous/Viscous/BTW/.entire/bin:$PATH"
git pull --rebase origin main
```

The env vars matter: this machine has no OS keyring, so without them git remote operations fail
with `The name is not activatable`, which reads like a network error rather than an auth one. That
is why the earlier rebase silently did nothing.

## The Noon Curveball

Full analysis in **`docs/buildathon/CURVEBALL.md`** — read it, it was written before any code
changed and states the invalidated assumption and the five-point revision.

In one line: **gate assumed the absence of an edge is a fact about the code. It is a fact about what
static analysis resolved.** `UNCOVERED` asserted absence as a finding; `COVERED` flattened resolved
edges and naming conventions into one verdict.

Scoring (15 pts) rewards, in this order: a fresh session reconstructing from checkpoint context;
**graph impact analysis run before editing**; a written statement of the invalidated assumption; a
focused revision preserving existing behaviour; tests for the new behaviour; a final checkpoint.
Graph use that happens after implementation, or explanation given verbally rather than captured in
context, scores in the partial band.

## Verification

```sh
gofmt -l -s . && go vet ./... && go build ./...
go test ./internal/sem/ -run 'TestGate|TestReach' -v 2>&1 | grep -E '^(--- )?(PASS|FAIL|ok)'
go test ./internal/cli/ -run TestGate -v 2>&1 | grep -E '^(--- )?(PASS|FAIL|ok)'

go build -o /tmp/eg ./cmd/entire-graph
/tmp/eg gate --repo . --base 4afea0a --head 4172fb3
/tmp/eg gate --repo . --base 4afea0a --head 4172fb3 --run
```

Whole suite: `GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null go test -timeout 30m ./...`
— expect exactly one failure, `TestDoctorWorksOutsideGitRepo`, which is **pre-existing upstream**
and fails on a pristine tree on this machine (verified by deleting our files and re-running).

## Conventions

- **TDD, strictly.** Write the test, watch it fail for the right reason, then implement.
- Nothing under `internal/sem/` other than `gate*.go` is modified — the provider must not regress.
- Deterministic: never range a map into output; sort before returning.
- Never claim more than was measured. Unplaceable, ambiguous and module-scope changes are each
  reported with their own reason rather than dropped. That principle is the product.
- The user runs all git commands personally. Print them, do not execute them.
