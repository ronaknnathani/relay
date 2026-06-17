# State Files — the single source of truth

All run state lives in a per-goal project dir (`~/.claude-build/projects/active/<goal-slug>/`). The
orchestrator updates these the instant something happens, so any tick/restart is idempotent and the
orchestrator's own context can stay lean (it reads digests, not history). Subagents are told which
file to append to and return only a digest.

## `goal.md` — definition of done
The goal in one paragraph + the **acceptance-criteria checklist**. This is `/goal` and the Phase-3
verification gate. Criteria must be machine-checkable by a subagent at the end. Tick boxes only when
a verification subagent confirms them — never on optimism.

## `plan.md` — the stack
Ordered PRs (`api → utils → stitch`), each with intent, scope, `depends-on`, branch/base, the
acceptance criteria it satisfies, and review-shape. Update bases as PRs merge and retarget.

## `progress.md` — append-only log
One line per material event: PR opened (tip/base), commit pushed (hash), rebase/cascade (old→new
tips), comment addressed (thread id), auto-merge armed/fired, PR merged, loop started/torn down.
Convert relative dates to absolute. This is what a restarted orchestrator reads to know where it is.

## `tradeoffs.md` — decisions, assumptions, deferrals the agent made
Every non-obvious call the agent made *on its own authority* (decomposition choices, conflict
resolutions, "smallest correct change" picks). Each entry: the decision, the why, and how to
reverse it. **If a call should have been the author's, it does NOT go here — it goes to
`questions.md`.** This file is the audit trail for "what did the agent assume."

## `follow-ups.md` — discovered work, NOT done in this run
Anything surfaced during implementation that's out of this goal's scope (a pre-existing bug noticed
in passing, a hardening idea, a latent edge case the current contract doesn't cover). Captured here
and stopped — **never auto-expanded into the current run** (guardrail 12). At the end, optionally
file these as todos/issues for the author.

## `questions.md` — the pending-decisions table (the human funnel)
The single place the author looks to see **what is waiting on them**. It is a **table that shows
only OPEN decisions** — once a decision is made, its row is removed (the made decision moves to
`tradeoffs.md` or `progress.md`). The author should be able to glance at this table and see exactly
the set of calls blocking the agent, nothing else.

The orchestrator **maintains this table and surfaces it to the author** whenever a decision opens or
closes (e.g. prints it at the end of a tick that changed it). Every open decision is *also* posted as
a marked reply on its PR thread (and that thread is left **unresolved** until answered) — the table
is the cross-PR rollup, the thread reply is the in-context ask. They stay in sync: answering the
thread closes the row.

Format:

```
# Pending author decisions (open only)

| # | PR | Thread / location | Decision needed | Options & agent's lean | Blocking? | Opened |
|---|----|-------------------|-----------------|------------------------|-----------|--------|
| 1 | #412 | auth/session.go:88 | session TTL: fixed or sliding? | A) fixed 24h (lean) B) sliding on activity | PR #412 paused | 2025-01-12 |
| 2 | #412 | auth/session.go:54 | reject or refresh an expired token? | A) reject 401 (lean) B) auto-refresh | non-blocking | 2025-01-12 |
```

Rules:
- **Open only.** Made decisions never appear here — they drop off the instant they're answered.
- One row per decision, attributed to the PR + exact location it came from.
- Mark whether it **blocks** that PR (the rest of the stack keeps moving regardless — guardrail 2).
- The agent may state its **lean** (recommended option) but must not act on it until the author
  confirms.
- Funnel ALL subagent questions here so the author is never pinged per-subagent.

## Invariants
- Write before you continue: never hold a decision/commit/answer only in context.
- One concept per file; never put state in the orchestrator's prose.
- These files + the live PRs/branches fully reconstruct the run. If they don't, you're keeping state
  in your head — fix that.
