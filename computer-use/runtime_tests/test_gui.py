#!/usr/bin/env python3
"""Linux provider GUI lease, pixel evidence, settle wait, and input tests."""

from runtime_test_support import *  # noqa: F401,F403


class GUILeaseTests(unittest.TestCase):
    def setUp(self):
        self.bounds = runtime.frame(0, 0, 100, 100)

    def test_ax_mutation_layout_failure_marks_lease_suspect(self):
        with mock.patch.object(
            runtime, "desktop_layout_state", return_value=None
        ):
            self.assertEqual(
                runtime.gui_lease_after_ax_mutation(
                    {"windows": []}, self.bounds
                ),
                "suspect",
            )

    def test_ax_mutation_layout_change_marks_lease_stale(self):
        with mock.patch.object(
            runtime,
            "desktop_layout_state",
            return_value={"windows": ["after"]},
        ):
            self.assertEqual(
                runtime.gui_lease_after_ax_mutation(
                    {"windows": ["before"]}, self.bounds
                ),
                "stale",
            )

    def test_stale_lease_is_revalidated_by_fresh_gui_snapshot(self):
        snapshot = {
            "frameId": "layout",
            "screenshotPngBase64": "png",
        }
        settled = {
            "stable": True,
            "elapsed_ms": 300,
            "polls": 3,
            "stable_samples": 4,
            "stable_for_ms": 300,
        }
        with mock.patch.object(
            runtime, "desktop_bounds", return_value=self.bounds
        ), mock.patch.object(
            runtime, "build_gui_snapshot", return_value=snapshot
        ), mock.patch.object(
            runtime, "execute_gui_action"
        ) as execute, mock.patch.object(
            runtime, "wait_for_gui_settle", return_value=settled
        ), mock.patch.object(
            runtime, "capture_window_pixels", return_value=None
        ):
            response = runtime.perform_gui(
                [{"type": "click", "x": 10, "y": 10}],
                gui_lease_state="stale",
            )

        self.assertTrue(response["ok"])
        self.assertTrue(response["gui_lease_revalidated"])
        self.assertEqual(response["gui_lease_state"], "fresh")
        execute.assert_called_once()

    def test_suspect_lease_rejects_coordinate_input_without_fresh_layout(self):
        snapshot = {
            "frameId": None,
            "screenshotPngBase64": "png",
        }
        with mock.patch.object(
            runtime, "desktop_bounds", return_value=self.bounds
        ), mock.patch.object(
            runtime, "build_gui_snapshot", return_value=snapshot
        ), mock.patch.object(runtime, "execute_gui_action") as execute:
            response = runtime.perform_gui(
                [{"type": "click", "x": 10, "y": 10}],
                gui_lease_state="suspect",
            )

        self.assertFalse(response["ok"])
        execute.assert_not_called()


class PixelEvidenceTests(unittest.TestCase):
    """Pixel comparison is a third effect check, independent of the tree.

    The tree misses whole classes of effects such as formatting, file state
    and canvas pixels; after ctrl+s it can stay byte-identical although the
    file was saved. An unchanged tree does not prove an action had no effect.
    """

    def _frame(self, fill, size=(80, 60)):
        width, height = size
        stride = width * 3
        return {"data": bytes([fill]) * (stride * height), "stride": stride,
                "channels": 3, "width": width, "height": height}

    def test_identical_frames_report_no_change(self):
        frame = self._frame(10)
        note = runtime.pixel_change_note(
            runtime.persistent_pixel_change(frame, self._frame(10), self._frame(10)))
        self.assertIn("sampled_pixel_change=none", note)

    def test_resize_is_reported_without_causal_inference(self):
        grown = self._frame(10, size=(90, 60))
        note = runtime.pixel_change_note(
            runtime.persistent_pixel_change(self._frame(10), grown, grown))
        self.assertIn("dimensions changed", note)
        self.assertIn("semantic effect are unknown", note)
        self.assertNotIn("something happened", note)

    def test_substantial_change_is_reported_with_a_region(self):
        after = self._frame(10)
        data = bytearray(after["data"])
        for y in range(0, 60):
            for x in range(20, 60):
                offset = y * after["stride"] + x * 3
                data[offset:offset + 3] = b"\xff\xff\xff"
        after["data"] = bytes(data)
        note = runtime.pixel_change_note(
            runtime.persistent_pixel_change(self._frame(10), after, after))
        self.assertIn("sampled_pixel_change=material", note)
        self.assertIn("% of sample points", note)
        self.assertIn("concentrated in", note)

    def test_gui_change_region_uses_full_desktop_image_pixels(self):
        after = self._frame(10)
        data = bytearray(after["data"])
        data[0:3] = b"\xff\xff\xff"
        after["data"] = bytes(data)

        note = runtime.pixel_change_note(
            runtime.persistent_pixel_change(self._frame(10), after, after),
            runtime.SCREENSHOT_SCOPE_SCREEN,
        )

        self.assertIn("pre-action full desktop", note)
        self.assertIn("% of sample points", note)
        self.assertIn("full-desktop image pixels", note)

    def _dots(self, count, size, offset_rows=1):
        frame = self._frame(10, size=size)
        data = bytearray(frame["data"])
        for i in range(count):
            offset = (8 * (i + offset_rows)) * frame["stride"] + 8 * 3
            data[offset:offset + 3] = b"\xff\xff\xff"
        frame["data"] = bytes(data)
        return frame

    def test_flicker_is_filtered_by_requiring_the_change_to_persist(self):
        """A change present in only one post-action capture is flicker.

        Two pre-action captures taken milliseconds apart cannot measure the
        noise of a 1 Hz text caret, so a no-op would report the 8x16 caret as
        a change. Following Playwright's rule of two consecutive matching
        screenshots, only changes present in both post-action captures count.
        """
        size = (1600, 1000)
        before = self._frame(10, size=size)
        blink_on = self._dots(1, size)          # caret shown
        blink_off = self._frame(10, size=size)  # caret hidden, as before

        change = runtime.persistent_pixel_change(before, blink_on, blink_off)
        self.assertEqual(change["changed"], 0, "a flicker must not count as a change")
        self.assertIn(
            "sampled_pixel_change=none", runtime.pixel_change_note(change))

    def test_a_real_change_persists_across_both_captures(self):
        size = (1600, 1000)
        before = self._frame(10, size=size)
        after = self._dots(30, size)

        change = runtime.persistent_pixel_change(before, after, after)
        self.assertEqual(change["changed"], 30)
        note = runtime.pixel_change_note(change)
        self.assertIn("sampled_pixel_change=material", note)
        self.assertIn("concentrated in", note)

    def test_resize_between_captures_is_reported_not_guessed(self):
        size = (1600, 1000)
        before = self._frame(10, size=size)
        grown = self._frame(10, size=(1608, 1000))
        change = runtime.persistent_pixel_change(before, grown, grown)
        self.assertTrue(change["resized"])
        note = runtime.pixel_change_note(change)
        self.assertIn("geometry_changed=true", note)
        self.assertIn("semantic effect are unknown", note)

    def test_missing_capture_never_pretends_to_have_compared(self):
        """A missing capture returns None instead of pretending to compare."""
        frame = self._frame(10)
        self.assertIsNone(runtime.persistent_pixel_change(None, frame, frame))
        self.assertIsNone(runtime.persistent_pixel_change(frame, None, frame))
        self.assertIsNone(runtime.persistent_pixel_change(frame, frame, None))
        self.assertIsNone(runtime.pixel_change_note(None))


