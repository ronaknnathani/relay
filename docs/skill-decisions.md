# Relay skill decisions

These decisions summarize aggregate observations across locally available Relay sessions,
transcripts, project state, direct author feedback, and pull-request review activity. Detailed
source accounting remains in local project notes; this repository keeps only non-linkable findings
and decision rationale.

A phase is material only when it changes the intended diff or commit, discovers a new Critical or
Important issue, exposes a failed acceptance criterion or required repository gate, resolves author
or reviewer feedback, records a necessary author decision, or performs the requested Git or
pull-request mutation. Restated context, unchanged clean reports, and unchanged passing checks are
no-ops.

Adaptive-delivery migration applies to newly created adaptive stack and program children. Existing
route-less or otherwise legacy workers retain their legacy behavior when resumed; these decisions do
not retroactively migrate them.

## Sequence decisions

| Sequence | Aggregate observation | Decision |
| --- | --- | --- |
| clarify + plan | Often repeated discovery and already-settled context; both were material when product intent or architecture was unresolved. | Select either phase only from current uncertainty. Reuse one exploration record while its snapshot and inputs remain fresh. |
| implement + validate | Validation exposed real failures, but a separate worker frequently repeated gates already owned by implementation on small changes. | Easy work keeps review and final gates under the implementation owner; standard and high-risk work retain independent validation. |
| simplify + review | Simplification was useful when complexity evidence existed and otherwise commonly added an unchanged pass. | Select simplification only when requested or triggered; always retain proportional review coverage. |
| delivery review + open-pr review | A second review at PR opening did not add a distinct safety boundary when fresh evidence already matched the route and snapshot. | `open-pr` consumes fresh review evidence and never starts a duplicate review. |
| pr-monitor + pr-fix | Observation and mutation are distinct safety responsibilities, while duplicate assessment created avoidable handoffs. | Keep a read-only monitor and a mutation-owning fixer with one authoritative worklist and result contract. |

## Skill decisions

Each record uses the same fields so additions and retirements remain reviewable.

### `build-write-like-me`
- Observed aggregate use: Specialized direct use outside delivery.
- Distinct contribution: Builds a reusable personal voice profile.
- Overlap: None with delivery orchestration.
- Feedback relevance: Prevents generic agent-authored prose.
- Safety role: Filters agent-authored source material from a voice corpus.
- Decision: Retained.
- Migration impact: None.

### `clarify`
- Observed aggregate use: Recurring delivery phase with both material decisions and repeated settled context.
- Distinct contribution: Records necessary author decisions and measurable acceptance criteria.
- Overlap: Shares repository discovery with `explore` and design context with `plan`.
- Feedback relevance: Addresses unclear intent and constraints.
- Safety role: Prevents guessed product decisions.
- Decision: Conditionally routed.
- Migration impact: Skipped with a reason for explicit easy tasks; retained when uncertainty exists.

### `commit`
- Observed aggregate use: Standalone use and shared use from delivery workflows.
- Distinct contribution: Creates scoped repository-style commits.
- Overlap: Git rules were repeated by PR and stack workflows.
- Feedback relevance: Supports narrow, reviewable changes.
- Safety role: Owns default-branch, staging, secret, and authorship checks.
- Decision: Retained and authoritative.
- Migration impact: Standalone invocation is unchanged; other skills reference this contract.

### `deliver-pr`
- Observed aggregate use: Default Relay project workflow and primary orchestration path.
- Distinct contribution: Coordinates one task to an open pull request.
- Overlap: Previously repeated phase, validation, review, and Git rules.
- Feedback relevance: Primary target for reducing deterministic latency without weakening review.
- Safety role: Owns durable routing, escalation, evidence, and phase progression.
- Decision: Simplified.
- Migration impact: Easy projects run `implement -> open-pr`; route-less projects resume the legacy sequence.

### `explore`
- Observed aggregate use: Shared discovery work across clarification, planning, implementation, and review.
- Distinct contribution: Produces grounded code-flow and relevant-file evidence.
- Overlap: Broad discovery was repeated by adjacent phases.
- Feedback relevance: Supports explicit rationale and codebase grounding.
- Safety role: Prevents assumption-driven changes.
- Decision: Retained and reusable.
- Migration impact: One snapshot-bound exploration result is reused until relevant inputs change.

