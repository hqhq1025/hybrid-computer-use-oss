#!/usr/bin/env python3
"""Linux provider identity primitives, geometry, operation guard, and state-only action tests."""

from runtime_test_support import *  # noqa: F401,F403
from runtime_tests.test_failure_recovery import FailureRecoveryCases


class ProviderIdentityPrimitiveTests(unittest.TestCase):
    def test_process_generation_handles_spaces_and_parentheses_in_comm(self):
        with tempfile.TemporaryDirectory() as root:
            pid_dir = os.path.join(root, "123")
            os.makedirs(pid_dir)
            suffix = ["S"] + [str(index) for index in range(4, 23)]
            with open(os.path.join(pid_dir, "stat"), "w", encoding="utf-8") as f:
                f.write("123 (worker name) with spaces) " + " ".join(suffix))
            runtime.process_generation.cache_clear()
            self.assertEqual(
                runtime.process_generation(123, proc_root=root),
                "22",
            )

    def test_process_generation_is_reread_for_each_operation(self):
        # The provider process outlives many operations. A cached start time
        # let a new process that reused a pid pass as the old one.
        with tempfile.TemporaryDirectory() as root:
            stat = os.path.join(root, "321", "stat")
            os.makedirs(os.path.dirname(stat))

            def start(value):
                suffix = ["S"] + [str(index) for index in range(4, 22)]
                with open(stat, "w", encoding="utf-8") as f:
                    f.write("321 (app) " + " ".join(suffix + [value]))

            start("1000")
            self.assertEqual(runtime.process_generation(321, proc_root=root), "1000")
            start("2000")
            with mock.patch.object(runtime, "list_apps_inventory",
                                   return_value={"text": "", "apps": [], "notes": []}):
                perform_provider_operation({"intent": "observe_apps"})
            self.assertEqual(runtime.process_generation(321, proc_root=root), "2000")

    def test_state_value_preserves_unknown(self):
        class MissingStateNode:
            def get_state_set(self):
                return None

        class BrokenStateNode:
            def get_state_set(self):
                raise RuntimeError("provider unavailable")

        self.assertIsNone(
            runtime.state_value(MissingStateNode(), STATE.SHOWING)
        )
        self.assertIsNone(
            runtime.state_value(BrokenStateNode(), STATE.SHOWING)
        )
        self.assertTrue(
            runtime.state_value(
                FakeNode(states={STATE.SHOWING}), STATE.SHOWING
            )
        )
        self.assertFalse(
            runtime.state_value(FakeNode(), STATE.SHOWING)
        )


class ExtentsTests(AtspiPatchedTestCase):
    def _node(self, x, y, width, height):
        return FakeNode(component=FakeComponent(extents=FakeExtents(x, y, width, height)))

    def test_accepts_a_normal_on_screen_widget(self):
        self.assertEqual(
            runtime.extents(self._node(10, 20, 100, 40)),
            runtime.frame(10, 20, 100, 40),
        )

    def test_accepts_negative_coordinates_from_a_second_monitor(self):
        """A monitor left of the primary one yields legitimate negative coordinates."""
        self.assertIsNotNone(runtime.extents(self._node(-1920, -100, 800, 600)))

    def test_rejects_int_min_origin_used_for_unrendered_widgets(self):
        """Unrendered widgets report an INT_MIN origin but a plausible size."""
        self.assertIsNone(runtime.extents(self._node(-2147483648, -2147483648, 1, 1)))

    def test_rejects_int_min_origin_on_either_axis(self):
        self.assertIsNone(runtime.extents(self._node(-2147483648, 20, 100, 40)))
        self.assertIsNone(runtime.extents(self._node(10, -2147483648, 100, 40)))

    def test_still_rejects_absurd_sizes(self):
        self.assertIsNone(runtime.extents(self._node(0, 0, 0, 40)))
        self.assertIsNone(runtime.extents(self._node(0, 0, 100, 999999)))


