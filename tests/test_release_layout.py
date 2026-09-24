import json
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def test_no_private_absolute_paths_in_public_entrypoints():
    paths = [
        ROOT / "README.md",
        ROOT / "docs/OSWORLD_V1.md",
        ROOT / "docs/OSWORLD_V2.md",
        ROOT / "integrations/osworld-v2/hybrid/run_hybrid.py",
        ROOT / "scripts/install-osworld-v2.py",
    ]
    for path in paths:
        assert "/datadrive2/" not in path.read_text(encoding="utf-8")


def test_agent_artifact_lock_is_complete():
    lock = json.loads(
        (ROOT / "configs/agent-artifacts.lock.json").read_text(encoding="utf-8")
    )
    assert lock["platform"] == "linux-x86_64"
    assert set(lock["agents"]) == {"codex", "claude_code"}
    for config in lock["agents"].values():
        assert len(config["archive_sha256"]) == 64
        assert config["archive_url"].startswith("https://github.com/")
