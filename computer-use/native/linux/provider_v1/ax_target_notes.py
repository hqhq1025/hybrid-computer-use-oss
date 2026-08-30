"""Target hit-test and model-facing action notes."""

from .foundation import (
    Atspi,  # pyright: ignore[reportAttributeAccessIssue]
    node_name,
    node_role,
    safe,
)
from .ax_snapshot import effective_name

A11Y_CHANNEL = "[a11y]"
GUI_CHANNEL = "[gui]"
# 坐标命中测试最多往下钻几层。AT-SPI 的 get_accessible_at_point 每次只返回
# **直接子节点**，要拿到叶子必须自己递归。给个上限，别在环形结构上转不出来。
HIT_TEST_MAX_DEPTH = 25


def element_at_point(window, x, y):
    """报出屏幕坐标 (x, y) 上的可访问元素。**只是提示，不是保证。**

    裸坐标点击目前是个纯盲点：工具打完就走，说不出打到了什么。有了这个
    至少能把"你以为点的"和"实际落在哪"对上一次。

    但准确率**因工具包而异**，实测（递归到叶子，拿元素自身中心点回测）：
        gedit(GTK) 11/11   Nautilus(GTK) 19/25
        LibreOffice(VCL) 12/25   VLC(Qt) 2/25
    VCL 与 Qt 上经常只解析到容器层。所以这条信息必须**明确标成提示**发出去，
    不能让 agent 拿它当"我点中了正确的东西"的证据——那会用一个新的谎
    去替换旧的沉默。
    """
    node = window
    for _ in range(HIT_TEST_MAX_DEPTH):
        component = safe(node.get_component_iface)
        if component is None:
            break
        child = safe(
            lambda: Atspi.Component.get_accessible_at_point(
                component, int(round(x)), int(round(y)), Atspi.CoordType.SCREEN
            )
        )
        if child is None:
            break
        node = child
    if node is window:
        return None
    return node


def describe_hit(window, x, y):
    """把命中结果写成一句可以直接拼进 Note 的话；测不出来就返回空串。"""
    node = safe(lambda: element_at_point(window, x, y))
    if node is None:
        return (
            " Hit test could not identify any element under that point, so this click "
            "is unverified: it may have landed on nothing, or on something this "
            "toolkit does not expose."
        )
    role = node_role(node)
    name = node_name(node)
    label = "{} {!r}".format(role, name) if name else role
    return (
        " Hit test says the element under that point is {} — treat this as a HINT, not "
        "proof: AT-SPI hit testing resolves only to a container on some toolkits "
        "(measured accurate 11/11 on GTK gedit but 12/25 on LibreOffice). If it names "
        "something other than your intended target, re-read the tree before "
        "continuing.".format(label)
    )


def addressing_channel(element_record):
    """这次动作的目标是靠树定位的，还是靠裸坐标定位的。"""
    return A11Y_CHANNEL if element_record else GUI_CHANNEL

UNVERIFIED_SYNTHESIS = (
    "Delivery to the intended target was not verified: AT-SPI input synthesis is "
    "global and reports success as soon as the event is queued."
)

# 语义调用同样不能当成"生效"的证据。这不是保守措辞，是实测结论：
#   Nautilus  文件图标的 `menu`   -> do_action True，菜单一个都不弹（焦点/选中三种前置都试过）
#   GIMP      图层 cell 的 `activate` -> do_action True，活动图层不变（截图 0 像素差异）
#   VLC       单选按钮的 `Toggle`  -> CHECKED 真的翻转了，面板却不切换
# 最后一条尤其要紧：**判据不能读被操作节点自身的状态**，状态会跟着变、行为没有。
#
# do_action 的返回值只说明工具包接受了这次调用，不说明界面发生了任何事。
# 把它当成成功上报，等于向 agent 谎报事实——它会据此推进下一步，
# 而真实界面还停在原地。
UNVERIFIED_SEMANTIC = (
    "The toolkit accepted the call; that is not evidence the action took effect. "
    "AT-SPI actions on Linux routinely report success while doing nothing. "
    "Confirm from the returned state before treating this as done."
)

# 走了坐标兜底时的即时纠偏。只在元素定向本可用时才有意义，所以由调用方决定是否附加。
PREFER_ELEMENT_INDEX = (
    " This did not use element-targeted invocation. If the target appears in the "
    "accessibility tree, prefer AX click with target.ref — it is verified, cheaper, "
    "and does not steal focus."
)


def resolved_note(element, element_record):
    """说清这次动作**作用在了谁身上**。

    此前动作 Note 只说"做了什么"，不说"对谁做的"。传选择器时尤其要紧：
    `push button "Save"` 最终解析到哪个节点，调用方完全看不见——而解析是
    多级回退的（runtimeId 路径 → automationId → name+role → role+几何），
    退到第几级、退到了谁身上，都不透明。

    报的是**活节点**读到的 role 与 name，不是快照记录里那份。两者不一致时
    单独警告：那意味着按路径解析到的已经不是原来那个控件了，正是本仓库
    反复修过的那类静默走偏——record_still_matches 里记着 Nautilus 的实例：
    菜单关掉之后同一个 index 9 解析到了工具栏的"切换视图选项"，
    于是"重命名"变成了别的操作，而且一路 isError=False。

    """
    if element is None or not element_record:
        return None
    index = element_record.get("index")
    live_role = str(node_role(element) or "")
    live_name = str(effective_name(element) or "")
    if live_name:
        shown = "{} {!r}".format(live_role, live_name)
    else:
        shown = "an unnamed {}".format(live_role or "element")
    note = "Resolved the target ref to {}.".format(shown)

    was_role = str(element_record.get("controlType") or "")
    was_name = str(element_record.get("name") or "")
    drifted = []
    if was_role and was_role != live_role:
        drifted.append("role was {!r}, now {!r}".format(was_role, live_role))
    if was_name and was_name != live_name:
        drifted.append("name was {!r}, now {!r}".format(was_name, live_name))
    if drifted:
        note += (
            " WARNING: this no longer matches the snapshot you read ({}). The "
            "reference resolved to a DIFFERENT control than the one you picked; "
            "re-read the tree before trusting this action.".format("; ".join(drifted))
        )
    return note


