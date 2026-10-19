package main

import (
	"strings"
	"testing"
)

const (
	untilMainHandle   = "w2:0123456789abcdef0123456789abcdef"
	untilDialogHandle = "w2:fedcba9876543210fedcba9876543210"
)

type untilFakeWindow struct {
	handle, role, title, path string
	modal                     bool
}

// appWideBackend is one application with several top-level windows. Like
// the Linux provider, an observe reads one window (the main one when no
// handle is given) and lists them all; a window list filtered by handle
// returns only that window's title.
type appWideBackend struct {
	recordingPlatformBackend
	open          []untilFakeWindow
	appGone       bool
	appGoneFacts  bool
	listRequests  []ProviderWindowListRequest
	observedHands []string
}

func untilMainWindow() untilFakeWindow {
	return untilFakeWindow{handle: untilMainHandle, role: "frame", title: "Sheet", path: "/window/1"}
}

func untilDialogWindow() untilFakeWindow {
	return untilFakeWindow{handle: untilDialogHandle, role: "dialog", title: "Insert Sheet", path: "/window/2", modal: true}
}

func (b *appWideBackend) goneFailure() *ProviderFailure {
	return &ProviderFailure{Code: "stale_app_ref", Phase: "precondition", Subject: "app", FactsComplete: b.appGoneFacts}
}

func (b *appWideBackend) snapshotFor(selected untilFakeWindow) *ProviderSnapshot {
	snapshot := mixedOrderedInputBatchTestSnapshot()
	snapshot.WindowHandle = selected.handle
	snapshot.WindowTitle = selected.title
	snapshot.SnapshotID = "until-" + selected.path
	for index := range snapshot.Elements {
		element := &snapshot.Elements[index]
		element.ProviderWindowObjectPath = selected.path
		element.ProviderWindowRole = selected.role
		element.ProviderWindowName = selected.title
	}
	snapshot.Elements[3].ProviderObjectPath = selected.path + "/save"
	// The root element is the window itself.
	snapshot.Elements[0].ProviderBusName = ":1.84"
	snapshot.Elements[0].ProviderObjectPath = selected.path
	if selected.role == "dialog" {
		snapshot.Elements[0].ControlType = "dialog"
		snapshot.Elements[0].Name = selected.title
		snapshot.Elements[3].Name = "Insert"
		snapshot.TreeLines[0] = "\t0 dialog \"Insert Sheet\""
		snapshot.TreeLines[3] = "\t\t\t3 push button \"Insert\" [click]"
	}
	for _, window := range b.open {
		snapshot.Windows = append(snapshot.Windows, topLevelWindow{
			Title: window.title, Role: window.role, Handle: window.handle,
			ProviderBusName: ":1.84", ProviderObjectPath: window.path,
			ProviderProcessGeneration: "pid:10:start:100",
			Selected:                  window.handle == selected.handle,
			Modal:                     window.modal, Active: window.modal, Showing: true,
		})
	}
	return snapshot
}

func (b *appWideBackend) Observe(request ProviderObserveRequest) (ProviderAccessibilityResult, error) {
	b.observeRequests = append(b.observeRequests, request)
	b.observedHands = append(b.observedHands, request.WindowHandle)
	if b.appGone {
		result := ProviderAccessibilityResult{OK: false, Error: "stale_app_ref: gone", Failure: b.goneFailure()}
		result.prepareForService()
		return result, nil
	}
	for _, window := range b.open {
		if request.WindowHandle == window.handle || (request.WindowHandle == "" && window.role != "dialog") {
			return ProviderAccessibilityResult{OK: true, Snapshot: b.snapshotFor(window)}, nil
		}
	}
	result := ProviderAccessibilityResult{OK: false, Error: "stale_window_ref: the referenced logical window no longer exists.",
		Failure: &ProviderFailure{Code: "stale_window_ref", Phase: "precondition", Subject: "window", FactsComplete: true}}
	result.prepareForService()
	return result, nil
}

