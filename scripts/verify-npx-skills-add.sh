#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
package_path="${repo_root}"

tmp_home="$(mktemp -d)"
tmp_config="$(mktemp -d)"
tmp_npm_cache="$(mktemp -d)"
cleanup() {
  rm -rf "${tmp_home}" "${tmp_config}" "${tmp_npm_cache}"
}
trap cleanup EXIT

export HOME="${tmp_home}"
export XDG_CONFIG_HOME="${tmp_config}"
export npm_config_cache="${tmp_npm_cache}"

if [[ ! -d "${repo_root}/skills" ]]; then
  echo "FAIL: ${repo_root}/skills does not exist" >&2
  exit 1
fi

if [[ -n "${SKILLS_CMD:-}" ]]; then
  read -r -a skills_cmd <<<"${SKILLS_CMD}"
elif command -v skills >/dev/null 2>&1; then
  skills_cmd=(skills)
elif command -v npm >/dev/null 2>&1; then
  skills_cmd=(npm exec --offline -- skills)
else
  echo "SKIP: neither skills nor npm is available to probe the skills CLI"
  exit 0
fi

skills_add=("${skills_cmd[@]}" add)

help_output="$("${skills_cmd[@]}" --help 2>&1)" || {
  echo "SKIP: skills CLI help is unavailable with command: ${skills_cmd[*]}"
  echo "${help_output}"
  exit 0
}

echo "Using skills add command: ${skills_add[*]}"

run_install() {
  local label="$1"
  shift
  echo "Checking ${label}: ${skills_add[*]} ${package_path} $*"
  "${skills_add[@]}" "${package_path}" "$@"
}

run_install "baseline install"

if grep -Eq -- '(^|[ ,])--agent([ =]|$)' <<<"${help_output}"; then
  run_install "codex-targeted install" --agent codex
else
  echo "SKIP: --agent is not advertised by skills --help"
  echo "${help_output}"
fi

if grep -Eq -- '(^|[ ,])--all([ ,]|$)' <<<"${help_output}"; then
  run_install "all-agent install" --all
else
  echo "SKIP: --all is not advertised by skills --help"
  echo "${help_output}"
fi
