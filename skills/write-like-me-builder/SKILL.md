---
name: write-like-me-builder
description: >-
  Use this skill whenever the user wants to capture, clone, or codify their own
  (or someone's) writing voice into a reusable skill. Trigger phrases include:
  "build a write-like-me skill", "build a sound-like-me skill", "analyze how I
  write", "make agents write or draft in my voice", "learn my style from my docs
  and PRs", "generate a voice profile from my github", "stop the agent sounding
  like a robot when it writes for me", "turn my docs and PRs into a personal
  style guide". It gathers a corpus of the person's real writing (GitHub PRs and
  comments, docs, blog posts, chat, optional Slack and email), filters out
  AI-written content, runs stylometric analysis, and synthesizes a reusable
  voice-profile skill. Prefer this skill over improvising the analysis yourself,
  because it encodes rate-limit-safe fetching and an agent-content filter you
  would otherwise get wrong. Do NOT use it to draft or edit a single email, PR,
  doc, or post, to lint code style, or to define team documentation standards.
---

# Write-Like-Me Builder

This skill builds a *personalized* writing-voice skill for one person. The output
is a `SKILL.md` that any agent can load to draft or edit prose in that person's
voice. You produce it by gathering the person's real writing from sources they
provide, analyzing it (both statistically and by reading it), and synthesizing a
profile grounded in real examples.

The core principle: **a voice profile is only as good as the real writing behind
it.** Don't invent traits. Every claim in the generated skill should trace to
something the person actually wrote. Quote them verbatim wherever you can.

## The workflow

1. **Collect sources** from the invoker (interactive prompt).
2. **Set up the workspace** (an output skill dir with a `source/` subdir for raw data).
3. **Gather raw data** into `source/`: {{subagent:large_context}} for delegated source workers where available, and use bundled scripts.
4. **Analyze** the data: run stylometrics, filter out content the person didn't
   actually write, and extract verbatim voice samples per register.
5. **Synthesize** the personalized `SKILL.md` voice profile.
6. **Self-consistency check** and present.

Do these in order. Steps 3 and 4 are where delegated workers do the heavy
lifting so the raw data never floods your context.

---

## Step 1: Collect sources

Ask the invoker which sources they want to include using whatever user-question
mechanism is available (multi-select if supported; otherwise ask a concise
numbered question). **Don't assume anything about their environment or what's
installed** — different users have different MCP servers and skills. Offer:

- **GitHub** — PR descriptions and review/issue comments they authored. Need
  their GitHub login and the org(s)/repo(s) to search. Usually the richest source
  of working voice.
- **Google Docs** — design docs, proposals, strategy docs, feedback. They provide
  doc links/IDs or a folder.
- **Local filesystem** — a blog repo, a docs directory, exported notes, an
  `.mbox`, a Slack export, anything on disk. They provide the path(s). **Always
  offer this** — it is the most portable source and sidesteps every tooling gap.
- **Session transcripts** — their own typed messages to coding agents. Great for
  the casual/chat register. Yes/no.
- **Slack** — yes/no.
- **Email** — yes/no.

If the invoker already named sources in their request, confirm and fill gaps
rather than re-asking everything.

### Discover capability before fetching (don't assume the environment)

Sources that need a tool to fetch (Google Docs, Slack, email, GitHub) are only
usable if that capability exists in *this* environment. So for each source the
invoker asked for, check first, then act, and degrade gracefully:

1. **Check whether a tool or skill can fetch it.**
   - Inspect the available MCP tools/capability list for relevant fetch/read/search
     capabilities (e.g. "google docs read", "slack messages search",
     "gmail email messages", "github").
   - Scan the available skills list for a relevant fetcher.
   - For GitHub, also confirm `gh auth status` works for the target host.
2. **If a capability exists, use it** (see Step 3 and `references/source-gathering.md`).
3. **If not, skip that source, record it, and continue with everything else.**
   Never let one missing source block the analysis. At the end, tell the invoker
   which sources were skipped and why (e.g. "Slack: no Slack fetch tool available
   in this environment"), and how to add it later (a local export almost always
   works, so point them back to the local-filesystem option).

Local-filesystem paths and bundled-script sources (transcripts) don't need this
check — they only need a path or the local transcript dir.

See `references/source-gathering.md` for the exact how-to and gotchas per source.

---

## Step 2: Set up the workspace

Decide where the generated skill goes (ask, or default to a directory named
`write-like-me/`). Inside it, create a `source/` subdir to hold all raw gathered
data:

```
write-like-me/
├── SKILL.md            <- the generated voice profile (final output)
└── source/             <- raw data, kept for transparency and re-runs
    ├── github/
    ├── gdocs/
    ├── transcripts/
    └── notes/
```

Keeping the raw data lets the invoker audit what the profile was built from, and
lets you re-run the analysis later without re-fetching.

---

## Step 3: Gather raw data

For each chosen source, fetch the raw data into `source/`. For large fetches:
{{subagent:large_context}} with one worker per source (or per batch) so raw data
does not fill your context. Use the bundled scripts for the fiddly parts.

- **GitHub** — `scripts/fetch_github.py` handles authoring + commenting,
  descriptions + comments, with rate-limit backoff and agent-content tagging.
  Run it from the assigned source worker (or inline for a small fetch) and write
  JSON into `source/github/`.
  Read `references/source-gathering.md` first — GitHub's search API rate-limits
  aggressively and the script encodes the safe pattern.
