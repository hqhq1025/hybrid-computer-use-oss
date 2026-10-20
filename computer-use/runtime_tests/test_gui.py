#!/usr/bin/env python3
"""Linux provider GUI lease, pixel evidence, settle wait, and input tests."""

from runtime_test_support import *  # noqa: F401,F403
import base64
import random


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


class GUICapturePipelineTests(unittest.TestCase):
    def patch(self, name, **kwargs):
        patcher = mock.patch.object(runtime, name, **kwargs)
        value = patcher.start()
        self.addCleanup(patcher.stop)
        return value

    def setUp(self):
        self.bounds = runtime.frame(0, 0, 100, 100)
        self.layout = {"desktop": self.bounds, "activeWindow": "a", "windows": []}
        published = mock.patch.dict(gui_dispatch._published_layouts, clear=True)
        published.start()
        self.addCleanup(published.stop)
        self.frame_id = runtime.desktop_layout_frame_id(self.layout)
        self.events = []
        self.frame = {"data": b"abc", "stride": 3, "channels": 3,
                      "width": 1, "height": 1}
        self.bounds_mock = self.patch("desktop_bounds", return_value=self.bounds)
        self.layout_mock = self.patch(
            "desktop_layout_state", side_effect=self.sample_layout)
        self.capture = self.patch("capture_gui_pixels", side_effect=self.capture_pixels)
        self.encoder = self.patch("encoded_screenshot", side_effect=self.encode_pixels)
        self.execute = self.patch(
            "execute_gui_action", side_effect=lambda action: self.events.append("action"))
        self.pixels = self.patch("pixbuf_sample", side_effect=self.sample_pixels)
        self.patch("scale_to_model", side_effect=lambda pixbuf, _bounds: pixbuf)
        self.wait = self.patch("wait_for_gui_settle", return_value={
            "stable": True, "elapsed_ms": 300, "polls": 3,
            "stable_samples": 4, "stable_for_ms": 300, "pixels": self.frame})

    def sample_layout(self, _bounds):
        self.events.append("layout")
        return self.layout

    def capture_pixels(self, _bounds):
        self.events.append("capture")
        return mock.sentinel.before if self.capture.call_count == 1 else mock.sentinel.after

    def sample_pixels(self, _bounds):
        self.events.append("pixels")
        return self.frame

    def encode_pixels(self, pixbuf):
        self.events.append("encode")
        return {"data": str(pixbuf), "mimeType": "image/png",
                "encoding": "png-compression-3"}

    def click(self):
        return runtime.perform_gui([{"type": "click", "x": 1, "y": 2}],
                                   frame_id=self.frame_id)

    def test_screenshot_captures_and_encodes_once_after_layout_bracket(self):
        response = runtime.perform_gui([{"type": "screenshot"}])
        self.assertTrue(response["ok"])
        self.assertEqual(self.events, ["layout", "capture", "layout", "action", "encode"])
        self.capture.assert_called_once()
        self.encoder.assert_called_once_with(mock.sentinel.before)
        self.pixels.assert_not_called()
        self.wait.assert_not_called()
        self.assertIsNone(response["quiescent"])
        self.assertIsNone(response["changed_since_before"])
        self.assertIn("without quiescence polling or a before/after visual comparison", response["text"])

    def test_success_encodes_only_final_frame_after_pixel_bracket(self):
        response = self.click()
        self.assertTrue(response["ok"])
        self.assertTrue(response["quiescent"])
        self.assertEqual(self.capture.call_count, 2)
        self.encoder.assert_called_once_with(mock.sentinel.after)
        # The final grab is both the delivered image and the "after" sample;
        # no raw desktop sample is taken between it and its encoding.
        self.assertEqual(self.events[-6:],
                         ["action", "layout", "capture", "layout", "encode", "pixels"])

    def test_cache_is_call_local(self):
        first = runtime.perform_gui([{"type": "screenshot"}])
        second = runtime.perform_gui([{"type": "screenshot"}])
        self.assertIsNot(first["snapshot"], second["snapshot"])
        self.assertEqual(self.capture.call_count, 2)
        self.assertEqual(self.encoder.call_count, 2)

    def test_capture_owns_metadata_and_caches_encoding(self):
        captured = runtime.capture_gui_snapshot(self.bounds)
        self.encoder.assert_not_called()
        self.layout["activeWindow"] = "changed"
        self.bounds["width"] = 200
        first = captured.encode()
        first["desktopLayout"]["windows"].append("changed")
        second = captured.encode()
        self.assertEqual(second["desktopLayout"]["activeWindow"], "a")
        self.assertEqual(second["desktopLayout"]["windows"], [])
        self.assertEqual(second["width"], 100)
        self.assertFalse(captured.matches_bounds(self.bounds))
        self.encoder.assert_called_once()

    def test_layout_retry_never_encodes_discarded_capture(self):
        changed = dict(self.layout, activeWindow="b")
        self.layout_mock.side_effect = [self.layout, changed, changed, changed]
        response = runtime.perform_gui([{"type": "screenshot"}])
        self.assertTrue(response["ok"])
        self.assertEqual(self.capture.call_count, 2)
        self.encoder.assert_called_once_with(mock.sentinel.after)
        self.assertEqual(response["snapshot"]["frameId"],
                         runtime.desktop_layout_frame_id(changed))

    def test_stale_initial_layout_returns_coherent_frame_before_input(self):
        response = runtime.perform_gui([{"type": "click", "x": 1, "y": 2}],
                                       frame_id="old-layout")
        self.assertFalse(response["ok"])
        self.execute.assert_not_called()
        self.encoder.assert_called_once_with(mock.sentinel.before)
        self.assertEqual(response["snapshot"]["frameId"], self.frame_id)

    def test_first_coordinate_uses_the_pre_action_bracket_without_resampling(self):
        response = self.click()
        self.assertTrue(response["ok"])
        self.execute.assert_called_once()
        # Two layout samples bracket the pre-action grab; the first coordinate
        # action is emitted right after them. Two more bracket the final grab.
        self.assertEqual(self.layout_mock.call_count, 4)
        self.assertEqual(self.events[:5],
                         ["layout", "capture", "layout", "pixels", "action"])

    def test_live_layout_rejects_later_coordinate_with_fresh_evidence(self):
        # A window that opens over the second click's point after the first.
        changed = dict(self.layout, activeWindow="b", windows=[
            {"id": "0xb", "x": 0, "y": 0, "width": 10, "height": 10}])
        self.layout_mock.side_effect = [self.layout, self.layout,
                                        changed, changed, changed]
        response = runtime.perform_gui([
            {"type": "click", "x": 1, "y": 2},
            {"type": "click", "x": 3, "y": 4}], frame_id=self.frame_id)
        self.assertFalse(response["ok"])
        self.execute.assert_called_once()
        self.assertTrue(response["mutation_unknown"])
        self.encoder.assert_called_once_with(mock.sentinel.after)
        self.assertEqual(response["snapshot"]["frameId"],
                         runtime.desktop_layout_frame_id(changed))

    # Point-based staleness: a coordinate still lands where it did when the
    # same window, with the same geometry, is topmost under it.
    def layouts(self, *window_lists):
        return [dict(self.layout, windows=[
            dict(zip(("id", "x", "y", "width", "height"), window))
            for window in windows]) for windows in window_lists]

    def publish(self, layout):
        """Return the frame id of a screenshot GUI returned with layout."""
        self.layout_mock.side_effect = [layout, layout]
        response = runtime.perform_gui([{"type": "screenshot"}])
        self.assertTrue(response["ok"])
        self.events.clear()
        self.execute.reset_mock()
        return response["snapshot"]["frameId"]

    def click_after(self, published, now, *points):
        frame_id = self.publish(published)
        self.layout_mock.side_effect = [now] * 8
        return runtime.perform_gui(
            [{"type": "click", "x": x, "y": y} for x, y in points],
            frame_id=frame_id)

    def test_unrelated_window_change_does_not_reject(self):
        before, after = self.layouts(
            [("0xa", 0, 0, 50, 50), ("0xb", 60, 60, 20, 20)],
            [("0xa", 0, 0, 50, 50), ("0xb", 70, 55, 25, 20), ("0xc", 80, 0, 10, 10)])
        self.assertNotEqual(runtime.desktop_layout_frame_id(before),
                            runtime.desktop_layout_frame_id(after))
        response = self.click_after(before, after, (10, 10))
        self.assertTrue(response["ok"], response.get("error"))
        self.execute.assert_called_once()

    def test_window_under_the_point_changing_rejects_before_input(self):
        before = self.layouts([("0xa", 0, 0, 50, 50)])[0]
        for name, windows in [
            ("moved", [("0xa", 5, 0, 50, 50)]),
            ("resized", [("0xa", 0, 0, 40, 50)]),
            ("covered", [("0xa", 0, 0, 50, 50), ("0xc", 5, 5, 10, 10)]),
            ("closed", []),
            ("raised other", [("0xa", 0, 0, 50, 50), ("0xb", 0, 0, 30, 30)]),
        ]:
            with self.subTest(name):
                response = self.click_after(before, self.layouts(windows)[0], (10, 10))
                self.assertFalse(response["ok"])
                self.assertEqual(response["failureCode"], "stale_gui_frame")
                self.execute.assert_not_called()

    def test_point_outside_any_window_stays_valid_until_one_covers_it(self):
        before, unrelated, covering = self.layouts(
            [("0xa", 0, 0, 20, 20)],
            [("0xa", 0, 0, 30, 30)],
            [("0xa", 0, 0, 30, 30), ("0xd", 50, 50, 30, 30)])
        self.assertTrue(self.click_after(before, unrelated, (60, 60))["ok"])
        self.assertFalse(self.click_after(before, covering, (60, 60))["ok"])

    def test_every_point_of_a_drag_is_checked(self):
        before, after = self.layouts(
            [("0xa", 0, 0, 50, 50)],
            [("0xa", 0, 0, 50, 50), ("0xe", 70, 70, 20, 20)])
        frame_id = self.publish(before)
        self.layout_mock.side_effect = [after] * 8
        response = runtime.perform_gui([{"type": "drag", "path": [
            {"x": 10, "y": 10}, {"x": 75, "y": 75}]}], frame_id=frame_id)
        self.assertFalse(response["ok"])
        self.execute.assert_not_called()

    def test_frame_this_provider_did_not_publish_rejects_any_layout_change(self):
        # After a provider restart the core still holds its old frame id.
        before, after = self.layouts(
            [("0xa", 0, 0, 50, 50), ("0xb", 60, 60, 20, 20)],
            [("0xa", 0, 0, 50, 50), ("0xb", 70, 60, 20, 20)])
        frame_id = self.publish(before)
        gui_dispatch._published_layouts.clear()
        self.layout_mock.side_effect = [after] * 8
        response = runtime.perform_gui([{"type": "click", "x": 10, "y": 10}],
                                       frame_id=frame_id)
        self.assertFalse(response["ok"])
        self.execute.assert_not_called()

    def test_desktop_resize_rejects(self):
        before = self.layouts([("0xa", 0, 0, 50, 50)])[0]
        after = dict(before, desktop=dict(self.bounds, width=90))
        self.assertFalse(self.click_after(before, after, (10, 10))["ok"])

    def test_mid_batch_unrelated_change_continues_and_target_change_stops(self):
        before, unrelated, covering = self.layouts(
            [("0xa", 0, 0, 50, 50), ("0xb", 60, 60, 20, 20)],
            [("0xa", 0, 0, 50, 50), ("0xb", 65, 60, 20, 20)],
            [("0xa", 0, 0, 50, 50), ("0xb", 0, 0, 20, 20)])
        for later, ok, calls in [(unrelated, True, 2), (covering, False, 1)]:
            with self.subTest(ok=ok):
                frame_id = self.publish(before)
                # Bracket of the pre-action grab, then the re-sample before
                # the second click, then the final bracket.
                self.layout_mock.side_effect = [before, before] + [later] * 6
                response = runtime.perform_gui([
                    {"type": "click", "x": 10, "y": 10},
                    {"type": "click", "x": 12, "y": 12}], frame_id=frame_id)
                self.assertEqual(response["ok"], ok)
                self.assertEqual(self.execute.call_count, calls)
                if not ok:
                    self.assertIn("changed during the batch", response["error"])
                    self.assertTrue(response["mutation_unknown"])

    def test_absent_initial_capture_rejects_keyboard_input_too(self):
        self.capture.side_effect = None
        self.capture.return_value = None
        response = runtime.perform_gui([{"type": "type", "text": "hello"}])
        self.assertFalse(response["ok"])
        self.execute.assert_not_called()
        self.encoder.assert_not_called()

    def test_initial_capture_exception_is_classified_before_input(self):
        self.capture.side_effect = RuntimeError("capture failed")
        response = self.click()
        self.assertFalse(response["ok"])
        self.assertFalse(response["input_events_emitted"])
        self.execute.assert_not_called()

    def test_partial_emission_uses_only_fresh_frame(self):
        self.execute.side_effect = runtime.InputEmissionError("release failed", True)
        response = self.click()
        self.assertFalse(response["ok"])
        self.assertTrue(response["effect_unknown"])
        self.assertTrue(response["partial_input_possible"])
        self.encoder.assert_called_once_with(mock.sentinel.after)
        self.assertIn("fresh", response["text"])

    def test_partial_emission_with_absent_capture_labels_fallback(self):
        self.capture.side_effect = [mock.sentinel.before, None]
        self.execute.side_effect = runtime.InputEmissionError("release failed", True)
        response = self.click()
        self.assertFalse(response["ok"])
        self.assertTrue(response["partial_input_possible"])
        self.assertTrue(response["mutation_unknown"])
        self.encoder.assert_called_once_with(mock.sentinel.before)
        self.assertIn("pre-action screenshot", response["text"])
        self.assertIn("does not show", response["text"])

    def test_absent_final_capture_is_not_success(self):
        self.capture.side_effect = [mock.sentinel.before, None]
        response = self.click()
        self.assertFalse(response["ok"])
        self.assertTrue(response["effect_unknown"])
        self.assertTrue(response["mutation_unknown"])
        self.assertEqual(response["input_actions_dispatched"], 1)
        self.encoder.assert_called_once_with(mock.sentinel.before)
        self.assertIn("pre-action screenshot", response["text"])

    def test_final_encoder_exception_preserves_mutation_and_fallback(self):
        self.encoder.side_effect = [RuntimeError("encoder failed"),
                                   self.encode_pixels(mock.sentinel.before)]
        response = self.click()
        self.assertFalse(response["ok"])
        self.assertTrue(response["effect_unknown"])
        self.assertTrue(response["mutation_unknown"])
        self.assertEqual(response["input_actions_dispatched"], 1)
        self.assertEqual(self.encoder.call_args_list,
                         [mock.call(mock.sentinel.after), mock.call(mock.sentinel.before)])
        self.assertIn("pre-action screenshot", response["text"])

    def test_partial_emission_with_encoder_failure_preserves_partial_evidence(self):
        self.execute.side_effect = runtime.InputEmissionError("release failed", True)
        self.encoder.side_effect = [RuntimeError("encoder failed"),
                                   self.encode_pixels(mock.sentinel.before)]
        response = self.click()
        self.assertFalse(response["ok"])
        self.assertTrue(response["partial_input_possible"])
        self.assertTrue(response["effect_unknown"])
        self.assertTrue(response["mutation_unknown"])
        self.assertIn("pre-action screenshot", response["text"])

    def test_no_encoder_candidate_is_classified_as_failure(self):
        self.encoder.side_effect = None
        self.encoder.return_value = None
        response = self.click()
        self.assertFalse(response["ok"])
        self.assertTrue(response["effect_unknown"])
        self.assertTrue(response["mutation_unknown"])
        self.assertFalse(response["snapshot"].get("screenshotPngBase64"))
        self.assertIn("No screenshot", response["text"])

    def test_screenshot_encoder_exception_is_attempted_once(self):
        self.encoder.side_effect = RuntimeError("encoder failed")
        response = runtime.perform_gui([{"type": "screenshot"}])
        self.assertFalse(response["ok"])
        self.assertFalse(response["effect_unknown"])
        self.assertFalse(response["mutation_unknown"])
        self.capture.assert_called_once()
        self.encoder.assert_called_once()
        self.assertIn("No screenshot", response["text"])

    def test_encoding_failure_cache_reraises_bounded_description(self):
        captured = runtime.capture_gui_snapshot(self.bounds)
        self.encoder.side_effect = ValueError("encoder failure " + "x" * 2048)
        with self.assertRaises(ValueError):
            captured.encode()
        with self.assertRaises(RuntimeError) as repeated:
            captured.encode()
        self.assertIn("encoder failure", str(repeated.exception))
        self.assertLessEqual(len(str(repeated.exception)), 512)
        self.encoder.assert_called_once()

    def test_changed_native_geometry_invalidates_final_pixel_coherence(self):
        # Both native sizes scale to 1365x768; delivered dimensions are insufficient.
        initial = runtime.frame(0, 0, 2560, 1440)
        final = runtime.frame(0, 0, 3840, 2160)
        self.bounds_mock.side_effect = [initial, final]
        response = runtime.perform_gui([{"type": "type", "text": "hello"}])
        self.assertTrue(response["ok"])
        self.assertIsNone(response["quiescent"])
        self.assertIsNone(response["changed_since_before"])
        self.assertEqual(response["snapshot"]["width"], 1365)
        self.assertEqual(response["snapshot"]["height"], 768)
        self.assertIn("changed again", response["text"])


