---
name: pr-fix
description: Bring a PR to mergeable — fix CI failures, address review comments, and resolve merge conflicts. Runs either from a supplied watcher worklist (delegated mode, one pass) or from its own assessment (direct mode, looping until clear). Use after opening a PR when CI is red, reviewers have left comments, or the branch conflicts with its base.
---

# PR Fix

Drive a PR to a mergeable state: a green CI, every review comment fixed-and-resolved or
replied-and-flagged, and no merge conflicts with the base. Work the three
fronts — CI, comments, conflicts — and loop until all are clear. Fix root causes, never silence
failures or guess an author's intent. Use `review`'s shared severity vocabulary (Critical / Important /
Suggestion). Run independent investigations as sub-agents when available; otherwise do them inline.

## Two modes — check your input first

**Delegated mode — a caller supplied a watcher worklist.** `pr-monitor` hands you items taken from a
`relay pr watch` digest: each carries `reason`, `source`, `id`, `answers`, `updatedAt`, `body`,
`thread_id`, `path`, `line`, and for checks the `check_name` and `check_run_id`. That worklist is **complete and
authoritative**:

- **Skip step 1's broad assessment.** Do not re-run the PR/comment/thread/check sweep and do not build
  a full context bundle — the watcher already observed all of it. Bind `REPO` and `PR` and go straight
  to the work.
