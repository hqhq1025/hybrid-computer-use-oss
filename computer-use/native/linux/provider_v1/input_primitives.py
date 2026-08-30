"""Low-level Linux focus and input emission primitives."""

from .foundation import (
    Atspi,  # pyright: ignore[reportAttributeAccessIssue]
    FOCUS_GRAB_CANDIDATES,
    node_name,
    node_pid,
    safe,
    state_contains,
    subprocess,
    time,
)
from .tree_walk import iter_all
from .x11_windows import app_windows, iter_apps


def window_is_active(window):
    return window is not None and state_contains(window, Atspi.StateType.ACTIVE)


def x11_holds_focus(window):
    app = safe(lambda: Atspi.Accessible.get_application(window))
    target_pid = node_pid(app) if app is not None else 0
    if not target_pid:
        return False
    try:
        active = subprocess.run(
            ["xdotool", "getactivewindow"],
            capture_output=True,
            text=True,
            timeout=3,
        )
        if active.returncode != 0 or not active.stdout.strip():
            return False
        prop = subprocess.run(
            ["xprop", "-id", active.stdout.strip(), "_NET_WM_PID"],
            capture_output=True,
            text=True,
            timeout=3,
        )
    except Exception:
        return False
    if prop.returncode != 0:
        return False
    digits = "".join(
        char for char in prop.stdout if char.isdigit() or char == " "
    ).split()
    return bool(digits) and int(digits[-1]) == int(target_pid)


def focus_window(window, timeout=1.0):
    if window is None:
        return False
    if window_is_active(window):
        return True
    if x11_holds_focus(window):
        return True

    def grab(node):
        component = safe(node.get_component_iface)
        if component is None:
            return False
        return bool(safe(lambda: Atspi.Component.grab_focus(component), False))

    focusable = [
        node
        for node in iter_all(window)
        if state_contains(node, Atspi.StateType.FOCUSABLE)
    ]
    focusable.sort(
        key=lambda node: state_contains(node, Atspi.StateType.FOCUSED),
        reverse=True,
    )
    for node in focusable[:FOCUS_GRAB_CANDIDATES]:
        if not grab(node):
            continue
        deadline = time.time() + timeout
        while time.time() < deadline:
            if window_is_active(window) or x11_holds_focus(window):
                return True
            time.sleep(0.05)
    title = str(node_name(window) or "").strip()
    if title:
        safe(
            lambda: subprocess.run(
                ["wmctrl", "-a", title],
                capture_output=True,
                timeout=3,
            )
        )
        deadline = time.time() + timeout
        while time.time() < deadline:
            if window_is_active(window) or x11_holds_focus(window):
                return True
            time.sleep(0.05)
    return window_is_active(window) or x11_holds_focus(window)


def active_accessible_window():
    try:
        for app in iter_apps():
            for _, window in app_windows(app):
                if state_contains(window, Atspi.StateType.ACTIVE):
                    return node_name(app), node_name(window)
    except Exception:
        return None, None
    return None, None


def require_window_focus(window, what):
    if focus_window(window):
        return
    app_name, window_name = active_accessible_window()
    if app_name is None:
        culprit = (
            " No window in the accessibility tree currently reports ACTIVE, so "
            "input focus is held by something the tree cannot see - most often a "
            "native dialog. Call gui with actions=[] to inspect the full desktop."
        )
    else:
        culprit = " Input focus is currently held by '{}' in '{}'.".format(
            window_name or "(untitled window)",
            app_name,
        )
    raise RuntimeError(
        "Refusing to synthesize {}: could not bring the target window to the "
        "foreground. Input synthesis is global and would be delivered to "
        "whichever window currently holds focus.{}".format(what, culprit)
    )


class InputEmissionError(RuntimeError):
    def __init__(self, description, events_emitted):
        super().__init__(
            "AT-SPI input emission failed while sending {}".format(description)
        )
        self.events_emitted = bool(events_emitted)


def require_input_emission(result, description, events_emitted=False):
    if result is False:
        raise InputEmissionError(description, events_emitted)
    return True


def emit_mouse_event(x, y, event, events_emitted=False):
    result = Atspi.generate_mouse_event(int(round(x)), int(round(y)), event)
    return require_input_emission(
        result,
        "mouse event {!r}".format(event),
        events_emitted,
    )


def emit_keyboard_event(
    keyval,
    keystring,
    synth_type,
    description,
    events_emitted=False,
):
    result = Atspi.generate_keyboard_event(keyval, keystring, synth_type)
    return require_input_emission(result, description, events_emitted)


def best_effort_mouse_release(x, y, event):
    try:
        Atspi.generate_mouse_event(int(round(x)), int(round(y)), event)
    except Exception:
        pass
