---
name: code-review
description: Code review a pull request — scores issues 0-100, filters at 80, and posts a summary plus inline comments via gh
---

# Code Review

Review a pull request with specialized review roles, score every issue 0-100, keep only high-confidence issues, and post a summary plus inline comments on the PR.

## Flags

- `--comment` — post the review as PR comments (summary + inline). Without it, report findings to the caller only.

## 1. Gather PR Context

Use the `gh` CLI to fetch what you need. Determine the repo and PR:

```bash
REPO=$(gh repo view --json nameWithOwner --jq .nameWithOwner)
PR_NUMBER=$(gh pr view --json number --jq .number)
HEAD_SHA=$(gh pr view --json headRefOid --jq .headRefOid)
```

Fetch the diff, changed files, and existing review comments (used later for dedup):

```bash
gh pr diff "$PR_NUMBER"
gh pr view "$PR_NUMBER" --json files,title,body
gh api "repos/$REPO/pulls/$PR_NUMBER/comments" --jq '.[] | {path: .path, line: .line, body: .body}'
```

## 2. Should-Review Gate

Skip the review and post nothing if any of these hold:

- The PR is closed.
- The change is trivially correct (automated dependency bump, single config value, no logic).
- The current `HEAD_SHA` has already been reviewed with no new commits since.

If skipping, report the skip reason to the caller and stop.

## 3. Run Review Roles (in parallel where possible)

Run these roles against the diff. With subagents available, dispatch each as its own subagent; otherwise run them sequentially.

- **2x guideline-compliance reviewers**: check the change against the repo's guideline files and author's global guidelines (CLAUDE.md / AGENTS.md or equivalent). Run two independent passes for consensus on guideline checks.
- **1x bug detector**: focus on obvious bugs in the changed lines only — syntax errors, type errors, missing imports, clear logic errors.
- **1x skeptic / edge-case hunter**: assume the change is wrong and actively hunt for bugs and edge cases where the implemented logic would fail — boundary values, empty/nil inputs, error and failure paths, off-by-one, concurrency, and unexpected input ordering.
- **1x history analyzer**: use `git blame` and history for context — security issues and incorrect logic in the introduced code.

```bash
# History context for a changed file:
git blame -L <start>,<end> -- <file>
git log --oneline -10 -- <file>
```

### Optional dual-model pass

If a second review model (e.g. Codex or Copilot) is available in the environment, run it in parallel over the same diff and merge its findings. Label each finding by source — `(claude review)`, `(codex review)`, or `(claude and codex review)` when both agreed. If no second model is available, skip this and proceed with the single-model review. This path is optional and feature-detected — never required.

## 4. Score Every Issue 0-100

Score each issue independently. Scoring considers evidence strength and verification against the actual code.

| Score | Meaning | Example |
|---|---|---|
| **100** | Definitely real issue | Missing import causes ImportError |
| **75** | Real and important | Logic error in edge case |
| **50** | Real but minor | Suboptimal naming |
| **25** | Might be real | Potential performance issue |
| **0** | False positive | Pre-existing issue |

**Filter out any issue with a score less than 80.** The 80 threshold prevents noise while preserving signal.

## 5. Deduplicate and Validate

- Remove duplicate issues found by multiple roles; combine overlapping ones.
- Filter out issues already reported in the existing inline comments fetched in step 1, plus resolved issues and cosmetic nits.
- Cross-validate any externally-sourced findings against the actual code (line numbers can be hallucinated).
- **Coverage check**: confirm every changed file was evaluated by at least one role. Re-review any uncovered files before posting a clean result.

## 6. Description Quality Gate

Before posting, validate each issue's description:
- At least 20 characters and a complete sentence explaining the issue to a developer.
- Not a placeholder (`test`, `todo`, `placeholder`, `fixme`, `tbd`).

Drop or rewrite any description that fails this gate. Never post placeholder text verbatim.

## 7. Post Results (with `--comment`)

Post a summary comment unconditionally (even with zero issues), then post inline comments for each surviving issue.

Clean result:

```bash
gh pr comment "$PR_NUMBER" --body "👍 LGTM"
```

Issues found:

```bash
gh pr comment "$PR_NUMBER" --body "## Code Review Summary
Concise summary of the review.

**Files reviewed:** X
**Issues found:** Y

[severity] brief summary of issue.
"
```

Post inline comments on the specific lines:

```bash
gh api "repos/$REPO/pulls/$PR_NUMBER/comments" \
  -f body="<issue description + suggested fix>" \
  -f commit_id="$HEAD_SHA" \
  -f path="<file>" \
  -F line=<line>
```

Line numbers must be within the PR diff range, or the GitHub API returns 422.

Without `--comment`, report the same summary and issue list directly to the caller instead of posting.

## Non-Negotiable Rules

1. HEAD already reviewed with no new commits → skip entirely.
2. Summary comment is mandatory for first-time reviews, even with zero issues.
3. Keep the summary to 3-5 sentences.
4. Only post issues scoring ≥ 80.
5. Line numbers must be within the PR diff range.
6. Do NOT block a PR by demanding mandatory changes — surface issues, let the author decide.
