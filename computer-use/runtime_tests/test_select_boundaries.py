"""Exact selection verification and honest evidence after partial mutations."""

from runtime_test_support import *  # noqa: F401,F403
from runtime_tests.test_capabilities import _Node, _Text, _TextApi


class SelectBoundaryTests(AtspiPatchedTestCase):
    def setUp(self):
        super().setUp()
        for name, value in (("time", _FakeClock()),
                            ("require_window_focus", lambda *args: None)):
            patch = mock.patch.object(runtime, name, value)
            patch.start()
            self.addCleanup(patch.stop)
        patch = mock.patch.object(runtime.Atspi, "Text", _TextApi, create=True)
        patch.start()
        self.addCleanup(patch.stop)

    def paragraphs(self, *texts):
        parent = _Node(role="document text")
        for text in texts:
            child = _Node(role="paragraph")
            child.text_iface = _Text(text)
            parent.add(child)
        return parent.children

    def assert_unknown(self, invoke, phase="readback"):
        with self.assertRaises(runtime.OperationFailure) as caught:
            invoke()
        self.assertEqual(caught.exception.failure_code, "effect_unknown")
        self.assertTrue(caught.exception.input_events_emitted)
        self.assertEqual(caught.exception.failure_phase, phase)
        return caught.exception

    def test_duplicate_substring_rejects_before_selection(self):
        para, = self.paragraphs("repeated repeated")
        with self.assertRaises(runtime.OperationFailure) as caught:
            runtime.perform_select({"text": "repeated"}, None, para, {}, None, [], {})
        self.assertEqual(caught.exception.failure_code, "invalid_argument")
        self.assertFalse(caught.exception.input_events_emitted)
        self.assertIsNone(para.text_iface.selection)

    def test_exact_text_range_is_required_even_if_other_span_is_nonempty(self):
        para, = self.paragraphs("prefix suffix")
        para.text_iface.selection = (0, 6)
        with mock.patch.object(_TextApi, "set_selection", return_value=True):
            self.assert_unknown(lambda: runtime.select_text_range(para, 7, 13))
        self.assertEqual(para.text_iface.selection, (0, 6))

    def test_unreadable_text_after_mutation_is_not_success(self):
        para, = self.paragraphs("selected")
        original = _TextApi.add_selection

        def mutate_and_lose_readback(iface, start, end):
            original(iface, start, end)
            iface.get_unreadable = True
            return True

        def read_count(iface):
            if getattr(iface, "get_unreadable", False):
                raise RuntimeError("provider disconnected")
            return 0

        with mock.patch.object(_TextApi, "add_selection", mutate_and_lose_readback), \
                mock.patch.object(_TextApi, "get_n_selections", read_count):
            self.assert_unknown(lambda: runtime.select_text_range(para, 0, 8))
        self.assertEqual(para.text_iface.selection, (0, 8))

    def test_api_exception_after_mutation_preserves_emission(self):
        para, = self.paragraphs("selected")

        def mutate_then_raise(iface, start, end):
            iface.selection = (start, end)
            raise RuntimeError("provider disconnected")

        with mock.patch.object(_TextApi, "add_selection", mutate_then_raise):
            self.assert_unknown(lambda: runtime.select_text_range(para, 0, 8), "emission")

    def test_cross_paragraph_overshoot_fails(self):
        paragraphs = self.paragraphs("one", "two", "three")

        def overshoot(key):
            for paragraph in paragraphs:
                paragraph.text_iface.selection = (0, len(paragraph.text_iface.text))

        with mock.patch.object(runtime, "send_key", overshoot):
            self.assert_unknown(lambda: runtime.select_text_run(
                object(), paragraphs[0], paragraphs[1]))

    def test_cross_paragraph_missing_prefix_fails(self):
        first, last = self.paragraphs("one", "two")

        def shifted(key):
            first.text_iface.selection = (1, 3)
            last.text_iface.selection = (0, 3)

        with mock.patch.object(runtime, "send_key", shifted):
            self.assert_unknown(lambda: runtime.select_text_run(object(), first, last))

    def test_cross_paragraph_exact_range_succeeds(self):
        first, last, outside = self.paragraphs("one", "two", "three")

        def extend(key):
            first.text_iface.selection = (0, 3)
            last.text_iface.selection = (0, 3)

        with mock.patch.object(runtime, "send_key", extend):
            run, emitted = runtime.select_text_run(object(), first, last)
        self.assertEqual(run, [first, last])
        self.assertTrue(emitted)
        self.assertIsNone(outside.text_iface.selection)

    def test_caret_move_before_keyboard_failure_is_reported(self):
        first, last = self.paragraphs("one", "two")
        with mock.patch.object(runtime, "send_key", side_effect=RuntimeError("keys failed")):
            self.assert_unknown(lambda: runtime.select_text_run(object(), first, last),
                                "emission")
        self.assertEqual(first.text_iface.caret, 0)

    def test_caret_refusal_after_move_reports_possible_mutation(self):
        first, last = self.paragraphs("one", "two")

        def move_and_refuse(iface, offset):
            iface.caret = offset
            return False

        with mock.patch.object(_TextApi, "set_caret_offset", move_and_refuse), \
                mock.patch.object(runtime, "send_key") as keys:
            self.assert_unknown(lambda: runtime.select_text_run(object(), first, last),
                                "emission")
        keys.assert_not_called()
        self.assertEqual(first.text_iface.caret, 0)

    def table(self):
        table = _Node(role="table")
        for index in range(4):
            table.add(_Node(role="table cell", name=str(index)))
        table.table_iface = table
        table.selection_iface = table
        table.selected = [3]
        return table

    def table_apis(self):
        table_api = mock.Mock()
        table_api.get_row_at_index.side_effect = lambda table, index: index // 2
        table_api.get_column_at_index.side_effect = lambda table, index: index % 2
        table_api.get_index_at.side_effect = lambda table, row, col: row * 2 + col
        table_api.get_accessible_at.side_effect = lambda table, row, col: table.children[row * 2 + col]
        selection_api = mock.Mock()
        selection_api.clear_selection.side_effect = lambda table: table.selected.clear() or True
        selection_api.select_child.side_effect = lambda table, index: table.selected.append(index) or True
        selection_api.get_n_selected_children.side_effect = lambda table: len(table.selected)
        selection_api.get_selected_child.side_effect = lambda table, index: table.children[table.selected[index]]
        for name, api in (("Table", table_api), ("Selection", selection_api)):
            patch = mock.patch.object(runtime.Atspi, name, api, create=True)
            patch.start()
            self.addCleanup(patch.stop)
        return table_api, selection_api

    def test_same_cell_count_with_wrong_cells_fails(self):
        table = self.table()
        _, selection = self.table_apis()
        selection.select_child.side_effect = lambda table, index: table.selected.append(index + 2) or True
        self.assert_unknown(lambda: runtime.select_cells(table.children[0], table.children[1]))
        self.assertEqual(table.selected, [2, 3])

    def test_different_table_end_rejects_before_clear(self):
        table, other = self.table(), self.table()
        _, selection = self.table_apis()
        with self.assertRaises(runtime.OperationFailure) as caught:
            runtime.select_cells(table.children[0], other.children[1])
        self.assertFalse(caught.exception.input_events_emitted)
        selection.clear_selection.assert_not_called()
        self.assertEqual(table.selected, [3])

    def test_failed_first_cell_still_reports_cleared_selection(self):
        table = self.table()
        _, selection = self.table_apis()
        selection.select_child.return_value = False
        selection.select_child.side_effect = None
        self.assert_unknown(lambda: runtime.select_cells(table.children[0], table.children[1]),
                            "emission")
        self.assertEqual(table.selected, [])

    def test_table_readback_disconnect_reports_mutated_range(self):
        table = self.table()
        _, selection = self.table_apis()
        selection.get_selected_child.side_effect = RuntimeError("disconnected")
        self.assert_unknown(lambda: runtime.select_cells(table.children[0], table.children[1]))
        self.assertEqual(table.selected, [0, 1])

    def test_clear_refusal_after_mutation_is_reported(self):
        table = self.table()
        _, selection = self.table_apis()

        def clear_and_refuse(table):
            table.selected.clear()
            return False

        selection.clear_selection.side_effect = clear_and_refuse
        self.assert_unknown(lambda: runtime.select_cells(table.children[0], table.children[1]),
                            "emission")
        selection.select_child.assert_not_called()
        self.assertEqual(table.selected, [])


