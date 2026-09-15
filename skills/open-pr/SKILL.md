---
name: open-pr
description: Commit, push, and open a pull request after fresh review and validation evidence passes, using the authoritative commit/rebase contracts and repository PR conventions.
---

# Open PR

Open the finished change; performs no new review and reruns no passing gate.

## Process

1. Read repository/global `AGENTS.md` and the PR template.
2. Require a clean, fully committed feature branch. If intended changes remain, apply the standalone
   `commit` contract for default-branch rejection, specific staging, secret inspection, message style,
   commit separation, and automated co-authorship. In an adaptive project, return to the selected
   review/validation owners because `HEAD` changed. In standalone use with no Relay state, continue in
   this invocation: review the committed diff once, run the repository-required gates, then proceed
   only if both pass.
3. For an adaptive project, require evidence from the route's canonical owners for the exact current
   snapshot and exact required gate set, and run the hard gates only after the final commit:

   ```bash
   relay route refresh "$SLUG"
   relay state evidence fresh "$SLUG" review
   relay state evidence fresh "$SLUG" validation
   ```

   Refuse stale, missing, failed, blocked, partial, extra, duplicate, or gate-digest-mismatched
   evidence. A route refresh that escalates also blocks this phase. For a legacy seven-phase project
   with no route, require its recorded review and validate
   phases to be complete and do not call adaptive route/evidence commands.
4. If the branch must be updated, apply the standalone `rebase` contract. Because rebasing changes
   the snapshot, stop and obtain fresh adaptive review/validation evidence before continuing.
5. Push the current feature branch. Ordinary first push uses `-u`; rewritten history uses only the
   `rebase` contract's force-with-lease rule.
6. Create the PR with the repository's base branch and template. The title follows local history.
   The body is one or two short prose paragraphs explaining why and what, followed by `Testing Done`
   containing only commands that actually ran. Clearly disclose automated authorship and whose behalf
   the agent acts on.
7. For a Relay project, while the current-route `open-pr` dispatch remains active, record the
   returned PR number and URL with `relay state pr`. The guarded command atomically completes the
   phase, writes the production `FinalResult` telemetry, and rejects stale evidence, wrong route or gate
   bindings, or a superseded dispatch. On terminal failure, record
   `relay state final "$SLUG" failed --reason "<specific reason>"`. Then return the PR URL or failure.

`--draft` is supported when the caller requests a draft. Never merge, enable auto-merge, invent test
results, post a second review, or open a duplicate PR.
