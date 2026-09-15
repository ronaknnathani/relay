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
never assume a fresh start. New adaptive delivery uses `relay route` for deterministic classification
and the state commands `dispatch`, `finish`, and `evidence`; legacy callers may continue using
`set`/`advance`. `done` and `skipped` are terminal, while `skipped`, `blocked`, and `escalated` require
durable reasons. Never hand-edit `state.json`.

Review and validation evidence is keyed to the active phase dispatch token, route revision, and exact
repository snapshot and exact required gate set. Any relevant route or content revision makes older
evidence stale. Easy routes are limited to changes that satisfy the current-fact safety rules;
validation commands persist only a gate ID, redacted display, exact digest, and exit status.
`open-pr` requires fresh passing evidence and performs no duplicate review.

The non-writing `SKILL.md` corpus has a 15,000 words aggregate budget. Named high-churn skills have
tighter checked-in budgets in `internal/generate/skill_size_test.go`; remove duplicated procedure and
reference its authoritative owner rather than raising a threshold.

## The generate / test loop

The package renderers are tested against source-derived expectations, so the real `skills/` tree stays
the only copy of skill content. After changing any skill:

```bash
go test ./internal/generate/
go build ./... && go vet ./... && go test ./...
```

Inspect the generated package locally when changing renderer behavior. The coupling test fails the
build if a forbidden plugin namespace leaks into the rendered package.

Regenerate the skill-layer image at a desktop viewport with:

```bash
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
  --headless=new --disable-gpu --hide-scrollbars --window-size=1600,4000 \
  --screenshot="$PWD/docs/skill-layers.png" \
  "file://$PWD/docs/skill-layers.html"
magick docs/skill-layers.png -crop 1600x1400+0+0 +repage docs/skill-layers.png
```

## Pull requests

Keep PRs small and single-intent. Sequence stacked work API → logic → integration. Commit messages use
a conventional-commit prefix and a body that explains *why*; PR summaries are concise prose (the diff
shows the *how*), with a `Testing Done` section that is just the commands you ran. Relay is built to
deliver exactly this kind of PR — `relay "<task>"` will drive one for you.
