---
name: stacked-pr-delivery
description: Use when given a design doc or a feature goal to deliver autonomously end-to-end as a stack of small PRs (API → utils → stitch). A lean orchestrator decomposes the goal, delegates every PR's full build cycle (clarify→plan→execute→simplify→review→fix→validate→ship) to subagents, runs a delegated monitor loop on the front PR (review comments, CI, staleness, conflicts), cascades changes through the stack, and auto-merges on genuine human approval — surfacing decisions to the author instead of making them. Invoke when the user says "deliver/implement/ship this design", "build out this feature as a stack", or hands you a design with a goal and a current→desired delta.
---

# Stacked PR Delivery

Drive a design or feature goal from intake to "all PRs merged" with **near-zero steering**. The
orchestrator that runs this skill is a **router, not a worker**: it holds the plan and the state
files, and delegates *all* execution to subagents so its own context stays lean and long-lived.

The [guardrails](references/guardrails.md) are the non-negotiable invariants of this workflow —
**read them before acting.**

## The one-paragraph model

You (orchestrator) turn a goal into an **acceptance-criteria list** and a **stacked PR plan**
(`api → utils → stitch`, smallest single-intent PRs). You spawn a **PR-builder subagent** per PR to
run the full build cycle and open the PR. You ensure exactly **one monitor loop** is running against
whichever PR currently sits on `master` (the front of the stack); each tick *delegates* detection +
remediation to subagents and reports back a digest. You never write code, never post a comment in
your own voice, never approve, and never make a design decision — you route work and surface
questions. You stop when every acceptance criterion is met and every PR is merged.

## When to use / not use

- **Use** for a multi-PR feature with a clear goal and a discoverable current→desired delta.
- **Don't use** for a single small change (just do it), or when the goal/delta is too vague to write
  acceptance criteria — first run `brainstorming` / ask the author to sharpen the goal.

## Operating principles (the orchestrator's contract)

1. **Delegate everything executable.** If a step writes code, runs tests, posts a comment, rebases,
   or reads more than a couple of files, it belongs in a subagent. The orchestrator reads digests,
   updates state files, and decides what to delegate next. Target: the orchestrator's own turns are
   short and mostly tool-routing. **Model:** spawn every subagent with the **default configured
   model** (inherit it — don't pin a tier); only override the model when the user has stated a
   preference for these subagents.
2. **State lives in files, never only in context.** Every tick and every PR is **idempotent and
   resumable** because the truth is on disk ([state files](references/state-files.md)). If the
   orchestrator is compacted or restarted, it reconstructs from files.
3. **One writer per branch, ever.** Never run two subagents that push the same branch at once.
   Serialize. (A concurrent double-push was a real near-miss.)
4. **Surface, don't decide.** Author design/scope calls get two things: a PR reply asking for input
   (marked, on the thread), **and** a row in the **pending-decisions table** the orchestrator keeps
   in `questions.md`. Never a unilateral decision. When unsure whether something is a fix or a
   decision, **treat it as a decision and surface it.** The orchestrator shows the author this table
   — **open decisions only** — whenever it changes; resolved decisions drop off the table.
5. **Never impersonate.** Every PR comment is prefixed `🤖 <agent> on behalf of <author>`.
6. **Approval is the only merge gate.** Auto-merge, armed only on master-base PRs, fires on a genuine
   human code-owner approval. Never self-approve, never `gh merge` directly.

## Workflow

### Phase 0 — Intake & decomposition  →  [references/decomposition.md](references/decomposition.md)
Input is either a full design doc **or** a goal + a way to find the current→desired delta. Produce,
in the project state dir:
- `goal.md` — the goal in one paragraph + **acceptance criteria** (a checklist that defines "done";
  this is your `/goal` and your final verification gate).
- `plan.md` — the stacked PR plan: ordered PRs, each with intent, scope, dependencies, and which
  layer it is. Decompose along the **interface-first** seam: `api → utils → stitch` — define the
  contract/types/surface first (plumbed but unconsumed), then the logic/helpers that operate on it,
  then the integration that wires it into the running system, then aggregation/telemetry. Mark which
  PRs are **independent** (parallelizable) vs **dependent** (pipelined). The breakdown is described
  in full in [decomposition.md](references/decomposition.md) — it's self-contained, no repo-specific
  files required.
- Initialize `progress.md`, `tradeoffs.md`, `follow-ups.md`, `questions.md`.
Decomposition is a planning act — do it yourself or via a single Plan subagent, then **get author
sign-off on `plan.md`** if the design left genuine ambiguity (use `AskUserQuestion`); otherwise
proceed with the smallest-PRs default and log the call in `tradeoffs.md`.

### Phase 1 — Build the stack  →  [references/pr-build-cycle.md](references/pr-build-cycle.md)
For each PR, spawn a **PR-builder subagent** that drives the PR through the author's build
pipeline by **invoking the `/build:*` skills directly** (`clarify → plan → execute → simplify →
review → fix → validate → ship`) in that PR's worktree, then opens the PR. The subagent does **not**
re-implement those phases — it calls the actual skills (`/build:plan`, `/build:implement`,
`/build:improve`, `/build:validate`, `/build:ship`, …). **No ship confirmation.** Parallelize
independent PRs (one subagent each, isolated worktrees); pipeline dependent ones (the API surface
must land before consumers compile). The subagent **surfaces blocking questions back to you** rather
than guessing; you route them to the pending-decisions table + the author and pause that PR only.

### Phase 2 — Monitor the front PR  →  [references/monitor-loop.md](references/monitor-loop.md)
The PR currently based on `master` (the front of the stack) gets a **delegated monitor loop**
(via `/loop`). The orchestrator's job is only to **ensure exactly one healthy loop is running** for
the front PR and to restart it if it dies — not to run ticks itself. Each tick:
1. Delegates **detection** (review comments on *both* endpoints incl. new replies, CI state,
   staleness, merge conflicts) to a scout subagent → returns a structured issue digest.
2. For each issue, delegates **remediation** to a fresh subagent (preserving context):
   obvious gaps get fixed + replied + the thread **resolved**; author decisions get a reply asking
   for input + an entry in `questions.md`, thread left **open**.
3. Re-verifies and **re-arms auto-merge** (it silently turns off); handles freshness/conflict
   rebases; **cascades** any content change into descendant PRs via subagents.
On approval → it auto-merges. On merge → the next PR retargets to `master`; **start its loop now**
and propagate the merge through the stack.

### Phase 3 — Converge & stop
When **all acceptance criteria in `goal.md` are checked** and **all PRs are merged**, run a final
verification subagent to confirm the delta is closed, then **STOP**: write `progress.md` →
"goal delivered — STOPPED", tear down the loop, and report. **Do not start the next design slice**
or invent scope — newly discovered work goes to `follow-ups.md`, not into this run.

## State files (single source of truth)  →  [references/state-files.md](references/state-files.md)
`goal.md` · `plan.md` · `progress.md` · `tradeoffs.md` · `follow-ups.md` · `questions.md` (the
**pending-decisions table**). Keep them current **every** tick and after **every** delegated step.
Surface the pending-decisions table to the author whenever a decision opens or closes.

## Stacked-PR mechanics  →  [references/stacked-mechanics.md](references/stacked-mechanics.md)
Rebase cascades (`--onto`), auto-merge rules, merge-queue method quirks, freshness rebases,
transient-401 retries — all the GitHub-stacking machinery the subagents need.

## Hard guardrails  →  [references/guardrails.md](references/guardrails.md)
The non-negotiable invariants. Read first. Violating one is a failure even if the task "works."
