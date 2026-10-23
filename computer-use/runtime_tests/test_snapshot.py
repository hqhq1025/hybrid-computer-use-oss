#!/usr/bin/env python3
"""Linux provider snapshot enumeration, rendering, pruning, and node description tests."""

from runtime_test_support import *  # noqa: F401,F403


class ShouldEnumerateChildrenTests(AtspiPatchedTestCase):
    def test_nautilus_style_small_managed_container_is_enumerated(self):
        """A small container that declares MANAGES_DESCENDANTS is still enumerated.

        The Nautilus sidebar (Recent, Home, Documents, ...) declares itself
        self-managed but has only 12 children and no Table interface, so the
        coordinate-addressing fallback cannot work. Refusing enumeration would
        hide the whole sidebar behind one "contents not enumerated" line.
        """
        sidebar = _LyingNode(12, states=(STATE.MANAGES_DESCENDANTS, STATE.SHOWING))

        self.assertTrue(runtime.should_enumerate_children(sidebar))

    def test_calc_style_sheet_is_still_refused(self):
        """A Calc sheet reporting about 2.1 billion children is still refused.

        HARD_CHILD_CAP rejects it independently of MANAGES_DESCENDANTS, so the
        guard holds even if the self-managed branch changes.
        """
        sheet = _LyingNode(2147483647, states=(STATE.MANAGES_DESCENDANTS, STATE.SHOWING))

        self.assertFalse(runtime.should_enumerate_children(sheet))

    def test_managed_container_above_cap_is_refused(self):
        """A self-managed container above the cap keeps the no-enumeration contract."""
        big = _LyingNode(
            runtime.MANAGED_ENUMERATE_CAP + 1, states=(STATE.MANAGES_DESCENDANTS,)
        )

        self.assertFalse(runtime.should_enumerate_children(big))

    def test_managed_container_exactly_at_cap_is_enumerated(self):
        """The cap is inclusive, so a container exactly at the cap is enumerated."""
        edge = _LyingNode(
            runtime.MANAGED_ENUMERATE_CAP, states=(STATE.MANAGES_DESCENDANTS,)
        )

        self.assertTrue(runtime.should_enumerate_children(edge))

    def test_huge_container_without_managed_state_is_refused(self):
        """The hard cap also applies to containers that are not self-managed."""
        liar = _LyingNode(runtime.HARD_CHILD_CAP + 1, states=(STATE.SHOWING,))

        self.assertFalse(runtime.should_enumerate_children(liar))

    def test_ordinary_container_is_enumerated(self):
        ordinary = FakeNode(children=(FakeNode(), FakeNode()))

        self.assertTrue(runtime.should_enumerate_children(ordinary))


class _CountingNode(_TreeNode):
    """A tree node that counts every provider read by field."""

    class _App:
        bus_name = ":1.42"

    app = _App()

    def __init__(self, reads, **kwargs):
        super().__init__(**kwargs)
        self.reads = reads

    def _count(self, field):
        key = (self.title or self.description or self.role, field)
        self.reads[key] = self.reads.get(key, 0) + 1

    def get_child_count(self):
        self._count("child_count")
        return super().get_child_count()

    def get_state_set(self):
        self._count("states")
        return super().get_state_set()

    def get_component_iface(self):
        self._count("component")
        return super().get_component_iface()

    def get_role_name(self):
        self._count("role")
        return super().get_role_name()

    def get_name(self):
        self._count("name")
        return super().get_name()

    def get_description(self):
        self._count("description")
        return super().get_description()

    def get_process_id(self):
        self._count("pid")
        return super().get_process_id()


