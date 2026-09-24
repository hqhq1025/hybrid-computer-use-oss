"""Screen capture, pixel comparison and screenshot encoding."""

from .foundation import (
    Atspi,
    Gdk,
    GdkPixbuf,
    GUI_MODEL_MAX_HEIGHT,
    GUI_MODEL_MAX_WIDTH,
    PIXEL_DIFF_STRIDE,
    PIXEL_DIFF_THRESHOLD,
    SCREENSHOT_SCOPE_SCREEN,
    SCREENSHOT_MAX_BASE64_CHARS,
    base64,
    safe,
)

def capture_window_pixels(bounds):
    """Capture raw pixels of a screen region for before/after comparison.

    The result is not an image for the model. The accessibility tree misses
    whole classes of effects; saving a file in LibreOffice Impress, for
    example, can leave it byte-for-byte unchanged. Pixels are evidence
    independent of the tree: an unchanged tree with a changed screen means
    the effect is real but invisible to the tree, and only an unchanged tree
    together with an unchanged screen is strong evidence that nothing
    happened.

    Use ``read_pixel_bytes()`` rather than ``get_pixels()``: under PyGObject
    the latter returns truncated data (under 100 KB instead of 5.85 MB for a
    1850x1053 capture).
    """
    if Gdk is None or bounds is None:
        return None
    try:
        screen = Gdk.Screen.get_default()
        if screen is None:
            return None
        pixbuf = Gdk.pixbuf_get_from_window(
            screen.get_root_window(),
            int(round(bounds["x"])), int(round(bounds["y"])),
            max(1, int(round(bounds["width"]))), max(1, int(round(bounds["height"]))))
        if pixbuf is None:
            return None
        return {
            "data": pixbuf.read_pixel_bytes().get_data(),
            "stride": pixbuf.get_rowstride(),
            "channels": pixbuf.get_n_channels(),
            "width": pixbuf.get_width(),
            "height": pixbuf.get_height(),
        }
    except Exception:
        return None


def pixel_change(before, after):
    """Return the changed fraction and region between two captures.

    Returns None when the captures cannot be compared, rather than reporting
    a comparison that did not happen.
    """
    if not before or not after:
        return None
    if (before["width"] != after["width"] or before["height"] != after["height"]
            or before["stride"] != after["stride"]):
        # A resize is itself a change but rules out a pixel-by-pixel
        # comparison; report it as such.
        return {"resized": True}
    a, b = before["data"], after["data"]
    stride, channels = before["stride"], before["channels"]
    width, height = before["width"], before["height"]
    changed = total = 0
    min_x = min_y = 1 << 30
    max_x = max_y = -1
    try:
        for y in range(0, height, PIXEL_DIFF_STRIDE):
            row = y * stride
            for x in range(0, width, PIXEL_DIFF_STRIDE):
                offset = row + x * channels
                total += 1
                delta = (abs(a[offset] - b[offset])
                         + abs(a[offset + 1] - b[offset + 1])
                         + abs(a[offset + 2] - b[offset + 2]))
                if delta > PIXEL_DIFF_THRESHOLD:
                    changed += 1
                    min_x = min(min_x, x)
                    min_y = min(min_y, y)
                    max_x = max(max_x, x)
                    max_y = max(max_y, y)
    except Exception:
        return None
    if total == 0:
        return None
    result = {"resized": False, "changed": changed, "total": total,
              "percent": 100.0 * changed / total}
    if max_x >= 0:
        result["box"] = (min_x, min_y, max_x - min_x + PIXEL_DIFF_STRIDE,
                         max_y - min_y + PIXEL_DIFF_STRIDE)
    return result


def pixel_buffers_identical(before, after):
    """Compare complete captured frames without sampling or colour tolerance."""
    if not before or not after:
        return None
    geometry = ("width", "height", "stride", "channels")
    if any(before.get(key) != after.get(key) for key in geometry):
        return False
    try:
        return before["data"] == after["data"]
    except Exception:
        return None


