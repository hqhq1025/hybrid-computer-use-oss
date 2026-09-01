#!/usr/bin/env python3
"""Provider V2 contract tests shared by macOS and Windows backends."""

import importlib.util
import io
import json
from pathlib import Path
import sys
import unittest
from unittest import mock


ROOT = Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from native.common.provider_v1 import wire_v2 as portable_wire
from native.common.provider_v1.images import png_content_digest


class FakeBackend:
    def observe_apps(self):
        return [{"name": "Editor", "pid": 42, "inputCapable": True}]

    def observe(self, payload):
        return {"ok": True, "text": payload["app"]}


class PortableProviderWireTests(unittest.TestCase):
    def test_schema_and_payload_version_are_strict(self):
        request = {
            "protocol": "osw-provider-v2",
            "schema_version": 2,
            "request_id": "portable-1",
            "operation": "observe_apps",
            "observe_apps": {"protocol_version": 1},
        }
        with self.assertRaises(portable_wire.NativeProviderError):
            portable_wire._validate_request(request)

    def test_dispatch_keeps_public_platform_out_of_contract(self):
        result = portable_wire._dispatch(
            FakeBackend(),
            "observe_apps",
            {"protocol_version": 2},
        )
        self.assertEqual(result["apps"][0]["name"], "Editor")
        self.assertNotIn("platform", result)

    def test_post_input_failure_is_effect_unknown(self):
        result = portable_wire._failure_result(
            portable_wire.NativeProviderError(
                "readback failed",
                code="readback_failed",
                phase="readback",
                emitted=True,
            ),
            mutable=True,
        )
        self.assertEqual(result["failure"]["code"], "effect_unknown")
        self.assertEqual(result["failure"]["underlying_code"], "readback_failed")
        self.assertTrue(result["mutation"]["input_events_emitted"])
        self.assertTrue(result["mutation"]["mutation_unknown"])

    def test_png_digest_ignores_ancillary_metadata(self):
        import base64
        import struct
        import zlib

        def chunk(kind, payload):
            return struct.pack(">I", len(payload)) + kind + payload + struct.pack(">I", zlib.crc32(kind + payload) & 0xFFFFFFFF)

        signature = b"\x89PNG\r\n\x1a\n"
        ihdr = chunk(b"IHDR", struct.pack(">IIBBBBB", 1, 1, 8, 6, 0, 0, 0))
        pixels = chunk(b"IDAT", zlib.compress(b"\x00\x00\x00\x00\xff"))
        end = chunk(b"IEND", b"")
        first = signature + ihdr + chunk(b"tEXt", b"timestamp=one") + pixels + end
        second = signature + ihdr + chunk(b"tEXt", b"timestamp=two") + pixels + end
        self.assertEqual(
            png_content_digest(base64.b64encode(first).decode()),
            png_content_digest(base64.b64encode(second).decode()),
        )


if __name__ == "__main__":
    unittest.main()
