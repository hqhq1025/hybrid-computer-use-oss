#!/usr/bin/env python3
"""Install an explicitly selected upstream WPS package without copying profiles."""

import argparse
import json
import os
from pathlib import Path
import subprocess


def run(command, timeout=60):
    result = subprocess.run(command, capture_output=True, text=True, timeout=timeout,
                            env={**os.environ, "DEBIAN_FRONTEND": "noninteractive"})
    if result.returncode:
        raise RuntimeError(f"{command[0]} failed ({result.returncode}):\n{result.stdout}\n{result.stderr}")
    return result.stdout


def inspect_package(package, manifest):
    metadata = {field: run(["dpkg-deb", "-f", str(package), field]).strip()
                for field in ("Package", "Version", "Architecture")}
    if metadata != manifest["package"]:
        raise ValueError("WPS package metadata differs from the selected manifest: " + json.dumps(metadata))
    architecture = run(["dpkg", "--print-architecture"]).strip()
    if metadata["Architecture"] != architecture:
        raise ValueError("WPS package architecture differs from the guest")
    return metadata


def install(package, manifest, check=False):
    package = package.resolve(strict=True)
    metadata = inspect_package(package, manifest)
    if run(["dpkg", "--audit"]).strip():
        raise RuntimeError("Resolve existing dpkg errors before installing WPS")
    plan = run(["apt-get", "--simulate", "--no-remove", "install", str(package)], timeout=180)
    if check:
        return {"package": metadata, "installation_performed": False, "dependency_plan": plan}
    if os.geteuid() != 0:
        raise RuntimeError("Run installation as root inside the guest")
    run(["apt-get", "--no-remove", "-y", "install", str(package)], timeout=600)
    if run(["dpkg", "--audit"]).strip():
        raise RuntimeError("dpkg reports incomplete packages after WPS installation")
    observed = run(["dpkg-query", "-W", "-f=${Version}\t${Status}", metadata["Package"]]).strip()
    if observed != metadata["Version"] + "\tinstall ok installed":
        raise RuntimeError("WPS package did not reach the selected installed state: " + observed)
    return {"package": metadata, "installation_performed": True, "dependency_plan": plan}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--package", type=Path, required=True, help="Downloaded upstream WPS .deb")
    parser.add_argument("--manifest", type=Path, default=Path(__file__).with_name("wps.json"))
    parser.add_argument("--check", action="store_true", help="Validate metadata and simulate dependencies only")
    args = parser.parse_args()
    print(json.dumps(install(args.package, json.loads(args.manifest.read_text()), args.check), indent=2))
