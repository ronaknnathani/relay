---
name: pr-monitor
description: Handle one pull request attention event end to end — read the watcher digest, triage it, delegate the fixing to pr-fix, re-observe the pull request, and exit. Use when the PR watcher wakes you, or run it manually for a one-shot check of an open PR.
---

# PR Monitor

Handle **one** pull request attention event, then stop. You are a **router**: the `relay pr watch`
runtime does the observing, you interpret its digest, delegate the fixing to `pr-fix`, re-observe the
pull request, and exit. You never write code, never post in your own voice, never approve, and never
merge.

There is **no acknowledgement**. The watcher never records that attention was handled, because a local
claim can be wrong. Every tick re-reads the live pull request, and an item disappears only when the
remote state itself no longer shows it: the check passes or reruns, the head SHA changes, the conflict
clears, the thread resolves, your reply lands on that exact source, auto-merge is armed, or the pull
request closes or merges. If nothing changed, the problem is still there and it will wake you again —
which is correct.

This skill has **no loop**. It does not schedule, does not recur, and does not own a next-tick time.
The watcher owns cadence; one run of this skill owns one digest.

## Where the work comes from

**Woken by the watcher.** The prompt names the project and a fingerprint:
`Run pr-monitor once for project <slug> using watcher fingerprint <fp>.` Bind them to `$SLUG` and
`$FP`, then read the digest that fingerprint names:

```bash
relay pr watch digest "$SLUG" --fingerprint "$FP" --json
relay pr watch status "$SLUG" --json     # current fingerprint, cadence, last wake, suppression
```

The digest record is refreshed on every observation, so the pull request metadata and bodies it
carries are the newest the watcher saw for that fingerprint — never a stale snapshot.

**Invoked manually, or with no Herdr.** Observe first, then act on what that observation recorded:

```bash
relay pr watch tick "$SLUG" --json       # fresh read-only observation; records its digest
```

`tick` works with no watcher running and never changes the watcher's schedule. If it returns no
actionable items, there is nothing to do — say so and stop.

## What the digest already decided

The digest is deterministic and complete; do not re-derive it with your own `gh` sweep. It carries the
pull request state, draft flag, base and head refs and SHAs, `mergeStateStatus`, `mergeable`,
`reviewDecision`, whether auto-merge is armed, every check with its status, conclusion, and run id, and
every actionable item with its `source`, `id`, `answers` token, `updatedAt`, `body`, thread id, file,
and line.

Each item carries a `reason`:

| Reason | What it means | Who acts |
|---|---|---|
| `failing-check` | a check concluded failure, error, timeout, action-required, startup-failure, canceled, or stale | you classify flake vs real; real → `pr-fix`. A canceled or stale check reported no result at all — rerun it |
| `changes-requested` | a CHANGES_REQUESTED review nobody has answered yet | `pr-fix` — answer that exact review; once answered the watcher reports `changes-requested-awaiting-rereview` and waits for the reviewer |
| `new-comment` / `new-review` / `new-inline-comment` | human activity the agent has not answered | `pr-fix` |
| `unresolved-thread` | an unresolved thread, or a new human reply after the agent answered | `pr-fix` |
| `merge-conflict` | the branch conflicts with its base | `pr-fix` |
| `blocked` | GitHub will not merge it and nothing else in the digest explains why | you investigate branch protection and required checks, then escalate |
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
ids, the comment, review, and thread items **with the bodies, ids, `answers` tokens, thread ids, files,
and lines the digest already carries**, and any merge conflict. Tell it that it is running in
**delegated mode**: the worklist is complete, it must not re-fetch the broad pull request context and
must not loop, and it must return a structured per-item result.

Pass each item's `answers` token through verbatim and require every reply to carry it in its marker.
That token is how the watcher knows which single piece of feedback was answered; without it, a reply
either answers nothing or — worse, if the watcher went back to matching on time — buries a comment
written after the observation you are acting on and shown to nobody.

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

### 4. Re-observe once — the remote state is the record

```bash
relay pr watch tick "$SLUG" --json
```

This is the only "did it work" signal there is. Compare it to the digest you started from:

- an item that is **gone** was genuinely resolved on GitHub;
- an item that is **still there** was not, whatever `pr-fix` reported.

If `pr-fix` failed or came back partial, say exactly what remains. There is nothing to suppress and
nothing to record: the next scheduled check re-observes and brings back whatever is still true.

### 5. Report, exit

Report a one-screen digest: what the attention was, what was delegated, what you did yourself, and
what the re-observation still shows. Then **stop**. Do not wait, do not schedule, do not start another
cycle.

If the pull request was **closed without merging**, the watcher hands you that escalation once and
then finishes — surface it to the author. If a merged **stack front** woke you, the stack orchestrator
owns the front-advance and must stop the old watcher with `relay pr watch stop <front-project-slug>`.

## Under a project workflow

Record the outcome with `relay state log <slug> "<one-line digest>"` when the project has Relay state.
If no watcher is running and this project should be watched, start it once with
`relay pr watch start <slug>` from a Herdr pane. Never start a second watcher for a project — `start`
adopts the running one. It refuses outright, creating nothing, unless exactly one live session carries
that project's Relay identity; if it refuses, say so and keep using this skill by hand.

## Guardrails (non-negotiable)

- **No loop.** One digest, one run, one exit. Never claim continuous monitoring.
- **No local "handled" claim.** The remote pull request is the only record of what is resolved.
- **Approval is the only merge path.** Never self-approve, never `gh pr merge` to merge now, never
  dismiss a review to unblock. Auto-merge fires on a genuine human code-owner approval.
- **Never impersonate, and always mark.** Every automated reply — yours or `pr-fix`'s — opens with the
  exact hidden marker `<!-- relay-agent-reply answers=<item answers token> -->` on its own
  line, then the visible `🤖 <agent> on behalf of <author>` disclosure. The marker is the only thing
  that tells the watcher an agent replied, and the token is the only thing that tells it *what* was
  answered; a reply without either is read as new human feedback forever. Reply on the **same source**
  you are answering: a conversation comment with `gh pr comment`, a review body with
  `gh pr review --comment`, an inline comment or thread with the inline replies endpoint.
- **Never silence a failure** (enforced inside `pr-fix`).
- **One writer per branch** — serialize every push.
- **Never report an item as resolved** unless the re-observation stopped showing it.

## Red flags

- Running a broad `gh` comment/check sweep instead of reading the digest the watcher already recorded.
- Reporting work as done on `pr-fix`'s word instead of on the re-observation.
- Handing `pr-fix` an infra flake, or rerunning a check it is already fixing.
- Two sub-agents pushing the same branch in one run.
- Letting a reply go out without the item's exact `answers` token, or with an id you made up.
- Scheduling a follow-up tick, recording a next-tick time, or starting a loop.
- Arming auto-merge on a pull request that is not based on the default branch.

## Verification checklist

- [ ] Read exactly one digest — by fingerprint from the wake, or from a fresh `tick`.
- [ ] Every `failing-check` was classified flake vs real before anything was delegated or rerun.
- [ ] Real failures, comments, threads, and conflicts went to a single delegated `pr-fix` call carrying their bodies, ids, and `answers` tokens.
- [ ] Flake reruns, the stale rebase, and auto-merge re-arming happened here, after `pr-fix` returned.
- [ ] One `relay pr watch tick` ran and every claim of "resolved" is backed by an item it no longer reports.
- [ ] The outcome was reported and the run exited without scheduling anything.
