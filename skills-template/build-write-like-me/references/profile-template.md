# The generated profile: structure and synthesis guidance

This is the shape of the `SKILL.md` you produce for the person, plus how to write
each part well. Adapt the sections to what the data supports — drop a section if
you have no evidence for it rather than padding it.

## Frontmatter

```yaml
---
name: sound-like-me
description: >-
  Write or edit prose in <Person>'s voice, or check that a draft sounds like
  them. Use for <the surfaces they actually write: blog posts, design docs, PR
  descriptions and review comments, chat, feedback, email>, or any first-person
  writing on their behalf, and whenever an agent is producing text that goes out
  under their name and needs to sound like them, not generated.
---
```

The skill is always named `sound-like-me`; the description carries the person's
name and surfaces so it triggers correctly.
Make the description specific and a little "pushy" about when to trigger — that
field is the main thing that decides whether an agent loads the skill.

## Body sections (in order)

1. **Title + one-line purpose.** "Write as <Person>. The goal is prose
   indistinguishable from what they'd write themselves."

2. **Voice in one paragraph.** A dense, readable summary of the through-line that
   holds across every surface. Lead with the strongest traits.

3. **Non-negotiable mechanics.** The handful of rules that, if broken, instantly
   break the illusion. These are the highest-signal, most testable items, so put
   them near the top. Typical entries: em-dash policy, semicolon policy,
   contractions, capitalization-by-surface, sentence economy, "ground every
   claim." Back each with the data ("essentially zero em-dashes across N docs").

4. **Pick the register.** A table mapping each surface they write on to: caps
   (lower/proper), typical length, stance, and signature tells. Then a one-line
   rule of thumb (e.g. "chat and review are fast and lowercase; anything written
   down is full prose"). This is often the most useful section — the same person
   sounds very different by surface.

5. **The moves that make it sound like them.** The recurring rhetorical habits:
   how they open, how they reason, how they frame decisions, how they hedge, how
   they handle caveats, how they critique. Write these as directives with a short
   "why."

6. **Register playbooks.** One short subsection per surface, each with 2-4
   **verbatim examples**. Verbatim quotes are the most valuable content in the
   whole profile — they transmit voice better than any description. Pull them
   from the cleaned, person-authored data.

7. **Signature phrases.** A scannable list of their actual recurring phrases and
   markers (openers, connectors, acknowledgements, labeled asides, etc.).

8. **What is NOT their voice (anti-slop tells).** The things that make a draft
   read as generated or ghost-written, especially the mechanical tells you found
   (em-dashes, templated walls, filler words, exhaustive auto-replies). This
   section is what keeps an agent from producing slop in their name.

9. **Editing mode.** How they edit (cut, tighten, preserve voice) vs. draft.

10. **Pre-send checklist.** A short numbered list an agent can run before output
    goes out: the mechanics, register fit, and "does it read like a person typed
    it."

## Synthesis principles

- **Ground every claim in data.** No invented traits. If you can't point to real
  writing for a trait, leave it out. Cite frequencies where you have them.
- **Quote verbatim, generously.** Lightly clean (fix a typo, trim length) but
  keep the phrasing. Prefer their own sentences over your paraphrase.
- **Capture register variation explicitly.** Don't average a casual chatter and a
  formal doc-writer into a mushy middle. Show both and say when each applies.
- **Respect their mechanics exactly.** Em-dash/semicolon habits, capitalization,
  contraction rate, sentence length — these are the fingerprint. Get them right.
- **Target the current voice.** If the data spans years and the voice evolved,
  build the profile around how they write *now*, and note the shift.
- **Genericize.** Strip company/system/internal names from examples (replace with
  generic placeholders) so the profile travels, unless the invoker wants them
  kept. The voice lives in the phrasing, not the proper nouns.
- **Make it actionable, not academic.** Write to an agent that will *produce*
  text as this person. Imperative guidance ("Lead with the point") beats
  description ("They tend to lead with the point").

## Self-consistency (do not skip)

The generated skill must obey its own rules. If the profile says the person
avoids em-dashes and semicolons, the skill's own prose must avoid them too (the
one allowed exception is the rule line that names the forbidden character, e.g.
"No em-dashes (—)."). After writing, grep the output against its own mechanical
rules and fix violations:

```bash
grep -n '—' sound-like-me/SKILL.md   # only the rule reference should remain
grep -n ';'  sound-like-me/SKILL.md
```

**Also verify genericization.** Synthesis subagents reliably under-genericize:
they leave the person's real company/system/colleague names in the verbatim
examples. Grep the output for the proper nouns that showed up in the source data
(product names, internal systems, team/person names) and replace each with a
generic placeholder, keeping the surrounding phrasing intact. The voice lives in
the phrasing, not the nouns, so swapping "the nimbus-agent deployed as part of
LIPA" for "the agent deployed as part of the platform" loses nothing and lets the
profile travel.

A no-em-dash guide full of em-dashes undermines its own credibility and, worse,
teaches the wrong pattern by example.
