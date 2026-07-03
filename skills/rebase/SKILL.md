---
name: rebase
description: Use when you need to rebase the current branch onto main, resolve merge conflicts, and force-push
argument-hint: "[BASE_BRANCH]"
---

# Rebase — Rebase, Fix Conflicts, Push

Rebase the current branch onto the base branch (default: `main`), resolve any merge conflicts, and force-push.

## Steps

### 1. Determine Base Branch

- If `$ARGUMENTS` contains a branch name, use it as the base.
- Otherwise, default to `main`.

### 2. Pre-check Working Tree

```bash
git status --porcelain
```

If output is non-empty, the working tree is dirty. Abort and report to the user:

```
Working tree has uncommitted changes. Commit or stash before rebasing:
  <output of git status --porcelain>
```

Do NOT auto-stash — too easy to lose work.

### 3. Fetch Latest

```bash
git fetch origin <BASE_BRANCH>
```

### 4. Rebase

```bash
git rebase origin/<BASE_BRANCH>
```

### 5. Handle Conflicts

If the rebase stops with conflicts:

1. Run `git diff --name-only --diff-filter=U` to list conflicted files
2. For each conflicted file:
   - Read the file to understand the conflict markers
   - Resolve the conflict by understanding both sides (ours = current branch work, theirs = base branch changes)
   - Prefer keeping our changes where they don't conflict with upstream intent
   - If unsure about a conflict, ask the user
3. Stage the resolved files: `git add <resolved-files>`
4. Continue the rebase: `git rebase --continue`
5. If more conflicts appear, repeat steps 1-4
6. Max 10 conflict rounds — if still conflicting, abort and ask the user

### 6. Verify

After successful rebase:
```bash
git log --oneline -5
```

### 7. Push

Check whether the branch has an upstream:

```bash
git rev-parse --abbrev-ref --symbolic-full-name @{upstream} 2>/dev/null
```

- **If the command fails** (no upstream): the branch has never been pushed. Use:
  ```bash
  git push -u origin "$(git rev-parse --abbrev-ref HEAD)"
  ```
- **If the command succeeds**: check ahead/behind state:
  ```bash
  ahead=$(git rev-list --count @{upstream}..HEAD)
  behind=$(git rev-list --count HEAD..@{upstream})
  ```
  - If both `ahead == 0` and `behind == 0`: nothing to push, skip.
  - Otherwise (ahead, behind, or diverged): `git push --force-with-lease`

`--force-with-lease` (not `--force`) fails safely if someone else pushed to the branch.

### 8. Report

```
Rebased onto origin/<BASE_BRANCH>.
  Conflicts resolved: <COUNT> files
  Push: <force-with-lease | initial -u | skipped (already in sync)>
```

If invoked as part of `/ship`, the PR will automatically update.
