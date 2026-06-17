# Phase 1 — Building the Stack

Each PR is delivered by **one PR-builder subagent** that drives the PR through the author's
own build pipeline by **invoking the `/build:*` skills** (via the Skill tool) in that PR's isolated
worktree, then opens the PR. The subagent does **not** re-implement these phases — it executes the
author's skills so the PR gets the author's exact workflow, prompts, and standards. The orchestrator
spawns these subagents, tracks them in `progress.md`, and routes any surfaced questions — it does
**not** run the cycle itself.

## The build cycle — invoke the author's `/build:*` skills (no ship confirmation)
In order, the subagent **calls each skill** and only falls back to doing the work in-prompt for a
phase that has no dedicated skill installed. If a single umbrella skill (e.g. `/build:build`) runs
the whole clarify→…→ship pipeline, prefer invoking that; otherwise run the phase skills in sequence:

| Phase | Action | Skill to invoke |
|-------|--------|-----------------|
| clarify | restate intent + this PR's acceptance criteria; list assumptions | (in-prompt, or `/build:build`'s clarify) |
| plan | implementation plan for this one PR's scope | **`/build:plan`** |
| execute | write code + tests (red-before-green), regen generated build files | **`/build:implement`** |
| simplify | cut to the minimum correct diff; remove orphans the change created | **`/build:improve`** |
| review | review the diff against the author's standards | author's review skill (e.g. a `code-review` / `review-pr` skill) |
| fix | address review findings (same worktree) | re-invoke `/build:implement` or fix in-prompt |
| validate | scoped build + tests green; verify this PR's acceptance criteria | **`/build:validate`** |
| ship | open the PR, set base correctly | **`/build:ship`** |

**Run `/build:ship` without asking for confirmation** — the author has pre-authorized ship for this
orchestrated flow. (If `/build:ship` itself has an interactive "ready to ship?" gate, the subagent is
authorized to proceed past it; record that it did so in `tradeoffs.md`.)

**Ship semantics (the handoff to Phase 2):**
- base = the parent branch (or `master` if this is the front PR).
- If base is `master` → arm auto-merge (`gh pr merge --auto`). If base is another feature branch →
  **do not** arm auto-merge (rule 6).
- Commit messages: `type(scope): imperative` + the author's co-author trailer. PR body is prose.
- Report back a digest: branch, tip hash, PR number, base, which acceptance criteria it claims, and
  any open question. The orchestrator records it and may immediately start the next dependent PR.

## Parallel fan-out vs pipeline
- **Independent PRs** (`depends-on: []`): spawn their PR-builder subagents in a single message so they
  run concurrently, each in its **own worktree** (rule 5 — never share a branch).
- **Dependent PRs**: a child may branch off the parent and scaffold, but its `execute`/`validate`
  can't truly pass until the parent's surface exists. Prefer to **start the child after the parent's
  surface is stable** (parent shipped or at least its API committed), then rebase the child onto the
  parent. Use `pipeline()`-style staging if you orchestrate via a Workflow script; otherwise spawn
  the child once the parent reports its API committed.

## Worktrees
One worktree per branch, named predictably (`<.worktrees>/<branch-with-slashes-as-underscores>`).
The subagent is told its exact worktree and **forbidden from touching any other**. For parallel
file-mutating agents, use isolated worktrees so they never collide.

## Surfacing questions during the build
If a PR-builder hits a real decision (ambiguous API shape, a behavior the design doesn't pin down),
it **stops that phase and returns the question** to the orchestrator. The orchestrator: adds a row to
the **pending-decisions table** (`questions.md`), shows the updated table to the author, asks
(`AskUserQuestion` or a tracking issue), and **pauses only that PR** — other PRs keep moving. Resume
the PR (via a fresh subagent with the answer) once the author responds, and **remove the row** from
the table. Never let a subagent invent the answer to keep moving (guardrail 2).

## When a PR-builder finishes
Orchestrator updates `progress.md` (PR opened, tip, base, criteria claimed), appends any trade-offs
the subagent reported to `tradeoffs.md`, any newly-discovered work to `follow-ups.md`, and decides
the next delegation (start a dependent PR; or, if this is the front PR on master, **start its monitor
loop** — Phase 2).
