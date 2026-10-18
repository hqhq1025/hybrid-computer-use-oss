package main

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestCompactAXActionContentDropsRoutineRouteNotes(t *testing.T) {
	result := compactAXActionContent(toolCallResult{
		Content: []contentItem{{Type: "text", Text: strings.Join([]string{
			"Note: [a11y] Resolved element 12 to button Save",
			"Note: [a11y][semantic] Invoked the element's AT-SPI accessibility action.",
			"Note: MODAL DIALOG: Preferences",
			"ax:",
			` - "dialog \"Preferences\""`,
		}, "\n")}},
	})
	text := resultText(result)
	if strings.Contains(text, "Resolved element") ||
		strings.Contains(text, "Invoked the element") {
		t.Fatalf("routine action narration leaked: %s", text)
	}
	if !strings.Contains(text, "MODAL DIALOG") ||
		!strings.Contains(text, `dialog \"Preferences\"`) {
		t.Fatalf("decision-relevant action evidence was removed: %s", text)
	}
}

func TestA11yBatchModePublishesAndEnforcesSafetyContract(t *testing.T) {
	tool := findToolDefinition(t, "AX")
	for _, fragment := range []string{
		"Semantic observe/find/act/assert_ax",
		"act consumes refs only",
		"act never accepts app, selector, or index",
		"Split before window changes",
	} {
		if !strings.Contains(tool.Description, fragment) {
			t.Fatalf("a11y batch description missing %q", fragment)
		}
	}
	branches := tool.InputSchema["oneOf"].([]any)
	if len(branches) != 6 {
		t.Fatalf("a11y oneOf branch count=%d want 6", len(branches))
	}
	for _, raw := range branches {
		branch := raw.(map[string]any)
		properties := branch["properties"].(map[string]any)
		mode := properties["mode"].(map[string]any)
		if _, ok := mode["const"].(string); !ok {
			t.Fatalf("every a11y oneOf branch must use mode const: %#v", mode)
		}
	}
	observeBranch := findA11ySchemaBranch(t, "observe", "app")
	observeProps := observeBranch["properties"].(map[string]any)
	observePayload := observeProps["observe"].(map[string]any)
	if values, ok := observePayload["enum"].([]string); !ok ||
		!reflect.DeepEqual(values, a11yObserveValues) {
		t.Fatalf("a11y standalone observe enum=%#v", observePayload["enum"])
	}
	if slices.Contains(a11yObserveValues, observeNone) {
		t.Fatal("standalone AX observation must not advertise observe=none")
	}
	findBranch := findA11ySchemaBranch(t, "find", "")
	findProps := findBranch["properties"].(map[string]any)
	assertBranch := findA11ySchemaBranch(t, "assert_ax", "")
	assertProps := assertBranch["properties"].(map[string]any)
	for mode, properties := range map[string]map[string]any{
		"observe":   observeProps,
		"find":      findProps,
		"assert_ax": assertProps,
	} {
		scope, ok := properties["scope"].(map[string]any)
		if !ok {
			t.Fatalf("a11y %s schema must expose scope", mode)
		}
		if values, ok := scope["enum"].([]string); !ok ||
			!reflect.DeepEqual(values, a11yScopeValues) {
			t.Fatalf("a11y %s scope enum=%#v", mode, scope["enum"])
		}
	}
	query := findProps["query"].(map[string]any)
	queryProps := query["properties"].(map[string]any)
	for _, field := range []string{
		"name_contains", "name_equals", "text_contains", "text_equals",
	} {
		if _, ok := queryProps[field]; !ok {
			t.Fatalf("a11y find query must expose %s", field)
		}
	}
	limitProperty := queryProps["limit"].(map[string]any)
	if limitProperty["maximum"] != maxAXFindPageSize {
		t.Fatalf("find limit is not bounded: %#v", limitProperty)
	}
	cursorBranchFound := false
	for _, raw := range branches {
		properties := raw.(map[string]any)["properties"].(map[string]any)
		if _, ok := properties["cursor"]; !ok {
			continue
		}
		cursorBranchFound = true
		if _, hasQuery := properties["query"]; hasQuery {
			t.Fatal("cursor continuation must not also expose query")
		}
		if pattern := properties["cursor"].(map[string]any)["pattern"]; pattern != "^f[1-9][0-9]*$" {
			t.Fatalf("find cursor pattern=%v", pattern)
		}
	}
	if !cursorBranchFound {
		t.Fatal("AX find schema has no cursor continuation branch")
	}
	if _, present := assertProps["timeout_ms"]; present {
		t.Fatal("assert_ax timeout_ms must live inside assertion")
	}
	defs := tool.InputSchema["$defs"].(map[string]any)
	assertion := defs["assertion"].(map[string]any)
	if ref := assertProps["assertion"].(map[string]any)["$ref"]; ref != "#/$defs/assertion" {
		t.Fatalf("assert_ax does not reuse the shared assertion schema: %v", ref)
	}
	assertionBranches, ok := assertion["oneOf"].([]any)
	if !ok || len(assertionBranches) != 5 {
		t.Fatalf(
			"assertion schema must distinguish element and window title: %#v",
			assertion,
		)
	}
	valueBranch := assertionBranches[2].(map[string]any)
	valueConstraints, ok := valueBranch["allOf"].([]any)
	if !ok || len(valueConstraints) != 1 {
		t.Fatalf("value assertion schema does not constrain contains: %#v",
			valueBranch)
	}
	windowTitleBranch := assertionBranches[4].(map[string]any)
	windowTitleProps := windowTitleBranch["properties"].(map[string]any)
	for _, field := range []string{
		"type",
		"text",
		"exists",
		"stable_ms",
		"timeout_ms",
	} {
		if _, ok := windowTitleProps[field]; !ok {
			t.Fatalf("window title assertion must expose %s", field)
		}
	}
	elementTarget := assertionBranches[0].(map[string]any)["properties"].(map[string]any)["target"].(map[string]any)
	targetBranches := elementTarget["oneOf"].([]any)
	for _, raw := range targetBranches {
		properties := raw.(map[string]any)["properties"].(map[string]any)
		refProperty, ok := properties["ref"].(map[string]any)
		if !ok {
			continue
		}
		if pattern := refProperty["pattern"]; pattern != "^r[1-9][0-9]*$" {
			t.Fatalf("assert_ax element ref accepts window refs: %#v", refProperty)
		}
	}
	windowTitleContains := windowTitleProps["text"].(map[string]any)
	if windowTitleContains["pattern"] != nonBlankPattern {
		t.Fatalf(
			"window title text pattern=%v, want a non-blank pattern",
			windowTitleContains["pattern"],
		)
	}
	appsBranch := findA11ySchemaBranch(t, "observe", "apps")
	appsProps := appsBranch["properties"].(map[string]any)
	for _, forbidden := range []string{
		"app", "actions", "observe", "text_limit", "timeout_ms",
		"max_tree_nodes", "max_tree_depth", "query", "assertion", "scope",
	} {
		if _, ok := appsProps[forbidden]; ok {
			t.Fatalf("observe/apps schema must not expose %s", forbidden)
		}
	}
	actBranch := findA11ySchemaBranch(t, "act", "")
	props := actBranch["properties"].(map[string]any)
	if ref := props["postcondition"].(map[string]any)["$ref"]; ref != "#/$defs/assertion" {
		t.Fatalf("act.postcondition does not reuse the shared assertion schema: %v", ref)
	}
	actions := props["actions"].(map[string]any)
	if actions["maxItems"] != a11yBatchMaxActions {
		t.Fatalf("a11y actions maxItems=%v want %d",
			actions["maxItems"], a11yBatchMaxActions)
	}
	if actions["minItems"] != 1 {
		t.Fatal("a11y act schema must require at least one action")
	}
	if _, ok := props["observe"]; !ok {
		t.Fatal("a11y batch must expose one final observe control")
	}
	actObserve := props["observe"].(map[string]any)
	description, _ := actObserve["description"].(string)
	for _, phrase := range []string{
		"ACT ONLY: none|diff|screenshot",
		"tree/full are invalid",
		"fresh post-action diff",
		"reread only if required",
	} {
		if !strings.Contains(description, phrase) {
			t.Fatalf("a11y act observe guidance missing %q: %q",
				phrase, description)
		}
	}
	if values, ok := actObserve["enum"].([]string); !ok ||
		!reflect.DeepEqual(values, a11yActObserveValues) {
		t.Fatalf("a11y act observe enum=%#v", actObserve["enum"])
	}
	for _, forbidden := range []string{observeTree, observeFull} {
		if slices.Contains(a11yActObserveValues, forbidden) {
			t.Fatalf("a11y act must not expose complete readback %q", forbidden)
		}
	}
	for _, forbidden := range []string{"expect", "settle_ms"} {
		if _, ok := props[forbidden]; ok {
			t.Fatalf("a11y batch beta must not expose action-local %s", forbidden)
		}
	}
	if _, ok := props["app"]; ok {
		t.Fatal("ref-only AX act must not expose app")
	}
	item := actions["items"].(map[string]any)
	actionBranches := item["oneOf"].([]any)
	gotTypes := make([]string, 0, len(actionBranches))
	for _, raw := range actionBranches {
		branch := raw.(map[string]any)
		fields := branch["properties"].(map[string]any)
		actionType := fields["type"].(map[string]any)
		branchTypes := []string{}
		if value, ok := actionType["const"].(string); ok {
			branchTypes = append(branchTypes, value)
		}
		if values, ok := actionType["enum"].([]string); ok {
			branchTypes = append(branchTypes, values...)
		}
		if len(branchTypes) == 0 {
			t.Fatalf("action branch has no const or enum type: %#v", actionType)
		}
		gotTypes = append(gotTypes, branchTypes...)
		if _, ok := fields["element"]; ok {
			t.Fatal("public action schema must not expose ambiguous element")
		}
		if _, ok := fields["element_index"]; ok {
			t.Fatal("public action schema must not expose ambiguous element_index")
		}
		if _, ok := fields["intent"]; ok {
			t.Fatal("public action schema must not expose misleading intent")
		}
		if _, ok := fields["target"]; !ok {
			t.Fatal("each public action schema must expose structured target")
		}
		if ref := fields["postcondition"].(map[string]any)["$ref"]; ref != "#/$defs/assertion" {
			t.Fatalf("action %v postcondition does not reuse assertion schema: %v",
				branchTypes, ref)
		}
		target := fields["target"].(map[string]any)
		targetFields := target["properties"].(map[string]any)
		if len(targetFields) != 1 || targetFields["ref"] == nil {
			t.Fatalf("public %v target is not ref-only: %#v", branchTypes, target)
		}
		pattern := targetFields["ref"].(map[string]any)["pattern"]
		for _, branchType := range branchTypes {
			want := "^r[1-9][0-9]*$"
			if branchType == "activate" || branchType == "close" {
				want = "^w[1-9][0-9]*$"
			}
			if pattern != want {
				t.Fatalf("%s target pattern=%v want %s", branchType, pattern, want)
			}
		}
		if slices.Contains(branchTypes, "keypress") {
			if _, ok := fields["keys"]; !ok {
				t.Fatal("public keypress schema must use GUI-compatible keys")
			}
			if _, ok := fields["key"]; ok {
				t.Fatal("public keypress schema must hide legacy key")
			}
		}
	}
	for _, windowAction := range []string{"activate", "close"} {
		count := 0
		for _, raw := range actionBranches {
			fields := raw.(map[string]any)["properties"].(map[string]any)
			if fields["type"].(map[string]any)["const"] == windowAction {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("%s must have one independent const branch, got %d",
				windowAction, count)
		}
	}
	for _, want := range []string{
		"activate", "close", "click", "keypress", "scroll", "set_value", "type",
	} {
		if !slices.Contains(gotTypes, want) {
			t.Fatalf("a11y action types missing %q: %#v", want, gotTypes)
		}
	}

	valid := []any{
		map[string]any{
			"type":   "click",
			"target": map[string]any{"ref": "r7"},
			"postcondition": map[string]any{
				"type": "state",
				"target": map[string]any{
					"ref": "r7",
				},
				"state": "selected",
				"value": true,
			},
		},
		map[string]any{
			"type": "keypress", "keys": []any{"CTRL", "L"},
		},
		map[string]any{
			"type": "type",
			"target": map[string]any{
				"ref": "r8",
			},
			"text": "Lovelace",
		},
	}
	normalized, err := normalizeA11yBatchActions(valid)
	if err != nil || len(normalized) != len(valid) {
		t.Fatalf("valid a11y batch rejected: actions=%#v err=%v",
			normalized, err)
	}
	if normalized[0].postcondition == nil {
		t.Fatal("action postcondition was not preserved by normalization")
	}
	if _, err := normalizeA11yBatchActions([]any{
		map[string]any{
			"type":          "click",
			"target":        map[string]any{"ref": "r7"},
			"postcondition": "selected",
		},
	}); err == nil || !strings.Contains(err.Error(), "postcondition must be an AX assertion object") {
		t.Fatalf("non-object action postcondition must be rejected: %v", err)
	}
	if _, err := normalizeA11yBatchActions([]any{
		map[string]any{"type": "keypress", "keys": []any{"TAB"}},
		map[string]any{
			"type":   "click",
			"target": map[string]any{"index": float64(7)},
		},
	}); err == nil || !strings.Contains(err.Error(), `target must be {"ref":"rN"} or {"ref":"wN"}`) {
		t.Fatalf("numeric index must fail ref-only batch validation: %v", err)
	}
	if _, err := normalizeA11yBatchActions([]any{
		map[string]any{
			"type": "keypress", "keys": []any{"TAB"}, "observe": "none",
		},
	}); err == nil || !strings.Contains(err.Error(), "does not accept field") {
		t.Fatalf("per-action observe must be rejected: %v", err)
	}
	if _, err := normalizeA11yBatchActions([]any{}); err == nil {
		t.Fatal("the normal mutating-batch validator must reject an empty sequence")
	}
	tooMany := make([]any, a11yBatchMaxActions+1)
	for index := range tooMany {
		tooMany[index] = map[string]any{
			"type": "keypress", "keys": []any{"TAB"},
		}
	}
	if _, err := normalizeA11yBatchActions(tooMany); err == nil {
		t.Fatal("a11y batch action count must be bounded")
	}
	if _, err := normalizeA11yBatchActions([]any{
		map[string]any{
			"type": "type", "text": strings.Repeat("a", maxActionText),
		},
		map[string]any{
			"type": "type", "text": strings.Repeat("b", maxActionText),
		},
		map[string]any{
			"type": "type", "text": "c",
		},
	}); err == nil || !strings.Contains(err.Error(), "cumulative") {
		t.Fatalf("a11y batch cumulative text budget must be bounded: %v", err)
	}

	service := newService()
	rejected := service.a11yBatch([]any{map[string]any{"type": "keypress", "keys": []any{"TAB"}}},
		observation{}, nil,
	)
	if !rejected.IsError ||
		rejected.StructuredContent["action_attempted"] != false {
		t.Fatalf("prevalidation failure must prove no action attempt: %#v", rejected)
	}
	noObservation := service.a11yBatch(
		[]any{}, observation{mode: observeNone}, nil,
	)
	if !noObservation.IsError ||
		!strings.Contains(resultText(noObservation), `mode="act" requires at least one action`) {
		t.Fatalf("empty a11y act must reject and point to observe mode: %#v",
			noObservation)
	}

	source := mainGoSource(t)
	for _, fragment := range []string{
		`a11yBatchActionObservation(`,
		`if index == count-1 && !hasPostcondition`,
		`postcondition != nil || action.postcondition != nil`,
		`postcondition != nil || actionPostconditionChecked`,
		`s.executeA11yBatchAction(`,
		`"observation_required"] = true`,
	} {
		if !strings.Contains(source, fragment) {
			t.Fatalf("a11y batch execution contract missing %q", fragment)
		}
	}
}

func TestA11yBatchRejectsRemovedIntentField(t *testing.T) {
	_, err := normalizeA11yBatchActions([]any{
		map[string]any{
			"type":   "click",
			"intent": "the Save button",
			"target": map[string]any{
				"selector": map[string]any{
					"role": "push button",
					"name": "Save",
				},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), `does not accept field "intent"`) {
		t.Fatalf("removed intent field was accepted: %v", err)
	}
}

func TestA11yKeypressUsesOnlyPublicKeys(t *testing.T) {
	normalized, err := normalizeA11yBatchActions([]any{
		map[string]any{
			"type": "keypress", "keys": []any{"CTRL", "L"},
		},
	})
	if err != nil || len(normalized) != 1 {
		t.Fatalf("keypress rejected: %#v err=%v", normalized, err)
	}
	if got := normalized[0].key; got != "CTRL+L" {
		t.Fatalf("normalized key=%#v want CTRL+L", got)
	}
	if _, err := normalizeA11yBatchActions([]any{
		map[string]any{
			"type": "keypress", "key": "Tab",
		},
	}); err == nil || !strings.Contains(err.Error(), `does not accept field "key"`) {
		t.Fatalf("legacy key must fail preflight: %v", err)
	}
}

func TestBatchMutationSummaryPreservesActionUnknown(t *testing.T) {
	emitted, partial, unknown := batchMutationSummary([]map[string]any{
		{
			"input_events_emitted": false,
			"mutation_unknown":     true,
		},
	})
	if emitted || partial || !unknown {
		t.Fatalf(
			"batch summary lost mutation uncertainty: emitted=%t partial=%t unknown=%t",
			emitted, partial, unknown,
		)
	}
}

func TestRefOwnedBatchDerivesOneWindowAndKeyboardInheritance(t *testing.T) {
	service, appRef, windowRef, elementRef := sheetBatchService(t)
	actions := []a11yBatchAction{
		{actionType: "click", targetRef: elementRef},
		{actionType: "keypress", key: "ENTER"},
	}
	context, err := service.deriveA11yBatchContext("", "", actions)
	if err != nil {
		t.Fatal(err)
	}
	if context.appRef != appRef || context.appToken != appRef ||
		context.windowRef != windowRef || context.windowHandle == "" {
		t.Fatalf("unexpected ref-owned context: %#v", context)
	}
}

func TestActionPostconditionsArePreflightedBeforeFirstInput(t *testing.T) {
	service, _, _, elementRef := sheetBatchService(t)
	backend := &recordingPlatformBackend{}
	service.backend = backend

	result := service.a11yBatch([]any{
		map[string]any{
			"type":   "click",
			"target": map[string]any{"ref": elementRef},
		},
		map[string]any{
			"type": "keypress",
			"keys": []any{"ENTER"},
			"postcondition": map[string]any{
				"type": "state",
				"target": map[string]any{
					"ref": "r999999",
				},
				"state": "selected",
				"value": true,
			},
		},
	},
		observation{},
		nil,
	)
	if !result.IsError ||
		result.StructuredContent["action_attempted"] != false {
		t.Fatalf("invalid action guard must fail before input: %#v", result)
	}
	if len(backend.calls) != 0 {
		t.Fatalf("preflight failure reached provider: %#v", backend.calls)
	}
	if !strings.Contains(
		resultText(result),
		"actions[1].postcondition",
	) {
		t.Fatalf("preflight error did not identify action guard: %q",
			resultText(result))
	}
}

func TestUnknownActionPostconditionRecoversCurrentWindowAfterSourceDisappears(
	t *testing.T,
) {
	service, appRef, sourceWindowRef, _ := sheetBatchService(t)
	current := editorFormSnapshot()
	current.App = appDescriptor{
		Name:              "Editor",
		ProviderBusName:   ":1.84",
		ProcessGeneration: "pid:10:start:100",
	}
	current.WindowTitle = "Document"
	current.WindowHandle = "w2:11111111111111111111111111111111"
	current.SnapshotID = "current"
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
				},
			},
			{
				OK:       true,
				Snapshot: current,
			},
		},
	}
	service.backend = backend
	context := &a11yBatchContext{
		appRef:       appRef,
		windowRef:    sourceWindowRef,
		appToken:     appRef,
		windowHandle: "w2:0123456789abcdef0123456789abcdef",
	}
	postcondition := toolCallResult{
		Content: []contentItem{{
			Type: "text",
			Text: "assert_ax observation_incomplete",
		}},
		IsError: true,
		StructuredContent: map[string]any{
			"assertion_status": "unknown",
			"failure_code":     "observation_incomplete",
		},
	}
	actionResult := toolCallResult{
		StructuredContent: map[string]any{
			"input_events_emitted": true,
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

	result := service.stopA11yBatchForActionPostcondition(
		context,
		0,
		2,
		action,
		actionEvidence,
		actionResult,
		postcondition,
		observation{mode: observeDiff},
		false,
	)
	if !result.IsError {
		t.Fatalf("unknown action postcondition must remain a failed batch: %#v",
			result)
	}
	for key, expected := range map[string]any{
		"failure_code":                        "action_postcondition_unknown",
		"failure_phase":                       "between_actions",
		"final_observation_returned":          true,
		"observation_required":                false,
		"next_identical_observation_required": false,
		"recovery_observation_attempted":      true,
		"recovery_observation_returned":       true,
		"cause_unattributed":                  true,
	} {
		if result.StructuredContent[key] != expected {
			t.Fatalf("%s=%#v want %#v in %#v",
				key, result.StructuredContent[key], expected,
				result.StructuredContent)
		}
	}
	continuation := result.StructuredContent["continuation"].(map[string]any)
	if continuation["state"] != "inspect_current_state" ||
		continuation["route"] != "continue_from_observation" ||
		continuation["retry_same_request"] != false {
		t.Fatalf("recovered continuation=%#v", continuation)
	}
	if len(backend.observeRequests) != 2 {
		t.Fatalf("observe calls=%d, want 2", len(backend.observeRequests))
	}
	if backend.observeRequests[0].WindowHandle != context.windowHandle ||
		backend.observeRequests[1].WindowHandle != "" {
		t.Fatalf("unexpected recovery observation targets: %#v",
			backend.observeRequests)
	}
	if text := resultText(result); !strings.Contains(text, `button "Save"`) ||
		!strings.Contains(text, "Fresh current state after an uncertain mutation") {
		t.Fatalf("fresh recovery observation missing: %q", text)
	}
}

func TestUnknownActionPostconditionRequiresObservationWhenNoneReturned(t *testing.T) {
	service := newService()
	postcondition := toolCallResult{
		Content: []contentItem{{
			Type: "text",
			Text: "assertion unavailable",
		}},
		IsError: true,
		StructuredContent: map[string]any{
			"assertion_status": "unknown",
			"failure_code":     "runtime_timeout",
		},
	}
	actionResult := toolCallResult{
		StructuredContent: map[string]any{
			"input_events_emitted": true,
		},
	}
	action := a11yBatchAction{
		actionType:    "click",
		postcondition: map[string]any{"type": "exists"},
	}
	actionEvidence := []map[string]any{
		batchActionEvidenceEntry(0, "click", actionResult),
	}

	result := service.stopA11yBatchForActionPostcondition(
		&a11yBatchContext{windowRef: "w1"},
		0,
		1,
		action,
		actionEvidence,
		actionResult,
		postcondition,
		observation{mode: observeNone},
		false,
	)
	if result.StructuredContent["failure_phase"] != "action_postcondition" ||
		result.StructuredContent["observation_required"] != true {
		t.Fatalf("unknown final guard evidence=%#v", result.StructuredContent)
	}
	continuation := result.StructuredContent["continuation"].(map[string]any)
	if continuation["state"] != "observation_required" ||
		continuation["route"] != "observe" {
		t.Fatalf("unknown guard continuation=%#v", continuation)
	}
}

func TestAllUntargetedBatchRequiresWindowRef(t *testing.T) {
	service, _, _, _ := sheetBatchService(t)
	_, err := service.deriveA11yBatchContext(
		"", "", []a11yBatchAction{{
			actionType: "keypress",
			key:        "CTRL+S",
		}},
	)
	if err == nil || !strings.Contains(err.Error(), "requires window_ref") {
		t.Fatalf("expected window_ref rejection, got %v", err)
	}
}

func TestCrossWindowBatchFailsBeforeInput(t *testing.T) {
	service, appRef, _, firstRef := sheetBatchService(t)
	secondHandle := "w2:fedcba9876543210fedcba9876543210"
	secondRef := service.refRegistry.Acquire(
		"libreoffice", secondHandle, `button "OK"`, CanonicalRole("button"),
		"OK", "c1",
		":1.84", "/window/2/ok", "pid:10:start:100",
		":1.84", "/window/2", "pid:10:start:100", "dialog", "Question",
		appRef,
	)
	_, err := service.deriveA11yBatchContext("", "", []a11yBatchAction{
		{actionType: "click", targetRef: firstRef},
		{actionType: "click", targetRef: secondRef},
	},
	)
	if err == nil || !strings.Contains(err.Error(), "multiple logical windows") {
		t.Fatalf("expected cross-window rejection, got %v", err)
	}
}

func TestRefOwnedBatchRejectsDuplicateAppOrWindowRef(t *testing.T) {
	service, appRef, windowRef, elementRef := sheetBatchService(t)
	for name, identity := range map[string]struct {
		appRef    string
		windowRef string
	}{
		"app_ref":    {appRef: appRef},
		"window_ref": {windowRef: windowRef},
	} {
		_, err := service.deriveA11yBatchContext(identity.appRef, identity.windowRef, []a11yBatchAction{{
			actionType: "click",
			targetRef:  elementRef,
		}},
		)
		if err == nil ||
			!strings.Contains(err.Error(), "must omit app_ref and window_ref") {
			t.Fatalf("%s duplicate identity was accepted: %v", name, err)
		}
	}
}

func TestPublicBatchRejectsSelectorTargets(t *testing.T) {
	_, err := normalizeA11yBatchActions([]any{map[string]any{
		"type": "click",
		"target": map[string]any{
			"selector": map[string]any{"role": "button", "name": "Save"},
		},
	}})
	if err == nil || !strings.Contains(err.Error(), `target must be {"ref":"rN"} or {"ref":"wN"}`) {
		t.Fatalf("expected ref-only rejection, got %v", err)
	}
}

func TestSetValueThenClickRemainsSchemaValid(t *testing.T) {
	actions, err := normalizeA11yBatchActions([]any{
		map[string]any{
			"type":   "set_value",
			"target": map[string]any{"ref": "r98"},
			"value":  "5",
		},
		map[string]any{
			"type":   "set_value",
			"target": map[string]any{"ref": "r99"},
			"value":  "2",
		},
		map[string]any{
			"type":   "click",
			"target": map[string]any{"ref": "r100"},
		},
	})
	if err != nil || len(actions) != 3 {
		t.Fatalf("set_value followed by click must remain schema-valid: actions=%#v err=%v", actions, err)
	}
}

func TestBatchContextCarriesPrivateWindowExpectation(t *testing.T) {
	service, _, windowRef, elementRef := sheetBatchService(t)
	context, err := service.deriveA11yBatchContext("", "", []a11yBatchAction{
		{actionType: "click", targetRef: elementRef},
	},
	)
	if err != nil {
		t.Fatal(err)
	}
	if context.windowRef != windowRef || context.expectedWindow == nil {
		t.Fatalf("batch context lacks expected window: %#v", context)
	}
	if context.expectedWindow.WindowHandle != context.windowHandle ||
		context.expectedWindow.ProviderBusName == "" ||
		context.expectedWindow.ProcessGeneration == "" {
		t.Fatalf("incomplete expected window: %#v", context.expectedWindow)
	}
}

func TestBatchStopsAfterConfirmedWindowTransition(t *testing.T) {
	service, appRef, _, _ := sheetBatchService(t)
	context := &a11yBatchContext{
		appRef:       appRef,
		appToken:     appRef,
		windowHandle: "w2:0123456789abcdef0123456789abcdef",
	}
	service.snapshots[appRef] = &appSnapshot{
		WindowHandle: "w2:fedcba9876543210fedcba9876543210",
	}
	if got := service.a11yBatchWindowContinuity(context); got != a11yWindowChanged {
		t.Fatal("confirmed logical-window transition was not detected")
	}
}

func TestBatchAllowsProviderRebindInsideSameNativeWindow(t *testing.T) {
	service, appRef, windowRef, _ := sheetBatchService(t)
	handle := "w2:0123456789abcdef0123456789abcdef"
	context := &a11yBatchContext{
		appRef:       appRef,
		appToken:     appRef,
		windowRef:    windowRef,
		windowHandle: handle,
	}
	service.snapshots[appRef] = &appSnapshot{
		WindowHandle:    handle,
		publicWindowRef: windowRef,
		App: appDescriptor{
			ProviderBusName:   ":1.84",
			ProcessGeneration: "pid:10:start:100",
		},
	}
	if got := service.a11yBatchWindowContinuity(context); got != a11yWindowSame {
		t.Fatal("provider rebind inside the same native window must keep wN valid")
	}
}

func TestBatchStopsWhenWindowContinuityCannotBeSampled(t *testing.T) {
	service, appRef, windowRef, _ := sheetBatchService(t)
	context := &a11yBatchContext{
		appRef:       appRef,
		appToken:     appRef,
		windowRef:    windowRef,
		windowHandle: "w2:0123456789abcdef0123456789abcdef",
	}
	if got := service.a11yBatchWindowContinuity(context); got != a11yWindowUnknown {
		t.Fatalf("missing refreshed snapshot was reported as %s", got)
	}
	service.snapshots[appRef] = &appSnapshot{}
	if got := service.a11yBatchWindowContinuity(context); got != a11yWindowUnknown {
		t.Fatalf("missing refreshed window handle was reported as %s", got)
	}
}

func TestBatchStopsWhenLogicalWindowChangesOnSameNativeHandle(t *testing.T) {
	service, appRef, oldWindowRef, _ := sheetBatchService(t)
	handle := "w2:0123456789abcdef0123456789abcdef"
	newWindowRef := service.windowRefRegistry.Acquire(
		"soffice", handle, `dialog "Question"`, "c2",
		":1.84", "/app/dialog/question", "pid:10:start:100",
		"dialog", "Question", appRef,
	)
	if newWindowRef == "" || newWindowRef == oldWindowRef {
		t.Fatalf(
			"logical replacement did not receive a new ref: old=%q new=%q",
			oldWindowRef, newWindowRef,
		)
	}
	service.snapshots[appRef] = &appSnapshot{
		WindowHandle:    handle,
		publicAppRef:    appRef,
		publicWindowRef: newWindowRef,
	}
	context := &a11yBatchContext{
		appRef:       appRef,
		appToken:     appRef,
		windowRef:    oldWindowRef,
		windowHandle: handle,
	}
	if got := service.a11yBatchWindowContinuity(context); got != a11yWindowChanged {
		t.Fatalf("same-handle logical replacement was reported as %s", got)
	}
}
