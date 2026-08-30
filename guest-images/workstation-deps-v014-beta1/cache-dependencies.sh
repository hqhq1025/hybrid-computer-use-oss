#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
usage="usage: cache-dependencies.sh CACHE_ROOT [MANIFEST_DIR]"
cache_root="${1:?${usage}}"
manifest_dir="${2:-${script_dir}/profiles/v2-full}"
mkdir -p "${cache_root}/debs" "${cache_root}/wheelhouse"
rm -f "${cache_root}/debs"/*.deb

docker run --rm \
    --network host \
    --mount "type=bind,src=${manifest_dir}/apt-packages.txt,dst=/input/apt-packages.txt,readonly" \
    --mount "type=bind,src=${cache_root}/debs,dst=/output" \
    ubuntu:22.04 \
    bash -lc '
        set -euo pipefail
        apt-get update
        apt-get install -y --no-install-recommends apt-rdepends dpkg-dev
        mapfile -t packages </input/apt-packages.txt
        {
            printf "%s\n" "${packages[@]}"
            apt-rdepends "${packages[@]}" | sed -n "/^[^ ].*/p"
        } | sort -u > /tmp/apt-closure.txt
        : >/tmp/apt-concrete.txt
        while IFS= read -r package; do
            candidate="$(apt-cache policy "${package}" \
                | sed -n "s/^  Candidate: //p" \
                | head -1)"
            if [[ -n "${candidate}" && "${candidate}" != "(none)" ]]; then
                printf "%s=%s\n" "${package}" "${candidate}" \
                    >>/tmp/apt-concrete.txt
            fi
        done </tmp/apt-closure.txt
        mapfile -t concrete_packages </tmp/apt-concrete.txt
        (cd /output && apt-get download "${concrete_packages[@]}")
        cd /output
        dpkg-scanpackages . /dev/null > Packages
        gzip -9 -c Packages > Packages.gz
    '

docker run --rm \
    --network host \
    --mount "type=bind,src=${manifest_dir}/python-packages.txt,dst=/input/python-packages.txt,readonly" \
    --mount "type=bind,src=${cache_root}/wheelhouse,dst=/output" \
    python:3.10-slim-bookworm \
    bash -lc '
        set -euo pipefail
        python -m pip install --no-cache-dir \
            pip==25.2 setuptools==80.9.0 wheel==0.45.1
        python -m pip wheel --no-cache-dir \
            --wheel-dir /output \
            --requirement /input/python-packages.txt
        python -m pip download --no-cache-dir \
            --dest /output \
            pip==25.2 setuptools==80.9.0 wheel==0.45.1
    '

node_version=20.19.5
node_archive="node-v${node_version}-linux-x64.tar.xz"
curl -fL --retry 5 \
    "https://nodejs.org/dist/v${node_version}/${node_archive}" \
    -o "${cache_root}/${node_archive}"
rm -rf "${cache_root}/node-runtime"
mkdir -p "${cache_root}/node-runtime"
mapfile -t node_packages <"${manifest_dir}/node-packages.txt"
docker run --rm \
    --network host \
    -e PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 \
    --mount "type=bind,src=${cache_root}/node-runtime,dst=/output" \
    node:${node_version}-bookworm-slim \
    npm install \
        --prefix /output \
        --ignore-scripts \
        --no-audit \
        --no-fund \
        "${node_packages[@]}"

find "${cache_root}/debs" -maxdepth 1 -type f -name '*.deb' -print0 \
    | sort -z | xargs -0 sha256sum >"${cache_root}/apt-debs.sha256"
(
    cd "${cache_root}/debs"
    sha256sum Packages Packages.gz >"${cache_root}/apt-repository.sha256"
)
find "${cache_root}/wheelhouse" -maxdepth 1 -type f -print0 \
    | sort -z | xargs -0 sha256sum >"${cache_root}/python-wheelhouse.sha256"
find "${cache_root}/node-runtime" -type f -print0 \
    | sort -z | xargs -0 sha256sum >"${cache_root}/node-runtime.sha256"
sha256sum "${cache_root}/${node_archive}" \
    >"${cache_root}/${node_archive}.sha256"
