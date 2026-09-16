---
name: deliver-pr
description: Deliver one change end to end as a single pull request using route-first, risk-proportional phases, conservative escalation, fresh review/validation evidence, and resumable project state.
---

# Deliver PR

Coordinate one change to an open PR. You route work and consume structured digests; phase workers own
their implementation. `route` and `open-pr` run inline. Selected middle phases run as focused
sub-agents when available.

## Goal and managed assignment

Set `/goal` to the user's requested outcome; this workflow is the execution method, not the goal.
Bind the invocation argument to `$SLUG`.

If `$HOME/.relay/projects/active/$SLUG/assignment.md` exists, read it fully and bind its `Program:` and
`Work item:` to `$PROGRAM` and `$ITEM`. Before routing and at every phase boundary, run:

```bash
relay program message inbox <program> <item> --json
```

Act on unread decisions, feedback, or instructions, then acknowledge each only after its action is
durable:

```bash
relay program message ack <program> <item> <inbox-id>
```

Keep an open-PR grant unread until the PR opens successfully. Never run `relay program decision open`.
A feedback message for an existing PR means make the change on the branch and pull request you already have,
not create another PR. After merge, the tech lead stops the watcher, sends `/exit`, and
the worktree is force-removed.

Managed child sessions always run under Herdr. If readiness fails, report Relay's exact instructions
and stop; a PR closed without merging requires a fresh grant and replacement PR. A standalone project
has no Herdr requirement.

## Resume and initialize

Start with `relay state next "$SLUG"`. If state exists, resume its recorded order. `relay resume`
rotates the trusted coordinator capability and gives the resumed coordinator a mode-0600 handoff
file; read it once, delete it immediately, and keep the token out of worker prompts. In particular, a
legacy seven-phase state has no `route`; do not insert phases, rewrite artifacts, or reclassify it.
Run that legacy order with its existing `set`/`advance` contract.

If state is absent, inspect `manifest.json` first. A manifest with no `delivery_mode` predates
adaptive delivery: initialize and run the legacy seven-phase order
`clarify,plan,implement,simplify,review,validate,open-pr` with `set`/`advance`. For an adaptive or
forced-full manifest, initialize the adaptive order and capture the returned
`coordinator_token`:

```bash
relay state init "$SLUG" --workflow deliver-pr \
  --phases "route,clarify,plan,implement,simplify,review,validate,open-pr"
```

Never hand-edit `state.json` or `progress.md`.

## Route first

For a new adaptive run, mark routing inline, invoke the `route` skill once, and finish it:

```bash
relay state dispatch "$SLUG" route --inline --coordinator-token "$COORDINATOR_TOKEN"
# run route inline; it invokes relay route classify and writes route.md
relay state finish "$SLUG" route done --artifact route.md --outcome material \
  --dispatch-token "$FINISH_TOKEN"
```

The route command durably marks unselected phases `skipped` with reasons. A forced-full manifest
selects all eight phases. A stack-candidate remains a conservative single-PR run while reporting the
`stack-ship` recommendation; never switch workflow ownership implicitly.

## Adaptive phase loop

Ask `relay state next "$SLUG"` after every state change.

- **Selected worker phase:** run
  `relay state dispatch "$SLUG" "$PHASE" --owner "$WORKER_ID" --coordinator-token
  "$COORDINATOR_TOKEN"` and capture its JSON capabilities. Dispatch exactly that skill with the task,
  route revision/digest, only its one-time scoped `finish_token` (`dispatch_token`), `review_token`,
  `validation_token`, or `route_token`, and fresh
  upstream artifact, then record its structured result with
  `relay state finish --dispatch-token "$FINISH_TOKEN"`. Reuse the same stable worker identity only
  when resuming the same worker; a replacement gets a new identity. Give workers the worktree/branch and require an artifact path, material/no-op
  outcome, checks, and blocking question; never ask for file dumps.
- **Other subagents:** only after classification, on non-easy or forced-full routes, record helpers not represented by a phase dispatch with
  `relay state worker "$SLUG" --task "<purpose>" --coordinator-token "$COORDINATOR_TOKEN"`.
  An unforced easy route launches no off-route helper; stale
  inputs require route refresh and escalation before more discovery.
- **`route`:** always inline. Reclassify from fresh facts after mutations; use
  `relay route refresh "$SLUG" --coordinator-token "$COORDINATOR_TOKEN"` only when the changed
  snapshot cannot be fully reassessed, which
  conservatively leaves the easy path. A full reassessment that keeps the same easy execution
  contract rebinds the active implementation dispatch to the new route revision instead of
  dispatching implementation again.
