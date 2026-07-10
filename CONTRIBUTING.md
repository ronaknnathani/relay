# Contributing to Relay

Relay skills are **agent-neutral** Markdown under `skills/<name>/SKILL.md`. The Go `relay generate`
compiler renders them into per-agent packages, so a skill must read correctly for Claude, Copilot, and
Codex alike.

## Skill anatomy

Author each skill with these sections (scale them to the skill — a small skill can be short):

1. **Frontmatter** — `name` and `description` only. The `description` is one line that says *when to
   use* the skill (this is the triggering surface, so make it specific). Do **not** add `model`,
   `argument-hint`, `disable-model-invocation`, or `color`.
2. **Overview** — one paragraph: what the skill does and the bar for doing it well.
3. **Process** — numbered, concrete steps. Prefer exact commands and lists over prose.
4. **Red flags** — the mistakes that mean "stop, you're doing it wrong."
5. **Verification checklist** — checkbox items that make "done" objectively checkable.

## Agent-neutral rules

- No Claude-only tool names. For delegation, write "dispatch a sub-agent when available; otherwise do
  it inline" — never name a specific runtime's tool. To request a model tier, use the `{{subagent}}`
  directive (`{{subagent:large_context}}` / `{{subagent:fast}}` / `{{subagent}}`); the generator renders
  the best mechanism each agent supports.
- No plugin-namespace references (`superpowers:`, `commit-commands:`, `pr-review-toolkit:`, `relay:`).
  Refer to sibling skills by bare name in backticks, e.g. `review`, `deliver-pr`.
- A skill does **one** job and returns. It does not auto-chain to the next skill — workflows
  (`deliver-pr`, `stack-ship`) own sequencing and track position via `relay state`.
- Keep the shared severity vocabulary where it applies: `Critical` / `Important` / `Suggestion`.

## State, not context

Workflow skills are **resume-first**: they read `relay state next <slug>` to learn where they are and
never assume a fresh start. They mutate state only through the `relay state` CLI (`init`, `next`,
`current`, `set`, `advance`, `pr`, `log`) — never by hand-editing `state.json` — so the schema stays
valid across agents.

## The generate / test loop

The package renderers are tested against source-derived expectations, so the real `skills/` tree stays
the only copy of skill content. After changing any skill:

```bash
go test ./internal/generate/
go build ./... && go vet ./... && go test ./...
```

Inspect the generated package locally when changing renderer behavior. The coupling test fails the
build if a forbidden plugin namespace leaks into the rendered package.

## Pull requests

Keep PRs small and single-intent. Sequence stacked work API → logic → integration. Commit messages use
a conventional-commit prefix and a body that explains *why*; PR summaries are concise prose (the diff
shows the *how*), with a `Testing Done` section that is just the commands you ran. Relay is built to
deliver exactly this kind of PR — `relay "<task>"` will drive one for you.
