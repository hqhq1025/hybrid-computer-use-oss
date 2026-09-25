import io
from pathlib import Path
import subprocess
import tarfile


ROOT = Path(__file__).resolve().parents[1]


def test_git_archive_excludes_local_payloads_even_when_tracked(tmp_path):
    def git(*args):
        return subprocess.run(
            ["git", *args], cwd=tmp_path, check=True, capture_output=True
        ).stdout

    git("init", "--quiet")
    (tmp_path / ".gitattributes").write_bytes((ROOT / ".gitattributes").read_bytes())
    fixture_paths = tuple(
        str(path.relative_to(ROOT))
        for path in sorted((ROOT / "computer-use/testdata").rglob("*"))
        if path.is_file()
    )
    assert fixture_paths
    source_paths = (
        "README.md", "LICENSE", "THIRD_PARTY_NOTICES.md", "VERSION",
        "computer-use/main.go", "hcu/osworld/run_queue.py",
        "computer-use/native/macos/Package.swift",
        "computer-use/native/windows/ComputerUse.Windows.Runtime/ProviderServer.cs",
        "computer-use/vendor/modules.txt",
        "computer-use/vendor/golang.org/x/text/LICENSE",
        "computer-use/vendor/golang.org/x/text/PATENTS",
        "computer-use/vendor/golang.org/x/text/cases/cases.go",
        "configs/agent-artifacts.lock.json", "skills/hybrid/SKILL.md",
        *fixture_paths,
    )
    excluded_paths = (
        "artifacts/.gitkeep", "artifacts/claude", "artifacts/codex",
        "artifacts/gemini-cli.tgz", "artifacts/grok", "artifacts/node",
        "external/OSWorld-V2/desktop_env/__init__.py", "runs/task/result.json",
        ".venv/bin/python", ".pytest_cache/state", ".env",
        "hcu/__pycache__/paths.cpython-310.pyc", "leftover.pyc",
        "debug.log", "guest.qcow2", "guest.qcow2.sha256",
        "computer-use/computer-use",
        "computer-use/computer-use.exe",
        "computer-use/native/macos/.build/debug/provider",
        "computer-use/native/windows/ComputerUse.Windows.Runtime/obj/project.assets.json",
        "computer-use/native/windows/ComputerUse.Windows.Runtime/bin/Release/provider.exe",
    )
    for name in source_paths + excluded_paths:
        path = tmp_path / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("synthetic archive fixture\n")
    # Archive an index tree, so the test creates no commits or history.
    git("add", "--all")
    tree = git("write-tree").decode().strip()
    (tmp_path / "untracked-secret.txt").write_text("not part of the tree\n")
    (tmp_path / "README.md").write_text("uncommitted edit\n")
    archive = git("archive", "--format=tar", "--prefix=hybrid-computer-use/", tree)
    with tarfile.open(fileobj=io.BytesIO(archive)) as bundle:
        files = {
            member.name.removeprefix("hybrid-computer-use/")
            for member in bundle.getmembers() if member.isfile()
        }
        assert files == {".gitattributes", *source_paths}
        assert not any(".git" in Path(member.name).parts for member in bundle)
        assert bundle.extractfile("hybrid-computer-use/README.md").read() == (
            b"synthetic archive fixture\n"
        )
