"""Sanitized execution evidence shared with the Judge.

The Judge sees completed tool events only: reasoning, images and events that
mention evaluator or gold-data locations are removed before upload.
"""

from __future__ import annotations

import json
import re
from pathlib import Path
from typing import Any, Iterable


LIVE_JUDGE_TRACE_EVIDENCE_ITEM_TYPES = {
    "command_execution",
    "file_change",
    "local_shell_call",
    "mcp_tool_call",
    "web_search",
}
LIVE_JUDGE_FORBIDDEN_TRACE_MARKERS = (
    "/evaluation_examples/",
    "/evaluator",
    "evaluation_examples",
    "evaluator.py",
    "gold/reference",
    "gold_artifact",
    "task_config",
)


PRIOR_JUDGE_COMMAND_MAX_CHARS = 2000
PRIOR_JUDGE_OUTPUT_MAX_CHARS = 6000


def completed_tool_events(lines: Iterable[str]) -> list[dict[str, Any]]:
    events = []
    for line in lines:
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        item = event.get("item")
        if (
            event.get("type") == "item.completed"
            and isinstance(item, dict)
            and item.get("type") in LIVE_JUDGE_TRACE_EVIDENCE_ITEM_TYPES
        ):
            events.append(event)
    return events


def build_live_judge_trace_evidence(
    trace_path: Path,
    output_path: Path,
) -> int:
    """Write completed execution events without reasoning or image payloads."""
    retained = 0
    with output_path.open("w", encoding="utf-8") as output:
        for line in trace_path.read_text(errors="replace").splitlines():
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                continue
            if event.get("type") != "item.completed":
                continue
            item = event.get("item") or {}
            if item.get("type") not in LIVE_JUDGE_TRACE_EVIDENCE_ITEM_TYPES:
                continue
            serialized = json.dumps(item, ensure_ascii=False).casefold()
            if any(
                marker in serialized
                for marker in LIVE_JUDGE_FORBIDDEN_TRACE_MARKERS
            ):
                continue

            sanitized = json.loads(json.dumps(event))
            result = (sanitized.get("item") or {}).get("result")
            if isinstance(result, dict) and isinstance(
                result.get("content"), list
            ):
                content = []
                for entry in result["content"]:
                    if not isinstance(entry, dict):
                        continue
                    if entry.get("type") == "text":
                        content.append(entry)
                    elif entry.get("type") == "image":
                        content.append({
                            "type": "image",
                            "mimeType": (
                                entry.get("mimeType")
                                or entry.get("mime_type")
                            ),
                            "omitted_from_judge_evidence": True,
                        })
                result["content"] = content
            output.write(json.dumps(sanitized, ensure_ascii=False) + "\n")
            retained += 1
    return retained


