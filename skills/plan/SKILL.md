---
name: plan
description: Turn a clarified requirements artifact into a concrete, executable implementation plan — a decisive architecture blueprint and a phased build sequence with exact file paths. Use after requirements are pinned down (e.g. by `clarify`) and before writing code, whenever a multi-step change needs a design and a task breakdown. Its output feeds `implement`.
---

# Plan

Given a clarified requirements artifact, produce an implementation plan concrete enough that someone
with no prior context could execute it. The bar: exact file paths and names, one committed
architecture decision, and a phased build sequence `implement` consumes step by step — no abstract
advice, no "add appropriate error handling". This is a peer phase: it grounds itself in the code via
`explore`, picks ONE approach and commits, then hands a plan to `implement`. It does **not** clarify
requirements (that's `clarify`, upstream) and does **not** write production code (that's `implement`,
downstream). It does not invoke either.

## Process

1. **Ground in the codebase first.** Read the clarified artifact (`clarify`'s requirements + success
   criteria), then call `explore` (dispatch a sub-agent when available; otherwise inline) and consume
   its **essential-files list and `file:line` citations** as the starting set for the existing
   patterns, conventions, and integration points this work touches. Cite each finding as
   `path/to/file.ext:line` — the blueprint
   is built on what's actually there, not what you assume.
2. **Decide the architecture and commit.** Survey the realistic approaches, then pick **ONE** and
   write the decision plus a one-paragraph rationale grounded in the patterns from step 1. Do not
   hedge with "Option A or B" — a plan is a decision. Record approaches you rejected in one line each
   only if a reader would otherwise wonder why; don't relitigate.
3. **Choose the test seams up front.** Pick where behavior gets verified, preferring a single highest
   seam (one integration/end-to-end point) over many low ones. Decide what's tested and what isn't,
   and capture it in an explicit **Testing Decisions** section. New behavior gets a test; a bug fix
   gets a test that fails without the fix.
4. **Design the components.** For each unit of work name the file(s), its single responsibility, and
   its dependencies on other units. Split by responsibility, not by technical layer; files that change
   together live together; follow the established structure rather than restructuring it.
5. **Write the Implementation Map** — every concrete create/modify action with its exact path:
   `Create src/x.ext` / `Modify src/y.ext:120-150` / `Test tests/x_test.ext`. No file is named only
   in prose; if it changes, it's in the map.
6. **Sequence the build into phases.** Order the work so each phase produces something verifiable,
   following dependency order (data/contracts → logic → wiring/config). Emit it as the **Build
   Sequence** checklist below — this is the executable artifact `implement` works through.
7. **Pin the critical details.** State the concrete error-handling behavior, state/data shape, and
   verification command for each phase. Add an **Out of Scope** section listing what this plan
   deliberately does not touch, so `implement` doesn't wander.
8. **Self-review with fresh eyes.** Walk every Success criterion in the clarified artifact and point to
   the task that satisfies it — list and fix any gap. Scan for placeholders (TBD, TODO, "handle edge
   cases", a test step with no assertion) and remove them. Confirm names/signatures used in a later
   task match what an earlier task defines. Fix inline; don't re-review.

## Task template

Each unit of work in the Build Sequence follows this shape:

```markdown
### Task N: <imperative title, no "and">
- **Description:** what this builds and why it's one unit.
- **Acceptance criteria:** 3–5 testable assertions (each checkable by a test, command, or observation).
- **Verification:** the exact command(s) to run and the expected result.
- **Dependencies:** task numbers this needs first (or "none").
- **Files:** Create/Modify/Test with exact paths (mirrors the Implementation Map).
- **Size:** XS · S · M · L · XL.
- [ ] step(s) — one concrete action each (write failing test, implement, run, commit)
```

**Break a task down if** it spans more than ~2 hours, needs 4+ acceptance criteria, touches
independent subsystems, or has "and" in the title — each of those is two tasks wearing one hat.

## Red flags

- Hedging between approaches instead of committing to one decision with a rationale.
- A file named only in prose, or a path written as a placeholder rather than the real one.
- "Add appropriate error handling / validation / edge cases" — say the concrete behavior instead.
- A test step with no assertion, or a "see Task N" cross-reference instead of the actual content.
- A task with "and" in its title, 4+ acceptance criteria, or that touches independent subsystems.
- Missing Testing Decisions or Out of Scope sections, or many low test seams where one high one fits.
- A Success criterion from the clarified artifact with no task that satisfies it.
- Clarifying requirements, writing production code, or invoking `clarify`/`implement` — all out of scope.

## Verification checklist

- [ ] Every blueprint finding cites `file:line` from a real `explore` of the codebase.
- [ ] Exactly one architecture approach is chosen, with a grounded rationale — no hedging.
- [ ] Test seams are chosen up front and captured in a **Testing Decisions** section.
- [ ] The Implementation Map lists every create/modify/test action with an exact path.
- [ ] The Build Sequence is a phased checklist in dependency order, each phase independently verifiable.
- [ ] Every task fits the template and the "break it down if" thresholds; none has "and" in the title.
- [ ] Each Success criterion in the clarified artifact maps to a task; no placeholders remain.
- [ ] An **Out of Scope** section bounds what the plan deliberately leaves untouched.
- [ ] The skill stayed in scope: no requirement clarification, no implementation, no downstream invocation.
