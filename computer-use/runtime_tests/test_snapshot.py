#!/usr/bin/env python3
"""Split Linux provider tests."""

import inspect

from runtime_test_support import *  # noqa: F401,F403

class ShouldEnumerateChildrenTests(AtspiPatchedTestCase):
    def test_nautilus_style_small_managed_container_is_enumerated(self):
        """回归：声明 MANAGES_DESCENDANTS 但很小的容器必须照常枚举。

        Nautilus 侧边栏（Recent/Home/Documents/…）声明了自管理，实际只有 12
        个子节点，而且**不实现 Table 接口**——坐标寻址兜底必然失败。一律拒绝
        枚举的旧实现让整个侧边栏对 agent 不可见：树里只留下一句
        "contents not enumerated"，导航功能等于不存在。
        """
        sidebar = _LyingNode(12, states=(STATE.MANAGES_DESCENDANTS, STATE.SHOWING))

        self.assertTrue(runtime.should_enumerate_children(sidebar))

    def test_calc_style_sheet_is_still_refused(self):
        """自报 21 亿的 Calc sheet 仍然不得枚举——放宽自管理不能把它放进来。

        它被 HARD_CHILD_CAP 拦下，与 MANAGES_DESCENDANTS 无关，所以即使
        自管理这条分支整个改掉，这道守卫也依然成立。
        """
        sheet = _LyingNode(2147483647, states=(STATE.MANAGES_DESCENDANTS, STATE.SHOWING))

        self.assertFalse(runtime.should_enumerate_children(sheet))

    def test_managed_container_above_cap_is_refused(self):
        """自管理且自报数量超过阈值时，回到"按契约不枚举"。"""
        big = _LyingNode(
            runtime.MANAGED_ENUMERATE_CAP + 1, states=(STATE.MANAGES_DESCENDANTS,)
        )

        self.assertFalse(runtime.should_enumerate_children(big))

    def test_managed_container_exactly_at_cap_is_enumerated(self):
        """阈值取闭区间：恰好等于上限仍然枚举，避免边界上少一个元素。"""
        edge = _LyingNode(
            runtime.MANAGED_ENUMERATE_CAP, states=(STATE.MANAGES_DESCENDANTS,)
        )

        self.assertTrue(runtime.should_enumerate_children(edge))

    def test_huge_container_without_managed_state_is_refused(self):
        """不声明自管理却谎报海量子节点的实现，同样要被硬上限兜住。"""
        liar = _LyingNode(runtime.HARD_CHILD_CAP + 1, states=(STATE.SHOWING,))

        self.assertFalse(runtime.should_enumerate_children(liar))

    def test_ordinary_container_is_enumerated(self):
        ordinary = FakeNode(children=(FakeNode(), FakeNode()))

        self.assertTrue(runtime.should_enumerate_children(ordinary))

class NodeRenderIsolationTests(AtspiPatchedTestCase):
    def test_node_budget_stops_before_fetching_the_next_child(self):
        child = _TreeNode(role="push button", name="Never fetched")
        root = _TreeNode(
            role="frame", name="Window", w=100, h=100, kids=(child,))
        original = root.get_child_at_index
        calls = []

        def counted(index):
            calls.append(index)
            return original(index)

        root.get_child_at_index = counted

        records, _lines = runtime.render_tree(
            root,
            None,
            [0],
            prune=False,
            max_tree_nodes=1,
            indexer=runtime.StableIndexer({}),
        )

        self.assertEqual(len(records), 1)
        self.assertEqual(
            calls, [],
            "child_at must not run after the node budget is already full",
        )

    def test_prune_probe_errors_are_not_reported_as_deliberate_filtering(self):
        for field in (
            "component",
            "role",
            "name",
            "description",
            "child_count",
        ):
            with self.subTest(field=field):
                healthy = _TreeNode(
                    role="push button", name="Healthy child")
                broken = _ProbeFailureNode(
                    field,
                    role="panel",
                    name="Broken",
                    kids=(healthy,),
                )
                root = _TreeNode(
                    role="frame",
                    name="Window",
                    w=100,
                    h=100,
                    kids=(broken,),
                )

                records, lines, annotations = runtime.render_tree(
                    root,
                    None,
                    [0],
                    prune=True,
                    indexer=runtime.StableIndexer({}),
                    include_annotations=True,
                )

                names = [record["name"] for record in records]
                self.assertIn("Healthy child", names)
                text = "\n".join(lines)
                self.assertNotIn("provider probe errors:", text)
                self.assertTrue(any(
                    item.get("kind") == "provider_omission"
                    and item.get("reason") == "provider_probe_error"
                    and item.get("count") == 1
                    for item in annotations
                ), annotations)
                self.assertNotIn("filtered: not interactable", text)

    def test_known_child_slot_returning_none_marks_acquisition_incomplete(self):
        missing = _TreeNode(role="push button", name="Missing")
        container = _TreeNode(
            role="panel", name="Container", kids=(missing,))
        container.get_child_at_index = lambda _index: None
        root = _TreeNode(
            role="frame", name="Window", w=100, h=100, kids=(container,))

        records, lines, annotations = runtime.render_tree(
            root,
            None,
            [0],
            prune=False,
            indexer=runtime.StableIndexer({}),
            include_annotations=True,
        )

        self.assertEqual(
            [record["name"] for record in records],
            ["Window", "Container"],
        )
        self.assertNotIn("Missing", "\n".join(lines))
        self.assertTrue(any(
            item.get("kind") == "provider_omission"
            and item.get("reason") == "provider_probe_error"
            and item.get("count") == 1
            for item in annotations
        ), annotations)

    def test_one_bad_node_does_not_abort_sibling_rendering(self):
        first = _TreeNode(role="push button", name="First")
        child = _TreeNode(role="push button", name="Healthy child")
        broken = _TreeNode(role="panel", name="Broken", kids=(child,))
        last = _TreeNode(role="push button", name="Last")
        root = _TreeNode(
            role="frame", name="Window", w=100, h=100,
            kids=(first, broken, last))
        original = runtime.record_for

        def record(node, *args, **kwargs):
            if node is broken:
                raise RuntimeError("bad AT-SPI property")
            return original(node, *args, **kwargs)

        with mock.patch.object(runtime, "record_for", side_effect=record):
            records, lines = runtime.render_tree(
                root, None, [0], prune=False,
                indexer=runtime.StableIndexer({}))

        names = [item["name"] for item in records]
        self.assertIn("First", names)
        self.assertNotIn("Broken", names)
        self.assertIn("Healthy child", names)
        self.assertIn("Last", names)
        text = "\n".join(lines)
        self.assertIn("accessibility node unavailable", text)
        self.assertIn("no public ref was issued", text)
        self.assertNotIn("path=0.1", text)
        self.assertNotIn("role='panel'", text)
        self.assertNotIn("bad AT-SPI property", text)
        self.assertIn("eligible healthy children follow", text)
        self.assertIn("sibling and eligible-child traversal continued", text)

    def test_failure_recovery_respects_non_enumerable_container_contract(self):
        child = _TreeNode(role="push button", name="Must stay hidden")
        broken = _TreeNode(role="panel", name="Broken", kids=(child,))
        root = _TreeNode(
            role="frame", name="Window", w=100, h=100, kids=(broken,))
        original = runtime.record_for

        def record(node, *args, **kwargs):
            if node is broken:
                raise RuntimeError("broken container")
            return original(node, *args, **kwargs)

        with mock.patch.object(runtime, "record_for", side_effect=record), \
                mock.patch.object(
                    runtime, "should_enumerate_children",
                    side_effect=lambda node, count=None: node is not broken):
            records, lines = runtime.render_tree(
                root, None, [0], prune=False,
                indexer=runtime.StableIndexer({}))

        self.assertNotIn("Must stay hidden", [item["name"] for item in records])
        self.assertIn("not safely enumerable", "\n".join(lines))

    def test_failure_recovery_does_not_open_collapsed_menu(self):
        hidden = _TreeNode(role="menu item", name="Hidden item")
        menu = _TreeNode(role="menu", name="File", kids=(hidden,))
        menu.states = {STATE.VISIBLE}
        root = _TreeNode(
            role="frame", name="Window", w=100, h=100, kids=(menu,))
        original = runtime.record_for

        def record(node, *args, **kwargs):
            if node is menu:
                raise RuntimeError("broken collapsed menu")
            return original(node, *args, **kwargs)

        with mock.patch.object(runtime, "record_for", side_effect=record):
            records, lines = runtime.render_tree(
                root, None, [0], prune=False,
                indexer=runtime.StableIndexer({}))

        self.assertNotIn("Hidden item", [item["name"] for item in records])
        self.assertIn("menu is collapsed", "\n".join(lines))

    def test_recovery_budget_reports_skipped_later_sibling(self):
        child = _TreeNode(role="push button", name="Recovered child")
        broken = _TreeNode(role="panel", name="Broken", kids=(child,))
        later = _TreeNode(role="push button", name="Later sibling")
        root = _TreeNode(
            role="frame", name="Window", w=100, h=100,
            kids=(broken, later))
        original = runtime.record_for

        def record(node, *args, **kwargs):
            if node is broken:
                raise RuntimeError("broken panel")
            return original(node, *args, **kwargs)

        with mock.patch.object(runtime, "record_for", side_effect=record):
            records, lines = runtime.render_tree(
                root, None, [0], prune=False,
                max_tree_nodes=2,
                indexer=runtime.StableIndexer({}))

        names = [item["name"] for item in records]
        self.assertIn("Recovered child", names)
        self.assertNotIn("Later sibling", names)
        text = "\n".join(lines)
        self.assertIn("node budget filled up", text)
        self.assertIn("budget-stopped roots reported separately", text)

    def test_fanout_cap_is_reported_instead_of_silent(self):
        children = tuple(
            _TreeNode(role="push button", name="Item {}".format(index))
            for index in range(runtime.MAX_CHILD_FANOUT + 3)
        )
        root = _TreeNode(
            role="frame", name="Window", w=100, h=100, kids=children)

        records, lines = runtime.render_tree(
            root, None, [0], prune=False,
            max_tree_nodes=runtime.MAX_ELEMENTS,
            indexer=runtime.StableIndexer({}))

        self.assertEqual(len(records), 1 + runtime.MAX_CHILD_FANOUT)
        text = "\n".join(lines)
        self.assertIn("3 child root(s) not traversed", text)
        self.assertIn("MAX_CHILD_FANOUT", text)

    def test_failure_diagnostic_obeys_character_budget(self):
        broken = _TreeNode(role="entry", name="Broken")
        root = _TreeNode(
            role="frame", name="Window", w=100, h=100, kids=(broken,))
        original = runtime.record_for

        def record(node, *args, **kwargs):
            if node is broken:
                raise RuntimeError("x" * 500)
            return original(node, *args, **kwargs)

        with mock.patch.object(runtime, "record_for", side_effect=record):
            _records, lines = runtime.render_tree(
                root, None, [0], prune=False,
                max_rendered_chars=180,
                indexer=runtime.StableIndexer({}))

        failure_lines = [
            line for line in lines
            if "accessibility node unavailable" in line
        ]
        self.assertEqual(len(failure_lines), 1)
        self.assertLessEqual(len(failure_lines[0]), 180)

