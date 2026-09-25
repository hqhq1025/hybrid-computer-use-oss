from __future__ import annotations

from collections.abc import Callable
import traceback
from typing import Any


def run_official_evaluator(evaluate: Callable[[], Any]) -> dict:
    """Run the official OSWorld evaluator once and record the outcome.

    An evaluator exception, including a result without a numeric score, is
    recorded instead of raised, so the run still seals an explicit RAW
    error and the result is excluded from scoring rather than lost.
    """
    try:
        result = evaluate()
        payload = dict(result) if isinstance(result, dict) else {"score": result}
        payload["score"] = float(payload.get("score", 0.0))
    except Exception as exc:
        return {
            "status": "error",
            "result": None,
            "error": f"{type(exc).__name__}: {exc}",
            "traceback": traceback.format_exc(),
        }
    return {
        "status": "completed",
        "result": payload,
        "error": None,
        "traceback": None,
    }
