"""X11 desktop layout, native window identity and window selection."""

from .foundation import (
    AX_WINDOW_ROLES,
    Atspi,
    Gdk,
    GUI_MAX_LAYOUT_WINDOWS,
    InventoryResult,
    MAX_SANE_EXTENT,
    OperationFailure,
    desktop,
    enumerate_children,
    extents,
    frame,
    hashlib,
    inventory_complete,
    inventory_errors,
    json,
    node_name,
    node_pid,
    node_role,
    process_generation,
    re,
    safe,
    state_contains,
    subprocess,
)
from .primitives import semantic_text_key

def desktop_bounds():
    """Return the full virtual desktop in X root coordinates.

    AT-SPI's SCREEN coordinates and XTEST both use the X root window coordinate
    space. GUI exposes the captured image with a (0,0) top-left and translates
    those image pixels back through this origin before XTEST emission.
    """
    if Gdk is None:
        return None
    try:
        screen = Gdk.Screen.get_default()
        if screen is None:
            return None
        monitor_bounds = []
        monitor_count = int(screen.get_n_monitors())
        for monitor_index in range(monitor_count):
            geometry = screen.get_monitor_geometry(monitor_index)
            if (
                geometry is None
                or geometry.width <= 0
                or geometry.height <= 0
                or geometry.width > MAX_SANE_EXTENT
                or geometry.height > MAX_SANE_EXTENT
                or abs(geometry.x) > MAX_SANE_EXTENT
                or abs(geometry.y) > MAX_SANE_EXTENT
            ):
                continue
            monitor_bounds.append(geometry)
        if monitor_bounds:
            left = min(geometry.x for geometry in monitor_bounds)
            top = min(geometry.y for geometry in monitor_bounds)
            right = max(
                geometry.x + geometry.width for geometry in monitor_bounds
            )
            bottom = max(
                geometry.y + geometry.height for geometry in monitor_bounds
            )
            if right > left and bottom > top:
                return frame(left, top, right - left, bottom - top)
        root = screen.get_root_window()
        if root is None:
            return None
        width = int(root.get_width())
        height = int(root.get_height())
        if width <= 0 or height <= 0:
            return None
        return frame(0, 0, width, height)
    except Exception:
        return None


def normalize_x11_window_id(value):
    try:
        return hex(int(str(value).strip(), 0))
    except (TypeError, ValueError):
        return None


def parse_x11_root_layout_properties(text):
    clients = re.search(
        r"^_NET_CLIENT_LIST_STACKING[^:]*:\s*window id #\s*(.*)$",
        str(text or ""),
        re.MULTILINE,
    )
    active = re.search(
        r"^_NET_ACTIVE_WINDOW[^:]*:\s*window id #\s*(.*)$",
        str(text or ""),
        re.MULTILINE,
    )
    if clients is None or active is None:
        return None
    window_ids = [
        normalize_x11_window_id(value)
        for value in re.findall(r"0x[0-9a-fA-F]+", clients.group(1))
    ]
    window_ids = [value for value in window_ids if value is not None]
    active_ids = re.findall(r"0x[0-9a-fA-F]+", active.group(1))
    active_id = normalize_x11_window_id(active_ids[0]) if active_ids else None
    if active_id == "0x0":
        active_id = None
    return window_ids, active_id


def parse_xwininfo_layout(text):
    output = str(text or "")
    title_match = re.search(
        r'^xwininfo:\s+Window id:\s+\S+(?:\s+"(.*)")?\s*$',
        output,
        re.MULTILINE,
    )
    mapped = re.search(r"^\s*Map State:\s*(\S+)\s*$", output, re.MULTILINE)
    x = re.search(r"^\s*Absolute upper-left X:\s*(-?\d+)\s*$",
                  output, re.MULTILINE)
    y = re.search(r"^\s*Absolute upper-left Y:\s*(-?\d+)\s*$",
                  output, re.MULTILINE)
    width = re.search(r"^\s*Width:\s*(\d+)\s*$", output, re.MULTILINE)
    height = re.search(r"^\s*Height:\s*(\d+)\s*$", output, re.MULTILINE)
    override = re.search(
        r"^\s*Override Redirect State:\s*(\S+)\s*$",
        output,
        re.MULTILINE,
    )
    if not (mapped and x and y and width and height):
        return None
    return {
        "visible": mapped.group(1) == "IsViewable",
        "x": int(x.group(1)),
        "y": int(y.group(1)),
        "width": int(width.group(1)),
        "height": int(height.group(1)),
        "title": title_match.group(1) if title_match and title_match.group(1) else "",
        "overrideRedirect": (
            override.group(1) == "yes" if override is not None else False),
    }


