import hashlib
import importlib.util
import json
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def digest(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def test_release_and_run_configuration_are_bound():
    release = json.loads(
        (ROOT / "releases/v0.14/release.json").read_text(encoding="utf-8")
    )
    assert release["tool_version"] == "v0.14"
    assert release["contract"]["public_tools"] == ["AX", "GUI"]
    assert digest(ROOT / "skills/hybrid/SKILL.md") == (
        "61777e8101103cba07be3eacf26fc745a6c5bc94cbd1707ca194050f1f993d16"
    )


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
        assert len(config["binary_sha256"]) == 64
        assert config["archive_url"].startswith("https://github.com/")


def test_runner_defaults_bind_pinned_agent_hashes():
    source = (
        ROOT / "integrations/osworld-v2/hybrid/run_hybrid.py"
    ).read_text(encoding="utf-8")
    lock = json.loads(
        (ROOT / "configs/agent-artifacts.lock.json").read_text(encoding="utf-8")
    )
    assert "DEFAULT_CODEX_SHA256" in source
    assert "DEFAULT_CLAUDE_CODE_SHA256" in source
    assert lock["agents"]["codex"]["binary_sha256"] in source or (
        '["codex"]["binary_sha256"]' in source
    )
