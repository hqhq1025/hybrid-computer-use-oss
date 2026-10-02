package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Inspect the delivered text evidence, including its JSON round trip.
func publicBatchEvidence(t *testing.T, result toolCallResult) map[string]any {
	t.Helper()
	result.StructuredContent["ax_mode"] = a11yModeAct
	visible := withAXTextEvidence(prepareAXEvidence(result))
	wire, err := json.Marshal(visible)
	if err != nil {
		t.Fatal(err)
	}
	var decoded toolCallResult
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, item := range decoded.Content {
		if item.Type == "text" && strings.HasPrefix(item.Text, "[ax] ") {
			var facts map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(item.Text, "[ax] ")), &facts); err != nil {
				t.Fatal(err)
			}
			return facts
		}
	}
	t.Fatalf("missing public AX evidence: %s", wire)
	return nil
}

func assertPublicBatchCounts(t *testing.T, facts map[string]any, requested, completed int) map[string]any {
	t.Helper()
	batch, ok := facts["batch"].(map[string]any)
	if !ok || batch["actions_requested"] != float64(requested) ||
		batch["actions_completed_without_call_error"] != float64(completed) {
		t.Fatalf("public whole-request counts lost: %#v", facts)
	}
	return batch
}

func orderedInputTestService(t *testing.T) (*service, string, string, string) {
	t.Helper()
	s := newService()
	app := s.appRefRegistry.Acquire("LibreOffice", ":1.84", "pid:10:start:100", "c1")
	handle := "w2:0123456789abcdef0123456789abcdef"
	window := s.windowRefRegistry.Acquire("libreoffice", handle, `frame "Sheet"`, "c1",
		":1.84", "/window/1", "pid:10:start:100", "frame", "Sheet", app)
	ref := s.refRegistry.Acquire("libreoffice", handle, `button "Save"`, CanonicalRole("button"), "Save", "c1",
		":1.84", "/window/1/save", "pid:10:start:100",
		":1.84", "/window/1", "pid:10:start:100", "frame", "Sheet", app, window)
	return s, app, window, ref
}

func orderedInputTestSnapshot() *appSnapshot {
	return &appSnapshot{
		App:         appDescriptor{Name: "Editor", PID: 42},
		WindowTitle: "Document", WindowHandle: "0x42", SnapshotID: "ordered-input-test",
		TreeLines: []string{
			"\t0 frame \"Document\"", "\t\t1 panel \"Form\"",
			"\t\t\t2 entry \"Name\" [focused]", "\t\t\t3 push button \"Save\" [click]",
			"\t\t4 status bar \"Ready\"",
		},
		Elements: []elementRecord{
			{Index: 0, RuntimeID: []int{0}, ControlType: "frame", Name: "Document"},
			{Index: 1, RuntimeID: []int{0, 0}, ControlType: "panel", Name: "Form"},
			{Index: 2, RuntimeID: []int{0, 0, 0}, ControlType: "entry", Name: "Name", States: "[focused]"},
			{Index: 3, RuntimeID: []int{0, 0, 1}, ControlType: "push button", Name: "Save", States: "[has-click-action]"},
			{Index: 4, RuntimeID: []int{0, 1}, ControlType: "status bar", Name: "Ready"},
		},
		scope: a11yScopeWindow,
	}
}

func TestOrderedBatchPreflightsEntireRequestBeforeNativeDispatch(t *testing.T) {
	for _, invalid := range []map[string]any{
		{"type": "keypress", "keys": []any{}},
		{"type": "increment", "target": map[string]any{"ref": "r1"}},
		{"type": "decrement", "target": map[string]any{"ref": "r1"}},
		{"type": "keypress", "keys": []any{"ENTER"}, "unsupported": true},
		{"type": "keypress", "keys": []any{"ENTER"}, "postcondition": map[string]any{"type": "invalid"}},
	} {
		s, _, window, _ := orderedInputTestService(t)
		legacy := &recordingPlatformBackend{}
		backend := &orderedInputRecordingBackend{recordingPlatformBackend: legacy}
		s.backend = backend
		result := s.a11yBatch("", []any{
			map[string]any{"type": "keypress", "keys": []any{"A"}}, invalid,
		}, observation{mode: observeNone}, nil, "", window)
		if !result.IsError || len(backend.requests) != 0 || len(legacy.calls) != 0 {
			t.Fatalf("invalid tail reached provider: invalid=%#v result=%#v", invalid, result)
		}
	}
}

