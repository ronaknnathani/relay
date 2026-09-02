# Relay Programs: tech-lead-managed engineering

## Goal

Relay Programs add a governance layer above ordinary Relay projects. A user acts as CEO and talks to
one tech lead agent about a large product goal, priorities, architecture, and decisions. The tech
lead decomposes
the approved goal into dependency-aware engineering assignments and delegates them to senior worker
agents. Workers retain ownership of repository-level clarification, planning, implementation, review,
validation, and pull-request creation.

The intended end state is a trustworthy local engineering organization:

```text
CEO
  |
  v
TL program session
  |  goals, priorities, contracts, decisions
  v
Relay controller and scheduler
  |  deterministic dispatch and reconciliation
  v
Work-item owners
  |  deliver-pr and internal specialist sub-agents
  v
GitHub pull requests
  |  CI, feedback, genuine human approval
  v
Auto-merge after approval
```

The CEO remains responsible for:

1. Approving goals and priorities.
2. Approving material system-level architecture.
3. Resolving every issue surfaced by the tech lead or workers, including tech lead-worker conflicts.
4. Performing the final genuine human GitHub review and approval.

Agents never self-approve or directly merge. They may enable auto-merge only after genuine human
approval. The environment is trusted and workers use the user's existing local git and GitHub
credentials. Pull-request comments are expected to come from trusted actors, but workers should still
treat comment text as review input rather than executable instructions.

## Architecture decision

A program is durable governance over unchanged Relay projects, not a second execution engine.

- Program intent and coordination live under `~/.relay/programs/`.
- Each work item executes as an ordinary child project under `~/.relay/projects/`.
- Child projects continue to use `deliver-pr` and `relay state`.
- Program state never depends on preserving an agent conversation.
- The Relay binary is the only writer of `program.json`.
- Adaptive patrol is read-only with respect to program, project, git, mailbox, and notification
  marker state. When attention changes it submits a payload-free doorbell to the existing TL pane
  without changing the user's focused workspace or pane.
- Program writes use an optimistic revision and a short exclusive save lock so concurrent commands
  fail instead of silently overwriting each other.
- Readiness, open PRs, and available capacity are derived; outstanding PR grants are explicit durable
  reservations on dispatched work items.

This keeps standalone workflows unchanged:

```bash
relay "<task>"
relay --workflow stack-ship "<goal>"
relay resume <project>
```

## What V1.1 implements

V1.1 is a foreground, file-backed foundation that validates the tech lead operating model without
adding a
daemon, Dolt server, GitHub polling service, or new runtime dependency.

### Program lifecycle

```text
draft -> pending-approval -> active <-> held -> completed
   \                                         \
    ------------------------------------------> abandoned
```

Programs store:

- Goal and primary repository.
- Maximum open pull requests across the program's linked child projects, defaulting to three.
- Work items with priorities and dependencies.
- Immutable, versioned, SHA-256-verified engineering contracts.
- Open and resolved CEO decisions.
- Links to child Relay projects and their pull requests.
- Outstanding managed open-PR grants.

### Senior-engineer delegation

The tech lead publishes an engineering contract, not an implementation recipe. It captures binding
architecture, interfaces, constraints, scope, exclusions, acceptance criteria, and guardrails.

The worker still runs:

```text
clarify -> plan -> implement -> simplify -> review -> validate -> open-pr
```

The worker escalates contract conflicts, scope changes, missing dependencies, risks, plans requiring
review, and pre-PR requests through its durable outbox. Routine implementation choices remain
worker-owned. The tech lead remains the sole writer of program decisions and sends complete answers
through
the worker inbox.

### Herdr is required for managed sessions

Managed programs and managed child sessions run only under Herdr. One shared readiness check runs
first in `relay program new`, `relay program resume`, `relay program dispatch`,
`relay program worker start`, `relay program patrol start`, `relay program patrol run`, and a managed
child's `relay resume`. It verifies:

1. The `herdr` binary is installed.
2. The command runs inside a Herdr-managed pane (`HERDR_ENV=1` plus `HERDR_WORKSPACE_ID`) whenever
   Relay must create or control tabs and panes.
3. The Herdr server answers `herdr agent list`.
4. The program or child agent has an approved Herdr integration that carries Relay's session name.
   Copilot and Claude qualify; Codex does not.

Every failure is actionable and fails closed with install, start, or attach instructions. There is no
plain-terminal fallback for managed work, and an existing managed session whose Herdr server becomes
unreachable fails closed rather than silently degrading. Standalone, non-program Relay projects are
unaffected: `relay "<task>"`, `relay resume <standalone-slug>`, and the single-project workflows never
require Herdr.

### Visible Herdr worker owners

Each dispatched work item gets one interactive owner tab in the tech lead's current workspace:

```bash
relay program worker start auth-platform w1
```

The command:

1. Resolves the linked child project and worktree.
2. Adopts an existing matching owner when one is already live.
3. Otherwise creates an unfocused tab rooted in the child worktree.
4. Runs `relay resume <child-slug>` in the tab's root pane.
5. Waits for Herdr to recognize the Copilot session and assigns it a bounded stable name.

