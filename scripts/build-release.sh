#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
OUT="${ROOT}/artifacts"
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
