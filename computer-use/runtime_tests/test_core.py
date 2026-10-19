#!/usr/bin/env python3
"""Linux provider text input, window focus, diagnostics, and action note tests."""

from runtime_test_support import *  # noqa: F401,F403


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


class FindEditableTextTests(AtspiPatchedTestCase):
    def test_skips_node_that_lacks_editable_state(self):
        """The first EditableText node in tree order may be a hidden placeholder.

        It lacks the EDITABLE state, so text written to it is silently lost.
        """
        placeholder = FakeNode(
            states=(STATE.VISIBLE, STATE.ENABLED), text=FakeTextIface(honest=False)
        )
        real_editor = FakeNode(
            states=(STATE.FOCUSED, STATE.EDITABLE, STATE.SHOWING),
            text=FakeTextIface(),
        )
        window = FakeNode(children=(placeholder, real_editor))

        self.assertIs(runtime.find_editable_text(window), real_editor)

    def test_prefers_focused_over_merely_showing(self):
        showing = FakeNode(states=(STATE.EDITABLE, STATE.SHOWING), text=FakeTextIface())
        focused = FakeNode(
            states=(STATE.EDITABLE, STATE.SHOWING, STATE.FOCUSED), text=FakeTextIface()
        )
        window = FakeNode(children=(showing, focused))

        self.assertIs(runtime.find_editable_text(window), focused)

    def test_does_not_write_to_showing_but_unfocused_editor(self):
        offscreen = FakeNode(states=(STATE.EDITABLE,), text=FakeTextIface())
        showing = FakeNode(states=(STATE.EDITABLE, STATE.SHOWING), text=FakeTextIface())
        window = FakeNode(children=(offscreen, showing))

        self.assertIsNone(runtime.find_editable_text(window))

    def test_refuses_to_guess_between_multiple_showing_editors(self):
        first = FakeNode(states=(STATE.EDITABLE, STATE.SHOWING), text=FakeTextIface())
        second = FakeNode(states=(STATE.EDITABLE, STATE.SHOWING), text=FakeTextIface())
        window = FakeNode(children=(first, second))

        self.assertIsNone(runtime.find_editable_text(window))

    def test_returns_none_when_no_node_has_editable_state(self):
        window = FakeNode(
            children=(
                FakeNode(states=(STATE.VISIBLE,), text=FakeTextIface()),
                FakeNode(states=(STATE.ENABLED,), text=FakeTextIface()),
            )
        )

        self.assertIsNone(runtime.find_editable_text(window))

    def test_ignores_nodes_without_editable_text_iface(self):
        read_only = FakeNode(
            states=(STATE.EDITABLE, STATE.FOCUSED),
            text=FakeTextIface(),
            editable=False,
        )
        editor = FakeNode(
            states=(STATE.EDITABLE, STATE.SHOWING), text=FakeTextIface())
        window = FakeNode(children=(read_only, editor))

        self.assertIsNone(runtime.find_editable_text(window))

    def test_unique_showing_editor_is_not_treated_as_focused(self):
        editor = FakeNode(
            states=(STATE.EDITABLE, STATE.SHOWING), text=FakeTextIface())
        window = FakeNode(children=(FakeNode(states=(STATE.VISIBLE,)), editor))

        self.assertIsNone(runtime.find_editable_text(window))

    def test_finds_editor_nested_deep_in_the_tree(self):
        editor = FakeNode(
            states=(STATE.EDITABLE, STATE.FOCUSED, STATE.SHOWING), text=FakeTextIface()
        )
        window = FakeNode(
            children=(FakeNode(children=(FakeNode(children=(editor,)),)),)
        )

        self.assertIs(runtime.find_editable_text(window), editor)


def _write_into_window(window, text):
    """Write the way an untargeted type action does: into the focused editor."""
    outcome, _before, _after, _emitted = runtime.insert_text_outcome_on_node(
        runtime.find_editable_text(window), text
    )
    return outcome


class InsertTextTests(AtspiPatchedTestCase):
    def _window_with(self, iface):
        return FakeNode(
            children=(
                FakeNode(
                    states=(STATE.EDITABLE, STATE.FOCUSED, STATE.SHOWING), text=iface
                ),
            )
        )

    def test_rejects_buffer_that_reports_success_without_writing(self):
        """AT-SPI reports success even for the wrong control; read back the length."""
        liar = FakeTextIface(honest=False)
        window = self._window_with(liar)

        self.assertEqual(_write_into_window(window, "hello"), "no_change")
        self.assertEqual(liar.inserts, [(0, "hello")], "the write must actually be attempted")

    def test_accepts_buffer_that_actually_grows(self):
        iface = FakeTextIface()
        window = self._window_with(iface)

        self.assertEqual(_write_into_window(window, "hello"), "verified")
        self.assertEqual(iface.text, "hello")

    def test_appends_at_end_of_existing_content(self):
        iface = FakeTextIface(text="abc")
        window = self._window_with(iface)

        self.assertEqual(_write_into_window(window, "def"), "verified")
        self.assertEqual(iface.text, "abcdef")

    def test_reports_no_change_when_insert_itself_fails(self):
        iface = FakeTextIface(accept=False)
        window = self._window_with(iface)

        self.assertEqual(_write_into_window(window, "hello"), "no_change")

    def test_empty_payload_is_a_successful_noop(self):
        iface = FakeTextIface(text="abc")
        window = self._window_with(iface)

        self.assertEqual(_write_into_window(window, ""), "verified")
        self.assertEqual(iface.inserts, [], "empty text must not produce a write call")

    def test_reports_no_change_when_no_editable_target_exists(self):
        window = FakeNode(children=(FakeNode(states=(STATE.VISIBLE,)),))

        self.assertEqual(_write_into_window(window, "hello"), "no_change")

    def test_accepted_write_without_readback_is_unknown_not_fallback_safe(self):
        iface = FakeTextIface(text="abc", caret=3)
        node = FakeNode(
            states=(STATE.EDITABLE, STATE.FOCUSED, STATE.SHOWING),
            text=iface,
        )
        with mock.patch.object(
            runtime,
            "read_all_text",
            side_effect=["abc", "abc", None],
        ):
            outcome, before, after, emitted = (
                runtime.insert_text_outcome_on_node(node, "de")
            )

        self.assertEqual(outcome, "effect_unknown")
        self.assertEqual((before, after), (3, 3))
        self.assertTrue(emitted)
        self.assertEqual(iface.inserts, [(3, "de")])


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


class TextInsertionPointTests(AtspiPatchedTestCase):
    def test_uses_caret_offset(self):
        iface = FakeTextIface(text="abcdef", caret=3)

        self.assertEqual(runtime.text_insertion_point(iface), (3, None))

    def test_prefers_a_non_empty_selection_over_the_caret(self):
        iface = FakeTextIface(text="abcdef", caret=0, selection=(2, 5))

        self.assertEqual(runtime.text_insertion_point(iface), (2, (2, 5)))

    def test_ignores_a_collapsed_selection(self):
        iface = FakeTextIface(text="abcdef", caret=4, selection=(2, 2))

        self.assertEqual(runtime.text_insertion_point(iface), (4, None))

    def test_falls_back_to_appending_when_caret_is_unavailable(self):
        iface = FakeTextIface(text="abcdef", caret=None)

        self.assertEqual(runtime.text_insertion_point(iface), (6, None))

    def test_falls_back_to_appending_when_caret_is_out_of_range(self):
        iface = FakeTextIface(text="abc", caret=99)

        self.assertEqual(runtime.text_insertion_point(iface), (3, None))


