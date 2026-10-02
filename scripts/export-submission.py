#!/usr/bin/env python3
"""Export the working tree, then verify the actual source archive on Linux.

This is identity redaction, not a certification of anonymous authorship.
Copyright and third-party attribution are deliberately preserved.
"""

from __future__ import annotations

import argparse
import gzip
from importlib import metadata
import io
import ipaddress
import json
import os
from pathlib import Path, PurePosixPath
import platform
import re
import stat
import subprocess
import sys
import tarfile
import zipfile


ROOT = Path(__file__).resolve().parents[1]
PREFIX = "submission"
MANIFEST = "SUBMISSION_MANIFEST.json"
ANONYMOUS_MODULE = "example.org/anonymous/computer-use"
ROOT_FILES = {
    "README.md", "SECURITY.md",
    "LICENSE", "THIRD_PARTY_NOTICES.md", "VERSION", "Makefile",
    "pyproject.toml", ".dockerignore",
}
USER_DOCS = {
    "docs/ARCHITECTURE.md", "docs/MCP.md", "docs/PLATFORMS.md",
    "docs/OSWORLD.md", "docs/REPRODUCIBILITY.md", "docs/AGENT_PROFILES.md",
    "docs/AX_AND_CDP.md", "docs/INSTRUCTIONS.md", "docs/ISOLATION.md",
}
USER_SCRIPTS = {
    "scripts/build-release.sh", "scripts/download-agent-artifacts.py",
    "scripts/fetch-osworld-v1-image.sh", "scripts/install-license-notices.sh",
    "scripts/install-osworld-v2.py", "scripts/smoke-mcp.py",
}
MANIFEST_FIELDS = {"format_version", "version", "module", "files", "package_scope"}
SOURCE_SUFFIXES = {
    ".go", ".py", ".swift", ".cs", ".csproj", ".json", ".plist",
    ".manifest", ".sh", ".ps1", ".txt", ".md", ".mod", ".sum", ".config",
}
EXCLUDED_PARTS = {
    ".git", ".agents", ".codex", ".claude", ".venv", "venv",
    "__pycache__", ".pytest_cache", ".ruff_cache", ".mypy_cache",
    ".build", "build", "dist", "bin", "obj", "artifacts", "external", "runs",
    "credentials", "secrets", "node_modules",
}
MAX_FILE_BYTES = 16 * 1024 * 1024
MAX_ARCHIVE_BYTES = 128 * 1024 * 1024
HOST_PATTERN = re.compile(
    r"/data(?:drive)\d*(?:/|\b)|/home/(?!user(?:/|\b))[\w.-]+|/Users/[\w.-]+"
)
IPV4_PATTERN = re.compile(r"(?<![\w.])(?:\d{1,3}\.){3}\d{1,3}(?![\w.])")
EXAMPLE_NETWORKS = tuple(ipaddress.ip_network(value) for value in (
    "192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24",
))
SECRET_PATTERN = re.compile(
    r"-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----"
    r"|\b(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,}"
    r"|AKIA[A-Z0-9]{16}|sk-[A-Za-z0-9_-]{32,})\b"
)
REQUIRED = {
    "VERSION", "LICENSE", "THIRD_PARTY_NOTICES.md", "licenses/Apache-2.0.txt",
    "licenses/Playwright-NOTICE.txt",
    "computer-use/go.mod", "computer-use/main.go", "computer-use/vendor/modules.txt",
    "computer-use/vendor/golang.org/x/text/LICENSE",
    "computer-use/vendor/golang.org/x/text/PATENTS",
    "instructions/base.md", "instructions/hybrid.md",
    "instructions/ax-only.md", "instructions/gui-only.md",
    "scripts/build-release.sh", "scripts/install-license-notices.sh",
    "scripts/smoke-mcp.py", "docker/Dockerfile.binary",
}


def excluded(path: PurePosixPath) -> bool:
    return any(
        part.lower() in EXCLUDED_PARTS
        or PurePosixPath(part.lower()).stem in {"credentials", "secrets", "id_rsa", "id_ed25519"}
        or part.lower().startswith((".env", ".credential", ".secret"))
        or part.lower().endswith((".egg-info", ".pyc", ".log", ".qcow2", ".pem", ".key"))
        for part in path.parts
    )


