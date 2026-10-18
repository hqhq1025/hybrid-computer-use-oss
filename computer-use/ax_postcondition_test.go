package main

import (
	"strings"
	"testing"
)

func TestPostconditionPassPreservesUnattributedMutationUncertainty(t *testing.T) {
	action := toolCallResult{
		StructuredContent: map[string]any{
			"input_events_emitted": true,
			"effect_unknown":       true,
			"mutation_unknown":     true,
		},
	}
	assertion := toolCallResult{
		StructuredContent: map[string]any{
			"assertion_status": "pass",
		},
	}
	merged := mergeA11yPostconditionResult(action, assertion)
	if merged.StructuredContent["postcondition_verified"] != true {
		t.Fatalf("passing postcondition was not recorded: %#v",
			merged.StructuredContent)
	}
	if merged.StructuredContent["effect_unknown"] != true ||
		merged.StructuredContent["mutation_unknown"] != true ||
		merged.StructuredContent["cause_unattributed"] != true {
		t.Fatalf("passing postcondition overclaimed action causality: %#v",
			merged.StructuredContent)
	}
}

func TestPassingPostconditionPreservesNestedActionUncertainty(t *testing.T) {
	structured := map[string]any{
		"input_events_emitted": true,
	}
	action := map[string]any{}
	preserveUnattributedPostconditionEvidence(structured, action)
	if action["mutation_unknown"] != true ||
		action["cause_unattributed"] != true ||
		structured["effect_unknown"] != true ||
		structured["mutation_unknown"] != true ||
		structured["cause_unattributed"] != true {
		t.Fatalf("postcondition erased nested uncertainty: structured=%#v action=%#v",
			structured, action)
	}
}

func TestPassingPostconditionKeepsIndependentTaskEffectProof(t *testing.T) {
	structured := map[string]any{
		"input_events_emitted": true,
		"task_effect_verified": true,
	}
	action := map[string]any{}
	preserveUnattributedPostconditionEvidence(structured, action)
	if _, present := structured["effect_unknown"]; present {
		t.Fatalf("independently verified effect became uncertain: %#v", structured)
	}
	if len(action) != 0 {
		t.Fatalf("independent effect proof altered action evidence: %#v", action)
	}
}

