---
name: review
description: Review a change with route-proportional roles, mandatory correctness and acceptance coverage, high-confidence findings, and snapshot-bound evidence. Supports local report mode and optional inline GitHub comments.
---

# Review

Review without editing or running repository gates. Pin the current repository snapshot and evaluate
the diff against task criteria and applicable coding guidance.

This skill is the independent review owner when the persisted route selects `review`. A genuinely
easy route assigns review ownership to `implement` and skips this phase unless risk, failure, scope
growth, stale evidence, or a blocking finding escalates the route.

## Scope

Read repository `AGENTS.md`, then global `~/AGENTS.md` or `~/.config/agents/AGENTS.md`; repository
guidance wins. Review only changed lines and behavior. If a PR exists, pin its head SHA, read its
title/body/files and existing comments for deduplication. Report a no-op when the diff is empty,
closed, or this exact head already has fresh review evidence.

## Proportional roles

Use the persisted route-selected roles; do not invent an unconditional panel.

- **Standard:** one general reviewer covering all mandatory axes, plus only roles named by
  route/change evidence or caller concern.
- **High-risk / stack-candidate / forced full:** general review plus every specialist corresponding
  to active risk triggers.

Role identifiers are the prompt filenames and live under `agents/`: `code-reviewer`, `silent-failure-hunter`,
`type-design-analyzer`, `pr-test-analyzer`, `comment-analyzer`, `security`, `git-history`, and
`prior-pr-history`. The general role is `code-reviewer`; route-selected specialist IDs therefore
resolve directly to real prompt files. Use specialist prompts only when selected. Give each reviewer
the diff, criteria, guidance, and role contract;
never anchor it with your own conclusion. Parallelize independent selected roles.

For a route-less legacy seven-phase project, preserve the historical review contract instead of
requiring adaptive state: run `code-reviewer`, `silent-failure-hunter`, `git-history`, and
`prior-pr-history`, plus change-triggered specialists. Write `review.md`, but use the legacy
`relay state set`/`advance` flow and do not call adaptive evidence commands.

## Mandatory axes

Every independent review covers:

1. **correctness** - behavior, edge cases, and error paths;
2. **acceptance-criteria compliance** - missing, partial, or extra behavior;
3. **scope/minimality** - unrelated changes, duplication, or avoidable breadth;
4. **clarity** - names, comments, docs, and maintainability.

Tests, documentation/comments, type design, security, concurrency, history, generated artifacts, and
repository guidelines receive specialist depth only when selected by current change-surface or risk
facts in the route.

## Findings

Score confidence from 0-100 and emit only findings at least 80. Tag impact separately:
`Critical`, `Important`, or `Suggestion`. Drop pre-existing issues on untouched lines, compiler/linter
findings, intentional behavior, duplicates, and anything without a concrete fix. Verify every
`file:line`; externally supplied line numbers are not trusted.

Critical or Important findings fail review evidence and route back to implementation. Suggestions
remain non-blocking. Always write `review.md`, including a clean result:

```markdown
# Review Summary
Snapshot: <fingerprint>
Roles: <route-selected roles>
## Critical
## Important
## Suggestion
## Strengths
```

Record normalized evidence only:

```bash
relay state evidence record "$SLUG" review \
  --result passed \
  --artifact review.md \
  --role code-reviewer \
  --critical 0 --important 0 --suggestion 0 \
  --dispatch-token "$DISPATCH_TOKEN"
```

Use repeated `--role` flags for selected specialists. Use `--result failed` when Critical or Important
is nonzero; Relay marks review escalated and reopens implementation. Never store finding bodies,
diffs, command output, transcript text, or secrets in state.
If review cannot run because authentication, tooling, or required input is unavailable, record
`--result blocked --blocker-category <category> --blocker-reason "<reason>"`. Relay blocks the
canonical review owner for coordinator recovery; it does not misclassify the blocker as a code
finding or reopen implementation by default.

## Inline mode

With `--comment`, post one automated-authorship summary and one inline comment per surviving finding.
Every summary and inline comment must visibly identify the automated agent and whose behalf it acts
on. Confirm every inline line is in the PR diff and do not duplicate an existing comment. Include
`<!-- relay-agent-reply -->` in the summary and every inline comment so watcher observation cannot
classify agent-authored feedback as human.
Without `--comment`, post nothing.

## Red flags

- Running every historical role regardless of route.
- Skipping a mandatory axis because only one general reviewer was selected.
- Editing code, running build/test gates, or deciding that a finding may be ignored.
- Reporting low-confidence, pre-existing, duplicate, or unverified findings.
- Recording review bodies or source content in state.
