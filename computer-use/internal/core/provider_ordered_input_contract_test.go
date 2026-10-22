package core

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hqhq1025/hybrid-computer-use-oss/computer-use/internal/contract"
)

func boolPointer(value bool) *bool {
	return &value
}

func intPointer(value int) *int {
	return &value
}

func testProviderOrderedInputRequest(
	keys ...string,
) ProviderOrderedInputRequest {
	handle := "w2:" + strings.Repeat("a", 32)
	steps := make([]ProviderOrderedInputStep, 0, len(keys))
	for index, key := range keys {
		steps = append(steps, ProviderOrderedInputStep{
			ActionIndex: 4 + index,
			Key:         key,
		})
	}
	return ProviderOrderedInputRequest{
		ContractVersion: ProviderOrderedInputContractV1,
		App:             "a1",
		AppIdentity: &ProviderAppIdentity{
			ProviderBusName:   "pid:42",
			ProcessGeneration: "pid:42:start:100",
		},
		WindowHandle: handle,
		ExpectedWindow: &ProviderWindowExpectation{
			WindowHandle:      handle,
			ProviderBusName:   "pid:42",
			ProcessGeneration: "pid:42:start:100",
			Role:              "window",
			Name:              "Document",
		},
		Steps: steps,
		FinalObservation: ProviderObservationOptions{
			CaptureSnapshot: boolPointer(true),
		},
	}
}

func testProviderOrderedInputSuccess(
	request ProviderOrderedInputRequest,
) ProviderOrderedInputResult {
	steps := make([]ProviderOrderedInputStepResult, 0, len(request.Steps))
	for _, step := range request.Steps {
		steps = append(steps, ProviderOrderedInputStepResult{
			ActionIndex:       step.ActionIndex,
			Status:            ProviderOrderedInputCompleted,
			DispatchAttempted: true,
			Emission:          ProviderOrderedInputEmissionComplete,
			WindowContinuity:  ProviderOrderedInputWindowSame,
		})
	}
	return ProviderOrderedInputResult{
		ContractVersion: ProviderOrderedInputContractV1,
		OK:              true,
		Steps:           steps,
		Snapshot: &ProviderSnapshot{
			WindowHandle: request.WindowHandle,
		},
		Mutation: ProviderMutationFacts{
			InputEventsEmitted:  true,
			DesktopStateMutated: true,
		},
	}
}

func TestProviderOrderedInputContractAcceptsCompleteSuccess(t *testing.T) {
	request := testProviderOrderedInputRequest("DOWN", "ENTER")
	result := testProviderOrderedInputSuccess(request)
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := result.ValidateFor(request); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ProviderOrderedInputResult
	if err := DecodeProviderWireJSON(encoded, &decoded); err != nil {
		t.Fatalf("strict result round trip failed: %v", err)
	}
	if err := decoded.ValidateFor(request); err != nil {
		t.Fatalf("round-tripped result failed validation: %v", err)
	}
}

