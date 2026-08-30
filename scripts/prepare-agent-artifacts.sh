#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
CODEX_SOURCE=${CODEX_BINARY:-$(command -v codex || true)}
CLAUDE_SOURCE=${CLAUDE_CODE_BINARY:-$(command -v claude || true)}

mkdir -p "${ROOT}/artifacts"

if [[ -n "${CODEX_SOURCE}" ]]; then
  install -m 0755 "$(readlink -f "${CODEX_SOURCE}")" "${ROOT}/artifacts/codex"
  "${ROOT}/artifacts/codex" --version
else
  echo "Codex binary not found; set CODEX_BINARY=/path/to/codex" >&2
fi

if [[ -n "${CLAUDE_SOURCE}" ]]; then
  install -m 0755 "$(readlink -f "${CLAUDE_SOURCE}")" "${ROOT}/artifacts/claude-code"
  "${ROOT}/artifacts/claude-code" --version
else
  echo "Claude Code binary not found; set CLAUDE_CODE_BINARY=/path/to/claude" >&2
fi

echo "Agent artifacts prepared under ${ROOT}/artifacts"
