# Noon Curveball — Graph is evidence, not an oracle

**Track E2 · received 12:00 IST, 2026-09-06.** Written before any code changed, so the reasoning is
captured in context rather than explained verbally after the fact.

## The constraint

The product must not present incomplete Graph relationships as certain; it must identify when
analysis may be partial; it must offer a safe fallback or verification path; existing behaviour for
fully resolved code must keep working; and it must ship a test or fixture representing incomplete
analysis. Users and agents must be able to tell apart **confirmed structural evidence**,
**heuristic or incomplete evidence**, and **claims that require source or test verification**.

## The assumption it invalidated

`gate` assumed **the absence of an edge is a fact about the code**. It is not. It is a fact about
what static analysis resolved.

That single assumption is load-bearing in two places, in opposite directions:

1. **`UNCOVERED` asserted absence.** `gateVerdictFor` (`internal/sem/gate.go`) returns `UNCOVERED`
   when the reachability walk returns no tests. The verdict was rendered as a finding about the
   repository — "no test reaches this" — when the evidence only supports "no test reaches this
   along a path the resolver could follow". Interface dispatch, reflection, table-driven
   registration and generated code all break that inference. The text output hedged in prose
   ("no path the graph can see"), but nothing machine-readable carried the caveat, and the JSON
   carried none at all.

2. **`COVERED` flattened three unequal evidence classes into one.** Attribution admits a test
   through three routes (`internal/sem/gate_reach.go`): a resolved `CALLS`/`TESTS` edge, a mirror
   test file, or a name that mentions the symbol. Only the first is structural evidence; the other
   two are naming conventions that happen to correlate. `GateSelectedTest.Route` recorded which
   route fired, but `GateVerdict` did not, so a convention-matched test and a resolver-confirmed
   call produced an identical `COVERED`.

Both are the same error: treating the graph as an oracle rather than as evidence with a provenance.

## The revision

Additive, so fully resolved code behaves exactly as before.

1. **Grade every claim.** `confirmed` (a resolved edge), `heuristic` (mirror or name convention),
   `unverified` (a claim resting on absence). Carried per selected test and aggregated per changed
   symbol, in both text and JSON.
2. **Detect partial analysis.** Read `snapshot.Header.PartialFailures`, `Warnings` and the
   inventory-only language tiers. A changed file the parser could not fully handle marks the
   result partial and names the reason, rather than reporting a verdict as if the input were whole.
3. **Provide the fallback.** When analysis is partial, or when a symbol's only evidence is
   heuristic, the emitted command widens from `-run '^(A|B)$'` to the package or suite, and says
   why. A narrow command derived from incomplete evidence is the dangerous output: it looks
   authoritative and silently skips what the resolver missed.
4. **Preserve existing behaviour.** With complete analysis and resolved edges, verdicts and the
   narrow command are byte-identical to before. This is asserted by a regression test, not assumed.
5. **Fixture for incomplete analysis.** A repository whose call graph cannot be fully resolved
   (dispatch through an interface, plus a file the parser rejects), asserting that the result is
   marked partial, the verdict is not presented as certain, and the fallback command is widened.

## Why the new result is safe

The tool now distinguishes what it *resolved* from what it *inferred* from silence, and it degrades
toward running more tests rather than fewer. A wrong `UNCOVERED` now costs the reader a source
check they were told to make; before, it cost them a false assurance they had no reason to doubt.
No verdict is removed and no previously narrow command widens unless the evidence behind it is
actually incomplete.
