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
from .ax_node_properties import (
    accessible_id,
    action_names,
    element_value,
    node_actions,
    node_description,
    numeric_value,
    parse_text_limit,
    placeholder_text,
    plain_text_from_rich_text,
    positive_int,
    readable_states,
    render_element_line,
    selected_option_name,
    state_segment,
    state_transition_note,
    text_attributes,
    text_value,
    text_value_with_status,
)
from .ax_records import (
    StableIndexer,
    effective_name,
    labelled_by_name,
    record_for,
    semantic_relations,
)
from .ax_virtual_table import (
    render_visible_cells,
    spreadsheet_a1,
    visible_cell_range,
)

def render_tree(root, window_bounds, root_path, text_limit=DEFAULT_TEXT_LIMIT,
                boxes=True, indexer=None,
                max_tree_nodes=MAX_ELEMENTS, max_tree_depth=MAX_DEPTH, prune=True,
                max_rendered_chars=MAX_RENDERED_CHARS,
                deadline_seconds=TREE_DEADLINE_SECONDS,
                include_annotations=False):
    records = []
    lines = []
    annotations = []
    no_child_count_hint = object()
    unknown_child_count = object()
    provider_probe_errors = {}

    # 每一类省略都单独计数：处方必须跟着原因走，合并计数就没法给出**匹配的**处方。
    dropped = {"count": 0, "chars": 0, "time": 0, "depth": 0,
               "budget": 0, "filtered": 0, "pressure": 0,
               "render_error": 0, "fanout": 0}
    budget = {"chars": 0}
    deadline = time.monotonic() + deadline_seconds if deadline_seconds else None
    pressure_at = max(1, int(max_tree_nodes * BUDGET_PRESSURE_RATIO))

    def record_provider_errors(errors):
        for field in errors:
            provider_probe_errors[field] = (
                provider_probe_errors.get(field, 0) + 1
            )

    def strict_child_count(node):
        try:
            return max(0, int(node.get_child_count() or 0))
        except Exception as exc:
            record_provider_errors(
                {"child_count": type(exc).__name__}
            )
            return unknown_child_count

    def child_entries(node, count_hint=no_child_count_hint):
        """Return a lazy bounded child iterator plus enumerability metadata."""
        count = (
            strict_child_count(node)
            if count_hint is no_child_count_hint
            else count_hint
        )
        if count is unknown_child_count:
            if state_contains(node, Atspi.StateType.MANAGES_DESCENDANTS):
                return iter(()), False, None

            def unknown_children():
                consecutive_errors = 0
                reached_cap = True
                for child_index in range(MAX_CHILD_FANOUT):
                    try:
                        child = node.get_child_at_index(child_index)
                    except IndexError:
                        reached_cap = False
                        break
                    except Exception as exc:
                        record_provider_errors(
                            {"child_at": type(exc).__name__}
                        )
                        consecutive_errors += 1
                        yield child_index, None
                        if consecutive_errors >= 8:
                            reached_cap = False
                            break
                        continue
                    consecutive_errors = 0
                    if child is None:
                        reached_cap = False
                        break
                    yield child_index, child
                if reached_cap:
                    record_provider_errors(
                        {"unknown_fanout": "bounded"}
                    )

            return unknown_children(), True, None

        if not should_enumerate_children(node, count):
            return iter(()), False, count
        fanout = min(count, MAX_CHILD_FANOUT)
        if count > fanout:
            excess = count - fanout
            dropped["count"] += excess
            dropped["fanout"] += excess

        def known_children():
            for child_index in range(fanout):
                try:
                    child = node.get_child_at_index(child_index)
                except Exception as exc:
                    record_provider_errors(
                        {"child_at": type(exc).__name__}
                    )
                    yield child_index, None
                    continue
                if child is None:
                    record_provider_errors({"child_at_none": "missing"})
                yield child_index, child

        return known_children(), True, fanout

    def is_structural_filler(record):
        """无语义、无状态、无操作能力的纯布局容器。"""
        return not (
            record["name"]
            or record["description"]
            or record["actions"]
            or record["value"]
            or record["placeholder"]
            or record["states"]
            or is_interactive_role(record["controlType"])
        )

    # 纯文本角色。它们没有可操作性，唯一的价值是"说出一段字"。
    TEXT_ONLY_ROLES = {"static", "label", "paragraph", "heading", "caption"}

    def visit_impl(node, depth, path, render_depth=0, parent_text=""):
        """depth 用于遍历预算，render_depth 用于缩进。

        两者必须分开：被裁掉的节点仍会递归子节点，若沿用 depth 做缩进，
        子节点会带着父节点的层级出现，树里就凭空多出空档——实测 Nautilus 上
        缩进会从第 1 层直接跳到第 6 层，读起来像断掉的树。
        render_depth 只在节点**真的被渲染**时才加一。
        """
        if node is None:
            return
        if len(records) >= max_tree_nodes:
            if node is not None:
                dropped["count"] += 1
                dropped["budget"] += 1
            return
        if depth > max_tree_depth:
            dropped["count"] += 1
            dropped["depth"] += 1
            return
        # 字符预算与节点预算是两件事，见 MAX_RENDERED_CHARS。
        if budget["chars"] >= max_rendered_chars:
            dropped["count"] += 1
            dropped["chars"] += 1
            return
        # 时间预算与前两者都是两件事，见 TREE_DEADLINE_SECONDS。
        # **根节点豁免**：一棵空树没有任何可寻址的元素，调用方连"这是什么窗口"
        # 都答不上来——maka-cu 在同一处也留了这个豁免，理由相同。
        if deadline is not None and records and time.monotonic() >= deadline:
            dropped["count"] += 1
            dropped["time"] += 1
            return

        child_count_hint = no_child_count_hint

        # 裁剪：只保留"可操作角色 + 屏幕上可见"的节点。
        # 离线轨迹回放达到 22% 压缩率、100% 目标保留率。被裁的只是它自己这一行，**仍然继续递归
        # 子节点**——中间容器往往正是有价值控件的父节点，连子树一起砍会适得其反。
        #
        # 判据**先只读它真正用到的四个字段**，不要先建整条记录。
        # 实测 GIMP（GAIL）：完整的 record_for 是 8.45ms/节点，而一次 render_tree
        # 调用它 3162 次却只产出 157 条记录——**95% 的开销当场丢弃**，
        # 占渲染耗时的 87%，整棵树 38s，超过 Go 层 30s 的超时。
        # 也就是说 a11y 通道在 GIMP 上**默认根本用不了**，而原因只是取数顺序。
        #
        # 这四个字段合计约 2.3ms；状态段(2.52)、动作表(1.78)、值(0.92)、
        # 占位符(0.58)、automationId(0.57) 只在节点确定保留时才读。
        # 幸存者会把这四个字段重读一遍，但幸存率只有 5%，重读的代价可以忽略。
        if prune and depth > 0:
            probe, probe_errors = strict_prune_probe(
                node, window_bounds, text_limit
            )
            record_provider_errors(probe_errors)
            child_count_hint = probe["child_count"]
            if (
                child_count_hint is None
                and "child_count" in probe_errors
            ):
                child_count_hint = unknown_child_count
            probe_frame = probe["frame"]
            probe_role = probe["role"]
            # 有名字的可见节点一律保留，哪怕角色不"可交互"。
            # 实测教训：行距 combo 的 toggle button 本身没有名字，agent 只能靠
            # 父节点 `panel Line Spacing` 指认它。纯按角色白名单裁掉这个 panel，
            # 目标元素虽然还在树里，却**没法被指认**——整条对话框链路当场断掉。
            # 保留率指标只看"目标在不在"，看不到这一层，是它的盲区。
            probe_name = probe["name"]
            keeps = bool(probe_errors) or (
                probe_frame is not None and (
                    is_interactive_role(probe_role)
                    or bool(probe_name)
                    or bool(probe["description"])
                )
            )
            # **文字与父节点完全重复的纯文本叶子不保留。**
            #
            # "有名字的可见节点一律保留"那条规则是为桌面对话框定的（见上面
            # Line Spacing 的例子），但在网页上"有名字"等于"所有文字"。
            # 实测 chrome://flags：521 个纯文本节点里 **212 个（41%）的文字与
            # 父节点逐字相同**——父节点已经把这句话说过一遍了，
            #     paragraph […]: "You will start with the entrypoint file…"
            #         static "You will start with the entrypoint file…"
            # 第二行没有增加任何信息，却要付同样的字节。
            #
            # 只裁**没有子节点**的纯文本角色：有子节点的容器仍然可能是指认
            # 无名控件的唯一途径，那正是 Line Spacing 那条教训。
            if (keeps and probe_role in TEXT_ONLY_ROLES and probe_name
                    and parent_text and probe_name == parent_text
                    and child_count_hint == 0):
                keeps = False
            if not keeps:
                dropped["count"] += 1
                dropped["filtered"] += 1
                children, _, _ = child_entries(
                    node, child_count_hint)
                for child_index, child in children:
                    if child is None:
                        continue
                    # 本节点没渲染，render_depth 不推进，子节点顶替它的位置
                    visit(child, depth + 1,
                          path + [child_index], render_depth, parent_text)
                return

        record = record_for(node, None, path, window_bounds, text_limit=text_limit)

        # 预算吃紧时优先保住有名字/有动作/有值的节点。丢容器只丢它自己这一行，
        # 仍然继续递归子节点——被丢的容器往往正是有价值控件的父节点。
        if (
            prune
            and len(records) >= pressure_at
            and is_structural_filler(record)
            and depth > 0
        ):
            dropped["count"] += 1
            dropped["pressure"] += 1
            children, _, _ = child_entries(node, child_count_hint)
            for child_index, child in children:
                if child is None:
                    continue
                visit(child, depth + 1,
                      path + [child_index], render_depth, "")
            return

        # 编号要**跨快照存活**，所以不能用 len(records) 这种遍历序号——
        # 插入一个元素就会把它后面所有元素的号全推走。见 StableIndexer。
        #
        # 发号必须在**确定这个节点真的进树之后**。原来它在两处裁剪之前，
        # 于是被裁掉的节点先领了号再被丢弃：号被消耗、却不进 records/refs。
        # 实测 Nautilus 一次快照 91 个元素、下标散布在 0..670——7 倍膨胀。
        # 而且被裁节点每次快照都重新领一批新号（它们不在 known 里），
        # 所以号会随会话单调涨下去，越涨越长、越涨越不像"树里的位置"。
        record["index"] = (indexer.index_for(path, record["controlType"],
                                             record["name"],
                                             record.get("nameHash", ""))
                           if indexer is not None else len(records))
        records.append(record)

        rendered = render_element_line(record, render_depth, boxes=boxes)
        lines.append(rendered)
        budget["chars"] += len(rendered) + 1
        role = record["localizedControlType"] or record["controlType"] or "element"

        # 未展开的菜单：保留节点自身（它仍是普通 click 的语义入口），
        # 但不递归其子项。role 与 state 都走 libatspi 本地缓存，零 D-Bus 成本。
        if (
            depth > 0
            and role.lower() in MENU_ROLES
            and not state_contains(node, Atspi.StateType.SHOWING)
        ):
            pending = (
                strict_child_count(node)
                if child_count_hint is no_child_count_hint
                else child_count_hint
            )
            if pending is unknown_child_count:
                pending = 0
            if pending:
                annotations.append({
                    "parentRuntimeId": path[:],
                    "kind": "hidden_children",
                    "count": int(pending),
                    "reason": "collapsed_menu",
                })
                lines.append(
                    ("\t" * (render_depth + 2))
                    + "(hidden-items={}; open menu, then observe)".format(
                        pending)
                )
            return

        children, children_enumerable, planned_children = child_entries(
            node, child_count_hint
        )
        if not children_enumerable:
            # 超大/自管理容器不能枚举，改用坐标寻址取当前视口内的单元格。
            rendered = render_visible_cells(
                node, render_depth, path, window_bounds, records, lines,
                text_limit=text_limit, max_tree_nodes=max_tree_nodes,
                boxes=boxes,
                # **必须显式传进去。** 这个函数是模块级的，看不到 render_tree
                # 的局部变量 indexer——漏传时它在函数体里是个自由名字，
                # Python 直到真正执行到那一行才报 NameError。
                # 于是这条路径一走就崩，而它正是 LibreOffice Calc 表格的渲染路径
                # （自管理容器拒绝枚举后的替代路径）。
                # 实测 Calc 快照曾连续返回 `name 'indexer' is not defined`，
                # 使调用方被迫放弃语义通道。
                indexer=indexer,
                annotations=annotations,
            )
            if rendered == 0:
                # 寻址也失败时显式说明，避免模型以为这里本来就是空的。
                lines.append(
                    ("\t" * (render_depth + 2))
                    + "(contents not enumerated: {} manages its own descendants "
                      "and cell addressing failed)".format(role)
                )
            return

        # 配额检查必须在 child_at() **之前**。visit() 开头虽然也查配额并立刻
        # return，但 range(child_count) 这个循环本身不会停 —— 面对谎报十亿
        # 子节点的容器，光是发起那些注定被丢弃的 child_at() 往返就要数十小时。
        # 这是比 should_enumerate_children() 更底层的兜底：后者依赖容器正确
        # 声明 MANAGES_DESCENDANTS，而这里不依赖任何声明。
        before_records = len(records)
        before_lines = len(lines)
        child_iterator = iter(children)
        child_slots_seen = 0
        while True:
            if len(records) >= max_tree_nodes:
                remaining = (
                    max(1, planned_children - child_slots_seen)
                    if planned_children is not None
                    else 1
                )
                dropped["count"] += remaining
                dropped["budget"] += remaining
                break
            try:
                child_index, child = next(child_iterator)
            except StopIteration:
                break
            child_slots_seen += 1
            if child is None:
                continue
            # 本节点渲染出来了，子节点缩进推进一级
            # 把本节点渲染出来的文字传下去，供子节点判断"我是否只是重复了父亲"。
            visit(child, depth + 1, path + [child_index], render_depth + 1,
                  str(record.get("name") or record.get("value") or ""))

        # 菜单的子项**一个都没渲染出来**时，在这个菜单自己身上说明白。
        #
        # 上面那条 "N items collapsed" 的判据是「菜单自己不 SHOWING」，
        # 而 GIMP（GAIL）根本不是这样：File 菜单自己**是** SHOWING 的——它就挂在
        # 菜单栏上——不 SHOWING 的是它底下那 39 个菜单项。于是那条判据不触发，
        # 39 项被逐个裁掉，agent 看到的是光秃秃一行 `menu "File"`，
        # 和一个真正的空菜单**长得一模一样**。
        #
        # 后果是实测到的：第 47、48 题里 agent 要把图导出，而树里
        # 找不到任何 Export/Save As——它只能得出"这个应用没有导出"的结论。
        # 真相是 File 底下就有 `Export to <文件名>`，点一下 File 就全出来了。
        # 树末尾那句全局的 "3092 node(s) omitted" 救不了它：那句话不指名道姓，
        # 没人能从中知道**是这个菜单**藏了东西。
        #
        # 判据改成看**结果**而不是看状态位：递归完一圈，如果这个菜单一条子记录
        # 都没产出、而 AT-SPI 说它有子节点，就照实说有多少项藏着。
        if (child_slots_seen and len(records) == before_records
                and len(lines) == before_lines
                and role.lower() in MENU_ROLES):
            lines.append(
                ("\t" * (render_depth + 2))
                + "(hidden-items={}; open menu, then observe)".format(
                    child_slots_seen)
            )
            annotations.append({
                "parentRuntimeId": path[:],
                "kind": "hidden_children",
                "count": int(child_slots_seen),
                "reason": "provider_children_not_rendered",
            })

    def visit(node, depth, path, render_depth=0, parent_text=""):
        records_before = len(records)
        lines_before = len(lines)
        chars_before = budget["chars"]
        try:
            return visit_impl(
                node, depth, path, render_depth, parent_text
            )
        except Exception as exc:
            # Roll back any partial records/lines from this node's subtree. Stable
            # index numbers consumed during the failed attempt may remain unused,
            # but no hidden actionable ref is published.
            del records[records_before:]
            del lines[lines_before:]
            budget["chars"] = chars_before
            if node is None:
                return
            dropped["count"] += 1
            dropped["render_error"] += 1
            if len(records) >= max_tree_nodes:
                dropped["budget"] += 1
                return
            role = str(safe(lambda: node_role(node), "element") or "element")
            name = limit_text(
                str(safe(lambda: effective_name(node), "") or ""),
                text_limit=text_limit,
            )
            deadline_expired = (
                deadline is not None and time.monotonic() >= deadline)
            collapsed_menu = (
                depth > 0
                and role.lower() in MENU_ROLES
                and not state_contains(node, Atspi.StateType.SHOWING)
            )
            children = []
            children_allowed = False
            if not deadline_expired and not collapsed_menu:
                children, children_allowed, _ = child_entries(node)
            if deadline_expired:
                child_status = "subtree not traversed because the time budget expired"
            elif collapsed_menu:
                child_status = (
                    "subtree not traversed because the menu is collapsed"
                )
            elif not children_allowed:
                child_status = (
                    "subtree not traversed because the container is not safely enumerable"
                )
            else:
                child_status = "eligible healthy children follow"
            diagnostic = (
                ("\t" * (render_depth + 1))
                + "(accessibility node unavailable; no public ref was issued; {})"
                .format(child_status)
            )
            if budget["chars"] < max_rendered_chars:
                remaining = max_rendered_chars - budget["chars"]
                diagnostic = diagnostic[:remaining]
                if diagnostic:
                    lines.append(diagnostic)
                    budget["chars"] += len(diagnostic) + 1
            if deadline_expired:
                dropped["time"] += 1
                return
            if not children_allowed:
                return
            child_iterator = iter(children)
            while True:
                if len(records) >= max_tree_nodes:
                    dropped["count"] += 1
                    dropped["budget"] += 1
                    break
                try:
                    child_index, child = next(child_iterator)
                except StopIteration:
                    break
                if child is None:
                    continue
                visit(
                    child,
                    depth + 1,
                    path + [child_index],
                    render_depth,
                    parent_text,
                )

    visit(root, 0, root_path)
    if dropped["count"]:
        # 省略账本保留原因、计数和可恢复路径，但不在每次快照里重复整段教程。
        # 完整语义已经在工具描述中；这里的职责是报告这一次到底缺了什么。
        reasons = []
        if dropped["time"]:
            reasons.append(
                "{} not visited: time budget exhausted ({:.0f}s); tree is partial; "
                "raising node limits does not fix this".format(
                    dropped["time"], deadline_seconds))
        if dropped["chars"]:
            reasons.append(
                "{} not rendered: the server-owned character budget was exhausted "
                "({} chars); use AX mode=find or a rooted observation for the needed "
                "branch".format(
                    dropped["chars"], max_rendered_chars))
        if dropped["budget"]:
            reasons.append(
                "{} not traversed: the server-owned node budget filled up "
                "({} canonical records); use AX mode=find or a rooted observation "
                "instead of requesting a larger whole-window tree".format(
                    dropped["budget"], max_tree_nodes))
        if dropped["depth"]:
            reasons.append(
                "{} subtree root(s) not traversed: the server-owned depth budget "
                "({}) was reached. Their descendants are absent from this snapshot; "
                "AX mode=find cannot discover nodes the provider did not expose".format(
                    dropped["depth"], max_tree_depth))
        if dropped["filtered"]:
            reasons.append(
                "{} filtered: not interactable and/or not on screen "
                "(rendered {}/{}); AX mode=find searches canonical records hidden "
                "only by the working projection, and a rooted observation can return "
                "local context".format(
                    dropped["filtered"], len(records), max_tree_nodes))
        if dropped["pressure"]:
            reasons.append(
                "{} unnamed layout containers dropped after {:.0%} node pressure; "
                "children remain listed".format(
                    dropped["pressure"], BUDGET_PRESSURE_RATIO))
        if dropped["render_error"]:
            reasons.append(
                "{} node(s) lost to isolated per-node exception; no stable ref; "
                "sibling and eligible-child traversal continued; budget-stopped roots "
                "reported separately".format(
                    dropped["render_error"]))
        if dropped["fanout"]:
            reasons.append(
                "{} child root(s) not traversed because a container exceeded "
                "MAX_CHILD_FANOUT={}; omitted subtrees are absent from this snapshot"
                .format(dropped["fanout"], MAX_CHILD_FANOUT))
        lines.append("({} node(s) omitted — {})".format(
            dropped["count"], "; ".join(reasons) if reasons else "cause not recorded"))
        annotations.append({
            "parentRuntimeId": root_path[:],
            "kind": "provider_omission",
            "count": int(dropped["count"]),
            "reason": ",".join(
                key for key in (
                    "time", "chars", "budget", "depth", "filtered",
                    "pressure", "render_error", "fanout",
                ) if dropped[key]
            ),
            "detail": "; ".join(reasons) if reasons else "cause not recorded",
        })
    if provider_probe_errors:
        error_count = sum(provider_probe_errors.values())
        annotations.append({
            "parentRuntimeId": root_path[:],
            "kind": "provider_omission",
            "count": int(error_count),
            "reason": "provider_probe_error",
            "detail": (
                "accessibility provider reads were incomplete; missing "
                "descendants are unknown"
            ),
        })
    window_bus = provider_bus_name(root)
    window_path = provider_object_path(root)
    window_generation = process_generation(node_pid(root))
    window_role = node_role(root)
    window_name = node_name(root)
    for record in records:
        record["providerWindowBusName"] = window_bus
        record["providerWindowObjectPath"] = window_path
        record["providerWindowProcessGeneration"] = window_generation
        record["providerWindowRole"] = window_role
        record["providerWindowName"] = window_name

    if include_annotations:
        return records, lines, annotations
    return records, lines


