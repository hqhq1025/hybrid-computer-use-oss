"""Canonical AX records and stable local indexing."""

from .foundation import (
    Atspi,  # pyright: ignore[reportAttributeAccessIssue]
    DEFAULT_TEXT_LIMIT,
    limit_text,
    node_name,
    node_pid,
    node_role,
    process_generation,
    provider_bus_name,
    provider_object_path,
    safe,
    state_value,
)
from .x11_windows import relative_frame
from .primitives import name_fingerprint
from .ax_node_properties import (
    accessible_id,
    element_value,
    node_actions,
    node_description,
    placeholder_text,
    state_segment,
    text_attributes,
)

class StableIndexer:
    """给元素发编号，让编号**跨快照存活**。

    照抄 Playwright 的 `ariaSnapshot.ts`：

        let ariaRef = element._ariaRef;
        if (!ariaRef || ariaRef.role !== role || ariaRef.name !== name) {
          ariaRef = { role, name, ref: 'e' + (++lastRef) };
          element._ariaRef = ariaRef;
        }

    **ref 不是遍历序号，是挂在元素身上、以 (role, name) 为失效条件的缓存 id。**
    同一个元素只要 role 和 name 都没变，跨多少次快照都是同一个号；
    role 或 name 变了就重新发号（因为它已经不是"同一个东西"了）。

    我们没法往 AT-SPI 对象上挂属性（每次操作都是新进程），但 Go 侧本来就缓存着
    上一份快照，把 `路径 -> (编号, role, name)` 传进来就等价了。

    修的是实测踩过的最贵的一类失败：F4 打开对话框后索引全部重排，用旧下标调
    click 时工具照点不误——本想点 Position Y，实际点到菜单，把对象高度误改成
    16.26cm，**全程零报错**。有了稳定编号，对话框关掉之后主窗口的控件会**拿回
    原来的号**，旧引用要么仍然正确、要么明确报"不存在"。
    """

    def __init__(self, known):
        # known: {"0.1.2": {"index": 7, "role": "push button", "name": "Save"}}
        self.known = known or {}
        self.next_free = 0
        for entry in self.known.values():
            try:
                self.next_free = max(self.next_free, int(entry.get("index", 0)) + 1)
            except (TypeError, ValueError):
                pass
        self.used = set()

    def index_for(self, path, role, name, name_hash=""):
        key = ".".join(str(p) for p in path)
        entry = self.known.get(key)
        if (
            entry is not None
            and entry.get("role") == role
            and entry.get("name") == name
            and str(entry.get("nameHash") or "") == str(name_hash or "")
        ):
            index = entry.get("index")
            if isinstance(index, int) and index not in self.used:
                self.used.add(index)
                return index
        while self.next_free in self.used:
            self.next_free += 1
        index = self.next_free
        self.used.add(index)
        self.next_free += 1
        return index


