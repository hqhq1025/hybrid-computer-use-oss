package main

import (
	"strings"
	"testing"
)

// windowScriptBackend answers apps listings from a script, one inventory per
// call (the last repeats), and records every other provider call.
type windowScriptBackend struct {
	*recordingPlatformBackend
	inventories [][]appDescriptor
	listings    int
}

func (b *windowScriptBackend) ObserveApps(ProviderObserveAppsRequest) (ProviderAccessibilityResult, error) {
	index := b.listings
	if index >= len(b.inventories) {
		index = len(b.inventories) - 1
	}
	b.listings++
	return ProviderAccessibilityResult{OK: true, Apps: b.inventories[index]}, nil
}

const (
	testHandleReport = "w2:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testHandleNotes  = "w2:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testHandleMail   = "w2:cccccccccccccccccccccccccccccccc"
)

type testWindow struct {
	handle, title string
	active        bool
}

func editorApps(windows ...testWindow) []appDescriptor {
	editor := appDescriptor{
		Name: "editor", BundleIdentifier: "editor", PID: 10, InputCapable: true,
		ProviderBusName: ":1.10", ProcessGeneration: "pid:10:start:1",
	}
	mail := appDescriptor{
		Name: "mail", BundleIdentifier: "mail", PID: 20, InputCapable: true,
		ProviderBusName: ":1.20", ProcessGeneration: "pid:20:start:1",
	}
	panel := appDescriptor{Name: "panel", PID: 30, InputCapable: false}
	for _, window := range windows {
		entry := providerAppWindow{
			Title: window.title, Role: "frame", Handle: window.handle,
			ProviderBusName: ":1.10", ProviderObjectPath: "/w/" + window.title,
			ProcessGeneration: "pid:10:start:1", Active: window.active, InputTarget: true,
		}
		if window.handle == testHandleMail {
			entry.ProviderBusName = ":1.20"
			entry.ProcessGeneration = "pid:20:start:1"
			mail.Windows = append(mail.Windows, entry)
			continue
		}
		editor.Windows = append(editor.Windows, entry)
	}
	return []appDescriptor{editor, mail, panel}
}

func windowScriptService(inventories ...[]appDescriptor) (*service, *windowScriptBackend) {
	backend := &windowScriptBackend{
		recordingPlatformBackend: &recordingPlatformBackend{},
		inventories:              inventories,
	}
	return newServiceWithBackend(backend), backend
}

func TestAppsListsEveryWindowWithRefTitleAppAndActive(t *testing.T) {
	service, _ := windowScriptService(editorApps(
		testWindow{testHandleReport, "Quarterly report - Editor", true},
		testWindow{testHandleNotes, "Notes - Editor", false},
		testWindow{testHandleMail, "Inbox - Mail", false},
	))
	text := resultText(service.listApps())
	for _, want := range []string{
		`w1 "Quarterly report - Editor"  app=editor a1  active`,
		`w2 "Notes - Editor"  app=editor a1`,
		`w3 "Inbox - Mail"  app=mail a2`,
		"(not input targets: panel)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("apps output lacks %q:\n%s", want, text)
		}
	}
	// The same window keeps its ref on the next listing.
	if again := resultText(service.listApps()); !strings.Contains(again, `w1 "Quarterly report - Editor"`) {
		t.Fatalf("window ref changed between listings:\n%s", again)
	}
}

func TestAppsSkipsWindowsWithoutStrongHandle(t *testing.T) {
	service, _ := windowScriptService(editorApps(
		testWindow{"", "Untracked - Editor", true},
	))
	text := resultText(service.listApps())
	if !strings.Contains(text, `"Untracked - Editor"  app=editor a1  (not addressable)`) ||
		strings.Contains(text, "w1 ") {
		t.Fatalf("unaddressable window got a ref:\n%s", text)
	}
}

func TestActivateByTitleResolvesExactlyOneWindowBeforeInput(t *testing.T) {
	inventory := editorApps(
		testWindow{testHandleReport, "Quarterly report - Editor", false},
		testWindow{testHandleNotes, "Notes - Editor", true},
		testWindow{testHandleMail, "Inbox - Mail", false},
	)
	for name, test := range map[string]struct {
		title string
		code  string
		want  []string
	}{
		"none": {"Budget", axCodeWindowNotFound, []string{
			`no visible window title contains "Budget"`, `w3 "Inbox - Mail"`}},
		"many": {"editor", axCodeAmbiguousWindow, []string{
			`2 windows match title "editor"`, `w1 "Quarterly report - Editor"`, `w2 "Notes - Editor"`}},
	} {
		service, backend := windowScriptService(inventory)
		result := service.callTool(publicToolAX, map[string]any{
			"mode": "act", "actions": []any{
				map[string]any{"type": "activate", "title": test.title},
			},
		})
		if !result.IsError || result.StructuredContent[axPublicErrorCodeKey] != test.code ||
			result.StructuredContent[axPublicErrorPathKey] != "actions[0].title" {
			t.Fatalf("%s: %#v", name, result.StructuredContent)
		}
		for _, want := range test.want {
			if !strings.Contains(resultText(result), want) {
				t.Fatalf("%s: message lacks %q: %s", name, want, resultText(result))
			}
		}
		if len(backend.calls) != 0 || len(backend.actionRequests) != 0 {
			t.Fatalf("%s: input reached the provider: %v", name, backend.calls)
		}
		prepared := withAXTextEvidence(prepareAXEvidence(result))
		if !strings.HasPrefix(prepared.Content[0].Text, `{"ok":false,"code":"`+test.code+`","path":"actions[0].title"`) ||
			!strings.Contains(prepared.Content[0].Text, `"hint":`) {
			t.Fatalf("%s: evidence line %s", name, prepared.Content[0].Text)
		}
	}

	service, _ := windowScriptService(inventory)
	window, err := service.resolveWindowTitle("QUARTERLY")
	if err != nil || window.ref != "w1" || window.handle != testHandleReport {
		t.Fatalf("one case-insensitive match must resolve to w1: %#v %#v", window, err)
	}
}