def persistent_pixel_change(before, after_one, after_two):
    """Count only changes present in both post-action captures.

    This is the desktop equivalent of Playwright's "capture until two
    consecutive screenshots match, then compare". Two pre-action captures
    cannot measure noise: they are milliseconds apart, so a 1 Hz text caret
    never blinks between them, the noise floor reads as zero, and a no-op
    keystroke is reported as a screen change located exactly at the caret. A
    blinking element differs between the two post-action captures (on in one,
    off in the other), so requiring both to differ from the pre-action capture
    filters it out, while a real UI change appears in both.

    Building the snapshot fills the gap between the two captures (resolve_app
    alone takes 0.15-0.3 s), so the extra capture (about 23 ms) adds almost
    no latency.
    """
    if not before or not after_one or not after_two:
        return None
    frames = (before, after_one, after_two)
    if len({(f["width"], f["height"], f["stride"]) for f in frames}) != 1:
        return {"resized": True}
    a, b, c = before["data"], after_one["data"], after_two["data"]
    stride, channels = before["stride"], before["channels"]
    width, height = before["width"], before["height"]
    changed = total = 0
    min_x = min_y = 1 << 30
    max_x = max_y = -1
    try:
        for y in range(0, height, PIXEL_DIFF_STRIDE):
            row = y * stride
            for x in range(0, width, PIXEL_DIFF_STRIDE):
                offset = row + x * channels
                total += 1
                base = (a[offset], a[offset + 1], a[offset + 2])
                one = (abs(base[0] - b[offset]) + abs(base[1] - b[offset + 1])
                       + abs(base[2] - b[offset + 2])) > PIXEL_DIFF_THRESHOLD
                if not one:
                    continue
                two = (abs(base[0] - c[offset]) + abs(base[1] - c[offset + 1])
                       + abs(base[2] - c[offset + 2])) > PIXEL_DIFF_THRESHOLD
                if not two:
                    # Changed in only one capture: flicker, not an effect.
                    continue
                changed += 1
                min_x = min(min_x, x)
                min_y = min(min_y, y)
                max_x = max(max_x, x)
                max_y = max(max_y, y)
    except Exception:
        return None
    if total == 0:
        return None
    result = {"resized": False, "changed": changed, "total": total,
              "percent": 100.0 * changed / total}
    if max_x >= 0:
        result["box"] = (min_x, min_y, max_x - min_x + PIXEL_DIFF_STRIDE,
                         max_y - min_y + PIXEL_DIFF_STRIDE)
    return result


def pixel_change_note(change, screenshot_scope="window"):
    """Describe a pixel comparison for the model, prefixed with `[pixels]`."""
    if change is None:
        return None
    full_screen = screenshot_scope == SCREENSHOT_SCOPE_SCREEN
    subject = "full desktop" if full_screen else "window"
    coordinate_space = "full-desktop image" if full_screen else "window-relative"
    if change.get("resized"):
        return (
            "[pixels] geometry_changed=true: The {} dimensions changed between "
            "captures, so a pixel-by-pixel comparison was not possible. Cause, input "
            "delivery, target identity, and semantic effect are unknown."
        ).format(subject)
    if change["changed"] == 0:
        return (
            "[pixels] sampled_pixel_change=none: No sampled pixel difference persisted "
            "between the pre-action {} and both post-action captures. This does not "
            "prove that no application or file state changed."
        ).format(subject)
    box = change.get("box")
    where = ""
    if box:
        where = " Changes are concentrated in {{{},{},{},{}}} ({} pixels).".format(
            *box, coordinate_space)
    strength = "tiny" if change["percent"] <= 0.1 else "material"
    return (
        "[pixels] sampled_pixel_change={}: A sampled pixel difference persisted between "
        "the pre-action {} and both post-action captures ({:.2f}% of sample points; "
        "{} changed).{} This is visual evidence only; input delivery, target identity, "
        "and task effect are unverified."
    ).format(strength, subject, change["percent"], change["changed"], where)