class RenderIndentationTests(AtspiPatchedTestCase):
    def _indent(self, line):
        return len(line) - len(line.lstrip("\t"))

    def _nodes(self, lines):
        """只取节点行。末尾可能跟一句"N node(s) omitted"提示，它不是节点。"""
        return [l for l in lines if l.strip() and l.strip()[0].isdigit()]

    def test_pruned_container_does_not_advance_child_indentation(self):
        """回归：被裁掉的中间容器不得推进子节点的缩进。

        裁剪只丢容器自己那一行、仍然递归子节点。早先子节点沿用遍历深度做缩进，
        于是每被裁掉一层，缩进就凭空多一格——实测 Nautilus 上从第 1 层直接
        跳到第 6 层，中间全是空档，读起来像一棵断掉的树，agent 无法据此判断
        父子关系（"这个按钮属于哪个面板"正是消歧无名控件的唯一线索）。
        """
        # filler 无名、无动作 → 会被裁剪；button 有名字 → 会被保留
        leaf = _TreeNode(role="push button", name="Save", x=1, y=1, w=5, h=5)
        middle = _TreeNode(role="filler", x=0, y=0, w=8, h=8, kids=(leaf,))
        root = _TreeNode(role="frame", name="Doc", w=100, h=100, kids=(middle,))

        _, lines = runtime.render_tree(root, None, [0], prune=True)

        rendered = self._nodes(lines)
        self.assertEqual(len(rendered), 2, "中间的 filler 应被裁掉：{}".format(rendered))
        self.assertIn('frame "Doc"', rendered[0])
        self.assertIn('push button "Save"', rendered[1])
        self.assertEqual(
            self._indent(rendered[1]) - self._indent(rendered[0]), 1,
            "被裁的 filler 不该在缩进上留下空档：{}".format(rendered),
        )

    def test_unpruned_tree_keeps_one_level_per_node(self):
        """不裁剪时缩进仍与真实层级一一对应，别为了修裁剪把正常路径改坏。"""
        leaf = _TreeNode(role="push button", name="Save", x=1, y=1, w=5, h=5)
        middle = _TreeNode(role="filler", x=0, y=0, w=8, h=8, kids=(leaf,))
        root = _TreeNode(role="frame", name="Doc", w=100, h=100, kids=(middle,))

        _, lines = runtime.render_tree(root, None, [0], prune=False)

        rendered = self._nodes(lines)
        self.assertEqual(len(rendered), 3)
        self.assertEqual(
            [self._indent(l) - self._indent(rendered[0]) for l in rendered], [0, 1, 2]
        )

class TreeOmissionTests(AtspiPatchedTestCase):
    def test_depth_limit_is_counted_and_names_the_real_cause(self):
        leaf = _TreeNode(
            role="push button", name="Deep", x=1, y=1, w=5, h=5)
        middle = _TreeNode(
            role="panel", name="Middle", x=0, y=0, w=10, h=10, kids=(leaf,))
        root = _TreeNode(
            role="frame", name="Doc", w=100, h=100, kids=(middle,))

        records, lines = runtime.render_tree(
            root,
            None,
            [0],
            prune=False,
            max_tree_depth=1,
        )
        text = "\n".join(lines)

        self.assertEqual([record["name"] for record in records], ["Doc", "Middle"])
        self.assertIn("1 node(s) omitted", text)
        self.assertIn("server-owned depth budget (1) was reached", text)
        self.assertIn("descendants are absent from this snapshot", text)
        self.assertIn("cannot discover nodes the provider did not expose", text)

class NodeDescriptionTests(AtspiPatchedTestCase):
    def _nodes(self, lines):
        return [l for l in lines if l.strip() and l.strip()[0].isdigit()]

    def test_unnamed_button_is_identified_by_its_description(self):
        """回归：GTK 的纯图标按钮名字为空，唯一可读标识在 description 里。

        实测 Nautilus 工具栏：`Go back` / `Go forward` / `Search` / `Show list`
        四个按钮 name 全空。不渲染 description 的话，返回/前进这类文件管理器
        核心操作在树里就只是一个无名 `push button`，agent 除了按像素坐标猜
        没有别的办法——a11y 优先的路径在这里直接断掉。
        """
        back = _TreeNode(role="push button", description="Go back", x=1, y=1, w=5, h=5)
        root = _TreeNode(role="frame", name="Files", w=100, h=100, kids=(back,))

        _, lines = runtime.render_tree(root, None, [0], prune=True)

        self.assertIn('[desc="Go back"]', "\n".join(lines))

    def test_description_disambiguates_identically_named_buttons(self):
        """三个都叫 Menu 的 toggle button 只能靠 description 区分。"""
        ops = _TreeNode(role="toggle button", name="Menu",
                        description="Show operations", x=1, y=1, w=5, h=5)
        view = _TreeNode(role="toggle button", name="Menu",
                         description="View options", x=7, y=1, w=5, h=5)
        root = _TreeNode(role="frame", name="Files", w=100, h=100, kids=(ops, view))

        _, lines = runtime.render_tree(root, None, [0], prune=True)

        rendered = self._nodes(lines)
        self.assertIn("Show operations", rendered[1])
        self.assertIn("View options", rendered[2])

    def test_description_equal_to_name_is_not_repeated(self):
        """description 与 name 相同时不重复渲染，白占 token。"""
        node = _TreeNode(role="push button", name="Home", description="Home",
                         x=1, y=1, w=5, h=5)
        root = _TreeNode(role="frame", name="Files", w=100, h=100, kids=(node,))

        _, lines = runtime.render_tree(root, None, [0], prune=True)

        self.assertNotIn("Description:", "\n".join(lines))

    def test_name_is_not_overwritten_by_description(self):
        """description 不得顶替 name：轨迹回放与保留率评测都按 role+name 匹配，
        改写 name 会让同一个元素在不同版本间对不上号。"""
        back = _TreeNode(role="push button", description="Go back", x=1, y=1, w=5, h=5)
        root = _TreeNode(role="frame", name="Files", w=100, h=100, kids=(back,))

        records, _ = runtime.render_tree(root, None, [0], prune=True)

        self.assertEqual(records[1]["name"], "")
        self.assertEqual(records[1]["description"], "Go back")

class StaleElementIndexTests(AtspiPatchedTestCase):
    """`runtimeId` 是位置性路径，树一变就指向别的控件。"""

    def record(self, role, name, frame=None, path=(0, 1)):
        return {"runtimeId": list(path), "controlType": role, "name": name,
                "automationId": "", "frame": frame}

    def test_rejects_node_whose_role_and_name_changed(self):
        """回归：陈旧下标解析到的是另一个控件时必须拒绝，不能照点不误。

        实测 Nautilus：拿右键菜单打开时的快照（`menu item Rename…`），菜单关掉后
        同一条路径指向工具栏的 `toggle button Menu`——"重命名"变成"切换视图选项"，
        全程 isError=False。同一份菜单里紧挨着就是 `Move to Trash`，
        静默点错是不可接受的失败模式。
        """
        other = _TreeNode(role="toggle button", name="Menu", x=715, y=23, w=26, h=46)

        self.assertFalse(
            runtime.record_still_matches(
                other, self.record("menu item", "Rename…"), None
            )
        )

    def test_accepts_unchanged_node(self):
        same = _TreeNode(role="menu item", name="Rename…", x=7, y=189, w=304, h=25)

        self.assertTrue(
            runtime.record_still_matches(
                same, self.record("menu item", "Rename…"), None
            )
        )

    def test_unnamed_element_is_matched_by_position(self):
        """对话框里大量控件没有名字，位置是唯一可用的身份线索。"""
        node = _TreeNode(role="toggle button", x=10, y=20, w=30, h=40)
        frame = {"x": 10, "y": 20, "width": 30, "height": 40}
        moved = {"x": 400, "y": 20, "width": 30, "height": 40}

        self.assertTrue(
            runtime.record_still_matches(node, self.record("toggle button", "", frame), None)
        )
        self.assertFalse(
            runtime.record_still_matches(node, self.record("toggle button", "", moved), None)
        )

    def test_named_node_does_not_satisfy_unnamed_record(self):
        """快照里没名字、解析出来的有名字，说明换了元素。"""
        named = _TreeNode(role="toggle button", name="Menu", x=10, y=20, w=30, h=40)
        frame = {"x": 10, "y": 20, "width": 30, "height": 40}

        self.assertFalse(
            runtime.record_still_matches(named, self.record("toggle button", "", frame), None)
        )

    def test_accessible_id_wins_when_present(self):
        """工具包给了稳定 id 时以它为准，名字或位置变化都不影响。"""

        class WithID(_TreeNode):
            def get_accessible_id(self):
                return "save-button"

        node = WithID(role="push button", name="Save As", x=99, y=99, w=1, h=1)
        record = {"runtimeId": [0], "controlType": "push button", "name": "Save",
                  "automationId": "save-button", "frame": None}

        self.assertTrue(runtime.record_still_matches(node, record, None))


    def test_find_element_refuses_stale_path_end_to_end(self):
        """行为级回归：陈旧下标穿过 find_element 时不得解析成另一个控件。

        这是上面那些判据真正要防住的东西——单测判据函数只能证明判据本身对，
        证明不了调用方用上了它。
        """
        toolbar = _TreeNode(role="toggle button", name="Menu", x=715, y=23, w=26, h=46)
        window = _TreeNode(role="frame", name="Files", w=900, h=600, kids=(toolbar,))
        app = _TreeNode(role="application", name="Files", kids=(window,))

        stale = {"runtimeId": [0, 0], "controlType": "menu item",
                 "name": "Rename…", "automationId": "", "frame": None}

        # 路径本身解析得到 toolbar，但它已经不是当初那个元素
        self.assertIs(runtime.resolve_path(app, [0, 0]), toolbar)
        self.assertIsNone(runtime.find_element(app, stale))

    def test_find_element_still_resolves_valid_path(self):
        item = _TreeNode(role="menu item", name="Rename…", x=7, y=189, w=304, h=25)
        window = _TreeNode(role="frame", name="Files", w=900, h=600, kids=(item,))
        app = _TreeNode(role="application", name="Files", kids=(window,))

        record = {"runtimeId": [0, 0], "controlType": "menu item",
                  "name": "Rename…", "automationId": "", "frame": None}

        self.assertIs(runtime.find_element(app, record), item)

