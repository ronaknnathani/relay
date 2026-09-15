# Per-PR adaptive build cycle

Each stack item runs through the `deliver-pr` route contract in its own worktree and project slug.
Pass intent, explicit exclusions, acceptance criteria, parent base, and worktree. `deliver-pr`
classifies the task, selects proportional phases, records evidence, and opens the PR; stack-ship does
not prescribe a fixed sequence.

Independent items may run concurrently in separate worktrees. Dependent items start only after their
parent surface is stable and remain based on the parent branch until front advancement.

The worker returns branch, tip SHA, PR number, base, route class, evidence status, criteria covered,
and blocking decisions. It does not start a watcher: the stack orchestrator owns exactly one watcher
for the front PR.

Before a managed worker opens a PR it must run its recorded `can-open-pr` command. Standalone stack
workers follow stack capacity and approval rules supplied by the orchestrator.
