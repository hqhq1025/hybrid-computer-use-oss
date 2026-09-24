#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
OUT="${ROOT}/artifacts"
TMP=$(mktemp -d)
trap 'rm -rf "${TMP}"' EXIT

mkdir -p "${OUT}"
docker build \
  --file "${ROOT}/docker/Dockerfile.binary" \
  --output "type=local,dest=${TMP}" \
  "${ROOT}"
install -m 0755 "${TMP}/computer-use" "${OUT}/computer-use"

"${OUT}/computer-use" --version
