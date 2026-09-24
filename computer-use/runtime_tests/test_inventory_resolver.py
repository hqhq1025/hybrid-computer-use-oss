#!/usr/bin/env python3
"""Linux provider inventory, window, and virtual-table resolver tests."""

from runtime_test_support import *  # noqa: F401,F403

class VirtualTableCellResolutionTests(AtspiPatchedTestCase):
    def test_snapshot_virtual_cell_path_resolves_through_table_api(self):
        cell = _NamedNode(name="B2", role="table cell")
        container = FakeNode()
        table = object()
        container.get_table_iface = lambda: table
        window = FakeNode()
        record = {
            "runtimeId": [0, 4, 1, 1],
            "controlType": "table cell",
            "name": "B2",
            "frame": None,
        }
        table_api = type(
            "FakeAtspiTable",
            (),
            {
                "get_accessible_at": staticmethod(
                    lambda actual, row, col: (
                        cell if actual is table and (row, col) == (1, 1)
                        else None
                    )
                )
            },
        )

        with mock.patch.object(runtime, "main_window", return_value=(0, window)), \
             mock.patch.object(runtime, "extents",
                               return_value=runtime.frame(0, 0, 100, 100)), \
             mock.patch.object(
                 runtime,
                 "resolve_path",
                 side_effect=lambda _app, path: (
                     container if path == [0, 4] else None
                 ),
             ), \
             mock.patch.object(runtime.Atspi, "Table", table_api, create=True):
            resolved = runtime.find_element_at_snapshot_path(FakeNode(), record)

        self.assertIs(resolved, cell)

class LabelledByTests(AtspiPatchedTestCase):
    """Unnamed controls borrow their name through LABELLED_BY.

    In the LibreOffice 7.3 Position and Size dialog all 13 spin buttons are
    unnamed, and each gets a name through this relation (Position Y,
    Position X, Width, Height, Angle, Radius), so they can be selected by
    name instead of by position.
    """

    def _labelled(self, label_text, own_name=""):
        label = _NamedNode(name=label_text, role="label")
        kind = runtime.Atspi.RelationType.LABELLED_BY
        return _NamedNode(
            name=own_name,
            role="spin button",
            relations=[_FakeRelation(kind, [label])],
        )

    def test_unnamed_control_borrows_the_label(self):
        node = self._labelled("Position Y:")
        # Drop the trailing colon: it adds nothing and is awkward in selectors.
        self.assertEqual(runtime.labelled_by_name(node), "Position Y")
        self.assertEqual(runtime.effective_name(node), "Position Y")

    def test_a_control_with_its_own_name_does_not_borrow(self):
        # Only unnamed nodes benefit. A label lookup for a button already
        # named Save adds nothing but costs a D-Bus round trip (about 1 ms).
        node = self._labelled("Position Y:", own_name="Save")
        self.assertEqual(runtime.effective_name(node), "Save")

    def test_identity_comparison_uses_the_same_name_as_the_record(self):
        """Records and live nodes are compared through the same effective_name().

        If a record stores one name and re-resolution compares another, the
        match fails silently and identity falls back to the weakest check,
        role plus screen position, which points elsewhere once anything moves.
        A borrowed name would do exactly that: the record says Position Y
        while node_name() returns an empty string for the same node.
        """
        node = self._labelled("Position Y:")
        record = {"name": "Position Y"}
        self.assertTrue(runtime.record_name_matches(node, record))
        # This fails if the live side uses the node's own empty name.
        self.assertFalse(runtime.record_name_matches(node, {"name": "Something Else"}))

    def test_borrowed_names_are_marked_so_the_source_is_visible(self):
        # Position Y appears twice in the Position and Size dialog, once per
        # tab, so callers must know the name is borrowed and may not be unique.
        marks = runtime.state_segment(FakeNode(), borrowed_name=True)
        self.assertIn("labelled", marks)
        self.assertNotIn("labelled", runtime.state_segment(FakeNode()))

class ModalDiagnosticTests(AtspiPatchedTestCase):
    """A dialog in front is reported at one of two levels of evidence."""

    def _window(self, name, role, states):
        return _NamedNode(name=name, role=role, states=states)

    def _app(self, *windows):
        return _NamedNode(name="app", role="application", children=windows)

    def test_modal_state_licenses_the_strong_claim(self):
        dialog = self._window("Position and Size", "dialog",
                              [STATE.MODAL, STATE.SHOWING, STATE.ACTIVE])
        main = self._window("Doc - Impress", "frame", [STATE.SHOWING])
        notes = runtime.modal_diagnostic(dialog, self._app(dialog, main))
        self.assertEqual(len(notes), 1)
        self.assertIn("MODAL DIALOG", notes[0])
        # Only the MODAL bit justifies saying other windows ignore input.
        self.assertIn("ignore input to every other window", notes[0])
        # Naming the blocked window tells the agent where to return.
        self.assertIn("Doc - Impress", notes[0])

    def test_a_dialog_without_the_modal_bit_gets_the_weaker_claim(self):
        """Some blocking dialogs never set MODAL.

        LibreOffice 7.3's Tip of the Day dialog is role=dialog, ACTIVE and
        SHOWING without MODAL, yet it blocks the application. Like ENABLED,
        MODAL is set inconsistently across Linux toolkits. Requiring MODAL
        would miss real blockers, while the strong claim would promise more
        than the states prove.
        """
        dialog = self._window("Tip of the Day: 1/223", "dialog",
                              [STATE.SHOWING, STATE.ACTIVE])
        main = self._window("Doc - Impress", "frame", [STATE.SHOWING])
        notes = runtime.modal_diagnostic(dialog, self._app(dialog, main))
        self.assertEqual(len(notes), 1)
        self.assertIn("DIALOG IN FRONT", notes[0])
        # Without MODAL the strong claim must not appear.
        self.assertNotIn("ignore input to every other window", notes[0])
        self.assertIn("not proof that the app is blocked", notes[0])

    def test_a_plain_main_window_says_nothing(self):
        main = self._window("Doc - Impress", "frame", [STATE.SHOWING, STATE.ACTIVE])
        self.assertEqual(runtime.modal_diagnostic(main, self._app(main)), [])

    def test_a_lone_dialog_is_the_main_interface_not_a_blocker(self):
        # With no other window, there is nothing for the dialog to block.
        only = self._window("Preferences", "dialog", [STATE.SHOWING, STATE.ACTIVE])
        self.assertEqual(runtime.modal_diagnostic(only, self._app(only)), [])

