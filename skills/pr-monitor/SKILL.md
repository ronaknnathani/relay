---
name: pr-monitor
description: Handle one pull request attention event end to end — read the watcher digest, triage it, delegate the fixing to pr-fix, acknowledge the digest, and exit. Use when the PR watcher wakes you, or run it manually for a one-shot check of an open PR.
---

# PR Monitor

Handle **one** pull request attention event, then stop. You are a **router**: the `relay pr watch`
runtime does the observing, you interpret its digest, delegate the fixing to `pr-fix`, record what you
covered, and exit. You never write code, never post in your own voice, never approve, and never merge.

This skill has **no loop**. It does not schedule, does not recur, and does not own a next-tick time.
The watcher owns cadence; one run of this skill owns one digest.

## Where the work comes from

**Woken by the watcher.** The prompt names the project and a fingerprint:
`Run pr-monitor once for project <slug> using watcher fingerprint <fp>.` Bind them to `$SLUG` and
`$FP`, then read the immutable digest that fingerprint names:

```bash
relay pr watch digest "$SLUG" --fingerprint "$FP" --json
relay pr watch status "$SLUG" --json     # current fingerprint, cadence, last wake, suppression
```

**Invoked manually, or with no Herdr.** Observe first, then act on what that observation recorded:

```bash
relay pr watch tick "$SLUG" --json       # fresh read-only observation; records an immutable digest
```

`tick` works with no watcher running and never changes the watcher's schedule. If it returns no
actionable items, there is nothing to do — say so and stop.

## What the digest already decided

The digest is deterministic and complete; do not re-derive it with your own `gh` sweep. It carries the
pull request state, draft flag, base and head refs and SHAs, `mergeStateStatus`, `mergeable`,
`reviewDecision`, whether auto-merge is armed, every check with its status, conclusion, and run id, and
every actionable item with its `source`, `id`, `updatedAt`, `body`, thread id, file, and line.

Each item carries a `reason`:

| Reason | What it means | Who acts |
|---|---|---|
| `failing-check` | a check concluded failure, error, timeout, action-required, or startup-failure | you classify flake vs real; real → `pr-fix` |
| `changes-requested` | the review decision is CHANGES_REQUESTED | `pr-fix` |
| `new-comment` / `new-review` / `new-inline-comment` | human activity the agent has not answered | `pr-fix` |
| `unresolved-thread` | an unresolved thread, or a new human reply after the agent answered | `pr-fix` |
| `merge-conflict` | the branch conflicts with its base | `pr-fix` |
| `stale-base` | the branch is behind its base | you rebase |
| `auto-merge-not-armed` | approved, green, clean, based on the default branch, not armed | you arm it |
| `closed-unmerged` | the pull request was closed without merging | escalate to the author |
| `stack-front-merged` | the stack's front pull request merged | the stack orchestrator retargets |

Fetch more only for the specific item you are acting on — a failed run's log, a file's history. Never
re-page the whole comment history; the watcher already did, and its digest is the record.

## The run

### 1. Triage

Split the items into what you own and what `pr-fix` owns.

**You classify each `failing-check` as an infra flake or a real failure.** An infra flake is a
dependency download, TLS timeout, registry 5xx, or sandbox limit — inspect the failed run
(`gh run view <run-id> --log-failed`, using the run id the digest carries) before deciding. Everything
else is real.

### 2. Delegate the real work to `pr-fix` — one writer, one call

Hand `pr-fix` **one** scoped worklist as a sub-agent: the real check failures with their names and run
ids, the comment, review, and thread items **with the bodies, ids, thread ids, files, and lines the
digest already carries**, and any merge conflict. Tell it that it is running in **delegated mode**: the
worklist is complete, it must not re-fetch the broad pull request context and must not loop, and it
must return a structured per-item result.

Never run two writers on one branch: exactly one `pr-fix` call per run, and never hand it an infra
flake.

### 3. Do the agent-side actions the digest names

