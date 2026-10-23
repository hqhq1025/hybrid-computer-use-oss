"""Exact selection verification and honest evidence after partial mutations."""

from runtime_test_support import *  # noqa: F401,F403
from runtime_tests.test_capabilities import _Node, _Text, _TextApi


class EmbeddedEditorSelectionTests(AtspiPatchedTestCase):
    def setUp(self):
        super().setUp()
        patch = mock.patch.object(runtime.Atspi, "Text", _TextApi, create=True)
        patch.start()
        self.addCleanup(patch.stop)

    def editor(self, *lines):
        children = [FakeNode(text=_Text(line)) for line in lines]
        editor = FakeNode(states=(STATE.EDITABLE,),
                          text=_Text("\ufffc" * len(children)), children=children)
        editor.links = children
        return editor

    def select(self, editor, text):
        notes, evidence = [], {}
        args = {} if text is None else {"text": text}
        runtime.perform_select(args, None, editor, {}, None, notes, evidence)
        return notes, evidence

    def test_substring_uses_embedded_text_offsets_and_reads_them_back(self):
        editor = self.editor("Heading", "Sample body 中文")
        self.assertEqual(runtime.element_value(editor), "Heading\nSample body 中文")
        notes, evidence = self.select(editor, "body 中文")
        self.assertEqual(editor.children[1].get_text_iface().selection, (7, 14))
        self.assertIsNone(editor.get_text_iface().selection)
        self.assertIn('selected 15..22 "body 中文"', notes[0])
        self.assertTrue(evidence["readback_verified"])

    def test_all_text_in_one_embedded_object_can_be_selected(self):
        editor = self.editor("Whole line")
        self.select(editor, None)
        self.assertEqual(editor.children[0].get_text_iface().selection, (0, 10))

    def test_duplicate_across_embedded_objects_is_ambiguous_before_input(self):
        editor = self.editor("body first", "second body")
        with self.assertRaises(runtime.OperationFailure) as caught:
            self.select(editor, "body")
        self.assertIn("select_text_ambiguous", str(caught.exception))
        self.assertFalse(caught.exception.input_events_emitted)
        self.assertTrue(all(child.get_text_iface().selection is None
                            for child in editor.children))

    def test_cross_object_text_is_rejected_before_input(self):
        for text in ("first\nsecond", None):
            with self.subTest(text=text):
                editor = self.editor("first", "second")
                with self.assertRaises(runtime.OperationFailure) as caught:
                    self.select(editor, text)
                self.assertIn("crosses embedded", str(caught.exception))
                self.assertFalse(caught.exception.input_events_emitted)
                self.assertIsNone(editor.get_text_iface().selection)

    def test_missing_hypertext_child_does_not_hide_duplicate(self):
        editor = self.editor("body", "body")
        editor.links = editor.links[:1]
        with self.assertRaises(runtime.OperationFailure) as caught:
            self.select(editor, "body")
        self.assertFalse(caught.exception.input_events_emitted)
        self.assertIn("completely", str(caught.exception))

    def test_nested_object_offsets_are_not_flattened_for_mutation(self):
        editor = self.editor("prefix\ufffcsuffix")
        with self.assertRaises(runtime.OperationFailure) as caught:
            self.select(editor, "suffix")
        self.assertFalse(caught.exception.input_events_emitted)
        self.assertIn("nested objects", str(caught.exception))

    def test_text_change_during_selection_is_not_reported_as_verified(self):
        editor = self.editor("body")

        def replace_and_select(iface, start, end):
            iface.text = "fake"
            iface.selection = (start, end)
            return True

        with mock.patch.object(_TextApi, "add_selection", replace_and_select):
            with self.assertRaises(runtime.OperationFailure) as caught:
                self.select(editor, "body")
        self.assertEqual(caught.exception.failure_code, "effect_unknown")
        self.assertTrue(caught.exception.input_events_emitted)


