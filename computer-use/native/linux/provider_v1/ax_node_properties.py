"""Linux provider module: ax snapshot."""

from .foundation import (
    Atspi,
    BUDGET_PRESSURE_RATIO,
    CLICK_COVERED_ACTIONS,
    DEFAULT_TEXT_LIMIT,
    MAX_CHILD_FANOUT,
    MAX_DEPTH,
    MAX_ELEMENTS,
    MAX_RENDERED_CHARS,
    MENU_ROLES,
    NON_SELF_ACTIONS,
    PIXEL_DIFF_STRIDE,
    PIXEL_DIFF_THRESHOLD,
    SCREENSHOT_MAX_BASE64_CHARS,
    TREE_DEADLINE_SECONDS,
    UNIVERSAL_ACTIONS,
    child_count,
    has_text_iface,
    is_interactive_role,
    limit_text,
    node_name,
    node_pid,
    node_role,
    normalized_action,
    os,
    process_generation,
    provider_bus_name,
    provider_object_path,
    re,
    safe,
    should_enumerate_children,
    state_contains,
    state_value,
    strict_prune_probe,
    time,
)

from .x11_windows import (
    relative_frame,
)
from .primitives import name_fingerprint, quoted
def node_actions(node):
    """**一次读完**该节点的动作表，同时给出渲染需要的两个结论。

    返回 `(未被 click 覆盖的动作名, 是否存在可调用的 click 入口)`。

    为什么必须合成一次读：LibreOffice 的 ATK 桥在被反复问动作表时会打出

        (soffice): CRITICAL: impl_get_NActions:
                   assertion 'ATK_IS_ACTION (user_data)' failed

    密集调用下**应用会整个退出**（实测：对话框循环第 3~4 轮 soffice 消失，
    随后 get_app_state 报 appNotFound）。渲染一棵树本来每个节点就要问一次，
    早先为了给出 `[has-click-action]` 标记又经 preferred_action_index() 问了
    第二次——等于在一个已知脆弱的桥上把压力翻倍。

    动作名与描述在同一轮里取，因此每个节点对 ATK 的问询次数减半。
    """
    # 先确认这个节点真的实现了 Action 接口再问动作数。
    #
    # 不先问就直接调 get_n_actions()，LibreOffice 的 ATK 桥会对着一个不是
    # ATK_ACTION 的对象打断言：
    #     CRITICAL: impl_get_NActions: assertion 'ATK_IS_ACTION (user_data)' failed
    # 而树里绝大多数节点（panel / filler / label / 各种容器）本来就没有这个接口，
    # 于是每渲染一棵树就刷出成百上千条。实测一轮对话框循环刷出 3482 条。
    if safe(node.get_action_iface) is None:
        return [], False
    count = int(safe(node.get_n_actions, 0) or 0)
    names = []
    has_click_entry = False
    for index in range(count):
        name = str(safe(lambda i=index: node.get_action_name(i), "") or "")
        description = str(
            safe(lambda i=index: node.get_action_description(i), "") or ""
        )
        label = name or description
        lower = label.strip().lower()
        # 与 preferred_action_index() 用同一套判据：精确命中，或含
        # activate/click/press 的兜底。两者必须一致，否则标记会撒谎。
        if lower in CLICK_COVERED_ACTIONS or (
            normalized_action(lower) not in NON_SELF_ACTIONS
            and ("activate" in lower or "click" in lower or "press" in lower)
        ):
            has_click_entry = True
        if not label or label in names:
            continue
        if lower in CLICK_COVERED_ACTIONS:
            continue
        # 人人都有的动作不进列表，也就不会让 is_structural_filler 失效。
        if normalized_action(lower) in UNIVERSAL_ACTIONS:
            continue
        names.append(label)
    return names, has_click_entry


