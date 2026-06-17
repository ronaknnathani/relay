# Phase 2 — The Monitor Loop

The front PR (the one based on `master`) is watched by a **delegated loop**. The orchestrator's only
jobs are: **ensure exactly one healthy loop is running for the current front PR**, restart it if it
dies, and consume each tick's digest to update state. It does **not** run ticks itself.

## Loop ownership & liveness
- Start the loop with `/loop` (or a `CronCreate`/`ScheduleWakeup` cadence) bound to a **tick routine**
  (below). Record the loop/job id in `progress.md`.
- Exactly **one** loop per front PR. Before starting one, check none is already running for it.
- If a tick fails to report or the loop id disappears, **restart it** — don't silently lose coverage.
- When the front PR merges, **tear down its loop** and **start a new loop for the next PR** (now
  retargeted to `master`). Never run loops for non-front PRs (they can't merge yet).
- Don't poll-spin: pick a cadence matched to how fast PR state actually changes (~10–15 min is
  plenty for review/CI); when waiting on harness-tracked subagent work, you're re-invoked on
  completion — don't burn ticks polling.

## The tick routine (delegated, idempotent)
Each tick the orchestrator delegates **detection** to a scout subagent, then delegates
**remediation** per issue. A tick that finds nothing does nothing.

### Step 1 — Detect (scout subagent → structured digest)
The scout gathers, for the front PR, with every `gh` call wrapped in 3–4× retry (sleep 3):
- **State:** `state, mergeStateStatus, reviewDecision, base, head, autoMergeRequest`, failing checks.
- **Review comments from BOTH endpoints** (guardrail 3):
  ```
  gh api repos/<owner>/<repo>/pulls/<n>/comments  --paginate   # INLINE diff comments
  gh api repos/<owner>/<repo>/issues/<n>/comments --paginate   # conversation comments
  ```
  Plus the review-thread resolution state via GraphQL (`reviewThreads { isResolved, comments }`).
  Build the **needs-response set**: any thread whose **latest** comment is from a human (not the
  agent, not an already-acked author note) AND is newer than the agent's last reply on that thread.
  Track by `comment id + updatedAt`. **Include new replies on already-answered threads** — the most
  common miss.
- **CI:** failing required checks; for each, whether it's an **infra flake** (dependency-download
  failure / TLS timeout / registry 5xx / sandbox limit) or a real failure.
- **Staleness/freshness:** is a freshness check tripping, or is the PR meaningfully behind master?
- **Mergeability:** `DIRTY`/conflict vs `CLEAN`/`BLOCKED`.
The scout returns a **digest**: `{state, mergeable, autoMerge, approvals, failingChecks[], flakes[],
needsResponse[ {threadId, file, line, author, body, classification} ], stale, conflict}`. It writes
nothing and decides nothing.

### Step 2 — Triage & delegate remediation (one subagent per fix, serialized per branch)
For each item in the digest, the orchestrator routes:
- **Infra-flaked CI** → trivial, inline: `gh run rerun <id> --failed` (never cancel queued runs).
- **Real CI failure** → remediation subagent: reproduce, fix root cause (red-before-green, no
  silencing), regen any generated build files, commit `fix(...)` + trailer, push. Cascade if base changed.
- **Review comment** → see classification below.
- **Stale / freshness trip** → remediation subagent: rebase onto fresh `origin/master`
  (new head re-triggers the check), force-push `--force-with-lease`, cascade descendants.
- **Conflict (DIRTY)** → remediation subagent: rebase onto fresh master, resolve faithfully (prefer
  master's version of already-merged content, keep this PR's own additions), regen generated build
  files, scoped build+test, force-push, cascade.
Run at most one writer per branch at a time (guardrail 5). After any push, **re-verify and re-arm
auto-merge** and verify descendant base refs didn't collapse.

### Step 3 — Reconcile merge state
- Approved + clean + green → ensure auto-merge is armed (re-arm if it turned off; "already queued" =
  success).
- Merged → record it; the next PR auto-retargets to master → verify it rebased cleanly → arm its
  auto-merge → **start its loop**, tear down this one.

### Step 4 — Update state & digest up
Append outcomes to `progress.md`; trade-offs to `tradeoffs.md`; discovered work to `follow-ups.md`;
open questions to `questions.md`. Return a one-screen digest to the author only if something
material happened (don't notify on a clean no-op tick).

## Classifying a comment
For each needs-response thread, the remediation subagent (or the scout, as a hint) classifies:

**Obvious gap-fix** — implement, reply, resolve. Signals:
- A correctness bug, a missing test, a style/coding-rule violation, an unhandled edge case.
- A clear, mechanical request with one right answer ("wrap this error", "rename this shadowing var").
- It does **not** change the intended contract/behavior the author chose.

**Author decision** — reply asking (marked), add a row to the **pending-decisions table**
(`questions.md`) and surface it to the author, leave thread **open**, do NOT implement. Signals:
- It changes intended behavior, an API/contract shape, or scope ("should we tolerate X or error?").
- Two or more reasonable answers exist and the comment doesn't pin one.
- You'd be choosing *what the product does*, not *whether the code is correct*.

**Uncertain → treat as a decision and surface.** (Guardrail 2.) When a human owner replies with a
concrete direction on a thread you'd surfaced ("let's reject expired tokens with a 401"), that *is*
the decision — implement it as requested and reply confirming.

### Replying & resolving
- Every reply is prefixed `🤖 <agent> on behalf of <author>` (guardrail 1).
- Fixed an obvious gap → reply describing exactly what changed (file/commit), then **resolve** the
  thread (GraphQL `resolveReviewThread`).
- Asked for input → reply with the question + concise reasoning/options (mark your lean), leave the
  thread **open**, add a row to the pending-decisions table. Resolve the thread and **remove the row**
  only after the author answers and you've acted.
- Disagree on technical grounds → reply with concise reasoning; never silently ignore.

## Cascade after any front-PR change
Every commit you add to a PR can break its descendants. After any push to a PR that has descendants,
delegate a cascade (guardrail 10): `git rebase --onto <new-tip> <old-tip> <descendant>` for each,
build+test, force-push, verify base refs. Update `progress.md` with the new descendant tips.
