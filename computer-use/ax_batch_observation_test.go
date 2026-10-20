package main

import (
	"strings"
	"testing"
)

func TestVerifiedCloseClearsTopLevelAndNestedUncertainty(t *testing.T) {
	structured := map[string]any{
		"effect_unknown":     true,
		"mutation_unknown":   true,
		"cause_unattributed": true,
	}
	action := map[string]any{
		"mutation_unknown":   true,
		"cause_unattributed": true,
	}
	clearVerifiedCloseUncertainty(structured, action)
	for _, key := range []string{
		"effect_unknown",
		"mutation_unknown",
		"cause_unattributed",
	} {
		if _, present := structured[key]; present {
			t.Fatalf("verified close retained top-level %s: %#v",
				key, structured)
		}
		if _, present := action[key]; present {
			t.Fatalf("verified close retained action-level %s: %#v",
				key, action)
		}
	}
}

func TestPostconditionFinalObservationUsesLatestSnapshot(t *testing.T) {
	service := newService()
	current := &appSnapshot{
		App:                 appDescriptor{Name: "Editor"},
		WindowTitle:         "Document",
		WindowHandle:        "window-1",
		SnapshotID:          "after",
		ScreenshotPNGBase64: "latest-image",
		ScreenshotMimeType:  "image/png",
	}
	result := service.renderAXBatchFinalObservation(
		current,
		nil,
		observation{mode: observeScreenshot},
	)
	foundLatest := false
	for _, item := range result.Content {
		if item.Type == "image" && item.Data == "latest-image" {
			foundLatest = true
			break
		}
	}
	if !foundLatest {
		t.Fatalf("postcondition final screenshot did not use the latest snapshot: %#v",
			result.Content)
	}
	if result.StructuredContent["final_observation_returned"] != true {
		t.Fatalf("final observation evidence missing: %#v", result.StructuredContent)
	}
}

func TestFinalObservationContinuationRequiresCompleteGoneSourceAndSameApp(t *testing.T) {
	service, appRef, sourceWindowRef, _ := sheetBatchService(t)
	currentWindowRef := service.windowRefRegistry.Acquire(
		"Editor",
		"w2:11111111111111111111111111111111",
		`window "Document"`,
		"",
		":1.8",
		"/org/a11y/atspi/accessible/8",
		"pid:44:start:100",
		"frame",
		"Document",
		appRef,
	)
	context := &axBatchContext{
		appRef:       appRef,
		windowRef:    sourceWindowRef,
		appToken:     appRef,
		windowHandle: "w2:0123456789abcdef0123456789abcdef",
	}
	valid := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":                "stale_window_ref",
			"source_window_status":        "gone",
			"recovery_inventory_complete": true,
			"current_window_ref":          currentWindowRef,
		},
	}
	entry, ok := service.finalObservationContinuation(context, valid)
	if !ok || entry == nil || entry.Ref != currentWindowRef {
		t.Fatalf("valid final observation continuation rejected: %#v, %t", entry, ok)
	}

	for name, mutate := range map[string]func(map[string]any){
		"incomplete inventory": func(fields map[string]any) {
			fields["recovery_inventory_complete"] = false
		},
		"source not gone": func(fields map[string]any) {
			fields["source_window_status"] = "present"
		},
		"wrong failure": func(fields map[string]any) {
			fields["failure_code"] = "observation_incomplete"
		},
		"same window": func(fields map[string]any) {
			fields["current_window_ref"] = sourceWindowRef
		},
	} {
		t.Run(name, func(t *testing.T) {
			fields := map[string]any{}
			for key, value := range valid.StructuredContent {
				fields[key] = value
			}
			mutate(fields)
			if entry, ok := service.finalObservationContinuation(
				context,
				toolCallResult{IsError: true, StructuredContent: fields},
			); ok || entry != nil {
				t.Fatalf("unsafe continuation accepted: %#v", entry)
			}
		})
	}
}

func TestFinalObservationFollowsVerifiedTransientWindowClosure(t *testing.T) {
	for _, mode := range []string{observeScreenshot, observeTree, observeFull} {
		t.Run(mode, func(t *testing.T) {
			testFinalObservationFollowsVerifiedTransientWindowClosure(t, mode)
		})
	}
}

