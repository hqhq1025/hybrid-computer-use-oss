"""Dispatch of Provider V2 operations to AX and GUI handlers."""

import math
import time
from .foundation import (
    Atspi,
    DEFAULT_TEXT_LIMIT,
    MAX_DEPTH,
    MAX_ELEMENTS,
    OperationFailure,
    extents,
    node_name,
    node_pid,
    node_role,
    process_generation,
    provider_bus_name,
    provider_object_path,
    safe,
    state_value,
)

from .ax_actions import (
    WHEEL_CLICKS_PER_PAGE,
    find_editable_text,
    focus_target_element,
    insert_text_outcome_on_node,
    parse_key,
    resolve_combo_editor,
    scroll_element,
    send_key,
    send_text,
    set_element_value_outcome,
)

from .ax_select import perform_select
from .gui_input import send_mouse_click
from .input_primitives import require_window_focus, window_in_foreground

from .ax_node_properties import (
    parse_text_limit,
    positive_int,
)

from .x11_windows import (
    _x11_window_inventory_cache,
    main_window,
    select_window_by_handle,
    window_identity_set,
    x11_root_window_state,
)

from .app_resolver import app_window_inventory, list_apps_inventory, windowless_snapshot
from .element_resolver import (
    find_element_at_snapshot_path,
    resolve_semantic_target,
)
from .window_resolver import (
    perform_ax_window_action,
    resolve_window_action_target,
)

from .snapshot import (
    build_snapshot,
)

from .gui_capture import (
    capture_window_pixels,
    persistent_pixel_change,
    pixel_change_note,
)
from .gui_dispatch import finalize_gui_response, perform_gui
from . import action_evidence as action_evidence_state
from .phase_timing import lap
from . import ui_events

from .actionability import (
    action_only_sets_state,
    current_geometry,
    do_action_by_index,
    point_hits_element,
    preferred_action_index,
    require_coordinate_delivery,
    require_stable_coordinate_geometry,
    screen_point,
    window_relative,
)

from .atspi_inventory import (
    resolve_operation_app,
    wait_for_ui_to_settle,
)

A11Y_CHANNEL = "[a11y]"
GUI_CHANNEL = "[gui]"
KEY_CHANNEL = "[keyboard]"
SEMANTIC = "[semantic] "
SYNTHESIS = "[synthesis] "

_TRANSIENT_WINDOW_ROLES = {
    "alert",
    "alert dialog",
    "dialog",
    "file chooser",
    "color chooser",
}


def _verify_expected_window(window, expectation):
    if not expectation:
        return
    expected_role = str(expectation.get("role") or "").strip().lower()
    current_role = str(node_role(window) or "").strip().lower()
    common_matches = (
        provider_bus_name(window)
        == str(expectation.get("providerBusName") or "").strip()
        and process_generation(node_pid(window))
        == str(expectation.get("processGeneration") or "").strip()
    )
    transient = (
        expected_role in _TRANSIENT_WINDOW_ROLES
        or current_role in _TRANSIENT_WINDOW_ROLES
    )
    transient_matches = (
        provider_object_path(window)
        == str(expectation.get("providerObjectPath") or "").strip()
        and current_role == expected_role
    )
    if not common_matches or (transient and not transient_matches):
        raise OperationFailure(
            "stale_target: the batch logical window changed before keyboard "
            "input; no input was emitted.",
            failure_code="stale_target",
            failure_phase="precondition",
            input_events_emitted=False,
            failure_subject="window",
        )


from .ax_target_notes import (
    PREFER_ELEMENT_INDEX,
    UNVERIFIED_SEMANTIC,
    UNVERIFIED_SYNTHESIS,
    addressing_channel,
    resolved_note,
)
from .ax_snapshot_diagnostics import (
    content_host_diagnostic,
    is_dropdown_item,
    snapshot_diagnostics,
)
from .ax_transition_evidence import (
    external_desktop_transition_after_ax_mutation,
)


def perform_provider_request(operation_kind, payload):
    """Dispatch one Provider V2 operation that validate_request() accepted."""
    action_intent = None
    if operation_kind == "resolve_and_act":
        action_intent = payload.get("intent")
    elif operation_kind == "window_action":
        action_intent = payload.get("action")
    return dispatch_provider_operation(
        payload,
        operation_kind=operation_kind,
        action_intent=action_intent,
    )


def dispatch_provider_operation(
    operation,
    operation_kind,
    action_intent=None,
):
    action_input = operation.get("input") or {}
    observation = operation.get("observation") or {}
    action_evidence_state.reset()
    # Native-window facts are stable only within one MCP operation. Reusing a
    # prior call's inventory can hide a newly opened dialog or retain a closed
    # window, so each operation starts with a fresh bounded inventory while
    # sharing that sample across its internal resolution steps.
    _x11_window_inventory_cache.clear()
    # A pid can be reused by a new process between operations; its start
    # time is what tells the two apart, so it is read again per operation.
    process_generation.cache_clear()
    if operation_kind == "observe_apps":
        inventory = list_apps_inventory()
        return {
            "ok": True,
            "text": inventory["text"],
            "apps": inventory["apps"],
            "notes": inventory["notes"],
            "inventory_issues": inventory.get("inventory_issues", []),
        }
    if operation_kind == "window_list":
        return app_window_inventory(
            operation.get("app", ""),
            app_identity=operation.get("app_identity"),
            target_window_handle=operation.get("window_handle"),
        )
    if operation_kind == "desktop_execute_batch":
        return _execute_desktop_batch(operation)
    if operation_kind == "observe":
        return _observe_operation(operation, observation)
    if (
        operation_kind == "resolve_and_act"
        and action_intent == "resolve_target"
    ):
        return _resolve_target_operation(operation)
    return _perform_ax_action(
        operation,
        operation_kind,
        action_intent,
        action_input,
        observation,
    )