class WrappedSelectionItemTests(AtspiPatchedTestCase):
    def setUp(self):
        super().setUp()
        self.selection = mock.Mock()
        self.selection.select_child.return_value = True
        self.selection.is_child_selected.return_value = True
        for name, value in (("time", _FakeClock()),):
            patch = mock.patch.object(runtime, name, value)
            patch.start()
            self.addCleanup(patch.stop)
        patch = mock.patch.object(runtime.Atspi, "Selection", self.selection,
                                  create=True)
        patch.start()
        self.addCleanup(patch.stop)

    def wrapped_cell(self):
        table = _Node(role="tree table")
        table.selection_iface = table
        table.add(_Node(role="table cell"))
        wrapper = _Node(role="table cell", states=(STATE.SELECTABLE,))
        wrapper.add(_Node(role="table cell"))
        target = _Node(role="table cell", name="Target", states=(STATE.SELECTABLE,))
        wrapper.add(target)
        table.add(wrapper)
        # The owner and renderer deliberately have different child indices.
        table.add(_Node(role="table cell"))
        table.children.insert(0, _Node(role="table cell", parent=table))
        return table, wrapper, target

    def test_cell_renderer_uses_owning_cell_index_and_verifies_target(self):
        table, wrapper, target = self.wrapped_cell()
        self.selection.select_child.side_effect = (
            lambda *_: target.states.add(STATE.SELECTED) or True)
        self.assertTrue(runtime.select_item(target))
        self.selection.select_child.assert_called_once_with(table, 2)
        self.selection.is_child_selected.assert_called_with(table, 2)

    def test_direct_item_still_uses_its_own_index(self):
        table, wrapper, _ = self.wrapped_cell()
        self.assertTrue(runtime.select_item(wrapper))
        self.selection.select_child.assert_called_once_with(table, 2)

    def test_an_arbitrary_panel_does_not_redirect_selection_to_ancestor(self):
        table, wrapper, target = self.wrapped_cell()
        wrapper.get_role_name = lambda: "panel"
        with self.assertRaises(runtime.OperationFailure) as caught:
            runtime.select_item(target)
        self.assertFalse(caught.exception.input_events_emitted)
        self.selection.select_child.assert_not_called()

    def test_named_selectable_ancestor_is_not_treated_as_a_renderer_wrapper(self):
        _, wrapper, target = self.wrapped_cell()
        wrapper.get_name = lambda: "Different item"
        with self.assertRaises(runtime.OperationFailure) as caught:
            runtime.select_item(target)
        self.assertFalse(caught.exception.input_events_emitted)
        self.selection.select_child.assert_not_called()

    def test_stale_child_index_rejects_before_input(self):
        _, _, target = self.wrapped_cell()
        target.get_index_in_parent = lambda: 0
        with self.assertRaises(runtime.OperationFailure) as caught:
            runtime.select_item(target)
        self.assertFalse(caught.exception.input_events_emitted)
        self.selection.select_child.assert_not_called()

    def test_owner_selected_without_target_selection_is_not_success(self):
        _, _, target = self.wrapped_cell()
        with self.assertRaises(runtime.OperationFailure) as caught:
            runtime.select_item(target)
        self.assertEqual(caught.exception.failure_code, "effect_unknown")
        self.assertTrue(caught.exception.input_events_emitted)

    def test_replaced_owner_during_selection_is_not_success(self):
        table, _, target = self.wrapped_cell()

        def replace(*_):
            target.states.add(STATE.SELECTED)
            table.children[2] = _Node(role="table cell", parent=table)
            return True

        self.selection.select_child.side_effect = replace
        with self.assertRaises(runtime.OperationFailure) as caught:
            runtime.select_item(target)
        self.assertEqual(caught.exception.failure_code, "effect_unknown")
        self.assertTrue(caught.exception.input_events_emitted)

    def test_matching_object_paths_from_other_bus_are_not_same_owner(self):
        _, wrapper, target = self.wrapped_cell()
        foreign = _Node(role="table cell", parent=wrapper)
        foreign.path = target.path
        foreign.app = mock.Mock(bus_name=":1.99")
        wrapper.get_child_at_index = lambda _: foreign
        with self.assertRaises(runtime.OperationFailure) as caught:
            runtime.select_item(target)
        self.assertFalse(caught.exception.input_events_emitted)
        self.selection.select_child.assert_not_called()


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

    def test_label_matching_option_does_not_skip_selection(self):
        combo, items = self._combo()
        combo.title = "Seattle"
        runtime.perform_select({"text": "Seattle"}, None, combo, {}, None, [], {})
        self.assertIn(STATE.SELECTED, items[2].states)
        self.assertNotIn(STATE.SELECTED, items[0].states)

    def test_label_matching_option_is_not_the_observed_value(self):
        combo, _items = self._combo()
        combo.title = "Seattle"
        self.assertEqual(runtime.element_value(combo), "Beijing")

    def test_matching_label_does_not_verify_a_refused_selection(self):
        combo, _items = self._combo(item_action=False, selection_api=False)
        combo.title = "Seattle"
        self.assert_unknown(lambda: runtime.perform_select(
            {"text": "Seattle"}, None, combo, {}, None, [], {}))

    def test_matching_label_without_selected_option_has_no_value(self):
        combo, items = self._combo()
        combo.title = "Seattle"
        for item in items:
            item.states.discard(STATE.SELECTED)
        self.assertEqual(runtime.element_value(combo), "")

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