func testFinalObservationFollowsVerifiedTransientWindowClosure(t *testing.T, mode string) {
	service, appRef, sourceWindowRef, _ := sheetBatchService(t)
	backend := &recordingPlatformBackend{
		accessibilityResponses: []ProviderAccessibilityResult{
			{
				OK:    false,
				Error: "stale_window_ref: source dialog is gone",
				Failure: &ProviderFailure{
					Code:          "stale_window_ref",
					Phase:         "precondition",
					FactsComplete: true,
					Subject:       "window",
				},
				RecoveryContext: &ProviderRecoveryContext{
					SourceAppStatus:    "alive",
					SourceWindowStatus: "gone",
					InventoryComplete:  true,
					CurrentWindow: &ProviderReplacementWindow{
						WindowHandle:       "w2:11111111111111111111111111111111",
						Title:              "Document",
						Role:               "frame",
						ProviderBusName:    ":1.84",
						ProviderObjectPath: "/window/current",
						ProcessGeneration:  "pid:10:start:100",
					},
				},
			},
			{
				OK: true,
				Snapshot: &ProviderSnapshot{
					App:                 appDescriptor{Name: "LibreOffice"},
					WindowTitle:         "Document",
					WindowHandle:        "w2:11111111111111111111111111111111",
					ScreenshotPNGBase64: "current-image",
					ScreenshotMimeType:  "image/png",
					TreeLines:           []string{`0 frame "Current document"`, ` 1 push button "Current action"`},
					Elements: []elementRecord{
						{Index: 0, RuntimeID: []int{0}, ControlType: "frame", Name: "Current document"},
						{Index: 1, RuntimeID: []int{0, 0}, ControlType: "push button", Name: "Current action"},
					},
				},
			},
		},
	}
	service.backend = backend
	context := &axBatchContext{
		appRef:       appRef,
		windowRef:    sourceWindowRef,
		appToken:     appRef,
		windowHandle: "w2:0123456789abcdef0123456789abcdef",
	}
	result := service.axBatchFinalObservation(
		context,
		observation{mode: mode},
		false,
	)
	if result.IsError ||
		result.StructuredContent["final_observation_returned"] != true ||
		result.StructuredContent["final_observation_followed_window_transition"] !=
			true {
		t.Fatalf("verified window transition did not return current observation: %#v",
			result)
	}
	if len(backend.observeRequests) != 2 {
		t.Fatalf("observe calls=%d, want 2", len(backend.observeRequests))
	}
	if mode == observeTree || mode == observeFull {
		if !strings.Contains(resultText(result), `button "Current action"`) {
			t.Fatalf("replacement window tree missing: %s", resultText(result))
		}
	}
	if mode == observeFull || mode == observeScreenshot {
		foundImage := false
		for _, item := range result.Content {
			foundImage = foundImage || item.Type == "image" && item.Data == "current-image"
		}
		if !foundImage || backend.observeRequests[1].Observation.IncludeScreenshot == nil ||
			!*backend.observeRequests[1].Observation.IncludeScreenshot {
			t.Fatal("replacement window final screenshot not requested or returned")
		}
	}
	if backend.observeRequests[0].WindowHandle != context.windowHandle ||
		backend.observeRequests[1].WindowHandle !=
			"w2:11111111111111111111111111111111" {
		t.Fatalf("unexpected observation targets: %#v", backend.observeRequests)
	}
	if _, err := service.windowRefRegistry.Resolve(sourceWindowRef, ""); err == nil ||
		!strings.Contains(err.Error(), string(AXRefStale)) {
		t.Fatalf("gone source window ref was not tombstoned: %v", err)
	}
}

func TestFinalObservationReturnsLifecycleWhenClosedAppRefIsStale(
	t *testing.T,
) {
	service, appRef, sourceWindowRef, _ := sheetBatchService(t)
	backend := &recordingPlatformBackend{
		accessibilityResponses: []ProviderAccessibilityResult{{
			OK:                   false,
			Error:                "stale_app_ref: process generation no longer exists",
			FailureCode:          "stale_app_ref",
			FailurePhase:         "precondition",
			FailureSubject:       "app",
			FailureFactsComplete: true,
		}},
	}
	service.backend = backend
	context := &axBatchContext{
		appRef:       appRef,
		windowRef:    sourceWindowRef,
		appToken:     appRef,
		windowHandle: "w2:0123456789abcdef0123456789abcdef",
	}
	result := service.axBatchFinalObservation(
		context,
		observation{mode: observeDiff},
		true,
	)
	if result.IsError ||
		result.StructuredContent["target_window_absent"] != true ||
		result.StructuredContent["final_observation_returned"] != true ||
		result.StructuredContent["observation_required"] == true {
		t.Fatalf("closed app ref did not produce lifecycle observation: %#v",
			result)
	}
}

func TestClosedWindowLifecycleRejectsIncompleteStaleAppFacts(t *testing.T) {
	context := &axBatchContext{windowRef: "w7"}
	for name, fields := range map[string]map[string]any{
		"incomplete facts": {
			"failure_code":           "stale_app_ref",
			"failure_subject":        "app",
			"failure_facts_complete": false,
		},
		"wrong subject": {
			"failure_code":           "stale_app_ref",
			"failure_subject":        "window",
			"failure_facts_complete": true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if closedWindowLifecycleProven(
				context,
				toolCallResult{IsError: true, StructuredContent: fields},
			) {
				t.Fatalf("unsafe stale-app lifecycle accepted: %#v", fields)
			}
		})
	}
}