class InfrastructureWindowTests(AtspiPatchedTestCase):
    def test_named_desktop_role_without_input_content_is_infrastructure(self):
        desktop_window = _NamedNode(
            name="Desktop",
            role="desktop",
            component=FakeComponent(
                extents=FakeExtents(0, 0, 1920, 1080)),
        )
        with mock.patch.object(
                runtime, "desktop_bounds",
                return_value=runtime.frame(0, 0, 1920, 1080)):
            self.assertTrue(runtime.is_infrastructure_window(desktop_window))

    def test_fullscreen_sparse_application_is_not_infrastructure_by_geometry(self):
        canvas_app = _NamedNode(
            name="Remote Session",
            role="frame",
            component=FakeComponent(
                extents=FakeExtents(0, 0, 1920, 1080)),
        )
        with mock.patch.object(
                runtime, "desktop_bounds",
                return_value=runtime.frame(0, 0, 1920, 1080)):
            self.assertFalse(runtime.is_infrastructure_window(canvas_app))

    def test_named_dialog_with_actionable_content_is_not_infrastructure(self):
        button = _NamedNode(name="OK", role="push button")
        dialog = _NamedNode(
            name="Question",
            role="dialog",
            children=(button,),
            component=FakeComponent(
                extents=FakeExtents(100, 100, 500, 300)),
        )
        with mock.patch.object(
                runtime, "desktop_bounds",
                return_value=runtime.frame(0, 0, 1920, 1080)):
            self.assertFalse(runtime.is_infrastructure_window(dialog))

class ResolvedNoteTests(AtspiPatchedTestCase):
    """Action notes name the element the target resolved to."""

    def test_reports_index_role_and_name(self):
        node = _NamedNode(name="Save", role="push button")
        note = runtime.resolved_note(node, {"index": 4, "controlType": "push button",
                                            "name": "Save"})
        self.assertEqual(note, "Resolved the target ref to push button 'Save'.")

    def test_drift_from_the_snapshot_is_a_warning_not_a_silent_pass(self):
        """Drift from the snapshot record produces a warning.

        Resolution falls back through runtimeId path, automationId, name plus
        role, and role plus geometry. After a menu closes, the same index can
        resolve to a different control, such as a toolbar toggle instead of
        the Rename item, while every step reports success. Silently acting on
        the wrong control is the worst failure mode.
        """
        node = _NamedNode(name="Menu", role="toggle button")
        note = runtime.resolved_note(node, {"index": 9, "controlType": "menu item",
                                            "name": "Rename"})
        self.assertIn("WARNING", note)
        self.assertIn("'menu item'", note)
        self.assertIn("'toggle button'", note)
        self.assertIn("'Rename'", note)

    def test_unnamed_targets_still_get_a_line(self):
        node = _NamedNode(name="", role="canvas")
        note = runtime.resolved_note(node, {"index": 7, "controlType": "canvas"})
        self.assertEqual(note, "Resolved the target ref to an unnamed canvas.")

    def test_coordinate_actions_have_no_element_to_report(self):
        # Coordinate actions resolve no element, so a note would be noise.
        self.assertIsNone(runtime.resolved_note(None, {"index": 1}))
        self.assertIsNone(runtime.resolved_note(_NamedNode(), None))