def allowed(name: str) -> bool:
    path = PurePosixPath(name)
    if name in ROOT_FILES:
        return True
    if excluded(path):
        return False
    if any(part.startswith(".") for part in path.parts):
        return False
    top = path.parts[0]
    if top == "computer-use":
        return path.suffix.lower() in SOURCE_SUFFIXES or path.name in {"LICENSE", "PATENTS"}
    if top in {"hcu", "osworld-overlay", "tests"}:
        return path.suffix == ".py" and name != "tests/test_source_archive.py"
    if top == "docs":
        return name in USER_DOCS
    if top == "instructions":
        return path.suffix == ".md"
    if top == "licenses":
        return path.suffix in {".txt", ".md"} or path.name in {"LICENSE", "NOTICE", "PATENTS"}
    if top == "scripts":
        return name in USER_SCRIPTS
    if top == "docker":
        return path.name.startswith("Dockerfile")
    if top == "configs":
        return (
            name in {
                "configs/env.example", "configs/agent-artifacts.lock.json",
                "configs/osworld-v1-excluded-tasks.json", "configs/osworld-v1-tasks.json",
            }
            or (len(path.parts) == 2 and path.name.endswith(".example.json"))
            or name == "configs/reproduction/README.md"
        )
    return False


def source_files(root: Path) -> list[Path]:
    paths = []
    roots = ROOT_FILES | {
        "computer-use", "hcu", "osworld-overlay", "tests",
        "docs", "instructions", "licenses", "scripts", "docker", "configs",
    }
    for top in sorted(roots):
        base = root / top
        if base.is_symlink():
            raise ValueError(f"source symlink rejected: {top}")
        if base.is_file():
            paths.append(base)
        elif base.is_dir():
            for directory, dirs, files in os.walk(base, followlinks=False):
                dirs[:] = sorted(
                    d for d in dirs
                    if not excluded(PurePosixPath((Path(directory) / d).relative_to(root).as_posix()))
                    and not d.startswith(".")
                )
                for name in [*dirs, *files]:
                    path = Path(directory) / name
                    relative = path.relative_to(root).as_posix()
                    if excluded(PurePosixPath(relative)):
                        continue
                    if path.is_symlink():
                        raise ValueError(f"source symlink rejected: {relative}")
                    if path.is_file() and allowed(relative):
                        paths.append(path)
    return sorted(paths)


def legal_file(name: str) -> bool:
    path = PurePosixPath(name)
    return (
        name in {"LICENSE", "THIRD_PARTY_NOTICES.md"}
        or name.startswith(("licenses/", "computer-use/vendor/"))
        or path.name in {"LICENSE", "NOTICE", "PATENTS"}
    )


def signature(path: Path) -> tuple[int, int, int, int]:
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode):
        raise ValueError("source must be a regular file")
    return info.st_ino, info.st_size, info.st_mtime_ns, info.st_mode


def sensitive_address(text: str) -> bool:
    # Recognize common escaped/defanged spellings without storing real hosts.
    text = text.replace("[.]", ".").replace(r"\.", ".")
    text = re.sub(r"\b(?:Chrome|Chromium|Firefox|Version)/\d+(?:\.\d+){3}\b", "", text)
    for match in IPV4_PATTERN.finditer(text):
        try:
            address = ipaddress.ip_address(match[0])
        except ValueError:
            continue
        if (
            address.is_loopback or address.is_unspecified
            or str(address) == "172.17.0.1"  # documented default Docker bridge
            or any(address in network for network in EXAMPLE_NETWORKS)
        ):
            continue
        return True
    return False


