package main

import (
	"strings"
	"testing"
)

func untilTitleService(t *testing.T, titles ...string) (*service, *recordingPlatformBackend, string) {
	t.Helper()
	s, _, window, _ := orderedInputTestService(t)
	backend := &recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{
			OK: true, Snapshot: mixedOrderedInputBatchTestSnapshot(),
			Mutation:       &ProviderMutationFacts{InputEventsEmitted: true},
			ActionEvidence: &ProviderActionEvidence{InputEmitted: true, Route: "keyboard_synthesis"},
		},
		windowResponse: &ProviderWindowResult{
			OK: true, AppName: "LibreOffice", AppPID: 10, WindowTitles: titles,
		},
	}
	s.backend = backend
	return s, backend, window
}

func publicLine(t *testing.T, result toolCallResult) string {
	t.Helper()
	visible := withAXTextEvidence(prepareAXEvidence(result))
	if len(visible.Content) == 0 {
		t.Fatal("no content")
	}
	return visible.Content[0].Text
}

func TestUntilOnBatchPassesAndReportsTrue(t *testing.T) {
	s, backend, window := untilTitleService(t, "Saved - Sheet")
	result := s.callTool(publicToolAX, map[string]any{
		"mode": "act", "window_ref": window,
		"actions": []any{map[string]any{"type": "keypress", "keys": []any{"CTRL", "S"}}},
		"until":   map[string]any{"type": "title", "text": "saved", "timeout_ms": 1.0},
	})
	if result.IsError || len(backend.actionRequests) != 1 {
		t.Fatalf("batch until: %s", resultText(result))
	}
	if line := publicLine(t, result); !strings.HasPrefix(line, `{"ok":true,"until":"true","input_emitted":true,"completed":1,"requested":1`) {
		t.Fatalf("evidence line %s", line)
	}
}

// A batch until checks the state after every action ran. When it stays
// false that is a result: nothing was skipped, and the model decides.
func TestUntilOnBatchThatStaysFalseIsAResult(t *testing.T) {
	s, backend, window := untilTitleService(t, "Sheet")
	result := s.callTool(publicToolAX, map[string]any{
		"mode": "act", "window_ref": window,
		"actions": []any{map[string]any{"type": "keypress", "keys": []any{"CTRL", "S"}}},
		"until":   map[string]any{"type": "title", "text": "Saved", "timeout_ms": 1.0},
	})
	if result.IsError || len(backend.actionRequests) != 1 {
		t.Fatalf("batch until false must be a result: %s", resultText(result))
	}
	if line := publicLine(t, result); !strings.HasPrefix(line, `{"ok":true,"until":"false","input_emitted":true,"completed":1,"requested":1`) {
		t.Fatalf("evidence line %s", line)
	}
}

func TestUntilOnActionGatesTheNextAction(t *testing.T) {
	s, backend, window := untilTitleService(t, "Sheet")
	result := s.callTool(publicToolAX, map[string]any{
		"mode": "act", "window_ref": window,
		"actions": []any{
			map[string]any{"type": "keypress", "keys": []any{"ENTER"},
				"until": map[string]any{"type": "title", "text": "Dialog", "timeout_ms": 1.0}},
			map[string]any{"type": "type", "text": "must not run"},
		},
	})
	if !result.IsError || len(backend.actionRequests) != 1 {
		t.Fatalf("a false action until must stop before the next action: requests=%d %s",
			len(backend.actionRequests), resultText(result))
	}
	line := publicLine(t, result)
	if !strings.HasPrefix(line, `{"ok":false,"code":"until_failed"`) ||
		!strings.Contains(line, `"completed":1,"requested":2`) {
		t.Fatalf("evidence line %s", line)
	}
}

func TestObserveUntilFalseIsAResultNotAnError(t *testing.T) {
	s, backend, window := untilTitleService(t, "Sheet")
	result := s.callTool(publicToolAX, map[string]any{
		"mode": "observe", "window_ref": window, "observe": "none",
		"until": map[string]any{"type": "title", "text": "Saved", "timeout_ms": 1.0},
	})
	if result.IsError || len(backend.actionRequests) != 0 {
		t.Fatalf("observe until false must be a result: %s", resultText(result))
	}
	if line := publicLine(t, result); !strings.HasPrefix(line, `{"ok":true,"until":"false","input_emitted":false`) {
		t.Fatalf("evidence line %s", line)
	}
}

func TestObserveUntilTrueReturnsCurrentState(t *testing.T) {
	s, _, window := untilTitleService(t, "Saved - Sheet")
	result := s.callTool(publicToolAX, map[string]any{
		"mode": "observe", "window_ref": window, "observe": "tree",
		"until": map[string]any{"type": "title", "text": "Saved", "timeout_ms": 1.0},
	})
	if result.IsError {
		t.Fatalf("observe until: %s", resultText(result))
	}
	text := resultText(withAXTextEvidence(prepareAXEvidence(result)))
	if !strings.HasPrefix(text, `{"ok":true,"until":"true"`) || !strings.Contains(text, "PASS:") ||
		!strings.Contains(text, `"Document"`) {
		t.Fatalf("observe until must return the check and the current tree: %s", text)
	}
}

