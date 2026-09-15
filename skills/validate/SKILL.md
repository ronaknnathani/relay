---
name: validate
description: Verify acceptance criteria and repository gates for the exact current snapshot, reusing only fresh evidence, recording normalized results, and returning failures to implementation without editing.
---

# Validate

Produce a pass/fail shipping verdict for the exact repository snapshot. This phase does not edit,
commit, push, review, or ship.

## Process

1. Read the task, acceptance criteria, route decision, repository snapshot, and existing normalized
   validation evidence. Confirm `validate` is the route's validation owner; unforced easy routes normally skip
   this phase because `implement` owns their final gates. If an easy route escalated, require
   `validate` ownership and independently run or reuse the now-required gate evidence.
   For a route-less legacy seven-phase project, `validate` owns the complete repository gate set,
   reconstructs criteria when artifacts are absent, writes the same verdict, and uses the legacy
   `relay state set`/`advance` flow without requiring route or evidence records.
2. Read the exact gate IDs and command digests bound to the route. Confirm the repository's current
   manifest, scripts, Makefile, and CI still define that set; reclassify if it changed. Do not invent a
   generic gate or treat an undefined gate as passing.
3. Compare the required gate set with evidence for the exact repository snapshot. Reuse a passing
   gate only when its ID, command digest, route revision, and snapshot are unchanged. Run stale or missing gates;
   never rerun an unchanged passing command merely because this phase started.
4. Check every acceptance criterion with a named test, command, or direct observation. Missing
   evidence is a failure.
5. Record normalized command strings and exit statuses only:

```bash
relay state evidence record "$SLUG" validation \
  --result passed \
  --artifact validation.md \
  --gate <id>="<exact command>" --exit-status 0 \
  --dispatch-token "$DISPATCH_TOKEN"
```

Repeat `--gate` and `--exit-status` in matching order. Relay requires exact set equality with the
route policy and stores only the gate ID, redacted display, command digest, and exit status. Never
store output, environment values, transcript text, or secrets in state.
When the repository defines no gates, record the explicit normalized form
`--result passed --no-gates` only when the route policy is `none`; do not invent a command or treat
missing discovery as no gates.
6. PASS only when every required command and criterion passes. On failure, write exact command,
   relevant error excerpt, and location to `validation.md`, then record `--result failed`. Relay marks
   `validate` escalated and reopens `implement` with that artifact. Return the failure; do not edit or
   advance to `open-pr`.
   If validation cannot run because authentication, tooling, or required input is unavailable,
   record `--result blocked --blocker-category <category> --blocker-reason "<reason>"`. Relay blocks
   the canonical validation owner for coordinator recovery instead of treating the blocker as a
   failed code gate.

## Verdict

```markdown
# Validation: <task> - VERDICT: PASS | FAIL
Snapshot: <fingerprint>
## Gates
- <gate>: PASS | FAIL | REUSED | N/A - `<command>`
## Acceptance criteria
- [x] <criterion> - <evidence>
- [ ] <criterion> - <failure and location>
## Failures
- `<command>` -> <error excerpt> @ <location>
```

N/A means the repository does not define that gate. It never means a required check was skipped.
Independent failing gates may run in parallel so one pass returns the full failure set.

## Red flags

- Editing code or weakening a test, lint rule, or threshold.
- Reusing evidence from a different snapshot or command.
- Rerunning all passing checks without determining which are stale or missing.
- Claiming an acceptance criterion passed without observable evidence.
- Advancing after failed or blocked evidence.