def _execute_desktop_batch(operation):
    try:
        response = perform_gui(
            operation.get("actions"),
            operation.get("settle_ms", 800),
            operation.get("frame_id"),
        )
    except Exception as exc:
        response = {
            "ok": False,
            "error": str(exc),
            "dispatched": 0,
            "input_events_emitted": False,
            "input_actions_dispatched": 0,
            "effect_unknown": False,
            "quiescent": None,
        }
    return finalize_gui_response(response)


def _observe_operation(operation, observation):
    snapshot = build_snapshot(
        operation.get("app", ""),
        text_limit=parse_text_limit(observation.get("text_limit"), DEFAULT_TEXT_LIMIT),
        max_tree_nodes=positive_int(observation.get("max_tree_nodes"), MAX_ELEMENTS),
        max_tree_depth=positive_int(observation.get("max_tree_depth"), MAX_DEPTH),
        prune=observation.get("prune", True),
        boxes=bool(observation.get("boxes", False)),
        known_refs=operation.get("known_refs"),
        # None selects the default policy, which includes a screenshot.
        # Find and assertion requests pass False: they query or assert
        # rather than observe, and assertions poll, so attaching a
        # screenshot (about 5120 tokens) to every poll would be costly.
        include_screenshot=observation.get("include_screenshot"),
        screenshot_scope=observation.get("screenshot_scope", "window"),
        app_identity=operation.get("app_identity"),
        target_window_handle=operation.get("window_handle"),
        # An unchanged window may return its previous tree; see
        # tree_reuse. Post-action snapshots below always walk.
        reuse_tree=True,
    )
    response = {"ok": True, "snapshot": snapshot}
    lap("observe_snapshot")
    # Modal notes come first: they determine how the rest of the tree
    # should be read.
    elements = snapshot.get("elements") or []
    shell_notes = snapshot_diagnostics(elements)
    # The two diagnostics describe degrees of the same problem and are
    # never emitted together: snapshot_diagnostics covers a tree reduced to
    # its window frame, content_host_diagnostic a complete shell whose
    # content plane is missing. Once the former fires, the latter would
    # only repeat its conclusion.
    diagnostics = snapshot.pop("modalNotes", []) + shell_notes
    if not shell_notes:
        diagnostics += content_host_diagnostic(elements)
    if diagnostics:
        response["notes"] = diagnostics
    return response


def _resolve_target_operation(operation):
    """Resolve a semantic target without acting on it."""
    app = resolve_operation_app(operation)
    target_window_handle = operation.get("window_handle")
    if target_window_handle:
        window_index, window, _window_selection = select_window_by_handle(
            app, target_window_handle
        )
        _verify_expected_window(
            window,
            operation.get("expected_window"),
        )
    else:
        window_index, window = main_window(app)
    bounds = extents(window)
    semantic_target = operation.get("target")
    if not isinstance(semantic_target, dict):
        raise OperationFailure(
            "semantic_target_invalid reason=missing_target; no input was emitted.",
            failure_code="semantic_target_invalid",
            failure_phase="precondition",
            input_events_emitted=False,
        )
    (
        _element,
        element_record,
        _semantic_note,
        _window,
        _bounds,
    ) = resolve_semantic_target(
        app,
        window_index,
        window,
        bounds,
        semantic_target,
        operation.get("known_refs"),
        operation,
    )
    return {
        "ok": True,
        "resolvedElement": element_record,
    }


