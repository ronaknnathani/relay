# Hard Guardrails

These are **invariants**, not preferences. The orchestrator enforces them on itself and injects the
relevant ones into every subagent prompt. Violating one is a failure even if the task "works."

## 1. Never speak as the author
Every comment, reply, or review the agent posts MUST be prefixed:
`🤖 <agent-name> on behalf of <author-handle>`. Never post anything that could read as the human's
own words.

## 2. Never decide what's the author's to decide
For every review comment and every fork in the design, **classify**: *obvious gap-fix* vs
*author decision* (see [comment-handling](monitor-loop.md#classifying-a-comment)). Implement only
obvious fixes. For decisions: reply asking for input, add a row to the pending-decisions table,
leave the thread **open**, and move on. **When you cannot cleanly tell, it is a decision — surface
it.** A concrete direction from a human owner on a surfaced thread *is* the decision — implement it
as stated and confirm.

## 3. Detection must be complete — both endpoints AND new replies
`gh pr view --json comments` returns only conversation comments. Inline diff comments live on
`pulls/{n}/comments`. A new reply on an already-answered thread is invisible to a "top-level comment
without a reply" scan. Every tick, scan **both** endpoints and flag a thread whose **latest activity**
is from a human and is newer than the agent's last response on it — keyed by `comment id + updatedAt`,
not by "top-level & unreplied". Include replies on already-answered threads.

## 4. Inspect before any destructive action
Never delete, dismiss, overwrite, or force-replace something you did not create — a review, a
branch, a file, a draft — **without first reading its contents** and confirming it's safe. If it
contradicts how it was described, or holds unsaved human work, **stop and surface** instead of
proceeding.

## 5. One writer per branch
Never run two subagents that commit/push the **same branch** concurrently — their pushes race and one
silently loses. Serialize all writes to a given branch. Parallelism is across **independent branches**
only.

## 6. Approval is the only merge path; never self-approve
- Merge only via **auto-merge**, and arm it **only on a PR whose base is `master`** (never on a PR
  targeting another feature branch — that collapses the stack).
- Auto-merge fires on a **genuine human code-owner approval**. The agent never approves, never
  `gh pr merge` to merge-now, never dismisses a changes-requested review to unblock itself.
- **Re-verify and re-arm auto-merge every tick** — it can turn OFF after force-pushes and after a
  CHANGES_REQUESTED→APPROVED transition. When a **merge queue** owns the strategy,
  `gh pr merge --auto --squash` is rejected; use `gh pr merge --auto` (no method flag). "Already
  queued to merge" means it's armed and the queue owns it — that's success, not an error.

## 7. Resolve only after fixing; never resolve an open question
Resolve a review thread (GraphQL `resolveReviewThread`) **only** once the fix is pushed *and* you've
replied describing it. If you replied to **ask the author** something, leave the thread **open** until
they answer and you've acted.

## 8. No silencing failures
Never make CI green by deleting/skipping a test, adding a lint-suppression, or muting a real error.
Fix the root cause, with a test that's **red before, green after**. Infra flakes (dependency-download
failure / TLS timeout / registry 5xx / sandbox limit) are the only "just retry" case —
`gh run rerun --failed`, never cancel a queued run.

## 9. Wrap every `gh` call in a 3–4× retry (sleep 3)
`gh` intermittently returns HTTP 401 with a valid token. Retry before concluding anything. Only
declare the token dead if `gh api user` also fails on every retry, then surface for re-auth.

## 10. Cascade every base change through descendants
When a PR's content changes (a review fix, a freshness rebase), **every descendant** must be rebased
with `git rebase --onto <new-base-tip> <old-base-tip> <descendant>` (plain rebase double-applies),
force-pushed `--force-with-lease`, and **verified that its base ref didn't collapse** to the wrong
branch. Do this in a subagent, serialized per branch (rule 5).

## 11. Idempotency & resumability
Assume the orchestrator can be compacted/restarted at any moment. Every decision, addressed-comment
id, commit hash, and open question is written to a state file the instant it happens, so any tick
re-run is a safe no-op on already-done work. Never rely on "I remember I already did X."

## 12. Stop at the goal boundary
When the acceptance criteria are met and all PRs are merged: **stop.** Do not start the next design
slice, do not expand scope. Discovered work → `follow-ups.md`. Ending cleanly is part of the job.

## Subagent prompt hygiene (orchestrator responsibility)
Every delegated prompt must: (a) name the exact worktree/branch and forbid touching others;
(b) restate the relevant guardrails above; (c) demand red-before-green for any fix; (d) require a
**structured digest** back (what changed, commit hash, new tip, test results, push status, and any
question to surface) — not a human-flavored essay; (e) forbid self-approve / impersonation / silent
truncation. Keep the orchestrator blind to file dumps — subagents return conclusions, not contents.
