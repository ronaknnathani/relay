---
name: simplify
description: Reduce complexity in recently changed code — cut needless abstraction, dead code, and duplication; improve names and structure — WITHOUT changing behavior. Quality only; it does not hunt for bugs (use review for that). Use after implementing a chunk of work, before review, or whenever a diff feels more complicated than the problem it solves. Triggers on "clean this up" / "tidy this" / "refactor this" when code already exists; for an underspecified goal with no code yet, use `clarify`.
---

# Simplify

Make a change simpler and clearer without changing what it does. The bar: readable, explicit code over
clever, compact code — would a senior engineer call this overcomplicated? Scope is the **recently
changed code** — the working diff (e.g. `git diff` against the branch point) unless the caller names
files. This is quality-only: it does not hunt for bugs (that's `review`).

## Step 0 — the invariant

**Preserve functionality. Never change what the code does, only how it does it.** Every original
feature, output, and behavior stays intact. If a simplification would alter behavior, it is a `review`
or implementation concern, not a simplification — stop and surface it.

## Scan — three buckets

Look at the changed code for:

- **Structural** — nesting depth ≥ 3, functions longer than ~50 lines, nested ternaries, boolean-flag
  parameters that select behavior, indirection that only forwards a call.
- **Naming** — `data`/`result`/`temp`/`foo`, names that mislead, names that imply something broader or
  different than the thing actually is.
- **Redundancy** — duplicated logic, dead/unreachable code, unused parameters/variables/fields,
  over-abstraction (a helper, wrapper, or interface used in exactly one place), defensive code for
  states an upstream invariant already makes impossible.

## The deletion test

For each abstraction or layer of indirection, ask: would removing it **concentrate** complexity
(then keep it — it's earning its keep) or merely **relocate** it (then remove it — it's not)? A
single-use helper usually relocates; inline it. A wrapper that hides a genuinely complex subsystem
behind a small interface usually concentrates; keep it.

## Two-sided guardrail

Simplifying is a balance, not a race to fewer lines. Pair every "look for" with a "don't":

| Look for | Don't |
|---|---|
| reduce nesting and indirection | introduce a nested ternary or a dense one-liner |
| inline a single-use helper | inline a helper that names a genuinely complex step |
| remove a redundant abstraction | remove a helpful abstraction that organizes the code |
| consolidate duplicated logic | merge unrelated concerns into one function |
| clearer names, fewer obvious comments | make the code harder to debug or extend |

If the "simplified" version is longer or harder to follow, it isn't a simplification — revert it.

## Process

1. **Establish a baseline.** Run the tests covering the change — or, if there is no suite or it cannot
   run, the narrowest available behavior check (build, typecheck, or lint) — and confirm it is green
   before you start. Never fabricate a green result; if the baseline is already red for unrelated
   reasons, note that and rely on the narrower check.
2. **Understand before changing (Chesterton's Fence).** Work out *why* the code is shaped the way it
   is — a guard, retry, or special case may exist for a real reason. Remove a guard only when its
   premise is verifiable in the code you can see; if confirming it needs reasoning about whether an
   upstream invariant truly holds, leave it and flag for `review`.
3. **One simplification at a time** → re-run that check → keep it only if it stays green; revert
   immediately if not. Tests are necessary but not sufficient: for a change on a path the suite does
   not exercise, reason explicitly about input/output equivalence — error paths, ordering, side
   effects, short-circuit evaluation — before keeping it.
4. **Tests are out of scope.** Never edit a test to make it pass — a breaking test means behavior
   changed, so revert the simplification, not the test. Do not refactor, consolidate, or delete tests
   or fixtures even when they look redundant; that silently weakens coverage.
5. **Keep the simplify diff separate** from feature/fix changes, so a reviewer reads pure refactor.
6. **Verify functionality preserved** at the end: the baseline check is green and you have reasoned
   about any untested paths you touched.

## Red flags

- The simplified code is longer, or harder to follow, than before.
- Editing, consolidating, or deleting a test or fixture (tests are out of scope).
- Calling behavior "unchanged" from a passing suite alone, on code the suite does not exercise.
- Removing an abstraction or guard without applying the deletion test / Chesterton's Fence.
- Touching code outside the working diff ("while I'm here…").
- Optimizing for fewer lines at the cost of clarity.

## Verification checklist

- [ ] The baseline check (tests for the changed code, or build/typecheck/lint) is green before and after — never a fabricated green.
- [ ] For any change on a path the suite does not cover, I reasoned about input/output equivalence, not just "tests pass".
- [ ] No test or fixture was edited, consolidated, or deleted — tests stayed out of scope.
- [ ] Every removed abstraction/guard passed the deletion test (and Chesterton's Fence for guards).
- [ ] The change stayed within the working diff.
- [ ] The diff is genuinely clearer — not merely shorter.
