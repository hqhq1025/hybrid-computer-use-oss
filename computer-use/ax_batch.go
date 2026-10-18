package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

type a11yBatchAction struct {
	actionType    string
	targetRef     string
	direction     string
	pages         float64
	text          string
	key           string
	value         string
	postcondition map[string]any
}

type a11yBatchContext struct {
	appRef         string
	windowRef      string
	appToken       string
	windowHandle   string
	expectedWindow *ProviderWindowExpectation
}

type a11yWindowContinuity string

const (
	a11yWindowSame    a11yWindowContinuity = "same"
	a11yWindowChanged a11yWindowContinuity = "changed"
	a11yWindowUnknown a11yWindowContinuity = "unknown"
)

func (s *service) deriveA11yBatchContext(
	explicitAppRef,
	explicitWindowRef string,
	actions []a11yBatchAction,
) (*a11yBatchContext, error) {
	context := &a11yBatchContext{}
	bindApp := func(ref, fallback string) error {
		ref = strings.TrimSpace(ref)
		fallback = strings.TrimSpace(fallback)
		if ref != "" {
			if context.appRef != "" && context.appRef != ref {
				return fmt.Errorf(
					"batch spans multiple application refs (%s and %s)",
					context.appRef, ref,
				)
			}
			context.appRef = ref
			context.appToken = ref
			return nil
		}
		if context.appToken == "" {
			context.appToken = fallback
		}
		return nil
	}
	bindWindow := func(ref, handle, appRef, fallbackApp string) error {
		ref = strings.TrimSpace(ref)
		handle = strings.TrimSpace(handle)
		if context.windowHandle != "" && context.windowHandle != handle {
			return fmt.Errorf(
				"batch spans multiple logical windows (%s and %s); split it before input",
				context.windowRef, ref,
			)
		}
		if context.windowRef == "" {
			context.windowRef = ref
			context.windowHandle = handle
		}
		return bindApp(appRef, fallbackApp)
	}

	if explicitAppRef != "" {
		entry, err := s.resolveShortAXAppRef(explicitAppRef)
		if err != nil {
			return nil, err
		}
		if err := bindApp(entry.Ref, entry.DisplayName); err != nil {
			return nil, err
		}
	}
	if explicitWindowRef != "" {
		entry, err := s.resolveShortAXWindowRefOwned(explicitWindowRef)
		if err != nil {
			return nil, err
		}
		if err := bindWindow(
			entry.Ref, entry.WindowHandle, entry.AppRef, entry.AppIdentity,
		); err != nil {
			return nil, err
		}
	}

	hasTarget := false
	hasOwnedShortTarget := false
	for index, action := range actions {
		address := strings.TrimSpace(action.targetRef)
		if address == "" {
			continue
		}
		hasTarget = true
		switch {
		case isShortAXRef(address):
			hasOwnedShortTarget = true
			entry, err := s.resolveShortAXRefOwned(address)
			if err != nil {
				return nil, fmt.Errorf("actions[%d].target: %w", index, err)
			}
			if err := bindWindow(
				entry.LogicalWindowRef,
				entry.WindowHandle,
				entry.AppRef,
				entry.AppIdentity,
			); err != nil {
				return nil, fmt.Errorf("actions[%d].target: %w", index, err)
			}
		case isShortAXWindowRef(address):
			hasOwnedShortTarget = true
			entry, err := s.resolveShortAXWindowRefOwned(address)
			if err != nil {
				return nil, fmt.Errorf("actions[%d].target: %w", index, err)
			}
			if err := bindWindow(
				entry.Ref, entry.WindowHandle, entry.AppRef, entry.AppIdentity,
			); err != nil {
				return nil, fmt.Errorf("actions[%d].target: %w", index, err)
			}
		default:
			return nil, fmt.Errorf(
				"actions[%d].target must use rN or wN", index,
			)
		}
	}
	if hasOwnedShortTarget &&
		(strings.TrimSpace(explicitAppRef) != "" ||
			strings.TrimSpace(explicitWindowRef) != "") {
		return nil, errors.New(
			"targeted AX actions must omit app_ref and window_ref; " +
				"each rN/wN target already owns its application and logical window",
		)
	}

	if context.windowHandle == "" {
		if !hasTarget {
			return nil, errors.New(
				"an all-untargeted batch requires window_ref; app names do not prove keyboard focus",
			)
		}
		return nil, errors.New(
			"targeted AX actions require an rN or wN ref returned by observe/find",
		)
	}
	if explicitAppRef != "" && context.appRef != explicitAppRef {
		return nil, fmt.Errorf(
			"app_ref %s conflicts with the batch window owner %s",
			explicitAppRef, context.appRef,
		)
	}
	if context.appToken == "" {
		return nil, errors.New(
			"the batch window has no current application owner; observe it again",
		)
	}
	if entry, err := s.windowRefRegistry.Resolve(context.windowRef, ""); err == nil {
		context.expectedWindow = &ProviderWindowExpectation{
			WindowHandle:       entry.WindowHandle,
			ProviderBusName:    entry.ProviderWindowBusName,
			ProviderObjectPath: entry.ProviderWindowObjectPath,
			ProcessGeneration:  entry.ProviderWindowProcessGeneration,
			Role:               entry.ProviderWindowRole,
			Name:               entry.ProviderWindowName,
		}
	}
	return context, nil
}

