---
name: pr-fix
description: Bring a PR to mergeable — fix CI failures, address review comments, and resolve merge conflicts, looping until checks are green and every comment is handled. Use after opening a PR when CI is red, reviewers have left comments, or the branch conflicts with its base.
---

# PR Fix

Drive a PR to a mergeable state: a green CI, every review comment fixed-and-resolved or
replied-and-flagged, and no merge conflicts with the base. Work the three
fronts — CI, comments, conflicts — and loop until all are clear. Fix root causes, never silence
failures or guess an author's intent. Use `review`'s shared severity vocabulary (Critical / Important /
Suggestion). Run independent investigations as sub-agents when available; otherwise do them inline.

## Quick commands

| Task | Command |
|------|---------|
| PR + check status | `gh pr view --json number,title,state,statusCheckRollup,mergeable` |
| CI checks | `gh pr checks` |
| Failed run logs | `gh run view <RUN_ID> --log-failed` |
| Inline review comments | `gh api "repos/$REPO/pulls/$PR/comments" --jq '.[] \| {id,user:.user.login,path,line,body}'` |
| Reply to a comment | `gh api "repos/$REPO/pulls/comments/<ID>/replies" -f body="..."` |
| Resolve a thread (after fixing) | `gh api graphql -f query='mutation($t:ID!){resolveReviewThread(input:{threadId:$t}){thread{isResolved}}}' -F t=<THREAD_ID>` |

`<RUN_ID>` comes from `gh pr checks` / `statusCheckRollup`; a `<THREAD_ID>` comes from the GraphQL
`reviewThreads` query. `gh` has no native thread-resolve — resolving requires the GraphQL mutation above.

## Process

1. **Assess.** `REPO=$(gh repo view --json nameWithOwner --jq .nameWithOwner)`,
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
   - Every reply must disclose it is automated and must not impersonate the author.

4. **Merge conflicts — research both intents, never abort.** Rebase/merge onto the base. For each
   conflict, research the intent behind BOTH sides before resolving — read the commit messages and the
   PR that introduced each hunk (`git log`, `git blame`, `gh pr view`). Preserve both intents where
   feasible. NEVER `--abort` the rebase/merge; resolve forward. Re-run the repo's validation after
   resolving and before continuing. Then **confirm your work survived** — your branch's commits are
   still in `git log` and the net diff against the base still contains your intended changes; a
   resolve-forward can silently drop a hunk even when validation passes.

5. **Loop until clear.** Commit and push fixes, then re-assess (step 1). Repeat until CI is green and
   every comment is addressed — fixed+resolved, or replied+flagged. Surface the flagged decisions to
   the caller as the remaining blockers.

## Red flags

- Doing what the error message literally says without diagnosing the root cause.
- Deleting/skipping a test, suppressing a lint, or weakening an assertion to turn a check green.
- Editing a failing test instead of the code it caught — a red test means behavior changed.
- Guessing an author-decision comment instead of replying and flagging it.
- A reply that reads as the human author's own words, with no automated-agent disclosure.
- Running `git rebase --abort` / `git merge --abort` instead of resolving the conflict.
- Resolving a conflict by keeping one side without understanding why the other side exists.
- Assuming `npm`/`make`/etc. instead of the command the repo's own config actually uses.
- Fixing from transient terminal output instead of a local PR context bundle that can be re-read and
  refreshed.

## Verification checklist

- [ ] `gh pr checks` is fully green; each fix reproduced a local red loop and has a red-before/green-after regression test.
- [ ] Remote PR context was captured locally before edits and refreshed after each push.
- [ ] No failure was silenced (no skipped/deleted test, no lint-suppression, no loosened assertion).
- [ ] Every review comment is fixed+resolved, or replied+flagged as an author decision left open.
- [ ] Every agent reply discloses it is automated and does not impersonate the author.
- [ ] All conflicts resolved forward (no abort), both intents researched and preserved, validation re-run after.
- [ ] After any rebase, confirmed my branch's commits survived (still in `git log`; net diff vs base still carries my changes).
- [ ] Findings reported with the shared severity vocabulary (Critical / Important / Suggestion).
