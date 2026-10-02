import io
import importlib.util
import json
from pathlib import Path
import subprocess
import stat
import tarfile
import zipfile

import pytest


ROOT = Path(__file__).resolve().parents[1]


def test_tracked_files_contain_no_private_hosts_or_paths():
    from test_release_layout import tracked_text_files

    private_markers = (
        "/" + "datadrive", "/" + "home/v-", "172." + "28.",
        "10." + "1.0.", "20." + "225.",
    )
    leaks = [
        (name, marker)
        for name, text in tracked_text_files()
        for marker in private_markers
        if marker in text
    ]
    assert leaks == []


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
        "configs/agent-artifacts.lock.json", "instructions/base.md",
        "instructions/hybrid.md", "instructions/ax-only.md", "instructions/gui-only.md",
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


@pytest.fixture
def exporter():
    spec = importlib.util.spec_from_file_location(
        "submission_export", ROOT / "scripts/export-submission.py",
    )
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


@pytest.fixture
def export_source(tmp_path, exporter):
    root = tmp_path / "working-tree"
    for name in exporter.REQUIRED:
        path = root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes((ROOT / name).read_bytes())
    return root


@pytest.mark.parametrize("suffix", [".zip", ".tar.gz"])
def test_working_tree_export_includes_untracked_fixes_and_rewrites_imports(
    tmp_path, exporter, export_source, suffix,
):
    root = export_source
    module = "github.com/review-author/private-repository/computer-use"
    (root / "computer-use/go.mod").write_text(f"module {module}\n\ngo 1.22\n")
    source = f'package main\nimport "{module}/internal/test"\n'
    (root / "computer-use/new.go").write_text(source)
    added = {
        "docs/REPRODUCIBILITY.md": "Current revision\n",
        "hcu/osworld/reproduce.py": "# new implementation\n",
        "tests/test_reproduce.py": "# new test\n",
        "configs/reproduction/README.md": "Configuration guide\n",
        "configs/osworld-v1-excluded-tasks.json": '{"excluded":[]}\n',
        "configs/osworld-v1-tasks.json": '{"domain":["public-task-id"]}\n',
        "instructions/hybrid.md": "Uncommitted profile fix\n",
    }
    for name, text in added.items():
        path = root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)
    # A worktree's .git can be a file containing a private absolute gitdir.
    (root / ".git").write_text("gitdir: /private/checkout\n")
    output = tmp_path / f"source{suffix}"
    report = exporter.export(root, output, [])
    unpacked, readback = exporter.unpack(output, tmp_path / "unpacked")
    assert readback == report
    assert set(report) == {"format_version", "version", "module", "files", "package_scope"}
    assert report["module"] == exporter.ANONYMOUS_MODULE
    for name, text in added.items():
        assert (unpacked / name).read_text() == text
    assert (unpacked / "computer-use/new.go").read_text() == source.replace(
        module, exporter.ANONYMOUS_MODULE,
    )
    assert (root / "computer-use/new.go").read_text() == source
    assert not (unpacked / ".git").exists()
    if suffix == ".zip":
        with zipfile.ZipFile(output) as bundle:
            assert all(member.date_time == (1980, 1, 1, 0, 0, 0) for member in bundle.infolist())
            assert all(not member.extra and not member.comment for member in bundle.infolist())
    else:
        with tarfile.open(output) as bundle:
            assert all(member.uid == member.gid == member.mtime == 0 for member in bundle)
            assert all(not member.uname and not member.gname for member in bundle)
    with pytest.raises(FileExistsError):
        exporter.export(root, output, [])
    with pytest.raises(ValueError, match="must not exist"):
        exporter.unpack(output, tmp_path / "unpacked")


def test_export_allowlist_excludes_payloads_and_preserves_legal_text(
    tmp_path, exporter, export_source,
):
    excluded = (
        ".env", ".env.local", ".git/config", "credentials/token.json",
        "runs/result.json", "artifacts/claude-code", "external/task.py",
        "hcu/secrets.json", "hcu/credentials.py", "hcu/__pycache__/module.pyc",
        "computer-use/native/windows/project/obj/private.cs",
        "computer-use/native/macos/.build/private.swift",
        "configs/personal.json", "configs/reproduction/credentials.json",
        "private-notes.md", "dist/source.tar.gz", "CHANGELOG.md", "CONTRIBUTING.md",
        "AGENTS.md", "docs/REVIEW.md", "docs/KNOWN_ISSUES.md", "docs/TEST_RESULTS.md",
        "configs/reproduction/offline.json",
        "configs/reproduction/internal.example.json",
        "configs/reproduction/test-run/config.example.json",
        ".github/workflows/ci.yml", ".gitignore", ".gitattributes",
        "scripts/export-submission.py", "scripts/internal-review.py",
        "tests/test_source_archive.py",
    )
    for name in excluded:
        path = export_source / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("must never ship\n")
    notice = "Copyright review-author\nRequired upstream attribution\n"
    (export_source / "LICENSE").write_text(notice)
    report = exporter.export(export_source, tmp_path / "source.tar.gz", ["review-author"])
    unpacked, _ = exporter.unpack(tmp_path / "source.tar.gz", tmp_path / "unpacked")
    assert not set(excluded) & set(report["files"])
    assert (unpacked / "LICENSE").read_text() == notice
    assert "LICENSE" in report["files"]


