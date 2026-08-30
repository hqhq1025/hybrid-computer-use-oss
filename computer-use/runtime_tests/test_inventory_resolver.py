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
    """无名控件从 LABELLED_BY 借名字。

    判据来自实测：LibreOffice 7.3「位置和大小」对话框里 13 个 spin button
    全部无名，13 个都能靠这条关系拿到名字（Position Y / Position X /
    Width / Height / Angle / Radius）。定位方式因此从"数第几个"变成"按名字选"。
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
        # 尾冒号要去掉：选择器里写冒号很别扭，而冒号不携带信息
        self.assertEqual(runtime.labelled_by_name(node), "Position Y")
        self.assertEqual(runtime.effective_name(node), "Position Y")

    def test_a_control_with_its_own_name_does_not_borrow(self):
        # 增益全部落在无名节点上。一个已经叫 Save 的按钮再关联一个 Save 标签，
        # 对 agent 没有新信息，却要付一次 DBus 往返（实测 1.037ms/节点）。
        node = self._labelled("Position Y:", own_name="Save")
        self.assertEqual(runtime.effective_name(node), "Save")

    def test_identity_comparison_uses_the_same_name_as_the_record(self):
        """这是本条改动最容易复发的坑，必须钉死。

        commit 5543a52 修过同型问题：record 里存一种名字、重解析时用另一种去比，
        必然失配；而失配是**静默**的，身份判据会悄悄退到最弱的"role + 屏幕位置"，
        元素一动就指向别人。

        借名字会原样重现它：记录里写着 Position Y，而 node_name() 在同一个节点上
        返回空串。所以两边必须走同一个 effective_name()。
        """
        node = self._labelled("Position Y:")
        record = {"name": "Position Y"}
        self.assertTrue(runtime.record_name_matches(node, record))
        # 反证：如果活节点侧改用自身名字（空串），这条断言就会失败
        self.assertFalse(runtime.record_name_matches(node, {"name": "Something Else"}))

    def test_borrowed_names_are_marked_so_the_source_is_visible(self):
        # 同一个「位置和大小」对话框里 Position Y 出现了**两次**（两个标签页各一个）。
        # 调用方需要知道这个名字是借来的、因此可能不唯一。
        marks = runtime.state_segment(FakeNode(), borrowed_name=True)
        self.assertIn("labelled", marks)
        self.assertNotIn("labelled", runtime.state_segment(FakeNode()))

class ModalDiagnosticTests(AtspiPatchedTestCase):
    """挡在前面的对话框要明说。分两档，因为证据强度不同。"""

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
        # 有 MODAL 位才能断言"应用会忽略其它窗口的输入"
        self.assertIn("ignore input to every other window", notes[0])
        # 要说清挡住了谁——agent 需要知道回哪儿去
        self.assertIn("Doc - Impress", notes[0])

    def test_a_dialog_without_the_modal_bit_gets_the_weaker_claim(self):
        """实测逼出来的一档。

        LibreOffice 7.3 的「Tip of the Day」是 role=dialog、ACTIVE、SHOWING，
        **却不设 MODAL**，而它确确实实挡在应用前面。MODAL 位在 Linux 上和
        ENABLED 一样，不同工具包设不设全凭自觉。

        只认 MODAL 会漏掉真实阻塞；把两档混为一谈，则是替一个不设 MODAL 的
        对话框打我们无权打的包票。
        """
        dialog = self._window("Tip of the Day: 1/223", "dialog",
                              [STATE.SHOWING, STATE.ACTIVE])
        main = self._window("Doc - Impress", "frame", [STATE.SHOWING])
        notes = runtime.modal_diagnostic(dialog, self._app(dialog, main))
        self.assertEqual(len(notes), 1)
        self.assertIn("DIALOG IN FRONT", notes[0])
        # 不许出现强断言
        self.assertNotIn("ignore input to every other window", notes[0])
        self.assertIn("not proof that the app is blocked", notes[0])

    def test_a_plain_main_window_says_nothing(self):
        main = self._window("Doc - Impress", "frame", [STATE.SHOWING, STATE.ACTIVE])
        self.assertEqual(runtime.modal_diagnostic(main, self._app(main)), [])

    def test_a_lone_dialog_is_the_main_interface_not_a_blocker(self):
        # 应用只有这一个对话框窗口时，它就是主界面。说"它挡着什么"是无中生有。
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
    """动作要说清作用在了谁身上。"""

    def test_reports_index_role_and_name(self):
        node = _NamedNode(name="Save", role="push button")
        note = runtime.resolved_note(node, {"index": 4, "controlType": "push button",
                                            "name": "Save"})
        self.assertEqual(note, "Resolved the target ref to push button 'Save'.")

    def test_drift_from_the_snapshot_is_a_warning_not_a_silent_pass(self):
        """这是本条真正的价值所在。

        解析是多级回退的（runtimeId 路径 → automationId → name+role → role+几何）。
        record_still_matches 里记着 Nautilus 的实例：菜单关掉之后同一个 index 9
        解析到了工具栏的"切换视图选项"，于是"重命名"变成了别的操作，
        而且一路 isError=False——静默操作错误的控件是最坏的失败模式。
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
        # GUI track 不定位任何元素，硬报一条只会是噪声
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
    """应用名可以用**进程名**去猜——以及别把网页地址当成进程名。

    第一条来自轨迹：`appNotFound("file-roller")`，而那个应用的 a11y 名是
    `Archive Manager`。两个名字之间没有公共子串，分隔符归一化再宽松也救不回来，
    但 `file-roller` 恰恰就是它的进程名。真机上同类的还有
    org.gnome.Software / snap-store。

    第二条是我加第一条时**自己引入的**：cmdline 的约定是 \0 分隔，但 Chrome
    那条读出来是一整块 `chrome --remote-debugging-port=1337`（空格分隔）。
    只按 \0 切再取 basename，就会从 `…/drugs.com` 里读出 "drugs.com"，
    把一个网页地址注册成 Chrome 的别名——工具凭空编出一个不存在的应用名。
    这两条都钉在这里。
    """

    def _process_names(self, blob, comm="x", exe=None):
        # 不去补丁 builtins.open——那既脆又会波及无关代码。process_names 接受
        # 一个 proc_root，测试造一棵假的 /proc 就够了。
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
        # 真机上 Chrome 就是这样：整块、空格分隔、末尾是访问过的地址。
        names = self._process_names(
            b"/opt/google/chrome/chrome --app=https://www.drugs.com",
            comm="chrome")
        self.assertIn("chrome", names)
        self.assertNotIn("drugs.com", names)
        self.assertNotIn("drugs", names)

    def test_suffixed_binary_also_registers_its_stem(self):
        # soffice.bin 要能被 "soffice" 猜中。
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
    """菜单的子项一个都没渲染出来时，要在这个菜单自己身上说明白。

    出处是第 47、48 题：agent 要把 GIMP 里的图导出，而树里找不到任何
    Export/Save As，只能得出"这个应用没有导出"的结论。真相是 File 底下就有
    `Export to <文件名>`，只是那 39 个菜单项自己不 SHOWING，被逐个裁掉了。

    原有的 "N items collapsed" 提示救不了这种情况，因为它的判据是「菜单自己不
    SHOWING」——而 GIMP 的 File 菜单**是** SHOWING 的，它就挂在菜单栏上。
    树末尾那句全局的 "N node(s) omitted" 也救不了：它不指名道姓，
    没人能从中知道是哪个菜单藏了东西。
    """

    def _hidden(self):
        """造一个"不在屏幕上"的菜单项——GIMP 未打开的菜单项就是这个状态：
        AT-SPI 里它存在、有名字，但没有可见的几何。"""
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
        # 数字要对得上，措辞要说清"打开之后才看得见"。
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
        # 子项渲染得出来时不该多这一句——否则每个正常菜单都要付这行字节。
        item = _TreeNode(role="menu item", name="Export As...", w=40, h=10)
        menu = _TreeNode(role="menu", name="File", w=40, h=20, kids=(item,))
        root = _TreeNode(role="frame", name="GIMP", w=100, h=100, kids=(menu,))

        _records, lines = runtime.render_tree(root, None, [0], prune=True)
        text = "\n".join(lines)

        self.assertIn("Export As...", text)
        self.assertNotIn("hidden-items=", text)