def action_names(node):
    """列出该节点**尚未被 click 工具覆盖**的语义动作。

    必须过滤掉 preferred_action_index() 会挑中的那些（click/press/activate/…），
    否则每个可点击节点都会显示 "Secondary Actions: click"，而那恰恰就是
    click 工具自己要调用的动作。重复列出会让模型误以为那是另一条备选路径，
    进而在语义 click 与坐标 fallback 之间反复摇摆，
    甚至退回坐标点击。

    macOS 侧的 meaningfulActions() 出于同样理由过滤掉
    AXPress / AXConfirm / AXOpen / AXShowMenu。这里与之对齐。
    """
    return node_actions(node)[0]


def accessible_id(node):
    getter = getattr(node, "get_accessible_id", None)
    return str(safe(getter, "") or "") if callable(getter) else ""


def text_value_with_status(node, text_limit=DEFAULT_TEXT_LIMIT):
    """Return text plus whether the provider read completed reliably."""
    try:
        text_iface = node.get_text_iface()
    except Exception:
        return "", False
    if text_iface is None:
        return "", True
    try:
        count = int(Atspi.Text.get_character_count(text_iface) or 0)
        if count <= 0:
            return "", True
        end_offset = count if text_limit is None else min(count, text_limit + 1)
        value = str(
            Atspi.Text.get_text(text_iface, 0, end_offset) or ""
        )
    except Exception:
        return "", False
    return limit_text(value, text_limit=text_limit), True


def text_value(node, text_limit=DEFAULT_TEXT_LIMIT):
    value, _complete = text_value_with_status(
        node, text_limit=text_limit
    )
    return value


def numeric_value(node):
    value_iface = safe(node.get_value_iface)
    if value_iface is None:
        return ""
    current = safe(lambda: Atspi.Value.get_current_value(value_iface))
    if current is None:
        return ""
    return str(current)


# 一个下拉框最多往下翻这么多节点去找"当前选中的是哪一项"。
# 选项列表通常几项到几十项，200 足够，而封顶保证这条路径的代价是有界的。
SELECTION_SCAN_BUDGET = 200


def selected_option_name(node):
    """下拉框当前选中的是哪一项。

    这补的是一个**大缺口**：此前 agent 在树里根本看不见任何下拉框的值。
    实测 Chrome 的打印对话框——`combo box "Margins"`、`"Destination"`、
    `"Scale"` 三个，树里全都只有名字、没有值，而任务恰恰要求"把边距设成 None"。
    看不见当前值，就既没法判断要不要改，也没法确认改成功了。

    值不在 Value 接口上（Chrome 的 combo box 根本不实现 Value），也不在 Text
    接口上（实测读回来是一个对象替换符 `￼`）。它在**后代 menu item 的
    SELECTED 状态**上——而且**下拉关着时也读得到**，这一点是实测确认的，
    否则这个功能就得先展开每一个下拉框，代价完全不可接受。

    有些下拉框确实读不到（实测 `combo box "Layout"` 就是 None）。那种情况下
    返回空串、树里不显示值——**不猜**。少给一个信号，好过给一个编出来的。
    """
    if node is None:
        return ""
    stack = [node]
    seen = 0
    while stack and seen < SELECTION_SCAN_BUDGET:
        current = stack.pop()
        seen += 1
        if current is not node and state_contains(current, Atspi.StateType.SELECTED):
            name = str(node_name(current) or "").strip()
            if name:
                return name
        for index in range(min(child_count(current), 100)):
            child = safe(lambda: current.get_child_at_index(index))
            if child is not None:
                stack.append(child)
    return ""


def element_value(node, text_limit=DEFAULT_TEXT_LIMIT):
    direct = text_value(node, text_limit=text_limit) or numeric_value(node)
    if direct:
        return direct
    # 只对下拉类角色做这次扫描，别的角色不付这个代价。
    role = str(node_role(node) or "")
    if role in ("combo box", "list box"):
        return limit_text(selected_option_name(node), text_limit=text_limit)
    return direct


