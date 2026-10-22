"""Check selected package validation and installation boundaries without apt."""

from pathlib import Path
import tempfile
import unittest
from unittest import mock

import install_wps


class WPSInstallerTests(unittest.TestCase):
    def setUp(self):
        self.metadata = {"Package": "wps-office", "Version": "selected", "Architecture": "amd64"}
        self.manifest = {"package": self.metadata}

    def test_rejects_mutable_download_with_another_version(self):
        with mock.patch.object(install_wps, "run", side_effect=["wps-office", "newer", "amd64"]):
            with self.assertRaisesRegex(ValueError, "metadata differs"):
                install_wps.inspect_package(Path("wps.deb"), self.manifest)

    def test_check_runs_only_dependency_simulation(self):
        with tempfile.NamedTemporaryFile(suffix=".deb") as package, \
                mock.patch.object(install_wps, "inspect_package", return_value=self.metadata), \
                mock.patch.object(install_wps, "run", side_effect=["", "dependency plan"]) as run:
            result = install_wps.install(Path(package.name), self.manifest, check=True)
            self.assertFalse(result["installation_performed"])
            self.assertIn("--simulate", run.call_args_list[-1].args[0])
            self.assertIn("--no-remove", run.call_args_list[-1].args[0])
            self.assertEqual(run.call_count, 2)

    def test_installed_version_must_match_selection(self):
        with tempfile.NamedTemporaryFile(suffix=".deb") as package, \
                mock.patch.object(install_wps, "inspect_package", return_value=self.metadata), \
                mock.patch.object(install_wps.os, "geteuid", return_value=0), \
                mock.patch.object(install_wps, "run", side_effect=[
                    "", "dependency plan", "installed", "", "different\tinstall ok installed"]):
            with self.assertRaisesRegex(RuntimeError, "selected installed state"):
                install_wps.install(Path(package.name), self.manifest)


if __name__ == "__main__":
    unittest.main()
