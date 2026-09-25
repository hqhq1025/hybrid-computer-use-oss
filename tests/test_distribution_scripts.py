import hashlib
import importlib.util
import io
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import zipfile

import pytest


ROOT = Path(__file__).resolve().parents[1]


def load_script(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / "scripts" / f"{name}.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def write_file(path, text, executable=False):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")
    if executable:
        path.chmod(0o755)


@pytest.fixture
def installer(tmp_path, monkeypatch):
    module = load_script("install-osworld-v2")
    source = tmp_path / "source"
    shutil.copytree(ROOT / "osworld-overlay", source / "osworld-overlay")
    checkout = tmp_path / "upstream without git"
    files = {
        "__init__.py": "",
        "controllers/python.py": "class PythonController:\n    def __init__(self, vm_ip, server_port=5000): pass\n",
        "providers/__init__.py": "def create_vm_manager_and_provider(provider_name, region, use_proxy=False, use_public_ip=False): pass\n",
        "providers/base.py": "class Provider: pass\n",
        "providers/docker/manager.py": "class DockerManager: pass\n",
        "providers/aws/proxy_pool.py": "class ProxyInfo: pass\ndef get_global_proxy_pool(): pass\ndef init_proxy_pool(): pass\n",
        "task_base.py": "class BaseTask:\n    def setup(self, env): pass\n    def evaluate(self, env): pass\n",
        "file_source.py": "def resolve_local_source(source): pass\n",
        "evaluators/metrics/__init__.py": "",
        "evaluators/metrics/utils.py": "def compare_urls(a, b): pass\n",
        "desktop_env.py": "# original environment\n",
    }
    for name, text in files.items():
        write_file(checkout / "desktop_env" / name, text)
    monkeypatch.setattr(module, "ROOT", source)
    monkeypatch.setattr(sys, "argv", ["install-osworld-v2.py", str(checkout)])
    return module, checkout, source / "osworld-overlay"


def test_installer_accepts_compatible_source_without_git(installer):
    module, checkout, overlay = installer
    assert not (checkout / ".git").exists()
    original = (checkout / "desktop_env/controllers/python.py").read_bytes()
    assert module.main() == 0
    for source in overlay.rglob("*.py"):
        assert (checkout / source.relative_to(overlay)).read_bytes() == source.read_bytes()
    assert (checkout / "desktop_env/controllers/python.py").read_bytes() == original


def test_installer_matches_positional_factory_and_keyword_controller_calls(installer):
    module, checkout, _ = installer
    (checkout / "desktop_env/providers/__init__.py").write_text(
        "def create_vm_manager_and_provider(name, location, /, *, use_proxy, use_public_ip): pass\n"
    )
    (checkout / "desktop_env/controllers/python.py").write_text(
        "class PythonController:\n    def __init__(self, *, vm_ip, server_port): pass\n"
    )
    assert module.main() == 0


@pytest.mark.parametrize("fault", ["missing", "syntax", "symbol", "signature", "extra_required", "overlay", "escape"])
def test_installer_rejects_incompatible_input_before_any_copy(installer, fault, tmp_path):
    module, checkout, overlay = installer
    controller = checkout / "desktop_env/controllers/python.py"
    if fault == "missing":
        controller.unlink()
    elif fault == "syntax":
        controller.write_text("class (", encoding="utf-8")
    elif fault == "symbol":
        controller.write_text("class Unrelated: pass\n", encoding="utf-8")
    elif fault == "signature":
        controller.write_text("class PythonController:\n    def __init__(self, host): pass\n")
    elif fault == "extra_required":
        factory = checkout / "desktop_env/providers/__init__.py"
        factory.write_text("def create_vm_manager_and_provider(provider_name, region, token, **kwargs): pass\n")
    elif fault == "overlay":
        write_file(overlay / "desktop_env/zzz.py", "def broken(")
    else:
        outside = tmp_path / "outside.py"
        outside.write_text("do not overwrite\n")
        (checkout / "desktop_env/zzz.py").symlink_to(outside)
        write_file(overlay / "desktop_env/zzz.py", "# replacement\n")
    original = (checkout / "desktop_env/desktop_env.py").read_bytes()
    with pytest.raises(SystemExit) as raised:
        module.main()
    assert raised.value.code == 2
    assert (checkout / "desktop_env/desktop_env.py").read_bytes() == original
    assert not (checkout / "desktop_env/cache.py").exists()
    if fault == "escape":
        assert outside.read_text() == "do not overwrite\n"


