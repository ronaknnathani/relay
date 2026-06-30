---
name: explore
description: Build a verified, read-only understanding of a codebase or a slice of it — entry points, code flow, architecture, module boundaries, dependencies, data flow, build/test systems, and existing patterns. A shared sub-skill that clarify, plan, implement, and review call on demand when they need codebase context; not invoked directly by users. Never edits anything.
---

# Explore

Build an accurate, cited map of a codebase — or one feature/area within it — so a downstream skill can
act on it. The output is understanding, not change: this skill **observes and reports only and must
never modify a file, run a mutating command, or write anything except its report.** The bar is a map a
reader can trust because every claim is either backed by a `file:line` reference or explicitly flagged
as unverified. Scope to the topic the caller named; do not map the whole repo when asked about a slice.

## Shared vocabulary

Use these terms so downstream skills that act on this map share one language:

- **Interface** — the surface a caller sees (signatures, exported names, public API).
- **Implementation** — the code behind that surface.
- **Module depth** — how much behavior an interface hides. A **deep** module hides a lot behind a
  small interface (good — complexity is *concentrated*); a **shallow** one exposes nearly as much as it
  hides (a thin pass-through that mostly *relocates* complexity).
- **Seam** — a boundary where behavior can be substituted or intercepted (an interface, a port, a
  dependency-injection point). Name seams explicitly; they are where `plan` and `implement` will cut.

## Process

1. **Detect the stack first — before asserting anything.** Read the dependency/manifest files
   (`go.mod`, `package.json`, `pyproject.toml`/`requirements.txt`, `Cargo.toml`, `pom.xml`, `Gemfile`,
   etc.) to learn languages, frameworks, and **versions**, plus the build/test entry points (`Makefile`,
   CI config, `scripts`). Cite the file for every stack claim; if you cannot confirm a version or
   choice, say so rather than guessing.
2. **Stage 1 — Feature/Area Discovery.** Find the entry points and the files relevant to the topic.
   Grep for the feature's names, routes, types, and config keys; locate where execution or a request
   begins. List the candidate files before diving in.
3. **Stage 2 — Code-Flow Tracing.** Follow the call chain end to end from each entry point, **through
   every abstraction layer**, down to where data is persisted, returned, or leaves the process (DB,
   queue, network, file). Record the chain as `file:line → file:line`. Note each seam you cross and
   whether the module behind it is deep or shallow.
4. **Stage 3 — Architecture Analysis.** Step back: module boundaries, ownership (who/what owns each
   area — `CODEOWNERS`, directory structure), dependencies and their direction, and the recurring
   patterns and existing abstractions a consumer should reuse rather than reinvent.
5. **Stage 4 — Implementation Details.** Capture the specifics a consumer needs: key signatures,
   invariants, error/validation paths, configuration, and the build/test commands that exercise this
   area — each with a `file:line`. **Identify and cite these commands; do not run them** — a build or
   test can mutate state (codegen, dependency installs, caches, network), which this skill must not.
6. **Close with the essential-files list** (see below) — the compact handoff artifact. Dispatch a
   sub-agent per independent entry point or area when sub-agents are available; otherwise trace each
   inline, one at a time.

## Citations and uncertainty

- Reference findings as `path/to/file.go:42` so they are clickable and checkable. Prefer a real line
  over prose whenever one exists.
- Mark every claim you could not confirm in the code as **(unverified)**, and say what would confirm it.
  Never present an inference as a fact.

## Essential files (required closing artifact)

End every report with the handful of files — typically 3-8 — someone must read to understand this
topic, each with a one-line why and the seam or role it plays:

```markdown
## Essential files
- `path/api/handler.go:88` — entry point; request enters here, routes to the service seam.
- `path/service/x.go:12` — deep module; the core logic lives behind this interface.
- `path/store/repo.go:30` — persistence boundary; where data is written.
```

## Red flags

- Editing a file, or running any mutating/stateful command — this skill is strictly read-only.
- Running build/test/codegen commands to observe behavior — identifying and citing them is the job; executing them is not.
- Asserting a framework or version without citing the manifest/dependency file it came from.
- Stopping the trace at an interface instead of following it through to where data lands.
- A claim with no `file:line` and no **(unverified)** tag.
- Mapping the entire repository when the caller asked about one feature or slice.
- Skipping the essential-files list, or padding it to twenty files instead of the load-bearing few.

## Verification checklist

- [ ] Stack and versions were read from dependency/manifest files first, each claim cited.
- [ ] Entry points identified, then traced end to end through every layer to where data is persisted.
- [ ] Module boundaries, ownership, dependencies, and reusable patterns are described.
- [ ] Every claim has a `file:line` or is tagged **(unverified)**.
- [ ] Seams are named, and deep vs. shallow modules are distinguished in the shared vocabulary.
- [ ] The report closes with a tight essential-files list (the load-bearing few, not an inventory).
- [ ] Nothing was modified — no files written beyond this report, no mutating commands run.
