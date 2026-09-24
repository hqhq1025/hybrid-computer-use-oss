"""Helpers that write the per-task result directory."""

from __future__ import annotations

from pathlib import Path


def write_local(path: Path, content: str) -> None:
    path.write_text(content, encoding="utf-8")