func (s *service) a11yBatchWindowContinuity(
	context *a11yBatchContext,
) a11yWindowContinuity {
	if s == nil || context == nil || strings.TrimSpace(context.windowHandle) == "" {
		return a11yWindowUnknown
	}
	snapshot := s.currentSnapshot(context.appToken)
	if snapshot == nil {
		return a11yWindowUnknown
	}
	if snapshot.WindowAbsent {
		return a11yWindowChanged
	}
	if strings.TrimSpace(snapshot.WindowHandle) == "" ||
		snapshot.WindowHandle != context.windowHandle {
		if strings.TrimSpace(snapshot.WindowHandle) == "" {
			return a11yWindowUnknown
		}
		return a11yWindowChanged
	}
	if strings.TrimSpace(context.windowRef) == "" ||
		strings.TrimSpace(snapshot.publicWindowRef) == "" {
		return a11yWindowUnknown
	}
	windowEntry, err := s.windowRefRegistry.Resolve(context.windowRef, "")
	if err != nil {
		var lifecycle *axRefLifecycleError
		if errors.As(err, &lifecycle) && lifecycle.lifecycle == AXRefNeedsRevalidation {
			return a11yWindowUnknown
		}
		return a11yWindowChanged
	}
	if windowEntry.WindowHandle != context.windowHandle ||
		snapshot.publicWindowRef != context.windowRef {
		return a11yWindowChanged
	}
	return a11yWindowSame
}

func a11yBatchActionFields(tool string) map[string]bool {
	common := map[string]bool{
		"type": true, "target": true, "postcondition": true,
	}
	for _, field := range map[string][]string{
		"activate":  {},
		"close":     {},
		"click":     {},
		"keypress":  {"keys"},
		"scroll":    {"direction", "pages"},
		"set_value": {"value"},
		"type":      {"text"},
	}[tool] {
		common[field] = true
	}
	return common
}

func requiredA11yBatchString(
	action map[string]any,
	field string,
	allowEmpty bool,
) (string, error) {
	value, present := action[field]
	if !present {
		return "", fmt.Errorf("missing required field %q", field)
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", field)
	}
	if !allowEmpty && strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("%s must not be empty", field)
	}
	return text, nil
}

func normalizedA11yBatchKey(action map[string]any) (string, error) {
	rawKeys, hasKeys := action["keys"]
	if !hasKeys {
		return "", errors.New(`missing required field "keys"`)
	}

	items, ok := rawKeys.([]any)
	if !ok {
		typed, typedOK := rawKeys.([]string)
		if !typedOK {
			return "", errors.New("keys must be an array of key names")
		}
		items = make([]any, len(typed))
		for index, item := range typed {
			items[index] = item
		}
	}
	if len(items) == 0 || len(items) > guiMaxKeys {
		return "", fmt.Errorf(
			"keys must contain between 1 and %d key names", guiMaxKeys)
	}
	keys := make([]string, 0, len(items))
	for index, item := range items {
		key, ok := item.(string)
		if !ok || strings.TrimSpace(key) == "" {
			return "", fmt.Errorf(
				"keys[%d] must be a non-empty string", index)
		}
		key = strings.TrimSpace(key)
		if len([]rune(key)) > guiMaxKeyLength {
			return "", fmt.Errorf(
				"keys[%d] accepts at most %d characters",
				index, guiMaxKeyLength,
			)
		}
		keys = append(keys, key)
	}
	return strings.Join(keys, "+"), nil
}

func validateA11yBatchNumber(action map[string]any, field string) error {
	value, present := action[field]
	if !present {
		return nil
	}
	switch typed := value.(type) {
	case int:
		return nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return fmt.Errorf("%s must be a finite number", field)
		}
		return nil
	case json.Number:
		if _, err := typed.Float64(); err != nil {
			return fmt.Errorf("%s must be a number", field)
		}
		return nil
	default:
		return fmt.Errorf("%s must be a number", field)
	}
}

// actionTargetRef reads an action's optional target, which is exactly
// {"ref": "rN"} or {"ref": "wN"}. Index and selector targets are read-only
// forms of assertions and observe roots, never action targets.
func actionTargetRef(action map[string]any) (string, error) {
	raw, present := action["target"]
	if !present {
		return "", nil
	}
	target, ok := raw.(map[string]any)
	ref, isString := target["ref"].(string)
	if !ok || len(target) != 1 || !isString {
		return "", errors.New(`target must be {"ref":"rN"} or {"ref":"wN"}`)
	}
	if !isShortAXRef(ref) && !isShortAXWindowRef(ref) {
		return "", errors.New(
			"target.ref must be an exact rN or wN copied from observe/find; " +
				"never invent placeholders such as r??",
		)
	}
	return ref, nil
}

