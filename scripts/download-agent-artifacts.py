#!/usr/bin/env python3
"""Download the agent CLIs pinned in configs/agent-artifacts.lock.json.

Each archive is fetched from its publisher and checked against the recorded
SHA-256. An entry with ``archive_member`` is extracted to its artifact path;
an entry without one is kept as the archive itself (the Gemini CLI npm
tarball, unpacked in the guest). Grok Build's member is Brotli-compressed and
is decoded with the pinned Node runtime, so runtimes are installed first.
"""

from __future__ import annotations

import hashlib
import json
import shutil
import stat
import subprocess
import tarfile
import tempfile
import urllib.request
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
LOCK_FILE = ROOT / "configs/agent-artifacts.lock.json"
EXECUTABLE = stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH
BROTLI_DECODE = (
    "const fs = require('fs');"
    "const zlib = require('zlib');"
    "fs.writeFileSync(process.argv[2],"
    " zlib.brotliDecompressSync(fs.readFileSync(process.argv[1])));"
)


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def install(name: str, config: dict, temp_dir: Path, node: Path | None) -> Path:
    archive = temp_dir / config["archive"]
    urllib.request.urlretrieve(config["archive_url"], archive)
    actual = sha256(archive)
    if actual != config["archive_sha256"]:
        raise RuntimeError(
            f"{name} archive SHA-256 mismatch: "
            f"expected {config['archive_sha256']}, got {actual}"
        )
    target = ROOT / config["artifact_path"]
    target.parent.mkdir(parents=True, exist_ok=True)
    if "archive_member" not in config:
        shutil.copyfile(archive, target)
        print(f"{name}: {config['version']} -> {target}")
        return target

    member = temp_dir / f"{name}.member"
    with tarfile.open(archive, "r:*") as bundle:
        source = bundle.extractfile(bundle.getmember(config["archive_member"]))
        if source is None:
            raise RuntimeError(f"{name} archive member is not a file")
        with member.open("wb") as output:
            shutil.copyfileobj(source, output)
    if config.get("brotli"):
        if node is None:
            raise RuntimeError(f"{name} needs the node runtime to decode Brotli")
        subprocess.run(
            [str(node), "-e", BROTLI_DECODE, str(member), str(target)],
            check=True,
        )
    else:
        shutil.move(member, target)
    target.chmod(target.stat().st_mode | EXECUTABLE)
    print(f"{name}: {config['version']} -> {target}")
    return target


def main() -> int:
    lock = json.loads(LOCK_FILE.read_text(encoding="utf-8"))
    with tempfile.TemporaryDirectory(prefix="hcu-agent-artifacts-") as directory:
        temp_dir = Path(directory)
        runtimes = {
            name: install(name, config, temp_dir, None)
            for name, config in lock["runtimes"].items()
        }
        for name, config in lock["agents"].items():
            install(name, config, temp_dir, runtimes.get("node"))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