class CaretInsertionTests(AtspiPatchedTestCase):
    def _window_with(self, iface):
        return FakeNode(
            children=(
                FakeNode(
                    states=(STATE.EDITABLE, STATE.FOCUSED, STATE.SHOWING), text=iface
                ),
            )
        )

    def test_inserts_at_the_caret_instead_of_the_end(self):
        """Appending at the end would ignore the caret the user or agent just placed."""
        iface = FakeTextIface(text="hello world", caret=5)

        self.assertEqual(
            _write_into_window(self._window_with(iface), " there"), "verified"
        )
        self.assertEqual(iface.text, "hello there world")

    def test_replaces_a_selection_the_way_typing_does(self):
        iface = FakeTextIface(text="hello world", caret=0, selection=(0, 5))

        self.assertEqual(
            _write_into_window(self._window_with(iface), "goodbye"), "verified"
        )
        self.assertEqual(iface.deletes, [(0, 5)])
        self.assertEqual(iface.text, "goodbye world")

    def test_failed_insert_after_deleting_a_selection_is_unknown(self):
        iface = FakeTextIface(
            text="hello world", caret=0, selection=(0, 5), accept=False)

        outcome, _before, _after, emitted = runtime.insert_text_outcome_on_node(
            runtime.find_editable_text(self._window_with(iface)), "goodbye"
        )

        self.assertEqual(outcome, "effect_unknown")
        self.assertTrue(emitted)
        self.assertEqual(iface.text, " world")

    def test_appends_when_the_control_has_no_caret(self):
        iface = FakeTextIface(text="abc", caret=None)

        self.assertEqual(
            _write_into_window(self._window_with(iface), "def"), "verified"
        )
        self.assertEqual(iface.text, "abcdef")

    def test_reports_character_counts(self):
        iface = FakeTextIface(text="abc", caret=3)

        self.assertEqual(
            runtime.insert_text_outcome_on_node(
                runtime.find_editable_text(self._window_with(iface)), "de"
            ),
            ("verified", 3, 5, True),
        )

    def test_reports_no_change_without_growth(self):
        iface = FakeTextIface(text="abc", caret=3, honest=False)

        outcome, before, after, _emitted = runtime.insert_text_outcome_on_node(
            runtime.find_editable_text(self._window_with(iface)), "de"
        )
        self.assertEqual(outcome, "no_change")
        self.assertEqual((before, after), (3, 3))

    def test_wrong_text_of_matching_length_is_unknown(self):
        iface = FakeTextIface(text="abc", caret=3)

        def corrupt(offset, payload):
            iface.inserts.append((offset, payload))
            iface.text = iface.text[:offset] + ("Z" * len(payload)) + iface.text[offset:]
            return True

        iface.insert = corrupt
        outcome, _before, _after, _emitted = runtime.insert_text_outcome_on_node(
            runtime.find_editable_text(self._window_with(iface)), "de"
        )

        self.assertEqual(outcome, "effect_unknown")
        self.assertEqual(iface.text, "abcZZ")


class SetElementValueTests(AtspiPatchedTestCase):
    def test_numeric_value_interface_precedes_editable_text_surface(self):
        text = FakeTextIface(text="0")
        value = FakeValueIface(0)
        node = FakeNode(
            states=(STATE.EDITABLE,), text=text, value=value)

        self.assertEqual(runtime.set_element_value_outcome(node, "-25"), "verified")
        self.assertEqual(value.current, -25.0)
        self.assertEqual(value.set_calls, [-25.0])
        self.assertEqual(text.set_calls, [])

    def test_unconfirmed_value_write_falls_back_to_editable_text(self):
        text = FakeTextIface(text="0")
        value = FakeValueIface(0, honest=False)
        node = FakeNode(
            states=(STATE.EDITABLE,), text=text, value=value)

        self.assertEqual(runtime.set_element_value_outcome(node, "-25"), "verified")
        self.assertEqual(value.current, 0.0)
        self.assertEqual(text.text, "-25")

    def test_value_interface_exception_falls_back_to_editable_text(self):
        text = FakeTextIface(text="0")
        value = FakeValueIface(0)
        node = FakeNode(states=(STATE.EDITABLE,), text=text, value=value)

        original = FakeAtspi.Value.set_current_value
        FakeAtspi.Value.set_current_value = staticmethod(
            lambda _iface, _value: (_ for _ in ()).throw(RuntimeError("dbus")))
        self.addCleanup(
            setattr, FakeAtspi.Value, "set_current_value", original)

        self.assertEqual(runtime.set_element_value_outcome(node, "-25"), "verified")
        self.assertEqual(text.text, "-25")

    def test_confirms_the_write_landed(self):
        iface = FakeTextIface(text="old")
        node = FakeNode(states=(STATE.EDITABLE,), text=iface)

        self.assertEqual(runtime.set_element_value_outcome(node, "new"), "verified")
        self.assertEqual(iface.text, "new")

    def test_rejects_a_control_that_reports_success_without_changing(self):
        """set_text_contents, like insert_text, returns True for the wrong control."""
        iface = FakeTextIface(text="old", honest=False)
        node = FakeNode(states=(STATE.EDITABLE,), text=iface)

        self.assertEqual(runtime.set_element_value_outcome(node, "new"), "no_change")

    def test_rejects_a_control_that_changes_to_a_different_value(self):
        """A change to some other value does not prove the requested value landed."""
        iface = FakeTextIface(text="old")

        def normalize(payload):
            iface.set_calls.append(payload)
            iface.text = payload.strip().upper()
            return True

        iface.set_contents = normalize
        node = FakeNode(states=(STATE.EDITABLE,), text=iface)

        self.assertEqual(
            runtime.set_element_value_outcome(node, " new "), "effect_unknown")
        self.assertEqual(iface.text, "NEW")

    def test_accepted_numeric_write_without_readback_is_unknown(self):
        value = FakeValueIface(0)
        node = FakeNode(value=value)
        with mock.patch.object(
            runtime.Atspi.Value,
            "get_current_value",
            side_effect=[0.0, None],
        ):
            self.assertEqual(
                runtime.set_element_value_outcome(node, "25"),
                "effect_unknown",
            )


class ChannelTaggingTests(AtspiPatchedTestCase):
    """Every action note carries a channel tag.

    The tags make the ratio of semantic to coordinate actions measurable and
    separate "the agent did not use accessibility" from "it tried and fell
    back after a failure"; the two cases need opposite fixes.
    """

    def setUp(self):
        super().setUp()
        self.window = FakeNode(states=(STATE.ACTIVE,))
        for name, value in (
            ("resolve_app", lambda q: FakeNode()),
            ("main_window", lambda a: (0, self.window)),
            ("extents", lambda n: runtime.frame(0, 0, 100, 100)),
            ("find_element_at_snapshot_path", lambda a, r: None),
            ("build_snapshot", lambda *a, **k: {"text": ""}),
            ("require_window_focus", lambda w, what: None),
            ("parse_key", lambda k: ([], k)),
            ("send_key", lambda k: None),
            ("send_text", lambda t: None),
            ("send_mouse_click", lambda *a: None),
            # Match scroll_element's signature: it takes an optional element
            # point and returns the route used, which the note text reports.
            ("scroll_element", lambda d, p, point=None: "keys"),
            ("time", _NoSleep()),
        ):
            original = getattr(runtime, name)
            setattr(runtime, name, value)
            self.addCleanup(setattr, runtime, name, original)

    def _notes(self, op):
        """Return only the action notes.

        The `[pixels]` note is a tree-independent effect check appended for
        every action type, so it stays out of assertions about the action.
        """
        return [n for n in perform_provider_operation(op).get("notes", [])
                if not n.startswith("[pixels]")]

    def test_semantic_and_synthesis_are_distinguishable(self):
        original = runtime.insert_text_outcome_on_node
        runtime.insert_text_outcome_on_node = (
            lambda root, text: ("verified", 0, 2, True)
        )
        self.addCleanup(
            setattr, runtime, "insert_text_outcome_on_node", original
        )

        semantic = self._notes({"intent": "type", "app": "x", "text": "hi"})
        self.assertIn("[semantic] ", semantic[0])

        runtime.insert_text_outcome_on_node = (
            lambda root, text: ("no_change", 0, 0, False)
        )
        synthesis = self._notes({"intent": "type", "app": "x", "text": "hi"})
        self.assertIn("[synthesis] ", synthesis[0])

    def test_every_action_path_carries_a_channel_tag(self):
        original = runtime.insert_text_outcome_on_node
        runtime.insert_text_outcome_on_node = (
            lambda root, text: ("no_change", 0, 0, False)
        )
        self.addCleanup(
            setattr, runtime, "insert_text_outcome_on_node", original
        )

        for op in (
            {"intent": "keypress", "app": "x", "key": "a"},
            {"intent": "type", "app": "x", "text": "hi"},
            {"intent": "scroll", "app": "x", "direction": "down", "pages": 1},
        ):
            notes = self._notes(op)
            self.assertTrue(notes, "{} produced no note".format(op["intent"]))
            # Each note is tagged on two orthogonal axes, addressing channel
            # and execution path; the agent needs both to choose a check.
            self.assertTrue(
                notes[0].startswith(("[a11y]", "[gui]", "[keyboard]")),
                "{} note lacks the addressing-channel label: {}".format(op["intent"], notes[0][:70]),
            )
            self.assertTrue(
                "[semantic] " in notes[0] or "[synthesis] " in notes[0],
                "{} note lacks the execution-path label: {}".format(op["intent"], notes[0][:70]),
            )


