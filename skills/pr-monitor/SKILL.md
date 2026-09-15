---
name: pr-monitor
description: Handle one pull request attention event by reading one watcher digest, sending one authoritative worklist to pr-fix, re-observing once, and exiting without mutation.
---

# PR Monitor

Handle one attention event as a read-only router. The watcher observes; `pr-fix` is the sole mutation
owner. This skill never mutates code, git, GitHub, workflow state, checks, reviews, threads, or
auto-merge.

There is **no acknowledgement**: remote PR state is the record. This skill has **no loop** and owns no
schedule. One digest, one run, one exit.

## Read one digest

For a watcher wake:

```bash
relay pr watch digest "$SLUG" --fingerprint "$FP" --json
relay pr watch status "$SLUG" --json
```

For manual use:

```bash
relay pr watch tick "$SLUG" --json
```

`tick` is a fresh read-only observation. If no actionable items exist, report that and stop. The
digest already includes PR/base/head state, checks and run ids, reviews/comments/threads, merge state,
and normalized item reasons; do not re-derive it with your own `gh` sweep. Fetch nothing broadly.

## Delegate once

Triage only enough to preserve one authoritative worklist. Pass every actionable item to one
`pr-fix` sub-agent in delegated mode, using the authoritative worklist schema defined in that skill.
Do not restate or alter its item field contract here. Wrap it with `watcher_mode` from the digest and
`owner_slug` from watcher status, include the digest fingerprint, and tell `pr-fix` the list is
complete. Copy both values exactly; never infer mode or ownership from the PR.

This includes real or suspected CI failures, possible infra flakes, comments, reviews, unresolved
threads, conflicts, stale base, auto-merge state, closed-unmerged escalation, and stack-front state.
`pr-fix` decides and performs branch and ordinary PR mutations. In `stack` mode it returns
`auto-merge-not-armed` as `ready-for-owner` without mutation, because the named stack orchestrator is
the sole auto-merge owner. Never run two writers.

Every reply produced downstream must begin with:

```text
<!-- relay-agent-reply answers=<item answers token> -->
🤖 <agent> on behalf of <author>
```

The item's `answers` value identifies exactly what was answered and must be copied verbatim by
`pr-fix`.

## Re-observe once

After `pr-fix` returns, re-observe once by running:

```bash
relay pr watch tick "$SLUG" --json
```

An item is resolved only when this observation no longer reports it. Report remaining items,
delegated results, and any escalation, then exit. Never schedule another run, claim a local item was
handled, approve, or merge.

## Invariants

- `pr-monitor` remains read-only and never mutates the branch or GitHub.
- One digest produces at most one delegated `pr-fix` call.
- One re-observation decides what remains.
- Approval remains the only merge path; automated replies are disclosed and use exact answer markers.
