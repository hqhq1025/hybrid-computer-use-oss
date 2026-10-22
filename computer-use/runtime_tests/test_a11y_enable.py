#!/usr/bin/env python3
"""Linux provider switches desktop accessibility on and explains apps it cannot see."""

from runtime_test_support import *  # noqa: F401,F403


class FakeSessionBus:
    """Session bus with an a11y status object and, optionally, Xfconf."""

    def __init__(self, enabled=False, xfconf=None, fail=()):
        self.enabled = enabled
        # None: no Xfconf on the bus; {}: Xfconf without Gtk/Modules.
        self.xfconf = xfconf
        self.fail = set(fail)
        self.writes = []

    def _maybe_fail(self, what):
        if what in self.fail:
            raise RuntimeError("{} failed".format(what))

    def has_owner(self, name):
        self._maybe_fail("has_owner")
        return name == "org.xfce.Xfconf" and self.xfconf is not None

    def get_property(self, name, path, interface, prop):
        self._maybe_fail("get")
        assert (name, path, interface, prop) == (
            "org.a11y.Bus", "/org/a11y/bus", "org.a11y.Status", "IsEnabled")
        return self.enabled

    def set_bool_property(self, name, path, interface, prop, value):
        self._maybe_fail("set")
        self.writes.append((interface, prop, value))
        self.enabled = value

    def xfconf_get_string(self, channel, prop):
        if (channel, prop) not in self.xfconf:
            raise RuntimeError(
                "GDBus.Error:org.xfce.Xfconf.Error.PropertyNotFound: no such")
        return self.xfconf[(channel, prop)]

    def xfconf_set_string(self, channel, prop, value):
        self.writes.append((channel, prop, value))
        self.xfconf[(channel, prop)] = value


class EnableSessionAccessibilityTests(unittest.TestCase):
    """Applications started by the user expose a tree without launch flags.

    In a plain session (no a11y-env.sh, toolkit-accessibility unset), Qt,
    Firefox, Chrome and Electron applications never registered on the AT-SPI
    bus, and GTK 2 applications registered only with GTK_MODULES set.
    """

    def test_turns_accessibility_on_in_a_plain_session(self):
        bus = FakeSessionBus(enabled=False, xfconf={})

        facts = runtime.enable_session_accessibility(bus)

        self.assertIn(("org.a11y.Status", "IsEnabled", True), bus.writes)
        self.assertIn(
            ("xsettings", "/Gtk/Modules", "gail:atk-bridge"), bus.writes)
        self.assertEqual(facts["errors"], [])

    def test_never_sets_screen_reader_enabled(self):
        """The launcher mirrors ScreenReaderEnabled to the key that starts Orca."""
        bus = FakeSessionBus(enabled=False)

        runtime.enable_session_accessibility(bus)

        self.assertNotIn(
            "ScreenReaderEnabled", [write[1] for write in bus.writes])

    def test_is_a_no_op_when_the_session_already_has_accessibility(self):
        """OSWorld guests export a11y-env.sh and set Gtk/Modules themselves."""
        bus = FakeSessionBus(
            enabled=True,
            xfconf={("xsettings", "/Gtk/Modules"): "canberra-gtk-module:gail:atk-bridge"},
        )

        facts = runtime.enable_session_accessibility(bus)

        self.assertEqual(bus.writes, [])
        self.assertEqual(facts, {"changed": [], "errors": []})

    def test_keeps_existing_gtk_modules(self):
        bus = FakeSessionBus(
            enabled=True,
            xfconf={("xsettings", "/Gtk/Modules"): "canberra-gtk-module:gail"},
        )

        runtime.enable_session_accessibility(bus)

        self.assertEqual(
            bus.xfconf[("xsettings", "/Gtk/Modules")],
            "canberra-gtk-module:gail:atk-bridge",
        )

    def test_leaves_xsettings_alone_without_xfconf(self):
        """GNOME's settings daemon derives Gtk/Modules from toolkit-accessibility."""
        bus = FakeSessionBus(enabled=False, xfconf=None)

        runtime.enable_session_accessibility(bus)

        self.assertEqual(bus.writes, [("org.a11y.Status", "IsEnabled", True)])

    def test_failures_are_reported_and_do_not_stop_the_other_switch(self):
        bus = FakeSessionBus(enabled=False, xfconf={}, fail={"get"})

        facts = runtime.enable_session_accessibility(bus)

        self.assertEqual(len(facts["errors"]), 1)
        self.assertIn("org.a11y.Status", facts["errors"][0])
        self.assertIn(
            ("xsettings", "/Gtk/Modules", "gail:atk-bridge"), bus.writes)


