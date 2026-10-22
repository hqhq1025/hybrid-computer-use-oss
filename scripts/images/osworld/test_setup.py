"""Check source repairs and profile preservation without changing the host."""

import configparser
import json
from pathlib import Path
import tempfile
import tarfile
import unittest
from unittest import mock

import configure_desktop
import configure_editor
import install
import install_qt


class SetupTests(unittest.TestCase):
    def test_spyder_repair_and_repeat_preserve_original(self):
        original = "image.scaled(image_width, image_height, Qt.KeepAspectRatio)\nsize = QSize(image_width, image_height)\n"
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "widget.py"
            path.write_text(original)
            result = install_qt.repair_spyder_file(path)
            self.assertEqual(result["calls_changed"], 2)
            self.assertIn("QSize(int(image_width), int(image_height))", path.read_text())
            self.assertEqual(path.with_suffix(".py.hcu-infra-original").read_text(), original)
            self.assertEqual(install_qt.repair_spyder_file(path)["spyder"], "already repaired")
            self.assertEqual(path.with_suffix(".py.hcu-infra-original").read_text(), original)

    def test_spyder_unknown_source_is_unchanged(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "widget.py"
            path.write_text("image.resize(width, height)\n")
            with self.assertRaisesRegex(RuntimeError, "Unrecognized"):
                install_qt.repair_spyder_file(path)
            self.assertEqual(path.read_text(), "image.resize(width, height)\n")

    def test_editor_preserves_settings_and_rejects_unparseable_profile(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            path = home / ".config/Code/User/settings.json"
            path.parent.mkdir(parents=True)
            path.write_text('{"editor.fontSize": 16}')
            configure_editor.configure(home)
            self.assertEqual(json.loads(path.read_text()), {
                "editor.fontSize": 16, "editor.accessibilitySupport": "on"})
            path.write_text("// JSONC profile\n{}")
            with self.assertRaises(json.JSONDecodeError):
                configure_editor.configure(home)
            self.assertEqual(path.read_text(), "// JSONC profile\n{}")

    def test_desktop_preferences_preserve_other_values(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            path = home / ".config/REAPER/reaper.ini"
            path.parent.mkdir(parents=True)
            path.write_text("[reaper]\nlinux_audio_mode=1\ncustomValue=keep\n")
            configure_desktop.configure(home, {"musescore_skipped_version": "selected"})
            parser = configparser.ConfigParser()
            parser.read(path)
            self.assertEqual(parser["reaper"]["customValue"], "keep")
            self.assertEqual(parser["reaper"]["linux_audio_mode"], "3")

    def test_preflight_rejects_missing_archive_before_installation(self):
        manifest = {"distribution": "ubuntu", "ubuntu_version": "22.04",
                    "architecture": "amd64", "joplin": {"file": "missing.AppImage"}}
        with tempfile.TemporaryDirectory() as directory, \
                mock.patch.object(install.platform, "freedesktop_os_release", return_value={
                    "ID": "ubuntu", "VERSION_ID": "22.04"}), \
                mock.patch.object(install.subprocess, "check_output", return_value="amd64\n"):
            with self.assertRaisesRegex(ValueError, "Missing asset"):
                install.preflight(manifest, Path(directory), ["joplin"])

    def test_freecad_rejects_other_package_selection_before_installation(self):
        with tempfile.TemporaryDirectory() as directory, \
                mock.patch.object(install_qt.subprocess, "run") as run:
            root = Path(directory)
            manifest = root / "manifest.json"
            manifest.write_text(json.dumps({"freecad_version": "different"}))
            archive = root / "freecad.tar"
            with tarfile.open(archive, "w") as output:
                output.add(manifest, arcname="manifest.json")
            with self.assertRaisesRegex(RuntimeError, "metadata differs"):
                install_qt.install_freecad(archive, {"freecad_version": "selected"})
            run.assert_not_called()


if __name__ == "__main__":
    unittest.main()