func TestSuccessfulCloseRecoversLifecycleObservation(t *testing.T) {
	service, appRef, sourceWindowRef, _ := sheetBatchService(t)
	backend := &recordingPlatformBackend{
		accessibilityResponses: []ProviderAccessibilityResult{{
			OK:    false,
			Error: "stale_window_ref: source window is gone",
			Failure: &ProviderFailure{
				Code:          "stale_window_ref",
				Phase:         "precondition",
				FactsComplete: true,
				Subject:       "window",
			},
			RecoveryContext: &ProviderRecoveryContext{
				SourceAppStatus:    "absent",
				SourceWindowStatus: "gone",
				InventoryComplete:  true,
			},
		}},
	}
	service.backend = backend
	context := &axBatchContext{
		appRef:       appRef,
		windowRef:    sourceWindowRef,
		appToken:     appRef,
		windowHandle: "w2:0123456789abcdef0123456789abcdef",
	}
	actionResult := toolCallResult{
		StructuredContent: map[string]any{
			"final_observation_returned": false,
			"observation_required":       true,
			"input_events_emitted":       true,
			"effect_unknown":             true,
			"mutation_unknown":           true,
		},
	}
	result := service.recoverClosedWindowActionObservation(
		context,
		axBatchAction{actionType: "close"},
		actionResult,
		observation{mode: observeDiff},
	)
	if result.IsError ||
		result.StructuredContent["target_window_absent"] != true ||
		result.StructuredContent["final_observation_returned"] != true ||
		result.StructuredContent["observation_required"] != false {
		t.Fatalf("successful close did not recover lifecycle evidence: %#v", result)
	}
	if result.StructuredContent["effect_unknown"] == true ||
		result.StructuredContent["mutation_unknown"] == true {
		t.Fatalf("verified close retained superseded uncertainty: %#v", result)
	}
	if len(backend.observeRequests) != 1 {
		t.Fatalf("close recovery observations=%d, want 1", len(backend.observeRequests))
	}
}

func TestFinalObservationDoesNotInferCloseFromNonCloseWindowDisappearance(
	t *testing.T,
) {
	service, appRef, sourceWindowRef, _ := sheetBatchService(t)
	service.backend = &recordingPlatformBackend{
		accessibilityResponses: []ProviderAccessibilityResult{{
			OK:    false,
			Error: "stale_window_ref: source window is gone",
			Failure: &ProviderFailure{
				Code:          "stale_window_ref",
				Phase:         "precondition",
				FactsComplete: true,
				Subject:       "window",
			},
			RecoveryContext: &ProviderRecoveryContext{
				SourceAppStatus:    "alive",
				SourceWindowStatus: "gone",
				InventoryComplete:  true,
			},
		}},
	}
	context := &axBatchContext{
		appRef:       appRef,
		windowRef:    sourceWindowRef,
		appToken:     appRef,
		windowHandle: "w2:0123456789abcdef0123456789abcdef",
	}
	result := service.axBatchFinalObservation(
		context,
		observation{mode: observeDiff},
		false,
	)
	if !result.IsError ||
		result.StructuredContent["target_window_absent"] == true ||
		result.StructuredContent["observation_required"] != true {
		t.Fatalf("non-close disappearance became lifecycle evidence: %#v", result)
	}
}

func TestEmptyScreenshotDoesNotClaimFinalObservation(t *testing.T) {
	snapshot := &appSnapshot{
		App:          appDescriptor{Name: "Editor"},
		WindowTitle:  "Document",
		WindowHandle: "window-1",
	}
	result, returned := renderAXScreenshotObservation(snapshot, nil)
	if returned ||
		result.StructuredContent["final_observation_returned"] != false ||
		result.StructuredContent["screenshot_returned"] != false ||
		result.StructuredContent["final_observation_kind"] != "unavailable" ||
		result.StructuredContent["observation_required"] != true {
		t.Fatalf("empty screenshot overstated final observation: %#v",
			result.StructuredContent)
	}
}

func TestEmptyCompactFallbackDoesNotClaimFinalObservation(t *testing.T) {
	service := newService()
	snapshot := &appSnapshot{
		App:          appDescriptor{Name: "Editor"},
		WindowTitle:  "Document",
		WindowHandle: "window-1",
		TreeLines:    []string{"ax:"},
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: nil,
			Roots:          nil,
		},
	}
	result := service.renderAXBatchFinalObservation(
		snapshot,
		nil,
		observation{mode: observeDiff},
	)
	if result.IsError {
		t.Fatalf("missing compact observation must not reverse an emitted action: %#v",
			result)
	}
	if result.StructuredContent["final_observation_returned"] != false ||
		result.StructuredContent["final_observation_kind"] != "unavailable" ||
		result.StructuredContent["observation_required"] != true ||
		result.StructuredContent["delivered_snapshot_baseline_advanced"] != false {
		t.Fatalf("empty compact fallback overstated final observation: %#v",
			result.StructuredContent)
	}
	if !strings.Contains(resultText(result), "final_observation_unavailable") {
		t.Fatalf("missing compact observation lacked recovery text: %q",
			resultText(result))
	}
}

func TestSharedDiffFallbackDoesNotClaimEmptyObservation(t *testing.T) {
	service := newService()
	snapshot := &appSnapshot{
		App:          appDescriptor{Name: "Editor"},
		WindowTitle:  "Document",
		WindowHandle: "window-1",
		TreeLines:    []string{"ax:"},
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: nil,
			Roots:          nil,
		},
	}
	result, lines, returned, compact, fullTree :=
		service.renderAXDiffOrCompactObservation(snapshot, nil)
	if returned || compact || fullTree || len(lines) != 0 {
		t.Fatalf("empty fallback was reported as an observation: returned=%t compact=%t full=%t lines=%#v",
			returned, compact, fullTree, lines)
	}
	if result.StructuredContent["final_observation_returned"] != false ||
		result.StructuredContent["observation_required"] != true ||
		result.StructuredContent["next_identical_observation_required"] != true {
		t.Fatalf("shared action fallback overstated observation evidence: %#v",
			result.StructuredContent)
	}
}

