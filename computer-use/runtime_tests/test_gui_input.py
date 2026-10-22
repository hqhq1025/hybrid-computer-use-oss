#!/usr/bin/env python3
"""Linux provider GUI input bounds, emission results, and accessibility coordinate fallback tests."""

from runtime_test_support import *  # noqa: F401,F403


class AccessibilityCoordinateFallbackTests(unittest.TestCase):
    def setUp(self):
        self.window = runtime.frame(100, 100, 800, 600)
        self.desktop = runtime.frame(0, 0, 1920, 1080)

    def test_uses_centre_of_visible_element_intersection(self):
        element = {"frame": runtime.frame(750, 550, 200, 200)}
        with mock.patch.object(
                runtime, "desktop_bounds", return_value=self.desktop):
            point = runtime.screen_point(self.window, element)

        self.assertEqual(point, (875.0, 675.0))

    def test_fails_closed_when_desktop_bounds_are_unavailable(self):
        element = {"frame": runtime.frame(10, 10, 100, 30)}
        with mock.patch.object(runtime, "desktop_bounds", return_value=None):
            with self.assertRaises(RuntimeError) as ctx:
                runtime.screen_point(self.window, element)

        self.assertIn("bounds are unavailable", str(ctx.exception))

    def test_rejects_element_below_the_window_instead_of_clicking_elsewhere(self):
        element = {"frame": runtime.frame(20, 6500, 100, 30)}
        with mock.patch.object(
                runtime, "desktop_bounds", return_value=self.desktop):
            with self.assertRaises(RuntimeError) as ctx:
                runtime.screen_point(self.window, element)

        self.assertIn("element_offscreen", str(ctx.exception))
        self.assertIn("scroll it into view", str(ctx.exception))

    def test_rejects_element_outside_the_desktop(self):
        element = {"frame": runtime.frame(-700, -500, 100, 30)}
        with mock.patch.object(
                runtime, "desktop_bounds", return_value=self.desktop):
            with self.assertRaises(RuntimeError) as ctx:
                runtime.screen_point(self.window, element)

        self.assertIn("element_offscreen", str(ctx.exception))


class InputBoundTests(unittest.TestCase):
    def test_click_count_has_a_hard_limit(self):
        with self.assertRaises(RuntimeError) as ctx:
            runtime.send_mouse_click(
                1, 2, "left", runtime.MAX_CLICK_COUNT + 1)

        self.assertIn("click_count", str(ctx.exception))

    def test_scroll_pages_has_a_hard_limit(self):
        with self.assertRaises(RuntimeError) as ctx:
            runtime.scroll_element(
                "down", runtime.MAX_SCROLL_PAGES + 1, point=(1, 2))

        self.assertIn("pages", str(ctx.exception))


class InputEmissionResultTests(unittest.TestCase):
    def test_mouse_false_return_is_an_emission_failure(self):
        with mock.patch.object(
                runtime.Atspi, "generate_mouse_event",
                return_value=False, create=True):
            with self.assertRaises(RuntimeError) as caught:
                runtime.send_mouse_click(1, 2, "left", 1)

        self.assertIn("input emission failed", str(caught.exception))

    def test_keyboard_false_return_is_an_emission_failure(self):
        with mock.patch.object(
                runtime.Atspi, "generate_keyboard_event",
                return_value=False, create=True):
            with self.assertRaises(RuntimeError) as caught:
                runtime.send_text("hello")

        self.assertIn("input emission failed", str(caught.exception))

    def test_non_ascii_text_is_sent_one_character_at_a_time(self):
        # One STRING event carrying "Grüße 你好" reached gedit as
        # "Gr好好e 好好": libatspi rebinds a spare keycode for every non-ASCII
        # character and a burst reuses it before the application reads the
        # previous mapping. ASCII runs still go in one event.
        events = []
        sleeps = []

        def generate(keyval, keystring, synth_type):
            events.append((keyval, keystring, synth_type))
            return True

        with mock.patch.object(
                runtime.Atspi, "generate_keyboard_event",
                side_effect=generate, create=True), \
                mock.patch.object(runtime.time, "sleep", side_effect=sleeps.append):
            runtime.send_text("ab Grüße 你好!")

        self.assertEqual(
            [keystring for _keyval, keystring, _type in events],
            ["ab Gr", "ü", "ß", "e ", "你", "好", "!"],
        )
        self.assertTrue(all(
            synth_type == runtime.Atspi.KeySynthType.STRING
            for _keyval, _keystring, synth_type in events))
        self.assertEqual(len(sleeps), 4)
        self.assertTrue(all(0 < delay <= 0.05 for delay in sleeps))

    def test_long_non_ascii_text_is_rejected_before_input(self):
        events = []
        with mock.patch.object(
                runtime.Atspi, "generate_keyboard_event",
                side_effect=lambda *args: events.append(args) or True,
                create=True):
            with self.assertRaises(runtime.OperationFailure) as caught:
                runtime.send_text("你" * 501)
        self.assertEqual(events, [])
        self.assertFalse(caught.exception.input_events_emitted)
        self.assertIn("no input was emitted", str(caught.exception))

    def test_gui_batch_rejects_long_non_ascii_text_before_input(self):
        with self.assertRaises(RuntimeError) as caught:
            runtime.validate_gui_action({"type": "type", "text": "好" * 501})
        self.assertIn("non-ASCII", str(caught.exception))
        runtime.validate_gui_action({"type": "type", "text": "好" * 500})

    def test_ascii_text_is_one_event(self):
        events = []
        with mock.patch.object(
                runtime.Atspi, "generate_keyboard_event",
                side_effect=lambda *args: events.append(args) or True,
                create=True):
            runtime.send_text("hello world")
        self.assertEqual(len(events), 1)

    def test_all_pressed_modifiers_are_released_after_one_release_fails(self):
        events = []

        def generate(keyval, _keystring, synth_type):
            events.append((keyval, synth_type))
            if synth_type == runtime.Atspi.KeySynthType.RELEASE and keyval == 2:
                return False
            return True

        with mock.patch.object(runtime, "parse_key", return_value=([1, 2], "S")), \
                mock.patch.object(runtime, "keycode", return_value=3), \
                mock.patch.object(
                    runtime.Atspi, "generate_keyboard_event",
                    side_effect=generate, create=True):
            with self.assertRaises(runtime.InputEmissionError):
                runtime.send_key("ctrl+shift+s")

        releases = [
            keyval for keyval, synth_type in events
            if synth_type == runtime.Atspi.KeySynthType.RELEASE
        ]
        self.assertEqual(releases, [2, 1])

    def test_mouse_release_is_retried_after_an_explicit_failure(self):
        results = iter([True, True, False, True])
        with mock.patch.object(
                runtime.Atspi, "generate_mouse_event",
                side_effect=lambda *_args: next(results), create=True) as generate:
            with self.assertRaises(runtime.InputEmissionError):
                runtime.send_mouse_click(1, 2, "left", 1)

        self.assertEqual(generate.call_count, 4)