class ProcessResourceCandidateTests(unittest.TestCase):
    def make_proc(self, root, pid=123):
        proc = os.path.join(root, str(pid))
        os.makedirs(os.path.join(proc, "fd"))
        return proc

    def test_unique_title_matched_fd_resolves_identity(self):
        with tempfile.TemporaryDirectory() as tmp:
            proc_root = os.path.join(tmp, "proc")
            proc = self.make_proc(proc_root)
            document = os.path.join(tmp, "Report.docx")
            with open(document, "wb") as handle:
                handle.write(b"document")
            os.symlink(document, os.path.join(proc, "fd", "4"))
            with open(os.path.join(proc, "cmdline"), "wb") as handle:
                handle.write(b"soffice.bin\0")

            state = runtime.process_resource_candidates(
                123,
                "Report.docx - Writer",
                app_aliases=["Writer"],
                proc_root=proc_root,
            )

        self.assertEqual(state["identityStatus"], "resolved")
        self.assertEqual(state["identityKind"], "file")
        self.assertEqual(state["identityValue"], document)
        self.assertEqual(state["identityBasis"], ["fd_title"])
        self.assertEqual(state["diskStatus"], "available")
        self.assertEqual(state["pathFingerprint"]["size"], 8)
        self.assertEqual(state["openFileFingerprint"]["fd"], 4)
        self.assertIsNone(state["dirty"])

    def test_same_basename_candidates_are_ambiguous(self):
        with tempfile.TemporaryDirectory() as tmp:
            proc_root = os.path.join(tmp, "proc")
            proc = self.make_proc(proc_root)
            for index, folder in enumerate(("a", "b"), 4):
                directory = os.path.join(tmp, folder)
                os.makedirs(directory)
                document = os.path.join(directory, "Report.docx")
                with open(document, "wb") as handle:
                    handle.write(folder.encode())
                os.symlink(document, os.path.join(proc, "fd", str(index)))
            with open(os.path.join(proc, "cmdline"), "wb") as handle:
                handle.write(b"soffice.bin\0")

            state = runtime.process_resource_candidates(
                123,
                "Report.docx - Writer",
                app_aliases=["Writer"],
                proc_root=proc_root,
            )

        self.assertEqual(state["identityStatus"], "ambiguous")
        self.assertEqual(len(state["candidates"]), 2)
        self.assertIsNone(state["identityValue"])

    def test_unrelated_resources_do_not_become_identity(self):
        with tempfile.TemporaryDirectory() as tmp:
            proc_root = os.path.join(tmp, "proc")
            proc = self.make_proc(proc_root)
            unrelated = os.path.join(tmp, "settings.db")
            with open(unrelated, "wb") as handle:
                handle.write(b"x")
            os.symlink(unrelated, os.path.join(proc, "fd", "4"))
            with open(os.path.join(proc, "cmdline"), "wb") as handle:
                handle.write(b"app\0")

            state = runtime.process_resource_candidates(
                123,
                "* Untitled - Editor",
                app_aliases=["Editor"],
                proc_root=proc_root,
            )

        self.assertEqual(state["identityStatus"], "unknown")
        self.assertEqual(state["candidates"], [])
        self.assertEqual(state["titleModifiedMarker"], "*")

    def test_cmdline_path_is_candidate_but_not_resolved_identity(self):
        with tempfile.TemporaryDirectory() as tmp:
            proc_root = os.path.join(tmp, "proc")
            proc = self.make_proc(proc_root)
            workspace = os.path.join(tmp, "project")
            os.makedirs(workspace)
            with open(os.path.join(proc, "cmdline"), "wb") as handle:
                handle.write(
                    b"/opt/Visual Studio Code/code\0"
                    + workspace.encode()
                    + b"\0--reuse-window\0"
                )

            state = runtime.process_resource_candidates(
                123,
                "project - Visual Studio Code",
                app_aliases=["Visual Studio Code"],
                proc_root=proc_root,
            )

        self.assertEqual(state["identityStatus"], "unknown")
        self.assertEqual([item["path"] for item in state["candidates"]], [workspace])
        self.assertEqual(state["candidates"][0]["sources"], ["cmdline_title"])

    def test_title_matching_uses_boundaries(self):
        self.assertFalse(
            runtime.path_matches_window_title("/tmp/log", "Catalog - Editor")
        )
        self.assertFalse(runtime.path_matches_window_title("/tmp/a", "Data - Editor"))
        self.assertTrue(
            runtime.path_matches_window_title(
                "/tmp/Quarterly Report.docx",
                "Quarterly Report.docx - Writer",
                app_aliases=["Writer"],
            )
        )
        self.assertFalse(
            runtime.path_matches_window_title(
                "/tmp/report.docx",
                "report.pdf - Viewer",
            )
        )
        self.assertFalse(
            runtime.path_matches_window_title(
                "/tmp/report",
                "report.docx - Viewer",
                kind="directory",
            )
        )
        self.assertFalse(
            runtime.path_matches_window_title(
                "/tmp/report.docx",
                "report.docx.bak - Editor",
            )
        )
        self.assertFalse(
            runtime.path_matches_window_title(
                "/tmp/report.docx",
                "old.report.docx - Editor",
            )
        )
        self.assertFalse(
            runtime.path_matches_window_title(
                "/tmp/report.docx",
                "draft-report.docx - Editor",
            )
        )
        for title in (
            "report.docx-draft - Editor",
            "report.docx~ - Editor",
            "report.docx—draft - Editor",
            "report.docx–draft - Editor",
            "report.docx draft - Editor",
            "report.docx-1.0 readonly - Editor",
        ):
            self.assertFalse(
                runtime.path_matches_window_title("/tmp/report.docx", title),
                title,
            )
        self.assertFalse(
            runtime.path_matches_window_title(
                "/tmp/dog.xcf",
                "* dog.xcf-1.0 (RGB color) - GIMP",
            )
        )
        for path, title in (
            ("/tmp/report", "report - final.docx"),
            ("/tmp/project", "project-1 - Editor"),
            ("/tmp/report.docx", "report.docx-1.0 ("),
            ("/tmp/report.docx", "** report.docx - Editor"),
        ):
            self.assertFalse(runtime.path_matches_window_title(path, title), title)
        self.assertTrue(
            runtime.path_matches_window_title(
                "/tmp/report.docx",
                "* report.docx - Editor",
                app_aliases=["Editor"],
            )
        )
        self.assertTrue(
            runtime.path_matches_window_title(
                "/tmp/report - final",
                "report - final — Writer",
                app_aliases=["Writer"],
            )
        )
        self.assertFalse(
            runtime.path_matches_window_title(
                "/tmp/report",
                "report - final — Writer",
                app_aliases=["Writer"],
            )
        )
        self.assertFalse(
            runtime.path_matches_window_title(
                "/tmp/Report.docx",
                "report.docx - Editor",
                app_aliases=["Editor"],
            )
        )
        self.assertFalse(
            runtime.path_matches_window_title(
                "/tmp/code",
                "notes.txt - Visual Studio Code",
                app_aliases=["code"],
            )
        )
        self.assertFalse(
            runtime.path_matches_window_title(
                "/tmp/notes.txt",
                "notes.txt - Barcode",
                app_aliases=["code"],
            )
        )
        for path, title in (
            ("/tmp/report.docx", "report.docx - Quarterly Report"),
            ("/tmp/report", "report - final"),
            ("/tmp/project", "project - Read Only"),
        ):
            self.assertFalse(
                runtime.path_matches_window_title(
                    path,
                    title,
                    app_aliases=["Writer", "code"],
                ),
                title,
            )

    def test_scan_errors_prevent_resolved_identity(self):
        with tempfile.TemporaryDirectory() as tmp:
            proc_root = os.path.join(tmp, "proc")
            proc = os.path.join(proc_root, "123")
            os.makedirs(proc)
            document = os.path.join(tmp, "Report.docx")
            with open(document, "wb") as handle:
                handle.write(b"document")
            with open(os.path.join(proc, "cmdline"), "wb") as handle:
                handle.write(b"app\0" + document.encode() + b"\0")

            state = runtime.process_resource_candidates(
                123,
                "Report.docx - Writer",
                app_aliases=["Writer"],
                proc_root=proc_root,
            )

        self.assertTrue(state["scanIncomplete"])
        self.assertEqual(state["identityStatus"], "unknown")

    def test_single_fd_failure_prevents_resolved_identity(self):
        with tempfile.TemporaryDirectory() as tmp:
            proc_root = os.path.join(tmp, "proc")
            proc = self.make_proc(proc_root)
            document = os.path.join(tmp, "Report.docx")
            with open(document, "wb") as handle:
                handle.write(b"document")
            os.symlink(document, os.path.join(proc, "fd", "4"))
            os.symlink(
                os.path.join(tmp, "missing.docx"),
                os.path.join(proc, "fd", "5"),
            )
            with open(os.path.join(proc, "cmdline"), "wb") as handle:
                handle.write(b"app\0")

            state = runtime.process_resource_candidates(
                123,
                "Report.docx - Writer",
                app_aliases=["Writer"],
                proc_root=proc_root,
            )

        self.assertTrue(state["scanIncomplete"])
        self.assertEqual(state["identityStatus"], "unknown")

    def test_deadline_is_rechecked_after_cmdline_read(self):
        with tempfile.TemporaryDirectory() as tmp:
            proc_root = os.path.join(tmp, "proc")
            proc = self.make_proc(proc_root)
            document = os.path.join(tmp, "Report.docx")
            with open(document, "wb") as handle:
                handle.write(b"document")
            with open(os.path.join(proc, "cmdline"), "wb") as handle:
                handle.write(b"app\0" + document.encode() + b"\0")
            readings = iter([0.0, 1.0])

            state = runtime.process_resource_candidates(
                123,
                "Report.docx - Writer",
                app_aliases=["Writer"],
                proc_root=proc_root,
                clock=lambda: next(readings, 1.0),
            )

        self.assertTrue(state["scanDeadlineExceeded"])
        self.assertTrue(state["scanIncomplete"])
        self.assertNotEqual(state["identityStatus"], "resolved")

    def test_deadline_is_rechecked_after_final_path_stat(self):
        with tempfile.TemporaryDirectory() as tmp:
            proc_root = os.path.join(tmp, "proc")
            proc = self.make_proc(proc_root)
            document = os.path.join(tmp, "Report.docx")
            with open(document, "wb") as handle:
                handle.write(b"document")
            os.symlink(document, os.path.join(proc, "fd", "4"))
            with open(os.path.join(proc, "cmdline"), "wb") as handle:
                handle.write(b"app\0")
            real_stat = os.stat
            document_stat_calls = [0]

            def counted_stat(path):
                if path == document:
                    document_stat_calls[0] += 1
                return real_stat(path)

            with mock.patch.object(runtime.os, "stat", side_effect=counted_stat):
                state = runtime.process_resource_candidates(
                    123,
                    "Report.docx - Writer",
                    app_aliases=["Writer"],
                    proc_root=proc_root,
                    clock=lambda: 1.0 if document_stat_calls[0] >= 1 else 0.0,
                )

        self.assertGreaterEqual(document_stat_calls[0], 1)
        self.assertTrue(state["scanDeadlineExceeded"])
        self.assertTrue(state["scanIncomplete"])
        self.assertNotEqual(state["identityStatus"], "resolved")

    def test_incomplete_fd_scan_cannot_resolve_identity(self):
        with tempfile.TemporaryDirectory() as tmp:
            proc_root = os.path.join(tmp, "proc")
            proc = self.make_proc(proc_root)
            first = os.path.join(tmp, "Report.docx")
            second_dir = os.path.join(tmp, "other")
            os.makedirs(second_dir)
            second = os.path.join(second_dir, "Report.docx")
            for path in (first, second):
                with open(path, "wb") as handle:
                    handle.write(path.encode())
            for index in range(runtime.RESOURCE_SCAN_MAX_FDS + 1):
                target = first if index == 0 else (
                    second if index == runtime.RESOURCE_SCAN_MAX_FDS else first
                )
                os.symlink(target, os.path.join(proc, "fd", str(index)))
            with open(os.path.join(proc, "cmdline"), "wb") as handle:
                handle.write(b"app\0")

            state = runtime.process_resource_candidates(
                123,
                "Report.docx - Writer",
                app_aliases=["Writer"],
                proc_root=proc_root,
            )

        self.assertTrue(state["scanIncomplete"])
        self.assertNotEqual(state["identityStatus"], "resolved")

    def test_multiple_open_fds_for_one_path_are_preserved(self):
        with tempfile.TemporaryDirectory() as tmp:
            proc_root = os.path.join(tmp, "proc")
            proc = self.make_proc(proc_root)
            document = os.path.join(tmp, "Report.docx")
            with open(document, "wb") as handle:
                handle.write(b"document")
            os.symlink(document, os.path.join(proc, "fd", "4"))
            os.symlink(document, os.path.join(proc, "fd", "5"))
            with open(os.path.join(proc, "cmdline"), "wb") as handle:
                handle.write(b"app\0")

            state = runtime.process_resource_candidates(
                123,
                "Report.docx - Writer",
                app_aliases=["Writer"],
                proc_root=proc_root,
            )

        self.assertEqual(state["identityStatus"], "resolved")
        self.assertEqual(
            [item["fd"] for item in state["openFileFingerprints"]],
            [4, 5],
        )
        self.assertIsNone(state["openFileFingerprint"])

    def test_deadline_exceeded_cannot_resolve_partial_scan(self):
        with tempfile.TemporaryDirectory() as tmp:
            proc_root = os.path.join(tmp, "proc")
            proc = self.make_proc(proc_root)
            document = os.path.join(tmp, "Report.docx")
            unrelated = os.path.join(tmp, "other.db")
            for path in (document, unrelated):
                with open(path, "wb") as handle:
                    handle.write(b"x")
            os.symlink(document, os.path.join(proc, "fd", "4"))
            os.symlink(unrelated, os.path.join(proc, "fd", "5"))
            with open(os.path.join(proc, "cmdline"), "wb") as handle:
                handle.write(b"app\0")
            readings = iter([0.0, 0.0, 0.0, 0.0, 1.0])

            state = runtime.process_resource_candidates(
                123,
                "Report.docx - Writer",
                app_aliases=["Writer"],
                proc_root=proc_root,
                clock=lambda: next(readings, 1.0),
            )

        self.assertTrue(state["scanDeadlineExceeded"])
        self.assertTrue(state["scanIncomplete"])
        self.assertEqual(state["identityStatus"], "unknown")

    def test_document_state_is_not_part_of_ax_snapshot_identity(self):
        first = runtime.snapshot_digest(
            "window",
            ["0 frame Doc"],
            "",
            "",
        )
        second = runtime.snapshot_digest(
            "window",
            ["0 frame Doc"],
            "",
            "",
        )
        self.assertEqual(first, second)

