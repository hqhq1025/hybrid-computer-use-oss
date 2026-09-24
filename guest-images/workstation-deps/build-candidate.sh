#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
usage="usage: build-candidate.sh SOURCE_QCOW2 CANDIDATE_QCOW2 SIZE_GIB CACHE_ROOT [MANIFEST_DIR]"
source_image="${1:?${usage}}"
candidate_image="${2:?${usage}}"
size_gib="${3:?${usage}}"
cache_root="${4:?${usage}}"
manifest_dir="${5:-${script_dir}/profiles/v2-full}"

if [[ ! -f "${source_image}" ]]; then
    printf 'source image does not exist: %s\n' "${source_image}" >&2
    exit 1
fi
if [[ -e "${candidate_image}" ]]; then
    printf 'candidate already exists: %s\n' "${candidate_image}" >&2
    exit 1
fi

mkdir -p "$(dirname "${candidate_image}")"
qemu-img create -f qcow2 \
    -o compat=1.1,lazy_refcounts=on \
    "${candidate_image}" \
    "${size_gib}G"
sudo env LIBGUESTFS_MEMSIZE=4096 virt-resize \
    --expand /dev/sda3 \
    "${source_image}" \
    "${candidate_image}"
sudo chown "$(id -u):$(id -g)" "${candidate_image}"

sudo env LIBGUESTFS_MEMSIZE=4096 virt-customize \
    -a "${candidate_image}" \
    --copy-in "${cache_root}/debs:/tmp" \
    --copy-in "${cache_root}/wheelhouse:/tmp" \
    --copy-in "${cache_root}/node-runtime:/tmp" \
    --upload "${cache_root}/node-v20.19.5-linux-x64.tar.xz:/tmp/node-v20.19.5-linux-x64.tar.xz" \
    --upload "${manifest_dir}/apt-packages.txt:/tmp/osw-apt-packages.txt" \
    --upload "${manifest_dir}/python-packages.txt:/tmp/osw-python-packages.txt" \
    --upload "${manifest_dir}/node-packages.txt:/tmp/osw-node-packages.txt" \
    --upload "${script_dir}/customize-guest.sh:/tmp/osw-customize-guest.sh" \
    --run-command '/bin/bash /tmp/osw-customize-guest.sh'

qemu-img check "${candidate_image}"
