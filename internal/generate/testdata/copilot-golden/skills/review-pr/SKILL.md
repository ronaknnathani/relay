---
name: review-pr
description: Comprehensive PR review using specialized review roles, each focusing on a different aspect of code quality
---

# Comprehensive PR Review

Run a comprehensive review of the current changes using multiple specialized review roles, each focusing on a different aspect of code quality.

This skill bundles six review roles as prompt files under `agents/`. Each role's full prompt lives in `agents/<role>.md`:

- **code-reviewer** (`agents/code-reviewer.md`) — general code quality and repo-guideline compliance
- **pr-test-analyzer** (`agents/pr-test-analyzer.md`) — test coverage quality and completeness
- **comment-analyzer** (`agents/comment-analyzer.md`) — comment accuracy and maintainability
- **silent-failure-hunter** (`agents/silent-failure-hunter.md`) — silent failures and error handling
- **type-design-analyzer** (`agents/type-design-analyzer.md`) — type design and invariants
- **code-simplifier** (`agents/code-simplifier.md`) — simplification while preserving behavior

## Review Workflow

1. **Determine review scope**
   - Run `git diff --name-only` (and `git status`) to identify changed files.
   - Check whether a PR already exists: `gh pr view` (if `gh` is available and a PR exists, scope to the PR diff).
   - If the caller named specific aspects, run only those roles; otherwise run all applicable roles.

2. **Determine applicable roles** based on the changes:
   - **Always**: code-reviewer (general quality)
   - **If test files changed**: pr-test-analyzer
   - **If comments/docs added or changed**: comment-analyzer
   - **If error handling changed**: silent-failure-hunter
   - **If types added/modified**: type-design-analyzer
   - **After the review passes**: code-simplifier (polish and refine)

3. **Run the review roles**

   Each role is defined by its prompt file under `agents/`. Run each applicable role with its prompt, scoped to the changed files.

   - **With subagents available** (preferred): dispatch each role as its own subagent so it gets a fresh context, then collect the reports. Roles are independent and may run in parallel.
   - **Without subagents**: run each role sequentially inline, applying its prompt to the diff yourself, one role at a time.

4. **Aggregate results** into a single summary:

   ```markdown
   # PR Review Summary

   ## Critical Issues (X found)
   - [role-name]: Issue description [file:line]

   ## Important Issues (X found)
   - [role-name]: Issue description [file:line]

   ## Suggestions (X found)
   - [role-name]: Suggestion [file:line]

   ## Strengths
   - What's well-done in this change

   ## Recommended Action
   1. Fix critical issues first
   2. Address important issues
   3. Consider suggestions
   4. Re-run the review after fixes
   ```

## Tips

- **Run early**: before creating the PR, not after.
- **Focus on changes**: roles analyze the diff by default.
- **Address critical first**: fix high-priority issues before lower-priority ones.
- **Re-run after fixes**: verify issues are resolved.
- Results are actionable with specific `file:line` references.
