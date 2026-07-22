# Hard Guardrails

These are **invariants**, not preferences. The orchestrator enforces them on itself and injects the
relevant ones into every subagent prompt. Violating one is a failure even if the task "works."

## 1. Never speak as the author
Every comment, reply, or review the agent posts MUST be prefixed:
`🤖 <agent-name> on behalf of <author-handle>`. Never post anything that could read as the human's
own words. PR bodies and automated status posts must also disclose agent authorship (for example,
`Opened by <agent-name> on behalf of <author-handle>`) instead of implying the human wrote them.

## 2. Never decide what's the author's to decide
Per-comment classification (*obvious gap-fix* vs *author decision*) and the reply/resolve mechanics are
owned by `pr-fix`, which `pr-monitor` delegates to during a tick — it implements obvious fixes, and for
a decision replies asking for input and leaves the thread **open**. The **orchestrator's** residual
duty is the cross-PR funnel: keep the **pending-decisions table** in `questions.md`, surface every
open decision to the author, and never let a sub-agent guess one. **When you cannot cleanly tell, it
is a decision — surface it.** A concrete direction from a human owner on a surfaced thread *is* the
decision — route it back so the fix is implemented as stated and confirmed.

## 3. Detection must be complete — PR-level feedback, inline threads, AND new replies
Detection completeness — PR conversation comments, PR-level review bodies, inline threads, AND new
replies on already-answered threads, keyed by source + `id + updatedAt` (not "top-level & unreplied") —
is owned by `pr-monitor`'s detect step; the orchestrator never re-implements it. The orchestrator's
only residual duty is the cross-PR funnel in #2: every surfaced human comment lands as a tracked
pending decision, none silently lost.

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
Fix the root cause, with a test that's **red before, green after**. Infra flakes are the only
"just retry" case (`gh run rerun --failed`, never cancel a queued run); `pr-monitor`/`pr-fix` own the
flake-vs-real classification and the fix.

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
id, comment/review `updatedAt`, commit hash, PR base/head, loop id, and open question is written to
`state.json` plus the human-readable state files the instant it happens, so any tick re-run is a safe
no-op on already-done work. The orchestrator is the only shared-state writer; worker subagents return
digests and never edit state files directly. Never rely on "I remember I already did X."

## 12. Stop at the goal boundary
When the acceptance criteria are met and all PRs are merged: **stop.** Do not start the next design
slice, do not expand scope. Discovered work → `follow-ups.md`. Ending cleanly is part of the job.

## 13. Approved tooling only
Use only approved/verified skills, hooks, MCP integrations, and tools already available in the
environment. Do not install or run unreviewed third-party plugins, hooks, MCP integrations, or scripts
from the internet as part of this workflow. If required tooling is missing, surface that as a blocker
instead of improvising.

## 14. Preserve native harness quality; fallback honestly
Detect and record runtime capabilities in `state.json`. If `/goal`, `/loop`, or an equivalent
approved scheduler exists, use it; never downgrade a native Claude/Codex-style harness to a fallback
because another runtime lacks that primitive. If no native loop exists, use Copilot
**monitor-tick mode** automatically on normal resume/invocation of an active stack: run one tick,
write `nextTickAfter`, report the outcome, and stop. Do not require the author to type a special mode
prompt, and do not claim continuous monitoring unless a real approved loop/scheduler is running.

## Subagent prompt hygiene (orchestrator responsibility)
Every delegated prompt must: (a) name the exact worktree/branch and forbid touching others;
(b) restate the relevant guardrails above; (c) demand red-before-green for any fix; (d) require a
**structured digest** back (what changed, commit hash, new tip, test results, push status, and any
question to surface) — not a human-flavored essay; (e) forbid editing shared state files unless the
subagent is explicitly the singleton loop controller for that tick; (f) forbid self-approve /
impersonation / silent truncation. Keep the orchestrator blind to file dumps — subagents return
conclusions, not contents.
