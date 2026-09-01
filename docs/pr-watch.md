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
relay pr watch stop <project-slug> [--json]
relay pr watch tick <project-slug> [--json]
relay pr watch digest <project-slug> --fingerprint <64-hex> [--json]
```

`start` and the hidden `run` process require Herdr: `start` hosts the watcher as a plain Herdr tab
labelled `relay-pr-watch:<project-slug>` and the watcher wakes a live pane. `status`, `stop`, `tick`, and
`digest` work with no Herdr, which is the manual path.

`tick` performs a fresh observation and records its digest without touching the watcher's schedule, so
it is safe to run beside a running watcher.

Every observation is read-only and complete. Conversation comments, review bodies, and inline comments
are read through `gh api --paginate`; review threads and the newest commit's check contexts through
paginated GraphQL connections. A check list that GitHub still reports another page for is an
observation error, never a shorter list quietly accepted — a truncated check list is
indistinguishable from a green pull request.

## Owner routing

| Mode | Who is woken |
|---|---|
| `standalone` | the project's own session |
| `managed` | the managed program's project worker — never the tech lead |
| `stack` | the stack orchestrator named by `--owner`, and only from the current front project |

The owner is the single live Herdr agent whose terminal title is the project's Relay identity. A
near-miss slug never matches, and duplicate claims refuse to resolve rather than guessing.

`start` proves that owner exists **before it creates the watcher tab**. Zero live owners or two of
them fails the command, and no tab and no process are created: a watcher whose owner does not exist
observes a pull request forever and hands its work to nobody, and that is not discovered until an
owner wake is finally attempted hours later in a pane nobody is reading. This is also why a
`deliver-pr` sub-agent inside a stack run cannot start one — the surrounding pane belongs to the
orchestrator, so nothing is titled for the child project.

`--mode managed` proves more, because managed mode claims this project is one program's work item and
its owner is that item's worker. Before creating anything it checks that the project has a readable
`assignment.md`, records a program and a work item, and that the work item exists and names this exact
project back.

The prompt is payload-free apart from the identifiers the owner needs:

```
Run pr-monitor once for project <slug> using watcher fingerprint <fingerprint>.
```

A missing, duplicated, or busy owner leaves the attention pending until the next scheduled check. An
uncertain delivery — Herdr may have staged the prompt without submitting it — suppresses further
automatic wakes until the watcher is restarted, because retrying can duplicate text in the composer.

## Cadence

- Every start runs an immediate observation. It is not a scheduled check, and it wakes the owner right
  away if the pull request already needs attention — first start or restart alike.
- Scheduled checks 1–4 run every 15 minutes, 5–6 every 30, and 7 onward every 60.
- A new head SHA, or a different set of actionable items — including attention appearing or clearing
  entirely — resets the count to zero and the next check to 15 minutes.
- A pending check becoming another pending check changes neither, so it is not a reset.
- Every watcher process start resets the count to zero and the next check to 15 minutes.
- An undelivered wake because the owner was missing, duplicated, busy, or unreachable holds the fast
  cadence and spends no scheduled check: the pull request did not get quieter, the delivery failed.
- The watcher wakes internally every 30 seconds to compare the clock with the next scheduled check.
  Those wakes print nothing.

## What counts as actionable

Actionable: failing checks (the watcher never judges flake versus real), a `CHANGES_REQUESTED` review
decision, human conversation, review, inline, and thread activity the agent has not answered,
unresolved threads and new replies on answered ones, merge conflicts, a branch behind its base, a
merge GitHub is blocking for a reason nothing else in the digest explains, an approved green clean
default-base pull request whose auto-merge is not armed, a pull request closed without merging, and —
in stack mode only — a merged front pull request.

Not actionable: pending or queued checks alone, an untouched review-required state, and a draft alone.

Nothing about a previous observation is carried into a new one. There is no acknowledgement, no
watermark, and no local claim that attention was handled: an item stops being reported only when the
current remote truth no longer shows it.

### Failing checks

A check is failing when its conclusion is failure, error, timed-out, action-required,
startup-failure, **canceled**, or **stale**. A required check that ends either way never reports a
result, so the pull request silently cannot merge and only a rerun clears it. Neutral and skipped are
deliberately not failures — GitHub counts both as satisfying a required check.

### A merge GitHub is blocking

`BLOCKED` is reported as the waiting code `merge-blocked` when a failing check, a pending check, a
draft, a review-required or changes-requested decision, a conflict, or a stale base already explains
it. When none of those do, it becomes the actionable `blocked` item: otherwise an approved, green,
unconflicted pull request that GitHub simply refuses to merge looks perfectly quiet, and the watcher
backs off to an hourly check while the owner watches it never merge.

## Telling an agent reply from a human one

Every automated Relay reply opens with the exact marker `<!-- relay-agent-reply -->` on a line of its
own, then the visible `🤖 <agent> on behalf of <author>` disclosure. The marker is the only signal the
watcher uses. An emoji, a phrase, or an author login can all be typed by a human quoting or joking
about a bot, and mistaking one of those for an agent reply silences live review feedback; quoting an
earlier agent reply indents the marker behind `> `, so it is still human activity.

Each source is reconciled on its own, and only against replies on that same source:

| Source | Answered by |
|---|---|
| conversation comment | a later marked agent comment in the conversation |
| review body | a later marked agent review |
| inline comment | a later marked agent reply chained onto that comment's thread |
| review thread | a later marked agent reply in that thread, or the thread being resolved |

A reply posted anywhere else answers nothing. Reconciling sources together let one reply mark several
independent pieces of feedback as handled, which silently dropped review comments nobody addressed.

## Terminal states

| Outcome | What the watcher does |
|---|---|
| merged, standalone or managed | finishes immediately and silently, with no owner wake |
| closed without merging | wakes the owner with the escalation, then finishes once that wake is *delivered* — an undelivered escalation holds the fast cadence and retries |
| merged stack front | wakes the orchestrator every check until the orchestrator runs `relay pr watch stop`, because only it knows the front-advance is done |
| three consecutive observation failures | fails visibly rather than running blind |

A watcher that reached a terminal state releases its lock and its process exits, and prints the
`relay pr watch stop` command that closes its tab. It never closes its own tab: doing so would race
with flushing its final output, and the pane is the only log it has.

`relay pr watch stop` signals the exact recorded pid, waits for the watcher to release its lock, then
closes the exact Herdr tab recorded in `watch.json` — including for a watcher that already finished on
its own. It never guesses at a tab, and when Herdr is unavailable it says which tab is still open and
the exact `herdr tab close <id>` that closes it rather than claiming a cleanup it did not perform.

## Runtime layout

```
~/.relay/run/pr-watch/<project-slug>/
  watch.lock                            lifetime singleton lock
  state.lock                            short mutation lock
  watch.json                            lifecycle, cadence, pull request, owner, tab, current digest
  digests/<fingerprint>.json            newest observation of one item set, mode 0600
```

Every record is written atomically at mode 0600. Digests are the only place comment bodies are kept;
watcher pane events carry reason codes, counts, pane ids, and a short fingerprint prefix, never a body,
a title, an author, or a whole fingerprint.

A fingerprint is the SHA-256 of the digest's sorted unique item keys, so it is stable across
re-observation of the same activity and never covers a body. No actionable items means an empty
fingerprint and no digest file.

## Digests

A fingerprint fixes a digest's item set, so re-observing the same activity always lands on the same
record. Everything else in that record — the head SHA, merge state, review decision, auto-merge, the
waiting codes, and the bodies themselves — is refreshed on every observation and written atomically,
so a reader never acts on a stale snapshot of a pull request that has moved on.

Digests are pruned to the newest 100 records. The digest the watcher currently carries is always
retained, and only regular files are ever removed.
