---
name: validate
description: "Batch 2.7: Validate — goal-backward verification, then user checkpoint before ship"
argument-hint: "SLUG"
disable-model-invocation: true
---

# Validate — Batch 2.7: Validate

Runs the validate phase as a subagent with fresh context. On FAIL, loops back to `/improve` (max 2 retries). On PASS, prompts the user with "Ready to ship?" and invokes `/ship` on approval.

## Setup

Read `$ARGUMENTS` as the project slug. Load project context:

```bash
SLUG="$ARGUMENTS"
PROJ="$HOME/.relay/active/$SLUG"
```

Read `$PROJ/manifest.json`. Confirm phase is `validate`. If the phase is past validate, tell the user to run `/ship $SLUG` instead.

Announce: "Validating against plan goals."

## Context Management Rules

- **Orchestrator stays lean (~10-15% context).** Pass file paths only to subagents — never paste file content into prompts.
- **Each subagent uses `model: "opus"`** for maximum context (1M).
- Subagents read plan.md from disk themselves.

## Phase: Validate

Launch a subagent (Agent tool, model: "opus") with this prompt:

> Read the plan at `$PROJ/plan.md`.
> Apply goal-backward verification — for each requirement, ask: "What must be TRUE about the codebase?"
> 1. Run the full test suite — capture output
> 2. Run linter/build commands — capture output
> 3. Verify each requirement from the plan is satisfied
> 4. Write a verification report to `$PROJ/verification.md` with:
>    - Each requirement: PASS or FAIL with evidence
>    - Test results
>    - Overall verdict: PASS or FAIL

Wait for completion. Read `$PROJ/verification.md`.

## Failure handling

Track validation attempts via `$PROJ/validate-attempts.txt`. Read the file (default 0 if missing).

```bash
ATTEMPTS_FILE="$PROJ/validate-attempts.txt"
ATTEMPTS=$(cat "$ATTEMPTS_FILE" 2>/dev/null || echo 0)
```

If the verification verdict shows ANY requirement FAILS:

- Increment the counter and write it back:
  ```bash
  ATTEMPTS=$((ATTEMPTS + 1))
  echo "$ATTEMPTS" > "$ATTEMPTS_FILE"
  ```
- If `ATTEMPTS < 3`: append the failing requirements from `$PROJ/verification.md` to `$PROJ/review-summary.md` under a `## Validation Failures` heading (replace the section if it already exists from a previous retry), so the Fix subagent in `/improve` will see them. Then reset the phase to `fix` and invoke `/improve`. The improve command will auto-chain back to `/validate` for re-verification.
  ```bash
  relay update "$SLUG" --phase fix --remove phases_completed=fix --add phases_remaining=fix
  ```
  Then invoke `/improve $SLUG`.
- If `ATTEMPTS >= 3`: stop. Display the verification failures from `$PROJ/verification.md` and ask the user how to proceed (use AskUserQuestion).

## Manifest update on PASS

If the verification verdict is PASS, reset the retry counter and update the manifest:

```bash
rm -f "$ATTEMPTS_FILE"
relay update "$SLUG" --phase rebase --add phases_completed=validate --remove phases_remaining=validate
```

## Checkpoint

After validation passes:

1. Read `$PROJ/verification.md` and display a summary to the user
2. Run `git diff --stat main...HEAD` and display the output
3. Use AskUserQuestion: "Implementation complete and validated. Ready to ship?"

## On approval

When the user approves, invoke `/ship $SLUG`.
