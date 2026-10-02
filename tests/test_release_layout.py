import json
from pathlib import Path
import subprocess
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parents[1]


def tracked_text_files():
    try:
        if not (ROOT / ".git").exists():
            raise FileNotFoundError("source archive has no Git metadata")
        listing = subprocess.run(
            ["git", "ls-files", "--cached", "--others", "--exclude-standard", "-z"],
            cwd=ROOT,
            capture_output=True,
            check=True,
        ).stdout
        names = listing.decode().split("\0")
    except (FileNotFoundError, subprocess.CalledProcessError):
        # The delivered source archive has no Git metadata.
        names = [
            str(path.relative_to(ROOT)) for path in ROOT.rglob("*")
            if path.is_file() and not any(
                part in {"artifacts", "external", "runs", ".venv", "__pycache__", ".pytest_cache"}
                for part in path.relative_to(ROOT).parts
            )
        ]
    for name in names:
        if not name or name == "tests/test_release_layout.py":
            continue
        try:
            yield name, (ROOT / name).read_text(encoding="utf-8")
        except (FileNotFoundError, UnicodeDecodeError):
            continue


def test_agent_artifact_lock_is_complete():
    lock = json.loads(
        (ROOT / "configs/agent-artifacts.lock.json").read_text(encoding="utf-8")
    )
    assert lock["platform"] == "linux-x86_64"
    assert set(lock["agents"]) == {"codex", "claude_code", "gemini_cli", "grok_build"}
    assert set(lock["runtimes"]) == {"node"}
    for config in [*lock["agents"].values(), *lock["runtimes"].values()]:
        digest = config["archive_sha256"]
        assert len(digest) == 64
        assert all(character in "0123456789abcdef" for character in digest)
        url = urlsplit(config["archive_url"])
        assert url.scheme == "https"
        assert url.hostname in {"github.com", "registry.npmjs.org", "nodejs.org"}
        assert config["version"] in url.path
        assert url.path.endswith("/" + config["archive"])
        assert config["artifact_path"].startswith("artifacts/")


# Supported agents and example configurations for each benchmark version.
EXPECTED_AGENTS = ("codex", "claude-code", "gemini-cli", "grok-build")
EXAMPLE_AGENTS = {
    "v1": ("codex", "claude-code", "gemini-cli", "grok-build"),
    "v2": ("codex", "claude-code"),
}


def test_environment_template_defines_only_variables_that_are_used():
    """Every variable in the template must be referenced somewhere, so an
    operator who edits it sees an effect. A reference may be code reading the
    variable, an example config expanding it, or another template line
    deriving a value from it."""
    template_lines = (ROOT / "configs/env.example").read_text(
        encoding="utf-8"
    ).splitlines()
    declared = [
        line.split("=", 1)[0].removeprefix("export ").strip()
        for line in template_lines
        if line.startswith("export ")
    ]
    others = [
        text
        for name, text in tracked_text_files()
        if not name.startswith("configs/env")
    ]
    unread = []
    for name in declared:
        # Its own declaration does not count as a use.
        rest = "\n".join(
            line for line in template_lines
            if not line.startswith(f"export {name}=")
        )
        if not (name in rest or any(name in text for text in others)):
            unread.append(name)
    assert unread == [], f"env.example declares unused variables: {unread}"


def test_harness_matrix_matches_the_runner_and_the_examples():
    """The four agents share one profile table; the example queues and the
    agent-profile page must agree with it."""
    from hcu.osworld.agents import AGENT_PROFILES

    assert tuple(AGENT_PROFILES) == EXPECTED_AGENTS

    examples = {}
    for path in (ROOT / "configs").glob("osworld-*-queue.example.json"):
        config = json.loads(path.read_text(encoding="utf-8"))
        examples.setdefault(config["benchmark_version"], set()).add(config["agent"])
    assert {version: sorted(agents) for version, agents in examples.items()} == {
        version: sorted(agents) for version, agents in EXAMPLE_AGENTS.items()
    }

    profiles = (ROOT / "docs/AGENT_PROFILES.md").read_text(encoding="utf-8")
    for agent in EXPECTED_AGENTS:
        assert f"`{agent}`" in profiles, f"{agent} is missing from AGENT_PROFILES.md"


def test_documented_guest_cleanup_covers_every_harness():
    """The isolation page must list the state the runner actually removes."""
    runtime = (ROOT / "hcu/osworld/runtime.py").read_text(encoding="utf-8")
    isolation = (ROOT / "docs/ISOLATION.md").read_text(encoding="utf-8")
    for state in (".codex", ".claude", ".gemini", ".grok", ".mcp.json",
                  "AGENTS.md", "CLAUDE.md", "GEMINI.md"):
        assert state in runtime, f"{state} is not cleaned in the guest"
        assert state in isolation, f"{state} is not documented as cleaned"
