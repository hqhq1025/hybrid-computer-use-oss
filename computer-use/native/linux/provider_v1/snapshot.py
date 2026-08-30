"""Linux provider module: snapshot."""

from .foundation import (
    Atspi,
    DEFAULT_TEXT_LIMIT,
    GUI_SNAPSHOT_LAYOUT_ATTEMPTS,
    MAX_DEPTH,
    MAX_ELEMENTS,
    SCREENSHOT_SCOPE_SCREEN,
    a11y_screenshots_enabled,
    desktop,
    extents,
    has_text_iface,
    hashlib,
    limit_text,
    node_name,
    node_pid,
    node_role,
    process_generation,
    provider_bus_name,
    provider_object_path,
    safe,
    state_contains,
    time,
)

from .ax_snapshot import (
    StableIndexer,
    render_tree,
)

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
    """元素指称的**唯一**渲染出口，与 Go 侧 renderElementReference 同一文法。

    硬性契约是**可回读**：这里吐出来的一串，原样贴回 `expect` 的 target 必须
    解析成同一个指称。所以名字一律加引号（`entry "Address and search bar"`），
    无名元素写成 `role="entry"`。

    原来焦点摘要走的是 `role + " " + name`，无引号。那种串横跨 role 与 name
    两个字段，任何解析器都拆不开：我们一边告诉 agent 焦点在
    `entry Address and search bar`，一边在它把这串贴进 expect 时判它不匹配，
    于是产出了自相矛盾的证据。打印出去的东西必须是我们自己认得的东西。
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
        root = desktop()
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
    """采样时刻，UTC，毫秒精度。

    快照必须自带时间：桌面上一份快照可能已经陈旧几十秒而调用方毫无察觉，
    浏览器里 DOM 变化通常伴随可观测事件，这里没有等价物。
    """
    now = time.time()
    return "{}.{:03d}Z".format(
        time.strftime("%Y-%m-%dT%H:%M:%S", time.gmtime(now)), int((now % 1) * 1000))


# "应用还在忙"的角色证据。进度条 / 忙碌指示器是工具包无关的通用控件。
BUSY_ROLES = {"progress bar", "busy indicator"}


def busy_signal(window, records):
    """这个应用有没有在明说"我还在干活"。

    存在的理由：异步/长耗时操作在树上表现为"什么都没变"，这与"信息被预算
    裁掉了"在返回文本上完全同形。有了这一位，"再等等"与"换个方式看"就不再
    需要靠猜。

    只从**存在**推语义，不从缺失推：没有 BUSY 位、没有进度条，不能反过来证明
    操作已经完成——AT-SPI 上状态位设不设跨工具包全凭自觉。
    """
    busy_state = getattr(Atspi.StateType, "BUSY", None)
    window_busy = bool(
        busy_state is not None and state_contains(window, busy_state))
    indicators = sum(
        1 for record in records
        if str(record.get("controlType") or "").strip().lower() in BUSY_ROLES)
    return {"windowBusy": window_busy, "progressElements": indicators}


def snapshot_digest(window_handle, lines, focused, selected):
    """快照的内容指纹。相同 = 这一次观测和上一次**逐字相同**。

    刻意不把截图算进去：像素每帧都在抖（光标闪烁、抗锯齿），把它算进来会让
    "相同"这件事永远不成立，那这个字段就没有意义了。指纹覆盖的是语义通道的
    全部内容：根窗口身份、树的每一行、焦点、选区。
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
    """构建应用快照。

    `include_screenshot=None` 表示"按当前策略决定"，见
    `a11y_screenshots_enabled()`；传 True/False 则强制。

    这里原本默认关闭，理由是"a11y 与 VLM 是两条独立轨道，a11y 轨不该顺带付
    截图的钱"。成本数字仍然成立（gedit 单次观测截图约 1014 token，占 35%，
    树裁剪后会升到 80% 左右）——**但"两条独立轨道"这个前提被实测推翻了**。

    实测 LibreOffice Impress：
    - 右对齐生效了，段落节点的 `Frame` 却一直指向占位符左端；
      `ctrl+r` 与 `ctrl+s` 都真的生效了，却都被"树没变"判成"送达但被忽略"。
      **两次假阴性，都是一张截图一眼判掉的。**
    - 反过来，「哪个 spin button 是 Position Y」靠截图只能凭标签的空间邻近去猜，
      靠树的 Description 是确定的。

    所以两条通道是**互补**，不是替代：树给可操作性，截图给可验证性。
    A11Y 截图现在由调用方通过 observe=screenshot/full 或 get_app_state 的
    include_screenshot=true 显式选择。

    `boxes` 默认 **False**（对齐 Playwright 的同名开关）。几何实测占整棵树的
    35–50%。它与 get_app_state 附带的窗口截图同属窗口相对空间，主要用于
    a11y 调试；`click(element_index)` 的坐标由服务端内部从记录里取，
    不经过渲染，所以关掉几何**不削弱任何 a11y 能力**。GUI 坐标必须改从
    gui 的全桌面图读取，不能直接复用这些 Frame 值。
    需要在树里直接看到矩形时传 `boxes: true`。
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
        # 标题不足以标识一个窗口，见 window_instance_handle。
        "windowHandle": handle,
        "windows": top_level_window_inventory(app, window_index),
        "windowBounds": bounds,
        "windowSelection": window_selection,
        # 观测的时效性与完整性是状态型协议的基本元数据。sampledAt 是**这一份**
        # 快照的采样时刻；snapshotId 是它的内容指纹，Go 侧靠它判等价。
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
        # 把 路径 -> (编号, role, name) 回给 Go，下次请求原样传回来，
        # 编号就能跨快照存活。见 StableIndexer。
        "refs": {
            ".".join(str(p) for p in r["runtimeId"]): {
                "index": r["index"], "role": r["controlType"], "name": r["name"],
                "nameHash": r.get("nameHash", ""),
            }
            for r in records
        },
        # 模态提示跟着快照走，而不是只在 get_app_state 里算一次：**动作后**的
        # 快照更需要它——正是那次点击弹出了对话框，那一刻说"你现在被挡住了"
        # 最有用。Go 侧的 appSnapshot 没有这个字段，encoding/json 会忽略它，
        # 运行时靠下面 pop 出来放进 notes。
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