class DropdownCommitTests(SelectBoundaryTests):
    """A drop-down that shows its choice must show the chosen option afterwards.

    A Qt combo box names itself after its current choice. Its popup items
    have only a Toggle action, which marks an item SELECTED without changing
    the choice, and the popup list has no Selection interface. The popup
    commits its current (FOCUSED) item on Return; arrow keys move that item.
    While the popup is closed the list reports a 0x0 frame.
    """

    def _qt_combo(self, value="Video", options=("Never", "Video", "", "Audio"),
                  commits=True):
        combo = _Node(role="combo box", name=value)
        combo.app.get_toolkit_name.return_value = "Qt"
        combo.get_application = lambda: combo.app
        popup = _Node(role="list", x=0, y=0, w=0, h=0)
        combo.add(popup)
        items = [_Node(role="list item", name=name) for name in options]
        popup.add(*items)
        state = {"focus": None, "keys": []}

        def set_open(opened):
            popup.component.extents.width = 235 if opened else 0
            popup.component.extents.height = 100 if opened else 0
            state["focus"] = options.index(combo.title) if opened else None
            for item in items:
                item.states.discard(STATE.FOCUSED)
            if opened:
                items[state["focus"]].states.add(STATE.FOCUSED)

        def combo_action(_index):
            set_open(True)
            return True

        combo.get_n_actions = lambda: 2
        combo.get_action_name = lambda index: ("ShowMenu", "Press")[index]
        combo.do_action = combo_action
        for item in items:
            item.get_n_actions = lambda: 1
            item.get_action_name = lambda _index: "Toggle"
            item.do_action = (lambda _index, item=item:
                              item.states.add(STATE.SELECTED) or True)

        def send_key(key):
            state["keys"].append(key)
            focus = state["focus"]
            if focus is None:
                return
            if key in ("Down", "Up"):
                step = 1 if key == "Down" else -1
                index = focus + step
                # The separator item cannot become current.
                while 0 <= index < len(items) and not items[index].title:
                    index += step
                if 0 <= index < len(items):
                    items[focus].states.discard(STATE.FOCUSED)
                    items[index].states.add(STATE.FOCUSED)
                    state["focus"] = index
            elif key == "Return":
                if commits:
                    combo.title = items[focus].title
                set_open(False)
            elif key == "Escape":
                set_open(False)

        patch = mock.patch.object(runtime, "send_key", send_key)
        patch.start()
        self.addCleanup(patch.stop)
        return combo, items, state

    def test_qt_combo_choice_is_committed_and_read_back(self):
        combo, _items, state = self._qt_combo()
        notes = []

        runtime.perform_select({"text": "Audio"}, None, combo, {}, None, notes, {})

        self.assertEqual(combo.title, "Audio")
        self.assertEqual(state["keys"], ["Down", "Return"])
        self.assertTrue(any("Audio" in note for note in notes))

    def test_choice_above_the_current_one_moves_up(self):
        combo, _items, state = self._qt_combo()

        runtime.perform_select({"text": "Never"}, None, combo, {}, None, [], {})

        self.assertEqual(combo.title, "Never")
        self.assertEqual(state["keys"], ["Up", "Return"])

    def test_selected_but_uncommitted_option_is_not_success(self):
        combo, items, state = self._qt_combo(commits=False)
        items[3].states.add(STATE.SELECTED)

        failure = self.assert_unknown(lambda: runtime.perform_select(
            {"text": "Audio"}, None, combo, {}, None, [], {}))

        self.assertEqual(combo.title, "Video")
        self.assertIn("\"Video\"", str(failure))
        self.assertIn("Return", state["keys"])

    def test_option_already_shown_needs_no_input(self):
        combo, _items, state = self._qt_combo()

        runtime.perform_select({"text": "Video"}, None, combo, {}, None, [], {})

        self.assertEqual(state["keys"], [])

    def test_select_on_a_popup_item_commits_its_combo(self):
        combo, items, state = self._qt_combo()
        combo.do_action(0)

        runtime.perform_select({}, None, items[0], {}, None, [], {})

        self.assertEqual(combo.title, "Never")
        self.assertEqual(state["keys"], ["Up", "Return"])

    def test_gtk_combo_commits_through_its_own_selection_without_keys(self):
        # GTK: Selection.select_child on the combo box sets its active item;
        # on the popup menu it only highlights the item.
        combo = _Node(role="combo box", name="Beijing")
        combo.app.get_toolkit_name.return_value = "gtk"
        combo.get_application = lambda: combo.app
        menu = _Node(role="menu", x=-2147483648, y=-2147483648, w=1, h=1)
        combo.add(menu)
        items = [_Node(role="menu item", name=name)
                 for name in ("Beijing", "Shanghai", "Seattle")]
        menu.add(*items)
        combo.selection_iface = mock.Mock()
        menu.selection_iface = mock.Mock()
        selection = mock.Mock()
        selection.get_n_selected_children.return_value = 1
        selection.get_selected_child.side_effect = (
            lambda target, index: next(item for item in items if item.title == combo.title))

        def select_child(target, index):
            if target is combo:
                combo.title = items[index].title
            else:
                items[index].states.add(STATE.SELECTED)
            return True

        selection.select_child = select_child
        selection.is_child_selected = (
            lambda _target, index: STATE.SELECTED in items[index].states)
        keys = []
        with mock.patch.object(runtime.Atspi, "Selection", selection, create=True), \
                mock.patch.object(runtime, "send_key", keys.append):
            runtime.perform_select({"text": "Seattle"}, None, combo, {}, None, [], {})

        self.assertEqual(combo.title, "Seattle")
        self.assertEqual(keys, [])

    def test_combo_value_is_the_choice_it_shows_not_a_stale_selected_item(self):
        # GTK leaves SELECTED on the popup item highlighted first, and Qt on
        # an item toggled without being committed.
        combo, items, _state = self._qt_combo(value="Audio")
        items[0].states.add(STATE.SELECTED)

        self.assertEqual(runtime.element_value(combo), "Audio")

    def test_labelled_combo_value_comes_from_its_selected_option(self):
        combo = _Node(role="combo box", name="City")
        menu = _Node(role="menu")
        combo.add(menu)
        menu.add(_Node(role="menu item", name="Beijing"),
                 _Node(role="menu item", name="Seattle",
                       states=(STATE.SELECTED,)))

        self.assertEqual(runtime.element_value(combo), "Seattle")

    def _gtk_named_combo(self):
        combo = _Node(role="combo box", name="Seattle")
        combo.app.get_toolkit_name.return_value = "gtk"
        combo.get_application = lambda: combo.app
        combo.selection_iface = combo
        menu = _Node(role="menu", states=(), w=0, h=0)
        combo.add(menu)
        items = [_Node(role="menu item", name=name) for name in ("Beijing", "Seattle")]
        menu.add(*items)
        # The accessible label and popup highlight both disagree with active.
        items[1].states.add(STATE.SELECTED)
        active = [0]
        selection = mock.Mock()
        selection.get_n_selected_children.return_value = 1
        selection.get_selected_child.side_effect = lambda *_: items[active[0]]
        selection.select_child.side_effect = lambda owner, index: active.__setitem__(0, index) or True
        patch = mock.patch.object(runtime.Atspi, "Selection", selection, create=True)
        patch.start()
        self.addCleanup(patch.stop)
        return combo, active, selection

    def test_gtk_label_and_popup_highlight_do_not_replace_active_value(self):
        combo, _active, _selection = self._gtk_named_combo()
        self.assertEqual(runtime.element_value(combo), "Beijing")

    def test_gtk_label_matching_choice_still_changes_active_item(self):
        combo, active, selection = self._gtk_named_combo()
        runtime.perform_select({"text": "Seattle"}, None, combo, {}, None, [], {})
        self.assertEqual(active, [1])
        selection.select_child.assert_called_once_with(combo, 1)
        self.assertEqual(runtime.element_value(combo), "Seattle")

    def test_gtk_active_item_already_selected_needs_no_input(self):
        combo, _active, selection = self._gtk_named_combo()
        runtime.perform_select({"text": "Beijing"}, None, combo, {}, None, [], {})
        selection.select_child.assert_not_called()

    def test_gtk_unreadable_active_value_does_not_trust_label_or_highlight(self):
        combo, _active, selection = self._gtk_named_combo()
        selection.get_selected_child.side_effect = RuntimeError("disconnected")
        selection.select_child.side_effect = None
        self.assertEqual(runtime.element_value(combo), "")
        self.assert_unknown(lambda: runtime.perform_select(
            {"text": "Seattle"}, None, combo, {}, None, [], {}))

    def test_gtk_missing_or_ambiguous_active_selection_has_no_value(self):
        combo, _active, selection = self._gtk_named_combo()
        for count in (0, 2):
            with self.subTest(count=count):
                selection.get_n_selected_children.return_value = count
                self.assertEqual(runtime.element_value(combo), "")

    def test_unreadable_toolkit_does_not_turn_a_matching_name_into_a_value(self):
        combo = _Node(role="combo box", name="Seattle")
        combo.get_application = lambda: combo.app
        combo.app.get_toolkit_name.side_effect = RuntimeError("disconnected")
        combo.add(_Node(role="menu").add(
            _Node(role="menu item", name="Beijing", states=(STATE.SELECTED,)),
            _Node(role="menu item", name="Seattle")))
        self.assertEqual(runtime.element_value(combo), "Beijing")


