"""Input method acknowledgements, focus changes and partial-emission evidence."""

from types import SimpleNamespace

from runtime_test_support import *  # noqa: F401,F403


class InputMethodAcknowledgementTests(unittest.TestCase):
    def setUp(self):
        self.clock = 0.0
        self.pending = []
        self.events = []
        self.committed = []
        self.current_context = "/org/freedesktop/IBus/InputContext_7"
        self.commit_delay = 0.2
        self.commit_value = None
        self.acknowledge_preedit = True
        self.focus_on_preedit = None
        self.ascii = ""
        self.composer = object.__new__(runtime.InputMethodText)
        self.composer.emitted = False
        self.composer.preedit = False
        self.composer.allowed_preedit = None
        self.composer.preedit_error = False
        self.composer.commits = []
        self.composer.context = self.current_context
        self.composer.engine_name = "xkb:us::eng"
        self.engine = ("IBusEngineDesc", {}, "xkb:us::eng")
        self.composer.deadline = 20.0
        self.composer.subscription = None
        self.composer.rule = None
        self.composer.property = lambda name: (
            self.current_context if name == "CurrentInputContext" else self.engine)
        context = SimpleNamespace(
            pending=lambda: bool(self.pending and self.pending[0][0] <= self.clock),
            iteration=lambda block: self.pending.pop(0)[1]())
        self.composer.GLib = SimpleNamespace(
            MainContext=SimpleNamespace(default=lambda: context))
        self.time_patch = mock.patch.object(runtime.time, "monotonic",
                                           side_effect=lambda: self.clock)
        self.sleep_patch = mock.patch.object(runtime.time, "sleep",
                                            side_effect=self.advance)
        self.time_patch.start()
        self.sleep_patch.start()
        self.addCleanup(self.time_patch.stop)
        self.addCleanup(self.sleep_patch.stop)

    def advance(self, seconds):
        self.clock += seconds

    def signal(self, member, text, visible=True):
        if member == "CommitText":
            self.committed.append(text)
        text_value = ("IBusText", {}, text, ())
        values = ((text_value,) if member == "CommitText"
                  else (text_value, len(text), visible))
        self.composer.receive(None, None, self.current_context, None, member,
                              SimpleNamespace(unpack=lambda: values), None)

    def preedit(self):
        if self.focus_on_preedit:
            self.current_context = self.focus_on_preedit
        self.signal("UpdatePreeditText", "u")
        self.ascii = ""

    def key(self, key):
        self.events.append((self.clock, "key", key))
        if key == "CTRL+SHIFT+u" and self.acknowledge_preedit:
            self.pending.append((self.clock + 0.05, self.preedit))
        elif key == "ENTER":
            char = self.commit_value or chr(int(self.ascii, 16))
            self.pending.append((self.clock + self.commit_delay,
                                 lambda: self.signal("CommitText", char)))

    def text(self, text):
        self.ascii += text
        self.events.append((self.clock, "text", text))
        value = "u" + self.ascii
        self.pending.append((self.clock + 0.01,
                             lambda: self.signal("UpdatePreeditText", value)))

    def test_waits_for_delayed_commit_before_next_character(self):
        self.composer.send("中ü", self.key, self.text)
        prefixes = [t for t, kind, value in self.events
                    if value == "CTRL+SHIFT+u"]
        self.assertEqual(len(prefixes), 2)
        self.assertGreaterEqual(prefixes[1], 0.25)
        self.assertEqual([value for _, kind, value in self.events if kind == "text"],
                         list("4e2dfc"))

    def test_ascii_line_feed_and_tab_reach_input_method_as_literal_text(self):
        payload = "left\nright\tend"
        with mock.patch.object(runtime, "InputMethodText", return_value=self.composer), \
                mock.patch.object(runtime, "send_key", side_effect=self.key), \
                mock.patch.object(runtime, "emit_keyboard_event",
                                  side_effect=lambda code, text, kind, detail: self.text(text)):
            runtime.send_text(payload)

        self.assertEqual("".join(self.committed), payload)
        self.assertEqual([value for _, kind, value in self.events if kind == "key"],
                         [key for _ in payload for key in ("CTRL+SHIFT+u", "ENTER")])

    def test_ascii_control_text_without_input_method_fails_before_input(self):
        for payload in ("line\nnext", "left\tright"):
            with self.subTest(payload=payload), \
                    mock.patch.object(runtime, "ibus_bindings", side_effect=ValueError("missing")), \
                    mock.patch.object(runtime, "emit_keyboard_event") as keyboard:
                with self.assertRaises(runtime.OperationFailure) as caught:
                    runtime.send_text(payload)
                self.assertFalse(caught.exception.input_events_emitted)
                keyboard.assert_not_called()

    def test_missing_preedit_does_not_send_hex_or_enter(self):
        self.acknowledge_preedit = False
        with self.assertRaises(runtime.OperationFailure) as caught:
            self.composer.send("中文", self.key, self.text)
        self.assertTrue(caught.exception.input_events_emitted)
        self.assertEqual([value for _, _, value in self.events], ["CTRL+SHIFT+u"])

    def test_wrong_commit_stops_before_next_character(self):
        self.commit_value = "ü"
        with self.assertRaises(runtime.OperationFailure) as caught:
            self.composer.send("中文", self.key, self.text)
        self.assertTrue(caught.exception.input_events_emitted)
        self.assertEqual([value for _, _, value in self.events],
                         ["CTRL+SHIFT+u", "4", "e", "2", "d", "ENTER"])

    def test_focus_change_after_preedit_stops_before_digits(self):
        self.focus_on_preedit = "/org/freedesktop/IBus/InputContext_8"
        with self.assertRaises(runtime.OperationFailure):
            self.composer.send("中", self.key, self.text)
        self.assertEqual(len(self.events), 1)

    def test_whole_text_deadline_is_not_reset_per_character(self):
        self.composer.deadline = 0.3
        with self.assertRaises(runtime.OperationFailure):
            self.composer.send("中文", self.key, self.text)
        self.assertLess(self.clock, 0.31)
        self.assertEqual([value for _, kind, value in self.events if kind == "text"],
                         list("4e2d"))

    def test_each_hex_digit_waits_for_application_preedit(self):
        self.composer.send("中", self.key, self.text)
        emitted = [(t, value) for t, kind, value in self.events if kind == "text"]
        self.assertEqual([value for _, value in emitted], list("4e2d"))
        for (earlier, _), (later, _) in zip(emitted, emitted[1:]):
            self.assertGreaterEqual(later - earlier, 0.01 - 1e-9)

    def test_unexpected_hex_preedit_stops_before_next_digit_or_enter(self):
        def wrong_digit(text):
            self.events.append((self.clock, "text", text))
            self.pending.append((self.clock + 0.01,
                                 lambda: self.signal("UpdatePreeditText", "u8")))
        with self.assertRaises(runtime.OperationFailure) as caught:
            self.composer.send("中", self.key, wrong_digit)
        self.assertTrue(caught.exception.input_events_emitted)
        self.assertEqual([value for _, _, value in self.events],
                         ["CTRL+SHIFT+u", "4"])

    def test_missing_ibus_fails_before_keyboard_input(self):
        with mock.patch.object(runtime, "ibus_bindings", side_effect=ValueError("missing")), \
                mock.patch.object(runtime, "emit_keyboard_event") as keyboard:
            with self.assertRaises(runtime.OperationFailure) as caught:
                runtime.send_text("hello 中")
        self.assertFalse(caught.exception.input_events_emitted)
        keyboard.assert_not_called()

    def test_subscriptions_are_removed_after_failed_input(self):
        self.composer.connection = mock.Mock()
        self.composer.subscription = 17
        self.composer.rule = "context rule"
        self.composer.match = mock.Mock()
        self.acknowledge_preedit = False
        with self.assertRaises(runtime.OperationFailure):
            self.composer.send("中", self.key, self.text)
        self.composer.connection.signal_unsubscribe.assert_called_once_with(17)
        self.composer.match.assert_called_once_with("RemoveMatch", "context rule")

    def test_cleanup_error_does_not_mask_partial_input_failure(self):
        self.acknowledge_preedit = False
        self.composer.close = mock.Mock(side_effect=RuntimeError("cleanup failed"))
        with self.assertRaises(runtime.OperationFailure) as caught:
            self.composer.send("中", self.key, self.text)
        self.assertIn("acknowledgement timed out", str(caught.exception))
        self.assertTrue(caught.exception.input_events_emitted)

    def test_engine_change_stops_before_input(self):
        self.engine = ("IBusEngineDesc", {}, "pinyin")
        with self.assertRaises(runtime.OperationFailure) as caught:
            self.composer.send("中", self.key, self.text)
        self.assertFalse(caught.exception.input_events_emitted)
        self.assertEqual(self.events, [])
