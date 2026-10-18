package main

import (
	"errors"
	"fmt"
	"strconv"
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
	title         string
	postcondition map[string]any
}

type a11yBatchContext struct {
	appRef         string
	windowRef      string
	appToken       string
	windowHandle   string
	expectedWindow *ProviderWindowExpectation
	// boundByFocus marks a batch bound to the window that held keyboard
	// focus before input; its keys and text must still reach that window.
	boundByFocus bool
}

type a11yWindowContinuity string

const (
	a11yWindowSame    a11yWindowContinuity = "same"
	a11yWindowChanged a11yWindowContinuity = "changed"
	a11yWindowUnknown a11yWindowContinuity = "unknown"
)

// deriveA11yBatchContext binds a batch to one logical window. The window
// comes from the actions' rN/wN refs, from window_ref, or, for a batch whose
// actions carry no ref, from the window that holds keyboard focus. A
// window_ref that names the same window as the refs is accepted; a
// different one is rejected. focus is the focused window read before any
// input; it is used only when nothing else names a window.
func (s *service) deriveA11yBatchContext(
	explicitWindowRef string,
	actions []a11yBatchAction,
	focus *axDesktopWindow,
) (*a11yBatchContext, error) {
	context := &a11yBatchContext{}
	bindWindow := func(ref, handle, appRef, appIdentity string) error {
		ref = strings.TrimSpace(ref)
		handle = strings.TrimSpace(handle)
		if context.windowHandle != "" && context.windowHandle != handle {
			return fmt.Errorf(
				"batch spans two windows (%s and %s); one act call works in one window, so split it",
				context.windowRef, ref,
			)
		}
		if context.windowRef == "" {
			context.windowRef = ref
			context.windowHandle = handle
		}
		if appRef != "" {
			context.appRef = appRef
			context.appToken = appRef
		} else if context.appToken == "" {
			context.appToken = appIdentity
		}
		return nil
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
	for index, action := range actions {
		address := strings.TrimSpace(action.targetRef)
		if address == "" {
			continue
		}
		hasTarget = true
		var err error
		if isShortAXRef(address) {
			var entry *AXRefEntry
			entry, err = s.resolveShortAXRefOwned(address)
			if err == nil {
				err = bindWindow(entry.LogicalWindowRef, entry.WindowHandle,
					entry.AppRef, entry.AppIdentity)
				if err != nil && explicitWindowRef != "" && index == 0 {
					err = fmt.Errorf("%s belongs to window %s, not window_ref %s; omit window_ref",
						address, entry.LogicalWindowRef, explicitWindowRef)
				}
			}
		} else {
			var entry *AXWindowRefEntry
			entry, err = s.resolveShortAXWindowRefOwned(address)
			if err == nil {
				err = bindWindow(entry.Ref, entry.WindowHandle, entry.AppRef, entry.AppIdentity)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("actions[%d].ref: %w", index, err)
		}
	}
	if context.windowHandle == "" && !hasTarget && focus != nil {
		entry, err := s.resolveShortAXWindowRefOwned(focus.ref)
		if err != nil {
			return nil, err
		}
		if err := bindWindow(entry.Ref, entry.WindowHandle, entry.AppRef, entry.AppIdentity); err != nil {
			return nil, err
		}
		context.boundByFocus = true
	}
	if context.windowHandle == "" {
		return nil, errors.New("the batch has no window: give an rN or wN ref, a title, or window_ref")
	}
	if context.appToken == "" {
		return nil, errors.New("the batch window has no current application owner; observe it again")
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
		message+"\nNo action was sent.",
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
		if strings.Contains(err.Error(), "was never issued in this session") {
			// A ref this session never returned: copy refs from results.
			if result.StructuredContent == nil {
				result.StructuredContent = map[string]any{}
			}
			result.StructuredContent["failure_code"] = "unknown_ref"
			result.StructuredContent[axPublicErrorHintKey] = `{"mode":"find","window_ref":"wN","query":{"name_contains":"..."}}`
		}
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

// a11yBatch decodes public action objects and runs them. Tests and the
// public act path share it.
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

// a11yBatchDecoded runs decoded actions in one window. Window titles and
// the focused window are resolved here, once, before any input; after that
// the batch uses only the resolved wN.
func (s *service) a11yBatchDecoded(
	actions []a11yBatchAction,
	obs observation,
	postcondition map[string]any,
	windowRef string,
) toolCallResult {
	requested := len(actions)
	bindingNotes := []string{}
	for index := range actions {
		if actions[index].title == "" {
			continue
		}
		window, argErr := s.resolveWindowTitle(actions[index].title)
		if argErr != nil {
			argErr.path = fmt.Sprintf("actions[%d].title", index)
			return axArgErrorResult(argErr, requested)
		}
		actions[index].targetRef = window.ref
		bindingNotes = append(bindingNotes, fmt.Sprintf(
			"title %s -> %s %s (%s)", strconv.Quote(actions[index].title),
			window.ref, strconv.Quote(window.title), window.appName))
	}
	var focus *axDesktopWindow
	if windowRef == "" && !a11yBatchHasRef(actions) {
		var argErr *axArgError
		focus, argErr = s.resolveFocusedWindow()
		if argErr != nil {
			return axArgErrorResult(argErr, requested)
		}
		bindingNotes = append(bindingNotes, fmt.Sprintf(
			"bound_window: %s %s (%s, focused at preflight)",
			focus.ref, strconv.Quote(focus.title), focus.appName))
	}
	result := s.a11yBatchBound(actions, obs, postcondition, windowRef, focus, requested)
	if len(bindingNotes) > 0 {
		result.Content = append(
			[]contentItem{{Type: "text", Text: strings.Join(bindingNotes, "\n")}},
			result.Content...,
		)
	}
	return result
}

func a11yBatchHasRef(actions []a11yBatchAction) bool {
	for _, action := range actions {
		if action.targetRef != "" {
			return true
		}
	}
	return false
}

func (s *service) a11yBatchBound(
	actions []a11yBatchAction,
	obs observation,
	postcondition map[string]any,
	windowRef string,
	focus *axDesktopWindow,
	requested int,
) toolCallResult {
	if err := s.preflightPlatformAXKeys(actions); err != nil {
		return a11yBatchPreflightError(err, requested)
	}
	if s.platformBackend().HostInfo().Platform == "windows" {
		if err := preflightWindowsAXKeys(actions); err != nil {
			return a11yBatchPreflightError(err, requested)
		}
	}
	context, err := s.deriveA11yBatchContext(windowRef, actions, focus)
	if err != nil {
		return a11yBatchPreflightError(err, requested)
	}
	if postcondition != nil {
		if err := s.preflightA11yPostcondition(context, postcondition); err != nil {
			return a11yBatchPreflightError(
				fmt.Errorf("until: %w", err), requested,
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
					"actions[%d].until: %w",
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
	guarded := postcondition != nil || context.boundByFocus
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
		if context.boundByFocus {
			// Keys and text without a ref go to the focused window. If focus
			// left the bound window, stop instead of sending input elsewhere.
			if stopped, changed := s.a11yBatchFocusChanged(context, index, len(actions), actionEvidence); changed {
				return s.recoverA11yBatchObservation(context, stopped, obs)
			}
		}
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
					"Stop; no recovery observation, until or later action was attempted."
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
					"Stopped at action %d/%d (%s). "+
						"%d earlier action(s) returned without error; "+
						"that does not verify their effect. %s",
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
				recordUntilWindowRef(
					result.StructuredContent,
					postconditionResult,
					finalObservation,
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

// a11yBatchFocusChanged reports whether keyboard focus left the window a
// focus-bound batch was bound to, and if so returns the stop result.
func (s *service) a11yBatchFocusChanged(
	context *a11yBatchContext,
	index, count int,
	actionEvidence []map[string]any,
) (toolCallResult, bool) {
	focus, argErr := s.resolveFocusedWindow()
	if argErr == nil && focus.handle == context.windowHandle {
		return toolCallResult{}, false
	}
	now := "no window has focus"
	if argErr == nil {
		now = fmt.Sprintf("focus is now on %s %s (%s)", focus.ref, strconv.Quote(focus.title), focus.appName)
	} else if argErr.code == axCodeRuntimeError {
		now = "the focused window could not be read"
	}
	anyInputEmitted, anyPartialPossible, anyMutationUnknown := batchMutationSummary(actionEvidence)
	message := fmt.Sprintf(
		"keyboard focus left the bound window %s before action %d/%d; %s. "+
			"%d action(s) ran; the rest were not sent.",
		context.windowRef, index+1, count, now, index,
	)
	stopped := textResult(message, true)
	stopped.StructuredContent = map[string]any{
		axPublicErrorCodeKey:     axCodeWindowChanged,
		axPublicErrorDetailKey:   message,
		axPublicErrorHintKey:     `{"mode":"act","actions":[{"type":"activate","ref":"` + context.windowRef + `"}]}`,
		"failure_code":           "stale_target",
		"failure_phase":          "between_actions",
		"input_events_emitted":   anyInputEmitted,
		"partial_input_possible": anyPartialPossible,
		"mutation_unknown":       anyMutationUnknown || anyInputEmitted,
		"observation_required":   true,
		"a11y_batch_result": map[string]any{
			"actions_requested":                    count,
			"actions_completed_without_call_error": index,
			"stopped_before_action_index":          index,
			"rejected_before_first_action":         index == 0,
			"partial_sequence":                     index > 0,
			"actions":                              actionEvidence,
		},
	}
	return stopped, true
}