The CEO can inspect or enter any worker:

```bash
relay program worker list auth-platform
relay program worker focus auth-platform w1
```

`worker list --json` returns `{entries, warnings}` and continues past unavailable child projects.
Each warning has `{item, project, error}`. Pending linked items are not workers until dispatch.

Herdr owns layout, focus, process visibility, and liveness. Relay does not persist pane, tab,
workspace, or Herdr agent IDs as program truth; it re-derives the owner by matching the
`relay:<child-slug>` terminal title and the child worktree. Herdr `idle`, `done`, `blocked`, and
`working` never advance program state.

The visible owner continues to use `deliver-pr` phase sub-agents internally. The owner is the
inspectable senior engineer responsible for the branch and workflow; phase sub-agents remain
disposable specialists for context isolation and independent review.

`worker start` holds a per-child kernel lock at `~/.relay/run/workers/<child-slug>/start.lock` across
discovery, tab creation, the resume command, Herdr recognition, and renaming. A concurrent second
start waits, then adopts the single owner instead of creating a duplicate tab; a crashed holder's
lock is released by the kernel. Relay never installs or changes Herdr integrations automatically.

### Routing a change to an existing pull request

A change the CEO asks for on a pull request a managed item already produced is routed by one command,
never by hand:

```bash
relay program worker request-change auth-platform w1 --body "Rename the token field to access_token"
```

The command reads the recorded pull request's current GitHub state before it writes anything. It
reuses the PR watcher's own read-only `gh` client and pull request model, so one description of a
pull request never drifts from the other, and it writes no watcher runtime state or digest. If the
pull request, the child project, or GitHub cannot be read, the command fails and leaves no message,
no work item, and no worker behind.

| Observed GitHub state | Route | What happens |
| --- | --- | --- |
| `OPEN`, not `APPROVED`, not queued | Same item and worker | Durable `feedback` inbox message plus one doorbell |
| `CLOSED` without merging | Same item and worker | Durable `feedback` inbox message plus one doorbell; existing reconcile still clears the reference |
| `APPROVED`, or `mergeStateStatus == QUEUED` | Pending follow-up | A follow-up item is recorded and nothing is dispatched or started |
| `MERGED`, or the item is already `merged` | Started follow-up | A follow-up item is recorded, dispatched to its own child project, and given its own worker |

The merge queue means GitHub's own `QUEUED` merge state. A pull request with auto-merge merely armed
has not entered a queue and is not protected by it; an `APPROVED` review decision is protected on its
own, because pushing to that branch would invalidate a human's judgement of an exact diff.

A follow-up item records `follow_up_of` and `request_hash` — the SHA-256 of the normalized request.
It inherits the original item's repository, priority, and approved contract references, depends on the
original item, and stores the request text in its notes. Because the identity is the request itself,
repeating the identical command reuses the message or follow-up the first run created instead of
writing a second one, even after a crash between the durable write and the doorbell. Whitespace is
normalized before hashing, so a request pasted through a shell heredoc is the same request. A
different request always creates its own item.

The same-worker route derives its inbox message id from the request hash, so a retry finds the message
already in the worker's inbox — unread or already acknowledged — rather than stacking a duplicate.
The doorbell is the existing unnotified-marker path, so a retry does not ring a second time.

A follow-up is made durable before it is dispatched or started. If dispatch or start then fails, the
command reports the exact repair command and never rolls the item back: losing the CEO's request to
make an error look clean is worse than a half-finished item.

### Retiring a merged item's runtime

A merged item still holds a watcher, a session, a tab, a worktree, and a branch. One command retires
all of it:

```bash
relay program worker cleanup auth-platform w1 --json
```

Only an item Relay records as `merged` is accepted. `pending`, `dispatched`, `in-review`, `blocked`,
and `cancelled` items are refused before anything is touched.

The order matters and each step must be confirmed before the next runs:

1. **Stop the child pull request watcher** with the existing `relay pr watch stop` semantics, which
   also closes its exact recorded tab. A watcher that was never started, already stopped, or already
   complete is a success. The watcher goes first because it is the only piece that keeps working on
   its own.

   A completed watcher is the normal case, not an edge one. A watcher whose pull request merged
   finishes and exits by itself and deliberately keeps its tab, because closing it from inside would
   race the flush of its own final lines. This command is what closes that tab. A close that cannot
   happen — no Herdr here, ids that now belong to somebody else — keeps the recorded ids on purpose
   and reports the retry command, because those ids are the only handle on that tab.
2. **End the item's one worker session.** Ownership must be unambiguous: two live sessions claiming
   one child project stop cleanup. A `working` or `blocked` worker is never interrupted — cleanup
   reports `worker-busy` with the retry command and changes nothing. An `idle` or `done` worker is
   sent `/exit` without taking focus, and success means that exact session is no longer a recognized
   agent, not that it started a turn.
