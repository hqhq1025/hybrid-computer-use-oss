#!/usr/bin/env python3
"""Linux provider semantic target and continuity resolver tests."""

from runtime_test_support import *  # noqa: F401,F403

class SemanticRuntimeResolutionTests(AtspiPatchedTestCase):
    def setUp(self):
        super().setUp()
        self.nodes_by_path = {}
        self.windows = []
        self._render = mock.patch.object(
            runtime,
            "render_tree",
            side_effect=self._render_tree,
        )
        self._render.start()
        self.addCleanup(self._render.stop)
        self._resolve = mock.patch.object(
            runtime,
            "find_element_at_snapshot_path_with_bounds",
            side_effect=lambda _app, record, _bounds: self.nodes_by_path.get(
                tuple(record.get("runtimeId") or [])),
        )
        self._resolve.start()
        self.addCleanup(self._resolve.stop)
        self._windows = mock.patch.object(
            runtime,
            "app_windows",
            side_effect=lambda _app: self.windows,
        )
        self._windows.start()
        self.addCleanup(self._windows.stop)
        self._extents = mock.patch.object(
            runtime,
            "extents",
            return_value=runtime.frame(0, 0, 100, 100),
        )
        self._extents.start()
        self.addCleanup(self._extents.stop)

    @staticmethod
    def _record_for(node, index, path):
        return {
            "index": index,
            "runtimeId": list(path),
            "automationId": "",
            "name": runtime.limit_text(
                runtime.effective_name(node),
                text_limit=runtime.DEFAULT_TEXT_LIMIT,
            ),
            "controlType": runtime.node_role(node),
            "localizedControlType": runtime.node_role(node),
            "className": "",
            "value": "",
            "nativeWindowHandle": 0,
            "frame": None,
            "actions": list(getattr(node, "_actions", [])),
            "states": str(getattr(node, "_states_text", "")),
            "description": str(getattr(node, "_description", "")),
            "placeholder": str(getattr(node, "_placeholder", "")),
            "textAttributes": None,
        }

    def _render_tree(self, root, _bounds, root_path, **kwargs):
        records = []
        indexer = kwargs.get("indexer")

        def visit(node, path):
            index = (
                indexer.index_for(
                    path, runtime.node_role(node), runtime.effective_name(node))
                if indexer is not None
                else len(records)
            )
            record = self._record_for(node, index, path)
            records.append(record)
            self.nodes_by_path[tuple(path)] = node
            for child_index, child in enumerate(node.children):
                visit(child, path + [child_index])

        visit(root, list(root_path))
        return records, []

    @staticmethod
    def _node(
        name, role, description="", actions=(), states="", atspi_states=()
    ):
        node = _NamedNode(name=name, role=role, states=atspi_states)
        node._description = description
        node._actions = list(actions)
        node._states_text = states
        return node

    @staticmethod
    def _provider(node, path, bus=":1.42", pid=4242):
        node.path = path
        node.app = type("ProviderApp", (), {"bus_name": bus})()
        node.get_process_id = lambda: pid
        return node

    def test_unique_target_resolves_with_current_path_and_stable_index(self):
        save = self._node("Save", "push button", actions=("click",))
        window = self._node("Editor", "frame")
        window.children = [save]
        self.windows = [(3, window)]
        target = {
            "selector": 'push button "Save" [a=click]',
            "role": "push button",
            "name": "Save",
            "hasName": True,
            "predicates": [{"kind": "action", "value": "click"}],
        }

        node, record, note, matched_window, _matched_bounds = (
            runtime.resolve_semantic_target(
            None,
            3,
            window,
            runtime.frame(0, 0, 100, 100),
            target,
            {
                "3.0": {
                    "index": 17,
                    "role": "push button",
                    "name": "Save",
                }
            },
            {},
        ))

        self.assertIs(node, save)
        self.assertIs(matched_window, window)
        self.assertEqual(record["runtimeId"], [3, 0])
        self.assertEqual(record["index"], 17)
        self.assertIn("fresh=true unique=true", note)

    def test_live_action_resolution_never_uses_working_tree_pruning(self):
        save = self._node("Save", "push button", actions=("click",))
        window = self._node("Editor", "frame")
        window.children = [save]
        self.windows = [(0, window)]

        def canonical_only(root, _bounds, root_path, **kwargs):
            self.assertFalse(kwargs.get("prune"))
            record = self._record_for(save, 7, list(root_path) + [0])
            self.nodes_by_path[tuple(record["runtimeId"])] = save
            return [record], []

        with mock.patch.object(
            runtime, "render_tree", side_effect=canonical_only
        ):
            node, _record, _note, _window, _bounds = (
                runtime.resolve_semantic_target(
                    None,
                    0,
                    window,
                    runtime.frame(0, 0, 100, 100),
                    {
                        "selector": 'push button "Save" [a=click]',
                        "role": "push button",
                        "name": "Save",
                        "hasName": True,
                        "predicates": [{
                            "kind": "action",
                            "value": "click",
                        }],
                    },
                    None,
                    {},
                )
            )
        self.assertIs(node, save)

    def test_name_match_outranks_description_fallback(self):
        by_description = self._node(
            "", "push button", description="Save")
        by_name = self._node("Save", "push button")
        window = self._node("Editor", "frame")
        window.children = [by_description, by_name]
        self.windows = [(0, window)]

        node, _record, _note, _window, _bounds = runtime.resolve_semantic_target(
            None,
            0,
            window,
            runtime.frame(0, 0, 100, 100),
            {
                "selector": 'push button "Save"',
                "role": "push button",
                "name": "Save",
                "hasName": True,
            },
            None,
            {},
        )

        self.assertIs(node, by_name)

    def test_working_tree_folded_name_resolves_live_cr_lf_and_tab(self):
        save = self._node("Save\r\n\tAs", "push button")
        window = self._node("Editor", "frame")
        window.children = [save]
        self.windows = [(0, window)]

        node, _record, _note, _window, _bounds = (
            runtime.resolve_semantic_target(
                None,
                0,
                window,
                runtime.frame(0, 0, 100, 100),
                {
                    "selector": 'push button "Save As"',
                    "role": "push button",
                    "name": "Save As",
                    "hasName": True,
                },
                None,
                {},
            )
        )

        self.assertIs(node, save)

    def test_working_tree_folded_name_resolves_live_description_fallback(self):
        operations = self._node(
            "",
            "push button",
            description="Show\r\n\toperations",
        )
        window = self._node("Editor", "frame")
        window.children = [operations]
        self.windows = [(0, window)]

        node, _record, _note, _window, _bounds = (
            runtime.resolve_semantic_target(
                None,
                0,
                window,
                runtime.frame(0, 0, 100, 100),
                {
                    "selector": 'push button "Show operations"',
                    "role": "push button",
                    "name": "Show operations",
                    "hasName": True,
                },
                None,
                {},
            )
        )

        self.assertIs(node, operations)

    def test_names_differing_only_by_folded_whitespace_are_ambiguous(self):
        window = self._node("Editor", "frame")
        window.children = [
            self._node("Open\r\nFile", "push button"),
            self._node("Open\tFile", "push button"),
        ]
        self.windows = [(0, window)]

        with self.assertRaises(runtime.OperationFailure) as raised:
            runtime.resolve_semantic_target(
                None,
                0,
                window,
                runtime.frame(0, 0, 100, 100),
                {
                    "selector": 'push button "Open File"',
                    "role": "push button",
                    "name": "Open File",
                    "hasName": True,
                },
                None,
                {},
            )

        self.assertEqual(
            raised.exception.failure_code, "semantic_target_ambiguous")
        self.assertFalse(raised.exception.input_events_emitted)

    def test_description_and_placeholder_predicates_fold_whitespace(self):
        search = self._node(
            "Search",
            "entry",
            description="Search\r\n\tcurrent document",
        )
        search._placeholder = "Type\r\n\tquery"
        window = self._node("Editor", "frame")
        window.children = [search]
        self.windows = [(0, window)]

        node, _record, _note, _window, _bounds = (
            runtime.resolve_semantic_target(
                None,
                0,
                window,
                runtime.frame(0, 0, 100, 100),
                {
                    "selector": (
                        'entry "Search" [desc="Search current document"] '
                        '[placeholder="Type query"]'
                    ),
                    "role": "entry",
                    "name": "Search",
                    "hasName": True,
                    "predicates": [
                        {
                            "kind": "desc",
                            "value": "Search current document",
                        },
                        {
                            "kind": "placeholder",
                            "value": "Type query",
                        },
                    ],
                },
                None,
                {},
            )
        )

        self.assertIs(node, search)

    def test_missing_target_fails_before_input(self):
        window = self._node("Editor", "frame")
        self.windows = [(0, window)]
        with self.assertRaises(runtime.OperationFailure) as raised:
            runtime.resolve_semantic_target(
                None,
                0,
                window,
                runtime.frame(0, 0, 100, 100),
                {
                    "selector": 'push button "Save"',
                    "role": "push button",
                    "name": "Save",
                    "hasName": True,
                },
                None,
                {},
            )

        self.assertEqual(
            raised.exception.failure_code, "semantic_target_not_found")
        self.assertEqual(raised.exception.failure_phase, "precondition")
        self.assertFalse(raised.exception.input_events_emitted)

    def test_any_provider_omission_prevents_not_found_claim(self):
        window = self._node("Editor", "frame")
        self.windows = [(0, window)]
        with mock.patch.object(
            runtime,
            "render_tree",
            return_value=(
                [],
                [],
                [{
                    "kind": "provider_omission",
                    "reason": "visible_cell_budget",
                    "count": 1,
                }],
            ),
        ):
            with self.assertRaises(runtime.OperationFailure) as raised:
                runtime.resolve_semantic_target(
                    None,
                    0,
                    window,
                    runtime.frame(0, 0, 100, 100),
                    {
                        "selector": 'push button "Save" [a=click]',
                        "role": "push button",
                        "name": "Save",
                        "hasName": True,
                        "predicates": [{
                            "kind": "action",
                            "value": "click",
                        }],
                    },
                    None,
                    {},
                )

        self.assertEqual(
            raised.exception.failure_code, "observation_incomplete"
        )
        self.assertFalse(raised.exception.input_events_emitted)

    def test_semantic_ref_does_not_recover_after_provider_window_churn(self):
        save = self._node("Save", "push button", actions=("click",))
        replacement = self._node("Preferences", "dialog")
        replacement.children = [save]
        self.windows = [(1, replacement)]
        target = {
            "selector": 'push button "Save"',
            "locatorKind": "semantic",
            "windowHandle": "w2:stable-window",
            "role": "push button",
            "name": "Save",
            "hasName": True,
            "providerBusName": ":1.42",
            "providerObjectPath": "/old/window",
            "processGeneration": "99123",
            "providerWindowBusName": ":1.42",
            "providerWindowObjectPath": "/old/window",
            "providerWindowProcessGeneration": "99123",
        }
        with mock.patch.object(
            runtime, "provider_bus_name", return_value=":1.42"
        ), mock.patch.object(
            runtime, "provider_object_path", return_value="/new/window"
        ), mock.patch.object(
            runtime, "node_pid", return_value=4242
        ), mock.patch.object(
            runtime, "process_generation", return_value="99123"
        ), mock.patch.object(
            runtime,
            "window_instance_handle",
            return_value="w2:stable-window",
        ):
            with self.assertRaises(runtime.OperationFailure) as raised:
                runtime.resolve_semantic_target(
                    None,
                    1,
                    replacement,
                    runtime.frame(0, 0, 100, 100),
                    target,
                    None,
                    {},
                )
        self.assertEqual(
            raised.exception.failure_code, "observation_incomplete")
        self.assertFalse(raised.exception.input_events_emitted)

    def test_exact_provider_element_survives_top_level_wrapper_rebuild(self):
        save = self._provider(
            self._node("Save", "push button", actions=("click",)),
            "/element/save",
        )
        replacement = self._provider(
            self._node("Editor", "frame"),
            "/window/rebuilt",
        )
        replacement.children = [save]
        self.windows = [(1, replacement)]
        target = {
            "selector": 'push button "Save"',
            "locatorKind": "semantic",
            "windowHandle": "w2:stable-window",
            "role": "push button",
            "name": "Save",
            "hasName": True,
            "providerBusName": ":1.42",
            "providerObjectPath": "/element/save",
            "processGeneration": "99123",
            "providerWindowBusName": ":1.17",
            "providerWindowObjectPath": "/window/old",
            "providerWindowProcessGeneration": "99123",
        }
        with mock.patch.object(
            runtime, "process_generation", return_value="99123"
        ), mock.patch.object(
            runtime,
            "window_instance_handle",
            return_value="w2:stable-window",
        ), mock.patch.object(
            runtime,
            "record_for",
            side_effect=lambda node, index, path, _bounds, **_kwargs: (
                self._record_for(node, index, path)
            ),
        ):
            node, _record, _note, matched_window, _bounds = (
                runtime.resolve_semantic_target(
                    None,
                    1,
                    replacement,
                    runtime.frame(0, 0, 100, 100),
                    target,
                    None,
                    {},
                )
            )

        self.assertIs(node, save)
        self.assertIs(matched_window, replacement)

    def test_exact_provider_element_recovers_without_strong_window_handle(self):
        save = self._provider(
            self._node("Save", "push button", actions=("click",)),
            "/element/save",
        )
        replacement = self._provider(
            self._node("Editor", "dialog"),
            "/window/rebuilt",
        )
        replacement.children = [save]
        self.windows = [(4, replacement)]
        target = {
            "selector": 'push button "Save"',
            "locatorKind": "semantic",
            "windowHandle": "pid4242/win1/dialog",
            "role": "push button",
            "name": "Save",
            "hasName": True,
            "providerBusName": ":1.42",
            "providerObjectPath": "/element/save",
            "processGeneration": "99123",
            "providerWindowBusName": ":1.42",
            "providerWindowObjectPath": "/window/old",
            "providerWindowProcessGeneration": "99123",
        }
        original_scan = runtime.scan_provider_identity
        with mock.patch.object(
            runtime,
            "scan_provider_identity",
            wraps=original_scan,
        ) as provider_scan, mock.patch.object(
            runtime, "process_generation", return_value="99123"
        ), mock.patch.object(
            runtime,
            "window_instance_handle",
            return_value="pid4242/win4/dialog",
        ), mock.patch.object(
            runtime,
            "record_for",
            side_effect=lambda node, index, path, _bounds, **_kwargs: (
                self._record_for(node, index, path)
            ),
        ):
            node, _record, _note, matched_window, _bounds = (
                runtime.resolve_semantic_target(
                    None,
                    4,
                    replacement,
                    runtime.frame(0, 0, 100, 100),
                    target,
                    None,
                    {},
                )
            )

        self.assertIs(node, save)
        self.assertIs(matched_window, replacement)
        self.assertEqual(provider_scan.call_count, 1)

    def test_provider_window_churn_does_not_rebind_across_windows(self):
        save = self._provider(
            self._node("Save", "push button", actions=("click",)),
            "/new/save",
        )
        sibling = self._provider(
            self._node("Other", "dialog"),
            "/new/window",
        )
        sibling.children = [save]
        self.windows = [(1, sibling)]
        target = {
            "selector": 'push button "Save"',
            "locatorKind": "semantic",
            "windowHandle": "w2:origin",
            "role": "push button",
            "name": "Save",
            "hasName": True,
            "providerBusName": ":1.42",
            "providerObjectPath": "/old/save",
            "processGeneration": "99123",
            "providerWindowBusName": ":1.42",
            "providerWindowObjectPath": "/old/window",
            "providerWindowProcessGeneration": "99123",
        }
        with mock.patch.object(
            runtime, "provider_bus_name", return_value=":1.42"
        ), mock.patch.object(
            runtime, "provider_object_path", return_value="/new/window"
        ), mock.patch.object(
            runtime, "node_pid", return_value=4242
        ), mock.patch.object(
            runtime, "process_generation", return_value="99123"
        ), mock.patch.object(
            runtime, "window_instance_handle", return_value="w2:sibling"
        ):
            with self.assertRaises(runtime.OperationFailure) as raised:
                runtime.resolve_semantic_target(
                    None,
                    1,
                    sibling,
                    runtime.frame(0, 0, 100, 100),
                    target,
                    None,
                    {},
                )
        self.assertEqual(
            raised.exception.failure_code, "stale_element_ref")
        self.assertFalse(raised.exception.input_events_emitted)

    def test_weak_window_handle_does_not_enable_semantic_rebinding(self):
        save = self._provider(
            self._node("Save", "push button", actions=("click",)),
            "/new/save",
        )
        replacement = self._provider(
            self._node("Preferences", "dialog"),
            "/new/window",
        )
        replacement.children = [save]
        self.windows = [(4, replacement)]
        target = {
            "selector": 'push button "Save"',
            "locatorKind": "semantic",
            "windowHandle": "pid4242/win1/dialog",
            "role": "push button",
            "name": "Save",
            "hasName": True,
            "providerBusName": ":1.42",
            "providerObjectPath": "/old/save",
            "processGeneration": "99123",
            "providerWindowBusName": ":1.42",
            "providerWindowObjectPath": "/old/window",
            "providerWindowProcessGeneration": "99123",
            "providerWindowRole": "dialog",
            "providerWindowName": "Preferences",
        }
        with mock.patch.object(
            runtime, "provider_bus_name", return_value=":1.42"
        ), mock.patch.object(
            runtime, "provider_object_path", return_value="/new/window"
        ), mock.patch.object(
            runtime, "node_pid", return_value=4242
        ), mock.patch.object(
            runtime, "process_generation", return_value="99123"
        ), mock.patch.object(
            runtime,
            "window_instance_handle",
            return_value="pid4242/win4/dialog",
        ):
            with self.assertRaises(runtime.OperationFailure) as raised:
                runtime.resolve_semantic_target(
                    None,
                    4,
                    replacement,
                    runtime.frame(0, 0, 100, 100),
                    target,
                    None,
                    {},
                )
        self.assertEqual(
            raised.exception.failure_code, "stale_element_ref")
        self.assertFalse(raised.exception.input_events_emitted)

    def test_weak_window_semantics_fail_closed_when_ambiguous(self):
        first = self._provider(
            self._node("Preferences", "dialog"), "/new/window/first"
        )
        second = self._provider(
            self._node("Preferences", "dialog"), "/new/window/second"
        )
        first.children = [
            self._provider(
                self._node("Save", "push button"), "/new/save/first"
            )
        ]
        second.children = [
            self._provider(
                self._node("Save", "push button"), "/new/save/second"
            )
        ]
        self.windows = [(4, first), (7, second)]
        target = {
            "selector": 'push button "Save"',
            "windowHandle": "pid4242/win1/dialog",
            "role": "push button",
            "name": "Save",
            "hasName": True,
            "providerBusName": ":1.42",
            "providerObjectPath": "/old/save",
            "processGeneration": "99123",
            "providerWindowBusName": ":1.42",
            "providerWindowObjectPath": "/old/window",
            "providerWindowProcessGeneration": "99123",
            "providerWindowRole": "dialog",
            "providerWindowName": "Preferences",
        }
        with mock.patch.object(
            runtime, "provider_bus_name", return_value=":1.42"
        ), mock.patch.object(
            runtime, "provider_object_path", return_value="/new/window"
        ), mock.patch.object(
            runtime, "node_pid", return_value=4242
        ), mock.patch.object(
            runtime, "process_generation", return_value="99123"
        ), mock.patch.object(
            runtime,
            "window_instance_handle",
            side_effect=lambda _app, index, _window: (
                "pid4242/win{}/dialog".format(index)
            ),
        ):
            with self.assertRaises(runtime.OperationFailure) as raised:
                runtime.resolve_semantic_target(
                    None,
                    4,
                    first,
                    runtime.frame(0, 0, 100, 100),
                    target,
                    None,
                    {},
                )
        self.assertEqual(
            raised.exception.failure_code, "stale_element_ref")
        self.assertFalse(raised.exception.input_events_emitted)

    def test_ambiguous_target_fails_before_input(self):
        window = self._node("Editor", "frame")
        window.children = [
            self._node("OK", "push button"),
            self._node("OK", "push button"),
        ]
        self.windows = [(0, window)]
        with self.assertRaises(runtime.OperationFailure) as raised:
            runtime.resolve_semantic_target(
                None,
                0,
                window,
                runtime.frame(0, 0, 100, 100),
                {
                    "selector": 'push button "OK"',
                    "role": "push button",
                    "name": "OK",
                    "hasName": True,
                },
                None,
                {},
            )

        self.assertEqual(
            raised.exception.failure_code, "semantic_target_ambiguous")
        self.assertEqual(raised.exception.failure_phase, "precondition")
        self.assertFalse(raised.exception.input_events_emitted)

    def test_provider_bound_ref_never_falls_back_to_same_name_peer(self):
        replacement = self._provider(
            self._node("Format", "menu"), "/new/format"
        )
        window = self._provider(
            self._node("Editor", "frame"), "/window"
        )
        window.children = [replacement]
        self.windows = [(0, window)]

        with mock.patch.object(
            runtime, "process_generation", return_value="99123"
        ), mock.patch.object(runtime, "record_for") as record_for:
            with self.assertRaises(runtime.OperationFailure) as raised:
                runtime.resolve_semantic_target(
                    None,
                    0,
                    window,
                    runtime.frame(0, 0, 100, 100),
                    {
                        "selector": 'menu "Format"',
                        "locatorKind": "provider_bound",
                        "role": "menu",
                        "name": "Format",
                        "hasName": True,
                        "providerBusName": ":1.42",
                        "providerObjectPath": "/old/format",
                        "processGeneration": "99123",
                    },
                    None,
                    {},
                )

        self.assertEqual(
            raised.exception.failure_code,
            "stale_element_ref",
        )
        self.assertFalse(raised.exception.input_events_emitted)

    def test_ordinary_semantic_ref_does_not_bind_replacement_provider(self):
        old_path = "/old/ok"
        replacement = self._provider(
            self._node("OK", "push button"), "/new/ok"
        )
        window = self._provider(
            self._node("Question", "dialog"), "/window"
        )
        window.children = [replacement]
        self.windows = [(0, window)]
        target = {
            "selector": 'push button "OK"',
            "locatorKind": "semantic",
            "role": "push button",
            "name": "OK",
            "hasName": True,
            "providerBusName": ":1.42",
            "providerObjectPath": old_path,
            "processGeneration": "99123",
        }
        with mock.patch.object(
            runtime, "process_generation", return_value="99123"
        ):
            with self.assertRaises(runtime.OperationFailure) as raised:
                runtime.resolve_semantic_target(
                    None,
                    0,
                    window,
                    runtime.frame(0, 0, 100, 100),
                    target,
                    None,
                    {},
                )
        self.assertEqual(
            raised.exception.failure_code, "stale_element_ref"
        )
        self.assertIn(
            "provider_identity_replaced", str(raised.exception)
        )
        self.assertFalse(raised.exception.input_events_emitted)

    def test_current_revision_semantic_ref_rebinds_same_path_in_strong_window(self):
        replacement = self._provider(
            self._node("Save", "push button", actions=("click",)),
            "/new/save",
        )
        window = self._provider(
            self._node("Editor", "frame"), "/new/window"
        )
        window.children = [replacement]
        self.windows = [(0, window)]
        target = {
            "selector": 'push button "Save"',
            "locatorKind": "semantic",
            "allowProviderRebind": True,
            "windowHandle": "w2:stable-window",
            "role": "push button",
            "name": "Save",
            "hasName": True,
            "providerBusName": ":1.42",
            "providerObjectPath": "/old/save",
            "processGeneration": "99123",
            "runtimeId": [0, 0],
        }
        with mock.patch.object(
            runtime, "process_generation", return_value="99123"
        ), mock.patch.object(
            runtime,
            "window_instance_handle",
            return_value="w2:stable-window",
        ):
            node, record, _note, matched_window, _bounds = (
                runtime.resolve_semantic_target(
                    None,
                    0,
                    window,
                    runtime.frame(0, 0, 100, 100),
                    target,
                    None,
                    {},
                )
            )
        self.assertIs(node, replacement)
        self.assertIs(matched_window, window)
        self.assertEqual(record["runtimeId"], [0, 0])
        self.assertEqual(record["providerObjectPath"], "/new/save")

    def test_current_revision_semantic_ref_rejects_changed_runtime_path(self):
        replacement = self._provider(
            self._node("Save", "push button", actions=("click",)),
            "/new/save",
        )
        window = self._provider(
            self._node("Editor", "frame"), "/new/window"
        )
        window.children = [
            self._provider(self._node("Spacer", "panel"), "/spacer"),
            replacement,
        ]
        self.windows = [(0, window)]
        target = {
            "selector": 'push button "Save"',
            "locatorKind": "semantic",
            "allowProviderRebind": True,
            "windowHandle": "w2:stable-window",
            "role": "push button",
            "name": "Save",
            "hasName": True,
            "providerBusName": ":1.42",
            "providerObjectPath": "/old/save",
            "processGeneration": "99123",
            "runtimeId": [0, 0],
        }
        with mock.patch.object(
            runtime, "process_generation", return_value="99123"
        ), mock.patch.object(
            runtime,
            "window_instance_handle",
            return_value="w2:stable-window",
        ):
            with self.assertRaises(runtime.OperationFailure) as caught:
                runtime.resolve_semantic_target(
                    None,
                    0,
                    window,
                    runtime.frame(0, 0, 100, 100),
                    target,
                    None,
                    {},
                )
        self.assertEqual(caught.exception.failure_code, "stale_element_ref")
        self.assertFalse(caught.exception.input_events_emitted)

    def test_live_semantic_resolution_rejects_same_path_replacement(self):
        replacement = self._provider(
            self._node("OK", "push button", actions=("click",)), "/new/ok"
        )
        window = self._provider(
            self._node("Question", "dialog"), "/window"
        )
        window.children = [replacement]
        app = self._node("Editor", "application")
        app.children = [window]
        self.windows = [(0, window)]
        target = {
            "selector": 'push button "OK"',
            "locatorKind": "semantic",
            "windowHandle": "w2:question",
            "role": "push button",
            "name": "OK",
            "hasName": True,
            "providerBusName": ":1.42",
            "providerObjectPath": "/old/ok",
            "processGeneration": "99123",
            "runtimeId": [0, 0],
        }

        with mock.patch.object(
            runtime, "app_windows", return_value=[(0, window)]
        ), mock.patch.object(
            runtime, "provider_bus_name", return_value=":1.42"
        ), mock.patch.object(
            runtime, "provider_object_path",
            side_effect=lambda node: node.path,
        ), mock.patch.object(
            runtime, "node_pid", return_value=4242
        ), mock.patch.object(
            runtime, "process_generation", return_value="99123"
        ), mock.patch.object(
            runtime, "window_instance_handle", return_value="w2:question"
        ):
            with self.assertRaises(runtime.OperationFailure) as raised:
                runtime.resolve_semantic_target(
                    app,
                    0,
                    window,
                    runtime.frame(0, 0, 100, 100),
                    target,
                    None,
                    {},
                )
        self.assertEqual(raised.exception.failure_code, "stale_element_ref")
        self.assertIn("provider_identity_replaced", str(raised.exception))
        self.assertFalse(raised.exception.input_events_emitted)

    def test_semantic_scan_does_not_render_full_records_for_nonmatches(self):
        target = self._node("Save", "push button")
        window = self._node("Editor", "frame")
        window.children = [
            self._node("Paragraph {}".format(index), "paragraph")
            for index in range(250)
        ] + [target]
        self.windows = [(0, window)]

        with mock.patch.object(
            runtime,
            "render_tree",
            side_effect=AssertionError("semantic fallback used full render"),
        ), mock.patch.object(
            runtime,
            "record_for",
            side_effect=AssertionError("semantic fallback built full records"),
        ) as record_for:
            node, _record, _note, _window, _bounds = (
                runtime.resolve_semantic_target(
                    None,
                    0,
                    window,
                    runtime.frame(0, 0, 100, 100),
                    {
                        "selector": 'push button "Save"',
                        "role": "push button",
                        "name": "Save",
                        "hasName": True,
                    },
                    None,
                    {},
                )
            )
        self.assertIs(node, target)
        record_for.assert_not_called()

    def test_provider_bound_identity_change_never_reuses_old_ref(self):
        replacement = self._provider(
            self._node("Saved", "toggle button"), "/same/control"
        )
        window = self._provider(
            self._node("Editor", "frame"), "/window"
        )
        window.children = [replacement]
        self.windows = [(0, window)]

        with mock.patch.object(
            runtime, "process_generation", return_value="99123"
        ), mock.patch.object(
            runtime,
            "record_for",
            side_effect=lambda node, index, path, bounds, text_limit: {
                **self._record_for(node, index, path),
                "providerBusName": ":1.42",
                "providerObjectPath": node.path,
                "processGeneration": "99123",
                "showing": True,
                "visible": True,
            },
        ), mock.patch.object(
            runtime, "render_tree"
        ) as render_tree:
            with self.assertRaises(runtime.OperationFailure) as raised:
                runtime.resolve_semantic_target(
                    None,
                    0,
                    window,
                    runtime.frame(0, 0, 100, 100),
                    {
                        "selector": 'push button "Save"',
                        "locatorKind": "provider_bound",
                        "role": "push button",
                        "name": "Save",
                        "hasName": True,
                        "providerBusName": ":1.42",
                        "providerObjectPath": "/same/control",
                        "processGeneration": "99123",
                    },
                    None,
                    {},
                )

        self.assertEqual(
            raised.exception.failure_code, "stale_element_ref"
        )
        self.assertIn("identity_changed", str(raised.exception))
        self.assertNotIn("/same/control", str(raised.exception))
        render_tree.assert_not_called()

    def test_provider_bound_record_read_failure_is_incomplete(self):
        save = self._provider(
            self._node("Save", "push button"), "/same/control"
        )
        window = self._provider(
            self._node("Editor", "frame"), "/window"
        )
        window.children = [save]
        self.windows = [(0, window)]

        with mock.patch.object(
            runtime, "process_generation", return_value="99123"
        ), mock.patch.object(
            runtime, "record_for", side_effect=RuntimeError("transient")
        ), mock.patch.object(runtime, "render_tree") as render_tree:
            with self.assertRaises(runtime.OperationFailure) as raised:
                runtime.resolve_semantic_target(
                    None,
                    0,
                    window,
                    runtime.frame(0, 0, 100, 100),
                    {
                        "selector": 'push button "Save"',
                        "locatorKind": "provider_bound",
                        "role": "push button",
                        "name": "Save",
                        "hasName": True,
                        "providerBusName": ":1.42",
                        "providerObjectPath": "/same/control",
                        "processGeneration": "99123",
                    },
                    None,
                    {},
                )

        self.assertEqual(
            raised.exception.failure_code, "observation_incomplete"
        )
        render_tree.assert_not_called()

    def test_provider_bound_match_survives_unrelated_incomplete_branch(self):
        target_node = self._provider(
            self._node("Format", "menu", actions=("click",)),
            "/target/format",
        )
        broken = self._provider(
            self._node("Broken", "group"),
            "/broken",
        )
        window = self._provider(
            self._node("Editor", "frame"),
            "/window",
        )

        def enumerate_children(node, _context):
            if node is window:
                return runtime.InventoryResult(
                    [(0, target_node), (1, broken)],
                    complete=True,
                )
            if node is broken:
                return runtime.InventoryResult(
                    [],
                    complete=False,
                    errors=["unrelated branch failed"],
                )
            return runtime.InventoryResult([], complete=True)

        with mock.patch.object(
            runtime, "process_generation", return_value="99123"
        ), mock.patch.object(
            runtime, "enumerate_children", side_effect=enumerate_children
        ), mock.patch.object(
            runtime,
            "record_for",
            side_effect=lambda node, index, path, bounds, text_limit: {
                **self._record_for(node, index, path),
                "providerBusName": ":1.42",
                "providerObjectPath": node.path,
                "processGeneration": "99123",
                "showing": True,
                "visible": True,
            },
        ):
            resolved, record, _note, _window, _bounds = (
                runtime.resolve_semantic_target(
                    None,
                    0,
                    window,
                    runtime.frame(0, 0, 100, 100),
                    {
                        "selector": 'menu "Format"',
                        "locatorKind": "provider_bound",
                        "role": "menu",
                        "name": "Format",
                        "hasName": True,
                        "providerBusName": ":1.42",
                        "providerObjectPath": "/target/format",
                        "processGeneration": "99123",
                    },
                    None,
                    {},
                )
            )

        self.assertIs(resolved, target_node)
        self.assertEqual(record["name"], "Format")

    def test_provider_bound_ref_uses_verified_runtime_path_before_tree_scan(self):
        target_node = self._provider(
            self._node("Format", "menu", actions=("click",)),
            "/target/format",
        )
        window = self._provider(
            self._node("Editor", "frame"),
            "/window",
        )
        window.children = [target_node]
        app = self._node("Office", "application")
        app.children = [window]
        self.windows = [(0, window)]

        with mock.patch.object(
            runtime, "process_generation", return_value="99123"
        ), mock.patch.object(
            runtime,
            "scan_provider_identity",
            side_effect=AssertionError("verified path should avoid a tree scan"),
        ), mock.patch.object(
            runtime,
            "record_for",
            side_effect=lambda node, index, path, bounds, text_limit: {
                **self._record_for(node, index, path),
                "providerBusName": ":1.42",
                "providerObjectPath": node.path,
                "processGeneration": "99123",
                "showing": True,
                "visible": True,
            },
        ):
            resolved, record, _note, _window, _bounds = (
                runtime.resolve_semantic_target(
                    app,
                    0,
                    window,
                    runtime.frame(0, 0, 100, 100),
                    {
                        "selector": 'menu "Format"',
                        "locatorKind": "provider_bound",
                        "role": "menu",
                        "name": "Format",
                        "hasName": True,
                        "providerBusName": ":1.42",
                        "providerObjectPath": "/target/format",
                        "processGeneration": "99123",
                        "runtimeId": [0, 0],
                    },
                    None,
                    {},
                )
            )

        self.assertIs(resolved, target_node)
        self.assertEqual(record["name"], "Format")

    def test_description_only_exact_provider_identity_resolves(self):
        description = "Open the contents of your desktop in a folder"
        target_node = self._provider(
            self._node(
                "",
                "list item",
                description=description,
                actions=("click",),
            ),
            "/places/desktop",
        )
        window = self._provider(
            self._node("Insert Image", "dialog"),
            "/window",
        )
        window.children = [target_node]
        app = self._node("Office", "application")
        app.children = [window]
        self.windows = [(0, window)]

        with mock.patch.object(
            runtime, "process_generation", return_value="99123"
        ), mock.patch.object(
            runtime,
            "scan_provider_identity",
            side_effect=AssertionError("verified path should avoid a tree scan"),
        ), mock.patch.object(
            runtime,
            "record_for",
            side_effect=lambda node, index, path, bounds, text_limit: {
                **self._record_for(node, index, path),
                "providerBusName": ":1.42",
                "providerObjectPath": node.path,
                "processGeneration": "99123",
                "showing": True,
                "visible": True,
            },
        ):
            resolved, record, _note, _window, _bounds = (
                runtime.resolve_semantic_target(
                    app,
                    0,
                    window,
                    runtime.frame(0, 0, 100, 100),
                    {
                        "selector": (
                            'list item [desc="Open the contents of your '
                            'desktop in a folder"]'
                        ),
                        "locatorKind": "semantic",
                        "role": "list item",
                        "hasName": False,
                        "predicates": [{
                            "kind": "desc",
                            "value": description,
                        }],
                        "providerBusName": ":1.42",
                        "providerObjectPath": "/places/desktop",
                        "processGeneration": "99123",
                        "runtimeId": [0, 0],
                    },
                    None,
                    {},
                )
            )

        self.assertIs(resolved, target_node)
        self.assertEqual(record["name"], "")
        self.assertEqual(record["description"], description)

    def test_description_only_provider_identity_rejects_changed_description(self):
        target_node = self._provider(
            self._node(
                "",
                "list item",
                description="Open a different folder",
                actions=("click",),
            ),
            "/places/desktop",
        )
        window = self._provider(
            self._node("Insert Image", "dialog"),
            "/window",
        )
        window.children = [target_node]
        app = self._node("Office", "application")
        app.children = [window]
        self.windows = [(0, window)]

        with mock.patch.object(
            runtime, "process_generation", return_value="99123"
        ), mock.patch.object(
            runtime,
            "record_for",
            side_effect=lambda node, index, path, bounds, text_limit: {
                **self._record_for(node, index, path),
                "providerBusName": ":1.42",
                "providerObjectPath": node.path,
                "processGeneration": "99123",
                "showing": True,
                "visible": True,
            },
        ):
            with self.assertRaises(runtime.OperationFailure) as raised:
                runtime.resolve_semantic_target(
                    app,
                    0,
                    window,
                    runtime.frame(0, 0, 100, 100),
                    {
                        "selector": (
                            'list item [desc="Open the contents of your '
                            'desktop in a folder"]'
                        ),
                        "locatorKind": "semantic",
                        "role": "list item",
                        "hasName": False,
                        "predicates": [{
                            "kind": "desc",
                            "value": (
                                "Open the contents of your desktop in a folder"
                            ),
                        }],
                        "providerBusName": ":1.42",
                        "providerObjectPath": "/places/desktop",
                        "processGeneration": "99123",
                        "runtimeId": [0, 0],
                    },
                    None,
                    {},
                )

        self.assertEqual(
            raised.exception.failure_code, "stale_element_ref"
        )
        self.assertFalse(raised.exception.input_events_emitted)

    def test_provider_bound_virtual_cell_uses_table_runtime_path(self):
        cell = self._provider(
            self._node("C2", "table cell", actions=("click",)),
            "/sheet/cell/c2",
        )
        table = self._provider(
            self._node("Sheet", "table"),
            "/sheet/table",
        )
        window = self._provider(
            self._node("Workbook", "frame"),
            "/window",
        )
        window.children = [table]
        app = self._node("Office", "application")
        app.children = [window]
        table.get_table_iface = lambda: object()
        self.windows = [(0, window)]

        table_api = mock.Mock()
        table_api.get_accessible_at.return_value = cell
        with mock.patch.object(
            runtime, "process_generation", return_value="99123"
        ), mock.patch.object(
            runtime.Atspi, "Table", table_api, create=True
        ), mock.patch.object(
            runtime,
            "scan_provider_identity",
            side_effect=AssertionError("virtual cell should avoid a tree scan"),
        ), mock.patch.object(
            runtime,
            "record_for",
            side_effect=lambda node, index, path, bounds, text_limit: {
                **self._record_for(node, index, path),
                "providerBusName": ":1.42",
                "providerObjectPath": node.path,
                "processGeneration": "99123",
                "showing": True,
                "visible": True,
            },
        ):
            resolved, record, _note, _window, _bounds = (
                runtime.resolve_semantic_target(
                    app,
                    0,
                    window,
                    runtime.frame(0, 0, 100, 100),
                    {
                        "selector": 'table cell "C2"',
                        "locatorKind": "provider_bound",
                        "role": "table cell",
                        "providerRoleAliases": ["cell", "table cell"],
                        "name": "C2",
                        "hasName": True,
                        "providerIdentityScope": "table_owner",
                        "providerBusName": ":1.42",
                        "providerObjectPath": "/sheet/table",
                        "processGeneration": "99123",
                        "runtimeId": [0, 0, 1, 2],
                    },
                    None,
                    {},
                )
            )

        self.assertIs(resolved, cell)
        self.assertEqual(record["name"], "C2")
        table_api.get_accessible_at.assert_called_once()

    def test_exact_provider_duplicate_merge_is_order_independent(self):
        base = {
            "index": 1,
            "runtimeId": [0, 1],
            "providerBusName": ":1.42",
            "providerObjectPath": "/same/control",
            "processGeneration": "99123",
            "controlType": "push button",
            "name": "Save",
            "description": "",
            "value": "",
            "placeholder": "",
            "states": "focused",
            "actions": ["click"],
            "showing": True,
            "visible": True,
        }
        alias = {
            **base,
            "index": 2,
            "runtimeId": [0, 2],
            "states": "selected",
            "description": "Save document",
        }

        first = runtime.deduplicate_provider_records(
            [base, alias], "w1"
        )[0]
        second = runtime.deduplicate_provider_records(
            [alias, base], "w1"
        )[0]

        for field in (
            "runtimeId", "runtimeAliases", "name", "description",
            "states", "actions", "showing", "visible",
        ):
            self.assertEqual(first[field], second[field], field)
        self.assertEqual(first["states"], "focused selected")

    def test_exact_provider_duplicate_incompatible_roles_are_unknown(self):
        records = [
            {
                "index": 1,
                "runtimeId": [0, 1],
                "providerBusName": ":1.42",
                "providerObjectPath": "/same/control",
                "processGeneration": "99123",
                "controlType": "push button",
                "name": "Save",
            },
            {
                "index": 2,
                "runtimeId": [0, 2],
                "providerBusName": ":1.42",
                "providerObjectPath": "/same/control",
                "processGeneration": "99123",
                "controlType": "entry",
                "name": "Save",
            },
        ]

        merged = runtime.deduplicate_provider_records(records, "w1")[0]

        self.assertTrue(merged["_providerIdentityConflict"])

    def test_conflicting_provider_duplicate_cannot_prove_unique_target(self):
        first = self._provider(
            self._node("Save", "push button"), "/same/control"
        )
        second = self._provider(
            self._node("Save", "entry"), "/same/control"
        )
        window = self._node("Editor", "frame")
        window.children = [first, second]
        self.windows = [(0, window)]

        with mock.patch.object(
            runtime, "process_generation", return_value="99123"
        ):
            with self.assertRaises(runtime.OperationFailure) as raised:
                runtime.resolve_semantic_target(
                    None,
                    0,
                    window,
                    runtime.frame(0, 0, 100, 100),
                    {
                        "selector": 'push button "Save"',
                        "role": "push button",
                        "name": "Save",
                        "hasName": True,
                    },
                    None,
                    {},
                )

        self.assertEqual(
            raised.exception.failure_code,
            "observation_incomplete",
        )

    def test_exact_provider_aliases_do_not_create_semantic_ambiguity(self):
        first = self._provider(
            self._node("Format", "menu"), "/same/format"
        )
        second = self._provider(
            self._node("Format", "menu"), "/same/format"
        )
        window = self._node("Editor", "frame")
        window.children = [first, second]
        self.windows = [(0, window)]

        with mock.patch.object(
            runtime,
            "window_instance_handle",
            return_value="w2:stable-window",
        ), mock.patch.object(
            runtime, "process_generation", return_value="99123"
        ):
            node, record, _note, _window, _bounds = (
                runtime.resolve_semantic_target(
                    None,
                    0,
                    window,
                    runtime.frame(0, 0, 100, 100),
                    {
                        "selector": 'menu "Format"',
                        "role": "menu",
                        "name": "Format",
                        "hasName": True,
                    },
                    None,
                    {},
                )
            )

        self.assertIs(node, first)
        self.assertEqual(
            record["runtimeAliases"],
            [[0, 0], [0, 1]],
        )

    def test_incomplete_semantic_scan_cannot_prove_unique(self):
        save = self._node("Save", "push button")
        window = self._node("Editor", "frame")
        window.children = [save]
        self.windows = [(0, window)]

        original_enumerate = runtime.enumerate_children

        def incomplete_children(node, label):
            result = original_enumerate(node, label)
            if node is window:
                return runtime.InventoryResult(
                    result.items,
                    complete=False,
                    errors=["provider child omitted"],
                )
            return result

        with mock.patch.object(
            runtime,
            "enumerate_children",
            side_effect=incomplete_children,
        ):
            with self.assertRaises(runtime.OperationFailure) as raised:
                runtime.resolve_semantic_target(
                    None,
                    0,
                    window,
                    runtime.frame(0, 0, 100, 100),
                    {
                        "selector": 'push button "Save"',
                        "role": "push button",
                        "name": "Save",
                        "hasName": True,
                    },
                    None,
                    {},
                )

        self.assertEqual(
            raised.exception.failure_code,
            "observation_incomplete",
        )

    def test_ambiguity_candidates_are_bounded(self):
        window = self._node("Editor", "frame")
        window.children = [
            self._node("Format", "menu")
            for _ in range(8)
        ]
        self.windows = [(0, window)]

        with mock.patch.object(
            runtime,
            "window_instance_handle",
            return_value="w2:stable-window",
        ):
            with self.assertRaises(runtime.OperationFailure) as raised:
                runtime.resolve_semantic_target(
                    None,
                    0,
                    window,
                    runtime.frame(0, 0, 100, 100),
                    {
                        "selector": 'menu "Format"',
                        "role": "menu",
                        "name": "Format",
                        "hasName": True,
                    },
                    None,
                    {},
                )

        self.assertEqual(
            raised.exception.failure_code,
            "semantic_target_ambiguous",
        )
        self.assertEqual(raised.exception.candidate_count, 8)
        self.assertEqual(len(raised.exception.candidates), 5)
        response = runtime.operation_failure_response(raised.exception)
        self.assertEqual(response["candidateCount"], 8)
        self.assertTrue(response["candidatesTruncated"])
        self.assertEqual(len(response["candidates"]), 5)

    def test_unscoped_target_does_not_search_sibling_window(self):
        main = self._node("Editor", "frame")
        save = self._node("Save", "push button")
        dialog = self._node(
            "Preferences",
            "dialog",
            atspi_states=(STATE.SHOWING,),
        )
        dialog.children = [save]
        self.windows = [(0, main), (1, dialog)]

        with self.assertRaises(runtime.OperationFailure) as raised:
            runtime.resolve_semantic_target(
                None,
                0,
                main,
                runtime.frame(0, 0, 100, 100),
                {
                    "selector": 'push button "Save"',
                    "role": "push button",
                    "name": "Save",
                    "hasName": True,
                },
                None,
                {},
            )

        self.assertEqual(
            raised.exception.failure_code, "semantic_target_not_found")
        self.assertEqual(raised.exception.failure_phase, "precondition")
        self.assertFalse(raised.exception.input_events_emitted)

    def test_window_scoped_target_ignores_duplicate_in_sibling_window(self):
        app = FakeNode()
        app.get_process_id = lambda: 0
        main_save = self._node("Save", "push button")
        dialog_save = self._node("Save", "push button")
        main = self._node(
            "Editor", "frame", atspi_states=(STATE.SHOWING,))
        dialog = self._node(
            "Preferences", "dialog", atspi_states=(STATE.SHOWING,))
        main.children = [main_save]
        dialog.children = [dialog_save]
        self.windows = [(0, main), (1, dialog)]

        node, record, note, matched_window, _bounds = (
            runtime.resolve_semantic_target(
                app,
                0,
                main,
                runtime.frame(0, 0, 100, 100),
                {
                    "selector": 'push button "Save"',
                    "windowHandle": "pid0/win1/dialog",
                    "role": "push button",
                    "name": "Save",
                    "hasName": True,
                },
                None,
                {},
            )
        )

        self.assertIs(node, dialog_save)
        self.assertIs(matched_window, dialog)
        self.assertEqual(record["runtimeId"], [1, 0])
        self.assertIn("window_scope=verified", note)
        self.assertNotIn("pid0/win1/dialog", note)
        self.assertIn("1 visible app-owned window(s)", note)

    def test_window_scoped_target_fails_if_origin_window_disappeared(self):
        app = FakeNode()
        app.get_process_id = lambda: 0
        main = self._node(
            "Editor", "frame", atspi_states=(STATE.SHOWING,))
        self.windows = [(0, main)]

        with self.assertRaises(runtime.OperationFailure) as raised:
            runtime.resolve_semantic_target(
                app,
                0,
                main,
                runtime.frame(0, 0, 100, 100),
                {
                    "selector": 'push button "Save"',
                    "windowHandle": "pid0/win1/dialog",
                    "role": "push button",
                    "name": "Save",
                    "hasName": True,
                },
                None,
                {},
            )

        self.assertEqual(raised.exception.failure_code, "stale_element_ref")
        self.assertEqual(raised.exception.failure_phase, "precondition")
        self.assertFalse(raised.exception.input_events_emitted)
