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

![Relay skill layers](docs/skill-layers.png)

Editable source: [docs/skill-layers.html](docs/skill-layers.html).

**Foundation skills** each do one thing. `explore` (read-only codebase understanding), `clarify`
(requirements + acceptance criteria), `plan` (an executable blueprint), `implement` (build it,
green each step), `simplify` (cut complexity, behavior unchanged), `review` (a reviewer-role library
with a confidence gate), `validate` (the repo's quality gates, goal-backward), `commit`, `rebase`,
`open-pr` (commit → PR in your conventions), `pr-fix` (CI, comments, conflicts).

**Workflow skills** compose them. `deliver-pr` is a resume-first router that drives one scoped change
through the foundation pipeline, one phase per sub-agent, to an open PR. `pr-monitor` watches one open
PR to merged, delegating real failures, review comments, and conflicts to `pr-fix`.

**Orchestration** is the third layer. The `stack-ship` skill turns a goal into an interface-first tree
of small PRs, builds each with `deliver-pr`, monitors the front PR with `pr-monitor`, and advances the
stack in order — stopping when every PR is merged and never merging without human approval.

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

Requires Go 1.25+ and at least one supported coding-agent CLI on your `PATH`.

```bash
git clone https://github.com/ronaknnathani/relay
cd relay
make install
```

`make install` installs the `relay` binary. Then run `relay setup <agent>` from the relay repository to
generate that agent's package and link Relay-managed skills into its personal skills directory.

| Agent | Prerequisite | Setup command | Skill install location |
| --- | --- | --- | --- |
| Claude Code | `claude` on your `PATH` | `relay setup claude` | `~/.claude/skills` |
| Codex CLI | `codex` on your `PATH` | `relay setup codex` | `~/.codex/skills` |
| GitHub Copilot CLI | `copilot` on your `PATH` | `relay setup copilot` | `~/.copilot/skills` |

Rerun the same setup command whenever you want to refresh one agent's generated skills. To remove
Relay-managed links for an agent, run `relay setup <agent> --uninstall`. Skills relay does not own are
never clobbered: a real file/dir with a colliding name is skipped, and a symlink that does not point
into relay's own sources is flagged so you can choose whether to replace it.

First run prompts for a branch prefix, your default agent, and that agent's permission mode (saved to
`~/.relay/config.json`). Permission modes are stored per agent and are requested only the first time
that agent is used. Update them with `relay config permission-mode <agent> <mode>`; update the
default agent with `relay config default-agent <agent>`. Project state lives under
`~/.relay/projects/`; worktrees under `<repo>/.worktrees/`.

## Multi-agent

Skills are authored once under `skills/` with agent-neutral conventions (a single `{{subagent}}`
directive carries model-tier intent; tool names and frontmatter are normalized per agent). `relay
generate` renders the strongest mechanism each agent supports — Claude's `Agent` tool and deterministic
slash invocation, Copilot's prose invocation and `AGENTS.md` context, and Codex's native skills under
`~/.codex/skills` — rather than a lowest-common-denominator. Generator tests compare each rendered
package to a source-derived expectation instead of duplicating the whole skill tree as fixtures.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for the skill anatomy, the agent-neutral authoring rules, and the
generate/test workflow.

## License

MIT — see [LICENSE](LICENSE). Some skills adapt content from open-source upstreams; see
[NOTICE](NOTICE) for attributions.