def labelled_by_name(node):
    """无名控件从 AT-SPI 的 `LABELLED_BY` 关系上借一个名字。

    这条一等信息源本仓库此前**从未读过**（全仓 grep get_relation_set /
    RelationType / LABELLED 零命中）。它治的正是我们最疼的病：手工跑 Impress
    时，「位置和大小」对话框里的 spin button 全部无名，只能靠数顺序点，
    那是最脆的一种定位。

    实测判据（LibreOffice 7.3「位置和大小」）：**13 个无名 spin button，
    13 个都拿到了名字**，包括 `Position Y:` / `Position X:` / `Width:` /
    `Height:` / `Angle:` / `Radius:`。定位方式因此从"数第几个"变成"按名字选"。

    **裁剪判据里刻意不查这个关系。** 那道判据跑在全部节点上（GIMP 一次
    render_tree 调用它 3162 次），把 DBus 往返放进去会原样撞回它当初要解决的
    那个问题——完整 record_for 曾让 GIMP 整棵树要 38s、超过 Go 层 30s 的超时。
    代价是：一个**仅仅因为借到名字才值得保留**的节点会被裁掉。实测未发生
    （「位置和大小」的 spin button 因为带动作而保留），但这是已知的边界。

    只对**自身无名**的节点查，原因同样是成本：get_relation_set 是每节点一次
    DBus 往返，实测 1.037ms/节点。全量查的话 GIMP 的 3261 个节点要多花
    3.4 秒，而它的 get_app_state 已经在 30s 超时线上。而增益**全部**落在
    无名节点上——一个已经叫 `Save` 的按钮再关联一个 `Save` 标签，
    对 agent 没有任何新信息。

    这不是"编一个名字"。无障碍名字的标准算法本来就包含 labelled-by 这一路
    （Playwright 的 accessible name 同样如此）；是工具包选择了把名字放在
    关系里而不是放在节点上。但来源仍然要能看见，所以渲染时会带 `labelled`
    标记——同一个对话框里 `Position Y:` 出现了两次（两个标签页各一个），
    调用方需要知道这个名字是借来的、可能不唯一。
    """
    relations = safe(lambda: node.get_relation_set(), None)
    if not relations:
        return ""
    names = []
    for relation in relations:
        kind = safe(lambda: relation.get_relation_type())
        if kind != Atspi.RelationType.LABELLED_BY:
            continue
        count = safe(lambda: relation.get_n_targets(), 0) or 0
        for index in range(count):
            target = safe(lambda: relation.get_target(index))
            if target is None:
                continue
            text = str(node_name(target) or "").strip()
            if text:
                names.append(text)
    # 标签文字常带尾冒号（`Position Y:`），去掉——选择器里写冒号很别扭，
    # 而冒号不携带任何信息。
    return " ".join(names).rstrip(":").strip()


def effective_name(node):
    """一个节点**对外的名字**——自身的，没有就从 LABELLED_BY 借。

    这必须是**唯一**的口径，凡是要比对名字的地方都从这里取。

    上一次没做到这件事的代价记在 commit 5543a52：record 里存的是截断过的
    名字，而重解析时拿完整名字去比，必然失配；失配是静默的，身份判据悄悄退到
    最弱的"role + 屏幕位置"，元素一动就指向别人。

    借名字会**原样重现**那个坑：记录里写着 `Position Y`，而 node_name() 在
    同一个节点上返回空串。所以 record_for 与 record_name_matches、
    find_element 的兜底扫描全部改走这里，一处口径，两边同尺。
    """
    own = node_name(node)
    if str(own or "").strip():
        return own
    return labelled_by_name(node)

def semantic_relations(node, text_limit=DEFAULT_TEXT_LIMIT):
    """Return bounded relation facts for retained nodes only.

    Relations are evidence, not a replacement address. Targets stay descriptive
    because AT-SPI relation objects do not expose the provider traversal path
    used by StableIndexer. Go therefore uses them only for conservative
    duplicate-label pruning, never for action resolution.
    """
    wanted = {}
    for enum_name, output_name in (
        ("LABELLED_BY", "labelled-by"),
        ("LABEL_FOR", "label-for"),
        ("CONTROLLER_FOR", "controller-for"),
        ("MEMBER_OF", "member-of"),
    ):
        value = getattr(Atspi.RelationType, enum_name, None)
        if value is not None:
            wanted[value] = output_name
    try:
        relations = node.get_relation_set()
    except Exception:
        return {}, False
    if relations is None:
        return {}, False
    if not relations:
        return {}, True
    out = {}
    complete = True
    for relation in relations:
        kind = safe(lambda: relation.get_relation_type())
        label = wanted.get(kind)
        if not label:
            continue
        count = min(int(safe(lambda: relation.get_n_targets(), 0) or 0), 16)
        targets = []
        for index in range(count):
            try:
                target = relation.get_target(index)
            except Exception:
                complete = False
                continue
            if target is None:
                complete = False
                continue
            targets.append({
                "role": str(safe(lambda: node_role(target), "") or ""),
                "name": limit_text(
                    str(
                        safe(lambda: node_name(target), "")
                        or safe(
                            lambda: element_value(
                                target,
                                text_limit=min(text_limit, 160),
                            ),
                            "",
                        )
                        or ""
                    ),
                    text_limit=min(text_limit, 160),
                ),
                "providerBusName": provider_bus_name(target),
                "providerObjectPath": provider_object_path(target),
                "processGeneration": process_generation(node_pid(target)),
            })
        if targets:
            out[label] = targets
    return out, complete


