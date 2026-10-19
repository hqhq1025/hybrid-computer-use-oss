package main

import (
	"strings"
	"testing"
)

func TestMissingPostActionSnapshotPreservesUnknownMutationEvidence(t *testing.T) {
	backend := &recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{
			OK: true,
			Mutation: &ProviderMutationFacts{
				MutationUnknown: true,
			},
		},
	}
	service := newServiceWithBackend(backend)
	_, _, result := service.readSnapshot("Editor", axServiceRequest{
		Action:    axClickCommand{},
		App:       "Editor",
		SourceRef: "r17",
	})
	if !result.IsError ||
		result.StructuredContent["failure_code"] != "effect_unknown" ||
		result.StructuredContent["mutation_unknown"] != true {
		t.Fatalf("unknown post-action mutation evidence was lost: %#v", result)
	}
	if strings.Contains(resultText(result), "input was emitted") {
		t.Fatalf("unknown-only mutation text falsely claimed emission: %s",
			resultText(result))
	}
}

func TestNoInputRecoverablePreconditionDetectionIsNarrow(t *testing.T) {
	stale := textResult(
		"stale_element_ref reason=path_changed_or_element_disappeared "+
			"element_index=6 role='push button' name='OK'; no input was emitted.",
		true,
	)
	stale.StructuredContent = map[string]any{
		"failure_code":           "stale_element_ref",
		"failure_phase":          "precondition",
		"input_events_emitted":   false,
		"partial_input_possible": false,
		"mutation_unknown":       false,
		"failure_facts_complete": true,
	}
	if !isNoInputRecoverablePrecondition(stale) {
		t.Fatal("the explicit precondition no-input stale-ref failure must require observation")
	}
	incomplete := stale
	incomplete.StructuredContent = map[string]any{}
	for key, value := range stale.StructuredContent {
		incomplete.StructuredContent[key] = value
	}
	incomplete.StructuredContent["failure_code"] = "observation_incomplete"
	incomplete.StructuredContent["failure_facts_complete"] = false
	if !isNoInputRecoverablePrecondition(incomplete) {
		t.Fatal("explicit no-mutation facts should allow one recovery observation")
	}
	for _, result := range []toolCallResult{
		textResult("stale_element_ref reason=path_changed_or_element_disappeared; no input was emitted", true),
		{IsError: true, StructuredContent: map[string]any{
			"failure_code":         "stale_element_ref",
			"input_events_emitted": false,
		}},
		{IsError: true, StructuredContent: map[string]any{
			"failure_code":           "stale_element_ref",
			"failure_phase":          "precondition",
			"input_events_emitted":   true,
			"partial_input_possible": false,
			"mutation_unknown":       false,
		}},
		{IsError: true, StructuredContent: map[string]any{
			"failure_code":           "stale_element_ref",
			"failure_phase":          "precondition",
			"input_events_emitted":   false,
			"partial_input_possible": true,
		}},
		{IsError: true, StructuredContent: map[string]any{
			"failure_code":         "stale_element_ref",
			"failure_phase":        "precondition",
			"input_events_emitted": false,
			"mutation_unknown":     true,
		}},
		{IsError: true, StructuredContent: map[string]any{
			"failure_code":           "stale_element_ref",
			"failure_phase":          "execution",
			"input_events_emitted":   false,
			"partial_input_possible": false,
			"mutation_unknown":       false,
		}},
		{IsError: false, StructuredContent: map[string]any{
			"failure_code":           "stale_element_ref",
			"failure_phase":          "precondition",
			"input_events_emitted":   false,
			"partial_input_possible": false,
			"mutation_unknown":       false,
		}},
		{IsError: true, StructuredContent: map[string]any{
			"failure_code":         "observation_incomplete",
			"failure_phase":        "precondition",
			"input_events_emitted": false,
			"mutation_unknown":     false,
		}},
		{IsError: true, StructuredContent: map[string]any{
			"failure_code":           "semantic_target_not_found",
			"failure_phase":          "precondition",
			"input_events_emitted":   false,
			"partial_input_possible": false,
			"mutation_unknown":       false,
			"failure_facts_complete": true,
		}},
	} {
		if isNoInputRecoverablePrecondition(result) {
			t.Fatalf("observation-only recovery must require a precondition failure with definitive no-input evidence: %#v", result)
		}
	}
}