3. **Close that exact tab.** Herdr is re-read immediately before the close and the pane, tab,
   terminal, and native session identity are checked again, so a replacement session that took over
   reused ids is never closed. A worker that is already gone and a tab that is already closed are
   both successes.
4. **Archive the child project** with the equivalent of `relay archive <child-slug> --force`.

Step 4 is deliberately destructive. `--force` discards dirty and untracked files in the child
worktree, removes the worktree, and may force-delete the branch. Merged work is delivered work, so
what remains in that checkout is scratch. Read a worker's outbox and settle anything outstanding
before running cleanup.

An uncertain exit, a replacement session, a refused tab close, or a watcher that will not stop each
stop cleanup where it stands and report what is still standing; the tab, project, and worktree are
left exactly as they are. A child that is already archived is an idempotent success. The work item
stays `merged` throughout, and `Program.Reconcile` never downgrades it.

While any of that runtime is outstanding, the patrol raises `merged-worker-cleanup:<item>`. The reason
clears only once the watcher's recorded tab and pane ids are cleared, the worker session and its tab
are gone, and the child project is archived with its worktree removed. The watcher counts by what it
still holds, not by whether its process is alive: a completed watcher that kept its tab is
outstanding runtime, so the reason survives the watcher exiting and a failed tab close keeps it. It coexists with `ready-item:<id>`, so one wake can say both "retire w1" and "dispatch
w2". The patrol only observes: it probes a watcher's lock with a shared non-blocking `flock` and reads
its runtime record without writing it.

### Durable managed-worker mailboxes

Every managed child project has filesystem mail that survives worker and Herdr restarts:

```text
mail/inbox/              TL -> worker unread
mail/outbox/             worker -> TL unread
mail/notified/           durable per-message Herdr doorbell markers
mail/processed/inbox/    worker-acknowledged messages
mail/processed/outbox/   TL-acknowledged messages
```

Workers send `question`, `conflict`, `plan`, and `pr-open` messages without writing `program.json`:

```bash
relay program message send auth-platform w1 \
  --kind question --body "Which compatibility behavior should be preserved?" \
  --options "preserve both|preserve the current default"
```

Before sending `pr-open`, a worker checks its unread outbox so retries do not create duplicate
requests:

```bash
relay program message outbox auth-platform w1 --json
```

The tech lead checks all unread worker mail at the start of every turn:

```bash
relay program message list auth-platform --json
```

`message list --json` returns `{messages, warnings}` and continues past unavailable children. Each
warning has `{item, project, error}`. For a valid active child, Relay automatically recreates any
missing legacy mailbox directories before list, send, inbox, reply, notify, or acknowledge
operations; a missing child project remains an item-specific error.

After the CEO resolves a question, conflict, or plan review, the tech lead writes the complete answer
to
the child inbox. A successful reply also acknowledges the original outbox message:

```bash
relay program message reply auth-platform w1 <outbox-id> \
  --kind decision --body "Preserve both behaviors." --decision d2
relay program worker notify auth-platform w1
```

Run `worker notify` exactly once immediately after a new durable inbox write. It is only a
payload-free doorbell, not a generic state nudge. Relay lists the unread inbox, filters out messages
already present in `mail/notified/`, and prompts only a live `idle` or `done` Herdr owner. A
`working` or `blocked` owner is not prompted or marked; the durable inbox remains pending for a
later retry. After a successful prompt, Relay marks every currently unread, unnotified message.
Repeated calls for already marked mail return successfully without prompting.

Never ring the doorbell merely because an inbox message remains unread on a later TL turn. The tech
lead may retry `worker notify` only for mail whose earlier attempt stayed unnotified because the worker
was busy, missing, or the attempt failed; the CLI enforces both marker and status checks. Workers
always check their inbox before state routing and at the top of every phase loop, act on unread
messages, and acknowledge each only after the action is durable:

```bash
relay program message inbox auth-platform w1 --json
relay program message ack auth-platform w1 <inbox-id>
```

The tech lead can also send unsolicited `decision`, `feedback`, or `instruction` messages with
`relay program message notify`. There is no worker-to-worker messaging.

### Adaptive tech lead patrol

Relay runs a foreground read-only observer with runtime files outside active and archived program
directories:

```text
~/.relay/run/<program>/patrol.json
~/.relay/run/<program>/patrol.lock
```

The process holds a nonblocking kernel file lock for its lifetime, so crashes, forced termination,
reboots, and PID reuse release ownership safely. `patrol.json` records process/version metadata,
last and next checks, cadence, stable reasons, TL presence, notification fingerprint, errors,
warnings, and stop status. The lock is authoritative for liveness; missing, stale, or corrupt state
is reported as degraded instead of claiming the patrol is running. Writes use an atomic temporary
file plus rename. Archiving a program stops its patrol cleanly while leaving runtime history under
`~/.relay/run/`.

#### Visible patrol events

