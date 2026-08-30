"""Bounded virtual-table discovery and rendering."""

from .foundation import (
    Atspi,  # pyright: ignore[reportAttributeAccessIssue]
    DEFAULT_TEXT_LIMIT,
    MAX_ELEMENTS,
    node_pid,
    node_role,
    os,
    process_generation,
    provider_bus_name,
    provider_object_path,
    safe,
)
from .ax_node_properties import text_value_with_status
from .ax_records import record_for

# 自管理表格容器一次最多渲染多少单元格。典型视口约 37 行 × 21 列 = 777 个，
# 实测取回（含文本）0.12s，0.2ms/cell。设上限兜住超宽表格。
MAX_TABLE_CELLS = int(os.environ.get("COMPUTER_USE_MAX_TABLE_CELLS", "1200"))


def visible_cell_range(node):
    """探测自管理表格容器当前视口对应的 (row0, row1, col0, col1)。

    MANAGES_DESCENDANTS 容器不能枚举子节点（Calc 的 sheet 谎报 10.7 亿个），
    但可以按坐标寻址。做法是用 Component.get_accessible_at_point 打容器矩形的
    两个对角，反解出可见的行列范围——在 Calc 里这是 O(1) 的像素→行列换算
    （ScAccessibleSpreadsheet::getAccessibleAtPoint 走 GetPosFromPixel）。

    返回 None 表示探测失败，调用方应回退到"不枚举"。
    """
    table = safe(node.get_table_iface)
    component = safe(node.get_component_iface)
    if table is None or component is None:
        return None
    rect = safe(
        lambda: Atspi.Component.get_extents(component, Atspi.CoordType.SCREEN)
    )
    if rect is None or rect.width <= 0 or rect.height <= 0:
        return None

    corners = [
        (rect.x + 3, rect.y + 3),
        (rect.x + rect.width - 4, rect.y + rect.height - 4),
    ]
    found = []
    for x, y in corners:
        cell = safe(
            lambda x=x, y=y: Atspi.Component.get_accessible_at_point(
                component, x, y, Atspi.CoordType.SCREEN
            )
        )
        if cell is None:
            return None
        index = safe(cell.get_index_in_parent)
        if index is None:
            return None
        row = safe(lambda: Atspi.Table.get_row_at_index(table, index))
        col = safe(lambda: Atspi.Table.get_column_at_index(table, index))
        if row is None or col is None:
            return None
        found.append((int(row), int(col)))

    if len(found) != 2:
        return None
    rows = sorted(f[0] for f in found)
    cols = sorted(f[1] for f in found)
    return rows[0], rows[1], cols[0], cols[1]


