#!/usr/bin/env python3
"""Linux provider snapshot rendering, pruning, indexing, and resolution tests."""

import inspect

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

        _, lines = runtime.render_tree(root, None, [0], prune=True)

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
        """GTK icon-only buttons have no name; only the description identifies them.

        In the Nautilus toolbar, `Go back`, `Go forward`, `Search` and
        `Show list` all have empty names. Without the description, core file
        manager actions render as unnamed `push button` nodes that can only be
        reached by coordinates.
        """
        back = _TreeNode(role="push button", description="Go back", x=1, y=1, w=5, h=5)
        root = _TreeNode(role="frame", name="Files", w=100, h=100, kids=(back,))

        _, lines = runtime.render_tree(root, None, [0], prune=True)

        self.assertIn('[desc="Go back"]', "\n".join(lines))

    def test_description_disambiguates_identically_named_buttons(self):
        """Toggle buttons all named Menu differ only in their description."""
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
        """A description equal to the name is not repeated, which would waste tokens."""
        node = _TreeNode(role="push button", name="Home", description="Home",
                         x=1, y=1, w=5, h=5)
        root = _TreeNode(role="frame", name="Files", w=100, h=100, kids=(node,))

        _, lines = runtime.render_tree(root, None, [0], prune=True)

        self.assertNotIn("Description:", "\n".join(lines))

    def test_name_is_not_overwritten_by_description(self):
        """The description never replaces the name.

        Elements are matched by role and name across snapshots and versions,
        so rewriting the name would break that matching.
        """
        back = _TreeNode(role="push button", description="Go back", x=1, y=1, w=5, h=5)
        root = _TreeNode(role="frame", name="Files", w=100, h=100, kids=(back,))

        records, _ = runtime.render_tree(root, None, [0], prune=True)

        self.assertEqual(records[1]["name"], "")
        self.assertEqual(records[1]["description"], "Go back")