class ClickableMarkerTests(AtspiPatchedTestCase):
    """agent 必须能从树里看出哪个元素可以按元素点、哪个只能坐标点。"""

    def make(self, actions):
        class Node(_TreeNode):
            def get_action_iface(self):
                # 真实节点没有动作时不实现 Action 接口。运行时先问接口再问动作数，
                # 否则 LibreOffice 的 ATK 桥会对非 ATK_ACTION 对象打断言。
                return object() if actions else None

            def get_n_actions(self):
                return len(actions)

            def get_action_name(self, index):
                return actions[index]

            def get_action_description(self, index):
                return ""

        return Node(role="push button", name="Home", x=1, y=1, w=5, h=5)

    def segment(self, node):
        """按 record_for 的真实用法：动作表只读一次，标记由它得出。"""
        _, has_click = runtime.node_actions(node)
        return runtime.state_segment(node, has_click_action=has_click)

    def test_element_with_click_action_is_marked(self):
        self.assertIn("has-click-action", self.segment(self.make(["click"])))

    def test_element_without_any_action_is_not_marked(self):
        """回归：Nautilus 侧边栏条目只有 component 接口，没有 Action 接口。

        旧渲染里它和一个有 click 动作的 push button 长得**一模一样**——
        action_names() 会隐藏 click 类动作，于是"有语义点击"和"根本点不了"
        被抹成同一个样子，agent 只好一律退回坐标，恰好背离 a11y 优先。
        """
        self.assertNotIn("has-click-action", self.segment(self.make([])))

    def test_marker_matches_what_click_would_actually_invoke(self):
        """标记与 click 真正调用的入口必须同源。

        两者现在是两份实现——`node_actions()` 出标记、`preferred_action_index()`
        出调用——因为渲染时的动作表只允许读一次（LibreOffice 的 ATK 桥被反复
        问会让应用整个退出）。既然实现分开了，就必须显式钉住它们的一致性，
        否则标记会变成新的谎言。
        """
        cases = [
            ["menu"],                    # 只有二级动作，click 无从下手
            ["activate"],                # 精确命中
            ["click", "menu"],           # 混合
            [],                          # 完全没有动作
            ["Press the button"],        # 只有描述式命名，靠子串兜底
        ]
        for actions in cases:
            node = self.make(actions)
            _, has_click = runtime.node_actions(node)
            invocable = runtime.preferred_action_index(node) is not None
            self.assertEqual(
                has_click, invocable,
                "动作表 {!r}：标记说 {}，实际可调用 {}".format(
                    actions, has_click, invocable),
            )
            self.assertEqual(has_click, "has-click-action" in self.segment(node))


class PlaceholderAttributeIntrospectionTests(AtspiPatchedTestCase):
    """对象属性只应在可能携带 placeholder 的文本输入角色上读取。"""

    class AttributeNode(_TreeNode):
        def __init__(self, *args, attributes=None, **kwargs):
            super().__init__(*args, **kwargs)
            self.attributes = attributes or {}
            self.attribute_reads = 0

        def get_attributes(self):
            self.attribute_reads += 1
            return self.attributes

    def test_non_text_record_does_not_read_object_attributes(self):
        node = self.AttributeNode(
            role="push button",
            name="Insert",
            x=1,
            y=1,
            w=20,
            h=10,
            attributes={"placeholder-text": "must not be read"},
        )

        record = runtime.record_for(node, 1, [0, 0], None)

        self.assertEqual(record["placeholder"], "")
        self.assertEqual(node.attribute_reads, 0)

    def test_text_input_reads_placeholder_once(self):
        node = self.AttributeNode(
            role="entry",
            name="",
            x=1,
            y=1,
            w=20,
            h=10,
            attributes={"placeholder-text": "Search files"},
        )

        record = runtime.record_for(node, 1, [0, 0], None)

        self.assertEqual(record["placeholder"], "Search files")
        self.assertEqual(node.attribute_reads, 1)

    def test_role_aliases_cover_model_facing_text_inputs(self):
        for role in (
            "entry",
            "text",
            "text box",
            "textbox",
            "search box",
            "searchbox",
            "text field",
            "textfield",
            "text area",
            "textarea",
            "password text",
            "combo box",
        ):
            node = self.AttributeNode(
                role=role,
                attributes={"placeholder": "Hint"},
            )
            self.assertEqual(runtime.placeholder_text(node), "Hint", role)
            self.assertEqual(node.attribute_reads, 1, role)


class ResolveAppRetryTests(AtspiPatchedTestCase):
    """"暂时读不到"不等于"不存在"。"""

    def setUp(self):
        super().setUp()
        self.slept = []
        real_sleep = runtime.time.sleep
        runtime.time.sleep = lambda s: self.slept.append(s)
        self.addCleanup(setattr, runtime.time, "sleep", real_sleep)

    def patch_iter_apps(self, results):
        """results 是每次调用的返回值；元素为异常时抛出。"""
        calls = {"n": 0}

        def fake():
            index = min(calls["n"], len(results) - 1)
            calls["n"] += 1
            outcome = results[index]
            if isinstance(outcome, Exception):
                raise outcome
            return outcome

        original = runtime.iter_apps
        runtime.iter_apps = fake
        self.addCleanup(setattr, runtime, "iter_apps", original)
        return calls

    def test_transient_empty_enumeration_is_retried(self):
        """回归：应用忙于重建窗口时枚举可能瞬时为空，不能就此宣告不存在。

        实测 LibreOffice：get_app_state 刚成功，紧接着 click 就报
        appNotFound("soffice")，而 AT-SPI 桌面里那一条自始至终都在。
        向 agent 谎报"应用不存在"会让它改用别的应用名、重启应用，
        甚至判定任务无法完成——而真相只是需要再读一次。
        """
        app = _TreeNode(role="application", name="soffice")
        self.patch_iter_apps([[], [], [app]])

        self.assertIs(runtime.resolve_app("soffice"), app)

    def test_enumeration_exception_is_retried(self):
        app = _TreeNode(role="application", name="soffice")
        self.patch_iter_apps([RuntimeError("dbus hiccup"), [app]])

        self.assertIs(runtime.resolve_app("soffice"), app)

    def test_genuinely_absent_app_still_fails(self):
        self.patch_iter_apps([[]])

        with self.assertRaises(RuntimeError) as caught:
            runtime.resolve_app("no-such-app")
        message = str(caught.exception)
        self.assertEqual(caught.exception.failure_code, "ax_app_unavailable")
        self.assertEqual(caught.exception.failure_phase, "precondition")
        self.assertFalse(caught.exception.input_events_emitted)
        self.assertIn("appNotVisibleToAccessibility", message)
        self.assertIn("does not prove", message)
        self.assertIn("no action was attempted", message)

    def test_found_on_first_try_does_not_sleep(self):
        """应用在就立刻返回，不给正常路径加延迟。"""
        app = _TreeNode(role="application", name="soffice")
        self.patch_iter_apps([[app]])

        runtime.resolve_app("soffice")

        self.assertEqual(self.slept, [])

    def test_bootstrap_rejects_multiple_matching_apps(self):
        first = _TreeNode(role="application", name="Editor")
        second = _TreeNode(role="application", name="Editor")
        self.patch_iter_apps([[first, second]])
        original_accepts = runtime.accepts_user_input
        runtime.accepts_user_input = lambda _app: True
        self.addCleanup(
            setattr, runtime, "accepts_user_input", original_accepts
        )

        with self.assertRaises(runtime.OperationFailure) as caught:
            runtime.resolve_app("Editor")

        self.assertEqual(caught.exception.failure_code, "ax_app_ambiguous")
        self.assertFalse(caught.exception.input_events_emitted)

    def test_exact_app_identity_does_not_fall_back_to_name(self):
        current = _TreeNode(role="application", name="LibreOffice")
        restarted = _TreeNode(role="application", name="LibreOffice")
        self.patch_iter_apps([[current, restarted]])
        original_bus = runtime.provider_bus_name
        original_generation = runtime.process_generation
        original_accepts = runtime.accepts_user_input
        runtime.provider_bus_name = lambda app: (
            ":1.84" if app is current else ":1.91"
        )
        runtime.process_generation = lambda pid, proc_root="/proc": "gen-current"
        runtime.accepts_user_input = lambda _app: True
        self.addCleanup(setattr, runtime, "provider_bus_name", original_bus)
        self.addCleanup(setattr, runtime, "process_generation", original_generation)
        self.addCleanup(setattr, runtime, "accepts_user_input", original_accepts)

        resolved = runtime.resolve_app_identity({
            "providerBusName": ":1.84",
            "processGeneration": "gen-current",
        })
        self.assertIs(resolved, current)

    def test_incomplete_app_inventory_cannot_prove_stale_app_ref(self):
        inventory = runtime.InventoryResult(
            [],
            complete=False,
            errors=["desktop child 0 was unavailable"],
        )
        calls = self.patch_iter_apps([inventory])

        with self.assertRaises(runtime.OperationFailure) as caught:
            runtime.resolve_app_identity({
                "providerBusName": ":1.84",
                "processGeneration": "gen-current",
            })

        self.assertEqual(
            caught.exception.failure_code, "observation_incomplete"
        )
        self.assertNotIn(":1.84", str(caught.exception))
        self.assertNotIn("gen-current", str(caught.exception))
        self.assertEqual(calls["n"], runtime.RESOLVE_APP_ATTEMPTS)

    def test_complete_app_inventory_can_prove_stale_app_ref(self):
        calls = self.patch_iter_apps([
            runtime.InventoryResult([], complete=True)
        ])

        with self.assertRaises(runtime.OperationFailure) as caught:
            runtime.resolve_app_identity({
                "providerBusName": ":1.84",
                "processGeneration": "gen-current",
            })

        self.assertEqual(caught.exception.failure_code, "stale_app_ref")
        self.assertEqual(calls["n"], runtime.RESOLVE_APP_ATTEMPTS)

    def test_app_ref_retries_transient_incomplete_inventory(self):
        app = _TreeNode(role="application", name="LibreOffice")
        calls = self.patch_iter_apps([
            runtime.InventoryResult([], complete=False, errors=["AX busy"]),
            runtime.InventoryResult([app], complete=True),
        ])
        original_bus = runtime.provider_bus_name
        original_generation = runtime.process_generation
        original_accepts = runtime.accepts_user_input
        runtime.provider_bus_name = lambda _app: ":1.84"
        runtime.process_generation = lambda _pid, proc_root="/proc": "gen-current"
        runtime.accepts_user_input = lambda _app: True
        self.addCleanup(setattr, runtime, "provider_bus_name", original_bus)
        self.addCleanup(setattr, runtime, "process_generation", original_generation)
        self.addCleanup(setattr, runtime, "accepts_user_input", original_accepts)

        resolved = runtime.resolve_app_identity({
            "providerBusName": ":1.84",
            "processGeneration": "gen-current",
        })

        self.assertIs(resolved, app)
        self.assertEqual(calls["n"], 2)
        self.assertEqual(len(self.slept), 1)

    def test_app_ref_retries_transient_complete_empty_inventory(self):
        app = _TreeNode(role="application", name="LibreOffice")
        calls = self.patch_iter_apps([
            runtime.InventoryResult([], complete=True),
            runtime.InventoryResult([app], complete=True),
        ])
        original_bus = runtime.provider_bus_name
        original_generation = runtime.process_generation
        original_accepts = runtime.accepts_user_input
        runtime.provider_bus_name = lambda _app: ":1.84"
        runtime.process_generation = lambda _pid, proc_root="/proc": "gen-current"
        runtime.accepts_user_input = lambda _app: True
        self.addCleanup(setattr, runtime, "provider_bus_name", original_bus)
        self.addCleanup(setattr, runtime, "process_generation", original_generation)
        self.addCleanup(setattr, runtime, "accepts_user_input", original_accepts)

        resolved = runtime.resolve_app_identity({
            "providerBusName": ":1.84",
            "processGeneration": "gen-current",
        })

        self.assertIs(resolved, app)
        self.assertEqual(calls["n"], 2)
        self.assertEqual(len(self.slept), 1)

    def test_app_ref_retries_transient_inventory_exception(self):
        app = _TreeNode(role="application", name="LibreOffice")
        calls = self.patch_iter_apps([
            RuntimeError("AX bus temporarily unavailable"),
            runtime.InventoryResult([app], complete=True),
        ])
        original_bus = runtime.provider_bus_name
        original_generation = runtime.process_generation
        original_accepts = runtime.accepts_user_input
        runtime.provider_bus_name = lambda _app: ":1.84"
        runtime.process_generation = lambda _pid, proc_root="/proc": "gen-current"
        runtime.accepts_user_input = lambda _app: True
        self.addCleanup(setattr, runtime, "provider_bus_name", original_bus)
        self.addCleanup(setattr, runtime, "process_generation", original_generation)
        self.addCleanup(setattr, runtime, "accepts_user_input", original_accepts)

        resolved = runtime.resolve_app_identity({
            "providerBusName": ":1.84",
            "processGeneration": "gen-current",
        })

        self.assertIs(resolved, app)
        self.assertEqual(calls["n"], 2)
        self.assertEqual(len(self.slept), 1)

    def test_window_title_is_not_an_application_bootstrap_identity(self):
        main = _TreeNode(
            role="frame", name="VLC media player", w=600, h=400
        )
        preferences = _TreeNode(
            role="dialog", name="Simple Preferences", w=800, h=700
        )
        main.states.add(STATE.SHOWING)
        preferences.states.add(STATE.SHOWING)
        app = _TreeNode(
            role="application", name="vlc", kids=(main, preferences)
        )
        self.patch_iter_apps([[app]])
        original_accepts = runtime.accepts_user_input
        runtime.accepts_user_input = lambda _app: True
        self.addCleanup(
            setattr, runtime, "accepts_user_input", original_accepts
        )

        with self.assertRaises(runtime.OperationFailure) as caught:
            runtime.resolve_app("Simple Preferences")
        self.assertEqual(caught.exception.failure_code, "ax_app_unavailable")