def _perform_ax_action(
    operation,
    operation_kind,
    action_intent,
    action_input,
    observation,
):
    """Resolve the target, emit one AX action, and observe its effect.

    The order is fixed: resolve, sample the before-state, act, then settle
    and read back. Each lap() marks the end of one of those phases.
    """
    app = resolve_operation_app(operation)
    # One xprop round: the managed client list and the active window are all
    # the external-transition check reads. Window geometry is not needed.
    root_window_before = x11_root_window_state()
    lap("resolve_app_and_root_window")
    (
        element,
        element_record,
        semantic_note,
        window,
        bounds,
    ) = _resolve_action_target(app, operation, operation_kind, action_input)
    lap("resolve_target")
    pixel_bounds = bounds
    # Capture pixels once before the action and twice after it; only a change
    # present in both later captures counts. Semantic targets are resolved
    # across all visible app-owned windows first, so pixel capture and later
    # focus actions bind to the window that actually matched rather than the
    # one main_window() picked before resolution.
    # Without a post-action snapshot there is no pixel note, so skip the sample.
    pixels_before = (
        safe(lambda: capture_window_pixels(pixel_bounds))
        if observation.get("capture_snapshot") is not False
        else None
    )
    lap("pixels_before")
    # Refresh the geometry from the live node right after resolving it;
    # otherwise semantic actions would use the live node while synthesized
    # input used the snapshot geometry.
    element_record, moved_note = current_geometry(element, element_record, bounds)
    # Record the top-level window set before acting. wait_for_ui_to_settle()
    # compares against it to detect opened or closed windows and decide
    # whether to wait longer for focus to settle.
    windows_before = safe(lambda: window_identity_set(app))
    lap("geometry_and_windows_before")
    # Dispatch queued AT-SPI events now, so the settle wait counts only events
    # the application emits after the input.
    ui_events.pump()
    # Every action reports the route it actually took and whether the result
    # was verified. A fresh accessibility tree in the response looks like
    # confirmation but is only a snapshot, not evidence that the action took
    # effect.
    notes = []
    action_evidence = _new_action_evidence()
    action_evidence_state.replace(action_evidence)
    _record_target_notes_and_state(
        notes,
        action_evidence,
        element,
        element_record,
        semantic_note,
        moved_note,
    )

    if operation_kind == "window_action":
        windowless_response = _perform_window_action(
            action_intent, app, window, element_record, notes, action_evidence
        )
        if windowless_response is not None:
            return windowless_response
    elif action_intent == "click":
        element_record = _perform_click(
            action_input,
            window,
            bounds,
            element,
            element_record,
            notes,
            action_evidence,
        )
    elif action_intent == "scroll":
        element_record = _perform_scroll(
            action_input,
            window,
            bounds,
            element,
            element_record,
            notes,
            action_evidence,
        )
    elif action_intent == "type":
        _perform_type(
            action_input, window, element, element_record, notes, action_evidence
        )
    elif action_intent == "keypress":
        _perform_keypress(
            action_input, window, element, element_record, notes, action_evidence
        )
    elif action_intent == "set_value":
        _perform_set_value(
            action_input, window, element, element_record, notes, action_evidence
        )
    elif action_intent == "select":
        to_element = _resolve_select_end(app, operation, action_input, window, bounds)
        perform_select(
            action_input, window, element, element_record, to_element,
            notes, action_evidence,
        )
    else:
        raise RuntimeError(
            'unsupportedProviderAction("{}")'.format(action_intent)
        )

    lap("action")
    # The input may have opened or closed native windows. Reads after this
    # point (settle, post-action snapshot, recovery) must not bind windows
    # against the native inventory sampled before the input.
    _x11_window_inventory_cache.clear()
    if observation.get("capture_snapshot") is False:
        return _delivery_only_response(action_evidence, element_record, notes)
    return _observe_after_action(
        operation,
        operation_kind,
        observation,
        app,
        bounds,
        element_record,
        notes,
        action_evidence,
        root_window_before,
        windows_before,
        pixels_before,
    )


def _resolve_action_target(app, operation, operation_kind, action_input):
    """Return (element, element_record, semantic_note, window, bounds).

    A target names either a snapshot element or a semantic locator, never
    both. Every failure here happens before any input is emitted.
    """
    element_record = operation.get("element")
    semantic_target = operation.get("target")
    semantic_note = None
    if element_record is not None and semantic_target is not None:
        raise OperationFailure(
            "semantic_target_invalid reason=both_numeric_and_semantic_targets; "
            "no input was emitted.",
            failure_code="semantic_target_invalid",
            failure_phase="precondition",
            input_events_emitted=False,
        )
    if semantic_target is not None and operation_kind == "window_action":
        (
            element,
            element_record,
            window,
            bounds,
        ) = resolve_window_action_target(app, semantic_target)
    else:
        target_window_handle = operation.get("window_handle")
        if target_window_handle:
            window_index, window, _window_selection = select_window_by_handle(
                app, target_window_handle
            )
            _verify_expected_window(
                window,
                operation.get("expected_window"),
            )
        else:
            window_index, window = main_window(app)
        bounds = operation.get("window_bounds") or extents(window)
    if semantic_target is not None and operation_kind != "window_action":
        (
            element,
            element_record,
            semantic_note,
            window,
            bounds,
        ) = resolve_semantic_target(
            app,
            window_index,
            window,
            bounds,
            semantic_target,
            operation.get("known_refs"),
            operation,
        )
    elif semantic_target is None and action_input.get("window_centre"):
        element = None
    elif semantic_target is None:
        element = find_element_at_snapshot_path(app, element_record)
        if element_record is not None and element is None:
            raise OperationFailure(
                "stale_element_ref reason=path_changed_or_element_disappeared "
                "no input was emitted. Refresh AX mode=observe/find and use the "
                "newly returned public ref.",
                failure_code="stale_element_ref",
                failure_phase="precondition",
                input_events_emitted=False,
            )
    return element, element_record, semantic_note, window, bounds


def _resolve_select_end(app, operation, action_input, window, bounds):
    """Resolve the select range end (to_ref) in the target's window, or None."""
    to_target = action_input.get("to_target")
    if to_target is None:
        return None
    window_path = provider_object_path(window)
    window_index = None
    for index in range(int(safe(app.get_child_count, 0) or 0)):
        child = safe(lambda i=index: app.get_child_at_index(i))
        if child is not None and provider_object_path(child) == window_path:
            window_index = index
            break
    if window_index is None:
        raise OperationFailure(
            "stale_element_ref reason=select_window_disappeared; no input was "
            "emitted.",
            failure_code="stale_element_ref",
            failure_phase="precondition",
            input_events_emitted=False,
        )
    to_element, _record, _note, to_window, _bounds = resolve_semantic_target(
        app, window_index, window, bounds, to_target,
        operation.get("known_refs"), operation,
    )
    if provider_object_path(to_window) != window_path:
        raise OperationFailure(
            "select_invalid_range: to_ref is in a different window than ref; "
            "no input was emitted.",
            failure_code="invalid_argument",
            failure_phase="precondition",
            input_events_emitted=False,
        )
    return to_element