class GUIEncodingTests(unittest.TestCase):
    def test_base64_budget_padding_boundary_encodes_only_selected_bytes(self):
        for length, budget, expected in [(3, 4, "png"), (4, 7, "jpeg"),
                                          (4, 8, "png"), (6, 8, "png"),
                                          (7, 8, "jpeg")]:
            with self.subTest(length=length, budget=budget):
                pixbuf = mock.Mock()
                pixbuf.save_to_bufferv.side_effect = [(True, b"p" * length), (True, b"j")]
                with mock.patch.object(runtime, "SCREENSHOT_MAX_BASE64_CHARS", budget), \
                        mock.patch.object(base64, "b64encode", wraps=base64.b64encode) as encode:
                    image = runtime.encoded_screenshot(pixbuf)
                self.assertEqual(image["mimeType"], "image/" + expected)
                self.assertLessEqual(len(image["data"]), budget)
                encode.assert_called_once_with(b"p" * length if expected == "png" else b"j")
                self.assertEqual(pixbuf.save_to_bufferv.call_args_list[0],
                                 mock.call("png", ["compression"], ["3"]))

    def test_all_overbudget_returns_smallest_raw_candidate(self):
        pixbuf = mock.Mock()
        pixbuf.save_to_bufferv.side_effect = [(True, b"p" * 13), (True, b"a" * 11),
                                             (True, b"b" * 8), (True, b"c" * 9),
                                             (True, b"d" * 10)]
        with mock.patch.object(runtime, "SCREENSHOT_MAX_BASE64_CHARS", 4), \
                mock.patch.object(base64, "b64encode", wraps=base64.b64encode) as encode:
            image = runtime.encoded_screenshot(pixbuf)
        self.assertEqual(image["encoding"], "jpeg-quality-70")
        self.assertGreater(len(image["data"]), 4)
        encode.assert_called_once_with(b"b" * 8)
        self.assertEqual([call.args[2] for call in pixbuf.save_to_bufferv.call_args_list],
                         [["3"], ["85"], ["70"], ["55"], ["40"]])

    def test_failed_formats_continue_and_total_failure_has_no_base64(self):
        pixbuf = mock.Mock()
        pixbuf.save_to_bufferv.side_effect = [RuntimeError("png unavailable"),
                                             (False, b""), (True, b"jpeg")]
        image = runtime.encoded_screenshot(pixbuf)
        self.assertEqual(image["encoding"], "jpeg-quality-70")
        pixbuf.save_to_bufferv.side_effect = RuntimeError("codec unavailable")
        with mock.patch.object(base64, "b64encode", wraps=base64.b64encode) as encode:
            self.assertIsNone(runtime.encoded_screenshot(pixbuf))
        encode.assert_not_called()

    @unittest.skipIf(runtime.GdkPixbuf is None, "GdkPixbuf unavailable")
    def test_real_png_and_jpeg_mime_decode_dimensions_and_lossless_pixels(self):
        from gi.repository import GLib

        width, height = 128, 96
        rng = random.Random(17)
        data = bytes(rng.getrandbits(8) for _ in range(width * height * 3))
        pixbuf = runtime.GdkPixbuf.Pixbuf.new_from_bytes(
            GLib.Bytes.new(data), runtime.GdkPixbuf.Colorspace.RGB,
            False, 8, width, height, width * 3)
        png_size = len(pixbuf.save_to_bufferv("png", ["compression"], ["3"])[1])
        candidates = [("png", "png-compression-3", png_size)]
        for quality in (85, 70, 55, 40):
            jpeg_size = len(pixbuf.save_to_bufferv("jpeg", ["quality"], [str(quality)])[1])
            self.assertGreater(candidates[-1][2], jpeg_size)
            candidates.append(("jpeg", "jpeg-quality-{}".format(quality), jpeg_size))
        for kind, encoding, size in candidates:
            with self.subTest(encoding=encoding):
                budget = 4 * ((size + 2) // 3)
                with mock.patch.object(runtime, "SCREENSHOT_MAX_BASE64_CHARS", budget):
                    image = runtime.encoded_screenshot(pixbuf)
                self.assertEqual(image["mimeType"], "image/" + kind)
                self.assertEqual(image["encoding"], encoding)
                self.assertLessEqual(len(image["data"]), budget)
                loader = runtime.GdkPixbuf.PixbufLoader.new_with_type(kind)
                loader.write(base64.b64decode(image["data"], validate=True))
                loader.close()
                decoded = loader.get_pixbuf()
                self.assertIn(image["mimeType"], loader.get_format().get_mime_types())
                self.assertEqual((decoded.get_width(), decoded.get_height()), (width, height))
                if kind == "png":
                    self.assertEqual(decoded.read_pixel_bytes().get_data(), data)


class GUICoordinateProvenanceTests(unittest.TestCase):
    def setUp(self):
        self.bounds = runtime.frame(0, 0, 100, 100)

    def test_coordinate_input_without_layout_provenance_is_rejected(self):
        snapshot = {
            "frameId": None,
            "screenshotPngBase64": "png",
        }
        with mock.patch.object(
            runtime, "desktop_bounds", return_value=self.bounds
        ), patch_gui_capture( return_value=snapshot
        ), mock.patch.object(runtime, "execute_gui_action") as execute:
            response = runtime.perform_gui(
                [{"type": "click", "x": 10, "y": 10}],
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
        a change. Only changes present in both post-action captures count.
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
                patch_gui_capture(
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