def render_visible_cells(
    node, depth, path, window_bounds, records, lines,
    text_limit=DEFAULT_TEXT_LIMIT, max_tree_nodes=MAX_ELEMENTS, indexer=None,
    boxes=False, annotations=None,
):
    """把自管理表格容器当前视口内的单元格渲染进树，返回渲染数量。

    这是 should_enumerate_children() 拒绝枚举之后的替代路径：枚举会掉进
    Calc 谎报的 10.7 亿子节点，而按 (row, col) 寻址在 Calc 里是 O(1) 查表。
    """
    def record_omission(reason, count=1):
        if annotations is None:
            return
        annotations.append({
            "parentRuntimeId": path[:],
            "kind": "provider_omission",
            "count": max(1, int(count)),
            "reason": reason,
        })

    span = visible_cell_range(node)
    if span is None:
        record_omission("visible_cell_range_unavailable")
        return 0
    row0, row1, col0, col1 = span
    table = safe(node.get_table_iface)
    if table is None:
        record_omission("table_interface_unavailable")
        return 0

    total = (row1 - row0 + 1) * (col1 - col0 + 1)
    probe_budget = min(MAX_TABLE_CELLS, total)
    record_budget = max(0, max_tree_nodes - len(records))
    count = 0
    empty = 0
    probed = 0
    missing = 0
    text_unavailable = 0
    for row in range(row0, row1 + 1):
        for col in range(col0, col1 + 1):
            if probed >= probe_budget or count >= record_budget:
                break
            probed += 1
            cell = safe(lambda r=row, c=col: Atspi.Table.get_accessible_at(table, r, c))
            if cell is None:
                missing += 1
                continue
            # 只取文本，不回退到 numeric_value：Calc 的空单元格 Value 接口
            # 返回 0.0，会让空白单元格看起来像是填了 0 —— 对"找出空单元格"
            # 这类任务是致命的误导。
            value, text_complete = text_value_with_status(
                cell, text_limit=text_limit
            )
            if not text_complete:
                text_unavailable += 1
                continue
            if not value:
                # 空单元格不进树。实测一张只有 3 列 4 行数据的表：视口里
                # 1081 个单元格中 1069 个是空的，占掉 19971 / 23182 token（86%）。
                # 空格子除了坐标不携带任何信息，而坐标本身就是 RxCy，
                # 从下面那行范围说明里可以直接推出来。
                #
                # 代价是空单元格拿不到 element_index。范围说明里给出的替代
                # 路径是**实测过的那一条**：用 press_key 把单元格光标移过去，
                # 再 type_text，内容会落进当前光标所在的格子。
                #
                # 名称框（树里的 `text Value: A1`）看起来更直接，但**没验证通过**：
                # set_value 能改它的文本却不触发跳转（控件变了、应用没照做，
                # 与下拉提交同一族），而 click 也没能让它获得键盘焦点。
                # 没验证通过的操作不写进给 agent 的提示里。
                empty += 1
                continue
            # 单元格用 (行, 列) 当路径后缀——它比遍历序号稳，滚动之后同一个格子
            # 还是同一个号。
            cell_path = path + [row, col]
            index = (indexer.index_for(cell_path, node_role(cell), "")
                     if indexer is not None else len(records))
            record = record_for(
                cell, index, cell_path, window_bounds, text_limit=text_limit
            )
            # The cell wrapper may be recreated between AX reads. Bind its
            # identity to the owning table plus row/column, then resolve the
            # current cell through the table interface before each action.
            record["providerIdentityScope"] = "table_owner"
            record["providerBusName"] = provider_bus_name(node)
            record["providerObjectPath"] = provider_object_path(node)
            record["processGeneration"] = process_generation(node_pid(node))
            records.append(record)
            # 必须带上 Frame。这些单元格是**屏幕上真实可见**的（它们正是坐标
            # 寻址取到的当前视口内容），漏掉 Frame 会让任何"只保留可见节点"的
            # 裁剪把整个下拉/表格内容判成不可见并全部丢掉——实测中行距下拉的
            # `cell R3C0 Double` 就是这么被三种裁剪策略同时丢掉的，
            # 而它恰恰是任务必须点中的那个元素。
            cell_frame = record.get("frame")
            frame_segment = ""
            if boxes and cell_frame is not None:
                frame_segment = " Frame: {{x: {0}, y: {1}, width: {2}, height: {3}}}".format(
                    round(cell_frame["x"]),
                    round(cell_frame["y"]),
                    round(cell_frame["width"]),
                    round(cell_frame["height"]),
                )
            # 用节点真实的 AT-SPI 角色，不要硬编码成 "cell"。角色保真度会影响
            # 任何按角色做的裁剪都依赖角色保真；实测中整批下拉选项曾因
            # table-cell 被硬编码成 cell 而判成无关角色丢掉。
            cell_role = record["localizedControlType"] or record["controlType"] or "table cell"
            a1 = spreadsheet_a1(row, col)
            lines.append(
                ("\t" * (depth + 2))
                + "{} {} {} (R{}C{}) {}{}".format(
                    index, cell_role, a1, row, col, value, frame_segment
                ).rstrip()
            )
            count += 1
        if probed >= probe_budget or count >= record_budget:
            break

    if missing:
        record_omission("visible_cell_lookup_unavailable", missing)
    if text_unavailable:
        record_omission(
            "visible_cell_text_unavailable", text_unavailable
        )
    if probed < total:
        record_omission("visible_cell_budget", total - probed)

    if count or empty:
        lines.append(
            ("\t" * (depth + 2))
            + "(cells {}:{} are in view; zero-based rows {}-{} x cols {}-{}: "
              "{} non-empty cell(s) listed, "
              "{} empty cell(s) omitted; table is {}x{}. Omitted cells have no "
              "stable ref. To put content in one, move the cell cursor there "
              "with press_key and then type_text — typing lands in whichever cell "
              "currently holds the cursor.)".format(
                  spreadsheet_a1(row0, col0), spreadsheet_a1(row1, col1),
                  row0, row1, col0, col1, count, empty,
                  safe(lambda: Atspi.Table.get_n_rows(table), "?"),
                  safe(lambda: Atspi.Table.get_n_columns(table), "?"),
              )
        )
    return count


def spreadsheet_a1(row, col):
    row = int(row)
    col = int(col)
    if row < 0 or col < 0:
        raise ValueError("spreadsheet coordinates must be non-negative")
    letters = ""
    number = col + 1
    while number:
        number, remainder = divmod(number - 1, 26)
        letters = chr(ord("A") + remainder) + letters
    return "{}{}".format(letters, row + 1)


