#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: open-pr.sh <command> [args]

commands:
  status
  default-branch
  ensure-branch <slug>
  stage -- <file>...
  commit <message-file>
  diff
  push
  pr-template
  create-pr [--draft] --title <title> --body-file <file>
EOF
}

fail() {
  echo "open-pr: $*" >&2
  exit 1
}

relay_bin() {
  printf '%s\n' "${RELAY_BIN:-relay}"
}

default_branch() {
  local branch
  branch=$(gh repo view --json defaultBranchRef --jq .defaultBranchRef.name 2>/dev/null || true)
  if [[ -z "$branch" ]]; then
    git remote set-head origin -a >/dev/null 2>&1 || true
    branch=$(basename "$(git symbolic-ref --quiet refs/remotes/origin/HEAD 2>/dev/null || true)")
  fi
  [[ -n "$branch" ]] || fail "cannot detect the default branch"
  printf '%s\n' "$branch"
}

branch_prefix() {
  local prefix
  prefix=$("$(relay_bin)" config branch-prefix)
  [[ -n "$prefix" ]] || fail "configured branch prefix is empty"
  printf '%s\n' "$prefix"
}

current_branch() {
  local branch
  branch=$(git branch --show-current)
  [[ -n "$branch" ]] || fail "HEAD is detached; switch to a branch before opening a PR"
  printf '%s\n' "$branch"
}

cmd=${1:-}
[[ -n "$cmd" ]] || { usage; exit 2; }
shift

case "$cmd" in
  status)
    current_branch
    git status --short
    ;;
  default-branch)
    default_branch
    ;;
  ensure-branch)
    slug=${1:-}
    [[ -n "$slug" ]] || fail "ensure-branch requires a slug"
    default=$(default_branch)
    current=$(current_branch)
    if [[ "$current" == "$default" ]]; then
      branch="$(branch_prefix)$slug"
      git switch -c "$branch" || fail "branch create failed; refusing to commit on $default"
    fi
    ;;
  stage)
    [[ ${1:-} == "--" ]] || fail "stage requires -- before file paths"
    shift
    [[ $# -gt 0 ]] || fail "stage requires at least one file"
    git add -- "$@"
    ;;
  commit)
    message_file=${1:-}
    [[ -n "$message_file" ]] || fail "commit requires a message file"
    [[ -f "$message_file" ]] || fail "message file does not exist: $message_file"
    git commit -F "$message_file"
    ;;
  diff)
    default=$(default_branch)
    git diff "origin/$default...HEAD"
    ;;
  push)
    git push -u origin "$(current_branch)"
    ;;
  pr-template)
    gh repo view --json pullRequestTemplates --jq '.pullRequestTemplates[0].body // empty'
    ;;
  create-pr)
    draft=()
    title=
    body_file=
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --draft)
          draft=(--draft)
          shift
          ;;
        --title)
          title=${2:-}
          [[ -n "$title" ]] || fail "--title requires a value"
          shift 2
          ;;
        --body-file)
          body_file=${2:-}
          [[ -n "$body_file" ]] || fail "--body-file requires a value"
          shift 2
          ;;
        *)
          fail "unknown create-pr argument: $1"
          ;;
      esac
    done
    [[ -n "$title" ]] || fail "create-pr requires --title"
    [[ -n "$body_file" ]] || fail "create-pr requires --body-file"
    [[ -f "$body_file" ]] || fail "body file does not exist: $body_file"
    gh pr create "${draft[@]}" --title "$title" --body-file "$body_file"
    ;;
  *)
    usage
    exit 2
    ;;
esac
