# Phase 2 — Monitor the front PR (stack-specific layer)

The per-PR tick routine — detect (CI, PR-level review bodies, inline threads, new replies, staleness,
conflicts) → delegate remediation to `pr-fix` → reconcile merge state → re-arm auto-merge → stop at
merge, in native-loop or one-tick-per-resume mode — is owned by the **`pr-monitor`** skill. Run exactly
one `pr-monitor` against the **front PR** (the one based on `master`). Never monitor a non-front PR as a
merge candidate — it cannot merge yet. This file covers only what the **stack** adds.

> Throughout, `master` denotes the repository's **default branch**; substitute the real default
> (`main`, etc.) in the commands below. `pr-monitor` already arms auto-merge only on a default-branch
> PR — the stack's front PR is exactly that.

## Run pr-monitor in orchestrator-driven ticks for a stacked front PR

`pr-monitor` changes the front PR during a tick (via `pr-fix`: CI fix, review fix, freshness rebase,
conflict resolve), and **every such push can break descendants**. So the orchestrator must see each
push. **Do not run `pr-monitor` as a fire-and-forget native loop for a front PR that has descendants** —
run it tick-by-tick under the orchestrator, and require each tick to report the front PR's
`old-tip → new-tip` whenever it pushed. After a reported tip change, run the **cascade** (below) before
the next tick. A front PR with **no** descendants may use a plain native loop.

## Front-advance (when the front PR merges)

Capture the front PR's tip **before** it merges (a squash-merge drops that commit from `master`), so
`<merged-parent-tip>` below stays valid. Do not rely on GitHub auto-retargeting. Once it merges:

```bash
git fetch origin
git rebase --onto origin/master <merged-parent-tip> <next-branch>
git push --force-with-lease origin <next-branch>
gh pr edit <next-pr> --base master
gh pr view <next-pr> --json baseRefName,mergeStateStatus   # confirm baseRefName == "master"
```

Then verify every other descendant still targets its intended parent feature branch (not `master`),
point `pr-monitor` at the new front PR, and let it arm auto-merge now that the base is `master`.

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
