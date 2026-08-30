"""Linux GUI capture, input, settling, and response helpers."""

from .foundation import (
    GUI_FRAME_GUARDED_ACTION_TYPES,
    GUI_INPUT_ACTION_TYPES,
    GUI_KEYBOARD_ACTION_INTERVAL_SECONDS,
    GUI_KEYBOARD_ACTION_TYPES,
    GUI_KEYBOARD_MICRO_WAIT_MAX_SECONDS,
    GUI_MAX_ACTIONS,
    GUI_MAX_SETTLE_MS,
    GUI_MAX_TOTAL_WAIT_SECONDS,
    GUI_POINTER_ACTION_TYPES,
    GUI_POINTER_KEYBOARD_BARRIER_SECONDS,
    GUI_QUIESCENT_MIN_SAMPLES,
    GUI_QUIESCENT_MIN_SECONDS,
    GUI_SETTLE_POLL_SECONDS,
    PIXEL_DIFF_STRIDE,
    PIXEL_DIFF_THRESHOLD,
    frame,
    json,
    time,
)

from .gui_capture import (
    capture_window_pixels,
    gui_model_dimensions,
    pixel_buffers_identical,
    pixel_change,
)
from .gui_input import (
    InputEmissionError,
    execute_gui_action,
    gui_wait_metrics,
    validate_gui_actions,
)
from .snapshot import build_gui_snapshot
from .x11_windows import (
    desktop_bounds,
    desktop_layout_frame_id,
    desktop_layout_state,
)

def wait_for_gui_settle(bounds, settle_ms, clock=None, sleep=None):
    """Wait for a sustained run of pixel-identical full-desktop samples."""
    if clock is None:
        clock = time.monotonic
    if sleep is None:
        sleep = time.sleep
    try:
        settle_ms = int(settle_ms)
    except (TypeError, ValueError):
        raise RuntimeError("gui settle_ms must be a whole number of milliseconds")
    if settle_ms < 0 or settle_ms > GUI_MAX_SETTLE_MS:
        raise RuntimeError(
            "gui settle_ms must be between 0 and {}".format(GUI_MAX_SETTLE_MS)
        )
    if settle_ms == 0:
        return {
            "stable": False,
            "elapsed_ms": 0,
            "polls": 0,
            "skipped": True,
            "reason": "settle_ms_zero",
        }

    previous = capture_window_pixels(bounds)
    if previous is None:
        return {"stable": False, "elapsed_ms": 0, "polls": 0, "unavailable": True}
    start = clock()
    stable_started_at = start
    stable_samples = 1
    polls = 0
    while True:
        now = clock()
        elapsed_seconds = max(0.0, now - start)
        elapsed_ms = int(round(elapsed_seconds * 1000))
        if elapsed_seconds >= settle_ms / 1000.0:
            return {
                "stable": False,
                "elapsed_ms": elapsed_ms,
                "polls": polls,
                "timed_out": True,
                "pixels": previous,
                "stable_samples": stable_samples,
                "stable_for_ms": int(round(
                    max(0.0, now - stable_started_at) * 1000
                )) if stable_samples else 0,
            }
        remaining = max(0.0, settle_ms / 1000.0 - elapsed_seconds)
        sleep(min(GUI_SETTLE_POLL_SECONDS, remaining))
        current = capture_window_pixels(bounds)
        sampled_at = clock()
        polls += 1
        identical = pixel_buffers_identical(previous, current)
        if identical is True:
            stable_samples += 1
        elif current is not None:
            stable_started_at = sampled_at
            stable_samples = 1
        else:
            stable_started_at = sampled_at
            stable_samples = 0
        stable_for = (
            sampled_at - stable_started_at if stable_samples else 0.0
        )
        if (
            stable_samples >= GUI_QUIESCENT_MIN_SAMPLES
            and stable_for + 1e-9 >= GUI_QUIESCENT_MIN_SECONDS
        ):
            return {
                "stable": True,
                "elapsed_ms": int(round((sampled_at - start) * 1000)),
                "polls": polls,
                "pixels": current,
                "stable_samples": stable_samples,
                "stable_for_ms": int(round(stable_for * 1000)),
            }
        if current is not None:
            previous = current