func TestProviderOrderedInputContractAcceptsTypedStops(t *testing.T) {
	request := testProviderOrderedInputRequest("A", "B", "C")
	tests := []struct {
		name   string
		result ProviderOrderedInputResult
	}{
		{
			name: "window changed before first dispatch",
			result: ProviderOrderedInputResult{
				ContractVersion: ProviderOrderedInputContractV1,
				Steps: []ProviderOrderedInputStepResult{
					{
						ActionIndex:      4,
						Status:           ProviderOrderedInputFailed,
						Emission:         ProviderOrderedInputEmissionNone,
						WindowContinuity: ProviderOrderedInputWindowChanged,
					},
					{
						ActionIndex: 5,
						Status:      ProviderOrderedInputNotAttempted,
						Emission:    ProviderOrderedInputEmissionNone,
					},
					{
						ActionIndex: 6,
						Status:      ProviderOrderedInputNotAttempted,
						Emission:    ProviderOrderedInputEmissionNone,
					},
				},
				StoppedActionIndex:          intPointer(4),
				FirstUnattemptedActionIndex: intPointer(4),
				Failure: &ProviderFailure{
					Code:          "stale_window_ref",
					Phase:         "precondition",
					FactsComplete: true,
				},
			},
		},
		{
			name: "partial emission stops later actions",
			result: ProviderOrderedInputResult{
				ContractVersion: ProviderOrderedInputContractV1,
				Steps: []ProviderOrderedInputStepResult{
					{
						ActionIndex:       4,
						Status:            ProviderOrderedInputCompleted,
						DispatchAttempted: true,
						Emission:          ProviderOrderedInputEmissionComplete,
						WindowContinuity:  ProviderOrderedInputWindowSame,
					},
					{
						ActionIndex:       5,
						Status:            ProviderOrderedInputFailed,
						DispatchAttempted: true,
						Emission:          ProviderOrderedInputEmissionPartial,
						WindowContinuity:  ProviderOrderedInputWindowSame,
					},
					{
						ActionIndex: 6,
						Status:      ProviderOrderedInputNotAttempted,
						Emission:    ProviderOrderedInputEmissionNone,
					},
				},
				StoppedActionIndex:          intPointer(5),
				FirstUnattemptedActionIndex: intPointer(6),
				Failure: &ProviderFailure{
					Code:          "input_emission_failed",
					Phase:         "emission",
					FactsComplete: true,
				},
				Mutation: ProviderMutationFacts{
					InputEventsEmitted:   true,
					PartialInputPossible: true,
					DesktopStateMutated:  true,
					MutationUnknown:      true,
				},
			},
		},
		{
			name: "same-window pre-dispatch transient can retry",
			result: ProviderOrderedInputResult{
				ContractVersion: ProviderOrderedInputContractV1,
				Steps: []ProviderOrderedInputStepResult{
					{
						ActionIndex:      4,
						Status:           ProviderOrderedInputFailed,
						Emission:         ProviderOrderedInputEmissionNone,
						WindowContinuity: ProviderOrderedInputWindowSame,
					},
					{
						ActionIndex: 5,
						Status:      ProviderOrderedInputNotAttempted,
						Emission:    ProviderOrderedInputEmissionNone,
					},
					{
						ActionIndex: 6,
						Status:      ProviderOrderedInputNotAttempted,
						Emission:    ProviderOrderedInputEmissionNone,
					},
				},
				StoppedActionIndex:          intPointer(4),
				FirstUnattemptedActionIndex: intPointer(4),
				Failure: &ProviderFailure{
					Code:          "provider_busy",
					Phase:         "precondition",
					FactsComplete: true,
				},
				RetrySameRequest: true,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.result.ValidateFor(request); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProviderOrderedInputContractAcceptsFinalObservationFailure(t *testing.T) {
	request := testProviderOrderedInputRequest("A", "B")
	result := testProviderOrderedInputSuccess(request)
	result.OK = false
	result.Snapshot = nil
	result.Failure = &ProviderFailure{
		Code:          "state_unavailable",
		Phase:         "observation",
		FactsComplete: true,
	}
	result.Mutation.MutationUnknown = true
	if err := result.ValidateFor(request); err != nil {
		t.Fatal(err)
	}

	result.RetrySameRequest = true
	if err := result.ValidateFor(request); err == nil ||
		!strings.Contains(err.Error(), "invalid stop, snapshot, or retry") {
		t.Fatalf("retryable observation failure error=%v", err)
	}
}

func TestProviderOrderedInputRequestRejectsNonBatchableShapes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ProviderOrderedInputRequest)
		want   string
	}{
		{
			name: "missing window expectation",
			mutate: func(request *ProviderOrderedInputRequest) {
				request.ExpectedWindow = nil
			},
			want: "exact expected_window",
		},
		{
			name: "noncontiguous indices",
			mutate: func(request *ProviderOrderedInputRequest) {
				request.Steps[1].ActionIndex++
			},
			want: "strictly contiguous",
		},
		{
			name: "missing final canonical boundary",
			mutate: func(request *ProviderOrderedInputRequest) {
				request.FinalObservation.CaptureSnapshot = boolPointer(false)
			},
			want: "final canonical snapshot",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := testProviderOrderedInputRequest("A", "B")
			test.mutate(&request)
			err := request.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want substring %q", err, test.want)
			}
		})
	}
}

func TestProviderOrderedInputRequestAcceptsMaximumNormalizedChord(t *testing.T) {
	keys := make([]string, contract.GUIMaxKeys)
	for index := range keys {
		keys[index] = strings.Repeat("K", contract.GUIMaxKeyLength)
	}
	request := testProviderOrderedInputRequest(strings.Join(keys, "+"))
	if err := request.Validate(); err != nil {
		t.Fatalf("maximum normalized chord was rejected: %v", err)
	}
	request.Steps[0].Key += "X"
	if err := request.Validate(); err == nil ||
		!strings.Contains(err.Error(), "too long") {
		t.Fatalf("overlong normalized chord error=%v", err)
	}
}

