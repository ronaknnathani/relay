---
name: commit
description: Create a single well-formed git commit from the working changes, following the repo's AGENTS.md and recent commit-message conventions. Use when the caller wants only to commit — not push and not open a PR (for that, use `open-pr`).
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
2. Read `<repo>/AGENTS.md` and `~/.config/agents/AGENTS.md` if present, then write a clear, concise commit message that matches those preferences and the repository's existing style. Lead with what changed and why.
3. End the commit message with a `Co-authored-by` trailer for the actual model/agent currently doing the work. Use the runtime-provided model identity when available, and the agent's verified no-reply identity for the email. Never hardcode a model name.

4. Commit with `git commit`.

**Published output names no internal program bookkeeping.** PR titles and bodies, commit messages,
PR and review replies, code, code comments, and test names never mention work-item ids (`w<N>`),
decision ids (`d<N>`), worker or session names derived from them, or that the change came from a
Relay program, tech lead, CEO, or managed worker. Describe the change on its own terms. Machinery
that is not prose stays: the hidden `relay-agent-reply` marker, the branch name, and the
`🤖 <agent> on behalf of <author>` disclosure (which never says "Relay program").

Use only `git add`, `git status`, and `git commit`. Do not push, do not run other tools, and do not take any other action.

## Red flags

- Published text that names a work-item id, decision id, derived worker name, or a Relay program / tech lead / CEO / managed-worker origin.
- A commit message that restates the diff instead of saying why the change exists.