def test_installer_has_no_force_bypass(installer, monkeypatch):
    module, checkout, _ = installer
    monkeypatch.setattr(sys, "argv", ["install-osworld-v2.py", str(checkout), "--force"])
    with pytest.raises(SystemExit) as raised:
        module.main()
    assert raised.value.code == 2
    assert not (checkout / "desktop_env/cache.py").exists()


@pytest.mark.parametrize("kind", ["member", "archive", "directory", "symlink", "empty", "missing", "corrupt"])
def test_agent_download_validates_archives_after_digest_check(tmp_path, monkeypatch, kind):
    module = load_script("download-agent-artifacts")
    monkeypatch.setattr(module, "ROOT", tmp_path)
    archive = tmp_path / "publisher.tgz"
    content = b"new publisher content for the same selected version\n"
    with tarfile.open(archive, "w:gz") as bundle:
        entry = tarfile.TarInfo("bin/agent")
        if kind == "directory":
            entry.type = tarfile.DIRTYPE
        elif kind == "symlink":
            entry.type = tarfile.SYMTYPE
            entry.linkname = "elsewhere"
        else:
            entry.size = 0 if kind == "empty" else len(content)
        bundle.addfile(entry, io.BytesIO(content) if entry.isfile() else None)
    if kind == "corrupt":
        archive.write_bytes(b"not an archive")
    config = {
        "archive": "download.tgz", "archive_url": "https://example.invalid/v1/agent.tgz",
        "version": "1", "artifact_path": "artifacts/agent",
        "archive_sha256": hashlib.sha256(archive.read_bytes()).hexdigest(),
    }
    if kind != "archive":
        config["archive_member"] = "missing" if kind == "missing" else "bin/agent"
    calls = []

    def download(url, destination):
        calls.append(url)
        shutil.copyfile(archive, destination)

    monkeypatch.setattr(module.urllib.request, "urlretrieve", download)
    target = tmp_path / config["artifact_path"]
    write_file(target, "previous install")
    if kind in {"member", "archive"}:
        assert module.install("agent", config, tmp_path, None) == target
        assert target.read_bytes() == (archive.read_bytes() if kind == "archive" else content)
        if kind == "member":
            assert os.access(target, os.X_OK)
    else:
        with pytest.raises((RuntimeError, KeyError, tarfile.ReadError)):
            module.install("agent", config, tmp_path, None)
        assert target.read_text() == "previous install"
    assert calls == [config["archive_url"]]


@pytest.mark.parametrize("delivery", ["member", "archive", "brotli"])
@pytest.mark.parametrize("fault", ["corrupt", "tampered", "missing_digest"])
def test_agent_download_rejects_unverified_content_without_overwriting_install(
    tmp_path, monkeypatch, delivery, fault,
):
    module = load_script("download-agent-artifacts")
    monkeypatch.setattr(module, "ROOT", tmp_path)
    archive = tmp_path / "publisher.tar"

    def make_archive(content):
        with tarfile.open(archive, "w") as bundle:
            entry = tarfile.TarInfo("bin/agent")
            entry.size = len(content)
            bundle.addfile(entry, io.BytesIO(content))

    make_archive(b"trusted content")
    config = {
        "archive": "download.tar", "archive_url": "https://example.invalid/v1/agent.tar",
        "version": "1", "artifact_path": "artifacts/agent",
        "archive_sha256": hashlib.sha256(archive.read_bytes()).hexdigest(),
    }
    if delivery != "archive":
        config["archive_member"] = "bin/agent"
    if delivery == "brotli":
        config["brotli"] = True
    if fault == "corrupt":
        archive.write_bytes(b"damaged download")
    elif fault == "tampered":
        make_archive(b"altered content")
    else:
        del config["archive_sha256"]

    monkeypatch.setattr(module.urllib.request, "urlretrieve",
                        lambda url, destination: shutil.copyfile(archive, destination))

    def unexpected_decode(*args, **kwargs):
        pytest.fail("unverified download reached the Brotli decoder")

    monkeypatch.setattr(module.subprocess, "run", unexpected_decode)
    target = tmp_path / config["artifact_path"]
    write_file(target, "previous good install", executable=True)
    original_mode = target.stat().st_mode
    if fault == "missing_digest":
        with pytest.raises(KeyError, match="archive_sha256"):
            module.install("agent", config, tmp_path, tmp_path / "node")
    else:
        with pytest.raises(RuntimeError, match="SHA-256 mismatch"):
            module.install("agent", config, tmp_path, tmp_path / "node")
    assert target.read_text() == "previous good install"
    assert target.stat().st_mode == original_mode
    assert not (tmp_path / "agent.member").exists()


