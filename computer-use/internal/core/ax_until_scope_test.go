package core

import (
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
		snapshot.Windows = append(snapshot.Windows, TopLevelWindow{
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
		result.PrepareForService()
		return result, nil
	}
	for _, window := range b.open {
		if request.WindowHandle == window.handle || (request.WindowHandle == "" && window.role != "dialog") {
			return ProviderAccessibilityResult{OK: true, Snapshot: b.snapshotFor(window)}, nil
		}
	}
	result := ProviderAccessibilityResult{OK: false, Error: "stale_window_ref: the referenced logical window no longer exists.",
		Failure: &ProviderFailure{Code: "stale_window_ref", Phase: "precondition", Subject: "window", FactsComplete: true}}
	result.PrepareForService()
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

func untilScopeService(t *testing.T, open ...untilFakeWindow) (*Service, *appWideBackend, *AXBatchContext) {
	t.Helper()
	s, appRef, windowRef, _ := sheetBatchService(t)
	backend := &appWideBackend{open: open}
	s.Backend = backend
	return s, backend, &AXBatchContext{
		AppRef: appRef, AppToken: appRef, WindowRef: windowRef, WindowHandle: untilMainHandle,
	}
}

func untilStatus(result ToolCallResult) string {
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
		result := s.runAXPostcondition(context, map[string]any{
			"type": "exists", "target": map[string]any{"selector": selector},
			"value": true, "timeout_ms": 1,
		})
		if result.IsError || untilStatus(result) != "pass" {
			t.Fatalf("%v in the dialog window did not satisfy until: %s", selector, ResultText(result))
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
	if result := s.runAXPostcondition(context, exists(true)); result.IsError || untilStatus(result) != "pass" {
		t.Fatalf("modal frame must satisfy role dialog: %s", ResultText(result))
	}
	context.WindowHandle = untilDialogHandle
	if result := s.runAXPostcondition(context, exists(true)); result.IsError || untilStatus(result) != "pass" {
		t.Fatalf("modal frame bound as the batch window must satisfy role dialog: %s", ResultText(result))
	}
	plain := newFolder
	plain.modal = false
	s, _, context = untilScopeService(t, untilMainWindow(), plain)
	if result := s.runAXPostcondition(context, exists(true)); untilStatus(result) == "pass" {
		t.Fatalf("a non-modal frame is not a dialog: %s", ResultText(result))
	}
	s, _, context = untilScopeService(t, untilMainWindow())
	if result := s.runAXPostcondition(context, exists(false)); result.IsError || untilStatus(result) != "pass" {
		t.Fatalf("no modal window left: %s", ResultText(result))
	}
}

// A negative selector is true only when no window of the app matches.
func TestNegativeSelectorUntilChecksEveryWindowOfTheApp(t *testing.T) {
	s, _, context := untilScopeService(t, untilMainWindow(), untilDialogWindow())
	result := s.runAXPostcondition(context, map[string]any{
		"type": "exists", "target": map[string]any{"selector": map[string]any{"role": "dialog"}},
		"value": false, "timeout_ms": 1,
	})
	if result.IsError || untilStatus(result) == "pass" {
		t.Fatalf("a dialog still open in another window must keep exists:false false: %s", ResultText(result))
	}
}

// A selector found in the bound window costs one read, as before.
func TestSelectorUntilMatchedInBoundWindowReadsOneWindow(t *testing.T) {
	s, backend, context := untilScopeService(t, untilMainWindow(), untilDialogWindow())
	result := s.runAXPostcondition(context, map[string]any{
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
	result := s.runAXPostcondition(context, map[string]any{
		"type": "exists", "target": map[string]any{"selector": map[string]any{"role": "button", "name": "Never"}},
		"value": true, "timeout_ms": 1,
	})
	ref, _ := result.StructuredContent["until_window_ref"].(string)
	if untilStatus(result) != "predicate_false" || !isShortAXWindowRef(ref) || ref == context.WindowRef {
		t.Fatalf("false until: status=%s window=%q", untilStatus(result), ref)
	}
}

// Shape 2: a title until for a new window is evaluated against every window
// of the app, not the batch window (tasks a434992a, 8ba5ae7a).
func TestTitleUntilSeesANewWindowOfTheApp(t *testing.T) {
	s, backend, context := untilScopeService(t, untilMainWindow(), untilDialogWindow())
	result := s.runAXPostcondition(context, map[string]any{
		"type": "window_title_contains", "text": "Insert Sheet", "exists": true, "timeout_ms": 1,
	})
	if result.IsError || untilStatus(result) != "pass" {
		t.Fatalf("dialog title did not satisfy until: %s", ResultText(result))
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
	context.WindowHandle = untilDialogHandle
	result := s.runAXPostcondition(context, map[string]any{
		"type": "window_title_contains", "text": "Insert Sheet", "exists": false, "timeout_ms": 1,
	})
	if result.IsError || untilStatus(result) != "pass" {
		t.Fatalf("closed dialog title must be absent: %s", ResultText(result))
	}
	stillOpen, _, context := untilScopeService(t, untilMainWindow(), untilDialogWindow())
	context.WindowHandle = untilDialogHandle
	result = stillOpen.runAXPostcondition(context, map[string]any{
		"type": "window_title_contains", "text": "Insert Sheet", "exists": false, "timeout_ms": 1,
	})
	if result.IsError || untilStatus(result) == "pass" {
		t.Fatalf("an open dialog must keep the negated title false: %s", ResultText(result))
	}
}

// A selector until bound to a window that closed reads the app's remaining
// windows instead of returning unknown.
func TestSelectorUntilAfterTheBatchDialogClosed(t *testing.T) {
	s, _, context := untilScopeService(t, untilMainWindow())
	context.WindowHandle = untilDialogHandle
	result := s.runAXPostcondition(context, map[string]any{
		"type": "exists", "target": map[string]any{"selector": map[string]any{"role": "dialog"}},
		"value": false, "timeout_ms": 1,
	})
	if result.IsError || untilStatus(result) != "pass" {
		t.Fatalf("closed dialog selector must be absent: %s", ResultText(result))
	}
}

// Shape 3: closing the app's last window retires its aN; a negated title
// until is still true when the complete inventory has no such process
// (task 0b17a146).
func TestNegatedTitleUntilAfterTheAppExited(t *testing.T) {
	s, backend, context := untilScopeService(t)
	backend.appGone, backend.appGoneFacts = true, true
	s.markAppIdentityLifecycle(context.AppRef, AXRefStale, "stale_app_ref")
	assertion := map[string]any{
		"type": "window_title_contains", "text": "H2O_Factsheet_WA.docx", "exists": false, "timeout_ms": 1,
	}
	result := s.runAXPostcondition(context, assertion)
	if result.IsError || untilStatus(result) != "pass" {
		t.Fatalf("an exited app has no window with that title: %s", ResultText(result))
	}
	if _, err := s.resolveShortAXAppRef(context.AppRef); err == nil {
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
	result = s.runAXPostcondition(context, assertion)
	if !result.IsError || untilStatus(result) != "observation_incomplete" {
		t.Fatalf("an incomplete inventory must stay unknown: %s", ResultText(result))
	}
}

func TestNegatedSelectorUntilAfterTheAppExited(t *testing.T) {
	s, backend, context := untilScopeService(t)
	backend.appGone, backend.appGoneFacts = true, true
	s.markAppIdentityLifecycle(context.AppRef, AXRefStale, "stale_app_ref")
	result := s.runAXPostcondition(context, map[string]any{
		"type": "exists", "target": map[string]any{"selector": map[string]any{"role": "dialog"}},
		"value": false, "timeout_ms": 1,
	})
	if result.IsError || untilStatus(result) != "pass" {
		t.Fatalf("an exited app has no matching element: %s", ResultText(result))
	}
}

// closingBackend closes the app with its last window, the way LibreOffice
// exits when its last document closes.
type closingBackend struct {
	*appWideBackend
	appRef  string
	service *Service
}

func (b *closingBackend) ActOnWindow(request ProviderWindowActionRequest) (ProviderWindowResult, error) {
	result, err := b.appWideBackend.ActOnWindow(request)
	b.open = nil
	b.appGone, b.appGoneFacts = true, true
	b.service.markAppIdentityLifecycle(b.appRef, AXRefStale, "stale_app_ref")
	return result, err
}
