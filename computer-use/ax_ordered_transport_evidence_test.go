package main

import (
	"errors"
	"testing"
)

func TestOrderedInputTransportUsesRequestDeliveryEvidence(t *testing.T) {
	for _, test := range []struct {
		name    string
		err     error
		unknown bool
	}{
		{"zero bytes", &runtimeTransportError{platform: "macOS", phase: "write_request", requestSent: false, cause: errors.New("broken pipe")}, false},
		{"partial write", &runtimeTransportError{platform: "macOS", phase: "write_request", requestSent: true, cause: errors.New("broken pipe")}, true},
		{"lost response", &runtimeTransportError{platform: "macOS", phase: "read_response", requestSent: true, cause: errors.New("EOF")}, true},
		{"timeout", &runtimeTimeoutError{platform: "macOS", phase: "write_request"}, true},
		{"unclassified", errors.New("connection lost"), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, _, window, _ := orderedInputTestService(t)
			backend := &orderedInputRecordingBackend{
				recordingPlatformBackend: &recordingPlatformBackend{}, err: test.err,
			}
			s.backend = backend
			result := s.axBatch([]any{
				map[string]any{"type": "keypress", "keys": []any{"A"}},
				map[string]any{"type": "keypress", "keys": []any{"B"}},
			}, observation{mode: observeNone}, window)
			if !result.IsError || len(backend.requests) != 1 {
				t.Fatalf("transport failure succeeded or replayed: %#v", result)
			}
			facts := result.StructuredContent["a11y_batch_result"].(map[string]any)
			if result.StructuredContent["mutation_unknown"] != test.unknown ||
				result.StructuredContent["partial_input_possible"] != test.unknown ||
				facts["rejected_before_first_action"] != !test.unknown {
				t.Fatalf("delivery evidence discarded: %#v", result.StructuredContent)
			}
			public := publicBatchEvidence(t, result)
			assertPublicBatchCounts(t, public, 2, 0)
			if !test.unknown && (public["code"] != axCodeRuntimeError || public["input_emitted"] != false) {
				t.Fatalf("unsent request reported input uncertainty: %#v", public)
			}
			if !test.unknown && facts["first_unattempted_action_index"] != 0 {
				t.Fatalf("unsent request lost its unattempted boundary: %#v", facts)
			}
		})
	}
}

func TestUnsentOrderedInputKeepsEarlierBatchInputEvidence(t *testing.T) {
	s, _, _, ref := orderedInputTestService(t)
	backend := &orderedInputRecordingBackend{
		recordingPlatformBackend: &recordingPlatformBackend{
			accessibilityResponse: &ProviderAccessibilityResult{
				OK: true, Snapshot: mixedOrderedInputBatchTestSnapshot(),
				Mutation:       &ProviderMutationFacts{InputEventsEmitted: true},
				ActionEvidence: &RuntimeActionEvidence{InputEmitted: true},
			},
		},
		err: &runtimeTransportError{platform: "macOS", phase: "write_request", requestSent: false, cause: errors.New("broken pipe")},
	}
	s.backend = backend
	result := s.axBatch([]any{
		map[string]any{"type": "click", "ref": ref},
		map[string]any{"type": "keypress", "keys": []any{"A"}},
		map[string]any{"type": "keypress", "keys": []any{"B"}},
	}, observation{mode: observeNone}, "")
	public := publicBatchEvidence(t, result)
	batch := assertPublicBatchCounts(t, public, 3, 1)
	if !result.IsError || public["input_emitted"] != true || batch["rejected_before_first_action"] == true {
		t.Fatalf("earlier click evidence discarded: %#v", public)
	}
	if len(backend.requests) != 1 || len(backend.actionRequests) != 1 {
		t.Fatal("failed ordered input was replayed")
	}
}