### `implement`
- Observed aggregate use: Core delivery phase and ordinary mutation owner.
- Distinct contribution: Changes code and tests and creates green commits.
- Overlap: Separate easy review and validation workers repeated context and handoffs.
- Feedback relevance: Owns correctness, tests, scope, and clarity for easy work.
- Safety role: Keeps tracer-bullet testing plus exact-snapshot review and final gates.
- Decision: Simplified.
- Migration impact: Easy work has one owner and one review pass; material findings escalate to independent phases.

### `open-pr`
- Observed aggregate use: Terminal delivery phase.
- Distinct contribution: Stages, commits, pushes, and opens the requested pull request.
- Overlap: Previously duplicated review, commit, and rebase procedures.
- Feedback relevance: Preserves concise pull-request conventions.
- Safety role: Requires fresh passing evidence for the current route, snapshot, gate policy, roles, and dispatches.
- Decision: Simplified.
- Migration impact: No second local review; successful PR recording is guarded.

### `plan`
- Observed aggregate use: Material for broader design and dependency work, but planning commonly
  matched implementation time on short mechanical changes.
- Distinct contribution: Commits architecture and an executable task sequence.
- Overlap: Repeated exploration, trivial restatement, and detailed task boilerplate that
  implementation could derive directly for settled mechanical work.
- Feedback relevance: Captures rationale, alternatives, and dependencies.
- Safety role: Prevents silent design invention.
- Decision: Conditionally routed.
- Migration impact: Skipped for easy work and for size-only standard work. Selected plans remain for
  unresolved architecture, dependency-sensitive sequencing, planning-relevant risk, stacks, or an
  explicitly forced full workflow. Standard work without a plan records a lightweight implementation
  map under the implementation owner.

### `pr-fix`
- Observed aggregate use: Review, CI, and conflict remediation.
- Distinct contribution: Mutates branches and GitHub state to resolve actionable PR items.
- Overlap: Monitor logic previously repeated assessment and reply procedures.
- Feedback relevance: Applies reviewer corrections without masking failures.
- Safety role: Remains the ordinary mutation owner.
- Decision: Simplified and authoritative.
- Migration impact: Delegated handling consumes one worklist and returns one result contract.

### `pr-monitor`
- Observed aggregate use: Watcher attention handling and manual PR checks.
- Distinct contribution: Triages one observed digest and verifies remote resolution.
- Overlap: Previously repeated `pr-fix` assessment.
- Feedback relevance: Keeps review and CI attention responsive.
- Safety role: Preserves a strict read-only observer boundary.
- Decision: Simplified and conditionally routed.
- Migration impact: One digest, one worklist, and one re-observation.

### `rebase`
- Observed aggregate use: Standalone use and shared conflict-resolution use.
- Distinct contribution: Updates the branch base and resolves conflicts.
- Overlap: Rebase rules were repeated across PR and stack skills.
- Feedback relevance: Preserves intended diffs during branch updates.
- Safety role: Owns resolve-forward and force-with-lease rules.
- Decision: Retained and authoritative.
- Migration impact: Standalone invocation is unchanged; other skills reference this contract.

### `review`
- Observed aggregate use: Recurring delivery phase and source of material correctness findings.
- Distinct contribution: Finds Critical and Important defects plus criteria, scope, and clarity gaps.
- Overlap: Unconditional role fan-out and open-PR review duplicated work.
- Feedback relevance: Correctness, clarity, tests, and minimal scope are mandatory categories.
- Safety role: Applies general coverage and every specialist lens selected from the actual change surface.
- Decision: Simplified.
- Migration impact: Independent non-easy review remains proportional; easy review is performed once by implementation.

### `route`
- Observed aggregate use: New capability justified by repeated orchestration overlap.
- Distinct contribution: Deterministic current-fact classification, phase selection, escalation, and evidence ownership.
- Overlap: Replaces routing prose formerly embedded in `deliver-pr`.
- Feedback relevance: Reduces easy-path latency while preserving explicit rationale.
- Safety role: Closed risk triggers, snapshot-bound assessment, and monotonic escalation.
- Decision: Added as the only routing capability.
- Migration impact: New adaptive projects persist route state; route-less projects remain compatible.