def gui_settle_note(result):
    if result.get("skipped"):
        if result.get("reason") == "observation_only":
            return (
                "No quiescence polling was needed for this read-only batch; the "
                "explicit before/after visual comparison was still performed."
            )
        if result.get("reason") == "no_input_actions":
            return (
                "No quiescence comparison was needed because the batch emitted no "
                "input events."
            )
        return (
            "No quiescence comparison was made because settle_ms=0; this says "
            "nothing about the action effect."
        )
    if result.get("unavailable"):
        return (
            "Quiescence was not established because desktop pixels could not be "
            "sampled; the final screenshot is still attached."
        )
    if result.get("invalidated_by_final_capture"):
        return (
            "A stable run was observed, but the desktop changed again while the final "
            "response screenshot was captured; quiescent was cleared so the verdict "
            "and attached image do not describe different states."
        )
    if result.get("stable"):
        return (
            "{} consecutive full-desktop screenshots were pixel-identical over "
            "{}ms (settled after {}ms and {} poll(s)); quiescent=true describes "
            "only full-frame byte equality and does not verify any action effect."
        ).format(
            result["stable_samples"], result["stable_for_ms"],
            result["elapsed_ms"], result["polls"])
    return (
        "No run of at least {} consecutive pixel-identical full-desktop screenshots "
        "covering at least {}ms was observed within {}ms ({} poll(s)); quiescence "
        "was not established and the final screenshot is only the latest sample."
    ).format(
        GUI_QUIESCENT_MIN_SAMPLES,
        int(GUI_QUIESCENT_MIN_SECONDS * 1000),
        result["elapsed_ms"],
        result["polls"],
    )


def gui_visual_change(before, after):
    identical = pixel_buffers_identical(before, after)
    if identical is None:
        return {
            "changed_since_before": None,
            "visual_change_percent": None,
            "visual_change_box": None,
            "visual_change_method": None,
        }
    if identical:
        return {
            "changed_since_before": False,
            "visual_change_percent": 0.0,
            "visual_change_box": None,
            "visual_change_method": "exact_full_frame_bytes",
        }
    change = pixel_change(before, after)
    method = (
        "exact_full_frame_boolean_with_sampled_rgb_stride_{}_threshold_{}_region"
        .format(PIXEL_DIFF_STRIDE, PIXEL_DIFF_THRESHOLD)
    )
    if change is None:
        return {
            "changed_since_before": True,
            "visual_change_percent": None,
            "visual_change_box": None,
            "visual_change_method": "exact_full_frame_bytes",
        }
    if change.get("resized"):
        return {
            "changed_since_before": True,
            "visual_change_percent": None,
            "visual_change_box": None,
            "visual_change_method": "frame_geometry_changed",
        }
    return {
        "changed_since_before": True,
        "visual_change_percent": change.get("percent"),
        "visual_change_box": change.get("box"),
        "visual_change_method": method,
    }


def gui_snapshot_visual_change(before, after):
    before_id = (before or {}).get("contentFrameId")
    after_id = (after or {}).get("contentFrameId")
    if not before_id or not after_id:
        return {
            "changed_since_before": None,
            "visual_change_percent": None,
            "visual_change_box": None,
            "visual_change_method": None,
        }
    changed = before_id != after_id
    return {
        "changed_since_before": changed,
        "visual_change_percent": None,
        "visual_change_box": None,
        "visual_change_method": "exact_returned_screenshot_bytes",
    }


