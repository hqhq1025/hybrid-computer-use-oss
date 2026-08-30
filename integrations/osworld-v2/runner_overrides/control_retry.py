from __future__ import annotations

import time
from collections.abc import Callable
from typing import Any, TypeVar


T = TypeVar("T")


def bounded_control_retry(
    operation: Callable[[], T],
    *,
    attempts: int = 3,
    initial_backoff_seconds: float = 0.5,
    retryable: Callable[[Exception], bool] | None = None,
    sleep: Callable[[float], Any] = time.sleep,
) -> tuple[T, dict[str, Any]]:
    if attempts < 1:
        raise ValueError("attempts must be positive")
    if initial_backoff_seconds < 0:
        raise ValueError("initial_backoff_seconds must be non-negative")
    should_retry = retryable or (lambda _exc: True)
    failures = []
    for attempt in range(1, attempts + 1):
        try:
            return operation(), {
                "attempts": attempt,
                "retry_assisted": attempt > 1,
                "failures": failures,
            }
        except Exception as exc:
            failure = f"{type(exc).__name__}: {exc}"
            failures.append(failure)
            if attempt >= attempts or not should_retry(exc):
                exc.retry_evidence = {
                    "attempts": attempt,
                    "retry_assisted": attempt > 1,
                    "failures": list(failures),
                }
                raise
            delay = initial_backoff_seconds * (2 ** (attempt - 1))
            if delay:
                sleep(delay)
    raise AssertionError("unreachable")