class FindWalkTests(AtspiPatchedTestCase):
    """AX find walks the window with its own caps, not the render budget."""

    def _wide_list(self, count):
        items = [_TreeNode(role="list item", name="Item %d" % index, y=index)
                 for index in range(count)]
        return _TreeNode(role="frame", name="Window", w=100, h=100,
                         kids=(_TreeNode(role="list", w=100, h=100, kids=items),))

    def _names(self, **limits):
        records, _lines, annotations = runtime.render_tree(
            self._wide_list(700), None, [0],
            indexer=runtime.StableIndexer({}), **limits)
        return {record["name"] for record in records}, annotations

    def test_find_walk_reaches_children_past_the_render_caps(self):
        names, annotations = self._names()
        self.assertNotIn("Item 699", names)
        self.assertTrue(annotations)
        names, annotations = self._names(**ax_snapshot.FIND_WALK_LIMITS)
        self.assertIn("Item 699", names)
        reasons = ",".join(str(annotation.get("reason")) for annotation in annotations)
        for cap in ("fanout", "budget", "chars", "time"):
            self.assertNotIn(cap, reasons)

    def test_find_walk_marks_the_tree_partial_at_its_own_cap(self):
        limits = dict(ax_snapshot.FIND_WALK_LIMITS, max_tree_nodes=50)
        names, annotations = self._names(**limits)
        self.assertNotIn("Item 699", names)
        self.assertTrue(any(
            annotation.get("kind") == "provider_omission"
            for annotation in annotations))

    def test_observe_request_selects_the_find_walk(self):
        seen = []

        class Stop(Exception):
            pass

        def build(*args, **kwargs):
            seen.append(kwargs.get("find_walk"))
            raise Stop()

        with mock.patch.object(ax_dispatch, "build_snapshot", build):
            for observation in ({}, {"find_walk": True}):
                with self.assertRaises(Stop):
                    ax_dispatch._observe_operation({"app": "Editor"}, observation)
        self.assertEqual(seen, [False, True])


class SingleReadPerNodeTests(AtspiPatchedTestCase):
    """Each provider field is read at most once per node per snapshot.

    Every read is a D-Bus round trip. The pruning probe, the enumeration
    guards and the record builder used to read the same fields again, which
    tripled the cost of a Calc snapshot.
    """

    def _tree(self, reads):
        hidden = _CountingNode(
            reads, role="panel", name="Offscreen", description="tip", w=0,
            h=0)
        hidden.component = None
        named = _CountingNode(reads, role="panel", name="Line Spacing")
        button = _CountingNode(reads, role="push button", name="OK")
        root = _CountingNode(
            reads, role="frame", name="Window", w=100, h=100,
            kids=(hidden, named, button))
        return root

    def test_every_field_is_read_at_most_once(self):
        reads = {}
        records, _lines, _annotations = runtime.render_tree(
            self._tree(reads), None, [0], prune=True,
            indexer=runtime.StableIndexer({}))

        self.assertEqual(
            [record["name"] for record in records],
            ["Window", "Line Spacing", "OK"])
        repeated = {key: count for key, count in reads.items() if count > 1}
        self.assertEqual(repeated, {})

    def test_invisible_node_is_pruned_without_reading_its_text(self):
        reads = {}
        runtime.render_tree(
            self._tree(reads), None, [0], prune=True,
            indexer=runtime.StableIndexer({}))

        self.assertNotIn(("Offscreen", "name"), reads)
        self.assertNotIn(("Offscreen", "description"), reads)
        self.assertNotIn(("Offscreen", "role"), reads)

    def test_process_id_is_read_once_per_application(self):
        reads = {}
        runtime.render_tree(
            self._tree(reads), None, [0], prune=True,
            indexer=runtime.StableIndexer({}))

        pid_reads = sum(
            count for (_node, field), count in reads.items()
            if field == "pid")
        # One read for the records of the bus, one for the window identity
        # fields added after traversal.
        self.assertLessEqual(pid_reads, 2)