class DesktopBoundsTests(unittest.TestCase):
    def test_monitor_union_preserves_negative_virtual_desktop_origin(self):
        class Geometry:
            def __init__(self, x, y, width, height):
                self.x = x
                self.y = y
                self.width = width
                self.height = height

        class Screen:
            geometries = (
                Geometry(-1920, -100, 1920, 1080),
                Geometry(0, 0, 1920, 1080),
            )

            def get_n_monitors(self):
                return len(self.geometries)

            def get_monitor_geometry(self, index):
                return self.geometries[index]

            def get_root_window(self):
                raise AssertionError("monitor geometry should be authoritative")

        class ScreenFactory:
            @staticmethod
            def get_default():
                return Screen()

        fake_gdk = type("FakeGdk", (), {"Screen": ScreenFactory})
        with mock.patch.object(runtime, "Gdk", fake_gdk):
            self.assertEqual(
                runtime.desktop_bounds(),
                runtime.frame(-1920, -100, 3840, 1180),
            )


class PerformOperationGuardTests(FailureRecoveryCases, AtspiPatchedTestCase):
    """The focus guard stays wired into the dispatch path."""

    def setUp(self):
        super().setUp()
        self.window = FakeNode()
        self.focus_calls = []
        self.sent_keys = []
        self.sent_text = []
        self.clicks = []

        def fake_require(window, what):
            self.focus_calls.append(what)

        self._patch("resolve_app", lambda query: FakeNode())
        self._patch("main_window", lambda app: (0, self.window))
        self._patch("extents", lambda node: runtime.frame(0, 0, 100, 100))
        self._patch("find_element_at_snapshot_path", lambda app, record: None)
        self._patch("build_snapshot", lambda *a, **k: {"text": ""})
        self._patch("require_window_focus", fake_require)
        self._patch("send_text", lambda text: self.sent_text.append(text))
        self._patch("parse_key", lambda key: ([], key))
        self._patch("send_key", lambda key: self.sent_keys.append(key))
        self._patch("coordinate_point_owner", lambda window, x, y: True)
        self._patch(
            "send_mouse_click", lambda x, y, b, c: self.clicks.append((x, y, b, c))
        )
        self._patch("time", _NoSleep())

    def _patch(self, name, value):
        original = getattr(runtime, name)
        setattr(runtime, name, value)
        self.addCleanup(setattr, runtime, name, original)

    def test_numeric_and_semantic_target_together_are_rejected_before_input(self):
        with self.assertRaises(runtime.OperationFailure) as raised:
            runtime.dispatch_provider_operation(
                {
                    "app": "x",
                    "element": {"index": 3},
                    "target": {"selector": "push button \"OK\""},
                    "input": {"key": "ENTER"},
                },
                operation_kind="resolve_and_act",
                action_intent="keypress",
            )
        self.assertEqual(raised.exception.failure_code, "semantic_target_invalid")
        self.assertFalse(raised.exception.input_events_emitted)
        self.assertEqual(self.sent_keys, [])
        self.assertEqual(self.focus_calls, [])

    def test_defunct_target_fails_as_stale_before_focus_or_input(self):
        defunct = FakeNode(states=(STATE.DEFUNCT, STATE.ENABLED))
        self._patch("find_element_at_snapshot_path", lambda app, record: defunct)
        self._patch("focus_target_element", mock.Mock(
            side_effect=AssertionError("a defunct target must not take focus")))
        with self.assertRaises(runtime.OperationFailure) as raised:
            perform_provider_operation(
                {"intent": "keypress", "app": "x", "key": "ENTER",
                 "element": {"index": 3, "path": [0, 1]}}
            )
        self.assertEqual(raised.exception.failure_code, "stale_element_ref")
        self.assertIn("target_defunct", str(raised.exception))
        self.assertFalse(raised.exception.input_events_emitted)
        self.assertEqual(self.sent_keys, [])

    def test_press_key_requires_focus_first(self):
        perform_provider_operation({"intent": "keypress", "app": "x", "key": "ctrl+a"})

        self.assertEqual(self.focus_calls, ["keypress"])
        self.assertEqual(self.sent_keys, ["ctrl+a"])

    def test_post_action_reads_do_not_reuse_pre_action_native_inventory(self):
        # The native window list was cached per operation, so a dialog that
        # the input opened had no native window in the post-action snapshot
        # and no strong handle; the host then needed a second read.
        def pre_action_read(key):
            runtime._x11_window_inventory_cache[4242] = "before-input"
            self.sent_keys.append(key)

        seen = []
        self._patch("send_key", pre_action_read)
        self._patch(
            "build_snapshot",
            lambda *a, **k: seen.append(
                dict(runtime._x11_window_inventory_cache)
            ) or {"text": ""},
        )
        perform_provider_operation(
            {"intent": "keypress", "app": "x", "key": "ctrl+o"}
        )
        self.assertEqual(self.sent_keys, ["ctrl+o"])
        self.assertEqual(seen, [{}])

    def test_invalid_key_is_rejected_before_stealing_focus(self):
        """Stealing focus disrupts the user, so a misspelled key fails first."""

        def refuse_parse(key):
            raise RuntimeError("Unsupported modifier: shft")

        self._patch("parse_key", refuse_parse)

        with self.assertRaises(RuntimeError) as ctx:
            perform_provider_operation({"intent": "keypress", "app": "x", "key": "shft+s"})
        self.assertIn("Unsupported modifier", str(ctx.exception))
        self.assertEqual(self.focus_calls, [], "invalid arguments must not touch the user's windows")
        self.assertEqual(self.sent_keys, [])

    def test_type_text_requires_focus_before_synthesis_fallback(self):
        perform_provider_operation({"intent": "type", "app": "x", "text": "hi"})

        self.assertEqual(self.focus_calls, ["type"])
        self.assertEqual(self.sent_text, ["hi"])

    def test_type_text_skips_focus_when_direct_write_succeeds(self):
        """A direct AT-SPI write needs no focus, so the user's window is left alone."""
        self._patch(
            "insert_text_outcome_on_node",
            lambda root, text: ("verified", 0, 2, True),
        )

        perform_provider_operation({"intent": "type", "app": "x", "text": "hi"})

        self.assertEqual(self.focus_calls, [])
        self.assertEqual(self.sent_text, [])

    def test_type_text_does_not_fallback_when_direct_write_effect_is_unknown(self):
        self._patch(
            "insert_text_outcome_on_node",
            lambda _node, _text: ("effect_unknown", 0, 0, True),
        )
        self._patch(
            "find_editable_text",
            lambda _window: FakeNode(),
        )

        with self.assertRaises(runtime.OperationFailure) as raised:
            perform_provider_operation(
                {"intent": "type", "app": "x", "text": "hi"}
            )

        self.assertEqual(raised.exception.failure_code, "effect_unknown")
        self.assertTrue(raised.exception.input_events_emitted)
        self.assertEqual(self.sent_text, [])

    def test_global_click_requires_focus(self):
        perform_provider_operation(
            {"intent": "click", "app": "x", "click_method": "global", "x": 5, "y": 5}
        )

        self.assertEqual(self.focus_calls, ["click"])
        self.assertEqual(len(self.clicks), 1)

    def test_scroll_requires_focus(self):
        perform_provider_operation(
            {"intent": "scroll", "app": "x", "direction": "down", "pages": 1}
        )

        self.assertEqual(self.focus_calls, ["scroll"])

    def test_window_scoped_scroll_uses_exact_window_without_element_lookup(self):
        selected = FakeNode()
        calls = []
        self._patch(
            "select_window_by_handle",
            lambda _app, handle: (2, selected, calls.append(handle)),
        )
        self._patch(
            "scroll_element",
            lambda direction, pages, point: "wheel",
        )

        perform_provider_operation({
            "intent": "scroll",
            "app": "x",
            "window_handle": "w2:private",
            "window_centre": True,
            "direction": "down",
            "pages": 1,
        })

        self.assertEqual(calls, ["w2:private"])

    def test_failed_focus_aborts_before_any_synthesis(self):
        def refuse(window, what):
            raise RuntimeError("Refusing to synthesize {}".format(what))

        self._patch("require_window_focus", refuse)

        with self.assertRaises(RuntimeError):
            perform_provider_operation({"intent": "keypress", "app": "x", "key": "a"})
        self.assertEqual(self.sent_keys, [], "no key may be synthesized after focus acquisition fails")

    def test_stale_element_ref_fails_before_any_input(self):
        record = {
            "index": 44,
            "runtimeId": [0, 3],
            "controlType": "entry",
            "name": "Search",
            "frame": {"x": 10, "y": 10, "width": 100, "height": 20},
        }
        with self.assertRaisesRegex(
            runtime.OperationFailure,
            "stale_element_ref reason=path_changed_or_element_disappeared",
        ) as raised:
            perform_provider_operation({
                "intent": "click",
                "app": "x",
                "element": record,
                "click_method": "auto",
            })
        self.assertEqual(raised.exception.failure_code, "stale_element_ref")
        self.assertEqual(raised.exception.failure_phase, "precondition")
        self.assertFalse(raised.exception.input_events_emitted)
        self.assertEqual(self.clicks, [])
        self.assertEqual(self.sent_keys, [])
        self.assertEqual(self.sent_text, [])


