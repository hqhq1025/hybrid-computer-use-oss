#!/usr/bin/env python3
from __future__ import annotations

import argparse
import shutil
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def patch_provider_factory(path: Path) -> None:
    text = path.read_text(encoding="utf-8")
    if 'provider_name == "hybrid"' in text:
        return
    marker = '''    elif provider_name == "docker":
        from desktop_env.providers.docker.manager import DockerVMManager
        from desktop_env.providers.docker.provider import DockerProvider
        return DockerVMManager(), DockerProvider(region)
'''
    replacement = marker + '''    elif provider_name == "hybrid":
        from desktop_env.providers.hybrid.manager import HybridVMManager
        from desktop_env.providers.hybrid.provider import HybridProvider
        return HybridVMManager(), HybridProvider(region)
'''
    if marker not in text:
        raise RuntimeError(f"cannot find Docker provider block in {path}")
    path.write_text(text.replace(marker, replacement, 1), encoding="utf-8")


def patch_desktop_env(path: Path) -> None:
    text = path.read_text(encoding="utf-8")
    if '"hybrid"' in text:
        return
    candidates = (
        '{"docker", "aws", "gcp", "azure", "aliyun", "volcengine"}',
        '{"docker", "aws", "gcp", "azure"}',
    )
    for marker in candidates:
        if marker in text:
            replacement = marker[:-1] + ', "hybrid"}'
            path.write_text(
                text.replace(marker, replacement, 1),
                encoding="utf-8",
            )
            return
    raise RuntimeError(f"cannot find clean-provider set in {path}")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("osworld_root", type=Path)
    args = parser.parse_args()
    checkout = args.osworld_root.expanduser().resolve()
    target = checkout / "desktop_env/providers/hybrid"
    target.mkdir(parents=True, exist_ok=True)
    for source in (ROOT / "integrations/osworld-v1/provider/hybrid").glob("*.py"):
        shutil.copy2(source, target / source.name)
    patch_provider_factory(checkout / "desktop_env/providers/__init__.py")
    patch_desktop_env(checkout / "desktop_env/desktop_env.py")
    print(f"OSWorld V1 hybrid provider installed into {checkout}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