func TestOrderedBatchCrossWindowTailRejectedBeforeInput(t *testing.T) {
	s, app, _, ref := orderedInputTestService(t)
	otherWindow := s.windowRefRegistry.Acquire("libreoffice", "w2:11111111111111111111111111111111", `frame "Other"`, "c1",
		":1.84", "/window/2", "pid:10:start:100", "frame", "Other", app)
	legacy := &recordingPlatformBackend{}
	backend := &orderedInputRecordingBackend{recordingPlatformBackend: legacy}
	s.backend = backend
	result := s.a11yBatch("", []any{
		map[string]any{"type": "click", "target": map[string]any{"ref": ref}},
		map[string]any{"type": "keypress", "keys": []any{"ENTER"}},
		map[string]any{"type": "close", "target": map[string]any{"ref": otherWindow}},
	}, observation{mode: observeNone}, nil)
	if !result.IsError || len(backend.requests) != 0 || len(legacy.calls) != 0 {
		t.Fatalf("cross-window batch reached provider: %#v", result)
	}
}

func TestOrderedBatchGuardKeepsExistingAssertionPath(t *testing.T) {
	for _, perAction := range []bool{false, true} {
		s, _, window, _ := orderedInputTestService(t)
		legacy := &recordingPlatformBackend{accessibilityResponse: &ProviderAccessibilityResult{
			OK: true, Snapshot: mixedOrderedInputBatchTestSnapshot(),
			Mutation: &ProviderMutationFacts{InputEventsEmitted: true},
		}, windowResponse: &ProviderWindowResult{
			OK: true, AppName: "LibreOffice", AppPID: 10, WindowTitles: []string{"Document"},
		}}
		backend := &orderedInputRecordingBackend{recordingPlatformBackend: legacy}
		s.backend = backend
		guard := map[string]any{"type": "window_title_contains", "text": "Document", "exists": true, "timeout_ms": 1}
		action := map[string]any{"type": "keypress", "keys": []any{"ENTER"}}
		batchGuard := guard
		if perAction {
			action["postcondition"] = guard
			batchGuard = nil
		}
		result := s.a11yBatch("", []any{action}, observation{mode: observeDiff}, batchGuard, "", window)
		if result.IsError || len(backend.requests) != 0 || len(legacy.actionRequests) != 1 || len(legacy.observeRequests) == 0 {
			t.Fatalf("guarded batch left assertion path: perAction=%t result=%#v calls=%#v", perAction, result, legacy.calls)
		}
		facts := result.StructuredContent["a11y_batch_result"].(map[string]any)
		if facts["final_observation_phase"] != "after_postcondition" || facts["final_observation_returned"] != true {
			t.Fatalf("guarded batch did not observe after its predicate: %#v", facts)
		}
	}
}

func TestOrderedBatchOptionalFinalObservationFailure(t *testing.T) {
	for _, tail := range []bool{false, true} {
		s, _, window, _ := orderedInputTestService(t)
		legacy := &recordingPlatformBackend{}
		backend := &orderedInputRecordingBackend{recordingPlatformBackend: legacy,
			execute: completedOrderedInputObservationFailure,
		}
		s.backend = backend
		actions := []any{map[string]any{"type": "keypress", "keys": []any{"ENTER"}}}
		if tail {
			actions = append(actions, map[string]any{"type": "type", "text": "must not run"})
		}
		result := s.a11yBatch("", actions, observation{mode: observeNone}, nil, "", window)
		facts := result.StructuredContent["a11y_batch_result"].(map[string]any)
		if result.IsError != tail || len(backend.requests) != 1 || len(legacy.calls) != 0 ||
			facts["actions_completed_without_call_error"] != 1 || facts["final_observation_returned"] != false ||
			result.StructuredContent["observation_required"] != true || result.StructuredContent["retry_same_request"] != false {
			t.Fatalf("optional observation hid unattempted tail or replayed input: tail=%t result=%#v", tail, result)
		}
		if tail && facts["first_unattempted_action_index"] != 1 {
			t.Fatalf("missing unattempted suffix: %#v", facts)
		}
	}
}