class ProcessNameAliasTests(unittest.TestCase):
    """Apps can be matched by process name, but URLs never become names.

    The accessible name `Archive Manager` shares no substring with the query
    `file-roller`, which is its process name; org.gnome.Software and
    snap-store behave the same way. /proc cmdline is normally NUL-separated,
    but Chrome's reads as one space-separated string, so splitting on NUL
    and taking the basename would register a URL argument as an app alias.
    """

    def _process_names(self, blob, comm="x", exe=None):
        # process_names accepts a proc_root, so a fake /proc tree avoids
        # patching builtins.open, which is brittle and affects unrelated code.
        import tempfile
        with tempfile.TemporaryDirectory() as root:
            pid_dir = os.path.join(root, "4242")
            os.makedirs(pid_dir)
            with open(os.path.join(pid_dir, "comm"), "w") as handle:
                handle.write(comm + "\n")
            with open(os.path.join(pid_dir, "cmdline"), "wb") as handle:
                handle.write(blob)
            if exe:
                os.symlink(exe, os.path.join(pid_dir, "exe"))
            return runtime.process_names(4242, proc_root=root)

    def test_null_separated_cmdline_gives_binary_name(self):
        names = self._process_names(b"/usr/bin/file-roller\0/tmp/a.zip\0",
                                    comm="file-roller")
        self.assertIn("file-roller", names)

    def test_space_separated_cmdline_does_not_leak_a_url(self):
        # Chrome's cmdline is one space-separated block ending with the URL.
        names = self._process_names(
            b"/opt/google/chrome/chrome --app=https://www.drugs.com",
            comm="chrome")
        self.assertIn("chrome", names)
        self.assertNotIn("drugs.com", names)
        self.assertNotIn("drugs", names)

    def test_suffixed_binary_also_registers_its_stem(self):
        names = self._process_names(b"/usr/lib/libreoffice/program/soffice.bin\0",
                                    comm="soffice.bin")
        self.assertIn("soffice.bin", names)
        self.assertIn("soffice", names)

    def test_generic_packaging_ancestor_registers_product_identity(self):
        names = self._process_names(
            b"/usr/lib/libreoffice/program/soffice.bin\0",
            comm="soffice.bin",
            exe="/usr/lib/libreoffice/program/soffice.bin",
        )
        self.assertIn("libreoffice", names)

    def test_product_query_matches_executable_identity_without_alias_table(self):
        app = type(
            "ProcessApp",
            (),
            {
                "get_name": lambda self: "soffice",
                "get_process_id": lambda self: 4242,
            },
        )()
        with mock.patch.object(
            runtime,
            "process_names",
            return_value={"soffice", "libreoffice"},
        ):
            self.assertTrue(
                runtime.matches_app_bootstrap_query(
                    app, "LibreOffice Impress"
                )
            )

    def test_meaningful_immediate_parent_does_not_leak_vendor_alias(self):
        names = self._process_names(
            b"/opt/google/chrome/chrome --app=https://example.com",
            comm="chrome",
            exe="/opt/google/chrome/chrome",
        )
        self.assertNotIn("google", names)

