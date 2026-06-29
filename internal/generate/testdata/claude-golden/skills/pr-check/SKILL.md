---
name: pr-check
description: Check PR status — review CI failures and address PR comments
---

# Check PR Status

Check the current PR's CI status, review failures, and address PR comments, then commit and push fixes.

## Quick Commands

| Task | Command |
|------|---------|
| View PR status | `gh pr view --json number,title,state,statusCheckRollup` |
| Check CI status | `gh pr checks` |
| View comments | `gh pr view --comments` |
| View failed logs | `gh run view <RUN_ID> --log-failed` |
| Reply to a comment | `gh api repos/$REPO/pulls/comments/<COMMENT_ID>/replies -f body="..."` |

## Steps

### 1. Get PR Status

```bash
gh pr view --json number,title,state,statusCheckRollup
gh pr checks
```

Look for passing (✓), failing (✗), and pending (○) checks.

### 2. If Checks Failed

1. **Get the run ID** from the `gh pr checks` output.
2. **View the failure logs**:
   ```bash
   gh run view <RUN_ID> --log-failed
   ```
3. **Identify the root cause** — lint, test, build, or type error.
4. **Reproduce and fix locally** using the project's own build/test/lint commands. Detect them from the repo rather than assuming a tool:
   - Read the repo's `Makefile`, `package.json` scripts, or CI config (`.github/workflows/*`) to find the commands the project actually uses.
   - If the build flow already supplies build/test/lint commands, use those.
   - Run the relevant command, fix the reported issues, and re-run until it passes.

### 3. Review and Address PR Comments

```bash
REPO=$(gh repo view --json nameWithOwner --jq .nameWithOwner)
PR_NUMBER=$(gh pr view --json number --jq .number)

# General + inline review comments
gh pr view --comments
gh api "repos/$REPO/pulls/$PR_NUMBER/comments" --jq '.[] | {id: .id, user: .user.login, path: .path, line: .line, body: .body}'
```

For each review comment, either address it with a code change, or reply explaining why it doesn't apply:

```bash
gh api "repos/$REPO/pulls/comments/<COMMENT_ID>/replies" -f body="<your response>"
```

Note: a reply posts your explanation but does not auto-resolve the thread — resolve it in the GitHub UI if needed.

### 4. Push Fixes

```bash
git add <fixed-files>
git commit -m "Address PR feedback: <summary>"
git push
```

Then re-check (step 1). Loop until all checks pass.

## Completion Criteria

- [ ] All CI checks passing
- [ ] All PR comments addressed (with a code change or a reply explaining why not)
- [ ] Fixes committed and pushed