@pytest.mark.parametrize("fault", ["host", "credential", "identity", "symlink", "missing"])
def test_export_rejects_sensitive_or_incomplete_source(
    tmp_path, exporter, export_source, fault,
):
    source = export_source / "instructions/hybrid.md"
    if fault == "host":
        source.write_text("/" + "datadrive2/private/work\n")
    elif fault == "credential":
        source.write_text("ghp_" + "a" * 36)
    elif fault == "identity":
        source.write_text("review-author wrote this\n")
    elif fault == "symlink":
        source.unlink()
        source.symlink_to(export_source / "LICENSE")
    else:
        source.unlink()
    output = tmp_path / "source.tar.gz"
    with pytest.raises(ValueError):
        exporter.export(export_source, output, ["review-author"])
    assert not output.exists()


def test_export_detects_concurrent_source_changes(tmp_path, exporter, export_source, monkeypatch):
    original = exporter.source_files
    calls = 0

    def listing(root):
        nonlocal calls
        calls += 1
        if calls == 2:
            (root / "instructions/hybrid.md").write_text("concurrent edit\n")
        return original(root)

    monkeypatch.setattr(exporter, "source_files", listing)
    with pytest.raises(ValueError, match="changed during snapshot"):
        exporter.export(export_source, tmp_path / "source.tar.gz", [])


@pytest.mark.parametrize("separator", [".", "[.]", r"\."])
def test_export_rejects_nonexample_addresses_without_a_host_denylist(
    tmp_path, exporter, export_source, separator,
):
    # Synthetic benchmark-network address, not a real operator endpoint.
    address = separator.join(("198", "18", "0", "7"))
    (export_source / "instructions/hybrid.md").write_text(address)
    output = tmp_path / "source.zip"
    with pytest.raises(ValueError, match="sensitive host"):
        exporter.export(export_source, output, [])
    assert not output.exists()


@pytest.mark.parametrize("address", [
    "127.0.0.1", "0.0.0.0", "172.17.0.1", "192.0.2.5", "198.51.100.9", "203.0.113.3",
])
def test_documentation_and_local_addresses_are_not_host_identity(exporter, address):
    assert not exporter.sensitive_address(address)


def test_browser_version_is_not_an_address(exporter):
    assert not exporter.sensitive_address("Mozilla/5.0 Chrome/120.0.0.0 Safari/537.36")
    assert exporter.sensitive_address("http://" + ".".join(("198", "18", "0", "7")))


def test_export_checks_member_names_for_identity(tmp_path, exporter, export_source):
    (export_source / "instructions/review-author.md").write_text("Neutral content\n")
    with pytest.raises(ValueError, match="identity in file path"):
        exporter.export(export_source, tmp_path / "source.zip", ["review-author"])


@pytest.fixture
def experiment_package(export_source):
    from hcu.osworld.reproduce import prepare

    output = export_source / "configs/reproduction/test-run"
    prepare(
        json.loads((ROOT / "configs/osworld-v1-codex-queue.example.json").read_text()),
        output=output, run_id="test-run", protocol_id="test-protocol",
        protocol_revision="test-revision", root=ROOT,
    )
    return output


def test_experiment_records_stay_local_and_outside_source_attachment(
    tmp_path, exporter, export_source, experiment_package,
):
    for name, text in {
        "summary.json": '{"record_kind":"test_fixture"}\n',
        "environment.json": '{"python":"test_fixture"}\n',
        "tasks.csv": "task_id,status\nexample,not_run\n",
    }.items():
        (experiment_package / name).write_text(text)
    archive = tmp_path / "source.zip"
    report = exporter.export(export_source, archive, [])
    unpacked, _ = exporter.unpack(archive, tmp_path / "unpacked")
    relative = "configs/reproduction/test-run"
    assert report["package_scope"] == "source-and-user-documentation"
    assert not any(name.startswith(relative + "/") for name in report["files"])
    assert not (unpacked / relative).exists()
    assert (experiment_package / "experiment.json").is_file()
    assert (experiment_package / "instructions/profile.md").is_file()
    assert (experiment_package / "tasks.csv").read_text() == "task_id,status\nexample,not_run\n"