class ListAppsWindowInventoryTests(AtspiPatchedTestCase):
    def test_lists_other_top_level_windows(self):
        main = _TreeNode(
            role="frame", name="VLC media player", w=600, h=400
        )
        preferences = _TreeNode(
            role="dialog", name="Simple Preferences", w=800, h=700
        )
        app = _TreeNode(
            role="application", name="vlc", kids=(main, preferences)
        )
        original_apps = runtime.iter_apps
        original_accepts = runtime.accepts_user_input
        runtime.iter_apps = lambda: [app]
        runtime.accepts_user_input = lambda _app: True
        self.addCleanup(setattr, runtime, "iter_apps", original_apps)
        self.addCleanup(
            setattr, runtime, "accepts_user_input", original_accepts
        )

        text = runtime.list_apps_text()

        self.assertIn('window="VLC media player"', text)
        self.assertIn("other_windows=Simple Preferences", text)
        self.assertNotIn("pid=", text)

    def test_pid_is_shown_only_for_duplicate_application_names(self):
        first_window = _TreeNode(role="frame", name="First")
        second_window = _TreeNode(role="frame", name="Second")
        first = _TreeNode(
            role="application", name="editor", kids=(first_window,)
        )
        second = _TreeNode(
            role="application", name="editor", kids=(second_window,)
        )
        first.get_process_id = lambda: 101
        second.get_process_id = lambda: 202
        original_apps = runtime.iter_apps
        original_accepts = runtime.accepts_user_input
        runtime.iter_apps = lambda: [first, second]
        runtime.accepts_user_input = lambda _app: True
        self.addCleanup(setattr, runtime, "iter_apps", original_apps)
        self.addCleanup(
            setattr, runtime, "accepts_user_input", original_accepts
        )

        text = runtime.list_apps_text()

        self.assertIn("pid=101", text)
        self.assertIn("pid=202", text)

    def test_duplicate_provider_identity_is_listed_once(self):
        first_window = _TreeNode(role="frame", name="Document")
        second_window = _TreeNode(role="frame", name="Document")
        first = _TreeNode(
            role="application", name="editor", kids=(first_window,)
        )
        second = _TreeNode(
            role="application", name="editor", kids=(second_window,)
        )
        first.get_process_id = lambda: 101
        second.get_process_id = lambda: 101
        original_apps = runtime.iter_apps
        original_accepts = runtime.accepts_user_input
        runtime.iter_apps = lambda: [first, second]
        runtime.accepts_user_input = lambda _app: True
        self.addCleanup(setattr, runtime, "iter_apps", original_apps)
        self.addCleanup(
            setattr, runtime, "accepts_user_input", original_accepts
        )

        with (
            mock.patch.object(runtime, "provider_bus_name", return_value=":1.42"),
            mock.patch.object(runtime, "process_generation", return_value="gen-101"),
        ):
            result = runtime.list_apps_inventory()

        self.assertEqual(len(result["apps"]), 1)
        self.assertEqual(result["text"].count('editor [window="Document"]'), 1)

    def test_operation_enumerates_apps_once_for_text_and_identity(self):
        window = _TreeNode(role="frame", name="Document")
        app = _TreeNode(role="application", name="editor", kids=(window,))
        app.get_process_id = lambda: 101
        calls = {"count": 0}
        original_apps = runtime.iter_apps
        original_accepts = runtime.accepts_user_input

        def one_inventory():
            calls["count"] += 1
            return [app]

        runtime.iter_apps = one_inventory
        runtime.accepts_user_input = lambda _app: True
        self.addCleanup(setattr, runtime, "iter_apps", original_apps)
        self.addCleanup(
            setattr, runtime, "accepts_user_input", original_accepts
        )

        result = perform_provider_operation({"intent": "observe_apps"})

        self.assertEqual(calls["count"], 1)
        self.assertEqual(result["text"], 'editor [window="Document"]')
        self.assertEqual(len(result["apps"]), 1)
        self.assertEqual(result["apps"][0]["displayText"], result["text"])
        self.assertTrue(result["apps"][0]["inputCapable"])

class ProviderInventoryCompletenessTests(AtspiPatchedTestCase):
    def test_desktop_child_count_failure_is_incomplete_not_empty(self):
        root = _TreeNode(role="desktop", name="Desktop")

        def fail():
            raise RuntimeError("AX bus failed")

        root.get_child_count = fail
        with mock.patch.object(runtime, "desktop", return_value=root):
            inventory = runtime.iter_apps()

        self.assertEqual(list(inventory), [])
        self.assertFalse(inventory.complete)
        self.assertTrue(inventory.errors)

    def test_none_desktop_child_is_incomplete_not_absent(self):
        root = _TreeNode(role="desktop", name="Desktop")
        root.get_child_count = lambda: 1
        root.get_child_at_index = lambda _index: None
        with mock.patch.object(runtime, "desktop", return_value=root):
            inventory = runtime.iter_apps()

        self.assertEqual(list(inventory), [])
        self.assertFalse(inventory.complete)

    def test_none_window_child_is_incomplete_not_windowless(self):
        app = _TreeNode(role="application", name="Editor")
        app.get_child_count = lambda: 1
        app.get_child_at_index = lambda _index: None

        inventory = runtime.app_windows(app)

        self.assertEqual(list(inventory), [])
        self.assertFalse(inventory.complete)