func (b *appWideBackend) ListWindows(request ProviderWindowListRequest) (ProviderWindowResult, error) {
	b.listRequests = append(b.listRequests, request)
	if b.appGone {
		return ProviderWindowResult{OK: false, Error: "stale_app_ref: gone", Failure: b.goneFailure()}, nil
	}
	titles := []string{}
	for _, window := range b.open {
		if request.WindowHandle == "" || request.WindowHandle == window.handle {
			titles = append(titles, window.title)
		}
	}
	if request.WindowHandle != "" && len(titles) == 0 {
		return ProviderWindowResult{OK: false, Error: "stale_window_ref: the referenced logical window no longer exists.",
			Failure: &ProviderFailure{Code: "stale_window_ref", Phase: "precondition", Subject: "window", FactsComplete: true}}, nil
	}
	return ProviderWindowResult{OK: true, AppName: "LibreOffice", AppPID: 10, WindowTitles: titles}, nil
}

func untilScopeService(t *testing.T, open ...untilFakeWindow) (*service, *appWideBackend, *a11yBatchContext) {
	t.Helper()
	s, appRef, windowRef, _ := sheetBatchService(t)
	backend := &appWideBackend{open: open}
	s.backend = backend
	return s, backend, &a11yBatchContext{
		appRef: appRef, appToken: appRef, windowRef: windowRef, windowHandle: untilMainHandle,
	}
}

func untilStatus(result toolCallResult) string {
	status, _ := result.StructuredContent["assertion_status"].(string)
	return status
}

// Shape 1: the batch acts in the document window and the awaited dialog is
// another top-level window of the same application (panel tasks 1d17d234,
// 04d9aeaf, a434992a).
func TestSelectorUntilSeesDialogInAnotherWindowOfTheApp(t *testing.T) {
	s, backend, context := untilScopeService(t, untilMainWindow(), untilDialogWindow())
	for _, selector := range []map[string]any{
		{"role": "dialog"},
		{"role": "button", "name": "Insert"},
	} {
		result := s.runA11yPostcondition(context, map[string]any{
			"type": "exists", "target": map[string]any{"selector": selector},
			"value": true, "timeout_ms": 1,
		})
		if result.IsError || untilStatus(result) != "pass" {
			t.Fatalf("%v in the dialog window did not satisfy until: %s", selector, resultText(result))
		}
	}
	for _, request := range backend.actionRequests {
		t.Fatalf("until sent input: %#v", request)
	}
}

// Full-e a10b69e1 #19: Thunderbird exposes its modal "New Folder" dialog
// as a top-level frame, so {"role":"dialog"} never matched although the
// next observe showed blocking=modal_dialog. A modal top-level window
// counts as a dialog; a non-modal one does not.
func TestDialogSelectorMatchesAModalTopLevelWindow(t *testing.T) {
	newFolder := untilFakeWindow{handle: untilDialogHandle, role: "frame", title: "New Folder", path: "/window/2", modal: true}
	exists := func(value bool) map[string]any {
		return map[string]any{"type": "exists", "value": value, "timeout_ms": 1,
			"target": map[string]any{"selector": map[string]any{"role": "dialog"}}}
	}
	s, _, context := untilScopeService(t, untilMainWindow(), newFolder)
	if result := s.runA11yPostcondition(context, exists(true)); result.IsError || untilStatus(result) != "pass" {
		t.Fatalf("modal frame must satisfy role dialog: %s", resultText(result))
	}
	context.windowHandle = untilDialogHandle
	if result := s.runA11yPostcondition(context, exists(true)); result.IsError || untilStatus(result) != "pass" {
		t.Fatalf("modal frame bound as the batch window must satisfy role dialog: %s", resultText(result))
	}
	plain := newFolder
	plain.modal = false
	s, _, context = untilScopeService(t, untilMainWindow(), plain)
	if result := s.runA11yPostcondition(context, exists(true)); untilStatus(result) == "pass" {
		t.Fatalf("a non-modal frame is not a dialog: %s", resultText(result))
	}
	s, _, context = untilScopeService(t, untilMainWindow())
	if result := s.runA11yPostcondition(context, exists(false)); result.IsError || untilStatus(result) != "pass" {
		t.Fatalf("no modal window left: %s", resultText(result))
	}
}