GUI_RESULT_FIELDS = (
    "dispatched",
    "input_events_emitted",
    "input_actions_dispatched",
    "mutation_unknown",
    "partial_input_possible",
    "effect_unknown",
    "quiescent",
    "changed_since_before",
    "visual_change_percent",
    "visual_change_box",
    "visual_change_method",
    "visual_change_observed",
)


def finalize_gui_response(response):
    """Attach a stable structured GUI result schema on success and failure."""
    if not isinstance(response, dict):
        return response
    defaults = {
        "dispatched": 0,
        "input_events_emitted": False,
        "input_actions_dispatched": 0,
        "mutation_unknown": False,
        "partial_input_possible": False,
        "effect_unknown": False,
        "quiescent": None,
        "changed_since_before": None,
        "visual_change_percent": None,
        "visual_change_box": None,
        "visual_change_method": None,
        "visual_change_observed": None,
    }
    for field, value in defaults.items():
        response.setdefault(field, value)
    gui_result = response.get("guiResult")
    if not isinstance(gui_result, dict):
        gui_result = {}
    for field, value in defaults.items():
        gui_result.setdefault(field, response.get(field, value))
    response["guiResult"] = gui_result
    return response


def normalize_gui_lease_state(value):
    state = str(value or "absent").strip().lower()
    return state if state in {"fresh", "suspect", "stale", "absent"} else "absent"
