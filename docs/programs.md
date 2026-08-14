# Relay Programs: CTO-managed engineering

## Goal

Relay Programs add a governance layer above ordinary Relay projects. A user acts as CEO and talks to
one CTO agent about a large product goal, priorities, architecture, and decisions. The CTO decomposes
the approved goal into dependency-aware engineering assignments and delegates them to senior worker
agents. Workers retain ownership of repository-level clarification, planning, implementation, review,
validation, and pull-request creation.

The intended end state is a trustworthy local engineering organization:

```text
CEO
  |
  v
CTO program session
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
3. Resolving every issue surfaced by the CTO or workers, including CTO-worker conflicts.
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
- Program writes use an optimistic revision and a short exclusive save lock so concurrent commands
  fail instead of silently overwriting each other.
- Readiness and capacity are derived instead of being copied into mutable status fields.

This keeps standalone workflows unchanged:

```bash
relay "<task>"
relay --workflow stack-ship "<goal>"
relay resume <project>
```

## What V1 implements

V1 is a foreground, file-backed foundation that validates the CTO operating model without adding a
daemon, Dolt server, GitHub polling service, or new runtime dependency.

### Program lifecycle

```text
draft -> pending-approval -> active <-> held -> completed
   \                                         \
    ------------------------------------------> abandoned
```

Programs store:

- Goal and primary repository.
- Maximum open Relay-managed pull requests, defaulting to three.
- Work items with priorities and dependencies.
- Immutable, versioned, SHA-256-verified engineering contracts.
- Open and resolved CEO decisions.
- Links to child Relay projects and their pull requests.

### Senior-engineer delegation

The CTO publishes an engineering contract, not an implementation recipe. It captures binding
architecture, interfaces, constraints, scope, exclusions, acceptance criteria, and guardrails.

The worker still runs:

```text
clarify -> plan -> implement -> simplify -> review -> validate -> open-pr
```

The worker escalates contract conflicts, scope changes, missing dependencies, risks, and other issues
through program decisions. Routine implementation choices remain worker-owned.

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

The cap applies to all open pull requests produced by Relay projects in the program's repository,
including standalone Relay projects and drafts when they have been recorded. Unmanaged pull requests
do not count.

Branches without pull requests do not consume capacity. This permits workers to prepare code while
three pull requests await review.

V1 provides a read-only gate:

```bash
relay program can-open-pr <program> <item>
```

Managed `deliver-pr` workers run it immediately before `open-pr`. V1 does not reserve capacity
atomically across concurrent processes; that becomes the controller's responsibility in a later
version.

### Foreground reconciliation

```bash
relay program tick <program>
```

A tick:

1. Validates program state and contract hashes.
2. Reads child Relay manifests and workflow PR references.
3. Uses local git ancestry to recognize merged branches.
4. Treats archived children as merged only when their manifest records a verified merge; discarded
   or missing children are surfaced as orphaned.
5. Reconciles dispatched items to `in-review` or `merged`.
6. Prints the next governance action, including a blocking command for orphaned work.
7. Writes only when reconciliation changed something.

Running an unchanged tick is idempotent: it does not rewrite `program.json` or grow `progress.md`.
If a linked child is missing or was archived without a verified merge, tick reports its item ID as
orphaned and prints an `item block` command instead of repeatedly suggesting reconciliation.

## State ownership

| State | Canonical owner | Writer |
| --- | --- | --- |
| Goal narrative and guardrails | `goal.md` | CEO and CTO |
| Program lifecycle, items, dependencies, decisions | `program.json` | `relay program` commands |
| Architecture contracts | `contracts/<name>/vN.md` | `contract publish`; immutable afterward |
| Human decision history | `decisions.md` | `relay program` commands |
| Program audit history | `progress.md` | `relay program` commands |
| Child worktree, branch, workflow, PR | Child Relay project | Existing project and `relay state` commands |
| Code and merge facts | git and GitHub | Existing git/GitHub workflow |

## Installation

After building this version of Relay, refresh the binary and generated skills:

```bash
make install
relay setup copilot
```

Substitute `claude` or `codex` when that is your configured agent.

## Conversational workflow

Create a program and enter its CTO session:

```bash
relay program new \
  "Build reliable organization-wide authentication" \
  --name auth-platform
```

The CTO reconstructs program state on every entry, works with you on the goal and architecture, and
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

### 1. Create without launching the CTO

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

Dispatch creates a normal child Relay project and prints its resume command:

```bash
relay resume auth-platform-w1
```

To immediately replace the current process with the worker session:

```bash
relay program dispatch auth-platform w1 --launch
```

The child receives:

- `assignment.md`
- Copies of its pinned immutable contracts
- Program and work-item references in its manifest
- The normal `deliver-pr` workflow

### 6. Handle a worker issue

The managed assignment gives workers the exact command. For example:

```bash
relay program decision open auth-platform \
  --item w1 \
  --kind conflict \
  --raised-by worker \
  --question "The existing token type cannot represent the approved expiry semantics. Change the contract or add an adapter?"
```

View it:

```bash
relay program status auth-platform
```

Resolve it after the CEO decides:

```bash
relay program decision resolve auth-platform d2 \
  --by ceo \
  --answer "Add an adapter and preserve the public token type."
```

### 7. Open a pull request

Immediately before the managed worker runs `open-pr`:

```bash
relay program can-open-pr auth-platform w1
```

If capacity is full, the worker stops with `open-pr` still pending. Resume the child after another
Relay-managed pull request merges.

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

## Existing limitations in V1

- No always-running controller or daemon.
- No automatic GitHub comment, review, CI, approval, or merge polling.
- No automatic managed `pr-monitor` wakeups.
- No atomic reservation around the three-PR gate.
- No Beads or Dolt integration.
- No scheduled or standing QA agent.
- No explicit QA work-item type.
- No multi-repository execution, despite reserving a repository field in the model.
- No web dashboard for programs.

These limitations are deliberate. V1 proves the governance, contract, dependency, and delegation
model before adding unattended machinery.

## Deferred roadmap

### V2: deterministic controller

Extend the V1 idempotent tick engine into the single source of automated reconciliation, then
optionally wrap it in a background process. The controller will:

- Wake work-item owners without depending on conversational session continuity.
- Add branch/process leases.
- Reserve pull-request capacity atomically.
- Detect changed GitHub state and invoke one bounded managed `pr-monitor` tick.
- Acknowledge GitHub watermarks only after successful worker coverage.
- Recover after crashes without creating two branch writers.

Standalone `pr-monitor` keeps its native loop. Managed programs use controller-owned tick scheduling;
exactly one scheduler owns each pull request.

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
2. Every issue found by the CTO or workers is surfaced to the CEO.
3. A CTO-worker disagreement pauses affected work and escalates to the CEO.
4. Workers own local clarification and implementation planning.
5. Contracts are immutable, versioned, hashed, and binding.
6. Program machine state is changed only through `relay program`.
7. Child workflow state is changed only through existing child-project commands.
8. One writer owns a branch at a time.
9. No more than three Relay-managed pull requests are open in one repository.
10. Agents never self-approve or directly merge.
11. Human GitHub approval is required before auto-merge.
12. Standalone Relay workflows remain fully supported.
