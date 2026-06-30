---
name: open-pr
description: Stage, commit, push, and open a pull request following the author's conventions — conventional-commit messages, a prose PR summary, and a Testing Done command list. Use when work is finished and the caller wants it committed and a PR opened. Supports --draft and an optional local review before push.
---

# Open PR

Take finished work from working tree to open pull request: stage specific files, commit with a
conventional-commit message, push, and open the PR in the author's house style. Detect the default
branch and the repo's PR template dynamically — never assume `main`, never hand-roll a body the repo
already has a template for. Execute the git/`gh` steps decisively once you know what to do, rather than
narrating each command.

## Flags

- `--draft` — open the PR as a draft (`gh pr create --draft`). Default is open (ready for review).
- `--no-review` — skip the optional local review before push.

## Process

1. **Branch-if-on-default gate.** Never commit straight onto the default branch. Detect it — and
   **hard-fail rather than silently skip the gate** if you can't — then branch (prefix `ronaknnathani/`,
   replacing `<slug>`). If the branch create fails, abort instead of falling through onto the default:

   ```bash
   DEFAULT_BRANCH=$(gh repo view --json defaultBranchRef --jq .defaultBranchRef.name 2>/dev/null)
   if [ -z "$DEFAULT_BRANCH" ]; then
     git remote set-head origin -a >/dev/null 2>&1                      # populate origin/HEAD if unset
     DEFAULT_BRANCH=$(basename "$(git symbolic-ref --quiet refs/remotes/origin/HEAD 2>/dev/null)")
   fi
   [ -z "$DEFAULT_BRANCH" ] && { echo "open-pr: cannot detect the default branch" >&2; exit 1; }
   if [ "$(git branch --show-current)" = "$DEFAULT_BRANCH" ]; then
     git switch -c "ronaknnathani/<slug>" \
       || { echo "open-pr: branch create failed; refusing to commit on $DEFAULT_BRANCH" >&2; exit 1; }
   fi
   ```

2. **Stage specific files.** Run `git status`, then `git add <files>` for exactly what belongs in this
   change — never `git add .`. Confirm the diff carries no secrets and no formatting churn mixed in
   with behavior.

3. **Commit with a conventional-commit message.** Type prefix
   (`feat` / `fix` / `refactor` / `test` / `docs` / `chore`), a subject, and a body that explains
   **why** the change exists — not a restatement of what the diff shows. Keep refactor commits separate
   from feature/fix commits (split with separate `git add`/`git commit` if both are staged). End every
   message with exactly:

   ```
   Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
   ```

4. **Local review before push (skip with `--no-review`).** Review the full diff against the branch
   point for correctness and leftover debris before it leaves your machine. Dispatch the `review` skill
   as a sub-agent when available; otherwise scan it inline:

   ```bash
   git diff "origin/$DEFAULT_BRANCH...HEAD"
   ```

5. **Push.**

   ```bash
   git push -u origin "$(git branch --show-current)"
   ```

6. **Prefer the repo's PR template.** Fetch it and fill each section from the actual change; only fall
   back to the body in step 7 when none exists:

   ```bash
   gh repo view --json pullRequestTemplates --jq '.pullRequestTemplates[0].body // empty'
   ```

7. **Open the PR.** Title is the conventional-commit subject. Body is the author's house style (see
   below). Add `--draft` only when the caller passed it.

   ```bash
   gh pr create --title "feat: <subject>" --body "$(cat <<'EOF'
   <One or two short prose paragraphs: WHY this change and WHAT it does, at a high level.
   No bullet list, no per-file walkthrough of the how — the diff shows the how.>

   ## Testing Done
   <command that was run>
   <command that was run>

   🤖 Generated with [Claude Code](https://claude.com/claude-code)
   EOF
   )"
   ```

8. **Return the PR URL** that `gh pr create` prints, to the caller.

## PR body conventions

The body format is in step 7; the red flags and checklist below guard it. The one rule not covered
there:

- **Disclose agent authorship** in any PR comment you post — mark it as written by an automated agent on
  the author's behalf; never phrase it as the author's own words.

## Red flags

- Committing onto the default branch instead of branching first.
- `git add .` / `git add -A` instead of staging the specific files.
- A PR summary that is a bullet list, or that enumerates each file/caller and how the code works.
- A `Testing Done` section padded with prose or per-command descriptions instead of bare commands.
- Refactor squashed into the same commit as a feature/fix; formatting churn mixed with behavior.
- A commit body that restates the diff ("what") instead of the reason ("why").
- Hand-rolling a body when the repo ships a PR template; assuming `main`/`master` as the base.
- Missing the `Co-Authored-By` trailer or the `🤖 Generated with` footer.

## Verification checklist

- [ ] `HEAD` is on a `ronaknnathani/` branch, not the default branch.
- [ ] Only the intended files were staged (no `git add .`); diff has no secrets or stray formatting.
- [ ] Commit message has a conventional-commit prefix, a why-focused body, and the `Co-Authored-By` trailer; refactor is its own commit.
- [ ] PR summary is 1–2 prose paragraphs on why/what — not bullets, not a file-by-file how.
- [ ] `Testing Done` is just the list of commands run; body ends with the `🤖 Generated with` footer.
- [ ] Repo PR template used when present; base branch detected dynamically; `--draft` applied only if requested.
- [ ] The PR URL was returned to the caller.