// A negative selector is true only when no window of the app matches.
func TestNegativeSelectorUntilChecksEveryWindowOfTheApp(t *testing.T) {
	s, _, context := untilScopeService(t, untilMainWindow(), untilDialogWindow())
	result := s.runA11yPostcondition(context, map[string]any{
		"type": "exists", "target": map[string]any{"selector": map[string]any{"role": "dialog"}},
		"value": false, "timeout_ms": 1,
	})
	if result.IsError || untilStatus(result) == "pass" {
		t.Fatalf("a dialog still open in another window must keep exists:false false: %s", resultText(result))
	}
}

// A selector found in the bound window costs one read, as before.
func TestSelectorUntilMatchedInBoundWindowReadsOneWindow(t *testing.T) {
	s, backend, context := untilScopeService(t, untilMainWindow(), untilDialogWindow())
	result := s.runA11yPostcondition(context, map[string]any{
		"type": "exists", "target": map[string]any{"selector": map[string]any{"role": "button", "name": "Save"}},
		"value": true, "timeout_ms": 1,
	})
	if untilStatus(result) != "pass" || len(backend.observedHands) != 1 {
		t.Fatalf("bound-window match: status=%s reads=%v", untilStatus(result), backend.observedHands)
	}
}

// A false selector until names the dialog that has focus in its hint.
func TestFalseSelectorUntilHintNamesTheFocusedDialog(t *testing.T) {
	s, _, context := untilScopeService(t, untilMainWindow(), untilDialogWindow())
	result := s.runA11yPostcondition(context, map[string]any{
		"type": "exists", "target": map[string]any{"selector": map[string]any{"role": "button", "name": "Never"}},
		"value": true, "timeout_ms": 1,
	})
	ref, _ := result.StructuredContent["until_window_ref"].(string)
	if untilStatus(result) != "predicate_false" || !isShortAXWindowRef(ref) || ref == context.windowRef {
		t.Fatalf("false until: status=%s window=%q", untilStatus(result), ref)
	}
	hint := untilWindowHint(map[string]any{"until_window_ref": ref})
	public := axPublicEvidence(map[string]any{
		"failure_code": "action_postcondition_not_satisfied", "until_window_ref": ref,
	}, true, nil)
	if hint != `{"mode":"observe","window_ref":"`+ref+`"}` || public["hint"] != hint {
		t.Fatalf("hint %q public %#v", hint, public)
	}
	generic := axPublicEvidence(map[string]any{"failure_code": "action_postcondition_not_satisfied"}, true, nil)
	if generic["hint"] != axPublicHints[axCodeUntilFailed] {
		t.Fatalf("without a known window the generic hint stays: %#v", generic)
	}
}

// Shape 2: a title until for a new window is evaluated against every window
// of the app, not the batch window (tasks a434992a, 8ba5ae7a).
func TestTitleUntilSeesANewWindowOfTheApp(t *testing.T) {
	s, backend, context := untilScopeService(t, untilMainWindow(), untilDialogWindow())
	result := s.runA11yPostcondition(context, map[string]any{
		"type": "window_title_contains", "text": "Insert Sheet", "exists": true, "timeout_ms": 1,
	})
	if result.IsError || untilStatus(result) != "pass" {
		t.Fatalf("dialog title did not satisfy until: %s", resultText(result))
	}
	for _, request := range backend.listRequests {
		if request.WindowHandle != "" {
			t.Fatalf("title until was narrowed to one window: %#v", request)
		}
	}
}