func TestSharedDiffFallbackIdentifiesFocusedObservation(t *testing.T) {
	service := newService()
	snapshot := editorFormSnapshot()
	typedTreeTextForTest(t, snapshot)
	result, lines, returned, compact, fullTree :=
		service.renderAXDiffOrCompactObservation(snapshot, nil)
	if !returned || !compact || fullTree || len(lines) == 0 ||
		result.StructuredContent["final_observation_kind"] != "focused" {
		t.Fatalf("focused observation kind is incomplete: result=%#v lines=%#v",
			result.StructuredContent, lines)
	}
}

func TestSharedDiffFallbackPrefersWorkingTreeForStabilizedWindow(t *testing.T) {
	service := newService()
	snapshot := editorFormSnapshot()
	typedTreeTextForTest(t, snapshot)

	result, lines, returned, compact, fullTree :=
		service.renderAXWorkingObservation(snapshot, nil)
	if !returned || compact || !fullTree || len(lines) <= 1 {
		t.Fatalf("stabilized working tree missing: returned=%t compact=%t full=%t lines=%#v",
			returned, compact, fullTree, lines)
	}
	if result.StructuredContent["final_observation_kind"] != "working_tree" ||
		!strings.Contains(resultText(result), `button "Save"`) {
		t.Fatalf("stabilized working tree is incomplete: %#v\n%s",
			result.StructuredContent, resultText(result))
	}
}

func TestBatchFinalObservationAdvancesFreshWorkingTreeBaseline(t *testing.T) {
	service := newService()
	snapshot := editorFormSnapshot()
	for index := range snapshot.Elements {
		snapshot.Elements[index].States = strings.ReplaceAll(
			snapshot.Elements[index].States,
			"[focused]",
			"",
		)
	}
	typedTreeTextForTest(t, snapshot)
	snapshot.sourceApp = "editor"
	snapshot.publicWindowRef = "w2"
	snapshot.projection = snapshotProjection{
		View:  treeViewWorking,
		Scope: axScopeWindow,
	}

	result := service.renderAXBatchFinalObservation(
		snapshot,
		nil,
		observation{mode: observeDiff},
	)
	if result.StructuredContent["final_observation_returned"] != true ||
		result.StructuredContent["observation_required"] == true ||
		result.StructuredContent["full_tree_returned"] != true ||
		result.StructuredContent["delivered_snapshot_baseline_advanced"] != true {
		t.Fatalf("fresh working tree did not close the observation loop: %#v",
			result.StructuredContent)
	}
	if got := service.deliveredSnapshotFor(snapshot); got != snapshot {
		t.Fatalf("fresh working tree did not become the new diff baseline: got=%#v want=%#v",
			got, snapshot)
	}
}

func TestStripSupersededObservationPlaceholderPreservesOtherLines(t *testing.T) {
	content := []contentItem{{
		Type: "text",
		Text: "Note: input emitted\n\n" +
			"(observe=\"none\": no final tree or screenshot was returned.)\n" +
			"failure detail",
	}}
	filtered := stripSupersededAXObservationPlaceholder(content)
	if len(filtered) != 1 {
		t.Fatalf("filtered content=%#v", filtered)
	}
	text := filtered[0].Text
	if strings.Contains(text, `observe="none"`) {
		t.Fatalf("placeholder survived: %q", text)
	}
	for _, want := range []string{"Note: input emitted", "failure detail"} {
		if !strings.Contains(text, want) {
			t.Fatalf("non-placeholder evidence %q was removed: %q", want, text)
		}
	}
}

func TestFinalObservationFailureDoesNotOverwritePostconditionFailure(t *testing.T) {
	action := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":         "postcondition_unknown",
			"failure_phase":        "postcondition",
			"input_events_emitted": true,
		},
	}
	observation := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":               "observation_incomplete",
			"failure_phase":              "final_observation",
			"final_observation_returned": false,
		},
	}
	merged := mergeAXBatchFinalObservation(action, observation)
	if merged.StructuredContent["failure_code"] != "postcondition_unknown" ||
		merged.StructuredContent["failure_phase"] != "postcondition" ||
		!merged.IsError {
		t.Fatalf("final observation masked postcondition failure: %#v",
			merged.StructuredContent)
	}
}

func TestRecoveryObservationPreservesOriginalUncertainty(t *testing.T) {
	failed := toolCallResult{
		IsError: true,
		Content: []contentItem{{Type: "text", Text: "effect unknown"}},
		StructuredContent: map[string]any{
			"failure_code":         "effect_unknown",
			"failure_phase":        "action",
			"input_events_emitted": true,
			"mutation_unknown":     true,
			"observation_required": true,
			"continuation": map[string]any{
				"state":     "observe_required",
				"next_mode": "observe",
			},
		},
	}
	observation := toolCallResult{
		Content: []contentItem{{Type: "text", Text: "fresh current state"}},
		StructuredContent: map[string]any{
			"final_observation_returned": true,
			"observation_required":       false,
		},
	}
	merged := mergeAXBatchRecoveryObservation(failed, observation)
	if !merged.IsError ||
		merged.StructuredContent["failure_code"] != "effect_unknown" ||
		merged.StructuredContent["failure_phase"] != "action" ||
		merged.StructuredContent["input_events_emitted"] != true ||
		merged.StructuredContent["mutation_unknown"] != true ||
		merged.StructuredContent["final_observation_returned"] != true ||
		merged.StructuredContent["observation_required"] != false ||
		merged.StructuredContent["recovery_observation_attempted"] != true ||
		merged.StructuredContent["recovery_observation_returned"] != true ||
		merged.StructuredContent["cause_unattributed"] != true {
		t.Fatalf("recovery observation corrupted action evidence: %#v", merged)
	}
	if _, present := merged.StructuredContent["continuation"]; present {
		t.Fatalf("successful recovery retained observe-required continuation: %#v",
			merged.StructuredContent)
	}
}

