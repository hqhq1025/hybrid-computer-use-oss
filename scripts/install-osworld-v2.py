#!/usr/bin/env python3
from __future__ import annotations

import argparse
import shutil
import subprocess
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
TESTED_COMMIT = "d578d2d4e0dc82b43e270fdaa7fa89d9708cd154"


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("osworld_v2_root", type=Path)
    parser.add_argument(
        "--force",
        action="store_true",
        help="Install on a checkout other than the tested commit.",
    )
    args = parser.parse_args()
    checkout = args.osworld_v2_root.expanduser().resolve()
    head = subprocess.run(
        ["git", "-C", str(checkout), "rev-parse", "HEAD"],
        check=True,
        capture_output=True,
        text=True,
    ).stdout.strip()
    if head != TESTED_COMMIT and not args.force:
        raise SystemExit(
            f"OSWorld V2 HEAD is {head}; tested {TESTED_COMMIT}. "
            "Review the overlay diff, then rerun with --force."
        )

    overlay = ROOT / "integrations/osworld-v2/overlay"
    for source in overlay.rglob("*"):
        if not source.is_file():
            continue
        destination = checkout / source.relative_to(overlay)
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(source, destination)
    cdp_source = (
        ROOT / "integrations/osworld-v2/patches/desktop_env/cdp.py"
    )
    shutil.copy2(cdp_source, checkout / "desktop_env/cdp.py")
    deploy_target = checkout / "deploy/hybrid"
    deploy_target.mkdir(parents=True, exist_ok=True)
    shutil.copy2(
        ROOT / "integrations/osworld-v2/hybrid/host_pressure.py",
        deploy_target / "host_pressure.py",
    )
    print(f"OSWorld V2 runtime overlay installed into {checkout}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
