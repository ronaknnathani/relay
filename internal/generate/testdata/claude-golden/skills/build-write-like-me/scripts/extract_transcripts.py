#!/usr/bin/env python3
"""
Extract a person's own typed messages from local coding-agent transcripts.

Reads Claude Code transcripts (JSONL under ~/.claude/projects by default), keeps
`type=user` text turns, and filters out noise: tool results, slash-command
artifacts, pasted dumps, and agent-orchestration prompts that the agent itself
generated (which get recorded as user turns but are NOT the person's voice).

Output: a text file of de-duplicated messages separated by a marker, plus a small
stats line. Feed it to analyze_style.py or read it directly for verbatim voice.

Usage:
  python extract_transcripts.py --out source/transcripts/messages.txt \
      [--projects-dir ~/.claude/projects]
"""
import argparse, glob, json, os, re

# Noise markers: harness artifacts and pasted/templated content (not their voice)
NOISE = [
    "<command-name>", "<command-message>", "<local-command", "<system-reminder",
    "tool_use_id", "<bash-", "[Request interrupted", "<command-args>",
    "Contents of", "<user-prompt-submit-hook>", "Caveat:", "<task-notification>",
]
# Agent-orchestration templates recorded as user turns
TEMPLATE_STARTS = ("You are", "View ", "Check pull", "Re-check", "In the current repository",
                   "View GitHub", "Determine and report", "You're scoring", "You are scoring")
TEMPLATE_FLAGS = ["Steps:", "ELIGIBLE FOR REVIEW", "STILL ELIGIBLE", "Conclude with",
                  "Report each", "--json", "Your job is to return", "ISSUE A:", "ISSUE B:"]

def is_noise(t):
    s = (t or "").strip()
    if not s:
        return True
    if any(m in s for m in NOISE):
        return True
    if s.count("\n") > 40 or len(s) > 4000:   # pasted dumps
        return True
    return False

def is_template(s):
    s = s.strip()
    if s.startswith(TEMPLATE_STARTS):
        return True
    if any(f in s for f in TEMPLATE_FLAGS):
        return True
    if s.count("`gh ") >= 2:
        return True
    return False

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", required=True)
    ap.add_argument("--projects-dir", default="~/.claude/projects")
    ap.add_argument("--keep-templates", action="store_true",
                    help="don't drop agent-orchestration template prompts")
    args = ap.parse_args()

    base = os.path.expanduser(args.projects_dir)
    files = glob.glob(os.path.join(base, "**", "*.jsonl"), recursive=True)
    out = []
    for fp in files:
        try:
            fh = open(fp)
        except Exception:
            continue
        for line in fh:
            try:
                rec = json.loads(line)
            except Exception:
                continue
            if rec.get("type") != "user":
                continue
            msg = rec.get("message", {})
            if not isinstance(msg, dict) or msg.get("role") != "user":
                continue
            content = msg.get("content")
            texts = []
            if isinstance(content, str):
                texts.append(content)
            elif isinstance(content, list):
                for part in content:
                    if isinstance(part, dict) and part.get("type") == "text":
                        texts.append(part.get("text", ""))
            for t in texts:
                if is_noise(t):
                    continue
                if not args.keep_templates and is_template(t):
                    continue
                out.append(t.strip())
        fh.close()

    seen, uniq = set(), []
    for t in out:
        k = t[:200]
        if k in seen:
            continue
        seen.add(k)
        uniq.append(t)

    os.makedirs(os.path.dirname(os.path.abspath(args.out)), exist_ok=True)
    with open(args.out, "w") as w:
        w.write("\n\n=====MSG=====\n\n".join(uniq))
    lengths = [len(t) for t in uniq]
    avg = sum(lengths) // max(1, len(lengths))
    print(f"transcripts scanned: {len(files)} | messages kept: {len(uniq)} | avg len: {avg}")
    print(f"written to: {args.out}")

if __name__ == "__main__":
    main()