func TestTitleResolvesOnceAndNeverRebinds(t *testing.T) {
	// The window list is read once, before input. A later listing in which
	// the resolved window is gone and another window has the same title
	// must not move the batch to that other window.
	before := editorApps(testWindow{testHandleReport, "Report - Editor", true})
	after := editorApps(testWindow{testHandleNotes, "Report - Editor", true})
	service, backend := windowScriptService(before, after)
	actions, argErr := decodeAXActions([]any{
		map[string]any{"type": "activate", "title": "Report"},
	})
	if argErr != nil {
		t.Fatal(argErr)
	}
	backend.err = errNoWindow
	result := service.a11yBatchDecoded(actions, observation{mode: observeNone}, nil, "")
	if backend.listings != 1 {
		t.Fatalf("title was resolved %d times, want once", backend.listings)
	}
	if !strings.Contains(resultText(result), `title "Report" -> w1 "Report - Editor" (editor)`) {
		t.Fatalf("resolution note missing: %s", resultText(result))
	}
	if actions[0].targetRef != "w1" {
		t.Fatalf("title resolved to %q", actions[0].targetRef)
	}
	if len(backend.actionRequests) != 0 {
		t.Fatalf("window action used a semantic action request: %#v", backend.actionRequests)
	}
}

func TestClosedWindowAfterTitleResolutionFailsAsStale(t *testing.T) {
	service, backend := windowScriptService(editorApps(
		testWindow{testHandleReport, "Report - Editor", true},
	))
	window, argErr := service.resolveWindowTitle("Report")
	if argErr != nil {
		t.Fatal(argErr)
	}
	// The window closes after resolution; its ref becomes stale. The batch
	// must fail on the ref, not search titles again.
	service.windowRefRegistry.MarkLifecycle(window.ref, AXRefStale, "window_closed")
	result := service.a11yBatchBound([]a11yBatchAction{{actionType: "close", targetRef: window.ref}},
		observation{mode: observeNone}, nil, "", nil, 1)
	if !result.IsError || backend.listings != 1 || len(backend.calls) != 0 {
		t.Fatalf("closed window was not reported stale before input: %#v listings=%d calls=%v",
			result.StructuredContent, backend.listings, backend.calls)
	}
	if code := axPublicErrorCode(result.StructuredContent); code != axCodeStaleRef {
		t.Fatalf("code=%q want stale_ref: %#v", code, result.StructuredContent)
	}
}

func TestRefLessKeysBindToTheFocusedWindow(t *testing.T) {
	service, _ := windowScriptService(editorApps(
		testWindow{testHandleReport, "Report - Editor", false},
		testWindow{testHandleNotes, "Notes - Editor", true},
	))
	focus, argErr := service.resolveFocusedWindow()
	if argErr != nil || focus.ref != "w2" {
		t.Fatalf("focused window = %#v %#v", focus, argErr)
	}
	context, err := service.deriveA11yBatchContext("", []a11yBatchAction{
		{actionType: "keypress", key: "CTRL+S"},
		{actionType: "type", text: "x"},
	}, focus)
	if err != nil || context.windowRef != "w2" || context.windowHandle != testHandleNotes ||
		!context.boundByFocus || context.expectedWindow == nil {
		t.Fatalf("focus binding = %#v %v", context, err)
	}
}

func TestRefLessKeysWithoutFocusedWindowFailBeforeInput(t *testing.T) {
	service, backend := windowScriptService(editorApps(
		testWindow{testHandleReport, "Report - Editor", false},
	))
	result := service.callTool(publicToolAX, map[string]any{
		"mode": "act", "actions": []any{
			map[string]any{"type": "keypress", "keys": []any{"ENTER"}},
		},
	})
	if !result.IsError || result.StructuredContent[axPublicErrorCodeKey] != axCodeNoFocusedWindow ||
		len(backend.calls) != 0 {
		t.Fatalf("no focused window: %#v calls=%v", result.StructuredContent, backend.calls)
	}
	if !strings.Contains(resultText(result), `w1 "Report - Editor"`) ||
		result.StructuredContent[axPublicErrorHintKey] == "" {
		t.Fatalf("no_focused_window must list windows and hint: %s", resultText(result))
	}
}

