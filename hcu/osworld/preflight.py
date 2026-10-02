from __future__ import annotations

import os
from pathlib import Path

import docker


DEDICATED_DOCKER_HOST = os.environ.get(
    "OSWORLD_V2_DOCKER_HOST",
    os.environ.get("DOCKER_HOST", "unix:///var/run/docker.sock"),
)
DEDICATED_DOCKER_ROOT = os.environ.get("OSWORLD_V2_DOCKER_ROOT", "")


def validate_official_asset_runtime(benchmark_version: str, task) -> dict:
    """Validate only the selected task, before creating a guest.

    This is a setup-asset contract, not a whole-task offline guarantee.
    Browser navigation, application APIs and evaluator networking are separate.
    """
    from desktop_env.file_source import (
        bind_setup_assets,
        local_root,
        manifest_path,
        read_asset_manifest,
        require_setup_source,
        resolve_local_source,
    )
    from hcu.osworld.assets import task_asset_sources
    from hcu.osworld.task import task_value

    # A process can validate several tasks in tests/preparation; never reuse
    # the preceding task's bindings while resolving the next one.
    bind_setup_assets(None, benchmark_version=benchmark_version)
    sources, unresolved = task_asset_sources(task, benchmark_version)
    variable = (
        "OSWORLD_V1_ASSET_ROOT" if benchmark_version == "v1"
        else "OSWORLD_FILE_BASE_URL"
    )
    root = local_root(variable)
    task_id = str(task_value(task, "id", ""))
    if not task_id:
        raise ValueError("Asset preflight requires the selected task id")
    manifest = read_asset_manifest(root) if root is not None else {"tasks": {}}
    declared = manifest["tasks"].get(f"{benchmark_version}:{task_id}")
    if declared is not None:
        sources.extend(
            str(manifest_path(root, value)) if "://" not in value and not Path(value).is_absolute()
            else value
            for value in declared
        )
    if unresolved and declared is None:
        raise RuntimeError(
            f"Task {task_id} has dynamic setup assets ({', '.join(unresolved)}). "
            f"Declare its exact sources in {variable}/setup-assets.json "
            f'under tasks["{benchmark_version}:{task_id}"].'
        )
    bindings = {}
    directories = []
    for source in dict.fromkeys(sources):
        local = resolve_local_source(source)
        if local is not None and Path(local).is_dir():
            directories.append(local)
            if declared is None:
                raise RuntimeError(
                    f"Task {task_id} declares an asset directory, not exact files: {source}. "
                    f'Provide tasks["{benchmark_version}:{task_id}"] in '
                    f"{variable}/setup-assets.json; directory existence is not completeness."
                )
            continue
        bindings[source] = require_setup_source(source)
    bind_setup_assets(bindings)
    return {
        "benchmark_version": benchmark_version,
        "task_id": task_id,
        "policy": "local-setup-assets-only",
        "asset_root": str(root) if root is not None else None,
        "manifest_task_entry": declared is not None,
        "dynamic_declarations": unresolved,
        "asset_directories": directories,
        "assets": [
            {"source": source, "local_path": path, "size_bytes": Path(path).stat().st_size}
            for source, path in bindings.items()
        ],
        "network_scope": "setup asset retrieval only; task websites and APIs remain available",
    }


def validate_dedicated_docker_runtime() -> dict:
    docker_host = os.environ.get("DOCKER_HOST", "")
    require_dedicated = (
        os.environ.get("OSWORLD_V2_REQUIRE_DEDICATED_DOCKER", "0") == "1"
    )
    if require_dedicated and docker_host != DEDICATED_DOCKER_HOST:
        raise RuntimeError(
            "OSWorld V2 requires the dedicated Docker daemon. "
            f"Expected DOCKER_HOST={DEDICATED_DOCKER_HOST}, "
            f"got {docker_host or '<unset>'}. "
            "Load your environment file (see configs/env.example) before "
            "launching the runner."
        )

    socket_path = Path(
        (docker_host or DEDICATED_DOCKER_HOST).removeprefix("unix://")
    )
    if (
        (docker_host or DEDICATED_DOCKER_HOST).startswith("unix://")
        and not socket_path.is_socket()
    ):
        raise RuntimeError(
            f"Dedicated OSWorld V2 Docker socket is unavailable: {socket_path}"
        )

    client = docker.from_env(timeout=30)
    try:
        info = client.info()
    finally:
        client.close()
    docker_root = str(info.get("DockerRootDir", ""))
    if (
        require_dedicated
        and DEDICATED_DOCKER_ROOT
        and docker_root != DEDICATED_DOCKER_ROOT
    ):
        raise RuntimeError(
            "Docker daemon identity mismatch. "
            f"Expected DockerRootDir={DEDICATED_DOCKER_ROOT}, "
            f"got {docker_root or '<missing>'}."
        )
    return {
        "docker_host": docker_host,
        "docker_root": docker_root,
        "server_version": info.get("ServerVersion"),
    }