def perform_gui(actions, settle_ms=800, frame_id=None, gui_lease_state=None):
    if actions is None:
        return {
            "ok": False,
            "error": "gui.actions is required; use actions=[] for observation",
            "preflight_only": True,
            "dispatched": 0,
            "input_events_emitted": False,
            "input_actions_dispatched": 0,
        }
    if not isinstance(actions, list):
        return {
            "ok": False,
            "error": "gui.actions must be an array",
            "preflight_only": True,
            "dispatched": 0,
            "input_events_emitted": False,
            "input_actions_dispatched": 0,
        }
    if len(actions) > GUI_MAX_ACTIONS:
        return {
            "ok": False,
            "error": "gui.actions accepts at most {} actions per call".format(
                GUI_MAX_ACTIONS),
            "preflight_only": True,
            "dispatched": 0,
            "input_events_emitted": False,
            "input_actions_dispatched": 0,
        }
    if len(actions) == 0:
        return {
            "ok": False,
            "error": "gui observation requires actions=[{\"type\":\"screenshot\"}]",
            "preflight_only": True,
            "dispatched": 0,
            "input_events_emitted": False,
            "input_actions_dispatched": 0,
        }
    try:
        normalized_settle_ms = int(settle_ms)
    except (TypeError, ValueError):
        normalized_settle_ms = -1
    if (
        isinstance(settle_ms, bool)
        or normalized_settle_ms != settle_ms
        or normalized_settle_ms < 0
        or normalized_settle_ms > GUI_MAX_SETTLE_MS
    ):
        return {
            "ok": False,
            "error": "gui settle_ms must be a whole number of milliseconds "
                     "between 0 and {}".format(GUI_MAX_SETTLE_MS),
            "preflight_only": True,
            "dispatched": 0,
            "input_events_emitted": False,
            "input_actions_dispatched": 0,
        }
    settle_ms = normalized_settle_ms
    _static_actions, static_errors = validate_gui_actions(actions)
    if len(actions) > 1 and any(
            action.get("type") == "screenshot"
            for action in actions if isinstance(action, dict)):
        static_errors.append({
            "index": next(
                index for index, action in enumerate(actions)
                if isinstance(action, dict)
                and action.get("type") == "screenshot"),
            "type": "screenshot",
            "error": "gui screenshot must be the only action because GUI returns only one final screenshot",
        })
    if static_errors:
        return {
            "ok": False,
            "error": "gui batch failed static pre-validation; no actions were "
                     "dispatched and no screenshot was captured",
            "validation_errors": static_errors,
            "preflight_only": True,
            "dispatched": 0,
            "input_events_emitted": False,
            "input_actions_dispatched": 0,
        }
    total_wait = sum(
        action.get("seconds", 0)
        for action in _static_actions
        if action["type"] == "wait"
    )
    if total_wait > GUI_MAX_TOTAL_WAIT_SECONDS:
        return {
            "ok": False,
            "error": "gui batch cumulative wait is {:.3g}s; maximum is {}s. "
                     "No actions were dispatched and no screenshot was captured.".format(
                         total_wait, GUI_MAX_TOTAL_WAIT_SECONDS),
            "preflight_only": True,
            "dispatched": 0,
            "input_events_emitted": False,
            "input_actions_dispatched": 0,
        }

    bounds = desktop_bounds()
    if bounds is None:
        return {
            "ok": False,
            "error": "full desktop bounds are unavailable; gui fails closed because "
                     "it cannot validate coordinates or capture its required screenshot",
            "dispatched": 0,
            "input_events_emitted": False,
            "input_actions_dispatched": 0,
            "preflight_only": True,
        }
    model_dimensions = gui_model_dimensions(bounds)
    model_bounds = frame(
        0,
        0,
        model_dimensions["width"],
        model_dimensions["height"],
    )
    size = "{}x{}".format(
        model_dimensions["width"],
        model_dimensions["height"],
    )
    snapshot = lambda: build_gui_snapshot(bounds)
    validated, coordinate_errors = validate_gui_actions(
        actions,
        model_bounds,
        bounds,
    )
    if coordinate_errors:
        return {
            "ok": False,
            "error": "gui batch failed coordinate pre-validation; no actions were "
                     "dispatched and no screenshot was captured",
            "validation_errors": coordinate_errors,
            "preflight_only": True,
            "dispatched": 0,
            "input_events_emitted": False,
            "input_actions_dispatched": 0,
        }
    current_snapshot = snapshot()
    prior_lease_state = normalize_gui_lease_state(gui_lease_state)
    def latest_snapshot_or_current():
        try:
            return snapshot()
        except Exception:
            return current_snapshot

    if not current_snapshot.get("screenshotPngBase64"):
        return {
            "ok": False,
            "error": "full-desktop screenshot capture is unavailable; gui failed closed "
                     "before emitting input because it could not return or validate the "
                     "visual state used for coordinates",
            "dispatched": 0,
            "input_events_emitted": False,
            "input_actions_dispatched": 0,
            "effect_unknown": False,
            "quiescent": None,
            "snapshot": current_snapshot,
        }
    wait_metrics = gui_wait_metrics(validated)
    frame_guarded_actions = any(
        action["type"] in GUI_FRAME_GUARDED_ACTION_TYPES for action in validated)
    if frame_guarded_actions and not current_snapshot.get("frameId"):
        return {
            "ok": False,
            "error": "desktop layout could not be sampled consistently around the "
                     "full-desktop screenshot; gui failed closed before coordinate input",
            "dispatched": 0,
            "input_events_emitted": False,
            "input_actions_dispatched": 0,
            "effect_unknown": False,
            "quiescent": None,
            "snapshot": current_snapshot,
        }
    lease_revalidated = bool(
        frame_guarded_actions
        and prior_lease_state in {"suspect", "stale"}
        and current_snapshot.get("frameId")
    )
    if frame_id and frame_guarded_actions:
        if current_snapshot.get("frameId") != frame_id:
            return {
                "ok": False,
                "error": "STALE GUI FRAME: the desktop layout changed since the "
                         "screenshot used for coordinates. No actions were dispatched "
                         "and no input events were emitted.",
                "text": "GUI rejected the coordinate batch against a fresh desktop layout sample.",
                "dispatched": 0,
                "input_events_emitted": False,
                "input_actions_dispatched": 0,
                "effect_unknown": False,
                "quiescent": None,
                "snapshot": current_snapshot,
            }

    mutating = any(
        action["type"] not in {"screenshot", "wait"}
        for action in validated
    )
    observation_only = (
        len(validated) == 1 and validated[0]["type"] == "screenshot")
    before_pixels = capture_window_pixels(bounds) if mutating else None
    dispatched = 0
    input_actions_dispatched = 0
    pointer_settle_credit = None
    for index, action in enumerate(validated):
        action_type = action["type"]
        try:
            if frame_id and action_type in GUI_FRAME_GUARDED_ACTION_TYPES:
                live_bounds = desktop_bounds()
                live_layout = desktop_layout_state(live_bounds)
                live_frame_id = desktop_layout_frame_id(live_layout)
                if live_frame_id != frame_id:
                    return {
                        "ok": False,
                        "error": "STALE GUI FRAME before action {} ({}): desktop layout "
                                 "changed during the batch, so this coordinate action was "
                                 "not emitted.".format(index + 1, action_type),
                        "text": "GUI stopped before emitting the stale coordinate action. "
                                "Earlier actions, if any, are reflected in the attached "
                                "full-desktop screenshot.",
                        "dispatched": dispatched,
                        "input_events_emitted": input_actions_dispatched > 0,
                        "input_actions_dispatched": input_actions_dispatched,
                        "partial_input_possible": False,
                        "effect_unknown": bool(input_actions_dispatched),
                        "mutation_unknown": bool(input_actions_dispatched),
                        "quiescent": None,
                        "snapshot": latest_snapshot_or_current(),
                    }
            if (pointer_settle_credit is not None
                    and action_type in GUI_KEYBOARD_ACTION_TYPES):
                remaining = max(
                    0.0,
                    GUI_POINTER_KEYBOARD_BARRIER_SECONDS
                    - pointer_settle_credit,
                )
                if remaining:
                    time.sleep(remaining)
            execute_gui_action(action)
            dispatched += 1
            if action_type in GUI_INPUT_ACTION_TYPES:
                input_actions_dispatched += 1
            if action_type in GUI_POINTER_ACTION_TYPES:
                pointer_settle_credit = 0.0
            elif action_type == "wait" and pointer_settle_credit is not None:
                pointer_settle_credit += action["seconds"]
            elif action_type in GUI_KEYBOARD_ACTION_TYPES:
                pointer_settle_credit = None
            if (
                action_type in GUI_KEYBOARD_ACTION_TYPES
                and index + 1 < len(validated)
                and validated[index + 1]["type"] in GUI_INPUT_ACTION_TYPES
            ):
                time.sleep(GUI_KEYBOARD_ACTION_INTERVAL_SECONDS)
        except Exception as exc:
            explicit_partial = (
                isinstance(exc, InputEmissionError) and exc.events_emitted
            )
            input_events_emitted = bool(
                input_actions_dispatched or explicit_partial)
            partial_input_possible = bool(
                explicit_partial
                or (
                    action_type in GUI_INPUT_ACTION_TYPES
                    and not isinstance(exc, InputEmissionError)
                )
            )
            return {
                "ok": False,
                "error": "gui action {} ({}) failed after {} dispatched action(s): {}".format(
                    index + 1, action_type or "unknown", dispatched, exc),
                "text": "GUI stopped at the first failed action. The attached full-desktop "
                        "screenshot is {}. The failing action may have emitted a "
                        "partial input sequence; no application effect is inferred.".format(
                            size),
                "dispatched": dispatched,
                "input_events_emitted": input_events_emitted,
                "input_actions_dispatched": input_actions_dispatched,
                "partial_input_possible": partial_input_possible,
                "effect_unknown": bool(
                    input_events_emitted
                    or partial_input_possible),
                "mutation_unknown": bool(
                    input_events_emitted
                    or partial_input_possible),
                "quiescent": None,
                "snapshot": latest_snapshot_or_current(),
            }
    try:
        settle_result = (
            wait_for_gui_settle(bounds, settle_ms)
            if not observation_only
            else {
                "stable": False,
                "elapsed_ms": 0,
                "polls": 0,
                "skipped": True,
                "reason": "observation_only",
            }
        )
        settled_pixels = (
            settle_result.get("pixels")
            or (capture_window_pixels(bounds) if mutating else None)
        )
        before_final_pixels = capture_window_pixels(bounds) if mutating else None
        final_snapshot = snapshot()
        after_final_pixels = capture_window_pixels(bounds) if mutating else None
    except Exception as exc:
        return {
            "ok": False,
            "error": "gui post-dispatch observation failed after {} dispatched "
                     "action(s): {}".format(dispatched, exc),
            "text": "GUI actions may already have changed the desktop. The latest "
                    "pre-action screenshot is attached because final capture failed.",
            "dispatched": dispatched,
            "input_events_emitted": input_actions_dispatched > 0,
            "input_actions_dispatched": input_actions_dispatched,
            "partial_input_possible": False,
            "effect_unknown": bool(input_actions_dispatched),
            "mutation_unknown": bool(input_actions_dispatched),
            "quiescent": None,
            "snapshot": current_snapshot,
        }
    final_capture_coherent = (
        pixel_buffers_identical(before_final_pixels, after_final_pixels)
        if mutating else None
    )
    if mutating and final_capture_coherent is not True:
        if settle_result.get("stable"):
            settle_result = dict(settle_result)
            settle_result["stable"] = False
            settle_result["invalidated_by_final_capture"] = True
        after_pixels = None
    else:
        after_pixels = after_final_pixels or settled_pixels
    visual = (
        gui_visual_change(before_pixels, after_pixels)
        if mutating else gui_visual_change(None, None)
    )
    gui_result = {
        "dispatched": dispatched,
        "input_events_emitted": input_actions_dispatched > 0,
        "input_actions_dispatched": input_actions_dispatched,
        "effect_unknown": mutating,
        "quiescent": True if settle_result.get("stable") else None,
        "changed_since_before": visual["changed_since_before"],
        "visual_change_percent": visual["visual_change_percent"],
        "visual_change_box": visual["visual_change_box"],
        "visual_change_method": visual["visual_change_method"],
        "visual_change_observed": visual["changed_since_before"] if mutating else None,
        **wait_metrics,
    }
    gui_result["gui_lease_state"] = (
        "fresh" if final_snapshot.get("frameId") else "suspect"
    )
    if lease_revalidated:
        gui_result["gui_lease_revalidated"] = True
    focus_note = ""
    if any(action["type"] in {"type", "keypress"} for action in validated):
        focus_note = (
            " GUI type/keypress input was emitted toward whichever control held input "
            "focus; gui did not identify or verify a target, delivery, or effect."
        )
    wait_note = ""
    if wait_metrics["keyboard_micro_wait_actions"]:
        wait_note = (
            " {} explicit wait action(s) of at most {:.1f}s were placed between "
            "keyboard/text actions. wait produced no intermediate observation; "
            "consecutive keyboard/text actions already receive a short processing "
            "barrier when no wait action separates them."
        ).format(
            wait_metrics["keyboard_micro_wait_actions"],
            GUI_KEYBOARD_MICRO_WAIT_MAX_SECONDS,
        )
    return {
        "ok": True,
        "text": "GUI dispatched {}/{} action(s); {} input-producing action(s) "
                "completed event emission. {} visual_changed={} is a non-assertive "
                "full-frame pixel observation; it does not verify delivery, target, "
                "or semantic effect. effect_unknown={}. The attached "
                "full-desktop screenshot is {}; coordinateSpace=screen_image_px.{}{}"
                .format(
                    dispatched, len(actions), input_actions_dispatched,
                    gui_settle_note(settle_result),
                    json.dumps(visual["changed_since_before"]),
                    str(mutating).lower(), size,
                    focus_note,
                    wait_note),
        **gui_result,
        "guiResult": gui_result,
        "snapshot": final_snapshot,
    }