- **`open-pr`:** always inline. It consumes the shared commit/rebase contracts and must not launch
  another review.

An unforced easy route therefore has two selected delivery phases after routing, one worker dispatch
for `implement`, and no inter-worker handoff; coordinator-owned `open-pr` is inline. It qualifies only when current task and repository facts
satisfy every easy-path rule, including an exact required gate set or verified no-gates state.
Routing performs any needed exploration inline; while the route remains easy, do not launch helper,
`clarify`, `plan`, `simplify`, `review`, or `validate` workers. `implement` applies every selected
review lens itself and records review and exact-gate evidence for the exact final snapshot.
Relay rejects unselected, out-of-order, already terminal, or duplicate active dispatches before
changing state. Only the current blocked or escalated selected phase may be redispatched.

When a worker surfaces a genuine author decision, mark the phase `blocked` with a reason and stop. In
managed mode send the assignment's exact `question|conflict` message and never prompt the worker or
write `questions.md`. Otherwise surface the question or write `questions.md` in a non-interactive
session.

For a selected managed planning phase, use
`relay program message send <program> <item> --kind plan --body "<summary and review request>"` and
stop for the program response.

## Evidence and failure routing

`implement` owns targeted red/green checks. On unforced easy routes it also performs the proportional
correctness/criteria/scope/clarity review, runs the repository's final required gates, and records
both passing evidence records for the exact final snapshot. Other routes keep independent `review`
and `validate` owners.

The `implement`, `review`, and `validate` skills own their evidence formats and failure details.
Critical or Important findings and failed gates must be recorded before the coordinator escalates
and returns work to implementation. After a fix, refresh the route and rerun only evidence made stale
by the mutation; never advance to `open-pr` from failed or blocked evidence.
Blocked evidence retains its category and reason on the canonical evidence owner so the coordinator
can resolve authentication, tooling, or input failures without routing them to implementation as
code defects.

If actual scope crosses the easy limit, any closed risk appears, or previously recorded easy
evidence becomes stale, coordinator-authenticated `relay route refresh` escalates and reopens independent `review` and
`validate`. Do not replace those owners with new implement-owned evidence after escalation.
Every new dispatch by a canonical owner invalidates that owner's earlier evidence before work starts.

Before inline `open-pr`, require both commands to succeed for the current snapshot:

```bash
relay state evidence fresh "$SLUG" review
relay state evidence fresh "$SLUG" validation
```

Any mutation makes old evidence stale. `open-pr` performs no second review.
`relay state dispatch "$SLUG" open-pr --inline --coordinator-token "$COORDINATOR_TOKEN"`
independently enforces the same route, snapshot,
selected-role, exact-gate, and canonical-owner evidence contract.

## Managed open-PR gate

Before managed `open-pr`, inspect
`relay program message outbox <program> <item> --json`. Send at most one `pr-open` request. After a
grant arrives, keep it unread, run the exact
`relay program can-open-pr <program> <item>` command from the assignment, and proceed only if it
passes. Acknowledge the grant only after the open PR is verified and recorded with `relay state pr`.

## Watcher handoff and completion

Pass the active `open-pr` `result_token` to the `open-pr` skill. That skill records the verified PR
exactly once with
`relay state pr "$SLUG" --number <n> --url <url> --dispatch-token "$RESULT_TOKEN"` before it
returns. Do not call `relay state pr` again. Verify the durable result with `relay state next`.

After the PR is durably recorded, run exactly one watcher command. If the project has a managed-program
`assignment.md`, use managed mode; otherwise use standalone mode:

```bash
# standalone project only
relay pr watch start "$SLUG"

# managed program child only
relay pr watch start "$SLUG" --mode managed
```

Never try both modes. The watcher belongs to the worker, never the tech lead. `start` adopts an already-running watcher and
refuses before it creates anything unless ownership is unambiguous. Watcher startup failure must never fail the delivery;
report a warning and point to manual `/pr-monitor`. A `stack-ship` sub-agent must
not start a project watcher because the surrounding pane is not the project owner.

Stop when `relay state next` prints empty. Report the PR URL,
route class, worker count, and watcher status. Do not merge, poll CI, or expand scope.

## Red flags

- Reclassifying or rewriting a legacy seven-phase state.
- Dispatching a skipped phase or adding an off-route easy-path worker.
- Repeating broad exploration while its snapshot is fresh.
- Guessing a decision, masking a failed gate, or keeping an easy route after new risk.
- Opening a PR with stale/missing review or validation evidence.
- Acknowledging a managed grant before PR success.
- Treating watcher startup as part of the open-PR success condition.
