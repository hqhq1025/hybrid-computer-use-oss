#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
OUT="${1:?usage: install-license-notices.sh OUTPUT_DIRECTORY}"
mkdir -p "${OUT}/licenses/golang.org/x/text"
cp -R "${ROOT}/licenses/." "${OUT}/licenses/"
install -m 0644 "${ROOT}/LICENSE" "${ROOT}/THIRD_PARTY_NOTICES.md" "${OUT}/"
install -m 0644 "${ROOT}/computer-use/vendor/golang.org/x/text/LICENSE" \
  "${ROOT}/computer-use/vendor/golang.org/x/text/PATENTS" "${OUT}/licenses/golang.org/x/text/"
