# Watch only the front — Stack-specific monitoring

Observation of the front PR is owned by the **`relay pr watch`** runtime, and interpreting one of its
digests is owned by the read-only **`pr-monitor`** skill (triage → delegate remediation to `pr-fix`
→ re-observe → exit). The stack orchestrator owns any approved auto-merge mutation after the monitor
reports readiness. This file covers only what the **stack** adds.

> Resolve the repository's default branch dynamically and bind it to `<default-branch>`. Only the
> stack orchestrator may arm approved auto-merge, and only for the default-branch front PR.

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
If the result for `auto-merge-not-armed` is `ready-for-owner`, verify `watcher_mode=stack` and
`owner_slug=<stack-orchestrator-slug>`, then let the orchestrator arm auto-merge. Do not send that
mutation back to `pr-fix`.

## Front-advance (when the front PR merges)

A merged front PR is actionable in stack mode: the watcher wakes you with a `stack-front-merged` item
instead of quietly completing. Capture the front PR's tip **before** it merges (a squash-merge drops
that commit from the default branch), so `<merged-parent-tip>` below stays valid. Do not rely on GitHub
auto-retargeting. Once it merges:

```bash
relay pr watch stop <front-project-slug>          # also closes the watcher's Herdr tab
git fetch origin
git rebase --onto origin/<default-branch> <merged-parent-tip> <next-branch>
git push --force-with-lease origin <next-branch>
gh pr edit <next-pr> --base <default-branch>
gh pr view <next-pr> --json baseRefName,mergeStateStatus
relay route advance <next-project-slug> --base <default-branch> --advance-token <token>
relay pr watch start <next-project-slug> --mode stack --owner <stack-orchestrator-slug>
```

The base update is required after squash merges, merge commits, and deleted parent branches: it
atomically records the new base identity and immutable start SHA, recomputes route freshness, and
consumes the child-issued capability so it cannot be reused.
Confirm `baseRefName == "<default-branch>"`. Then verify every other descendant still targets its
intended parent feature branch (not the default branch), and let the new front watcher wake you once
auto-merge can be armed on the default-branch PR.

Stopping the old watcher is **your** job and it is not optional. A merged front PR stays actionable in
stack mode — the watcher has no local record of "handled", so it re-observes the same merge and wakes
you again until you stop it.

## Cascade (after any content change to a PR with descendants)

Every commit added to a PR can break its descendants. After any push to a PR that has descendants,
delegate a cascade under the **Cascade every parent content change** guardrail: for each descendant,

```bash
git rebase --onto <new-tip> <old-tip> <descendant>
# build + test, then:
git push --force-with-lease origin <descendant>
```

Verify each descendant's base ref did not collapse to the wrong branch, update the new descendant
tips in `plan.md`, and append the event with `relay state log <stack-project-slug> "<summary>"`.
Never edit `state.json` directly. Serialize per branch under the **One writer per branch** guardrail.

## Auto-merge across the stack

Only the stack orchestrator arms auto-merge, and only on a default-branch front PR after a read-only
`pr-monitor` result reports readiness. Descendants wait their turn. As each PR merges, front-advance
promotes the next one and it becomes eligible. See [stacked-mechanics.md](stacked-mechanics.md) for
the `--onto` rebase, freshness rebases, and transient-401 retry details.