class ActionNotesTests(AtspiPatchedTestCase):
    """Action notes state which path ran and whether the result was confirmed."""

    def setUp(self):
        super().setUp()
        self.window = FakeNode(states=(STATE.ACTIVE,))
        self._patch("resolve_app", lambda query: FakeNode())
        self._patch("main_window", lambda app: (0, self.window))
        self._patch("extents", lambda node: runtime.frame(0, 0, 100, 100))
        self._patch("desktop_bounds", lambda: runtime.frame(0, 0, 1920, 1080))
        self._patch("find_element_at_snapshot_path", lambda app, record: None)
        self._patch("build_snapshot", lambda *a, **k: {"text": ""})
        self._patch("parse_key", lambda key: ([], key))
        self._patch("send_key", lambda key: None)
        self._patch("send_text", lambda text: None)
        self._patch("send_mouse_click", lambda *a: None)
        self._patch("coordinate_point_owner", lambda window, x, y: True)
        self._patch("time", _NoSleep())

    def _patch(self, name, value):
        original = getattr(runtime, name)
        setattr(runtime, name, value)
        self.addCleanup(setattr, runtime, name, original)

    def test_direct_write_is_reported_as_confirmed(self):
        self._patch(
            "insert_text_outcome_on_node",
            lambda root, text: ("verified", 3, 8, True),
        )

        notes = [n for n in perform_provider_operation(
            {"intent": "type", "app": "x", "text": "hello"}
        )["notes"] if not n.startswith("[pixels]")]

        self.assertEqual(len(notes), 1)
        self.assertIn("confirm it landed (3 -> 8 characters)", notes[0])
        self.assertNotIn("not verified", notes[0])
        # The confirmation covers the control, not the application: many
        # dialogs commit control state to the document only on OK/Apply.
        self.assertIn("may still require OK/Apply", notes[0])

    def test_synthesis_fallback_is_reported_as_unverified(self):
        self._patch(
            "insert_text_outcome_on_node",
            lambda root, text: ("no_change", 0, 0, False),
        )

        notes = perform_provider_operation(
            {"intent": "type", "app": "x", "text": "hello"}
        )["notes"]

        self.assertIn("fell back to", notes[0])
        self.assertIn("was not verified", notes[0])

    def test_delivery_only_action_skips_settle_and_snapshot(self):
        self._patch(
            "wait_for_ui_to_settle",
            mock.Mock(side_effect=AssertionError("settle must be skipped")),
        )
        self._patch(
            "build_snapshot",
            mock.Mock(side_effect=AssertionError("snapshot must be skipped")),
        )
        self._patch(
            "capture_window_pixels",
            mock.Mock(side_effect=AssertionError("no pixel note, no pixel sample")),
        )
        response = runtime.dispatch_provider_operation(
            {
                "app": "x",
                "input": {"key": "ENTER"},
                "observation": {"capture_snapshot": False},
            },
            operation_kind="resolve_and_act",
            action_intent="keypress",
        )
        self.assertTrue(response["ok"])
        self.assertTrue(response["observationSkipped"])
        self.assertTrue(response["actionEvidence"]["input_emitted"])
        self.assertNotIn("snapshot", response)

    def test_press_key_is_always_reported_as_unverified(self):
        notes = perform_provider_operation(
            {"intent": "keypress", "app": "x", "key": "ctrl+a"}
        )["notes"]

        self.assertIn("ctrl+a", notes[0])
        self.assertIn("was not verified", notes[0])

    def test_accessibility_click_is_reported_as_a_semantic_action(self):
        element = FakeNode()
        self._patch("find_element_at_snapshot_path", lambda app, record: element)
        self._patch("preferred_action_index", lambda node: 0)
        self._patch("do_action_by_index", lambda node, index: True)

        notes = perform_provider_operation(
            {"intent": "click", "app": "x", "click_method": "auto", "x": 1, "y": 1}
        )["notes"]

        self.assertIn("AT-SPI accessibility action", notes[0])
        self.assertNotIn("not verified", notes[0])

    def test_auto_click_reports_the_coordinate_fallback(self):
        element = FakeNode()
        self._patch("find_element_at_snapshot_path", lambda app, record: element)
        self._patch("preferred_action_index", lambda node: None)
        self._patch("do_action_by_index", lambda node, index: False)

        notes = perform_provider_operation(
            {"intent": "click", "app": "x", "click_method": "auto", "x": 1, "y": 1}
        )["notes"]

        self.assertIn("No usable AT-SPI action", notes[0])
        self.assertIn("was not verified", notes[0])

    def test_auto_click_fails_closed_for_menu_without_semantic_action(self):
        element = FakeNode()
        pointer_calls = []
        self._patch("find_element_at_snapshot_path", lambda app, record: element)
        self._patch("preferred_action_index", lambda node: None)
        self._patch("do_action_by_index", lambda node, index: False)
        self._patch("node_role", lambda node: "menu")
        self._patch("node_name", lambda node: "Format")
        self._patch(
            "send_mouse_click",
            lambda *args: pointer_calls.append(args),
        )

        with self.assertRaises(runtime.OperationFailure) as raised:
            perform_provider_operation(
                {
                    "intent": "click",
                    "app": "x",
                    "click_method": "auto",
                    "x": 1,
                    "y": 1,
                }
            )

        self.assertEqual(
            raised.exception.failure_code,
            "semantic_action_unavailable",
        )
        self.assertFalse(raised.exception.input_events_emitted)
        self.assertIn("automatic coordinate fallback is disabled", str(raised.exception))
        self.assertEqual(pointer_calls, [])

    def test_auto_multi_click_uses_real_pointer_events_not_one_semantic_action(self):
        element = FakeNode()
        self._patch("find_element_at_snapshot_path", lambda app, record: element)
        semantic_calls = []
        pointer_calls = []
        self._patch(
            "do_action_by_index",
            lambda node, index: semantic_calls.append((node, index)) or True)
        self._patch(
            "screen_point", lambda bounds, element=None, x=None, y=None: (10, 20))
        self._patch(
            "current_geometry",
            lambda element, record, bounds: (record, None))
        self._patch(
            "send_mouse_click",
            lambda x, y, button, count: pointer_calls.append(
                (x, y, button, count)))

        notes = perform_provider_operation({
            "intent": "click",
            "app": "x",
            "click_method": "auto",
            "click_count": 2,
            "element": {"frame": runtime.frame(0, 0, 10, 10)},
        })["notes"]

        self.assertEqual(semantic_calls, [])
        self.assertEqual(pointer_calls, [(10, 20, "left", 2)])
        self.assertTrue(any("2 coordinate click(s)" in note for note in notes))