func TestRecoveryObservationFailureStillRequiresObservation(t *testing.T) {
	failed := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":         "effect_unknown",
			"failure_phase":        "action",
			"input_events_emitted": true,
			"mutation_unknown":     true,
			"observation_required": true,
		},
	}
	observation := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":               "observation_incomplete",
			"final_observation_returned": false,
			"observation_required":       true,
		},
	}
	merged := mergeAXBatchRecoveryObservation(failed, observation)
	if merged.StructuredContent["failure_code"] != "effect_unknown" ||
		merged.StructuredContent["observation_required"] != true ||
		merged.StructuredContent["recovery_observation_returned"] != false ||
		merged.StructuredContent["recovery_observation_failure_code"] !=
			"observation_incomplete" {
		t.Fatalf("failed recovery lost continuation evidence: %#v", merged)
	}
}

func TestRecoveryObservationRetriesAtAppRootWhenReplacementRefStales(t *testing.T) {
	service, appRef, sourceWindowRef, _ := sheetBatchService(t)
	replacementHandle := "w2:11111111111111111111111111111111"
	replacementRef := service.windowRefRegistry.Acquire(
		"editor",
		replacementHandle,
		`window "Document"`,
		"c2",
		":1.84",
		"/app/window/document",
		"pid:10:start:100",
		"window",
		"Document",
		appRef,
	)
	current := editorFormSnapshot()
	current.App = appDescriptor{
		Name:              "Editor",
		ProviderBusName:   ":1.84",
		ProcessGeneration: "pid:10:start:100",
	}
	current.WindowTitle = "Document"
	current.WindowHandle = replacementHandle
	current.SnapshotID = "current"
	backend := &recordingPlatformBackend{
		accessibilityResponses: []ProviderAccessibilityResult{
			{
				OK:    false,
				Error: "stale_window_ref: replacement changed again",
				Failure: &ProviderFailure{
					Code:          "stale_window_ref",
					Phase:         "precondition",
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
	service.backend = backend
	failed := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":         "stale_target",
			"failure_phase":        "between_actions",
			"input_events_emitted": true,
			"mutation_unknown":     true,
			"observation_required": true,
			"current_window_ref":   replacementRef,
		},
	}
	context := &axBatchContext{
		appRef:       appRef,
		appToken:     appRef,
		windowRef:    sourceWindowRef,
		windowHandle: "w2:0123456789abcdef0123456789abcdef",
	}

	result := service.recoverAXBatchObservation(
		context,
		failed,
		observation{mode: observeDiff},
	)
	if !result.IsError {
		t.Fatalf("recovered observation must preserve the stopped batch error")
	}
	for key, expected := range map[string]any{
		"failure_code":                        "stale_target",
		"failure_phase":                       "between_actions",
		"final_observation_returned":          true,
		"observation_required":                false,
		"next_identical_observation_required": false,
		"recovery_observation_returned":       true,
		"recovery_app_root_retry_attempted":   true,
		"recovery_targeted_failure_code":      "stale_window_ref",
		"cause_unattributed":                  true,
	} {
		if result.StructuredContent[key] != expected {
			t.Fatalf("%s=%#v want %#v in %#v",
				key, result.StructuredContent[key], expected,
				result.StructuredContent)
		}
	}
	if len(backend.observeRequests) != 2 {
		t.Fatalf("observe calls=%d, want 2", len(backend.observeRequests))
	}
	if backend.observeRequests[0].WindowHandle != replacementHandle {
		t.Fatalf("first recovery was not replacement-targeted: %#v",
			backend.observeRequests[0])
	}
	if backend.observeRequests[1].WindowHandle != "" {
		t.Fatalf("second recovery was not app-rooted: %#v",
			backend.observeRequests[1])
	}
	if text := resultText(result); !strings.Contains(text, `button "Save"`) ||
		!strings.Contains(text, "window-targeted read became stale") {
		t.Fatalf("app-root recovery observation missing: %q", text)
	}
}

func TestRecoveryObservationDoesNotRetryUnrelatedFailureAtAppRoot(t *testing.T) {
	for _, code := range []string{"permission_denied", "invalid_argument"} {
		result := toolCallResult{
			IsError: true,
			StructuredContent: map[string]any{
				"failure_code": code,
			},
		}
		if axRecoveryObservationCanRetryAtAppRoot(result) {
			t.Fatalf("%s unexpectedly allowed app-root retry", code)
		}
	}
}

func TestRecoveryObservationPreservesNonObserveContinuation(t *testing.T) {
	failed := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":         "effect_unknown",
			"observation_required": true,
		},
	}
	observation := toolCallResult{
		StructuredContent: map[string]any{
			"final_observation_returned": true,
			"continuation": map[string]any{
				"state":     "ready",
				"route":     "GUI",
				"next_mode": "screenshot",
			},
		},
	}
	merged := mergeAXBatchRecoveryObservation(failed, observation)
	continuation, ok := merged.StructuredContent["continuation"].(map[string]any)
	if !ok || continuation["route"] != "GUI" {
		t.Fatalf("successful recovery dropped unrelated continuation: %#v",
			merged.StructuredContent)
	}
}

