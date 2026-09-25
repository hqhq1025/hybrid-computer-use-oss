"""Application and full-desktop snapshot construction."""

import hashlib
import time
from .foundation import (
    Atspi,
    DEFAULT_TEXT_LIMIT,
    GUI_SNAPSHOT_LAYOUT_ATTEMPTS,
    MAX_DEPTH,
    MAX_ELEMENTS,
    SCREENSHOT_SCOPE_SCREEN,
    a11y_screenshots_enabled,
    extents,
    has_text_iface,
    limit_text,
    node_name,
    node_pid,
    node_role,
    process_generation,
    provider_bus_name,
    provider_object_path,
    safe,
    state_contains,
)

from .ax_snapshot import render_tree
from .ax_records import StableIndexer

from .x11_windows import (
    app_windows,
    content_frame_id,
    desktop_bounds,
    desktop_layout_frame_id,
    desktop_layout_state,
    iter_apps,
    main_window,
    select_window_by_handle,
    strong_window_instance_handles,
    window_instance_handle,
)

from .gui_capture import (
    capture_gui_image,
    capture_window_image,
    gui_model_dimensions,
)

from .atspi_inventory import (
    resolve_app,
    resolve_app_identity,
)
from .primitives import quoted
from .tree_walk import find_first
from .window_diagnostics import (
    foreign_foreground_note,
    modal_diagnostic,
    window_label,
)


def element_reference(role, name):
    """Render an element reference; the single renderer for this form.

    Uses the same grammar as renderElementReference on the Go side. The hard
    contract is round-tripping: the returned string, pasted verbatim into an
    ``expect`` target, must parse back to the same reference. Names are
    therefore always quoted (``entry "Address and search bar"``) and unnamed
    elements are written as ``role="entry"``. An unquoted ``role name``
    string spans both fields and cannot be split, so the tool would reject
    text it printed itself.
    """
    role = (role or "").strip()
    name = (name or "").strip()
    if not role and not name:
        return "an element with neither a role nor a name"
    if not name:
        return "role=" + quoted(role)
    if not role:
        return quoted(name)
    return role + " " + quoted(name)


def focused_summary(app_pid, text_limit=DEFAULT_TEXT_LIMIT):
    try:
        for app in iter_apps():
            if node_pid(app) != app_pid:
                continue
            _, win = main_window(app)
            focused = find_first(
                win, lambda node: state_contains(node, Atspi.StateType.FOCUSED)
            )
            if focused is None:
                return None
            role = node_role(focused)
            name = limit_text(node_name(focused), text_limit=text_limit)
            return element_reference(role, name)
    except Exception:
        return None


def selected_text(app_pid, text_limit=DEFAULT_TEXT_LIMIT):
    try:
        for app in iter_apps():
            if node_pid(app) != app_pid:
                continue
            _, win = main_window(app)
            focused = find_first(
                win, lambda node: state_contains(node, Atspi.StateType.FOCUSED)
            )
            if focused is None or not has_text_iface(focused):
                return None
            text_iface = safe(focused.get_text_iface)
            selections = safe(lambda: Atspi.Text.get_text_selections(text_iface), [])
            if selections:
                selection = selections[0]
                end_offset = selection.end_offset
                if text_limit is not None:
                    end_offset = min(end_offset, selection.start_offset + text_limit + 1)
                value = Atspi.Text.get_text(
                    text_iface, selection.start_offset, end_offset
                )
                return limit_text(value, text_limit=text_limit)
    except Exception:
        return None
    return None


def top_level_window_inventory(app, selected_index=None):
    windows = app_windows(app)
    strong_handles = strong_window_instance_handles(app, windows)
    inventory = []
    for index, window in windows:
        inventory.append({
            "title": str(node_name(window) or "").strip(),
            "role": str(node_role(window) or "window").strip().lower(),
            "handle": strong_handles.get(index) or (
                "weak:pid{}/win{}/{}".format(
                    node_pid(app) or 0,
                    index,
                    str(node_role(window) or "window").strip().lower().replace(
                        " ", "-"
                    ),
                )
            ),
            "providerBusName": provider_bus_name(window),
            "providerObjectPath": provider_object_path(window),
            "providerProcessGeneration": process_generation(node_pid(window)),
            "selected": index == selected_index,
            "active": state_contains(window, Atspi.StateType.ACTIVE),
            "modal": state_contains(window, Atspi.StateType.MODAL),
            "showing": state_contains(window, Atspi.StateType.SHOWING),
        })
    return inventory