func TestProviderOrderedInputResultRejectsContradictoryEvidence(t *testing.T) {
	request := testProviderOrderedInputRequest("A", "B", "C")
	tests := []struct {
		name   string
		result ProviderOrderedInputResult
		want   string
	}{
		{
			name: "missing result entry",
			result: func() ProviderOrderedInputResult {
				result := testProviderOrderedInputSuccess(request)
				result.Steps = result.Steps[:2]
				return result
			}(),
			want: "2 steps, want 3",
		},
		{
			name: "continued after failure",
			result: ProviderOrderedInputResult{
				ContractVersion: ProviderOrderedInputContractV1,
				Steps: []ProviderOrderedInputStepResult{
					{
						ActionIndex:      4,
						Status:           ProviderOrderedInputFailed,
						Emission:         ProviderOrderedInputEmissionNone,
						WindowContinuity: ProviderOrderedInputWindowChanged,
					},
					{
						ActionIndex:       5,
						Status:            ProviderOrderedInputCompleted,
						DispatchAttempted: true,
						Emission:          ProviderOrderedInputEmissionComplete,
						WindowContinuity:  ProviderOrderedInputWindowSame,
					},
					{
						ActionIndex: 6,
						Status:      ProviderOrderedInputNotAttempted,
						Emission:    ProviderOrderedInputEmissionNone,
					},
				},
			},
			want: "continued after",
		},
		{
			name: "window changed but input emitted",
			result: ProviderOrderedInputResult{
				ContractVersion: ProviderOrderedInputContractV1,
				Steps: []ProviderOrderedInputStepResult{
					{
						ActionIndex:       4,
						Status:            ProviderOrderedInputFailed,
						DispatchAttempted: true,
						Emission:          ProviderOrderedInputEmissionComplete,
						WindowContinuity:  ProviderOrderedInputWindowChanged,
					},
					{
						ActionIndex: 5,
						Status:      ProviderOrderedInputNotAttempted,
						Emission:    ProviderOrderedInputEmissionNone,
					},
					{
						ActionIndex: 6,
						Status:      ProviderOrderedInputNotAttempted,
						Emission:    ProviderOrderedInputEmissionNone,
					},
				},
			},
			want: "dispatched without proving",
		},
		{
			name: "partial emission omitted aggregate",
			result: ProviderOrderedInputResult{
				ContractVersion: ProviderOrderedInputContractV1,
				Steps: []ProviderOrderedInputStepResult{
					{
						ActionIndex:       4,
						Status:            ProviderOrderedInputFailed,
						DispatchAttempted: true,
						Emission:          ProviderOrderedInputEmissionPartial,
						WindowContinuity:  ProviderOrderedInputWindowSame,
					},
					{
						ActionIndex: 5,
						Status:      ProviderOrderedInputNotAttempted,
						Emission:    ProviderOrderedInputEmissionNone,
					},
					{
						ActionIndex: 6,
						Status:      ProviderOrderedInputNotAttempted,
						Emission:    ProviderOrderedInputEmissionNone,
					},
				},
			},
			want: "aggregate input emission",
		},
		{
			name: "retry after prior emission",
			result: func() ProviderOrderedInputResult {
				result := ProviderOrderedInputResult{
					ContractVersion: ProviderOrderedInputContractV1,
					Steps: []ProviderOrderedInputStepResult{
						{
							ActionIndex:       4,
							Status:            ProviderOrderedInputCompleted,
							DispatchAttempted: true,
							Emission:          ProviderOrderedInputEmissionComplete,
							WindowContinuity:  ProviderOrderedInputWindowSame,
						},
						{
							ActionIndex:      5,
							Status:           ProviderOrderedInputFailed,
							Emission:         ProviderOrderedInputEmissionNone,
							WindowContinuity: ProviderOrderedInputWindowSame,
						},
						{
							ActionIndex: 6,
							Status:      ProviderOrderedInputNotAttempted,
							Emission:    ProviderOrderedInputEmissionNone,
						},
					},
					StoppedActionIndex:          intPointer(5),
					FirstUnattemptedActionIndex: intPointer(5),
					Failure: &ProviderFailure{
						Code:          "provider_busy",
						Phase:         "precondition",
						FactsComplete: true,
					},
					Mutation: ProviderMutationFacts{
						InputEventsEmitted: true,
						MutationUnknown:    true,
					},
					RetrySameRequest: true,
				}
				return result
			}(),
			want: "same-request retry",
		},
		{
			name: "retry with nontransient failure",
			result: ProviderOrderedInputResult{
				ContractVersion: ProviderOrderedInputContractV1,
				Steps: []ProviderOrderedInputStepResult{
					{
						ActionIndex:      4,
						Status:           ProviderOrderedInputFailed,
						Emission:         ProviderOrderedInputEmissionNone,
						WindowContinuity: ProviderOrderedInputWindowSame,
					},
					{
						ActionIndex: 5,
						Status:      ProviderOrderedInputNotAttempted,
						Emission:    ProviderOrderedInputEmissionNone,
					},
					{
						ActionIndex: 6,
						Status:      ProviderOrderedInputNotAttempted,
						Emission:    ProviderOrderedInputEmissionNone,
					},
				},
				StoppedActionIndex:          intPointer(4),
				FirstUnattemptedActionIndex: intPointer(4),
				Failure: &ProviderFailure{
					Code:          "permission_denied",
					Phase:         "precondition",
					FactsComplete: true,
				},
				RetrySameRequest: true,
			},
			want: "complete transient",
		},
		{
			name: "wrong first unattempted index",
			result: func() ProviderOrderedInputResult {
				result := testProviderOrderedInputSuccess(request)
				result.OK = false
				result.Steps[1] = ProviderOrderedInputStepResult{
					ActionIndex:      5,
					Status:           ProviderOrderedInputFailed,
					Emission:         ProviderOrderedInputEmissionNone,
					WindowContinuity: ProviderOrderedInputWindowChanged,
				}
				result.Steps[2] = ProviderOrderedInputStepResult{
					ActionIndex: 6,
					Status:      ProviderOrderedInputNotAttempted,
					Emission:    ProviderOrderedInputEmissionNone,
				}
				result.StoppedActionIndex = intPointer(5)
				result.FirstUnattemptedActionIndex = intPointer(6)
				result.Failure = &ProviderFailure{
					Code:          "stale_window_ref",
					Phase:         "precondition",
					FactsComplete: true,
				}
				result.Snapshot = nil
				result.Mutation = ProviderMutationFacts{
					InputEventsEmitted: true,
					MutationUnknown:    true,
				}
				return result
			}(),
			want: "first_unattempted",
		},
		{
			name: "success without final snapshot",
			result: func() ProviderOrderedInputResult {
				result := testProviderOrderedInputSuccess(request)
				result.Snapshot = nil
				return result
			}(),
			want: "final canonical snapshot",
		},
		{
			name: "success with absent expected window",
			result: func() ProviderOrderedInputResult {
				result := testProviderOrderedInputSuccess(request)
				result.Snapshot.WindowAbsent = true
				return result
			}(),
			want: "final canonical snapshot",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.result.ValidateFor(request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want substring %q", err, test.want)
			}
		})
	}
}