func TestNoInputStaleFailureReturnsIndependentCurrentObservation(t *testing.T) {
	current := windowTransitionSnapshot(
		"w2:"+strings.Repeat("b", 32),
		"Current Document",
		true,
	)
	backend := &recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{
			OK:       true,
			Snapshot: current,
		},
	}
	service := newServiceWithBackend(backend)
	before := windowTransitionSnapshot(
		"w2:"+strings.Repeat("a", 32),
		"Old Dialog",
		true,
	)
	service.rememberSnapshot("editor", before)
	service.rememberDeliveredSnapshot(before)
	failed := textResult(
		"stale_element_ref: target disappeared; no input was emitted",
		true,
	)
	failed.StructuredContent = map[string]any{
		"failure_code":           "stale_element_ref",
		"failure_phase":          "precondition",
		"input_events_emitted":   false,
		"partial_input_possible": false,
		"mutation_unknown":       false,
		"failure_facts_complete": true,
	}

	result := service.recoverNoInputPreconditionObservation(
		"editor",
		before,
		axServiceRequest{Observe: observeDiff},
		failed,
	)
	if !result.IsError ||
		result.StructuredContent["failure_code"] != "stale_element_ref" ||
		result.StructuredContent["action_outcome"] != "not_executed" ||
		result.StructuredContent["failed_action_input_emitted"] != false ||
		result.StructuredContent["failed_action_caused_ui_change"] != false ||
		result.StructuredContent["current_ui_returned"] != true ||
		result.StructuredContent["current_state_cause"] != "independent" ||
		result.StructuredContent["recovery_read_attempted"] != true ||
		result.StructuredContent["final_observation_returned"] != true ||
		result.StructuredContent["observation_required"] != false {
		t.Fatalf("no-input recovery overstated or omitted evidence: %#v", result)
	}
	if len(backend.observeRequests) != 1 ||
		backend.observeRequests[0].WindowHandle != "" {
		t.Fatalf("recovery must make one app-root read: %#v", backend.observeRequests)
	}
	text := resultText(result)
	for _, fragment := range []string{
		"The requested action was not executed",
		"independent current-state observation",
		"Current Document",
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("recovered response is missing %q: %s", fragment, text)
		}
	}
	if service.hasPendingFailedObservation("editor") {
		t.Fatal("successful inline recovery must not leave an explicit-observe gate")
	}
}

func TestNoInputRecoveryRespectsObserveNone(t *testing.T) {
	backend := &recordingPlatformBackend{}
	service := newServiceWithBackend(backend)
	before := windowTransitionSnapshot(
		"w2:"+strings.Repeat("a", 32),
		"Old Dialog",
		true,
	)
	failed := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":           "stale_element_ref",
			"failure_phase":          "precondition",
			"input_events_emitted":   false,
			"partial_input_possible": false,
			"mutation_unknown":       false,
			"failure_facts_complete": true,
		},
	}
	result := service.recoverNoInputPreconditionObservation(
		"editor",
		before,
		axServiceRequest{Observe: observeNone},
		failed,
	)
	if result.StructuredContent["observation_required"] != true ||
		result.StructuredContent["recovery_read_attempted"] != false ||
		len(backend.observeRequests) != 0 {
		t.Fatalf("observe=none unexpectedly performed recovery: %#v", result)
	}
}