class _ToggleOnlyNode(FakeNode):
    """A checkable control whose only AT-SPI action is `toggle`.

    Qt exposes checkable buttons and item-view cells this way. The toolkit is
    readable only from the application object, as on Qt.
    """

    def __init__(self, toolkit, role="check box", name="Input / Codecs"):
        super().__init__(states=(STATE.SHOWING, STATE.VISIBLE))
        self._toolkit = toolkit
        self.role = role
        self.title = name
        self.actions = []

    def get_application(self):
        node = FakeNode()
        node.get_toolkit_name = lambda: self._toolkit
        return node

    def get_action_iface(self):
        return object()

    def get_n_actions(self):
        return 1

    def get_action_name(self, index):
        return "Toggle"

    def get_action_description(self, index):
        return ""

    def get_role_name(self):
        return self.role

    def get_name(self):
        return self.title

    def do_action(self, index):
        self.actions.append(index)
        return True


class StateOnlyActionDeliveryTests(AtspiPatchedTestCase):
    """A click on a control whose action only flips a state flag.

    VLC (Qt 5) Simple Preferences: the category buttons are checkable
    QToolButtons with the single action `Toggle`. Invoking it turned the
    button checked while the page stayed on Interface Settings, because Qt
    calls toggle() rather than click(). A pointer click switched the page.
    """

    def setUp(self):
        super().setUp()
        self.window = FakeNode(states=(STATE.ACTIVE,))
        self.pointer = []
        self._patch("resolve_app", lambda query: FakeNode())
        self._patch("main_window", lambda app: (0, self.window))
        self._patch("extents", lambda node: runtime.frame(0, 0, 400, 300))
        self._patch("desktop_bounds", lambda: runtime.frame(0, 0, 1920, 1080))
        self._patch("build_snapshot", lambda *a, **k: {"text": ""})
        self._patch("coordinate_point_owner", lambda window, x, y: True)
        self._patch("window_in_foreground", lambda window: True)
        self._patch("point_hits_element", lambda window, element, x, y: True)
        self._patch(
            "current_geometry", lambda element, record, bounds: (record, None))
        self._patch(
            "require_stable_coordinate_geometry",
            lambda element, record, bounds: (record, 2))
        self._patch(
            "send_mouse_click",
            lambda x, y, button, count: self.pointer.append((x, y, button, count)))
        self._patch("time", _NoSleep())

    def _patch(self, name, value):
        original = getattr(runtime, name)
        setattr(runtime, name, value)
        self.addCleanup(setattr, runtime, name, original)

    def _click(self, element):
        self._patch("find_element_at_snapshot_path", lambda app, record: element)
        return perform_provider_operation({
            "intent": "click",
            "app": "vlc",
            "click_method": "auto",
            "element": {"frame": runtime.frame(100, 10, 110, 76)},
        })

    def test_qt_toggle_only_control_gets_a_real_pointer_click(self):
        element = _ToggleOnlyNode("Qt")

        response = self._click(element)

        self.assertEqual(element.actions, [])
        self.assertEqual(self.pointer, [(155.0, 48.0, "left", 1)])
        self.assertEqual(response["actionEvidence"]["route"], "coordinate_synthesis")
        self.assertTrue(response["actionEvidence"]["point_owner_verified"])
        note = " ".join(response["notes"])
        self.assertIn("Clicked the element with the pointer", note)
        self.assertIn("toolkit toggle", note)

    def test_gtk_toggle_stays_semantic(self):
        # GTK's toggle emits the widget's own signals (a GTK 3 check button
        # switches through it), so a pointer click would only add risk.
        element = _ToggleOnlyNode("GTK")

        response = self._click(element)

        self.assertEqual(element.actions, [0])
        self.assertEqual(self.pointer, [])
        self.assertEqual(response["actionEvidence"]["route"], "semantic_action")

    def test_qt_toggle_falls_back_to_the_action_when_the_window_is_behind(self):
        # The pointer route never raises the window to avoid a state-only
        # action; it keeps the action and says the app may not respond.
        self._patch("window_in_foreground", lambda window: False)
        element = _ToggleOnlyNode("Qt")

        response = self._click(element)

        self.assertEqual(element.actions, [0])
        self.assertEqual(self.pointer, [])
        self.assertEqual(response["actionEvidence"]["route"], "semantic_action")
        note = " ".join(response["notes"])
        self.assertIn("the application may not respond", note)
        self.assertIn("not in the foreground", note)

    def test_qt_toggle_falls_back_when_another_element_is_under_the_point(self):
        self._patch("point_hits_element", lambda window, element, x, y: False)
        element = _ToggleOnlyNode("Qt")

        response = self._click(element)

        self.assertEqual(element.actions, [0])
        self.assertEqual(self.pointer, [])
        evidence = response["actionEvidence"]
        self.assertEqual(evidence["route"], "semantic_action")
        # A refused pointer route leaves no coordinate evidence behind.
        self.assertFalse(evidence["geometry_sampled"])
        self.assertIn("another element is under", " ".join(response["notes"]))

    def test_qt_toggle_falls_back_when_geometry_is_unstable(self):
        def unstable(element, record, bounds):
            raise runtime.OperationFailure(
                "target_unstable: moving", failure_code="target_unstable",
                failure_phase="precondition", input_events_emitted=False)

        self._patch("require_stable_coordinate_geometry", unstable)
        element = _ToggleOnlyNode("Qt")

        response = self._click(element)

        self.assertEqual(element.actions, [0])
        self.assertEqual(self.pointer, [])
        self.assertIn("target_unstable", " ".join(response["notes"]))

    def test_qt_press_action_stays_semantic(self):
        # Qt's Press calls click()/animateClick(), which emits clicked().
        element = _ToggleOnlyNode("Qt", role="push button", name="Save")
        element.get_action_name = lambda index: "Press"

        response = self._click(element)

        self.assertEqual(element.actions, [0])
        self.assertEqual(self.pointer, [])
        self.assertEqual(response["actionEvidence"]["route"], "semantic_action")


