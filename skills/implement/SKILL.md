---
name: implement
description: Execute an implementation plan (from `plan`) task by task, writing the code and tests and leaving the system green at every step. Use once you have an agreed plan or a clear next slice of work to build. It does not plan and does not open a PR — it turns a plan into working, tested code.
---

# Implement

Turn an implementation plan into working, tested code, one thin slice at a time, keeping the build and
existing tests green at every step. The bar: at any moment you could stop and the system is committable
— nothing half-built, nothing red. This phase consumes the artifact from `plan`; it does **not** design
the approach and does **not** open a PR. Decisions in the plan are settled — follow them; if one is
wrong or missing, stop and surface it rather than quietly inventing a different design.

## Process

1. **Load the plan and review it critically.** Read it end to end before touching code. If a task is
   ambiguous, a step is missing, or the approach looks wrong, raise it now — don't paper over a gap
   mid-implementation. Turn the plan's tasks into an ordered checklist of slices.
2. **Learn the repo's own commands.** Read the Makefile, `package.json` scripts, or CI config to find
   the real build, typecheck, and test commands. Use those exact commands — never assume a toolchain
   the repo doesn't use.
3. **Pick the smallest complete slice.** A slice is the smallest change that is independently testable
   and leaves the system green — one task from the plan, often smaller. Do not start work on the
   default branch (main/master) without explicit consent.
4. **Tracer-bullet TDD — one test ↔ one cycle.** Write a single failing test for the slice's next
   behavior, watch it fail (red), write the minimum code to pass it (green), then refactor if needed.
   Never write all the tests up front, and **never refactor while a test is red** — get to green
   first. For a bug, use the **Prove-It** pattern: first write the reproduction test that fails
   *because* of the bug, then fix the code so it passes.
5. **Verify the slice.** Typecheck and run the targeted test files; confirm the slice's new test passes
   and no existing test regressed. The slice isn't done until the system is green again.
6. **Commit the green slice.** Commit a coherent, working increment (follow the repo's `commit`
   conventions). Keep the commit to this slice — don't fold in unrelated changes.
7. **Next slice.** Repeat 3–6 until every task in the plan is done. Hold scope: implement the plan, not
   adjacent improvements you notice along the way (note them for later).
8. **Final verification.** Run the full build and test suite once. This is implement's own green
   self-check, **not** the formal quality gate — `validate` independently runs the repo's full ordered
   gates and owns the authoritative go/no-go. Report what was built and which verifications passed.
   Stop here — implementation is complete; a separate workflow handles review and the PR.

## Cadence

Typecheck and the targeted test files run continuously — after every red→green→refactor cycle. The full
suite runs once at the end (and any time you suspect a broad regression). Stay in this loop; don't let
work pile up between verifications.

## When to stop and ask

Stop and surface the issue rather than guessing when you hit a real blocker: a missing dependency, a
plan step that can't be followed as written, an instruction you don't understand, or a verification
that fails repeatedly for a reason the plan doesn't cover. A plan gap is the planner's to resolve —
flag it; don't silently redesign around it.

## Red flags

- More than ~100 lines written without running a test — you've left tracer-bullet TDD.
- Refactoring while a test is red — get to green first, always.
- Writing all the tests up front instead of one failing test per cycle.
- A broken or red state left between slices — every commit must be green.
- Mixing unrelated changes into one slice or one commit.
- Touching files outside the plan's scope, or "improving" adjacent code while you're in there.
- Assuming a build/test command instead of using the repo's own (Makefile / package.json / CI).
- Inventing a different design because a plan decision seemed wrong — stop and surface it instead.

## Verification checklist

- [ ] Each slice was the smallest independently-testable increment, and the system was green after it.
- [ ] Every behavior was driven by a failing test first (Prove-It for bugs); no refactor happened while red.
- [ ] Targeted tests + typecheck ran continuously; the full suite passed once at the end.
- [ ] Build and existing tests were green between every slice — no red state was committed.
- [ ] Only files in the plan's scope were touched; no unrelated changes rode along.
- [ ] The repo's own build/test commands were used, not an assumed toolchain.
- [ ] Blockers and plan gaps were surfaced, not worked around by guessing.
- [ ] No PR was opened and no next skill was invoked — implementation just returned.