class FocusProbeTests(AtspiPatchedTestCase):
    """The snapshot walk answers find_first(FOCUSED) or says it cannot."""

    def _focused(self, name, **kwargs):
        node = _TreeNode(role="push button", name=name, **kwargs)
        node.states.add(STATE.FOCUSED)
        return node

    def _walk(self, root, **kwargs):
        probe = runtime.FocusProbe()
        runtime.render_tree(
            root, None, [0], prune=True, indexer=runtime.StableIndexer({}),
            focus_probe=probe, **kwargs)
        return probe

    def test_probe_returns_the_node_find_first_returns(self):
        # The first focused node in preorder sits inside an invisible,
        # pruned container; the walk still visits it.
        early = self._focused("Early", w=0, h=0)
        hidden = _TreeNode(role="filler", w=0, h=0, kids=(early,))
        late = self._focused("Late")
        root = _TreeNode(
            role="frame", name="Window", w=100, h=100, kids=(hidden, late))

        probe = self._walk(root)

        self.assertTrue(probe.known)
        expected = runtime.find_first(
            root, lambda node: runtime.state_contains(node, STATE.FOCUSED))
        self.assertIs(probe.facts.node, expected)
        self.assertIs(expected, early)

    def test_no_focused_node_is_a_definite_answer(self):
        root = _TreeNode(
            role="frame", name="Window", w=100, h=100,
            kids=(_TreeNode(role="push button", name="OK"),))

        probe = self._walk(root)

        self.assertTrue(probe.known)
        self.assertIsNone(probe.facts)

    def test_node_budget_cut_before_focus_makes_the_answer_unknown(self):
        buttons = [
            _TreeNode(role="push button", name="B{}".format(index))
            for index in range(3)
        ]
        root = _TreeNode(
            role="frame", name="Window", w=100, h=100,
            kids=tuple(buttons) + (self._focused("Target"),))

        probe = self._walk(root, max_tree_nodes=2)

        self.assertFalse(probe.known)

    def test_collapsed_menu_makes_the_answer_unknown(self):
        item = self._focused("Hidden item")
        menu = _TreeNode(role="menu", name="File", kids=(item,))
        menu.states = {STATE.VISIBLE}
        root = _TreeNode(
            role="frame", name="Window", w=100, h=100, kids=(menu,))

        probe = self._walk(root)

        self.assertFalse(probe.known)

    def test_focus_found_before_a_later_cut_stays_known(self):
        first = self._focused("First")
        rest = [
            _TreeNode(role="push button", name="R{}".format(index))
            for index in range(4)
        ]
        root = _TreeNode(
            role="frame", name="Window", w=100, h=100,
            kids=(first,) + tuple(rest))

        probe = self._walk(root, max_tree_nodes=3)

        self.assertTrue(probe.known)
        self.assertIs(probe.facts.node, first)

    def test_unknown_probe_falls_back_to_one_find_first(self):
        window = object()
        focused = object()
        probe = runtime.FocusProbe()
        probe.skipped()
        calls = []

        def fake_find_first(root, predicate, budget=None):
            calls.append(root)
            return focused

        with mock.patch.object(runtime, "iter_apps", return_value=[window]), \
                mock.patch.object(runtime, "node_pid", return_value=7), \
                mock.patch.object(runtime, "main_window",
                                  return_value=(0, window)), \
                mock.patch.object(runtime, "find_first",
                                  side_effect=fake_find_first), \
                mock.patch.object(runtime, "node_role", return_value="entry"), \
                mock.patch.object(runtime, "node_name", return_value="Name"):
            summary, selected = runtime.focus_and_selection(
                7, walked_window=window, focus_probe=probe)

        self.assertEqual(calls, [window])
        self.assertEqual(summary, 'entry "Name"')
        self.assertIsNone(selected)


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

        records, _lines, _annotations = runtime.render_tree(
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
            records, lines, _annotations = runtime.render_tree(
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
                    side_effect=lambda node, count=None, facts=None: (
                        node is not broken)):
            records, lines, _annotations = runtime.render_tree(
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
            records, lines, _annotations = runtime.render_tree(
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
            records, lines, _annotations = runtime.render_tree(
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

        records, lines, _annotations = runtime.render_tree(
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
            _records, lines, _annotations = runtime.render_tree(
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
        """Return node lines only, skipping a trailing "N node(s) omitted" hint."""
        return [l for l in lines if l.strip() and l.strip()[0].isdigit()]

    def test_pruned_container_does_not_advance_child_indentation(self):
        """A pruned intermediate container does not add a level of indentation.

        Pruning drops only the container's own line and still visits its
        children. Indenting by traversal depth would add a phantom level for
        every pruned ancestor, which in Nautilus jumps from level 1 to level 6
        and reads like a broken tree. Parent-child structure is often the only
        way to tell unnamed controls apart.
        """
        # The unnamed filler without actions is pruned; the named button stays.
        leaf = _TreeNode(role="push button", name="Save", x=1, y=1, w=5, h=5)
        middle = _TreeNode(role="filler", x=0, y=0, w=8, h=8, kids=(leaf,))
        root = _TreeNode(role="frame", name="Doc", w=100, h=100, kids=(middle,))

        _, lines, _ = runtime.render_tree(root, None, [0], prune=True)

        rendered = self._nodes(lines)
        self.assertEqual(len(rendered), 2, "the intermediate filler must be pruned: {}".format(rendered))
        self.assertIn('frame "Doc"', rendered[0])
        self.assertIn('push button "Save"', rendered[1])
        self.assertEqual(
            self._indent(rendered[1]) - self._indent(rendered[0]), 1,
            "a pruned filler must not leave an indentation gap: {}".format(rendered),
        )

    def test_unpruned_tree_keeps_one_level_per_node(self):
        """Without pruning, indentation still matches the real depth of each node."""
        leaf = _TreeNode(role="push button", name="Save", x=1, y=1, w=5, h=5)
        middle = _TreeNode(role="filler", x=0, y=0, w=8, h=8, kids=(leaf,))
        root = _TreeNode(role="frame", name="Doc", w=100, h=100, kids=(middle,))

        _, lines, _ = runtime.render_tree(root, None, [0], prune=False)

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

        records, lines, _annotations = runtime.render_tree(
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


class HiddenNestedWindowTests(AtspiPatchedTestCase):
    """Hidden Qt dock windows must not spend the node budget.

    Krita parents dozens of closed dockers to its main window as frames that
    are neither SHOWING nor VISIBLE. Their subtrees held about half of the
    window's nodes (Qt keeps SHOWING on their combo-popup items), so the
    record and character budgets ran out before the menu bar, and observe
    and find never saw "Settings".
    """

    def _window(self, hidden_count=40):
        hidden = []
        for index in range(hidden_count):
            items = tuple(
                _TreeNode(role="list item", name="Option {}-{}".format(index, item))
                for item in range(5))
            docker = _TreeNode(
                role="frame", name="Docker {}".format(index), kids=items)
            docker.states = {STATE.SENSITIVE}
            hidden.append(docker)
        menu = _TreeNode(role="menu item", name="Settings", x=10, y=0, w=60,
                         h=20)
        bar = _TreeNode(role="menu bar", name="", w=500, h=20, kids=(menu,))
        return _TreeNode(role="frame", name="Krita", w=1000, h=700,
                         kids=tuple(hidden) + (bar,))

    def test_hidden_nested_window_subtree_is_skipped(self):
        records, _lines, annotations = runtime.render_tree(
            self._window(), None, [0], prune=True, max_tree_nodes=60,
            indexer=runtime.StableIndexer({}))
        names = [record["name"] for record in records]
        self.assertIn("Settings", names)
        self.assertFalse(any(name.startswith("Option") for name in names))
        omitted = [a for a in annotations if a.get("kind") == "provider_omission"]
        self.assertTrue(omitted)

    def test_showing_nested_window_is_still_walked(self):
        visible = _TreeNode(role="frame", name="Layers", w=100, h=100,
                            kids=(_TreeNode(role="push button", name="Add"),))
        root = _TreeNode(role="frame", name="Krita", w=1000, h=700,
                         kids=(visible,))
        records, _lines, _annotations = runtime.render_tree(
            root, None, [0], prune=True, indexer=runtime.StableIndexer({}))
        self.assertIn("Add", [record["name"] for record in records])


class NodeDescriptionTests(AtspiPatchedTestCase):
    def _nodes(self, lines):
        return [l for l in lines if l.strip() and l.strip()[0].isdigit()]

    def test_unnamed_button_is_identified_by_its_description(self):
        """GTK icon-only buttons have no name; only the description identifies them.

        In the Nautilus toolbar, `Go back`, `Go forward`, `Search` and
        `Show list` all have empty names. Without the description, core file
        manager actions render as unnamed `push button` nodes that can only be
        reached by coordinates.
        """
        back = _TreeNode(role="push button", description="Go back", x=1, y=1, w=5, h=5)
        root = _TreeNode(role="frame", name="Files", w=100, h=100, kids=(back,))

        _, lines, _ = runtime.render_tree(root, None, [0], prune=True)

        self.assertIn('[desc="Go back"]', "\n".join(lines))

    def test_description_disambiguates_identically_named_buttons(self):
        """Toggle buttons all named Menu differ only in their description."""
        ops = _TreeNode(role="toggle button", name="Menu",
                        description="Show operations", x=1, y=1, w=5, h=5)
        view = _TreeNode(role="toggle button", name="Menu",
                         description="View options", x=7, y=1, w=5, h=5)
        root = _TreeNode(role="frame", name="Files", w=100, h=100, kids=(ops, view))

        _, lines, _ = runtime.render_tree(root, None, [0], prune=True)

        rendered = self._nodes(lines)
        self.assertIn("Show operations", rendered[1])
        self.assertIn("View options", rendered[2])

    def test_description_equal_to_name_is_not_repeated(self):
        """A description equal to the name is not repeated, which would waste tokens."""
        node = _TreeNode(role="push button", name="Home", description="Home",
                         x=1, y=1, w=5, h=5)
        root = _TreeNode(role="frame", name="Files", w=100, h=100, kids=(node,))

        _, lines, _ = runtime.render_tree(root, None, [0], prune=True)

        self.assertNotIn("Description:", "\n".join(lines))

    def test_name_is_not_overwritten_by_description(self):
        """The description never replaces the name.

        Elements are matched by role and name across snapshots and versions,
        so rewriting the name would break that matching.
        """
        back = _TreeNode(role="push button", description="Go back", x=1, y=1, w=5, h=5)
        root = _TreeNode(role="frame", name="Files", w=100, h=100, kids=(back,))

        records, _, _ = runtime.render_tree(root, None, [0], prune=True)

        self.assertEqual(records[1]["name"], "")
        self.assertEqual(records[1]["description"], "Go back")


class ClickableMarkerTests(AtspiPatchedTestCase):
    """The tree marks which elements can be clicked without coordinates."""

    def make(self, actions):
        class Node(_TreeNode):
            def get_action_iface(self):
                # Real nodes without actions do not implement Action. The
                # runtime checks the interface before the action count because
                # LibreOffice's ATK bridge asserts on non-ATK_ACTION objects.
                return object() if actions else None

            def get_n_actions(self):
                return len(actions)

            def get_action_name(self, index):
                return actions[index]

            def get_action_description(self, index):
                return ""

        return Node(role="push button", name="Home", x=1, y=1, w=5, h=5)

    def segment(self, node):
        """Like record_for, read the action table once and derive the marker."""
        _, has_click = runtime.node_actions(node)
        return runtime.state_segment(node, has_click_action=has_click)

    def test_element_with_click_action_is_marked(self):
        self.assertIn("has-click-action", self.segment(self.make(["click"])))

    def test_element_without_any_action_is_not_marked(self):
        """Nautilus sidebar rows have a Component interface but no Action interface.

        node_actions() hides click-like actions, so without the marker such a
        row renders exactly like a push button with a click action, and the
        agent has to fall back to coordinates for both.
        """
        self.assertNotIn("has-click-action", self.segment(self.make([])))

    def test_marker_matches_what_click_would_actually_invoke(self):
        """The click marker agrees with the action that click would invoke.

        `node_actions()` produces the marker and `preferred_action_index()`
        picks the action, because rendering may read the action table only
        once: repeated queries through LibreOffice's ATK bridge can make the
        application exit. This test keeps the two implementations consistent.
        """
        cases = [
            ["menu"],                    # secondary action only, nothing to click
            ["activate"],                # exact match
            ["click", "menu"],           # mixed
            [],                          # no actions at all
            ["Press the button"],        # descriptive name, substring fallback
        ]
        for actions in cases:
            node = self.make(actions)
            _, has_click = runtime.node_actions(node)
            invocable = runtime.preferred_action_index(node) is not None
            self.assertEqual(
                has_click, invocable,
                "action table {!r}: mark says {}, invocable is {}".format(
                    actions, has_click, invocable),
            )
            self.assertEqual(has_click, "has-click-action" in self.segment(node))


class PlaceholderAttributeIntrospectionTests(AtspiPatchedTestCase):
    """Object attributes are read only for text inputs that may carry a placeholder."""

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


class DropdownItemTests(AtspiPatchedTestCase):
    """Semantic actions on dropdown items close the popup without committing.

    Nothing afterwards reveals that the value was not applied.
    """

    def popup(self):
        return _TreeNode(role="window", name="")

    def test_table_cell_in_unnamed_popup_is_a_dropdown_item(self):
        """LibreOffice's line spacing dropdown exposes its items as table cells.

        do_action on `table cell Double` returns True and closes the dropdown,
        but the control still shows Single; only a coordinate click commits.
        The failure is invisible afterwards: the popup disappears with its
        elements, so the tree always changes and a "nothing changed, retry"
        check never fires, and the dialog's combo box is a phantom node that
        reports no value to read back.
        """
        cell = _TreeNode(role="table cell", name="Double", x=3, y=71, w=358, h=21)

        self.assertTrue(runtime.is_dropdown_item(cell, self.popup()))

    def test_menu_item_is_not_treated_as_dropdown_item(self):
        """Menu items are not dropdown items.

        do_action works on items such as `Rename…` in the Nautilus context
        menu, so treating them as dropdown items would block a working path.
        """
        item = _TreeNode(role="menu item", name="Rename…", x=7, y=189, w=304, h=25)

        self.assertFalse(runtime.is_dropdown_item(item, self.popup()))

    def test_table_cell_in_main_frame_is_not_a_dropdown_item(self):
        """Calc sheet cells also use the table cell role but are not dropdown items."""
        cell = _TreeNode(role="table cell", name="A1", x=10, y=10, w=80, h=20)
        frame = _TreeNode(role="frame", name="untitled - Calc", w=900, h=600)

        self.assertFalse(runtime.is_dropdown_item(cell, frame))

    def test_table_cell_in_named_dialog_is_not_a_dropdown_item(self):
        """A dialog has a title and the dialog role; a dropdown is an unnamed window."""
        cell = _TreeNode(role="table cell", name="calc-test.csv", x=41, y=157, w=332, h=21)
        dialog = _TreeNode(role="dialog", name="Document Recovery", w=578, h=406)

        self.assertFalse(runtime.is_dropdown_item(cell, dialog))


class EmptyCellOmissionTests(AtspiPatchedTestCase):
    """Most cells in a Calc viewport are empty, and rendering them wastes budget."""

    def test_empty_cells_get_no_record_and_the_notice_names_the_verified_path(self):
        """Empty cells are skipped before record_for, which would spend budget.

        Omitted cells have no element index, so the notice says how to reach
        them. It is an annotation on the table, because the core renders
        annotations and drops provider lines. Typing a reference into the
        Name Box does not work: set_value changes its text without
        navigating, and click does not give it keyboard focus. Moving the
        cell cursor with keypress and then typing without a ref does work.
        """
        table = object()
        node = _TreeNode(role="table", name="Data")
        node.get_table_iface = lambda: table
        cells = {(0, 0): _TreeNode(role="table cell", name="A1"),
                 (0, 1): _TreeNode(role="table cell", name="B1")}
        table_api = mock.Mock()
        table_api.get_accessible_at.side_effect = lambda _table, row, col: cells[(row, col)]
        table_api.get_n_rows.return_value = 1
        table_api.get_n_columns.return_value = 2
        values = {id(cells[(0, 0)]): ("", True), id(cells[(0, 1)]): ("42", True)}
        records = []
        lines = []
        annotations = []
        record_for = mock.Mock(return_value={
            "index": 1, "localizedControlType": "table cell",
            "controlType": "table cell", "frame": None,
        })

        with mock.patch.object(
            runtime, "visible_cell_range", return_value=(0, 0, 0, 1)
        ), mock.patch.object(
            runtime.Atspi, "Table", table_api, create=True
        ), mock.patch.object(
            runtime, "text_value_with_status",
            side_effect=lambda cell, text_limit=None: values[id(cell)],
        ), mock.patch.object(runtime, "record_for", record_for):
            rendered = runtime.render_visible_cells(
                node, 0, [0], None, records, lines,
                indexer=runtime.StableIndexer({}), annotations=annotations,
            )

        self.assertEqual(rendered, 1)
        self.assertEqual(record_for.call_count, 1)
        self.assertIs(record_for.call_args.args[0], cells[(0, 1)])
        summary = [a for a in annotations if a["kind"] == "cell_summary"]
        self.assertEqual(1, len(summary))
        self.assertEqual([0], summary[0]["parentRuntimeId"])
        self.assertEqual(1, summary[0]["count"])
        notice = summary[0]["detail"]
        self.assertIn("1 empty cells in view A1:B1 omitted (1 listed; table is 1x2)", notice)
        self.assertIn("move the cell cursor there with keypress, then type without a ref", notice)
        self.assertNotIn("Name Box", notice)


class ElectronActionNamesTests(AtspiPatchedTestCase):
    """Chromium and Electron action names cause both missed and false click entries."""

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
        """`doDefault` is Chromium's default action and serves as the click entry.

        VS Code nodes expose ('doDefault', 'showContextMenu'). Without this name
        they get no `[has-click-action]` marker and
        `click_method:"accessibility"` fails, so Electron apps could not be
        clicked through the semantic channel at all.
        """
        node = self.make(["doDefault", "showContextMenu"])
        names, has_click = runtime.node_actions(node)

        self.assertTrue(has_click)
        self.assertIsNotNone(runtime.preferred_action_index(node))
        self.assertNotIn("doDefault", names,
                         "it is the click itself and must not be listed in More actions")

    def test_click_ancestor_is_not_this_element_s_click(self):
        """`clickAncestor` contains "click" but clicks an ancestor element.

        VS Code nodes expose ('clickAncestor', 'showContextMenu'), which the
        substring fallback would otherwise match. Treating it as this
        element's click would act elsewhere while neither the result nor the
        tree shows it, the worst class of failure.
        """
        node = self.make(["clickAncestor", "showContextMenu"])
        _, has_click = runtime.node_actions(node)

        self.assertFalse(has_click)
        self.assertIsNone(runtime.preferred_action_index(node))

    def test_gecko_spells_it_with_a_space(self):
        """Toolkits spell the same action differently.

        Chromium and Electron use `clickAncestor`, while Gecko (Thunderbird)
        uses `click ancestor` with a space. Excluding only one spelling lets
        the other match the substring fallback and click the ancestor again.
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
            self.assertFalse(has_click, "{!r} must not count as this element's action".format(spelling))

    def test_gecko_checkbox_state_named_actions_are_click_entries(self):
        """Gecko names checkbox actions after the resulting state.

        A checked box exposes only `uncheck` and an unchecked one only
        `check`; invoking it toggles the box. Without these names every Gecko
        checkbox falls back to coordinate clicks, and settings screens consist
        mostly of checkboxes.
        """
        for spelling in ("check", "uncheck"):
            node = self.make([spelling])
            names, has_click = runtime.node_actions(node)
            self.assertTrue(has_click, "{!r} must count as a click entry point".format(spelling))
            self.assertNotIn(spelling, names, "it is the click itself and must not be listed in More actions")

    def test_plain_click_still_works(self):
        node = self.make(["click", "showContextMenu"])
        _, has_click = runtime.node_actions(node)

        self.assertTrue(has_click)
        self.assertIsNotNone(runtime.preferred_action_index(node))


class ObjectReplacementTests(AtspiPatchedTestCase):
    def test_object_replacement_placeholder_is_stripped(self):
        """Chromium marks embedded objects with U+FFFC, rendered as `Value: ￼￼￼`.

        The placeholder carries no information yet looks like content, which
        suggests that the control already has a value. Pages such as the
        VS Code welcome page contain many of them.
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
        """Focus held by something invisible to the accessibility tree is reported.

        After settings.json changes, VS Code can show a native "A setting has
        changed that requires a restart to take effect." dialog. It belongs to
        the same process and locks the app, yet does not exist in AT-SPI. The
        agent sees a normal tree while every action is rejected for no visible
        reason; only a screenshot can explain it.
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
        """If the diagnostic enumeration fails, the original error is still raised."""

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
        """Qt stores tooltips as whole HTML documents, CSS included.

        In VLC's preferences such HTML took more than half of an observation
        while the message is usually one sentence, so an unprocessed
        `Description:` would exhaust the observation budget.
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
        """Plain text is untouched, since code, templates and math use angle brackets.

        Stripping them would alter the data the agent reads.
        """
        for text in ("a < b and c > d", "List<String> items", "if x<3: pass"):
            self.assertEqual(runtime.plain_text_from_rich_text(text), text)

    def test_empty_and_none_are_safe(self):
        self.assertEqual(runtime.plain_text_from_rich_text(None), "")
        self.assertEqual(runtime.plain_text_from_rich_text(""), "")


class PruneCostTests(AtspiPatchedTestCase):
    """Pruning reads only the fields it needs before building a full record.

    In GIMP (GAIL) a full record_for costs about 8 ms per node, and pruning
    discards most records a render would build. Building them eagerly pushes
    a whole-tree render past the server-side request timeout.
    """

    def test_pruned_nodes_never_pay_for_a_full_record(self):
        # Unnamed fillers that pruning removes, plus one button that is kept.
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

        records, _, _ = runtime.render_tree(window, {"x": 0, "y": 0, "width": 100, "height": 100}, [0])

        # Only the window and the surviving button pay for a full record.
        self.assertEqual(len(records), 2, [r["name"] for r in records])
        self.assertEqual(len(calls), 2,
                         "pruned nodes must not reach record_for, but it ran {} times".format(len(calls)))

    def test_prune_decision_still_keeps_named_containers(self):
        """Named containers are still kept.

        The toggle button of the line spacing combo box is unnamed and can
        only be identified through its parent `panel Line Spacing`. Pruning
        the panel would leave the target in the tree but unidentifiable.
        """
        inner = _TreeNode(role="toggle button", name="", x=2, y=2, w=4, h=4)
        named_panel = _TreeNode(role="panel", name="Line Spacing", x=1, y=1, w=8, h=8,
                                kids=(inner,))
        window = _TreeNode(role="frame", name="W", x=0, y=0, w=100, h=100,
                           kids=(named_panel,))
        records, _, _ = runtime.render_tree(
            window, {"x": 0, "y": 0, "width": 100, "height": 100}, [0])
        self.assertIn("Line Spacing", [r["name"] for r in records])

    def test_unpruned_view_does_not_apply_pressure_pruning(self):
        fillers = tuple(
            _TreeNode(role="filler", x=1, y=1, w=5, h=5)
            for _ in range(4)
        )
        window = _TreeNode(
            role="frame", name="W", w=100, h=100, kids=fillers)

        records, _, _ = runtime.render_tree(
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

        records, _, _ = runtime.render_tree(
            window,
            {"x": 0, "y": 0, "width": 100, "height": 100},
            [0],
            prune=True,
            max_tree_nodes=5,
        )

        self.assertEqual(len(records), 5)
        self.assertEqual(records[-1]["controlType"], "push button")
