"""Action choice, geometry stability and coordinate-delivery checks."""

from .foundation import (
    Atspi,
    CLICK_COVERED_ACTIONS,
    NON_SELF_ACTIONS,
    OperationFailure,
    frame,
    math,
    normalized_action,
    os,
    safe,
    time,
)

from .x11_windows import (
    desktop_bounds,
    desktop_layout_state,
    frame_intersection,
    relative_frame,
    x11_window_id_for_accessible,
)

from .gui_capture import (
    gui_model_dimensions,
)
from . import action_evidence
from .input_primitives import require_window_focus

from .element_resolver import (
    same_frame,
)

def preferred_action_index(node):
    # Use CLICK_COVERED_ACTIONS directly rather than a local copy: the same
    # set decides which action a click invokes and which actions are hidden
    # as redundant, so two copies would drift apart.
    preferred_exact = CLICK_COVERED_ACTIONS
    if safe(node.get_action_iface) is None:
        return None
    count = int(safe(node.get_n_actions, 0) or 0)
    fallback = None
    for index in range(count):
        name = str(safe(lambda i=index: node.get_action_name(i), "") or "")
        description = str(safe(lambda i=index: node.get_action_description(i), "") or "")
        lower = (name or description).lower()
        if lower in preferred_exact:
            return index
        if normalized_action(lower) in NON_SELF_ACTIONS:
            continue
        if fallback is None and (
            "activate" in lower or "click" in lower or "press" in lower
        ):
            fallback = index
    return fallback


def do_action_by_index(node, index):
    if index is None:
        return False
    return bool(safe(lambda: node.do_action(int(index)), False))


def window_relative(window_bounds, x, y):
    """Convert an AX input-synthesis screen point to window coordinates.

    AX tree frames and the optional window screenshot use window-relative
    coordinates. The GUI tool accepts only coordinates in its own full-desktop
    image and never uses this conversion.
    """
    if window_bounds is None:
        return x, y
    return x - window_bounds["x"], y - window_bounds["y"]


# An element is stable once two consecutive samples match: the desktop analogue
# of Playwright waiting for an unchanged bounding box across two animation
# frames.
STABLE_SAMPLE_SECONDS = 0.08


def stable_timeout_seconds():
    """Return the maximum wait for an element to stop moving, in seconds.

    COMPUTER_USE_STABLE_TIMEOUT_MS overrides the 1 second default. The default
    covers most one-off relayouts without letting a permanently moving element
    (progress bar, animation) delay a click by seconds. Animation-heavy
    applications may need more; 0 disables the wait.
    """
    raw = os.environ.get("COMPUTER_USE_STABLE_TIMEOUT_MS", "").strip()
    if not raw:
        return 1.0
    try:
        return max(0.0, float(raw) / 1000.0)
    except ValueError:
        return 1.0


def wait_until_stable(element, window_bounds, first=None):
    """Wait for an element to stop moving before acting on it.

    Returns ``(final_frame, samples, settled)``. Pass ``first`` when the frame
    has already been sampled, to avoid a redundant read.

    Only stability is checked, not enablement.
    Playwright's actionability checks also wait for "enabled", but that signal
    is unreliable over AT-SPI: Nautilus file icons expose neither ENABLED nor
    SENSITIVE (only SHOWING, VISIBLE and FOCUSABLE) yet offer working "open"
    and "menu" actions, so an ENABLED gate would reject usable targets. A
    "receives events" hit test is skipped for the same reason: measured hit
    rates were 11/11 in gedit, 19/25 in Nautilus and 12/25 in LibreOffice, so
    such a gate would block about half of valid LibreOffice clicks.
    """
    if element is None:
        return None, 0, True
    previous = first if first is not None else safe(
        lambda: relative_frame(element, window_bounds)
    )
    samples = 1
    if previous is None:
        return None, samples, False
    deadline = time.monotonic() + stable_timeout_seconds()
    while True:
        time.sleep(STABLE_SAMPLE_SECONDS)
        current = safe(lambda: relative_frame(element, window_bounds))
        samples += 1
        if current is None:
            return None, samples, False
        if same_frame(previous, current):
            return current, samples, True
        previous = current
        if time.monotonic() >= deadline:
            # The sampler reports instability. Coordinate routes fail closed
            # in require_stable_coordinate_geometry(); semantic provider
            # actions do not depend on geometry.
            return current, samples, False