def snapshot(root: Path, identities: list[str]) -> dict[str, tuple[bytes, int]]:
    paths = source_files(root)
    signatures = {path: signature(path) for path in paths}
    payload = {}
    total = 0
    for path in paths:
        name = path.relative_to(root).as_posix()
        if signatures[path][1] > MAX_FILE_BYTES:
            raise ValueError(f"source file too large: {name}")
        data = path.read_bytes()
        total += len(data)
        if total > MAX_ARCHIVE_BYTES:
            raise ValueError("source snapshot exceeds size limit")
        payload[name] = (data, 0o755 if signatures[path][3] & 0o111 else 0o644)
    if source_files(root) != paths or any(signature(p) != signatures[p] for p in paths):
        raise ValueError("working tree changed during snapshot; retry after writers finish")
    missing = REQUIRED - payload.keys()
    if missing:
        raise ValueError(f"missing required source files: {sorted(missing)}")
    module_text = payload["computer-use/go.mod"][0].decode("utf-8")
    match = re.search(r"(?m)^module\s+(\S+)\s*$", module_text)
    if not match:
        raise ValueError("go.mod has no module declaration")
    original_module = match[1]
    module_parts = original_module.split("/")
    markers = [marker for marker in identities if marker]
    owner = module_parts[1] if len(module_parts) >= 3 and module_parts[0] == "github.com" else None
    if owner:
        markers.append(owner)
    for name, (data, mode) in list(payload.items()):
        text = data.decode("utf-8")
        if name == "THIRD_PARTY_NOTICES.md":
            # Only the project fork locator is nonlegal metadata. Preserve the
            # upstream source, license text and copyright holders verbatim.
            if owner:
                text = re.sub(
                    rf"(?m)^`{re.escape(owner)}/[\w.-]+`, itself derived from$",
                    "an intermediate project fork, itself derived from",
                    text,
                )
            noncopyright = "\n".join(
                line for line in text.splitlines()
                if not line.lstrip().casefold().startswith("copyright")
            )
            if any(marker.casefold() in noncopyright.casefold() for marker in markers):
                raise ValueError(f"project identity remains in {name}; manual attribution review required")
        if not legal_file(name):
            rewritten = text.replace(original_module, ANONYMOUS_MODULE)
            text = rewritten
            for marker in markers:
                if marker.casefold() in text.casefold():
                    raise ValueError(f"unredacted project identity in {name}; review source or attribution")
        # Notices are exempt from author redaction, never from credential checks.
        if HOST_PATTERN.search(text) or SECRET_PATTERN.search(text) or sensitive_address(text):
            raise ValueError(f"sensitive host path/address or credential pattern in {name}")
        if any(marker.casefold() in name.casefold() for marker in markers):
            raise ValueError(f"project identity in file path: {name}")
        payload[name] = (text.encode("utf-8"), mode)
    report = {
        "format_version": 1,
        "version": payload["VERSION"][0].decode().strip(),
        "module": ANONYMOUS_MODULE,
        "files": sorted(payload),
        "package_scope": "source-and-user-documentation",
    }
    payload[MANIFEST] = (json.dumps(report, indent=2, sort_keys=True).encode() + b"\n", 0o644)
    return payload


def export(root: Path, output: Path, identities: list[str]) -> dict:
    if not (output.name.endswith(".zip") or output.name.endswith(".tar.gz")):
        raise ValueError("submission output must end in .zip or .tar.gz")
    payload = snapshot(root.resolve(), identities)
    output.parent.mkdir(parents=True, exist_ok=True)
    # Exclusive creation prevents accidental replacement of a reviewed artifact.
    with output.open("xb") as raw:
        try:
            if output.suffix == ".zip":
                with zipfile.ZipFile(raw, "w", compression=zipfile.ZIP_DEFLATED) as archive:
                    for name, (data, mode) in sorted(payload.items()):
                        entry = zipfile.ZipInfo(f"{PREFIX}/{name}", date_time=(1980, 1, 1, 0, 0, 0))
                        entry.create_system = 3
                        entry.external_attr = (stat.S_IFREG | mode) << 16
                        entry.compress_type = zipfile.ZIP_DEFLATED
                        archive.writestr(entry, data)
            else:
                with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=0) as compressed:
                    with tarfile.open(fileobj=compressed, mode="w") as archive:
                        for name, (data, mode) in sorted(payload.items()):
                            entry = tarfile.TarInfo(f"{PREFIX}/{name}")
                            entry.size, entry.mode = len(data), mode
                            archive.addfile(entry, io.BytesIO(data))
        except BaseException:
            output.unlink()
            raise
    return json.loads(payload[MANIFEST][0])


def archive_entries(path: Path):
    if path.suffix == ".zip":
        with zipfile.ZipFile(path) as bundle:
            for entry in bundle.infolist():
                mode = entry.external_attr >> 16
                if entry.is_dir() or stat.S_IFMT(mode) not in {0, stat.S_IFREG}:
                    raise ValueError("unsafe or non-regular ZIP member")
                with bundle.open(entry) as stream:
                    yield entry.filename, entry.file_size, mode, stream
    elif path.name.endswith(".tar.gz"):
        with tarfile.open(path, "r:gz") as bundle:
            for entry in bundle:
                if not entry.isfile():
                    raise ValueError("unsafe or non-regular tar member")
                with bundle.extractfile(entry) as stream:
                    yield entry.name, entry.size, entry.mode, stream
    else:
        raise ValueError("submission archive must end in .zip or .tar.gz")


