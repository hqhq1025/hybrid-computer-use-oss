package core

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hqhq1025/hybrid-computer-use-oss/computer-use/internal/contract"
)

func TestTextDeliveryEvidenceReachesModel(t *testing.T) {
	for _, file := range []string{"text-first-message-unknown.response.json", "text-focus-changed.response.json"} {
		t.Run(file, func(t *testing.T) {
			response, err := DecodeProviderWireResponse(readProviderFixture(t, file))
			if err != nil {
				t.Fatal(err)
			}
			assertProviderFixtureRoundTrip(t, readProviderFixture(t, file), response)
			var evidence *RuntimeActionEvidence
			var mutation *ProviderMutationFacts
			if response.Desktop != nil {
				evidence, mutation = response.Desktop.ActionEvidence, response.Desktop.Mutation
			} else {
				evidence, mutation = response.Accessibility.ActionEvidence, response.Accessibility.Mutation
			}
			for _, tool := range []string{contract.PublicToolAX, contract.PublicToolGUI} {
				for _, screenshot := range []bool{false, true} {
					var result ToolCallResult
					service := NewService()
					if tool == contract.PublicToolGUI {
						desktop := ProviderDesktopResult{OK: false, Error: "Text delivery interrupted; observe before replaying.", Mutation: mutation, ActionEvidence: evidence, Failure: &ProviderFailure{Code: "desktop_input_failed", Phase: "emission"}}
						if screenshot {
							desktop.Snapshot = &AppSnapshot{ScreenshotPNGBase64: testPNGBase64(t, 10, 10), Width: 10, Height: 10, CoordinateSpace: "screen_image_px"}
						}
						result = service.guiResponse(desktop)
					} else {
						ax := ProviderAccessibilityResult{OK: false, Error: "Text delivery interrupted; observe before replaying.", Mutation: mutation, ActionEvidence: evidence, Failure: &ProviderFailure{Code: "desktop_input_failed", Phase: "emission"}}
						ax.PrepareForService()
						result = service.renderRuntimeReportedFailure("", AXServiceRequest{}, ax, "", nil, nil)
						result.StructuredContent["ax_mode"] = "act"
						if screenshot { // The batch path must retain the failing action's evidence.
							result = prependAXBatchSummary(result, "", map[string]any{"actions_requested": 2, "actions_completed_without_call_error": 0, "failed_action_index": 0, "actions": []map[string]any{batchActionEvidenceEntry(0, "type", result)}})
						}
					}
					visible := FinalizeForModel(tool, result, false)
					text := ResultText(visible)
					if strings.Count(text, `"text_delivery"`) != 1 {
						t.Fatalf("%s evidence missing or duplicated: %s", tool, text)
					}
					for _, forbidden := range []string{"input.window_messages", "document_replace", `"message":258`, "runtime_action_evidence", "emission_route"} {
						if strings.Contains(text, forbidden) {
							t.Fatalf("private evidence %q leaked: %s", forbidden, text)
						}
					}
					for _, want := range []string{`"attempted_messages":1`, `"effect_verified":false`, `"sequence_phase":"character"`} {
						if !strings.Contains(text, want) {
							t.Fatalf("missing %s: %s", want, text)
						}
					}
					if evidence.UnknownMessageIndex != nil {
						if !strings.Contains(text, `"unknown_message_index":0`) || !strings.Contains(text, `"input_emitted":false`) {
							t.Fatalf("unknown first send became confirmed emission: %s", text)
						}
					} else if !strings.Contains(text, `"unknown_message_index":null`) {
						t.Fatalf("no outstanding message became index zero: %s", text)
					}
				}
			}
		})
	}
}

func TestTextDeliveryEvidencePreservesInterruptedSequence(t *testing.T) {
	for _, phase := range []string{"enter_character", "enter_up", "surrogate_low"} {
		t.Run(phase, func(t *testing.T) {
			response, err := DecodeProviderWireResponse(readProviderFixture(t, "text-first-message-unknown.response.json"))
			if err != nil {
				t.Fatal(err)
			}
			input := response.Desktop.ActionEvidence
			input.SourceUTF16Index, input.AttemptedMessages, input.ReturnedMessages = 2, 4, 3
			unknown := 3
			input.UnknownMessageIndex, input.SequencePhase = &unknown, phase
			data, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeProviderWireResponse(data)
			if err != nil {
				t.Fatal(err)
			}
			public := publicTextDelivery(decoded.Desktop.ActionEvidence)
			if public["source_utf16_index"] != 2 || public["attempted_messages"] != 4 || public["returned_messages"] != 3 || public["sequence_phase"] != phase || *public["unknown_message_index"].(*int) != 3 || public["effect_verified"] != false {
				t.Fatalf("lost interrupted sequence: %#v", public)
			}
		})
	}
}