func TestProviderOrderedInputStrictDecodeRejectsUnknownFields(t *testing.T) {
	request := testProviderOrderedInputRequest("ENTER")
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	encoded = []byte(strings.Replace(
		string(encoded),
		`"contract_version":1`,
		`"contract_version":1,"fast":true`,
		1,
	))
	var decoded ProviderOrderedInputRequest
	if err := DecodeProviderWireJSON(encoded, &decoded); err == nil ||
		!strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("strict decoder error=%v, want unknown field", err)
	}
}

type orderedInputRecordingBackend struct {
	*recordingPlatformBackend
	requests []ProviderOrderedInputRequest
	execute  func(ProviderOrderedInputRequest) ProviderOrderedInputResult
	err      error
}

func (backend *orderedInputRecordingBackend) ExecuteOrderedInput(
	request ProviderOrderedInputRequest,
) (ProviderOrderedInputResult, error) {
	backend.requests = append(backend.requests, request)
	if backend.err != nil {
		return ProviderOrderedInputResult{}, backend.err
	}
	if backend.execute != nil {
		return backend.execute(request), nil
	}
	return ProviderOrderedInputResult{}, nil
}

func TestProviderOrderedInputBackendIsOptional(t *testing.T) {
	legacy := &recordingPlatformBackend{}
	if _, ok := orderedInputBackend(legacy); ok {
		t.Fatal("legacy backend unexpectedly advertises ordered input")
	}
	ordered := &orderedInputRecordingBackend{
		recordingPlatformBackend: legacy,
	}
	if _, ok := orderedInputBackend(ordered); !ok {
		t.Fatal("optional ordered-input backend was not detected")
	}
}

func TestProviderOrderedInputRequestsCompileOnlyPlannedRuns(t *testing.T) {
	handle := "w2:" + strings.Repeat("b", 32)
	context := &AXBatchContext{
		AppToken:     "a1",
		WindowRef:    "w1",
		WindowHandle: handle,
		expectedWindow: &ProviderWindowExpectation{
			WindowHandle:      handle,
			ProviderBusName:   "pid:84",
			ProcessGeneration: "pid:84:start:200",
			Role:              "window",
			Name:              "Document",
		},
	}
	actions := []axBatchAction{
		{actionType: "click", targetRef: "r1"},
		{actionType: "keypress", key: "DOWN"},
		{actionType: "keypress", key: "ENTER"},
		{actionType: "set_value", targetRef: "r2", value: "6"},
		{actionType: "keypress", key: "TAB"},
	}
	plan, err := compileAXBatchExecutionPlan(
		context,
		actions,
		observation{mode: contract.ObserveDiff},
	)
	if err != nil {
		t.Fatal(err)
	}
	requests, err := providerOrderedInputRequestsFromPlan(
		context,
		plan,
		actions,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("ordered input requests=%d, want 2: %#v",
			len(requests), requests)
	}
	if len(requests[0].Steps) != 2 ||
		requests[0].Steps[0].ActionIndex != 1 ||
		requests[0].Steps[0].Key != "DOWN" ||
		requests[0].Steps[1].ActionIndex != 2 ||
		requests[0].Steps[1].Key != "ENTER" {
		t.Fatalf("first ordered run is wrong: %#v", requests[0])
	}
	if len(requests[1].Steps) != 1 ||
		requests[1].Steps[0].ActionIndex != 4 ||
		requests[1].Steps[0].Key != "TAB" {
		t.Fatalf("second ordered run is wrong: %#v", requests[1])
	}
	for _, request := range requests {
		if request.FinalObservation.CaptureSnapshot == nil ||
			!*request.FinalObservation.CaptureSnapshot {
			t.Fatalf("ordered run lacks canonical boundary: %#v", request)
		}
	}
}