def unpack(archive: Path, work_dir: Path) -> tuple[Path, dict]:
    if work_dir.exists():
        raise ValueError("verification work directory must not exist")
    payload = {}
    total = 0
    for member_name, size, mode, stream in archive_entries(archive):
        parts = PurePosixPath(member_name).parts
        if (
            len(parts) < 2 or parts[0] != PREFIX
            or ".." in parts or "\\" in member_name
            or member_name != "/".join(parts)
        ):
            raise ValueError("unsafe archive member")
        name = "/".join(parts[1:])
        if name in payload or (name != MANIFEST and not allowed(name)):
            raise ValueError(f"duplicate or non-allowlisted archive member: {name}")
        total += size
        if size > MAX_FILE_BYTES or total > MAX_ARCHIVE_BYTES:
            raise ValueError("archive exceeds size limit")
        data = stream.read(MAX_FILE_BYTES + 1)
        if len(data) != size:
            raise ValueError("archive member size mismatch")
        payload[name] = (data, mode)
    if MANIFEST not in payload or not REQUIRED <= payload.keys():
        raise ValueError("archive lacks manifest or required source files")
    report = json.loads(payload[MANIFEST][0])
    if (
        not isinstance(report, dict) or set(report) != MANIFEST_FIELDS
        or report["format_version"] != 1
        or report["package_scope"] != "source-and-user-documentation"
    ):
        raise ValueError("unexpected submission manifest fields or format")
    if report["files"] != sorted(payload.keys() - {MANIFEST}):
        raise ValueError("archive members differ from export manifest")
    if report.get("module") != ANONYMOUS_MODULE:
        raise ValueError("unexpected submission module")
    module = payload["computer-use/go.mod"][0].decode("utf-8")
    if not re.search(rf"(?m)^module\s+{re.escape(ANONYMOUS_MODULE)}\s*$", module):
        raise ValueError("source module differs from submission manifest")
    if report.get("version") != payload["VERSION"][0].decode("utf-8").strip():
        raise ValueError("source version differs from submission manifest")
    root = work_dir / "source"
    root.mkdir(parents=True)
    for name, (data, mode) in payload.items():
        path = root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
        path.chmod(0o755 if mode & 0o111 else 0o644)
    return root, report


def check_licenses(root: Path, output: Path) -> list[str]:
    notices = {"LICENSE": "LICENSE", "THIRD_PARTY_NOTICES.md": "THIRD_PARTY_NOTICES.md"}
    for source in (root / "licenses").rglob("*"):
        if source.is_file():
            name = source.relative_to(root).as_posix()
            notices[name] = name
    for name in ("LICENSE", "PATENTS"):
        notices[f"computer-use/vendor/golang.org/x/text/{name}"] = f"licenses/golang.org/x/text/{name}"
    actual = {p.relative_to(output).as_posix() for p in output.rglob("*") if p.is_file()}
    if actual != {"computer-use", *notices.values()}:
        raise ValueError(f"unexpected release contents: {sorted(actual)}")
    for source, destination in notices.items():
        data = (root / source).read_bytes()
        if not data.strip() or (output / destination).read_bytes() != data:
            raise ValueError(f"missing or altered license notice: {destination}")
    return sorted(notices.values())


def environment_report(root: Path, output: Path, builder: str, env: dict) -> dict:
    def capture(command):
        return subprocess.check_output(command, env=env, text=True, timeout=60).strip()

    if builder == "docker":
        dockerfile = (root / "docker/Dockerfile.binary").read_text()
        base = re.search(r"(?mi)^FROM\s+(\S+)\s+AS\s+builder\s*$", dockerfile)
        if not base:
            raise ValueError("cannot identify Docker builder image")
        # Read build info from the delivered executable, not the host's Go version.
        go_output = capture([
            "docker", "run", "--rm", "--network", "none",
            "--mount", f"type=bind,src={output},dst=/release,readonly",
            "--entrypoint", "go", base[1], "version", "/release/computer-use",
        ])
        docker_versions = json.loads(capture([
            "docker", "version", "--format",
            '{"client":"{{.Client.Version}}","server":"{{.Server.Version}}"}',
        ]))
    else:
        go_output = capture(["go", "version", str(output / "computer-use")])
        docker_versions = None
    go_version = go_output.rsplit(": ", 1)[-1]
    if not re.fullmatch(r"go\d+\.\d+(?:\.\d+)?(?:[a-zA-Z0-9.-]+)?", go_version):
        raise ValueError("cannot read Go compiler version from executable")
    packages = {}
    for dist in metadata.distributions():
        name = dist.metadata["Name"]
        if name:
            # Match metadata lookup order when system and user installs coexist.
            normalized = re.sub(r"[-_.]+", "-", name.casefold())
            packages.setdefault(normalized, (name, dist.version))
    return {
        "system": platform.system(), "architecture": platform.machine(),
        "python": {
            "implementation": platform.python_implementation(),
            "version": platform.python_version(),
            "resolved_distributions": [
                {"name": name, "version": version} for name, version in sorted(packages.values(), key=lambda item: item[0].casefold())
            ],
            "scope": "verification Python environment; names and versions only, no install paths",
        },
        "go_compiler": go_version,
        "docker": docker_versions,
    }


