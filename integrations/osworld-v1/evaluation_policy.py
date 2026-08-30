#!/usr/bin/env python3
"""Formal OSWorld task-set policy shared by the runners."""

from __future__ import annotations

import hashlib
import json
from pathlib import Path


ROOT = Path(__file__).resolve().parent
GOOGLE_DRIVE_POLICY_PATH = (
    ROOT / "evaluation_exclusions" / "google_drive.json"
)
FORMAL_TASK_SET_PATH = (
    ROOT / "evaluation_exclusions" / "formal_task_set.json"
)


def _walk(value):
    if isinstance(value, dict):
        yield value
        for item in value.values():
            yield from _walk(item)
    elif isinstance(value, list):
        for item in value:
            yield from _walk(item)


def task_uses_google_drive(task_config):
    for item in _walk(task_config):
        item_type = str(item.get("type") or "").strip().casefold()
        parameters = item.get("parameters") or {}
        platform = str(parameters.get("platform") or "").strip().casefold()
        if item_type in {"googledrive", "googledrive_file"}:
            return True
        if item_type == "login" and platform == "googledrive":
            return True
    return False


def load_google_drive_policy(path=GOOGLE_DRIVE_POLICY_PATH):
    source = Path(path)
    payload = json.loads(source.read_text())
    task_ids = payload.get("task_ids")
    if not isinstance(task_ids, list) or not task_ids:
        raise ValueError(f"{source} must contain a non-empty task_ids list")
    if len(task_ids) != len(set(task_ids)):
        raise ValueError(f"{source} contains duplicate task IDs")
    return {
        **payload,
        "task_ids": frozenset(str(task_id) for task_id in task_ids),
        "path": str(source),
    }


GOOGLE_DRIVE_POLICY = load_google_drive_policy()
GOOGLE_DRIVE_TASK_IDS = GOOGLE_DRIVE_POLICY["task_ids"]
FORMAL_TASK_SET = json.loads(FORMAL_TASK_SET_PATH.read_text())


def task_ids_sha256(task_ids):
    payload = "\n".join(sorted(set(task_ids))).encode("utf-8")
    return hashlib.sha256(payload).hexdigest()


def task_configs_record(examples_root, task_ids):
    root = Path(examples_root)
    selected = sorted(set(str(task_id) for task_id in task_ids))
    by_id = {}
    for path in root.glob("*/*.json"):
        payload = json.loads(path.read_text(encoding="utf-8"))
        task_id = str(payload.get("id") or "")
        if task_id in by_id:
            raise ValueError(f"duplicate task config ID: {task_id}")
        by_id[task_id] = path

    missing = [task_id for task_id in selected if task_id not in by_id]
    if missing:
        raise ValueError(
            "missing task configs for selected IDs: " + ", ".join(missing)
        )

    digest = hashlib.sha256()
    entries = []
    for task_id in selected:
        path = by_id[task_id]
        raw = path.read_bytes()
        relative_path = path.relative_to(root).as_posix()
        file_sha256 = hashlib.sha256(raw).hexdigest()
        digest.update(task_id.encode("utf-8"))
        digest.update(b"\0")
        digest.update(relative_path.encode("utf-8"))
        digest.update(b"\0")
        digest.update(file_sha256.encode("ascii"))
        digest.update(b"\n")
        entries.append(
            {
                "task_id": task_id,
                "path": relative_path,
                "sha256": file_sha256,
            }
        )
    return {
        "version": 1,
        "algorithm": "sha256(task_id NUL relative_path NUL file_sha256 LF)",
        "task_config_count": len(entries),
        "task_configs_sha256": digest.hexdigest(),
        "entries": entries,
    }


def assert_formal_full_task_set(task_ids):
    task_ids = set(task_ids)
    excluded = sorted(task_ids & GOOGLE_DRIVE_TASK_IDS)
    if excluded:
        raise ValueError(
            "formal task set contains permanently excluded Google Drive "
            "tasks: " + ", ".join(excluded)
        )
    expected_count = FORMAL_TASK_SET["expected_task_count"]
    expected_digest = FORMAL_TASK_SET["expected_task_ids_sha256"]
    actual_count = len(task_ids)
    actual_digest = task_ids_sha256(task_ids)
    if actual_count != expected_count or actual_digest != expected_digest:
        raise ValueError(
            "formal task-set drift: "
            f"expected {expected_count}/{expected_digest}, "
            f"found {actual_count}/{actual_digest}"
        )


def task_set_record(task_ids, excluded_task_ids):
    selected = sorted(set(task_ids))
    excluded = sorted(set(excluded_task_ids))
    return {
        "version": 1,
        "policy": "FORMAL_NO_GOOGLE_DRIVE",
        "selected_task_count": len(selected),
        "selected_task_ids_sha256": task_ids_sha256(selected),
        "excluded_task_count": len(excluded),
        "excluded_task_ids": excluded,
        "google_drive_policy": str(
            GOOGLE_DRIVE_POLICY_PATH.relative_to(ROOT)
        ),
        "google_drive_policy_sha256": hashlib.sha256(
            GOOGLE_DRIVE_POLICY_PATH.read_bytes()
        ).hexdigest(),
        "formal_task_set": str(FORMAL_TASK_SET_PATH.relative_to(ROOT)),
        "formal_task_set_sha256": hashlib.sha256(
            FORMAL_TASK_SET_PATH.read_bytes()
        ).hexdigest(),
        "is_formal_full_task_set": (
            len(selected) == FORMAL_TASK_SET["expected_task_count"]
            and task_ids_sha256(selected)
            == FORMAL_TASK_SET["expected_task_ids_sha256"]
        ),
    }


def policy_exclusion(task_id, task_config):
    listed = task_id in GOOGLE_DRIVE_TASK_IDS
    detected = task_uses_google_drive(task_config)
    if listed != detected:
        raise ValueError(
            "Google Drive exclusion manifest/config drift for "
            f"{task_id}: listed={listed} detected={detected}"
        )
    if listed:
        return {
            "policy": GOOGLE_DRIVE_POLICY["policy"],
            "reason": GOOGLE_DRIVE_POLICY["reason"],
            "manifest": GOOGLE_DRIVE_POLICY["path"],
        }
    return None


def assert_task_allowed(task_id, task_config):
    exclusion = policy_exclusion(task_id, task_config)
    if exclusion:
        raise ValueError(
            f"task {task_id} is excluded by {exclusion['policy']}; "
            "Google Drive tasks are not part of formal hybrid evaluation"
        )