func TestFinalObservationCompletesZeroStablePostcondition(t *testing.T) {
	service := newService()
	showing := true
	record := &elementRecord{
		Index:              7,
		RuntimeID:          []int{0, 4, 7},
		ControlType:        "check box",
		Name:               "Automatic quotes",
		States:             "[checked]",
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/org/a11y/quote/rebuilt",
		ProcessGeneration:  "99123",
		Showing:            &showing,
		Visible:            &showing,
	}
	node := &AXNode{
		ID:                 "0/4/7",
		Role:               "checkbox",
		SemanticClass:      "control",
		Name:               record.Name,
		RuntimeID:          append([]int(nil), record.RuntimeID...),
		ProviderBusName:    record.ProviderBusName,
		ProviderObjectPath: record.ProviderObjectPath,
		ProcessGeneration:  record.ProcessGeneration,
		SemanticTarget:     `checkbox "Automatic quotes"`,
		LocatorKind:        AXLocatorSemantic,
		SourceRecord:       record,
		Showing:            &showing,
		Visible:            &showing,
	}
	workingNode := *node
	tree := &AXSnapshotV1{
		AppRef:               "a1",
		AppIdentity:          "editor",
		WindowHandle:         "private-window",
		WindowRef:            "w1",
		Revision:             "c2",
		RefInventoryComplete: true,
		CanonicalRoots:       []*AXNode{node},
		Roots:                []*AXNode{&workingNode},
		ByID:                 map[string]*AXNode{"0/4/7": node},
	}
	service.refRegistry.AssignSnapshotRefs(tree)
	if node.Ref == "" || node.LocatorKind != AXLocatorSemantic {
		t.Fatalf("canonical ref metadata was not synchronized: %#v", node)
	}
	snapshot := &appSnapshot{
		App:             appDescriptor{Name: "editor"},
		WindowHandle:    "private-window",
		Elements:        []elementRecord{*record},
		publicAppRef:    "a1",
		publicWindowRef: "w1",
		axSnapshot:      tree,
	}
	assertion := map[string]any{
		"type":      "state",
		"target":    map[string]any{"ref": node.Ref},
		"state":     "checked",
		"value":     true,
		"stable_ms": float64(0),
	}
	incomplete := a11yAssertionResult(
		"assert_ax observation_incomplete: live AX target incomplete",
		true,
		"observation_incomplete",
		1,
		0,
	)
	spec, err := parseA11yAssertionSpec(assertion)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := service.refRegistry.Resolve(node.Ref, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.exactRefRecordFromSnapshot(
		snapshot,
		spec.targetAddress,
		entry,
	); err != nil {
		t.Fatalf("test fixture cannot prove exact ref: %v entry=%#v", err, entry)
	}
	finalObservation := toolCallResult{
		StructuredContent: map[string]any{
			"final_observation_returned": true,
		},
		axProofSnapshot: snapshot,
	}
	recovered := service.reconcileA11yPostconditionFromFinalObservation(
		assertion,
		incomplete,
		finalObservation,
	)
	if recovered.IsError ||
		recovered.StructuredContent["assertion_status"] != "pass" ||
		recovered.StructuredContent["postcondition_recovered_by_final_observation"] != true ||
		recovered.StructuredContent["postcondition_reconciled_by_canonical_observation"] != true ||
		recovered.StructuredContent["cause_unattributed"] != true {
		t.Fatalf("fresh final observation did not complete postcondition: %#v",
			recovered)
	}

	record.States = "[enabled]"
	reconciledFalse := service.reconcileA11yPostconditionFromFinalObservation(
		assertion,
		incomplete,
		finalObservation,
	)
	if reconciledFalse.IsError ||
		reconciledFalse.StructuredContent["assertion_status"] != "predicate_false" ||
		reconciledFalse.StructuredContent["postcondition_reconciled_by_canonical_observation"] != true {
		t.Fatalf("fresh final observation did not resolve false predicate: %#v",
			reconciledFalse)
	}

	expected, err := service.refRegistry.Resolve(node.Ref, "")
	if err != nil {
		t.Fatal(err)
	}
	absentTree := *tree
	absentTree.CanonicalRoots = nil
	absentTree.Roots = nil
	absentTree.ByID = map[string]*AXNode{}
	absentTree.RefInventoryComplete = true
	absentSnapshot := *snapshot
	absentSnapshot.Elements = nil
	absentSnapshot.axSnapshot = &absentTree
	for _, expectedExists := range []bool{false, true} {
		existsAssertion := map[string]any{
			"type":   "exists",
			"target": map[string]any{"ref": node.Ref},
			"value":  expectedExists,
		}
		got := service.reconcileA11yPostconditionFromCanonicalSnapshot(
			existsAssertion,
			incomplete,
			&absentSnapshot,
			nil,
			expected,
		)
		wantStatus := "pass"
		if expectedExists {
			wantStatus = "predicate_false"
		}
		if got.IsError ||
			got.StructuredContent["assertion_status"] != wantStatus {
			t.Fatalf("complete exact-ref absence with exists=%t: %#v",
				expectedExists, got)
		}
	}

	assertion["stable_ms"] = float64(100)
	unchanged := service.reconcileA11yPostconditionFromFinalObservation(
		assertion,
		incomplete,
		finalObservation,
	)
	if !unchanged.IsError {
		t.Fatalf("final observation bypassed stable_ms: %#v", unchanged)
	}

	expectedIdentity := *expected
	service.refRegistry.MarkStale(node.Ref, "test replacement")
	staleResult := service.reconcileA11yPostconditionFromCanonicalSnapshot(
		map[string]any{
			"type":   "exists",
			"target": map[string]any{"ref": node.Ref},
			"value":  false,
		},
		incomplete,
		&absentSnapshot,
		nil,
		&expectedIdentity,
	)
	if !staleResult.IsError ||
		staleResult.StructuredContent["postcondition_reconciled_by_canonical_observation"] == true {
		t.Fatalf("stale identity was incorrectly treated as proven absence: %#v",
			staleResult)
	}
}

func TestActionPostconditionReconciliationSamplesAfterPolling(t *testing.T) {
	service, appRef, windowRef, _ := sheetBatchService(t)
	handle := "w2:0123456789abcdef0123456789abcdef"
	snapshot := &appSnapshot{
		App: appDescriptor{
			Name:              "LibreOffice",
			PID:               10,
			ProviderBusName:   ":1.84",
			ProcessGeneration: "pid:10:start:100",
		},
		WindowTitle:  "Sheet",
		WindowHandle: handle,
		SnapshotID:   "c2",
		Elements: []elementRecord{
			{
				Index:                           0,
				RuntimeID:                       []int{0},
				ControlType:                     "frame",
				Name:                            "Sheet",
				States:                          "[active showing visible]",
				ProviderBusName:                 ":1.84",
				ProviderObjectPath:              "/window/1",
				ProcessGeneration:               "pid:10:start:100",
				ProviderWindowBusName:           ":1.84",
				ProviderWindowObjectPath:        "/window/1",
				ProviderWindowProcessGeneration: "pid:10:start:100",
				ProviderWindowRole:              "frame",
				ProviderWindowName:              "Sheet",
			},
			{
				Index:                           1,
				RuntimeID:                       []int{0, 0},
				ControlType:                     "button",
				Name:                            "Save",
				States:                          "[enabled showing visible]",
				Actions:                         []string{"click"},
				ProviderBusName:                 ":1.84",
				ProviderObjectPath:              "/window/1/save",
				ProcessGeneration:               "pid:10:start:100",
				ProviderWindowBusName:           ":1.84",
				ProviderWindowObjectPath:        "/window/1",
				ProviderWindowProcessGeneration: "pid:10:start:100",
				ProviderWindowRole:              "frame",
				ProviderWindowName:              "Sheet",
			},
		},
		scope: a11yScopeWindow,
	}
	backend := &recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{
			OK:       true,
			Snapshot: snapshot,
		},
	}
	service.backend = backend
	noScreenshot := false
	initial, _, initialResult := service.refreshSnapshot(
		appRef,
		axServiceRequest{
			Operation:          axServiceObserve,
			App:                appRef,
			IncludeScreenshot:  &noScreenshot,
			TargetWindowHandle: handle,
			SourceRef:          windowRef,
			Scope:              a11yScopeWindow,
			TreeView:           treeViewWorking,
		},
	)
	if initialResult.IsError || initial == nil ||
		initial.axSnapshot == nil ||
		len(initial.axSnapshot.CanonicalRoots) != 1 ||
		len(initial.axSnapshot.CanonicalRoots[0].Children) != 1 {
		t.Fatalf("initial canonical observation failed: %#v snapshot=%#v",
			initialResult, initial)
	}
	elementRef := initial.axSnapshot.CanonicalRoots[0].Children[0].Ref
	if elementRef == "" {
		t.Fatal("initial canonical observation did not issue an element ref")
	}
	snapshot.Elements[1].States = "[checked enabled showing visible]"
	backend.observeRequests = nil
	incomplete := a11yAssertionResult(
		"assert_ax observation_incomplete: live AX target incomplete",
		true,
		"observation_incomplete",
		1,
		0,
	)
	reconciled := service.reconcileA11yActionPostcondition(
		&a11yBatchContext{
			appRef:       appRef,
			appToken:     appRef,
			windowRef:    windowRef,
			windowHandle: handle,
		},
		map[string]any{
			"type":   "state",
			"target": map[string]any{"ref": elementRef},
			"state":  "checked",
			"value":  true,
		},
		incomplete,
	)
	if reconciled.IsError ||
		reconciled.StructuredContent["assertion_status"] != "pass" ||
		reconciled.StructuredContent["postcondition_reconciled_by_canonical_observation"] != true ||
		reconciled.StructuredContent["postcondition_recovered_by_final_observation"] == true {
		t.Fatalf("action guard did not reconcile from a fresh post-poll snapshot: %#v",
			reconciled)
	}
	if len(backend.observeRequests) != 1 {
		t.Fatalf("canonical reconciliation observe calls=%d, want 1",
			len(backend.observeRequests))
	}
}

