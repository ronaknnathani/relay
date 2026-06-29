---
name: pr-monitor
description: Monitor one open PR until it is merged — each tick detect CI failures, review comments, staleness, and conflicts, delegate the fixing to pr-fix, re-arm auto-merge, and stop at merge. Use after open-pr to drive a PR to merged with near-zero steering, via a native loop when one exists or one bounded tick per resume otherwise.
---

# PR Monitor

Watch one open PR and drive it to merged with near-zero steering. Each tick: detect what is blocking,
delegate the fixing to `pr-fix`, reconcile the merge state, and stop when it merges. You are a
**router** — detection and remediation run in sub-agents that return digests; you read digests and own
the run's state. You never write code or post in your own voice, never approve, and never merge.

## Completion promise (the stop gate)

This run is done — and only done — when the PR is **merged**. Keep ticking until then. State the
done-string only when it is unequivocally true: *all review threads resolved or surfaced, CI green,
approved, and merged.* A vague "looks good" is not done.

## Run mode — native loop, or one tick per resume

- **Native loop:** if `/loop` or an approved scheduler (`CronCreate` / `ScheduleWakeup`) exists, run
  the tick routine on a ~10–15 min cadence (matched to how fast PR state changes). Keep exactly **one**
  healthy loop per PR; restart it if it dies; record the loop id in the run's state.
- **Tick fallback:** if no native loop exists, each resume/invocation runs exactly **one** tick,
  records `nextTickAfter`, reports a digest, and stops. Don't claim continuous monitoring you can't
  provide.

## The tick (delegated, idempotent — a tick that finds nothing does nothing)

### 1. Detect — scout sub-agent → a digest (writes nothing, decides nothing)

Gather for the PR, every `gh` call wrapped in a 3–4× retry (sleep 3):

- `state, mergeStateStatus, reviewDecision, base, head, autoMergeRequest`, and failing checks.
- **All PR-visible feedback, paginated** — conversation comments, PR-level review bodies/summaries, and
  inline diff comments, plus GraphQL review-thread resolution state (`reviewThreads { isResolved }`,
  paging beyond the first 100 threads/comments on large PRs):

  ```bash
  gh api "repos/$OWNER/$REPO/issues/$N/comments" --paginate   # PR conversation comments
  gh api "repos/$OWNER/$REPO/pulls/$N/reviews"   --paginate   # PR-level review bodies (not on code)
  gh api "repos/$OWNER/$REPO/pulls/$N/comments"  --paginate   # inline diff comments
  ```

  Build the **needs-response set**: any comment, review body, or thread whose *latest* activity is from
  a human and is newer than the agent's last reply for that source — keyed by source + `id + updatedAt`.
  **Include new replies on already-answered threads** (the most common miss). PR-level review bodies
  are comments on the PR, not on code — don't miss them.
- **CI:** failing required checks; mark each as an **infra flake** (dependency-download / TLS timeout /
  registry 5xx / sandbox limit) or a real failure.
- **Staleness:** a freshness check tripping, or the PR meaningfully behind its base.
- **Mergeability:** `DIRTY`/conflict vs `CLEAN`/`BLOCKED`.

Digest: `{state, mergeable, autoMerge, approvals, failingChecks[], flakes[], needsResponse[{source,
id, threadId, file?, line?, author, body, updatedAt, classification}], stale, conflict}` — carry each
item's `body` (and a `classification` hint) so the remediator need not re-fetch it.

### 2. Remediate — delegate to `pr-fix` (serialized, one writer per branch)

Hand the **real** problems to `pr-fix` as a sub-agent — it owns the fixing: real CI failures (reproduce
→ root-cause → regression test, never silenced), review comments (it classifies obvious gap-fix vs
author decision and replies/resolves or flags to the author), and merge **conflicts** (resolve forward,
never abort). Pass it the needs-response items with their `body` and any `classification` hint so it
need not re-fetch.

Two items `pr-fix` does **not** own — `pr-monitor` handles them directly, since they fix nothing in the
code:
- an **infra-flaked** check → `gh run rerun <id> --failed` (never cancel a queued run);
- a **clean-but-stale** PR (a freshness check tripping, or the branch behind its base, *without* a
  conflict) → rebase onto the fresh base so a new head re-triggers the checks, then force-push with
  `--force-with-lease`. If the rebase surfaces a conflict, it is no longer staleness — route it to
  `pr-fix`'s conflict handling.

Never run two writers on the same branch at once, and never hand `pr-fix` an infra flake (only real
failures). After any push, the next tick re-checks — don't assume the fix stuck.

### 3. Reconcile merge state

- **Approved + clean + green** → re-verify and **re-arm auto-merge**. It silently turns off after a
  force-push and after a CHANGES_REQUESTED→APPROVED transition, so re-arm every tick; "already queued"
  means it is armed. Arm auto-merge **only on a PR based on the default branch**.
- **Merged** → record it, tear down the loop, and stop.

### 4. Update state & report

Record outcomes in the run's state (`relay state log` / `relay state pr` under a relay project, or the
run's state files otherwise). In native-loop mode, surface a one-screen digest only when something
material happened; in tick mode, always report the tick outcome plus `nextTickAfter`, then stop.

## Guardrails (non-negotiable)

- **Approval is the only merge path.** Never self-approve, never `gh pr merge` to merge now, never
  dismiss a review to unblock. Auto-merge fires on a genuine human code-owner approval.
- **Never impersonate.** Every agent reply is prefixed `🤖 <agent> on behalf of <author>`.
- **Never silence a failure** (enforced inside `pr-fix`).
- **One writer per branch** — serialize all pushes to a given branch.
- **Idempotent & resumable** — track addressed comment ids + `updatedAt`; a re-run is a no-op on
  already-handled work. Never rely on "I remember I did X" — read the state.
- **Stop at merge.** Don't start new work; discovered out-of-scope work goes to follow-ups.

## Red flags

- Approving or `gh pr merge`-ing yourself instead of letting human approval drive auto-merge.
- Re-fixing a comment already handled (not keying the needs-response set on `id + updatedAt`).
- Missing PR-level review bodies or new replies on resolved threads.
- Claiming continuous monitoring while in tick mode.
- Two sub-agents pushing the same branch in one tick.
- Letting auto-merge stay off after a force-push (must re-arm every tick).

## Verification checklist

- [ ] Exactly one loop (native mode) or one tick recorded with `nextTickAfter` (tick mode).
- [ ] Detection covered conversation comments, PR-level review bodies, inline threads, and new replies.
- [ ] Real failures, comments, and conflicts were delegated to `pr-fix`; infra flakes and clean-but-stale rebases were handled directly (not handed to `pr-fix`).
- [ ] Auto-merge re-armed when approved+clean+green; armed only on a default-branch-based PR.
- [ ] No self-approval, no manual merge, no impersonated reply.
- [ ] Stopped only when the PR is merged; the done-string was true.