@pytest.mark.parametrize("state", [
    "ready", "partial", "oversized", "corrupt", "missing_member",
    "download_corrupt", "download_tampered", "download_oversized",
])
def test_image_download_size_and_archive_checks(tmp_path, state):
    publisher = tmp_path / "publisher.zip"
    with zipfile.ZipFile(publisher, "w") as bundle:
        bundle.writestr("other" if state == "missing_member" else "Ubuntu.qcow2", b"image fixture")
        bundle.writestr("unexpected.txt", b"do not extract")
    expected_size = publisher.stat().st_size
    expected_digest = hashlib.sha256(publisher.read_bytes()).hexdigest()
    script = (ROOT / "scripts/fetch-osworld-v1-image.sh").read_text()
    script = script.replace('ARCHIVE_SIZE="12273896463"', f'ARCHIVE_SIZE="{expected_size}"')
    script = script.replace(
        'ARCHIVE_SHA256="b795b6cd4c69b252c1b4f10150a347795555032501b60fd031751ed09b896712"',
        f'ARCHIVE_SHA256="{expected_digest}"',
    )
    local_script = tmp_path / "fetch.sh"
    local_script.write_text(script)
    destination = tmp_path / "output with spaces"
    destination.mkdir()
    archive = destination / "Ubuntu.qcow2.zip"
    data = publisher.read_bytes()
    image = destination / "Ubuntu.qcow2"
    if state.startswith("download_"):
        image.write_bytes(b"previous good image")
        if state == "download_corrupt":
            publisher.write_bytes(b"x" * len(data))
        elif state == "download_oversized":
            publisher.write_bytes(data + b"x")
        else:
            with zipfile.ZipFile(publisher, "w") as bundle:
                bundle.writestr("Ubuntu.qcow2", b"other fixture")
                bundle.writestr("unexpected.txt", b"do not extract")
            assert publisher.stat().st_size == expected_size
    else:
        archive.write_bytes({
            "partial": data[:10], "oversized": data + b"x", "corrupt": b"x" * len(data),
        }.get(state, data))
    bin_dir = tmp_path / "bin"
    write_file(bin_dir / "aria2c", '#!/bin/sh\nprintf download >> "$TEST_LOG"\ncp "$PUBLISHER" "$ARCHIVE"\n', True)
    write_file(bin_dir / "qemu-img", '#!/bin/sh\nprintf qemu-info >> "$TEST_LOG"\n', True)
    log = tmp_path / "calls"
    env = dict(os.environ, PATH=f"{bin_dir}:{os.environ['PATH']}", TEST_LOG=str(log),
               PUBLISHER=str(publisher), ARCHIVE=str(archive))
    result = subprocess.run(["bash", str(local_script), str(destination)], env=env, capture_output=True, text=True)
    calls = log.read_text() if log.exists() else ""
    if state in {"ready", "partial", "oversized", "corrupt"}:
        assert result.returncode == 0, result.stdout + result.stderr
        assert (destination / "Ubuntu.qcow2").read_bytes() == b"image fixture"
        assert calls == ("download" if state != "ready" else "") + "qemu-info"
    elif state.startswith("download_"):
        assert result.returncode != 0
        assert image.read_bytes() == b"previous good image"
        assert calls == "download"
        if state != "download_oversized":
            assert "checksum did NOT match" in result.stderr
    else:
        assert result.returncode != 0
        assert not (destination / "Ubuntu.qcow2").exists()
        assert calls == ""
    assert not (destination / "unexpected.txt").exists()