func TestNoInputRecoveryFailureKeepsExplicitObservationGate(t *testing.T) {
	backend := &recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{
			OK:    false,
			Error: "AX observation remained incomplete",
			Failure: &ProviderFailure{
				Code:          "observation_incomplete",
				Phase:         "precondition",
				FactsComplete: false,
			},
		},
	}
	service := newServiceWithBackend(backend)
	failed := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":           "stale_element_ref",
			"failure_phase":          "precondition",
			"input_events_emitted":   false,
			"partial_input_possible": false,
			"mutation_unknown":       false,
			"failure_facts_complete": true,
		},
	}
	result := service.recoverNoInputPreconditionObservation(
		"editor",
		nil,
		axServiceRequest{Observe: observeDiff},
		failed,
	)
	if result.StructuredContent["failure_code"] != "stale_element_ref" ||
		result.StructuredContent["observation_required"] != true ||
		result.StructuredContent["current_ui_returned"] != false ||
		result.StructuredContent["recovery_read_attempted"] != true ||
		result.StructuredContent["recovery_observation_returned"] != false ||
		result.StructuredContent["recovery_observation_failure_code"] !=
			"observation_incomplete" {
		t.Fatalf("failed recovery lost original failure or gate: %#v", result)
	}
}

func TestStaleFailureRequiresExplicitObservationWithoutFreshState(t *testing.T) {
	service := newService()
	before := &appSnapshot{
		App:             appDescriptor{Name: "Editor", PID: 7},
		WindowTitle:     "Question",
		WindowHandle:    "pid7/win0/frame",
		publicWindowRef: "w1",
		SnapshotID:      "visible-a",
		sourceApp:       "editor",
		projection:      snapshotProjection{View: treeViewWorking},
	}
	service.rememberSnapshot("editor", before)
	service.rememberDeliveredSnapshot(before)
	failed := textResult(
		"stale_element_ref reason=path_changed_or_element_disappeared; no input was emitted.",
		true,
	)
	failed.StructuredContent = map[string]any{
		"failure_code":           "stale_element_ref",
		"failure_phase":          "precondition",
		"input_events_emitted":   false,
		"partial_input_possible": false,
		"mutation_unknown":       false,
		"failure_facts_complete": true,
	}
	result := service.staleObservationRequired("editor", before, failed).
		withEvidenceScope("delivery_or_local_ui")
	text := resultText(result)
	for _, fragment := range []string{
		"STALE ELEMENT REFERENCE RECOVERY REQUIRED",
		"action_outcome=not_executed",
		"input_events_emitted=false",
		"observation_required=true",
		"This action did not run and did not cause any UI change",
		"No fresh UI observation was performed after this failure",
		"Call AX mode=observe or mode=find",
		"Do not describe a later disappearance or completed state as possible success",
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("stale failure is missing %q:\n%s", fragment, text)
		}
	}
	for _, forbidden := range []string{
		"Fresh window:",
		"fresh_state_inlined",
		"identity-compatible candidate",
		"fresh_snapshot_id",
		"fresh_window_handle",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("stale failure leaked fresh UI state %q:\n%s", forbidden, text)
		}
	}
	if len(service.snapshots) != 0 {
		t.Fatalf("stale failure must invalidate the action cache: %#v", service.snapshots)
	}
	if len(service.observations.lastComplete) == 0 {
		t.Fatal("stale failure must preserve the last complete continuity baseline")
	}
	for key, want := range map[string]any{
		"action_outcome":                 "not_executed",
		"failed_action_input_emitted":    false,
		"failed_action_caused_ui_change": false,
		"observation_required":           true,
		"current_ui_returned":            false,
		"recovery_read_attempted":        false,
		"evidence_scope":                 "error_only",
	} {
		if got := result.StructuredContent[key]; got != want {
			t.Fatalf("%s=%#v, want %#v; all=%#v", key, got, want,
				result.StructuredContent)
		}
	}
}

