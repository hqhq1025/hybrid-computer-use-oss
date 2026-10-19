#!/usr/bin/env python3
"""Provider tests for new AX capabilities on LibreOffice."""

from runtime_test_support import *  # noqa: F401,F403


class _Node(_TreeNode):
    """A tree node that knows its parent and exposes optional interfaces."""

    _paths = 0

    def __init__(self, role="panel", name="", parent=None, states=None,
                 x=10, y=10, w=20, h=10, **kwargs):
        super().__init__(role=role, name=name, x=x, y=y, w=w, h=h, **kwargs)
        if states is not None:
            self.states = set(states)
        self.parent = parent
        _Node._paths += 1
        self.path = "/org/a11y/atspi/accessible/{}".format(_Node._paths)
        self.app = mock.Mock(bus_name=":1.42")
        self.text_iface = None
        self.selection_iface = None
        self.table_iface = None
        self.relations = []
        for child in self.children:
            child.parent = self

    def add(self, *children):
        for child in children:
            child.parent = self
            self.children.append(child)
        return self

    def get_parent(self):
        return self.parent

    def get_index_in_parent(self):
        return self.parent.children.index(self) if self.parent else -1

    def get_relation_set(self):
        return self.relations

    def get_text_iface(self):
        return self.text_iface

    def get_selection_iface(self):
        return self.selection_iface

    def get_table_iface(self):
        return self.table_iface

    def get_process_id(self):
        return 4242


class ComboProjectionTests(AtspiPatchedTestCase):
    """LibreOffice gtk3 combos: a drawn toggle stands for a hidden combo box."""

    def _combo_group(self, panel_name="Line Spacing", combo_name=""):
        # LibreOffice draws only the toggle; the text and combo box report
        # neither SHOWING nor a frame (origin INT_MIN).
        text = _Node(role="text", states=(), x=-2147483648, y=-2147483648, w=1, h=1)
        combo = _Node(role="combo box", name=combo_name, states=(),
                      x=-2147483648, y=-2147483648, w=1, h=1)
        toggle = _Node(role="toggle button")
        inner = _Node(role="filler").add(text, combo, toggle)
        panel = _Node(role="panel", name=panel_name).add(_Node(role="filler").add(inner))
        return panel, combo, toggle

    def test_toggle_of_hidden_combo_renders_as_named_combo_box(self):
        panel, _combo, toggle = self._combo_group()
        record = runtime.record_for(toggle, 0, [0, 0, 0, 2], None)
        self.assertEqual(record["controlType"], "combo box")
        self.assertEqual(record["name"], "Line Spacing")
        self.assertEqual(record["nameSource"], "ancestor")

    def test_combo_label_prefers_the_combo_name(self):
        _panel, _combo, toggle = self._combo_group(combo_name="Page Style")
        record = runtime.record_for(toggle, 0, [0], None)
        self.assertEqual(record["name"], "Page Style")
        self.assertEqual(record["nameSource"], "provider")

    def test_live_identity_checks_use_the_projected_role(self):
        """A ref recorded as combo box must still match its live toggle."""
        _panel, _combo, toggle = self._combo_group()
        record = runtime.record_for(toggle, 0, [0], None)
        self.assertTrue(runtime.record_still_matches(toggle, record, None))
        self.assertEqual(runtime.effective_name(toggle), "Line Spacing")

    def test_plain_toggle_button_is_not_projected(self):
        toggle = _Node(role="toggle button")
        _Node(role="filler").add(_Node(role="push button"), toggle)
        record = runtime.record_for(toggle, 0, [0], None)
        self.assertEqual(record["controlType"], "toggle button")

    def test_combo_projection_survives_render_tree_pruning(self):
        """The Paragraph dialog combo was pruned and unfindable (0810415c)."""
        panel, _combo, _toggle = self._combo_group()
        window = _Node(role="dialog", name="Paragraph", x=0, y=0, w=200, h=200).add(panel)
        records, _lines, _ = runtime.render_tree(
            window, {"x": 0, "y": 0, "width": 200, "height": 200}, [0])
        combos = [r for r in records if r["controlType"] == "combo box"]
        self.assertEqual([r["name"] for r in combos], ["Line Spacing"])


if __name__ == "__main__":
    unittest.main()