// Shape 2, negate: the batch window is the dialog that the action closed
// (tasks 9195653c, 8ba5ae7a, 7767eef2). Its handle is stale, yet the app's
// complete inventory answers the predicate.
func TestNegatedTitleUntilAfterTheBatchDialogClosed(t *testing.T) {
	s, _, context := untilScopeService(t, untilMainWindow())
	context.windowHandle = untilDialogHandle
	result := s.runA11yPostcondition(context, map[string]any{
		"type": "window_title_contains", "text": "Insert Sheet", "exists": false, "timeout_ms": 1,
	})
	if result.IsError || untilStatus(result) != "pass" {
		t.Fatalf("closed dialog title must be absent: %s", resultText(result))
	}
	stillOpen, _, context := untilScopeService(t, untilMainWindow(), untilDialogWindow())
	context.windowHandle = untilDialogHandle
	result = stillOpen.runA11yPostcondition(context, map[string]any{
		"type": "window_title_contains", "text": "Insert Sheet", "exists": false, "timeout_ms": 1,
	})
	if result.IsError || untilStatus(result) == "pass" {
		t.Fatalf("an open dialog must keep the negated title false: %s", resultText(result))
	}
}

// A selector until bound to a window that closed reads the app's remaining
// windows instead of returning unknown.
func TestSelectorUntilAfterTheBatchDialogClosed(t *testing.T) {
	s, _, context := untilScopeService(t, untilMainWindow())
	context.windowHandle = untilDialogHandle
	result := s.runA11yPostcondition(context, map[string]any{
		"type": "exists", "target": map[string]any{"selector": map[string]any{"role": "dialog"}},
		"value": false, "timeout_ms": 1,
	})
	if result.IsError || untilStatus(result) != "pass" {
		t.Fatalf("closed dialog selector must be absent: %s", resultText(result))
	}
}

// Shape 3: closing the app's last window retires its aN; a negated title
// until is still true when the complete inventory has no such process
// (task 0b17a146).
func TestNegatedTitleUntilAfterTheAppExited(t *testing.T) {
	s, backend, context := untilScopeService(t)
	backend.appGone, backend.appGoneFacts = true, true
	s.markAppIdentityLifecycle(context.appRef, AXRefStale, "stale_app_ref")
	assertion := map[string]any{
		"type": "window_title_contains", "text": "H2O_Factsheet_WA.docx", "exists": false, "timeout_ms": 1,
	}
	result := s.runA11yPostcondition(context, assertion)
	if result.IsError || untilStatus(result) != "pass" {
		t.Fatalf("an exited app has no window with that title: %s", resultText(result))
	}
	if _, err := s.resolveShortAXAppRef(context.appRef); err == nil {
		t.Fatal("the absence check revived the retired app ref")
	}
	// The name read fails here (a provider that cannot list a missing app
	// by name), so the poll reads the exact retired process.
	exact := false
	for _, request := range backend.listRequests {
		exact = exact || (request.AppIdentity != nil &&
			request.AppIdentity.ProcessGeneration == "pid:10:start:100")
	}
	if !exact {
		t.Fatalf("the fallback read must name the exact retired process: %#v", backend.listRequests)
	}

	backend.appGoneFacts = false
	result = s.runA11yPostcondition(context, assertion)
	if !result.IsError || untilStatus(result) != "observation_incomplete" {
		t.Fatalf("an incomplete inventory must stay unknown: %s", resultText(result))
	}
}

func TestNegatedSelectorUntilAfterTheAppExited(t *testing.T) {
	s, backend, context := untilScopeService(t)
	backend.appGone, backend.appGoneFacts = true, true
	s.markAppIdentityLifecycle(context.appRef, AXRefStale, "stale_app_ref")
	result := s.runA11yPostcondition(context, map[string]any{
		"type": "exists", "target": map[string]any{"selector": map[string]any{"role": "dialog"}},
		"value": false, "timeout_ms": 1,
	})
	if result.IsError || untilStatus(result) != "pass" {
		t.Fatalf("an exited app has no matching element: %s", resultText(result))
	}
}

