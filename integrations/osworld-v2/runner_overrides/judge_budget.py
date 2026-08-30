from __future__ import annotations

import json
from pathlib import Path
from typing import Any, Iterable


TOOL_ITEM_TYPES = {
    "command_execution",
    "file_change",
    "local_shell_call",
    "mcp_tool_call",
    "web_search",
}


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
            and item.get("type") in TOOL_ITEM_TYPES
        ):
            events.append(event)
    return events


def count_completed_tools(path: Path) -> int:
    if not path.is_file():
        return 0
    with path.open(encoding="utf-8", errors="replace") as source:
        return len(completed_tool_events(source))


def tool_budget_state(
    path: Path,
    *,
    hard_limit: int = 30,
    finalization_reserve: int = 4,
) -> dict[str, int | bool]:
    if hard_limit <= 0:
        raise ValueError("hard_limit must be positive")
    if finalization_reserve < 0 or finalization_reserve >= hard_limit:
        raise ValueError(
            "finalization_reserve must be non-negative and below hard_limit"
        )
    completed = count_completed_tools(path)
    return {
        "completed_tools": completed,
        "hard_limit": hard_limit,
        "finalization_at": hard_limit - finalization_reserve,
        "request_finalization": completed >= hard_limit - finalization_reserve,
        "exhausted": completed >= hard_limit,
        "remaining": max(0, hard_limit - completed),
    }