func TestExplicitObservationSeparatesLaterStateFromFailedAction(t *testing.T) {
	service := newService()
	before := &appSnapshot{
		App:       appDescriptor{Name: "Editor", PID: 7},
		sourceApp: "editor",
	}
	service.rememberFailedObservation("editor", before)
	current := &appSnapshot{App: appDescriptor{Name: "Editor", PID: 7}}
	note := service.consumeFailedObservation("Editor", current)
	for _, fragment := range []string{
		"explicit observation follows a stale-reference action",
		"emitted no input",
		"did not cause any UI change",
		"independent later observation",
		"Do not describe any difference",
		"call find and use the returned rN ref",
	} {
		if !strings.Contains(note, fragment) {
			t.Fatalf("explicit recovery observation is missing %q: %q", fragment, note)
		}
	}
	if repeated := service.consumeFailedObservation("editor", current); repeated != "" {
		t.Fatalf("failed-action continuity note must be consumed once, got %q", repeated)
	}
	result := withFailedActionContinuity(textResult("fresh state", false), true)
	for key, want := range map[string]any{
		"follows_no_input_stale_failure":     true,
		"failed_action_caused_current_state": false,
		"current_state_cause":                "independent",
	} {
		if got := result.StructuredContent[key]; got != want {
			t.Fatalf("%s=%#v, want %#v; all=%#v", key, got, want,
				result.StructuredContent)
		}
	}
}

func TestFailedObservationLedgerConsumesOneIdentityAsAUnit(t *testing.T) {
	service := newService()
	writer := &appSnapshot{
		App:          appDescriptor{Name: "soffice", PID: 7},
		WindowHandle: "pid7/writer/frame",
		sourceApp:    "writer",
	}
	calc := &appSnapshot{
		App:          appDescriptor{Name: "soffice", PID: 7},
		WindowHandle: "pid7/calc/frame",
		sourceApp:    "calc",
	}
	service.rememberFailedObservation("writer", writer)
	service.rememberFailedObservation("calc", calc)

	if note := service.consumeFailedObservation("calc", calc); note == "" {
		t.Fatal("the matching Calc observation must consume its own ledger entry")
	}
	if len(service.pendingFailedObservations) != 1 {
		t.Fatalf("consumption must remove one complete entry, got %#v",
			service.pendingFailedObservations)
	}
	if service.pendingFailedObservations[0].query != "writer" {
		t.Fatalf("the Calc observation consumed the Writer entry: %#v",
			service.pendingFailedObservations)
	}
	if note := service.consumeFailedObservation("writer", writer); note == "" {
		t.Fatal("the independent Writer entry must remain pending")
	}
	if len(service.pendingFailedObservations) != 0 {
		t.Fatalf("all matched entries should now be consumed: %#v",
			service.pendingFailedObservations)
	}
}

func TestFailedObservationRequiresNamedQueryAcrossRootChange(t *testing.T) {
	service := newService()
	before := &appSnapshot{
		App:          appDescriptor{Name: "Google Chrome", PID: 7},
		WindowHandle: "pid7/win0/frame",
		sourceApp:    "Google Chrome",
	}
	service.rememberFailedObservation("Google Chrome", before)
	if !failedObservationMatches(
		service.pendingFailedObservations[0],
		"google",
		nil,
	) {
		t.Fatal("pending stale identity must honor the same loose app alias contract")
	}

	after := &appSnapshot{
		App:          appDescriptor{Name: "Google Chrome", PID: 7},
		WindowHandle: "pid7/win1/dialog",
	}
	if note := service.consumeFailedObservation("", after); note != "" {
		t.Fatal("an unnamed foreground observation must not guess across root windows")
	}
	if note := service.consumeFailedObservation("google", after); note == "" {
		t.Fatal("the original loose app query must consume the entry across a root change")
	}
}

func TestContentTransitionFallbackReturnsWorkingTree(t *testing.T) {
	service := newService()
	previous := editorFormSnapshot()
	previous.sourceApp = "editor"
	previous.publicWindowRef = "w1"
	previous.SnapshotID = "previous"
	previous.contentRevision = "c1:0123456789abcdef01234567"
	previous.projection = snapshotProjection{
		View: treeViewWorking, Scope: axScopeWindow,
	}
	previous.axSnapshot = nil
	service.rememberDeliveredSnapshot(previous)

	current := editorFormSnapshot()
	current.sourceApp = "editor"
	current.publicWindowRef = "w1"
	current.SnapshotID = "current"
	current.contentRevision = "c1:89abcdef0123456701234567"
	current.projection = snapshotProjection{
		View: treeViewWorking, Scope: axScopeWindow,
	}
	current.axSnapshot = nil
	current.TreeLines = []string{
		`ax:`,
		` - document "Settings":`,
		`  - checkbox "New panel control" [ref=r9]`,
	}

	result, lines, returned, compact, fullTree :=
		service.renderAXDiffOrCompactObservation(current, nil)
	if !returned || compact || !fullTree || len(lines) != len(current.TreeLines) {
		t.Fatalf("content transition fallback missing: returned=%t compact=%t full=%t lines=%#v",
			returned, compact, fullTree, lines)
	}
	if result.StructuredContent["content_transition_working_tree"] != true ||
		result.StructuredContent["final_observation_kind"] != "working_tree" ||
		!strings.Contains(resultText(result), `checkbox "New panel control"`) {
		t.Fatalf("content transition evidence is incomplete: %#v\n%s",
			result.StructuredContent, resultText(result))
	}
}