func TestFocusChangeBeforeFirstActionStopsWithoutInput(t *testing.T) {
	// Preflight sees w2 focused; by the time the first key is due, focus has
	// moved to w1.
	service, backend := windowScriptService(
		editorApps(
			testWindow{testHandleReport, "Report - Editor", false},
			testWindow{testHandleNotes, "Notes - Editor", true},
		),
		editorApps(
			testWindow{testHandleReport, "Report - Editor", true},
			testWindow{testHandleNotes, "Notes - Editor", false},
		),
	)
	actions, _ := decodeAXActions([]any{
		map[string]any{"type": "keypress", "keys": []any{"ENTER"}},
	})
	result := service.a11yBatchDecoded(actions, observation{mode: observeNone}, nil, "")
	if !result.IsError || result.StructuredContent[axPublicErrorCodeKey] != axCodeWindowChanged ||
		len(backend.actionRequests) != 0 {
		t.Fatalf("focus change before input: %#v requests=%d", result.StructuredContent, len(backend.actionRequests))
	}
	public := axPublicEvidence(result.StructuredContent, true, result.Content)
	if public["input_emitted"] != false || public["completed"] != 0 || public["requested"] != 1 {
		t.Fatalf("public counts: %#v", public)
	}
	if !strings.Contains(resultText(result), `focus is now on w1 "Report - Editor"`) {
		t.Fatalf("message must name the new focus: %s", resultText(result))
	}
}

func TestFocusChangeAfterActionKStopsAtKPlusOne(t *testing.T) {
	focused := editorApps(
		testWindow{testHandleReport, "Report - Editor", true},
	)
	moved := editorApps(
		testWindow{testHandleReport, "Report - Editor", false},
		testWindow{testHandleMail, "Inbox - Mail", true},
	)
	// Listings: preflight, check before action 1, check before action 2
	// (still focused), check before action 3 (moved).
	service, backend := windowScriptService(focused, focused, focused, moved)
	backend.accessibilityResponse = &ProviderAccessibilityResult{
		OK: true, Snapshot: &appSnapshot{
			App: appDescriptor{Name: "editor", BundleIdentifier: "editor", PID: 10,
				ProviderBusName: ":1.10", ProcessGeneration: "pid:10:start:1"},
			WindowTitle:  "Report - Editor",
			WindowHandle: testHandleReport,
			SnapshotID:   "report",
			TreeLines:    []string{"\t0 frame \"Report - Editor\""},
			Elements: []elementRecord{{
				Index: 0, RuntimeID: []int{0}, ControlType: "frame", Name: "Report - Editor",
				ProviderWindowBusName: ":1.10", ProviderWindowObjectPath: "/w/Report - Editor",
				ProviderWindowProcessGeneration: "pid:10:start:1", ProviderWindowRole: "frame",
				ProviderWindowName: "Report - Editor",
			}},
		},
		Mutation:       &ProviderMutationFacts{InputEventsEmitted: true},
		ActionEvidence: &ProviderActionEvidence{InputEmitted: true, Route: "keyboard_synthesis"},
	}
	actions, _ := decodeAXActions([]any{
		map[string]any{"type": "keypress", "keys": []any{"A"}},
		map[string]any{"type": "keypress", "keys": []any{"B"}},
		map[string]any{"type": "keypress", "keys": []any{"C"}},
	})
	result := service.a11yBatchDecoded(actions, observation{mode: observeNone}, nil, "")
	if !result.IsError || result.StructuredContent[axPublicErrorCodeKey] != axCodeWindowChanged {
		t.Fatalf("focus change after action 2: %#v", result.StructuredContent)
	}
	if len(backend.actionRequests) != 2 {
		t.Fatalf("sent %d keys, want 2 before the stop", len(backend.actionRequests))
	}
	for _, request := range backend.actionRequests {
		if request.WindowHandle != testHandleReport || request.ExpectedWindow == nil {
			t.Fatalf("key not bound to the focused window: %#v", request)
		}
	}
	public := axPublicEvidence(result.StructuredContent, true, result.Content)
	if public["completed"] != 2 || public["requested"] != 3 || public["input_emitted"] != true {
		t.Fatalf("public counts: %#v", public)
	}
}

var errNoWindow = errorString("window action not modeled in this test")

type errorString string

func (e errorString) Error() string { return string(e) }

func TestPublicCountsReadTopLevelStopFacts(t *testing.T) {
	public := axPublicEvidence(map[string]any{
		"failure_code": "stale_target", "failure_phase": "between_actions",
		"actions_requested": 2, "actions_completed_without_call_error": 1,
		"input_events_emitted": true,
	}, true, []contentItem{
		{Type: "text", Text: `bound_window: w2 "Doc" (app, focused at preflight)`},
		{Type: "text", Text: "batch_window_changed: action 1/2 completed"},
	})
	if public["code"] != axCodeWindowChanged || public["completed"] != 1 ||
		public["requested"] != 2 || public["message"] != "batch_window_changed: action 1/2 completed" {
		t.Fatalf("public evidence = %#v", public)
	}
}
