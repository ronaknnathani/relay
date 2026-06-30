---
name: stack-ship
description: Use when given a design doc or feature goal to deliver end-to-end as a stack of small PRs, or to resume an active stack. Decompose into acceptance criteria and an interface-first PR plan, delegate build and monitor work, keep durable state, use native loop/goal harnesses when available, use Copilot monitor ticks when not, surface author decisions, cascade stack changes, auto-merge only on human approval, and stop when the goal is delivered.
---

# Stack Ship

Drive a design or feature goal from intake to "all PRs merged" with **near-zero steering**. The
orchestrator is a **router, not a worker**: it owns the plan, state, delegation, and author-facing
decisions; subagents own execution and return structured digests.

The [guardrails](references/guardrails.md) are non-negotiable — **read them before acting.**

> Throughout this skill `master` denotes the repository's **default branch**; substitute the real
> default (`main`, etc.). Auto-merge is armed only on a default-branch-based PR — which, in a stack, is
> exactly the front PR.

## The one-paragraph model

You (orchestrator) turn a goal into an **acceptance-criteria list** and a **stacked PR plan**
(`api → utils → stitch`, smallest single-intent PRs). You spawn a **`deliver-pr` sub-agent** per PR to
run the full single-PR pipeline and open the PR. You drive whichever PR currently sits on `master` (the
front of the stack) to merged with the **`pr-monitor`** skill — a native `/loop` when available, else
one tick per resume — and add only the stack-specific front-advance and cascade. You never write code,
never post in your own voice, never
approve, never make author-owned design decisions, and never install unreviewed tooling — you route
work and surface questions. You stop when every acceptance criterion is met and every PR is merged.

## When to use / not use

- **Use** for a multi-PR feature with a clear goal and a discoverable current→desired delta.
- **Use again / resume** for an active stacked delivery run. If a native loop is unavailable (for
  example in Copilot), the skill automatically reconstructs state and runs one monitor tick for the
  current front PR — the author should not have to type a special "monitor-tick mode" prompt.
- **Don't use** for a single small change (just do it), or when the goal/delta is too vague to write
  acceptance criteria — first run `brainstorming` / ask the author to sharpen the goal.

## Operating principles (the orchestrator's contract)