func normalizeA11yBatchActions(raw any) ([]a11yBatchAction, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, errors.New("actions must be an array")
	}
	if len(items) == 0 {
		return nil, errors.New(
			"actions must contain at least one action; use AX mode=\"observe\" for a read-only observation",
		)
	}
	if len(items) > a11yBatchMaxActions {
		return nil, fmt.Errorf(
			"actions accepts at most %d actions", a11yBatchMaxActions)
	}

	normalized := make([]a11yBatchAction, 0, len(items))
	totalText := 0
	for index, item := range items {
		action, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("actions[%d] must be an object", index)
		}
		tool, err := requiredA11yBatchString(action, "type", false)
		if err != nil {
			return nil, fmt.Errorf("actions[%d]: %w", index, err)
		}
		normalizedKey := ""
		switch tool {
		case "activate", "close", "click", "keypress", "scroll", "set_value", "type":
		default:
			return nil, fmt.Errorf(
				"actions[%d].type %q is unsupported; use activate, close, click, "+
					"keypress, scroll, set_value, or type",
				index, tool,
			)
		}
		allowed := a11yBatchActionFields(tool)
		for field := range action {
			if !allowed[field] {
				return nil, fmt.Errorf(
					"actions[%d] type %q does not accept field %q",
					index, tool, field,
				)
			}
		}
		elementIndex, err := actionTargetRef(action)
		if err != nil {
			return nil, fmt.Errorf("actions[%d].target: %w", index, err)
		}

		switch tool {
		case "activate", "close":
			if elementIndex == "" {
				return nil, fmt.Errorf(
					"actions[%d] %s requires target", index, tool)
			}
			if tool == "close" && index != len(items)-1 {
				return nil, newArgumentRuleError(argumentRuleCloseNotLast, fmt.Sprintf(
					"actions[%d] close must be the final action", index))
			}
			if !isShortAXWindowRef(elementIndex) {
				return nil, fmt.Errorf(
					"actions[%d] %s requires target.ref matching wN",
					index, tool,
				)
			}
		case "click":
			if elementIndex == "" {
				return nil, fmt.Errorf(
					"actions[%d] click requires target", index)
			}
			if !isShortAXRef(elementIndex) {
				return nil, fmt.Errorf(
					"actions[%d] click requires target.ref matching rN", index)
			}
		case "keypress":
			normalizedKey, err = normalizedA11yBatchKey(action)
			if err != nil {
				return nil, fmt.Errorf("actions[%d]: %w", index, err)
			}
		case "scroll":
			direction, err := requiredA11yBatchString(
				action, "direction", false,
			)
			if err != nil {
				return nil, fmt.Errorf("actions[%d]: %w", index, err)
			}
			switch direction {
			case "up", "down", "left", "right":
			default:
				return nil, fmt.Errorf(
					"actions[%d].direction must be up, down, left, or right",
					index,
				)
			}
			if err := validateA11yBatchNumber(action, "pages"); err != nil {
				return nil, fmt.Errorf("actions[%d]: %w", index, err)
			}
			pages := floatValue(optionalFloat(action, "pages"), 1)
			if pages <= 0 || pages > maxScrollPages {
				return nil, fmt.Errorf(
					"actions[%d].pages must be greater than 0 and at most %d",
					index, maxScrollPages,
				)
			}
		case "set_value":
			if elementIndex == "" {
				return nil, fmt.Errorf(
					"actions[%d] set_value requires target", index)
			}
			if !isShortAXRef(elementIndex) {
				return nil, fmt.Errorf(
					"actions[%d] set_value requires target.ref matching rN", index)
			}
			value, err := requiredA11yBatchString(action, "value", true)
			if err != nil {
				return nil, fmt.Errorf("actions[%d]: %w", index, err)
			}
			if len([]rune(value)) > maxActionText {
				return nil, fmt.Errorf(
					"actions[%d].value accepts at most %d characters",
					index, maxActionText,
				)
			}
			totalText += len([]rune(value))
		case "type":
			text, err := requiredA11yBatchString(action, "text", false)
			if err != nil {
				return nil, fmt.Errorf("actions[%d]: %w", index, err)
			}
			if len([]rune(text)) > maxActionText {
				return nil, fmt.Errorf(
					"actions[%d].text accepts at most %d characters",
					index, maxActionText,
				)
			}
			totalText += len([]rune(text))
		}
		if elementIndex != "" &&
			(tool == "keypress" || tool == "scroll" || tool == "type") &&
			!isShortAXRef(elementIndex) {
			return nil, fmt.Errorf(
				"actions[%d] %s target.ref must match rN", index, tool)
		}
		if totalText > a11yBatchMaxText {
			return nil, newArgumentRuleError(argumentRuleTextBudget, fmt.Sprintf(
				"the cumulative text/value budget is %d characters",
				a11yBatchMaxText,
			))
		}
		var postcondition map[string]any
		if rawPostcondition, present := action["postcondition"]; present {
			postcondition, ok = rawPostcondition.(map[string]any)
			if !ok {
				return nil, fmt.Errorf(
					"actions[%d].postcondition must be an AX assertion object",
					index,
				)
			}
		}

		normalized = append(normalized, a11yBatchAction{
			actionType:    tool,
			targetRef:     elementIndex,
			direction:     optionalString(action, "direction"),
			pages:         floatValue(optionalFloat(action, "pages"), 1),
			text:          optionalString(action, "text"),
			key:           normalizedKey,
			value:         optionalString(action, "value"),
			postcondition: postcondition,
		})
	}
	return normalized, nil
}