// The whole act call: closing the last window with a negated title until
// reports until true, not until_unknown.
func TestActCloseLastWindowWithNegatedTitleUntil(t *testing.T) {
	s, backend, context := untilScopeService(t, untilMainWindow())
	backend.windowResponse = &ProviderWindowResult{
		OK: true, Snapshot: &ProviderSnapshot{WindowAbsent: true, ClosedWindowHandle: untilMainHandle},
		Mutation:       &ProviderMutationFacts{InputEventsEmitted: true, DesktopStateMutated: true},
		ActionEvidence: &ProviderActionEvidence{InputEmitted: true, Route: "native_window_command"},
	}
	closeThenGone := &closingBackend{appWideBackend: backend, appRef: context.appRef, service: s}
	s.backend = closeThenGone
	result := s.callTool(publicToolAX, map[string]any{
		"mode": "act", "actions": []any{map[string]any{"type": "close", "ref": context.windowRef}},
		"until": map[string]any{"type": "title", "text": "Sheet", "negate": true, "timeout_ms": 1.0},
	})
	if line := publicLine(t, result); !strings.HasPrefix(line, `{"ok":true,"until":"true"`) {
		t.Fatalf("evidence line %s\n%s", line, resultText(result))
	}
}

// relaunchBackend models an application that was closed and launched
// again: the old process is gone, and a name lookup sees the new process
// once its window appears.
type relaunchBackend struct {
	appWideBackend
	newTitles []string
	shownFrom int
}

func (b *relaunchBackend) ListWindows(request ProviderWindowListRequest) (ProviderWindowResult, error) {
	b.listRequests = append(b.listRequests, request)
	if request.AppIdentity != nil {
		return ProviderWindowResult{OK: false, Error: "stale_app_ref: gone",
			Failure: &ProviderFailure{Code: "stale_app_ref", Phase: "precondition", Subject: "app", FactsComplete: true}}, nil
	}
	if len(b.listRequests) < b.shownFrom {
		// The complete desktop inventory has no such application yet.
		return ProviderWindowResult{OK: true}, nil
	}
	return ProviderWindowResult{OK: true, AppName: "LibreOffice", AppPID: 11, WindowTitles: b.newTitles}, nil
}

// Tasks 7ae48c60 #34 and 7767eef2 #15 (full-e), 0b17a146 #16 (full-d): the
// app was closed and launched again. A title until bound to the old
// process never saw the new window; it must follow the app name.
func TestTitleUntilFollowsTheAppNameAcrossARelaunch(t *testing.T) {
	s, _, context := untilScopeService(t)
	s.markAppIdentityLifecycle(context.appRef, AXRefStale, "stale_app_ref")
	backend := &relaunchBackend{newTitles: []string{"30_1.pptx - LibreOffice Impress"}, shownFrom: 3}
	s.backend = backend
	result := s.runA11yPostcondition(context, map[string]any{
		"type": "window_title_contains", "text": "30_1.pptx", "exists": true, "timeout_ms": 2000,
	})
	if result.IsError || untilStatus(result) != "pass" {
		t.Fatalf("the relaunched window was not seen: %s", resultText(result))
	}
	for _, request := range backend.listRequests {
		if request.App != "LibreOffice" || request.AppIdentity != nil {
			t.Fatalf("a poll was bound to the old process: %#v", backend.listRequests)
		}
	}
}

// A complete inventory in which the title never appears answers the until:
// false, not unknown.
func TestTitleUntilNeverShownInACompleteInventoryIsFalse(t *testing.T) {
	s, _, context := untilScopeService(t)
	backend := &relaunchBackend{shownFrom: 1 << 30}
	s.backend = backend
	result := s.runA11yPostcondition(context, map[string]any{
		"type": "window_title_contains", "text": "30_1.pptx", "exists": true, "timeout_ms": 1,
	})
	if result.IsError || untilStatus(result) != "window_title_timeout" {
		t.Fatalf("a complete inventory without the title must be until false: %s", resultText(result))
	}
	negated := s.runA11yPostcondition(context, map[string]any{
		"type": "window_title_contains", "text": "30_1.pptx", "exists": false, "timeout_ms": 1,
	})
	if negated.IsError || untilStatus(negated) != "pass" {
		t.Fatalf("the negated title must be true: %s", resultText(negated))
	}
}

// dialogOKBackend closes the dialog when the batch presses its OK button.
type dialogOKBackend struct {
	appWideBackend
}

