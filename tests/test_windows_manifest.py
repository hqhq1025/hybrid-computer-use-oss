import json
import os
from pathlib import Path
import shutil
import subprocess
import xml.etree.ElementTree as ET

import pytest


ROOT = Path(__file__).resolve().parents[1]
PROJECT = ROOT / "computer-use/native/windows/ComputerUse.Windows.Runtime"
DOTNET = os.environ.get("DOTNET_TEST_BINARY") or shutil.which("dotnet")


@pytest.mark.skipif(not DOTNET, reason=".NET SDK unavailable")
@pytest.mark.parametrize("version", [None, "2.3.4-rc.1"])
def test_manifest_uses_build_version_without_modifying_template(tmp_path, version):
    template = PROJECT / "app.manifest"
    original = template.read_bytes()
    command = [
        DOTNET, "msbuild", str(PROJECT / "ComputerUse.Windows.Runtime.csproj"),
        "-nologo", "-target:GenerateVersionedApplicationManifest",
        f"-property:IntermediateOutputPath={tmp_path}{os.sep}",
        "-getProperty:ApplicationManifest,AssemblyVersion,Version",
    ]
    if version:
        command.append(f"-property:Version={version}")
    result = subprocess.run(
        command, capture_output=True, text=True, timeout=60, check=True,
    )
    properties = json.loads(result.stdout)["Properties"]
    expected = version or (ROOT / "VERSION").read_text().strip()
    assert properties["Version"] == expected
    manifest = Path(properties["ApplicationManifest"])
    assert manifest.is_relative_to(tmp_path)
    identity = ET.parse(manifest).find("{urn:schemas-microsoft-com:asm.v1}assemblyIdentity")
    assert identity is not None
    assert identity.attrib["version"] == expected.split("-")[0] + ".0"
    assert identity.attrib["version"] == properties["AssemblyVersion"]
    assert template.read_bytes() == original
