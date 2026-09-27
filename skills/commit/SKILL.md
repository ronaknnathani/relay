---
name: commit
description: Create one well-formed commit from intended working changes, with safe branch/staging/secret checks and repository-style authorship metadata. Does not push or open a PR.
---

# Commit

This is the authoritative commit contract used by standalone commits and delivery workflows.

1. Read repository and global `AGENTS.md` guidance. Inspect `git status --short`,
   `git diff HEAD`, `git branch --show-current`, the dynamically detected default branch, and recent
   commit subjects.
2. Refuse to commit on the default branch. If invoked from a delivery workflow, create/use its
   configured feature branch; otherwise report the exact blocker.
3. Separate intended work from unrelated user changes. Stage specific files with
   `git add -- <paths...>`; never use `git add .`, `git add -A`, or stage a path not inspected.
4. Inspect the staged diff for credentials, tokens, private keys, generated noise, and unintended
   files. Stop on possible secrets rather than committing them.
5. Match the repository's recent message style. Lead with the meaningful change; add a body only when
   it explains why. Keep unrelated refactors in a separate commit.
6. End with the runtime-required `Co-authored-by` trailer identifying the actual automated agent/model,
   then run `git commit`.
7. Re-read `git status --short` and report the commit SHA plus any intentionally uncommitted paths.

Do not push, rebase, open a PR, discard changes, or amend unless the caller explicitly requested it.