class SnapshotDiagnosticsTests(AtspiPatchedTestCase):
    """An app whose accessibility tree holds only its window frame is flagged.

    Chrome started without --force-renderer-accessibility registers with
    AT-SPI and exposes its window title, yet the tree has a single element.
    Without a note the agent cannot tell an empty UI from one it cannot see.
    """

    def test_flags_app_that_exposes_only_its_window_frame(self):
        records = [
            {
                "controlType": "frame",
                "name": "about:blank - Google Chrome",
                "actions": ["doDefault", "showContextMenu"],
            }
        ]

        notes = runtime.snapshot_diagnostics(records)

        self.assertEqual(len(notes), 1)
        self.assertIn("ACCESSIBILITY CONTENT UNCERTAIN", notes[0])
        self.assertIn("does NOT prove the visible window is empty", notes[0])
        self.assertIn("not", notes[0].lower())
        self.assertNotIn("need --force-renderer-accessibility", notes[0])

    def test_stays_silent_when_the_tree_has_real_content(self):
        records = [
            {"controlType": "frame", "name": "Untitled - gedit", "actions": []},
            {"controlType": "push button", "name": "Save", "actions": ["click"]},
        ]

        self.assertEqual(runtime.snapshot_diagnostics(records), [])

    def test_a_named_non_container_alone_counts_as_content(self):
        records = [
            {"controlType": "frame", "name": "Doc", "actions": []},
            {"controlType": "label", "name": "hello", "actions": []},
        ]

        self.assertEqual(runtime.snapshot_diagnostics(records), [])

    def test_containers_without_content_do_not_count(self):
        """Unnamed containers without actions amount to an empty shell."""
        records = [
            {"controlType": "frame", "name": "App", "actions": ["doDefault"]},
            {"controlType": "panel", "name": "", "actions": []},
            {"controlType": "filler", "name": "", "actions": []},
        ]

        self.assertEqual(len(runtime.snapshot_diagnostics(records)), 1)

    def test_empty_record_set_is_not_diagnosed(self):
        """No records at all means no window was read, which is out of scope here."""
        self.assertEqual(runtime.snapshot_diagnostics([]), [])

    def test_get_app_state_returns_the_diagnostic_as_notes(self):
        shell = {
            "elements": [
                {"controlType": "frame", "name": "W", "actions": ["doDefault"]}
            ]
        }
        original = runtime.build_snapshot
        runtime.build_snapshot = lambda *a, **k: shell
        self.addCleanup(setattr, runtime, "build_snapshot", original)

        response = perform_provider_operation({"intent": "observe", "app": "x"})

        self.assertTrue(response["ok"])
        self.assertEqual(len(response["notes"]), 1)
        self.assertIn("ACCESSIBILITY CONTENT UNCERTAIN", response["notes"][0])


class ContentHostDiagnosticTests(unittest.TestCase):
    def test_generic_text_table_or_list_does_not_clear_host_uncertainty(self):
        for generic_role in ("text", "table", "list"):
            with self.subTest(role=generic_role):
                records = [
                    {
                        "runtimeId": [0],
                        "controlType": "frame",
                        "name": "Workspace",
                    },
                    {
                        "runtimeId": [0, 0],
                        "controlType": "page tab list",
                        "name": "Tabs",
                    },
                    {
                        "runtimeId": [0, 1],
                        "controlType": generic_role,
                        "name": "Sidebar",
                    },
                ]

                notes = runtime.content_host_diagnostic(records)

                self.assertEqual(len(notes), 1)
                self.assertIn("CONTENT PLANE UNCERTAIN", notes[0])
                self.assertIn(
                    "Ordinary text/table/list nodes elsewhere", notes[0])
                self.assertIn("not proof", notes[0])
                self.assertNotIn("CONTENT PLANE MISSING", notes[0])
                self.assertIn(
                    "does not expose per-node rectangles", notes[0])
                self.assertNotIn("Frame values shown here", notes[0])

    def test_distinctive_content_root_clears_the_diagnostic(self):
        records = [
            {"runtimeId": [0], "controlType": "frame", "name": "Workspace"},
            {
                "runtimeId": [0, 0],
                "controlType": "page tab list",
                "name": "Tabs",
            },
            {
                "runtimeId": [0, 1],
                "controlType": "document web",
                "name": "Page",
            },
        ]

        self.assertEqual(runtime.content_host_diagnostic(records), [])

    def test_empty_embedded_container_is_only_an_uncertain_signal(self):
        records = [
            {"runtimeId": [0], "controlType": "frame", "name": "Workspace"},
            {
                "runtimeId": [0, 0],
                "controlType": "viewport",
                "name": "",
            },
        ]

        notes = runtime.content_host_diagnostic(records)

        self.assertEqual(len(notes), 1)
        self.assertIn("CONTENT PLANE UNCERTAIN", notes[0])
        self.assertIn("uncertain diagnostic", notes[0])
        self.assertIn("renderer does not export accessibility", notes[0])
        self.assertNotIn("The content is on screen", notes[0])


class MainWindowSelectionTests(AtspiPatchedTestCase):
    """An open modal dialog is observed instead of the main window.

    With LibreOffice Writer's Format > Paragraph dialog open, the frame reports
    (SHOWING, VISIBLE) and the dialog (SHOWING, VISIBLE, MODAL); neither
    reports ACTIVE. Ranking by ACTIVE, then SHOWING, then child order picks
    the frame, whose tree contains no dialog node at all.
    """

    def _app(self, *windows):
        return FakeNode(children=windows)

    def test_visible_modal_dialog_wins_over_main_frame(self):
        frame = FakeNode(states=(STATE.SHOWING, STATE.VISIBLE))
        dialog = FakeNode(states=(STATE.SHOWING, STATE.VISIBLE, STATE.MODAL))
        self._patch_roles({id(frame): "frame", id(dialog): "dialog"})

        index, chosen = runtime.main_window(self._app(frame, dialog))

        self.assertIs(chosen, dialog)
        self.assertEqual(index, 1)

    def test_modal_dialog_wins_even_when_frame_claims_active(self):
        """A modal window blocks the others by definition, so it outranks ACTIVE."""
        frame = FakeNode(states=(STATE.SHOWING, STATE.VISIBLE, STATE.ACTIVE))
        dialog = FakeNode(states=(STATE.SHOWING, STATE.VISIBLE, STATE.MODAL))
        self._patch_roles({id(frame): "frame", id(dialog): "dialog"})

        _, chosen = runtime.main_window(self._app(frame, dialog))

        self.assertIs(chosen, dialog)

    def test_hidden_modal_dialog_does_not_win(self):
        """A closed modal dialog still attached to the tree does not take over."""
        frame = FakeNode(states=(STATE.SHOWING, STATE.VISIBLE, STATE.ACTIVE))
        stale = FakeNode(states=(STATE.MODAL,))
        self._patch_roles({id(frame): "frame", id(stale): "dialog"})

        _, chosen = runtime.main_window(self._app(frame, stale))

        self.assertIs(chosen, frame)

    def test_active_modal_wins_over_earlier_modal(self):
        """A combo box popup is a second modal window stacked on the dialog.

        In LibreOffice the Paragraph dialog is MODAL+SHOWING, while the line
        spacing popup is a separate top-level window that is also ACTIVE.
        Taking the first modal window would hide the popup's options.
        """
        dialog = FakeNode(states=(STATE.SHOWING, STATE.VISIBLE, STATE.MODAL))
        popup = FakeNode(
            states=(STATE.SHOWING, STATE.VISIBLE, STATE.MODAL, STATE.ACTIVE)
        )
        self._patch_roles({id(dialog): "dialog", id(popup): "window"})

        _, chosen = runtime.main_window(self._app(dialog, popup))

        self.assertIs(chosen, popup)

    def test_falls_back_to_active_then_showing(self):
        plain = FakeNode(states=(STATE.SHOWING,))
        active = FakeNode(states=(STATE.SHOWING, STATE.ACTIVE))
        self._patch_roles({id(plain): "frame", id(active): "frame"})

        _, chosen = runtime.main_window(self._app(plain, active))

        self.assertIs(chosen, active)

    def test_selection_metadata_reports_reason_and_all_candidates(self):
        frame = _NamedNode(
            name="Document", role="frame", states=(STATE.SHOWING,))
        dialog = _NamedNode(
            name="Confirm", role="dialog",
            states=(STATE.SHOWING, STATE.ACTIVE, STATE.MODAL))
        self._patch_roles({id(frame): "frame", id(dialog): "dialog"})

        index, chosen, selection = runtime.select_main_window(
            self._app(frame, dialog))

        self.assertEqual(index, 1)
        self.assertIs(chosen, dialog)
        self.assertEqual(selection["reason"], "active_modal")
        self.assertEqual(selection["candidateCount"], 2)
        self.assertEqual(
            [candidate["selected"] for candidate in selection["candidates"]],
            [False, True],
        )
        self.assertEqual(selection["candidates"][1]["title"], "Confirm")
        self.assertEqual(selection["candidates"][1]["role"], "dialog")

    def test_raises_when_no_window_exists(self):
        self._patch_roles({})
        with self.assertRaises(RuntimeError):
            runtime.main_window(FakeNode(children=()))

    def _patch_roles(self, mapping):
        patcher = mock.patch.object(
            FakeNode,
            "get_role_name",
            lambda node: mapping.get(id(node), "frame"),
        )
        patcher.start()
        self.addCleanup(patcher.stop)
        # The fakes have no component for the real extents() to query;
        # app_windows can classify them by role alone.
        original_ext = runtime.extents
        self.addCleanup(setattr, runtime, "extents", original_ext)
        runtime.extents = lambda node: None