class AXWindowCloseEvidenceTests(AtspiPatchedTestCase):
    def setUp(self):
        super().setUp()
        self.window = _TreeNode(role="frame", name="Editor")
        self.window.path = "/window"
        self.window.app = type("ProviderApp", (), {"bus_name": ":1.42"})()
        self.window.get_process_id = lambda: 4242
        self.app = _TreeNode(
            role="application", name="editor", kids=(self.window,)
        )
        self.record = {"controlType": "frame", "runtimeId": [0]}

    def _patch_common(self, window_inventories, root_state):
        return (
            mock.patch.object(
                runtime,
                "strong_window_instance_handle_inventory",
                return_value=runtime.InventoryResult(
                    [(0, "w2:private")], complete=True
                ),
            ),
            mock.patch.object(
                runtime,
                "x11_window_id_for_accessible",
                return_value="0x100",
            ),
            mock.patch.object(
                runtime,
                "process_generation",
                return_value="99123",
            ),
            mock.patch.object(
                runtime,
                "app_windows",
                side_effect=window_inventories,
            ),
            mock.patch.object(
                runtime,
                "x11_root_window_state",
                return_value=root_state,
            ),
            mock.patch.object(
                runtime,
                "x11_window_inventory_for_pid",
                return_value=runtime.InventoryResult(
                    [{"id": "0x100"}], complete=True
                ),
            ),
            mock.patch.object(
                runtime.subprocess,
                "run",
                return_value=subprocess.CompletedProcess([], 0, "", ""),
            ),
            mock.patch.object(
                runtime, "AX_WINDOW_ACTION_TIMEOUT_SECONDS", 0
            ),
        )

    def test_incomplete_post_close_inventory_is_effect_unknown(self):
        before = runtime.InventoryResult(
            [(0, self.window)], complete=True
        )
        after = runtime.InventoryResult(
            [], complete=False, errors=["AX bus failed"]
        )
        patches = self._patch_common(
            [before, after],
            {"windowIds": {"0x100"}, "activeWindow": "0x100"},
        )
        runtime._CURRENT_ACTION_EVIDENCE = {
            "route": "unknown",
            "input_emitted": False,
        }
        try:
            with patches[0], patches[1], patches[2], patches[3], patches[4], \
                    patches[5], patches[6], patches[7]:
                with self.assertRaises(runtime.OperationFailure) as raised:
                    runtime.perform_ax_window_action(
                        "close", self.app, self.window, self.record
                    )
        finally:
            evidence = runtime._CURRENT_ACTION_EVIDENCE
            runtime._CURRENT_ACTION_EVIDENCE = None

        self.assertEqual(raised.exception.failure_code, "effect_unknown")
        self.assertTrue(raised.exception.input_events_emitted)
        self.assertEqual(evidence["route"], "native_window_command")
        self.assertTrue(evidence["input_emitted"])
        response = runtime.operation_failure_response(raised.exception)
        self.assertTrue(response["mutation_unknown"])

    def test_ax_disappearance_cannot_replace_native_close_proof(self):
        before = runtime.InventoryResult(
            [(0, self.window)], complete=True
        )
        after = runtime.InventoryResult([], complete=True)
        patches = self._patch_common(
            [before, after],
            {"windowIds": {"0x100"}, "activeWindow": "0x100"},
        )
        with patches[0], patches[1], patches[2], patches[3], patches[4], \
                patches[5], patches[6], patches[7]:
            with self.assertRaises(runtime.OperationFailure) as raised:
                runtime.perform_ax_window_action(
                    "close", self.app, self.window, self.record
                )

        self.assertEqual(raised.exception.failure_code, "effect_unknown")
        self.assertTrue(raised.exception.input_events_emitted)

    def test_complete_native_inventory_can_confirm_windowless_close(self):
        before = runtime.InventoryResult(
            [(0, self.window)], complete=True
        )
        after = runtime.InventoryResult(
            [(0, self.window)], complete=False, errors=["AX app exited"]
        )
        patches = self._patch_common(
            [before, after],
            {"windowIds": {"0x100"}, "activeWindow": "0x100"},
        )
        with patches[0], patches[1], patches[2], patches[3], \
                mock.patch.object(
                    runtime,
                    "x11_root_window_state",
                    side_effect=[
                        {
                            "windowIds": {"0x100"},
                            "activeWindow": "0x100",
                        },
                        {"windowIds": set(), "activeWindow": None},
                    ],
                ), \
                mock.patch.object(
                    runtime,
                    "x11_window_inventory_for_pid",
                    return_value=runtime.InventoryResult([], complete=True),
                ), patches[6], patches[7]:
            result = runtime.perform_ax_window_action(
                "close", self.app, self.window, self.record
            )

        self.assertTrue(result["windowless"])
        self.assertIn("transition_observed=true", result["note"])

    def test_wmctrl_timeout_preserves_possible_emission(self):
        before = runtime.InventoryResult(
            [(0, self.window)], complete=True
        )
        patches = self._patch_common(
            [before],
            {"windowIds": {"0x100"}, "activeWindow": "0x100"},
        )
        runtime._CURRENT_ACTION_EVIDENCE = {
            "route": "unknown",
            "input_emitted": False,
        }
        try:
            with patches[0], patches[1], patches[2], patches[3], patches[4], \
                    patches[5], mock.patch.object(
                        runtime.subprocess,
                        "run",
                        side_effect=subprocess.TimeoutExpired(
                            ["wmctrl", "-ic", "0x100"], 3
                        ),
                    ), patches[7]:
                with self.assertRaises(runtime.OperationFailure) as raised:
                    runtime.perform_ax_window_action(
                        "close", self.app, self.window, self.record
                    )
        finally:
            evidence = runtime._CURRENT_ACTION_EVIDENCE
            runtime._CURRENT_ACTION_EVIDENCE = None

        self.assertEqual(raised.exception.failure_code, "effect_unknown")
        self.assertTrue(raised.exception.input_events_emitted)
        self.assertFalse(raised.exception.failure_facts_complete)
        self.assertEqual(evidence["route"], "native_window_command")
        self.assertTrue(evidence["input_emitted"])
        response = runtime.operation_failure_response(raised.exception)
        self.assertTrue(response["input_events_emitted"])
        self.assertTrue(response["mutation_unknown"])

class AXDirectTargetResolutionTests(AtspiPatchedTestCase):
    def setUp(self):
        super().setUp()
        runtime._x11_window_inventory_cache.clear()

    def test_window_action_resolution_does_not_scan_descendants(self):
        window = _TreeNode(role="frame", name="Document")
        app = _TreeNode(role="application", name="editor", kids=(window,))
        target = {
            "windowHandle": "w2:private",
            "providerWindowRole": "frame",
            "providerWindowName": "Document",
        }
        with mock.patch.object(
            runtime,
            "app_windows",
            return_value=runtime.InventoryResult([(0, window)], complete=True),
        ), mock.patch.object(
            runtime,
            "strong_window_instance_handle_inventory",
            return_value=runtime.InventoryResult(
                [(0, "w2:private")], complete=True
            ),
        ), mock.patch.object(
            runtime,
            "extents",
            return_value=runtime.frame(0, 0, 800, 600),
        ):
            resolved, record, owner, bounds = (
                runtime.resolve_window_action_target(app, target)
            )

        self.assertIs(resolved, window)
        self.assertIs(owner, window)
        self.assertEqual(record["name"], "Document")
        self.assertEqual(bounds["width"], 800)
        self.assertEqual(record["_windowHandle"], "w2:private")

    def test_window_action_resolution_does_not_require_geometry(self):
        window = _TreeNode(role="frame", name="Document")
        app = _TreeNode(role="application", name="editor", kids=(window,))
        target = {
            "windowHandle": "w2:private",
            "providerWindowRole": "frame",
            "providerWindowName": "Document",
        }
        with mock.patch.object(
            runtime,
            "app_windows",
            return_value=runtime.InventoryResult([(0, window)], complete=True),
        ), mock.patch.object(
            runtime,
            "strong_window_instance_handle_inventory",
            return_value=runtime.InventoryResult(
                [(0, "w2:private")], complete=True
            ),
        ), mock.patch.object(runtime, "extents", return_value=None):
            resolved, record, owner, bounds = (
                runtime.resolve_window_action_target(app, target)
            )

        self.assertIs(resolved, window)
        self.assertIs(owner, window)
        self.assertIsNone(bounds)
        self.assertIsNone(record["frame"])

    def test_window_action_resolution_preserves_incomplete_native_inventory(self):
        window = _TreeNode(role="frame", name="Document")
        app = _TreeNode(role="application", name="editor", kids=(window,))
        target = {
            "windowHandle": "w2:private",
            "providerWindowRole": "frame",
            "providerWindowName": "Document",
        }
        with mock.patch.object(
            runtime,
            "app_windows",
            return_value=runtime.InventoryResult([(0, window)], complete=True),
        ), mock.patch.object(
            runtime,
            "strong_window_instance_handle_inventory",
            return_value=runtime.InventoryResult(
                [], complete=False, errors=["wmctrl timed out"]
            ),
        ):
            with self.assertRaises(runtime.OperationFailure) as raised:
                runtime.resolve_window_action_target(app, target)

        self.assertEqual(
            raised.exception.failure_code,
            "observation_incomplete",
        )
        self.assertEqual(raised.exception.failure_subject, "window")

    def test_window_action_rejects_changed_native_instance_before_command(self):
        window = _TreeNode(role="frame", name="Document")
        app = _TreeNode(role="application", name="editor", kids=(window,))
        record = {
            "controlType": "frame",
            "runtimeId": [0],
            "_windowHandle": "w2:old",
        }
        with mock.patch.object(
            runtime,
            "app_windows",
            return_value=runtime.InventoryResult([(0, window)], complete=True),
        ), mock.patch.object(
            runtime,
            "strong_window_instance_handle_inventory",
            return_value=runtime.InventoryResult(
                [(0, "w2:new")], complete=True
            ),
        ), mock.patch.object(runtime.subprocess, "run") as command:
            with self.assertRaises(runtime.OperationFailure) as raised:
                runtime.perform_ax_window_action(
                    "close", app, window, record
                )

        self.assertEqual(raised.exception.failure_code, "stale_window_ref")
        self.assertFalse(raised.exception.input_events_emitted)
        command.assert_not_called()

    def test_native_inventory_failure_is_not_cached_across_operations(self):
        failed = RuntimeError("wmctrl unavailable")
        recovered = subprocess.CompletedProcess(
            [],
            0,
            "0x01000001 0 4242 10 20 800 600 host Document\n",
            "",
        )
        with mock.patch.object(
            runtime.subprocess,
            "run",
            side_effect=[failed, recovered],
        ):
            first = runtime.x11_window_inventory_for_pid(4242)
            second = runtime.x11_window_inventory_for_pid(4242)

        self.assertFalse(first.complete)
        self.assertTrue(second.complete)
        self.assertEqual(second[0]["id"], "0x1000001")

    def test_internal_resolve_target_uses_action_resolver_without_input(self):
        app = _TreeNode(role="application", name="editor")
        window = _TreeNode(role="frame", name="Document")
        element = _TreeNode(role="push button", name="Save")
        record = {
            "index": 7,
            "runtimeId": [0, 1],
            "controlType": "push button",
            "name": "Save",
        }
        with mock.patch.object(
            runtime, "resolve_operation_app", return_value=app
        ), mock.patch.object(
            runtime, "main_window", return_value=(0, window)
        ), mock.patch.object(
            runtime, "extents", return_value=runtime.frame(0, 0, 800, 600)
        ), mock.patch.object(
            runtime,
            "resolve_semantic_target",
            return_value=(element, record, None, window, runtime.frame(0, 0, 800, 600)),
        ), mock.patch.object(
            runtime,
            "send_mouse_click",
            side_effect=AssertionError("resolve_target must not emit input"),
        ):
            response = perform_provider_operation({
                "intent": "resolve_target",
                "app": "a1",
                "target": {
                    "selector": 'button "Save"',
                    "role": "button",
                    "name": "Save",
                    "hasName": True,
                },
            })

        self.assertTrue(response["ok"])
        self.assertEqual(response["resolvedElement"], record)