func TestVerifiedCloseScreenshotDoesNotBecomeEffectUnknown(t *testing.T) {
	action := toolCallResult{
		StructuredContent: map[string]any{
			"input_events_emitted":   true,
			"postcondition_verified": true,
		},
	}
	observation := closedWindowFinalObservation(
		&axBatchContext{windowRef: "w7"},
		observation{mode: observeScreenshot},
	)
	merged := mergeAXBatchFinalObservation(action, observation)
	if merged.IsError ||
		merged.StructuredContent["target_window_absent"] != true ||
		merged.StructuredContent["observation_required"] != true ||
		merged.StructuredContent["next_identical_observation_required"] != false {
		t.Fatalf("verified close lifecycle evidence was lost: %#v", merged)
	}
	if _, ok := merged.StructuredContent["effect_unknown"]; ok {
		t.Fatalf("verified close was marked effect_unknown: %#v",
			merged.StructuredContent)
	}
	if _, ok := merged.StructuredContent["mutation_unknown"]; ok {
		t.Fatalf("verified close was marked mutation_unknown: %#v",
			merged.StructuredContent)
	}
}

func TestMissingFinalObservationMarksEmittedMutationUnknown(t *testing.T) {
	action := toolCallResult{
		StructuredContent: map[string]any{
			"input_events_emitted": true,
			"mutation_unknown":     false,
		},
	}
	observation := toolCallResult{
		StructuredContent: map[string]any{
			"final_observation_returned": false,
			"observation_required":       true,
		},
	}
	merged := mergeAXBatchFinalObservation(action, observation)
	if merged.IsError ||
		merged.StructuredContent["effect_unknown"] != true ||
		merged.StructuredContent["mutation_unknown"] != true ||
		merged.StructuredContent["observation_failure_code"] !=
			"final_observation_unavailable" {
		t.Fatalf("missing final observation lost uncertainty: %#v", merged)
	}
}

func TestMissingFinalObservationUsesNestedRuntimeEmissionEvidence(t *testing.T) {
	action := toolCallResult{
		StructuredContent: map[string]any{
			"runtime_action_evidence": RuntimeActionEvidence{
				Route:        "semantic_action",
				InputEmitted: true,
			},
			"actionability": ActionabilityEvidence{
				Route:        "semantic_action",
				InputEmitted: TriTrue,
			},
		},
	}
	observation := toolCallResult{
		StructuredContent: map[string]any{
			"final_observation_returned": false,
			"observation_required":       true,
		},
	}
	merged := mergeAXBatchFinalObservation(action, observation)
	if merged.IsError ||
		merged.StructuredContent["input_events_emitted"] != true ||
		merged.StructuredContent["effect_unknown"] != true ||
		merged.StructuredContent["mutation_unknown"] != true {
		t.Fatalf("nested emission evidence was ignored: %#v", merged)
	}
}

func TestFinalObservationFailurePreservesSuccessfulAction(t *testing.T) {
	action := toolCallResult{
		Content: []contentItem{{Type: "text", Text: "action delivered"}},
		StructuredContent: map[string]any{
			"input_events_emitted": true,
			"mutation_unknown":     false,
		},
	}
	observation := toolCallResult{
		IsError: true,
		Content: []contentItem{{
			Type: "text",
			Text: "runtime_timeout: final AX observation timed out",
		}},
		StructuredContent: map[string]any{
			"failure_code":               "runtime_timeout",
			"failure_phase":              "final_observation",
			"final_observation_returned": false,
			"observation_required":       true,
		},
	}
	merged := mergeAXBatchFinalObservation(action, observation)
	if merged.IsError {
		t.Fatalf("post-action observation failure reversed successful delivery: %#v",
			merged)
	}
	if merged.StructuredContent["input_events_emitted"] != true ||
		merged.StructuredContent["effect_unknown"] != true ||
		merged.StructuredContent["mutation_unknown"] != true ||
		merged.StructuredContent["observation_failure_code"] != "runtime_timeout" ||
		merged.StructuredContent["final_observation_returned"] != false ||
		merged.StructuredContent["observation_required"] != true {
		t.Fatalf("post-action observation evidence is incomplete: %#v",
			merged.StructuredContent)
	}
	if _, present := merged.StructuredContent["failure_code"]; present {
		t.Fatalf("observation failure was exposed as action failure: %#v",
			merged.StructuredContent)
	}
}

