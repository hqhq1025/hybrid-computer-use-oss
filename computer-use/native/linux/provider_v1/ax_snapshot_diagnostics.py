"""Diagnostics derived from an accessibility snapshot."""

from .foundation import node_name, node_role

def snapshot_diagnostics(records):
    """Detect a snapshot that contains only top-level/container structure.

    This observation is intentionally diagnostic rather than causal.  A sparse tree
    can mean delayed, collapsed, pruned, toolkit-limited, or genuinely sparse UI;
    the snapshot alone cannot identify which mechanism produced it.
    """
    if not records:
        return []
    for record in records:
        role = str(record.get("controlType") or "").lower()
        if role in {"frame", "window", "dialog", "alert", "application"}:
            continue
        if record.get("name") or record.get("actions") or record.get("value"):
            return []
    return [
        "ACCESSIBILITY CONTENT UNCERTAIN: this snapshot contains no named, valued, or "
        "actionable accessibility node beyond top-level window/container records "
        "({} element(s) total). That observation does NOT prove the visible window is "
        "empty, that element-targeted actions are globally impossible, or that a "
        "renderer failed to export accessibility; the tree may be delayed, pruned, "
        "collapsed, toolkit-limited, or genuinely sparse. Inspect the full-desktop "
        "screenshot, open the relevant UI, and sample again before concluding that the "
        "content is unavailable.".format(len(records))
    ]


# 内容平面的角色。共同点：它们承载的是**应用的内容本身**，而不是操作内容的外壳。
# 任何以 "document" 开头的角色（document web / document frame / document text /
# document spreadsheet / document presentation / document email）都算，
# 这与 is_interactive_role 的约定一致。
CONTENT_PLANE_ROLES = {
    "canvas",
    "drawing area",
    "terminal",
    "image map",
    "tree table",
    "web view",
    "page",
}

# 嵌入式内容的**容器**角色。它们本身不是内容，是内容可能出现的地方；
# 空容器只能提高"当前快照没有看到内容根"的可能性，不能证明内容缺席。
EMBEDDED_CONTAINER_ROLES = {
    "scroll pane",
    "viewport",
    "internal frame",
    "embedded",
    "layered pane",
    "split pane",
}


def is_content_plane_role(role):
    normalized = str(role or "").strip().lower()
    if normalized.startswith("document"):
        return True
    return normalized in CONTENT_PLANE_ROLES


