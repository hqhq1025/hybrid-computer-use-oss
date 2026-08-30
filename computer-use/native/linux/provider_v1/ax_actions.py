"""Linux provider module: ax actions."""

from .foundation import (
    Atspi,
    Gdk,
    MAX_CHILD_FANOUT,
    MAX_SCROLL_PAGES,
    MENU_ROLES,
    child_at,
    child_count,
    has_editable_text_iface,
    has_text_iface,
    math,
    node_role,
    os,
    safe,
    state_contains,
    time,
)

from .actionability import (
    do_action_by_index,
)

from .input_primitives import (
    emit_keyboard_event,
    emit_mouse_event,
    require_window_focus,
)

from .element_resolver import (
    iter_all,
)

KEY_ALIASES = {
    "return": "Return",
    "enter": "Return",
    "tab": "Tab",
    "escape": "Escape",
    "esc": "Escape",
    "backspace": "BackSpace",
    "back_space": "BackSpace",
    "delete": "Delete",
    "space": "space",
    "left": "Left",
    "up": "Up",
    "right": "Right",
    "down": "Down",
    "arrowleft": "Left",
    "arrowup": "Up",
    "arrowright": "Right",
    "arrowdown": "Down",
    "home": "Home",
    "end": "End",
    "pageup": "Page_Up",
    "page_up": "Page_Up",
    "prior": "Page_Up",
    "pagedown": "Page_Down",
    "page_down": "Page_Down",
    "next": "Page_Down",
    "slash": "slash",
    "forward_slash": "slash",
}

MODIFIER_KEYS = {
    "ctrl": "Control_L",
    "control": "Control_L",
    "control_l": "Control_L",
    "ctrl_l": "Control_L",
    "shift": "Shift_L",
    "shift_l": "Shift_L",
    "alt": "Alt_L",
    "alt_l": "Alt_L",
    "super": "Super_L",
    "super_l": "Super_L",
    "win": "Super_L",
    "cmd": "Super_L",
}


def normalize_key_expression(key):
    text = str(key or "").strip()
    if not text.startswith("<"):
        return text
    modifiers = []
    while text.startswith("<"):
        end = text.find(">")
        if end <= 1:
            break
        modifiers.append(text[1:end])
        text = text[end + 1:]
    if not text:
        return str(key or "").strip()
    return "+".join(modifiers + [text])


def keycode(name):
    """把键名解析成 X11 hardware keycode。

    Atspi.generate_keyboard_event(keyval, keystring, synth_type) 在
    PRESS / RELEASE / PRESSRELEASE 三种模式下，第一个参数要的是
    **hardware keycode**；只有 SYM 模式才接受 keysym。

    传 keysym 进去不会报错，而是被截断到低 8 位，发出完全不相干的键：

        Escape    keysym 65307 -> 截断 27  -> 实际发出 'r'
        Return    keysym 65293 -> 截断 13  -> 实际发出 '4'
        Delete    keysym 65535 -> 截断 255 -> 实际发出 XF86RFKill
        Control_L keysym 65507 -> 截断 227 -> 修饰键根本没按下

    这是静默注入错误动作，比直接报错危险，所以这里解析失败一律抛异常。
    """
    if Gdk is None:
        raise RuntimeError("Gdk unavailable; cannot resolve keycode for: " + name)
    sym = Gdk.keyval_from_name(name)
    if not sym and len(name) == 1:
        sym = ord(name)
    if not sym:
        raise RuntimeError("Unsupported key: " + name)
    keymap = Gdk.Keymap.get_default()
    ok, entries = keymap.get_entries_for_keyval(sym)
    if not ok or not entries:
        raise RuntimeError("No hardware keycode mapped for key: " + name)
    return int(entries[0].keycode)