def _new_action_evidence():
    return {
        "route": "unknown",
        "geometry_sampled": False,
        "geometry_stable": None,
        "window_focus_verified": None,
        "target_focus_verified": None,
        "point_owner_verified": None,
        "enabled": None,
        "editable": None,
        "focus_verified": None,
        "editable_verified": None,
        "readback_sampled": False,
        "readback_verified": None,
        "input_emitted": False,
    }


def _record_target_notes_and_state(
    notes,
    action_evidence,
    element,
    element_record,
    semantic_note,
    moved_note,
):
    if semantic_note:
        notes.append(semantic_note)
    # The resolved-target note precedes all action notes: first what was
    # targeted, then what was done.
    resolved = resolved_note(element, element_record)
    if resolved:
        notes.append(A11Y_CHANNEL + " " + resolved)
    if moved_note:
        # A11Y_CHANNEL has no trailing space; without the separator this would
        # render as `[a11y]The element MOVED`.
        notes.append(A11Y_CHANNEL + " " + moved_note)
    if element is not None:
        # A present ENABLED/EDITABLE state is useful positive evidence. Its
        # absence is not a reliable negative across AT-SPI providers (GTK file
        # items and several LibreOffice nodes omit these states while still
        # accepting input), so preserve that case as unknown.
        if state_value(element, Atspi.StateType.ENABLED) is True:
            action_evidence["enabled"] = True
        if state_value(element, Atspi.StateType.EDITABLE) is True:
            action_evidence["editable"] = True
        if state_value(element, Atspi.StateType.DEFUNCT) is True:
            raise OperationFailure(
                "stale_element_ref reason=target_defunct; no input was emitted.",
                failure_code="stale_element_ref",
                failure_phase="precondition",
                input_events_emitted=False,
            )


def _perform_window_action(
    action_intent, app, window, element_record, notes, action_evidence
):
    """Run a native window command; return the response if no window is left."""
    window_result = perform_ax_window_action(
        action_intent, app, window, element_record
    )
    action_evidence["route"] = "native_window_command"
    action_evidence["input_emitted"] = True
    action_evidence["readback_sampled"] = True
    action_evidence["readback_verified"] = True
    notes.append(window_result["note"])
    if window_result["windowless"]:
        return {
            "ok": True,
            "snapshot": windowless_snapshot(
                window_result["app_name"],
                window_result["app_pid"],
                window_result["closed_handle"],
            ),
            "input_events_emitted": True,
            "desktop_state_mutated": True,
            "mutation_unknown": False,
            "actionEvidence": action_evidence,
            "notes": notes,
        }
    return None


def _perform_click(
    action_input,
    window,
    bounds,
    element,
    element_record,
    notes,
    action_evidence,
):
    """Click by the requested method and return the refreshed element record."""
    click_method = (action_input.get("click_method") or "auto").lower()
    click_count = max(1, int(action_input.get("click_count", 1) or 1))
    if click_method == "accessibility":
        _accessibility_click(
            action_input, element, click_count, notes, action_evidence
        )
    elif click_method == "app_post":
        raise RuntimeError("click_method 'app_post' is not supported on Linux")
    elif click_method == "sky_click":
        raise RuntimeError("click_method 'sky_click' is not supported on Linux")
    elif click_method == "global":
        element_record = _global_click(
            action_input,
            window,
            bounds,
            element,
            element_record,
            click_count,
            notes,
            action_evidence,
        )
    elif click_method == "auto":
        element_record = _auto_click(
            action_input,
            window,
            bounds,
            element,
            element_record,
            click_count,
            notes,
            action_evidence,
        )
    else:
        raise RuntimeError("Invalid click_method '{}'".format(click_method))
    return element_record


def _accessibility_click(
    action_input, element, click_count, notes, action_evidence
):
    if element is None:
        raise RuntimeError("click_method 'accessibility' requires a target ref")
    if action_input.get("mouse_button", "left") != "left":
        raise RuntimeError(
            "click_method 'accessibility' only supports mouse_button 'left'"
        )
    if click_count != 1:
        raise RuntimeError(
            "click_method 'accessibility' cannot represent click_count > 1; "
            "use click_method 'auto' or 'global' for a real multi-click"
        )
    if not do_action_by_index(element, preferred_action_index(element)):
        raise RuntimeError(
            "click_method 'accessibility' could not click the requested element"
        )
    action_evidence["route"] = "semantic_action"
    action_evidence["input_emitted"] = True
    notes.append(
        A11Y_CHANNEL
        + SEMANTIC
        + "Invoked the element's AT-SPI accessibility action. "
        + UNVERIFIED_SEMANTIC
    )