- **Google Docs** — only if a Docs-reading tool/skill exists (capability check in
  Step 1; commonly a document-reading MCP tool). If it does:
  {{subagent:large_context}} for each batch of ~3-4 doc IDs; each worker reads via the
  available Docs reader (use the `full_text` field, ignore the verbose element
  map), assesses authorship, and returns distilled voice notes + verbatim quotes
  into `source/gdocs/`. If no Docs capability is available, skip and note it (or
  ask for an export/local copies).
- **Local files** — read the markdown/text directly; for large file sets, start
  delegated workers: {{subagent:large_context}} to summarize voice per file. Save to
  `source/notes/`. This path also handles any export the invoker drops on disk
  (mbox, Slack JSON, etc.).
- **Session transcripts** — `scripts/extract_transcripts.py` pulls the person's
  own typed messages and filters out noise (commands, tool results, pasted dumps,
  templated agent prompts). Output to `source/transcripts/`.
- **Slack / email** — only if the Step 1 check found a usable capability. Use
  whatever Slack/email MCP or skill is available to pull the person's own
  messages, or parse a local export they provided. If neither exists, skip and
  tell the invoker it couldn't be fetched, then continue with the rest.

**Always save the raw data to disk before analyzing.** That is the
reference/source directory the invoker can inspect.

---

## Step 4: Analyze

Two complementary passes. Do both.

**a) Statistical (stylometrics).** Run `scripts/analyze_style.py` over the
gathered data. It computes, overall and per register: em-dash and semicolon
frequency, lowercase-sentence-start rate, average sentence length, question rate,
contraction rate, exclamation rate, top sentence openers, and filler-word counts.
These numbers anchor the profile in fact (e.g. "essentially zero em-dashes",
"~60% of review comments start lowercase", "average sentence dropped from 24 to
18 words over time").

**b) Filter out what they didn't actually write.** This is critical. Modern PRs
and docs are often agent-assisted, and including AI-generated text poisons the
profile. Exclude:
- comments/PR replies made on their behalf by a coding agent,
- PR descriptions for agent-written code,
- AI-edited passages.

`references/agent-content-filtering.md` has the heuristics (attribution markers,
the "fixed in `<sha>` + polished mechanism" auto-reply pattern, em-dash-as-AI-
tell, templated PR bodies). The scripts tag suspected agent content; you decide
the cutoffs and can eyeball borderline cases.

**c) Read for voice.** Numbers don't capture voice. For large cleaned corpora:
{{subagent:large_context}} to read; otherwise read inline. Pull out, per
register: 4-6 verbatim sentences that best capture the voice, structural
patterns, tone, and signature phrases. The verbatim quotes are the single most
valuable thing in the final profile.

Group by **register** (chat vs. code review vs. design doc vs. feedback vs. blog
vs. email). The same person writes very differently by surface, and the generated
skill must capture that. Also look for **evolution** over time if the data spans
years; if the voice shifted, target the *current* voice.

---

## Step 5: Synthesize the SKILL.md

Write the personalized voice profile following the structure and guidance in
`references/profile-template.md`. In short, the generated skill should contain:

- frontmatter (name + a pushy, specific description),
- voice in one paragraph,
- non-negotiable mechanics (the rules that, if broken, shatter the illusion),
- a register map (how their tone shifts by surface),
- the moves that make it sound like them,
- register playbooks with **verbatim examples**,
- signature phrases,
- anti-patterns ("what is NOT their voice"),
- an editing mode and a pre-send checklist.

Write it as an actionable guide to an agent that will *write as the person*, not
as an academic description. Genericize away the person's company/system/internal
names unless the invoker wants them kept (a voice profile should travel).

---

## Step 6: Self-consistency check and present

The generated skill must obey its own rules. If you concluded the person never
uses em-dashes or semicolons, the skill itself must not use them (a no-em-dash
guide riddled with em-dashes is self-defeating). Grep the output for its own
mechanical rules and fix violations:

```bash
grep -c '—' write-like-me/SKILL.md   # expect ~0 if they avoid em-dashes
grep -c ';'  write-like-me/SKILL.md
```

Then tell the invoker: where the skill is, what sources fed it, what you
excluded (and why), and any gaps (e.g. "Slack wasn't usable, here's how to add
it later"). Offer to draft a sample in one register so they can sanity-check the
voice.

---

## Bundled resources

- `scripts/fetch_github.py` — robust GitHub fetcher (authored + commented PRs;
  descriptions + comments; rate-limit backoff; agent-content tagging).
- `scripts/analyze_style.py` — stylometrics over gathered data, overall and per
  register, with agent-content filtering.
- `scripts/extract_transcripts.py` — pull a person's own messages from local
  coding-agent transcripts.
- `references/source-gathering.md` — per-source how-to and gotchas.
- `references/agent-content-filtering.md` — heuristics to exclude content the
  person didn't write.
- `references/profile-template.md` — the structure of the generated profile and
  detailed synthesis guidance.

## Notes on doing this well

- **Subjective skill, no rigid evals.** You can't unit-test a voice. The quality
  bar is "would the person recognize this as how they write?" Verbatim examples
  and accurate mechanics matter more than any score.
- **Delegate reading, synthesize yourself.** Reading 20 docs or 500 comments in
  your own context is wasteful and lossy. For large corpora, start reader
  workers: {{subagent:large_context}} and have them return distilled analyses;
  you keep the conclusions, not the dumps.
- **Be honest about gaps.** If a source was unusable or thin, say so in the
  handoff rather than papering over it with invented traits.