1. **Delegate everything executable.** If a step writes code, runs tests, posts a comment, rebases,
   opens/updates PRs, posts GitHub comments, resolves threads, or reads more than a couple of files,
   it belongs in a subagent. The orchestrator reads digests, updates state files, and decides what to
   delegate next. **Model:** spawn every subagent with the **default configured model** (inherit it —
   don't pin a tier); only override the model when the user has stated a preference for these
   subagents.
2. **One state writer.** The orchestrator is the canonical writer for the run's state files. Worker
   subagents return structured digests; they do not append to shared state files directly. If a
   delegated loop controller owns a tick, it assumes this same singleton state-writer role for that
   tick.
3. **State lives in files, never only in context.** Every tick and every PR is **idempotent and
   resumable** because the truth is on disk ([state files](references/state-files.md)): human-readable
   Markdown plus machine-readable `state.json`.
4. **One writer per branch, ever.** Never run two subagents that push the same branch at once.
   Serialize. (A concurrent double-push was a real near-miss.)
5. **Surface, don't decide.** Author design/scope calls get two things: a PR reply asking for input
   (marked, on the thread), **and** a row in the **pending-decisions table** the orchestrator keeps
   in `questions.md`. Never a unilateral decision. When unsure whether something is a fix or a
   decision, **treat it as a decision and surface it.** The orchestrator shows the author this table
   — **open decisions only** — whenever it changes; resolved decisions drop off the table.
6. **Never impersonate.** Every PR comment is prefixed `🤖 <agent> on behalf of <author>`, and PR
   bodies/status posts disclose agent authorship instead of implying the author wrote them.
7. **Approval is the only merge gate.** Auto-merge, armed only on master-base PRs, fires on a genuine
   human code-owner approval. Never self-approve, never `gh merge` directly.
8. **Approved tooling only.** Use only approved/verified skills, hooks, MCP integrations, and tools
   already available in the environment. Never install or run unreviewed third-party
   plugins/hooks/scripts from the internet during this workflow.
9. **Use the best native harness.** Detect runtime capabilities once and record them in `state.json`.
   If `/goal` or `/loop` exists, use it; never downgrade Claude/Codex-style native primitives to a
   fallback because another runtime lacks them. If no native loop exists, use monitor-tick mode
   automatically on resume and be honest that coverage is tick-based, not continuous.

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
- Initialize `state.json`, `progress.md`, `tradeoffs.md`, `follow-ups.md`, `questions.md`. Record
  runtime capabilities in `state.json` (`/goal`, `/loop`, scheduler, monitor mode) before building.
Decomposition is a planning act — do it yourself or via a single Plan subagent, then **get author
sign-off on `plan.md`** if the design left genuine ambiguity (use `ask_user`); otherwise
proceed with the smallest-PRs default and log the call in `tradeoffs.md`.

### Phase 1 — Build the stack  →  [references/pr-build-cycle.md](references/pr-build-cycle.md)
For each PR, spawn a **`deliver-pr` sub-agent** that runs the full single-PR pipeline
(`clarify → plan → implement → simplify → review → validate → open-pr`) in that PR's worktree and opens
the PR. You do **not** re-implement those phases — `deliver-pr` owns them; you hand it this PR's intent,
scope, acceptance criteria, branch, and base. Parallelize independent PRs (one sub-agent each, isolated
worktrees); pipeline dependent ones (the API surface must land before consumers compile). `deliver-pr`
**surfaces blocking questions back to you** rather than guessing; you route them to the
pending-decisions table + the author and pause that PR only.

### Phase 2 — Monitor the front PR  →  [references/monitor-loop.md](references/monitor-loop.md)
The PR currently based on `master` (the front of the stack) is driven to merged by the **`pr-monitor`**
skill — it owns the tick routine (detect → delegate remediation to `pr-fix` → reconcile → re-arm
auto-merge → stop at merge), in native-loop mode or one tick per resume. Run exactly one `pr-monitor`
against the front PR at a time; never monitor a non-front PR as a merge candidate (it can't merge yet).

You add only the two **stack-specific** parts `pr-monitor` deliberately leaves out:
- **Front-advance:** when the front PR merges, explicitly rebase/retarget the next PR onto `master`,
  verify descendant base refs did not collapse, then point `pr-monitor` at the new front PR.
- **Cascade:** after any content change to a PR that has descendants, rebase each descendant
  (`git rebase --onto <new-tip> <old-tip> <descendant>`), build+test, force-push, and verify base refs.

Author decisions that `pr-fix` surfaces are routed to the pending-decisions table + the author. See
[stacked-mechanics.md](references/stacked-mechanics.md) for the rebase/auto-merge machinery.

### Phase 3 — Converge & stop
When **all acceptance criteria in `goal.md` are checked** and **all PRs are merged**, run a final
verification subagent to confirm the delta is closed, then **STOP**: write `progress.md` →
"goal delivered — STOPPED", tear down the native loop or mark monitor-tick mode stopped, and report.
**Do not start the next design slice** or invent scope — newly discovered work goes to
`follow-ups.md`, not into this run.

## State files (single source of truth)  →  [references/state-files.md](references/state-files.md)
`goal.md` · `plan.md` · `state.json` · `progress.md` · `tradeoffs.md` · `follow-ups.md` ·
`questions.md` (the **pending-decisions table**). Keep them current **every** tick and after
**every** delegated step. Surface the pending-decisions table to the author whenever a decision opens
or closes; record closed decisions durably outside `questions.md`.

## Stacked-PR mechanics  →  [references/stacked-mechanics.md](references/stacked-mechanics.md)
Rebase cascades (`--onto`), auto-merge rules, merge-queue method quirks, freshness rebases,
transient-401 retries — all the GitHub-stacking machinery the subagents need.

## Hard guardrails  →  [references/guardrails.md](references/guardrails.md)
The non-negotiable invariants. Read first. Violating one is a failure even if the task "works."