def x11_root_window_state(timeout=0.25):
    """Return managed window IDs and active window with one bounded X11 query."""
    try:
        root = subprocess.run(
            ["xprop", "-root", "_NET_CLIENT_LIST_STACKING", "_NET_ACTIVE_WINDOW"],
            capture_output=True,
            text=True,
            timeout=timeout,
        )
    except Exception:
        return None
    if root.returncode != 0:
        return None
    properties = parse_x11_root_layout_properties(root.stdout)
    if properties is None:
        return None
    window_ids, active_id = properties
    if len(window_ids) > GUI_MAX_LAYOUT_WINDOWS:
        return None
    return {"windowIds": set(window_ids), "activeWindow": active_id}


def frame_intersects(first, second):
    if first is None or second is None:
        return False
    return (
        first["width"] > 0
        and first["height"] > 0
        and first["x"] < second["x"] + second["width"]
        and first["x"] + first["width"] > second["x"]
        and first["y"] < second["y"] + second["height"]
        and first["y"] + first["height"] > second["y"]
    )


def frame_intersection(first, second):
    if not frame_intersects(first, second):
        return None
    left = max(first["x"], second["x"])
    top = max(first["y"], second["y"])
    right = min(first["x"] + first["width"], second["x"] + second["width"])
    bottom = min(first["y"] + first["height"], second["y"] + second["height"])
    return frame(left, top, right - left, bottom - top)


def desktop_layout_state(bounds):
    """Return coordinate-relevant X11 desktop state without reading pixels.

    Titles, contents, caret state, clocks, and animations are excluded. The
    virtual desktop, visible managed top-level windows in stacking order, their
    geometry, and the active window are the facts that invalidate coordinates.
    """
    if bounds is None:
        return None
    try:
        root = subprocess.run(
            ["xprop", "-root", "_NET_CLIENT_LIST_STACKING", "_NET_ACTIVE_WINDOW"],
            capture_output=True,
            text=True,
            timeout=3,
        )
        if root.returncode != 0:
            return None
        properties = parse_x11_root_layout_properties(root.stdout)
        if properties is None:
            return None
        window_ids, active_id = properties
        if len(window_ids) > GUI_MAX_LAYOUT_WINDOWS:
            return None
        visible = []
        for window_id in window_ids:
            info = subprocess.run(
                ["xwininfo", "-id", window_id],
                capture_output=True,
                text=True,
                timeout=3,
            )
            if info.returncode != 0:
                return None
            parsed = parse_xwininfo_layout(info.stdout)
            if parsed is None:
                return None
            window_frame = frame(
                parsed["x"], parsed["y"], parsed["width"], parsed["height"])
            if not parsed["visible"] or not frame_intersects(window_frame, bounds):
                continue
            visible.append({
                "id": window_id,
                "x": parsed["x"] - int(round(bounds["x"])),
                "y": parsed["y"] - int(round(bounds["y"])),
                "width": parsed["width"],
                "height": parsed["height"],
                "title": parsed["title"],
                "overrideRedirect": parsed["overrideRedirect"],
            })
        return {
            "desktop": {
                "x": 0,
                "y": 0,
                "width": int(round(bounds["width"])),
                "height": int(round(bounds["height"])),
                "screenX": int(round(bounds["x"])),
                "screenY": int(round(bounds["y"])),
            },
            "activeWindow": active_id,
            "windows": visible,
        }
    except Exception:
        return None


