#!/usr/bin/env python3
"""
Compute stylometrics over gathered writing, overall and per register.

Reads any of:
  - GitHub JSON (descriptions.json / comments.json from fetch_github.py)
  - transcript text (messages separated by '=====MSG=====' from extract_transcripts.py)
  - arbitrary text/markdown files (treated as one register, e.g. "blog")

By default it EXCLUDES content tagged agent_authored (GitHub) and filters obvious
template prompts (transcripts), so the numbers reflect the person's own voice.

Metrics per register and overall: count, em-dashes, semicolons, lowercase-start
rate, avg sentence length (words), question rate, contraction rate, exclamation
rate, top sentence openers. These anchor the profile in fact (e.g. "~zero
em-dashes", "60% of review comments start lowercase").

Usage:
  python analyze_style.py \
      --github-dir source/github \
      --transcripts source/transcripts/messages.txt \
      --text blog=~/blog/**/*.md --text docs=source/notes/*.md
"""
import argparse, glob, json, os, re
from collections import Counter

def starts_lower(s):
    for ch in s:
        if ch.isalpha():
            return ch.islower()
    return False

def metrics(texts):
    texts = [t.strip() for t in texts if t and t.strip()]
    n = len(texts)
    if n == 0:
        return None
    words_total = sum(len(t.split()) for t in texts)
    emdash = sum(t.count("—") for t in texts)
    semic = sum(t.count(";") for t in texts)
    excl = sum(t.count("!") for t in texts)
    low = sum(1 for t in texts if starts_lower(t))
    q = sum(1 for t in texts if "?" in t)
    contr = sum(len(re.findall(r"\b\w+'(?:s|re|ve|ll|t|d|m)\b", t, re.I)) for t in texts)
    sents = []
    for t in texts:
        for s in re.split(r"(?<=[.!?])\s+", t):
            wl = len(s.split())
            if 2 < wl < 80:
                sents.append(wl)
    avg_sent = round(sum(sents) / len(sents), 1) if sents else 0
    openers = Counter()
    for t in texts:
        w = re.findall(r"[A-Za-z']+", t)
        if w:
            openers[w[0].lower()] += 1
    per1k = lambda x: round(1000 * x / max(1, words_total), 2)
    return {
        "items": n, "words": words_total,
        "lowercase_start_pct": round(100 * low / n),
        "question_pct": round(100 * q / n),
        "avg_sentence_words": avg_sent,
        "emdash_per_1k": per1k(emdash), "semicolon_per_1k": per1k(semic),
        "exclamation_per_1k": per1k(excl), "contraction_per_1k": per1k(contr),
        "emdash_total": emdash, "semicolon_total": semic,
        "top_openers": openers.most_common(15),
    }

def load_github(d, include_agent):
    reg = {}
    for fn, kindfield in (("descriptions.json", None), ("comments.json", "kind")):
        p = os.path.join(d, fn)
        if not os.path.exists(p):
            continue
        for rec in json.load(open(p)):
            if rec.get("agent_authored") and not include_agent:
                continue
            if fn == "descriptions.json":
                reg.setdefault("pr_description", []).append(rec.get("body", "") or "")
            else:
                reg.setdefault(rec.get(kindfield, "comment"), []).append(rec.get("body", "") or "")
    return reg

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--github-dir", default="")
    ap.add_argument("--transcripts", default="")
    ap.add_argument("--text", action="append", default=[],
                    help="register=glob, e.g. blog=~/blog/**/*.md (repeatable)")
    ap.add_argument("--include-agent", action="store_true",
                    help="include content tagged agent_authored (default: exclude)")
    ap.add_argument("--out", default="", help="optional path to write JSON summary")
    args = ap.parse_args()

    registers = {}
    if args.github_dir:
        registers.update(load_github(args.github_dir, args.include_agent))
    if args.transcripts and os.path.exists(args.transcripts):
        msgs = open(args.transcripts).read().split("=====MSG=====")
        registers["chat"] = [m.strip() for m in msgs if m.strip()]
    for spec in args.text:
        if "=" not in spec:
            continue
        name, pattern = spec.split("=", 1)
        chunks = []
        for fp in glob.glob(os.path.expanduser(pattern), recursive=True):
            try:
                t = open(fp).read()
            except Exception:
                continue
            t = re.sub(r"```.*?```", " ", t, flags=re.S)      # strip code
            t = re.sub(r"^\+\+\+.*?\+\+\+", " ", t, flags=re.S)  # toml frontmatter
            t = re.sub(r"^---.*?---", " ", t, flags=re.S)        # yaml frontmatter
            t = re.sub(r"https?://\S+", " ", t)
            chunks.append(t)
        if chunks:
            registers[name] = chunks

    summary = {}
    alltexts = []
    print("=" * 64)
    for reg, texts in registers.items():
        m = metrics(texts)
        if not m:
            continue
        summary[reg] = m
        alltexts += texts
        print(f"\n[{reg}]  items={m['items']}  words={m['words']}")
        print(f"  lowercase-start: {m['lowercase_start_pct']}%   questions: {m['question_pct']}%   "
              f"avg sentence: {m['avg_sentence_words']} words")
        print(f"  em-dash/1k: {m['emdash_per_1k']} (tot {m['emdash_total']})   "
              f"semicolon/1k: {m['semicolon_per_1k']} (tot {m['semicolon_total']})   "
              f"contraction/1k: {m['contraction_per_1k']}")
        print(f"  top openers: {m['top_openers'][:10]}")
        # Short-message baseline: the cleanest own-typed signal, least contaminated
        # by pasted blocks or agent-generated template prompts. If em-dashes/
        # semicolons vanish here but show up above, the long items are pasted/AI.
        short = [t for t in texts if len(t.strip()) <= 280]
        ms = metrics(short)
        if ms and ms["items"] >= 5:
            summary.setdefault(reg + "__short", ms)
            print(f"  short-only ({ms['items']} msgs <=280ch): em-dash/1k={ms['emdash_per_1k']} "
                  f"semicolon/1k={ms['semicolon_per_1k']} lowercase-start={ms['lowercase_start_pct']}%  "
                  f"<- cleanest mechanics signal")
    overall = metrics(alltexts)
    if overall:
        summary["_overall"] = overall
        print(f"\n[OVERALL] items={overall['items']} words={overall['words']} "
              f"em-dash/1k={overall['emdash_per_1k']} semicolon/1k={overall['semicolon_per_1k']} "
              f"avg-sentence={overall['avg_sentence_words']}")
    print("=" * 64)
    if args.out:
        json.dump(summary, open(args.out, "w"), indent=2)
        print(f"summary written to {args.out}")

if __name__ == "__main__":
    main()
