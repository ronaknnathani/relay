# Source gathering: how-to and gotchas

How to pull each source into `source/`. Read the relevant section before
gathering from that source.

## GitHub (PR descriptions + review/issue comments)

The richest source of a working engineer's voice. Use the bundled
`scripts/fetch_github.py` rather than hand-rolling `gh` calls.

What you need from the invoker:
- their GitHub **login** (e.g. `janedoe` or `janedoe_Corp`),
- the **org(s)** or repo(s) to search,
- optionally a date range (e.g. "all of 2025") to bias toward hand-written,
  pre-agent material.

Auth: LinkedIn-style enterprise orgs live on `github.com` under a separate
account; the standard `gh` CLI works once that account is active
(`gh auth status` to check). For true GitHub Enterprise Server hosts, pass
`--hostname`.

What the script collects:
- **PR descriptions** for PRs the person authored (the PR body),
- **review comments, issue comments, and review bodies** the person wrote on any
  PR they commented on.

### Critical: GitHub rate limits

GitHub's **search** endpoint (`search/issues`) is throttled hard, and a burst of
calls trips a **secondary rate limit** that silently fails subsequent calls. Two
rules the script already encodes, but know them if you modify it:

1. **Use GET, with query params in the path.** Do NOT pass `-f key=value` to
   `gh api` for a read — `-f` flips the request to POST, which on a comments
   endpoint tries to *create* a comment (422) and trips the abuse detector.
   Correct: `gh api -X GET "/repos/OWNER/REPO/issues/123/comments?per_page=100"`.
2. **Back off on rate-limit responses.** When stderr/stdout contains "secondary
   rate limit" / "rate limit" / "abuse", sleep with growing backoff and retry.
   Serialize the search calls (a few seconds apart) and keep fetch concurrency
   low (3-4 workers).

If you see every result come back as 0 or empty, you are almost certainly
rate-limited, not "finished." Check for the rate-limit message.

## Google Docs

**First check the capability exists.** Not every environment has a Docs reader.
Inspect the available MCP tools/capability list for "google docs read" and the
available skills list for something that can fetch a Google Doc by ID. If nothing is available, skip
Docs, tell the invoker, and offer the fallback: have them export the docs (File →
Download → Markdown/Text) or paste content into local files, then use the
local-filesystem path instead.

There is also **no Drive search** in the standard toolset, so even when a reader
exists, the invoker must give you the specific doc links/IDs (or a folder they
can enumerate). Extract the doc ID from a URL like
`https://docs.google.com/document/d/<DOC_ID>/edit?tab=t.0`.

If a Docs reader is available (commonly a `read_google_docs_document` MCP tool),
read each doc with it:
- Use `include_all_tabs=true` to get multi-tab docs.
- **Use only the `full_text` field.** The response also returns a giant
  `element_mapping` array of per-paragraph styling — ignore it, it is pure noise
  and will blow up context.
- If `has_more` is true on a long doc, the first ~8000 chars of prose is plenty
  for voice; don't exhaustively paginate.

Because the output is token-heavy, **fan out subagents** (~3-4 docs each) that
read and return distilled voice notes + verbatim quotes, not raw doc text. Tell
each subagent to note the `Author:` line and judge whether the prose is
hand-written or AI-edited (see agent-content-filtering.md).

## Local files (blog, docs repos, notes)

Read markdown/text directly, or have a subagent summarize voice per file. For a
blog, parse front-matter dates so you can track evolution over time. Strip code
blocks, YAML front-matter, and link URLs before computing stylometrics so they
don't skew sentence-length and punctuation counts.

## Session transcripts (coding-agent chat)

`scripts/extract_transcripts.py` reads local Claude Code transcripts
(`~/.claude/projects/**/*.jsonl`), pulls `type=user` text messages, and filters
out noise. Important nuance: transcripts contain two very different things:
- the person's **own typed messages** (authentic casual/chat voice) — keep these,
- **agent-orchestration prompts that the agent itself generated** and that got
  recorded as user turns (e.g. "You are scoring...", "View pull request #N...",
  long templated task prompts) — these are NOT the person's voice; filter them.

The script applies heuristics for this, but spot-check: if a "message" is a
structured task prompt with `Steps:` and multiple shell commands, it's a template,
not their voice.

## Slack

Ask if they want Slack, then **check for a capability** before promising anything.
Inspect the available MCP tools/capability list for "slack messages" or "slack
search" and the available skills list for something that can fetch *this
person's own messages*. Order of
preference:
1. A Slack tool/skill that supports an author filter (`from:@them`, e.g. a
   `search.messages` style API) — use it.
2. A real **export** they provide (Slack JSON / `.mbox`) — parse it via the
   local-filesystem path.
3. Otherwise, **skip and tell the invoker** Slack couldn't be fetched.

Important caveat that catches people out: the common semantic "search Slack" tools
have **no author filter**. They rank public-channel content by relevance and
cannot return "all messages from person X" — a `from:@them` string in such a query
is treated as plain search text and ignored. A tool merely being present does not
mean it can isolate one person's voice. If the only Slack tool is semantic search,
treat Slack as unavailable and ask for an export instead.

## Email

Same pattern. Ask, then check for a capability: an email/Gmail MCP or skill that
can return the person's **sent** messages, or a local export (e.g. an `.mbox` of
Sent mail). Use whichever exists. If neither does, skip and note it.

When you do have email, parse for messages the person sent, and strip quoted reply
chains and signatures before analyzing so you capture their words, not the thread.
