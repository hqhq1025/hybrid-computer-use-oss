#!/usr/bin/env python3
"""Linux provider app resolution, window listing, inventory completeness, and AX window close tests."""

from runtime_test_support import *  # noqa: F401,F403


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

        with self.assertRaises(runtime.OperationFailure) as caught:
            runtime.resolve_app("Editor")

        self.assertEqual(caught.exception.failure_code, "ax_app_ambiguous")
        self.assertFalse(caught.exception.input_events_emitted)
        # The recovery names the current call, not the removed app_ref.
        self.assertIn('{"mode":"apps"}', str(caught.exception))
        self.assertNotIn("app_ref", str(caught.exception))
        # The model picks a window, not an aN.
        self.assertIn("wN", str(caught.exception))
        self.assertNotIn("aN", str(caught.exception))
        # Each matching process is named so the server can list its windows.
        self.assertEqual(caught.exception.candidate_count, 2)
        self.assertEqual(
            [candidate["name"] for candidate in caught.exception.candidates],
            ["Editor", "Editor"],
        )
        for candidate in caught.exception.candidates:
            self.assertEqual(
                set(candidate),
                {"name", "providerBusName", "processGeneration"},
            )

    def test_exact_app_identity_does_not_fall_back_to_name(self):
        current = _TreeNode(role="application", name="LibreOffice")
        restarted = _TreeNode(role="application", name="LibreOffice")
        self.patch_iter_apps([[current, restarted]])
        original_bus = runtime.provider_bus_name
        original_generation = runtime.process_generation
        runtime.provider_bus_name = lambda app: (
            ":1.84" if app is current else ":1.91"
        )
        runtime.process_generation = lambda pid, proc_root="/proc": "gen-current"
        self.addCleanup(setattr, runtime, "provider_bus_name", original_bus)
        self.addCleanup(setattr, runtime, "process_generation", original_generation)

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
        runtime.provider_bus_name = lambda _app: ":1.84"
        runtime.process_generation = lambda _pid, proc_root="/proc": "gen-current"
        self.addCleanup(setattr, runtime, "provider_bus_name", original_bus)
        self.addCleanup(setattr, runtime, "process_generation", original_generation)

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
        runtime.provider_bus_name = lambda _app: ":1.84"
        runtime.process_generation = lambda _pid, proc_root="/proc": "gen-current"
        self.addCleanup(setattr, runtime, "provider_bus_name", original_bus)
        self.addCleanup(setattr, runtime, "process_generation", original_generation)

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
        runtime.provider_bus_name = lambda _app: ":1.84"
        runtime.process_generation = lambda _pid, proc_root="/proc": "gen-current"
        self.addCleanup(setattr, runtime, "provider_bus_name", original_bus)
        self.addCleanup(setattr, runtime, "process_generation", original_generation)

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
        runtime.iter_apps = lambda: [app]
        self.addCleanup(setattr, runtime, "iter_apps", original_apps)

        text = runtime.list_apps_inventory()["text"]

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
        runtime.iter_apps = lambda: [first, second]
        self.addCleanup(setattr, runtime, "iter_apps", original_apps)

        text = runtime.list_apps_inventory()["text"]

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
        runtime.iter_apps = lambda: [first, second]
        self.addCleanup(setattr, runtime, "iter_apps", original_apps)

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

        def one_inventory():
            calls["count"] += 1
            return [app]

        runtime.iter_apps = one_inventory
        self.addCleanup(setattr, runtime, "iter_apps", original_apps)

        result = perform_provider_operation({"intent": "observe_apps"})

        self.assertEqual(calls["count"], 1)
        self.assertEqual(result["text"], 'editor [window="Document"]')
        self.assertEqual(len(result["apps"]), 1)
        self.assertEqual(result["apps"][0]["displayText"], result["text"])
        self.assertTrue(result["apps"][0]["inputCapable"])


