from __future__ import annotations

import os
from pathlib import Path

import docker


DEDICATED_DOCKER_HOST = os.environ.get(
    "OSWORLD_V2_DOCKER_HOST",
    os.environ.get("DOCKER_HOST", "unix:///var/run/docker.sock"),
)
DEDICATED_DOCKER_ROOT = os.environ.get("OSWORLD_V2_DOCKER_ROOT", "")


def validate_official_asset_runtime() -> dict:
    raw_asset_root = os.environ.get("OSWORLD_FILE_BASE_URL", "")
    if not raw_asset_root:
        raise RuntimeError(
            "OSWorld V2 requires a local gated asset snapshot. "
            "OSWORLD_FILE_BASE_URL is unset. "
            "Source deploy/osworld-v2.env before launching the runner."
        )
    if "://" in raw_asset_root:
        raise RuntimeError(
            "OSWorld V2 formal runs must not resolve task assets online. "
            "Set OSWORLD_FILE_BASE_URL to the absolute local gated snapshot, "
            f"got {raw_asset_root}."
        )

    asset_root = Path(raw_asset_root).expanduser()
    if not asset_root.is_absolute():
        raise RuntimeError(
            "OSWORLD_FILE_BASE_URL must be an absolute local path, "
            f"got {raw_asset_root}."
        )
    asset_root = asset_root.resolve()
    if not asset_root.is_dir():
        raise RuntimeError(
            f"Local OSWorld V2 asset snapshot is unavailable: {asset_root}"
        )
    if not (asset_root / "task_001").is_dir():
        raise RuntimeError(
            "Local OSWorld V2 asset snapshot is incomplete or points at the "
            f"wrong directory: {asset_root}"
        )
    return {"asset_root": str(asset_root)}


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
            "Source deploy/osworld-v2.env before launching the runner."
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
