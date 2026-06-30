---
name: submit
description: Submit work — commit, push, and create a pull request
---

# Submit Work

Commit changes, push to the remote, and create a pull request.

## Flags

- `--draft` — create the PR in draft mode (`gh pr create --draft`). Default is an open (ready-for-review) PR.
- `--no-review` — skip the local code-review step.

## Workflow

Execute these steps in order.

### 1. Commit

- Run `git status` to see the changes.
- Stage **specific files** with `git add <files>` — not `git add .`.
- Commit with a clear message that matches the repository's style, ending with the trailer:

  ```
  Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
  ```

### 2. Local Review (skip with `--no-review`)

Detect the default branch, then review the full diff for issues before pushing:

```bash
DEFAULT_BRANCH=$(gh repo view --json defaultBranchRef --jq .defaultBranchRef.name 2>/dev/null \
  || basename "$(git symbolic-ref refs/remotes/origin/HEAD)")
git diff "origin/$DEFAULT_BRANCH...HEAD"
```

### 3. Push

```bash
git push -u origin "$(git branch --show-current)"
```

### 4. Check for a Repo PR Template

```bash
gh repo view --json pullRequestTemplates --jq '.pullRequestTemplates[0].body // empty'
```

- If a template is returned, use it as the PR body structure and fill in each section with details from the actual changes.
- If the template has a testing section, add `- [x] Local code review completed` to it (when local review was performed). If there's no testing section, append it at the end.
- If no template exists, use the default format below.

### 5. Create the PR

Add `--draft` to `gh pr create` only if the caller passed `--draft`.

```bash
gh pr create --title "Your PR title" --body "$(cat <<'EOF'
## Summary
- Brief description of what changed and why

## Testing Done
- [x] Local code review completed
- [ ] Tests added/updated
- [ ] Tests pass

<describe specific testing performed>
EOF
)"
```

### 6. Completion

Return the PR URL to the caller.

## Critical Rules

- Stage specific files, not everything.
- **Prefer the repo's own PR template** when one exists — it's the repo's standard.
- Detect the default branch dynamically; never assume `main` or `master`.
- `--draft` is passed to `gh pr create` only when the caller requested a draft (default is open).
