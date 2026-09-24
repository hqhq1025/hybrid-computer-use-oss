#!/usr/bin/env bash
set -euo pipefail

REVISION="a5d9c3eaae98eebf6e3a0beb84e7e47cf72ae133"
ARCHIVE_NAME="Ubuntu.qcow2.zip"
IMAGE_NAME="Ubuntu.qcow2"
ARCHIVE_SIZE="12273896463"
ARCHIVE_SHA256="b795b6cd4c69b252c1b4f10150a347795555032501b60fd031751ed09b896712"
URL="https://huggingface.co/datasets/xlangai/ubuntu_osworld/resolve/${REVISION}/${ARCHIVE_NAME}"

if [[ $# -ne 1 ]]; then
    echo "usage: $0 DESTINATION_DIRECTORY" >&2
    exit 2
fi

destination="$(realpath -m "$1")"
archive="${destination}/${ARCHIVE_NAME}"
image="${destination}/${IMAGE_NAME}"
mkdir -p "$destination"

if [[ -f "$archive" ]]; then
    current_size="$(stat -c %s "$archive")"
    if (( current_size >= ARCHIVE_SIZE )) \
        && ! echo "${ARCHIVE_SHA256}  ${archive}" | sha256sum --check --status; then
        rm -f "$archive" "${archive}.aria2"
    fi
fi

if [[ ! -f "$archive" ]] \
    || ! echo "${ARCHIVE_SHA256}  ${archive}" | sha256sum --check --status; then
    if command -v aria2c >/dev/null 2>&1; then
        aria2c \
            --continue=true \
            --max-connection-per-server=16 \
            --split=16 \
            --min-split-size=16M \
            --file-allocation=none \
            --dir="$destination" \
            --out="$ARCHIVE_NAME" \
            "$URL"
    else
        curl --fail --location --continue-at - --output "$archive" "$URL"
    fi
fi

echo "${ARCHIVE_SHA256}  ${archive}" | sha256sum --check
unzip -t "$archive"

# Re-extract when the image is missing or truncated by an interrupted unzip.
expected_image_size="$(unzip -Zl "$archive" "$IMAGE_NAME" | awk '{print $4}')"
if [[ ! -f "$image" ]] || [[ "$(stat -c %s "$image")" != "$expected_image_size" ]]; then
    unzip -o "$archive" -d "$destination"
fi

qemu-img info "$image"

printf 'export OSWORLD_V1_GUEST_IMAGE=%q\n' "$image"