class ProviderStartupEnablesAccessibilityTests(unittest.TestCase):
    def test_provider_start_enables_accessibility_once(self):
        with mock.patch.object(
            runtime, "_provider_initialized", False
        ), mock.patch.object(
            runtime, "require_desktop_session"
        ), mock.patch.object(
            runtime.Atspi, "init"
        ), mock.patch.object(
            runtime.Atspi, "set_timeout"
        ), mock.patch.object(
            runtime.ui_events, "start"
        ), mock.patch.object(
            runtime, "enable_session_accessibility",
            return_value={"changed": [], "errors": []},
        ) as enable:
            runtime._initialize_provider({"request_id": "r1"})
            runtime._initialize_provider({"request_id": "r2"})

        enable.assert_called_once_with()

    def test_enable_failure_does_not_fail_provider_start(self):
        """A session without the a11y bus must still serve GUI and GTK3 apps."""
        with mock.patch.object(
            runtime, "_provider_initialized", False
        ), mock.patch.object(
            runtime, "require_desktop_session"
        ), mock.patch.object(
            runtime.Atspi, "init"
        ), mock.patch.object(
            runtime.Atspi, "set_timeout"
        ), mock.patch.object(
            runtime.ui_events, "start"
        ), mock.patch.object(
            runtime, "enable_session_accessibility",
            side_effect=RuntimeError("no session bus"),
        ):
            runtime._initialize_provider({"request_id": "r1"})
            self.assertTrue(runtime._provider_initialized)


class ChromiumTreeRequestTests(AtspiPatchedTestCase):
    """Chromium builds the tree below its frames only for a screen reader.

    Chrome 150 and Electron apps started after IsEnabled exposed only their
    application and frame (2 nodes) until an AT called GetAttributes.
    """

    def setUp(self):
        super().setUp()
        runtime._attribute_requested.clear()
        self.addCleanup(runtime._attribute_requested.clear)

    def _app(self, bus_name):
        app = _TreeNode(role="application", name="Browser",
                        kids=(_TreeNode(role="frame", name="Page"),))
        calls = []
        app.get_attributes = lambda: calls.append(bus_name) or {}
        return app, calls

    def test_listing_asks_each_application_for_attributes_once(self):
        app, calls = self._app(":1.7")
        root = _TreeNode(role="desktop", name="Desktop", kids=(app,))
        with mock.patch.object(runtime, "desktop", return_value=root), \
                mock.patch.object(runtime, "provider_bus_name",
                                  return_value=":1.7"):
            runtime.iter_apps()
            runtime.iter_apps()

        self.assertEqual(calls, [":1.7"])

    def test_restarted_application_is_asked_again(self):
        first, calls = self._app(":1.7")
        second, _ = self._app(":1.9")
        second.get_attributes = first.get_attributes
        names = iter([":1.7", ":1.9"])
        with mock.patch.object(runtime, "provider_bus_name",
                               side_effect=lambda _app: next(names)):
            runtime.request_full_tree(first)
            runtime.request_full_tree(second)

        self.assertEqual(len(calls), 2)

    def test_attribute_failure_does_not_break_the_listing(self):
        app = _TreeNode(role="application", name="Hung")

        def fail():
            raise RuntimeError("timeout")

        app.get_attributes = fail
        root = _TreeNode(role="desktop", name="Desktop", kids=(app,))
        with mock.patch.object(runtime, "desktop", return_value=root), \
                mock.patch.object(runtime, "provider_bus_name",
                                  return_value=":1.8"):
            inventory = runtime.iter_apps()

        self.assertEqual(list(inventory), [app])
        self.assertTrue(inventory.complete)
