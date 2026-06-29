---
name: ship
description: "Batch 3: Ship — rebase, create PR, pass CI, code review"
---

# Ship — Batch 3: Shipping

Automatic shipping pipeline. Rebase, create PR, pass CI, and run code review.

## Setup

Read `$ARGUMENTS` as the project slug. Load project context:

```bash
SLUG="$ARGUMENTS"
PROJ="$HOME/.relay/active/$SLUG"
```

Read `$PROJ/manifest.json`. Confirm phase is in the ship batch (`rebase`, `pr`, `ci`, or `code-review`). Resume at the current phase if ahead of `rebase`.

Announce: "Shipping. Running: rebase → create PR → CI → code review."

## Phase: Rebase

Skip if manifest shows this phase is already completed.

Invoke the `rebase` skill to rebase the branch onto `main` (or the base branch). This ensures clean history before the PR.

Update manifest:
```bash
relay update "$SLUG" --phase pr --add phases_completed=rebase --remove phases_remaining=rebase
```

## Phase: Create PR

Invoke the `submit` skill to create the PR.

The PR body should follow these conventions:
- **Summary**: Concise prose paragraphs explaining why and what (not how — the diff shows that)
- **Testing Done**: List the commands that were run

Read `$PROJ/plan.md` and `$PROJ/verification.md` for context when writing the PR description.

Record the PR in the manifest:
```bash
relay update "$SLUG" --phase ci --pr.number <NUMBER> --pr.url "<URL>" --add phases_completed=pr --remove phases_remaining=pr
```

## Phase: CI Checks

Launch a subagent (task tool):

> Run the `pr-check` skill to check CI status for the PR.
> If CI failures: examine the failure logs, fix issues locally, commit and push, re-check.
> Loop until all checks pass. Max 3 attempts.
> Report final CI status.

Wait for completion. Update manifest:
```bash
relay update "$SLUG" --phase code-review --pr.ci_status passing --add phases_completed=ci --remove phases_remaining=ci
```

If CI cannot be fixed after 3 attempts, report the failures to the user and stop.

## Phase: Code Review

Launch a subagent (task tool):

> Invoke the `code-review` skill to review the PR.
> If issues are found: fix them, commit and push.
> After fixing, re-check CI passes.
> Loop until the code review is clean. Max 3 attempts.

Wait for completion. Update manifest:
```bash
relay update "$SLUG" --status complete --phase done --add phases_completed=code-review --remove phases_remaining=code-review
```

## Done

Report to the user:

```
Build complete! PR is up and CI is passing.
  PR: <URL>

Run `relay archive <SLUG>` when the PR is merged.
```