def _global_click(
    action_input,
    window,
    bounds,
    element,
    element_record,
    click_count,
    notes,
    action_evidence,
):
    element_record, _ = require_stable_coordinate_geometry(
        element, element_record, bounds
    )
    x, y = screen_point(
        bounds,
        element_record,
        action_input.get("x"),
        action_input.get("y"),
    )
    require_coordinate_delivery(window, x, y, "click")
    send_mouse_click(
        x, y, action_input.get("mouse_button", "left"), click_count
    )
    action_evidence["route"] = "coordinate_synthesis"
    action_evidence["geometry_sampled"] = True
    action_evidence["geometry_stable"] = True
    action_evidence["window_focus_verified"] = True
    action_evidence["point_owner_verified"] = True
    action_evidence["focus_verified"] = True
    action_evidence["input_emitted"] = True
    notes.append(
        addressing_channel(element_record)
        + SYNTHESIS
        + "Synthesized {} coordinate click(s) at ({:.0f}, {:.0f}) in "
        "window-relative pixels after bringing the window to the foreground. {}{}"
        .format(
            click_count,
            *window_relative(bounds, x, y),
            UNVERIFIED_SYNTHESIS,
            # Omit PREFER_ELEMENT_INDEX when the caller already named
            # an element: this route is usually taken because the
            # semantic action failed (several applications report
            # AT-SPI action success without any effect), so
            # coordinates are the only remaining option.
            "" if element_record else PREFER_ELEMENT_INDEX,
        )
    )
    return element_record


def _auto_click(
    action_input,
    window,
    bounds,
    element,
    element_record,
    click_count,
    notes,
    action_evidence,
):
    """Prefer the element's AT-SPI action; fall back to a coordinate click."""
    handled = False
    state_only_note = ""
    if (
        element is not None
        and action_input.get("mouse_button", "left") == "left"
        and click_count == 1
        and not is_dropdown_item(element, window)
    ):
        action_index = preferred_action_index(element)
        if action_only_sets_state(element, action_index):
            pointer_record, refused = _pointer_click_for_state_only_action(
                window, bounds, element, element_record, notes,
                action_evidence)
            if pointer_record is not None:
                return pointer_record
            state_only_note = (
                " This element's only AT-SPI action is a toolkit toggle that "
                "changes its checked state without the click the application "
                "listens for, and a real pointer click was not safe ({}), so "
                "the application may not respond even though the state "
                "changes.".format(refused)
            )
        handled = do_action_by_index(element, action_index)
    if handled:
        action_evidence["route"] = "semantic_action"
        action_evidence["input_emitted"] = True
        notes.append(
            A11Y_CHANNEL
            + SEMANTIC
            + "Invoked the element's AT-SPI accessibility action. "
            + UNVERIFIED_SEMANTIC
            + state_only_note
        )
    else:
        if element is not None and node_role(element) == "menu":
            raise OperationFailure(
                "semantic_action_unavailable role='menu' name={!r}; "
                "the menu exposed no usable AT-SPI action, and automatic "
                "coordinate fallback is disabled because duplicated or stale "
                "menu geometry can activate a different command. No input was "
                "emitted. Open the menu with keyboard navigation or use gui "
                "after observing a fresh full-desktop frame.".format(
                    node_name(element)
                ),
                failure_code="semantic_action_unavailable",
                failure_phase="precondition",
                input_events_emitted=False,
            )
        element_record, _ = require_stable_coordinate_geometry(
            element, element_record, bounds
        )
        x, y = screen_point(
            bounds,
            element_record,
            action_input.get("x"),
            action_input.get("y"),
        )
        require_coordinate_delivery(window, x, y, "click")
        send_mouse_click(
            x,
            y,
            action_input.get("mouse_button", "left"),
            click_count,
        )
        action_evidence["route"] = "coordinate_synthesis"
        action_evidence["geometry_sampled"] = True
        action_evidence["geometry_stable"] = True
        action_evidence["window_focus_verified"] = True
        action_evidence["point_owner_verified"] = True
        action_evidence["focus_verified"] = True
        action_evidence["input_emitted"] = True
        if is_dropdown_item(element, window):
            # Explain why the semantic route was skipped. Otherwise the
            # note reads as if the element had no action; it has one,
            # but that action closes the drop-down without committing
            # the value and leaves nothing to verify.
            reason = (
                "This element is an item inside a drop-down popup, where the "
                "AT-SPI action closes the popup without committing the value "
                "and leaves nothing to verify against, so this went straight to "
                "{} coordinate click(s) at ({:.0f}, {:.0f}) in window-relative "
                "pixels. ".format(
                    click_count, *window_relative(bounds, x, y))
            )
        else:
            reason = (
                "No usable AT-SPI action was available, so this fell back to a "
                "{} coordinate click(s) at ({:.0f}, {:.0f}) in window-relative pixels "
                "after bringing the window to the foreground. ".format(
                    click_count, *window_relative(bounds, x, y)
                )
            )
        notes.append(
            addressing_channel(element_record)
            + SYNTHESIS
            + reason
            + UNVERIFIED_SYNTHESIS
        )
    return element_record