def encoded_screenshot(pixbuf):
    candidates = []
    formats = [
        ("png", ["compression"], ["9"], "image/png"),
        ("jpeg", ["quality"], ["85"], "image/jpeg"),
        ("jpeg", ["quality"], ["70"], "image/jpeg"),
        ("jpeg", ["quality"], ["55"], "image/jpeg"),
        ("jpeg", ["quality"], ["40"], "image/jpeg"),
    ]
    for image_format, keys, values, mime_type in formats:
        try:
            ok, data = pixbuf.save_to_bufferv(image_format, keys, values)
        except Exception:
            continue
        if not ok:
            continue
        encoded = base64.b64encode(bytes(data)).decode("ascii")
        candidates.append({
            "data": encoded,
            "mimeType": mime_type,
            "encoding": image_format + (
                "-quality-" + values[0] if image_format == "jpeg"
                else "-compression-" + values[0]
            ),
        })
        if len(encoded) <= SCREENSHOT_MAX_BASE64_CHARS:
            return candidates[-1]
    if not candidates:
        return None
    return min(candidates, key=lambda candidate: len(candidate["data"]))


def capture_window_image(bounds):
    if Gdk is None or bounds is None:
        return None
    try:
        screen = Gdk.Screen.get_default()
        if screen is None:
            return None
        root = screen.get_root_window()
        pixbuf = Gdk.pixbuf_get_from_window(
            root,
            int(round(bounds["x"])),
            int(round(bounds["y"])),
            max(1, int(round(bounds["width"]))),
            max(1, int(round(bounds["height"]))),
        )
        if pixbuf is None:
            return None
        if pixbuf_looks_black(pixbuf):
            return None
        return encoded_screenshot(pixbuf)
    except Exception:
        return None


def gui_model_dimensions(bounds):
    """Return the exact pixel dimensions delivered to the model."""
    if bounds is None:
        return None
    native_width = max(1, int(round(bounds["width"])))
    native_height = max(1, int(round(bounds["height"])))
    scale = min(
        1.0,
        float(GUI_MODEL_MAX_WIDTH) / native_width,
        float(GUI_MODEL_MAX_HEIGHT) / native_height,
    )
    return {
        "width": max(1, int(round(native_width * scale))),
        "height": max(1, int(round(native_height * scale))),
        "scale_x": scale,
        "scale_y": scale,
    }


def capture_gui_image(bounds):
    """Capture a full desktop image in the public GUI coordinate space."""
    if Gdk is None or GdkPixbuf is None or bounds is None:
        return None
    try:
        screen = Gdk.Screen.get_default()
        if screen is None:
            return None
        pixbuf = Gdk.pixbuf_get_from_window(
            screen.get_root_window(),
            int(round(bounds["x"])),
            int(round(bounds["y"])),
            max(1, int(round(bounds["width"]))),
            max(1, int(round(bounds["height"]))),
        )
        if pixbuf is None or pixbuf_looks_black(pixbuf):
            return None
        dimensions = gui_model_dimensions(bounds)
        if (
            pixbuf.get_width() != dimensions["width"]
            or pixbuf.get_height() != dimensions["height"]
        ):
            pixbuf = pixbuf.scale_simple(
                dimensions["width"],
                dimensions["height"],
                GdkPixbuf.InterpType.BILINEAR,
            )
            if pixbuf is None:
                return None
        return encoded_screenshot(pixbuf)
    except Exception:
        return None


def capture_window_png(bounds):
    """Return only the base64 image data from capture_window_image()."""
    image = capture_window_image(bounds)
    return image.get("data") if image else None


def pixbuf_looks_black(pixbuf):
    try:
        pixels = pixbuf.get_pixels()
        channels = pixbuf.get_n_channels()
        rowstride = pixbuf.get_rowstride()
        width = pixbuf.get_width()
        height = pixbuf.get_height()
        if width <= 0 or height <= 0 or channels < 3:
            return True
        step_x = max(1, width // 16)
        step_y = max(1, height // 16)
        checked = 0
        for y in range(0, height, step_y):
            row = y * rowstride
            for x in range(0, width, step_x):
                offset = row + (x * channels)
                if (
                    pixels[offset] > 3
                    or pixels[offset + 1] > 3
                    or pixels[offset + 2] > 3
                ):
                    return False
                checked += 1
        return checked > 0
    except Exception:
        return False