def _bounded_evidence_text(value: object, limit: int) -> str:
    text = str(value or "")
    if len(text) <= limit:
        return text
    half = max(1, (limit - 80) // 2)
    omitted = len(text) - (half * 2)
    return (
        text[:half]
        + f"\n...[{omitted} chars omitted from prior Judge evidence]...\n"
        + text[-half:]
    )


def build_prior_judge_trace_evidence(
    trace_path: Path,
    output_path: Path,
) -> int:
    """Write a bounded, reasoning-free ledger of completed Judge tools."""
    retained = 0
    with output_path.open("w", encoding="utf-8") as output:
        for line in trace_path.read_text(errors="replace").splitlines():
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                continue
            if event.get("type") != "item.completed":
                continue
            item = event.get("item") or {}
            if item.get("type") not in LIVE_JUDGE_TRACE_EVIDENCE_ITEM_TYPES:
                continue
            serialized = json.dumps(item, ensure_ascii=False).casefold()
            if any(
                marker in serialized
                for marker in LIVE_JUDGE_FORBIDDEN_TRACE_MARKERS
            ):
                continue

            compact_item = {
                key: item[key]
                for key in ("id", "type", "tool", "exit_code", "status")
                if key in item
            }
            if "command" in item:
                compact_item["command"] = _bounded_evidence_text(
                    item["command"],
                    PRIOR_JUDGE_COMMAND_MAX_CHARS,
                )
            if "arguments" in item:
                arguments = item["arguments"]
                if not isinstance(arguments, str):
                    arguments = json.dumps(
                        arguments,
                        ensure_ascii=False,
                        sort_keys=True,
                    )
                compact_item["arguments"] = _bounded_evidence_text(
                    arguments,
                    PRIOR_JUDGE_COMMAND_MAX_CHARS,
                )
            for key in ("aggregated_output", "output", "text"):
                if key in item:
                    compact_item[f"{key}_excerpt"] = (
                        _bounded_evidence_text(
                            item[key],
                            PRIOR_JUDGE_OUTPUT_MAX_CHARS,
                        )
                    )
            if "result" in item:
                compact_item["result_excerpt"] = _bounded_evidence_text(
                    json.dumps(
                        item["result"],
                        ensure_ascii=False,
                        sort_keys=True,
                    ),
                    PRIOR_JUDGE_OUTPUT_MAX_CHARS,
                )
            output.write(json.dumps(
                {"type": "item.completed", "item": compact_item},
                ensure_ascii=False,
            ) + "\n")
            retained += 1
    return retained


def live_judge_mutation_audit(events_text: str) -> dict:
    mutating_mcp_tools = {
        "click", "double_click", "right_click", "drag", "gui", "hotkey",
        "key", "press_key", "scroll", "set_value", "type_text",
    }
    command_patterns = (
        r"(^|[;&|]\s*)(rm|mv|cp|touch|mkdir|rmdir|chmod|chown|ln|"
        r"truncate|dd|install)\b",
        r"(^|[^<])>>?",
        r"\bsed\s+-i\b",
        r"\btee\b",
        r"\b(os\.(remove|unlink|rename|replace|mkdir|makedirs)|"
        r"shutil\.(copy|copy2|move|rmtree)|"
        r"\.(write_text|write_bytes|write)\s*\()",
        r"\b(open|zipfile\.ZipFile)\s*\([^)]*,\s*['\"](?:w|a|x)",
    )
    mutating_mcp_calls = []
    suspicious_commands = []
    for line in (events_text or "").splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if event.get("type") != "item.completed":
            continue
        item = event.get("item") or {}
        item_type = item.get("type")
        if item_type == "command_execution":
            command = str(item.get("command") or "")
            if any(
                re.search(pattern, command, flags=re.IGNORECASE)
                for pattern in command_patterns
            ):
                suspicious_commands.append(command[:1000])
            continue
        if item_type != "mcp_tool_call":
            continue
        tool = str(item.get("tool") or "").rsplit(".", 1)[-1].lower()
        arguments = item.get("arguments")
        if isinstance(arguments, str):
            try:
                arguments = json.loads(arguments)
            except json.JSONDecodeError:
                arguments = {}
        if tool == "gui":
            actions = (
                arguments.get("actions", [])
                if isinstance(arguments, dict)
                else []
            )
            mutating_mcp_calls.extend(
                f"gui:{str(action.get('type') or '').lower()}"
                for action in actions
                if isinstance(action, dict)
                and str(action.get("type") or "").lower()
                not in {"", "screenshot", "wait"}
            )
        elif tool in {"ax", "a11y"}:
            if (
                isinstance(arguments, dict)
                and str(arguments.get("mode") or "").lower() == "act"
            ):
                mutating_mcp_calls.extend(
                    f"ax:{str(action.get('type') or '').lower()}"
                    for action in arguments.get("actions") or []
                    if isinstance(action, dict)
                )
        elif tool in mutating_mcp_tools:
            mutating_mcp_calls.append(tool)
    return {
        "mutating_mcp_calls": mutating_mcp_calls,
        "mutation_capable_mcp_used": bool(mutating_mcp_calls),
        "suspicious_commands": suspicious_commands,
        "mutation_capable_command_used": bool(suspicious_commands),
    }


def nonvolatile_judge_changes(integrity: dict) -> list[str]:
    volatile_prefixes = (
        ".chrome-osworld/",
        ".config/google-chrome/",
    )
    return [
        path
        for path in (
            integrity.get("added_files", [])
            + integrity.get("removed_files", [])
            + integrity.get("changed_files", [])
        )
        if not path.startswith(volatile_prefixes)
    ]
