---
name: review
description: Review a change for correctness, quality, and guideline compliance using a library of specialized reviewer roles. Use before opening a PR, after finishing a chunk of work, or to review an existing PR. Two output modes — a local severity-ranked report for a fix loop (default), or inline GitHub comments (--comment). Scores every finding for confidence, filters the noise, and delegates each role adversarially.
---

# Review

Review a change with a library of specialized reviewer roles, score every finding for confidence,
drop the noise, and emit results in one of two modes. The goal is signal: high-confidence findings a
senior engineer would actually raise, each with a concrete fix — never a wall of nitpicks.

## Modes

- **report** (default) — produce a local, severity-ranked findings report for the author or a fix
  loop (e.g. `pr-fix`). Posts nothing.
- **inline** (`--comment`) — post a summary comment plus inline comments on the PR via `gh`.

## The reviewer-role library

Each role is a prompt file under `agents/`, focused on one failure mode. Run a role as its own
sub-agent when sub-agents are available (fresh context, parallelizable); otherwise apply its prompt
inline, one role at a time.

**Every role reports under one contract:** score each finding 0-100 for confidence and surface only
those ≥ 80; tag each with a severity (Critical / Important / Suggestion) as a separate axis from
confidence; stay advisory — surface findings with a concrete fix, never block. This contract overrides
any different scale or posture a bundled role's own prompt describes.

- **code-reviewer** (`agents/code-reviewer.md`) — guideline compliance, correctness, edge cases.
- **silent-failure-hunter** (`agents/silent-failure-hunter.md`) — swallowed errors, log-and-continue,
  fallbacks that hide failure.
- **type-design-analyzer** (`agents/type-design-analyzer.md`) — encapsulation and invariants; make
  illegal states unrepresentable.
- **pr-test-analyzer** (`agents/pr-test-analyzer.md`) — behavioral and error-path coverage; tests that
  catch real bugs, not implementation-coupled ones.
- **comment-analyzer** (`agents/comment-analyzer.md`) — comment accuracy, "why" over "what", rot.
- **security** (`agents/security.md`) — STRIDE pass over trust boundaries; Always/Ask-First/Never;
  injection and untrusted-input handling.
- **git-history** (`agents/git-history.md`) — `git blame`/log context: does the change contradict why
  the surrounding code exists?
- **prior-pr-history** (`agents/prior-pr-history.md`) — was this approach already tried, discussed, or
  reverted in earlier PRs?

## 1. Scope and the should-review gate

Load guidance before judging standards. Read the repo's `AGENTS.md` when present, then read global
guidance from `~/AGENTS.md` or `~/.config/agents/AGENTS.md` when present. Repo guidance takes precedence
over global guidance. Include the applicable guidance in each reviewer role's prompt so sub-agents review
against the same rules instead of relying on memory.

Pin a fixed point and confirm there is something to review:

```bash
BASE=$(git merge-base HEAD origin/HEAD 2>/dev/null || git rev-parse HEAD~1)
git diff --name-only "$BASE"...HEAD   # must be non-empty
```

If `gh` is available and a PR exists, scope to the PR diff and gather context for dedup:

```bash
REPO=$(gh repo view --json nameWithOwner --jq .nameWithOwner)
PR=$(gh pr view --json number --jq .number)
HEAD_SHA=$(gh pr view --json headRefOid --jq .headRefOid)
gh pr diff "$PR"                            # the PR-scoped diff to review
gh pr view "$PR" --json title,body,files    # spec text + changed files — feeds the Spec axis (step 3)
gh api "repos/$REPO/pulls/$PR/comments" --jq '.[] | {path, line, body}'   # existing comments (dedup)
```

**Skip and report the skip reason** (post nothing) if: the diff is empty, the PR is closed, the change
is trivially correct (dependency bump, single config value, no logic), or this exact `HEAD_SHA` was
already reviewed with no new commits.

## 2. Pick the applicable roles

Always run **code-reviewer**, **silent-failure-hunter**, **git-history**, and **prior-pr-history**.
Add roles by what changed: tests → **pr-test-analyzer**; new/changed types → **type-design-analyzer**;
comments/docs → **comment-analyzer**; any trust boundary, input parsing, auth, or untrusted data →
**security**. If the caller named specific aspects, run only those.

## 3. Review along two axes, kept separate

Every change is judged on two independent axes — do not let one contaminate the other, and do not
merge them when reporting:

