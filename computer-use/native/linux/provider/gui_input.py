"""Validation and AT-SPI synthesis of GUI pointer and keyboard actions."""

import math
import time
from .foundation import (
    Atspi,
    GUI_ACTION_FIELDS,
    GUI_ACTION_TYPES,
    GUI_KEYBOARD_ACTION_TYPES,
    GUI_KEYBOARD_MICRO_WAIT_MAX_SECONDS,
    GUI_MAX_DRAG_POINTS,
    GUI_MAX_KEYS,
    GUI_MAX_KEY_LENGTH,
    GUI_MAX_SCROLL_DELTA,
    GUI_MAX_TEXT_LENGTH,
    GUI_POINTER_ACTION_TYPES,
    MAX_CLICK_COUNT,
)



from .actionability import (
    screen_absolute_point,
)
from .input_primitives import (
    best_effort_mouse_release,
    emit_keyboard_event,
    emit_mouse_event,
)

def mouse_button_events(button):
    normalized = (button or "left").lower()
    if normalized == "right":
        return "b3p", "b3r"
    if normalized == "wheel":
        return "b2p", "b2r"
    if normalized == "back":
        return "b8p", "b8r"
    if normalized == "forward":
        return "b9p", "b9r"
    return "b1p", "b1r"






















def send_mouse_click(x, y, button, count):
    down, up = mouse_button_events(button)
    repeat = max(1, int(count or 1))
    if repeat > MAX_CLICK_COUNT:
        raise RuntimeError(
            "click_count must be between 1 and {}".format(MAX_CLICK_COUNT))
    for _ in range(repeat):
        emitted = False
        pressed = False
        try:
            emitted = emit_mouse_event(x, y, "abs", emitted)
            emitted = emit_mouse_event(x, y, down, emitted)
            pressed = True
            time.sleep(0.035)
            emitted = emit_mouse_event(x, y, up, emitted)
            pressed = False
        finally:
            if pressed:
                best_effort_mouse_release(x, y, up)
        time.sleep(0.05)


def send_drag(from_x, from_y, to_x, to_y):
    emitted = False
    pressed = False
    try:
        emitted = emit_mouse_event(from_x, from_y, "abs", emitted)
        emitted = emit_mouse_event(from_x, from_y, "b1p", emitted)
        pressed = True
        steps = 12
        for step in range(1, steps + 1):
            x = from_x + ((to_x - from_x) * step / steps)
            y = from_y + ((to_y - from_y) * step / steps)
            emitted = emit_mouse_event(x, y, "abs", emitted)
            time.sleep(0.02)
        emitted = emit_mouse_event(to_x, to_y, "b1r", emitted)
        pressed = False
    finally:
        if pressed:
            best_effort_mouse_release(to_x, to_y, "b1r")


def send_drag_path(points):
    if len(points) < 2:
        raise RuntimeError("gui drag requires path with at least two points")
    start_x, start_y = points[0]
    emitted = False
    pressed = False
    try:
        emitted = emit_mouse_event(start_x, start_y, "abs", emitted)
        emitted = emit_mouse_event(start_x, start_y, "b1p", emitted)
        pressed = True
        for x, y in points[1:]:
            emitted = emit_mouse_event(x, y, "abs", emitted)
            time.sleep(0.02)
        end_x, end_y = points[-1]
        emitted = emit_mouse_event(end_x, end_y, "b1r", emitted)
        pressed = False
    finally:
        if pressed:
            end_x, end_y = points[-1]
            best_effort_mouse_release(end_x, end_y, "b1r")


def send_gui_scroll(scroll_x, scroll_y, x, y):
    dx = int(round(scroll_x))
    dy = int(round(scroll_y))
    emitted = emit_mouse_event(x, y, "abs", False)
    # Computer scroll values are deltas, not raw X11 wheel-click counts.
    vertical_clicks = max(1, abs(round(dy / 100))) if dy else 0
    horizontal_clicks = max(1, abs(round(dx / 100))) if dx else 0
    for _ in range(vertical_clicks):
        emitted = emit_mouse_event(
            x, y, "b5c" if dy > 0 else "b4c", emitted)
    for _ in range(horizontal_clicks):
        emitted = emit_mouse_event(
            x, y, "b6c" if dx > 0 else "b7c", emitted)


def finite_gui_number(value, label):
    try:
        number = float(value)
    except (TypeError, ValueError):
        raise RuntimeError("{} must be a finite number".format(label))
    if not math.isfinite(number):
        raise RuntimeError("{} must be a finite number".format(label))
    return number