class HiddenMenuItemsTests(AtspiPatchedTestCase):
    """A menu whose children were all pruned says so on its own line.

    GIMP's closed File menu is SHOWING on the menu bar while none of its items
    are, so pruning removes all of them, Export included, and the agent would
    conclude the app cannot export. The "N items collapsed" hint only covers
    menus that are not SHOWING themselves, and the global "N node(s) omitted"
    footer does not say which menu hid its items.
    """

    def _hidden(self):
        """Build an off-screen menu item, like one in a closed GIMP menu.

        It exists in AT-SPI and has a name, but no visible geometry.
        """
        node = _TreeNode(role="menu item", name="Export As...", w=0, h=0)
        node.states = set()
        return node

    def test_menu_with_all_children_pruned_says_how_many(self):
        item = self._hidden()
        menu = _TreeNode(role="menu", name="File", w=40, h=20, kids=(item,))
        root = _TreeNode(role="frame", name="GIMP", w=100, h=100, kids=(menu,))

        _records, lines = runtime.render_tree(root, None, [0], prune=True)
        text = "\n".join(lines)

        self.assertIn('menu "File"', text)
        self.assertIn("hidden-items=1", text)
        self.assertIn("open menu, then observe", text)

        _records, _lines, annotations = runtime.render_tree(
            root,
            None,
            [0],
            prune=True,
            include_annotations=True,
        )
        self.assertTrue(
            any(
                item.get("kind") == "hidden_children"
                and item.get("count") == 1
                for item in annotations
            ),
            annotations,
        )

    def test_menu_with_visible_children_gets_no_such_note(self):
        # Menus that render their children skip the note to save output bytes.
        item = _TreeNode(role="menu item", name="Export As...", w=40, h=10)
        menu = _TreeNode(role="menu", name="File", w=40, h=20, kids=(item,))
        root = _TreeNode(role="frame", name="GIMP", w=100, h=100, kids=(menu,))

        _records, lines = runtime.render_tree(root, None, [0], prune=True)
        text = "\n".join(lines)

        self.assertIn("Export As...", text)
        self.assertNotIn("hidden-items=", text)