func TestBatchFinalActionCapturesRequestedObservationDirectly(t *testing.T) {
	final := observation{mode: observeDiff}
	intermediate := axBatchActionObservation(0, 2, final, false)
	if intermediate.resolvedMode() != observeNone ||
		intermediate.captureSnapshot == nil ||
		!*intermediate.captureSnapshot {
		t.Fatalf("intermediate action observation=%#v", intermediate)
	}
	last := axBatchActionObservation(1, 2, final, false)
	if last.resolvedMode() != observeDiff || last.captureSnapshot != nil {
		t.Fatalf("final action did not own requested observation: %#v", last)
	}
	postconditionLast := axBatchActionObservation(1, 2, final, true)
	if postconditionLast.resolvedMode() != observeNone ||
		postconditionLast.captureSnapshot == nil ||
		*postconditionLast.captureSnapshot {
		t.Fatalf("postcondition final action captured the wrong time point: %#v",
			postconditionLast)
	}
}

func TestFinalObservationCannotRewriteEmittedActionAsPreflight(t *testing.T) {
	action := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":         "postcondition_unknown",
			"failure_phase":        "postcondition",
			"input_events_emitted": true,
			"mutation_unknown":     true,
			"preflight_only":       false,
			"evidence_scope":       "post_action_observation",
		},
	}
	observation := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":               "observation_incomplete",
			"failure_phase":              "final_observation",
			"final_observation_returned": false,
			"preflight_only":             true,
			"evidence_scope":             "error_only",
		},
	}
	merged := mergeAXBatchFinalObservation(action, observation)
	if merged.StructuredContent["preflight_only"] != false {
		t.Fatalf("emitted action was rewritten as preflight: %#v",
			merged.StructuredContent)
	}
	if merged.StructuredContent["evidence_scope"] != "post_action_observation" {
		t.Fatalf("post-action evidence scope was lost: %#v",
			merged.StructuredContent)
	}
}

func TestFinalObservationCannotClearMonotonicActionEvidence(t *testing.T) {
	action := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":           "postcondition_unknown",
			"failure_phase":          "postcondition",
			"input_events_emitted":   true,
			"partial_input_possible": true,
			"mutation_unknown":       true,
		},
	}
	observation := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"input_events_emitted":   false,
			"partial_input_possible": false,
			"mutation_unknown":       false,
		},
	}
	merged := mergeAXBatchFinalObservation(action, observation)
	for _, key := range []string{
		"input_events_emitted",
		"partial_input_possible",
		"mutation_unknown",
	} {
		if merged.StructuredContent[key] != true {
			t.Fatalf("%s was cleared by final observation: %#v",
				key, merged.StructuredContent)
		}
	}
}

func TestVerifiedCloseUsesLifecycleObservationInsteadOfClosedWindowRead(t *testing.T) {
	result := closedWindowFinalObservation(
		&axBatchContext{windowRef: "w7"},
		observation{mode: observeDiff},
	)
	if result.IsError {
		t.Fatalf("verified close lifecycle observation must not be an error: %#v", result)
	}
	text := resultText(result)
	if !strings.Contains(text, "effect: target_window_absent") ||
		!strings.Contains(text, "absent_refs: [w7]") {
		t.Fatalf("close lifecycle evidence is incomplete: %q", text)
	}
	if result.StructuredContent["target_window_absent"] != true ||
		result.StructuredContent["final_observation_returned"] != true ||
		result.StructuredContent["final_observation_kind"] != "lifecycle" {
		t.Fatalf("close lifecycle fields are incomplete: %#v", result.StructuredContent)
	}
}

func TestExistingCloseLifecycleGetsVerifiedOutcomeFields(t *testing.T) {
	service := newService()
	result := service.recoverClosedWindowActionObservation(
		&axBatchContext{windowRef: "w7"},
		axBatchAction{actionType: "close"},
		toolCallResult{
			IsError: true,
			Content: []contentItem{
				{Type: "text", Text: "final_observation_unavailable: stale app"},
				{Type: "text", Text: "ax_diff:\n effect: target_window_absent"},
			},
			StructuredContent: map[string]any{
				"target_window_absent":                true,
				"final_observation_returned":          true,
				"observation_required":                false,
				"next_identical_observation_required": false,
				"input_events_emitted":                true,
				"effect_unknown":                      true,
				"mutation_unknown":                    true,
				"observation_failure_code":            "stale_app_ref",
				"post_action_observation_status":      "unknown",
				"continuation":                        map[string]any{"state": "observe_required"},
			},
		},
		observation{mode: observeDiff},
	)
	if result.IsError ||
		result.StructuredContent["close_effect_verified"] != true ||
		result.StructuredContent["observation_required"] != false {
		t.Fatalf("verified close lifecycle was not finalized: %#v",
			result.StructuredContent)
	}
	if _, present := result.StructuredContent["task_effect_verified"]; present {
		t.Fatalf("close proof claimed whole-task success: %#v",
			result.StructuredContent)
	}
	for _, key := range []string{
		"effect_unknown",
		"mutation_unknown",
		"observation_failure_code",
		"post_action_observation_status",
		"continuation",
	} {
		if _, present := result.StructuredContent[key]; present {
			t.Fatalf("verified close retained superseded %s: %#v",
				key, result.StructuredContent)
		}
	}
	if text := resultText(result); strings.Contains(
		text,
		"final_observation_unavailable",
	) || !strings.Contains(text, "target_window_absent") {
		t.Fatalf("verified close retained superseded observation text: %q", text)
	}
}