def validate_mouse_modifier_keys(keys):
    from .ax_actions import MODIFIER_KEYS, keycode, normalize_key_expression

    if keys is None:
        return []
    if not isinstance(keys, list):
        raise RuntimeError("gui mouse action keys must be an array")
    if len(keys) > GUI_MAX_KEYS:
        raise RuntimeError(
            "gui mouse action accepts at most {} modifier keys".format(
                GUI_MAX_KEYS))
    normalized = []
    for key in keys:
        if not isinstance(key, str) or not key.strip():
            raise RuntimeError(
                "gui mouse action keys must contain non-empty modifier names")
        token = normalize_key_expression(key).strip().lower()
        if "+" in token or token not in MODIFIER_KEYS:
            raise RuntimeError(
                "gui mouse action keys accept only CTRL, SHIFT, ALT, or SUPER")
        keycode(MODIFIER_KEYS[token])
        normalized.append(token.upper())
    return normalized


def with_held_mouse_modifiers(keys, callback):
    from .ax_actions import MODIFIER_KEYS, keycode

    codes = [keycode(MODIFIER_KEYS[key.lower()]) for key in (keys or [])]
    pressed = []
    emitted = False
    primary_error = None
    try:
        for code in codes:
            emitted = emit_keyboard_event(
                code, None, Atspi.KeySynthType.PRESS,
                "held mouse modifier press", emitted)
            pressed.append(code)
        callback()
    except Exception as exc:
        primary_error = exc
    release_error = None
    for code in reversed(pressed):
        try:
            emitted = emit_keyboard_event(
                code, None, Atspi.KeySynthType.RELEASE,
                "held mouse modifier release", emitted)
        except Exception as exc:
            if release_error is None:
                release_error = exc
    if primary_error is not None:
        raise primary_error
    if release_error is not None:
        raise release_error


def validate_gui_action(action, bounds=None, native_bounds=None):
    from .ax_actions import parse_key

    """Normalize one action without producing input, focus, or wait side effects."""
    if not isinstance(action, dict):
        raise RuntimeError("gui action must be an object")
    action_type = str(action.get("type") or "").strip().lower()
    if action_type not in GUI_ACTION_TYPES:
        raise RuntimeError(
            "unsupported gui action type {!r}; expected {}".format(
                action_type, ", ".join(sorted(GUI_ACTION_TYPES)))
        )
    unexpected = sorted(set(action) - GUI_ACTION_FIELDS[action_type])
    if unexpected:
        raise RuntimeError(
            "gui {} action has unsupported field(s): {}".format(
                action_type, ", ".join(unexpected))
        )

    normalized = {"type": action_type}
    if action_type in {"click", "double_click", "move"}:
        if bounds is None:
            normalized["x"] = finite_gui_number(
                action.get("x"), "gui {} x".format(action_type))
            normalized["y"] = finite_gui_number(
                action.get("y"), "gui {} y".format(action_type))
        else:
            normalized["x"], normalized["y"] = screen_absolute_point(
                action.get("x"), action.get("y"), bounds, native_bounds)
    if action_type == "click":
        button = str(action.get("button") or "left").strip().lower()
        if button not in {"left", "right", "wheel", "back", "forward"}:
            raise RuntimeError(
                "gui click button must be left, right, wheel, back, or forward")
        normalized["button"] = button
    elif action_type == "double_click":
        normalized["button"] = "left"
    if action_type in GUI_POINTER_ACTION_TYPES:
        normalized["keys"] = validate_mouse_modifier_keys(action.get("keys"))
    if action_type == "drag":
        path = action.get("path")
        if not isinstance(path, list) or len(path) < 2:
            raise RuntimeError("gui drag requires path with at least two points")
        if len(path) > GUI_MAX_DRAG_POINTS:
            raise RuntimeError(
                "gui drag path accepts at most {} points".format(GUI_MAX_DRAG_POINTS))
        normalized["path"] = []
        for point in path:
            x = point.get("x") if isinstance(point, dict) else None
            y = point.get("y") if isinstance(point, dict) else None
            if bounds is None:
                normalized["path"].append((
                    finite_gui_number(x, "gui drag x"),
                    finite_gui_number(y, "gui drag y"),
                ))
            else:
                normalized["path"].append(
                    screen_absolute_point(x, y, bounds, native_bounds)
                )
    elif action_type == "scroll":
        if bounds is None:
            normalized["x"] = finite_gui_number(action.get("x"), "gui scroll x")
            normalized["y"] = finite_gui_number(action.get("y"), "gui scroll y")
        else:
            normalized["x"], normalized["y"] = screen_absolute_point(
                action.get("x"), action.get("y"), bounds, native_bounds)
        dx = finite_gui_number(action.get("scroll_x", 0), "gui scroll scroll_x")
        dy = finite_gui_number(action.get("scroll_y", 0), "gui scroll scroll_y")
        if abs(dx) > GUI_MAX_SCROLL_DELTA or abs(dy) > GUI_MAX_SCROLL_DELTA:
            raise RuntimeError(
                "gui scroll scroll_x and scroll_y must be between -{} and {}".format(
                    GUI_MAX_SCROLL_DELTA, GUI_MAX_SCROLL_DELTA))
        if int(round(dx)) == 0 and int(round(dy)) == 0:
            raise RuntimeError("gui scroll requires a non-zero scroll_x or scroll_y")
        normalized["scroll_x"] = dx
        normalized["scroll_y"] = dy
    elif action_type == "type":
        text = action.get("text")
        if not isinstance(text, str):
            raise RuntimeError("gui type requires text")
        if len(text) > GUI_MAX_TEXT_LENGTH:
            raise RuntimeError(
                "gui type text accepts at most {} characters".format(
                    GUI_MAX_TEXT_LENGTH))
        normalized["text"] = text
    elif action_type == "keypress":
        keys = action.get("keys")
        if not isinstance(keys, list) or not keys:
            raise RuntimeError("gui keypress requires a non-empty keys array")
        if len(keys) > GUI_MAX_KEYS:
            raise RuntimeError(
                "gui keypress accepts at most {} keys".format(GUI_MAX_KEYS))
        if not all(
                isinstance(key, str)
                and key.strip()
                and len(key) <= GUI_MAX_KEY_LENGTH
                for key in keys):
            raise RuntimeError(
                "gui keypress keys must be non-empty strings of at most {} characters".format(
                    GUI_MAX_KEY_LENGTH))
        normalized["keys"] = [key.strip() for key in keys]
        parse_key("+".join(normalized["keys"]))
    elif action_type == "wait":
        normalized["seconds"] = 1.0
    return normalized