class FocusWindowTests(AtspiPatchedTestCase):
    def test_active_window_needs_no_grab(self):
        component = FakeComponent(grabs=True)
        window = FakeNode(
            states=(STATE.ACTIVE,),
            children=(FakeNode(states=(STATE.FOCUSABLE,), component=component),),
        )

        self.assertTrue(runtime.focus_window(window))
        self.assertEqual(component.grab_calls, 0, "an already active window must not grab focus again")

    def test_grabs_focusable_child_to_activate_window(self):
        window = FakeNode()
        component = FakeComponent(window=window, grabs=True)
        window.children = [FakeNode(states=(STATE.FOCUSABLE,), component=component)]

        self.assertTrue(runtime.focus_window(window))
        self.assertEqual(component.grab_calls, 1)

    def test_prefers_the_previously_focused_widget(self):
        """An inactive window keeps FOCUSED on its last focused widget; try it first."""
        window = FakeNode()
        plain = FakeComponent(window=window, grabs=True)
        remembered = FakeComponent(window=window, grabs=True)
        window.children = [
            FakeNode(states=(STATE.FOCUSABLE,), component=plain),
            FakeNode(states=(STATE.FOCUSABLE, STATE.FOCUSED), component=remembered),
        ]

        self.assertTrue(runtime.focus_window(window))
        self.assertEqual(remembered.grab_calls, 1)
        self.assertEqual(plain.grab_calls, 0)

    def test_returns_false_when_nothing_can_take_focus(self):
        component = FakeComponent(grabs=False)
        window = FakeNode(
            children=(FakeNode(states=(STATE.FOCUSABLE,), component=component),)
        )

        self.assertFalse(runtime.focus_window(window))

    def test_skips_non_focusable_nodes(self):
        component = FakeComponent(grabs=True)
        window = FakeNode(children=(FakeNode(states=(STATE.VISIBLE,), component=component),))

        self.assertFalse(runtime.focus_window(window))
        self.assertEqual(component.grab_calls, 0)

    def test_caps_the_number_of_grab_attempts(self):
        """Each grab moves focus inside the app, so the whole tree is not tried."""
        components = [FakeComponent(grabs=False) for _ in range(20)]
        window = FakeNode(
            children=[
                FakeNode(states=(STATE.FOCUSABLE,), component=c) for c in components
            ]
        )

        self.assertFalse(runtime.focus_window(window))
        attempted = sum(1 for c in components if c.grab_calls)
        self.assertEqual(attempted, runtime.FOCUS_GRAB_CANDIDATES)

    def test_none_window_is_not_focusable(self):
        self.assertFalse(runtime.focus_window(None))


class RequireWindowFocusTests(AtspiPatchedTestCase):
    def test_passes_through_when_window_is_active(self):
        window = FakeNode(states=(STATE.ACTIVE,))

        runtime.require_window_focus(window, "press_key")  # must not raise

    def test_raises_instead_of_synthesizing_into_the_wrong_window(self):
        component = FakeComponent(grabs=False)
        window = FakeNode(
            children=(FakeNode(states=(STATE.FOCUSABLE,), component=component),)
        )

        with self.assertRaises(RuntimeError) as ctx:
            runtime.require_window_focus(window, "press_key")
        message = str(ctx.exception)
        self.assertIn("press_key", message)
        self.assertIn("foreground", message)


class TargetedKeyboardFocusTests(AtspiPatchedTestCase):
    def test_already_focused_target_needs_no_grab(self):
        element = FakeNode(states=(STATE.FOCUSED, STATE.EDITABLE))
        window = FakeNode(states=(STATE.ACTIVE,), children=(element,))

        runtime.focus_target_element(
            window, element,
            {"runtimeId": [0], "controlType": "", "name": ""},
            "targeted type_text",
        )

    def test_grab_focus_must_land_on_the_exact_target(self):
        component = FakeComponent()
        element = FakeNode(
            states=(STATE.EDITABLE,), component=component)
        component.grab_focus = lambda: element.states.add(STATE.FOCUSED) or True
        window = FakeNode(states=(STATE.ACTIVE,), children=(element,))

        runtime.focus_target_element(
            window, element,
            {"runtimeId": [0], "controlType": "", "name": ""},
            "targeted press_key",
        )
        self.assertIn(STATE.FOCUSED, element.states)

    def test_focus_failure_emits_no_keyboard_input(self):
        component = FakeComponent(grabs=False)
        element = FakeNode(
            states=(STATE.EDITABLE,), component=component)
        window = FakeNode(states=(STATE.ACTIVE,), children=(element,))

        with self.assertRaises(RuntimeError) as caught:
            runtime.focus_target_element(
                window, element,
                {"runtimeId": [0], "controlType": "", "name": ""},
                "targeted type_text",
            )
        self.assertIn("no keyboard input was emitted", str(caught.exception))

    def test_same_name_focused_sibling_does_not_satisfy_target_focus(self):
        target = _NamedNode(
            name="Search", role="entry",
            component=FakeComponent(grabs=False))
        sibling = _NamedNode(
            name="Search", role="entry",
            states=(STATE.FOCUSED,))
        window = FakeNode(states=(STATE.ACTIVE,), children=(target, sibling))

        with self.assertRaises(RuntimeError):
            runtime.focus_target_element(
                window, target,
                {"runtimeId": [0], "controlType": "entry", "name": "Search"},
                "targeted type_text",
            )

    def test_exact_target_resolution_never_rebinds_to_same_name_sibling(self):
        replacement = _NamedNode(name="Other", role="entry")
        same_name = _NamedNode(name="Search", role="entry")
        window = _NamedNode(
            name="Window", role="frame",
            states=(STATE.ACTIVE, STATE.SHOWING),
            children=(replacement, same_name),
            component=FakeComponent(extents=FakeExtents(0, 0, 100, 100)),
        )
        app = _NamedNode(name="App", role="application", children=(window,))
        record = {
            "runtimeId": [0, 0],
            "controlType": "entry",
            "name": "Search",
        }

        self.assertIsNone(runtime.find_element_at_snapshot_path(app, record))

    def test_targeted_type_text_uses_the_resolved_node(self):
        target = FakeNode(states=(STATE.EDITABLE, STATE.FOCUSED))
        other = FakeNode(states=(STATE.EDITABLE, STATE.FOCUSED))
        window = FakeNode(states=(STATE.ACTIVE,), children=(other, target))
        observed = []

        with mock.patch.object(runtime, "resolve_app", return_value=FakeNode()), \
                mock.patch.object(runtime, "main_window", return_value=(0, window)), \
                mock.patch.object(runtime, "extents",
                                  return_value=runtime.frame(0, 0, 100, 100)), \
                mock.patch.object(
                    runtime, "find_element_at_snapshot_path", return_value=target), \
                mock.patch.object(runtime, "current_geometry",
                                  side_effect=lambda e, r, b: (r, None)), \
                mock.patch.object(runtime, "focus_target_element"), \
                mock.patch.object(
                    runtime, "insert_text_outcome_on_node",
                    side_effect=lambda node, text: observed.append(
                        (node, text)) or ("verified", 0, len(text), True)), \
                mock.patch.object(runtime, "wait_for_ui_to_settle",
                                  return_value=None), \
                mock.patch.object(runtime, "build_snapshot",
                                  return_value={"text": "", "elements": []}), \
                mock.patch.object(runtime, "capture_window_pixels",
                                  return_value=None):
            response = perform_provider_operation({
                "intent": "type",
                "app": "x",
                "text": "hello",
                "element": {"runtimeId": [0, 1], "controlType": "", "name": ""},
            })

        self.assertTrue(response["ok"])
        self.assertEqual(observed, [(target, "hello")])

    def test_keyboard_fallback_reverifies_target_focus(self):
        target = FakeNode(states=(STATE.EDITABLE, STATE.FOCUSED))
        window = FakeNode(states=(STATE.ACTIVE,), children=(target,))
        focus_checks = []
        sent = []

        def check_focus(*_args):
            focus_checks.append(True)
            if len(focus_checks) == 2:
                raise RuntimeError("focus moved")

        with mock.patch.object(runtime, "resolve_app", return_value=FakeNode()), \
                mock.patch.object(runtime, "main_window", return_value=(0, window)), \
                mock.patch.object(runtime, "extents",
                                  return_value=runtime.frame(0, 0, 100, 100)), \
                mock.patch.object(
                    runtime, "find_element_at_snapshot_path", return_value=target), \
                mock.patch.object(runtime, "current_geometry",
                                  side_effect=lambda e, r, b: (r, None)), \
                mock.patch.object(
                    runtime, "focus_target_element", side_effect=check_focus), \
                mock.patch.object(
                    runtime, "insert_text_outcome_on_node",
                    return_value=("no_change", 0, 0, False)), \
                mock.patch.object(
                    runtime, "send_text",
                    side_effect=lambda text: sent.append(text)), \
                mock.patch.object(runtime, "capture_window_pixels",
                                  return_value=None):
            with self.assertRaises(RuntimeError):
                perform_provider_operation({
                    "intent": "type",
                    "app": "x",
                    "text": "hello",
                    "element": {
                        "runtimeId": [0, 0],
                        "controlType": "",
                        "name": "",
                    },
                })

        self.assertEqual(len(focus_checks), 2)
        self.assertEqual(sent, [])


