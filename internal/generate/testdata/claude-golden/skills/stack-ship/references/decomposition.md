# Phase 0 — Intake & Decomposition

Goal: turn whatever the author handed you into two artifacts the rest of the run depends on —
`goal.md` (what "done" means) and `plan.md` (the stack). Get these right and the run is mostly
mechanical; get them wrong and you'll steer forever.

## Inputs you might receive
- **A full design doc** — extract the goal and the acceptance criteria from it.
- **A goal + a delta-finder** — e.g. "implement stable pod identity; the design is at X, the current
  code is at Y, find the gap." Here you (or a single Explore subagent) discover the current→desired
  delta first.
Either way, **never start building until `goal.md` and `plan.md` exist.**

## `goal.md` — the definition of done
Write the goal in one paragraph, then a **checkable acceptance-criteria list**. Criteria must be
verifiable by a subagent at the end (a test exists, a field is plumbed end-to-end, a behavior holds).
This list is your `/goal` and your Phase-3 verification gate. Example shape:

```
# Goal
<one paragraph: what the feature does and why, the current→desired delta.>

## Acceptance criteria (done = all checked)
- [ ] api: <the new types/contract/surface defined, defaulted, plumbed end-to-end, not yet consumed>
- [ ] utils: <the logic/planner implemented + unit-tested in isolation, error paths covered>
- [ ] stitch: <integration wires the logic into the running system; behavior observable in tests>
- [ ] all PRs merged; no open author decisions in the pending-decisions table
```

A goal of "feature implemented" with no machine-checkable done-ness gives nothing to *prove*
convergence against. Acceptance criteria make the stop condition objective.

## The decomposition pattern: interface-first (`api → utils → stitch`)
Decompose into the **smallest single-intent PRs**, ordered so each PR is reviewable on its own and
the dependency only ever points backward. The general shape (self-contained — adapt the names to
your stack):

1. **API / interface surface** — the types, schema, public function signatures, config/flags, or
   data contract. Plumbed end-to-end but **intentionally not yet consumed**. This is the smallest,
   fastest-to-review PR and it unblocks everything downstream.
2. **Validation** — only if a constraint can't be expressed declaratively in the schema/types and
   needs its own gate (a validator/webhook/guard). Omit when the type system already enforces it.
3. **Utils / logic** — the pure helpers, planners, transforms that operate on the surface from (1).
   Unit-testable in isolation, no integration yet.
4. **Stitch / integration** — wire the logic into the running system (the controller/handler/route
   that actually calls it). This is where behavior becomes observable.
5. **Aggregation / telemetry** — status rollups, metrics, dashboards, cross-cutting reporting.

Each PR names which step it is in its description — reviewers approve a pinned-scope "API surface"
PR far faster than a sprawling one. Skip steps that don't apply; never merge two steps to "save a
PR" unless they're genuinely one intent.

Each PR entry in `plan.md`:

```
## PR <n>: <type(scope): imperative title>
- layer: api | webhook | utils | controller | stitch | telemetry
- intent: one sentence (one PR = one intent; renames are their own PR)
- scope: files/areas; what it deliberately does NOT touch
- depends-on: [PR ids]  (empty = independent → parallelizable)
- branch: <prefix>/<slug>   base: <parent branch or master>
- acceptance: which goal.md criteria this PR satisfies
- review-shape: prose summary (why/what, not a file list); Testing Done = commands only
```

### Parallel vs pipelined — be honest about the dependency chain
"Stacked PRs can run in parallel" is **only partly true**. `api → utils → stitch` is a dependency
chain: utils won't compile until the API surface exists; stitch needs both. So:
- **Independent PRs** (no `depends-on`) → fan out, one subagent each, isolated worktrees.
- **Dependent PRs** → pipeline: a child can *start* (branch off the parent, scaffold tests) while the
  parent is in review, but it cannot **finish/compile** until the parent's surface is real, and it
  must rebase whenever the parent changes. Don't promise parallelism the dependency graph forbids.

Encode the dependency graph so the orchestrator pipelines dependent PRs correctly instead of fanning
out blindly on a chain it can't actually parallelize.

## Author sign-off
If the design left genuine ambiguity (multiple reasonable decompositions, an undecided API shape),
present the plan via `AskUserQuestion` **before** building. If it's unambiguous, proceed with the
smallest-PRs default and record the decomposition call in `tradeoffs.md`. Do **not** block on
confirmation for things with an obvious right answer — the goal is near-zero steering, which means
asking only the questions that actually change the plan.

## Coding/PR preferences to bake into every PR (author's house style)
- One intent per PR; no dead code "for the next PR"; no adjacent refactors.
- PR summary = 1–2 prose paragraphs on *why/what*, never a per-file table; `Testing Done` = the
  commands run, no preamble; PR bodies disclose agent authorship.
- Match surrounding style; surgical changes only; every changed line traces to the goal/comment.
- Tests: behavior-named, red-before-green, cover the cross-revision / unreferenced / paused cases.
- Regenerate any generated build/codegen files after adding/removing files; run the **scoped** test
  target for the package you touched, not the whole monorepo.