func TestProviderOrderedInputRequestsFallBackWithoutWindowIdentity(t *testing.T) {
	context := testAXBatchPlanContext()
	context.AppToken = "a1"
	actions := []axBatchAction{{
		actionType: "keypress",
		key:        "ENTER",
	}}
	plan, err := compileAXBatchExecutionPlan(
		context,
		actions,
		observation{},
	)
	if err != nil {
		t.Fatal(err)
	}
	requests, err := providerOrderedInputRequestsFromPlan(
		context,
		plan,
		actions,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 0 {
		t.Fatalf("missing provider window identity did not fall back: %#v",
			requests)
	}
}

func orderedInputBatchTestSnapshot(
	request ProviderOrderedInputRequest,
) *ProviderSnapshot {
	snapshot := orderedInputTestSnapshot()
	snapshot.App = AppDescriptor{
		Name:              request.App,
		PID:               10,
		ProviderBusName:   request.AppIdentity.ProviderBusName,
		ProcessGeneration: request.AppIdentity.ProcessGeneration,
		InputCapable:      true,
	}
	snapshot.WindowHandle = request.WindowHandle
	for index := range snapshot.Elements {
		snapshot.Elements[index].ProviderWindowBusName =
			request.ExpectedWindow.ProviderBusName
		snapshot.Elements[index].ProviderWindowObjectPath =
			request.ExpectedWindow.ProviderObjectPath
		snapshot.Elements[index].ProviderWindowProcessGeneration =
			request.ExpectedWindow.ProcessGeneration
		snapshot.Elements[index].ProviderWindowRole =
			request.ExpectedWindow.Role
		snapshot.Elements[index].ProviderWindowName =
			request.ExpectedWindow.Name
	}
	return snapshot
}

func mixedOrderedInputBatchTestSnapshot() *ProviderSnapshot {
	request := testProviderOrderedInputRequest("ENTER")
	request.App = "LibreOffice"
	request.AppIdentity = &ProviderAppIdentity{
		ProviderBusName:   ":1.84",
		ProcessGeneration: "pid:10:start:100",
	}
	request.WindowHandle = "w2:0123456789abcdef0123456789abcdef"
	request.ExpectedWindow = &ProviderWindowExpectation{
		WindowHandle:       request.WindowHandle,
		ProviderBusName:    ":1.84",
		ProcessGeneration:  "pid:10:start:100",
		ProviderObjectPath: "/window/1",
		Role:               "frame",
		Name:               "Sheet",
	}
	snapshot := orderedInputBatchTestSnapshot(request)
	for index := range snapshot.Elements {
		snapshot.Elements[index].ProviderBusName = ":1.84"
		snapshot.Elements[index].ProcessGeneration = "pid:10:start:100"
	}
	snapshot.Elements[3].ProviderObjectPath = "/window/1/save"
	return snapshot
}

func completedOrderedInputObservationFailure(
	request ProviderOrderedInputRequest,
) ProviderOrderedInputResult {
	result := testProviderOrderedInputSuccess(request)
	result.OK = false
	result.Snapshot = nil
	result.Failure = &ProviderFailure{
		Code:          "stale_window_ref",
		Phase:         "observation",
		FactsComplete: true,
		Subject:       "window",
	}
	result.Mutation.MutationUnknown = true
	return result
}

func TestAXMixedBatchExecutesEveryPlannedOrderedInputRun(t *testing.T) {
	service, _, _, elementRef := orderedInputTestService(t)
	legacy := &recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{
			OK:       true,
			Snapshot: mixedOrderedInputBatchTestSnapshot(),
			Mutation: &ProviderMutationFacts{InputEventsEmitted: true},
		},
	}
	backend := &orderedInputRecordingBackend{
		recordingPlatformBackend: legacy,
		execute: func(request ProviderOrderedInputRequest) ProviderOrderedInputResult {
			result := testProviderOrderedInputSuccess(request)
			result.Snapshot = orderedInputBatchTestSnapshot(request)
			return result
		},
	}
	service.Backend = backend

	result := service.axBatch([]any{
		map[string]any{
			"type": "click",
			"ref":  elementRef,
		},
		map[string]any{"type": "keypress", "keys": []any{"DOWN"}},
		map[string]any{"type": "keypress", "keys": []any{"ENTER"}},
		map[string]any{"type": "type", "text": "not-run"},
		map[string]any{"type": "keypress", "keys": []any{"TAB"}},
	},
		observation{mode: contract.ObserveNone},
		"",
	)
	if result.IsError {
		t.Fatalf("mixed ordered batch failed: %s\n%#v", ResultText(result), result)
	}
	if len(backend.requests) != 2 {
		t.Fatalf("ordered requests=%d, want 2: %#v",
			len(backend.requests), backend.requests)
	}
	if len(legacy.actionRequests) != 2 {
		t.Fatalf("legacy semantic actions=%d, want 2: %#v",
			len(legacy.actionRequests), legacy.actionRequests)
	}
	if backend.requests[0].Steps[0].ActionIndex != 1 ||
		backend.requests[0].Steps[1].ActionIndex != 2 ||
		backend.requests[1].Steps[0].ActionIndex != 4 {
		t.Fatalf("ordered runs lost global indices: %#v", backend.requests)
	}
	facts, _ := result.StructuredContent["a11y_batch_result"].(map[string]any)
	entries, _ := facts["actions"].([]map[string]any)
	if len(entries) != 5 ||
		entries[1]["execution_path"] != "provider_ordered_input" ||
		entries[2]["execution_path"] != "provider_ordered_input" ||
		entries[4]["execution_path"] != "provider_ordered_input" ||
		facts["actions_completed_without_call_error"] != 5 {
		t.Fatalf("mixed batch evidence is incomplete: %#v", facts)
	}
}