def _pointer_click_for_state_only_action(
    window, bounds, element, element_record, notes, action_evidence
):
    """Click a state-only-action element with the pointer when that is safe.

    Return ``(record, None)`` after the click, or ``(None, reason)`` when a
    guard refused it before any input; the caller then keeps the semantic
    action. The guards are those of every coordinate click (stable live
    geometry, a visible on-screen point, the window in the foreground and
    owning the point) plus a hit test that the point reaches this element.
    """
    # The pointer route never raises the window: a state-only action is the
    # fallback, and taking focus from the user to avoid it is not worth it.
    if not window_in_foreground(window):
        return None, "the window is not in the foreground"
    evidence_before = dict(action_evidence)
    try:
        record, _ = require_stable_coordinate_geometry(
            element, element_record, bounds)
        x, y = screen_point(bounds, record, None, None)
        require_coordinate_delivery(window, x, y, "click")
        hits = point_hits_element(window, element, x, y)
    except (OperationFailure, RuntimeError) as refused:
        action_evidence.clear()
        action_evidence.update(evidence_before)
        reason = getattr(refused, "failure_code", None) or str(refused)
        return None, reason.split(":")[0].strip()
    if not hits:
        action_evidence.clear()
        action_evidence.update(evidence_before)
        return None, "another element is under its centre"
    send_mouse_click(x, y, "left", 1)
    action_evidence.update(
        route="coordinate_synthesis",
        geometry_sampled=True,
        geometry_stable=True,
        window_focus_verified=True,
        point_owner_verified=True,
        focus_verified=True,
        input_emitted=True,
    )
    notes.append(
        A11Y_CHANNEL
        + SYNTHESIS
        + "Clicked the element with the pointer at ({:.0f}, {:.0f}) in "
        "window-relative pixels after checking that the window is in front "
        "and the point is on this element. Its only AT-SPI action is a "
        "toolkit toggle that changes the checked state without the click the "
        "application listens for, so a real click was needed. {}".format(
            *window_relative(bounds, x, y), UNVERIFIED_SYNTHESIS)
    )
    return record, None


def _perform_scroll(
    action_input,
    window,
    bounds,
    element,
    element_record,
    notes,
    action_evidence,
):
    direction = action_input.get("direction", "down")
    pages = action_input.get("pages", 1)
    point = None
    if action_input.get("window_centre") and bounds is not None:
        point = (
            bounds["x"] + bounds["width"] / 2,
            bounds["y"] + bounds["height"] / 2,
        )
    elif element_record and element_record.get("frame"):
        element_record, _ = require_stable_coordinate_geometry(
            element, element_record, bounds
        )
        point = screen_point(bounds, element_record, None, None)
    uses_wheel = point is not None and direction not in ("left", "right")
    if uses_wheel:
        require_coordinate_delivery(window, point[0], point[1], "scroll")
    else:
        require_window_focus(window, "scroll")
    route = scroll_element(direction, pages, point)
    if route == "wheel":
        action_evidence["route"] = "coordinate_synthesis"
        action_evidence["geometry_sampled"] = True
        action_evidence["geometry_stable"] = True if element is not None else None
        action_evidence["window_focus_verified"] = True
        action_evidence["point_owner_verified"] = True
        action_evidence["focus_verified"] = True
        action_evidence["input_emitted"] = True
        wx, wy = window_relative(bounds, *point)
        notes.append(
            A11Y_CHANNEL
            + SYNTHESIS
            + "Scrolled with {} wheel notch(es) at ({:.0f}, {:.0f}) in "
            "window-relative pixels — {}. Two caveats. First, the wheel acts on the "
            "scrollable ancestor under that point, which is not necessarily the "
            "element you named. Second, one page is APPROXIMATED as {} notches; "
            "the real distance is whatever the application's scroll settings say, "
            "so do not treat pages=1 as exactly one Page_Down. This also moves the "
            "real pointer. {}".format(
                int(math.ceil(float(pages or 1))) * WHEEL_CLICKS_PER_PAGE,
                wx,
                wy,
                # State where the position came from: without a target ref
                # the wheel lands at the window centre, and claiming a
                # targeted scroll would mislead every later inference.
                "no target ref was given, so this is the centre of the window "
                "— the wheel lands on whatever scrollable sits there"
                if action_input.get("window_centre")
                else "the position came from the target ref, so this scroll IS "
                     "targeted",
                WHEEL_CLICKS_PER_PAGE,
                UNVERIFIED_SYNTHESIS,
            )
        )
    else:
        action_evidence["route"] = "keyboard_synthesis"
        action_evidence["window_focus_verified"] = True
        action_evidence["focus_verified"] = True
        action_evidence["input_emitted"] = True
        notes.append(
            KEY_CHANNEL
            + SYNTHESIS
            + "Scrolled by synthesizing page keys after bringing the window to the "
            "foreground. NOTE: no target ref directed this scroll — the keys "
            "go to whatever widget currently holds focus inside the window. This "
            "route is used when the element has no usable geometry, and for "
            "horizontal scrolling, whose wheel buttons this project has not "
            "measured. If the wrong region scrolled, focus that region first "
            "(click it) and scroll again. {}".format(UNVERIFIED_SYNTHESIS)
        )
    return element_record