def desktop_layout_frame_id(layout):
    if layout is None:
        return None
    coordinate_layout = {
        "desktop": layout.get("desktop"),
        "activeWindow": layout.get("activeWindow"),
        "windows": [
            {
                "id": window.get("id"),
                "x": window.get("x"),
                "y": window.get("y"),
                "width": window.get("width"),
                "height": window.get("height"),
                "overrideRedirect": bool(window.get("overrideRedirect")),
            }
            for window in layout.get("windows", [])
        ],
    }
    encoded = json.dumps(
        coordinate_layout,
        ensure_ascii=True,
        sort_keys=True,
        separators=(",", ":"),
    ).encode("ascii")
    return "layout-v1-" + hashlib.sha256(encoded).hexdigest()


_x11_window_inventory_cache = {}


def x11_window_inventory_for_pid(pid):
    """Return bounded native windows with explicit enumeration completeness."""
    pid = int(pid or 0)
    if pid <= 0:
        return InventoryResult(
            complete=False,
            errors=["application process identity is unavailable"],
        )
    cached = _x11_window_inventory_cache.get(pid)
    if cached is not None:
        return cached
    try:
        result = subprocess.run(
            ["wmctrl", "-lpG"],
            capture_output=True,
            text=True,
            timeout=3,
        )
    except Exception as error:
        return InventoryResult(
            complete=False,
            errors=["native window inventory failed: {}".format(
                type(error).__name__
            )],
        )
    if result.returncode != 0:
        return InventoryResult(
            complete=False,
            errors=["native window inventory command failed"],
        )
    windows = []
    for line in result.stdout.splitlines():
        parts = line.split(None, 8)
        if len(parts) < 8:
            continue
        try:
            window_pid = int(parts[2])
            x, y, width, height = map(int, parts[3:7])
        except (TypeError, ValueError):
            continue
        if window_pid != pid:
            continue
        windows.append({
            "id": normalize_x11_window_id(parts[0]),
            "x": x,
            "y": y,
            "width": width,
            "height": height,
            "title": parts[8] if len(parts) > 8 else "",
        })
    inventory = InventoryResult([
        window for window in windows if window.get("id")
    ], complete=True)
    _x11_window_inventory_cache[pid] = inventory
    return inventory


def x11_windows_for_pid(pid):
    """Return the native-window inventory for ``pid`` as a plain list."""
    return list(x11_window_inventory_for_pid(pid))


def frame_distance(first, second):
    if not first or not second:
        return None
    return (
        abs(int(first.get("x", 0)) - int(second.get("x", 0)))
        + abs(int(first.get("y", 0)) - int(second.get("y", 0)))
        + abs(int(first.get("width", 0)) - int(second.get("width", 0)))
        + abs(int(first.get("height", 0)) - int(second.get("height", 0)))
    )


def x11_window_id_for_accessible(app, window):
    """Uniquely correlate one AX top-level window with one managed X11 window."""
    candidates = x11_windows_for_pid(node_pid(app))
    if not candidates:
        return None
    title_key = semantic_text_key(node_name(window))
    if title_key:
        title_matches = [
            candidate for candidate in candidates
            if semantic_text_key(candidate.get("title")) == title_key
        ]
        if len(title_matches) == 1:
            return title_matches[0]["id"]
        if title_matches:
            candidates = title_matches
    root_state = x11_root_window_state()
    active_id = root_state.get("activeWindow") if root_state else None
    if (
        state_contains(window, Atspi.StateType.ACTIVE)
        and active_id
        and sum(candidate["id"] == active_id for candidate in candidates) == 1
    ):
        return active_id
    bounds = safe(lambda: extents(window))
    ranked = []
    for candidate in candidates:
        distance = frame_distance(bounds, candidate)
        if distance is not None:
            ranked.append((distance, candidate["id"]))
    ranked.sort()
    if not ranked:
        return candidates[0]["id"] if len(candidates) == 1 else None
    if len(ranked) == 1:
        return ranked[0][1]
    if ranked[0][0] + 8 < ranked[1][0]:
        return ranked[0][1]
    return None