func TestObserveUntilNegateWaitsForAbsence(t *testing.T) {
	s, _, window := untilTitleService(t, "Sheet")
	result := s.callTool(publicToolAX, map[string]any{
		"mode": "observe", "window_ref": window, "observe": "none",
		"until": map[string]any{"type": "title", "text": "Saving", "negate": true, "timeout_ms": 1.0},
	})
	if result.IsError || !strings.HasPrefix(publicLine(t, result), `{"ok":true,"until":"true"`) {
		t.Fatalf("negated title predicate: %s", resultText(result))
	}
}

// A truncated find prints the exact call that returns the next page, and
// that call works.
func TestTruncatedFindPrintsTheNextPageCall(t *testing.T) {
	svc, snapshot, matches := findExposureFixture(t, 8)
	first := findDeliveryFinal(svc.renderFindPage(snapshot, matches, 0, 3, "target", nil, false))
	text := resultText(first)
	cursor, _ := first.StructuredContent["next_cursor"].(string)
	if cursor == "" {
		t.Fatalf("truncated page has no cursor: %s", text)
	}
	call := `{"mode":"find","cursor":"` + cursor + `"}`
	if !strings.Contains(text, "matches: 8") || !strings.Contains(text, "range=1..3 next_cursor="+cursor) ||
		!strings.Contains(text, "next page: "+call) {
		t.Fatalf("truncated page must print the next-page call %s:\n%s", call, text)
	}
	request, argErr := decodeAXRequest(decodeJSONForTest(t, call))
	if argErr != nil {
		t.Fatalf("printed call is not valid: %#v", argErr)
	}
	next := findDeliveryFinal(svc.a11yFind(request))
	if next.IsError || !strings.Contains(resultText(next), "range=4..") {
		t.Fatalf("next page call failed: %s", resultText(next))
	}
}

// Every public error code is produced from the internal facts that mean it,
// and every error line carries a hint.
func TestEveryPublicErrorCodeHasAHint(t *testing.T) {
	for code, full := range map[string]map[string]any{
		axCodeInvalidArgument: {axPublicErrorCodeKey: axCodeInvalidArgument, axPublicErrorHintKey: `{"mode":"apps"}`},
		axCodeStaleRef:        {"failure_code": "stale_element_ref"},
		axCodeWindowChanged:   {"failure_code": "stale_target", "failure_phase": "between_actions"},
		axCodeUntilFailed:     {"failure_code": "action_postcondition_not_satisfied"},
		axCodeUntilUnknown:    {"failure_code": "postcondition_unknown"},
		axCodeEffectUnknown:   {"failure_code": "effect_unknown", "input_events_emitted": true},
		axCodeAppUnavailable:  {"failure_code": "ax_app_unavailable"},
		axCodeRuntimeError:    {"failure_code": "runtime_timeout"},
		axCodeAmbiguousWindow: {axPublicErrorCodeKey: axCodeAmbiguousWindow, axPublicErrorHintKey: `{"type":"activate","ref":"w1"}`},
		axCodeWindowNotFound:  {axPublicErrorCodeKey: axCodeWindowNotFound, axPublicErrorHintKey: `{"type":"activate","ref":"wN"}`},
		axCodeNoFocusedWindow: {axPublicErrorCodeKey: axCodeNoFocusedWindow, axPublicErrorHintKey: `{"type":"activate","title":"window title"}`},
	} {
		public := axPublicEvidence(full, true, []contentItem{{Type: "text", Text: "detail"}})
		if public["code"] != code {
			t.Fatalf("%s: code=%v from %#v", code, public["code"], full)
		}
		if hint, _ := public["hint"].(string); hint == "" {
			t.Fatalf("%s has no hint", code)
		}
		if public["ok"] != false || public["message"] != "detail" {
			t.Fatalf("%s: %#v", code, public)
		}
	}
}

// An exists predicate whose target never appears is false, a result; the
// model should not read it as "could not check".
func TestUntilExistsMissingTargetIsFalseNotUnknown(t *testing.T) {
	service := newServiceWithBackend(&recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{OK: true, Snapshot: editorFormSnapshot()},
	})
	result := service.callTool(publicToolAX, map[string]any{
		"mode": "observe", "app": "Editor", "observe": "none",
		"until": map[string]any{"type": "exists", "selector": map[string]any{"role": "dialog"}, "timeout_ms": 1.0},
	})
	if result.IsError {
		t.Fatalf("missing exists target must be a false result: %s", resultText(result))
	}
	if line := publicLine(t, result); !strings.HasPrefix(line, `{"ok":true,"until":"false"`) {
		t.Fatalf("evidence line %s", line)
	}
}

func TestNeverIssuedRefIsStaleRefWithHint(t *testing.T) {
	result := newService().callTool(publicToolAX, map[string]any{
		"mode": "act", "actions": []any{map[string]any{"type": "click", "ref": "r103"}},
	})
	line := publicLine(t, result)
	if !strings.HasPrefix(line, `{"ok":false,"code":"stale_ref"`) || !strings.Contains(line, `"hint":`) ||
		!strings.Contains(line, `"input_emitted":false`) {
		t.Fatalf("evidence line %s", line)
	}
}