class ListAppsTopLevelWindowTests(AtspiPatchedTestCase):
    def _patch_inventory(self, apps, handles, root_state, infrastructure=()):
        original_apps = runtime.iter_apps
        runtime.iter_apps = lambda: apps
        self.addCleanup(setattr, runtime, "iter_apps", original_apps)
        for patcher in (
            mock.patch.object(runtime, "strong_window_instance_handles",
                              side_effect=lambda app, windows: handles.get(id(app), {})),
            mock.patch.object(runtime, "correlation_root_window_state", return_value=root_state),
            mock.patch.object(runtime, "opaque_window_instance_handle_for_id",
                              side_effect=lambda app, window_id: "w2:" + str(window_id)),
            mock.patch.object(runtime, "is_infrastructure_window",
                              side_effect=lambda window: window in infrastructure),
            mock.patch.object(runtime, "provider_bus_name", return_value=":1.9"),
            mock.patch.object(runtime, "process_generation", return_value="gen"),
        ):
            patcher.start()
            self.addCleanup(patcher.stop)

    def test_every_window_is_listed_with_handle_and_active_flag(self):
        doc = _TreeNode(role="frame", name="Report.txt - Editor")
        prefs = _TreeNode(role="dialog", name="Preferences")
        app = _TreeNode(role="application", name="editor", kids=(doc, prefs))
        app.get_process_id = lambda: 101
        self._patch_inventory(
            [app], {id(app): {0: "w2:0xa", 1: "w2:0xb"}},
            {"windowIds": {"0xa", "0xb"}, "activeWindow": "0xb"},
        )

        windows = runtime.list_apps_inventory()["apps"][0]["windows"]

        self.assertEqual([w["title"] for w in windows],
                         ["Report.txt - Editor", "Preferences"])
        self.assertEqual([w["handle"] for w in windows], ["w2:0xa", "w2:0xb"])
        self.assertEqual([w["active"] for w in windows], [False, True])
        self.assertTrue(all(w["inputTarget"] for w in windows))

    def test_window_without_strong_handle_is_listed_unaddressable(self):
        window = _TreeNode(role="frame", name="Untracked")
        app = _TreeNode(role="application", name="editor", kids=(window,))
        app.get_process_id = lambda: 101
        self._patch_inventory([app], {id(app): {}},
                              {"windowIds": set(), "activeWindow": None})

        windows = runtime.list_apps_inventory()["apps"][0]["windows"]

        self.assertEqual(windows[0]["handle"], "")
        self.assertFalse(windows[0]["active"])

    def test_infrastructure_window_is_marked(self):
        panel = _TreeNode(role="frame", name="")
        app = _TreeNode(role="application", name="panel", kids=(panel,))
        app.get_process_id = lambda: 7
        self._patch_inventory([app], {id(app): {0: "w2:0xc"}},
                              {"windowIds": {"0xc"}, "activeWindow": "0xc"},
                              infrastructure=(panel,))

        windows = runtime.list_apps_inventory()["apps"][0]["windows"]

        self.assertFalse(windows[0]["inputTarget"])

    def test_app_is_input_capable_iff_a_listed_window_is_an_input_target(self):
        panel = _TreeNode(role="frame", name="")
        dock = _TreeNode(role="frame", name="")
        infra = _TreeNode(role="application", name="panel", kids=(panel, dock))
        infra.get_process_id = lambda: 7
        doc = _TreeNode(role="frame", name="Doc")
        editor = _TreeNode(role="application", name="editor", kids=(panel, doc))
        editor.get_process_id = lambda: 8
        self._patch_inventory(
            [infra, editor], {}, {"windowIds": set(), "activeWindow": None},
            infrastructure=(panel, dock),
        )
        with mock.patch.object(
            runtime, "process_generation", side_effect=lambda pid: str(pid)
        ):
            apps = {
                a["name"]: a for a in runtime.list_apps_inventory()["apps"]
            }

        self.assertFalse(apps["panel"]["inputCapable"])
        self.assertIn("NOT an input target", apps["panel"]["displayText"])
        self.assertTrue(apps["editor"]["inputCapable"])
        self.assertEqual(
            [w["inputTarget"] for w in apps["editor"]["windows"]],
            [False, True],
        )

    def test_unanswered_application_marks_the_listing_incomplete(self):
        doc = _TreeNode(role="frame", name="Doc")
        editor = _TreeNode(role="application", name="editor", kids=(doc,))
        editor.get_process_id = lambda: 8
        hung = _TreeNode(role="application", name="")
        hung.get_process_id = lambda: 9

        def no_answer():
            raise RuntimeError("Timeout was reached")

        hung.get_child_count = no_answer
        self._patch_inventory(
            [editor, hung], {}, {"windowIds": set(), "activeWindow": None},
        )
        with mock.patch.object(
            runtime, "process_generation", side_effect=lambda pid: str(pid)
        ):
            result = runtime.list_apps_inventory()

        self.assertEqual([a["name"] for a in result["apps"]], ["editor"])
        self.assertEqual(len(result["notes"]), 1)
        self.assertIn("1 application(s) did not answer", result["notes"][0])
        self.assertEqual(len(result["inventory_issues"]), 1)
        self.assertEqual(result["inventory_issues"][0]["app"], "unnamed application")
        self.assertIn("remain unknown", result["inventory_issues"][0]["reason"])

    def test_complete_listing_has_no_notes(self):
        doc = _TreeNode(role="frame", name="Doc")
        editor = _TreeNode(role="application", name="editor", kids=(doc,))
        editor.get_process_id = lambda: 8
        self._patch_inventory(
            [editor], {}, {"windowIds": set(), "activeWindow": None},
        )
        result = runtime.list_apps_inventory()
        self.assertEqual(result["notes"], [])
        self.assertEqual(result["inventory_issues"], [])

    def test_unreadable_window_manager_falls_back_to_atspi_active(self):
        window = _TreeNode(role="frame", name="Doc")
        window.states.add(STATE.ACTIVE)
        app = _TreeNode(role="application", name="editor", kids=(window,))
        app.get_process_id = lambda: 101
        self._patch_inventory([app], {id(app): {0: "w2:0xa"}}, None)

        windows = runtime.list_apps_inventory()["apps"][0]["windows"]

        self.assertTrue(windows[0]["active"])