class AppNotFoundListsWindowsTests(AtspiPatchedTestCase):
    """appNotFound 的候选表要连**窗口标题**一起给。

    轨迹证据：GIMP 那一段里 agent 三次拿对话框的名字当应用名去调——
    appNotFound("script-fu") 两次、appNotFound("file-jpeg") 一次。
    它并没有猜错：屏幕上确实摆着一个 "Script-Fu Console"、一个
    "Export Image as JPEG"。错的是它无从知道这两个窗口都归 gimp 管，
    因为候选表里只有一行光秃秃的 "gimp"。
    """

    def test_listing_names_the_windows_each_app_owns(self):
        window = _NamedNode(name="Export Image as JPEG", role="frame")
        app = _NamedNode(name="gimp", role="application", children=[window])
        # node_pid 直接取 get_process_id，假节点没有这个方法就会抛
        # AttributeError，而候选表的构造整个包在 try 里——异常会把它清空，
        # 测试于是看到一条光秃秃的 appNotFound，查半天才发现是假节点的锅。
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
        # 光有 "gimp" 不够——要能看出那个对话框归它管。
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
    """render_visible_cells 必须拿到 indexer——漏传时它整条路径都会崩。

    出处是第 108 题（LibreOffice Calc 算年龄）的真实轨迹：get_app_state
    连着两次返回 `name 'indexer' is not defined`，agent 只好整题改用 Bash。

    根因是作用域：render_visible_cells 是**模块级函数**，看不到 render_tree
    的局部变量 indexer，而它函数体里直接用了这个名字。Python 直到真正执行到
    那一行才报 NameError，所以静态上毫无征兆——而那一行正是 LibreOffice Calc
    表格的渲染路径（自管理容器拒绝枚举后的替代路径）。
    """

    def test_signature_accepts_indexer(self):
        import inspect
        params = inspect.signature(runtime.render_visible_cells).parameters
        self.assertIn("indexer", params,
                      "render_visible_cells 必须显式接收 indexer，"
                      "否则函数体里那个自由名字会在运行时炸")

    def test_call_site_passes_indexer(self):
        # 光有参数不够：调用点漏传的话，默认值 None 会让单元格退回遍历序号，
        # 下标就不再跨快照稳定了——那是另一种更隐蔽的错。
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
