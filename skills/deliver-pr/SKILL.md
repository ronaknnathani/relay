---
name: deliver-pr
description: Deliver one change end to end as a single pull request — clarify → plan → implement → simplify → review → validate → open-pr — resuming from wherever it left off. Use to take a task from intent to an open, mergeable PR with each phase run as a focused sub-agent. This is the default workflow the binary launches when you create a new project (`relay "<task>"`).
---

# Deliver PR

Drive one change from a task to an open PR by orchestrating the foundation skills, one phase at a time,
with durable resumable state. You are a **router, not a worker**: you read state, dispatch the next
phase as a sub-agent, record the result, and move on. You do not do the phase work yourself, and you
stay context-light — you read digests and state, never file dumps.

## Session goal

In every agent harness, set `/goal` to the user's requested outcome. The `deliver-pr` workflow is the
execution method, not the goal. Keep `task.md`, requirements, and `relay state` as the durable
definition and progress record across resumes.

## Managed assignment — before state routing

Bind the invocation argument to `$SLUG`, then check
`$HOME/.relay/projects/active/$SLUG/assignment.md`. If it exists, read it completely before asking
`relay state` for the next phase. This is a managed program worker: the assignment's contracts and
escalation commands are binding, while the worker still owns independent `clarify` and `plan` work.
Bind the exact `Program:` and `Work item:` values from the assignment to `$PROGRAM` and `$ITEM`, then
run and read:

```bash
relay program message inbox <program> <item> --json
```

Use the exact program and item from the assignment in place of the placeholders. Act on every unread
decision, feedback, or instruction before state routing. Acknowledge each message only after its
requested action or resulting state/artifact update is durable:

```bash
relay program message ack <program> <item> <inbox-id>
```

An open-PR grant instruction is the exception: keep it unread until `open-pr` succeeds and the PR is
recorded. If `open-pr` fails, do not acknowledge the grant.

Herdr notification is only a payload-free doorbell and may be lost, so this inbox check is mandatory
even when no prompt arrived. Managed child sessions always run under Herdr: if `relay resume` reports
a Herdr readiness failure, report its exact setup or start instructions and stop instead of working
outside Herdr. If `assignment.md` does not exist, this is a standalone project: follow the standalone
path below exactly as before, with no Herdr requirement.

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

1. For a managed assignment, run and process
   `relay program message inbox <program> <item> --json` again at the top of every loop. Use the exact
   program and item from `assignment.md`, and acknowledge each message only after its action is
   durable.
2. If `PHASE` is `open-pr` and this is a managed assignment:
   - Inspect `relay program message outbox <program> <item> --json`. If an unread `pr-open` request
     exists and no grant-approved inbox instruction exists, stop without sending another request.
   - If neither a request nor grant-approved instruction exists, send exactly one `pr-open` message
     using the assignment's command and stop.
   - Only after reading the tech lead's grant-approved inbox instruction, leave that message unread and
     run
     the exact `relay program can-open-pr <program> <item>` command from `assignment.md`. If it fails,
     keep `open-pr` pending and stop. If it passes, continue to the `open-pr` phase.
   - If a previously recorded pull request was closed without merging, Relay clears the stale reference
     during `program tick`. Request a fresh `pr-open` grant and open a replacement pull request through
     the same gate; never reopen or reuse the closed reference yourself.
3. `relay state set "$SLUG" "$PHASE" in-progress`
4. **Dispatch a sub-agent** (when available; otherwise run inline) to run the `$PHASE` skill on this
   project. Hand it the task and the **upstream artifact only** — not your own conclusions. It does the
   work and returns a **structured digest**: what it produced, the artifact path, test/gate results,
   and any blocking question — never a file dump.
5. **On a blocking author-decision** (the sub-agent surfaces a real design/scope choice it shouldn't
   guess): surface it to the author (use an interactive prompt when available; otherwise write it to
   `questions.md` in the project dir, alongside `task.md`/`notes.md`, and stop). Do not advance. Resume
   when the author answers. For a managed assignment, never prompt the worker or write
   `questions.md`: run the exact `relay program message send <program> <item> --kind
   question|conflict --body ...` command from `assignment.md`, then stop. Contract, scope,
   dependency, and risk conflicts stop the affected work; tech lead-worker conflicts escalate to the CEO.
   Never run `relay program decision open` or otherwise write program state.
6. **On success:** for a managed `open-pr`, first verify the PR is open and recorded, then acknowledge
   the grant-approved inbox message. Never acknowledge it after a failed `open-pr`. Next run
   `relay state log "$SLUG" "$PHASE done: <one-line digest>"`, then
   `PHASE=$(relay state advance "$SLUG")` — this marks the current phase done and prints the next one.
   If `PHASE` is empty, go to **Done**; otherwise loop back to step 1 with the new `PHASE`.