class ProjectedComboSelectTests(SelectBoundaryTests):
    """A LibreOffice gtk3 drop-down: a drawn toggle beside a hidden combo box.

    The toggle is projected as the combo box. Its list opens in a separate
    untitled popup window of table cells that has no relation back to the
    toggle; the toggle reports CHECKED while the list is open, and the text
    beside it shows the choice. Selection.select_child on a cell only
    highlights it: activating a cell commits the row under the list cursor,
    and grab_focus on a cell moves the cursor there.
    """

    def _lo_combo(self, value="10 pt", options=("8 pt", "10 pt", "12 pt"),
                  grab_moves_cursor=True, list_only=False):
        app = _Node(role="application", name="soffice")
        frame = _Node(role="frame", name="Untitled 1 - LibreOffice Calc")
        entry = _Node(role="text", states=(
            STATE.SHOWING, STATE.VISIBLE, STATE.EDITABLE))
        entry.text_iface = _Text("" if list_only else value)
        toggle = _Node(role="toggle button")
        frame.add(_Node(role="tool bar").add(_Node(role="filler").add(
            entry, _Node(role="combo box", states=()), toggle)))
        popup = _Node(role="window", states=())
        table = _Node(role="table")
        popup.add(_Node(role="scroll pane").add(table))
        cells = [_Node(role="table cell", name=name) for name in options]
        table.add(*cells)
        app.add(frame, popup)
        state = {"cursor": None, "keys": [], "clicks": 0, "value": value}

        def set_open(opened):
            for node in (toggle, popup):
                node.states.discard(STATE.CHECKED if node is toggle
                                    else STATE.SHOWING)
            for cell in cells:
                cell.states.discard(STATE.FOCUSED)
                cell.states.discard(STATE.SELECTED)
            state["cursor"] = None
            if opened:
                toggle.states.add(STATE.CHECKED)
                popup.states.add(STATE.SHOWING)
                state["cursor"] = options.index(state["value"])
                cells[state["cursor"]].states.update(
                    (STATE.FOCUSED, STATE.SELECTED))

        def click(_index):
            state["clicks"] += 1
            set_open(STATE.CHECKED not in toggle.states)
            return True

        toggle.get_n_actions = lambda: 1
        toggle.get_action_name = lambda _index: "click"
        toggle.do_action = click

        def grab(index):
            if grab_moves_cursor and state["cursor"] is not None:
                cells[state["cursor"]].states.discard(STATE.FOCUSED)
                cells[index].states.add(STATE.FOCUSED)
                state["cursor"] = index
            return True

        def activate(_index):
            if state["cursor"] is not None:
                state["value"] = cells[state["cursor"]].title
                if not list_only:
                    entry.text_iface.text = state["value"]
            set_open(False)
            return True

        for index, cell in enumerate(cells):
            cell.component.grab_focus = lambda index=index: grab(index)
            cell.get_n_actions = lambda: 3
            cell.get_action_name = (
                lambda k: ("expand or contract", "edit", "activate")[k])
            cell.do_action = activate

        def send_key(key):
            state["keys"].append(key)
            if key == "Escape":
                set_open(False)

        patch = mock.patch.object(runtime, "send_key", send_key)
        patch.start()
        self.addCleanup(patch.stop)
        return toggle, entry, cells, state

    def test_choice_is_committed_through_the_list_and_read_back(self):
        toggle, entry, _cells, state = self._lo_combo()
        notes = []

        runtime.perform_select(
            {"text": "12 pt"}, None, toggle, {}, None, notes, {})

        self.assertEqual(entry.text_iface.text, "12 pt")
        self.assertNotIn(STATE.CHECKED, toggle.states)
        self.assertEqual(state["keys"], [])
        self.assertTrue(any("12 pt" in note for note in notes))

    def test_select_on_a_cell_of_the_open_list_commits_its_combo(self):
        toggle, entry, cells, _state = self._lo_combo()
        toggle.do_action(0)

        runtime.perform_select({}, None, cells[2], {}, None, [], {})

        self.assertEqual(entry.text_iface.text, "12 pt")
        self.assertNotIn(STATE.CHECKED, toggle.states)

    def test_highlighted_but_uncommitted_choice_is_not_success(self):
        toggle, entry, _cells, _state = self._lo_combo(grab_moves_cursor=False)

        failure = self.assert_unknown(lambda: runtime.perform_select(
            {"text": "12 pt"}, None, toggle, {}, None, [], {}))

        self.assertEqual(entry.text_iface.text, "10 pt")
        self.assertIn("\"10 pt\"", str(failure))

    def test_unknown_option_closes_the_list_it_opened(self):
        toggle, entry, _cells, _state = self._lo_combo()

        with self.assertRaises(runtime.OperationFailure) as caught:
            runtime.perform_select(
                {"text": "13 pt"}, None, toggle, {}, None, [], {})

        self.assertEqual(caught.exception.failure_code, "invalid_argument")
        self.assertIn("\"12 pt\"", str(caught.exception))
        self.assertNotIn(STATE.CHECKED, toggle.states)
        self.assertEqual(entry.text_iface.text, "10 pt")

    def test_choice_already_shown_needs_no_input(self):
        toggle, _entry, _cells, state = self._lo_combo()

        runtime.perform_select(
            {"text": "10 pt"}, None, toggle, {}, None, [], {})

        self.assertEqual(state["clicks"], 0)

    def test_list_only_choice_is_read_back_from_the_reopened_list(self):
        toggle, _entry, _cells, state = self._lo_combo(list_only=True)

        runtime.perform_select(
            {"text": "12 pt"}, None, toggle, {}, None, [], {})

        self.assertEqual(state["value"], "12 pt")
        self.assertNotIn(STATE.CHECKED, toggle.states)
        self.assertEqual(state["clicks"], 3)

    def test_list_only_uncommitted_choice_is_not_success(self):
        toggle, _entry, _cells, state = self._lo_combo(
            list_only=True, grab_moves_cursor=False)

        failure = self.assert_unknown(lambda: runtime.perform_select(
            {"text": "12 pt"}, None, toggle, {}, None, [], {}))

        self.assertEqual(state["value"], "10 pt")
        self.assertIn("\"10 pt\"", str(failure))
        self.assertNotIn(STATE.CHECKED, toggle.states)