def utc_timestamp():
    """Return the sampling time in UTC with millisecond precision.

    Snapshots carry their own timestamp because a desktop snapshot can be
    tens of seconds stale without the caller noticing; unlike DOM changes in
    a browser, desktop changes usually come with no observable event.
    """
    now = time.time()
    return "{}.{:03d}Z".format(
        time.strftime("%Y-%m-%dT%H:%M:%S", time.gmtime(now)), int((now % 1) * 1000))


# Roles that show the application is busy. Progress bars and busy indicators
# are toolkit-independent controls.
BUSY_ROLES = {"progress bar", "busy indicator"}


def busy_signal(window, records):
    """Report whether the application explicitly signals that it is busy.

    An asynchronous or long-running operation looks like "nothing changed" in
    the tree, which reads the same as content cut by the budget; this signal
    tells the caller whether to wait or to observe differently.

    Only presence carries meaning: a missing BUSY state or progress bar does
    not prove that the operation finished, because toolkits set these states
    inconsistently over AT-SPI.
    """
    busy_state = getattr(Atspi.StateType, "BUSY", None)
    window_busy = bool(
        busy_state is not None and state_contains(window, busy_state))
    indicators = sum(
        1 for record in records
        if str(record.get("controlType") or "").strip().lower() in BUSY_ROLES)
    return {"windowBusy": window_busy, "progressElements": indicators}


def snapshot_digest(window_handle, lines, focused, selected):
    """Return a content fingerprint of a snapshot.

    Equal digests mean two observations are textually identical. Screenshots
    are deliberately excluded: pixels vary between frames (caret blinking,
    anti-aliasing), so including them would make digests never match. The
    digest covers the whole semantic channel: root window identity, every
    tree line, focus and selection.
    """
    payload = "\n".join(
        [str(window_handle or "")] + [str(line) for line in (lines or [])]
        + [str(focused or ""), str(selected or "")])
    return hashlib.sha1(payload.encode("utf-8", "replace")).hexdigest()[:12]


