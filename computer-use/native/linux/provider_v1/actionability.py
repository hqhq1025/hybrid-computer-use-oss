"""Linux provider module: actionability."""

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
    # 直接用 CLICK_COVERED_ACTIONS，不再维护一份副本。
    # 两份集合是同一件事的两面（一个决定调用哪个动作、一个决定不要重复展示它），
    # 抄成两处必然分歧：本轮加 `doDefault` 时就漏了这一份，
    # 结果 VS Code 的 19 个节点仍然点不动。
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
    """把 A11Y 内部合成使用的屏幕坐标换回目标窗口相对坐标。

    A11Y tree Frame 与它的可选窗口截图使用这个空间。独立 gui 工具只接受
    自己返回的全桌面屏幕绝对坐标，不经过这里。
    """
    if window_bounds is None:
        return x, y
    return x - window_bounds["x"], y - window_bounds["y"]


# 稳定判据：两次采样一致即认为停住了。Playwright 等的是"连续两个动画帧内
# bounding box 不变"，这里是同一件事的桌面版。
STABLE_SAMPLE_SECONDS = 0.08


def stable_timeout_seconds():
    """等元素停住的上限，可调。

    默认 1 秒是折中：够长，能盖住实测里绝大多数一次性重排；够短，遇到永远在
    动的东西（进度条、动画）也不会把一次点击拖成几秒。动画重的应用可以调大，
    追求最低延迟的场景可以调小到 0——那等于关掉这项等待。
    """
    raw = os.environ.get("COMPUTER_USE_STABLE_TIMEOUT_MS", "").strip()
    if not raw:
        return 1.0
    try:
        return max(0.0, float(raw) / 1000.0)
    except ValueError:
        return 1.0


def wait_until_stable(element, window_bounds, first=None):
    """等元素停止移动再动手。返回 (最终 frame, 采样次数, 是否稳定下来)。

    **只做 stable，不做 enabled。** Playwright 的可操作性检查里还有"等元素
    enabled"，那一条在 Linux 上不能照搬：本仓库实测 Nautilus 的文件图标根本
    不设 ENABLED / SENSITIVE，状态里只有 SHOWING / VISIBLE / FOCUSABLE，却带着
    open / menu 两个动作、完全可点。拿 ENABLED 当门禁会让 agent 直接跳过可用
    目标。宁可不等这一项，也不能用一个不可靠的信号去拦住真实可用的元素。
    同理不做 "receives events" 的命中测试门禁——本仓库实测命中率
    gedit 11/11、Nautilus 19/25、LibreOffice 12/25，拿它当门禁会拦掉一半
    LibreOffice 的正常点击。

    `first` 让调用方把已经取过的那次采样传进来，省掉一次重复读取。
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
    """把记录里的几何换成**活节点此刻的位置**，并说明它挪了多少。

    Playwright 的 locator 在每次动作时重新解析，这是同一件事的桌面版。
    我们其实早就在 find_element 里重新解析出了活节点，却仍然拿 Go 缓存里的
    那份旧记录去算坐标——于是同一次调用里有两套事实：语义路径作用在活节点上，
    合成路径打在快照拍摄时的位置上。

    实测：VS Code 里由外部收起侧栏后，click(element_index=21) 把坐标合成在
    (572,38)，正是缓存框 {547,28,50,19} 的中心，而界面那时已经重排。

    桌面比浏览器更需要这一条：这里没有"页面加载完成"这种事件，界面可以在
    两次工具调用之间被动画、异步加载、甚至**别的进程**移动。

    返回 (记录, 位移说明)。活节点取不到位置时保留旧记录仅供诊断；
    coordinate fallback 会在 require_stable_coordinate_geometry() 中拒绝，
    绝不会使用旧坐标发出输入。
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
    # 这里只服务 A11Y click 的元素坐标兜底。gui 不经过这里。
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