These change no code, so they are yours — run them after `pr-fix` returns, so pushes stay serialized:

- **a `failing-check` you classified as a flake** → `gh run rerun <run-id> --failed` (never cancel a
  queued run).
- **`stale-base`** → rebase onto the fresh base so a new head re-triggers the checks, then force-push
  with `--force-with-lease`. If the rebase surfaces a conflict it is no longer staleness — leave it for
  `pr-fix` on the next attention event.
- **`auto-merge-not-armed`** → re-arm auto-merge. It silently turns off after a force-push and after a
  CHANGES_REQUESTED→APPROVED transition. Arm it **only** on a pull request based on the default branch;
  "already queued" means it is armed.
- **`closed-unmerged`** → do not reopen it; surface it to the author as a durable escalation.
- **`stack-front-merged`** → report it to the stack orchestrator; the front-advance is its job.

### 4. Acknowledge — only once every item is covered

Acknowledging means every item in the digest was handled or durably escalated. It does **not** mean the
pull request is green.

```bash
relay pr watch acknowledge "$SLUG" --fingerprint "$FP" --outcome handled     # the work was done
relay pr watch acknowledge "$SLUG" --fingerprint "$FP" --outcome escalated   # durably raised to the author
relay pr watch acknowledge "$SLUG" --fingerprint "$FP" --outcome obsolete    # the digest no longer describes the PR
```

If `pr-fix` failed or came back partial, **do not acknowledge**. Leave the digest unacknowledged, say
what remains, and let the next scheduled check bring it back. Acknowledging resets the watcher to its
15-minute cadence; the same outcome twice is a no-op, and a different outcome for the same fingerprint
is refused.

### 5. Re-observe once, report, exit

```bash
relay pr watch tick "$SLUG" --json
```

Report a one-screen digest: what the attention was, what was delegated, what you did yourself, the
acknowledgement outcome, and the post-fix state. Then **stop**. Do not wait, do not schedule, do not
start another cycle.

## Under a project workflow

Record the outcome with `relay state log <slug> "<one-line digest>"` when the project has Relay state.
If no watcher is running and this project should be watched, start it once with
`relay pr watch start <slug>` from a Herdr pane. Never start a second watcher for a project — `start`
adopts the running one.

## Guardrails (non-negotiable)

- **No loop.** One digest, one run, one exit. Never claim continuous monitoring.
- **Approval is the only merge path.** Never self-approve, never `gh pr merge` to merge now, never
  dismiss a review to unblock. Auto-merge fires on a genuine human code-owner approval.
- **Never impersonate.** Every agent reply is prefixed `🤖 <agent> on behalf of <author>` — that
  disclosure is also how the watcher tells an agent reply from a human one.
- **Never silence a failure** (enforced inside `pr-fix`).
- **One writer per branch** — serialize every push.
- **Never acknowledge unfinished work**, and never acknowledge a fingerprint you did not read.

## Red flags

- Running a broad `gh` comment/check sweep instead of reading the digest the watcher already recorded.
- Acknowledging after a failed or partial `pr-fix`.
- Handing `pr-fix` an infra flake, or rerunning a check it is already fixing.
- Two sub-agents pushing the same branch in one run.
- Scheduling a follow-up tick, recording a next-tick time, or starting a loop.
- Arming auto-merge on a pull request that is not based on the default branch.

## Verification checklist

- [ ] Read exactly one digest — by fingerprint from the wake, or from a fresh `tick`.
- [ ] Every `failing-check` was classified flake vs real before anything was delegated or rerun.
- [ ] Real failures, comments, threads, and conflicts went to a single delegated `pr-fix` call carrying their bodies and ids.
- [ ] Flake reruns, the stale rebase, and auto-merge re-arming happened here, after `pr-fix` returned.
- [ ] The digest was acknowledged only once every item was handled or durably escalated.
- [ ] One `relay pr watch tick` ran, the outcome was reported, and the run exited without scheduling anything.