def parse_key(key):
    """把按键字符串解析成 (修饰键 keycode 列表, 规范化后的主键)，不发送任何事件。

    解析必须能独立于发送先做一遍：夺焦点是有副作用的（会打断用户），
    不该为了一个根本拼错的按键先把窗口抢过来、再报参数错误。
    """
    normalized_expression = normalize_key_expression(key)
    parts = [part for part in normalized_expression.split("+") if part]
    if not parts:
        raise RuntimeError("Unsupported key: " + str(key))
    main = parts[-1]
    codes = []
    for modifier in parts[:-1]:
        name = MODIFIER_KEYS.get(modifier.lower())
        if name is None:
            # 原实现是静默 continue，会让 "ctrl+shft+s" 这类拼写错误
            # 悄悄退化成孤立的 "s"，行为诡异且极难排查。
            raise RuntimeError(
                "Unsupported modifier: {}. Use CTRL, SHIFT, ALT, or SUPER; "
                "Control_L/Ctrl_L and GTK forms such as <Control>s are also accepted."
                .format(modifier))
        codes.append(keycode(name))
    normalized = KEY_ALIASES.get(main.lower(), main)
    # 单字符键只有在**无修饰键**时才能走 STRING。STRING 是"插入这段文本"
    # 的语义，会绕过已经 PRESS 的修饰键状态，使 ctrl+a 退化成输入字面 'a'。
    if not (len(normalized) == 1 and not codes):
        keycode(normalized)  # 提前确认主键可解析，失败就在这里报错
    return codes, normalized


def send_key(key):
    codes, normalized = parse_key(key)
    pressed = []
    emitted = False
    primary_error = None
    try:
        for code in codes:
            emitted = emit_keyboard_event(
                code,
                None,
                Atspi.KeySynthType.PRESS,
                "modifier press for {!r}".format(key),
                emitted,
            )
            pressed.append(code)
        if len(normalized) == 1 and not pressed:
            emitted = emit_keyboard_event(
                0,
                normalized,
                Atspi.KeySynthType.STRING,
                "text key {!r}".format(normalized),
                emitted,
            )
        else:
            emitted = emit_keyboard_event(
                keycode(normalized),
                None,
                Atspi.KeySynthType.PRESSRELEASE,
                "key {!r}".format(normalized),
                emitted,
            )
    except Exception as exc:
        primary_error = exc

    # Release every pressed modifier even if one release fails. Stopping after
    # the first failed release can leave the remaining modifiers stuck.
    release_error = None
    for code in reversed(pressed):
        try:
            emitted = emit_keyboard_event(
                code,
                None,
                Atspi.KeySynthType.RELEASE,
                "modifier release for {!r}".format(key),
                emitted,
            )
        except Exception as exc:
            if release_error is None:
                release_error = exc
    if primary_error is not None:
        raise primary_error
    if release_error is not None:
        raise release_error


def send_text(text):
    emit_keyboard_event(
        0,
        str(text),
        Atspi.KeySynthType.STRING,
        "text payload",
        False,
    )


def find_editable_text(root):
    """挑出真正该接收文本的可编辑控件。

    不能只看 EditableText 接口存不存在就取树序第一个。gedit 这类应用里，
    树序靠前的往往是隐藏的占位控件：它实现了 EditableText 接口，却没有
    EDITABLE 状态。对它调 Atspi.EditableText.insert_text() **照样返回 True**，
    但字符数不变——文本静默写丢，工具却报成功。

    因此这里同时要求 EDITABLE 与 FOCUSED，并且只接受唯一候选。没有明确
    焦点时不按可见性或树序猜测，交给标记为 unverified 的键盘路径。
    """
    focused = []
    for node in iter_all(root):
        if not (has_editable_text_iface(node) and has_text_iface(node)):
            continue
        if not state_contains(node, Atspi.StateType.EDITABLE):
            continue
        if state_contains(node, Atspi.StateType.FOCUSED):
            focused.append(node)
    if len(focused) == 1:
        return focused[0]
    return None


def read_all_text(text_iface):
    """读出控件当前的全部文本；读不到返回 None（用来区分"空"和"读不到"）。"""
    if text_iface is None:
        return None
    count = safe(lambda: Atspi.Text.get_character_count(text_iface))
    if count is None:
        return None
    count = int(count)
    if count <= 0:
        return ""
    return safe(lambda: Atspi.Text.get_text(text_iface, 0, count))