func TestA11yAssertionSpecOwnsTimeoutAndRejectsIndexPostcondition(t *testing.T) {
	assertion := map[string]any{
		"type":       "state",
		"target":     map[string]any{"ref": "r17"},
		"state":      "checked",
		"value":      true,
		"timeout_ms": float64(2500),
	}
	spec, err := parseA11yAssertionSpec(assertion)
	if err != nil {
		t.Fatal(err)
	}
	if spec.timeoutMS != 2500 || spec.targetAddress != "r17" {
		t.Fatalf("parsed assertion drifted: %#v", spec)
	}
	service := newService()
	if err := service.preflightA11yPostcondition(
		&a11yBatchContext{windowHandle: "window-1", windowRef: "w1"},
		map[string]any{
			"type":   "exists",
			"target": map[string]any{"index": float64(7)},
			"value":  true,
		},
	); err == nil || !strings.Contains(err.Error(), "not stable across mutations") {
		t.Fatalf("numeric postcondition target was not rejected: %v", err)
	}
}

func TestValueAssertionAcceptsAndNormalizesJSONScalars(t *testing.T) {
	for _, tc := range []struct {
		name     string
		expected any
		observed string
	}{
		{name: "number", expected: float64(100), observed: "100.0"},
		{name: "boolean", expected: true, observed: "TRUE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertion := map[string]any{
				"type":   "value",
				"target": map[string]any{"ref": "r17"},
				"value":  tc.expected,
			}
			spec, err := parseA11yAssertionSpec(assertion)
			if err != nil {
				t.Fatal(err)
			}
			if spec.assertionType != "value_equals" {
				t.Fatalf("assertion type = %q", spec.assertionType)
			}
			goal := assertionGoal(assertion)
			if ok, detail := goal.check(
				&elementRecord{Value: tc.observed},
				nil,
			); !ok {
				t.Fatalf("scalar value did not match: %s", detail)
			}
		})
	}
}