func (s *service) executeA11yBatchAction(
	context *a11yBatchContext,
	action a11yBatchAction,
	obs observation,
) toolCallResult {
	const noDeclaredTarget = ""
	switch action.actionType {
	case "activate", "close":
		return s.windowAction(
			context.appToken,
			action.targetRef,
			noDeclaredTarget,
			action.actionType,
			obs,
		).withEvidenceScope("delivery_or_local_ui")
	case "click":
		return s.click(
			context.appToken,
			action.targetRef,
			noDeclaredTarget,
			nil,
			nil,
			1,
			"left",
			"auto",
			obs,
		).withEvidenceScope("delivery_or_local_ui")
	case "keypress":
		return s.pressKey(
			context.appToken,
			action.targetRef,
			noDeclaredTarget,
			context.windowHandle,
			context.expectedWindow,
			action.key,
			obs,
		).withEvidenceScope("delivery_or_local_ui")
	case "scroll":
		return s.scroll(
			context.appToken,
			action.direction,
			action.targetRef,
			noDeclaredTarget,
			context.windowHandle,
			context.expectedWindow,
			action.pages,
			obs,
		).withEvidenceScope("delivery_or_local_ui")
	case "set_value":
		return s.setValue(
			context.appToken,
			action.targetRef,
			noDeclaredTarget,
			action.value,
			obs,
		).withEvidenceScope("delivery_or_local_ui")
	case "type":
		return s.typeText(
			context.appToken,
			action.targetRef,
			noDeclaredTarget,
			context.windowHandle,
			context.expectedWindow,
			action.text,
			obs,
		).withEvidenceScope("delivery_or_local_ui")
	default:
		return textResult(
			fmt.Sprintf("unsupported AX action %q", action.actionType),
			true,
		).withEvidenceScope("error_only")
	}
}

func prependA11yBatchSummary(
	result toolCallResult,
	summary string,
	facts map[string]any,
) toolCallResult {
	if strings.TrimSpace(summary) != "" {
		result.Content = append(
			[]contentItem{{Type: "text", Text: summary}},
			result.Content...,
		)
	}
	if result.StructuredContent == nil {
		result.StructuredContent = map[string]any{}
	}
	result.StructuredContent["a11y_batch_result"] = facts
	if actions, ok := facts["actions"].([]map[string]any); ok {
		emitted, partial, unknown := batchMutationSummary(actions)
		// A successful tail cannot erase uncertainty from an earlier action.
		for key, value := range map[string]bool{
			"input_events_emitted":   emitted,
			"partial_input_possible": partial,
			"mutation_unknown":       unknown,
		} {
			if value {
				result.StructuredContent[key] = true
			}
		}
	}
	return result
}

func compactAXActionContent(result toolCallResult) toolCallResult {
	routine := []string{
		"Invoked the element's AT-SPI accessibility action",
		"Synthesized ",
		"Resolved element",
		"Resolved semantic target",
		"The element MOVED",
	}
	for itemIndex := range result.Content {
		if result.Content[itemIndex].Type != "text" {
			continue
		}
		lines := strings.Split(result.Content[itemIndex].Text, "\n")
		kept := make([]string, 0, len(lines))
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "Note:") {
				drop := false
				for _, marker := range routine {
					if strings.Contains(trimmed, marker) {
						drop = true
						break
					}
				}
				if drop {
					continue
				}
			}
			if strings.HasPrefix(trimmed, `(observe="none":`) {
				kept = append(kept, "observe: none")
				continue
			}
			if strings.HasPrefix(trimmed, `(observe="screenshot":`) {
				kept = append(kept, "observe: screenshot")
				continue
			}
			kept = append(kept, line)
		}
		result.Content[itemIndex].Text = strings.TrimSpace(
			strings.Join(kept, "\n"),
		)
	}
	return result
}