class AppNotFoundListsWindowsTests(AtspiPatchedTestCase):
    """An app lookup failure lists the window titles each candidate owns.

    Callers often name an app after a dialog they can see, such as
    "file-jpeg" for GIMP's "Export Image as JPEG" window. A bare "gimp"
    entry does not reveal that GIMP owns that window.
    """

    def test_listing_names_the_windows_each_app_owns(self):
        window = _NamedNode(name="Export Image as JPEG", role="frame")
        app = _NamedNode(name="gimp", role="application", children=[window])
        # node_pid calls get_process_id directly. Without it the fake raises
        # AttributeError inside the guarded candidate listing, which then
        # comes back empty and hides the cause.
        app.get_process_id = lambda: 0

        with mock.patch.object(runtime, "iter_apps", return_value=[app]), \
             mock.patch.object(runtime, "app_windows",
                               return_value=[(0, window)]), \
             mock.patch.object(runtime, "matches_query", return_value=False):
            with self.assertRaises(RuntimeError) as caught:
                runtime.resolve_app("file-jpeg")

        message = str(caught.exception)
        self.assertEqual(caught.exception.failure_code, "ax_app_unavailable")
        self.assertIn('appNotVisibleToAccessibility("file-jpeg")', message)
        self.assertIn("no action was attempted", message)
        # The owner name alone is not enough; the owned dialog must be listed.
        self.assertIn("gimp", message)
        self.assertIn("Export Image as JPEG", message)

    def test_incomplete_app_inventory_requires_provider_reset(self):
        incomplete = runtime.InventoryResult(
            [],
            complete=False,
            errors=["AX bus disconnected"],
        )
        with mock.patch.object(
            runtime, "iter_apps", return_value=incomplete
        ), mock.patch.object(runtime.time, "sleep", return_value=None):
            with self.assertRaises(runtime.OperationFailure) as caught:
                runtime.resolve_app("Editor")

        self.assertEqual(
            caught.exception.failure_code,
            "ax_app_unavailable",
        )
        self.assertTrue(caught.exception.provider_reset_required)

    def test_complete_app_inventory_miss_does_not_require_reset(self):
        complete = runtime.InventoryResult([], complete=True)
        with mock.patch.object(
            runtime, "iter_apps", return_value=complete
        ), mock.patch.object(
            runtime, "input_capable_app_labels", return_value=[]
        ), mock.patch.object(runtime.time, "sleep", return_value=None):
            with self.assertRaises(runtime.OperationFailure) as caught:
                runtime.resolve_app("Editor")

        self.assertEqual(
            caught.exception.failure_code,
            "ax_app_unavailable",
        )
        self.assertFalse(caught.exception.provider_reset_required)

    def test_app_window_inventory_returns_all_owned_titles(self):
        first = _NamedNode(name="Tip of the Day", role="dialog")
        second = _NamedNode(
            name="Quarterly Review - LibreOffice Impress",
            role="frame",
        )
        app = _NamedNode(
            name="soffice",
            role="application",
            children=[first, second],
        )
        app.get_process_id = lambda: 42
        with mock.patch.object(runtime, "resolve_app", return_value=app), \
             mock.patch.object(
                 runtime,
                 "app_windows",
                 return_value=runtime.InventoryResult(
                     [(0, first), (1, second)], complete=True
                 ),
             ):
            result = runtime.app_window_inventory("libreoffice")

        self.assertTrue(result["ok"])
        self.assertEqual(result["appName"], "soffice")
        self.assertEqual(result["appPid"], 42)
        self.assertEqual(
            result["windowTitles"],
            ["Tip of the Day", "Quarterly Review - LibreOffice Impress"],
        )