func TestValueContainsRejectsNonStringScalar(t *testing.T) {
	_, err := parseA11yAssertionSpec(map[string]any{
		"type":   "value",
		"target": map[string]any{"ref": "r17"},
		"value":  float64(100),
		"match":  "contains",
	})
	if err == nil || !strings.Contains(err.Error(), "requires a string value") {
		t.Fatalf("numeric contains assertion was not rejected clearly: %v", err)
	}
}

func TestPostconditionFailureClassificationPreservesUnderlyingCause(t *testing.T) {
	predicateFalse := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"assertion_status":          "predicate_false",
			"assertion_sampled_live_ui": true,
		},
	}
	classification, underlying := classifyA11yPostconditionFailure(
		predicateFalse,
	)
	if classification != "postcondition_not_satisfied" ||
		underlying != "predicate_false" {
		t.Fatalf(
			"predicate failure classification = %q, %q",
			classification,
			underlying,
		)
	}

	incomplete := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":              "observation_incomplete",
			"assertion_sampled_live_ui": false,
		},
	}
	classification, underlying = classifyA11yPostconditionFailure(incomplete)
	if classification != "postcondition_unknown" ||
		underlying != "observation_incomplete" {
		t.Fatalf(
			"infrastructure failure classification = %q, %q",
			classification,
			underlying,
		)
	}
}

