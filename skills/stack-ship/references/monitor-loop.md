# Phase 2 — The Monitor Loop / Monitor Tick

The front PR (the one based on `master`) is watched by the same **tick routine** in every runtime.
Only the trigger changes:

- **Native loop mode** — if `/loop` or an equivalent approved scheduler exists, use it. The
  orchestrator ensures exactly one healthy delegated loop is running for the current front PR,
  restarts it if it dies, and consumes each tick's digest to update state.
- **Copilot monitor-tick mode** — if no native loop exists, this skill automatically treats any normal
  resume/invocation for an active stack as one bounded monitor tick. It reconstructs from
  `state.json`, runs exactly one tick, writes `nextTickAfter`, reports a digest, and stops. The author
  should not have to type a special "monitor-tick mode" prompt.

Worker subagents return structured digests; only the orchestrator (or the singleton delegated loop
controller for a native-loop tick) writes shared state. Never claim continuous monitoring in
monitor-tick mode unless a real approved scheduler is invoking the ticks.

## Monitor ownership & liveness
- Capability preflight: record `runtime.name`, `runtime.goalMode`, `runtime.monitorMode`, loop/scheduler
  availability, `lastTickAt`, and `nextTickAfter` in `state.json`.
- Prefer native mode when available. Start the loop with `/loop` (or a
  `CronCreate`/`ScheduleWakeup` cadence) bound to the **tick routine** below. Record the loop/job id
  in `state.json` and `progress.md`.
- Exactly **one** native loop per front PR. Before starting one, check none is already running for it.
- If a native tick fails to report or the loop id disappears, **restart it** — don't silently lose
  coverage.
- In monitor-tick mode, do not create a fake loop. Each invocation runs one tick if `nextTickAfter` is
  due or if the user explicitly asks to continue/check the stack; then it exits after updating state.
- When the front PR merges, tear down its native loop if one exists, explicitly rebase/retarget the
  next PR to `master`, then start a new native loop or set `nextTickAfter` for the next Copilot tick.
  Never monitor non-front PRs as merge candidates (they can't merge yet).
- Don't poll-spin: pick a cadence matched to how fast PR state actually changes (~10–15 min is plenty
  for review/CI). In monitor-tick mode this cadence is advisory state for the next resume, not a claim
  that Copilot will wake itself up.

## The tick routine (delegated, idempotent)
Each tick the orchestrator delegates **detection** to a scout subagent, then delegates
**remediation** per issue. A tick that finds nothing does nothing.

### Step 1 — Detect (scout subagent → structured digest)
The scout gathers, for the front PR, with every `gh` call wrapped in 3–4× retry (sleep 3):
- **State:** `state, mergeStateStatus, reviewDecision, base, head, autoMergeRequest`, failing checks.
- **All PR-visible feedback** (guardrail 3), paginated:
  ```
  gh api repos/<owner>/<repo>/issues/<n>/comments --paginate   # PR conversation comments
  gh api repos/<owner>/<repo>/pulls/<n>/reviews   --paginate   # PR-level review bodies/summaries
  gh api repos/<owner>/<repo>/pulls/<n>/comments  --paginate   # inline diff comments
  ```
  Also fetch review-thread resolution state via GraphQL (`reviewThreads { isResolved, comments }`),
  paging beyond the first 100 threads/comments when needed. PR-level review bodies are comments on the
  PR itself, not directly on code; they are actionable and must not be missed.
  Build the **needs-response set**: any conversation comment, review body, or review thread whose
  **latest** activity is from a human (not the agent, not an already-acked author note) AND is newer
  than the agent's last reply/ack for that source. Track by source + `id + updatedAt`. **Include new
  replies on already-answered threads** — the most common miss.
- **CI:** failing required checks; for each, whether it's an **infra flake** (dependency-download
  failure / TLS timeout / registry 5xx / sandbox limit) or a real failure.
- **Staleness/freshness:** is a freshness check tripping, or is the PR meaningfully behind master?
- **Mergeability:** `DIRTY`/conflict vs `CLEAN`/`BLOCKED`.
The scout returns a **digest**: `{state, mergeable, autoMerge, approvals, failingChecks[], flakes[],
needsResponse[ {source, id, threadId, file?, line?, author, body, updatedAt, classification} ], stale,
conflict}`. It writes nothing and decides nothing.

### Step 2 — Triage & delegate remediation (one subagent per fix, serialized per branch)
For each item in the digest, the orchestrator routes:
- **Infra-flaked CI** → CI-control subagent: `gh run rerun <id> --failed` (never cancel queued runs),
  then return the run id and result.
- **Real CI failure** → remediation subagent: reproduce, fix root cause (red-before-green, no
  silencing), regen any generated build files, commit `fix(...)` + trailer, push. Cascade if base changed.
- **Review comment / review body** → see classification below.
- **Stale / freshness trip** → remediation subagent: rebase onto fresh `origin/master`
  (new head re-triggers the check), force-push `--force-with-lease`, cascade descendants.
- **Conflict (DIRTY)** → remediation subagent: rebase onto fresh master, resolve faithfully (prefer
  master's version of already-merged content, keep this PR's own additions), regen generated build
  files, scoped build+test, force-push, cascade.
Run at most one writer per branch at a time (guardrail 5). After any push, **re-verify and re-arm
auto-merge** and verify descendant base refs didn't collapse.

### Step 3 — Reconcile merge state
- Approved + clean + green → delegate an auto-merge-control subagent to re-verify and arm auto-merge
  (re-arm if it turned off; "already queued" = success).
- Merged → record it, tear down the native loop if one exists, then delegate a front-advance subagent
  to explicitly rebase/retarget the next PR to `master` and verify descendants' base refs did not
  collapse. When it returns, delegate auto-merge-control for the new front PR, update state, and start
  its native loop or schedule the next monitor tick according to `runtime.monitorMode`.

### Step 4 — Update state & digest up
Update `state.json`; append outcomes to `progress.md`; trade-offs to `tradeoffs.md`; discovered work
to `follow-ups.md`; open questions to `questions.md`; closed decisions to `progress.md`. In native
loop mode, return a one-screen digest only if something material happened. In monitor-tick mode,
always report the tick outcome plus `nextTickAfter`, then stop.

## Classifying a comment or review body
For each needs-response thread/comment/review body, the remediation subagent (or the scout, as a
hint) classifies:

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
  thread if it is a review thread (GraphQL `resolveReviewThread`).
- Asked for input → reply with the question + concise reasoning/options (mark your lean), leave the
  thread **open**, add a row to the pending-decisions table. Resolve the thread and **remove the row**
  only after the author answers and you've acted.
- Disagree on technical grounds → reply with concise reasoning; never silently ignore.

## Cascade after any front-PR change
Every commit you add to a PR can break its descendants. After any push to a PR that has descendants,
delegate a cascade (guardrail 10): `git rebase --onto <new-tip> <old-tip> <descendant>` for each,
build+test, force-push, verify base refs. Update `state.json` and `progress.md` with the new
descendant tips.