def test_lineage_redaction_preserves_upstream_and_copyright(tmp_path, exporter, export_source):
    (export_source / "computer-use/go.mod").write_text(
        "module github.com/review-author/project/computer-use\n",
    )
    copyright_text = "Copyright (c) review-author\nMIT permission text must remain verbatim.\n"
    notice = (
        "The implementation descends from\n"
        "`review-author/project-fork`, itself derived from\n"
        "`upstream/implementation`.\n\n" + copyright_text
    )
    (export_source / "THIRD_PARTY_NOTICES.md").write_text(notice)
    (export_source / "LICENSE").write_text(copyright_text)
    output = tmp_path / "source.zip"
    report = exporter.export(export_source, output, [])
    unpacked, _ = exporter.unpack(output, tmp_path / "unpacked")
    expected = notice.replace("`review-author/project-fork`", "an intermediate project fork")
    assert (unpacked / "THIRD_PARTY_NOTICES.md").read_text() == expected
    assert (unpacked / "LICENSE").read_text() == copyright_text
    assert (export_source / "THIRD_PARTY_NOTICES.md").read_text() == notice
    assert "rewritten_files" not in report
    (export_source / "THIRD_PARTY_NOTICES.md").write_text(notice + "\nMaintained by review-author\n")
    with pytest.raises(ValueError, match="manual attribution review"):
        exporter.export(export_source, tmp_path / "unsafe.zip", [])


@pytest.mark.parametrize("fault", [
    "traversal", "symlink", "duplicate", "extra", "version", "module", "metadata",
])
def test_unpack_rejects_unsafe_or_mismatched_members(
    tmp_path, exporter, export_source, fault,
):
    payload = exporter.snapshot(export_source, [])
    archive = tmp_path / "bad.tar.gz"
    with tarfile.open(archive, "w:gz") as bundle:
        for name, (data, mode) in payload.items():
            if name == "VERSION" and fault == "version":
                data = b"999\n"
            if name == "computer-use/go.mod" and fault == "module":
                data = b"module example.org/wrong\n"
            if name == exporter.MANIFEST and fault == "metadata":
                manifest = json.loads(data)
                manifest["captured_at_utc"] = "2000-01-01T00:00:00Z"
                data = json.dumps(manifest).encode()
            entry = tarfile.TarInfo(f"submission/{name}")
            entry.size, entry.mode = len(data), mode
            bundle.addfile(entry, io.BytesIO(data))
        if fault not in {"version", "module", "metadata"}:
            entry = tarfile.TarInfo({
                "traversal": "submission/../escape.py",
                "symlink": "submission/hcu/escape.py",
                "duplicate": "submission/VERSION",
                "extra": "submission/artifacts/agent",
            }[fault])
            if fault == "symlink":
                entry.type = tarfile.SYMTYPE
                entry.linkname = "/etc/passwd"
            bundle.addfile(entry)
    work = tmp_path / "unpacked"
    with pytest.raises(ValueError):
        exporter.unpack(archive, work)
    assert not work.exists()


@pytest.mark.parametrize("fault", ["traversal", "symlink", "duplicate", "extra"])
def test_zip_unpack_rejects_unsafe_members(tmp_path, exporter, export_source, fault):
    payload = exporter.snapshot(export_source, [])
    archive = tmp_path / "bad.zip"
    with zipfile.ZipFile(archive, "w") as bundle:
        for name, (data, mode) in payload.items():
            entry = zipfile.ZipInfo(f"submission/{name}")
            entry.external_attr = (stat.S_IFREG | mode) << 16
            bundle.writestr(entry, data)
        entry = zipfile.ZipInfo({
            "traversal": "submission/../escape.py",
            "symlink": "submission/hcu/escape.py",
            "duplicate": "submission/VERSION",
            "extra": "submission/artifacts/agent",
        }[fault])
        entry.external_attr = ((stat.S_IFLNK if fault == "symlink" else stat.S_IFREG) | 0o644) << 16
        if fault == "duplicate":
            with pytest.warns(UserWarning, match="Duplicate"):
                bundle.writestr(entry, b"extra")
        else:
            bundle.writestr(entry, b"extra")
    work = tmp_path / "unpacked"
    with pytest.raises(ValueError):
        exporter.unpack(archive, work)
    assert not work.exists()