class DropdownItemTests(AtspiPatchedTestCase):
    """下拉项上的语义调用会关掉弹窗却不提交值，且事后无从校验。"""

    def popup(self):
        return _TreeNode(role="window", name="")

    def test_table_cell_in_unnamed_popup_is_a_dropdown_item(self):
        """回归：LibreOffice 行距下拉，点 `table cell Double`。

        实测 do_action 返回 True、下拉关闭、控件仍显示 Single（截图核实）；
        换坐标点击才真正提交。比"不生效"更糟的是事后无法校验——弹窗连同元素
        一起消失，动作前后的树必然不同，通用的"什么都没变就重试"不会触发；
        而对话框里的 combo box 是不上报值的幻影节点，读回来确认也读不到。
        """
        cell = _TreeNode(role="table cell", name="Double", x=3, y=71, w=358, h=21)

        self.assertTrue(runtime.is_dropdown_item(cell, self.popup()))

    def test_menu_item_is_not_treated_as_dropdown_item(self):
        """菜单项不在此列：Nautilus 右键菜单的 `Rename…` 实测 do_action 完全正常，
        一并改掉会把好路也堵死。"""
        item = _TreeNode(role="menu item", name="Rename…", x=7, y=189, w=304, h=25)

        self.assertFalse(runtime.is_dropdown_item(item, self.popup()))

    def test_table_cell_in_main_frame_is_not_a_dropdown_item(self):
        """Calc 的单元格用的就是 table cell 这个角色，它们不是下拉项。"""
        cell = _TreeNode(role="table cell", name="A1", x=10, y=10, w=80, h=20)
        frame = _TreeNode(role="frame", name="untitled - Calc", w=900, h=600)

        self.assertFalse(runtime.is_dropdown_item(cell, frame))

    def test_table_cell_in_named_dialog_is_not_a_dropdown_item(self):
        """对话框有标题、角色是 dialog；下拉是无名的 window。"""
        cell = _TreeNode(role="table cell", name="calc-test.csv", x=41, y=157, w=332, h=21)
        dialog = _TreeNode(role="dialog", name="Document Recovery", w=578, h=406)

        self.assertFalse(runtime.is_dropdown_item(cell, dialog))

class EmptyCellOmissionTests(AtspiPatchedTestCase):
    """Calc 视口里绝大多数是空单元格，全渲染等于把配额烧在没有信息的格子上。"""

    def test_notice_states_the_verified_path_only(self):
        """回归：给 agent 的替代路径必须是实测过的那一条。

        空单元格不进树之后就没有 element_index 了，提示必须说清楚怎么够到它们。
        最初写的是"用名称框输入引用再回车"——看起来更直接，但实测**没通过**：
        `set_value` 能改名称框的文本却不触发跳转（控件变了、应用没照做，
        与下拉提交同一族），`click` 也没能让它获得键盘焦点。
        实测能走通的是"press_key 移动单元格光标 + type_text"。
        """
        source = effective_runtime_source()

        self.assertIn("empty cell(s) omitted", source)
        self.assertIn("move the cell cursor there", source)
        self.assertNotIn("Name Box and press", source)

    def test_empty_cells_are_skipped_before_costing_a_record(self):
        """空单元格要在建 record 之前就跳过，否则配额照样被吃掉。"""
        body = inspect.getsource(runtime.render_visible_cells)

        skip = body.index("if not value:")
        build = body.index("record = record_for(")
        self.assertLess(skip, build, "跳过判断必须在 record_for 之前")

class ElectronActionNamesTests(AtspiPatchedTestCase):
    """Chromium/Electron 用自己的一套动作名，两个方向都会出错。"""

    def make(self, actions):
        class Node(_TreeNode):
            def get_action_iface(self):
                return object() if actions else None

            def get_n_actions(self):
                return len(actions)

            def get_action_name(self, index):
                return actions[index]

            def get_action_description(self, index):
                return ""

        return Node(role="section", x=1, y=1, w=5, h=5)

    def test_do_default_is_the_click_entry(self):
        """回归：VS Code 19 个节点的动作表是 ('doDefault', 'showContextMenu')。

        `doDefault` 就是 Chromium 的默认动作。不认这个名字，这些元素既拿不到
        `[has-click-action]`，`click_method:"accessibility"` 也会直接失败——
        整个 Electron 系应用在语义通道上等于不可点。
        """
        node = self.make(["doDefault", "showContextMenu"])
        names, has_click = runtime.node_actions(node)

        self.assertTrue(has_click)
        self.assertIsNotNone(runtime.preferred_action_index(node))
        self.assertNotIn("doDefault", names,
                         "它就是 click 本身，不该再列进 More actions")

    def test_click_ancestor_is_not_this_element_s_click(self):
        """回归：`clickAncestor` 含 click，会被子串兜底匹中——但它点的是**祖先**。

        VS Code 实测有 14 个节点是 ('clickAncestor', 'showContextMenu')。
        把它当成本元素的点击入口，agent 会以为点中了目标而实际点在别处，
        且从返回值和树里都看不出来——这是最坏的一类失败。
        """
        node = self.make(["clickAncestor", "showContextMenu"])
        _, has_click = runtime.node_actions(node)

        self.assertFalse(has_click)
        self.assertIsNone(runtime.preferred_action_index(node))

    def test_gecko_spells_it_with_a_space(self):
        """回归：同一语义在不同工具包里拼法不同。

        Chromium/Electron 叫 `clickAncestor`，Gecko/Thunderbird 叫
        `click ancestor`（带空格）。只排掉其中一种，另一种照样被子串兜底匹中，
        agent 又去点了祖先节点。实测两者都出现过。
        """
        node = self.make(["click ancestor"])
        _, has_click = runtime.node_actions(node)

        self.assertFalse(has_click)
        self.assertIsNone(runtime.preferred_action_index(node))

    def test_normalization_ignores_case_and_separators(self):
        for spelling in ("clickAncestor", "click ancestor", "Click-Ancestor",
                         "click_ancestor", "CLICKANCESTOR"):
            node = self.make([spelling])
            _, has_click = runtime.node_actions(node)
            self.assertFalse(has_click, "{!r} 应当被判为非本元素动作".format(spelling))

    def test_gecko_checkbox_state_named_actions_are_click_entries(self):
        """回归：Gecko 用结果状态命名复选框动作——勾上时是 `uncheck`、
        没勾时是 `check`，任一时刻只暴露适用的那个，调用它就等于 toggle。

        不认这两个名字，Gecko 的复选框全部退回坐标点击，而设置类界面几乎
        全是复选框（Thunderbird 5/14 个任务是账户设置、3/14 是消息过滤器）。
        """
        for spelling in ("check", "uncheck"):
            node = self.make([spelling])
            names, has_click = runtime.node_actions(node)
            self.assertTrue(has_click, "{!r} 应当算作点击入口".format(spelling))
            self.assertNotIn(spelling, names, "它就是 click 本身，不该再列进 More actions")

    def test_plain_click_still_works(self):
        node = self.make(["click", "showContextMenu"])
        _, has_click = runtime.node_actions(node)

        self.assertTrue(has_click)
        self.assertIsNotNone(runtime.preferred_action_index(node))

    def test_covered_set_has_a_single_source_of_truth(self):
        """判据只许有一份。抄成两处必然分歧——本轮加 doDefault 时就漏了副本。"""
        source = effective_runtime_source()

        self.assertEqual(source.count('"default.activate",'), 1,
                         "CLICK_COVERED_ACTIONS 不应存在副本")

class ObjectReplacementTests(AtspiPatchedTestCase):
    def test_object_replacement_placeholder_is_stripped(self):
        """回归：Chromium 用 U+FFFC 给嵌入对象占位，渲染出来是 `Value: ￼￼￼`。

        它零信息量，更糟的是**看起来像内容**——agent 会以为这个控件已经有值。
        VS Code 欢迎页实测 186 个占位符散布在 115 行里。
        """
        self.assertEqual(runtime.limit_text("￼"), "")
        self.assertEqual(runtime.limit_text("a￼￼b"), "ab")

    def test_real_text_is_untouched(self):
        self.assertEqual(runtime.limit_text("Save As…"), "Save As…")

class FocusDiagnosticTests(AtspiPatchedTestCase):
    def test_names_the_window_that_actually_holds_focus(self):
        other = _TreeNode(role="frame", name="Terminal", w=100, h=100)
        other.states.add(STATE.ACTIVE)
        app = _TreeNode(role="application", name="gnome-terminal", kids=(other,))
        original = runtime.iter_apps
        runtime.iter_apps = lambda: [app]
        self.addCleanup(setattr, runtime, "iter_apps", original)

        target = _TreeNode(role="frame", name="Editor", w=100, h=100)
        with self.assertRaises(RuntimeError) as caught:
            runtime.require_window_focus(target, "press_key")

        self.assertIn("Terminal", str(caught.exception))
        self.assertIn("gnome-terminal", str(caught.exception))

    def test_no_accessible_window_active_points_at_the_screenshot_channel(self):
        """回归：焦点被无障碍树看不见的东西拿着时，必须说出来。

        实测 VS Code：改完 settings.json 弹出原生对话框
        「A setting has changed that requires a restart to take effect.」，
        与 VS Code 同进程、锁住整个应用，**AT-SPI 里完全不存在**。
        此时 agent 看到一棵正常的树、每个动作都被正确拒绝，却无从知道原因——
        a11y 通道在这里是瞎的，只有截图能回答。
        """
        original = runtime.iter_apps
        runtime.iter_apps = lambda: []
        self.addCleanup(setattr, runtime, "iter_apps", original)

        target = _TreeNode(role="frame", name="Editor", w=100, h=100)
        with self.assertRaises(RuntimeError) as caught:
            runtime.require_window_focus(target, "press_key")

        message = str(caught.exception)
        self.assertIn("cannot see", message)
        self.assertIn("gui", message)

    def test_diagnostic_never_turns_a_clear_error_into_a_crash(self):
        """诊断代码枚举失败时必须安静退场，错误本身仍要抛出来。"""

        def boom():
            raise RuntimeError("a11y bus went away")

        original = runtime.iter_apps
        runtime.iter_apps = boom
        self.addCleanup(setattr, runtime, "iter_apps", original)

        target = _TreeNode(role="frame", name="Editor", w=100, h=100)
        with self.assertRaises(RuntimeError) as caught:
            runtime.require_window_focus(target, "press_key")

        self.assertIn("Refusing to synthesize", str(caught.exception))