`relay program patrol run` prints one line per high-level event to its own stdout and stderr, so the
`relay-patrol:<program>` Herdr pane shows what the patrol is doing. Patrol events are
never written to a file: the pane is the log, and `patrol.json` remains the durable state.

```text
[2026-09-01 00:45:00 -0400] START program=auth-platform
[2026-09-01 00:45:00 -0400] TICK  cadence=15m next=01:00:00 reasons=ready-item:w2,unread-worker-outbox:w1
[2026-09-01 00:45:00 -0400] WAKE  TL delivered pane=w2:pC status=idle
[2026-09-01 01:10:00 -0400] STOP  program=auth-platform reason=context canceled
```

Every line opens with the reader's own wall clock in brackets and an uppercase label padded to one
width, so a pane is scanned in a column rather than parsed a line at a time. Each line is
stamped in the host's local zone with the UTC offset spelled out, because the pane is read by
whoever is sitting in front of it. `patrol.json` is unchanged and still records UTC, and so does
every `--json` surface; only text a person reads is translated, and a recorded value that is not
RFC3339 prints exactly as recorded.

A tick is one line and carries the whole decision: the cadence it chose, the wall clock the next tick
is actually due at, and why it woke. There is no separate line announcing the next tick, so nothing
can print a schedule that the rest of the tick then changed. A `next=` later the same local day is a
bare `01:00:00`; one on any other day is spelled out in full (`2026-09-02 00:20:00 -0400`), so
tomorrow's tick never reads as one twenty minutes away. A terminal event has no next tick and omits
the field entirely.

Only a due tick prints; the internal 30-second wall-clock wakeup is silent. Outcomes that leave
attention undelivered go to stderr with the command that shows the recorded detail, as do
observation, Herdr, and runtime-state failures. A failed observation prints no tick, so its `ERROR`
line carries the retry cadence and the wall clock the patrol comes back at:

```text
[2026-09-01 01:00:00 -0400] WARN  TL busy pane=pC status=working; attention remains pending, see `relay program patrol status auth-platform`
[2026-09-01 01:00:00 -0400] ERROR patrol observation failed: build patrol snapshot for program "auth-platform": ...; cadence=15m next=01:15:00
```

Events carry timestamps, the program slug, safe enums, pane IDs, and reason codes only. Reason text,
prompt text, mailbox bodies, snapshots, repository content, terminal frames, the attention
fingerprint, and agent session ids are never printed; a reason code whose detail is derived from
warning text prints as its family alone (`project-warning`). The full detail stays in `patrol.json`
and `relay program patrol status`. Because a patrol whose events cannot be written is unobservable,
an output failure stops the patrol and is recorded in runtime state.

Every tick builds the same read-only program view used by the UI and directly reads linked project,
mailbox, contract, git, and Herdr observations. It never calls program reconciliation commands,
writes worker mail, acknowledges messages, creates notification markers, grants PR capacity,
dispatches work, starts workers, or launches a tech lead.

Draft and pending-approval programs check every 30 minutes and attend only to open decisions,
approval, or unread linked worker outboxes. Active programs check every 15 minutes while mail,
decisions, ready/blocked/orphaned work, missing workers, early child phases, contract drift, or
important child warnings need attention; otherwise they check every 30 minutes. Held programs keep
running, use 15 minutes for mail, decisions, blocked work, or missing workers, and ignore ready work.
Completed, abandoned, archived, or missing programs stop.

#### Live tech lead doorbells

When attention changes, the patrol finds the single live Herdr pane whose title carries the exact
`relay:program:<program>` identity. The tech lead must be `idle` or `done`; `working`, `blocked`,
absent, or duplicated tech leads are skipped. `relay program resume` also refuses to launch a second
tech lead for the
same program and points at the live pane instead.

Relay stages the payload-free prompt `Check Relay program mail and patrol state.` with `agent
prompt`. Herdr normally submits it after its own paste-settling delay. Relay waits for that state
transition first. Only when the exact pane is still idle after the grace period does Relay write a
carriage return through Herdr's terminal-session control protocol:

```text
herdr agent prompt <tl-pane> "Check Relay program mail and patrol state."
herdr terminal session control <tl-pane> --takeover
  {"type":"terminal.input","bytes":"DQ=="}
```

The fallback sends the base64-encoded carriage-return byte directly to the pane's terminal stream.
Relay reads the pane's current dimensions and passes them to the control command, so direct attach
does not resize the live TUI. Only a small output tail is retained so in-band `terminal.closed`
failures are surfaced without persisting terminal frames.
The user's currently focused workspace and pane do not change. Relay confirms a new `working`,
`blocked`, or completed state transition after either submission path.

The patrol arms the attention fingerprint only after confirmed delivery. Definite pre-submission
failures retry up to three times. If text may have been staged but no turn transition can be
confirmed, Relay records an `uncertain` wake, suppresses every further automatic doorbell for that
patrol process, and instructs the operator to inspect and clear the tech lead composer before
restarting
patrol. This prevents repeated prompt text from accumulating. Unchanged successfully delivered
attention is re-armed after two hours.

