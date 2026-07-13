---
name: todo
description: "Capture, list, and complete repo-scoped todos"
---

# /todo — Repo-Scoped Todos

Capture todos from within a session for later pickup. Runs as a sub-agent to keep the main session context clean.

## Dispatch

Delegate this work to a fast subagent when the runtime supports it; otherwise do it inline with this prompt:

> Run the following shell command and report the output exactly as printed:
>
> ```bash
> relay todo $ARGUMENTS
> ```
>
> If `$ARGUMENTS` is empty, run `relay todo list` instead.
>
> Print the command output and nothing else. No commentary, no suggestions.