def positive_int(value, fallback):
    if isinstance(value, bool):
        return fallback
    if isinstance(value, float) and not value.is_integer():
        return fallback
    try:
        integer = int(value)
    except (TypeError, ValueError):
        return fallback
    return integer if integer > 0 else fallback


def parse_text_limit(value, fallback=DEFAULT_TEXT_LIMIT):
    if isinstance(value, str) and value.lower() == "max":
        return None
    return positive_int(value, fallback)


# 对决策有用、但此前完全不在树里体现的状态。只渲染"非默认"的那一侧：
# 每个节点都标 enabled/not-focused 只会淹没信号，而 disabled / checked /
# expanded / selected / focused 才是 agent 需要据以决策的信息。
NOTABLE_STATES = (
    ("CHECKED", "checked"),
    ("EXPANDED", "expanded"),
    ("SELECTED", "selected"),
    ("FOCUSED", "focused"),
)


# 动作名 -> 它承诺会改变的东西。**表里的名字全部来自实测**，不是猜的。
#
# 思路照抄 Playwright 的 `_setChecked`（packages/playwright-core/src/server/dom.ts）：
#
#     const finalState = await isChecked(progress);
#     if (finalState.matches !== state)
#       throw new NonRecoverableDOMError('Clicking the checkbox did not change its state');
#
# 它抛的是 **NonRecoverableDOMError**——不重试，直接抛。重复同一个动作不会有
# 不同结果。
#
# **第一版这张表是照 Playwright 的语义写的**（check / uncheck / expand / collapse
# / select / deselect），实测六个应用**一个都没有**，整张表是死代码。
# 真实存在的是下面这些（gedit / Nautilus / VLC / VS Code / GIMP / Thunderbird
# 全量扫出来的动作名）：
#
#     click 1197 / press 144 / activate 138 / expand or contract 119 / edit 119
#     menu 81 / open 80 / click ancestor 75 / showcontextmenu 61 / release 33
#     showmenu 27 / dodefault 18 / setfocus 4 / increase 3 / decrease 3 / toggle 2
#
# 注意 `expand or contract` 和 `toggle` 是**翻转**语义，不是"设成某个值"——
# 所以判据是"必须翻转"，不是"必须等于某值"。
ACTION_MUST_FLIP = {
    "expand or contract": "EXPANDED",
    "expandorcontract": "EXPANDED",
    "toggle": "CHECKED",
}
# 方向明确的动作：值必须变。
ACTION_MUST_CHANGE_VALUE = ("increase", "decrease")


def readable_states(node):
    """读回那几个**可判定**的状态；读不到就返回 None（不返回空字典冒充读到了）。"""
    if node is None:
        return None
    out = {}
    for name, _label in NOTABLE_STATES:
        state = getattr(Atspi.StateType, name, None)
        if state is None:
            continue
        out[name] = state_contains(node, state)
    return out or None


def state_transition_note(action, before, after):
    """动作承诺会改变什么时，动作后回读校验。

    返回 (note, failed)。failed=True 表示这是**不该重试**的失败——
    重复同一个动作不会有不同结果。

    **边界必须写清楚：状态翻转证明的是"控件状态动了"，不是"行为发生了"。**
    本仓库实测过反例：VLC 首选项里那颗单选按钮，`Toggle` 之后 CHECKED 真的
    翻转了，面板却**不切换**。所以这条判据接不住"状态变了行为没变"那一类。
    把它当万能验证会制造假阳性——它只接住一类：**动作承诺翻转，而状态没翻**。
    """
    name = ACTION_MUST_FLIP.get(str(action).strip().lower())
    if name is None or not before or not after:
        return None, False
    was, now = before.get(name), after.get(name)
    if was is None or now is None:
        return None, False
    if was != now:
        return ("[state] {} flipped {} -> {}, which is what the '{}' action promises. "
                "Note the limit: this proves the CONTROL changed, not that the "
                "application acted on it — measured on VLC, a radio button's CHECKED "
                "flipped while the panel it selects did not switch. Confirm the actual "
                "effect before treating this as done."
                .format(name, was, now, action), False)
    return ("The '{}' action reported success, but {} is still {} — that action promises "
            "to flip it, so nothing happened. Repeating the same call will not help; "
            "the toolkit accepted it without acting. Reach the element with a coordinate "
            "click instead (gui, or click with click_method \"global\")."
            .format(action, name, now), True)