## Phase gates (where judgment applies)

- **After `plan`:** if the design left genuine ambiguity, get author sign-off on the plan before
  `implement`; otherwise proceed with the smallest-change default and log the call. In managed mode,
  run the assignment's exact
  `relay program message send <program> <item> --kind plan --body "<describe the plan and requested review>"`
  command and stop instead of requesting interactive approval. Standalone behavior is unchanged.
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

## After the PR is open — hand it to the watcher

`deliver-pr` still **ends at an open PR**. The watcher is a follow-on service, not another phase: it
observes the PR and wakes this project's session when it needs attention, so nobody has to poll.

Once `open-pr` succeeded and the PR is recorded (`relay state pr`), start or adopt it:

```bash
relay pr watch start "$SLUG"                  # standalone project
relay pr watch start "$SLUG" --mode managed   # managed program worker (owner is this worker, never the tech lead)
```

`start` requires Herdr and adopts an already-running watcher, so running it twice is safe. It wakes
**one exact live session** — the pane whose Relay title names this project, which is this session.

`start` **refuses before it creates anything** unless exactly one live session carries that identity:
zero owners or two owners means a watcher would either hand its work to nobody or not know whom to
wake, so no tab and no process are created. `--mode managed` additionally verifies this project really
is a program work item — a readable `assignment.md`, and a program work item that names this project
back — before creating anything.

**A watcher-start failure must never fail the delivery.** The PR is open and recorded; that is the
phase's outcome. Report the exact failure as an actionable warning and say that `/pr-monitor` can be
run manually instead — it works with no watcher and no Herdr, via `relay pr watch tick <slug> --json`.

Two cases where you deliberately do **not** start one:

- **A standalone run with no Herdr pane.** It cannot host a watcher. Say so and point at the manual
  `/pr-monitor` fallback. Nothing else about the standalone path changes.
- **Running as a `stack-ship` sub-agent.** The surrounding pane belongs to the stack orchestrator, not
  to this project, so no live session is titled for this project and a watcher started here would wake
  nobody. `start` refuses before creating a tab, so no orphan watcher can exist; treat that refusal as
  a skip with a warning and let the orchestrator start the front watcher itself with
  `--mode stack --owner <stack-slug>`.

## Done

When `relay state next "$SLUG"` is empty, run a final check that the PR is open and its acceptance
criteria are met, then **stop**. Report the PR URL (recorded via `relay state pr`) and whether a
watcher is running for it. Newly discovered out-of-scope work goes to follow-ups, not into this run —
do not expand scope or start the next change.

## Red flags

- Doing a phase's work yourself instead of dispatching it (you are a router).
- Reading file contents into your own context instead of routing on digests.
- Hand-editing `state.json`/`progress.md` instead of using `relay state`.
- Advancing past `review` with Critical/Important findings unaddressed.
- Guessing an author decision instead of surfacing it and pausing.
- Skipping the managed inbox check because no Herdr doorbell arrived.
- Calling `program decision open`, prompting the worker, or writing `questions.md` in managed mode.
- Sending duplicate `pr-open` requests instead of checking the unread worker outbox.
- Acknowledging an open-PR grant before the PR is successfully opened and recorded.
- Assuming a fresh start instead of resuming from `relay state next`.
- Failing the delivery because `relay pr watch start` failed — the open, recorded PR is the outcome.
- Starting a watcher from inside a `stack-ship` sub-agent, where it would wake nobody.
- Merging, or watching CI yourself — that is the watcher plus `pr-monitor`/`stack-ship`, not `deliver-pr`.

## Verification checklist

- [ ] Managed runs checked the durable inbox before state routing and at every phase loop.
- [ ] Managed messages were acknowledged only after their actions became durable.
- [ ] Managed `open-pr` sent at most one pending request and kept its grant unread until PR success.
- [ ] Started from `relay state next` (initialized state only if absent) — never assumed a fresh run.
- [ ] Each phase ran as a delegated sub-agent that returned a digest; state advanced via `relay state`.
- [ ] `plan` got author sign-off when the design was ambiguous.
- [ ] `review` ran and every Critical/Important finding was addressed before `validate`.
- [ ] `validate` passed on the repo's own gates before `open-pr`.
- [ ] After `open-pr`, `relay pr watch start` ran once (or was skipped with a stated reason), and any failure was reported as a warning rather than failing the delivery.
- [ ] Ended at an open PR with its URL recorded; stopped without expanding scope or merging.
