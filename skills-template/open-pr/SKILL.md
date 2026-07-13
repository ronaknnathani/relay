---
name: open-pr
description: Stage, commit, push, and open a pull request following the author's repository and AGENTS.md conventions — repo-style commit messages, a prose PR summary, and a Testing Done command list. Use when work is finished and the caller wants it committed and a PR opened. Supports --draft and an optional local review before push.
---

# Open PR

Take finished work from working tree to open pull request: stage specific files, commit with a
repo-style message, push, and open the PR in the author's house style. Read `<repo>/AGENTS.md` and
`~/.config/agents/AGENTS.md` first (repo-specific rules win) and follow those preferences for commit
messages and PR descriptions. Execute git/`gh` operations through this skill's bundled
`scripts/open-pr.sh` helper rather than inlining command sequences in the skill.

## Flags

- `--draft` — open the PR as a draft (`gh pr create --draft`). Default is open (ready for review).
- `--no-review` — skip the optional local review before push.

## Process

1. **Branch-if-on-default gate.** Never commit straight onto the default branch. Invoke the helper's
   `ensure-branch <slug>` command. It hard-fails if the default branch cannot be detected and, when a
   new branch is needed, reads the branch prefix from the first-time `relay` config
   (`relay config branch-prefix`) rather than hardcoding a personal prefix.

2. **Stage specific files.** Invoke the helper's `status` command, then its `stage -- <files...>`
   command for exactly what belongs in this change — never stage the whole tree. Confirm the diff carries
   no secrets and no formatting churn mixed in with behavior.

3. **Commit in the repo's style.** Match the repository's recent commit style and the AGENTS.md
   preferences. Do not force Conventional Commits unless the repo already uses them. The subject should
   lead with what changed; include a body only when useful, and make it explain **why** the change exists
   rather than restating the diff. Keep refactor commits separate from feature/fix commits (split with
   separate `stage`/`commit` helper calls if both are present). End every message with a
   `Co-authored-by` trailer for the actual model/agent currently doing the work; never hardcode a model.
   Use the runtime-provided model identity when available, and the agent's verified no-reply identity for
   the email. Then invoke the helper's `commit <message-file>` command.

4. **Local review before push (skip with `--no-review`).** Review the full diff from the helper's `diff`
   command for correctness and leftover debris before it leaves your machine. Dispatch the `review` skill
   as a sub-agent when available; otherwise scan it inline.

5. **Push.** Invoke the helper's `push` command.

6. **Prefer the repo's PR template.** Fetch it and fill each section from the actual change; only fall
   back to the body in step 7 when none exists. Use the helper's `pr-template` command to fetch it.

7. **Open the PR.** Title follows the repo's PR-title style. Body follows AGENTS.md: one or two short
   prose paragraphs focused on why/what, no bullet list, no per-file walkthrough, and a `Testing Done`
   section containing only the commands run. Disclose automated authorship with the actual agent/model
   used, not a hardcoded Claude footer. Add `--draft` to the helper call only when the caller passed it,
   and invoke `create-pr --title <title> --body-file <body-file>`.

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
- Missing the actual-model `Co-authored-by` trailer or automated-authorship disclosure.

## Verification checklist

- [ ] `HEAD` is on a branch using the configured `relay` branch prefix, not the default branch.
- [ ] Only the intended files were staged (no `git add .`); diff has no secrets or stray formatting.
- [ ] Commit message matches repo/AGENTS.md style, includes the actual-model `Co-authored-by` trailer, and keeps refactor separate.
- [ ] PR summary is 1–2 prose paragraphs on why/what — not bullets, not a file-by-file how.
- [ ] `Testing Done` is just the list of commands run; body discloses the actual automated agent/model.
- [ ] Repo PR template used when present; base branch detected dynamically; `--draft` applied only if requested.
- [ ] The PR URL was returned to the caller.