The attention fingerprint includes sorted unread worker-outbox message ids, not just their count, so
a second question on the same item wakes the live tech lead immediately. The tech lead then
reconstructs durable
state and performs the needed governance work in the existing CEO-facing conversation.

Merge progression needs no separate trigger. Every snapshot overlays authoritative GitHub pull
request state and reconciles it in memory, so a merged child pull request makes its dependent item
ready—and raises `ready-item:<id>` attention—without anyone having run `relay program tick` first.
The patrol stays read-only: it wakes the same live tech lead, which then runs `program tick`,
dispatches the ready item, and starts or adopts its worker.

Manage it with:

```bash
relay program patrol start auth-platform
relay program patrol status auth-platform --json
relay program patrol tick auth-platform --json
relay program patrol stop auth-platform
```

`start` adopts an existing lock holder or creates an unfocused plain Herdr tab labeled
`relay-patrol:<program>` in the program repository; it does not launch a coding agent. Both `start`
and `run` require Herdr and fail closed with setup instructions when it is missing. The interactive
tech lead checks patrol status on entry and then stays available to the CEO. A patrol doorbell tells
that
same session to reload durable state and process current mail and program observations.

`patrol status` reports the runtime lock as the authority for `running`, and still surfaces `error`,
`stop_reason`, and `warning` from the last recorded state when the patrol failed or stopped, so a
dead patrol explains itself instead of reading as a plain `not-running`. Its text output prints the
last and next tick and the last tech lead wake in the host's local zone with the offset spelled out;
`--json` returns the stored UTC record unchanged, so anything comparing timestamps reads the JSON.

Patrol tech lead discovery requires the agent launch adapter to carry the
`relay:program:<program>` session name. Claude and Copilot support named sessions; Relay rejects
`patrol start` and `patrol run` for agents without that capability instead of claiming the tech lead
is
monitored.

### Dependency queue

An item is ready only when:

- The program is active.
- No program-wide decision is open.
- The item is pending.
- No item-scoped decision is open.
- Every dependency has merged.
- Every pinned contract version is approved.

Rejected contracts remain unready. Publish a corrected immutable version, approve it, and update the
item's pinned contract reference to supersede the rejected version.

`relay program queue` and `relay program tick` calculate this state deterministically.

### Pull-request capacity

The cap applies only to open pull requests recorded by active child projects linked to the current
program. Standalone Relay projects, child projects linked to other programs, and unmanaged pull
requests do not count.

Branches without pull requests do not consume capacity. This permits workers to prepare code while
three pull requests await review.

Capacity is:

```text
available = max_open_prs - open - reserved
```

and floors at zero. `open` counts actual open PRs for linked active child projects in this program.
`reserved` counts outstanding grants on dispatched items that do not yet have a recorded PR. Once the
linked PR appears, reconciliation changes the item to `in-review` and clears the grant, so the PR is
open rather than double-counted as reserved.

The tech lead serializes worker requests with:

```bash
relay program grant-open-pr <program> <item> --by tl
relay program revoke-open-pr <program> <item> --by tl --reason "<reason>"
```

`grant-open-pr` verifies contracts and project state, checks decisions and derived capacity, then
saves the grant in `program.json`. The optimistic revision check and a kernel-held save lock make
that save the canonical reservation: concurrent grant commands cannot both overwrite the same
capacity snapshot. The save lock is an advisory `flock` held only while Relay reads the current
revision, writes a temporary file, and renames it, so a crashed writer's lock is released by the
kernel and never needs manual removal.
Only after the save succeeds does Relay reply to the oldest unread `pr-open` request, or send an
unsolicited inbox instruction when no request exists. A successful reply acknowledges the request.
The Herdr notification is last and best-effort. Busy or missing workers are reported as successful
pending notes because the inbox is durable; an actual prompt or marker failure is reported as a
warning.

This save → durable mail → doorbell order makes failures recoverable. If mail fails, the command
reports that the grant already exists and prints a `program message notify` repair command; the tech
lead
may repair the inbox or revoke the unused grant. A missing or failed Herdr prompt never rolls back
the grant or inbox message.

The worker then runs the read-only gate:

```bash
relay program can-open-pr <program> <item>
```

For a dispatched managed item, `can-open-pr` requires the outstanding grant. It does not consume or
modify the grant and passes even when aggregate `available` is zero because that item's reservation
is already counted. An `in-review` item with its existing PR remains idempotently allowed. Workers
keep the grant-approved inbox message unread until `open-pr` succeeds and the PR is recorded.

A pull request that GitHub reports as closed without merging stops consuming capacity and stops
blocking the item: reconciliation clears the recorded reference, and `grant-open-pr` also clears a
known-closed reference so the item can open a replacement pull request.

### Foreground reconciliation

```bash
relay program tick <program>
```

A tick:

1. Validates program state and contract hashes.
2. Reads child Relay manifests and workflow PR references.
3. Uses local git ancestry to recognize merged branches.
4. Overlays authoritative GitHub lifecycle state for every recorded pull request, for active and
   archived linked children alike. A merged pull request marks the item merged even when the branch
   was squashed, rebased, or pruned and the manifest never recorded a local merge. A closed pull
   request never counts as merged.