func TestPostconditionFailureDoesNotInventInputEmission(t *testing.T) {
	action := toolCallResult{
		StructuredContent: map[string]any{
			"input_events_emitted": false,
			"preflight_only":       true,
		},
	}
	assertion := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":              "observation_incomplete",
			"assertion_sampled_live_ui": false,
		},
	}
	merged := mergeA11yPostconditionResult(action, assertion)
	if merged.StructuredContent["input_events_emitted"] != false ||
		merged.StructuredContent["preflight_only"] != true {
		t.Fatalf("postcondition invented input emission: %#v",
			merged.StructuredContent)
	}
	if _, ok := merged.StructuredContent["mutation_unknown"]; ok {
		t.Fatalf("preflight-only failure invented mutation uncertainty: %#v",
			merged.StructuredContent)
	}
}

func TestClosedWindowNegativeTitlePostconditionUsesOwningApp(t *testing.T) {
	backend := &recordingPlatformBackend{
		windowResponse: &ProviderWindowResult{
			OK:           true,
			WindowTitles: []string{"Document - Editor"},
		},
	}
	service, appRef, windowRef, _ := sheetBatchService(t)
	service.backend = backend
	result := service.runA11yPostcondition(
		&a11yBatchContext{
			appRef:       appRef,
			windowRef:    windowRef,
			appToken:     appRef,
			windowHandle: "private-window",
		},
		map[string]any{
			"type":       "window_title_contains",
			"text":       "Confirm Removal",
			"exists":     false,
			"timeout_ms": float64(1),
		},
	)
	if result.IsError ||
		result.StructuredContent["assertion_status"] != "pass" {
		t.Fatalf("negative close postcondition did not use app inventory: %#v", result)
	}
	if len(backend.calls) != 1 || backend.calls[0] != "window_list" {
		t.Fatalf("unexpected provider calls: %#v", backend.calls)
	}
}

