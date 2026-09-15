---
name: implement
description: Execute an implementation plan (from `plan`) task by task, writing the code and tests and leaving the system green at every step. Use once you have an agreed plan or a clear next slice of work to build. It does not plan and does not open a PR — it turns a plan into working, tested code.
---

# Implement

Turn the selected implementation input into working, tested code, one thin slice at a time, keeping the build and
existing tests green at every step. The bar: at any moment you could stop and the system is committable
— nothing half-built, nothing red. When `plan` was selected, consume its artifact. An unforced easy route uses
the explicit task, requirements, and `route.md` handoff, including its embedded findings or linked
fresh `exploration.md`; do not dispatch or repeat a separate exploration. A standard route may legitimately omit `plan`; then consume the
explicit task, route decision, requirements, and fresh exploration artifact instead. This phase does
**not** invent unresolved design decisions or open a PR.

Read the persisted route before starting. The route's review owner and validation owner determine who
records final evidence. The coordinator supplies the dispatch token returned by
`relay state dispatch`; never obtain or substitute another phase's token.

For a legacy seven-phase project with no persisted route, follow the recorded plan and original
seven-phase contract. Do not call `relay route refresh` or evidence commands; run the targeted checks
for each slice and the repository's full build/test suite once at the end, as legacy implementation
did.

## Process

1. **Load the selected inputs critically.** If `plan` is selected, read it end to end. On an easy
   route, use the task, requirements, and reusable exploration handoff in `route.md`; routing already performed the one allowed
   exploration. On another route without `plan`, also consume its fresh exploration artifact. If
   those selected inputs leave an unresolved design choice or omit a necessary step, surface the gap
   rather than inventing a design. Turn the settled work into an ordered checklist of slices.
2. **Load coding guidance.** Read the repo's `AGENTS.md` when present, then read global guidance from
   `~/AGENTS.md` or `~/.config/agents/AGENTS.md` when present. Repo guidance takes precedence over
   global guidance. Treat these files as implementation constraints for scope, style, tests, errors,
   comments, and workflow; if a plan conflicts with them, stop and surface the conflict before coding.
3. **Learn the repo's own commands.** Read the Makefile, `package.json` scripts, or CI config to find
   the real build, typecheck, and test commands. Use those exact commands — never assume a toolchain
   the repo doesn't use.
4. **Pick the smallest complete slice.** A slice is the smallest change that is independently testable
   and leaves the system green — one task from the plan, often smaller. Do not start work on the
   default branch (main/master) without explicit consent.
5. **Tracer-bullet TDD — one test ↔ one cycle.** Write a single failing test for the slice's next
   behavior, watch it fail (red), write the minimum code to pass it (green), then refactor if needed.
   Never write all the tests up front, and **never refactor while a test is red** — get to green
   first. For a bug, use the **Prove-It** pattern: first write the reproduction test that fails
   *because* of the bug, then fix the code so it passes.
6. **Verify the slice.** Typecheck and run the targeted test files; confirm the slice's new test passes
   and no existing test regressed. The slice isn't done until the system is green again.
7. **Commit the green slice.** Commit a coherent, working increment (follow the repo's `commit`
   conventions). Keep the commit to this slice — don't fold in unrelated changes.
8. **Next slice.** Repeat 4–7 until every task in the plan is done. Hold scope: implement the plan, not
   adjacent improvements you notice along the way (note them for later).
9. **Commit before final evidence.** Finish the green slice commits first. Evidence recorded before a
   commit or any other mutation is stale by definition.
10. **Reassess after the final mutation (adaptive only).** Content changes invalidate the prior risk
    assessment and gate policy. Re-run the route skill against the exact final snapshot. A plain
    `relay route refresh "$SLUG"` without that reassessment conservatively leaves the easy path. When
    the reassessment keeps the same easy execution contract, Relay rebinds the active implementation
    dispatch to the new route revision, so keep using the original dispatch token below. If the route
    escalates or changes owners, stop and let the coordinator dispatch the newly selected phase.
11. **Honor review and validation ownership.**
    - On an unforced easy route, inspect the exact final diff yourself without a separate review worker. Cover
      the mandatory review axes: correctness, acceptance-criteria compliance, scope/minimality, and
      clarity. Apply every route-selected specialist lens for tests, documentation/comments, type
      design, history, or repository guidelines in that same pass. Record all selected role
      identifiers, but do not launch a separate review worker.
    - Record passing review evidence only after the inspection:
      `relay state evidence record "$SLUG" review --result passed --artifact implementation.md
      --role <each-route-selected-role> --critical 0 --important 0 --suggestion <count>
      --dispatch-token "$DISPATCH_TOKEN"`.
    - Then run every gate in the route's exact required gate set once and record gate IDs, commands,
      and statuses
      with `relay state evidence record "$SLUG" validation --result passed --artifact
      implementation.md --gate <id>="<exact command>" --exit-status 0 ...
      --dispatch-token "$DISPATCH_TOKEN"`; use `--no-gates` only when the route records the verified
      no-gates policy. Relay rejects missing, extra, duplicate, or changed gates and persists only the
      ID, redacted display, exact digest, and exit status.
    - On every other route, run only targeted checks needed to keep each slice green. Leave independent
      review and the final full gate set to `review` and `validate`.
    A Critical/Important finding or failed easy gate must be recorded immediately; Relay escalates
    the route and reopens implementation plus independent review/validation. A blocked review or
    validation records its blocker category and reason and blocks its canonical evidence owner for
    coordinator recovery instead of fabricating a code failure. Do not overwrite failed evidence
    with implement-owned passing evidence after ownership changed.
12. **Freeze the evidence snapshot.** Do not edit, format, generate, or commit after recording easy
    evidence. Any post-implementation mutation invalidates freshness and requires route refresh,
    escalation, and independent phases.
13. **Return a structured digest.** Include changed files, behavior, commits, targeted checks, route
    class, actual diff size, review/validation ownership and evidence, and any blocker. Stop without
    opening a PR.

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
- Ignoring repo/global AGENTS.md guidance or silently choosing between conflicting instructions.
- Inventing a different design because a plan decision seemed wrong — stop and surface it instead.

## Verification checklist

- [ ] Each slice was the smallest independently-testable increment, and the system was green after it.
- [ ] Repo `AGENTS.md` and global `~/AGENTS.md` or `~/.config/agents/AGENTS.md` guidance were read when present and applied with repo guidance taking precedence.
- [ ] Every behavior was driven by a failing test first (Prove-It for bugs); no refactor happened while red.
- [ ] Targeted checks ran after every mutation; easy final review and gates ran only while implement remained both evidence owners.
- [ ] Easy review and validation evidence were recorded for the same final snapshot, with no later mutation.
- [ ] Build and existing tests were green between every slice — no red state was committed.
- [ ] Only files in the plan's scope were touched; no unrelated changes rode along.
- [ ] The repo's own build/test commands were used, not an assumed toolchain.
- [ ] Blockers and plan gaps were surfaced, not worked around by guessing.
- [ ] No PR was opened and no next skill was invoked — implementation just returned.