@pytest.mark.parametrize("build", ["release", "local"])
def test_linux_builds_preserve_all_notice_text(tmp_path, build):
    repo = tmp_path / "source with spaces"
    script = repo / "scripts/build-release.sh"
    write_file(script, (ROOT / "scripts/build-release.sh").read_text())
    write_file(repo / "scripts/install-license-notices.sh", (ROOT / "scripts/install-license-notices.sh").read_text())
    write_file(repo / "Makefile", (ROOT / "Makefile").read_text())
    write_file(repo / "VERSION", (ROOT / "VERSION").read_text())
    notices = {
        "LICENSE": "LICENSE",
        "THIRD_PARTY_NOTICES.md": "THIRD_PARTY_NOTICES.md",
        "computer-use/vendor/golang.org/x/text/LICENSE": "licenses/golang.org/x/text/LICENSE",
        "computer-use/vendor/golang.org/x/text/PATENTS": "licenses/golang.org/x/text/PATENTS",
    }
    for notice in (ROOT / "licenses").rglob("*"):
        if notice.is_file():
            relative = str(notice.relative_to(ROOT))
            notices[relative] = relative
    for source in notices:
        write_file(repo / source, (ROOT / source).read_text())
    mock = tmp_path / "bin/docker"
    write_file(mock, '''#!/usr/bin/env python3
import pathlib, shutil, sys
args = sys.argv[1:]
assert args[args.index("--network") + 1] == "none"
output = pathlib.Path(args[args.index("--output") + 1].split("dest=", 1)[1])
repo = pathlib.Path(args[-1])
for source, destination in {
    "LICENSE": "LICENSE", "THIRD_PARTY_NOTICES.md": "THIRD_PARTY_NOTICES.md",
    "computer-use/vendor/golang.org/x/text/LICENSE": "licenses/golang.org/x/text/LICENSE",
    "computer-use/vendor/golang.org/x/text/PATENTS": "licenses/golang.org/x/text/PATENTS",
}.items():
    target = output / destination
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(repo / source, target)
binary = output / "computer-use"
binary.write_text("#!/bin/sh\\nprintf 'test-version\\\\n'\\n")
binary.chmod(0o755)
''', True)
    go = mock.parent / "go"
    write_file(go, '''#!/usr/bin/env python3
import os, pathlib, sys
assert (os.environ["GOOS"], os.environ["GOARCH"], os.environ["CGO_ENABLED"]) == ("linux", "amd64", "0")
output = pathlib.Path(sys.argv[sys.argv.index("-o") + 1])
output.write_text("mock local executable")
''', True)
    command = ["bash", str(script)] if build == "release" else ["make", "build-local"]
    result = subprocess.run(command, cwd=repo, capture_output=True, text=True,
                            env=dict(os.environ, PATH=f"{mock.parent}:{os.environ['PATH']}"))
    assert result.returncode == 0, result.stdout + result.stderr
    if build == "release":
        assert result.stdout.strip() == "test-version"
    else:
        assert (repo / "artifacts/computer-use").read_text() == "mock local executable"
    for source, destination in notices.items():
        assert (repo / "artifacts" / destination).read_bytes() == (ROOT / source).read_bytes()

    dockerfile = (ROOT / "docker/Dockerfile.binary").read_text()
    assert "COPY LICENSE THIRD_PARTY_NOTICES.md /out/" in dockerfile
    assert "COPY licenses/ /out/licenses/" in dockerfile
    assert "COPY computer-use/vendor/golang.org/x/text/LICENSE computer-use/vendor/golang.org/x/text/PATENTS /out/licenses/golang.org/x/text/" in dockerfile
    assert "COPY --from=builder /out/ /" in dockerfile


@pytest.mark.parametrize("script", ["build-release.sh", "fetch-osworld-v1-image.sh", "install-license-notices.sh"])
def test_distribution_shell_syntax(script):
    subprocess.run(["bash", "-n", str(ROOT / "scripts" / script)], check=True)