### `simplify`
- Observed aggregate use: Material when complexity or duplication existed and often a no-op otherwise.
- Distinct contribution: Removes observable complexity without changing behavior.
- Overlap: An unconditional pass duplicated review and validation work.
- Feedback relevance: Supports narrow, minimal diffs.
- Safety role: Mutations remain behavior-preserving and trigger-gated.
- Decision: Conditionally routed.
- Migration impact: Skipped with a reason unless requested or justified by current evidence.

### `stack-ship`
- Observed aggregate use: Standalone stacked delivery.
- Distinct contribution: Dependency-aware front advancement and cascade.
- Overlap: Previously duplicated fixed delivery and Git contracts.
- Feedback relevance: Preserves small, reviewable pull-request stacks.
- Safety role: Keeps watcher ownership, human approval, and front-only merge boundaries.
- Decision: Simplified.
- Migration impact: New adaptive stack children use `deliver-pr`; existing route-less or legacy
  workers retain legacy behavior.

### `tl`
- Observed aggregate use: Managed multi-pull-request programs.
- Distinct contribution: Owns program decisions, contracts, capacity, patrol, and worker lifecycle.
- Overlap: Previously duplicated generic delivery and delegation prose.
- Feedback relevance: Surfaces program decisions and preserves parent ownership.
- Safety role: Keeps managed-worker boundaries, grants, approval, and cleanup.
- Decision: Simplified.
- Migration impact: New adaptive program children classify first and execute only route-selected
  delivery phases; existing route-less or legacy workers retain legacy behavior.

### `cto`
- Observed aggregate use: Legacy program-orchestration entry point.
- Distinct contribution: None beyond the retained `tl` contract.
- Overlap: Fully duplicated tech-lead program ownership and dispatch.
- Feedback relevance: A single program owner avoids conflicting orchestration instructions.
- Safety role: Retirement preserves `tl` decision, approval, and worker-boundary safeguards.
- Decision: Retired and merged into `tl`.
- Migration impact: Replace `/cto` with `/tl`; setup removes only exact Relay-managed links.

### `validate`
- Observed aggregate use: Recurring phase that exposed real gate and acceptance failures.
- Distinct contribution: Produces a ship-readiness verdict from repository-defined checks.
- Overlap: Separate easy validation repeated implementation-owned final gates.
- Feedback relevance: Missing or stale validation is a recurring correctness concern.
- Safety role: Binds required gates to the exact route, snapshot, and dispatch.
- Decision: Simplified.
- Migration impact: Independent for non-easy work; easy work reuses only fresh implementation evidence.

### `write-like-me`
- Observed aggregate use: Specialized direct use outside delivery.
- Distinct contribution: Writes first-person prose in the author's voice.
- Overlap: None with delivery orchestration.
- Feedback relevance: Prevents robotic outward-facing text.
- Safety role: Voice matching does not itself authorize publication; callers that post externally
  remain responsible for Relay's visible automated-authorship disclosure requirement.
- Decision: Retained.
- Migration impact: Excluded from the delivery instruction budget.

### `relay-status`
- Observed aggregate use: Deterministic status lookup.
- Distinct contribution: None beyond the existing CLI.
- Overlap: Fully duplicates `relay status`.
- Feedback relevance: No reasoning contribution was observed.
- Safety role: The replacement command remains read-only.
- Decision: Retired.
- Migration impact: Replace `/relay-status <args>` with `relay status <args>`.

### `relay-archive`
- Observed aggregate use: Deterministic project archival.
- Distinct contribution: None beyond the existing CLI.
- Overlap: Fully duplicates `relay archive`.
- Feedback relevance: No reasoning contribution was observed.
- Safety role: The CLI retains archive preconditions and ownership checks.
- Decision: Retired.
- Migration impact: Replace `/relay-archive <args>` with `relay archive <args>`.

### `todo`
- Observed aggregate use: Deterministic repository todo operations.
- Distinct contribution: None beyond the existing CLI.
- Overlap: Fully duplicates `relay todo`.
- Feedback relevance: No reasoning contribution was observed.
- Safety role: The CLI retains scoped persistence behavior.
- Decision: Retired.
- Migration impact: Replace `/todo <args>` with `relay todo <args>`.