class SettleWaitTests(AtspiPatchedTestCase):
    """Post-action settle wait.

    These tests cover a race that silently targets the wrong window: for a
    few tens of milliseconds a new window is in the tree while ACTIVE is
    still on the old one. A snapshot taken then shows the old window, and
    the tree is fully self-consistent, so nothing in it reveals the problem.
    """

    def _wait(self, app, before, clock, **kwargs):
        return runtime.wait_for_ui_to_settle(
            app,
            before,
            clock=clock.monotonic,
            sleep=clock.sleep,
            **kwargs
        )

    def test_returns_right_after_the_minimum_wait_when_nothing_opened(self):
        """The common path stays fast: no window change means only the minimum wait."""
        clock = _FakeClock()
        main = _WindowNode("frame", "Inbox", active=True)
        app = _ScriptedApp(clock, [(0.0, [main])])
        before = runtime.window_identity_set(app)

        self.assertIsNone(self._wait(app, before, clock))
        self.assertAlmostEqual(clock.now, runtime.SETTLE_MIN_SECONDS, places=6)

    def test_waits_until_the_newly_opened_window_takes_focus(self):
        """A synthetic window becomes active after it first appears."""
        clock = _FakeClock()
        main_active = _WindowNode("frame", "Inbox", active=True)
        main_idle = _WindowNode("frame", "Inbox")
        filters_idle = _WindowNode("frame", "Message Filters")
        filters_active = _WindowNode("frame", "Message Filters", active=True)
        app = _ScriptedApp(
            clock,
            [
                (0.0, [main_active]),
                (0.070, [main_active, filters_idle]),
                (0.123, [main_idle, filters_active]),
            ],
        )
        before = runtime.window_identity_set(app)

        self.assertIsNone(self._wait(app, before, clock))
        self.assertGreaterEqual(clock.now, 0.123)
        # Beyond waiting long enough, main_window() must then pick the new window.
        _, chosen = runtime.main_window(app)
        self.assertEqual(runtime.node_name(chosen), "Message Filters")

    def test_appearance_without_focus_transfer_keeps_the_active_window(self):
        """An inactive synthetic window does not replace the active target."""
        clock = _FakeClock()
        main_active = _WindowNode("frame", "Inbox", active=True)
        filters_idle = _WindowNode("frame", "Message Filters")
        app = _ScriptedApp(
            clock,
            [(0.0, [main_active]), (0.070, [main_active, filters_idle])],
        )

        clock.sleep(0.12)  # fixed post-action delay before the snapshot
        _, chosen = runtime.main_window(app)
        self.assertEqual(runtime.node_name(chosen), "Inbox")

    def test_modal_window_counts_as_settled(self):
        """A modal window may report MODAL without ACTIVE; either counts as settled."""
        clock = _FakeClock()
        main = _WindowNode("frame", "Writer", active=True)
        alert = _WindowNode("alert", "Question", modal=True)
        app = _ScriptedApp(clock, [(0.0, [main]), (0.045, [main, alert])])
        before = runtime.window_identity_set(app)

        self.assertIsNone(self._wait(app, before, clock))
        _, chosen = runtime.main_window(app)
        self.assertEqual(runtime.node_name(chosen), "Question")

    def test_waits_for_focus_to_come_back_after_a_window_closes(self):
        """After a dialog closes, focus takes a while to return to the main window."""
        clock = _FakeClock()
        main_idle = _WindowNode("frame", "Writer")
        main_active = _WindowNode("frame", "Writer", active=True)
        dialog = _WindowNode("dialog", "Find and Replace", active=True)
        app = _ScriptedApp(
            clock,
            [
                (0.0, [main_idle, dialog]),
                (0.05, [main_idle]),
                (0.30, [main_active]),
            ],
        )
        before = runtime.window_identity_set(app)

        self.assertIsNone(self._wait(app, before, clock))
        self.assertGreaterEqual(clock.now, 0.30)

    def test_window_opening_after_the_minimum_wait_is_out_of_scope(self):
        """An unchanged window set ends settling at the minimum wait.

        A window that appears later is not covered by this wait. The caller
        must observe again to detect that asynchronous transition.
        """
        clock = _FakeClock()
        main = _WindowNode("frame", "Inbox", active=True)
        slow = _WindowNode("frame", "Slow Dialog")
        app = _ScriptedApp(
            clock,
            [(0.0, [main]), (runtime.SETTLE_MIN_SECONDS + 0.05, [main, slow])],
        )
        before = runtime.window_identity_set(app)

        self.assertIsNone(self._wait(app, before, clock))
        self.assertAlmostEqual(clock.now, runtime.SETTLE_MIN_SECONDS, places=6)
        self.assertEqual(runtime.window_identity_set(app), before)
        clock.sleep(0.05)
        self.assertNotEqual(runtime.window_identity_set(app), before)

    def test_reports_when_the_new_window_never_takes_focus(self):
        """A timeout is reported, since the snapshot may show an unexpected window."""
        clock = _FakeClock()
        main = _WindowNode("frame", "Inbox", active=True)
        tooltip = _WindowNode("window", "")
        app = _ScriptedApp(clock, [(0.0, [main]), (0.05, [main, tooltip])])
        before = runtime.window_identity_set(app)

        note = self._wait(app, before, clock, timeout_seconds=0.4)
        self.assertIsNotNone(note)
        self.assertIn("target ref", note)
        # The wait is bounded and never hangs the tool.
        self.assertLess(clock.now, runtime.SETTLE_MIN_SECONDS + 0.4 + 0.1)

    def test_missing_baseline_skips_the_wait(self):
        """Without a pre-action window set, the wait stops at the minimum settle time."""
        clock = _FakeClock()
        app = _ScriptedApp(clock, [(0.0, [_WindowNode("frame", "Inbox")])])

        self.assertIsNone(self._wait(app, None, clock))
        self.assertAlmostEqual(clock.now, runtime.SETTLE_MIN_SECONDS, places=6)


class ScreenshotPolicyTests(unittest.TestCase):
    """Accessibility screenshot policy and the GUI screenshots it never disables.

    Accessibility screenshots are on by default because the tree alone gives
    false negatives: right-aligning text or saving a file can take effect
    while the tree suggests the input was delivered but ignored.
    """

    def setUp(self):
        self._saved = os.environ.get("COMPUTER_USE_A11Y_SCREENSHOTS")
        self.addCleanup(self._restore)

    def _restore(self):
        if self._saved is None:
            os.environ.pop("COMPUTER_USE_A11Y_SCREENSHOTS", None)
        else:
            os.environ["COMPUTER_USE_A11Y_SCREENSHOTS"] = self._saved

    def test_enabled_by_default(self):
        os.environ.pop("COMPUTER_USE_A11Y_SCREENSHOTS", None)
        self.assertTrue(runtime.a11y_screenshots_enabled())

    def test_can_be_switched_off_for_the_ab_test(self):
        for value in ("0", "false", "no", "off", "OFF", " 0 "):
            os.environ["COMPUTER_USE_A11Y_SCREENSHOTS"] = value
            self.assertFalse(runtime.a11y_screenshots_enabled(), value)

    def test_unrecognised_values_keep_screenshots_on(self):
        """Only explicit off values disable screenshots; a typo keeps the default."""
        for value in ("1", "true", "yes", "", "maybe"):
            os.environ["COMPUTER_USE_A11Y_SCREENSHOTS"] = value
            self.assertTrue(runtime.a11y_screenshots_enabled(), value)

    def test_gui_screenshot_is_independent_of_a11y_policy(self):
        os.environ["COMPUTER_USE_A11Y_SCREENSHOTS"] = "0"
        self.assertFalse(runtime.a11y_screenshots_enabled())
        desktop = runtime.frame(0, 0, 1600, 900)
        with mock.patch.object(runtime, "desktop_bounds", return_value=desktop), \
                mock.patch.object(
                runtime, "build_gui_snapshot",
                return_value={"frameId": "layout", "screenshotPngBase64": "png"}):
            response = runtime.perform_gui([{"type": "screenshot"}])
        self.assertTrue(response["ok"])
        self.assertEqual(response["snapshot"]["screenshotPngBase64"], "png")
        self.assertFalse(response["input_events_emitted"])
        self.assertEqual(response["input_actions_dispatched"], 0)
        self.assertIn(
            "0 input-producing action(s) completed event emission",
            response["text"],
        )
        self.assertNotIn("settle_ms=0", response["text"])


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


