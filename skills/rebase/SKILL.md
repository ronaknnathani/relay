---
name: rebase
description: Rebase the current feature branch onto its dynamically detected base, resolve conflicts forward, verify the intended diff, mark evidence stale, and push safely with force-with-lease.
argument-hint: "[BASE_BRANCH]"
---

# Rebase

This is the authoritative rebase and force-push contract.

1. Require a clean worktree; never auto-stash. Refuse the default branch.
2. Use the supplied base when present. Otherwise resolve `origin/HEAD`, then repository configuration;
   never assume `main` or `master`. Fetch that exact remote base.
3. Record the pre-rebase intended diff and branch commits, then run
   `git rebase origin/<base>`.
4. For each conflict, understand both sides and resolve forward: preserve the feature's intended
   behavior while incorporating the new base behavior. Stage only resolved files and continue
   non-interactively. If intent is genuinely ambiguous, abort and surface the decision; never guess
   through destructive conflict resolution.
5. Compare the post-rebase intended diff with the recorded intent. Stop if work disappeared, unrelated
   changes appeared, or conflict markers remain.
6. A rewritten HEAD makes prior review and validation evidence stale. Run `relay route refresh
   "$SLUG" --dispatch-token "$ROUTE_TOKEN"` when operating as an adaptive phase worker, or use the
   coordinator capability when rebasing inline. A compatible route keeps the active phase dispatch
   and original finish token; a changed route contract requires coordinator redispatch. Require new
   evidence before PR delivery.
   For a route-less legacy seven-phase project, skip the unavailable refresh command, retain the
   recorded legacy order, and report that downstream review and validation must rerun after the
   rewritten HEAD.
7. If no upstream exists, push with `git push -u origin <branch>`. If already synchronized, skip.
   Otherwise use `git push --force-with-lease`, never `--force`.

Report base, rewritten commit range, conflicts resolved, intended-diff result, evidence status, and
push result. Do not merge.