class KeySyntaxTests(unittest.TestCase):
    def test_accepts_common_gtk_and_x11_key_spellings(self):
        self.assertEqual(
            runtime.normalize_key_expression("<Control><Shift>s"),
            "Control+Shift+s",
        )
        self.assertEqual(runtime.MODIFIER_KEYS["control_l"], "Control_L")
        self.assertEqual(runtime.MODIFIER_KEYS["ctrl_l"], "Control_L")
        self.assertEqual(runtime.KEY_ALIASES["arrowdown"], "Down")
        self.assertEqual(runtime.KEY_ALIASES["arrowup"], "Up")
        self.assertEqual(runtime.KEY_ALIASES["pageup"], "Page_Up")
        self.assertEqual(runtime.KEY_ALIASES["pagedown"], "Page_Down")
        self.assertEqual(runtime.KEY_ALIASES["slash"], "slash")
        self.assertEqual(runtime.KEY_ALIASES["forward_slash"], "slash")


class KeyResolutionTests(unittest.TestCase):
    """Keys resolve to hardware keycodes before any input is sent."""

    def setUp(self):
        if runtime.Gdk is None:
            self.skipTest("Gdk is unavailable")

    def test_punctuation_main_key_with_modifier(self):
        # Gdk.keyval_from_name(",") returns VoidSymbol, which is truthy, so
        # CTRL+, (VS Code settings) failed with "No hardware keycode".
        for key, keysym_name in (
            ("CTRL+,", "comma"),
            ("ctrl+.", "period"),
            ("ctrl+/", "slash"),
        ):
            with self.subTest(key=key):
                self.assertEqual(
                    runtime.keysym(key.split("+")[-1]),
                    runtime.Gdk.keyval_from_name(keysym_name),
                )

    def test_named_keys_in_any_case(self):
        for key, expected in (
            ("INSERT", "Insert"),
            ("caps_lock", "Caps_Lock"),
            ("MENU", "Menu"),
            ("f4", "F4"),
        ):
            with self.subTest(key=key):
                self.assertEqual(
                    runtime.keysym(key), runtime.Gdk.keyval_from_name(expected)
                )

    def test_bare_modifier_is_a_key(self):
        # keycode needs a display keymap; the name it receives is the point.
        with mock.patch.object(runtime, "keycode", side_effect=lambda name: 1):
            _codes, normalized = runtime.parse_key("SHIFT")
        self.assertEqual(normalized, "Shift_L")

    def test_unknown_key_still_fails(self):
        self.assertEqual(runtime.keysym("NoSuchKeyName"), 0)
        with self.assertRaises(RuntimeError):
            runtime.keycode("NoSuchKeyName")


