---
name: implement
description: "Batch 2: Implement — execute plan, then auto-chains to improve and validate"
---

# Implement — Batch 2: Implementation

Runs the implement phase as a subagent with fresh context, then auto-chains to `/improve`.

## Setup

Read `$ARGUMENTS` as the project slug. Load project context:

```bash
SLUG="$ARGUMENTS"
PROJ="$HOME/.relay/active/$SLUG"
```

Read `$PROJ/manifest.json`. Confirm phase is `implement`. If the phase is past implement, redirect:
- `simplify`, `review`, or `fix`: tell the user to run `/improve $SLUG` instead.
- `validate`: tell the user to run `/validate $SLUG` instead.
- `rebase`, `pr`, `ci`, or `code-review`: tell the user to run `/ship $SLUG` instead.
- `done`: tell the user the project is complete.

Announce: "Implementing. Running: implement (then auto-chains to improve and validate)."

## Context Management Rules

- **Orchestrator stays lean (~10-15% context).** Pass file paths only to subagents — never paste file content into prompts.
- **Each subagent uses `model: "opus"`** for maximum context (1M).
- Subagents read plan.md and spec.md from disk themselves.
- Decisions in plan.md are non-negotiable (locked from planning phase).

## Phase: Implement

Skip if manifest shows this phase is already completed.

Launch a subagent (task tool) with this prompt:

> You are implementing a feature. Read these files for context:
> - Plan: `$PROJ/plan.md`
> - Task: `$PROJ/task.md`
> - Spec: `$PROJ/spec.md` (if it exists)
>
> Execute the plan using the `executing-plans` skill.
> Decisions in the plan are non-negotiable — do not deviate.
> Commit after meaningful chunks following the `commit` skill conventions.

Wait for completion. Update manifest:
```bash
relay update "$SLUG" --phase simplify --add phases_completed=implement --remove phases_remaining=implement
```

Then invoke `/improve $SLUG`.