- **Standards** — does it follow the repo's and author's documented conventions (repo `AGENTS.md`, then
  global `~/AGENTS.md` or `~/.config/agents/AGENTS.md`, plus CLAUDE.md or equivalent guideline files)?
  Omit anything a linter or compiler already enforces; distinguish a hard violation from a judgment
  call.
- **Spec** — does it do what the task/PRD/issue asked? Report three buckets: missing or partial
  requirements, changes nobody asked for, and implementations that look incorrect. Quote the spec.

## 4. Delegate each role adversarially (doubt-driven)

This is the highest-leverage rule. When you hand a finding or a diff to a reviewer sub-agent, give it
**only the artifact and the contract it must satisfy — never your own reasoning or your conclusion
that the code is fine.** A reviewer that sees "this looks correct" anchors to agreement. Prompt it to
**find what is wrong**, not to confirm. If a role and the author disagree, run at most **3** back-and-
forth cycles, then record the open question rather than looping forever.

## 5. Score every finding 0-100, then filter

Score each finding for **confidence that it is real**, verified against the actual code. This axis is
confidence only — *severity* (Critical/Important/Suggestion, i.e. impact) is a separate dimension you
tag independently, so a high-confidence finding can still be a Suggestion.

| Score | Confidence the finding is real |
|---|---|
| 100 | Certain — verified against the code (e.g. missing import → ImportError) |
| 80  | High — strong evidence it is real |
| 50  | Plausible — might be real, not verified |
| 25  | Weak — speculative |
| 0   | False positive / pre-existing / on a line the change did not touch |

**Drop every finding below 80.** Then drop anything on this false-positive exclusion list:

- Pre-existing issues on lines the change did not touch.
- Anything a linter, compiler, or formatter already catches.
- Lines explicitly suppressed (lint-ignore) or intentional per the change's stated goal.
- Anything a senior engineer would not bother to raise.

## 6. Dedup, validate, description gate

- Merge duplicate/overlapping findings from multiple roles into one.
- Drop findings already covered by existing PR comments (from step 1) and resolved threads.
- Cross-validate any externally-sourced finding against the real code — **line numbers can be
  hallucinated.** Confirm every changed file was covered by at least one role.
- Each finding's description must be a complete sentence ≥ 20 chars explaining the issue and naming a
  concrete fix. Never emit a placeholder (`todo`, `fixme`, `tbd`).

## 7. Emit

**report mode** — group by the shared severity vocabulary, with `file:line` and a fix per finding:

```markdown
# Review Summary
## Critical (N)      — must fix before merge
- [role] issue + fix  (file:line)
## Important (N)      — should fix
- [role] issue + fix  (file:line)
## Suggestion (N)     — optional
- [role] note  (file:line)
## Strengths
- what's well done
```

Always emit this summary, even with zero findings (a `# Review Summary` of `👍 LGTM` plus any
strengths) — a clean review still reports.

**inline mode** (`--comment`) — post a mandatory summary comment (3-5 sentences, even when clean:
`👍 LGTM`), then one inline comment per surviving finding. Inline prefixes: `Nit:` / `Optional:` /
`FYI:` for sub-Critical notes. Line numbers must fall inside the PR diff range or the API returns 422:

```bash
gh api "repos/$REPO/pulls/$PR/comments" \
  -f body="<issue + suggested fix>" -f commit_id="$HEAD_SHA" -f path="<file>" -F line=<line>
```

## Severity vocabulary (shared with pr-fix and deliver-pr)

`Critical` (blocks merge) · `Important` (should fix) · `Suggestion` (optional) for reports;
`Nit:` / `Optional:` / `FYI:` prefixes for inline comments.

## Red flags

- Posting a finding without verifying the line against the real diff.
- Reporting style a linter already enforces, or a pre-existing issue on an untouched line.
- Reviewing without loading repo/global AGENTS.md guidance when those files exist, or not passing that
  guidance to reviewer sub-agents.
- Telling a reviewer sub-agent the code is correct before it looks (anchoring it to agreement).
- Demanding mandatory changes that block the PR — surface findings; the author decides.

## Verification checklist

- [ ] Fixed point pinned; diff non-empty; should-review gate evaluated.
- [ ] Repo `AGENTS.md` and global `~/AGENTS.md` or `~/.config/agents/AGENTS.md` guidance were loaded when present and passed to reviewer roles.
- [ ] Every changed file covered by at least one role.
- [ ] Every emitted finding scores ≥ 80, survives the exclusion list, and names a concrete fix.
- [ ] No duplicate of an existing PR comment.
- [ ] A summary is emitted (report or inline) even when the review is clean; in `inline` mode every inline comment's line is within the diff range.