def record_for(node, index, path, window_bounds, text_limit=DEFAULT_TEXT_LIMIT):
    bounds = relative_frame(node, window_bounds)
    role = node_role(node)
    # 动作表只问一次，两个字段共用——见 node_actions() 里关于 LibreOffice
    # ATK 桥的说明。
    actions, has_click_action = node_actions(node)
    own_name = node_name(node)
    relations, relations_complete = semantic_relations(
        node, text_limit=text_limit
    )
    labelled_names = [
        str(target.get("name") or "").strip()
        for target in relations.get("labelled-by", [])
        if str(target.get("name") or "").strip()
    ]
    if str(own_name or "").strip():
        full_name = own_name
        name_source = "provider"
        borrowed = False
    else:
        full_name = " ".join(labelled_names).rstrip(":").strip()
        name_source = "labelled_by" if full_name else ""
        borrowed = bool(full_name)
    name = limit_text(full_name, text_limit=text_limit)
    record = {
        "index": index,
        "runtimeId": path[:],
        "automationId": accessible_id(node),
        "providerBusName": provider_bus_name(node),
        "providerObjectPath": provider_object_path(node),
        "processGeneration": process_generation(node_pid(node)),
        "showing": state_value(node, Atspi.StateType.SHOWING),
        "visible": state_value(node, Atspi.StateType.VISIBLE),
        "name": name,
        "controlType": role,
        "localizedControlType": role,
        "className": str(safe(node.get_toolkit_name, "") or ""),
        "value": element_value(node, text_limit=text_limit),
        "nativeWindowHandle": 0,
        "frame": bounds,
        "actions": actions,
        "states": state_segment(
            node, has_click_action=has_click_action, borrowed_name=borrowed
        ),
        "description": node_description(node, text_limit=text_limit),
        "placeholder": placeholder_text(
            node, text_limit=text_limit, role=role
        ),
        # 格式属性跟着记录走，**不单独遍历**。树渲染本来就在访问每个节点，
        # 而 Go 侧已经缓存着动作前的快照，两份一比就是零额外 AT-SPI 调用。
        #
        # 走过一版弯路：先按"焦点文本节点"取，结果 LibreOffice 的 root pane
        # **自带 FOCUSED 位**（这个坑仓库里早记过），取到的永远是它；
        # 改成扫全树又要 551ms 且漏掉目标段落。跟着 record 走两个问题都没有。
        "textAttributes": text_attributes(node),
        "relations": relations,
        "relationReadComplete": relations_complete,
        "nameSource": name_source,
    }
    # 名字被截断时，另外记一份**完整名字的哈希**。
    #
    # 不记的代价是实测过的：record_still_matches 拿实时的完整 name 去比
    # record 里已截断的 name，必然失配。同一个节点、同一个名字，只要
    # text_limit 从 500 调到 40，身份判定就从 True 翻成 False，
    # 然后静默退到最弱的"role + 屏幕位置"判据。
    # 也就是说：**agent 为省 token 调低 text_limit，会悄悄削弱元素身份**。
    #
    # 存哈希而不是存阈值，是写测试时改的：只按截断后的前缀比，两个前缀相同的
    # 不同元素会被判成同一个——那等于把一个静默失配换成一个静默误判，更糟。
    # 哈希让判定回到"完整名字精确相等"，代价是极少数记录上多 16 个字符。
    if str(full_name or "") != name:
        record["nameHash"] = name_fingerprint(full_name)
    return record