def text_insertion_point(text_iface):
    """决定文本该插到哪里，返回 (offset, selection)。

    优先级：非空选区起点 > caret > 末尾追加。selection 非空时调用方要先把它
    删掉——真的在键盘上打字会覆盖选中内容，`type_text` 应该保持一致。

    只有读不到 caret 时才退回末尾追加（部分控件不实现 caret 查询）。
    """
    count = safe(lambda: Atspi.Text.get_character_count(text_iface))
    count = int(count) if count is not None else 0

    selections = int(safe(lambda: Atspi.Text.get_n_selections(text_iface), 0) or 0)
    for index in range(max(selections, 0)):
        span = safe(lambda index=index: Atspi.Text.get_selection(text_iface, index))
        if span is None:
            continue
        start = int(safe(lambda: span.start_offset, 0) or 0)
        end = int(safe(lambda: span.end_offset, 0) or 0)
        if end > start:
            return start, (start, end)

    caret = safe(lambda: Atspi.Text.get_caret_offset(text_iface))
    if caret is not None and 0 <= int(caret) <= count:
        return int(caret), None
    return count, None


def insert_text_detail(root, text):
    """通过 AT-SPI 直写文本，返回 (是否落地, 写前字符数, 写后字符数)。

    调用方需要这三个值来如实报告"写进去了、并且确认过"，而不是只说一句成功。
    """
    node = find_editable_text(root)
    status, before, after, _emitted = insert_text_outcome_on_node(node, text)
    if status == "effect_unknown":
        raise RuntimeError(
            "AT-SPI text write may have deleted the selected text, changed the "
            "control to unexpected content, or lost readback; refusing automatic "
            "keyboard fallback"
        )
    return status == "verified", before, after


def insert_text_detail_on_node(node, text):
    status, before, after, _emitted = insert_text_outcome_on_node(node, text)
    if status == "effect_unknown":
        raise RuntimeError(
            "AT-SPI text write may have deleted the selected text, changed the "
            "control to unexpected content, or lost readback; refusing automatic "
            "keyboard fallback"
        )
    return status == "verified", before, after


def insert_text_outcome_on_node(node, text):
    """Write text and distinguish verified, confirmed-no-change, and unknown."""
    if node is None:
        return "no_change", 0, 0, False
    editable = safe(node.get_editable_text_iface)
    text_iface = safe(node.get_text_iface)
    if editable is None or text_iface is None:
        return "no_change", 0, 0, False
    payload = str(text)
    if not payload:
        count = int(safe(lambda: Atspi.Text.get_character_count(text_iface), 0) or 0)
        return "verified", count, count, False

    original_text = read_all_text(text_iface)
    if original_text is None:
        return "no_change", 0, 0, False
    offset, selection = text_insertion_point(text_iface)
    selection_deleted = False
    if selection is not None:
        accepted = safe(
            lambda: Atspi.EditableText.delete_text(
                editable, selection[0], selection[1]),
            False,
        )
        after_delete = read_all_text(text_iface)
        expected_after_delete = (
            original_text[:selection[0]] + original_text[selection[1]:]
        )
        if after_delete is None and accepted:
            return "effect_unknown", len(original_text), len(original_text), True
        if after_delete != expected_after_delete:
            if after_delete is not None and after_delete != original_text:
                return (
                    "effect_unknown",
                    len(original_text),
                    len(after_delete),
                    True,
                )
            return "no_change", len(original_text), (
                len(after_delete) if after_delete is not None else len(original_text)
            ), False
        if not accepted and after_delete == original_text:
            return "no_change", len(original_text), len(original_text), False
        selection_deleted = after_delete != original_text

    # 删掉选区会改变内容，精确判定的基准必须在删除之后再读一次。
    before_text = read_all_text(text_iface)
    if before_text is None:
        return (
            "effect_unknown" if selection_deleted else "no_change",
            len(original_text),
            len(original_text),
            selection_deleted,
        )
    before = len(before_text)
    offset = max(0, min(offset, before))

    if not safe(
        lambda: Atspi.EditableText.insert_text(editable, offset, payload, len(payload)),
        False,
    ):
        if selection_deleted:
            return "effect_unknown", before, before, True
        return "no_change", before, before, False
    # insert_text 的返回值和字符数都不可信：错误控件可以返回 True，错误文本
    # 也可以保持相同长度。回读完整内容并核对精确插入结果。
    after_text = read_all_text(text_iface)
    if after_text is None:
        return "effect_unknown", before, before, True
    expected = before_text[:offset] + payload + before_text[offset:]
    if after_text == expected:
        return "verified", before, len(after_text), True
    if after_text != before_text:
        return "effect_unknown", before, len(after_text), True
    return "no_change", before, len(after_text), True