- Fetch only what the specific item needs: the failed run's log for a `check_run_id`, `git log`/`git
  blame` for a conflicting hunk, the file under an inline comment.
- **Fix once, then return** — no reassessment loop. The watcher re-observes after your push, and the
  next attention event carries whatever is left.
- **Return a structured result, one entry per supplied item:** the item id, what you did, `fixed`,
  `replied`, `escalated`, or `failed`, and the reason when it is not `fixed`. Report pushed commits and
  the new head SHA.
- **Never** run `relay pr watch tick` or `status`, and never schedule anything. The caller owns the
  watcher record.

**Direct mode — no worklist was supplied.** Assess the PR yourself and loop until clear, exactly as
described below. This is the manual path and it is unchanged.

## Quick commands

| Task | Command |
|------|---------|
| PR + check status | `gh pr view --json number,title,state,statusCheckRollup,mergeable` |
| CI checks | `gh pr checks` |
| Failed run logs | `gh run view <RUN_ID> --log-failed` |
| Inline review comments | `gh api "repos/$REPO/pulls/$PR/comments" --jq '.[] \| {id,user:.user.login,path,line,body}'` |
| Reply to an inline comment | `gh api "repos/$REPO/pulls/comments/<ID>/replies" -f body="$BODY"` |
| Reply to a conversation comment | `gh pr comment "$PR" --body "$BODY"` |
| Reply to a review body | `gh pr review "$PR" --comment --body "$BODY"` |
| Resolve a thread (after fixing) | `gh api graphql -f query='mutation($t:ID!){resolveReviewThread(input:{threadId:$t}){thread{isResolved}}}' -F t=<THREAD_ID>` |

`<RUN_ID>` comes from a delegated item's `check_run_id`, or from `gh pr checks` / `statusCheckRollup`;
a `<THREAD_ID>` comes from a delegated item's `thread_id`, or from the GraphQL `reviewThreads` query.
`gh` has no native thread-resolve — resolving requires the GraphQL mutation above.

## Every automated reply — exact marker, visible disclosure, same source

Every reply you post to a pull request starts with these two lines, then a blank line, then the reply:

```
<!-- relay-agent-reply answers=<item answers token> -->
🤖 <agent> on behalf of <author>
```

The HTML comment is invisible on GitHub and is the **only** thing that tells the `relay pr watch`
runtime an agent wrote a reply. A reply without it is read as fresh human feedback and will wake the
owner again forever; the emoji line alone is not enough, because a human can type an emoji. The
disclosure line is the human-visible half of the same promise: never write as if you were the author.

**The marker must name the exact item it answers — copy the item's `answers` field verbatim.** Every
digest item carries one, and the watcher matches replies by that id, never by time. A marker that
names nothing (`<!-- relay-agent-reply answers= -->` or the bare marker) answers nothing on a
conversation, a review, or a thread, so the item keeps waking the owner. A marker that names the
wrong id answers the wrong thing — and never the one in front of you.

This is not bookkeeping. A reviewer can write a second comment between the watcher's last look and
your reply, and the watcher has never shown it to anybody: a reply anchored to the id you were given
leaves that comment actionable, while an unanchored one buries it.

**Reply on the same source you are answering.** The watcher reconciles each source independently, so
an answer posted somewhere else does not answer anything:

| Item | Reply with | Marker |
|---|---|---|
| `new-comment` | `gh pr comment` — the pull request conversation | `answers=comment:200` |
| `new-review` | `gh pr review --comment` — a review, not a conversation comment | `answers=review:100` |
| `new-inline-comment` | the inline replies endpoint on that comment | `answers=inline-comment:300` |
| `unresolved-thread` | the inline replies endpoint on that thread | `answers=review-thread:<thread-id>:<comment-id>` |
| `changes-requested` | `gh pr review --comment` on the review that requested them | `answers=review:100` |

The ids in that column are examples; the item's own `answers` token is the truth. A thread's token
names the exact comment the digest reported, because a new reply arriving beside it is a different
item the watcher must still be able to surface.

## Process

1. **Assess.** *(Direct mode only — delegated mode skips this entirely and uses the supplied
   worklist.)* `REPO=$(gh repo view --json nameWithOwner --jq .nameWithOwner)`,
   `PR=$(gh pr view --json number --jq .number)`. Pull check status, the comment list, and `mergeable`.
   Triage into the three fronts below. Detect the repo's OWN build/test/lint commands from its
   `Makefile`, `package.json` scripts, or CI config (`.github/workflows/*`) — never assume a toolchain.

   Before editing, materialize the remote PR context onto the local filesystem so the fix is based on a
   stable, inspectable record rather than scattered terminal output. Use a non-committed directory under
   the git metadata dir, e.g. `CTX_DIR="$(git rev-parse --git-dir)/relay/pr-fix/$PR"`, and write at
   least: PR metadata, `statusCheckRollup`, `gh pr checks`, inline review comments, review threads,
   failed-run logs, the base-vs-head diff, and the discovered build/test commands. Re-read those files to
   plan the fix. Refresh the bundle after every push before deciding the PR is clear.

2. **CI failures — Stop-the-Line, red-loop-first.** Do not blindly do what the error message literally
   says; diagnose. For each failing check:
   - **Reproduce locally first.** Run the repo's own failing command to get a tight red loop. If it
     only fails in CI, line up versions/env before guessing.
   - **Localize, then minimize.** Find the failing layer (triage table below), then shrink to the
     smallest failing case — one test, one file.
   - **Fix the ROOT cause**, not the symptom.
   - **Regression test: red before, green after.** Add/adjust a test that fails without your fix and
     passes with it. Re-run the repo's full check command end to end until green.
   - **Never silence a failure** — no deleting or skipping a test, no lint-suppression, no loosened
     assertion to go green. A test that now fails means behavior changed: fix the code, not the test.

   | Category | Tell | Route |
   |---|---|---|
   | Type | type/compile error, signature mismatch | fix the type or the call site at the root |
   | Import | unresolved/circular import, missing symbol | fix the path/export; do not stub it out |
   | Config | failing lint/format/CI step config | match the repo's configured rule, don't suppress |
   | Dependency | version/lockfile/resolution error | reconcile the manifest + lockfile together |
   | Environment | passes locally, fails only in CI | align runtime/version/env vars with CI |

3. **Review comments — classify each, then act.**
   - **Obvious gap-fix** (a clear bug, a missing test, a style/rule violation, a one-right-answer
     mechanical change): implement it, reply describing exactly what changed, and resolve the thread
     (GraphQL `resolveReviewThread` — see Quick commands). Only mark a thread resolved once the fix is
     pushed; if you can't resolve it programmatically, say so rather than claiming it's done.
   - **Author decision** (changes intended behavior, API shape, or scope; two-plus reasonable answers):
     do NOT guess. Reply asking for input, FLAG it to the author, and leave the thread open.
   - **When unsure which it is, treat it as a decision** and surface it.
   - Every reply carries the marker naming that item's `answers` token and the visible
     `🤖 <agent> on behalf of <author>` disclosure, and goes on the same source it answers.

4. **Merge conflicts — research both intents, never abort.** Rebase/merge onto the base. For each
   conflict, research the intent behind BOTH sides before resolving — read the commit messages and the
   PR that introduced each hunk (`git log`, `git blame`, `gh pr view`). Preserve both intents where
   feasible. NEVER `--abort` the rebase/merge; resolve forward. Re-run the repo's validation after
   resolving and before continuing. Then **confirm your work survived** — your branch's commits are
   still in `git log` and the net diff against the base still contains your intended changes; a
   resolve-forward can silently drop a hunk even when validation passes.

5. **Loop until clear.** *(Direct mode only.)* Commit and push fixes, then re-assess (step 1). Repeat
   until CI is green and every comment is addressed — fixed+resolved, or replied+flagged. Surface the
   flagged decisions to the caller as the remaining blockers. **In delegated mode, stop after one
   pass** and return the per-item result instead; the watcher re-observes and the caller decides what
   happens next.

## Red flags

- Doing what the error message literally says without diagnosing the root cause.
- Deleting/skipping a test, suppressing a lint, or weakening an assertion to turn a check green.
- Editing a failing test instead of the code it caught — a red test means behavior changed.
- Guessing an author-decision comment instead of replying and flagging it.
- A reply that reads as the human author's own words, with no automated-agent disclosure.
- A reply missing the marker, naming no `answers` token, naming an id you invented instead of the
  item's own, or posted on a different source than the one it answers — every one of those either
  keeps waking the owner or answers something nobody asked about.
- Running `git rebase --abort` / `git merge --abort` instead of resolving the conflict.
- Resolving a conflict by keeping one side without understanding why the other side exists.
- Assuming `npm`/`make`/etc. instead of the command the repo's own config actually uses.
- Fixing from transient terminal output instead of a local PR context bundle that can be re-read and
  refreshed. *(Direct mode; in delegated mode the supplied worklist is that record.)*
- Re-fetching the whole PR context, or looping, when a delegated worklist was supplied.
- Touching the watcher record — `relay pr watch tick` and `status` belong to the caller.

## Verification checklist

- [ ] In delegated mode: every supplied item has a returned outcome, no broad re-fetch or reassessment loop ran, and the watcher record was left untouched.
- [ ] In direct mode: `gh pr checks` is fully green; each fix reproduced a local red loop and has a red-before/green-after regression test.
- [ ] Direct mode captured the remote PR context locally before edits and refreshed it after each push.
- [ ] No failure was silenced (no skipped/deleted test, no lint-suppression, no loosened assertion).
- [ ] Every review comment is fixed+resolved, or replied+flagged as an author decision left open.
- [ ] Every agent reply carries the marker with that item's exact `answers` token and the visible disclosure, and was posted on the same source it answers.
- [ ] All conflicts resolved forward (no abort), both intents researched and preserved, validation re-run after.
- [ ] After any rebase, confirmed my branch's commits survived (still in `git log`; net diff vs base still carries my changes).
- [ ] Findings reported with the shared severity vocabulary (Critical / Important / Suggestion).