class PointHitsElementTests(AtspiPatchedTestCase):
    """The pointer route clicks only when the point reaches the element."""

    def _node(self, role, name, frame, parent=None, path=""):
        node = _ToggleOnlyNode("Qt", role=role, name=name)
        node.parent = parent
        node.path = path
        node.get_parent = lambda: node.parent
        node.frame = frame
        return node

    def setUp(self):
        super().setUp()
        self._original_extents = runtime.extents
        runtime.extents = lambda node: getattr(node, "frame", None)
        self.addCleanup(setattr, runtime, "extents", self._original_extents)
        self.window = FakeNode()
        self.button = self._node(
            "check box", "Input / Codecs", runtime.frame(952, 213, 110, 76),
            path="/a/1")

    def _hit(self, hit):
        original = runtime._deepest_accessible_at
        runtime._deepest_accessible_at = lambda window, x, y: hit
        self.addCleanup(setattr, runtime, "_deepest_accessible_at", original)
        return runtime.point_hits_element(self.window, self.button, 1007, 251)

    def test_the_element_itself(self):
        same = self._node("check box", "Input / Codecs",
                          runtime.frame(952, 213, 110, 76), path="/a/1")
        self.assertTrue(self._hit(same))

    def test_a_descendant_of_the_element(self):
        label = self._node("label", "Input / Codecs",
                           runtime.frame(960, 270, 90, 15),
                           parent=self.button, path="/a/2")
        self.assertTrue(self._hit(label))

    def test_a_fresh_object_for_the_same_cell(self):
        # Qt returns a new accessible for an item-view cell on every query.
        fresh = self._node("check box", "Input / Codecs",
                           runtime.frame(952, 213, 110, 76), path="/a/99")
        self.assertTrue(self._hit(fresh))

    def test_another_element_under_the_point(self):
        popup_item = self._node("list item", "Never",
                                runtime.frame(950, 240, 300, 25), path="/b/1")
        self.assertFalse(self._hit(popup_item))

    def test_nothing_under_the_point(self):
        self.assertFalse(self._hit(None))