5. Treats archived children as merged when their manifest records a verified merge or GitHub reports
   the recorded pull request as merged; discarded or missing children are surfaced as orphaned.
6. Reconciles dispatched items to `in-review` or `merged`.
7. Recovers from a pull request that was closed without merging: the stale reference is cleared, an
   `in-review` item returns to `dispatched`, and the item records one note. The item can then receive
   a fresh `grant-open-pr` and open a replacement pull request.
8. Prints the next governance action, including a blocking command for orphaned work.
9. Writes only when reconciliation changed something.

Running an unchanged tick is idempotent: it does not rewrite `program.json` or grow `progress.md`,
and closed-pull-request recovery notes the item once rather than on every tick. If a linked child is
missing or was archived without a verified merge, tick reports its item ID as orphaned and prints an
`item block` command instead of repeatedly suggesting reconciliation.

`relay archive` applies the same authoritative check: a child whose branch has no local ancestry but
whose recorded pull request is merged on GitHub archives cleanly, records `merged`, and removes its
branch without `--force`. A closed or unknown pull request keeps the existing unmerged-branch
protection.

Relay resolves pull request state by querying only the pull requests a program actually records,
with bounded parallelism and a short in-process cache, so repositories with thousands of historical
pull requests stay correct and cheap. When `gh` is missing, unauthenticated, or failing, the overlay
is skipped and the recorded pull request conservatively continues to consume capacity.

#### Unreadable linked children

An unreadable linked child never fails the whole program. `program status`, `program queue`,
`program tick`, `can-open-pr`, and `grant-open-pr` return the item as unavailable plus a structured
warning, and the read-only UI reports the same warning under project source health. An unavailable
child is never reported as merged or orphaned and never fabricates free capacity: its recorded pull
request still counts as open. Item repair commands (`item link`, `item block`, `item cancel`) keep
working so the tech lead can fix the child.

### Local program UI

Each program has a live, read-only mission-control view:

```bash
relay program ui auth-platform
```

Relay binds an embedded web application to `127.0.0.1` on an available port, opens the browser, and
serves until interrupted with Ctrl-C. Use `--no-open` when another process will open the URL, or
`--port <number>` to request a fixed local port.

The UI refreshes automatically and visualizes:

- The approved goal and background from program artifacts.
- Overall completion and pull-request capacity.
- Every work item by status, priority, worker, mailbox, and pull request.
- Dependency links as a circuit-style graph plus an accessible task ledger.
- Open and resolved decisions, contracts, assumptions, warnings, and next action.
- Patrol status, cadence, next check, exact attention reasons, TL presence, and errors.
- Child requirements, plans, progress, tradeoffs, questions, follow-ups, and other known artifacts.
- Live GitHub PR checks/review state and Herdr worker liveness when those sources are available.

Selecting a task updates the URL hash and opens its artifact detail panel. The page polls Relay every
three seconds, preserves the last good snapshot during temporary failures, and backs off while
reconnecting. GitHub reads are cached and shared, so one snapshot performs at most one `gh` read per
recorded pull request and the UI reports exactly the capacity, status, and readiness the strict CLI
reports.

The server accepts only `GET` and `HEAD`, binds only to loopback, rejects unexpected Host headers,
loads no external assets, and never changes program, project, mailbox, git, Herdr, or GitHub state.
Archived programs are viewable with the same command.

## State ownership

| State | Canonical owner | Writer |
| --- | --- | --- |
| Goal narrative and guardrails | `goal.md` | CEO and TL |
| Program lifecycle, items, dependencies, decisions | `program.json` | `relay program` commands |
| Architecture contracts | `contracts/<name>/vN.md` | `contract publish`; immutable afterward |
| Human decision history | `decisions.md` | `relay program` commands |
| Program audit history | `progress.md` | `relay program` commands |
| Child worktree, branch, workflow, PR | Child Relay project | Existing project and `relay state` commands |
| Worker handoffs | Child `mail/` directories | TL and worker message commands |
| CEO change requests against an existing pull request | Child inbox message or a follow-up work item | `relay program worker request-change`; keyed by the normalized request hash |
| Worker process, tab, pane, and focus | Herdr runtime | Herdr; non-authoritative and re-derived |
| Patrol process, cadence, reasons, and attention fingerprint | `~/.relay/run/<slug>/patrol.json` | `relay program patrol run`; read-only toward program/project state |
| Managed worker start exclusion | `~/.relay/run/workers/<child-slug>/start.lock` | `relay program worker start`; kernel-released advisory lock |
| Pull request lifecycle | GitHub | GitHub; observed read-only through `gh` |
| Code and merge facts | git and GitHub | Existing git/GitHub workflow |

## Installation

After building this version of Relay, refresh the binary and generated skills:

```bash
make install
relay setup copilot
```