func TestFinalObservationReturnsLifecycleWhenClosedAppHasNoReplacementWindow(
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
	result := service.axBatchFinalObservation(
		context,
		observation{mode: observeDiff},
		true,
	)
	if result.IsError ||
		result.StructuredContent["target_window_absent"] != true ||
		result.StructuredContent["final_observation_returned"] != true ||
		result.StructuredContent["observation_required"] == true ||
		result.StructuredContent["next_identical_observation_required"] != false {
		t.Fatalf("closed last window did not return lifecycle observation: %#v",
			result)
	}
	if !strings.Contains(resultText(result), "effect: target_window_absent") {
		t.Fatalf("closed-window lifecycle text missing: %q", resultText(result))
	}
}

func TestSharedDiffFallbackReturnsFreshBoundedWorkingTree(t *testing.T) {
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

	result, lines, returned, compact, fullTree :=
		service.renderAXDiffOrCompactObservation(snapshot, nil)
	if !returned || compact || !fullTree || len(lines) <= 1 {
		t.Fatalf("fresh working tree fallback missing: returned=%t compact=%t full=%t lines=%#v",
			returned, compact, fullTree, lines)
	}
	if result.StructuredContent["working_tree_fallback"] != true ||
		result.StructuredContent["final_observation_kind"] != "working_tree" ||
		!strings.Contains(resultText(result), `button "Save"`) {
		t.Fatalf("fresh working tree fallback evidence is incomplete: %#v\n%s",
			result.StructuredContent, resultText(result))
	}
}

func TestCloseObserveNoneDoesNotLeakReplacementWindowObservation(t *testing.T) {
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
						Title:              "Save Changes",
						Role:               "dialog",
						ProviderBusName:    ":1.84",
						ProviderObjectPath: "/window/save",
						ProcessGeneration:  "pid:10:start:100",
					},
				},
			},
			{
				OK: true,
				Snapshot: &ProviderSnapshot{
					App:          appDescriptor{Name: "Editor"},
					WindowTitle:  "Save Changes",
					WindowHandle: "w2:11111111111111111111111111111111",
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
	actionResult := toolCallResult{
		StructuredContent: map[string]any{
			"final_observation_returned": false,
			"input_events_emitted":       true,
		},
	}

	result := service.recoverClosedWindowActionObservation(
		context,
		axBatchAction{actionType: "close"},
		actionResult,
		observation{mode: observeNone},
	)
	if result.StructuredContent["close_effect_verified"] != false ||
		result.StructuredContent["final_observation_kind"] != "none" ||
		result.StructuredContent["final_observation_returned"] != false ||
		result.StructuredContent["observation_required"] != true ||
		result.StructuredContent["next_identical_observation_required"] != false {
		t.Fatalf("unverified observe=none close evidence is incomplete: %#v", result)
	}
	if strings.Contains(resultText(result), "Save Changes") {
		t.Fatalf("observe=none leaked replacement-window observation: %q",
			resultText(result))
	}
	continuation := result.StructuredContent["continuation"].(map[string]any)
	if continuation["route"] != "observe" ||
		continuation["retry_same_request"] != false {
		t.Fatalf("unverified close continuation=%#v", continuation)
	}
	if len(backend.observeRequests) != 2 {
		t.Fatalf("close lifecycle probes=%d, want 2", len(backend.observeRequests))
	}
}
