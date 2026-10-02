#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
OUT="${1:-${ROOT}/artifacts}"
if [[ $# -gt 1 ]]; then
  echo "usage: build-release.sh [NEW_OUTPUT_DIRECTORY]" >&2
  exit 2
fi
# An explicit output is a distribution directory, never a shared download cache.
if [[ $# -eq 1 && -e "${OUT}" ]]; then
  echo "Release output directory must not exist: ${OUT}" >&2
  exit 2
fi
TMP=$(mktemp -d)
trap 'rm -rf "${TMP}"' EXIT

mkdir -p "${OUT}"
# Required Go dependencies are vendored, so the build needs no network.
docker build \
  --network none \
  --file "${ROOT}/docker/Dockerfile.binary" \
  --output "type=local,dest=${TMP}" \
  "${ROOT}"
install -m 0755 "${TMP}/computer-use" "${OUT}/computer-use"
bash "${ROOT}/scripts/install-license-notices.sh" "${OUT}"

"${OUT}/computer-use" --version
