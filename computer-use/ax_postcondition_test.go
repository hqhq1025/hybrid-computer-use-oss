package main

import (
	"strings"
	"testing"
)

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