def insert_text(root, text):
    written, _, _ = insert_text_detail(root, text)
    return written


def focus_target_element(window, element, element_record, what, timeout=1.0):
    """Focus one resolved element and verify that exact target owns focus."""
    if element is None or element_record is None:
        raise RuntimeError("{} requires a resolvable target ref".format(what))
    require_window_focus(window, what)

    def target_is_focused():
        return state_contains(element, Atspi.StateType.FOCUSED)

    component = safe(element.get_component_iface)
    if component is None:
        if target_is_focused():
            return
        raise RuntimeError(
            "{} could not focus the requested element; no keyboard input was "
            "emitted".format(what)
        )
    accepted = safe(lambda: Atspi.Component.grab_focus(component), False)
    if not accepted and target_is_focused():
        return
    if not accepted:
        raise RuntimeError(
            "{} could not focus the requested element; no keyboard input was "
            "emitted".format(what)
        )
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if target_is_focused():
            return
        time.sleep(0.02)
    raise RuntimeError(
        "{} requested focus for the element, but that exact target did not report "
        "focus within {:.1f}s; no keyboard input was emitted".format(what, timeout)
    )


def set_element_value_outcome(node, value):
    """Set a value without converting unavailable readback into safe failure."""
    value_iface = safe(node.get_value_iface) if node is not None else None
    if value_iface is not None:
        try:
            numeric = float(value)
        except (TypeError, ValueError):
            numeric = None
        if numeric is not None:
            before = safe(lambda: Atspi.Value.get_current_value(value_iface))
            try:
                accepted = bool(Atspi.Value.set_current_value(value_iface, numeric))
            except Exception:
                accepted = False
            if accepted:
                after = safe(lambda: Atspi.Value.get_current_value(value_iface))
                if after is None:
                    return "effect_unknown"
                if math.isclose(float(after), numeric, rel_tol=1e-9, abs_tol=1e-9):
                    return "verified"
                if before is not None and not math.isclose(
                        float(after), float(before), rel_tol=1e-9, abs_tol=1e-9):
                    return "effect_unknown"
    if node is not None and has_editable_text_iface(node):
        editable = safe(node.get_editable_text_iface)
        if editable is not None:
            payload = str(value)
            text_iface = safe(node.get_text_iface)
            before = read_all_text(text_iface)
            if not safe(
                lambda: Atspi.EditableText.set_text_contents(editable, payload),
                False,
            ):
                return "no_change"
            # set_text_contents 和 insert_text 一样，对写不进去的控件同样返回
            # True。只有精确回读到请求值才算确认；变成任意其他值不能被描述成
            # "the control now holds it"。
            after = read_all_text(text_iface)
            if after is None:
                return "effect_unknown"
            if after == payload:
                return "verified"
            if after != before:
                return "effect_unknown"
            return "no_change"
    return "no_change"


def set_element_value(node, value):
    outcome = set_element_value_outcome(node, value)
    if outcome == "effect_unknown":
        raise RuntimeError(
            "AT-SPI value write indicates the control changed, but the result was "
            "unexpected or exact readback was unavailable; refusing automatic "
            "keyboard fallback"
        )
    return outcome == "verified"