func a11yBatchValidationError(message string, requested int) toolCallResult {
	result := textResult(
		"AX BATCH REJECTED BEFORE ACTION 1: "+message+
			"\naction_outcome=not_executed\n"+
			"actions_completed_without_call_error=0\n"+
			"No accessibility action was attempted.",
		true,
	)
	result.StructuredContent = map[string]any{
		"action_attempted":     false,
		"input_events_emitted": false,
		"a11y_batch_result": map[string]any{
			"actions_requested":                    requested,
			"actions_completed_without_call_error": 0,
			"failed_action_index":                  nil,
			"rejected_before_first_action":         true,
			"partial_sequence":                     false,
		},
	}
	return result
}

func a11yBatchPreflightError(err error, requested int) toolCallResult {
	return withArgumentRule(
		withAXLifecycleFailure(a11yBatchValidationError(err.Error(), requested), err),
		err,
	)
}

func withAXLifecycleFailure(result toolCallResult, err error) toolCallResult {
	var lifecycle *axRefLifecycleError
	if !errors.As(err, &lifecycle) {
		return result
	}
	code := ""
	switch lifecycle.kind {
	case "app":
		code = "stale_app_ref"
	case "window":
		code = "stale_window_ref"
	case "element":
		code = "stale_element_ref"
	default:
		return result
	}
	complete := lifecycle.lifecycle != AXRefNeedsRevalidation
	if !complete {
		code = "observation_incomplete"
	}
	if result.StructuredContent == nil {
		result.StructuredContent = map[string]any{}
	}
	result.StructuredContent["failure_code"] = code
	result.StructuredContent["failure_subject"] = lifecycle.kind
	result.StructuredContent["failure_phase"] = "precondition"
	result.StructuredContent["failure_facts_complete"] = complete
	result.StructuredContent["observation_required"] = true
	result.StructuredContent["final_observation_returned"] = false
	result.StructuredContent["partial_input_possible"] = false
	result.StructuredContent["input_events_emitted"] = false
	result.StructuredContent["retry_same_request"] = false
	return result
}

