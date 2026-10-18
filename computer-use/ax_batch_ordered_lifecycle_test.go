package main

import "testing"

func orderedLifecycleFailure(request ProviderOrderedInputRequest, failure ProviderFailure) ProviderOrderedInputResult {
	first := request.Steps[0].ActionIndex
	return ProviderOrderedInputResult{
		ContractVersion: providerOrderedInputContractV1,
		Steps: []ProviderOrderedInputStepResult{{
			ActionIndex: first, Status: ProviderOrderedInputFailed,
			Emission: ProviderOrderedInputEmissionNone, WindowContinuity: ProviderOrderedInputWindowUnknown,
		}},
		StoppedActionIndex: &first, FirstUnattemptedActionIndex: &first, Failure: &failure,
	}
}

func TestOrderedBatchTypedLifecycleScope(t *testing.T) {
	for _, tc := range []struct {
		code, subject        string
		complete             bool
		app, window, element AXRefLifecycle
	}{
		{"stale_app_ref", "app", true, AXRefStale, AXRefStale, AXRefStale},
		{"stale_app_ref", "app", false, AXRefNeedsRevalidation, AXRefNeedsRevalidation, AXRefNeedsRevalidation},
		{"stale_window_ref", "window", true, AXRefActive, AXRefStale, AXRefStale},
		{"stale_window_ref", "window", false, AXRefActive, AXRefNeedsRevalidation, AXRefActive},
		{"stale_target", "element", true, AXRefActive, AXRefActive, AXRefActive},
		{"stale_target", "", true, AXRefActive, AXRefActive, AXRefActive},
		{"observation_incomplete", "window", false, AXRefActive, AXRefNeedsRevalidation, AXRefActive},
		{"observation_incomplete", "app", false, AXRefNeedsRevalidation, AXRefNeedsRevalidation, AXRefNeedsRevalidation},
	} {
		t.Run(tc.code+"/"+tc.subject+"/"+string(tc.window), func(t *testing.T) {
			s, app, window, element := orderedInputTestService(t)
			sibling := s.windowRefRegistry.Acquire("libreoffice", "w2:11111111111111111111111111111111", `frame "Other"`, "c1",
				":1.84", "/window/2", "pid:10:start:100", "frame", "Other", app)
			legacy := &recordingPlatformBackend{}
			backend := &orderedInputRecordingBackend{
				recordingPlatformBackend: legacy,
				execute: func(request ProviderOrderedInputRequest) ProviderOrderedInputResult {
					return orderedLifecycleFailure(request, ProviderFailure{
						Code: tc.code, Subject: tc.subject, Phase: "precondition", FactsComplete: tc.complete,
					})
				},
			}
			s.backend = backend
			result := s.a11yBatch([]any{
				map[string]any{"type": "keypress", "keys": []any{"ESC"}},
			}, observation{mode: observeNone}, nil, window)

			if !result.IsError || result.StructuredContent["failure_code"] != tc.code ||
				result.StructuredContent["failure_facts_complete"] != tc.complete || len(legacy.calls) != 0 {
				t.Fatalf("typed failure lost or replayed: %#v", result)
			}
			if s.appRefRegistry.entries[app].Lifecycle != tc.app ||
				s.windowRefRegistry.entries[window].Lifecycle != tc.window ||
				s.windowRefRegistry.entries[sibling].Lifecycle != tc.app ||
				s.refRegistry.entries[element].Lifecycle != tc.element {
				t.Fatalf("incorrect lifecycle scope: app=%s window=%s sibling=%s element=%s",
					s.appRefRegistry.entries[app].Lifecycle, s.windowRefRegistry.entries[window].Lifecycle,
					s.windowRefRegistry.entries[sibling].Lifecycle, s.refRegistry.entries[element].Lifecycle)
			}
			if tc.window != AXRefActive {
				result = s.a11yBatch([]any{
					map[string]any{"type": "keypress", "keys": []any{"ESC"}},
				}, observation{mode: observeNone}, nil, window)

				code := "stale_window_ref"
				if tc.window == AXRefNeedsRevalidation {
					code = "observation_incomplete"
				}
				if !result.IsError || result.StructuredContent["failure_code"] != code || len(backend.requests) != 1 {
					t.Fatalf("repeated input was not rejected locally with typed evidence: %#v", result)
				}
			}
		})
	}
}

func TestOrderedBatchInvalidResultCannotRetireIdentity(t *testing.T) {
	s, app, window, ref := orderedInputTestService(t)
	legacy := &recordingPlatformBackend{}
	backend := &orderedInputRecordingBackend{recordingPlatformBackend: legacy,
		execute: func(request ProviderOrderedInputRequest) ProviderOrderedInputResult {
			result := orderedLifecycleFailure(request, ProviderFailure{
				Code: "stale_app_ref", Subject: "app", Phase: "precondition", FactsComplete: true,
			})
			result.ContractVersion++
			return result
		},
	}
	s.backend = backend
	result := s.a11yBatch([]any{map[string]any{"type": "keypress", "keys": []any{"ESC"}}},
		observation{mode: observeNone}, nil, window)

	if !result.IsError || result.StructuredContent["failure_code"] != "runtime_transport_lost" ||
		result.StructuredContent["mutation_unknown"] != true || len(backend.requests) != 1 || len(legacy.calls) != 0 {
		t.Fatalf("invalid result trusted or replayed: %#v", result)
	}
	if s.appRefRegistry.entries[app].Lifecycle != AXRefActive ||
		s.windowRefRegistry.entries[window].Lifecycle != AXRefActive || s.refRegistry.entries[ref].Lifecycle != AXRefActive {
		t.Fatal("invalid native result retired identity")
	}
}

func TestOrderedBatchRequestRejectionIsNotTransportUncertainty(t *testing.T) {
	s, _, window, _ := orderedInputTestService(t)
	legacy := &recordingPlatformBackend{}
	backend := &orderedInputRecordingBackend{recordingPlatformBackend: legacy,
		err: &runtimeRequestRejectionError{operation: "ordered_input", rejection: ProviderRequestRejection{
			Code: "invalid_arguments", Phase: "request_validation", DispatchAttempted: boolPointer(false), Message: "invalid key",
		}},
	}
	s.backend = backend
	result := s.a11yBatch([]any{map[string]any{"type": "keypress", "keys": []any{"ESC"}}},
		observation{mode: observeNone}, nil, window)

	facts := result.StructuredContent["a11y_batch_result"].(map[string]any)
	if !result.IsError || result.StructuredContent["provider_request_rejected"] != true ||
		facts["rejected_before_first_action"] != true || facts["first_unattempted_action_index"] != 0 ||
		facts["mutation_unknown"] != false || facts["partial_input_possible"] != false || len(legacy.calls) != 0 {
		t.Fatalf("typed rejection became transport uncertainty: %#v", result)
	}
}
