---
name: stack-ship
description: "Use only for a standalone stack explicitly launched with `relay --workflow stack-ship`; never invoke inside a tech-lead-managed Relay program. Decompose one goal into small PRs, delegate adaptive delivery and monitoring, surface author decisions, and auto-merge only after human approval."
disable-model-invocation: true
---

# Stack Ship

Own only stack-specific planning, dependencies, front advancement, and completion. Read
[guardrails](references/guardrails.md) first. Never invoke inside a tech-lead-managed Relay program.

Set `/goal <the user's requested outcome>` from the original task, not instructions to run the
stack-ship workflow. Relay project artifacts and `relay state` remain the durable source of truth.
The accepted goal and criteria are durable: this is your `/goal`. Never replace this native goal with a fallback. When a real author decision is needed, use
AskUserQuestion and record it.

## Decompose

Create `goal.md` with measurable acceptance criteria and `plan.md` with small, single-intent PRs in
dependency order, normally interface/API before implementation before integration. Mark independent
branches for parallel work and dependent branches for pipelining. Keep stack state, progress,
tradeoffs, follow-ups, and open decisions durable. Get author sign-off only where a genuine design or
scope choice remains.

## Build each PR

Create each child as a registered Relay project from its exact parent base:

```bash
relay "<child intent and acceptance criteria>" \
  --name <child-project-slug> --base <parent-branch-or-commit> --no-launch
```

Relay creates and records the child branch, worktree, manifest, parent base, and start SHA. Then
launch an adaptive `deliver-pr` worker in the manifest's worktree with that child slug, intent,
exclusions, acceptance criteria, and an explicit instruction that it is a stack child. The child owns
its route, evidence, commits, and PR, then returns the one-time `stack-advance` capability issued by
`relay state grant <child-project-slug> stack-advance --coordinator-token <child-token>`;
this stack orchestrator owns topology, parent/base updates, front advancement, and stack state.
Do not create an arbitrary worktree first or copy child phase procedures here.

Parallelize only independent branches; one writer per branch. A managed program worker must honor its
recorded `can-open-pr` grant immediately before `open-pr`, but stack-ship itself is standalone and
never creates a nested program.

## Watch only the front

Start one watcher for the PR based on the repository default branch:

```bash
relay pr watch start <front-project-slug> --mode stack --owner <stack-orchestrator-slug>
```

There is never a watcher on a
non-front PR. For each wake, invoke `pr-monitor` once; it reads one digest, delegates all mutations to
`pr-fix`, and re-observes once. When its result marks `auto-merge-not-armed` as `ready-for-owner`,
verify the watcher `owner_slug` is this orchestrator and arm approved auto-merge yourself; no worker
may perform that mutation.

After the front merges:

1. `relay pr watch stop <front-project-slug>`;
2. fetch origin, dynamically detect the default branch, and advance the next branch with
   `git rebase --onto origin/<default-branch> <merged-parent-tip> <next-branch>`, where
   `<merged-parent-tip>` is the merged parent branch's pre-merge tip; never substitute a plain
   `git rebase`;
3. push the next branch with `--force-with-lease` and retarget its PR to `<default-branch>`;
4. consume the child's returned one-time capability with
   `relay route advance <next-project-slug> --base <default-branch> --advance-token <token>`;
5. resume the child with `relay resume <next-project-slug>` so adaptive `deliver-pr` redispatches
   work reopened by the route change, records fresh review and exact-gate validation evidence, and
   reconciles the existing PR through `open-pr`; wait for a fresh terminal `opened` result;
6. verify descendant base refs and intended diffs did not collapse;
7. cascade the new parent tip through descendants with `git rebase --onto` and
   `--force-with-lease`;
8. only after the refreshed child delivery is complete, start the next front watcher with
   `relay pr watch start <next-project-slug> --mode stack --owner <stack-orchestrator-slug>`.

Auto-merge is armed only on the front and fires only after genuine human code-owner approval. Never
self-approve, merge immediately, or arm a child PR based on another feature branch.

## Stop

When all acceptance criteria are met and every PR is merged, stop the final watcher, mark the goal
delivered, and exit. New work goes to `follow-ups.md`; do not extend the stack.

References:

- [decomposition](references/decomposition.md)
- [per-PR route contract](references/pr-build-cycle.md)
- [front watcher](references/monitor-loop.md)
- [stack mechanics](references/stacked-mechanics.md)
- [state files](references/state-files.md)
- [guardrails](references/guardrails.md)