func TestAXMixedBatchStopsOnOrderedPartialEmissionWithoutRetry(t *testing.T) {
	service, _, _, elementRef := orderedInputTestService(t)
	legacy := &recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{
			OK:       true,
			Snapshot: mixedOrderedInputBatchTestSnapshot(),
			Mutation: &ProviderMutationFacts{InputEventsEmitted: true},
		},
	}
	backend := &orderedInputRecordingBackend{
		recordingPlatformBackend: legacy,
		execute: func(request ProviderOrderedInputRequest) ProviderOrderedInputResult {
			stopped := request.Steps[1].ActionIndex
			return ProviderOrderedInputResult{
				ContractVersion: ProviderOrderedInputContractV1,
				Steps: []ProviderOrderedInputStepResult{
					{
						ActionIndex:       request.Steps[0].ActionIndex,
						Status:            ProviderOrderedInputCompleted,
						DispatchAttempted: true,
						Emission:          ProviderOrderedInputEmissionComplete,
						WindowContinuity:  ProviderOrderedInputWindowSame,
					},
					{
						ActionIndex:       stopped,
						Status:            ProviderOrderedInputFailed,
						DispatchAttempted: true,
						Emission:          ProviderOrderedInputEmissionPartial,
						WindowContinuity:  ProviderOrderedInputWindowSame,
					},
				},
				StoppedActionIndex: &stopped,
				Failure: &ProviderFailure{
					Code:          "input_emission_failed",
					Phase:         "emission",
					FactsComplete: true,
				},
				Mutation: ProviderMutationFacts{
					InputEventsEmitted:   true,
					PartialInputPossible: true,
					DesktopStateMutated:  true,
					MutationUnknown:      true,
				},
			}
		},
	}
	service.Backend = backend

	result := service.axBatch([]any{
		map[string]any{
			"type": "click",
			"ref":  elementRef,
		},
		map[string]any{"type": "keypress", "keys": []any{"A"}},
		map[string]any{"type": "keypress", "keys": []any{"B"}},
		map[string]any{"type": "type", "text": "not-run"},
	},
		observation{mode: contract.ObserveNone},
		"",
	)
	if !result.IsError {
		t.Fatalf("partial ordered emission was reported as success: %#v", result)
	}
	if len(backend.requests) != 1 || len(legacy.actionRequests) != 1 {
		t.Fatalf("failed ordered run was retried or later action ran: ordered=%d legacy=%d",
			len(backend.requests), len(legacy.actionRequests))
	}
	facts, _ := result.StructuredContent["a11y_batch_result"].(map[string]any)
	entries, _ := facts["actions"].([]map[string]any)
	if facts["failed_action_index"] != 2 ||
		facts["actions_completed_without_call_error"] != 2 ||
		facts["first_unattempted_action_index"] != 3 ||
		len(entries) != 3 ||
		entries[2]["emission"] != "partial" ||
		result.StructuredContent["partial_input_possible"] != true {
		t.Fatalf("partial mixed evidence is incomplete: %#v / %#v",
			facts, result.StructuredContent)
	}
	public := publicBatchEvidence(t, result)
	batch := assertPublicBatchCounts(t, public, 4, 2)
	if batch["failed_action_index"] != float64(2) ||
		public["partial_input_possible"] != true ||
		public["mutation_unknown"] != true {
		t.Fatalf("public partial boundary lost: %#v", public)
	}
}

func TestAXWholeOrderedTransportFailurePreservesUncertainty(t *testing.T) {
	service, _, windowRef, _ := orderedInputTestService(t)
	service.Backend = &orderedInputRecordingBackend{
		recordingPlatformBackend: &recordingPlatformBackend{},
		err:                      errors.New("connection closed after request write"),
	}

	result := service.axBatch([]any{
		map[string]any{"type": "keypress", "keys": []any{"A"}},
		map[string]any{"type": "keypress", "keys": []any{"B"}},
	},
		observation{mode: contract.ObserveNone},
		windowRef,
	)
	if !result.IsError {
		t.Fatalf("ordered transport failure was reported as success: %#v", result)
	}
	facts, _ := result.StructuredContent["a11y_batch_result"].(map[string]any)
	if facts["actions_requested"] != 2 ||
		facts["rejected_before_first_action"] != false ||
		facts["partial_input_possible"] != true ||
		facts["mutation_unknown"] != true ||
		result.StructuredContent["partial_input_possible"] != true ||
		result.StructuredContent["mutation_unknown"] != true {
		t.Fatalf("whole ordered transport uncertainty was lost: %#v / %#v",
			facts, result.StructuredContent)
	}
}

func TestAXMixedOrderedTransportFailurePreservesUncertainty(t *testing.T) {
	service, _, _, elementRef := orderedInputTestService(t)
	legacy := &recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{
			OK:       true,
			Snapshot: mixedOrderedInputBatchTestSnapshot(),
			Mutation: &ProviderMutationFacts{InputEventsEmitted: true},
		},
	}
	backend := &orderedInputRecordingBackend{
		recordingPlatformBackend: legacy,
		err:                      errors.New("connection closed after request write"),
	}
	service.Backend = backend

	result := service.axBatch([]any{
		map[string]any{
			"type": "click",
			"ref":  elementRef,
		},
		map[string]any{"type": "keypress", "keys": []any{"A"}},
		map[string]any{"type": "keypress", "keys": []any{"B"}},
		map[string]any{"type": "type", "text": "not-run"},
	},
		observation{mode: contract.ObserveNone},
		"",
	)
	if !result.IsError {
		t.Fatalf("mixed ordered transport failure was reported as success: %#v", result)
	}
	facts, _ := result.StructuredContent["a11y_batch_result"].(map[string]any)
	if facts["actions_requested"] != 4 ||
		facts["actions_completed_without_call_error"] != 1 ||
		facts["rejected_before_first_action"] != false ||
		facts["first_unattempted_action_index"] != nil ||
		result.StructuredContent["partial_input_possible"] != true ||
		result.StructuredContent["mutation_unknown"] != true {
		t.Fatalf("mixed ordered transport uncertainty was lost: %#v / %#v",
			facts, result.StructuredContent)
	}
	if len(backend.requests) != 1 || len(legacy.actionRequests) != 1 {
		t.Fatalf("transport-lost ordered run was retried: ordered=%d legacy=%d",
			len(backend.requests), len(legacy.actionRequests))
	}
}