def plain_text_from_rich_text(value):
    """把 Qt 的富文本 tooltip 还原成纯文本。

    Qt 会把 tooltip 存成一整段 HTML，带 `<head>` 里的 CSS。VLC 首选项实测：
    19 段这样的 HTML 合计 9149 字符，占整次观测的 **56%**，而其中真正的信息
    往往只有一句话。不处理的话，`Description:` 段会把整个观测预算吃掉。

    判据卡在 `<html>` 开头——这是 Qt 富文本的标志。**不对普通文本做剥离**：
    真实内容里完全可能出现尖括号（代码、模板、数学表达式），
    对它们动手会篡改 agent 读到的数据。
    """
    text = str(value or "")
    if not text.lstrip().lower().startswith("<html"):
        return text
    # `<head>` 里全是 CSS，剥完标签会漏成 `p, li { white-space: pre-wrap; }`，
    # 所以整块丢掉而不是逐个剥标签。
    text = re.sub(r"(?is)<head>.*?</head>", " ", text)
    text = re.sub(r"(?is)<style.*?</style>", " ", text)
    text = re.sub(r"(?i)<br\s*/?>", " ", text)
    text = re.sub(r"(?s)<[^>]+>", "", text)
    for entity, char in (("&amp;", "&"), ("&lt;", "<"), ("&gt;", ">"),
                         ("&quot;", '"'), ("&nbsp;", " ")):
        text = text.replace(entity, char)
    return " ".join(text.split())


def node_description(node, text_limit=DEFAULT_TEXT_LIMIT):
    """取控件的描述文本（AT-SPI `get_description`，GTK 通常填的是 tooltip）。

    实测 Nautilus：工具栏上 `Go back` / `Go forward` / `Search` / `Show list`
    四个按钮**名字全是空的**，唯一的可读标识就在 description 里；而三个
    `toggle button Menu` 名字完全相同，只有 description（`Show operations` /
    `View options`）能区分。不渲染这一项，返回/前进这类文件管理器核心操作
    在树里就只是 `push button`，agent 除了按像素坐标猜没有别的办法——
    a11y 优先的整条路径在这里断掉。

    GTK 的惯例与 macOS 相反：macOS 把可读标签放 AXTitle，GTK 常常只填
    tooltip。所以这不是"移植时漏了"，是 Linux 侧必须多读一处。

    与 name 分开渲染而不是顶替它：name 是元素身份的一部分（轨迹回放、
    裁剪保留率都按 `role + name` 匹配），改写它会让同一个元素在不同版本里
    对不上号。
    """
    getter = getattr(node, "get_description", None)
    raw = str(safe(getter, "") or "") if callable(getter) else ""
    return limit_text(plain_text_from_rich_text(raw), text_limit=text_limit)


PLACEHOLDER_ROLES = {
    "combo box",
    "entry",
    "password text",
    "search box",
    "text",
    "text area",
    "text box",
    "text field",
}


def role_supports_placeholder(role):
    normalized = str(role or "").strip().lower().replace("_", " ")
    return (
        normalized in PLACEHOLDER_ROLES
        or normalized.replace(" ", "") in {
            "searchbox",
            "textarea",
            "textbox",
            "textfield",
        }
    )


