#!/usr/bin/env python3
from __future__ import annotations

import hashlib
import json
import shutil
import stat
import tarfile
import tempfile
import urllib.request
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
LOCK_FILE = ROOT / "configs/agent-artifacts.lock.json"


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def install_agent(name: str, config: dict, temp_dir: Path) -> None:
    archive = temp_dir / config["archive"]
    urllib.request.urlretrieve(config["archive_url"], archive)
    actual_archive = sha256(archive)
    if actual_archive != config["archive_sha256"]:
        raise RuntimeError(
            f"{name} archive SHA-256 mismatch: "
            f"expected {config['archive_sha256']}, got {actual_archive}"
        )

    with tarfile.open(archive, "r:gz") as bundle:
        member = bundle.getmember(config["archive_member"])
        source = bundle.extractfile(member)
        if source is None:
            raise RuntimeError(f"{name} archive member is not a file")
        target = ROOT / config["artifact_path"]
        target.parent.mkdir(parents=True, exist_ok=True)
        with target.open("wb") as output:
            shutil.copyfileobj(source, output)
        target.chmod(
            target.stat().st_mode
            | stat.S_IXUSR
            | stat.S_IXGRP
            | stat.S_IXOTH
        )
    print(f"{name}: {config['version']} -> {target}")


def main() -> int:
    lock = json.loads(LOCK_FILE.read_text(encoding="utf-8"))
    with tempfile.TemporaryDirectory(prefix="hcu-agent-artifacts-") as directory:
        temp_dir = Path(directory)
        for name, config in lock["agents"].items():
            install_agent(name, config, temp_dir)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
