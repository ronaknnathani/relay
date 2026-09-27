# Stack guardrails

- Use `deliver-pr` for per-PR routing and evidence; do not duplicate phase procedures.
- Use `commit` for branch, staging, secret, message, and authorship rules.
- Use `rebase` for base detection, resolve-forward conflicts, intended-diff verification, evidence
  staleness, and force-with-lease.
- Use `pr-monitor` for one read-only digest cycle and `pr-fix` as the sole mutation/reply owner.
- Never speak as the author. Automated GitHub text identifies the agent and whose behalf it acts on;
  replies use the exact marker contract owned by `pr-fix`.
- Never guess an author decision. Record and surface it; keep the affected branch paused.
- One writer per branch. Parallelism is only across independent worktrees.
- Auto-merge only the front PR, based on the default branch, after genuine human code-owner approval.
  Never self-approve, dismiss a review, or merge immediately.
- Inspect before destructive action. Never overwrite or delete unexamined human work.
- Cascade every parent content change through descendants and verify their base refs and intended
  diffs afterward.
- Keep stack state durable and resumable. Stop at the accepted goal; new scope becomes follow-up work.
- Use only approved tooling already present in the environment.