def placeholder_text(node, text_limit=DEFAULT_TEXT_LIMIT, role=None):
    """取控件的占位提示文本（AT-SPI 的 `placeholder-text` 对象属性）。

    对齐 macOS 的 `placeholderValue`。必须和真实内容分开渲染：占位文本长得
    像内容但其实是空的，混在一起 agent 会以为输入框已经有值、跳过输入，
    或者把提示语当成数据读走。

    `get_attributes()` 是可选且昂贵的 AT-SPI 对象属性接口，不能为整棵树上的
    panel、shape、table cell 等节点预探测。Impress 在文档结构变更后被这样
    全树扫描会直接退出。placeholder 只对文本输入类角色有定义，因此先按角色
    收窄，再对真正可能携带该字段的节点读取一次。
    """
    effective_role = node_role(node) if role is None else role
    if not role_supports_placeholder(effective_role):
        return ""
    getter = getattr(node, "get_attributes", None)
    attributes = safe(getter) if callable(getter) else {}
    for key in ("placeholder-text", "placeholder"):
        value = attributes.get(key)
        if value:
            return limit_text(str(value), text_limit=text_limit)
    return ""


TEXT_ATTRIBUTES_OF_INTEREST = (
    "justification", "size", "weight", "style", "underline", "strikethrough",
    "family-name", "fg-color", "bg-color", "line-height", "indent",
    "left-margin", "right-margin", "text-decoration",
)


def text_attributes(node):
    """Read selected text-format properties without claiming an empty result."""
    iface = safe(node.get_text_iface)
    if iface is None:
        return None
    raw = safe(lambda: Atspi.Text.get_default_attributes(iface))
    if not raw:
        return None
    try:
        table = dict(raw)
    except Exception:
        return None
    return {key: table[key] for key in TEXT_ATTRIBUTES_OF_INTEREST if key in table}


def render_element_line(record, render_depth, boxes=True):
    """把一条元素记录渲染成一行。文法（借鉴 Playwright 的 aria snapshot）：

        <缩进><index> <role> "<name>" [<states>] [desc="…"] [placeholder="…"]
                     [actions=a,b] {x,y,w,h}: "<value>"

    - 所有自由文本一律**加引号**，消掉旧格式里名字与字段分隔符撞车的歧义
    - 附加属性一律进**方括号**，与 aria snapshot 的 `[checked]` `[level=1]` 同构
    - 几何压成 `{x,y,w,h}`：旧写法 `Frame: {x: 687, y: 23, width: 64, height: 46}`
      是 45 字符，新写法 14 字符，**每行省 31 字符而信息不少一个字**。
      实测几何占整棵树的 35–50%，这一项就能砍掉其中约三分之二。
    - `boxes=False` 时整段几何都不渲染（对齐 Playwright 的 `boxes` 开关）。
      **这不削弱任何能力**：`click(element_index)` 的坐标是服务端内部从记录里
      取的，不经过这一行；而 agent 需要坐标时截图恒带、且与树同一个坐标空间
      （已实测：真实 agent 只靠截图命中了 a11y 树里零存在的 GIMP 画布目标）。
    - 值放在**最后、冒号之后**，对齐 aria 的 `- textbox: Enter your name`
    - 空的段一律省略，不留空壳
    - `index` 保留在行首：我们没有 selector，它是唯一的引用手段
    """
    role = record["localizedControlType"] or record["controlType"] or "element"
    title = record["name"] or record["automationId"] or ""
    parts = ["{} {}".format(record["index"], role)]
    if title:
        parts.append(quoted(title))
    state_seg = record.get("states", "")
    if state_seg:
        parts.append(state_seg.strip())
    if record.get("description") and record["description"] != title:
        # 单独一段，不并进 name：name 是元素身份的一部分（轨迹按 role+name
        # 匹配）。GTK 把可读标签放在这里的情况很常见，尤其是纯图标按钮。
        parts.append("[desc=" + quoted(record["description"]) + "]")
    if record.get("placeholder") and record["placeholder"] != title:
        # 提示不是内容——控件其实是空的，所以绝不能混进值里。
        parts.append("[placeholder=" + quoted(record["placeholder"]) + "]")
    if record["actions"]:
        parts.append("[actions=" + ",".join(record["actions"]) + "]")
    if boxes and record["frame"] is not None:
        f = record["frame"]
        parts.append("{{{0},{1},{2},{3}}}".format(
            round(f["x"]), round(f["y"]), round(f["width"]), round(f["height"])))
    line = ("\t" * (render_depth + 1)) + " ".join(parts)
    if record["value"] and record["value"] != title:
        line += ": " + quoted(record["value"])
    return line


