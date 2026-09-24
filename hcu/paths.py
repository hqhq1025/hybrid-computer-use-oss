"""Filesystem locations shared by the harness entry points.

Every location can be overridden through the environment so that the
repository, the OSWorld checkout, the agent artifacts and the results can
live on different volumes.
"""

from __future__ import annotations

import os
from pathlib import Path

REPO_ROOT = Path(
    os.environ.get(
        "HYBRID_COMPUTER_USE_ROOT",
        Path(__file__).resolve().parents[1],
    )
).expanduser().resolve()
OSWORLD_ROOT = Path(
    os.environ.get("OSWORLD_V2_ROOT", REPO_ROOT / "external" / "OSWorld-V2")
).expanduser().resolve()
DEFAULT_ARTIFACT_DIR = Path(
    os.environ.get("HCU_ARTIFACT_DIR", REPO_ROOT / "artifacts")
).expanduser()
DEFAULT_SKILL_FILE = REPO_ROOT / "skills/hybrid/SKILL.md"
DEFAULT_BASE_INSTRUCTIONS_FILE = (
    REPO_ROOT / "skills/base/CODEX_BASE_INSTRUCTIONS.md"
)


def results_root() -> Path:
    return Path(
        os.environ.get("HCU_RESULTS_DIR", REPO_ROOT / "runs")
    ).expanduser().resolve()


def anchor_path_args(args, *names: str) -> None:
    """Make path arguments absolute before an entry point changes directory.

    Entry points switch into the OSWorld checkout because upstream code uses
    checkout-relative paths; user-supplied relative paths must keep meaning
    "relative to where the command was started".
    """
    for name in names:
        value = getattr(args, name)
        if not value:
            continue
        absolute = os.path.abspath(os.path.expanduser(os.fspath(value)))
        setattr(args, name, type(value)(absolute) if isinstance(value, Path) else absolute)