def content_host_diagnostic(records):
    """Flag host-like structure without a distinctive content root.

    The result is deliberately uncertain: a sampled/pruned tree cannot prove that
    content is absent or attribute the observation to a renderer/toolkit.

    判据全部落在**角色语义**上，一个应用名/进程名都不出现——嵌入式内容宿主
    横跨浏览器、Electron、PDF 查看器、嵌入式 webview，按名字枚举是永远维护
    不完的表：

      1. 存在 role=frame（顶层应用窗口；dialog/menu/popup 不在此列，
         它们本来就不承载内容平面）；
      2. 该 frame 的子树里没有明显的内容根角色；
      3. 该子树里存在"可能承载内容"的结构信号，二者任一：
         a. 标签式文档界面（page tab list / page tab）——按角色定义，
            标签页是**文档的**标签，有标签而无文档即缺内容；
         b. 一个渲染出来没有任何子节点的嵌入式内容容器
            （scroll pane / viewport / embedded / internal frame …）。

    第 3 条是为了信噪比：计算器、设置面板这类窗口本来就没有内容平面，
    只有 1、2 两条会把它们一并报出来。
    """
    if not records:
        return []

    def path_of(record):
        return tuple(record.get("runtimeId") or ())

    def role_of(record):
        return str(record.get("controlType") or "").strip().lower()

    def is_descendant(record, root_path):
        path = path_of(record)
        return len(path) > len(root_path) and path[:len(root_path)] == root_path

    for frame_record in records:
        if role_of(frame_record) != "frame":
            continue
        root_path = path_of(frame_record)
        subtree = [r for r in records if is_descendant(r, root_path)] if root_path else [
            r for r in records if r is not frame_record
        ]
        if not subtree:
            # 一个子节点都没有 = 空壳，那是 snapshot_diagnostics 的题目。
            continue
        # Only distinctive content roots can clear a frame-wide warning. Generic
        # text/table/list roles also occur in toolbars, sidebars, menus and status
        # panes, so their mere presence elsewhere is not evidence that the hosted
        # content plane is represented.
        if any(is_content_plane_role(role_of(r)) for r in subtree):
            continue
        evidence = None
        for record in subtree:
            role = role_of(record)
            if role in {"page tab list", "page tab"}:
                evidence = ("a tabbed document interface (role={}) with no document "
                            "behind any tab".format(role))
                break
        if evidence is None:
            for record in subtree:
                role = role_of(record)
                if role not in EMBEDDED_CONTAINER_ROLES:
                    continue
                child_path = path_of(record)
                if child_path and any(is_descendant(r, child_path) for r in records):
                    continue
                evidence = ("an embedded-content container (role={}) that came back "
                            "with nothing inside it".format(role))
                break
        if evidence is None:
            continue
        title = str(frame_record.get("name") or "").strip() or "(untitled)"
        return [
            "CONTENT PLANE UNCERTAIN: {!r} contains {}, but this sampled accessibility "
            "tree has no distinctive content root (for example document-*, canvas, "
            "web view, page, terminal, or tree table). Ordinary text/table/list nodes "
            "elsewhere in the frame are not enough to prove that the hosted content "
            "itself is represented. This is an uncertain diagnostic, not proof that "
            "the window is empty or that a renderer does not export accessibility; "
            "the content may be collapsed, lazy, pruned, delayed, toolkit-limited, or "
            "visually absent. Inspect the full-desktop screenshot, open/render the "
            "relevant content, and sample again. GUI coordinates come only from gui's "
            "full-desktop screenshot; this accessibility tree retains geometry "
            "internally and does not expose per-node rectangles.".format(title, evidence)
        ]
    return []


def is_dropdown_item(node, window):
    """这个元素是不是"下拉列表里的一项"。

    这类元素上的语义调用有一个特殊的坏处：**它会关掉弹窗，但不提交值**。
    实测 LibreOffice 7.3「格式 → 段落 → 行距」下拉，点 `table cell Double`：

        do_action  -> 返回 True，下拉关闭，控件仍然显示 Single
        坐标点击   -> 下拉关闭，控件显示 Double            （截图核实）

    比"不生效"更糟的是**事后无法校验**：弹窗连同那个元素一起消失了，
    动作前后的树必然不同，通用的"什么都没变就重试"判据在这里不会触发；
    而 LibreOffice 对话框里的 combo box 是个不上报值的幻影节点
    （没有 frame、text 为空），想读回来确认也读不到。
    所以只能在**调用之前**就避开语义通道。

    影响面不小：跨 Calc / Impress / Writer 的轨迹中，下拉和调色板提交
    反复出现，是 LibreOffice 侧的主要阻塞之一。

    判据刻意收得很窄——弹出窗口里的 `table cell`：
    - 菜单项不在此列。Nautilus 右键菜单的 `menu item Rename…` 实测
      do_action 完全正常，一并改掉会把好路也堵死。
    - 主窗口里的表格单元格不在此列。Calc 的单元格用的就是这个角色，
      它们不是下拉项，语义调用没有这个问题。
    """
    if node is None or window is None:
        return False
    if node_role(node) != "table cell":
        return False
    # 下拉在 X11 上是独立顶层窗口，角色 `window` 且没有标题；
    # 应用主窗口是 `frame`，对话框是 `dialog`/`alert`，都带名字。
    return node_role(window) == "window" and not node_name(window)




