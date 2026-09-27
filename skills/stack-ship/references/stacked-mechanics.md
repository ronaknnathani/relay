# Stacked-PR Mechanics (subagent toolbox)

Concrete GitHub-stacking machinery the remediation/cascade subagents need. The orchestrator hands
the relevant snippet to the subagent; it doesn't run these itself.

## Transient 401 retry (wrap EVERY gh call)
```bash
for i in 1 2 3 4; do
  OUT=$(gh <args> 2>&1); echo "$OUT" | grep -q '401' || break; sleep 3
done
```
Token is only actually dead if `gh api user` also fails every retry → surface for re-auth.

## Rebase cascade after a base changes (squash-merge or freshness)
A plain `git rebase` double-applies the parent's commits. Use `--onto`:
```bash
git rebase --onto <new-base-tip> <old-base-tip> <descendant-branch>
# resolve faithfully: take the default branch's version of already-merged content, keep this PR's own additions
git push --force-with-lease origin <descendant-branch>
```
After every cascade: confirm each descendant's **base ref did not collapse** (e.g. D's base must
stay the parent feature branch, not jump to the default branch) — a collapsed base silently
squashes the stack.

## Advance the next front PR after its parent merges
Do **not** rely on GitHub auto-retargeting. Once the old front PR merges to the dynamically detected
default branch, explicitly move the next PR onto that branch, then cascade its descendants. Preserve
the merged parent branch's pre-merge tip as `<merged-parent-tip>` and do not substitute a plain
`git rebase`:
```bash
git fetch origin
git checkout <next-branch>
git rebase --onto origin/<default-branch> <merged-parent-tip> <next-branch>
git push --force-with-lease origin <next-branch>
gh pr edit <next-pr> --base <default-branch>
gh pr view <next-pr> --json baseRefName,headRefName,mergeStateStatus
```
Confirm `baseRefName == "<default-branch>"` for the new front PR. Then verify each descendant PR
still targets its intended parent feature branch, not the default branch, unless it is now the front
PR.

## Freshness / staleness
A time/distance-based freshness check (PR open too long, or N commits behind) trips even at 1 commit
behind. Remedy: fetch origin and use
`git rebase --onto origin/<default-branch> <old-default-tip> <branch>`, where
`<old-default-tip>` is the exact default-branch commit currently beneath the branch, so the **new
head re-triggers** the check; force-push; cascade descendants. Never use a plain `git rebase`.
Conclusion of the stale check is typically `TIMED_OUT`.

## Auto-merge
- Arm **only** when base is the repository's default branch: `gh pr merge <n> --auto` (NO `--squash`
  if a **merge queue** owns the strategy — that flag is rejected; "already queued to merge" =
  armed, queue owns it).
- Auto-merge silently turns **OFF** after force-pushes and after CHANGES_REQUESTED→APPROVED.
  **Re-verify and re-arm after each relevant watcher event** when the PR is approved + clean.
- As each PR merges to the default branch, explicitly advance the next PR with the procedure above.
  Only after its base is verified as the default branch should auto-merge be armed and its stack
  watcher started.
- Never `gh pr merge` to merge-now, never self-approve, never dismiss a review to unblock.

## Resolve a review thread (GraphQL — REST can't)
```bash
# find unresolved threads
gh api graphql -f query='query($o:String!,$r:String!,$n:Int!){repository(owner:$o,name:$r){
  pullRequest(number:$n){reviewThreads(first:100){nodes{id isResolved
    comments(first:1){nodes{author{login} body}}}}}}}' -F o=<owner> -F r=<repo> -F n=<n>
# resolve one after fix is pushed + replied
gh api graphql -f query='mutation($t:ID!){resolveReviewThread(input:{threadId:$t}){thread{isResolved}}}' -F t=<threadId>
```
The `first:100` query is a page, not a guarantee of completeness; follow cursors when there are more
threads/comments.

## Reply on a thread (inline)

Delegate every reply to `pr-fix`, the sole mutation and reply owner. Pass the exact watcher item,
including its `answers` token; the posted body must begin with:

```text
<!-- relay-agent-reply answers=<item answers token> -->
🤖 <agent> on behalf of <author>
```

Never post a direct stack-orchestrator reply or guess the marker token. `pr-fix` pre-checks for a
stray PENDING review and applies the inspect-before-destructive-action guardrail before removing only
a genuinely empty automated draft.

## Detecting all PR-visible feedback
```bash
gh api repos/<owner>/<repo>/issues/<n>/comments --paginate   # PR conversation comments
gh api repos/<owner>/<repo>/pulls/<n>/reviews   --paginate   # PR-level review bodies/summaries
gh api repos/<owner>/<repo>/pulls/<n>/comments  --paginate   # inline diff comments
```
Also fetch GraphQL review threads for resolution state. PR-level review bodies are comments on the PR
itself, not directly on code; do not miss them by only scanning inline comments. Needs-response =
thread/comment/review whose latest human activity is newer than the agent's last reply/ack for that
source (track by source + id + updatedAt). Include replies on already-answered threads.

## Merge-queue note
With a merge queue, the queue controls squash/rebase and serializes merges. `--auto` hands the PR to
the queue; `autoMergeRequest` may read null/OFF while the queue owns it — that's expected, not a
failure.
