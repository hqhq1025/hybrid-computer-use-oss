"""Small image helpers that avoid optional Pillow dependencies."""

from __future__ import annotations

import base64
import hashlib
import struct


def png_content_digest(encoded: str) -> str:
    """Hash PNG image-data chunks while ignoring timestamp/metadata chunks."""
    data = base64.b64decode(encoded)
    if data[:8] != b"\x89PNG\r\n\x1a\n":
        return hashlib.sha256(data).hexdigest()
    offset = 8
    image_data = bytearray()
    while offset + 12 <= len(data):
        size = struct.unpack(">I", data[offset:offset + 4])[0]
        kind = data[offset + 4:offset + 8]
        payload_start = offset + 8
        payload_end = payload_start + size
        if payload_end + 4 > len(data):
            break
        if kind in {b"IHDR", b"PLTE", b"IDAT", b"IEND"}:
            image_data.extend(kind)
            image_data.extend(data[payload_start:payload_end])
        offset = payload_end + 4
    return hashlib.sha256(image_data).hexdigest()