func (s *service) a11yBatch(
	rawActions any,
	obs observation,
	postcondition map[string]any,
	ownership ...string,
) toolCallResult {
	appRef := ""
	windowRef := ""
	if len(ownership) > 0 {
		appRef = ownership[0]
	}
	if len(ownership) > 1 {
		windowRef = ownership[1]
	}
	requested := 0
	if items, ok := rawActions.([]any); ok {
		requested = len(items)
	}
	items, ok := rawActions.([]any)
	if !ok {
		return a11yBatchValidationError(
			"actions must be an array", requested)
	}
	if len(items) == 0 {
		return a11yBatchValidationError(
			"mode=\"act\" requires at least one action; use mode=\"observe\" "+
				"for a read-only observation",
			0,
		)
	}
	actions, err := normalizeA11yBatchActions(rawActions)
	if err != nil {
		return a11yBatchPreflightError(err, requested)
	}
	if err := s.preflightPlatformAXKeys(actions); err != nil {
		return a11yBatchPreflightError(err, requested)
	}
	if s.platformBackend().HostInfo().Platform == "windows" {
		if err := preflightWindowsAXKeys(actions); err != nil {
			return a11yBatchPreflightError(err, requested)
		}
	}
	context, err := s.deriveA11yBatchContext(appRef, windowRef, actions)
	if err != nil {
		return a11yBatchPreflightError(err, requested)
	}
	if postcondition != nil {
		if err := s.preflightA11yPostcondition(context, postcondition); err != nil {
			return a11yBatchPreflightError(
				fmt.Errorf("postcondition: %w", err), requested,
			)
		}
	}
	for index, action := range actions {
		if action.postcondition == nil {
			continue
		}
		if err := s.preflightA11yPostcondition(
			context,
			action.postcondition,
		); err != nil {
			return a11yBatchPreflightError(
				fmt.Errorf(
					"actions[%d].postcondition: %w",
					index,
					err,
				),
				requested,
			)
		}
	}

	executionPlan, err := compileA11yBatchExecutionPlan(context, actions, obs, postcondition)
	if err != nil {
		return a11yBatchPreflightError(err, requested)
	}
	orderedRuns := map[int]ProviderOrderedInputRequest{}
	// The existing assertion path owns predicate identity and reconciliation.
	// Keep guarded batches there until it supports pre-input identity capture
	// across internal ordered-run snapshot commits.
	guarded := postcondition != nil
	for _, action := range actions {
		guarded = guarded || action.postcondition != nil
	}
	if _, available := orderedInputBackend(s.platformBackend()); available && !guarded {
		requests, err := providerOrderedInputRequestsFromPlan(context, executionPlan, actions)
		if err != nil {
			return a11yBatchPreflightError(err, requested)
		}
		if result, used := s.executeWholeBatchOrderedInput(context, actions, executionPlan, obs, postcondition); used {
			return result
		}
		for _, request := range requests {
			orderedRuns[request.Steps[0].ActionIndex] = request
		}
	}
	actionEvidence := make([]map[string]any, 0, len(actions))
	for index := 0; index < len(actions); index++ {
		action := actions[index]
		actionObservation := a11yBatchActionObservation(
			index,
			len(actions),
			obs,
			postcondition != nil || action.postcondition != nil,
		)
		windowAction := action.actionType == "activate" ||
			action.actionType == "close"
		result := toolCallResult{}
		actionEntry := map[string]any{}
		request, orderedRun := orderedRuns[index]
		if orderedRun {
			runEnd := index + len(request.Steps) - 1
			execution := s.executePlannedOrderedInputRun(context, request,
				runEnd == len(actions)-1 && obs.resolvedMode() == observeScreenshot)
			result = execution.result
			runEvidence := orderedInputActionEvidence(actions[index:runEnd+1], execution.nativeResult)
			actionEvidence = append(actionEvidence, runEvidence...)
			if result.IsError {
				return s.stopA11yOrderedInputRun(context, actions, index, runEnd,
					execution, actionEvidence, obs)
			}
			actionEntry = runEvidence[len(runEvidence)-1]
			index = runEnd
			action = actions[index]
			if index == len(actions)-1 && obs.resolvedMode() != observeNone {
				result = mergeA11yBatchFinalObservation(result,
					s.renderA11yBatchFinalObservation(execution.snapshot, execution.notes, obs))
			}
		} else {
			result = s.executeA11yBatchAction(context, action, actionObservation)
			if !result.IsError ||
				(action.actionType == "close" &&
					axEvidenceReportsInputEmission(result.StructuredContent)) {
				result = s.recoverClosedWindowActionObservation(
					context,
					action,
					result,
					obs,
				)
			}
			actionEntry = batchActionEvidenceEntry(index, action.actionType, result)
		}
		actionPostconditionResult := toolCallResult{}
		actionPostconditionChecked := false
		if result.IsError {
			actionEvidence = append(actionEvidence, actionEntry)
			anyInputEmitted, anyPartialPossible, anyMutationUnknown :=
				batchMutationSummary(
					actionEvidence,
				)
			facts := map[string]any{
				"actions_requested":                    len(actions),
				"actions_completed_without_call_error": index,
				"failed_action_index":                  index,
				"failed_action_number":                 index + 1,
				"failed_action_type":                   action.actionType,
				"rejected_before_first_action":         false,
				"partial_sequence":                     index > 0,
				"final_observation_returned":           false,
				"actions":                              actionEvidence,
				"any_input_emitted":                    anyInputEmitted,
				"partial_input_possible":               anyPartialPossible,
			}
			if postcondition != nil {
				facts["postcondition"] = map[string]any{
					"status": "not_checked",
				}
			}
			if result.StructuredContent == nil {
				result.StructuredContent = map[string]any{}
			}
			finalObservationReturned, _ :=
				result.StructuredContent["final_observation_returned"].(bool)
			preparationFailed := result.StructuredContent["focus_preparation"] != nil ||
				result.StructuredContent["pointer_preparation"] != nil
			if !finalObservationReturned && !preparationFailed {
				result.StructuredContent["observation_required"] = true
			}
			result.StructuredContent["partial_sequence_possible"] = index > 0
			result.StructuredContent["input_events_emitted"] = anyInputEmitted
			result.StructuredContent["partial_input_possible"] =
				anyPartialPossible
			result.StructuredContent["mutation_unknown"] =
				anyMutationUnknown || anyInputEmitted || anyPartialPossible
			result = s.recoverA11yBatchObservation(context, result, obs)
			finalObservationReturned, _ =
				result.StructuredContent["final_observation_returned"].(bool)
			facts["final_observation_returned"] = finalObservationReturned
			facts["observation_required"] =
				result.StructuredContent["observation_required"]
			observationSummary := "No final batch observation was returned; " +
				"observe fresh state before recovery."
			if preparationFailed {
				observationSummary = "Preparation may have changed state or may complete later. " +
					"Stop; no recovery observation, postcondition or later action was attempted."
			}
			if finalObservationReturned {
				observationSummary = "A fresh current-state observation follows. " +
					"It does not prove which action caused that state."
				if result.StructuredContent["action_outcome"] == "not_executed" {
					observationSummary = "A fresh independent current-state " +
						"observation follows. The failed action emitted no input " +
						"and did not cause that state."
				}
			}
			return prependA11yBatchSummary(
				result,
				fmt.Sprintf(
					"AX BATCH STOPPED AT ACTION %d/%d (%s). "+
						"%d earlier action call(s) returned without a call error. "+
						"This does not verify their delivery or task effect. "+
						"%s",
					index+1, len(actions), action.actionType, index,
					observationSummary,
				),
				facts,
			)
		}
		if action.postcondition != nil {
			actionPostconditionResult = s.runA11yPostcondition(
				context,
				action.postcondition,
				action.actionType == "close",
			)
			if a11yPostconditionNeedsCanonicalReconciliation(
				action.postcondition,
				actionPostconditionResult,
			) {
				actionPostconditionResult = s.reconcileA11yActionPostcondition(
					context,
					action.postcondition,
					actionPostconditionResult,
				)
			}
			actionPostconditionChecked = true
			actionEntry["postcondition"] = compactA11yPostconditionEvidence(
				action.postcondition,
				actionPostconditionResult,
			)
			actionEvidence = append(actionEvidence, actionEntry)
			if !a11yPostconditionPassed(actionPostconditionResult) {
				return s.stopA11yBatchForActionPostcondition(
					context,
					index,
					len(actions),
					action,
					actionEvidence,
					result,
					actionPostconditionResult,
					obs,
					postcondition != nil,
				)
			}
			result.StructuredContent["postcondition_verified"] = true
			preserveUnattributedPostconditionEvidence(
				result.StructuredContent,
				actionEntry,
			)
		} else if !orderedRun {
			actionEvidence = append(actionEvidence, actionEntry)
		}
		if index < len(actions)-1 {
			continuity := s.a11yBatchWindowContinuity(context)
			if continuity == a11yWindowSame {
				continue
			}
			anyInputEmitted, anyPartialPossible, anyMutationUnknown :=
				batchMutationSummary(
					actionEvidence,
				)
			failureCode := "stale_target"
			messageCode := "batch_window_changed"
			if continuity == a11yWindowUnknown {
				failureCode = "observation_incomplete"
				messageCode = "batch_window_unknown"
			}
			stopped := textResult(
				fmt.Sprintf(
					"%s: action %d/%d completed, but the logical window is %s "+
						"before action %d. The remaining actions were not attempted; "+
						"observe current state first.",
					messageCode, index+1, len(actions), continuity, index+2,
				),
				true,
			)
			stopped.StructuredContent = map[string]any{
				"failure_code":                         failureCode,
				"failure_phase":                        "between_actions",
				"window_continuity":                    string(continuity),
				"actions_requested":                    len(actions),
				"actions_completed_without_call_error": index + 1,
				"stopped_before_action_index":          index + 1,
				"rejected_before_first_action":         false,
				"partial_sequence":                     true,
				"final_observation_returned":           false,
				"actions":                              actionEvidence,
				"any_input_emitted":                    anyInputEmitted,
				"input_events_emitted":                 anyInputEmitted,
				"partial_input_possible":               anyPartialPossible,
				"mutation_unknown":                     anyMutationUnknown || anyInputEmitted || anyPartialPossible,
				"observation_required":                 true,
			}
			if postcondition != nil {
				stopped.StructuredContent["postcondition"] = map[string]any{
					"status": "not_checked",
				}
			}
			return s.recoverA11yBatchObservation(context, stopped, obs)
		}
		if index == len(actions)-1 {
			result = compactAXActionContent(result)
			postconditionResult := toolCallResult{}
			if postcondition != nil {
				postconditionResult = s.runA11yPostcondition(
					context,
					postcondition,
					action.actionType == "close",
				)
			}
			finalObservation := toolCallResult{}
			captureFinalAfterPostcondition :=
				(postcondition != nil || actionPostconditionChecked) &&
					obs.resolvedMode() != observeNone
			if captureFinalAfterPostcondition {
				observationProof := postconditionResult
				if postcondition == nil {
					observationProof = actionPostconditionResult
				}
				postconditionVerified :=
					a11yPostconditionPassed(observationProof)
				if action.actionType == "close" && postconditionVerified {
					finalObservation = closedWindowFinalObservation(
						context,
						obs,
					)
				} else if observationProof.axProofSnapshot != nil &&
					obs.resolvedMode() == observeDiff {
					finalObservation = s.renderA11yBatchFinalObservation(
						observationProof.axProofSnapshot,
						observationProof.axProofNotes,
						obs,
					)
					if finalObservation.StructuredContent == nil {
						finalObservation.StructuredContent = map[string]any{}
					}
					finalObservation.StructuredContent["final_observation_reused_postcondition_snapshot"] = true
				} else {
					finalObservation = s.a11yBatchFinalObservation(
						context,
						obs,
						action.actionType == "close",
					)
				}
				if postcondition != nil {
					postconditionResult = s.reconcileA11yPostconditionFromFinalObservation(
						postcondition,
						postconditionResult,
						finalObservation,
					)
				}
			}
			if postcondition != nil {
				result = mergeA11yPostconditionResult(
					result,
					postconditionResult,
				)
				if a11yPostconditionPassed(postconditionResult) &&
					len(actionEvidence) > 0 {
					preserveUnattributedPostconditionEvidence(
						result.StructuredContent,
						actionEvidence[len(actionEvidence)-1],
					)
				}
			}
			if captureFinalAfterPostcondition {
				result = mergeA11yBatchFinalObservation(
					result,
					finalObservation,
				)
				if action.actionType == "close" &&
					result.StructuredContent["target_window_absent"] == true &&
					len(actionEvidence) > 0 {
					clearVerifiedCloseUncertainty(
						result.StructuredContent,
						actionEvidence[len(actionEvidence)-1],
					)
				}
			}
			if obs.resolvedMode() != observeNone && !windowAction {
				if required, _ :=
					result.StructuredContent["observation_required"].(bool); required {
					result = s.recoverA11yBatchObservation(
						context,
						result,
						obs,
					)
				}
			}
			finalObservationReturned := obs.resolvedMode() != observeNone
			if value, ok := result.StructuredContent["final_observation_returned"].(bool); ok {
				finalObservationReturned = value
			}
			observationRequired := !finalObservationReturned &&
				obs.resolvedMode() != observeNone
			if value, present :=
				result.StructuredContent["observation_required"].(bool); present {
				observationRequired = value
			} else {
				result.StructuredContent["observation_required"] =
					observationRequired
			}
			nextIdenticalObservationRequired := observationRequired
			if value, present :=
				result.StructuredContent["next_identical_observation_required"].(bool); present {
				nextIdenticalObservationRequired = value
			} else {
				result.StructuredContent["next_identical_observation_required"] =
					nextIdenticalObservationRequired
			}
			facts := map[string]any{
				"actions_requested":                    len(actions),
				"actions_completed_without_call_error": len(actions),
				"failed_action_index":                  nil,
				"rejected_before_first_action":         false,
				"partial_sequence":                     false,
				"final_observation_returned":           finalObservationReturned,
				"observation_required":                 observationRequired,
				"next_identical_observation_required":  nextIdenticalObservationRequired,
				"actions":                              actionEvidence,
			}
			if postcondition != nil || actionPostconditionChecked {
				facts["final_observation_phase"] = "after_postcondition"
			}
			if postcondition != nil {
				facts["postcondition"] = compactA11yPostconditionEvidence(
					postcondition,
					postconditionResult,
				)
			} else {
				facts["final_observation_action_index"] = index
			}
			return prependA11yBatchSummary(
				result,
				"",
				facts,
			)
		}
	}
	panic("normalizeA11yBatchActions returned an empty sequence")
}

