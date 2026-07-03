---
name: commit
description: Create a single well-formed git commit from the working changes, following the repo's conventional-commit conventions. Use when the caller wants only to commit — not push and not open a PR (for that, use `open-pr`).
---

# Commit

Create a single git commit for the current changes.

## Gather context

First inspect the working tree. Run these read-only commands and read their output:

- `git status` — what is staged and unstaged
- `git diff HEAD` — the full staged and unstaged diff
- `git branch --show-current` — the current branch
- `git log --oneline -10` — recent commit message style to match

## Create the commit

Based on the changes above, create one commit:

1. Stage the relevant files with `git add <specific-files>` (stage the files this change touches, not unrelated work).
2. Write a clear, concise commit message that matches the repository's existing style. Lead with what changed and why.
3. End the commit message with the trailer:

   ```
   Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
   ```

4. Commit with `git commit`.

Use only `git add`, `git status`, and `git commit`. Do not push, do not run other tools, and do not take any other action.