class StaleElementIndexTests(AtspiPatchedTestCase):
    """`runtimeId` is a positional path that points elsewhere once the tree changes."""

    def record(self, role, name, frame=None, path=(0, 1)):
        return {"runtimeId": list(path), "controlType": role, "name": name,
                "automationId": "", "frame": frame}

    def test_rejects_node_whose_role_and_name_changed(self):
        """A stale index that now resolves to a different control is rejected.

        In Nautilus, a path recorded for `menu item Rename…` while a context
        menu was open points at the toolbar's `toggle button Menu` once the
        menu closes, and nothing reports an error. `Move to Trash` sits next
        to Rename in the same menu, so silently acting on the wrong item is
        unacceptable.
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
        """Many dialog controls are unnamed, so position is their only identity clue."""
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
        """A named node resolved for an unnamed record means the element changed."""
        named = _TreeNode(role="toggle button", name="Menu", x=10, y=20, w=30, h=40)
        frame = {"x": 10, "y": 20, "width": 30, "height": 40}

        self.assertFalse(
            runtime.record_still_matches(named, self.record("toggle button", "", frame), None)
        )

    def test_accessible_id_wins_when_present(self):
        """A stable toolkit id takes precedence over changes in name or position."""

        class WithID(_TreeNode):
            def get_accessible_id(self):
                return "save-button"

        node = WithID(role="push button", name="Save As", x=99, y=99, w=1, h=1)
        record = {"runtimeId": [0], "controlType": "push button", "name": "Save",
                  "automationId": "save-button", "frame": None}

        self.assertTrue(runtime.record_still_matches(node, record, None))


    def test_find_element_refuses_stale_path_end_to_end(self):
        """find_element does not resolve a stale path to a different control.

        Unit tests of the matching predicate cannot show that callers use it,
        so this test exercises the whole lookup.
        """
        toolbar = _TreeNode(role="toggle button", name="Menu", x=715, y=23, w=26, h=46)
        window = _TreeNode(role="frame", name="Files", w=900, h=600, kids=(toolbar,))
        app = _TreeNode(role="application", name="Files", kids=(window,))

        stale = {"runtimeId": [0, 0], "controlType": "menu item",
                 "name": "Rename…", "automationId": "", "frame": None}

        # The path alone reaches the toolbar, which is not the recorded element.
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

        action_names() hides click-like actions, so without the marker such a
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


class ResolveAppRetryTests(AtspiPatchedTestCase):
    """A temporarily unreadable app is not an absent app."""

    def setUp(self):
        super().setUp()
        self.slept = []
        real_sleep = runtime.time.sleep
        runtime.time.sleep = lambda s: self.slept.append(s)
        self.addCleanup(setattr, runtime.time, "sleep", real_sleep)

    def patch_iter_apps(self, results):
        """Each item is the result of one call; exception items are raised."""
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
        """An enumeration can be briefly empty while an app rebuilds its windows.

        LibreOffice can be missing from an enumeration right after a
        successful observation although its AT-SPI desktop entry never goes
        away. Reporting the app as absent would push the agent to try other
        names, restart the app or give up, when reading again would find it.
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
        """A present app is returned immediately, without delaying the normal path."""
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

    def test_notice_states_the_verified_path_only(self):
        """The omission notice suggests only the route that works.

        Omitted empty cells have no element index, so the notice must say how
        to reach them. Typing a reference into the Name Box does not work:
        set_value changes its text without navigating, like the dropdown commit
        above, and click does not give it keyboard focus. Moving the cell
        cursor with press_key and then calling type_text does work.
        """
        source = effective_runtime_source()

        self.assertIn("empty cell(s) omitted", source)
        self.assertIn("move the cell cursor there", source)
        self.assertNotIn("Name Box and press", source)

    def test_empty_cells_are_skipped_before_costing_a_record(self):
        """Empty cells are skipped before record_for, which would still spend budget."""
        body = inspect.getsource(runtime.render_visible_cells)

        skip = body.index("if not value:")
        build = body.index("record = record_for(")
        self.assertLess(skip, build, "the skip decision must happen before record_for")

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

    def test_covered_set_has_a_single_source_of_truth(self):
        """The click-covered action set is defined once, since copies drift apart."""
        source = effective_runtime_source()

        self.assertEqual(source.count('"default.activate",'), 1,
                         "CLICK_COVERED_ACTIONS must not be duplicated")

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

        records, _ = runtime.render_tree(window, {"x": 0, "y": 0, "width": 100, "height": 100}, [0])

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
    """Actions that declare a target state are verified by reading it back.

    Modeled on Playwright's `_setChecked` (dom.ts):

        const finalState = await isChecked(progress);
        if (finalState.matches !== state)
          throw new NonRecoverableDOMError('Clicking the checkbox did not change its state');

    The error is non-recoverable and not retried: repeating the same action
    cannot give a different result.
    """

    def test_action_names_come_from_measurement_not_from_playwright(self):
        """ACTION_MUST_FLIP lists action names observed in real applications.

        Playwright's set-state names (check, uncheck, expand, collapse) do not
        describe AT-SPI flip actions. Observed names such as
        `expand or contract` and `toggle` flip a state instead of setting a
        value.
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
        """A reached state does not prove that the intended behavior happened.

        In VLC's preferences, toggling a radio button flips CHECKED without
        switching the panel. This check cannot detect that, so the note says
        so instead of becoming a source of false positives.
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
        # The note must say not to retry and name the next route to try.
        self.assertIn("Repeating the same call will not help", note)
        self.assertIn("gui", note)

    def test_actions_that_declare_nothing_are_not_judged(self):
        """Actions such as `click` and `menu` declare no target state to verify.

        Judging them anyway would produce false positives.
        """
        # Common action names that promise no readable state change.
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
    """Indices survive across snapshots, following Playwright's ariaSnapshot.ts.

    If opening a dialog renumbered every element, a click with an index from
    the previous snapshot would silently hit a different control.
    """

    def test_same_element_keeps_its_number(self):
        known = {"0.1": {"index": 7, "role": "push button", "name": "Save"}}
        indexer = runtime.StableIndexer(known)
        self.assertEqual(indexer.index_for([0, 1], "push button", "Save"), 7)

    def test_role_or_name_change_invalidates_the_number(self):
        """As in Playwright, a changed role or name gets a new index.

        The element is no longer the same thing.
        """
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
        """With traversal order, one insertion would shift every later index."""
        known = {
            "0.0": {"index": 0, "role": "frame", "name": "W"},
            "0.1": {"index": 1, "role": "push button", "name": "A"},
            "0.2": {"index": 2, "role": "push button", "name": "B"},
        }
        indexer = runtime.StableIndexer(known)
        self.assertEqual(indexer.index_for([0, 0], "frame", "W"), 0)
        # A newly inserted element gets a fresh index instead of taking another's.
        fresh = indexer.index_for([0, 1, 5], "push button", "NEW")
        self.assertEqual(indexer.index_for([0, 1], "push button", "A"), 1)
        self.assertEqual(indexer.index_for([0, 2], "push button", "B"), 2)
        self.assertGreaterEqual(fresh, 3)

    def test_numbers_are_never_reused_within_one_snapshot(self):
        """Reusing an index within one snapshot would silently target the wrong node."""
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
    """Pruned nodes do not consume an index before being dropped.

    This must be an AtspiPatchedTestCase: under a bare TestCase relative_frame
    returns None, so every node below the root is pruned as invisible and the
    fixture exercises no pruning at all.
    """

    def test_pruned_nodes_do_not_consume_numbers(self):
        """Indices stay dense when nodes are pruned.

        Assigning an index before the prune decision spends numbers on nodes
        that never reach the records, leaving gaps. Pruned nodes are also
        missing from the known map, so each snapshot would claim new numbers
        and indices would keep growing during a session. With N elements in
        the tree, indices must fall in 0..N-1.
        """
        # Unnamed panels, pruned by role, surround two named buttons. Geometry
        # is explicit because the first prune check requires a non-None frame.
        filler = [_TreeNode(role="panel", x=1, y=1, w=5, h=5)
                  for _ in range(8)]
        keep_a = _TreeNode(role="push button", name="Save", x=2, y=2, w=5, h=5)
        keep_b = _TreeNode(role="push button", name="Cancel", x=3, y=3, w=5, h=5)
        window = _TreeNode(role="frame", name="W", w=900, h=600,
                           kids=tuple(filler[:4]) + (keep_a,)
                                + tuple(filler[4:]) + (keep_b,))

        # Pass an indexer: without one the index falls back to len(records),
        # which is dense by construction and would prove nothing.
        records, _lines = runtime.render_tree(
            window, None, [0], indexer=runtime.StableIndexer({}), prune=True)
        indices = sorted(r["index"] for r in records)
        self.assertEqual(indices, list(range(len(records))),
                         "indices must be dense, got {}".format(indices))