func batchActionEvidenceEntry(
	index int,
	actionType string,
	result toolCallResult,
) map[string]any {
	entry := map[string]any{
		"action_index":  index,
		"action_number": index + 1,
		"action_type":   actionType,
		"call_error":    result.IsError,
	}
	if result.StructuredContent != nil {
		if evidence, present := result.StructuredContent["actionability"]; present {
			entry["actionability"] = compactBatchActionability(evidence)
		}
		for _, key := range []string{
			"failure_code",
			"failure_phase",
			"focus_preparation",
			"pointer_preparation",
			"input_events_emitted",
			"partial_input_possible",
			"mutation_unknown",
		} {
			if value, present := result.StructuredContent[key]; present {
				entry[key] = value
			}
		}
	}
	return entry
}

func batchMutationSummary(actions []map[string]any) (bool, bool, bool) {
	emitted := false
	partial := false
	unknown := false
	for _, action := range actions {
		if value, ok := action["input_events_emitted"].(bool); ok && value {
			emitted = true
		}
		if value, ok := action["partial_input_possible"].(bool); ok && value {
			partial = true
		}
		if value, ok := action["mutation_unknown"].(bool); ok && value {
			unknown = true
		}
		actionability, _ := action["actionability"].(map[string]any)
		if actionability == nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(
			fmt.Sprint(actionability["input_emitted"]),
		)) {
		case "true":
			emitted = true
		case "unknown":
			partial = true
			unknown = true
		}
	}
	return emitted, partial, unknown
}

func compactBatchActionability(value any) any {
	evidence, ok := value.(ActionabilityEvidence)
	if !ok {
		if pointer, pointerOK := value.(*ActionabilityEvidence); pointerOK &&
			pointer != nil {
			evidence = *pointer
			ok = true
		}
	}
	if !ok {
		return value
	}
	return map[string]any{
		"resolution":             evidence.Resolution,
		"window_verified":        evidence.WindowVerified,
		"route":                  evidence.Route,
		"stable":                 evidence.Stable,
		"focus_verified":         evidence.FocusVerified,
		"editable_verified":      evidence.EditableVerified,
		"input_emitted":          evidence.InputEmitted,
		"delivery_verified":      evidence.DeliveryVerified,
		"readback_sampled":       evidence.ReadbackSampled,
		"postcondition_verified": evidence.PostconditionVerified,
	}
}
