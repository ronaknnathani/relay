# Relay

Relay is an open-source system of **composable agent skills and workflows** that automate
software-engineering work through small, reusable building blocks. Skills are authored once in an
agent-neutral source and compiled per coding agent, so the same library drives **Claude, Copilot, and
Codex**.

The design principle is leverage through composition: small single-purpose skills compose into
workflows, workflows compose into an orchestrator, and heavy work is delegated to sub-agents so the
driving agent stays context-light. Every change is clarified, planned, implemented, simplified,
reviewed, validated, and monitored to merge.

## The three layers

```
Layer 3 — Orchestrator      stack-ship ........ a goal → a stack of small PRs, each delivered and
                                                monitored to merge
                                |
Layer 2 — Workflows         deliver-pr ........ one change → one open PR (clarify→…→open-pr)
                            pr-monitor ........ one open PR → merged (detect → fix → re-arm → merge)
                                |
Layer 1 — Foundation        explore  clarify  plan  implement  simplify  review  validate
                            commit  rebase  open-pr  pr-fix
```

**Foundation skills** each do one thing. `explore` (read-only codebase understanding), `clarify`
(requirements + acceptance criteria), `plan` (an executable blueprint), `implement` (build it,
green each step), `simplify` (cut complexity, behavior unchanged), `review` (a reviewer-role library
with a confidence gate), `validate` (the repo's quality gates, goal-backward), `commit`, `rebase`,
`open-pr` (commit → PR in your conventions), `pr-fix` (CI, comments, conflicts).

**Workflow skills** compose them. `deliver-pr` is a resume-first router that drives one change through
the foundation pipeline, one phase per sub-agent, tracking position in durable state. `pr-monitor`
watches one open PR to merged.

**The orchestrator**, `stack-ship`, turns a goal into an interface-first stack of small PRs, builds
each with `deliver-pr`, drives the front PR with `pr-monitor`, and cascades stack changes — stopping
when every PR is merged and never merging without human approval.

## The `relay` CLI

A thin Go binary that makes starting and resuming work ergonomic:

```bash
relay "Add retry logic to the HTTP client"     # create a worktree + project, launch the agent on deliver-pr
relay -n my-slug "..."                          # custom slug
relay --workflow stack-ship "<design goal>"     # launch the multi-PR orchestrator instead
relay                                           # list active projects
relay resume <slug>                             # reopen where you left off
```

It also owns the **`relay state`** machine — the deterministic, resumable state that workflow skills
read and update (so they never hand-edit JSON) — and the **`relay generate`** compiler that renders
the agent-neutral skill source into per-agent packages.

## Install

Requires Go 1.25+ and a coding agent (Claude Code, Copilot, or Codex) on your `PATH`.

```bash
git clone https://github.com/ronaknnathani/relay
cd relay
make install
```

`make install` installs the relay binary. Then run `relay setup <agent>` from the relay repository to
link generated skills into that agent's personal skills directory: Claude uses `~/.claude/skills`, and
Copilot uses `~/.copilot/skills`. To refresh only Copilot's skills, run `relay setup copilot`. Skills
relay does not own are never clobbered: a real file/dir with a colliding name is skipped, and a symlink
that does not point into relay's own sources is flagged so you can choose whether to replace it.

First run prompts for a branch prefix, your default agent, and that agent's permission mode (saved to
`~/.relay/config.json`). Permission modes are stored per agent and are requested only the first time
that agent is used. Update them with `relay config permission-mode <agent> <mode>`; update the
default agent with `relay config default-agent <agent>`. Project state lives under
`~/.relay/projects/`; worktrees under `<repo>/.worktrees/`.

## Multi-agent

Skills are authored once under `skills/` with agent-neutral conventions (a single `{{subagent}}`
directive carries model-tier intent; tool names and frontmatter are normalized per agent). `relay
generate` renders the strongest mechanism each agent supports — Claude's `Agent` tool and deterministic
slash invocation, Copilot's prose invocation and `AGENTS.md` context, and so on — rather than a
lowest-common-denominator. Golden tests byte-pin every agent's rendered package.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for the skill anatomy, the agent-neutral authoring rules, and the
generate/test workflow.

## License

MIT — see [LICENSE](LICENSE). Some skills adapt content from open-source upstreams; see
[NOTICE](NOTICE) for attributions.