class GUIScreenCoordinateTests(unittest.TestCase):
    def setUp(self):
        self.desktop = runtime.frame(0, 0, 1920, 1080)

    def test_absolute_point_is_not_offset_or_clamped(self):
        self.assertEqual(
            runtime.screen_absolute_point(125, 250, self.desktop), (125.0, 250.0)
        )

    def test_image_point_is_translated_for_a_negative_virtual_desktop_origin(self):
        desktop = runtime.frame(-1920, -100, 3840, 1180)

        self.assertEqual(
            runtime.screen_absolute_point(100, 50, desktop),
            (-1820.0, -50.0),
        )

    def test_native_image_point_maps_one_to_one_to_desktop(self):
        dimensions = runtime.gui_model_dimensions(self.desktop)
        model = runtime.frame(
            0, 0, dimensions["width"], dimensions["height"])
        x, y = runtime.screen_absolute_point(
            model["width"] / 2 - 0.5,
            model["height"] / 2 - 0.5,
            model,
            self.desktop,
        )

        self.assertAlmostEqual(x, 959.5, places=3)
        self.assertAlmostEqual(y, 539.5, places=3)

    def test_1080p_desktop_is_delivered_at_native_resolution(self):
        dimensions = runtime.gui_model_dimensions(self.desktop)

        self.assertEqual(dimensions["width"], 1920)
        self.assertEqual(dimensions["height"], 1080)
        self.assertEqual(dimensions["scale_x"], 1.0)
        self.assertEqual(dimensions["scale_y"], 1.0)
        self.assertAlmostEqual(
            dimensions["width"] / dimensions["height"],
            self.desktop["width"] / self.desktop["height"],
            places=2,
        )

    def test_larger_desktop_is_bounded_to_1080p(self):
        dimensions = runtime.gui_model_dimensions(
            runtime.frame(0, 0, 2560, 1440))

        self.assertEqual(dimensions["width"], 1920)
        self.assertEqual(dimensions["height"], 1080)
        self.assertEqual(dimensions["scale_x"], 0.75)
        self.assertEqual(dimensions["scale_y"], 0.75)

    def test_out_of_bounds_point_is_rejected_instead_of_moved(self):
        with self.assertRaises(RuntimeError) as ctx:
            runtime.screen_absolute_point(1920, 20, self.desktop)

        self.assertIn("outside the full desktop image bounds", str(ctx.exception))
        self.assertIn("gui", str(ctx.exception))

    def test_coordinate_validation_fails_closed_without_desktop_bounds(self):
        with mock.patch.object(runtime, "desktop_bounds", return_value=None):
            with self.assertRaises(RuntimeError) as ctx:
                runtime.screen_absolute_point(10, 20)

        self.assertIn("bounds are unavailable", str(ctx.exception))
        self.assertIn("refusing", str(ctx.exception))

    def test_gui_snapshot_captures_the_desktop_without_rendering_the_tree(self):
        layout = {
            "desktop": {"x": 0, "y": 0, "width": 1920, "height": 1080},
            "activeWindow": "0x20",
            "windows": [
                {"id": "0x20", "x": 10, "y": 20, "width": 800, "height": 600},
            ],
        }
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(runtime, "capture_gui_image",
                                  return_value={"data": "png", "mimeType": "image/png",
                                                "encoding": "png-compression-9"}) as capture, \
                mock.patch.object(runtime, "desktop_layout_state",
                                  return_value=layout), \
                mock.patch.object(
                    runtime, "resolve_app",
                    side_effect=AssertionError("app-free gui resolved an app")), \
                mock.patch.object(runtime, "render_tree",
                                  side_effect=AssertionError("GUI screenshot walked tree")):
            snapshot = runtime.build_gui_snapshot()

        capture.assert_called_once_with(self.desktop)
        self.assertEqual(snapshot["screenshotPngBase64"], "png")
        self.assertEqual(snapshot["coordinateSpace"], "screen_image_px")
        self.assertEqual(snapshot["width"], 1920)
        self.assertEqual(snapshot["height"], 1080)
        self.assertEqual(
            snapshot["frameId"],
            runtime.desktop_layout_frame_id(layout),
        )
        self.assertEqual(
            snapshot["contentFrameId"],
            runtime.content_frame_id("png"),
        )
        self.assertEqual(snapshot["desktopLayout"], layout)
        self.assertNotIn("app", snapshot)
        self.assertNotIn("elements", snapshot)
        self.assertNotIn("treeLines", snapshot)

    def test_screenshot_encoding_falls_back_to_same_size_jpeg_under_budget(self):
        class Pixbuf:
            def save_to_bufferv(self, image_format, _keys, values):
                if image_format == "png":
                    return True, b"p" * 500000
                quality = int(values[0])
                sizes = {85: 400000, 70: 200000, 55: 150000, 40: 100000}
                return True, b"j" * sizes[quality]

        image = runtime.encoded_screenshot(Pixbuf())

        self.assertEqual(image["mimeType"], "image/jpeg")
        self.assertEqual(image["encoding"], "jpeg-quality-70")
        self.assertLessEqual(
            len(image["data"]), runtime.SCREENSHOT_MAX_BASE64_CHARS)

    def test_screenshot_encoding_keeps_png_when_it_fits(self):
        class Pixbuf:
            def save_to_bufferv(self, image_format, _keys, _values):
                if image_format == "png":
                    return True, b"p" * 100
                raise AssertionError("JPEG fallback should not run")

        image = runtime.encoded_screenshot(Pixbuf())

        self.assertEqual(image["mimeType"], "image/png")
        self.assertEqual(image["encoding"], "png-compression-9")

    def test_gui_snapshot_retries_until_layout_brackets_the_screenshot(self):
        first = {
            "desktop": {"x": 0, "y": 0, "width": 1920, "height": 1080},
            "activeWindow": "0x10",
            "windows": [
                {"id": "0x10", "x": 0, "y": 0, "width": 800, "height": 600},
            ],
        }
        second = dict(first, activeWindow="0x20")
        with mock.patch.object(
                runtime, "desktop_layout_state",
                side_effect=[first, second, second, second]), \
                mock.patch.object(
                    runtime, "capture_gui_image",
                    side_effect=[
                        {"data": "raced", "mimeType": "image/png", "encoding": "png-compression-9"},
                        {"data": "coherent", "mimeType": "image/png", "encoding": "png-compression-9"},
                    ]) as capture:
            snapshot = runtime.build_gui_snapshot(self.desktop)

        self.assertEqual(capture.call_count, 2)
        self.assertEqual(snapshot["screenshotPngBase64"], "coherent")
        self.assertEqual(
            snapshot["frameId"], runtime.desktop_layout_frame_id(second))

    def test_gui_snapshot_with_perpetual_layout_race_has_no_frame_id(self):
        layouts = []
        for active in range(runtime.GUI_SNAPSHOT_LAYOUT_ATTEMPTS * 2):
            layouts.append({
                "desktop": {"x": 0, "y": 0, "width": 1920, "height": 1080},
                "activeWindow": hex(active + 1),
                "windows": [],
            })
        with mock.patch.object(
                runtime, "desktop_layout_state", side_effect=layouts), \
                mock.patch.object(
                    runtime, "capture_gui_image",
                    return_value={"data": "png", "mimeType": "image/png",
                                  "encoding": "png-compression-9"}):
            snapshot = runtime.build_gui_snapshot(self.desktop)

        self.assertIsNone(snapshot["frameId"])
        self.assertEqual(snapshot["screenshotPngBase64"], "png")

    def test_gui_layout_frame_ignores_pixel_only_changes(self):
        layout = {
            "desktop": {"x": 0, "y": 0, "width": 1920, "height": 1080},
            "activeWindow": "0x20",
            "windows": [
                {"id": "0x20", "x": 10, "y": 20, "width": 800, "height": 600},
            ],
        }
        with mock.patch.object(runtime, "desktop_layout_state",
                               return_value=layout), \
                mock.patch.object(
                    runtime, "capture_gui_image",
                    side_effect=[
                        {"data": "png-a", "mimeType": "image/png", "encoding": "png-compression-9"},
                        {"data": "png-b", "mimeType": "image/png", "encoding": "png-compression-9"},
                    ]):
            first = runtime.build_gui_snapshot(self.desktop)
            second = runtime.build_gui_snapshot(self.desktop)

        self.assertEqual(first["frameId"], second["frameId"])
        self.assertNotEqual(first["contentFrameId"], second["contentFrameId"])

    def test_gui_layout_frame_changes_for_coordinate_relevant_layout(self):
        base = {
            "desktop": {"x": 0, "y": 0, "width": 1920, "height": 1080},
            "activeWindow": "0x20",
            "windows": [
                {"id": "0x10", "x": 0, "y": 0, "width": 400, "height": 300},
                {"id": "0x20", "x": 10, "y": 20, "width": 800, "height": 600},
            ],
        }
        variants = [
            dict(base, activeWindow="0x10"),
            dict(base, windows=[
                base["windows"][0],
                dict(base["windows"][1], x=11),
            ]),
            dict(base, windows=base["windows"][:1]),
            dict(base, windows=list(reversed(base["windows"]))),
        ]

        original = runtime.desktop_layout_frame_id(base)
        for layout in variants:
            with self.subTest(layout=layout):
                self.assertNotEqual(
                    original,
                    runtime.desktop_layout_frame_id(layout),
                )

    def test_desktop_layout_state_uses_visible_x11_toplevels_only(self):
        root_props = (
            "_NET_CLIENT_LIST_STACKING(WINDOW): window id # "
            "0x10, 0x20, 0x30\n"
            "_NET_ACTIVE_WINDOW(WINDOW): window id # 0x20\n"
        )
        window_info = {
            "0x10": (
                'xwininfo: Window id: 0x10 "Tip of the Day"\n'
                "Absolute upper-left X:  10\n"
                "Absolute upper-left Y:  20\n"
                "Width: 800\nHeight: 600\nMap State: IsViewable\n"
                "Override Redirect State: no\n"
            ),
            "0x20": (
                'xwininfo: Window id: 0x20 "Document"\n'
                "Absolute upper-left X:  100\n"
                "Absolute upper-left Y:  200\n"
                "Width: 900\nHeight: 700\nMap State: IsViewable\n"
                "Override Redirect State: no\n"
            ),
            "0x30": (
                "Absolute upper-left X:  1\n"
                "Absolute upper-left Y:  2\n"
                "Width: 3\nHeight: 4\nMap State: IsUnMapped\n"
            ),
        }

        def run(command, **_kwargs):
            if command[:2] == ["xprop", "-root"]:
                return mock.Mock(returncode=0, stdout=root_props)
            return mock.Mock(returncode=0, stdout=window_info[command[-1]])

        with mock.patch.object(runtime.subprocess, "run", side_effect=run):
            layout = runtime.desktop_layout_state(self.desktop)

        self.assertEqual(layout["activeWindow"], "0x20")
        self.assertEqual(layout["windows"], [
            {"id": "0x10", "x": 10, "y": 20, "width": 800, "height": 600,
             "title": "Tip of the Day", "overrideRedirect": False},
            {"id": "0x20", "x": 100, "y": 200, "width": 900, "height": 700,
             "title": "Document", "overrideRedirect": False},
        ])

    def test_layout_frame_ignores_title_only_changes(self):
        first = {
            "desktop": {"x": 0, "y": 0, "width": 1920, "height": 1080},
            "activeWindow": "0x20",
            "windows": [{
                "id": "0x20", "x": 10, "y": 20, "width": 800, "height": 600,
                "title": "Document A", "overrideRedirect": False,
            }],
        }
        second = json.loads(json.dumps(first))
        second["windows"][0]["title"] = "Document B"

        self.assertEqual(
            runtime.desktop_layout_frame_id(first),
            runtime.desktop_layout_frame_id(second),
        )

    def test_desktop_layout_state_rejects_unbounded_window_lists(self):
        ids = ", ".join(
            "0x{:x}".format(index + 1)
            for index in range(runtime.GUI_MAX_LAYOUT_WINDOWS + 1)
        )
        root_props = (
            "_NET_CLIENT_LIST_STACKING(WINDOW): window id # " + ids + "\n"
            "_NET_ACTIVE_WINDOW(WINDOW): window id # 0x1\n"
        )
        with mock.patch.object(
                runtime.subprocess, "run",
                return_value=mock.Mock(returncode=0, stdout=root_props)) as run:
            layout = runtime.desktop_layout_state(self.desktop)

        self.assertIsNone(layout)
        self.assertEqual(run.call_count, 1)

    def test_root_window_state_uses_one_bounded_query(self):
        root_props = (
            "_NET_CLIENT_LIST_STACKING(WINDOW): window id # 0x10, 0x20\n"
            "_NET_ACTIVE_WINDOW(WINDOW): window id # 0x20\n"
        )
        with mock.patch.object(
                runtime.subprocess, "run",
                return_value=mock.Mock(returncode=0, stdout=root_props)) as run:
            state = runtime.x11_root_window_state()

        self.assertEqual(state, {
            "windowIds": {"0x10", "0x20"},
            "activeWindow": "0x20",
        })
        self.assertEqual(run.call_count, 1)
        self.assertEqual(run.call_args.kwargs["timeout"], 0.25)

    def test_gui_window_actions_are_removed(self):
        for action_type in ("activate_window", "close_window"):
            with self.assertRaisesRegex(
                    RuntimeError, "unsupported gui action type"):
                runtime.validate_gui_action(
                    {"type": action_type, "x": 10, "y": 20},
                    self.desktop,
                )

    def test_gui_executes_actions_in_order_and_returns_one_final_screenshot(self):
        executed = []
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(
                runtime, "execute_gui_action",
                side_effect=lambda action: executed.append(action["type"])), \
                mock.patch.object(
                    runtime, "build_gui_snapshot",
                    return_value={"frameId": "layout", "screenshotPngBase64": "png"}):
            response = runtime.perform_gui([
                {"type": "move", "x": 1, "y": 2},
                {"type": "click", "x": 1, "y": 2},
            ])
        self.assertTrue(response["ok"])
        self.assertEqual(executed, ["move", "click"])
        self.assertEqual(response["dispatched"], 2)
        self.assertTrue(response["input_events_emitted"])
        self.assertEqual(response["input_actions_dispatched"], 2)
        self.assertTrue(response["effect_unknown"])
        self.assertNotIn("executed", response["text"].lower())
        self.assertEqual(response["snapshot"]["screenshotPngBase64"], "png")

    def test_gui_stops_at_first_failure_and_still_returns_screenshot(self):
        executed = []

        def execute(action):
            executed.append(action["type"])
            if action["type"] == "click":
                raise RuntimeError("blocked")

        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(runtime, "execute_gui_action", side_effect=execute), \
                mock.patch.object(
                    runtime, "build_gui_snapshot",
                    return_value={"frameId": "layout", "screenshotPngBase64": "png"}):
            response = runtime.perform_gui([
                {"type": "move", "x": 1, "y": 2},
                {"type": "click", "x": 1, "y": 2},
                {"type": "type", "text": "must not run"},
            ])
        self.assertFalse(response["ok"])
        self.assertEqual(executed, ["move", "click"])
        self.assertIn("action 2", response["error"])
        self.assertEqual(response["snapshot"]["screenshotPngBase64"], "png")

    def test_gui_does_not_claim_emission_when_atspi_returns_false(self):
        snapshot = {"frameId": "layout", "screenshotPngBase64": "png"}
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(
                    runtime, "build_gui_snapshot", return_value=snapshot), \
                mock.patch.object(
                    runtime, "capture_window_pixels", return_value=None), \
                mock.patch.object(
                    runtime.Atspi, "generate_mouse_event",
                    return_value=False, create=True):
            response = runtime.perform_gui([
                {"type": "click", "x": 1, "y": 2},
            ])

        self.assertFalse(response["ok"])
        self.assertFalse(response["input_events_emitted"])
        self.assertEqual(response["input_actions_dispatched"], 0)
        self.assertIn("input emission failed", response["error"])

    def test_gui_reports_partial_emission_when_release_fails(self):
        snapshot = {"frameId": "layout", "screenshotPngBase64": "png"}
        results = iter([True, True, False, True])
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(
                    runtime, "build_gui_snapshot", return_value=snapshot), \
                mock.patch.object(
                    runtime, "capture_window_pixels", return_value=None), \
                mock.patch.object(
                    runtime.Atspi, "generate_mouse_event",
                    side_effect=lambda *_args: next(results), create=True):
            response = runtime.perform_gui([
                {"type": "click", "x": 1, "y": 2},
            ])

        self.assertFalse(response["ok"])
        self.assertTrue(response["input_events_emitted"])
        self.assertTrue(response["partial_input_possible"])
        self.assertEqual(response["input_actions_dispatched"], 0)

    def test_later_invalid_action_rejects_the_whole_batch_before_side_effects(self):
        execute = mock.Mock()
        actions = [
            {"type": "click", "x": 10, "y": 20},
            {
                "type": "scroll",
                "x": 10,
                "y": 20,
                "scroll_y": runtime.GUI_MAX_SCROLL_DELTA + 1,
            },
        ]
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(runtime, "execute_gui_action", execute), \
                mock.patch.object(
                    runtime, "build_gui_snapshot",
                    return_value={"frameId": "layout", "screenshotPngBase64": "png"}):
            response = runtime.perform_gui(actions)

        self.assertFalse(response["ok"])
        self.assertIn("static pre-validation", response["error"])
        self.assertIn("no actions were dispatched", response["error"])
        self.assertEqual(response["dispatched"], 0)
        self.assertFalse(response["input_events_emitted"])
        self.assertEqual(response["input_actions_dispatched"], 0)
        execute.assert_not_called()
        self.assertNotIn("snapshot", response)

    def test_gui_limits_are_enforced_by_runtime_validation(self):
        cases = [
            (
                {
                    "type": "scroll",
                    "x": 1,
                    "y": 1,
                    "scroll_y": runtime.GUI_MAX_SCROLL_DELTA + 1,
                },
                "between",
            ),
            (
                {
                    "type": "drag",
                    "path": [
                        {"x": 1, "y": 1}
                        for _ in range(runtime.GUI_MAX_DRAG_POINTS + 1)
                    ],
                },
                "at most",
            ),
            (
                {
                    "type": "keypress",
                    "keys": ["CTRL" for _ in range(runtime.GUI_MAX_KEYS + 1)],
                },
                "at most",
            ),
            (
                {
                    "type": "keypress",
                    "keys": ["x" * (runtime.GUI_MAX_KEY_LENGTH + 1)],
                },
                "at most",
            ),
            (
                {
                    "type": "type",
                    "text": "x" * (runtime.GUI_MAX_TEXT_LENGTH + 1),
                },
                "at most",
            ),
        ]
        for action, expected in cases:
            with self.subTest(action=action["type"]):
                with self.assertRaises(RuntimeError) as ctx:
                    runtime.validate_gui_action(action, self.desktop)
                self.assertIn(expected, str(ctx.exception))

    def test_gui_scroll_uses_only_computer_contract_field_names(self):
        normalized = runtime.validate_gui_action({
            "type": "scroll",
            "x": 10,
            "y": 20,
            "scroll_x": -2,
            "scroll_y": 3,
        }, self.desktop)
        self.assertEqual(normalized["scroll_x"], -2)
        self.assertEqual(normalized["scroll_y"], 3)
        with self.assertRaisesRegex(RuntimeError, "unsupported field"):
            runtime.validate_gui_action({
                "type": "scroll",
                "x": 10,
                "y": 20,
                "delta_y": 3,
            }, self.desktop)

    def test_gui_rejects_cumulative_wait_before_any_action(self):
        execute = mock.Mock()
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(runtime, "execute_gui_action", execute), \
                mock.patch.object(
                    runtime, "build_gui_snapshot",
                    return_value={"frameId": "layout", "screenshotPngBase64": "png"}):
            response = runtime.perform_gui([
                {"type": "wait"}
                for _ in range(runtime.GUI_MAX_TOTAL_WAIT_SECONDS + 1)
            ])
        self.assertFalse(response["ok"])
        self.assertIn("cumulative wait", response["error"])
        self.assertIn("No actions were dispatched", response["error"])
        execute.assert_not_called()
        self.assertNotIn("snapshot", response)

    def test_gui_action_count_limit_is_enforced_before_execution(self):
        execute = mock.Mock()
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(runtime, "execute_gui_action", execute), \
                mock.patch.object(
                    runtime, "build_gui_snapshot",
                    return_value={"frameId": "layout", "screenshotPngBase64": "png"}):
            response = runtime.perform_gui([
                {"type": "screenshot"}
                for _ in range(runtime.GUI_MAX_ACTIONS + 1)
            ])

        self.assertFalse(response["ok"])
        self.assertIn("at most", response["error"])
        execute.assert_not_called()

    def test_gui_type_and_keypress_state_current_focus_boundary(self):
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(runtime, "parse_key", return_value=([], "S")), \
                mock.patch.object(runtime, "execute_gui_action"), \
                mock.patch.object(
                    runtime, "build_gui_snapshot",
                    return_value={"frameId": "layout", "screenshotPngBase64": "png"}):
            response = runtime.perform_gui([
                {"type": "type", "text": "hello"},
                {"type": "keypress", "keys": ["CTRL", "S"]},
            ])

        self.assertTrue(response["ok"])
        self.assertIn("whichever control held input focus", response["text"])
        self.assertIn("did not identify or verify a target", response["text"])
        self.assertTrue(response["effect_unknown"])

    def test_gui_fails_closed_when_desktop_bounds_are_unavailable(self):
        with mock.patch.object(runtime, "desktop_bounds", return_value=None), \
                mock.patch.object(runtime, "capture_window_image", return_value=None):
            response = runtime.perform_gui([{"type": "click", "x": 1, "y": 2}])

        self.assertFalse(response["ok"])
        self.assertIn("fails closed", response["error"])

    def test_gui_fails_closed_before_input_when_screenshot_is_unavailable(self):
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(
                    runtime, "build_gui_snapshot",
                    return_value={
                        "frameId": "layout",
                        "screenshotPngBase64": None,
                    }), \
                mock.patch.object(runtime, "execute_gui_action") as execute:
            response = runtime.perform_gui([
                {"type": "click", "x": 1, "y": 2},
            ])

        self.assertFalse(response["ok"])
        self.assertIn("failed closed", response["error"])
        self.assertFalse(response["input_events_emitted"])
        execute.assert_not_called()

    def test_gui_fails_closed_before_input_when_layout_is_incoherent(self):
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(
                    runtime, "build_gui_snapshot",
                    return_value={
                        "frameId": None,
                        "screenshotPngBase64": "png",
                    }), \
                mock.patch.object(runtime, "execute_gui_action") as execute:
            response = runtime.perform_gui([
                {"type": "click", "x": 1, "y": 2},
            ])

        self.assertFalse(response["ok"])
        self.assertIn("layout could not be sampled consistently", response["error"])
        self.assertFalse(response["input_events_emitted"])
        execute.assert_not_called()

    def test_pure_observation_survives_missing_layout_provenance(self):
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(
                    runtime, "build_gui_snapshot",
                    return_value={
                        "frameId": None,
                        "contentFrameId": "pixels",
                        "screenshotPngBase64": "png",
                    }):
            response = runtime.perform_gui([{"type": "screenshot"}])

        self.assertTrue(response["ok"])
        self.assertIsNone(response["snapshot"]["frameId"])

    def test_gui_settle_requires_a_300ms_stable_run(self):
        frame = {
            "data": bytes([10, 20, 30]),
            "stride": 3,
            "channels": 3,
            "width": 1,
            "height": 1,
        }
        with mock.patch.object(
                runtime, "capture_window_pixels",
                side_effect=[frame, frame, frame, frame]):
            now = [0.0]

            def sleep(seconds):
                now[0] += seconds

            result = runtime.wait_for_gui_settle(
                self.desktop,
                800,
                clock=lambda: now[0],
                sleep=sleep,
            )

        self.assertTrue(result["stable"])
        self.assertGreaterEqual(result["stable_samples"], 4)
        self.assertGreaterEqual(result["stable_for_ms"], 300)
        self.assertEqual(result["polls"], 3)
        self.assertEqual(result["elapsed_ms"], 300)
        note = runtime.gui_settle_note(result)
        self.assertIn("consecutive full-desktop screenshots", note)
        self.assertIn("over 300ms", note)
        self.assertIn("does not verify any action effect", note)

    def test_gui_settle_requires_four_samples_even_when_polling_is_delayed(self):
        frame = {
            "data": bytes([10, 20, 30]),
            "stride": 3,
            "channels": 3,
            "width": 1,
            "height": 1,
        }
        now = [0.0]

        def delayed_sleep(_seconds):
            now[0] += 0.15

        with mock.patch.object(
                runtime, "capture_window_pixels",
                side_effect=[frame, frame, frame, frame]):
            result = runtime.wait_for_gui_settle(
                self.desktop,
                800,
                clock=lambda: now[0],
                sleep=delayed_sleep,
            )

        self.assertTrue(result["stable"])
        self.assertEqual(result["stable_samples"], 4)
        self.assertEqual(result["polls"], 3)
        self.assertEqual(result["elapsed_ms"], 450)

    def test_gui_settle_compares_every_pixel_without_colour_tolerance(self):
        first = {
            "data": bytes([0] * 27),
            "stride": 27,
            "channels": 3,
            "width": 9,
            "height": 1,
        }
        changed_unsampled_pixel = dict(
            first,
            data=bytes([0, 0, 0, 1, 0, 0] + [0] * 21),
        )
        samples = [
            first,
            first,
            changed_unsampled_pixel,
            changed_unsampled_pixel,
            changed_unsampled_pixel,
            changed_unsampled_pixel,
        ]
        now = [0.0]

        def sleep(seconds):
            now[0] += seconds

        with mock.patch.object(
                runtime, "capture_window_pixels", side_effect=samples):
            result = runtime.wait_for_gui_settle(
                self.desktop,
                1000,
                clock=lambda: now[0],
                sleep=sleep,
            )

        self.assertTrue(result["stable"])
        self.assertEqual(result["stable_samples"], 4)
        self.assertEqual(result["polls"], 5)
        self.assertEqual(result["elapsed_ms"], 500)

    def test_gui_settle_resets_the_stable_run_after_a_change(self):
        first = {
            "data": bytes([0, 0, 0]),
            "stride": 3,
            "channels": 3,
            "width": 1,
            "height": 1,
        }
        second = dict(first, data=bytes([255, 255, 255]))
        samples = [first, first, first, second, second, second, second]
        now = [0.0]

        def sleep(seconds):
            now[0] += seconds

        with mock.patch.object(
                runtime, "capture_window_pixels", side_effect=samples):
            result = runtime.wait_for_gui_settle(
                self.desktop,
                1000,
                clock=lambda: now[0],
                sleep=sleep,
            )

        self.assertTrue(result["stable"])
        self.assertEqual(result["stable_samples"], 4)
        self.assertGreaterEqual(result["stable_for_ms"], 300)
        self.assertEqual(result["polls"], 6)

    def test_gui_settle_timeout_is_visible_not_an_error(self):
        before = {
            "data": bytes([0, 0, 0]),
            "stride": 3,
            "channels": 3,
            "width": 1,
            "height": 1,
        }
        after = dict(before, data=bytes([255, 255, 255]))
        now = [0.0]

        def sleep(seconds):
            now[0] += seconds

        with mock.patch.object(
                runtime, "capture_window_pixels", side_effect=[before, after]):
            result = runtime.wait_for_gui_settle(
                self.desktop,
                100,
                clock=lambda: now[0],
                sleep=sleep,
            )

        self.assertFalse(result["stable"])
        self.assertTrue(result["timed_out"])
        self.assertIn(
            "quiescence was not established", runtime.gui_settle_note(result))

    def test_settle_ms_zero_is_reported_as_an_explicit_skip(self):
        result = runtime.wait_for_gui_settle(self.desktop, 0)

        self.assertTrue(result["skipped"])
        self.assertEqual(result["reason"], "settle_ms_zero")
        self.assertIn("settle_ms=0", runtime.gui_settle_note(result))

    def test_perform_gui_rejects_settle_above_limit_before_input(self):
        with mock.patch.object(runtime, "execute_gui_action") as execute, \
                mock.patch.object(runtime, "desktop_bounds") as bounds:
            response = runtime.perform_gui(
                [{"type": "click", "x": 1, "y": 2}],
                settle_ms=3001,
            )

        self.assertFalse(response["ok"])
        self.assertTrue(response["preflight_only"])
        self.assertIn("between 0 and 3000", response["error"])
        execute.assert_not_called()
        bounds.assert_not_called()

    def test_perform_gui_uses_builtin_settle_for_mutating_batch(self):
        pixels = {
            "data": bytes([0, 0, 0]),
            "stride": 3,
            "channels": 3,
            "width": 1,
            "height": 1,
        }
        settled = {
            "stable": True,
            "elapsed_ms": 300,
            "polls": 3,
            "stable_samples": 4,
            "stable_for_ms": 300,
            "pixels": pixels,
        }
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(runtime, "execute_gui_action"), \
                mock.patch.object(
                    runtime, "capture_window_pixels", return_value=pixels), \
                mock.patch.object(
                    runtime, "wait_for_gui_settle",
                    return_value=settled) as wait, \
                mock.patch.object(
                    runtime, "build_gui_snapshot",
                    return_value={"frameId": "layout", "screenshotPngBase64": "png"}):
            response = runtime.perform_gui(
                [{"type": "click", "x": 1, "y": 2}],
                settle_ms=900,
            )

        wait.assert_called_once_with(self.desktop, 900)
        self.assertTrue(response["ok"])
        self.assertTrue(response["quiescent"])
        self.assertTrue(response["effect_unknown"])
        self.assertIn("pixel-identical over 300ms", response["text"])
        self.assertIn("effect_unknown=true", response["text"])

    def test_gui_reports_nonassertive_visual_change(self):
        before = {
            "data": bytes([0, 0, 0]),
            "stride": 3,
            "channels": 3,
            "width": 1,
            "height": 1,
        }
        after = dict(before, data=bytes([255, 255, 255]))
        settled = {
            "stable": True,
            "elapsed_ms": 300,
            "polls": 3,
            "stable_samples": 4,
            "stable_for_ms": 300,
            "pixels": after,
        }
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(
                    runtime, "capture_window_pixels",
                    side_effect=[before, after, after]), \
                mock.patch.object(runtime, "execute_gui_action"), \
                mock.patch.object(
                    runtime, "wait_for_gui_settle", return_value=settled), \
                mock.patch.object(
                    runtime, "build_gui_snapshot",
                    return_value={"frameId": "layout", "screenshotPngBase64": "png"}):
            response = runtime.perform_gui(
                [{"type": "click", "x": 1, "y": 2}],
                settle_ms=900,
            )

        self.assertTrue(response["changed_since_before"])
        self.assertTrue(response["visual_change_observed"])
        self.assertTrue(response["guiResult"]["visual_change_observed"])
        self.assertIn("exact_full_frame", response["visual_change_method"])
        self.assertNotIn("visual_change_verified", response)
        self.assertNotIn("visual_change_verified", response["guiResult"])
        self.assertGreater(response["visual_change_percent"], 0)
        self.assertIn("visual_changed=true", response["text"])
        self.assertIn("non-assertive", response["text"])

    def test_gui_reports_nonassertive_no_visual_change(self):
        frame = {
            "data": bytes([10, 20, 30]),
            "stride": 3,
            "channels": 3,
            "width": 1,
            "height": 1,
        }
        settled = {
            "stable": True,
            "elapsed_ms": 300,
            "polls": 3,
            "stable_samples": 4,
            "stable_for_ms": 300,
            "pixels": frame,
        }
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(
                    runtime, "capture_window_pixels",
                    side_effect=[frame, frame, frame]), \
                mock.patch.object(runtime, "execute_gui_action"), \
                mock.patch.object(
                    runtime, "wait_for_gui_settle", return_value=settled), \
                mock.patch.object(
                    runtime, "build_gui_snapshot",
                    return_value={"frameId": "layout", "screenshotPngBase64": "png"}):
            response = runtime.perform_gui(
                [{"type": "click", "x": 1, "y": 2}],
            )

        self.assertFalse(response["changed_since_before"])
        self.assertFalse(response["visual_change_observed"])
        self.assertEqual(
            response["visual_change_method"], "exact_full_frame_bytes")
        self.assertNotIn("visual_change_verified", response)
        self.assertIn("visual_changed=false", response["text"])

    def test_gui_observation_is_immediate_and_does_not_compare_frames(self):
        snapshot = {
            "frameId": "layout",
            "contentFrameId": "pixels",
            "screenshotPngBase64": "png",
        }
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(runtime, "capture_window_pixels") as capture, \
                mock.patch.object(runtime, "execute_gui_action"), \
                mock.patch.object(
                    runtime, "wait_for_gui_settle") as wait, \
                mock.patch.object(
                    runtime, "build_gui_snapshot",
                    return_value=snapshot):
            response = runtime.perform_gui([{"type": "screenshot"}])

        wait.assert_not_called()
        capture.assert_not_called()
        self.assertIsNone(response["changed_since_before"])
        self.assertIsNone(response["visual_change_observed"])
        self.assertIsNone(response["quiescent"])
        self.assertFalse(response["effect_unknown"])
        self.assertEqual(response["snapshot"], snapshot)

    def test_gui_visual_change_detects_changes_missed_by_sampled_region_stats(self):
        before = {
            "data": bytes([0] * 27),
            "stride": 27,
            "channels": 3,
            "width": 9,
            "height": 1,
        }
        after = dict(
            before,
            data=bytes([0, 0, 0, 1, 0, 0] + [0] * 21),
        )

        result = runtime.gui_visual_change(before, after)

        self.assertTrue(result["changed_since_before"])
        self.assertEqual(result["visual_change_percent"], 0.0)
        self.assertIn("sampled_rgb_stride", result["visual_change_method"])

    def test_runtime_rejects_frame_stale_against_live_desktop(self):
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(
                    runtime, "build_gui_snapshot",
                    return_value={
                        "frameId": "current-frame",
                        "screenshotPngBase64": "png",
                    }), \
                mock.patch.object(runtime, "execute_gui_action") as execute:
            response = runtime.perform_gui(
                [{"type": "click", "x": 1, "y": 2}],
                frame_id="old-frame",
            )

        self.assertFalse(response["ok"])
        self.assertIn("STALE GUI FRAME", response["error"])
        self.assertEqual(response["dispatched"], 0)
        execute.assert_not_called()

    def test_keyboard_only_batch_ignores_coordinate_frame_guard(self):
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(
                    runtime, "build_gui_snapshot",
                    return_value={
                        "frameId": "current-frame",
                        "screenshotPngBase64": "png",
                    }), \
                mock.patch.object(runtime, "execute_gui_action") as execute, \
                mock.patch.object(
                    runtime, "wait_for_gui_settle",
                    return_value={"stable": False, "elapsed_ms": 0, "polls": 0,
                                  "skipped": True}):
            response = runtime.perform_gui(
                [{"type": "type", "text": "hello"}],
                frame_id="old-coordinate-frame",
                settle_ms=0,
            )

        self.assertTrue(response["ok"])
        execute.assert_called_once()

    def test_runtime_stale_guard_uses_layout_frame_not_content_frame(self):
        layout = {
            "desktop": {"x": 0, "y": 0, "width": 1920, "height": 1080},
            "activeWindow": "0x20",
            "windows": [],
        }
        frame_id = runtime.desktop_layout_frame_id(layout)
        snapshots = [
            {
                "frameId": frame_id,
                "contentFrameId": "changed-content",
                "screenshotPngBase64": "png",
            },
            {
                "frameId": frame_id,
                "contentFrameId": "final-content",
                "screenshotPngBase64": "png",
            },
        ]
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(runtime, "build_gui_snapshot",
                                  side_effect=snapshots), \
                mock.patch.object(
                    runtime, "desktop_layout_state", return_value=layout), \
                mock.patch.object(runtime, "execute_gui_action") as execute, \
                mock.patch.object(
                    runtime, "wait_for_gui_settle",
                    return_value={"stable": False, "elapsed_ms": 0, "polls": 0,
                                  "skipped": True}):
            response = runtime.perform_gui(
                [{"type": "click", "x": 1, "y": 2}],
                frame_id=frame_id,
            )

        self.assertTrue(response["ok"])
        execute.assert_called_once()

    def test_runtime_rechecks_frame_before_each_coordinate_action(self):
        layout_a = {
            "desktop": {"x": 0, "y": 0, "width": 1920, "height": 1080},
            "activeWindow": "0x10",
            "windows": [],
        }
        layout_b = dict(layout_a, activeWindow="0x20")
        frame_id = runtime.desktop_layout_frame_id(layout_a)
        current = {
            "frameId": frame_id,
            "contentFrameId": "pixels-a",
            "screenshotPngBase64": "png",
        }
        executed = []

        def execute(action):
            executed.append(action["type"])

        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(
                    runtime, "build_gui_snapshot", return_value=current), \
                mock.patch.object(
                    runtime, "desktop_layout_state",
                    side_effect=[layout_a, layout_b]), \
                mock.patch.object(
                    runtime, "execute_gui_action", side_effect=execute):
            response = runtime.perform_gui(
                [
                    {"type": "click", "x": 1, "y": 2},
                    {"type": "wait"},
                    {"type": "click", "x": 3, "y": 4},
                ],
                frame_id=frame_id,
            )

        self.assertFalse(response["ok"])
        self.assertIn("changed during the batch", response["error"])
        self.assertEqual(executed, ["click", "wait"])
        self.assertIn("before action 3", response["error"])
        self.assertEqual(response["dispatched"], 2)
        self.assertTrue(response["input_events_emitted"])
        self.assertEqual(response["input_actions_dispatched"], 1)
        self.assertTrue(response["mutation_unknown"])

    def test_gui_failure_exports_structured_result_metadata(self):
        response = runtime.finalize_gui_response({
            "ok": False,
            "error": "stale",
            "dispatched": 2,
            "input_events_emitted": True,
            "input_actions_dispatched": 1,
            "effect_unknown": True,
            "quiescent": None,
        })

        self.assertEqual(response["guiResult"]["dispatched"], 2)
        self.assertTrue(response["guiResult"]["input_events_emitted"])
        self.assertEqual(response["guiResult"]["input_actions_dispatched"], 1)
        self.assertFalse(response["guiResult"]["partial_input_possible"])
        for removed in (
            "window_commands_dispatched",
            "desktop_state_mutated",
            "window_action_observations",
        ):
            self.assertNotIn(removed, response["guiResult"])

    def test_post_dispatch_capture_failure_preserves_mutation_accounting(self):
        initial = {"frameId": "layout", "screenshotPngBase64": "png"}
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(
                    runtime, "build_gui_snapshot",
                    side_effect=[initial, RuntimeError("capture failed")]), \
                mock.patch.object(runtime, "capture_window_pixels", return_value=None), \
                mock.patch.object(runtime, "execute_gui_action"), \
                mock.patch.object(
                    runtime, "wait_for_gui_settle",
                    return_value={"stable": False, "elapsed_ms": 0, "polls": 0,
                                  "skipped": True}):
            response = runtime.perform_gui([
                {"type": "move", "x": 1, "y": 2},
            ], settle_ms=0)

        self.assertFalse(response["ok"])
        self.assertEqual(response["dispatched"], 1)
        self.assertTrue(response["input_events_emitted"])
        self.assertEqual(response["input_actions_dispatched"], 1)
        self.assertNotIn("desktop_state_mutated", response)
        self.assertIn("post-dispatch observation failed", response["error"])

    def test_action_failure_and_error_screenshot_failure_preserve_partial_input(self):
        initial = {"frameId": "layout", "screenshotPngBase64": "png"}
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(
                    runtime, "build_gui_snapshot",
                    side_effect=[initial, RuntimeError("capture failed")]), \
                mock.patch.object(runtime, "capture_window_pixels", return_value=None), \
                mock.patch.object(
                    runtime, "execute_gui_action",
                    side_effect=runtime.InputEmissionError(
                        "button release", True)):
            response = runtime.perform_gui([
                {"type": "click", "x": 1, "y": 2},
            ])

        self.assertFalse(response["ok"])
        self.assertTrue(response["input_events_emitted"])
        self.assertTrue(response["partial_input_possible"])
        self.assertTrue(response["mutation_unknown"])
        self.assertEqual(response["snapshot"], initial)

    def test_final_capture_change_clears_quiescent(self):
        stable = {
            "data": bytes([0, 0, 0]),
            "stride": 3,
            "channels": 3,
            "width": 1,
            "height": 1,
        }
        changed = dict(stable, data=bytes([1, 0, 0]))
        settled = {
            "stable": True,
            "elapsed_ms": 300,
            "polls": 3,
            "stable_samples": 4,
            "stable_for_ms": 300,
            "pixels": stable,
        }
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(
                    runtime, "build_gui_snapshot",
                    return_value={
                        "frameId": "layout",
                        "screenshotPngBase64": "png",
                    }), \
                mock.patch.object(
                    runtime, "capture_window_pixels",
                    side_effect=[stable, stable, changed]), \
                mock.patch.object(runtime, "execute_gui_action"), \
                mock.patch.object(
                    runtime, "wait_for_gui_settle", return_value=settled):
            response = runtime.perform_gui([
                {"type": "click", "x": 1, "y": 2},
            ])

        self.assertTrue(response["ok"])
        self.assertIsNone(response["quiescent"])
        self.assertIsNone(response["changed_since_before"])
        self.assertIn("changed again", response["text"])

    def test_gui_exception_still_has_structured_result(self):
        with mock.patch.object(
                runtime, "perform_gui", side_effect=RuntimeError("capture failed")):
            response = perform_provider_operation({
                "intent": "desktop_execute_batch",
                "actions": [],
            })

        self.assertFalse(response["ok"])
        self.assertEqual(response["error"], "capture failed")
        self.assertEqual(response["guiResult"]["dispatched"], 0)
        self.assertFalse(response["guiResult"]["input_events_emitted"])

    def test_pointer_to_keyboard_batch_inserts_focus_barrier(self):
        events = []

        def execute(action):
            events.append(("dispatch", action["type"]))

        def sleep(seconds):
            events.append(("sleep", seconds))

        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(runtime, "execute_gui_action", side_effect=execute), \
                mock.patch.object(runtime.time, "sleep", side_effect=sleep), \
                mock.patch.object(
                    runtime, "wait_for_gui_settle",
                    return_value={"stable": False, "elapsed_ms": 0, "polls": 0,
                                  "skipped": True}), \
                mock.patch.object(
                    runtime, "build_gui_snapshot",
                    return_value={"frameId": "layout", "screenshotPngBase64": "png"}):
            response = runtime.perform_gui([
                {"type": "click", "x": 10, "y": 20},
                {"type": "type", "text": "hello"},
            ])

        self.assertTrue(response["ok"])
        self.assertEqual(events, [
            ("dispatch", "click"),
            ("sleep", runtime.GUI_POINTER_KEYBOARD_BARRIER_SECONDS),
            ("dispatch", "type"),
        ])

    def test_mid_batch_screenshot_is_rejected_before_input(self):
        events = []

        def execute(action):
            events.append(("dispatch", action["type"]))

        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(runtime, "execute_gui_action", side_effect=execute), \
                mock.patch.object(
                    runtime.time, "sleep",
                    side_effect=lambda seconds: events.append(("sleep", seconds))), \
                mock.patch.object(
                    runtime, "wait_for_gui_settle",
                    return_value={"stable": False, "elapsed_ms": 0, "polls": 0,
                                  "skipped": True}), \
                mock.patch.object(
                    runtime, "build_gui_snapshot",
                    return_value={"frameId": "layout", "screenshotPngBase64": "png"}):
            response = runtime.perform_gui([
                {"type": "click", "x": 10, "y": 20},
                {"type": "screenshot"},
                {"type": "keypress", "keys": ["ENTER"]},
            ])

        self.assertFalse(response["ok"])
        self.assertTrue(response["preflight_only"])
        self.assertFalse(response["input_events_emitted"])
        self.assertEqual(events, [])

    def test_explicit_wait_already_breaks_pointer_to_keyboard_transition(self):
        sleeps = []

        def validate(actions, *_args):
            return [
                dict(action, seconds=1.0)
                if action["type"] == "wait"
                else action
                for action in actions
            ], []

        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(
                    runtime, "validate_gui_actions",
                    side_effect=validate), \
                mock.patch.object(runtime, "execute_gui_action"), \
                mock.patch.object(
                    runtime.time, "sleep", side_effect=lambda seconds: sleeps.append(seconds)), \
                mock.patch.object(
                    runtime, "wait_for_gui_settle",
                    return_value={"stable": False, "elapsed_ms": 0, "polls": 0,
                                  "skipped": True}), \
                mock.patch.object(
                    runtime, "build_gui_snapshot",
                    return_value={"frameId": "layout", "screenshotPngBase64": "png"}):
            response = runtime.perform_gui([
                {"type": "click", "x": 10, "y": 20},
                {"type": "wait"},
                {"type": "keypress", "keys": ["ENTER"]},
            ])

        self.assertTrue(response["ok"])
        self.assertEqual(sleeps, [])

    def test_fixed_wait_fully_covers_pointer_to_keyboard_barrier(self):
        sleeps = []
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(runtime, "execute_gui_action"), \
                mock.patch.object(
                    runtime.time, "sleep", side_effect=lambda seconds: sleeps.append(seconds)), \
                mock.patch.object(
                    runtime, "wait_for_gui_settle",
                    return_value={"stable": False, "elapsed_ms": 0, "polls": 0,
                                  "skipped": True}), \
                mock.patch.object(
                    runtime, "build_gui_snapshot",
                    return_value={"frameId": "layout", "screenshotPngBase64": "png"}):
            response = runtime.perform_gui([
                {"type": "click", "x": 10, "y": 20},
                {"type": "wait"},
                {"type": "type", "text": "hello"},
            ])

        self.assertTrue(response["ok"])
        self.assertEqual(sleeps, [])

    def test_consecutive_keyboard_actions_have_a_processing_barrier(self):
        events = []
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(
                    runtime, "validate_gui_actions",
                    side_effect=lambda actions, *_args: (actions, [])), \
                mock.patch.object(
                    runtime, "execute_gui_action",
                    side_effect=lambda action: events.append(
                        ("dispatch", action["type"]))), \
                mock.patch.object(
                    runtime.time, "sleep",
                    side_effect=lambda seconds: events.append(("sleep", seconds))), \
                mock.patch.object(
                    runtime, "wait_for_gui_settle",
                    return_value={"stable": False, "elapsed_ms": 0, "polls": 0,
                                  "skipped": True}), \
                mock.patch.object(
                    runtime, "build_gui_snapshot",
                    return_value={"frameId": "layout", "screenshotPngBase64": "png"}):
            response = runtime.perform_gui([
                {"type": "type", "text": "one"},
                {"type": "keypress", "keys": ["ENTER"]},
                {"type": "type", "text": "two"},
            ], settle_ms=0)

        self.assertTrue(response["ok"])
        self.assertEqual(events, [
            ("dispatch", "type"),
            ("sleep", runtime.GUI_KEYBOARD_ACTION_INTERVAL_SECONDS),
            ("dispatch", "keypress"),
            ("sleep", runtime.GUI_KEYBOARD_ACTION_INTERVAL_SECONDS),
            ("dispatch", "type"),
        ])

    def test_gui_reports_fixed_waits_without_micro_wait_classification(self):
        def validate(actions, *_args):
            return [
                dict(action, seconds=1.0)
                if action["type"] == "wait"
                else action
                for action in actions
            ], []

        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(
                    runtime, "validate_gui_actions",
                    side_effect=validate), \
                mock.patch.object(runtime, "execute_gui_action"), \
                mock.patch.object(
                    runtime, "wait_for_gui_settle",
                    return_value={"stable": False, "elapsed_ms": 0, "polls": 0,
                                  "skipped": True}), \
                mock.patch.object(
                    runtime, "build_gui_snapshot",
                    return_value={"frameId": "layout", "screenshotPngBase64": "png"}):
            response = runtime.perform_gui([
                {"type": "keypress", "keys": ["ARROWDOWN"]},
                {"type": "wait"},
                {"type": "keypress", "keys": ["ARROWDOWN"]},
                {"type": "wait"},
            ], settle_ms=0)

        self.assertTrue(response["ok"])
        self.assertEqual(response["wait_actions_requested"], 2)
        self.assertEqual(response["wait_seconds_requested"], 2.0)
        self.assertEqual(response["keyboard_micro_wait_actions"], 0)

    def test_execute_gui_action_uses_only_prevalidated_payload(self):
        events = []
        with mock.patch.object(
                runtime, "send_mouse_click",
                side_effect=lambda *args: events.append(("click", args))), \
                mock.patch.object(
                    runtime, "send_drag_path",
                    side_effect=lambda path: events.append(("drag", path))), \
                mock.patch.object(
                    runtime, "send_gui_scroll",
                    side_effect=lambda *args: events.append(("scroll", args))), \
                mock.patch.object(
                    runtime, "send_text",
                    side_effect=lambda text: events.append(("type", text))), \
                mock.patch.object(
                    runtime, "send_key",
                    side_effect=lambda key: events.append(("key", key))), \
                mock.patch.object(
                    runtime, "require_window_focus",
                    side_effect=AssertionError("app-free gui must not focus a window")):
            runtime.execute_gui_action({
                "type": "click",
                "x": 3.0,
                "y": 4.0,
                "button": "left",
            })
            runtime.execute_gui_action({
                "type": "drag",
                "path": [(1.0, 2.0), (3.0, 4.0)],
            })
            runtime.execute_gui_action({
                "type": "scroll",
                "x": 5.0,
                "y": 6.0,
                "scroll_x": 0.0,
                "scroll_y": 2.0,
            })
            runtime.execute_gui_action({"type": "type", "text": "hello"})
            runtime.execute_gui_action({
                "type": "keypress",
                "keys": ["CTRL", "S"],
            })

        self.assertEqual(
            [event[0] for event in events],
            ["click", "drag", "scroll", "type", "key"],
        )
