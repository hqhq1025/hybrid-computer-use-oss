#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
OUT="${ROOT}/artifacts/releases/v0.14"
TMP=$(mktemp -d)
trap 'rm -rf "${TMP}"' EXIT

mkdir -p "${OUT}"
docker build \
  --file "${ROOT}/docker/Dockerfile.binary" \
  --output "type=local,dest=${TMP}" \
  "${ROOT}"
install -m 0755 "${TMP}/computer-use" "${OUT}/computer-use"
cp "${ROOT}/releases/v0.14/release.json" "${OUT}/release.json"

sha256sum "${OUT}/computer-use"
"${OUT}/computer-use" --version
