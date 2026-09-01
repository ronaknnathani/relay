# Relay PR Watch

`relay pr watch` is the deterministic runtime that observes one project's pull request and wakes that
project's exact live session when it needs attention. It is strictly read-only toward git, GitHub,
project workflow state, and program state: it never reruns a check, rebases, pushes, replies, resolves
a thread, arms auto-merge, approves, or merges. Every mutation belongs to the woken owner, which runs
`pr-monitor` once and delegates the fixing to `pr-fix`.

## Commands

```bash
relay pr watch start <project-slug> [--mode standalone|managed|stack] [--owner <session-slug>] [--json]
relay pr watch status <project-slug> [--json]
relay pr watch stop <project-slug>
relay pr watch tick <project-slug> [--json]
relay pr watch digest <project-slug> --fingerprint <64-hex> [--json]
relay pr watch acknowledge <project-slug> --fingerprint <64-hex> --outcome handled|escalated|obsolete [--json]
```

`start` and the hidden `run` process require Herdr: `start` hosts the watcher as a plain Herdr tab
labelled `relay-pr-watch:<project-slug>` and the watcher wakes a live pane. `status`, `stop`, `tick`,
`digest`, and `acknowledge` work with no Herdr, which is the manual path.

`tick` performs a fresh observation and records its digest without touching the watcher's schedule, so
it is safe to run beside a running watcher.

## Owner routing

| Mode | Who is woken |
|---|---|
| `standalone` | the project's own session |
| `managed` | the managed program's project worker — never the tech lead |
| `stack` | the stack orchestrator named by `--owner`, and only from the current front project |

The owner is the single live Herdr agent whose terminal title is the project's Relay identity. A
near-miss slug never matches, and duplicate claims refuse to resolve rather than guessing. The prompt
is payload-free apart from the identifiers the owner needs:

```
Run pr-monitor once for project <slug> using watcher fingerprint <fingerprint>.
```

A missing, duplicated, or busy owner leaves the attention pending until the next scheduled check. An
uncertain delivery — Herdr may have staged the prompt without submitting it — suppresses further
automatic wakes until the watcher is restarted, because retrying can duplicate text in the composer.

## Cadence

- The immediate observation at start is a **baseline**, not a scheduled check. A first-ever start
  records it without waking anyone; a restart may wake for attention that is still unacknowledged.
- Scheduled checks 1–4 run every 15 minutes, 5–6 every 30, and 7 onward every 60.
- A new head SHA or an acknowledgement resets the count to zero and the next check to 15 minutes.
- A pending check becoming another pending check is not a reset.
- The watcher wakes internally every 30 seconds to compare the clock with the next scheduled check and
  to pick up an acknowledgement another process recorded. Those wakes print nothing.

## What counts as actionable

Actionable: failing checks (the watcher never judges flake versus real), a `CHANGES_REQUESTED` review
decision, human conversation, review, inline, and thread activity the agent has not answered,
unresolved threads and new replies on answered ones, merge conflicts, a branch behind its base, an
approved green clean default-base pull request whose auto-merge is not armed, a pull request closed
without merging, and — in stack mode only — a merged front pull request.

Not actionable: pending or queued checks alone, an untouched review-required state, a draft alone, and
a digest that was already acknowledged. A merged pull request completes a standalone or managed watch
silently, with no owner wake.

## Runtime layout

```
~/.relay/run/pr-watch/<project-slug>/
  watch.lock                            lifetime singleton lock
  state.lock                            short mutation lock
  watch.json                            lifecycle, cadence, pull request, owner, current digest
  digests/<fingerprint>.json            immutable, mode 0600
  acknowledgements/<fingerprint>.json   immutable, mode 0600
```

Every record is written atomically at mode 0600. Digests are the only place comment bodies are kept;
watcher pane events carry reason codes, counts, pane ids, and a short fingerprint prefix, never a body,
a title, an author, or a whole fingerprint.

A fingerprint is the SHA-256 of the digest's sorted unique item keys, so it is stable across
re-observation of the same activity and never covers a body. No actionable items means an empty
fingerprint and no digest file.

## Acknowledgement

Acknowledging means every item in a digest was covered — handled, durably escalated, or obsolete. It
does not mean the pull request is green. The record is immutable: repeating the same outcome is a
no-op, and a different outcome for a recorded fingerprint is refused. Acknowledging also folds that
digest's exact item keys into the watcher's watermark, so the same activity stops surfacing while
newer activity on the same source still does.

Runtime records are pruned to the newest 100 digests and 200 acknowledgements. The current digest and
every unacknowledged digest are always retained, and only regular files are ever removed.