def opaque_window_instance_handle_for_id(app, window_id):
    pid = node_pid(app)
    generation = process_generation(pid)
    if not window_id or not pid or not generation:
        return None
    material = "{}|{}|{}".format(
        pid, generation, window_id
    ).encode("utf-8", "replace")
    return "w2:" + hashlib.sha256(material).hexdigest()[:32]


def opaque_window_instance_handle(app, window):
    return opaque_window_instance_handle_for_id(
        app, x11_window_id_for_accessible(app, window)
    )


def strong_window_instance_handle_inventory(app, windows=None):
    """Return only one-to-one AX-to-managed-window bindings."""
    if windows is None:
        windows = app_windows(app)
    if not inventory_complete(windows):
        return InventoryResult(
            complete=False,
            errors=inventory_errors(windows),
        )
    native_inventory = x11_window_inventory_for_pid(node_pid(app))
    if not native_inventory.complete:
        return InventoryResult(
            complete=False,
            errors=native_inventory.errors,
        )
    native = {
        item["id"]: item
        for item in native_inventory
        if item.get("id")
    }
    grouped = {}
    for index, window in windows:
        role = str(node_role(window) or "").strip().lower()
        if role not in AX_WINDOW_ROLES:
            continue
        window_id = x11_window_id_for_accessible(app, window)
        if window_id:
            grouped.setdefault(window_id, []).append((index, window))
    selected = {}
    for window_id, matches in grouped.items():
        if len(matches) == 1:
            selected[matches[0][0]] = opaque_window_instance_handle_for_id(
                app, window_id
            )
            continue
        native_title = semantic_text_key(
            (native.get(window_id) or {}).get("title")
        )
        exact = [
            (index, window)
            for index, window in matches
            if native_title
            and semantic_text_key(node_name(window)) == native_title
        ]
        if len(exact) == 1:
            selected[exact[0][0]] = opaque_window_instance_handle_for_id(
                app, window_id
            )
    return InventoryResult({
        index: handle
        for index, handle in selected.items()
        if str(handle or "").startswith("w2:")
    }.items(), complete=True)


def strong_window_instance_handles(app, windows=None):
    return dict(strong_window_instance_handle_inventory(app, windows))


def window_instance_handle(app, window_index, window, strong_handles=None):
    """Return a stable native handle, or a scoped weak inventory identity."""
    if strong_handles is None:
        strong_handles = strong_window_instance_handles(app)
    strong = strong_handles.get(window_index)
    if strong:
        return strong
    role = str(node_role(window) or "window").strip().lower().replace(" ", "-")
    return "pid{}/win{}/{}".format(node_pid(app) or 0, window_index, role)


def content_frame_id(screenshot):
    if not screenshot:
        return None
    return "content-v1-" + hashlib.sha256(
        screenshot.encode("ascii")
    ).hexdigest()


def relative_frame(node, window_bounds):
    bounds = extents(node)
    if bounds is None:
        return None
    if window_bounds is None:
        return bounds
    return frame(
        bounds["x"] - window_bounds["x"],
        bounds["y"] - window_bounds["y"],
        bounds["width"],
        bounds["height"],
    )


def iter_apps():
    try:
        root = desktop()
    except Exception as error:
        return InventoryResult(
            complete=False,
            errors=["desktop lookup failed: {}".format(type(error).__name__)],
        )
    if root is None:
        return InventoryResult(
            complete=False,
            errors=["desktop lookup returned no root"],
        )
    children = enumerate_children(root, "desktop")
    return InventoryResult(
        [app for _, app in children],
        complete=children.complete,
        errors=children.errors,
    )


def app_windows(app):
    windows = []
    children = enumerate_children(app, "application")
    complete = children.complete
    errors = list(children.errors)
    for index, child in children:
        try:
            direct_role = str(child.get_role_name() or "").lower()
        except Exception as error:
            complete = False
            errors.append(
                "application child {} role failed: {}".format(
                    index, type(error).__name__
                )
            )
            continue
        # Tests and alternate providers may supply a normalized role adapter.
        # The direct read above is still required so provider failures remain
        # distinguishable from an actual empty role.
        role = str(node_role(child) or direct_role).lower()
        bounds = extents(child)
        if role in {"frame", "window", "dialog", "alert"} or bounds is not None:
            windows.append((index, child))
    return InventoryResult(windows, complete=complete, errors=errors)