class ComboOptionSelectTests(SelectBoundaryTests):
    """select on a closed drop-down picks the option named by text.

    Chrome and Firefox expose an HTML <select> as a combo box with a menu of
    menu items whose only action is "select"; the options are not SHOWING
    while the list is closed, so they have no refs. select {ref: combo,
    text: "Seattle"} searched the combo's own text instead and failed with
    select_text_not_found. Chrome selects through the item action (its
    Selection.select_child returns False); Firefox through either.
    """

    def _combo(self, value="Beijing", options=("Beijing", "Shanghai", "Seattle"),
               item_action=True, selection_api=True):
        combo = _Node(role="combo box", name="City")
        menu = _Node(role="menu")
        combo.add(menu)
        items = []
        for name in options:
            item = _Node(role="menu item", name=name, states=())
            if name == value:
                item.states.add(STATE.SELECTED)
            items.append(item)
        menu.add(*items)

        def choose(index):
            for other in items:
                other.states.discard(STATE.SELECTED)
            items[index].states.add(STATE.SELECTED)
            return True

        for index, item in enumerate(items):
            item.get_n_actions = lambda: 1
            item.get_action_name = lambda _i: "select"
            item.do_action = (lambda _i, index=index: choose(index)) if item_action \
                else (lambda _i: True)
        if selection_api:
            menu.selection_iface = mock.Mock()
            selection = mock.Mock()
            selection.select_child = lambda _iface, index: choose(index)
            selection.is_child_selected = (
                lambda _iface, index: STATE.SELECTED in items[index].states)
            patch = mock.patch.object(runtime.Atspi, "Selection", selection, create=True)
            patch.start()
            self.addCleanup(patch.stop)
        return combo, items

    def test_option_is_chosen_by_its_name(self):
        combo, items = self._combo()
        notes = []

        runtime.perform_select({"text": "Seattle"}, None, combo, {}, None, notes, {})

        self.assertIn(STATE.SELECTED, items[2].states)
        self.assertNotIn(STATE.SELECTED, items[0].states)
        self.assertTrue(any("Seattle" in note for note in notes))

    def test_selection_api_is_used_when_the_item_action_does_nothing(self):
        combo, items = self._combo(item_action=False)

        runtime.perform_select({"text": "Seattle"}, None, combo, {}, None, [], {})

        self.assertIn(STATE.SELECTED, items[2].states)

    def test_unknown_option_is_rejected_before_input(self):
        combo, _ = self._combo()
        with self.assertRaises(runtime.OperationFailure) as caught:
            runtime.perform_select({"text": "Paris"}, None, combo, {}, None, [], {})
        self.assertEqual(caught.exception.failure_code, "invalid_argument")
        self.assertFalse(caught.exception.input_events_emitted)
        self.assertIn("Seattle", str(caught.exception))

    def test_option_that_does_not_take_reports_unknown_effect(self):
        combo, _ = self._combo(item_action=False, selection_api=False)
        self.assert_unknown(lambda: runtime.perform_select(
            {"text": "Seattle"}, None, combo, {}, None, [], {}))
