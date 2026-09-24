---
name: plan
description: Resolve a selected architecture, dependency, or risk-design question into a compact executable blueprint. Use only when routing selects a distinct planning decision; change size alone does not justify a standalone plan. Its output feeds `implement`.
---

# Plan

Resolve the specific planning question selected by the route and hand `implement` a compact,
executable blueprint. A large mechanical diff is not enough: size alone does not justify this phase.
Planning is for an architecture decision, dependency-sensitive sequencing, stack decomposition, or
a public-contract, migration, security, concurrency, dependency, build, or release design.

Requirements must already be pinned by the selected task artifacts, whether or not `clarify` ran.
Stop on a product or author decision instead of disguising it as architecture. This phase does not
write production code or invoke upstream/downstream phases.

## Process

1. **Reuse fresh evidence.** Read the selected task/requirements artifacts and fresh `exploration.md`.
   Do not repeat broad discovery. If it is stale, request one replacement exploration; otherwise
   investigate only a missing fact needed for the selected decision, and cite decisive evidence as
   `path:line`.
2. **Commit one decision.** State the chosen architecture and why it fits existing patterns. Record a
   rejected alternative only when its omission would surprise a reader. Never hand implementation a
   menu of options.
3. **Choose Testing Decisions.** Name the highest useful test seam, the behavior it proves, and any
   lower-level testing deliberately omitted.
4. **Write the Implementation Map.** List every create/modify/test action with an exact path and
   responsibility. Group files that must change together.
5. **Write the Build Sequence.** Order independently verifiable slices by dependency order. Each
   slice needs its outcome, files, verification command, and dependency on earlier slices—not a
   repeated template for facts already stated elsewhere.
6. **Bound the work.** Add **Out of Scope**, concrete failure/error behavior, and any prerequisite
   that must be true before mutation. Map every acceptance criterion to a slice, then remove
   placeholders and duplicated prose.

## Output

Keep `plan.md` concise and use these sections:

1. **Decision** — the selected architecture and rationale.
2. **Testing Decisions** — test seam and assertions.
3. **Implementation Map** — exact paths and responsibilities.
4. **Build Sequence** — dependency order and verification.
5. **Out of Scope / Blockers** — explicit boundaries and prerequisites.

## Red flags

- Planning because a settled mechanical change is merely large.
- Repeating exploration, requirements, or the same file list in multiple sections.
- Hedging between approaches instead of choosing one.
- Abstract steps such as "handle errors" or "add tests" without exact behavior and assertions.
- A changed file absent from the Implementation Map.
- A sequence item that has no verification or depends on an undefined earlier result.
- Quietly deciding unresolved product intent or author policy.

## Verification checklist

- [ ] The route selected a distinct planning decision; size alone did not trigger this artifact.
- [ ] Fresh exploration was reused and decisive claims cite real code.
- [ ] Exactly one architecture approach and test seam are chosen.
- [ ] Every changed file is in the Implementation Map.
- [ ] The Build Sequence is dependency-ordered and independently verifiable.
- [ ] Every acceptance criterion is covered without duplicated boilerplate or placeholders.
- [ ] Out-of-scope work, blockers, and prerequisites are explicit.
