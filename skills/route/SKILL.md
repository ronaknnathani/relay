---
name: route
description: Classify a Relay delivery as easy, standard, high-risk, or stack-candidate from normalized task and repository facts, persist selected/skipped phases, and escalate conservatively when evidence changes.
---

# Route

Classify delivery work; do not implement it. Bind the project slug to `$SLUG`, read `task.md`,
`manifest.json`, and any existing fresh `exploration.md`, then gather only the normalized facts
accepted by `relay route classify`.

## First classification

Obtain the initial snapshot with `relay route snapshot "$SLUG"` before exploration; it does not
require a persisted route. Use at most one broad repository exploration for that snapshot. Reuse task, assignment,
requirements, and repository facts instead of asking routine questions they already answer. Record:
whether requested behavior is explicit; unresolved product/design decisions; the exact required
repository gate set or an explicit verified no-gates result;
whether every closed risk was evaluated; whether predicted file/line counts were explicitly estimated;
the predicted counts; stack decomposition and rationale; requested full/simplify behavior;
duplication/generated-churn/review-cleanup signals; and only these risk triggers:
`public-contract`, `persistence-migration`, `auth-security`, `concurrency-distributed`,
`dependency-build-release`, `generated-artifact`, `destructive-operation`,
`unresolved-review-ci`, `failed-gate`. Also record normalized change surfaces for tests,
documentation/comments, type design, history-sensitive compatibility, and repository guidelines so
the existing specialist lenses are reachable without adding another review phase.

Invoke the deterministic policy; never reproduce or override its matrix in prose:

Write repository-derived gates to a private JSON file with a file-writing tool, never a shell
heredoc or interpolated command string. Each entry has a stable `id`, an `argv` array, and optional
`env` object, for example `[{"id":"test","argv":["go","test","./..."]}]`. Do not use `sh -c`,
`bash -c`, backticks, command substitution, or `eval`; Relay rejects shell command strings.

```bash
relay route classify "$SLUG" \
  --coordinator-token "$COORDINATOR_TOKEN" \
  --requested-behavior-explicit \
  [--gate-file "$GATE_FILE" | --no-repository-gates] \
  --risk-assessment-complete \
  --predicted-size-known \
  --predicted-files <count> \
  --predicted-lines <count> \
  [--unresolved-decision] [--full] [--simplify] \
  [--stack-decomposition --stack-rationale "<reason>"] \
  [--duplication] [--generated-churn] [--review-cleanup] \
  [--changes-tests] [--changes-documentation-comments] [--changes-type-design] \
  [--history-sensitive] [--changes-repository-guidelines] \
  [--risk <closed-trigger> ...]
```

Coordinator-authenticated classification and refresh fetch and pin the current `origin/<base>` SHA
before snapshotting when the manifest base is a known remote branch. A valid local-only branch or
commit SHA remains a local snapshot base and is not incorrectly fetched as an origin branch. It must
later be rebound to a real remote PR base before `open-pr` dispatch. A pending PR is the exception:
preserve its already verified creation-time base pin. `relay route base "$SLUG" --base <branch> --sha <pr-base-sha>
--coordinator-token "$COORDINATOR_TOKEN"` may pin that exact SHA only when Git proves it is a commit
contained by the intended remote base branch; arbitrary refs, `HEAD`, feature branches, and unrelated
commits are rejected.

Use a stable, descriptive ID for each gate. Relay validates the structured argv before use and
stores only each ID, a redacted display, and the SHA-256 digest of the exact argv and environment.
`--no-repository-gates` is valid only after checking the
repository's manifest, scripts, Makefile, and CI and finding no relevant gate. Omit
`--risk-assessment-complete` or `--predicted-size-known` when that work was not done. A completed
risk assessment is bound to the repository fingerprint plus the normalized `task.md`,
`requirements.md`, and `assignment.md` input revision captured by classification; a changed
repository or project input clears it. Missing safety or size facts, an unknown gate policy, or conflicting gate flags produce
the standard route or an error, never easy. Write `route.md` with the returned
class, selected/skipped phases and reasons,
review roles, review owner, validation owner, gate IDs, snapshot and input revisions, any stack recommendation,
and either the reusable findings from `exploration.md` or an explicit relative link to that artifact.
The handoff must name relevant files, symbols, constraints, existing patterns, and repository commands
so `implement` does not repeat discovery. Do not include
transcript text, command output, secrets, or file contents.

## Refresh and escalation

After implementation mutations, repeat the full classification against the new snapshot so the risk
assessment, exact gate policy, and size facts match the actual diff. If that reassessment is unavailable, run:

```bash
relay route refresh "$SLUG" --coordinator-token "$COORDINATOR_TOKEN"
```

This measures the actual diff, invalidates the old risk assessment and snapshot-bound evidence,
conservatively leaves the easy path, reopens independent `review` and `validate` when selected, and
never downgrades automatically. When new uncertainty, meaningful scope
growth, a Critical/Important finding, a failed gate, a risk trigger, or a stack boundary appears,
record it explicitly:

```bash
relay route escalate "$SLUG" <standard|high-risk|stack-candidate> \
  --coordinator-token "$COORDINATOR_TOKEN" \
  --reason "<specific evidence>" [--risk <closed-trigger>] \
  [--stack-rationale "<required when target is stack-candidate>"]
```

`--stack-rationale` is required whenever the target is `stack-candidate`, including when the stack
boundary was discovered after initial classification. Never convert a stack-candidate to
`stack-ship`; report the recommendation and continue with the conservative single-PR route unless
the caller explicitly chose stack ownership.

When the active worker, rather than the coordinator, must classify, refresh, or escalate after its own
mutation, use that dispatch's one-time `route_token` as `--dispatch-token`; never pass the reusable
coordinator capability to a worker. Relay preserves the active dispatch only when the route class,
selected phases, review/validation owners, gate policy, risks, and task inputs remain compatible.
Otherwise the coordinator must redispatch the newly current phase.

## Return

Return only a compact digest: class, forced-full status, selected phases, skipped phases with reasons,
review roles, review owner, validation owner, snapshot file/line counts, escalation reasons, route
artifact path, and any blocking decision. Never return file contents or restate the policy.