class QtRichTextTests(AtspiPatchedTestCase):
    def test_qt_tooltip_html_is_reduced_to_its_message(self):
        """回归：Qt 把 tooltip 存成整段 HTML，CSS 也在里面。

        VLC 首选项实测 19 段这样的 HTML 合计 9149 字符，占整次观测的 56%，
        而真正的信息往往只有一句话。不处理的话 `Description:` 会把观测预算吃光。
        """
        blob = (
            '<html><head><meta name="qrichtext" content="1" />'
            '<style type="text/css"> p, li { white-space: pre-wrap; } </style>'
            '</head><body><p>Show a controller in fullscreen mode</p></body></html>'
        )

        self.assertEqual(
            runtime.plain_text_from_rich_text(blob),
            "Show a controller in fullscreen mode",
        )

    def test_br_becomes_a_separator_not_a_join(self):
        blob = "<html><body><p>one<br/>two</p></body></html>"

        self.assertEqual(runtime.plain_text_from_rich_text(blob), "one two")

    def test_plain_text_with_angle_brackets_is_untouched(self):
        """不对普通文本动手：真实内容里完全可能有尖括号（代码、模板、数学式），
        剥它们等于篡改 agent 读到的数据。"""
        for text in ("a < b and c > d", "List<String> items", "if x<3: pass"):
            self.assertEqual(runtime.plain_text_from_rich_text(text), text)

    def test_empty_and_none_are_safe(self):
        self.assertEqual(runtime.plain_text_from_rich_text(None), "")
        self.assertEqual(runtime.plain_text_from_rich_text(""), "")

class PruneCostTests(AtspiPatchedTestCase):
    """裁剪判据必须**先只读它用到的字段**，不能先建整条记录。

    实测 GIMP（GAIL）：完整的 record_for 是 8.45ms/节点，而一次 render_tree
    调用它 3162 次却只产出 157 条记录——**95% 的开销当场丢弃**，占渲染耗时的
    87%，整棵树 38.4s，超过 Go 层 30s 的超时。也就是说 a11y 通道在 GIMP 上
    **默认根本用不了**，而原因只是取数顺序。改完 17.5s，行数一字不差。
    """

    def test_pruned_nodes_never_pay_for_a_full_record(self):
        # 一堆会被裁掉的无名 filler，外加一个留得下的按钮。
        fillers = [_TreeNode(role="filler", name="", x=1, y=1, w=5, h=5)
                   for _ in range(30)]
        keeper = _TreeNode(role="push button", name="OK", x=1, y=1, w=5, h=5)
        window = _TreeNode(role="frame", name="W", x=0, y=0, w=100, h=100,
                           kids=tuple(fillers) + (keeper,))

        calls = []
        original = runtime.record_for

        def counted(node, *args, **kwargs):
            calls.append(runtime.node_name(node))
            return original(node, *args, **kwargs)

        runtime.record_for = counted
        self.addCleanup(setattr, runtime, "record_for", original)

        records, _ = runtime.render_tree(window, {"x": 0, "y": 0, "width": 100, "height": 100}, [0])

        # 只有窗口自身与幸存的按钮该付全量代价。
        self.assertEqual(len(records), 2, [r["name"] for r in records])
        self.assertEqual(len(calls), 2,
                         "被裁掉的节点不该走 record_for，实际调用了 {} 次".format(len(calls)))

    def test_prune_decision_still_keeps_named_containers(self):
        """省开销不许省掉正确性：有名字的容器仍然要留住。

        实测教训——行距 combo 的 toggle button 本身没有名字，agent 只能靠父节点
        `panel Line Spacing` 指认它。裁掉这个 panel，目标虽在树里却没法被指认。
        """
        inner = _TreeNode(role="toggle button", name="", x=2, y=2, w=4, h=4)
        named_panel = _TreeNode(role="panel", name="Line Spacing", x=1, y=1, w=8, h=8,
                                kids=(inner,))
        window = _TreeNode(role="frame", name="W", x=0, y=0, w=100, h=100,
                           kids=(named_panel,))
        records, _ = runtime.render_tree(
            window, {"x": 0, "y": 0, "width": 100, "height": 100}, [0])
        self.assertIn("Line Spacing", [r["name"] for r in records])

    def test_unpruned_view_does_not_apply_pressure_pruning(self):
        fillers = tuple(
            _TreeNode(role="filler", x=1, y=1, w=5, h=5)
            for _ in range(4)
        )
        window = _TreeNode(
            role="frame", name="W", w=100, h=100, kids=fillers)

        records, _ = runtime.render_tree(
            window,
            {"x": 0, "y": 0, "width": 100, "height": 100},
            [0],
            prune=False,
            max_tree_nodes=5,
        )

        self.assertEqual(
            len(records),
            5,
            "prune=false must retain layout nodes even after the pressure threshold",
        )

    def test_pressure_pruning_keeps_unnamed_interactive_roles(self):
        prefix = tuple(
            _TreeNode(
                role="push button",
                name="Keep {}".format(index),
                x=1,
                y=1,
                w=5,
                h=5,
            )
            for index in range(3)
        )
        unnamed = _TreeNode(
            role="push button", name="", x=1, y=1, w=5, h=5)
        window = _TreeNode(
            role="frame", name="W", w=100, h=100,
            kids=prefix + (unnamed,))

        records, _ = runtime.render_tree(
            window,
            {"x": 0, "y": 0, "width": 100, "height": 100},
            [0],
            prune=True,
            max_tree_nodes=5,
        )

        self.assertEqual(len(records), 5)
        self.assertEqual(records[-1]["controlType"], "push button")

class StateReadbackTests(unittest.TestCase):
    """动作声明了目标状态时，动作后回读校验。

    照抄 Playwright 的 `_setChecked`（dom.ts）：

        const finalState = await isChecked(progress);
        if (finalState.matches !== state)
          throw new NonRecoverableDOMError('Clicking the checkbox did not change its state');

    注意它抛的是 **NonRecoverable**——不重试。重复同一个动作不会有不同结果。
    """

    def test_action_names_come_from_measurement_not_from_playwright(self):
        """第一版这张表照 Playwright 的语义写（check/uncheck/expand/collapse），
        实测六个应用**一个都没有**，整张表是死代码。

        真实存在的是 `expand or contract`(119)、`toggle`(2) 这些，
        而且它们是**翻转**语义不是"设成某值"。
        """
        for dead in ("check", "uncheck", "expand", "collapse", "select", "deselect"):
            self.assertNotIn(dead, runtime.ACTION_MUST_FLIP, dead)
        self.assertIn("expand or contract", runtime.ACTION_MUST_FLIP)
        self.assertIn("toggle", runtime.ACTION_MUST_FLIP)

    def test_state_flipped_as_promised(self):
        note, failed = runtime.state_transition_note(
            "toggle", {"CHECKED": False}, {"CHECKED": True})
        self.assertFalse(failed)
        self.assertIn("CHECKED flipped False -> True", note)

    def test_boundary_is_stated_not_hidden(self):
        """**状态到位 ≠ 行为发生。**

        本仓库实测过反例：VLC 首选项里那颗单选按钮，Toggle 之后 CHECKED 真的
        翻转了，面板却不切换。这条判据接不住那一类，措辞里必须讲明白——
        不然它就成了新的假阳性来源。
        """
        note, _ = runtime.state_transition_note(
            "toggle", {"CHECKED": False}, {"CHECKED": True})
        self.assertIn("proves the CONTROL changed", note)
        self.assertIn("VLC", note)

    def test_state_did_not_change_is_a_hard_failure(self):
        note, failed = runtime.state_transition_note(
            "expand or contract", {"EXPANDED": False}, {"EXPANDED": False})
        self.assertTrue(failed)
        self.assertIn("promises to flip it", note)
        # 必须明说"别重试"，并给出下一条可走的路。
        self.assertIn("Repeating the same call will not help", note)
        self.assertIn("gui", note)

    def test_actions_that_declare_nothing_are_not_judged(self):
        """`click` / `menu` 这类动作没声明目标状态，回读无从谈起——
        **判不了就别判**，硬判会制造假阳性。"""
        # 这些是实测里最常见的动作名，它们都不承诺改变任何可读状态。
        for action in ("click", "menu", "activate", "press", "open", "dodefault", ""):
            note, failed = runtime.state_transition_note(
                action, {"CHECKED": False}, {"CHECKED": True})
            self.assertIsNone(note, action)
            self.assertFalse(failed, action)

    def test_unreadable_state_is_not_treated_as_failure(self):
        note, failed = runtime.state_transition_note("toggle", None, {"CHECKED": True})
        self.assertIsNone(note)
        self.assertFalse(failed)
        note, failed = runtime.state_transition_note("toggle", {"CHECKED": False}, {})
        self.assertIsNone(note)
        self.assertFalse(failed)

class StableIndexTests(unittest.TestCase):
    """编号要跨快照存活。照抄 Playwright 的 ariaSnapshot.ts。

    修的是实测踩过的最贵一类失败：F4 打开对话框后索引全部重排，用旧下标调
    click 时工具照点不误——本想点 Position Y，实际点到菜单，把对象高度误改成
    16.26cm，**全程零报错**。
    """

    def test_same_element_keeps_its_number(self):
        known = {"0.1": {"index": 7, "role": "push button", "name": "Save"}}
        indexer = runtime.StableIndexer(known)
        self.assertEqual(indexer.index_for([0, 1], "push button", "Save"), 7)

    def test_role_or_name_change_invalidates_the_number(self):
        """失效条件照抄 Playwright：role 或 name 变了就重新发号——
        因为它已经不是"同一个东西"了。"""
        known = {"0.1": {"index": 7, "role": "push button", "name": "Save"}}
        self.assertNotEqual(
            runtime.StableIndexer(known).index_for([0, 1], "push button", "Saved"), 7)
        self.assertNotEqual(
            runtime.StableIndexer(known).index_for([0, 1], "menu item", "Save"), 7)

    def test_full_name_fingerprint_change_invalidates_truncated_name(self):
        known = {
            "0.1": {
                "index": 7,
                "role": "push button",
                "name": "Manage settings...",
                "nameHash": "old-full-name",
            }
        }
        self.assertEqual(
            runtime.StableIndexer(known).index_for(
                [0, 1],
                "push button",
                "Manage settings...",
                "old-full-name",
            ),
            7,
        )
        self.assertNotEqual(
            runtime.StableIndexer(known).index_for(
                [0, 1],
                "push button",
                "Manage settings...",
                "different-full-name",
            ),
            7,
        )

    def test_inserting_an_element_does_not_shift_the_others(self):
        """这是遍历序号最致命的毛病：插一个元素，它后面所有编号全推走。"""
        known = {
            "0.0": {"index": 0, "role": "frame", "name": "W"},
            "0.1": {"index": 1, "role": "push button", "name": "A"},
            "0.2": {"index": 2, "role": "push button", "name": "B"},
        }
        indexer = runtime.StableIndexer(known)
        self.assertEqual(indexer.index_for([0, 0], "frame", "W"), 0)
        # 中间插进来一个新元素——它拿新号，不占用别人的
        fresh = indexer.index_for([0, 1, 5], "push button", "NEW")
        self.assertEqual(indexer.index_for([0, 1], "push button", "A"), 1)
        self.assertEqual(indexer.index_for([0, 2], "push button", "B"), 2)
        self.assertGreaterEqual(fresh, 3)

    def test_numbers_are_never_reused_within_one_snapshot(self):
        """同一份快照里两个元素拿到同一个号 = 静默指错对象。"""
        known = {"0.1": {"index": 3, "role": "text", "name": ""},
                 "0.2": {"index": 3, "role": "text", "name": ""}}
        indexer = runtime.StableIndexer(known)
        first = indexer.index_for([0, 1], "text", "")
        second = indexer.index_for([0, 2], "text", "")
        self.assertNotEqual(first, second)

    def test_without_prior_knowledge_it_numbers_from_zero(self):
        indexer = runtime.StableIndexer(None)
        self.assertEqual(indexer.index_for([0], "frame", "W"), 0)
        self.assertEqual(indexer.index_for([0, 0], "push button", "A"), 1)