def _perform_type(
    action_input, window, element, element_record, notes, action_evidence
):
    element, combo_editor = resolve_combo_editor(element, element_record)
    # A caller-supplied element_index is a real target, not a hint. Focus and
    # verify that exact node before any keyboard fallback.
    if element_record is not None:
        if not combo_editor:
            focus_target_element(
                window, element, element_record, "targeted type_text")
            action_evidence["window_focus_verified"] = True
            action_evidence["target_focus_verified"] = True
        (
            write_outcome,
            before_chars,
            after_chars,
            semantic_input_emitted,
        ) = insert_text_outcome_on_node(element, action_input.get("text", ""))
    else:
        (
            write_outcome,
            before_chars,
            after_chars,
            semantic_input_emitted,
        ) = insert_text_outcome_on_node(
            find_editable_text(window), action_input.get("text", "")
        )
    if semantic_input_emitted:
        action_evidence["route"] = "editable_text_api"
        action_evidence["input_emitted"] = True
        action_evidence["readback_sampled"] = True
    if write_outcome == "effect_unknown":
        raise OperationFailure(
            "effect_unknown: the AT-SPI editable-text API may have changed "
            "the control, but exact readback was unavailable; automatic "
            "keyboard fallback was not attempted.",
            failure_code="effect_unknown",
            failure_phase="readback",
            input_events_emitted=semantic_input_emitted,
            failure_subject="element",
        )
    if write_outcome == "verified":
        action_evidence["route"] = "editable_text_api"
        action_evidence["editable_verified"] = True
        action_evidence["readback_verified"] = True
        action_evidence["readback_sampled"] = True
        action_evidence["input_emitted"] = True
        notes.append(
            A11Y_CHANNEL
            + SEMANTIC
            + "Wrote the text through the AT-SPI editable-text API and read it back "
            "to confirm it landed ({} -> {} characters). This confirms the control "
            "changed; if the control belongs to a dialog, the application may still "
            "require OK/Apply before the value takes effect.".format(
                before_chars, after_chars
            )
        )
    else:
        if combo_editor:
            raise OperationFailure(
                "The combobox editor did not confirm the text insertion; "
                "keyboard fallback was not attempted.",
                failure_code="effect_unknown" if semantic_input_emitted else "unsupported_action",
                failure_phase="readback" if semantic_input_emitted else "precondition",
                input_events_emitted=semantic_input_emitted,
                failure_subject="element")
        if element_record is None:
            require_window_focus(window, "type")
            action_evidence["window_focus_verified"] = True
        else:
            # The semantic write attempt may itself move focus. Re-establish
            # and verify the exact target immediately before global synthesis.
            focus_target_element(
                window, element, element_record,
                "targeted type_text keyboard fallback")
            action_evidence["window_focus_verified"] = True
            action_evidence["target_focus_verified"] = True
        send_text(action_input.get("text", ""))
        action_evidence["route"] = "keyboard_synthesis"
        action_evidence["focus_verified"] = True
        action_evidence["editable_verified"] = None
        action_evidence["input_emitted"] = True
        notes.append(
            KEY_CHANNEL
            + SYNTHESIS
            + "The AT-SPI editable-text write did not land, so this fell back to "
            "global key synthesis after {}. {}".format(
                "verifying the requested element held focus"
                if element_record is not None
                else "bringing the window to the foreground",
                UNVERIFIED_SYNTHESIS,
            )
        )


def _perform_keypress(
    action_input, window, element, element_record, notes, action_evidence
):
    # Parse the key before taking focus, so a misspelled key fails without
    # first pulling the user's window to the foreground.
    parse_key(action_input.get("key", ""))
    if element_record is not None:
        focus_target_element(
            window, element, element_record, "targeted press_key")
        action_evidence["window_focus_verified"] = True
        action_evidence["target_focus_verified"] = True
    else:
        require_window_focus(window, "keypress")
        action_evidence["window_focus_verified"] = True
    send_key(action_input.get("key", ""))
    action_evidence["route"] = "keyboard_synthesis"
    action_evidence["focus_verified"] = True
    action_evidence["input_emitted"] = True
    notes.append(
        KEY_CHANNEL
        + SYNTHESIS
        + "Synthesized '{}' after {}. {}".format(
            action_input.get("key", ""),
            "verifying the requested element held focus"
            if element_record is not None
            else "bringing the window to the foreground",
            UNVERIFIED_SYNTHESIS,
        )
    )