class ProviderInventoryCompletenessTests(AtspiPatchedTestCase):
    def test_incomplete_application_enumeration_reaches_apps_evidence(self):
        root = _TreeNode(role="desktop", name="Desktop")
        def fail():
            raise RuntimeError("AX bus failed")
        root.get_child_count = fail
        with mock.patch.object(runtime, "desktop", return_value=root), mock.patch.object(
            runtime, "correlation_root_window_state", return_value=None
        ):
            result = runtime.list_apps_inventory()
        self.assertEqual(result["apps"], [])
        self.assertEqual(result["inventory_issues"], [{
            "reason": "The accessibility bus did not list every application."
        }])
        self.assertTrue(result["notes"])

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

    def test_unreadable_window_role_is_incomplete_not_windowless(self):
        """A failed role read is not an empty role: the inventory is incomplete."""
        window = _TreeNode(role="frame", name="Editor")

        def broken_role():
            raise RuntimeError("AT-SPI call timed out")

        window.get_role_name = broken_role
        app = _TreeNode(role="application", name="Editor", kids=(window,))

        inventory = runtime.app_windows(app)

        self.assertEqual(list(inventory), [])
        self.assertFalse(inventory.complete)
        self.assertIn("role failed: RuntimeError", inventory.errors[0])


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

    def test_activation_is_observed_only_when_the_window_becomes_active(self):
        states = iter([
            {"windowIds": {"0x100", "0x200"}, "activeWindow": "0x200"},
            {"windowIds": {"0x100", "0x200"}, "activeWindow": "0x100"},
        ])
        with mock.patch.object(
            runtime, "x11_root_window_state", side_effect=lambda: next(states)
        ), mock.patch.object(
            runtime, "app_windows",
            return_value=runtime.InventoryResult([], complete=True),
        ), mock.patch.object(
            runtime, "x11_window_inventory_for_pid",
            side_effect=AssertionError("activation never reads close evidence"),
        ), mock.patch.object(runtime, "time", _NoSleep()):
            observed, windowless = window_resolver._await_window_transition(
                "activate", self.app, 4242, "0x100"
            )
        self.assertTrue(observed)
        self.assertFalse(windowless)

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

    def test_native_inventory_is_listed_once_per_operation(self):
        listing = subprocess.CompletedProcess(
            [],
            0,
            "0x01000001 0 4242 10 20 800 600 host Document\n"
            "0x02000001 0 5151 0 0 300 200 host Panel\n",
            "",
        )
        with mock.patch.object(
            runtime.subprocess, "run", return_value=listing
        ) as command:
            first = runtime.x11_window_inventory_for_pid(4242)
            second = runtime.x11_window_inventory_for_pid(5151)
            missing = runtime.x11_window_inventory_for_pid(9999)

        self.assertEqual(command.call_count, 1)
        self.assertEqual([w["id"] for w in first], ["0x1000001"])
        self.assertEqual(first[0]["title"], "Document")
        self.assertNotIn("pid", first[0])
        self.assertEqual([w["id"] for w in second], ["0x2000001"])
        self.assertTrue(missing.complete)
        self.assertEqual(list(missing), [])

    def test_window_correlation_reads_root_state_once_per_operation(self):
        root = subprocess.CompletedProcess(
            [],
            0,
            "_NET_CLIENT_LIST_STACKING(WINDOW): window id # 0x1000001\n"
            "_NET_ACTIVE_WINDOW(WINDOW): window id # 0x1000001\n",
            "",
        )
        with mock.patch.object(
            runtime.subprocess, "run", return_value=root
        ) as command:
            first = runtime.correlation_root_window_state()
            second = runtime.correlation_root_window_state()
            runtime._x11_window_inventory_cache.clear()
            third = runtime.correlation_root_window_state()

        self.assertEqual(command.call_count, 2)
        self.assertEqual(first, second)
        self.assertEqual(third["activeWindow"], "0x1000001")

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


