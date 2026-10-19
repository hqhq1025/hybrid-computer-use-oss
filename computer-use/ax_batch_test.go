package main

import (
	"encoding/json"
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

func TestA11yBatchSourceKeepsExecutionContract(t *testing.T) {
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

func TestDecodeAXActionsAcceptsFlatShapes(t *testing.T) {
	actions, err := decodeAXActions([]any{
		map[string]any{
			"type": "click", "ref": "r7",
			"until": map[string]any{"type": "state", "ref": "r7", "state": "selected"},
		},
		map[string]any{"type": "keypress", "keys": []any{"CTRL", "L"}},
		map[string]any{"type": "type", "ref": "r8", "text": "Lovelace"},
		map[string]any{"type": "set_value", "ref": "r9", "value": ""},
		map[string]any{"type": "scroll", "direction": "down", "pages": 2.5},
		map[string]any{"type": "activate", "title": "Report"},
		map[string]any{"type": "close", "ref": "w3"},
	})
	if err != nil || len(actions) != 7 {
		t.Fatalf("valid batch rejected: %#v %v", actions, err)
	}
	if actions[0].postcondition == nil || actions[0].postcondition["type"] != "state" ||
		actions[0].postcondition["value"] != true {
		t.Fatalf("action until not decoded: %#v", actions[0].postcondition)
	}
	if actions[1].key != "CTRL+L" || actions[2].targetRef != "r8" ||
		actions[4].pages != 2.5 || actions[5].title != "Report" ||
		actions[6].targetRef != "w3" {
		t.Fatalf("decoded actions: %#v", actions)
	}
}

func TestDecodeAXActionsRejectsWithPathAndHint(t *testing.T) {
	tooMany := make([]any, a11yBatchMaxActions+1)
	for index := range tooMany {
		tooMany[index] = map[string]any{"type": "keypress", "keys": []any{"TAB"}}
	}
	for name, test := range map[string]struct {
		raw     any
		path    string
		message string
	}{
		"empty":        {[]any{}, "actions", "at least one action"},
		"too many":     {tooMany, "actions", "at most"},
		"not an array": {"click r1", "actions", "array"},
		"click needs ref": {[]any{map[string]any{"type": "click"}},
			"actions[0].ref", "click needs ref"},
		"wN on click": {[]any{map[string]any{"type": "click", "ref": "w1"}},
			"actions[0].ref", "wN is only for activate and close"},
		"rN on close": {[]any{map[string]any{"type": "close", "ref": "r1"}},
			"actions[0].ref", "rN is an element"},
		"close not last": {[]any{
			map[string]any{"type": "close", "ref": "w1"},
			map[string]any{"type": "click", "ref": "r1"},
		}, "actions[0]", "close must be the last action"},
		"activate both": {[]any{map[string]any{"type": "activate", "ref": "w1", "title": "x"}},
			"actions[0]", "exactly one of ref (wN) or title"},
		"activate neither": {[]any{map[string]any{"type": "activate"}},
			"actions[0]", "exactly one of ref (wN) or title"},
		"blank title": {[]any{map[string]any{"type": "activate", "title": " "}},
			"actions[0].title", "non-blank"},
		"field of another type": {[]any{map[string]any{"type": "click", "ref": "r1", "text": "x"}},
			"actions[0].text", "click does not accept"},
		"legacy key": {[]any{map[string]any{"type": "keypress", "key": "Tab"}},
			"actions[0].key", `use "keys"`},
		"blank key": {[]any{map[string]any{"type": "keypress", "keys": []any{" "}}},
			"actions[0].keys[0]", "key name"},
		"intent": {[]any{map[string]any{"type": "click", "ref": "r1", "intent": "Save"}},
			"actions[0].intent", "click does not accept"},
		"index target": {[]any{map[string]any{"type": "click", "index": 7.0}},
			"actions[0].index", "click does not accept"},
		"until not object": {[]any{map[string]any{"type": "click", "ref": "r1", "until": "selected"}},
			"actions[0].until", "until must be an object"},
		"per-action observe": {[]any{map[string]any{"type": "keypress", "keys": []any{"TAB"}, "observe": "none"}},
			"actions[0].observe", "keypress does not accept"},
		"type case": {[]any{map[string]any{"type": "Click", "ref": "r1"}},
			"actions[0].type", "type must be one of"},
		"direction case": {[]any{map[string]any{"type": "scroll", "direction": "UP"}},
			"actions[0].direction", "scroll needs direction"},
		"text budget": {[]any{
			map[string]any{"type": "type", "text": strings.Repeat("a", maxActionText)},
			map[string]any{"type": "type", "text": strings.Repeat("b", maxActionText)},
			map[string]any{"type": "type", "text": "c"},
		}, "actions[2]", "limited to"},
	} {
		_, err := decodeAXActions(test.raw)
		if err == nil || err.code != axCodeInvalidArgument ||
			err.path != test.path || !strings.Contains(err.message, test.message) {
			t.Fatalf("%s: got %#v", name, err)
		}
	}
}

// TestModelWrittenActionShapesGetUsableAnswers covers shapes models wrote in
// a real panel. The first is valid now; the others name the field and show
// the accepted shape.
func TestModelWrittenActionShapesGetUsableAnswers(t *testing.T) {
	if _, err := decodeAXActions([]any{map[string]any{"ref": "r103", "type": "click"}}); err != nil {
		t.Fatalf("flat ref click must be valid: %v", err)
	}
	for _, test := range []struct {
		action map[string]any
		path   string
		hint   string
	}{
		{map[string]any{"ref": "w1", "action": "close"}, "actions[0].action", `{"type":"close","ref":"w1"}`},
		{map[string]any{"ref": "r1", "action": "press"}, "actions[0].action", `{"type":"click","ref":"r1"}`},
		{map[string]any{"type": "press", "ref": "r1"}, "actions[0].type", `{"type":"click","ref":"r1"}`},
		{map[string]any{"type": "click", "target": map[string]any{"ref": "r4"}}, "actions[0].target", `{"type":"click","ref":"r4"}`},
		{map[string]any{"type": "keypress", "key": "CTRL+S"}, "actions[0].key", `{"type":"keypress","keys":["CTRL","S"]}`},
	} {
		_, err := decodeAXActions([]any{test.action})
		if err == nil || err.path != test.path || err.hint != test.hint {
			t.Fatalf("%v: got %#v", test.action, err)
		}
		if _, hintErr := decodeAXActions([]any{decodeJSONForTest(t, err.hint)}); hintErr != nil {
			t.Fatalf("hint %s is not a valid action: %v", err.hint, hintErr)
		}
	}
}

func decodeJSONForTest(t *testing.T, text string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		t.Fatalf("decode %s: %v", text, err)
	}
	return value
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
	context, err := service.deriveA11yBatchContext("", actions, nil)
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

	result := service.a11yBatch(
		[]any{
			map[string]any{"type": "click", "ref": elementRef},
			map[string]any{
				"type": "keypress",
				"keys": []any{"ENTER"},
				"until": map[string]any{
					"type": "state", "ref": "r999999", "state": "selected",
				},
			},
		},
		observation{},
		nil,
		"",
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
		"actions[1].until",
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

func TestRefLessBatchWithoutFocusOrWindowHasNoWindow(t *testing.T) {
	service, _, _, _ := sheetBatchService(t)
	_, err := service.deriveA11yBatchContext("", []a11yBatchAction{{
		actionType: "keypress",
		key:        "CTRL+S",
	}}, nil)
	if err == nil || !strings.Contains(err.Error(), "the batch has no window") {
		t.Fatalf("expected no-window rejection, got %v", err)
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
	_, err := service.deriveA11yBatchContext(
		"", []a11yBatchAction{
			{actionType: "click", targetRef: firstRef},
			{actionType: "click", targetRef: secondRef},
		}, nil,
	)
	if err == nil || !strings.Contains(err.Error(), "batch spans two windows") {
		t.Fatalf("expected cross-window rejection, got %v", err)
	}
}

func TestBatchAcceptsMatchingWindowRefAndRejectsAnother(t *testing.T) {
	service, appRef, windowRef, elementRef := sheetBatchService(t)
	context, err := service.deriveA11yBatchContext(windowRef, []a11yBatchAction{{
		actionType: "click", targetRef: elementRef,
	}}, nil)
	if err != nil || context.windowRef != windowRef {
		t.Fatalf("matching window_ref was rejected: %#v %v", context, err)
	}
	other := service.windowRefRegistry.Acquire(
		"libreoffice", "w2:fedcba9876543210fedcba9876543210", `dialog "Question"`, "c1",
		":1.84", "/window/2", "pid:10:start:100", "dialog", "Question", appRef,
	)
	_, err = service.deriveA11yBatchContext(other, []a11yBatchAction{{
		actionType: "click", targetRef: elementRef,
	}}, nil)
	if err == nil || !strings.Contains(err.Error(), "omit window_ref") {
		t.Fatalf("conflicting window_ref was accepted: %v", err)
	}
}

func TestBatchContextCarriesPrivateWindowExpectation(t *testing.T) {
	service, _, windowRef, elementRef := sheetBatchService(t)
	context, err := service.deriveA11yBatchContext(
		"", []a11yBatchAction{
			{actionType: "click", targetRef: elementRef},
		}, nil,
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

// a11yBatch decodes public action objects and runs them, so tests can drive
// a batch from raw JSON-shaped actions. The public act path decodes in
// decodeAXRequest and calls a11yBatchDecoded directly.
func (s *service) a11yBatch(
	rawActions any,
	obs observation,
	postcondition map[string]any,
	windowRef string,
) toolCallResult {
	actions, argErr := decodeAXActions(rawActions)
	if argErr != nil {
		requested := 0
		if items, ok := rawActions.([]any); ok {
			requested = len(items)
		}
		return axArgErrorResult(argErr, requested)
	}
	return s.a11yBatchDecoded(actions, obs, postcondition, windowRef)
}
