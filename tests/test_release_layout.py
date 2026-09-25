import json
from pathlib import Path
import subprocess

import pytest


ROOT = Path(__file__).resolve().parents[1]
# Host paths and addresses of the machines this project was developed on.
PRIVATE_MARKERS = ("/datadrive", "/home/v-", "172.28.", "10.1.0.", "20.225.")


def tracked_text_files():
    try:
        listing = subprocess.run(
            ["git", "ls-files", "-z"],
            cwd=ROOT,
            capture_output=True,
            check=True,
        ).stdout
    except (FileNotFoundError, subprocess.CalledProcessError):
        pytest.skip("not a git checkout")
    for name in listing.decode().split("\0"):
        if not name or name == "tests/test_release_layout.py":
            continue
        try:
            yield name, (ROOT / name).read_text(encoding="utf-8")
        except (FileNotFoundError, UnicodeDecodeError):
            continue


def test_tracked_files_contain_no_private_hosts_or_paths():
    leaks = [
        (name, marker)
        for name, text in tracked_text_files()
        for marker in PRIVATE_MARKERS
        if marker in text
    ]
    assert leaks == []


def test_agent_artifact_lock_is_complete():
    lock = json.loads(
        (ROOT / "configs/agent-artifacts.lock.json").read_text(encoding="utf-8")
    )
    assert lock["platform"] == "linux-x86_64"
    assert set(lock["agents"]) == {"codex", "claude_code", "gemini_cli", "grok_build"}
    assert set(lock["runtimes"]) == {"node"}
    for config in [*lock["agents"].values(), *lock["runtimes"].values()]:
        assert len(config["archive_sha256"]) == 64
        assert config["archive_url"].startswith("https://")
        assert config["artifact_path"].startswith("artifacts/")