# 像素比对的采样步长与阈值。
# 实测 1850x1053 窗口：抓一张 23ms，4px 步长比对 67ms、8px 步长 16ms。
# 取 8px——每个动作多约 40ms，相对 resolve_app 自己的 0.3s 可以忽略，
# 而 8px 足以看出一段文字挪位置、一个对话框开合。
# 判据不用"变化超过某个百分比"，用**持续性**——见 persistent_pixel_change。
#
# 走过两版弯路，都记下来：
#  1. 一开始设了个固定阈值 PIXEL_FAINT_PERCENT=0.05%，低于它就算"微弱"。
#     这是**猜的**，而 Playwright 恰恰拒绝猜：它的 toHaveScreenshot 把
#     maxDiffPixels / maxDiffPixelRatio **默认留空**交给调用方，对噪声源
#     采取的是消除（caret:"hide"、animations:"disabled" 都是默认值）。
#  2. 第二版改成"动作前连抓两张测噪声底"。方向对，但两张只隔几毫秒，
#     1Hz 的文本光标根本没来得及闪，噪声底测出来是 0——于是**空操作也被判成
#     "屏幕变了"**，变化区域正是那个 8x16 的光标。
#
# 现在照 Playwright"反复截图直到连续两张一致再比"的思路：动作后抓两张，
# 只认在两张里**都存在**的变化。闪烁的东西两张状态不同，自然被滤掉。
# 两张之间的间隔由建快照的耗时填上，几乎不额外增加延迟。