def state_segment(node, has_click_action=False, borrowed_name=False):
    """把值得关注的状态渲染成紧凑标记，如 `[checked expanded]`。

    **只报告"存在即有意义"的状态，绝不从状态的缺失反推语义。**

    这条规则是踩坑换来的：最初这里会在缺少 `ENABLED` 时标 `[disabled]`，
    实测发现 Nautilus 的文件图标根本不设 `ENABLED` / `SENSITIVE`——
    它们状态里只有 `SHOWING, VISIBLE, FOCUSABLE`，却带着 `open` / `menu` 两个动作，
    完全可操作。被误标成 disabled 之后，agent 会直接跳过真正的目标。

    对照组同样说明问题：gedit 那个被我一度当成"禁用"的 `Clear Highlight`，
    其实是**没有 SHOWING**（藏在折叠的搜索面板里），不是禁用。

    结论：AT-SPI 的 ENABLED/SENSITIVE 在 Linux 上不可靠，不同工具包设不设全凭自觉，
    没有可靠判据能区分"真禁用"与"这个工具包不上报"。宁可少给一个信号，
    也不能给一个会让 agent 跳过可用目标的假信号。
    """
    marks = []
    for name, label in NOTABLE_STATES:
        state = getattr(Atspi.StateType, name, None)
        if state is not None and state_contains(node, state):
            marks.append(label)
    # `clickable` 不是 AT-SPI 状态，是一条**能力**判断，但它属于同一个括号组：
    # agent 扫一眼就该知道这个元素能不能直接按元素点。
    #
    # 为什么必须显式标出来：action_names() 会隐藏 click 类动作（否则每个可点节点
    # 都显示 "More actions: click"，模型会以为那是 click 工具之外的另一条路，
    # 于是在两个工具之间反复摇摆）。但一并隐藏之后，"有语义点击"和"根本没有动作
    # 接口"在树里长得**一模一样**：
    #     3  push button Home              <- 有 click 动作，可直接调
    #     12 list item  Recent files       <- 没有 Action 接口，只能坐标点
    # agent 无从分辨，只好一律退回坐标——恰好背离 a11y 优先。
    #
    # 实测这是少数派（Nautilus 10 个可点 vs 103 个不可点，gedit 7 vs 30），
    # 所以标可点的比标不可点的省一个数量级。
    #
    # 判据必须与 click 工具实际调用的入口同源（preferred_action_index），
    # 否则这个标记本身就成了新的谎言。
    if has_click_action:
        # 措辞刻意是"有一个动作"而不是"点得动"。它保证的是 click 工具能在这个
        # 元素上找到可调用的动作，**不保证那个动作生效**——实测 Nautilus /
        # GIMP / VLC 三个应用的动作都会返回成功却什么都不做。
        # 叫 `clickable` 会被读成"点这里就行"，那就是工具在替 agent 打包票。
        marks.append("has-click-action")
    # 名字是从 LABELLED_BY 借来的就要标出来，理由不是洁癖：同一个「位置和大小」
    # 对话框里 `Position Y` 出现了**两次**（两个标签页各一个）。调用方需要知道
    # 这个名字是借来的、因此可能不唯一——选择器命中多个时会报歧义，
    # 那时这个标记就是解释。
    if borrowed_name:
        marks.append("labelled")
    if not marks:
        return ""
    return " [" + " ".join(marks) + "]"