def _perform_set_value(
    action_input, window, element, element_record, notes, action_evidence
):
    if element is None:
        raise RuntimeError("unknown target ref")
    element, combo_editor = resolve_combo_editor(element, element_record)
    value = action_input.get("value", "")
    value_outcome = set_element_value_outcome(element, value)
    if value_outcome == "effect_unknown":
        action_evidence["route"] = "value_api"
        action_evidence["input_emitted"] = True
        action_evidence["readback_sampled"] = True
        raise OperationFailure(
            "effect_unknown: the AT-SPI value API may have changed the "
            "control, but exact readback was unavailable; automatic "
            "keyboard fallback was not attempted.",
            failure_code="effect_unknown",
            failure_phase="readback",
            input_events_emitted=True,
            failure_subject="element",
        )
    if value_outcome != "verified":
        if combo_editor:
            raise OperationFailure(
                "The combobox editor did not confirm the value write; keyboard "
                "fallback was not attempted. Read the control before retrying.",
                failure_code="effect_unknown", failure_phase="readback",
                input_events_emitted=True, failure_subject="element")
        # A refused semantic write does not mean the control is read-only:
        # Blink text inputs, for example, implement neither the AT-SPI
        # Value nor the EditableText interface. As click and type_text do,
        # fall back to synthesis: focus, select all, then type. The note
        # must state that this path has no read-back confirmation and that
        # keyboard synthesis is global, so delivery is not guaranteed.
        focus_target_element(
            window, element, element_record,
            "targeted set_value keyboard fallback",
        )
        send_key("ctrl+a")
        time.sleep(0.05)
        send_text(value)
        action_evidence["route"] = "keyboard_synthesis"
        action_evidence["window_focus_verified"] = True
        action_evidence["target_focus_verified"] = True
        action_evidence["focus_verified"] = True
        action_evidence["readback_verified"] = False
        action_evidence["input_emitted"] = True
        notes.append(
            addressing_channel(element_record)
            + SYNTHESIS
            + "The AT-SPI value API refused this element (Blink and some toolkits "
            "do not implement it on text inputs), so this fell back to focusing "
            "the control, selecting all, and typing. There is no read-back "
            "confirmation on this path, and keyboard synthesis is global — verify "
            "from the tree that the control now holds what you wanted. {}".format(
                UNVERIFIED_SYNTHESIS)
        )
    else:
        action_evidence["route"] = "value_api"
        action_evidence["readback_verified"] = True
        action_evidence["readback_sampled"] = True
        action_evidence["input_emitted"] = True
        notes.append(
            A11Y_CHANNEL
            + SEMANTIC
            + "Wrote the value through the AT-SPI API and read it back to confirm the "
            "control now holds it. Note the limit of that check: it confirms the "
            "CONTROL changed, not that the application adopted the value. Dialogs "
            "commonly keep control state separate from document state and only commit "
            "on OK/Apply — verify the actual effect (document content, a re-read of "
            "the relevant element) rather than trusting this line."
        )


def _delivery_only_response(action_evidence, element_record, notes):
    response = {
        "ok": True,
        "actionEvidence": action_evidence,
        "input_events_emitted": bool(action_evidence.get("input_emitted")),
        "desktop_state_mutated": bool(action_evidence.get("input_emitted")),
        "mutation_unknown": False,
        "observationSkipped": True,
    }
    if element_record is not None:
        response["resolvedElement"] = element_record
    if notes:
        response["notes"] = notes
    return response


def _observe_after_action(
    operation,
    operation_kind,
    observation,
    app,
    bounds,
    element_record,
    notes,
    action_evidence,
    root_window_before,
    windows_before,
    pixels_before,
):
    """Settle, read the window back, and report what changed."""
    settle_note = wait_for_ui_to_settle(app, windows_before)
    lap("settle_wait")
    if settle_note:
        notes.append(settle_note)
    # Compare pixels only after the UI settles: right after the action the UI
    # may still be repainting, so an earlier diff would show an intermediate
    # frame rather than the effect.
    after_bounds = safe(lambda: extents(main_window(app)[1])) or bounds
    after_pixel_bounds = after_bounds
    pixels_after_one = safe(lambda: capture_window_pixels(after_pixel_bounds))
    lap("after_bounds_and_pixels_after_one")
    after_snapshot = build_snapshot(
        operation.get("app", ""),
        text_limit=parse_text_limit(
            observation.get("text_limit"), DEFAULT_TEXT_LIMIT),
        max_tree_nodes=positive_int(
            observation.get("max_tree_nodes"), MAX_ELEMENTS),
        max_tree_depth=positive_int(
            observation.get("max_tree_depth"), MAX_DEPTH),
        prune=observation.get("prune", True),
        boxes=bool(observation.get("boxes", False)),
        include_screenshot=observation.get("include_screenshot", False),
        known_refs=operation.get("known_refs"),
        screenshot_scope=observation.get("screenshot_scope", "window"),
        app_identity=operation.get("app_identity"),
    )
    lap("post_action_snapshot")
    # Modal notes matter most right after an action opens a modal dialog,
    # because the caller's element list predates the dialog. Insert them first
    # so they are read before any action note.
    notes[:0] = after_snapshot.pop("modalNotes", [])
    # Run the content-host check on the post-action snapshot as well: an
    # action that brings a content host to the front (a browser window, a
    # webview tab) is exactly when the caller must learn that this channel
    # cannot see the content, and it often will not observe again separately.
    notes += content_host_diagnostic(after_snapshot.get("elements") or [])
    root_window_after = x11_root_window_state()
    app_windows_after = safe(lambda: window_identity_set(app))
    lap("diagnostics_root_and_windows_after")
    response = {
        "ok": True,
        "snapshot": after_snapshot,
        "actionEvidence": action_evidence,
        "externalDesktopTransition": (
            external_desktop_transition_after_ax_mutation(
                root_window_before,
                root_window_after,
                windows_before,
                app_windows_after,
            )
        ),
    }
    if operation_kind == "window_action":
        response.update({
            "input_events_emitted": True,
            "desktop_state_mutated": True,
            "mutation_unknown": False,
        })
    if element_record is not None:
        response["resolvedElement"] = element_record
    # Take the second post-action capture after building the snapshot. That
    # work spaces the two captures far enough apart for a 1 Hz caret blink to
    # toggle, so the blink is not reported as a persistent change.
    change_note = pixel_change_note(persistent_pixel_change(
        pixels_before, pixels_after_one,
        safe(lambda: capture_window_pixels(after_pixel_bounds))),
        "window")
    lap("pixels_after_two_and_diff")
    if change_note:
        notes.append(change_note)
    if notes:
        response["notes"] = notes
    return response
