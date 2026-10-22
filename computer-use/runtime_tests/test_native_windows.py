#!/usr/bin/env python3
"""Windows of processes that export no accessibility tree."""

from runtime_test_support import *  # noqa: F401,F403


def _managed(*windows):
    return runtime.InventoryResult(list(windows), complete=True)


def _window(pid, window_id, title, x=0, y=0, width=800, height=600):
    return {"pid": pid, "id": window_id, "title": title, "x": x, "y": y,
            "width": width, "height": height}


class NativeOnlyWindowTests(AtspiPatchedTestCase):
    """A process that never registers on the bus still has managed windows.

    Without them in the listing, AX could not activate, close or wait for
    such a window by title (window_not_found, wait_timeout), although the
    window was on screen and GUI could operate it.
    """

    def setUp(self):
        super().setUp()
        for name, value in (
            ("x11_window_type", lambda window_id: (
                "DOCK" if window_id == "0x5" else "")),
            ("process_generation", lambda pid: "gen-{}".format(pid)),
            ("process_name", lambda pid, proc_root="/proc": "paint3d"),
        ):
            patcher = mock.patch.object(runtime, name, side_effect=value)
            patcher.start()
            self.addCleanup(patcher.stop)

    def test_only_on_screen_titled_application_windows_off_the_bus(self):
        windows = runtime.native_only_windows(
            [
                _window(30, "0x1", "Scene - Paint3D"),
                _window(21, "0x2", "Untitled - gedit"),
                _window(30, "0x3", ""),
                _window(30, "0x4", "Hidden", x=-900, y=-700),
                _window(30, "0x5", "Dock"),
                _window(30, "0x6", "Empty", width=0),
            ],
            accessible_pids={21},
            active_window_id="0x1",
        )

        self.assertEqual(["Scene - Paint3D"], [w["title"] for w in windows])
        window = windows[0]
        self.assertEqual("paint3d", window["app"])
        self.assertTrue(window["active"])
        self.assertEqual(runtime.window_handle_for_pid(30, "0x1"), window["handle"])
        self.assertTrue(window["handle"].startswith("w2:"))

    def test_listing_gives_them_a_handle_and_no_tree_flag(self):
        window = _TreeNode(role="frame", name="Untitled - gedit")
        app = _TreeNode(role="application", name="gedit", kids=(window,))
        app.get_process_id = lambda: 21
        original_apps = runtime.iter_apps
        runtime.iter_apps = lambda: [app]
        self.addCleanup(setattr, runtime, "iter_apps", original_apps)
        managed = _managed(_window(21, "0x2", "Untitled - gedit"),
                           _window(30, "0x1", "Scene - Paint3D"))
        with mock.patch.object(runtime, "_x11_managed_windows",
                               return_value=managed), \
                mock.patch.object(runtime, "process_toolkit", return_value=""):
            inventory = runtime.list_apps_inventory()

        native = [app for app in inventory["apps"] if app["name"] == "paint3d"]
        self.assertEqual(1, len(native))
        listed = native[0]["windows"]
        self.assertEqual(["Scene - Paint3D"], [w["title"] for w in listed])
        self.assertTrue(listed[0]["noAccessibilityTree"])
        self.assertTrue(listed[0]["handle"].startswith("w2:"))
        self.assertTrue(listed[0]["inputTarget"])
        # The window itself says it has no tree; a toolkit without restart
        # advice adds no note.
        self.assertEqual([], inventory["notes"])

    def test_restart_advice_stays_for_toolkits_that_can_export_a_tree(self):
        with tempfile.TemporaryDirectory() as root:
            os.makedirs(os.path.join(root, "20"))
            with open(os.path.join(root, "20", "maps"), "w") as maps:
                maps.write("7f00-7f01 r-xp 0 08:01 1 /usr/lib/libQt5Core.so.5\n")
            notes = runtime.invisible_window_notes(
                [{"pid": 20, "title": "VLC media player"},
                 {"pid": 22, "title": "Scene - Paint3D"}],
                accessible_pids=set(), proc_root=root)

        self.assertEqual(1, len(notes))
        self.assertIn('"VLC media player" (pid 20, Qt)', notes[0])


class NativeWindowActionTests(AtspiPatchedTestCase):
    def setUp(self):
        super().setUp()
        self.window = {"pid": 30, "id": "0x1", "title": "Scene - Paint3D",
                       "app": "paint3d", "handle": "w2:abc", "generation": "g",
                       "active": False}

    def test_window_action_without_bus_identity_resolves_the_native_window(self):
        operation = {"app": "paint3d", "window_handle": "w2:abc",
                     "target": {"selector": "window", "hasName": True,
                                "windowHandle": "w2:abc"}}
        with mock.patch.object(runtime, "_x11_managed_windows",
                               return_value=_managed()), \
                mock.patch.object(runtime, "iter_apps",
                                  return_value=runtime.InventoryResult([], complete=True)), \
                mock.patch.object(runtime, "x11_root_window_state",
                                  return_value={"windowIds": {"0x1"}, "activeWindow": None}), \
                mock.patch.object(runtime, "native_only_windows",
                                  return_value=[self.window]):
            self.assertIs(self.window, runtime.native_window_for_operation(operation))
            operation["target"]["providerBusName"] = ":1.5"
            self.assertIsNone(runtime.native_window_for_operation(operation))

    def test_activate_is_confirmed_by_the_window_manager(self):
        states = iter([{"windowIds": {"0x1"}, "activeWindow": "0x9"},
                       {"windowIds": {"0x1"}, "activeWindow": "0x1"}])
        with mock.patch.object(runtime, "_emit") as emit, \
                mock.patch.object(runtime, "x11_root_window_state",
                                  side_effect=lambda: next(states)), \
                mock.patch.object(runtime.time, "sleep"):
            response = runtime.perform_native_window_action("activate", self.window)

        emit.assert_called_once_with("activate", "0x1")
        self.assertTrue(response["ok"])
        self.assertTrue(response["input_events_emitted"])

    def test_close_that_leaves_the_window_is_effect_unknown(self):
        with mock.patch.object(runtime, "_emit"), \
                mock.patch.object(runtime, "x11_root_window_state",
                                  return_value={"windowIds": {"0x1"},
                                                "activeWindow": "0x1"}), \
                mock.patch.object(runtime, "AX_WINDOW_ACTION_TIMEOUT_SECONDS", 0.0):
            with self.assertRaises(runtime.OperationFailure) as caught:
                runtime.perform_native_window_action("close", self.window)

        self.assertEqual("effect_unknown", caught.exception.failure_code)
        self.assertTrue(caught.exception.input_events_emitted)


if __name__ == "__main__":
    unittest.main()
