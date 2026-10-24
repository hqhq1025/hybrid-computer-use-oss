package core

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hqhq1025/hybrid-computer-use-oss/computer-use/internal/contract"
)

func TestKeyboardDeliveryFailureReachesModel(t *testing.T) {
	fixture := readProviderFixture(t, "keyboard-receiver-changed.response.json")
	response, err := DecodeProviderWireResponse(fixture)
	if err != nil {
		t.Fatal(err)
	}
	assertProviderFixtureRoundTrip(t, fixture, response)
	for _, consumed := range []int{0, 3, 4} {
		response.Desktop.ActionEvidence.ConsumedEvents = consumed
		for _, tool := range []string{contract.PublicToolAX, contract.PublicToolGUI} {
			for _, batch := range []bool{false, true} {
				// Exercise strict decoding of both payload types. Four removals
				// may still fail modifier verification and never prove effect.
				wire := response
				if tool == contract.PublicToolAX {
					wire.Operation = ProviderWireResolveAndAct
					wire.Accessibility = &ProviderAccessibilityResult{
						OK: false, Error: response.Desktop.Error,
						Mutation: response.Desktop.Mutation, Failure: response.Desktop.Failure,
						ActionEvidence: response.Desktop.ActionEvidence,
					}
					wire.Desktop = nil
				}
				encoded, err := json.Marshal(wire)
				if err != nil {
					t.Fatal(err)
				}
				decoded, err := DecodeProviderWireResponse(encoded)
				if err != nil {
					t.Fatal(err)
				}
				service := NewService()
				var result ToolCallResult
				if tool == contract.PublicToolGUI {
					desktop := *decoded.Desktop
					if batch {
						desktop.Snapshot = &AppSnapshot{ScreenshotPNGBase64: testPNGBase64(t, 10, 10), Width: 10, Height: 10, CoordinateSpace: "screen_image_px"}
					}
					result = service.guiResponse(desktop)
				} else {
					ax := *decoded.Accessibility
					ax.PrepareForService()
					result = service.renderRuntimeReportedFailure("", AXServiceRequest{}, ax, "", nil, nil)
					result.StructuredContent["ax_mode"] = "act"
					if batch {
						result = prependAXBatchSummary(result, "", map[string]any{"actions_requested": 2, "actions_completed_without_call_error": 0, "failed_action_index": 0, "actions": []map[string]any{batchActionEvidenceEntry(0, "keypress", result)}})
					}
				}
				text := ResultText(FinalizeForModel(tool, result, false))
				if !result.IsError || strings.Count(text, `"keyboard_delivery"`) != 1 {
					t.Fatalf("%s failure evidence missing or duplicated: %s", tool, text)
				}
				for _, want := range []string{`"requested_events":4`, fmt.Sprintf(`"consumed_events":%d`, consumed), `"effect_verified":false`, `"input_emitted":true`, `"mutation_unknown":true`} {
					if !strings.Contains(text, want) {
						t.Fatalf("%s missing %s: %s", tool, want, text)
					}
				}
				public := publicKeyboardDelivery(response.Desktop.ActionEvidence)
				if public["consumed_events"] != consumed {
					t.Fatalf("consumption count changed: %#v", public)
				}
				for _, forbidden := range []string{"text_delivery", "input.keyboard_queue", "runtime_action_evidence", "emission_route"} {
					if strings.Contains(text, forbidden) {
						t.Fatalf("%s private or unrelated evidence %q leaked: %s", tool, forbidden, text)
					}
				}
			}
		}
	}
}

func TestKeyboardEvidenceDoesNotAppearForOtherActions(t *testing.T) {
	for _, value := range []any{nil, (*RuntimeActionEvidence)(nil), RuntimeActionEvidence{}, &RuntimeActionEvidence{Route: "keyboard"}} {
		if got := publicKeyboardDelivery(value); got != nil {
			t.Fatalf("invented keyboard queue evidence: %#v", got)
		}
	}
}
