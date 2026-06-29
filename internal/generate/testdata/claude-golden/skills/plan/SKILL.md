---
name: plan
description: "Batch 1: Plan — discuss requirements, design approach, write implementation plan"
argument-hint: "SLUG"
disable-model-invocation: true
---

# Plan — Batch 1: Planning

Interactive planning phase. Discuss requirements with the user, design an approach, and write a detailed implementation plan.

**Do NOT use Claude's built-in plan mode.** This command controls the planning flow directly.

## Setup

Read `$ARGUMENTS` as the project slug. Load project context:

```bash
SLUG="$ARGUMENTS"
PROJ="$HOME/.relay/active/$SLUG"
```

Read these files:
- `$PROJ/task.md` — the original task description
- `$PROJ/manifest.json` — current project state

Confirm the phase is `plan` or `discuss`. If not, tell the user which command to run instead.

`cd` into the worktree path from the manifest.

## Phase 1: Brainstorm

**Quick mode**: If the system prompt contains `Mode: quick`, skip brainstorming. Read the codebase to understand relevant patterns, then proceed directly to writing the plan.

Do NOT invoke a separate brainstorming skill. Follow the inline brainstorming flow below instead.

<HARD-GATE>
Do NOT write any code, scaffold any project, or take any implementation action until you have presented a design and the user has approved it. This applies to EVERY project regardless of perceived simplicity. Even "simple" projects get a design — it can be short (a few sentences), but you MUST present it and get approval.
</HARD-GATE>

### Understanding the idea

- Explore the current project state first (files, docs, recent commits)
- Before asking detailed questions, assess scope: if the request describes multiple independent subsystems, flag this immediately. Don't refine details of a project that needs decomposition first.
- If the project is too large for a single spec, help the user decompose into sub-projects. Each gets its own spec → plan → implementation cycle. Brainstorm the first sub-project.
- Ask questions one at a time to refine the idea
- Prefer multiple choice questions when possible, but open-ended is fine too
- Only one question per message
- Focus on understanding: purpose, constraints, success criteria

### Exploring approaches

- Propose 2-3 different approaches with trade-offs
- Present options conversationally with your recommendation and reasoning
- Lead with your recommended option and explain why

### Presenting the design

- Scale each section to its complexity: a few sentences if straightforward, up to 200-300 words if nuanced
- Ask after each section whether it looks right so far
- Cover: architecture, components, data flow, error handling, testing as relevant
- Be ready to go back and clarify if something doesn't make sense

### Design principles

- Break the system into smaller units with one clear purpose, well-defined interfaces, testable independently
- Explore the current structure before proposing changes. Follow existing patterns.
- Where existing code has problems that affect the work, include targeted improvements — no unrelated refactoring
- YAGNI ruthlessly — remove unnecessary features from all designs

### After design approval

Once the user approves the design:

1. Write the spec to `$PROJ/spec.md`
2. **Spec self-review** — look at the spec with fresh eyes:
   - **Placeholder scan:** Any "TBD", "TODO", incomplete sections, or vague requirements? Fix them.
   - **Internal consistency:** Do any sections contradict each other?
   - **Scope check:** Focused enough for a single implementation plan?
   - **Ambiguity check:** Could any requirement be interpreted two ways? Pick one and make it explicit.
   Fix any issues inline. No user gate here — just fix and move on.

## Phase 2: Write Plan

Invoke the `writing-plans` skill to create a detailed implementation plan:
- Numbered tasks with exact files to create/modify
- **Bake design decisions from spec.md into task descriptions** — each task should be self-contained so executors don't need to reference spec.md separately
- Size tasks so each can be completed within ~50% of an agent's context (2-3 tasks for complex work, up to 5 for simple work)
- Include a validation section with specific commands to verify correctness
- **Do NOT offer the execution handoff choice from writing-plans** — this command controls the next step

Save the plan to `$PROJ/plan.md`.

Update manifest and proceed directly to implementation:
```bash
relay update "$SLUG" --phase implement --status implementing --add phases_completed=plan,discuss --remove phases_remaining=plan,discuss
```

Then invoke `/implement $SLUG` to begin the implementation batch.