# 应用解析的重试次数与间隔。见 resolve_app() 的说明：应用忙于重建窗口时，
# 枚举可能瞬时读不到，而"读不到"被当成"不存在"会让 agent 走上完全错误的分支。
RESOLVE_APP_ATTEMPTS = int(os.environ.get("COMPUTER_USE_RESOLVE_ATTEMPTS", "3"))
RESOLVE_APP_RETRY_SECONDS = float(
    os.environ.get("COMPUTER_USE_RESOLVE_RETRY", "0.3")
)


# 一"页"折算成多少次滚轮。滚轮一格通常是 3 行，这里取 5 格 ≈ 15 行。
# 这是**近似**，真实距离取决于应用自己的滚动设置——所以 Note 里要说是近似，
# 不能让 agent 以为 pages=1 精确等于一次 Page_Down。
WHEEL_CLICKS_PER_PAGE = 5


def scroll_element(direction, pages, point=None):
    """滚动。有元素坐标就用滚轮按位置滚，没有就退回往焦点发 Page 键。

    此前这里**只有**后一条路：element_index 是必填参数却完全不参与，键盘
    事件落到当前焦点上。工具描述里如实标注了这一点——但如实说明一个缺陷
    不等于修好它。

    滚轮是真的按位置生效，实测（gedit 打开 600 行文件，在文本区中心发 6 次
    b5c）：
        对照组（什么都不做）  文本区 0% 像素变化
        实验组（滚轮 x6）     文本区 23% 像素变化

    要说清它**滚的是谁**：滚轮作用在指针下方的可滚动祖先上，不一定是你指定
    的那个元素本身。这正是"在这个位置滚"的语义，但和"滚动这个控件"不完全
    等同，Note 里要讲明白。

    横向滚动仍然走 Left/Right 键：滚轮的横向按钮（b6/b7）我没有实测过，
    不测就不发——这个仓库里没测过的路径不该以"应该能work"的名义上线。

    返回实际走的路线，让 Note 说实话。
    """
    pages = float(pages or 1)
    if not math.isfinite(pages) or pages <= 0 or pages > MAX_SCROLL_PAGES:
        raise RuntimeError(
            "pages must be greater than 0 and at most {}".format(MAX_SCROLL_PAGES))
    repeat = max(1, int(math.ceil(pages)))
    horizontal = direction in ("left", "right")
    if point is not None and not horizontal:
        x, y = point
        emitted = emit_mouse_event(x, y, "abs", False)
        event = "b4c" if direction == "up" else "b5c"
        for _ in range(repeat * WHEEL_CLICKS_PER_PAGE):
            emitted = emit_mouse_event(x, y, event, emitted)
            time.sleep(0.03)
        return "wheel"

    key = "Page_Down"
    if direction == "up":
        key = "Page_Up"
    elif direction == "left":
        key = "Left"
    elif direction == "right":
        key = "Right"
    for _ in range(repeat):
        send_key(key)
        time.sleep(0.04)
    return "keys"


# 每条动作 Note 都带**两个正交的标签**，因为这是两件不同的事，
# 混成一个会同时丢掉两边的信息。
#
# 【寻址轴】目标是**怎么定位到的**——决定了该拿什么去验证它：
#   [a11y]      通过无障碍树的 element_index 定位
#   [gui]       通过屏幕坐标定位，树完全没有参与
#   [keyboard]  通过当前输入焦点定位，两条通道都没参与
#
# 【执行轴】定位好之后**用什么把动作发出去**：
#   [semantic]   AT-SPI 语义动作 / 可编辑文本 API
#   [synthesis]  XTEST 全局合成
#
# 两个轴会交叉。最要紧的一格是 `[a11y][synthesis]`：**用树给的坐标去合成点击**
# ——寻址是 a11y 的，执行是合成的。以前只有执行轴一个标签，这一格会被记成
# 纯合成，于是"agent 压根没用 a11y"和"用了但语义动作失效后回落"就分不开了。