def current_geometry(element, element_record, window_bounds):
    """Replace the record's frame with the live node's current frame.

    Like a Playwright locator, the target is re-resolved at action time. The
    live node is already resolved, so coordinates must come from it rather
    than from the snapshot record cached by the Go side; otherwise semantic
    actions and synthesized input could target different positions within one
    call. Desktop UIs have no page-load event, and animation, asynchronous
    loading or another process can move an element between tool calls.

    Returns ``(record, movement_note)``. If the live frame is unavailable, the
    old record is kept for diagnostics only; coordinate fallbacks reject it in
    require_stable_coordinate_geometry() and never emit input at stale
    coordinates.
    """
    if element is None or not element_record:
        return element_record, None
    live = safe(lambda: relative_frame(element, window_bounds))
    if live is None:
        return element_record, None
    old = element_record.get("frame")
    updated = dict(element_record)
    updated["frame"] = live
    if not old:
        return updated, None
    dx = live["x"] - old["x"]
    dy = live["y"] - old["y"]
    if abs(dx) <= 2 and abs(dy) <= 2:
        return updated, None
    return updated, (
        "The element MOVED since the snapshot: it was at {{{:.0f},{:.0f}}} and is now "
        "at {{{:.0f},{:.0f}}} (window-relative). This action used its CURRENT position, "
        "not the one in the tree you read. Any other coordinate you took from that "
        "snapshot is stale by the same amount.".format(
            old["x"], old["y"], live["x"], live["y"]
        )
    )


def require_stable_coordinate_geometry(element, element_record, window_bounds):
    """Return a fresh stable frame for a coordinate fallback or fail closed."""
    action_evidence.update(geometry_sampled=element is not None)
    if element is None or not element_record:
        return element_record, 0
    first = safe(lambda: relative_frame(element, window_bounds))
    if first is None:
        action_evidence.update(geometry_stable=None)
        raise OperationFailure(
            "observation_incomplete: live target geometry is unavailable; "
            "no coordinate input was emitted.",
            failure_code="observation_incomplete",
            failure_phase="precondition",
            input_events_emitted=False,
            failure_subject="element",
        )
    live, samples, settled = wait_until_stable(
        element, window_bounds, first=first
    )
    if live is None:
        action_evidence.update(geometry_stable=None)
        raise OperationFailure(
            "observation_incomplete: live target geometry could not be sampled; "
            "no coordinate input was emitted.",
            failure_code="observation_incomplete",
            failure_phase="precondition",
            input_events_emitted=False,
            failure_subject="element",
        )
    if not settled:
        action_evidence.update(geometry_stable=False)
        raise OperationFailure(
            "target_unstable: target geometry did not stabilize before the "
            "coordinate-action deadline; no input was emitted.",
            failure_code="target_unstable",
            failure_phase="precondition",
            input_events_emitted=False,
        )
    updated = dict(element_record)
    updated["frame"] = live
    action_evidence.update(geometry_stable=True)
    return updated, samples


def coordinate_point_owner(window, x, y):
    """Return true/false/unknown for exact native ownership of a screen point."""
    app = safe(lambda: Atspi.Accessible.get_application(window))
    target_id = (
        x11_window_id_for_accessible(app, window) if app is not None else None
    )
    layout = desktop_layout_state(desktop_bounds())
    if not target_id or layout is None:
        return None
    if layout.get("activeWindow") != target_id:
        return False
    containing = []
    for candidate in layout.get("windows", []):
        candidate_frame = frame(
            candidate.get("x", 0) + layout["desktop"].get("screenX", 0),
            candidate.get("y", 0) + layout["desktop"].get("screenY", 0),
            candidate.get("width", 0),
            candidate.get("height", 0),
        )
        if (
            candidate_frame["width"] > 0
            and candidate_frame["height"] > 0
            and candidate_frame["x"] <= x <
                candidate_frame["x"] + candidate_frame["width"]
            and candidate_frame["y"] <= y <
                candidate_frame["y"] + candidate_frame["height"]
        ):
            containing.append(candidate.get("id"))
    if not containing:
        return False
    return containing[-1] == target_id