func (b *dialogOKBackend) ResolveAndAct(request ProviderActionRequest) (ProviderAccessibilityResult, error) {
	b.actionRequests = append(b.actionRequests, request)
	b.open = []untilFakeWindow{untilMainWindow()}
	return ProviderAccessibilityResult{OK: true, Snapshot: b.snapshotFor(untilMainWindow()),
		Mutation:       &ProviderMutationFacts{InputEventsEmitted: true},
		ActionEvidence: &ProviderActionEvidence{InputEmitted: true, Route: "keyboard_synthesis"}}, nil
}

// Full-d a434992a #10/#20 and 04d9aeaf #9: the batch acts in a dialog and
// presses OK, then waits on the main window title (or on the dialog title
// to go). The title read was bound to the batch window, the dialog that the
// action closed, so every poll hit stale_window_ref and the until ended
// unknown although the main window title was there.
func TestTitleUntilAfterTheBatchClosedItsDialog(t *testing.T) {
	for _, until := range []map[string]any{
		{"type": "title", "text": "Sheet", "timeout_ms": 1.0},
		{"type": "title", "text": "Insert Sheet", "negate": true, "timeout_ms": 1.0},
	} {
		s, _, context := untilScopeService(t, untilMainWindow(), untilDialogWindow())
		backend := &dialogOKBackend{appWideBackend{open: []untilFakeWindow{untilMainWindow(), untilDialogWindow()}}}
		s.backend = backend
		dialog := s.windowRefRegistry.Acquire("libreoffice", untilDialogHandle, `dialog "Insert Sheet"`, "c1",
			":1.84", "/window/2", "pid:10:start:100", "dialog", "Insert Sheet", context.appRef)
		for _, form := range []map[string]any{
			{"actions": []any{map[string]any{"type": "keypress", "keys": []any{"ENTER"}}}, "until": until},
			{"actions": []any{map[string]any{"type": "keypress", "keys": []any{"ENTER"}, "until": until}}},
		} {
			backend.open = []untilFakeWindow{untilMainWindow(), untilDialogWindow()}
			form["mode"], form["window_ref"], form["observe"] = "act", dialog, "none"
			result := s.callTool(publicToolAX, form)
			if line := publicLine(t, result); !strings.HasPrefix(line, `{"ok":true`) ||
				!strings.Contains(line, `"until_verified":true`) && !strings.Contains(line, `"until":"true"`) {
				t.Fatalf("until %v after the dialog closed: %s\n%s", until, line, resultText(result))
			}
			for _, request := range backend.listRequests {
				if request.WindowHandle != "" {
					t.Fatalf("the title read was bound to one window: %#v", request)
				}
			}
		}
	}
}

// closingBackend closes the app with its last window, the way LibreOffice
// exits when its last document closes.
type closingBackend struct {
	*appWideBackend
	appRef  string
	service *service
}

func (b *closingBackend) ActOnWindow(request ProviderWindowActionRequest) (ProviderWindowResult, error) {
	result, err := b.appWideBackend.ActOnWindow(request)
	b.open = nil
	b.appGone, b.appGoneFacts = true, true
	b.service.markAppIdentityLifecycle(b.appRef, AXRefStale, "stale_app_ref")
	return result, err
}

// Model-visible AX output speaks the current interface: until, not the
// removed postcondition field, in the evidence line and in the text.
func TestFailedUntilOutputNamesNoRemovedField(t *testing.T) {
	s, _, window := untilTitleService(t, "Sheet")
	result := s.callTool(publicToolAX, map[string]any{
		"mode": "act", "window_ref": window,
		"actions": []any{
			map[string]any{"type": "keypress", "keys": []any{"ENTER"},
				"until": map[string]any{"type": "title", "text": "Dialog", "timeout_ms": 1.0}},
			map[string]any{"type": "type", "text": "must not run"},
		},
	})
	text := resultText(withAXTextEvidence(prepareAXEvidence(result)))
	for _, removed := range []string{"postcondition", "assert_ax", "observation_target", "app_ref", "target.ref"} {
		if strings.Contains(text, removed) {
			t.Fatalf("model-visible output names %q:\n%s", removed, text)
		}
	}
	if !strings.Contains(text, `"action_until":`) || !strings.Contains(text, "action 1 until:") {
		t.Fatalf("until evidence missing:\n%s", text)
	}
}
