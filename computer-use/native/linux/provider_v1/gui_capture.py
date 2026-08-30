"""Linux provider module: gui capture."""

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
    """抓指定屏幕区域的原始像素，用于**前后比对**（不是给 agent 看的图）。

    存在的理由：树接不住整类效果。实测 LibreOffice Impress 上，右对齐生效、
    文件保存成功，**a11y 树都字节不变**，于是两个真的生效了的动作被判成
    "送达但被忽略"，agent 多花两步去自证（占那次 12 步的 17%）。

    像素是**独立于树**的第三种判据：树没变而屏幕变了，说明效果真实存在、
    只是树看不见；树没变屏幕也没变，才是"确实什么都没发生"的强证据。

    注意 `read_pixel_bytes()` 而不是 `get_pixels()`——后者在 PyGObject 上
    返回的是截断数据（实测 1850x1053 只拿到不到 100KB，正确值 5.85MB）。
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
    """比出变化比例与变化区域；比不了就返回 None（**不假装比过**）。"""
    if not before or not after:
        return None
    if (before["width"] != after["width"] or before["height"] != after["height"]
            or before["stride"] != after["stride"]):
        # 窗口尺寸变了本身就是一种变化，但无法逐点比对——如实说明。
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


# 动作前后要比对的文本属性。**只挑格式类**，不是全量——全量里大半是
# writing-mode / variant 这种恒定值，比了也没信息。
#
# 这条能力是查 Playwright 时反推出来的：它的 aria snapshot 只渲染一小撮语义属性
# （[checked] [level=1]），而"查任意样式属性"是 `toHaveCSS` 这类**断言**的事——
# **快照是给 agent 看的摘要，断言是精确查询，两者不必是同一套字段。**
#
# 顺着这个思路回头查 AT-SPI，才发现 `Atspi.Text.get_default_attributes()`
# 一直都能读到 justification / size / weight / fg-color 这些东西。
# 我此前写进文档的"改段落对齐后 a11y 树字节不变"**是错的**——
# 树不变是因为**我们没渲染这些字段**，不是信息不存在。
# 实测取一次 0.10–0.41ms/节点（对比 record_for 在 GIMP 上是 8.45ms），近乎免费。
def persistent_pixel_change(before, after_one, after_two):
    """只认**在两张动作后画面里都存在**的变化。

    这是 Playwright 那条"反复截图直到连续两张一致再比"在桌面上的等价物。
    它解决的是我第一版栽的跟头：动作前连抓两张测噪声，但两张只隔几毫秒，
    1Hz 的文本光标根本没来得及闪，噪声底测出来是 0——于是**空操作的
    第二次 Ctrl+L 也被判成"屏幕变了"**，变化区域正是那个 8x16 的光标。

    闪烁的东西在两张动作后画面里状态不同（一亮一灭），所以"两张都相对
    动作前有差异"这个条件会把它滤掉；真正的界面变化则两张都在。

    两张之间的间隔由**建快照的时间**填上（resolve_app 自己就要 0.15–0.3s），
    所以除了多抓一张图（23ms）几乎不额外增加延迟。
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
                    # 只在其中一张里变了——闪烁，不是效果。
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
    """把比对结果写成一句给 agent 看的话。`[pixels]` 前缀便于机器识别。"""
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
    """Compatibility wrapper for older tests and direct callers."""
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
