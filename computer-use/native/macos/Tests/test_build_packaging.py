import os
from pathlib import Path
import plistlib
import shutil
import subprocess
import tempfile
import unittest


RUNTIME = Path(__file__).resolve().parents[1]
REPO = RUNTIME.parents[2]
MOCK = """#!/usr/bin/env python3
import os
from pathlib import Path
import sys
name = Path(sys.argv[0]).name
args = sys.argv[1:]
if name == "swift":
    output = Path(os.environ["MOCK_SWIFT_OUTPUT"])
    output.mkdir(parents=True, exist_ok=True)
    for filename in ["computer-use-macos-provider", "libComputerUseProviderKit.dylib"]:
        (output / filename).write_text("mock compiler output")
    if "--show-bin-path" in args:
        print(output)
elif name in ["swiftc", "go"] and "-o" in args:
    output = Path(args[args.index("-o") + 1])
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text("mock compiler output")
"""


class MacPackagingTests(unittest.TestCase):
    def test_native_drivers_include_full_license_tree(self):
        for driver in ("swiftc", "swiftpm"):
            with self.subTest(driver=driver), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                repo = root / "repo with spaces"
                runtime = repo / "computer-use/native/macos"
                runtime.mkdir(parents=True)
                for name in ("build.sh", "ProviderInfo.plist"):
                    shutil.copyfile(RUNTIME / name, runtime / name)
                for name in ("LICENSE", "THIRD_PARTY_NOTICES.md", "VERSION"):
                    shutil.copyfile(REPO / name, repo / name)
                shutil.copytree(REPO / "licenses", repo / "licenses")
                nested = repo / "licenses/nested/additional.txt"
                nested.parent.mkdir()
                nested.write_text("nested license content\n")
                vendor = Path("computer-use/vendor/golang.org/x/text")
                (repo / vendor).mkdir(parents=True)
                for name in ("LICENSE", "PATENTS"):
                    shutil.copyfile(REPO / vendor / name, repo / vendor / name)
                tools = root / "tools"
                tools.mkdir()
                mock = tools / "mock"
                mock.write_text(MOCK)
                mock.chmod(0o755)
                for name in ("swift", "swiftc", "go", "codesign", "plutil", "install_name_tool"):
                    (tools / name).symlink_to(mock)
                output = root / "packaged output"
                env = {
                    **os.environ,
                    "PATH": str(tools) + os.pathsep + os.environ["PATH"],
                    "GOARCH": "arm64",
                    "COMPUTER_USE_MACOS_BUILD_DRIVER": driver,
                    "COMPUTER_USE_SWIFTC": str(tools / "swiftc"),
                    "COMPUTER_USE_MACOS_SDK": "/mock/sdk",
                    "MOCK_SWIFT_OUTPUT": str(root / "compiler output"),
                }
                subprocess.run(["bash", str(runtime / "build.sh"), str(output)],
                               env=env, check=True, capture_output=True, timeout=30)
                with (runtime / "ProviderInfo.plist").open("rb") as stream:
                    expected_identity = plistlib.load(stream)["CFBundleIdentifier"]
                with (output / "ComputerUseProvider.app/Contents/Info.plist").open("rb") as stream:
                    self.assertEqual(plistlib.load(stream)["CFBundleIdentifier"], expected_identity)
                for source in (repo / "licenses").rglob("*"):
                    if source.is_file():
                        self.assertEqual(source.read_bytes(), (output / source.relative_to(repo)).read_bytes())
                self.assertTrue((output / "licenses/Apache-2.0.txt").is_file())
                for name in ("LICENSE", "PATENTS"):
                    self.assertEqual((repo / vendor / name).read_bytes(),
                                     (output / "licenses/golang.org/x/text" / name).read_bytes())

    def test_direct_compiler_includes_gui_helpers(self):
        script = (RUNTIME / "build.sh").read_text()
        for source in ("GUIDrag.swift", "GUIStillness.swift"):
            self.assertIn("/Sources/ComputerUseProviderKit/" + source, script)


if __name__ == "__main__":
    unittest.main()
