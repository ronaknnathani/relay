---
name: validate
description: Verify a change is ready to ship by checking it against its acceptance criteria and running the repo's own quality gates in order, then report a pass/fail verdict with the exact failures. Use to decide go/no-go right before shipping — when you want a pass/fail verdict against acceptance criteria and the repo's quality gates (lint/build/test), not a code critique (use `review` for that). It reports — it does not fix (route failures to `pr-fix`/`implement`) and does not ship.
---

# Validate

Decide whether a change is ready to ship and report the verdict — pass, or fail with the exact
failures someone else can act on. The bar: every acceptance criterion from `clarify`/`plan` is
demonstrably met, and every quality gate the repo defines is green, each shown by a command or
observation rather than asserted. This skill is a gate, not a fixer: on failure it hands precise
error-feedback to `pr-fix` or `implement` and stops. It does **not** edit code and does **not** ship.

## Process

1. **Gather the acceptance criteria.** Read the Success criteria from the `clarify`/`plan` artifacts.
   These are the goal-backward targets — what must be TRUE about the codebase. If no artifact exists,
   reconstruct the criteria from the task description and list them, so the verdict is checkable.
2. **Discover the repo's own gate commands.** Find how *this* repo lints, type-checks, tests, builds,
   and audits — from its scripts/manifest/CI config (`explore` when it isn't obvious). Use the repo's
   actual commands; never substitute a generic command or invent a gate the repo doesn't define. A
   gate the repo doesn't have is skipped as not-applicable (and noted), not faked.
3. **Run every applicable quality gate — none skippable.** The gates, in report order:
   **lint → typecheck → unit tests → build → integration → e2e → security/audit → bundle-size**. This
   sequence is the *reporting/intent order*, not a stop-on-first-failure barrier: run independent gates
   in parallel when the tooling allows (dispatch sub-agents when available; otherwise inline), **never
   stop at the first failure**, and capture every gate's output so the report is complete in one pass.
4. **Check each acceptance criterion, goal-backward.** For each criterion, name the test, command, or
   direct observation that demonstrates it holds — and run/observe it. A criterion with no evidence is
   a FAIL, not a pass-by-assertion; "looks done" is not evidence.
5. **Render the verdict.** PASS only if every applicable gate is green AND every acceptance criterion
   has passing evidence. Otherwise FAIL. Report, for each failure: the **exact command run**, its
   **error output**, and the **location** (file/line/criterion) — this is the error-feedback `pr-fix`
   or `implement` consumes. Do not attempt deep fixes here; a one-line obvious typo is still routed
   out, not silently patched.

## Never mask a failure to go green

Disabling a test, skipping a spec, loosening a lint rule, or lowering a threshold to make a gate pass
is a hard red flag — it converts a real failure into a hidden one. If a gate fails, report it as a
FAIL with its evidence and route it out. The only legitimate "skip" is a gate the repo genuinely does
not define, recorded as not-applicable.

## Output schema (the verdict)

```markdown
# Validation: <task>  — VERDICT: PASS | FAIL
## Gates
  - <gate>: PASS | FAIL | N/A — `<exact command>`  (on FAIL: error + location)
## Acceptance criteria
  - [x] <criterion> — evidence: <test/command/observation>
  - [ ] <criterion> — FAIL: <what's missing + where>
## Failures (for pr-fix / implement)
  - `<exact command>` → <error output> @ <file:line | criterion>
```

A PASS verdict means nothing in the Failures section. List every failure, not just the first — one
pass should give the fixer everything to act on.

## Red flags

- Skipping a gate that applies, or running them out of order, instead of the full lint→…→bundle-size sequence.
- Disabling/loosening a test, spec, lint rule, or threshold to turn a gate green.
- Substituting a generic command for the repo's own gate command, or inventing a gate the repo lacks.
- Marking an acceptance criterion PASS without a test/command/observation behind it.
- Editing code to fix a failure here instead of routing it to `pr-fix`/`implement`.
- Reporting only the first failure when later gates also failed — the fixer needs the whole set.
- Shipping, merging, or invoking the next skill — `validate` returns a verdict and stops.

## Verification checklist

- [ ] Acceptance criteria came from the `clarify`/`plan` artifact (or were reconstructed and listed).
- [ ] Gate commands are the repo's own, discovered from its config — not generic or invented.
- [ ] Every applicable gate ran in order; results captured; none skipped or masked to go green.
- [ ] Each acceptance criterion is backed by a named test/command/observation, not assertion.
- [ ] The verdict is PASS only with all gates green and all criteria evidenced; else FAIL.
- [ ] Each failure reports exact command + error + location for `pr-fix`/`implement` to consume.
- [ ] No code was edited, nothing was shipped, and no next skill was invoked.