class SameTitleWindowHandleTests(AtspiPatchedTestCase):
    """Hidden same-title windows must not take the handle of the shown one.

    Electron applications keep hidden BrowserWindows that AT-SPI still lists
    as frames. Joplin 3.5 lists three frames named "Joplin" while the window
    manager manages one window: the 1536x842 main window, plus two hidden
    800x600 ones that wmctrl does not list. All three matched the managed
    window by title, the tie gave none of them a handle, and every node of
    Joplin became addressable=false (owner_identity_incomplete).
    """

    def _joplin(self):
        main = _TreeNode(role="frame", name="Joplin", x=192, y=132, w=1536, h=842)
        hidden_a = _TreeNode(role="frame", name="Joplin", x=560, y=253, w=800, h=600)
        hidden_b = _TreeNode(role="frame", name="Joplin", x=560, y=253, w=800, h=600)
        app = _TreeNode(role="application", name="joplin",
                        kids=(main, hidden_a, hidden_b))
        app.get_process_id = lambda: 4633
        return app

    def _managed(self, *windows):
        return runtime.InventoryResult(list(windows), complete=True)

    def test_the_frame_matching_the_managed_geometry_gets_the_handle(self):
        app = self._joplin()
        managed = self._managed({
            "pid": 4633, "id": "0x3200004", "x": 197, "y": 161,
            "width": 1536, "height": 842, "title": "Joplin"})
        with mock.patch.object(runtime, "_x11_managed_windows",
                               return_value=managed), \
                mock.patch.object(runtime, "process_generation",
                                  return_value="77"), \
                mock.patch.object(runtime, "correlation_root_window_state",
                                  return_value=None):
            handles = runtime.strong_window_instance_handles(app)

        self.assertEqual(list(handles), [0])
        self.assertTrue(handles[0].startswith("w2:"))

    def test_equally_close_frames_stay_unaddressable(self):
        """Two frames at the managed geometry cannot be told apart."""
        twin_a = _TreeNode(role="frame", name="Doc", x=0, y=0, w=800, h=600)
        twin_b = _TreeNode(role="frame", name="Doc", x=0, y=0, w=800, h=600)
        app = _TreeNode(role="application", name="editor", kids=(twin_a, twin_b))
        app.get_process_id = lambda: 9
        managed = self._managed({
            "pid": 9, "id": "0x1", "x": 0, "y": 0,
            "width": 800, "height": 600, "title": "Doc"})
        with mock.patch.object(runtime, "_x11_managed_windows",
                               return_value=managed), \
                mock.patch.object(runtime, "process_generation",
                                  return_value="77"), \
                mock.patch.object(runtime, "correlation_root_window_state",
                                  return_value=None):
            handles = runtime.strong_window_instance_handles(app)

        self.assertEqual(handles, {})


