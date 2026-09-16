---
name: open-pr
description: Commit, push, and open a pull request after fresh review and validation evidence passes, using the authoritative commit/rebase contracts and repository PR conventions.
---

# Open PR

Open the finished change; performs no new review and reruns no passing gate.

## Process

1. Read repository/global `AGENTS.md` and the PR template.
2. Detect the current branch and the repository's default branch. In standalone use from the
   dynamically detected default branch, create and switch to a feature branch before invoking
   `commit`: read the configured prefix with `relay config branch-prefix`, derive a concise task slug,
   validate the full name with `git check-ref-format --branch`, verify it does not already exist
   locally or at `origin`, then run `git switch -c <prefix><slug>`. This preserves the current working
   changes without committing them to the default branch. Refuse a detached HEAD or a colliding branch
   name rather than switching to unrelated existing work.
3. Require a clean, fully committed feature branch. If intended changes remain, apply the standalone
   `commit` contract for default-branch rejection, specific staging, secret inspection, message style,
   commit separation, and automated co-authorship. In an adaptive project, return to the selected
   review/validation owners because `HEAD` changed. In standalone use with no Relay state, continue in
   this invocation: review the committed diff once, run the repository-required gates, then proceed
   only if both pass.
4. For an adaptive project, require the coordinator to refresh the route after the final commit and
   before dispatching `open-pr`, then require evidence from the route's canonical owners for the
   exact current snapshot and exact required gate set:

   ```bash
   relay state evidence fresh "$SLUG" review
   relay state evidence fresh "$SLUG" validation
   ```

   Refuse stale, missing, failed, blocked, partial, extra, duplicate, or gate-digest-mismatched
   evidence. A route refresh that escalates also blocks this phase. For a legacy seven-phase project
   with no route, require its recorded review and validate
   phases to be complete and do not call adaptive route/evidence commands.
5. If the branch must be updated, apply the standalone `rebase` contract. Because rebasing changes
   the snapshot, stop and obtain fresh adaptive review/validation evidence before continuing.
6. Push the current feature branch. Ordinary first push uses `-u`; rewritten history uses only the
   `rebase` contract's force-with-lease rule.
7. Create the PR with the repository's base branch and template. The title follows local history.
   The body is one or two short prose paragraphs explaining why and what, followed by `Testing Done`
   containing only commands that actually ran. Clearly disclose automated authorship and whose behalf
   the agent acts on.
8. For a Relay project, while the current-route `open-pr` dispatch remains active, record the
   returned PR number and URL with
   `relay state pr "$SLUG" --number <n> --url <url> --dispatch-token "$RESULT_TOKEN"`.
   The guarded command atomically completes the
   phase, writes the production `FinalResult` telemetry, verifies the GitHub repository, URL, branch,
   head SHA, base ref, and immutable remote base SHA, and rejects stale evidence or a
   **superseded dispatch**. If PR creation succeeded but recording is ambiguous, do not create another PR: run
   `relay state pr "$SLUG" --reconcile --dispatch-token "$RESULT_TOKEN"` to find and verify the unique
   open PR for the recorded head/base or recover the persisted PR identity if it has already merged.
   If the PR's creation-time base SHA differs, Relay returns labeled project, base, and SHA fields
   instead of executable command text. The coordinator must pass those values as separate arguments
   to `relay route base`, refresh the route without replacing that pending PR pin, rerun stale
   evidence, redispatch `open-pr`, and reconcile the existing PR. Never execute or paste an error
   string as a shell command.
   If that pending PR closed without merging, verify the closed state and record terminal failure;
   never discard a still-open pending PR. On terminal failure before a PR exists, record
   `relay state final "$SLUG" failed --reason "<specific reason>" --dispatch-token "$RESULT_TOKEN"`.
   Then return the PR URL or failure.

`--draft` is supported when the caller requests a draft. Never merge, enable auto-merge, invent test
results, post a second review, or open a duplicate PR.