def x11_active_client_size():
    """Return the X11 active window's size without its invisible shadow.

    Used to arbitrate when AT-SPI states cannot tell windows apart. Returns
    ``(width, height)``, or None when unavailable.

    Size is the criterion because position and title are unreliable: GTK4
    reports accessibility extents with a (0,0) origin that does not match
    screen coordinates, and the secondary dialog in front may have no title.
    The X11 size includes the invisible shadow border drawn by GTK; after
    subtracting ``_GTK_FRAME_EXTENTS`` it matches the accessibility size
    exactly (for example 827x291 minus (61,61,55,67) gives 705x169).
    """
    try:
        active = subprocess.run(["xdotool", "getactivewindow"],
                                capture_output=True, text=True, timeout=3)
        if active.returncode != 0 or not active.stdout.strip():
            return None
        window_id = active.stdout.strip()
        info = subprocess.run(["xwininfo", "-id", window_id],
                              capture_output=True, text=True, timeout=3).stdout
        width = re.search(r"Width:\s+(\d+)", info)
        height = re.search(r"Height:\s+(\d+)", info)
        if not (width and height):
            return None
        size = [int(width.group(1)), int(height.group(1))]
        extents = subprocess.run(["xprop", "-id", window_id, "_GTK_FRAME_EXTENTS"],
                                 capture_output=True, text=True, timeout=3).stdout
        numbers = [int(n) for n in re.findall(r"\d+", extents)]
        if len(numbers) >= 4:
            left, right, top, bottom = numbers[:4]
            size[0] -= left + right
            size[1] -= top + bottom
        return tuple(size)
    except Exception:
        return None


def x11_preferred_window(candidates):
    """Return the one candidate that X11 considers frontmost, or None.

    The GNOME portal file dialog and its secondary confirmation ("file exists,
    replace?") are two windows of the same application with identical
    AT-SPI states: MODAL and VISIBLE, neither ACTIVE nor SHOWING. Taking the
    first modal window always yields the file chooser while the untitled
    confirmation in front holds focus, which stalls nested save and overwrite
    flows. The confirmation is present in the accessibility tree; only the
    window choice is wrong.

    Without a unique match this returns None and the regular order applies:
    an ambiguous arbiter must not force a decision.
    """
    target = x11_active_client_size()
    if target is None:
        return None
    matched = []
    for index, window in candidates:
        frame = safe(lambda: extents(window))
        if not frame:
            continue
        if (abs(int(frame["width"]) - target[0]) <= 2
                and abs(int(frame["height"]) - target[1]) <= 2):
            matched.append((index, window))
    return matched[0] if len(matched) == 1 else None


def window_candidate_summary(index, window):
    bounds = safe(lambda: extents(window))
    return {
        "index": index,
        "role": node_role(window),
        "title": node_name(window) or "",
        "active": state_contains(window, Atspi.StateType.ACTIVE),
        "showing": state_contains(window, Atspi.StateType.SHOWING),
        "visible": state_contains(window, Atspi.StateType.VISIBLE),
        "modal": state_contains(window, Atspi.StateType.MODAL),
        "bounds": bounds,
    }


def select_main_window(app):
    """Return the selected window plus the evidence used to choose it."""
    windows = app_windows(app)
    if not windows:
        raise RuntimeError(
            "No top-level AT-SPI window is available for " + node_name(app)
        )

    candidates = [window_candidate_summary(index, window)
                  for index, window in windows]

    def selected(index, window, reason):
        for candidate in candidates:
            candidate["selected"] = candidate["index"] == index
        return index, window, {
            "reason": reason,
            "candidateCount": len(candidates),
            "candidates": candidates,
        }

    modal = [
        (index, window)
        for index, window in windows
        if state_contains(window, Atspi.StateType.MODAL)
        and state_contains(window, Atspi.StateType.SHOWING)
    ]
    for index, window in modal:
        if state_contains(window, Atspi.StateType.ACTIVE):
            return selected(index, window, "active_modal")
    if len(windows) > 1:
        preferred = x11_preferred_window(windows)
        if preferred is not None:
            return selected(
                preferred[0], preferred[1],
                "x11_active_client_size_unique_match")
    if modal:
        return selected(modal[0][0], modal[0][1], "showing_modal")
    for index, window in windows:
        if state_contains(window, Atspi.StateType.ACTIVE):
            return selected(index, window, "active")
    for index, window in windows:
        if state_contains(window, Atspi.StateType.SHOWING):
            return selected(index, window, "showing")
    return selected(windows[0][0], windows[0][1], "first_available")


