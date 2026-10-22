package core

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hqhq1025/hybrid-computer-use-oss/computer-use/internal/contract"
)

type axBatchAction struct {
	actionType string
	targetRef  string
	// toRef is the range end of a select action.
	toRef     string
	direction string
	pages     float64
	text      string
	key       string
	value     string
	title     string
	// wait is set for a wait step, which sends no input.
	wait *axWait
}

type AXBatchContext struct {
	AppRef         string `json:"-"`
	WindowRef      string `json:"-"`
	AppToken       string `json:"-"`
	WindowHandle   string `json:"-"`
	expectedWindow *ProviderWindowExpectation
	// boundByFocus marks a batch bound to the window that held keyboard
	// focus before input; its keys and text must still reach that window.
	boundByFocus bool
	// report records the snapshots the batch passes through for the act
	// report; nil outside act.
	report *actReportTrace
}

type axWindowContinuity string

const (
	axWindowSame    axWindowContinuity = "same"
	axWindowChanged axWindowContinuity = "changed"
	axWindowUnknown axWindowContinuity = "unknown"
)

// deriveAXBatchContext binds a batch to one logical window. The window
// comes from the actions' rN/wN refs, from window_ref, or, for a batch whose
// actions carry no ref, from the window that holds keyboard focus. A
// window_ref that names the same window as the refs is accepted; a
// different one is rejected. focus is the focused window read before any
// input; it is used only when nothing else names a window.
func (s *Service) deriveAXBatchContext(
	explicitWindowRef string,
	actions []axBatchAction,
	focus *axDesktopWindow,
) (*AXBatchContext, error) {
	context := &AXBatchContext{}
	bindWindow := func(ref, handle, appRef, appIdentity string) error {
		ref = strings.TrimSpace(ref)
		handle = strings.TrimSpace(handle)
		if context.WindowHandle != "" && context.WindowHandle != handle {
			return axInvalid("", fmt.Sprintf(
				"batch spans two windows (%s and %s); one act call works in one window, so split it",
				context.WindowRef, ref),
				"Split the actions into separate act calls, one window per call.",
			)
		}
		if context.WindowRef == "" {
			context.WindowRef = ref
			context.WindowHandle = handle
		}
		if appRef != "" {
			context.AppRef = appRef
			context.AppToken = appRef
		} else if context.AppToken == "" {
			context.AppToken = appIdentity
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
					err = axInvalid("window_ref", fmt.Sprintf("%s belongs to window %s, not window_ref %s; omit window_ref",
						address, entry.LogicalWindowRef, explicitWindowRef),
						"Omit window_ref so the action refs determine the window, or use refs from window_ref.")
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
			var argument *axArgError
			if errors.As(err, &argument) && argument.path == "" {
				argument.path = fmt.Sprintf("actions[%d].ref", index)
			}
			return nil, fmt.Errorf("actions[%d].ref: %w", index, err)
		}
	}
	if context.WindowHandle == "" && !hasTarget && focus != nil {
		entry, err := s.resolveShortAXWindowRefOwned(focus.ref)
		if err != nil {
			return nil, err
		}
		if err := bindWindow(entry.Ref, entry.WindowHandle, entry.AppRef, entry.AppIdentity); err != nil {
			return nil, err
		}
		context.boundByFocus = true
	}
	if context.WindowHandle == "" {
		return nil, errors.New("the batch has no window: give an rN or wN ref, a title, or window_ref")
	}
	if context.AppToken == "" {
		return nil, errors.New("the batch window has no current application owner; observe it again")
	}
	if entry, err := s.WindowRefRegistry.Resolve(context.WindowRef, ""); err == nil {
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

func (s *Service) axBatchWindowContinuity(
	context *AXBatchContext,
) axWindowContinuity {
	if s == nil || context == nil || strings.TrimSpace(context.WindowHandle) == "" {
		return axWindowUnknown
	}
	snapshot := s.currentSnapshot(context.AppToken)
	if snapshot == nil {
		return axWindowUnknown
	}
	if snapshot.WindowAbsent {
		return axWindowChanged
	}
	if strings.TrimSpace(snapshot.WindowHandle) == "" ||
		snapshot.WindowHandle != context.WindowHandle {
		if strings.TrimSpace(snapshot.WindowHandle) == "" {
			return axWindowUnknown
		}
		return axWindowChanged
	}
	if strings.TrimSpace(context.WindowRef) == "" ||
		strings.TrimSpace(snapshot.publicWindowRef) == "" {
		return axWindowUnknown
	}
	windowEntry, err := s.WindowRefRegistry.Resolve(context.WindowRef, "")
	if err != nil {
		var lifecycle *axRefLifecycleError
		if errors.As(err, &lifecycle) && lifecycle.lifecycle == AXRefNeedsRevalidation {
			return axWindowUnknown
		}
		return axWindowChanged
	}
	if windowEntry.WindowHandle != context.WindowHandle ||
		snapshot.publicWindowRef != context.WindowRef {
		return axWindowChanged
	}
	return axWindowSame
}

func (s *Service) executeAXBatchAction(
	context *AXBatchContext,
	action axBatchAction,
	obs observation,
) ToolCallResult {
	const noDeclaredTarget = ""
	switch action.actionType {
	case "activate", "close":
		return s.windowAction(
			context.AppToken,
			action.targetRef,
			noDeclaredTarget,
			action.actionType,
			obs,
		).withEvidenceScope("delivery_or_local_ui")
	case "click":
		return s.click(
			context.AppToken,
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
			context.AppToken,
			action.targetRef,
			noDeclaredTarget,
			context.WindowHandle,
			context.expectedWindow,
			action.key,
			obs,
		).withEvidenceScope("delivery_or_local_ui")
	case "scroll":
		return s.scroll(
			context.AppToken,
			action.direction,
			action.targetRef,
			noDeclaredTarget,
			context.WindowHandle,
			context.expectedWindow,
			action.pages,
			obs,
		).withEvidenceScope("delivery_or_local_ui")
	case "set_value":
		return s.setValue(
			context.AppToken,
			action.targetRef,
			noDeclaredTarget,
			action.value,
			obs,
		).withEvidenceScope("delivery_or_local_ui")
	case "select":
		return s.selectRange(
			context.AppToken,
			action.targetRef,
			action.toRef,
			action.text,
			obs,
		).withEvidenceScope("delivery_or_local_ui")
	case "type":
		return s.typeText(
			context.AppToken,
			action.targetRef,
			noDeclaredTarget,
			context.WindowHandle,
			context.expectedWindow,
			action.text,
			obs,
		).withEvidenceScope("delivery_or_local_ui")
	default:
		return TextResult(
			fmt.Sprintf("unsupported AX action %q", action.actionType),
			true,
		).withEvidenceScope("error_only")
	}
}

func prependAXBatchSummary(
	result ToolCallResult,
	summary string,
	facts map[string]any,
) ToolCallResult {
	if strings.TrimSpace(summary) != "" {
		result.Content = append(
			[]ContentItem{{Type: "text", Text: summary}},
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

func compactAXActionContent(result ToolCallResult) ToolCallResult {
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

func axBatchValidationError(message string, requested int) ToolCallResult {
	result := TextResult(
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

func axBatchPreflightError(err error, requested int) ToolCallResult {
	var argument *axArgError
	if errors.As(err, &argument) {
		return axArgErrorResult(argument, requested)
	}
	return withArgumentRule(
		withAXLifecycleFailure(axBatchValidationError(err.Error(), requested), err),
		err,
	)
}

func withAXLifecycleFailure(result ToolCallResult, err error) ToolCallResult {
	var lifecycle *axRefLifecycleError
	if !errors.As(err, &lifecycle) {
		if strings.Contains(err.Error(), "was never issued in this session") {
			// A ref this session never returned: copy refs from results.
			if result.StructuredContent == nil {
				result.StructuredContent = map[string]any{}
			}
			result.StructuredContent["failure_code"] = "unknown_ref"
			result.StructuredContent[contract.AXPublicErrorHintKey] = `{"mode":"find","window_ref":"wN","query":{"name_contains":"..."}}`
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

// axBatchDecoded runs decoded actions in one window. Window titles and
// the focused window are resolved here, once, before any input; after that
// the batch uses only the resolved wN.
func (s *Service) axBatchDecoded(
	actions []axBatchAction,
	obs observation,
	windowRef string,
) ToolCallResult {
	requested := len(actions)
	if axBatchOnlyWaits(actions) {
		return s.axWaitOnly(actions, obs, windowRef)
	}
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
	if windowRef == "" && !axBatchHasRef(actions) {
		var argErr *axArgError
		focus, argErr = s.resolveFocusedWindow()
		if argErr != nil {
			return axArgErrorResult(argErr, requested)
		}
		bindingNotes = append(bindingNotes, fmt.Sprintf(
			"bound_window: %s %s (%s, focused at preflight)",
			focus.ref, strconv.Quote(focus.title), focus.appName))
	}
	result := s.axBatchBound(actions, obs, windowRef, focus, requested)
	if len(bindingNotes) > 0 {
		at := 0
		if len(result.Content) > 0 && isActReportText(result.Content[0].Text) {
			at = 1
		}
		notes := ContentItem{Type: "text", Text: strings.Join(bindingNotes, "\n")}
		result.Content = append(result.Content[:at:at],
			append([]ContentItem{notes}, result.Content[at:]...)...)
	}
	return result
}

// axBatchHasInputRef reports whether an input action names a ref.
func axBatchHasInputRef(actions []axBatchAction) bool {
	for _, action := range actions {
		if action.actionType != "wait" && action.targetRef != "" {
			return true
		}
	}
	return false
}

func axBatchHasRef(actions []axBatchAction) bool {
	for _, action := range actions {
		if action.targetRef != "" {
			return true
		}
	}
	return false
}

// axBatchBound runs the batch and puts the act report first.
func (s *Service) axBatchBound(
	actions []axBatchAction,
	obs observation,
	windowRef string,
	focus *axDesktopWindow,
	requested int,
) ToolCallResult {
	trace := newActReportTrace(obs)
	result := s.axBatchRun(actions, obs, windowRef, focus, requested, trace)
	return s.withActReport(result, actions, trace)
}

func (s *Service) axBatchRun(
	actions []axBatchAction,
	obs observation,
	windowRef string,
	focus *axDesktopWindow,
	requested int,
	trace *actReportTrace,
) ToolCallResult {
	if err := s.preflightPlatformAXKeys(actions); err != nil {
		return axBatchPreflightError(err, requested)
	}
	if s.platformBackend().HostInfo().Platform == "windows" {
		if err := preflightWindowsAXKeys(actions); err != nil {
			return axBatchPreflightError(err, requested)
		}
	}
	context, err := s.deriveAXBatchContext(windowRef, actions, focus)
	if err != nil {
		return axBatchPreflightError(err, requested)
	}
	context.report = trace
	trace.bind(s, context)
	lastInput := -1
	for index, action := range actions {
		if action.actionType != "wait" {
			lastInput = index
			continue
		}
		if action.wait.ref == "" {
			continue
		}
		if err := s.preflightAXWaitRef(context, action.wait.ref); err != nil {
			return axBatchPreflightError(fmt.Errorf("actions[%d].ref: %w", index, err), requested)
		}
	}

	executionPlan, err := compileAXBatchExecutionPlan(context, actions, obs)
	if err != nil {
		return axBatchPreflightError(err, requested)
	}
	orderedRuns := map[int]ProviderOrderedInputRequest{}
	// Wait steps poll between actions on the per-action path; the ordered
	// provider path runs keys back to back.
	guarded := context.boundByFocus || lastInput < len(actions)-1
	for _, action := range actions {
		guarded = guarded || action.actionType == "wait"
	}
	if _, available := orderedInputBackend(s.platformBackend()); available && !guarded {
		requests, err := providerOrderedInputRequestsFromPlan(context, executionPlan, actions)
		if err != nil {
			return axBatchPreflightError(err, requested)
		}
		if result, used := s.executeWholeBatchOrderedInput(context, actions, executionPlan, obs); used {
			return result
		}
		for _, request := range requests {
			orderedRuns[request.Steps[0].ActionIndex] = request
		}
	}
	actionEvidence := make([]map[string]any, 0, len(actions))
	waitNotes := []string{}
	for index := 0; index < len(actions); index++ {
		action := actions[index]
		if action.actionType == "wait" {
			// A wait step between actions: it sends no input.
			trace.recordBefore(s, index)
			waitResult := s.runAXWait(context, action.wait)
			actionEvidence = append(actionEvidence, axWaitEvidenceEntry(index, waitResult))
			if !axPostconditionPassed(waitResult) {
				return s.stopAXBatchAtWait(context, actions, index, waitResult, actionEvidence, waitNotes, obs)
			}
			waitNotes = append(waitNotes, firstLine(ResultText(waitResult)))
			s.rebindAfterWindowWait(context, action.wait, waitResult)
			if index < len(actions)-1 && actions[index+1].actionType != "wait" {
				if stopped, changed := s.axBatchContinuityStop(context, actions, index, actionEvidence, obs); changed {
					return stopped
				}
			}
			continue
		}
		actionObservation := axBatchActionObservation(
			index,
			len(actions),
			obs,
			index < len(actions)-1 && index == lastInput,
		)
		actionObservation.lightReadback = axBatchLightReadback(actions, index, lastInput)
		windowAction := action.actionType == "activate" ||
			action.actionType == "close"
		if context.boundByFocus {
			// Keys and text without a ref go to the focused window. If focus
			// left the bound window, stop instead of sending input elsewhere.
			if stopped, changed := s.axBatchFocusChanged(context, index, len(actions), actionEvidence); changed {
				return s.recoverAXBatchObservation(context, stopped, obs)
			}
		}
		trace.recordBefore(s, index)
		result := ToolCallResult{}
		actionEntry := map[string]any{}
		request, orderedRun := orderedRuns[index]
		if orderedRun {
			runEnd := index + len(request.Steps) - 1
			execution := s.executePlannedOrderedInputRun(context, request,
				runEnd == len(actions)-1 && *obs.resolvedScreenshot())
			result = execution.result
			runEvidence := orderedInputActionEvidence(actions[index:runEnd+1], execution.nativeResult)
			actionEvidence = append(actionEvidence, runEvidence...)
			if result.IsError {
				return s.stopAXOrderedInputRun(context, actions, index, runEnd,
					execution, actionEvidence, obs)
			}
			actionEntry = runEvidence[len(runEvidence)-1]
			index = runEnd
			action = actions[index]
			if index == len(actions)-1 && obs.resolvedMode() != contract.ObserveNone {
				result = mergeAXBatchFinalObservation(result,
					s.renderAXBatchFinalObservation(execution.snapshot, execution.notes, obs))
			}
		} else {
			result = s.executeAXBatchAction(context, action, actionObservation)
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
		if result.IsError && index+1 < len(actions) && actions[index+1].actionType == "wait" &&
			axReadbackOnlyFailure(result) {
			// The input was sent and only the read after it failed, for
			// example because the window closed or the application exited.
			// The wait that follows is the check the caller asked for, so it
			// decides instead of this read.
			result.IsError = false
			actionEntry["call_error"] = false
		}
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
			result = s.recoverAXBatchObservation(context, result, obs)
			finalObservationReturned, _ =
				result.StructuredContent["final_observation_returned"].(bool)
			facts["final_observation_returned"] = finalObservationReturned
			facts["observation_required"] =
				result.StructuredContent["observation_required"]
			observationSummary := "No final batch observation was returned; " +
				"observe fresh state before recovery."
			if preparationFailed {
				observationSummary = "Preparation may have changed state or may complete later. " +
					"Stop; no recovery observation, wait or later action was attempted."
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
			return prependAXBatchSummary(
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
		if !orderedRun {
			actionEvidence = append(actionEvidence, actionEntry)
		}
		if index < lastInput && actions[index+1].actionType != "wait" {
			if stopped, changed := s.axBatchContinuityStop(context, actions, index, actionEvidence, obs); changed {
				return stopped
			}
			continue
		}
		if index < lastInput {
			continue
		}
		if index == lastInput {
			result = compactAXActionContent(result)
			// Waits after the last action run before the final observation,
			// which then shows the state they waited for.
			for next := index + 1; next < len(actions); next++ {
				trace.recordBefore(s, next)
				waitResult := s.runAXWait(context, actions[next].wait)
				actionEvidence = append(actionEvidence, axWaitEvidenceEntry(next, waitResult))
				if !axPostconditionPassed(waitResult) {
					return s.stopAXBatchAtWait(context, actions, next, waitResult, actionEvidence, waitNotes, obs)
				}
				waitNotes = append(waitNotes, firstLine(ResultText(waitResult)))
				s.rebindAfterWindowWait(context, actions[next].wait, waitResult)
			}
			if index < len(actions)-1 && obs.resolvedMode() != contract.ObserveNone &&
				action.actionType != "close" {
				result = mergeAXBatchFinalObservation(result,
					s.axBatchFinalObservation(context, obs, true))
			}
			if index < len(actions)-1 {
				// The waits after the last input held: the awaited state
				// was read live, which answers a read that failed after the
				// input (for example, the window closed or the app exited).
				result.StructuredContent["wait_verified"] = true
				axClearReadbackFailure(result.StructuredContent)
			}
			if len(waitNotes) > 0 {
				result.Content = append(
					[]ContentItem{{Type: "text", Text: strings.Join(waitNotes, "\n")}},
					result.Content...,
				)
			}
			if obs.resolvedMode() != contract.ObserveNone && !windowAction {
				if required, _ :=
					result.StructuredContent["observation_required"].(bool); required {
					result = s.recoverAXBatchObservation(
						context,
						result,
						obs,
					)
				}
			}
			finalObservationReturned := obs.resolvedMode() != contract.ObserveNone
			if value, ok := result.StructuredContent["final_observation_returned"].(bool); ok {
				finalObservationReturned = value
			}
			observationRequired := !finalObservationReturned &&
				obs.resolvedMode() != contract.ObserveNone
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
			if index < len(actions)-1 {
				facts["final_observation_phase"] = "after_wait"
			} else {
				facts["final_observation_action_index"] = index
			}
			return prependAXBatchSummary(
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
	result ToolCallResult,
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

// axBatchFocusChanged reports whether keyboard focus left the window a
// focus-bound batch was bound to, and if so returns the stop result.
func (s *Service) axBatchFocusChanged(
	context *AXBatchContext,
	index, count int,
	actionEvidence []map[string]any,
) (ToolCallResult, bool) {
	focus, argErr := s.resolveFocusedWindow()
	if argErr == nil && focus.handle == context.WindowHandle {
		return ToolCallResult{}, false
	}
	now := "no window has focus"
	if argErr == nil {
		now = fmt.Sprintf("focus is now on %s %s (%s)", focus.ref, strconv.Quote(focus.title), focus.appName)
	} else if argErr.Code == contract.AXCodeRuntimeError {
		now = "the focused window could not be read"
	}
	anyInputEmitted, anyPartialPossible, anyMutationUnknown := batchMutationSummary(actionEvidence)
	message := fmt.Sprintf(
		"keyboard focus left the bound window %s before action %d/%d; %s. "+
			"%d action(s) ran; the rest were not sent.",
		context.WindowRef, index+1, count, now, index,
	)
	stopped := TextResult(message, true)
	stopped.StructuredContent = map[string]any{
		contract.AXPublicErrorCodeKey:   contract.AXCodeWindowChanged,
		contract.AXPublicErrorDetailKey: message,
		contract.AXPublicErrorHintKey:   `{"mode":"act","actions":[{"type":"activate","ref":"` + context.WindowRef + `"}]}`,
		"failure_code":                  "stale_target",
		"failure_phase":                 "between_actions",
		"input_events_emitted":          anyInputEmitted,
		"partial_input_possible":        anyPartialPossible,
		"mutation_unknown":              anyMutationUnknown || anyInputEmitted,
		"observation_required":          true,
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
