# SPDX-License-Identifier: Apache-2.0
# Modified from xlang-ai/OSWorld-V2 file_source.py; see THIRD_PARTY_NOTICES.md.
"""Local task assets, separate from browser and application network access.

Setup never fetches assets from the network. HTTP source strings are resolved
through a local snapshot or an exact source map in ``setup-assets.json``.
The explicit preparation CLI is the only built-in asset downloader.
"""

from __future__ import annotations

import json
import os
from pathlib import Path
from urllib.parse import unquote, urlparse


DEFAULT_BASE_URL = "https://huggingface.co/datasets/xlangai/osworld_v2_assets/resolve/main"
HISTORY_DB_URL = (
    "https://huggingface.co/datasets/xlangai/ubuntu_osworld_file_cache/resolve/main/"
    "chrome/44ee5668-ecd5-4366-a6ce-c1c9b8d4e938/history_empty.sqlite?download=true"
)
MANIFEST_NAME = "setup-assets.json"
_setup_bindings: dict[str, str] | None = None
_asset_benchmark: str | None = None


def bind_setup_assets(
    bindings: dict[str, str] | None, *, benchmark_version: str | None = None,
) -> None:
    """Bind setup retrieval to files admitted by this task's host preflight."""
    global _setup_bindings, _asset_benchmark
    if benchmark_version is not None:
        if benchmark_version not in ("v1", "v2"):
            raise ValueError(f"Unknown asset benchmark: {benchmark_version}")
        _asset_benchmark = benchmark_version
    _setup_bindings = dict(bindings) if bindings is not None else None


def local_root(variable: str) -> Path | None:
    raw = os.environ.get(variable, "")
    if not raw:
        return None
    parsed = urlparse(raw)
    if parsed.scheme == "file":
        if parsed.netloc not in ("", "localhost"):
            raise ValueError(f"{variable} must refer to a local filesystem")
        raw = unquote(parsed.path)
    elif "://" in raw:
        raise ValueError(f"{variable} must be a local directory, not a URL")
    root = Path(raw).expanduser()
    if not root.is_absolute():
        raise ValueError(f"{variable} must be an absolute local directory")
    return root.resolve()


def manifest_path(root: Path, relative: str) -> Path:
    if not isinstance(relative, str) or not relative:
        raise ValueError("Asset manifest paths must be nonempty relative strings")
    candidate = (root / relative).resolve()
    if Path(relative).is_absolute() or not candidate.is_relative_to(root.resolve()):
        raise ValueError(f"Asset manifest path escapes its root: {relative}")
    return candidate


def read_asset_manifest(root: Path) -> dict:
    path = root / MANIFEST_NAME
    if not path.exists():
        return {"version": 1, "sources": {}, "tasks": {}}
    data = json.loads(path.read_text(encoding="utf-8"))
    if (
        not isinstance(data, dict)
        or data.get("version") != 1
        or not isinstance(data.get("sources"), dict)
        or not isinstance(data.get("tasks"), dict)
    ):
        raise ValueError(f"Invalid setup asset manifest: {path}")
    for source, relative in data["sources"].items():
        if not isinstance(source, str) or not source:
            raise ValueError(f"Invalid source in {path}")
        manifest_path(root, relative)
    for task, sources in data["tasks"].items():
        if (
            not isinstance(task, str)
            or not isinstance(sources, list)
            or any(not isinstance(source, str) or not source for source in sources)
        ):
            raise ValueError(f"Invalid task dependency list in {path}")
    return data


def snapshot_source(url: str) -> Path | None:
    parsed = urlparse(url)
    if parsed.scheme not in ("http", "https"):
        return None
    for variable, repo in (
        ("OSWORLD_V1_ASSET_ROOT", "ubuntu_osworld_file_cache"),
        ("OSWORLD_FILE_BASE_URL", "osworld_v2_assets"),
    ):
        prefix = f"/datasets/xlangai/{repo}/resolve/main/"
        if parsed.hostname == "huggingface.co" and parsed.path.startswith(prefix):
            root = local_root(variable)
            if root is not None:
                return manifest_path(root, unquote(parsed.path[len(prefix):]))
    return None


def resolve_local_source(url: str) -> str | None:
    """Resolve existing files/dirs; never perform a network request.

    Kept compatible with upstream evaluators that also use this helper.
    Setup uses ``require_setup_source`` to additionally require a readable file.
    """
    if not url:
        return None
    if url.startswith("file://"):
        parsed = urlparse(url)
        if parsed.netloc not in ("", "localhost"):
            raise ValueError("Asset file URI must be local")
        path = Path(unquote(parsed.path))
        return str(path.resolve()) if path.exists() else None
    if "://" not in url:
        for raw in (url, unquote(url)):
            path = Path(raw).expanduser()
            if path.exists():
                return str(path.resolve())
        return None
    # Exact manifest bindings take precedence over conventional HF layout.
    matches = set()
    variables = {
        "v1": ("OSWORLD_V1_ASSET_ROOT",),
        "v2": ("OSWORLD_FILE_BASE_URL",),
    }.get(_asset_benchmark, ("OSWORLD_V1_ASSET_ROOT", "OSWORLD_FILE_BASE_URL"))
    for variable in variables:
        try:
            root = local_root(variable) if os.environ.get(variable) else None
        except ValueError:
            # Preflight validates the selected benchmark's root. An unrelated
            # benchmark's URL base must not block local V1 snapshot resolution.
            continue
        if root is None or not root.is_dir():
            continue
        relative = read_asset_manifest(root)["sources"].get(url)
        if relative is not None:
            matches.add(manifest_path(root, relative))
    if len(matches) > 1:
        raise ValueError(f"Conflicting local bindings for setup asset: {url}")
    candidate = next(iter(matches)) if matches else snapshot_source(url)
    return str(candidate) if candidate is not None and candidate.exists() else None


def require_setup_source(source: str) -> str:
    if _setup_bindings is not None:
        local = _setup_bindings.get(source)
        if local is None:
            # Manifests may spell a local file as a file URI or snapshot path.
            resolved = resolve_local_source(source)
            admitted = {Path(path).resolve() for path in _setup_bindings.values()}
            if resolved is not None and Path(resolved).resolve() in admitted:
                local = resolved
            else:
                raise ValueError(
                    f"Undeclared setup asset: {source}. Add the exact dependency "
                    "to this task's setup-assets.json entry and rerun preflight."
                )
    else:
        local = resolve_local_source(source)
    if local is None or not Path(local).is_file():
        raise FileNotFoundError(
            f"Setup asset is not a local file: {source}. "
            "Setup network fallback is disabled. Prepare the selected task's "
            "assets with python -m hcu.osworld.prepare_assets before running."
        )
    with open(local, "rb"):
        pass
    return local


def get_base_url() -> str:
    root = local_root("OSWORLD_FILE_BASE_URL")
    return str(root) if root is not None else DEFAULT_BASE_URL


def asset(path: str) -> str:
    if not isinstance(path, str) or not path or path.startswith("/"):
        raise ValueError("asset() requires a nonempty relative path")
    if ".." in Path(unquote(path)).parts:
        raise ValueError("asset() path must not escape its root")
    base = get_base_url()
    return f"{base.rstrip('/')}/{path}"
