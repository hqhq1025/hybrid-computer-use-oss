"""Cross-file macOS identity checks runnable without the Apple SDK."""

from pathlib import Path
import plistlib
import re


MACOS = Path(__file__).resolve().parents[1] / "computer-use/native/macos"
KIT = MACOS / "Sources/ComputerUseProviderKit"


def provider_identity():
    with (MACOS / "ProviderInfo.plist").open("rb") as stream:
        return plistlib.load(stream)["CFBundleIdentifier"]


def test_packaged_identity_matches_permissions_and_self_exclusion():
    identity = provider_identity()
    assert identity == "org.computer-use.provider.v2"
    permissions = (KIT / "Permissions.swift").read_text()
    client = re.search(r"providerBundleIdentifier\s*=\s*\"([^\"]+)\"", permissions)
    assert client is not None
    assert client.group(1) == identity
    discovery = (KIT / "AppDiscovery.swift").read_text()
    exclusions = re.search(
        r"internalProviderBundleIdentifiers:\s*Set<String>\s*=\s*\[([^]]*)\]",
        discovery,
    )
    assert exclusions is not None
    assert re.findall(r"\"([^\"]+)\"", exclusions.group(1)) == [identity]


def test_logger_subsystems_share_the_packaged_provider_namespace():
    namespace, version = provider_identity().rsplit(".", 1)
    assert version == "v2"
    subsystems = [
        (path.name, subsystem)
        for path in KIT.glob("*.swift")
        for subsystem in re.findall(r"subsystem:\s*\"([^\"]+)\"", path.read_text())
    ]
    assert subsystems, "No logger subsystems found"
    assert all(value == namespace for _, value in subsystems), subsystems


def test_project_fixture_ids_are_neutral_and_discovery_uses_packaged_identity():
    identity = provider_identity()
    discovery = (MACOS / "Tests/ComputerUseProviderKitTests/AppDiscoveryWaitTests.swift").read_text()
    fixture_ids = re.findall(r"\"([^\"]*computer-use[^\"]*)\"", discovery)
    assert fixture_ids
    assert set(fixture_ids) == {identity}
    for root in (MACOS / "Sources", MACOS / "Tests"):
        for path in root.rglob("*.swift"):
            for value in re.findall(r'bundleIdentifier:\s*"([^"]+)"', path.read_text()):
                if "computer-use" in value:
                    assert value.startswith("org.computer-use."), path.relative_to(MACOS)
