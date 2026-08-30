#!/usr/bin/env python3
"""Load and validate product tool releases independently from evaluation runs."""

import hashlib
import json
import os
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
RELEASE_DIR = ROOT / "releases"
CURRENT_FILE = RELEASE_DIR / "CURRENT"
CANDIDATE_DIR = ROOT / "candidates"
LEGACY_MCP_COMMAND = "computer-use"
LEGACY_AX_TELEMETRY_PATH = "/tmp/computer-use-ax-telemetry.jsonl"


def mcp_binary_runtime(binary):
    if not isinstance(binary, dict):
        raise ValueError("implementation.mcp_binary must be an object")
    command = str(binary.get("command") or binary.get("path") or "").strip()
    if not command:
        raise ValueError(
            "implementation.mcp_binary must define command or path"
        )
    telemetry_path = str(binary.get("ax_telemetry_path") or "").strip()
    if not telemetry_path:
        executable = os.path.basename(command) or LEGACY_MCP_COMMAND
        telemetry_path = f"/tmp/{executable}-ax-telemetry.jsonl"
    return command, telemetry_path


def sha256_file(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def current_tool_version():
    version = CURRENT_FILE.read_text(encoding="utf-8").strip()
    if not version:
        raise ValueError(f"empty current tool version: {CURRENT_FILE}")
    return version


def tool_release_path(version):
    version = str(version or "").strip()
    if not version or "/" in version or version in {".", ".."}:
        raise ValueError(f"invalid tool version: {version!r}")
    nested = RELEASE_DIR / version / "release.json"
    if nested.is_file():
        return nested
    return RELEASE_DIR / f"{version}.json"


def load_tool_release(version):
    path = tool_release_path(version)
    try:
        release = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise ValueError(f"unknown tool version {version!r}: {path}") from exc
    if not isinstance(release, dict):
        raise ValueError(f"tool release must be a JSON object: {path}")
    if release.get("tool_version") != version:
        raise ValueError(
            f"tool release version mismatch: requested {version!r}, "
            f"manifest has {release.get('tool_version')!r}"
        )
    implementation = release.get("implementation")
    if not isinstance(implementation, dict):
        raise ValueError(f"tool release has no implementation object: {path}")
    for field in ("image", "image_id", "mcp_binary"):
        if not implementation.get(field):
            raise ValueError(f"tool release is missing implementation.{field}: {path}")
    return release


def tool_release_record(version):
    release = load_tool_release(version)
    path = tool_release_path(version)
    implementation = release["implementation"]
    binary = implementation["mcp_binary"]
    command, telemetry_path = mcp_binary_runtime(binary)
    record = {
        "tool_version": version,
        "tool_release_manifest": str(path.relative_to(ROOT)),
        "tool_release_manifest_sha256": sha256_file(path),
        "tool_component_version": binary["version"],
        "tool_image": implementation["image"],
        "tool_image_id": implementation["image_id"],
        "tool_mcp_command": command,
        "tool_ax_telemetry_path": telemetry_path,
    }
    public_tools = (release.get("contract") or {}).get("public_tools")
    if isinstance(public_tools, list) and public_tools:
        record["tool_public_tools"] = list(public_tools)
    return record


def result_tool_metadata(record):
    if not record:
        return {}
    result = {
        key: record[key]
        for key in (
            "tool_version",
            "tool_release_manifest",
            "tool_release_manifest_sha256",
            "tool_component_version",
        )
    }
    result["tool_mcp_command"] = record.get(
        "tool_mcp_command",
        LEGACY_MCP_COMMAND,
    )
    result["tool_ax_telemetry_path"] = record.get(
        "tool_ax_telemetry_path",
        LEGACY_AX_TELEMETRY_PATH,
    )
    if record.get("tool_public_tools"):
        result["tool_public_tools"] = list(record["tool_public_tools"])
    return result


def load_tool_candidate(path):
    path = Path(path)
    if not path.is_absolute():
        path = ROOT / path
    try:
        candidate = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise ValueError(f"unknown tool candidate manifest: {path}") from exc
    if not isinstance(candidate, dict):
        raise ValueError(f"tool candidate must be a JSON object: {path}")
    for field in (
        "candidate_id",
        "baseline_tool_version",
        "change_ids",
        "implementation",
    ):
        if not candidate.get(field):
            raise ValueError(f"tool candidate is missing {field}: {path}")
    implementation = candidate["implementation"]
    if not isinstance(implementation, dict):
        raise ValueError(
            f"tool candidate implementation must be an object: {path}"
        )
    for field in ("image", "image_id", "mcp_binary"):
        if not implementation.get(field):
            raise ValueError(
                f"tool candidate is missing implementation.{field}: {path}"
            )
    baseline = load_tool_release(candidate["baseline_tool_version"])
    return candidate, path, baseline


def tool_candidate_record(path):
    candidate, resolved_path, _ = load_tool_candidate(path)
    implementation = candidate["implementation"]
    binary = implementation["mcp_binary"]
    command, telemetry_path = mcp_binary_runtime(binary)
    try:
        relative = str(resolved_path.relative_to(ROOT))
    except ValueError:
        relative = str(resolved_path)
    record = {
        "tool_candidate_id": candidate["candidate_id"],
        "tool_candidate_manifest": relative,
        "tool_candidate_manifest_sha256": sha256_file(resolved_path),
        "baseline_tool_version": candidate["baseline_tool_version"],
        "tool_change_ids": list(candidate["change_ids"]),
        "tool_component_version": binary["version"],
        "tool_image": implementation["image"],
        "tool_image_id": implementation["image_id"],
        "tool_mcp_command": command,
        "tool_ax_telemetry_path": telemetry_path,
    }
    public_tools = (candidate.get("contract") or {}).get("public_tools")
    if isinstance(public_tools, list) and public_tools:
        record["tool_public_tools"] = list(public_tools)
    return record


def result_tool_candidate_metadata(record):
    if not record:
        return {}
    result = {
        key: record[key]
        for key in (
            "tool_candidate_id",
            "tool_candidate_manifest",
            "tool_candidate_manifest_sha256",
            "baseline_tool_version",
            "tool_change_ids",
            "tool_component_version",
            "tool_image",
            "tool_image_id",
        )
    }
    result["tool_mcp_command"] = record.get(
        "tool_mcp_command",
        LEGACY_MCP_COMMAND,
    )
    result["tool_ax_telemetry_path"] = record.get(
        "tool_ax_telemetry_path",
        LEGACY_AX_TELEMETRY_PATH,
    )
    if record.get("tool_public_tools"):
        result["tool_public_tools"] = list(record["tool_public_tools"])
    return result


def assert_tool_candidate_image(path, image, image_id=None):
    candidate, _, _ = load_tool_candidate(path)
    implementation = candidate["implementation"]
    expected_image = implementation["image"]
    if image != expected_image:
        raise ValueError(
            f"tool candidate {candidate['candidate_id']} requires image "
            f"{expected_image}, got {image}"
        )
    expected_id = implementation["image_id"]
    if image_id is not None and image_id != expected_id:
        raise ValueError(
            f"tool candidate {candidate['candidate_id']} image identity "
            f"mismatch: expected {expected_id}, got {image_id}"
        )
    return candidate


def assert_tool_release_image(version, image, image_id=None):
    release = load_tool_release(version)
    implementation = release["implementation"]
    expected_image = implementation["image"]
    if image != expected_image:
        raise ValueError(
            f"tool {version} requires image {expected_image}, got {image}"
        )
    expected_id = implementation["image_id"]
    if image_id is not None and image_id != expected_id:
        raise ValueError(
            f"tool {version} image identity mismatch: expected {expected_id}, "
            f"got {image_id}"
        )
    return release
