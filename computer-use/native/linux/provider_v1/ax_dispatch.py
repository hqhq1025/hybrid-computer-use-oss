"""Linux provider module: dispatch."""

from .foundation import (
    Atspi,
    DEFAULT_TEXT_LIMIT,
    MAX_CHILD_FANOUT,
    MAX_DEPTH,
    MAX_ELEMENTS,
    OperationFailure,
    child_at,
    child_count,
    extents,
    is_interactive_role,
    limit_text,
    math,
    node_name,
    node_pid,
    node_role,
    process_generation,
    provider_bus_name,
    provider_object_path,
    safe,
    state_contains,
    state_value,
    time,
)

from .ax_actions import (
    WHEEL_CLICKS_PER_PAGE,
    find_editable_text,
    focus_target_element,
    insert_text_outcome_on_node,
    parse_key,
    scroll_element,
    send_key,
    send_text,
    set_element_value_outcome,
)

from .gui_input import require_window_focus, send_mouse_click

from .ax_snapshot import (
    effective_name,
    parse_text_limit,
    positive_int,
    readable_states,
)

from .x11_windows import (
    _x11_window_inventory_cache,
    app_windows,
    desktop_bounds,
    desktop_layout_state,
    main_window,
    select_window_by_handle,
    window_identity_set,
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

from .actionability import (
    current_geometry,
    do_action_by_index,
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
from .window_diagnostics import (
    accepts_user_input,
    app_input_windows,
    foreign_foreground_note,
    is_infrastructure_window,
    modal_diagnostic,
    window_label,
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
    describe_hit,
    element_at_point,
    resolved_note,
)
from .ax_snapshot_diagnostics import (
    content_host_diagnostic,
    is_content_plane_role,
    is_dropdown_item,
    snapshot_diagnostics,
)
from .ax_transition_evidence import (
    external_desktop_transition_after_ax_mutation,
    gui_lease_after_ax_mutation,
    public_element_record,
)

def perform_provider_request(operation_kind, payload):
    """Dispatch one validated Provider V2 operation."""
    if operation_kind not in {
        "observe_apps",
        "observe",
        "resolve_and_act",
        "desktop_execute_batch",
        "window_list",
        "window_action",
    }:
        raise ValueError(
            "unsupported provider operation {!r}".format(operation_kind)
        )
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
    if operation_kind == "observe_apps":
        inventory = list_apps_inventory()
        return {
            "ok": True,
            "text": inventory["text"],
            "apps": inventory["apps"],
        }
    if operation_kind == "window_list":
        return app_window_inventory(
            operation.get("app", ""),
            app_identity=operation.get("app_identity"),
            target_window_handle=operation.get("window_handle"),
        )
    if operation_kind == "desktop_execute_batch":
        try:
            response = perform_gui(
                operation.get("actions"),
                operation.get("settle_ms", 800),
                operation.get("frame_id"),
                operation.get("lease_state"),
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
    if operation_kind == "observe":
        snapshot = build_snapshot(
            operation.get("app", ""),
            text_limit=parse_text_limit(observation.get("text_limit"), DEFAULT_TEXT_LIMIT),
            max_tree_nodes=positive_int(observation.get("max_tree_nodes"), MAX_ELEMENTS),
            max_tree_depth=positive_int(observation.get("max_tree_depth"), MAX_DEPTH),
            prune=observation.get("prune", True),
            boxes=bool(observation.get("boxes", False)),
            known_refs=operation.get("known_refs"),
            # None = 按默认策略（带图）。`find` 与 `verify` 会显式传 False：
            # 前者是查询、后者是断言，都不是"观测"，不该付视觉 token
            # ——基线里一次观测的视觉部分是 5120 token，而 verify 会**轮询**，
            # 带图的话一次断言就能烧掉十几次观测的钱。
            include_screenshot=observation.get("include_screenshot"),
            screenshot_scope=observation.get("screenshot_scope", "window"),
            app_identity=operation.get("app_identity"),
            target_window_handle=operation.get("window_handle"),
        )
        response = {"ok": True, "snapshot": snapshot}
        # 模态提示排在最前：它决定了下面整棵树该怎么读。
        elements = snapshot.get("elements") or []
        shell_notes = snapshot_diagnostics(elements)
        # 两条诊断说的是同一件事的两个程度，**不同时发**：
        # snapshot_diagnostics 管"整棵树只剩窗口框"，content_host_diagnostic 管
        # "外壳齐全但内容平面缺席"。前者已经触发时，后者只是重复同一个结论。
        diagnostics = snapshot.pop("modalNotes", []) + shell_notes
        if not shell_notes:
            diagnostics += content_host_diagnostic(elements)
        if diagnostics:
            response["notes"] = diagnostics
        return response
    if (
        operation_kind == "resolve_and_act"
        and action_intent == "resolve_target"
    ):
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

    app = resolve_operation_app(operation)
    gui_bounds_before = desktop_bounds()
    gui_layout_before = desktop_layout_state(gui_bounds_before)
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
    pixel_bounds = bounds
    # 动作前抓一张像素，动作后抓两张——只认在两张里都持续存在的变化。
    # 语义目标先在所有可见 app-owned 窗口里解析，像素和后续焦点动作才能绑定到
    # 真正命中的窗口，而不是解析前碰巧被 main_window 选中的那个窗口。
    pixels_before = safe(lambda: capture_window_pixels(pixel_bounds))
    # 重新解析出活节点之后，**立刻**把几何也刷新成它当前的位置。
    # 不这么做的话，语义路径用活节点、合成路径用旧几何，是同一次调用里的两套事实。
    element_record, moved_note = current_geometry(element, element_record, bounds)
    # 动作之前先记下顶层窗口集合。动作之后要靠它判断有没有开出/关掉窗口，
    # 从而决定该不该多等焦点落定——详见 wait_for_ui_to_settle。
    windows_before = safe(lambda: window_identity_set(app))
    # 每个动作都要说清楚实际走了哪条路径、结果有没有被校验过。返回一棵新的
    # accessibility tree 看着像执行确认，其实只是快照，不能当成动作生效的证据。
    notes = []
    action_evidence = {
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
    action_evidence_state.replace(action_evidence)
    if semantic_note:
        notes.append(semantic_note)
    # "解析到了谁"排在所有动作 Note 之前：先说对谁做，再说做了什么。
    resolved = resolved_note(element, element_record)
    if resolved:
        notes.append(A11Y_CHANNEL + " " + resolved)
    if moved_note:
        # 通道标签后面要有空格，否则渲染成 `[a11y]The element MOVED`。
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

    if operation_kind == "window_action":
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
                "guiLeaseState": gui_lease_after_ax_mutation(
                    gui_layout_before, gui_bounds_before
                ),
                "actionEvidence": action_evidence,
                "notes": notes,
            }
    elif action_intent == "click":
        click_method = (action_input.get("click_method") or "auto").lower()
        click_count = max(1, int(action_input.get("click_count", 1) or 1))
        if click_method == "accessibility":
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
        elif click_method == "app_post":
            raise RuntimeError("click_method 'app_post' is not supported on Linux")
        elif click_method == "sky_click":
            raise RuntimeError("click_method 'sky_click' is not supported on Linux")
        elif click_method == "global":
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
                    # 调用方已经指定了元素，再劝它"改用 element_index"是答非所问：
                    # 走到这里通常正是因为语义调用失效了（实测多个应用的
                    # AT-SPI 动作返回成功却不生效），此时坐标是唯一出路。
                    "" if element_record else PREFER_ELEMENT_INDEX,
                )
            )
        elif click_method == "auto":
            handled = False
            if (
                element is not None
                and action_input.get("mouse_button", "left") == "left"
                and click_count == 1
                and not is_dropdown_item(element, window)
            ):
                handled = do_action_by_index(element, preferred_action_index(element))
            if handled:
                action_evidence["route"] = "semantic_action"
                action_evidence["input_emitted"] = True
                notes.append(
                    A11Y_CHANNEL
                    + SEMANTIC
                    + "Invoked the element's AT-SPI accessibility action. "
                    + UNVERIFIED_SEMANTIC
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
                    # 说清楚为什么没走语义通道。不解释的话这条 Note 读起来像
                    # "这个元素没有动作"，与事实相反——它有，只是那个动作会
                    # 关掉下拉却不提交值，而且关掉之后连校验的机会都没有。
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
        else:
            raise RuntimeError("Invalid click_method '{}'".format(click_method))
    elif action_intent == "scroll":
        direction = action_input.get("direction", "down")
        pages = action_input.get("pages", 1)
        # 有元素几何就按位置滚。element_index 从"必填但没人用"变成真的参与定位。
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
                    # **这句话必须说实话。** element_index 现在可以留空（那表示
                    # "滚当前窗口"），而原文一律写着"位置来自 element_index，
                    # 所以这次滚动是定位过的"——留空时那是假的。
                    # 工具骗 agent 一次，它后面所有的推断都建立在假前提上。
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
    elif action_intent == "type":
        # A caller-supplied element_index is a real target, not a hint. Focus and
        # verify that exact node before any keyboard fallback.
        if element_record is not None:
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
            # Without a target, resolve the unique focused editable control.
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
    elif action_intent == "keypress":
        # 先解析再夺焦点：拼错的按键不该先把用户的窗口抢过来才报错。
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
    elif action_intent == "set_value":
        if element is None:
            raise RuntimeError("unknown target ref")
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
            # 语义写值失败**不等于写不了**。实测 Chrome 的 `entry "Name"`
            # （chrome://settings/manageProfile 里的用户名框）明明holds "Person 1"，
            # set_value 却报"不可设置"——Blink 的输入框不实现 AT-SPI 的
            # Value/EditableText 接口。
            #
            # click 和 type_text 早就有"语义失败就降级到合成"的模式，
            # set_value 却只会硬失败，于是一个到处都是的控件类型整个不可用。
            # 这里补上同一条降级：聚焦 → 全选 → 打字，与人手做的完全一样。
            #
            # 降级要**说清楚**：它没有回读确认，也不保证落到了那个控件上
            # ——键盘合成是全局的。
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
    else:
        raise RuntimeError(
            'unsupportedProviderAction("{}")'.format(action_intent)
        )

    if observation.get("capture_snapshot") is False:
        after_layout = desktop_layout_state(gui_bounds_before)
        response = {
            "ok": True,
            "actionEvidence": action_evidence,
            "input_events_emitted": bool(action_evidence.get("input_emitted")),
            "desktop_state_mutated": bool(action_evidence.get("input_emitted")),
            "mutation_unknown": False,
            "observationSkipped": True,
            "guiLeaseState": (
                "suspect"
                if gui_layout_before is None or after_layout is None
                else "fresh"
                if gui_layout_before == after_layout
                else "stale"
            ),
        }
        if element_record is not None:
            response["resolvedElement"] = public_element_record(element_record)
        if notes:
            response["notes"] = notes
        return response

    settle_note = wait_for_ui_to_settle(app, windows_before)
    if settle_note:
        notes.append(settle_note)
    # 像素比对必须在**安置之后**：动作刚发出去时界面还没画完，
    # 那时比出来的"变化"是渲染中途的样子，不是效果。
    after_bounds = safe(lambda: extents(main_window(app)[1])) or bounds
    after_pixel_bounds = after_bounds
    pixels_after_one = safe(lambda: capture_window_pixels(after_pixel_bounds))
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
    # 动作**开出**模态对话框是最需要说这句话的时刻：下标全部重排，而 agent
    # 手上那份还是点击之前的。插在最前面，让它在所有动作 Note 之前被读到。
    notes[:0] = after_snapshot.pop("modalNotes", [])
    # 通道失效检测也要跟着动作后的快照走：动作把一个内容宿主推到前面
    # （打开了浏览器窗口、切到了 webview 标签）正是最该说"这条通道在这里看不见
    # 内容"的时刻，而那时调用方往往不会再单独调一次 get_app_state。
    notes += content_host_diagnostic(after_snapshot.get("elements") or [])
    after_layout = desktop_layout_state(gui_bounds_before)
    app_windows_after = safe(lambda: window_identity_set(app))
    response = {
        "ok": True,
        "snapshot": after_snapshot,
        "actionEvidence": action_evidence,
        "guiLeaseState": (
            "suspect"
            if gui_layout_before is None or after_layout is None
            else "fresh"
            if gui_layout_before == after_layout
            else "stale"
        ),
        "externalDesktopTransition": (
            external_desktop_transition_after_ax_mutation(
                gui_layout_before,
                after_layout,
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
        response["resolvedElement"] = public_element_record(element_record)
    # 第二张动作后画面放在**建完快照之后**抓：resolve_app 自己就要 0.15–0.3s，
    # 这段已有的耗时正好当作两张之间的间隔，足够让 1Hz 的光标闪一次。
    change_note = pixel_change_note(persistent_pixel_change(
        pixels_before, pixels_after_one,
        safe(lambda: capture_window_pixels(after_pixel_bounds))),
        "window")
    if change_note:
        notes.append(change_note)
    if notes:
        response["notes"] = notes
    return response
