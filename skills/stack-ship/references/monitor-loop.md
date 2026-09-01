# Phase 2 — Monitor the front PR (stack-specific layer)

Observation of the front PR is owned by the **`relay pr watch`** runtime, and interpreting one of its
digests is owned by the **`pr-monitor`** skill (triage → delegate remediation to `pr-fix` → re-arm
auto-merge → acknowledge → exit). This file covers only what the **stack** adds.

> Throughout, `master` denotes the repository's **default branch**; substitute the real default
> (`main`, etc.) in the commands below. `pr-monitor` arms auto-merge only on a default-branch PR — the
> stack's front PR is exactly that.

## Point the watcher at the front PR, with yourself as the owner

The front project's own session is a `deliver-pr` sub-agent that ends when the PR opens, so it cannot
be the owner. Start the watcher yourself and name your orchestrator session:

```bash
relay pr watch start <front-project-slug> --mode stack --owner <stack-orchestrator-slug>
relay pr watch status <front-project-slug> --json      # mode, owner, cadence, current digest
```

`--owner` is required in stack mode, and only the current front project's watcher ever wakes you.
Never start a watcher for a non-front PR: it cannot merge yet, so nothing it observed would be
actionable. A `deliver-pr` sub-agent must not start one either — its owner validation fails by design,
because the surrounding pane is yours, not the project's.

When the watcher wakes you, run `pr-monitor` **once** for the fingerprint it names. Because a `pr-fix`
push can break descendants, require that run to report the front PR's `old-tip → new-tip` whenever it
pushed, and run the **cascade** below before anything else.

## Front-advance (when the front PR merges)

A merged front PR is actionable in stack mode: the watcher wakes you with a `stack-front-merged` item
instead of quietly completing. Capture the front PR's tip **before** it merges (a squash-merge drops
that commit from `master`), so `<merged-parent-tip>` below stays valid. Do not rely on GitHub
auto-retargeting. Once it merges:

```bash
relay pr watch acknowledge <front-project-slug> --fingerprint <fp> --outcome handled
relay pr watch stop <front-project-slug>
git fetch origin
git rebase --onto origin/master <merged-parent-tip> <next-branch>
git push --force-with-lease origin <next-branch>
gh pr edit <next-pr> --base master
gh pr view <next-pr> --json baseRefName,mergeStateStatus   # confirm baseRefName == "master"
relay pr watch start <next-project-slug> --mode stack --owner <stack-orchestrator-slug>
```

Then verify every other descendant still targets its intended parent feature branch (not `master`),
and let the new front watcher wake you once auto-merge can be armed on the `master`-based PR.

## Cascade (after any content change to a PR with descendants)

Every commit added to a PR can break its descendants. After any push to a PR that has descendants,
delegate a cascade (guardrails.md #10): for each descendant,

```bash
git rebase --onto <new-tip> <old-tip> <descendant>
# build + test, then:
git push --force-with-lease origin <descendant>
```

Verify each descendant's base ref did not collapse to the wrong branch, and record the new descendant
tips in `state.json` + `progress.md`. Serialize per branch (guardrails.md #5).

## Auto-merge across the stack

`pr-monitor` arms auto-merge only on a `master`-based PR, so in a stack **only the front PR** is ever
armed; descendants wait their turn. As each PR merges, front-advance promotes the next one and it
becomes eligible. See [stacked-mechanics.md](stacked-mechanics.md) for the `--onto` rebase, freshness
rebases, and transient-401 retry details.
