# Phase 1 — Build the stack (per-PR build cycle)

Each PR in the stack is built by a **`deliver-pr` sub-agent** — it owns the single-PR pipeline
(`clarify → plan → implement → simplify → review → validate → open-pr`). stack-ship does not
re-implement those phases; this file covers only what the **stack** adds on top.

## Per PR

Create the PR's worktree first — stack-ship owns worktrees; `deliver-pr` runs **in** one, it does not
create its own — then spawn the `deliver-pr` sub-agent and hand it:

- **a per-PR slug** — e.g. `<goalSlug>-<pr-id>`. `deliver-pr` runs `relay state init` under this slug
  and owns a **separate, nested `state.json`** from the stack run's; keep the slugs distinct.
- **intent + scope** — the one thing this PR does, and what it deliberately does not touch.
- **acceptance criteria** — the `goal.md` criteria this PR satisfies. These **seed** `deliver-pr`'s
  `clarify` phase (confirm/refine them against the code), so it does not re-derive criteria you already
  pinned in `plan.md`.
- **branch + base** — `<prefix>/<slug>`, based on its parent branch (or `master` for the front PR).
- the worktree path, and a prohibition on touching any other worktree.

It runs the pipeline, opens the PR, and returns a **structured digest**: branch, tip hash, PR number,
base, the criteria it claims, and any blocking question. You read the digest — not the work.

## Worktrees

One worktree per branch: `<.worktrees>/<branch-with-slashes-as-underscores>`. A sub-agent is forbidden
from touching any other worktree. Serialize all writes to a given branch (guardrails.md #5).

## Parallel vs pipelined

- **Independent PRs** (`depends-on: []`) → spawn their `deliver-pr` sub-agents in one message so they
  run concurrently, each in its own worktree.
- **Dependent PRs** → start the child after the parent's surface is stable (parent opened, or its API
  committed), then rebase the child onto the parent. A child can scaffold early but cannot compile
  until the parent's surface is real.

## Ship semantics for stacked bases

`deliver-pr` ends at an open PR. For the stack:

- The PR's **base** is its parent branch (or `master` if it is the front PR).
- Arm auto-merge **only** when the base is `master` — never on a PR based on another feature branch
  (that collapses the stack). `pr-monitor` enforces this; see [guardrails.md](guardrails.md) #6.
- Commit messages: `type(scope): imperative` + the author's co-author trailer; the PR body is prose
  with agent-authorship disclosure.

## Surfacing questions

If a `deliver-pr` sub-agent hits a real author decision, it stops that phase and returns the question.
You add a row to `questions.md`, surface it to the author, and pause **only that PR** — the rest of the
stack keeps moving. Resume the PR when the author answers.
