"""Prepare/check selected setup assets without starting Docker or an agent.

Example (after installing the OSWorld overlay):
  python -m hcu.osworld.prepare_assets --benchmark-version v1 \
    --task-id TASK_UUID --asset-root /data/v1-assets --download

For Python tasks whose dependencies are computed at runtime, --sources-file
accepts a JSON list of exact source URLs or root-relative local filenames.
It supplies the task inventory, not a recipe for solving the task.
"""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import tempfile
from urllib.parse import urlparse

import requests

from hcu.paths import OSWORLD_ROOT
from hcu.osworld.assets import task_asset_sources
from hcu.osworld.batch_config import normalized_tasks, validate_config
from hcu.osworld.preflight import validate_official_asset_runtime


def download_asset(source: str, destination: Path) -> None:
    """Bounded HTTP transfer; publish only after the complete body is received."""
    destination.parent.mkdir(parents=True, exist_ok=True)
    temporary = None
    try:
        with requests.get(source, stream=True, timeout=(15, 120)) as response:
            response.raise_for_status()
            with tempfile.NamedTemporaryFile(dir=destination.parent, delete=False) as stream:
                temporary = Path(stream.name)
                size = 0
                for chunk in response.iter_content(chunk_size=1024 * 1024):
                    stream.write(chunk)
                    size += len(chunk)
            length = response.headers.get("Content-Length")
            # requests transparently decompresses Content-Encoding bodies.
            if length is not None and not response.headers.get("Content-Encoding") and size != int(length):
                raise ValueError(f"Incomplete setup asset response: {source}")
            os.replace(temporary, destination)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def write_manifest(root: Path, manifest: dict) -> None:
    from desktop_env.file_source import MANIFEST_NAME

    root.mkdir(parents=True, exist_ok=True)
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(mode="w", dir=root, delete=False, encoding="utf-8") as stream:
            temporary = Path(stream.name)
            json.dump(manifest, stream, indent=2)
            stream.write("\n")
        os.replace(temporary, root / MANIFEST_NAME)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def prepare_task_assets(task, version: str, root: Path, *, download: bool, sources=None) -> dict:
    from desktop_env.file_source import (
        bind_setup_assets,
        manifest_path,
        read_asset_manifest,
        resolve_local_source,
        snapshot_source,
    )
    from hcu.osworld.task import task_value

    root = root.expanduser().resolve()
    variable = "OSWORLD_V1_ASSET_ROOT" if version == "v1" else "OSWORLD_FILE_BASE_URL"
    os.environ[variable] = str(root)
    bind_setup_assets(None, benchmark_version=version)
    manifest = read_asset_manifest(root)
    key = f"{version}:{task_value(task, 'id')}"
    if sources is not None:
        if not isinstance(sources, list) or any(not isinstance(item, str) or not item for item in sources):
            raise ValueError("--sources-file must contain a JSON list of nonempty source strings")
        manifest["tasks"][key] = sources
    known, unresolved = task_asset_sources(task, version)
    declared = manifest["tasks"].get(key)
    if unresolved and declared is None:
        raise ValueError(
            f"Dynamic dependencies for {key}: {', '.join(unresolved)}. "
            "Supply --sources-file with the exact setup source list."
        )
    all_sources = list(known)
    if declared is not None:
        all_sources.extend(
            str(manifest_path(root, value)) if "://" not in value and not Path(value).is_absolute()
            else value
            for value in declared
        )
    missing = []
    local_paths = {}
    for source in dict.fromkeys(all_sources):
        local = resolve_local_source(source)
        if local is not None:
            if Path(local).is_dir() and declared is None:
                raise ValueError(
                    f"{source} is a directory; use --sources-file to inventory exact files."
                )
            local_paths[source] = local
            continue
        parsed = urlparse(source)
        if parsed.scheme not in ("http", "https") or not download:
            missing.append(source)
            continue
        destination = snapshot_source(source)
        if destination is None or not destination.is_relative_to(root):
            relative = manifest["sources"].get(source)
            if relative is None:
                index = len(manifest["sources"]) + 1
                relative = f"downloads/{index:06d}/asset"
                while (root / relative).exists() or relative in manifest["sources"].values():
                    index += 1
                    relative = f"downloads/{index:06d}/asset"
                manifest["sources"][source] = relative
            destination = manifest_path(root, relative)
        download_asset(source, destination)
        # Keep completed transfers reusable if a later asset fails.
        write_manifest(root, manifest)
    if missing:
        raise FileNotFoundError(
            "Missing selected setup assets (use --download for HTTP sources):\n"
            + "\n".join(missing)
        )
    if declared is None:
        # Capture the resolved file, including percent-decoded task paths.
        # Manifest-relative paths otherwise use the asset root, not the checkout.
        manifest["tasks"][key] = [
            local_paths[source] if "://" not in source else source
            for source in known
        ]
    # Atomic metadata, with no content-hash admission requirement.
    write_manifest(root, manifest)
    return validate_official_asset_runtime(version, task)


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    selection = parser.add_mutually_exclusive_group(required=True)
    selection.add_argument("--task-id")
    selection.add_argument("--config", type=Path, help="Prepare every task in batch configuration order")
    parser.add_argument("--benchmark-version", choices=("v1", "v2"))
    parser.add_argument("--task-domain")
    parser.add_argument("--asset-root", type=Path, required=True)
    parser.add_argument("--sources-file", type=Path, help="JSON array of exact task asset sources")
    parser.add_argument("--download", action="store_true", help="Explicitly allow HTTP asset preparation")
    args = parser.parse_args(argv)
    try:
        return _prepare(args, parser)
    except (OSError, ValueError) as exc:
        parser.exit(1, f"{parser.prog}: error: {exc}\n")