func TestVerifiedCloseDoesNotClaimTargetScreenshot(t *testing.T) {
	result := finalizeVerifiedCloseLifecycle(closedWindowFinalObservation(
		&axBatchContext{windowRef: "w7"},
		observation{mode: observeScreenshot},
	))
	if result.IsError {
		t.Fatalf("missing screenshot after verified close must not reverse success: %#v", result)
	}
	if result.StructuredContent["final_observation_returned"] != false ||
		result.StructuredContent["screenshot_returned"] != false ||
		result.StructuredContent["target_window_absent"] != true ||
		result.StructuredContent["observation_required"] != true ||
		result.StructuredContent["next_identical_observation_required"] != false {
		t.Fatalf("closed-window screenshot evidence is dishonest: %#v", result.StructuredContent)
	}
	if result.StructuredContent["close_effect_verified"] != true {
		t.Fatalf("verified close effect was hidden: %#v", result.StructuredContent)
	}
}

func TestCloseObserveNoneStillVerifiesLifecycleWithoutReturningObservation(
	t *testing.T,
) {
	service, appRef, sourceWindowRef, _ := sheetBatchService(t)
	backend := &recordingPlatformBackend{
		accessibilityResponses: []ProviderAccessibilityResult{{
			OK:    false,
			Error: "stale_window_ref: source window is gone",
			Failure: &ProviderFailure{
				Code:          "stale_window_ref",
				Phase:         "precondition",
				FactsComplete: true,
				Subject:       "window",
			},
			RecoveryContext: &ProviderRecoveryContext{
				SourceAppStatus:    "absent",
				SourceWindowStatus: "gone",
				InventoryComplete:  true,
			},
		}},
	}
	service.backend = backend
	context := &axBatchContext{
		appRef:       appRef,
		windowRef:    sourceWindowRef,
		appToken:     appRef,
		windowHandle: "w2:0123456789abcdef0123456789abcdef",
	}
	actionResult := toolCallResult{
		IsError: true,
		Content: []contentItem{{
			Type: "text",
			Text: "close input emitted; final observation unavailable",
		}},
		StructuredContent: map[string]any{
			"failure_code":                   "stale_window_ref",
			"failure_phase":                  "post_action_observation",
			"final_observation_returned":     false,
			"observation_required":           true,
			"input_events_emitted":           true,
			"effect_unknown":                 true,
			"mutation_unknown":               true,
			"cause_unattributed":             true,
			"partial_input_possible":         false,
			"post_action_observation_status": "unknown",
		},
	}

	result := service.recoverClosedWindowActionObservation(
		context,
		axBatchAction{actionType: "close"},
		actionResult,
		observation{mode: observeNone},
	)
	if result.IsError ||
		result.StructuredContent["target_window_absent"] != true ||
		result.StructuredContent["close_effect_verified"] != true ||
		result.StructuredContent["final_observation_kind"] != "none" ||
		result.StructuredContent["final_observation_returned"] != false ||
		result.StructuredContent["observation_required"] != false ||
		result.StructuredContent["next_identical_observation_required"] != false {
		t.Fatalf("observe=none close lifecycle evidence is incomplete: %#v", result)
	}
	for _, key := range []string{
		"effect_unknown",
		"mutation_unknown",
		"cause_unattributed",
		"continuation",
		"observation_failure_code",
		"post_action_observation_status",
	} {
		if _, present := result.StructuredContent[key]; present {
			t.Fatalf("verified close retained superseded %s: %#v",
				key, result.StructuredContent)
		}
	}
	text := resultText(result)
	if strings.Contains(text, "ax_diff:") ||
		!strings.Contains(text, `observe="none" returned no final tree or screenshot`) {
		t.Fatalf("observe=none close output is misleading: %q", text)
	}
	if len(backend.observeRequests) != 1 {
		t.Fatalf("close lifecycle probes=%d, want 1", len(backend.observeRequests))
	}
}

func TestVerifiedClosePreservesIndependentActionUncertainty(t *testing.T) {
	result := finalizeVerifiedCloseLifecycle(toolCallResult{
		IsError: true,
		Content: []contentItem{{
			Type: "text",
			Text: "input emitted\nfinal_observation_unavailable: stale app\n" +
				"partial input remains possible",
		}},
		StructuredContent: map[string]any{
			"target_window_absent":       true,
			"final_observation_returned": true,
			"failure_code":               "effect_unknown",
			"failure_phase":              "action",
			"input_events_emitted":       true,
			"partial_input_possible":     true,
			"mutation_unknown":           true,
			"effect_unknown":             true,
			"observation_required":       false,
			"task_effect_verified":       true,
		},
	})
	if !result.IsError ||
		result.StructuredContent["close_effect_verified"] != true ||
		result.StructuredContent["failure_code"] != "effect_unknown" ||
		result.StructuredContent["partial_input_possible"] != true ||
		result.StructuredContent["mutation_unknown"] != true ||
		result.StructuredContent["effect_unknown"] != true {
		t.Fatalf("close proof erased independent action uncertainty: %#v", result)
	}
	if _, present := result.StructuredContent["task_effect_verified"]; present {
		t.Fatalf("close proof claimed whole-task success: %#v",
			result.StructuredContent)
	}
	text := resultText(result)
	if strings.Contains(text, "final_observation_unavailable:") ||
		!strings.Contains(text, "input emitted") ||
		!strings.Contains(text, "partial input remains possible") {
		t.Fatalf("close cleanup removed unrelated evidence: %q", text)
	}
}