def build_snapshot(
    query,
    text_limit=DEFAULT_TEXT_LIMIT,
    max_tree_nodes=MAX_ELEMENTS,
    max_tree_depth=MAX_DEPTH,
    include_screenshot=None,
    prune=True,
    boxes=False,
    known_refs=None,
    screenshot_scope="window",
    app_identity=None,
    target_window_handle=None,
):
    """Build an application snapshot.

    ``include_screenshot=None`` defers to a11y_screenshots_enabled(); True or
    False forces the choice. Callers request AX screenshots explicitly with
    observe=screenshot or observe=full.

    A window screenshot is costly (about a thousand tokens, most of a pruned
    observation), but the tree and the screenshot are complementary rather
    than redundant: the tree
    provides actionability and the screenshot verifiability. Some effects
    never reach the tree (saving a file in LibreOffice Impress, for example),
    while the tree's description identifies controls, such as which spin
    button is "Position Y", that a screenshot can only guess from label
    proximity.

    ``boxes`` defaults to False, like Playwright's option of the same name;
    geometry takes 35-50% of the rendered tree. Frames share the
    window-relative space of the AX window screenshot and mainly serve
    debugging. Element-targeted clicks read coordinates from the records
    internally, so leaving frames out of the rendering costs no AX
    capability. GUI coordinates must come from the GUI tool's full-desktop
    image, never from these frames.
    """
    app = (
        resolve_app_identity(app_identity)
        if app_identity is not None
        else resolve_app(query)
    )
    window_index, window, window_selection = select_window_by_handle(
        app, target_window_handle
    )
    bounds = extents(window)
    records, lines, annotations = render_tree(
        window,
        bounds,
        [window_index],
        text_limit=text_limit,
        max_tree_nodes=max_tree_nodes,
        max_tree_depth=max_tree_depth,
        prune=prune,
        boxes=boxes,
        indexer=StableIndexer(known_refs),
        include_annotations=True,
    )
    pid = node_pid(app)
    handle = window_instance_handle(app, window_index, window)
    title = window_label(window, text_limit=text_limit)
    focused = focused_summary(pid, text_limit=text_limit)
    selected = selected_text(pid, text_limit=text_limit)
    screenshot_bounds = (
        desktop_bounds()
        if screenshot_scope == SCREENSHOT_SCOPE_SCREEN
        else bounds
    )
    screenshot = (
        capture_window_image(screenshot_bounds)
        if (a11y_screenshots_enabled() if include_screenshot is None else include_screenshot)
        else None
    )
    return {
        "app": {
            "name": node_name(app),
            "bundleIdentifier": node_name(app),
            "pid": pid,
            "providerBusName": provider_bus_name(app),
            "processGeneration": process_generation(pid),
        },
        "windowTitle": title,
        # A title does not identify a window; see window_instance_handle().
        "windowHandle": handle,
        "windows": top_level_window_inventory(app, window_index),
        "windowBounds": bounds,
        "windowSelection": window_selection,
        # Freshness and completeness are basic metadata for a stateful
        # protocol. sampledAt is when this snapshot was sampled; snapshotId is
        # its content fingerprint, which the Go side compares for equivalence.
        "sampledAt": utc_timestamp(),
        "snapshotId": snapshot_digest(handle, lines, focused, selected),
        "busy": busy_signal(window, records),
        "screenshotPngBase64": screenshot.get("data") if screenshot else None,
        "screenshotMimeType": screenshot.get("mimeType") if screenshot else None,
        "screenshotEncoding": screenshot.get("encoding") if screenshot else None,
        "treeLines": lines,
        "focusedSummary": focused,
        "selectedText": selected,
        "elements": records,
        "annotations": annotations,
        # Return path -> (index, role, name) to Go; passing it back unchanged
        # with the next request keeps indices stable across snapshots. See
        # StableIndexer.
        "refs": {
            ".".join(str(p) for p in r["runtimeId"]): {
                "index": r["index"], "role": r["controlType"], "name": r["name"],
                "nameHash": r.get("nameHash", ""),
            }
            for r in records
        },
        # Modal notes travel with every snapshot, not only with observations:
        # post-action snapshots need them most, because the action may be what
        # opened the dialog. The Go appSnapshot has no such field and
        # encoding/json ignores it; the dispatcher pops it into the notes.
        "modalNotes": modal_diagnostic(window, app) + foreign_foreground_note(app),
    }


def build_gui_snapshot(bounds=None):
    """Capture only the full desktop; GUI never resolves an app or walks AT-SPI."""
    if bounds is None:
        bounds = desktop_bounds()
    screenshot = None
    layout = None
    for _ in range(GUI_SNAPSHOT_LAYOUT_ATTEMPTS):
        before_layout = desktop_layout_state(bounds)
        screenshot = capture_gui_image(bounds)
        after_layout = desktop_layout_state(bounds)
        if before_layout is not None and before_layout == after_layout:
            layout = after_layout
            break
    dimensions = gui_model_dimensions(bounds)
    return {
        "sampledAt": utc_timestamp(),
        "coordinateSpace": "screen_image_px",
        "width": dimensions["width"] if dimensions is not None else None,
        "height": dimensions["height"] if dimensions is not None else None,
        "frameId": desktop_layout_frame_id(layout),
        "desktopLayout": layout,
        "contentFrameId": content_frame_id(
            screenshot.get("data") if screenshot else None),
        "screenshotPngBase64": screenshot.get("data") if screenshot else None,
        "screenshotMimeType": screenshot.get("mimeType") if screenshot else None,
        "screenshotEncoding": screenshot.get("encoding") if screenshot else None,
    }