func TestRefOwnedBatchScopesSelectorPostconditionToSameWindow(t *testing.T) {
	service, _, windowRef, elementRef := sheetBatchService(t)
	context, err := service.deriveA11yBatchContext(
		"", []a11yBatchAction{{
			actionType: "click",
			targetRef:  elementRef,
		}}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if context.windowRef != windowRef {
		t.Fatalf("element ref did not preserve its owning window: %#v", context)
	}
	if err := service.preflightA11yPostcondition(
		context,
		map[string]any{
			"type": "exists",
			"target": map[string]any{
				"selector": map[string]any{
					"role": "text",
					"name": "Saved",
				},
			},
			"value": true,
		},
	); err != nil {
		t.Fatalf("selector postcondition rejected its inherited window: %v", err)
	}
}

func TestActionPostconditionFailureStopsLaterActionsWithCurrentEvidence(t *testing.T) {
	service := newService()
	assertionSnapshot := &appSnapshot{
		App:                 appDescriptor{Name: "Editor"},
		WindowHandle:        "private-window",
		SnapshotID:          "c2",
		ScreenshotPNGBase64: testPNGBase64(t, 2, 2),
		ScreenshotMimeType:  "image/png",
		publicAppRef:        "a1",
		publicWindowRef:     "w1",
	}
	postcondition := toolCallResult{
		Content: []contentItem{{
			Type: "text",
			Text: "assertion_passed=false failure_code=predicate_false",
		}},
		IsError: true,
		StructuredContent: map[string]any{
			"assertion_status":          "fail",
			"failure_code":              "predicate_false",
			"assertion_sampled_live_ui": true,
		},
		axProofSnapshot: assertionSnapshot,
	}
	actionResult := toolCallResult{
		Content: []contentItem{{Type: "text", Text: "action delivered"}},
		StructuredContent: map[string]any{
			"input_events_emitted": true,
			"actionability": map[string]any{
				"input_emitted": "true",
			},
		},
	}
	action := a11yBatchAction{
		actionType: "click",
		postcondition: map[string]any{
			"type": "state",
			"target": map[string]any{
				"ref": "r1",
			},
			"state": "selected",
			"value": true,
		},
	}
	actionEvidence := []map[string]any{
		batchActionEvidenceEntry(0, "click", actionResult),
	}
	actionEvidence[0]["postcondition"] =
		compactA11yPostconditionEvidence(action.postcondition, postcondition)

	result := service.stopA11yBatchForActionPostcondition(
		&a11yBatchContext{windowRef: "w1"},
		0,
		2,
		action,
		actionEvidence,
		actionResult,
		postcondition,
		observation{mode: observeScreenshot},
		false,
	)
	if !result.IsError {
		t.Fatalf("failed action guard must fail the incomplete batch: %#v", result)
	}
	if text := resultText(result); strings.Contains(text, `observe="none"`) ||
		strings.Contains(text, "observe: none") {
		t.Fatalf("current guard observation retained superseded placeholder: %q", text)
	}
	for key, expected := range map[string]any{
		"failure_code":                        "action_postcondition_not_satisfied",
		"failure_phase":                       "between_actions",
		"observation_required":                false,
		"next_identical_observation_required": false,
		"final_observation_returned":          true,
	} {
		if result.StructuredContent[key] != expected {
			t.Fatalf("%s=%#v want %#v in %#v",
				key, result.StructuredContent[key], expected,
				result.StructuredContent)
		}
	}
	continuation := result.StructuredContent["continuation"].(map[string]any)
	if continuation["state"] != "predicate_false" ||
		continuation["route"] != "continue_from_observation" {
		t.Fatalf("false predicate continuation=%#v", continuation)
	}

	prepared := prepareAXEvidence(result)
	batch := prepared.StructuredContent["batch"].(map[string]any)
	if batch["guard_failed_action_index"] != 0 ||
		batch["stopped_before_action_index"] != 1 {
		t.Fatalf("guard stop position was hidden: %#v", batch)
	}
	actions := batch["actions"].([]map[string]any)
	if len(actions) != 1 || actions[0]["until"] == nil {
		t.Fatalf("failed action guard evidence was hidden: %#v", batch)
	}
}

// Probe #13: optionalBool used to decode any string other than "true" as
// false, so {"type":"exists","value":"yes"} silently asserted absence.
func TestAssertionBooleansRejectStrings(t *testing.T) {
	for name, assertion := range map[string]map[string]any{
		"exists value": {
			"type": "exists", "target": map[string]any{"ref": "r1"}, "value": "yes",
		},
		"exists true string": {
			"type": "exists", "target": map[string]any{"ref": "r1"}, "value": "true",
		},
		"state value": {
			"type": "state", "target": map[string]any{"ref": "r1"},
			"state": "checked", "value": "false",
		},
		"window title exists": {
			"type": "window_title_contains", "text": "Save", "exists": "no",
		},
	} {
		if _, err := parseA11yAssertionSpec(assertion); err == nil {
			t.Fatalf("%s: string boolean was accepted", name)
		}
	}
	if got := optionalBool(map[string]any{"value": "yes"}, "value"); got != nil {
		t.Fatalf("optionalBool coerced a string to %v", *got)
	}
}

// The published "value" predicate must keep its comparison when the goal is
// rebuilt from the raw assertion (canonical reconciliation). It used to
// switch on the raw type, build an empty goal for "value", and pass for any
// observed value.
func TestValueAssertionGoalIsNotEmpty(t *testing.T) {
	for _, assertion := range []map[string]any{
		{"type": "value", "target": map[string]any{"ref": "r1"}, "value": "draft"},
		{"type": "value", "target": map[string]any{"ref": "r1"}, "value": "draft", "match": "contains"},
	} {
		goal := assertionGoal(assertion)
		if goal.empty() {
			t.Fatalf("assertion %v produced an empty goal", assertion)
		}
		if ok, detail := goal.check(&elementRecord{Value: "final"}, nil); ok {
			t.Fatalf("assertion %v passed for an unrelated value: %s", assertion, detail)
		}
	}
}