def test_license_check_requires_exact_clean_release_contents(tmp_path, exporter, export_source):
    output = tmp_path / "release"
    subprocess.run(
        ["bash", str(ROOT / "scripts/install-license-notices.sh"), str(output)], check=True,
    )
    (output / "computer-use").write_bytes(b"unit test placeholder, not build evidence")
    assert "LICENSE" in exporter.check_licenses(ROOT, output)
    (output / "downloaded-agent").write_text("must not redistribute")
    with pytest.raises(ValueError, match="unexpected release contents"):
        exporter.check_licenses(ROOT, output)
    (output / "downloaded-agent").unlink()
    (output / "LICENSE").write_text("truncated")
    with pytest.raises(ValueError, match="altered license"):
        exporter.check_licenses(ROOT, output)


def test_real_source_snapshot_has_current_instruction_and_contract_files(tmp_path, exporter):
    """This checks real payloads; CI separately compiles and smokes the archive."""
    output = tmp_path / "real-source.zip"
    report = exporter.export(ROOT, output, [])
    unpacked, _ = exporter.unpack(output, tmp_path / "unpacked")
    for name in (
        "instructions/base.md", "instructions/hybrid.md",
        "computer-use/testdata/contract/public_contract.json",
        "licenses/Apache-2.0.txt", "licenses/Playwright-NOTICE.txt",
        "computer-use/vendor/golang.org/x/text/LICENSE",
        "hcu/osworld/run_queue.py",
        "configs/osworld-v1-tasks.json", "configs/osworld-v1-excluded-tasks.json",
    ):
        assert (unpacked / name).read_bytes() == (ROOT / name).read_bytes()
    assert set(json.loads((unpacked / exporter.MANIFEST).read_text())["files"]) == set(report["files"])


def test_real_source_attachment_has_no_export_process_or_project_dates(tmp_path, exporter):
    archive = tmp_path / "submission.zip"
    exporter.export(ROOT, archive, [])
    with zipfile.ZipFile(archive) as bundle:
        names = {name.removeprefix("submission/") for name in bundle.namelist()}
        assert not any(name.startswith(".github/") for name in names)
        assert not names & {
            ".gitignore", ".gitattributes", "scripts/export-submission.py",
            "tests/test_source_archive.py", "CONTRIBUTING.md", "CHANGELOG.md",
        }
        manifest = json.loads(bundle.read("submission/SUBMISSION_MANIFEST.json"))
        assert set(manifest) == {"format_version", "version", "module", "files", "package_scope"}
        dependencies = json.loads(bundle.read("submission/configs/agent-artifacts.lock.json"))
        assert "updated_at_utc" not in dependencies
        assert all("published_at_utc" not in item for item in dependencies["agents"].values())
        for name in names:
            text = bundle.read("submission/" + name).decode()
            assert "current-working-tree-including-allowed-untracked-files" not in text, name
            assert "This used to be loose enough" not in text, name
            assert "measured hit rates were" not in text, name
            assert "hybrid MCP prototype" not in text, name
            assert "Observed windows reached the tree within" not in text, name
            assert "so measured timings can be replayed" not in text, name
            assert "legacy unknown-preservation assertions" not in text, name
            assert "--legacy-red" not in text, name
            assert "candidateABatchService" not in text, name
            assert "The cross-model comparison runs" not in text, name
            assert "PRIVATE_MARKERS" not in text, name
            assert "the old function name was too generic" not in text, name
            assert "20260925-120000" not in text, name


def test_environment_report_records_versions_without_editable_paths(
    tmp_path, exporter, export_source, monkeypatch,
):
    from types import SimpleNamespace

    monkeypatch.setattr(exporter.metadata, "distributions", lambda: [
        SimpleNamespace(metadata={"Name": "local-package"}, version="1.2.3",
                        locate_file=lambda name: "/private/editable/path"),
        SimpleNamespace(metadata={"Name": "pytest"}, version="8.4.2"),
        SimpleNamespace(metadata={"Name": "local_package"}, version="0.1.0"),
    ])
    monkeypatch.setattr(
        exporter.subprocess, "check_output",
        lambda *args, **kwargs: f"{tmp_path}/computer-use: go1.22.12\n",
    )
    result = exporter.environment_report(export_source, tmp_path, "local", {})
    assert result["go_compiler"] == "go1.22.12"
    assert result["python"]["resolved_distributions"] == [
        {"name": "local-package", "version": "1.2.3"},
        {"name": "pytest", "version": "8.4.2"},
    ]
    assert "/private" not in json.dumps(result)
    assert str(tmp_path) not in json.dumps(result)


def test_explicit_release_output_refuses_shared_existing_directory(tmp_path):
    cached = tmp_path / "artifacts"
    cached.mkdir()
    (cached / "downloaded-agent").write_text("preserve this external dependency")
    result = subprocess.run(
        ["bash", str(ROOT / "scripts/build-release.sh"), str(cached)],
        capture_output=True, text=True,
    )
    assert result.returncode == 2
    assert "must not exist" in result.stderr
    assert (cached / "downloaded-agent").read_text() == "preserve this external dependency"
