---
name: clarify
description: Turn a vague or underspecified task into a crisp requirements + testable acceptance-criteria document by closing the gap between what was asked and what is actually wanted. Use at the start of any non-trivial task before planning, whenever the goal is fuzzy ("make it faster", "make it more robust", "add validation"), or whenever you cannot predict how the user would judge "done". Its output feeds `plan`. (For tidying existing code, use `simplify`, not clarify.)
---

# Clarify

Produce a requirements + acceptance-criteria artifact that pins down what success looks like, so a
planner never has to guess. The bar: every requirement is measurable and every acceptance criterion is
checkable — you could hand the artifact to someone else and they'd build the right thing. This is a
peer phase to `plan`, not its parent: it grounds itself in the code via `explore`, then hands its
artifact to `plan`. It does **not** design the implementation and does **not** invoke `plan`.

## Process

1. **Resolve from the codebase before asking.** If a question's answer is discoverable in the code —
   how a thing currently works, what the existing convention is, where an integration point lives —
   call `explore` (dispatch a sub-agent when available; otherwise do it inline) and find it. Only ask
   the user what you genuinely cannot determine. Bothering them with a discoverable fact erodes trust.
2. **Form an explicit hypothesis of the whole task.** Write down, for yourself, the outcome you think
   they want and the success criteria you'd accept. This is what you'll test against the stop
   condition — and it makes your questions sharper.
3. **Walk the coverage checklist** to find the real gaps. Probe each dimension; skip the ones the code
   or the request already settle:
   - **Edge cases** — empty/missing/malformed input, boundaries, concurrency, large scale.
   - **Error handling** — what should fail loudly, what should degrade, what the user sees on failure.
   - **Integration points** — callers, callees, schemas, events, and contracts this touches.
   - **Scope boundaries** — what is explicitly in vs. out; where this task ends.
   - **Design preferences** — conventions, libraries, patterns the user wants honored or avoided.
   - **Backward compatibility** — existing data, APIs, configs, behavior that must keep working.
   - **Performance needs** — latency/throughput/memory targets, and on which path.
4. **Interview one question at a time.** Asking several at once is bewildering — never dump a list.
   Each question carries your recommended default AND your current hypothesis, made visible:

   > **Q:** Should the cache invalidate on write, or expire on a TTL?
   > **GUESS:** TTL — the call site tolerates staleness and write-through adds lock contention here.

   Lead with the highest-leverage unknown (the one whose answer most changes the plan). Ask the
   recommended default as a yes/no when you can, so the user can confirm with a word.
5. **Reframe every vague requirement into a measurable one.** A requirement you can't test isn't a
   requirement. Convert runtime words into numbers and conditions:
   - "make it faster" → "p95 < 200ms on the cold path under N concurrent requests"
   - "handle errors gracefully" → "on a 5xx from X, retry twice then surface a typed error to the caller"
   - "clean up the API" → "remove deprecated field Y; no caller in the repo references it"
6. **Apply the stop condition (~95% confidence).** Stop interviewing when you can predict how the user
   would react to your next three questions — i.e. their answers wouldn't change the artifact. Don't
   pad with low-value questions once you're there; don't stop early while a checklist dimension is
   still genuinely open.
7. **Write the artifact** in the schema below and hand it to `plan`. List anything still unresolved
   under *Open assumptions* with the default you're proceeding on — never silently pick.

## Reject vague delegation

"Sounds good", "whatever you think", "you decide" is not a sign-off — it's an unanswered question. Do
not bank it as confidence. Restate your concrete recommendation and get an explicit yes:

> You said "whatever you think." To be explicit: I'll **expire on a 60s TTL** and not invalidate on
> write. Good to lock that in, or do you want write-through? (Y/n)

If the user truly defers, record it as a resolved assumption with your chosen default and the reason —
so the artifact still reads as a decision, not a shrug.

## Output schema (the artifact)

```markdown
# Clarified: <task>
## Outcome            — the one-sentence result the user actually wants
## Primary user       — who consumes this and in what workflow
## Why now            — the trigger / pain motivating it (1–2 lines; omit if there's no clear one)
## Success criteria    — testable, checkable list; each item a verifiable assertion
  - [ ] <measurable criterion, e.g. "p95 < 200ms on cold path">
## Constraints        — must-honor conventions, libraries, compat, perf budgets
## Out of scope       — explicitly excluded, so plan doesn't wander
## Open assumptions   — unresolved items + the default each proceeds on, and why
```

Every Success criterion must be checkable by a test, a command, or an unambiguous observation — never
"works well" or "is robust". If you can't state how you'd verify it, it isn't done. State each as an
**observable outcome** (what must be true), never the **mechanism** that achieves it — "on a 5xx, the
caller sees a typed error within 2 retries" is an outcome; *how* to implement the retry belongs to
`plan`. When a measurable reframe starts naming structure or an algorithm, you've crossed into design;
pull back to the outcome.

## Red flags

- Asking the user something `explore` could have answered from the codebase.
- Dumping several questions at once instead of one at a time.
- A question without a visible GUESS and a recommended default.
- A success criterion that isn't measurable ("fast", "clean", "robust", "user-friendly").
- Banking "sounds good" / "whatever you think" as confidence instead of restating and confirming.
- Silently choosing when uncertain instead of recording an Open assumption with its default.
- Designing the implementation, or invoking `plan` — both are out of scope for `clarify`.
- Interviewing past the stop condition, or stopping while a checklist dimension is still open.

## Verification checklist

- [ ] Every discoverable fact was resolved via `explore`, not asked of the user.
- [ ] Questions were asked one at a time, each with a visible GUESS and a recommended default.
- [ ] The coverage checklist was walked; each dimension is settled or listed under Open assumptions.
- [ ] Every Success criterion is testable/checkable — no vague adjectives.
- [ ] No vague delegation was banked as confidence; deferrals are recorded as resolved assumptions.
- [ ] The artifact follows the schema and is self-contained enough for `plan` to consume directly.
- [ ] The skill stayed in scope: no implementation design, no `plan` invocation.