class OverrideRedirectPopupHandleTests(AtspiPatchedTestCase):
    """An untitled popup frame gets the handle of its override-redirect window.

    Chromium opens its app menu, context menus and <select> lists as
    override-redirect X11 windows, which wmctrl never lists, and AT-SPI
    shows each as an untitled frame. Without a native window the frame got
    no handle, so every menu item rendered addressable=false with
    owner_identity_incomplete and AX could not pick a menu item.
    """

    XWININFO_CHILDREN = (
        "\nxwininfo: Window id: 0x5c5 (the root window) (has no name)\n\n"
        "  Root window id: 0x5c5 (the root window) (has no name)\n"
        "  Parent window id: 0x0 (none)\n"
        "     3 children:\n"
        "     0x2000011 (has no name): ()  467x813+501+105  +501+105\n"
        "     0x2000004 \"Page - Google Chrome\": ()  945x1033+10+37  +10+37\n"
        "     0x3000001 (has no name): ()  10x10+10+10  +10+10\n"
    )

    def _xwininfo(self, viewable=True, override=True):
        return (
            "xwininfo: Window id: 0x2000011 (has no name)\n"
            "  Absolute upper-left X:  501\n  Absolute upper-left Y:  105\n"
            "  Width: 467\n  Height: 813\n"
            "  Map State: {}\n  Override Redirect State: {}\n".format(
                "IsViewable" if viewable else "IsUnMapped",
                "yes" if override else "no")
        )

    def _run(self, viewable=True, override=True, pid=389):
        def run(command, **_kwargs):
            if command[:2] == ["wmctrl", "-lpG"]:
                out = "0x02000004  0 389 10 37 945 1033 host Page - Google Chrome\n"
            elif command[:3] == ["xwininfo", "-root", "-children"]:
                out = self.XWININFO_CHILDREN
            elif command[:3] == ["xwininfo", "-id", "0x2000011"]:
                out = self._xwininfo(viewable, override)
            elif command[:3] == ["xprop", "-id", "0x2000011"]:
                out = "_NET_WM_PID(CARDINAL) = {}\n".format(pid)
            else:
                out = ""
            return subprocess.CompletedProcess(command, 0, out, "")
        return run

    def _chrome(self, popup_name=""):
        page = _TreeNode(role="frame", name="Page - Google Chrome",
                         x=10, y=37, w=945, h=1033)
        menu = _TreeNode(role="frame", name=popup_name, x=501, y=105, w=467, h=813)
        app = _TreeNode(role="application", name="Google Chrome", kids=(page, menu))
        app.get_process_id = lambda: 389
        return app

    def _handles(self, app, **run_options):
        with mock.patch.object(runtime.subprocess, "run",
                               side_effect=self._run(**run_options)), \
                mock.patch.object(runtime, "process_generation", return_value="77"), \
                mock.patch.object(runtime, "correlation_root_window_state",
                                  return_value=None):
            return runtime.strong_window_instance_handles(app)

    def test_untitled_popup_takes_the_override_redirect_window(self):
        handles = self._handles(self._chrome())

        self.assertEqual(sorted(handles), [0, 1])
        self.assertNotEqual(handles[0], handles[1])

    def test_unmapped_or_managed_windows_are_not_popups(self):
        self.assertEqual(sorted(self._handles(self._chrome(), viewable=False)), [0])
        self.assertEqual(sorted(self._handles(self._chrome(), override=False)), [0])

    def test_popup_of_another_process_is_not_taken(self):
        self.assertEqual(sorted(self._handles(self._chrome(), pid=999)), [0])

    def test_untitled_frames_of_desktop_components_do_not_scan(self):
        panel_frame = _TreeNode(role="frame", name="", x=0, y=0, w=1920, h=27)
        panel = _TreeNode(role="application", name="xfce4-panel", kids=(panel_frame,))
        panel.get_process_id = lambda: 85
        calls = []

        def run(command, **_kwargs):
            calls.append(command[0])
            return subprocess.CompletedProcess(command, 0, "", "")

        with mock.patch.object(runtime.subprocess, "run", side_effect=run), \
                mock.patch.object(runtime, "process_generation", return_value="77"), \
                mock.patch.object(runtime, "correlation_root_window_state",
                                  return_value=None):
            runtime.strong_window_instance_handles(panel)

        self.assertEqual(calls, ["wmctrl"])
