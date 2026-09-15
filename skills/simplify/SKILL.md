---
name: simplify
description: Simplify recently changed code without changing behavior, but only when the persisted route selects the phase from an author request or observable complexity evidence.
---

# Simplify

Run only when selected by the persisted route. Valid triggers are an author request, observable
duplication, generated churn, broader complexity, or a review finding that explicitly requests
cleanup. If none applies, the route skips this phase and records why the diff is already simple
enough.

A legacy seven-phase project has no persisted route and still runs its recorded simplify phase.
Use the same behavior-preserving scope and targeted checks, but do not call adaptive route or evidence
commands.

## Invariant

Preserve behavior. Work only in the current diff and never edit, consolidate, or delete tests. A
possible behavior correction belongs to implementation/review, not this phase.

## Process

1. Read the route trigger, changed diff, applicable guidance, and any scoped review finding. Do not
   broaden the task into a general refactor.
2. Look for:
   - unnecessary nesting or forwarding indirection;
   - misleading names;
   - duplicated logic;
   - dead code introduced by the change;
   - single-use abstractions that relocate rather than hide complexity;
   - generated churn that can be removed without editing generated outputs incorrectly.
3. Apply one coherent simplification at a time. Keep an abstraction when it concentrates complexity
   or protects a verified invariant. Reject dense rewrites that are shorter but harder to debug.
4. Run targeted checks covering each mutation. Do not rerun an unconditional baseline or full suite;
   final gates belong to the route's validation owner.
5. On an adaptive project, run `relay route refresh "$SLUG"` after the final mutation. The new snapshot automatically makes
   prior review or validation evidence stale.
6. Return changed files, the removed complexity, targeted checks, material/no-op outcome, and any
   behavior uncertainty. A no-op is valid when inspection confirms the trigger no longer applies.

## Red flags

- Running without a persisted trigger.
- Changing behavior, public contracts, or tests.
- Touching code outside the working diff.
- Removing a guard without proving its upstream invariant.
- Claiming fewer lines are inherently simpler.
