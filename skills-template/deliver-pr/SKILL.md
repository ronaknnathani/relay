---
name: deliver-pr
description: Deliver one change end to end as a single pull request — clarify → plan → implement → simplify → review → validate → open-pr — resuming from wherever it left off. Use to take a task from intent to an open, mergeable PR with each phase run as a focused sub-agent. This is the default workflow the binary launches when you create a new project (`relay "<task>"`).
---

# Deliver PR

Drive one change from a task to an open PR by orchestrating the foundation skills, one phase at a time,
with durable resumable state. You are a **router, not a worker**: you read state, dispatch the next
phase as a sub-agent, record the result, and move on. You do not do the phase work yourself, and you
stay context-light — you read digests and state, never file dumps.

## Resume-first — always start here

`<slug>` is the argument this skill was invoked with — bind it to `$SLUG` before anything else. Every
invocation (first run or resume) then begins by asking the binary where this run is. State is owned by
`relay state`; never hand-edit it.

```bash
PHASE=$(relay state next "$SLUG" 2>/dev/null)
if [ $? -ne 0 ]; then                       # no state yet → first run: initialize, then ask again
  relay state init "$SLUG" --workflow deliver-pr \
    --phases "clarify,plan,implement,simplify,review,validate,open-pr"
  PHASE=$(relay state next "$SLUG")
fi
```

Never assume a fresh start: an interrupted run returns its in-progress phase and continues it. When
`relay state next` prints empty, every phase is done — go to **Done**.

## The phase pipeline

Each phase is a foundation skill. Run the one `relay state next` reports, in this order:

| Phase | Skill | Consumes | Produces |
|---|---|---|---|
| clarify | `clarify` | the task | requirements + acceptance criteria |
| plan | `plan` | requirements | a blueprint + phased build sequence |
| implement | `implement` | the plan | code + tests, green (commits as it goes) |
| simplify | `simplify` | the diff | a cleaner diff, behavior unchanged |
| review | `review` (report mode) | the diff + criteria | a severity-ranked findings report |
| validate | `validate` | the diff + criteria | a pass verdict on the repo's gates |
| open-pr | `open-pr` | the committed branch | an open PR |

## Per-phase loop (the router contract)

For the phase `relay state next` reported:

1. `relay state set "$SLUG" "$PHASE" in-progress`
2. **Dispatch a sub-agent** (when available; otherwise run inline) to run the `$PHASE` skill on this
   project. Hand it the task and the **upstream artifact only** — not your own conclusions. It does the
   work and returns a **structured digest**: what it produced, the artifact path, test/gate results,
   and any blocking question — never a file dump.
3. **On a blocking author-decision** (the sub-agent surfaces a real design/scope choice it shouldn't
   guess): surface it to the author (use an interactive prompt when available; otherwise write it to
   `questions.md` in the project dir, alongside `task.md`/`notes.md`, and stop). Do not advance. Resume
   when the author answers.
4. **On success:** `relay state log "$SLUG" "$PHASE done: <one-line digest>"`, then
   `PHASE=$(relay state advance "$SLUG")` — this marks the current phase done and prints the next one.
   If `PHASE` is empty, go to **Done**; otherwise loop back to step 1 with the new `PHASE`.

## Phase gates (where judgment applies)

- **After `plan`:** if the design left genuine ambiguity, get author sign-off on the plan before
  `implement`; otherwise proceed with the smallest-change default and log the call.
- **review → address:** run `review` in report mode. By the time `review` runs, `implement` and
  `simplify` are already marked done, so addressing findings means **reopening** the owning phase — the
  CLI allows a backward move. While `review` returns Critical or Important findings:
  `relay state set "$SLUG" implement in-progress` (or `simplify`), dispatch a sub-agent scoped to those
  findings, `relay state set "$SLUG" implement done`, then re-dispatch `review`. The `review` phase
  stays in-progress throughout — use explicit `set`, not `advance`, for the reopened phase. Suggestions
  are non-blocking. Only `advance` out of `review` once it is clean of Critical/Important.
- **No merge gate here.** `deliver-pr` ends at an *open* PR. Watching CI, handling review comments, and
  merging belong to `pr-monitor` / `stack-ship` — not this skill.

## Delegation contract

Every sub-agent prompt: name the worktree/branch, give it the task + the one upstream artifact, demand
a structured digest back (not prose, not file contents), and tell it to surface a blocking question
rather than guess. Keep yourself blind to file dumps — you route on digests and `relay state`.

## Done

When `relay state next "$SLUG"` is empty, run a final check that the PR is open and its acceptance
criteria are met, then **stop**. Report the PR URL (recorded via `relay state pr`). Newly discovered
out-of-scope work goes to follow-ups, not into this run — do not expand scope or start the next change.

## Red flags

- Doing a phase's work yourself instead of dispatching it (you are a router).
- Reading file contents into your own context instead of routing on digests.
- Hand-editing `state.json`/`progress.md` instead of using `relay state`.
- Advancing past `review` with Critical/Important findings unaddressed.
- Guessing an author decision instead of surfacing it and pausing.
- Assuming a fresh start instead of resuming from `relay state next`.
- Merging, or watching CI — that is `pr-monitor`/`stack-ship`, not `deliver-pr`.

## Verification checklist

- [ ] Started from `relay state next` (initialized state only if absent) — never assumed a fresh run.
- [ ] Each phase ran as a delegated sub-agent that returned a digest; state advanced via `relay state`.
- [ ] `plan` got author sign-off when the design was ambiguous.
- [ ] `review` ran and every Critical/Important finding was addressed before `validate`.
- [ ] `validate` passed on the repo's own gates before `open-pr`.
- [ ] Ended at an open PR with its URL recorded; stopped without expanding scope or merging.
