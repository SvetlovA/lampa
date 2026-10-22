#!/usr/bin/env bash
# ralphex calls this executable as `exec <codex options>` with the prompt on stdin.
# agrouter supplies its own exec and replays stdin to codex unchanged.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# These docs describe the project to Jev; they are not passed as codex arguments.
docs=(
  --doc="$root/AGENTS.md"
  --doc="$root/README.md"
  --doc="$root/backend/README.md"
  --doc="$root/docs/settings-sync-backend-design.md"
)

if [[ "${1:-}" == exec ]]; then
  shift
fi

exec agrouter exec codex "${docs[@]}" -- "$@"