func TestAXBatchUsesOrderedBackendForWholeUntargetedKeypressRun(t *testing.T) {
	service, _, windowRef, _ := orderedInputTestService(t)
	legacy := &recordingPlatformBackend{}
	backend := &orderedInputRecordingBackend{
		recordingPlatformBackend: legacy,
		execute: func(request ProviderOrderedInputRequest) ProviderOrderedInputResult {
			result := testProviderOrderedInputSuccess(request)
			result.Snapshot = orderedInputBatchTestSnapshot(request)
			return result
		},
	}
	service.Backend = backend

	result := service.axBatch([]any{
		map[string]any{"type": "keypress", "keys": []any{"DOWN"}},
		map[string]any{"type": "keypress", "keys": []any{"ENTER"}},
	},
		observation{mode: contract.ObserveNone},
		windowRef,
	)
	if result.IsError {
		t.Fatalf("ordered batch failed: %s\n%#v", ResultText(result), result)
	}
	if len(backend.requests) != 1 {
		t.Fatalf("ordered backend requests=%d, want 1", len(backend.requests))
	}
	request := backend.requests[0]
	if request.App != "LibreOffice" ||
		request.AppIdentity == nil ||
		request.AppIdentity.ProviderBusName != ":1.84" {
		t.Fatalf("ordered request did not bind runtime app identity: %#v", request)
	}
	if len(legacy.calls) != 0 {
		t.Fatalf("ordered batch also used the legacy path: %#v", legacy.calls)
	}
	facts, _ := result.StructuredContent["a11y_batch_result"].(map[string]any)
	if facts["execution_path"] != "provider_ordered_input" ||
		facts["actions_completed_without_call_error"] != 2 {
		t.Fatalf("ordered batch evidence is incomplete: %#v", facts)
	}
}

func TestAXWholeOrderedInputRecoversCurrentWindowAfterCompletedInput(t *testing.T) {
	service, _, windowRef, _ := orderedInputTestService(t)
	current := mixedOrderedInputBatchTestSnapshot()
	current.WindowHandle = "w2:11111111111111111111111111111111"
	current.WindowTitle = "Find & Replace"
	legacy := &recordingPlatformBackend{
		accessibilityResponses: []ProviderAccessibilityResult{
			{
				OK:    false,
				Error: "state_unavailable: source window changed",
				Failure: &ProviderFailure{
					Code:          "state_unavailable",
					Phase:         "observation",
					FactsComplete: true,
					Subject:       "window",
				},
			},
			{
				OK:       true,
				Snapshot: current,
			},
		},
	}
	backend := &orderedInputRecordingBackend{
		recordingPlatformBackend: legacy,
		execute:                  completedOrderedInputObservationFailure,
	}
	service.Backend = backend

	result := service.axBatch([]any{
		map[string]any{"type": "keypress", "keys": []any{"SUPER", "F"}},
	},
		observation{mode: contract.ObserveDiff},
		windowRef,
	)
	if result.IsError {
		t.Fatalf("completed input with recovered current window failed: %#v", result)
	}
	for key, expected := range map[string]any{
		"final_observation_returned":                                 true,
		"observation_required":                                       false,
		"recovery_observation_returned":                              true,
		"recovery_app_root_retry_attempted":                          true,
		"recovery_targeted_failure_code":                             "state_unavailable",
		"ordered_input_actions_completed_before_observation_failure": true,
		"ordered_input_observation_recovered":                        true,
		"task_effect_verified":                                       false,
	} {
		if result.StructuredContent[key] != expected {
			t.Fatalf("%s=%#v want %#v in %#v",
				key, result.StructuredContent[key], expected,
				result.StructuredContent)
		}
	}
	for _, key := range []string{
		"failure_code", "failure_phase", "effect_unknown", "mutation_unknown",
	} {
		if _, present := result.StructuredContent[key]; present {
			t.Fatalf("%s survived successful recovery: %#v",
				key, result.StructuredContent)
		}
	}
	facts := result.StructuredContent["a11y_batch_result"].(map[string]any)
	if facts["actions_completed_without_call_error"] != 1 ||
		facts["partial_sequence"] != false ||
		facts["mutation_unknown"] != false {
		t.Fatalf("recovered ordered-input facts are inconsistent: %#v", facts)
	}
	if len(legacy.observeRequests) != 2 ||
		legacy.observeRequests[0].WindowHandle == "" ||
		legacy.observeRequests[1].WindowHandle != "" {
		t.Fatalf("recovery did not fall back from window to app root: %#v",
			legacy.observeRequests)
	}

	result.StructuredContent["ax_mode"] = contract.AXModeAct
	prepared := PrepareAXEvidence(result)
	if prepared.StructuredContent["input_emitted"] != true ||
		prepared.StructuredContent["task_effect_verified"] == true {
		t.Fatalf("recovered action overstated causal evidence: %#v", prepared.StructuredContent)
	}
	if _, present := prepared.StructuredContent["mutation_unknown"]; present {
		t.Fatalf("compact result retained resolved mutation uncertainty: %#v",
			prepared.StructuredContent)
	}
}