class PerformOperationGuardTests(AtspiPatchedTestCase):
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

    def test_operation_failure_response_reports_complete_no_input_facts(self):
        exc = runtime.OperationFailure(
            "stale",
            failure_code="stale_element_ref",
            failure_phase="precondition",
            input_events_emitted=False,
        )
        response = runtime.operation_failure_response(exc)
        self.assertFalse(response["input_events_emitted"])
        self.assertFalse(response["partial_input_possible"])
        self.assertFalse(response["mutation_unknown"])
        self.assertTrue(response["failureFactsComplete"])
        self.assertEqual(response["failureSubject"], "element")

    def test_observation_incomplete_marks_failure_facts_incomplete(self):
        response = runtime.operation_failure_response(runtime.OperationFailure(
            "window inventory unavailable",
            failure_code="observation_incomplete",
            failure_phase="precondition",
            failure_subject="window",
        ))
        self.assertFalse(response["failureFactsComplete"])

    def test_runtime_failure_after_input_preserves_mutation_unknown(self):
        runtime._CURRENT_ACTION_EVIDENCE = {
            "route": "keyboard_synthesis",
            "input_emitted": True,
        }
        self.addCleanup(setattr, runtime, "_CURRENT_ACTION_EVIDENCE", None)

        response = runtime.unexpected_operation_failure_response(
            RuntimeError("snapshot failed")
        )

        self.assertEqual(response["failureCode"], "effect_unknown")
        self.assertTrue(response["input_events_emitted"])
        self.assertTrue(response["mutation_unknown"])
        self.assertFalse(response["failureFactsComplete"])

    def test_operation_failure_after_input_rewrites_precondition_claim(self):
        runtime._CURRENT_ACTION_EVIDENCE = {
            "route": "semantic_action",
            "input_emitted": True,
        }
        self.addCleanup(setattr, runtime, "_CURRENT_ACTION_EVIDENCE", None)

        response = runtime.operation_failure_response(
            runtime.OperationFailure(
                "stale_app_ref; no action was attempted",
                failure_code="stale_app_ref",
                failure_phase="precondition",
                input_events_emitted=False,
            )
        )

        self.assertEqual(response["failureCode"], "effect_unknown")
        self.assertEqual(
            response["failurePhase"], "post_action_observation"
        )
        self.assertEqual(
            response["underlyingFailureCode"], "stale_app_ref"
        )
        self.assertTrue(response["input_events_emitted"])
        self.assertTrue(response["mutation_unknown"])
        self.assertNotIn("no action was attempted", response["error"])

    def test_observation_failure_subjects_are_layered(self):
        cases = [
            (
                "observation_incomplete",
                "accessibility application inventory could not be read",
                "app",
            ),
            (
                "observation_incomplete",
                "application window inventory could not be read",
                "window",
            ),
            (
                "observation_incomplete",
                "provider identity could not be checked",
                "element",
            ),
        ]
        for code, message, expected in cases:
            with self.subTest(message=message):
                exc = runtime.OperationFailure(
                    message,
                    failure_code=code,
                    failure_phase="precondition",
                )
                self.assertEqual(exc.failure_subject, expected)

    def test_replacement_window_is_machine_readable(self):
        replacement = {
            "windowHandle": "w2:0123456789abcdef0123456789abcdef",
            "title": "Question",
            "role": "dialog",
        }
        exc = runtime.OperationFailure(
            "stale",
            failure_code="stale_element_ref",
            failure_phase="precondition",
            replacement_window=replacement,
        )
        response = runtime.operation_failure_response(exc)
        self.assertEqual(response["failureSubject"], "element")
        self.assertEqual(response["replacementWindow"], replacement)

    def test_recovery_window_rejects_ambiguous_visible_candidates(self):
        first = FakeNode()
        second = FakeNode()
        windows = runtime.InventoryResult([(0, first), (1, second)])
        with (
            mock.patch.object(recovery, "state_value", return_value=True),
            mock.patch.object(
                recovery,
                "strong_window_instance_handle_inventory",
                return_value=runtime.InventoryResult(
                    [(0, "w2:first"), (1, "w2:second")],
                    complete=True,
                ),
            ),
        ):
            self.assertEqual(
                runtime.recovery_window_candidate(FakeNode(), windows),
                {},
            )

    def test_bounded_recovery_reports_gone_source_and_one_current_window(self):
        app = FakeNode()
        current = FakeNode(states={STATE.SHOWING})
        metadata = {
            app: (":1.42", "", 4242, "Editor", "application"),
            current: (":1.42", "/window/current", 4242, "Save As", "dialog"),
        }
        operation = {
            "app_identity": {
                "providerBusName": ":1.42",
                "processGeneration": "gen-4242",
            },
            "target": {
                "windowHandle": "w2:source",
                "providerWindowBusName": ":1.42",
                "providerWindowObjectPath": "/window/source",
                "providerWindowProcessGeneration": "gen-4242",
            },
        }
        with (
            mock.patch.object(
                recovery,
                "iter_apps",
                return_value=runtime.InventoryResult([app], complete=True),
            ),
            mock.patch.object(
                recovery,
                "app_windows",
                return_value=runtime.InventoryResult([(1, current)], complete=True),
            ),
            mock.patch.object(
                recovery,
                "provider_bus_name",
                side_effect=lambda node: metadata[node][0],
            ),
            mock.patch.object(
                recovery,
                "provider_object_path",
                side_effect=lambda node: metadata[node][1],
            ),
            mock.patch.object(
                recovery,
                "node_pid",
                side_effect=lambda node: metadata[node][2],
            ),
            mock.patch.object(
                recovery,
                "node_name",
                side_effect=lambda node: metadata[node][3],
            ),
            mock.patch.object(
                recovery,
                "node_role",
                side_effect=lambda node: metadata[node][4],
            ),
            mock.patch.object(
                recovery,
                "process_generation",
                side_effect=lambda pid: "gen-{}".format(pid),
            ),
            mock.patch.object(
                recovery,
                "strong_window_instance_handle_inventory",
                return_value=runtime.InventoryResult(
                    [(1, "w2:current")],
                    complete=True,
                ),
            ),
        ):
            context = recovery.bounded_recovery_context(operation)

        self.assertEqual(context["sourceAppStatus"], "alive")
        self.assertEqual(context["sourceWindowStatus"], "gone")
        self.assertTrue(context["inventoryComplete"])
        self.assertEqual(context["currentWindow"], {
            "windowHandle": "w2:current",
            "title": "Save As",
            "role": "dialog",
            "providerBusName": ":1.42",
            "providerObjectPath": "/window/current",
            "processGeneration": "gen-4242",
        })
        self.assertNotIn("replacementWindow", context)

    def test_bounded_recovery_ignores_visible_root_without_strong_handle(self):
        app = FakeNode()
        current = FakeNode(states={STATE.SHOWING, STATE.ACTIVE})
        residual = FakeNode(states={STATE.SHOWING, STATE.MODAL})
        metadata = {
            app: (":1.42", "", 4242, "Editor", "application"),
            current: (":1.42", "/window/current", 4242, "Document", "frame"),
            residual: (":1.42", "/window/residual", 4242, "Closed", "dialog"),
        }
        operation = {
            "app_identity": {
                "providerBusName": ":1.42",
                "processGeneration": "gen-4242",
            },
            "window_handle": "w2:source",
        }
        with (
            mock.patch.object(
                recovery,
                "iter_apps",
                return_value=runtime.InventoryResult([app], complete=True),
            ),
            mock.patch.object(
                recovery,
                "app_windows",
                return_value=runtime.InventoryResult(
                    [(0, current), (1, residual)],
                    complete=True,
                ),
            ),
            mock.patch.object(
                recovery,
                "provider_bus_name",
                side_effect=lambda node: metadata[node][0],
            ),
            mock.patch.object(
                recovery,
                "provider_object_path",
                side_effect=lambda node: metadata[node][1],
            ),
            mock.patch.object(
                recovery,
                "node_pid",
                side_effect=lambda node: metadata[node][2],
            ),
            mock.patch.object(
                recovery,
                "node_name",
                side_effect=lambda node: metadata[node][3],
            ),
            mock.patch.object(
                recovery,
                "node_role",
                side_effect=lambda node: metadata[node][4],
            ),
            mock.patch.object(
                recovery,
                "process_generation",
                side_effect=lambda pid: "gen-{}".format(pid),
            ),
            mock.patch.object(
                recovery,
                "strong_window_instance_handle_inventory",
                return_value=runtime.InventoryResult(
                    [(0, "w2:current")],
                    complete=True,
                ),
            ),
        ):
            context = recovery.bounded_recovery_context(operation)

        self.assertEqual(context["sourceWindowStatus"], "gone")
        self.assertTrue(context["inventoryComplete"])
        self.assertEqual(
            context["currentWindow"]["windowHandle"],
            "w2:current",
        )

    def test_read_only_stale_observe_includes_recovery_context(self):
        exc = runtime.OperationFailure(
            "stale window",
            failure_code="stale_window_ref",
            failure_phase="precondition",
            failure_facts_complete=True,
        )
        recovery_context = {
            "sourceAppStatus": "alive",
            "sourceWindowStatus": "gone",
            "inventoryComplete": True,
        }
        with (
            mock.patch.object(
                response_builder.action_evidence,
                "snapshot",
                return_value={},
            ),
            mock.patch.object(
                response_builder,
                "bounded_recovery_context",
                return_value=recovery_context,
            ),
        ):
            response = runtime.operation_failure_response(
                exc,
                recovery_operation={
                    "app_identity": {
                        "providerBusName": ":1.42",
                        "processGeneration": "gen-4242",
                    },
                    "window_handle": "w2:source",
                },
            )

        self.assertEqual(response["failureCode"], "stale_window_ref")
        self.assertFalse(response["input_events_emitted"])
        self.assertFalse(response["mutation_unknown"])
        self.assertEqual(response["recoveryContext"], recovery_context)

    def test_bounded_recovery_reports_exited_only_from_complete_inventory(self):
        operation = {
            "app_identity": {
                "providerBusName": ":1.42",
                "processGeneration": "gen-4242",
            },
        }
        with mock.patch.object(
            recovery,
            "iter_apps",
            return_value=runtime.InventoryResult([], complete=True),
        ):
            context = recovery.bounded_recovery_context(operation)

        self.assertEqual(context, {
            "sourceAppStatus": "exited",
            "sourceWindowStatus": "gone",
            "inventoryComplete": True,
        })

    def test_bounded_recovery_reports_present_without_guessing_current_window(self):
        app = FakeNode()
        source = FakeNode(states={STATE.SHOWING})
        peer = FakeNode(states={STATE.SHOWING})
        operation = {
            "app_identity": {
                "providerBusName": ":1.42",
                "processGeneration": "gen-4242",
            },
            "target": {
                "windowHandle": "w2:source",
                "providerWindowBusName": ":1.42",
                "providerWindowObjectPath": "/window/source",
                "providerWindowProcessGeneration": "gen-4242",
            },
        }
        with (
            mock.patch.object(
                recovery,
                "iter_apps",
                return_value=runtime.InventoryResult([app], complete=True),
            ),
            mock.patch.object(
                recovery,
                "app_windows",
                return_value=runtime.InventoryResult(
                    [(0, source), (1, peer)],
                    complete=True,
                ),
            ),
            mock.patch.object(
                recovery,
                "provider_bus_name",
                side_effect=lambda node: ":1.42",
            ),
            mock.patch.object(
                recovery,
                "provider_object_path",
                side_effect=lambda node: (
                    "/window/source" if node is source else "/window/peer"
                ),
            ),
            mock.patch.object(
                recovery,
                "node_pid",
                return_value=4242,
            ),
            mock.patch.object(
                recovery,
                "process_generation",
                return_value="gen-4242",
            ),
            mock.patch.object(
                recovery,
                "strong_window_instance_handle_inventory",
                return_value=runtime.InventoryResult(
                    [(0, "w2:source"), (1, "w2:peer")],
                    complete=True,
                ),
            ),
        ):
            context = recovery.bounded_recovery_context(operation)

        self.assertEqual(context["sourceAppStatus"], "alive")
        self.assertEqual(context["sourceWindowStatus"], "present")
        self.assertTrue(context["inventoryComplete"])
        self.assertNotIn("currentWindow", context)

    def test_bounded_recovery_keeps_absence_unknown_when_inventory_incomplete(self):
        operation = {
            "app_identity": {
                "providerBusName": ":1.42",
                "processGeneration": "gen-4242",
            },
        }
        with mock.patch.object(
            recovery,
            "iter_apps",
            return_value=runtime.InventoryResult([], complete=False),
        ):
            context = recovery.bounded_recovery_context(operation)

        self.assertEqual(context, {
            "sourceAppStatus": "unknown",
            "sourceWindowStatus": "unknown",
            "inventoryComplete": False,
        })

    def test_bounded_recovery_does_not_prove_gone_when_native_inventory_fails(self):
        app = FakeNode()
        current = FakeNode(states={STATE.SHOWING})
        operation = {
            "app_identity": {
                "providerBusName": ":1.42",
                "processGeneration": "gen-4242",
            },
            "target": {"windowHandle": "w2:source"},
        }
        with (
            mock.patch.object(
                recovery,
                "iter_apps",
                return_value=runtime.InventoryResult([app], complete=True),
            ),
            mock.patch.object(
                recovery,
                "app_windows",
                return_value=runtime.InventoryResult([(0, current)], complete=True),
            ),
            mock.patch.object(recovery, "provider_bus_name", return_value=":1.42"),
            mock.patch.object(recovery, "node_pid", return_value=4242),
            mock.patch.object(
                recovery,
                "process_generation",
                return_value="gen-4242",
            ),
            mock.patch.object(
                recovery,
                "strong_window_instance_handle_inventory",
                return_value=runtime.InventoryResult([], complete=False),
            ),
        ):
            context = recovery.bounded_recovery_context(operation)

        self.assertFalse(context["inventoryComplete"])
        self.assertEqual(context["sourceWindowStatus"], "unknown")
        self.assertNotIn("currentWindow", context)

    def test_bounded_recovery_does_not_choose_window_when_visibility_is_unknown(self):
        app = FakeNode()
        unknown = FakeNode()
        visible = FakeNode(states={STATE.SHOWING})
        operation = {
            "app_identity": {
                "providerBusName": ":1.42",
                "processGeneration": "gen-4242",
            },
        }
        with (
            mock.patch.object(
                recovery,
                "iter_apps",
                return_value=runtime.InventoryResult([app], complete=True),
            ),
            mock.patch.object(
                recovery,
                "app_windows",
                return_value=runtime.InventoryResult(
                    [(0, unknown), (1, visible)],
                    complete=True,
                ),
            ),
            mock.patch.object(recovery, "provider_bus_name", return_value=":1.42"),
            mock.patch.object(recovery, "node_pid", return_value=4242),
            mock.patch.object(
                recovery,
                "process_generation",
                return_value="gen-4242",
            ),
            mock.patch.object(
                recovery,
                "strong_window_instance_handle_inventory",
                return_value=runtime.InventoryResult(
                    [(0, "w2:unknown"), (1, "w2:visible")],
                    complete=True,
                ),
            ),
            mock.patch.object(
                recovery,
                "state_value",
                side_effect=lambda node, _state: (
                    None if node is unknown else True
                ),
            ),
        ):
            context = recovery.bounded_recovery_context(operation)

        self.assertFalse(context["inventoryComplete"])
        self.assertNotIn("currentWindow", context)

    def test_expected_transient_window_rejects_provider_replacement(self):
        window = FakeNode()
        expectation = {
            "windowHandle": "w2:source",
            "providerBusName": ":1.42",
            "providerObjectPath": "/dialog/old",
            "processGeneration": "gen-4242",
            "role": "file chooser",
            "name": "Save As",
        }
        with (
            mock.patch.object(runtime, "provider_bus_name", return_value=":1.42"),
            mock.patch.object(runtime, "provider_object_path", return_value="/window/document"),
            mock.patch.object(runtime, "node_pid", return_value=4242),
            mock.patch.object(runtime, "process_generation", return_value="gen-4242"),
            mock.patch.object(runtime, "node_role", return_value="frame"),
            mock.patch.object(runtime, "node_name", return_value="Document"),
        ):
            with self.assertRaises(runtime.OperationFailure) as caught:
                runtime._verify_expected_window(window, expectation)
        self.assertEqual(caught.exception.failure_code, "stale_target")
        self.assertFalse(caught.exception.input_events_emitted)

    def test_expected_transient_window_rejects_same_role_new_provider_owner(self):
        window = FakeNode()
        expectation = {
            "windowHandle": "w2:source",
            "providerBusName": ":1.42",
            "providerObjectPath": "/dialog/old",
            "processGeneration": "gen-4242",
            "role": "dialog",
            "name": "Question",
        }
        with (
            mock.patch.object(runtime, "provider_bus_name", return_value=":1.42"),
            mock.patch.object(
                runtime,
                "provider_object_path",
                return_value="/dialog/later",
            ),
            mock.patch.object(runtime, "node_pid", return_value=4242),
            mock.patch.object(runtime, "process_generation", return_value="gen-4242"),
            mock.patch.object(runtime, "node_role", return_value="dialog"),
            mock.patch.object(runtime, "node_name", return_value="Question"),
        ):
            with self.assertRaises(runtime.OperationFailure) as caught:
                runtime._verify_expected_window(window, expectation)
        self.assertEqual(caught.exception.failure_code, "stale_target")
        self.assertFalse(caught.exception.input_events_emitted)

    def test_expected_native_window_allows_provider_object_rebuild(self):
        window = FakeNode()
        expectation = {
            "windowHandle": "w2:source",
            "providerBusName": ":1.42",
            "providerObjectPath": "/window/old",
            "processGeneration": "gen-4242",
            "role": "frame",
            "name": "Old title",
        }
        with (
            mock.patch.object(runtime, "provider_bus_name", return_value=":1.42"),
            mock.patch.object(runtime, "provider_object_path", return_value="/window/new"),
            mock.patch.object(runtime, "node_pid", return_value=4242),
            mock.patch.object(runtime, "process_generation", return_value="gen-4242"),
            mock.patch.object(runtime, "node_role", return_value="frame"),
            mock.patch.object(runtime, "node_name", return_value="New title"),
        ):
            runtime._verify_expected_window(window, expectation)

    def test_expected_transient_window_allows_title_only_change(self):
        window = FakeNode()
        expectation = {
            "windowHandle": "w2:source",
            "providerBusName": ":1.42",
            "providerObjectPath": "/dialog/options",
            "processGeneration": "gen-4242",
            "role": "dialog",
            "name": "Options - General",
        }
        with (
            mock.patch.object(runtime, "provider_bus_name", return_value=":1.42"),
            mock.patch.object(
                runtime,
                "provider_object_path",
                return_value="/dialog/options",
            ),
            mock.patch.object(runtime, "node_pid", return_value=4242),
            mock.patch.object(runtime, "process_generation", return_value="gen-4242"),
            mock.patch.object(runtime, "node_role", return_value="dialog"),
            mock.patch.object(runtime, "node_name", return_value="Options - View"),
        ):
            runtime._verify_expected_window(window, expectation)

    def test_recovery_failure_cannot_mask_effect_unknown(self):
        with (
            mock.patch.object(
                response_builder.action_evidence,
                "snapshot",
                return_value={"input_emitted": True},
            ),
            mock.patch.object(
                response_builder,
                "bounded_recovery_context",
                side_effect=RuntimeError("inventory failed"),
            ),
        ):
            response = response_builder.unexpected_operation_failure_response(
                RuntimeError("snapshot failed"),
                recovery_operation={"app": "Editor"},
            )

        self.assertEqual(response["failureCode"], "effect_unknown")
        self.assertTrue(response["mutation_unknown"])
        self.assertEqual(response["recoveryContext"], {
            "sourceAppStatus": "unknown",
            "sourceWindowStatus": "unknown",
            "inventoryComplete": False,
        })

    def test_operation_failure_after_input_includes_recovery_context(self):
        recovery_context = {
            "sourceAppStatus": "alive",
            "sourceWindowStatus": "present",
            "inventoryComplete": True,
        }
        with (
            mock.patch.object(
                response_builder.action_evidence,
                "snapshot",
                return_value={"input_emitted": True},
            ),
            mock.patch.object(
                response_builder,
                "bounded_recovery_context",
                return_value=recovery_context,
            ),
        ):
            response = response_builder.operation_failure_response(
                runtime.OperationFailure(
                    "observation_incomplete",
                    failure_code="observation_incomplete",
                    failure_phase="observation",
                ),
                recovery_operation={"app": "Editor"},
            )

        self.assertEqual(response["failureCode"], "effect_unknown")
        self.assertTrue(response["mutation_unknown"])
        self.assertEqual(response["recoveryContext"], recovery_context)

    def test_explicit_failure_subject_overrides_message_inference(self):
        exc = runtime.OperationFailure(
            "observation_incomplete: provider text that mentions window inventory",
            failure_code="observation_incomplete",
            failure_phase="precondition",
            failure_subject="element",
        )
        self.assertEqual(exc.failure_subject, "element")