Substitute `claude` or `codex` when that is your configured agent.

## Conversational workflow

Create a program and enter its tech lead session:

```bash
relay program new \
  "Build reliable organization-wide authentication" \
  --name auth-platform
```

The tech lead reconstructs program state on every entry, works with you on the goal and architecture,
and
uses the commands below to maintain durable state. Re-enter later with:

```bash
relay program resume auth-platform
```

Get non-chat visibility at any time:

```bash
relay program status auth-platform
relay program queue auth-platform
relay program tick auth-platform
```

## Manual CLI walkthrough

The same lifecycle can be driven explicitly.

### 1. Create without launching the tech lead

```bash
relay program new \
  "Build reliable organization-wide authentication" \
  --name auth-platform \
  --no-launch
```

Edit the generated file:

```text
~/.relay/programs/active/auth-platform/goal.md
```

Replace each `_TBD_` section with the proposed outcome, priorities, architecture, and guardrails.

### 2. Publish and approve an architecture contract

Write a contract:

```bash
cat > /tmp/auth-contract.md <<'CONTRACT'
# Authentication contract

## Outcome

All services use the same token validation contract.

## Binding interfaces

- Preserve the current public login endpoint.
- Introduce one shared token verifier.

## Constraints

- No database migration in this slice.
- Existing tokens remain valid.

## Acceptance criteria

- Existing login behavior remains compatible.
- Invalid and expired tokens are covered by tests.
CONTRACT
```

Publish it:

```bash
relay program contract publish auth-platform architecture \
  --file /tmp/auth-contract.md
```

Publishing creates `architecture@v1` and opens a CEO decision. After reviewing it:

```bash
relay program contract approve auth-platform architecture@v1 --by ceo
```

If it is not acceptable, reject it through the contract command rather than generic decision
resolution:

```bash
relay program contract reject auth-platform architecture@v1 \
  --by ceo \
  --reason "Missing rollback and compatibility constraints"
```

Then publish the corrected version and move affected items to it:

```bash
relay program contract publish auth-platform architecture --file /path/to/revised-contract.md
relay program contract approve auth-platform architecture@v2 --by ceo
relay program item update auth-platform w1 \
  --remove-contract architecture@v1 \
  --add-contract architecture@v2
```

### 3. Add dependency-aware work

```bash
relay program item add auth-platform \
  "Introduce the shared token verifier" \
  --priority P0 \
  --contract architecture@v1

relay program item add auth-platform \
  "Adopt the verifier in the API service" \
  --priority P1 \
  --depends-on w1 \
  --contract architecture@v1
```

Adjust work when needed:

```bash
relay program item update auth-platform w2 --priority P0
relay program item update auth-platform w2 --note "Coordinate with API owners"
```

### 4. Submit and approve the program

```bash
relay program submit auth-platform
relay program approve auth-platform --by ceo
```

### 5. Inspect and dispatch ready work

```bash
relay program queue auth-platform
relay program dispatch auth-platform w1
```

Dispatch creates a normal child Relay project. Start its visible owner:

```bash
relay program worker start auth-platform w1
```

Inspect or enter the worker later:

```bash
relay program worker list auth-platform
relay program worker focus auth-platform w1
```

With `--json`, worker list returns `{entries, warnings}`. An unavailable linked child contributes a
structured `{item, project, error}` warning without hiding healthy workers. Pending items that are
linked but not dispatched are omitted.

Dispatch prints the `relay program worker start` command for the new child. The legacy
`relay program dispatch auth-platform w1 --launch` foreground path remains available, but the tech
lead
workflow uses Herdr tabs so the CEO-facing session stays responsive.

The child receives:

- `assignment.md`
- Copies of its pinned immutable contracts
- Program and work-item references in its manifest
- The normal `deliver-pr` workflow

### 6. Handle a worker issue

The managed assignment gives workers the exact command. For example:

```bash
relay program message send auth-platform w1 \
  --kind conflict \
  --body "The existing token type cannot represent the approved expiry semantics. Change the contract or add an adapter?"
```

The tech lead lists it, opens or uses the corresponding program decision, and surfaces that decision
to
the CEO:

```bash
relay program message list auth-platform --json
relay program decision open auth-platform \
  --item w1 \
  --kind conflict \
  --raised-by worker \
  --question "The existing token type cannot represent the approved expiry semantics. Change the contract or add an adapter?"
```

The aggregate JSON result is `{messages, warnings}`. Missing, orphaned, or archived child projects
produce per-item `{item, project, error}` warnings while messages from healthy children remain
available. Text mode prints the same warnings and continues.

After the CEO decides, the tech lead resolves the program decision and replies through the mailbox:

```bash
relay program decision resolve auth-platform d2 \
  --by ceo \
  --answer "Add an adapter and preserve the public token type."
relay program message reply auth-platform w1 <outbox-id> \
  --kind decision \
  --body "Add an adapter and preserve the public token type." \
  --decision d2
relay program worker notify auth-platform w1
```

### 7. Open a pull request