def _prepare(args: argparse.Namespace, parser: argparse.ArgumentParser) -> int:
    if args.config is not None:
        if args.sources_file is not None or args.benchmark_version is not None or args.task_domain is not None:
            parser.error("--config cannot be combined with --sources-file, --benchmark-version or --task-domain")
        config_path = args.config.expanduser().resolve()
        config = json.loads(config_path.read_text(encoding="utf-8"))
        validate_config(config)
        version = str(config.get("benchmark_version") or "v2")
        selected = [
            {"id": entry["id"], "task_domain": entry.get("task_domain", config.get("task_domain"))}
            for entry in normalized_tasks(config)
        ]
    else:
        if args.benchmark_version is None:
            parser.error("--task-id requires --benchmark-version")
        version = args.benchmark_version
        selected = [{"id": args.task_id, "task_domain": args.task_domain}]
    root = args.asset_root.expanduser().resolve()
    sources = (
        json.loads(args.sources_file.read_text(encoding="utf-8"))
        if args.sources_file is not None else None
    )
    variable = "OSWORLD_V1_ASSET_ROOT" if version == "v1" else "OSWORLD_FILE_BASE_URL"
    os.environ[variable] = str(root)
    from hcu.osworld.task_loader import load_task_config

    os.chdir(OSWORLD_ROOT)
    import fcntl

    root.mkdir(parents=True, exist_ok=True)
    with (root / ".setup-assets.lock").open("a") as lock:
        # OSWorld orchestration is Linux-only. Do not let concurrent preparers
        # overwrite one another's source map or allocate the same filename.
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        reports = []
        for entry in selected:
            try:
                task = load_task_config(
                    None,
                    task_id=entry["id"],
                    base_dir=str(OSWORLD_ROOT / "evaluation_examples"),
                    domain=entry["task_domain"],
                    eval_version=version,
                    prefer_class=version == "v2",
                )
                report = prepare_task_assets(
                    task, version, root, download=args.download, sources=sources,
                )
                reports.append({**report, "task_domain": entry["task_domain"], "status": "prepared"})
            except Exception as exc:
                if args.config is None:
                    raise
                reports.append({
                    "task_id": entry["id"],
                    "task_domain": entry["task_domain"],
                    "status": "failed",
                    "error": f"{type(exc).__name__}: {exc}",
                })
    if args.config is not None:
        failed = [entry["task_id"] for entry in reports if entry["status"] == "failed"]
        print(json.dumps({
            "config": str(config_path),
            "benchmark_version": version,
            "asset_root": str(root),
            "task_count": len(selected),
            "failed_task_ids": failed,
            "tasks": reports,
        }, indent=2))
        return 1 if failed else 0
    print(json.dumps(report, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