def validate_gui_actions(actions, bounds=None, native_bounds=None):
    normalized = []
    errors = []
    for index, action in enumerate(actions):
        action_type = action.get("type") if isinstance(action, dict) else None
        try:
            normalized.append(
                validate_gui_action(action, bounds, native_bounds)
            )
        except Exception as exc:
            errors.append({
                "index": index,
                "type": action_type or "unknown",
                "error": str(exc),
            })
    return normalized, errors


def execute_gui_action(action):
    from .ax_actions import send_key, send_text

    """Execute one action that has already passed validate_gui_action."""
    action_type = action["type"]
    if action_type == "screenshot":
        return
    if action_type in {"click", "double_click"}:
        x, y = action["x"], action["y"]
        button = action["button"]
        count = 2 if action_type == "double_click" else 1
        with_held_mouse_modifiers(
            action.get("keys"),
            lambda: send_mouse_click(x, y, button, count),
        )
        return
    if action_type == "move":
        x, y = action["x"], action["y"]
        with_held_mouse_modifiers(
            action.get("keys"),
            lambda: emit_mouse_event(x, y, "abs"),
        )
        return
    if action_type == "drag":
        with_held_mouse_modifiers(
            action.get("keys"),
            lambda: send_drag_path(action["path"]),
        )
        return
    if action_type == "scroll":
        with_held_mouse_modifiers(
            action.get("keys"),
            lambda: send_gui_scroll(
                action["scroll_x"], action["scroll_y"],
                action["x"], action["y"]),
        )
        return
    if action_type == "type":
        send_text(action["text"])
        return
    if action_type == "keypress":
        send_key("+".join(action["keys"]))
        return
    if action_type == "wait":
        time.sleep(1.0)
        return
    raise AssertionError("validated unsupported gui action: " + action_type)


def gui_wait_metrics(actions):
    wait_actions = 0
    wait_seconds = 0.0
    keyboard_micro_wait_actions = 0
    for index, action in enumerate(actions):
        if action["type"] != "wait":
            continue
        wait_actions += 1
        wait_seconds += action["seconds"]
        previous_type = actions[index - 1]["type"] if index else None
        next_type = (
            actions[index + 1]["type"]
            if index + 1 < len(actions)
            else None
        )
        if (
            action["seconds"] <= GUI_KEYBOARD_MICRO_WAIT_MAX_SECONDS
            and previous_type in GUI_KEYBOARD_ACTION_TYPES
            and next_type in GUI_KEYBOARD_ACTION_TYPES
        ):
            keyboard_micro_wait_actions += 1
    return {
        "wait_actions_requested": wait_actions,
        "wait_seconds_requested": round(wait_seconds, 6),
        "keyboard_micro_wait_actions": keyboard_micro_wait_actions,
    }
