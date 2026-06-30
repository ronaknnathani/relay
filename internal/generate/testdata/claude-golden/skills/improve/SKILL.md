---
name: improve
description: "Batch 2.5: Improve — simplify code and run review-pr, then fix issues"
argument-hint: "SLUG"
disable-model-invocation: true
---

# Improve — Batch 2.5: Improve

Runs simplify → review → fix as subagents with fresh context, then auto-chains to `/validate`.

## Setup

Read `$ARGUMENTS` as the project slug. Load project context:

```bash
SLUG="$ARGUMENTS"
PROJ="$HOME/.relay/active/$SLUG"
```

Read `$PROJ/manifest.json`. Confirm phase is in the improve batch (`simplify`, `review`, or `fix`). If the phase is `simplify`, `review`, or `fix`, resume at the current phase and skip earlier phases. If the phase is `validate`, tell the user to run `/validate $SLUG` instead. If the phase is `rebase`, `pr`, `ci`, or `code-review`, tell the user to run `/ship $SLUG` instead. If the phase is `done`, tell the user the project is complete.

Announce: "Improving. Running: simplify → review → fix."

## Context Management Rules

- **Orchestrator stays lean (~10-15% context).** Pass file paths only to subagents — never paste file content into prompts.
- **Each subagent uses `model: "opus"`** for maximum context (1M).
- Subagents read plan.md and spec.md from disk themselves.
- Decisions in plan.md are non-negotiable (locked from planning phase).

## Phase: Simplify

Skip if manifest shows this phase is already completed.

Launch a subagent (Agent tool, model: "opus") with this prompt:

> Your task: invoke the `simplify` skill, then commit results.
>
> 1. Call Skill(skill="simplify")
> 2. After the skill completes, run relevant test/build commands to confirm nothing broke
> 3. Commit changes via the commit skill (skip if no changes)
> 4. Report what changed

Wait for completion. Update manifest:
```bash
relay update "$SLUG" --phase review --add phases_completed=simplify --remove phases_remaining=simplify
```

## Phase: Review

Skip if manifest shows this phase is already completed.

Launch a subagent (Agent tool, model: "opus") with this prompt:

> Your task: invoke the `review-pr` skill and save findings.
>
> 1. Call Skill(skill="review-pr")
> 2. Aggregate the toolkit's findings into `$PROJ/review-summary.md`, with each issue rated Critical / Important / Minor and including file:line references where relevant
> 3. Report the summary file path

Wait for completion. Update manifest:
```bash
relay update "$SLUG" --phase fix --add phases_completed=review --remove phases_remaining=review
```

## Phase: Fix

Skip if manifest shows this phase is already completed.

Read `$PROJ/review-summary.md`. If it has no Critical or Important issues AND no `## Validation Failures` section, skip the subagent launch (no work to do).

Otherwise, Launch a subagent (Agent tool, model: "opus") with this prompt:

> Read `$PROJ/review-summary.md`.
> Fix all Critical and Important issues. If a `## Validation Failures` section is present, treat each failure listed there as a Critical issue and fix it as well.
> **Deviation rules**: Auto-fix bugs and missing critical functionality. Escalate architectural changes to the user.
> Re-run tests after each fix. Commit the fixes.

Wait for completion (or skip immediately if there was nothing to fix). Update manifest:
```bash
relay update "$SLUG" --phase validate --add phases_completed=fix --remove phases_remaining=fix
```

## Auto-chain

Invoke `/validate $SLUG`.