func TestAXWholeOrderedInputKeepsUncertaintyWhenRecoveryFails(t *testing.T) {
	service, _, windowRef, _ := orderedInputTestService(t)
	recoveryFailure := ProviderAccessibilityResult{
		OK:    false,
		Error: "state_unavailable: no current window",
		Failure: &ProviderFailure{
			Code:          "state_unavailable",
			Phase:         "observation",
			FactsComplete: true,
			Subject:       "window",
		},
	}
	legacy := &recordingPlatformBackend{
		accessibilityResponses: []ProviderAccessibilityResult{
			recoveryFailure,
			recoveryFailure,
		},
	}
	backend := &orderedInputRecordingBackend{
		recordingPlatformBackend: legacy,
		execute:                  completedOrderedInputObservationFailure,
	}
	service.Backend = backend

	result := service.axBatch([]any{
		map[string]any{"type": "keypress", "keys": []any{"SUPER", "F"}},
	},
		observation{mode: contract.ObserveDiff},
		windowRef,
	)
	if !result.IsError ||
		result.StructuredContent["failure_code"] != "stale_window_ref" ||
		result.StructuredContent["mutation_unknown"] != true ||
		result.StructuredContent["observation_required"] != true ||
		result.StructuredContent["recovery_observation_returned"] != false ||
		result.StructuredContent["recovery_observation_failure_code"] !=
			"state_unavailable" {
		t.Fatalf("failed recovery cleared uncertainty: %#v", result)
	}
	facts := result.StructuredContent["a11y_batch_result"].(map[string]any)
	if facts["partial_sequence"] != false ||
		facts["mutation_unknown"] != true {
		t.Fatalf("failed recovery corrupted ordered-input facts: %#v", facts)
	}
	if len(legacy.observeRequests) != 2 {
		t.Fatalf("recovery observe calls=%d, want 2",
			len(legacy.observeRequests))
	}
}

func TestAXBatchPreservesOrderedPartialEmissionWithoutLegacyRetry(t *testing.T) {
	service, _, windowRef, _ := orderedInputTestService(t)
	legacy := &recordingPlatformBackend{}
	backend := &orderedInputRecordingBackend{
		recordingPlatformBackend: legacy,
		execute: func(request ProviderOrderedInputRequest) ProviderOrderedInputResult {
			stopped := request.Steps[1].ActionIndex
			unattempted := request.Steps[2].ActionIndex
			return ProviderOrderedInputResult{
				ContractVersion: ProviderOrderedInputContractV1,
				Steps: []ProviderOrderedInputStepResult{
					{
						ActionIndex:       request.Steps[0].ActionIndex,
						Status:            ProviderOrderedInputCompleted,
						DispatchAttempted: true,
						Emission:          ProviderOrderedInputEmissionComplete,
						WindowContinuity:  ProviderOrderedInputWindowSame,
					},
					{
						ActionIndex:       stopped,
						Status:            ProviderOrderedInputFailed,
						DispatchAttempted: true,
						Emission:          ProviderOrderedInputEmissionPartial,
						WindowContinuity:  ProviderOrderedInputWindowSame,
					},
					{
						ActionIndex: request.Steps[2].ActionIndex,
						Status:      ProviderOrderedInputNotAttempted,
						Emission:    ProviderOrderedInputEmissionNone,
					},
				},
				StoppedActionIndex:          &stopped,
				FirstUnattemptedActionIndex: &unattempted,
				Failure: &ProviderFailure{
					Code:          "input_emission_failed",
					Phase:         "emission",
					FactsComplete: true,
				},
				Mutation: ProviderMutationFacts{
					InputEventsEmitted:   true,
					PartialInputPossible: true,
					DesktopStateMutated:  true,
					MutationUnknown:      true,
				},
			}
		},
	}
	service.Backend = backend

	result := service.axBatch([]any{
		map[string]any{"type": "keypress", "keys": []any{"A"}},
		map[string]any{"type": "keypress", "keys": []any{"B"}},
		map[string]any{"type": "keypress", "keys": []any{"C"}},
	},
		observation{mode: contract.ObserveNone},
		windowRef,
	)
	if !result.IsError {
		t.Fatalf("partial ordered emission was reported as success: %#v", result)
	}
	if len(backend.requests) != 1 || len(legacy.calls) != 0 {
		t.Fatalf("partial ordered batch was retried: ordered=%d legacy=%#v",
			len(backend.requests), legacy.calls)
	}
	facts, _ := result.StructuredContent["a11y_batch_result"].(map[string]any)
	actions, _ := facts["actions"].([]map[string]any)
	if len(actions) != 3 ||
		actions[1]["emission"] != "partial" ||
		actions[2]["status"] != "not_attempted" ||
		facts["first_unattempted_action_index"] != 2 {
		t.Fatalf("partial ordered evidence is incomplete: %#v", facts)
	}
}