def select_window_by_handle(app, target_window_handle):
    """Resolve one exact logical window without foreground fallback."""
    target = str(target_window_handle or "").strip()
    if not target:
        return select_main_window(app)
    windows = app_windows(app)
    if not windows.complete:
        raise OperationFailure(
            "observation_incomplete: the application window inventory could not "
            "be read completely.",
            failure_code="observation_incomplete",
            failure_phase="observation",
            input_events_emitted=False,
            failure_subject="window",
        )
    strong_inventory = strong_window_instance_handle_inventory(app, windows)
    if not strong_inventory.complete:
        raise OperationFailure(
            "observation_incomplete: the native window inventory could not "
            "be read completely.",
            failure_code="observation_incomplete",
            failure_phase="observation",
            input_events_emitted=False,
            failure_subject="window",
        )
    strong_handles = dict(strong_inventory)
    matched = [
        (index, window)
        for index, window in windows
        if strong_handles.get(index) == target
    ]
    if not matched:
        raise OperationFailure(
            "stale_window_ref: the referenced logical window no longer exists.",
            failure_code="stale_window_ref",
            failure_phase="precondition",
            input_events_emitted=False,
        )
    if len(matched) != 1:
        raise OperationFailure(
            "observation_incomplete: the logical window identity is not unique.",
            failure_code="observation_incomplete",
            failure_phase="observation",
            input_events_emitted=False,
            failure_subject="window",
        )
    index, window = matched[0]
    return index, window, {
        "reason": "exact_logical_window",
        "candidateCount": len(windows),
        "candidates": [window_candidate_summary(i, item) for i, item in windows],
    }


def main_window(app):
    """Return ``(index, window)`` for the top-level window to operate on.

    Order: an active modal window; with several windows, a unique X11
    active-window size match (see x11_preferred_window()); a showing modal
    window; ACTIVE; SHOWING; and finally the first window.

    Modal windows rank above ACTIVE. AT-SPI has no equivalent of macOS's
    kAXFocusedWindowAttribute, so the window must be inferred from states,
    and applications such as LibreOffice report ACTIVE on neither frames nor
    dialogs. Selection would then fall through to SHOWING, where a modal
    dialog later in child order loses to the main window and the caller never
    sees the dialog. MODAL is the strongest signal: by definition it blocks
    interaction with the rest of the application.

    Several modal windows can coexist: a LibreOffice combo-box drop-down is a
    separate top-level window with MODAL, SHOWING and ACTIVE, above a dialog
    that is only MODAL and SHOWING. ACTIVE therefore breaks the tie among
    modal candidates so the drop-down options stay visible.
    """
    index, window, _selection = select_main_window(app)
    return index, window


def window_identity_set(app):
    """Return the identities of the application's current top-level windows.

    Used to detect whether an action opened or closed a window. An identity
    is ``(index, role, title)``. A newly mapped window may receive its real
    title later and so change identity; that does not affect the check,
    because it still counts as newly appeared and only the change matters.
    """
    identity = set()
    for index, window in app_windows(app):
        identity.add((index, node_role(window), node_name(window)))
    return identity


def window_identity_inventory(app, windows=None):
    if windows is None:
        windows = app_windows(app)
    identities = {
        (index, node_role(window), node_name(window))
        for index, window in windows
    }
    return InventoryResult(
        identities,
        complete=inventory_complete(windows),
        errors=inventory_errors(windows),
    )