class DenseIndexTests(AtspiPatchedTestCase):
    """被裁掉的节点**不能先领号再被丢掉**。

    必须继承 AtspiPatchedTestCase：裸 TestCase 下 relative_frame 全返回 None，
    于是 depth>0 的节点一律被判成不可见而裁光，树里只剩根节点——
    那种 fixture 测不出任何裁剪行为。这一条是写测试时当场踩到的。
    """

    def test_pruned_nodes_do_not_consume_numbers(self):
        """被裁掉的节点**不能先领号再被丢掉**。

        原来发号在裁剪判断之前，于是号被消耗、节点却不进 records/refs：
        打印出来的下标稀疏带洞。实测 Nautilus 一次快照 91 个元素、
        下标散布在 0..670——7.4 倍膨胀。而且被裁节点不在 known 里，
        每次快照都重新领一批新号，号会随会话单调涨下去。

        判据只看**稠密**：树里有 N 个元素，下标就该落在 0..N-1。
        """
        # 一堆无名 panel（会被角色裁剪丢掉）夹着两个有名按钮。
        # 几何必须显式给，裁剪判据的第一关就是 frame 不为 None。
        filler = [_TreeNode(role="panel", x=1, y=1, w=5, h=5)
                  for _ in range(8)]
        keep_a = _TreeNode(role="push button", name="Save", x=2, y=2, w=5, h=5)
        keep_b = _TreeNode(role="push button", name="Cancel", x=3, y=3, w=5, h=5)
        window = _TreeNode(role="frame", name="W", w=900, h=600,
                           kids=tuple(filler[:4]) + (keep_a,)
                                + tuple(filler[4:]) + (keep_b,))

        # 必须传 indexer——不传的话 index 退回 len(records)，那本来就是稠密的，
        # 测不出任何东西。
        records, _lines = runtime.render_tree(
            window, None, [0], indexer=runtime.StableIndexer({}), prune=True)
        indices = sorted(r["index"] for r in records)
        self.assertEqual(indices, list(range(len(records))),
                         "下标应当稠密，实际 {}".format(indices))

class TruncatedNameIdentityTests(AtspiPatchedTestCase):
    """name 被 text_limit 截断之后，元素身份**不能悄悄退化**。

    实测复现：同一个节点、同一个名字，text_limit 从 500 调到 40，
    record_still_matches 就从 True 翻成 False——因为它拿实时的完整 name
    去比 record 里已截断的 name。失配之后静默退到最弱的"role + 屏幕位置"判据。

    也就是说：**agent 为省 token 调低 text_limit，会悄悄削弱元素身份**。
    阈值是每次请求可传的参数，所以比较时不能假定它等于默认值。
    """

    LONG = "Manage — Settings and keyboard shortcuts, and more actions for the editor"

    def test_truncated_name_still_identifies_the_same_element(self):
        node = _TreeNode(role="push button", name=self.LONG)
        for limit in (500, 40, 10):
            record = runtime.record_for(node, 7, [0, 1], None, text_limit=limit)
            self.assertTrue(
                runtime.record_still_matches(node, record, None),
                "text_limit={} 时同一个节点没认出来".format(limit))

    def test_a_different_element_sharing_the_prefix_is_not_confused(self):
        """修截断不能换来一个静默误判。

        只按截断后的前缀比的话，两个前缀相同的不同元素会被判成同一个——
        那等于把一个静默失配换成一个静默误判，更糟。所以截断的记录
        随身带完整名字的指纹，判定回到"完整名字精确相等"。
        """
        node = _TreeNode(role="push button", name=self.LONG)
        sibling = _TreeNode(
            role="push button",
            name="Manage — Settings and keyboard shortcuts, and MORE actions here")
        for limit in (40, 10):
            record = runtime.record_for(node, 7, [0, 1], None, text_limit=limit)
            self.assertFalse(
                runtime.record_still_matches(sibling, record, None),
                "text_limit={} 时前缀相同的另一个元素被误判成同一个".format(limit))

    def test_short_truncation_margins_always_carry_a_fingerprint(self):
        """省略号会让渲染名比阈值多 3 个字符，不能拿长度关系判断是否截断。"""
        prefix = "abcdefghij"
        for suffix in ("k", "kl", "klm"):
            node = _TreeNode(role="push button", name=prefix + suffix)
            sibling = _TreeNode(role="push button", name=prefix + "z" * len(suffix))
            record = runtime.record_for(node, 7, [0, 1], None, text_limit=10)

            self.assertEqual(record["name"], prefix + "...")
            self.assertIn("nameHash", record)
            self.assertTrue(runtime.record_still_matches(node, record, None))
            self.assertFalse(runtime.record_still_matches(sibling, record, None))

            known = {
                "0.1": {
                    "index": 7,
                    "role": "push button",
                    "name": record["name"],
                    "nameHash": record["nameHash"],
                }
            }
            sibling_record = runtime.record_for(
                sibling, 8, [0, 1], None, text_limit=10
            )
            self.assertNotEqual(
                runtime.StableIndexer(known).index_for(
                    [0, 1],
                    sibling_record["controlType"],
                    sibling_record["name"],
                    sibling_record["nameHash"],
                ),
                7,
            )

    def test_short_names_carry_no_fingerprint(self):
        """没截断就不该多带字段——指纹只为截断的记录存在。"""
        node = _TreeNode(role="push button", name="Save")
        record = runtime.record_for(node, 0, [0], None, text_limit=500)
        self.assertNotIn("nameHash", record)

class SnapshotGrammarTests(unittest.TestCase):
    """快照文法。借鉴 Playwright 的 aria snapshot（`- role "name" [attr=value]`）。

    最要紧的一条是**自由文本必须加引号**。旧格式是
    `<idx> <role> <name> Description: <desc>`，而名字本身可以含冒号——
    实测 LibreOffice Impress 里就有 `panel PageShape: Weekday in school`。
    于是"名字"与"字段分隔符"在词法上无法区分，我们发出去的是一种歧义文法。
    """

    def _line(self, **overrides):
        record = {
            "index": 7, "localizedControlType": "push button", "controlType": "push button",
            "name": "", "automationId": "", "value": "", "states": "",
            "description": "", "placeholder": "", "actions": [], "frame": None,
        }
        record.update(overrides)
        return runtime.render_element_line(record, 0)

    def test_name_containing_a_colon_stays_unambiguous(self):
        """回归：这正是旧格式解析不了的那一行。"""
        line = self._line(name="PageShape: Weekday in school",
                          description="Slide")
        self.assertIn('"PageShape: Weekday in school"', line)
        self.assertIn('[desc="Slide"]', line)

    def test_quotes_inside_a_name_are_escaped(self):
        line = self._line(name='say "hi"')
        self.assertIn(r'"say \"hi\""', line)

    def test_geometry_is_compact(self):
        """`Frame: {x: 687, y: 23, width: 64, height: 46}` 45 字符 → 14 字符。

        实测几何占整棵树的 35–50%，压缩后全树省约 30%，信息不少一个字。
        """
        line = self._line(frame={"x": 687.0, "y": 23.0, "width": 64.0, "height": 46.0})
        self.assertIn("{687,23,64,46}", line)
        self.assertNotIn("Frame:", line)

    def test_value_goes_last_after_a_colon(self):
        """对齐 aria 的 `- textbox: Enter your name`。"""
        line = self._line(localizedControlType="text", value="baseline-marker",
                          states="[focused]")
        self.assertTrue(line.rstrip().endswith(': "baseline-marker"'), line)

    def test_empty_segments_are_omitted(self):
        """空的段不留空壳——否则每行都拖一串没有信息的字节。"""
        self.assertEqual(self._line(name="OK"), '\t7 push button "OK"')

    def test_index_stays_at_the_head(self):
        """我们没有 selector，下标是唯一的引用手段，必须好取。"""
        line = self._line(name="OK").strip()
        self.assertTrue(line.startswith("7 "), line)

class ElementReferenceTests(unittest.TestCase):
    """元素指称只有一个渲染出口，输出必须能被 expect 的 target 解析器原样回读。

    起因是焦点摘要走的是另一套渲染：`role + " " + name`，无引号。于是我们
    一边告诉 agent 焦点在 `entry Address and search bar`，一边在它把这串贴回
    `expect` 时判它不匹配——裸串横跨 role 与 name 两个字段，结构性必假。
    实测 focus_moves_to 的 4 次使用全部 verified=false，其中 3 次证据自相矛盾。
    """

    def test_named_element_is_quoted_like_a_tree_line(self):
        self.assertEqual(
            runtime.element_reference("entry", "Address and search bar"),
            'entry "Address and search bar"',
        )

    def test_unnamed_element_uses_the_field_form(self):
        """`an unnamed separator` 是散文，回读不了；`role="separator"` 才回读得了。"""
        self.assertEqual(runtime.element_reference("separator", ""), 'role="separator"')

    def test_quotes_inside_a_name_are_escaped(self):
        self.assertEqual(
            runtime.element_reference("entry", 'say "hi"'), r'entry "say \"hi\""'
        )

    def test_focused_summary_uses_the_shared_renderer(self):
        """判定源与证据源要同步，先得让证据本身可回读。"""
        node = object()
        with mock.patch.object(runtime, "iter_apps", return_value=[node]), \
                mock.patch.object(runtime, "node_pid", return_value=42), \
                mock.patch.object(runtime, "main_window", return_value=(None, node)), \
                mock.patch.object(runtime, "find_first", return_value=node), \
                mock.patch.object(runtime, "node_role", return_value="entry"), \
                mock.patch.object(runtime, "node_name",
                                  return_value="Address and search bar"), \
                mock.patch.object(runtime, "desktop", return_value=None):
            self.assertEqual(
                runtime.focused_summary(42), 'entry "Address and search bar"'
            )