def require_coordinate_delivery(window, x, y, what):
    require_window_focus(window, what)
    action_evidence.update(window_focus_verified=True)
    owner = coordinate_point_owner(window, x, y)
    if owner is True:
        action_evidence.update(point_owner_verified=True)
        return
    action_evidence.update(point_owner_verified=owner)
    reason = "unknown" if owner is None else "different_window"
    raise OperationFailure(
        "coordinate_target_unverified: point owner is {}; no input was "
        "emitted.".format(reason),
        failure_code="coordinate_target_unverified",
        failure_phase="precondition",
        input_events_emitted=False,
    )


def screen_point(window_bounds, element=None, x=None, y=None):
    if element is not None:
        f = element.get("frame")
        if f is not None and window_bounds is not None:
            element_on_screen = frame(
                window_bounds["x"] + f["x"],
                window_bounds["y"] + f["y"],
                f["width"],
                f["height"],
            )
            visible = frame_intersection(element_on_screen, window_bounds)
            desktop = desktop_bounds()
            if desktop is None:
                raise RuntimeError(
                    "full desktop bounds are unavailable; refusing accessibility "
                    "coordinate fallback because the element's visible screen region "
                    "cannot be validated"
                )
            visible = frame_intersection(visible, desktop)
            if visible is None:
                raise RuntimeError(
                    "element_offscreen: the element frame does not intersect the target "
                    "window and full desktop; scroll it into view before coordinate input"
                )
            return (
                visible["x"] + visible["width"] / 2,
                visible["y"] + visible["height"] / 2,
            )
    if x is None or y is None or window_bounds is None:
        raise RuntimeError("coordinate action requires window bounds and x/y")
    # Window-relative x/y input is used only by AX coordinate fallbacks; GUI
    # input never takes this path.
    left, top = window_bounds["x"], window_bounds["y"]
    right = left + max(window_bounds["width"] - 1, 0)
    bottom = top + max(window_bounds["height"] - 1, 0)
    return (
        min(max(left + float(x), left), right),
        min(max(top + float(y), top), bottom),
    )


def screen_absolute_point(x, y, bounds=None, native_bounds=None):
    """Translate one delivered-image coordinate into the X root coordinate.

    The public GUI coordinate space is the returned full-desktop image, whose
    top-left pixel is always (0,0). The delivered image may be smaller than the
    native virtual desktop, so the runtime scales against the two exact extents
    and then adds the native desktop origin. It never adds an application-window
    offset and never clamps the point.
    """
    if x is None or y is None:
        raise RuntimeError("GUI coordinate action requires full-desktop image x/y")
    try:
        x = float(x)
        y = float(y)
    except (TypeError, ValueError):
        raise RuntimeError("GUI coordinates must be finite numbers")
    if not math.isfinite(x) or not math.isfinite(y):
        raise RuntimeError("GUI coordinates must be finite numbers")
    native_bounds = (
        desktop_bounds()
        if native_bounds is None and bounds is None
        else native_bounds or bounds
    )
    if native_bounds is None:
        raise RuntimeError(
            "full desktop bounds are unavailable; refusing screen-image input "
            "because gui cannot validate where it would be delivered"
        )
    if bounds is None:
        dimensions = gui_model_dimensions(native_bounds)
        bounds = frame(0, 0, dimensions["width"], dimensions["height"])
    right = max(bounds["width"] - 1, 0)
    bottom = max(bounds["height"] - 1, 0)
    if x < 0 or x > right or y < 0 or y > bottom:
        raise RuntimeError(
            "screen-image point ({:.0f}, {:.0f}) is outside the full desktop "
            "image bounds {{0,0,{:.0f},{:.0f}}}; read coordinates from "
            "gui".format(
                x, y, bounds["width"], bounds["height"])
        )
    scale_x = float(native_bounds["width"]) / float(bounds["width"])
    scale_y = float(native_bounds["height"]) / float(bounds["height"])
    native_x = native_bounds["x"] + min(
        float(native_bounds["width"]) - 1,
        max(0.0, (x + 0.5) * scale_x - 0.5),
    )
    native_y = native_bounds["y"] + min(
        float(native_bounds["height"]) - 1,
        max(0.0, (y + 0.5) * scale_y - 0.5),
    )
    return native_x, native_y