class ExactLogicalWindowReadTests(AtspiPatchedTestCase):
    def test_window_handle_selects_the_requested_sibling(self):
        first = _NamedNode(name="Document", role="frame")
        second = _NamedNode(name="Chart Type", role="dialog")
        app = _NamedNode(
            name="soffice",
            role="application",
            children=[first, second],
        )
        windows = runtime.InventoryResult(
            [(0, first), (1, second)], complete=True
        )
        with mock.patch.object(runtime, "app_windows", return_value=windows), \
             mock.patch.object(
                 runtime,
                 "strong_window_instance_handle_inventory",
                 return_value=runtime.InventoryResult(
                     [(0, "w2:0"), (1, "w2:1")],
                     complete=True,
                 ),
             ):
            index, window, evidence = runtime.select_window_by_handle(
                app, "w2:1"
            )
        self.assertEqual(index, 1)
        self.assertIs(window, second)
        self.assertEqual(evidence["reason"], "exact_logical_window")

    def test_incomplete_window_inventory_cannot_select_a_window(self):
        app = _NamedNode(name="soffice", role="application")
        with mock.patch.object(
            runtime,
            "app_windows",
            return_value=runtime.InventoryResult(
                [], complete=False, errors=["AX bus failed"]
            ),
        ):
            with self.assertRaises(runtime.OperationFailure) as caught:
                runtime.select_window_by_handle(app, "w2:1")
        self.assertEqual(
            caught.exception.failure_code, "observation_incomplete"
        )

    def test_complete_missing_window_is_stale(self):
        app = _NamedNode(name="soffice", role="application")
        with mock.patch.object(
            runtime,
            "app_windows",
            return_value=runtime.InventoryResult([], complete=True),
        ), mock.patch.object(
            runtime,
            "strong_window_instance_handle_inventory",
            return_value=runtime.InventoryResult([], complete=True),
        ):
            with self.assertRaises(runtime.OperationFailure) as caught:
                runtime.select_window_by_handle(app, "w2:1")
        self.assertEqual(caught.exception.failure_code, "stale_window_ref")
        self.assertEqual(caught.exception.failure_phase, "precondition")

