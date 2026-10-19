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
	next := findDeliveryFinal(svc.axFind(request))
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
		axCodeWaitTimeout:     {"failure_code": "wait_timeout"},
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
