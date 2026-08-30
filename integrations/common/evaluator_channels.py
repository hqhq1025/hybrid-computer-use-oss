from __future__ import annotations

from collections.abc import Callable
from typing import Any


def evaluate_official_only(
    *,
    task_id: str,
    evaluate: Callable[[], Any],
    installer: Callable[[str], dict] | None = None,
) -> dict:
    result = evaluate()
    payload = (
        dict(result)
        if isinstance(result, dict)
        else {"score": float(result)}
    )
    installation = (
        installer(task_id)
        if installer is not None
        else {
            "installed": False,
            "task_id": task_id,
            "reason": "not_packaged_in_oss",
            "modules": [],
        }
    )
    return {
        "official": {
            "status": "completed",
            "result": payload,
            "error": None,
            "traceback": None,
        },
        "audited": None,
        "override_installation": installation,
        "official_score": float(payload.get("score", 0.0)),
        "audited_score": None,
        "official_score_replaced": False,
    }
