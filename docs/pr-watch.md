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
paginated GraphQL connections.

A GraphQL list whose final page still reports `hasNextPage` is an observation error, never a shorter
list quietly accepted: a truncated check list is indistinguishable from a green pull request, and a
truncated thread list from one whose reviewers are all answered. A response carrying a GraphQL
`errors` list fails the same way — GitHub answers a partially failed query with a zero exit code, so
a thread it declined to return would otherwise look like a thread that does not exist.

REST collections carry no cursor and no total, so their completeness rests on `gh api --paginate`
itself: it follows the Link header's `rel="next"` until GitHub stops sending one and exits nonzero the
moment a page fails, so a zero exit means every page was read. There is nothing in the payload to
verify afterwards and nothing worth inventing — a guessed "full page" heuristic breaks exactly where
GitHub's page size is not what it was guessed to be. What the watcher does enforce is that a failed
command and a page stream that does not decode cleanly are both observation errors.

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
decision nobody has answered, human conversation, review, inline, and thread activity the agent has
not answered, unresolved threads, merge conflicts, a branch behind its base, a merge GitHub is
blocking for a reason nothing else in the digest explains, an approved green clean default-base pull
request whose auto-merge is not armed, a pull request closed without merging, and — in stack mode
only — a merged front pull request.

Not actionable: pending or queued checks alone, an untouched review-required state, a draft alone, a
changes-requested review an anchored reply already answered, and a resolved thread.

Nothing about a previous observation is carried into a new one. There is no acknowledgement, no
watermark, and no local claim that attention was handled: an item stops being reported only when the
current remote truth no longer shows it.

### Review threads

An unresolved thread is actionable while it holds human activity no anchored agent reply names. A
**resolved** thread never is, whatever it contains — an agent reply, no agent reply, or a human
speaking last. Resolution is GitHub's own current truth and the one signal a human controls directly:
somebody decided that conversation is finished. A reviewer who is not finished, or who comes back to
it, leaves the thread unresolved, and the thread comes back with them.

Reporting a resolved thread as an unresolved one sent the owner to argue with a settled conversation,
which is what a thread two reviewers resolved between themselves did on every single check.

### A changes-requested review that was answered

GitHub keeps reporting `CHANGES_REQUESTED` until the same reviewer submits another review, so a
decision reported as actionable on its own woke the owner — and started another writer — on every
check forever, rewriting an answer that was already posted and pushed. The decision is reported
against the exact review that requested the changes, and once an anchored Relay review answers that
exact review it becomes the waiting code `changes-requested-awaiting-rereview`: still blocked, still
honest, and nobody's turn but the reviewer's.

Only current remote evidence makes it actionable again — a new review, a human edit of that review,
or a decision GitHub no longer reports as `CHANGES_REQUESTED`. Pushing a new head is not evidence: it
restarts the fast cadence, because the pull request changed, but it wakes nobody, because the
reviewer has not looked at it yet.

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

Every automated Relay reply opens with an exact marker on a line of its own, then the visible
`🤖 <agent> on behalf of <author>` disclosure:

```
<!-- relay-agent-reply answers=comment:200 -->
```

The marker is the only signal the watcher uses. An emoji, a phrase, or an author login can all be
typed by a human quoting or joking about a bot, and mistaking one of those for an agent reply silences
live review feedback; quoting an earlier agent reply indents the marker behind `> `, so it is still
human activity.

**The marker names the exact activity it answers, and answers nothing else.** Every actionable item
carries the token a reply must copy in its `answers` field, so an agent never derives one:

| Source | Item `answers` token | Answered by |
|---|---|---|
| conversation comment | `comment:<comment-id>` | a later marked agent comment in the conversation naming that id |
| review body | `review:<review-id>` | a later marked agent review naming that id |
| inline comment | `inline-comment:<comment-id>` | a later marked agent inline reply naming that id |
| review thread | `review-thread:<thread-id>:<comment-id>` | a later marked agent reply in that thread naming that comment |

A reply posted anywhere else answers nothing, because each source is reconciled only against replies
on that same source. A reply *is* an answer when it is no older than the activity it names, so a human
edit that moves an activity past the reply makes it feedback again, and a brand-new comment, review,
reply, or thread reply is a different id that nothing has answered yet.

Anchoring on an id rather than on a timestamp is the whole point. A stream-wide "latest agent reply"
time hid real feedback: the watcher saw comment A at 10:15, a reviewer wrote comment B at 10:19 that no
digest had ever carried, and the agent's 10:20 answer to A made B look answered — permanently, because
nothing about a later observation would ever bring it back.

A thread's token names the exact comment the digest reported rather than the thread alone, for the same
reason: a reply that arrived beside it is a different item the watcher must still be able to surface.

### The bare marker

`<!-- relay-agent-reply -->`, the marker Relay wrote before it named what it answered, is still
recognized as an agent reply — such a reply is never actionable itself. It *answers* exactly one
thing: the inline comment GitHub itself chained it to through `in_reply_to_id`. That id is GitHub's,
not the agent's, so it cannot silently cover a sibling comment the reply never saw.

There is no equivalent for a conversation comment, a review body, or a thread reply. Nothing there
ties a bare reply to a single activity, so accepting one would hide whatever else was written before
it. A bare reply on those sources answers nothing and the item keeps waking the owner until an
anchored reply lands — noisy, and the safe direction to be wrong in.

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