class VisibleCellsIndexerTests(AtspiPatchedTestCase):
    """render_visible_cells receives the indexer explicitly.

    It is a module-level function and cannot see render_tree's local indexer.
    Referring to that name raises NameError only when the line runs, and that
    line is the rendering path for LibreOffice Calc sheets, the fallback used
    when a self-managed container refuses enumeration.
    """

    def test_signature_accepts_indexer(self):
        import inspect
        params = inspect.signature(runtime.render_visible_cells).parameters
        self.assertIn("indexer", params,
                      "render_visible_cells 必须显式接收 indexer，"
                      "否则函数体里那个自由名字会在运行时炸")

    def test_call_site_passes_indexer(self):
        # The parameter alone is not enough: if the call site omits it, the
        # None default falls back to traversal order and indices stop being
        # stable across snapshots, which is a subtler bug.
        import inspect
        source = inspect.getsource(runtime.render_tree)
        self.assertIn("render_visible_cells(", source)
        call = source[source.index("render_visible_cells("):]
        call = call[:call.index(")\n")]
        self.assertIn("indexer=indexer", call,
                      "render_tree 调用 render_visible_cells 时必须把 indexer 传下去")
        self.assertIn(
            "annotations=annotations",
            call,
            "自管理表格的采集缺口必须进入统一 completeness 账本",
        )

    def test_visible_cell_range_failure_marks_acquisition_incomplete(self):
        annotations = []
        with mock.patch.object(runtime, "visible_cell_range", return_value=None):
            rendered = runtime.render_visible_cells(
                _TreeNode(role="table", name="Data"),
                0,
                [0],
                None,
                [],
                [],
                indexer=runtime.StableIndexer({}),
                annotations=annotations,
            )

        self.assertEqual(rendered, 0)
        self.assertTrue(any(
            item.get("kind") == "provider_omission"
            and item.get("reason") == "visible_cell_range_unavailable"
            for item in annotations
        ), annotations)

    def test_visible_cell_lookup_none_marks_acquisition_incomplete(self):
        table = object()
        node = _TreeNode(role="table", name="Data")
        node.get_table_iface = lambda: table
        annotations = []
        table_api = mock.Mock()
        table_api.get_accessible_at.return_value = None

        with mock.patch.object(
            runtime, "visible_cell_range", return_value=(0, 0, 0, 0)
        ), mock.patch.object(
            runtime.Atspi, "Table", table_api, create=True
        ):
            rendered = runtime.render_visible_cells(
                node,
                0,
                [0],
                None,
                [],
                [],
                indexer=runtime.StableIndexer({}),
                annotations=annotations,
            )

        self.assertEqual(rendered, 0)
        self.assertTrue(any(
            item.get("kind") == "provider_omission"
            and item.get("reason") == "visible_cell_lookup_unavailable"
            and item.get("count") == 1
            for item in annotations
        ), annotations)

    def test_visible_cell_text_failure_marks_acquisition_incomplete(self):
        table = object()
        node = _TreeNode(role="table", name="Data")
        node.get_table_iface = lambda: table
        cell = _TreeNode(role="table cell", name="A1")
        cell._text = object()
        annotations = []
        table_api = mock.Mock()
        table_api.get_accessible_at.return_value = cell
        text_api = mock.Mock()
        text_api.get_character_count.side_effect = RuntimeError(
            "text read failed"
        )

        with mock.patch.object(
            runtime, "visible_cell_range", return_value=(0, 0, 0, 0)
        ), mock.patch.object(
            runtime.Atspi, "Table", table_api, create=True
        ), mock.patch.object(
            runtime.Atspi, "Text", text_api, create=True
        ):
            rendered = runtime.render_visible_cells(
                node,
                0,
                [0],
                None,
                [],
                [],
                indexer=runtime.StableIndexer({}),
                annotations=annotations,
            )

        self.assertEqual(rendered, 0)
        self.assertTrue(any(
            item.get("kind") == "provider_omission"
            and item.get("reason") == "visible_cell_text_unavailable"
            and item.get("count") == 1
            for item in annotations
        ), annotations)

    def test_visible_cell_probe_budget_marks_unread_cells(self):
        table = object()
        node = _TreeNode(role="table", name="Data")
        node.get_table_iface = lambda: table
        cell = _TreeNode(role="table cell", name="A1")
        annotations = []
        table_api = mock.Mock()
        table_api.get_accessible_at.return_value = cell

        with mock.patch.object(
            runtime, "visible_cell_range", return_value=(0, 0, 0, 1)
        ), mock.patch.object(
            runtime.Atspi, "Table", table_api, create=True
        ), mock.patch.object(
            runtime, "MAX_TABLE_CELLS", 1
        ), mock.patch.object(
            runtime, "text_value_with_status", return_value=("value", True)
        ), mock.patch.object(
            runtime,
            "record_for",
            return_value={
                "index": 1,
                "localizedControlType": "table cell",
                "controlType": "table cell",
                "frame": None,
            },
        ):
            rendered = runtime.render_visible_cells(
                node,
                0,
                [0],
                None,
                [],
                [],
                indexer=runtime.StableIndexer({}),
                annotations=annotations,
            )

        self.assertEqual(rendered, 1)
        self.assertTrue(any(
            item.get("kind") == "provider_omission"
            and item.get("reason") == "visible_cell_budget"
            and item.get("count") == 1
            for item in annotations
        ), annotations)

    def test_visible_cell_geometry_respects_boxes_boundary(self):
        import inspect
        params = inspect.signature(runtime.render_visible_cells).parameters
        self.assertIn("boxes", params)
        self.assertFalse(params["boxes"].default)
        source = inspect.getsource(runtime.render_visible_cells)
        self.assertIn("if boxes and cell_frame is not None", source)

        render_source = inspect.getsource(runtime.render_tree)
        call = render_source[render_source.index("render_visible_cells("):]
        call = call[:call.index(")\n")]
        self.assertIn("boxes=boxes", call)

    def test_spreadsheet_coordinates_include_canonical_a1_names(self):
        self.assertEqual(runtime.spreadsheet_a1(0, 0), "A1")
        self.assertEqual(runtime.spreadsheet_a1(0, 25), "Z1")
        self.assertEqual(runtime.spreadsheet_a1(0, 26), "AA1")
        self.assertEqual(runtime.spreadsheet_a1(2, 3), "D3")

class SemanticRoleAliasTests(unittest.TestCase):
    def test_standard_and_provider_roles_both_match(self):
        self.assertTrue(runtime.semantic_role_matches("push button", "button"))
        self.assertTrue(runtime.semantic_role_matches("push button", "push button"))
        self.assertTrue(runtime.semantic_role_matches("entry", "textbox"))
        self.assertFalse(runtime.semantic_role_matches("check box", "button"))