class TruncatedNameIdentityTests(AtspiPatchedTestCase):
    """Truncating a name with text_limit does not weaken element identity.

    Comparing the live full name with a record's truncated name fails once
    text_limit drops from 500 to 40, and matching silently falls back to the
    weakest role-plus-position check. text_limit is a per-request parameter,
    so comparisons cannot assume the default value.
    """

    LONG = "Manage — Settings and keyboard shortcuts, and more actions for the editor"

    def test_truncated_name_still_identifies_the_same_element(self):
        node = _TreeNode(role="push button", name=self.LONG)
        for limit in (500, 40, 10):
            record = runtime.record_for(node, 7, [0, 1], None, text_limit=limit)
            self.assertTrue(
                runtime.record_still_matches(node, record, None),
                "text_limit={} failed to recognize the same node".format(limit))

    def test_a_different_element_sharing_the_prefix_is_not_confused(self):
        """Two elements sharing a truncated prefix are not confused.

        Comparing only the truncated prefix would trade a silent mismatch for a
        worse silent false match. Truncated records therefore carry a
        fingerprint of the full name, and matching compares full names exactly.
        """
        node = _TreeNode(role="push button", name=self.LONG)
        sibling = _TreeNode(
            role="push button",
            name="Manage — Settings and keyboard shortcuts, and MORE actions here")
        for limit in (40, 10):
            record = runtime.record_for(node, 7, [0, 1], None, text_limit=limit)
            self.assertFalse(
                runtime.record_still_matches(sibling, record, None),
                "text_limit={} confused a different element sharing the prefix".format(limit))

    def test_short_truncation_margins_always_carry_a_fingerprint(self):
        """The ellipsis adds 3 characters, so length alone cannot reveal truncation."""
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
        """Only truncated records carry a name fingerprint."""
        node = _TreeNode(role="push button", name="Save")
        record = runtime.record_for(node, 0, [0], None, text_limit=500)
        self.assertNotIn("nameHash", record)

