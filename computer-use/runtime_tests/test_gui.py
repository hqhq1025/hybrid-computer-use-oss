#!/usr/bin/env python3
"""Linux provider GUI screen coordinate, layout frame, batch execution, and schema tests."""

from runtime_test_support import *  # noqa: F401,F403


def captured(value, matches_bounds=True):
    """Return a CapturedGUISnapshot double; ``pixels`` is its comparison sample."""
    if isinstance(value, Exception):
        return value
    value = dict(value)
    result = mock.Mock(spec=runtime.CapturedGUISnapshot)
    result.pixels = value.pop("pixels", None)
    result.has_image = bool(value.get("screenshotPngBase64"))
    result.frame_id = value.get("frameId")
    result.matches_bounds.return_value = matches_bounds
    result.encode.return_value = value
    return result


def patch_gui_capture(*, return_value=None, side_effect=None):
    """Mock raw capture explicitly; existing dispatch cases supply wire fixtures."""
    if side_effect is not None:
        return mock.patch.object(runtime, "capture_gui_snapshot",
                                 side_effect=[captured(value) for value in side_effect])
    return mock.patch.object(runtime, "capture_gui_snapshot",
                             return_value=captured(return_value))


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

    def test_delivered_image_point_maps_back_by_pixel_center(self):
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

    def test_1080p_desktop_is_delivered_at_the_size_the_model_sees(self):
        # GPT-5.4 at detail high shrinks a 1920x1080 image to 1365x768 before
        # the model sees it, so coordinates read from that image were 0.71x
        # the delivered pixels. Delivering 1365x768 makes them one space.
        dimensions = runtime.gui_model_dimensions(self.desktop)

        self.assertEqual((dimensions["width"], dimensions["height"]), (1365, 768))
        self.assertAlmostEqual(dimensions["scale_x"], 1365 / 1920)
        self.assertAlmostEqual(dimensions["scale_y"], 768 / 1080)

    def test_delivered_image_stays_within_the_model_image_box(self):
        for native, delivered in [
            ((1280, 720), (1280, 720)),
            ((1366, 768), (1366, 768)),
            ((2560, 1440), (1365, 768)),
            ((1920, 1200), (1229, 768)),
            ((3440, 1440), (1366, 572)),
            ((1080, 1920), (432, 768)),
        ]:
            with self.subTest(native=native):
                dimensions = runtime.gui_model_dimensions(
                    runtime.frame(0, 0, *native))
                self.assertEqual(
                    (dimensions["width"], dimensions["height"]), delivered)
                self.assertAlmostEqual(
                    dimensions["scale_x"], delivered[0] / native[0])
                self.assertAlmostEqual(
                    dimensions["scale_y"], delivered[1] / native[1])

    def test_last_delivered_pixel_maps_inside_the_native_desktop(self):
        dimensions = runtime.gui_model_dimensions(self.desktop)
        model = runtime.frame(0, 0, dimensions["width"], dimensions["height"])
        x, y = runtime.screen_absolute_point(
            model["width"] - 1, model["height"] - 1, model, self.desktop)

        self.assertLessEqual(x, 1919)
        self.assertLessEqual(y, 1079)
        self.assertGreater(x, 1917)
        self.assertGreater(y, 1077)

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
                mock.patch.object(runtime, "capture_gui_pixels",
                                  return_value=mock.sentinel.pixels) as capture, \
                mock.patch.object(runtime, "scale_to_model",
                                  return_value=mock.sentinel.scaled) as scale, \
                mock.patch.object(runtime, "encoded_screenshot",
                                  return_value={"data": "png", "mimeType": "image/png",
                                                "encoding": "png-compression-3"}) as encode, \
                mock.patch.object(runtime, "desktop_layout_state",
                                  return_value=layout), \
                mock.patch.object(
                    runtime, "resolve_app",
                    side_effect=AssertionError("app-free gui resolved an app")), \
                mock.patch.object(runtime, "render_tree",
                                  side_effect=AssertionError("GUI screenshot walked tree")):
            snapshot = runtime.capture_gui_snapshot().encode()

        capture.assert_called_once_with(self.desktop)
        scale.assert_called_once_with(mock.sentinel.pixels, self.desktop)
        encode.assert_called_once_with(mock.sentinel.scaled)
        self.assertEqual(snapshot["screenshotPngBase64"], "png")
        self.assertEqual(snapshot["coordinateSpace"], "screen_image_px")
        self.assertEqual(snapshot["width"], 1365)
        self.assertEqual(snapshot["height"], 768)
        self.assertEqual(
            snapshot["frameId"],
            runtime.desktop_layout_frame_id(layout),
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
        self.assertEqual(image["encoding"], "png-compression-3")

    def test_gui_snapshot_retries_until_layout_brackets_the_screenshot(self):
        first = {
            "desktop": {"x": 0, "y": 0, "width": 1920, "height": 1080},
            "activeWindow": "0x10",
            "windows": [
                {"id": "0x10", "x": 0, "y": 0, "width": 800, "height": 600},
            ],
        }
        second = dict(first, activeWindow="0x20")
        with mock.patch.object(runtime, "scale_to_model", side_effect=lambda pixbuf, bounds: pixbuf), \
                mock.patch.object(
                runtime, "desktop_layout_state",
                side_effect=[first, second, second, second]), \
                mock.patch.object(
                    runtime, "capture_gui_pixels",
                    side_effect=[mock.sentinel.raced, mock.sentinel.coherent]) as capture, \
                mock.patch.object(
                    runtime, "encoded_screenshot",
                    return_value={"data": "coherent", "mimeType": "image/png",
                                  "encoding": "png-compression-3"}) as encode:
            snapshot = runtime.capture_gui_snapshot(self.desktop).encode()

        self.assertEqual(capture.call_count, 2)
        encode.assert_called_once_with(mock.sentinel.coherent)
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
        with mock.patch.object(runtime, "scale_to_model", side_effect=lambda pixbuf, bounds: pixbuf), \
                mock.patch.object(
                runtime, "desktop_layout_state", side_effect=layouts), \
                mock.patch.object(
                    runtime, "capture_gui_pixels", return_value=mock.sentinel.pixels), \
                mock.patch.object(
                    runtime, "encoded_screenshot",
                    return_value={"data": "png", "mimeType": "image/png",
                                  "encoding": "png-compression-3"}):
            snapshot = runtime.capture_gui_snapshot(self.desktop).encode()

        self.assertIsNone(snapshot["frameId"])
        self.assertEqual(snapshot["screenshotPngBase64"], "png")

    def test_gui_snapshot_reuses_each_closing_layout_sample(self):
        layouts = [
            {"desktop": {"x": 0, "y": 0, "width": 1920, "height": 1080},
             "activeWindow": hex(index + 1), "windows": []}
            for index in range(runtime.GUI_SNAPSHOT_LAYOUT_ATTEMPTS + 1)
        ]
        with mock.patch.object(
                runtime, "desktop_layout_state", side_effect=layouts) as layout, \
                mock.patch.object(
                    runtime, "capture_gui_pixels", return_value=mock.sentinel.pixels):
            snapshot = runtime.capture_gui_snapshot(self.desktop)

        self.assertIsNone(snapshot.frame_id)
        self.assertEqual(
            layout.call_count, runtime.GUI_SNAPSHOT_LAYOUT_ATTEMPTS + 1)

    def test_gui_snapshot_title_change_during_capture_keeps_frame(self):
        first = {
            "desktop": {"x": 0, "y": 0, "width": 1920, "height": 1080},
            "activeWindow": "0x20",
            "windows": [{"id": "0x20", "x": 10, "y": 20, "width": 800,
                         "height": 600, "title": "Saving...",
                         "overrideRedirect": False}],
        }
        retitled = json.loads(json.dumps(first))
        retitled["windows"][0]["title"] = "Saved"
        with mock.patch.object(
                runtime, "desktop_layout_state",
                side_effect=[first, retitled]) as layout, \
                mock.patch.object(
                    runtime, "capture_gui_pixels",
                    return_value=mock.sentinel.pixels) as capture:
            snapshot = runtime.capture_gui_snapshot(self.desktop)

        capture.assert_called_once()
        self.assertEqual(layout.call_count, 2)
        self.assertEqual(snapshot.frame_id, runtime.desktop_layout_frame_id(first))

    def test_gui_layout_frame_ignores_pixel_only_changes(self):
        layout = {
            "desktop": {"x": 0, "y": 0, "width": 1920, "height": 1080},
            "activeWindow": "0x20",
            "windows": [
                {"id": "0x20", "x": 10, "y": 20, "width": 800, "height": 600},
            ],
        }
        with mock.patch.object(runtime, "scale_to_model", side_effect=lambda pixbuf, bounds: pixbuf), \
                mock.patch.object(runtime, "desktop_layout_state",
                               return_value=layout), \
                mock.patch.object(
                    runtime, "capture_gui_pixels",
                    side_effect=[mock.sentinel.first, mock.sentinel.second]), \
                mock.patch.object(
                    runtime, "encoded_screenshot",
                    side_effect=[
                        {"data": "png-a", "mimeType": "image/png", "encoding": "png-compression-3"},
                        {"data": "png-b", "mimeType": "image/png", "encoding": "png-compression-3"},
                    ]):
            first = runtime.capture_gui_snapshot(self.desktop).encode()
            second = runtime.capture_gui_snapshot(self.desktop).encode()

        self.assertEqual(first["frameId"], second["frameId"])
        self.assertNotEqual(first["screenshotPngBase64"], second["screenshotPngBase64"])

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
                patch_gui_capture(
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
                patch_gui_capture(
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
                patch_gui_capture( return_value=snapshot), \
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
                patch_gui_capture( return_value=snapshot), \
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
                patch_gui_capture(
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

    def test_out_of_image_point_is_rejected_once_with_batch_index(self):
        execute = mock.Mock()
        validate = mock.Mock(wraps=runtime.validate_gui_action)
        actions = [
            {"type": "type", "text": "hello"},
            {"type": "click", "x": self.desktop["width"] + 5, "y": 20},
        ]
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(runtime, "execute_gui_action", execute), \
                mock.patch.object(runtime, "validate_gui_action", validate), \
                patch_gui_capture(
                    return_value={"frameId": "layout", "screenshotPngBase64": "png"}):
            response = runtime.perform_gui(actions)

        self.assertFalse(response["ok"])
        self.assertIn("coordinate pre-validation", response["error"])
        self.assertEqual(
            [(item["index"], item["type"]) for item in response["validation_errors"]],
            [(1, "click")],
        )
        self.assertIn("outside the full desktop image bounds",
                      response["validation_errors"][0]["error"])
        # Each action is validated once; the second pass only maps coordinates.
        self.assertEqual(validate.call_count, len(actions))
        execute.assert_not_called()

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

    def test_gui_type_and_keypress_state_current_focus_boundary(self):
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(runtime, "parse_key", return_value=([], "S")), \
                mock.patch.object(runtime, "execute_gui_action"), \
                patch_gui_capture(
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
                patch_gui_capture(
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
                patch_gui_capture(
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
                patch_gui_capture(
                    return_value={
                        "frameId": None,
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
                    runtime, "wait_for_gui_settle",
                    return_value=settled) as wait, \
                patch_gui_capture(
                    return_value={"frameId": "layout", "screenshotPngBase64": "png",
                                  "pixels": pixels}):
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
                mock.patch.object(runtime, "execute_gui_action"), \
                mock.patch.object(
                    runtime, "wait_for_gui_settle", return_value=settled), \
                patch_gui_capture(side_effect=[
                    {"frameId": "layout", "screenshotPngBase64": "png",
                     "pixels": before},
                    {"frameId": "layout", "screenshotPngBase64": "png",
                     "pixels": after}]):
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
                mock.patch.object(runtime, "execute_gui_action"), \
                mock.patch.object(
                    runtime, "wait_for_gui_settle", return_value=settled), \
                patch_gui_capture(
                    return_value={"frameId": "layout", "screenshotPngBase64": "png",
                                  "pixels": frame}):
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
            "screenshotPngBase64": "png",
        }
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(runtime, "capture_window_pixels") as capture, \
                mock.patch.object(runtime, "execute_gui_action"), \
                mock.patch.object(
                    runtime, "wait_for_gui_settle") as wait, \
                patch_gui_capture(
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
                patch_gui_capture(
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
        self.assertEqual(response["failureCode"], "stale_gui_frame")
        self.assertEqual(response["failurePhase"], "precondition")
        self.assertIn("layout changed", response["error"])
        self.assertEqual(response["dispatched"], 0)
        execute.assert_not_called()

    def test_keyboard_only_batch_ignores_coordinate_frame_guard(self):
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                patch_gui_capture(
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
                "screenshotPngBase64": "png",
            },
            {
                "frameId": frame_id,
                "screenshotPngBase64": "png",
            },
        ]
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                patch_gui_capture(
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
            "screenshotPngBase64": "png",
        }
        executed = []

        def execute(action):
            executed.append(action["type"])

        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                patch_gui_capture( return_value=current), \
                mock.patch.object(
                    runtime, "desktop_layout_state",
                    side_effect=[layout_b]), \
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
        self.assertEqual(response["failureCode"], "stale_gui_frame")
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
                patch_gui_capture(
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
                patch_gui_capture(
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

    def _settled_click(self, settle_pixels, before, delivered, matches=True):
        settled = {
            "stable": True,
            "elapsed_ms": 300,
            "polls": 3,
            "stable_samples": 4,
            "stable_for_ms": 300,
            "pixels": settle_pixels,
        }
        captures = [
            captured({"frameId": "layout", "screenshotPngBase64": "png",
                      "pixels": before}),
            captured({"frameId": "layout", "screenshotPngBase64": "png-final",
                      "pixels": delivered}, matches_bounds=matches),
        ]
        with mock.patch.object(runtime, "desktop_bounds", return_value=self.desktop), \
                mock.patch.object(runtime, "capture_gui_snapshot",
                                  side_effect=captures), \
                mock.patch.object(runtime, "execute_gui_action"), \
                mock.patch.object(
                    runtime, "wait_for_gui_settle", return_value=settled):
            return runtime.perform_gui([{"type": "click", "x": 1, "y": 2}])

    def test_final_capture_change_clears_quiescent(self):
        stable = {"data": bytes([0, 0, 0]), "stride": 3, "channels": 3,
                  "width": 1, "height": 1}
        changed = dict(stable, data=bytes([1, 0, 0]))
        response = self._settled_click(stable, stable, changed)

        self.assertTrue(response["ok"])
        self.assertIsNone(response["quiescent"])
        self.assertIn("changed again", response["text"])
        # The before/after comparison describes the attached image itself.
        self.assertTrue(response["changed_since_before"])
        self.assertEqual(response["snapshot"]["screenshotPngBase64"], "png-final")

    def test_quiescent_requires_the_delivered_frame_to_equal_the_settle_sample(self):
        # Regression: the settle verdict used to be checked against two extra
        # raw samples taken around the final grab, never against the grab that
        # was encoded. A frame that changed only inside that grab still read as
        # quiescent=true next to an image of a different state.
        settled = {"data": bytes([0, 0, 0]), "stride": 3, "channels": 3,
                   "width": 1, "height": 1}
        same = dict(settled)
        repainted = dict(settled, data=bytes([0, 9, 0]))

        self.assertTrue(self._settled_click(settled, settled, same)["quiescent"])
        response = self._settled_click(settled, settled, repainted)
        self.assertIsNone(response["quiescent"])
        self.assertIsNone(response["guiResult"]["quiescent"])
        self.assertIn("changed again", response["text"])

    def test_final_frame_with_other_geometry_has_no_pixel_verdict(self):
        frame = {"data": bytes([0, 0, 0]), "stride": 3, "channels": 3,
                 "width": 1, "height": 1}
        response = self._settled_click(frame, frame, frame, matches=False)

        self.assertTrue(response["ok"])
        self.assertIsNone(response["quiescent"])
        self.assertIsNone(response["changed_since_before"])

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
                patch_gui_capture(
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
                patch_gui_capture(
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
                patch_gui_capture(
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
                patch_gui_capture(
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
                patch_gui_capture(
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
                patch_gui_capture(
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


class GUISchemaContractTests(unittest.TestCase):
    """The Linux GUI validator accepts exactly what the published GUI schema
    accepts. The Go test TestGUIActionSamplesMatchSchema writes the samples
    and their schema verdicts; key-name resolution needs a display and is
    stubbed, because the schema leaves key names open."""

    def test_validate_gui_action_matches_published_schema(self):
        path = Path(__file__).resolve().parent.parent / "testdata" / "contract" / "gui_action_samples.json"
        samples = json.loads(path.read_text())
        self.assertGreater(len(samples), 100)
        mismatches = []
        # Whether a key chord names real keys (a modifier before the main
        # key, a resolvable keysym) needs the keyboard map; the schema leaves
        # key names open, so parse_key is stubbed.
        with mock.patch.object(runtime, "keycode", return_value=1), \
                mock.patch("native.linux.provider.ax_actions.keycode", return_value=1), \
                mock.patch("native.linux.provider.ax_actions.parse_key", return_value=([], "a")):
            for sample in samples:
                try:
                    runtime.validate_gui_action(sample["action"])
                    accepted = True
                except RuntimeError:
                    accepted = False
                if accepted != sample["accepted"]:
                    mismatches.append((sample["origin"], sample["action"], sample["accepted"]))
        self.maxDiff = None
        self.assertEqual(mismatches[:20], [], "{} GUI schema/provider mismatches".format(len(mismatches)))