def verify(archive: Path, work_dir: Path, builder: str, python_tests: bool = False) -> dict:
    if not sys.platform.startswith("linux"):
        raise ValueError("release verification requires Linux")
    root, exported = unpack(archive, work_dir)
    output = work_dir / "release"
    env = dict(os.environ)
    for key in list(env):
        if key.startswith(("COMPUTER_USE_", "HCU_", "OSWORLD_", "HYBRID_COMPUTER_USE_")) or key in {
            "CLAUDE_TEST_BINARY", "CODEX_TEST_BINARY", "PYTEST_ADDOPTS", "PYTHONPATH",
        }:
            del env[key]
    env.update(
        GOOS="linux", GOARCH="amd64", CGO_ENABLED="0", GOPROXY="off", GOWORK="off",
        PYTHONPATH=str(root),
    )

    def run(command: list[str], cwd: Path, timeout: int = 600) -> str:
        captured = ""
        try:
            completed = subprocess.run(
                command, cwd=cwd, env=env, check=True, timeout=timeout,
                stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
            )
            captured = completed.stdout
        except (subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
            captured = error.output or ""
            if isinstance(captured, bytes):
                captured = captured.decode("utf-8", errors="replace")
            raise
        finally:
            captured = captured.replace(str(work_dir), "$WORK_DIR").replace(str(ROOT), "$SOURCE_ROOT")
            with (work_dir / "verification.log").open("a") as log:
                log.write(captured)
            print(captured, end="", flush=True)
        return captured

    if builder == "docker":
        run(["bash", "scripts/build-release.sh", str(output)], root, 1200)
    else:
        run(["go", "test", "./..."], root / "computer-use")
        output.mkdir()
        run([
            "go", "build", "-trimpath", "-ldflags",
            f"-s -w -X main.version={exported['version']}",
            "-o", str(output / "computer-use"), ".",
        ], root / "computer-use")
        run(["bash", "scripts/install-license-notices.sh", str(output)], root)
    version = subprocess.check_output(
        [str(output / "computer-use"), "--version"], env=env, text=True, timeout=10,
    ).strip()
    if version != exported["version"]:
        raise ValueError("built binary version differs from source VERSION")
    run([sys.executable, "scripts/smoke-mcp.py", str(output / "computer-use")], root, 30)
    notices = check_licenses(root, output)
    test_output = ""
    if python_tests:
        test_output = run([sys.executable, "-m", "pytest", "-q", "-ra", "-p", "no:cacheprovider", "tests"], root)
    result = {
        "status": "passed", "builder": builder, "version": version,
        "source": "source-archive", "public_tools": ["AX", "GUI"],
        "license_files_checked": notices,
        "anonymity_status": "legal attribution retained; manual review required",
        "desktop_or_model_validation": False,
        "python_tests": "passed (see log for skips)" if python_tests else "not run",
        "python_test_summary": test_output.strip().splitlines()[-1] if test_output.strip() else None,
        "environment": environment_report(root, output, builder, env),
    }
    (work_dir / "verification.json").write_text(json.dumps(result, indent=2) + "\n")
    return result


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    exporting = commands.add_parser("export", help="snapshot current allowed working-tree files")
    exporting.add_argument("--source", type=Path, default=ROOT)
    exporting.add_argument("--output", required=True, type=Path)
    exporting.add_argument("--identity", action="append", default=[], help="additional identity marker that must not remain outside notices")
    verifying = commands.add_parser("verify", help="unpack, build, smoke and check notices; no desktop input")
    verifying.add_argument("archive", type=Path)
    verifying.add_argument("--work-dir", required=True, type=Path)
    verifying.add_argument("--builder", choices=["docker", "local"], default="docker")
    verifying.add_argument("--python-tests", action="store_true", help="run offline Python tests from the unpacked source; install .[test] first")
    args = parser.parse_args()
    try:
        if args.command == "export":
            report = export(args.source, args.output, args.identity)
            result = {
                "archive": args.output.name,
                "file_count": len(report["files"]),
                "anonymity_status": "legal attribution retained; manual review required",
            }
        else:
            result = verify(args.archive.resolve(), args.work_dir.resolve(), args.builder, args.python_tests)
    except (OSError, ValueError, subprocess.SubprocessError, tarfile.TarError, zipfile.BadZipFile) as error:
        parser.exit(1, f"{error}\n")
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
