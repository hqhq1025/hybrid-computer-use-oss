#!/usr/bin/env python3
from __future__ import annotations

import hashlib
import json
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def tree_sha256(root: Path) -> str:
    rows = []
    for path in sorted(root.rglob("*")):
        if not path.is_file():
            continue
        if "__pycache__" in path.parts or path.suffix == ".pyc":
            continue
        rows.append(f"{sha256(path)}  ./{path.relative_to(root).as_posix()}\n")
    return hashlib.sha256("".join(rows).encode()).hexdigest()


def main() -> int:
    release = json.loads(
        (ROOT / "releases/v0.14/release.json").read_text(encoding="utf-8")
    )
    expected_tree = release["implementation"]["source"]["tree_sha256"]
    actual_tree = tree_sha256(ROOT / "computer-use")
    if actual_tree != expected_tree:
        raise SystemExit(
            f"source tree mismatch: expected {expected_tree}, got {actual_tree}"
        )

    expected = {
        ROOT / "skills/hybrid/SKILL.md":
            "61777e8101103cba07be3eacf26fc745a6c5bc94cbd1707ca194050f1f993d16",
        ROOT / "skills/ax-only/SKILL.md":
            "74c9f917d2e834d6bc564e7e82d54410ec9055992cabdd7ea07c86eb462d3d2a",
        ROOT / "skills/gui-only/SKILL.md":
            "ab300d8edcb704e6094bea3ec4db82fef30b7835f0143fa5556ff6550e4d8a99",
        ROOT / "skills/base/CODEX_BASE_INSTRUCTIONS.md":
            "2db42251f92d78881226342d36eb7a269f58cc63442915e2fa224dcbe14346af",
    }
    for path, digest in expected.items():
        actual = sha256(path)
        if actual != digest:
            raise SystemExit(f"{path}: expected {digest}, got {actual}")

    binary = ROOT / "artifacts/releases/v0.14/computer-use"
    if binary.is_file():
        expected_binary = release["implementation"]["mcp_binary"]["sha256"]
        actual_binary = sha256(binary)
        if actual_binary != expected_binary:
            raise SystemExit(
                f"release binary mismatch: expected {expected_binary}, "
                f"got {actual_binary}"
            )
    print("v0.14 source and prompt artifacts verified")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
