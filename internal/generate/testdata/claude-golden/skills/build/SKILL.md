---
name: build
description: "Start a new build project — creates worktree, branch, and begins planning"
argument-hint: "TASK_DESCRIPTION | --quick TASK"
disable-model-invocation: true
---

# /build — Start New Project

Create an isolated workspace and begin the planning phase.

## Steps

### 1. Parse Arguments

From `$ARGUMENTS`:
- If starts with `--quick`: set quick mode, strip the flag, rest is the task
- Otherwise: full mode, entire argument is the task

### 2. Create Project

Run the `build` CLI to create the project (worktree, branch, manifest, scaffold files):

```bash
# For full mode:
build --no-launch "$TASK"

# For quick mode:
build --no-launch --quick "$TASK"
```

If the CLI is not available, create the project inline:
- Derive slug from task (lowercase kebab-case, max 40 chars, strip filler words)
- `git worktree add .worktrees/ronaknnathani_<slug> -b ronaknnathani/<slug>`
- Create `~/.relay/active/<slug>/` with manifest.json and task.md

### 3. Enter Worktree

`cd` into the worktree path.

### 4. Start Planning

Invoke `/plan <SLUG>`.