class SnapshotGrammarTests(unittest.TestCase):
    """Snapshot line grammar, modeled on Playwright's aria snapshot format.

    Playwright writes `- role "name" [attr=value]`. Free text must be quoted:
    names can contain colons, such as `panel PageShape: Weekday in school` in
    LibreOffice Impress, so an unquoted
    `<idx> <role> <name> Description: <desc>` line cannot separate the name
    from the field delimiters.
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
        """This line is ambiguous unless the name is quoted."""
        line = self._line(name="PageShape: Weekday in school",
                          description="Slide")
        self.assertIn('"PageShape: Weekday in school"', line)
        self.assertIn('[desc="Slide"]', line)

    def test_quotes_inside_a_name_are_escaped(self):
        line = self._line(name='say "hi"')
        self.assertIn(r'"say \"hi\""', line)

    def test_geometry_is_compact(self):
        """Geometry shrinks from 45 characters to 14 without losing information.

        `Frame: {x: 687, y: 23, width: 64, height: 46}` becomes
        `{687,23,64,46}`. Geometry makes up 35-50% of a tree, so this saves
        about 30% of the whole tree.
        """
        line = self._line(frame={"x": 687.0, "y": 23.0, "width": 64.0, "height": 46.0})
        self.assertIn("{687,23,64,46}", line)
        self.assertNotIn("Frame:", line)

    def test_value_goes_last_after_a_colon(self):
        """Matches the aria snapshot form `- textbox: Enter your name`."""
        line = self._line(localizedControlType="text", value="baseline-marker",
                          states="[focused]")
        self.assertTrue(line.rstrip().endswith(': "baseline-marker"'), line)

    def test_empty_segments_are_omitted(self):
        """Empty segments leave no placeholder bytes on the line."""
        self.assertEqual(self._line(name="OK"), '\t7 push button "OK"')

    def test_index_stays_at_the_head(self):
        """The index leads the line so it is easy to extract as a reference."""
        line = self._line(name="OK").strip()
        self.assertTrue(line.startswith("7 "), line)

class ElementReferenceTests(unittest.TestCase):
    """Element references have one renderer whose output parses as a target.

    A separate unquoted `role + " " + name` rendering, such as
    `entry Address and search bar`, cannot be pasted back into `expect`: the
    bare string spans the role and name fields, so it never matches.
    """

    def test_named_element_is_quoted_like_a_tree_line(self):
        self.assertEqual(
            runtime.element_reference("entry", "Address and search bar"),
            'entry "Address and search bar"',
        )

    def test_unnamed_element_uses_the_field_form(self):
        """Unnamed elements render as `role="separator"`, which parses back.

        Prose such as `an unnamed separator` cannot be read back as a target.
        """
        self.assertEqual(runtime.element_reference("separator", ""), 'role="separator"')

    def test_quotes_inside_a_name_are_escaped(self):
        self.assertEqual(
            runtime.element_reference("entry", 'say "hi"'), r'entry "say \"hi\""'
        )

    def test_focused_summary_uses_the_shared_renderer(self):
        """Focus evidence uses the shared renderer so it reads back as a target."""
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