The managed worker checks its unread outbox. If no `pr-open` request is pending, it sends exactly one
and stops:

```bash
relay program message outbox auth-platform w1 --json
relay program message send auth-platform w1 \
  --kind pr-open --body "Ready to open the managed pull request; please grant capacity."
```

The tech lead grants capacity when available without escalating a routine request to the CEO:

```bash
relay program grant-open-pr auth-platform w1 --by tl
```

After reading the grant-approved inbox instruction, the worker leaves it unread and runs:

```bash
relay program can-open-pr auth-platform w1
```

Only after that succeeds does the worker run `open-pr`. It acknowledges the grant inbox message only
after the PR is open and recorded. If `open-pr` fails, the message stays unread for safe retry.

### 8. Reconcile progress

After a pull request opens or merges:

```bash
git fetch origin
relay program tick auth-platform
relay program queue auth-platform
```

When every item is merged or cancelled:

```bash
relay program finish auth-platform
```

The program moves to `~/.relay/programs/archived/`.

### 9. Route a late change and retire finished runtime

If the CEO asks for a change to a pull request that already exists, route it rather than messaging
the worker:

```bash
relay program worker request-change auth-platform w1 --body "Rename the token field to access_token"
```

Read the reported `route`. `same-worker` means the existing worker got the request. `follow-up-pending`
means the pull request is approved or queued and the follow-up waits for the merge — run the same
command again afterwards. `follow-up-dispatched` means the follow-up has its own project and worker.

Once an item is merged and its runtime is no longer needed:

```bash
relay program worker cleanup auth-platform w1 --json
```

This stops the child watcher, exits the worker, closes its tab, and archives the child project with
`--force`, discarding anything uncommitted left in that worktree. A `worker-busy` status means the
worker is still running: retry later rather than forcing it.

## Existing limitations in V1.1

- No always-running program controller or daemon.
- The program tick performs no GitHub comment, review, CI, approval, or merge polling of its own. It
  reads pull request lifecycle state on demand for recorded pull requests only; per-project pull
  request observation belongs to the `relay pr watch` runtime.
- No managed program or managed child session without Herdr.
- No worker-to-worker direct messaging or agent meetings.
- No Beads or Dolt integration.
- No scheduled or standing QA agent.
- No explicit QA work-item type.
- No multi-repository execution, despite reserving a repository field in the model.

These limitations are deliberate. V1.1 proves the governance, contract, dependency, and delegation
model before adding unattended machinery.

## Deferred roadmap

### V2: deterministic controller

Extend the V1.1 idempotent tick engine into the single source of automated reconciliation, then
optionally wrap it in a background process. The controller will:

- Wake work-item owners without depending on conversational session continuity.
- Add branch/process leases.
- Recover after crashes without creating two branch writers.

Per-project pull request attention already works this way: `relay pr watch --mode managed` observes a
worker's pull request and wakes that exact worker pane. It keeps no local record of what was handled —
every check re-reads the live pull request, so attention clears only when GitHub itself stops showing
it. Exactly one watcher owns each pull request.

### V3: explicit QA missions

Add CEO-invoked, project-scoped QA missions containing:

- Environment setup.
- Approved end-to-end commands.
- Investigation focus.
- Expected invariants.
- Evidence and reproduction requirements.
- Safety constraints.

Confirmed, deduplicated findings become program work items. QA remains explicit until real usage
justifies a schedule.

### V4: Beads evaluation and adoption

Beads is a strong candidate for replacing the native portfolio graph because it already provides
dependencies, ready-work calculation, claims, gates, messages, and provenance.

Adoption is deferred until an explicit spike validates:

- A reviewed and version-pinned `bd` binary.
- Stealth operation without hooks, MCP, `AGENTS.md` modification, or agent setup.
- Reliable backup, migration, and recovery behavior.
- Embedded single-writer behavior under the Relay controller.
- Stable JSON CLI contracts.
- A clear migration for existing `program.json` data.

If adopted:

- Beads owns goals, work items, dependencies, priorities, gates, messages, and provenance.
- Relay continues to own contracts, worktrees, branches, sessions, workflow phases, PR capacity,
  controller cursors, process leases, and GitHub reconciliation.

### Later: multi-repository programs

Permit work items to target different repositories, then add cross-repository dependency and capacity
semantics only after a real program requires them.

## Design invariants

1. The CEO remains in the goal, architecture, escalation, and final-review loops.
2. Every issue found by the tech lead or workers is surfaced to the CEO.
3. A tech lead-worker disagreement pauses affected work and escalates to the CEO.
4. Workers own local clarification and implementation planning.
5. Contracts are immutable, versioned, hashed, and binding.
6. Program machine state is changed only through `relay program`.
7. Child workflow state is changed only through existing child-project commands.
8. One writer owns a branch at a time.
9. No more than three linked child-project pull requests are open per program.
10. Agents never self-approve or directly merge.
11. Human GitHub approval is required before auto-merge.
12. Standalone Relay workflows remain fully supported.
