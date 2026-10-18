package main

import (
	"fmt"
	"strings"
)

func a11yBatchActionObservation(
	index,
	count int,
	final observation,
	hasPostcondition bool,
) observation {
	if index == count-1 && !hasPostcondition {
		// Capture the requested final state in the last action's provider call.
		return final
	}
	captureSnapshot := index < count-1
	return observation{
		mode:            observeNone,
		captureSnapshot: &captureSnapshot,
	}
}

func closedWindowFinalObservation(
	context *a11yBatchContext,
	obs observation,
) toolCallResult {
	windowRef := ""
	if context != nil {
		windowRef = strings.TrimSpace(context.windowRef)
	}
	refLine := ""
	if windowRef != "" {
		refLine = "\n absent_refs: [" + windowRef + "]"
	}
	result := textResult(
		"ax_diff:\n effect: target_window_absent"+refLine,
		false,
	)
	result.StructuredContent = map[string]any{
		"full_tree_returned":                   false,
		"tree_reference_only":                  false,
		"compact_action_observation":           true,
		"delivered_snapshot_baseline_advanced": false,
		"target_window_absent":                 true,
		"final_observation_kind":               "lifecycle",
		"next_identical_observation_required":  false,
		"observation_required":                 false,
	}
	if obs.resolvedMode() == observeScreenshot {
		result.StructuredContent["final_observation_returned"] = false
		result.StructuredContent["screenshot_returned"] = false
		result.StructuredContent["observation_required"] = true
		result.Content = append(result.Content, contentItem{
			Type: "text",
			Text: "Target-window screenshot unavailable because the verified " +
				"closed window no longer exists.",
		})
		return result
	}
	result.StructuredContent["final_observation_returned"] = true
	return result
}

func closedWindowLifecycleProven(
	context *a11yBatchContext,
	result toolCallResult,
) bool {
	if context == nil ||
		!isShortAXWindowRef(context.windowRef) ||
		!result.IsError ||
		result.StructuredContent == nil {
		return false
	}
	code, _ := result.StructuredContent["failure_code"].(string)
	switch code {
	case "stale_window_ref":
		sourceStatus, _ :=
			result.StructuredContent["source_window_status"].(string)
		inventoryComplete, _ :=
			result.StructuredContent["recovery_inventory_complete"].(bool)
		return sourceStatus == "gone" && inventoryComplete
	case "stale_app_ref":
		subject, _ := result.StructuredContent["failure_subject"].(string)
		factsComplete, _ :=
			result.StructuredContent["failure_facts_complete"].(bool)
		return subject == "app" && factsComplete
	default:
		return false
	}
}

func finalizeVerifiedCloseLifecycle(result toolCallResult) toolCallResult {
	if result.StructuredContent == nil ||
		result.StructuredContent["target_window_absent"] != true {
		return result
	}
	result.Content = stripSupersededFinalObservationUnavailable(result.Content)
	result.StructuredContent["close_effect_verified"] = true
	result.StructuredContent["next_identical_observation_required"] = false
	delete(result.StructuredContent, "task_effect_verified")

	finalReturned, _ :=
		result.StructuredContent["final_observation_returned"].(bool)
	if finalReturned {
		result.StructuredContent["observation_required"] = false
	} else {
		result.StructuredContent["observation_required"] = true
	}

	failureCode, _ :=
		result.StructuredContent["failure_code"].(string)
	observationFailureCode, _ :=
		result.StructuredContent["observation_failure_code"].(string)
	actionFailure := failureCode != ""
	if !actionFailure {
		result.IsError = false
		clearVerifiedCloseUncertainty(result.StructuredContent, nil)
		delete(result.StructuredContent, "observation_failure_code")
		delete(result.StructuredContent, "post_action_observation_status")
	}
	if !result.StructuredContent["observation_required"].(bool) {
		delete(result.StructuredContent, "continuation")
	} else if observationFailureCode != "" {
		result.StructuredContent["continuation"] = map[string]any{
			"state":              "observation_required",
			"route":              "GUI",
			"retry_same_request": false,
		}
	}
	return result
}

func stripSupersededFinalObservationUnavailable(
	content []contentItem,
) []contentItem {
	filtered := content[:0]
	for _, item := range content {
		if item.Type != "text" {
			filtered = append(filtered, item)
			continue
		}
		lines := strings.Split(item.Text, "\n")
		kept := lines[:0]
		for _, line := range lines {
			if strings.HasPrefix(
				strings.TrimSpace(line),
				"final_observation_unavailable:",
			) {
				continue
			}
			kept = append(kept, line)
		}
		item.Text = strings.TrimSpace(strings.Join(kept, "\n"))
		if item.Text != "" {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func (s *service) recoverClosedWindowActionObservation(
	context *a11yBatchContext,
	action a11yBatchAction,
	result toolCallResult,
	obs observation,
) toolCallResult {
	if s == nil ||
		action.actionType != "close" ||
		result.StructuredContent == nil {
		return result
	}
	omitFinalObservation := obs.resolvedMode() == observeNone
	returned, hasReturned :=
		result.StructuredContent["final_observation_returned"].(bool)
	required, _ :=
		result.StructuredContent["observation_required"].(bool)
	if hasReturned && returned && !required {
		result = finalizeVerifiedCloseLifecycle(result)
		if omitFinalObservation {
			result = omitVerifiedCloseFinalObservation(result)
		}
		return result
	}
	delete(result.StructuredContent, "observation_required")
	delete(result.StructuredContent, "next_identical_observation_required")
	probeObservation := obs
	if omitFinalObservation {
		probeObservation = observation{mode: observeDiff}
	}
	finalObservation := s.a11yBatchFinalObservation(
		context,
		probeObservation,
		true,
	)
	if omitFinalObservation &&
		finalObservation.StructuredContent["target_window_absent"] != true {
		result.StructuredContent["final_observation_kind"] = "none"
		result.StructuredContent["final_observation_returned"] = false
		result.StructuredContent["close_effect_verified"] = false
		result.StructuredContent["observation_required"] = true
		result.StructuredContent["next_identical_observation_required"] = false
		result.StructuredContent["continuation"] = map[string]any{
			"state":              "observation_required",
			"route":              "observe",
			"retry_same_request": false,
		}
		return result
	}
	merged := mergeA11yBatchFinalObservation(result, finalObservation)
	merged = finalizeVerifiedCloseLifecycle(merged)
	if omitFinalObservation &&
		merged.StructuredContent["close_effect_verified"] == true {
		merged = omitVerifiedCloseFinalObservation(merged)
	}
	return merged
}

func omitVerifiedCloseFinalObservation(result toolCallResult) toolCallResult {
	if result.StructuredContent == nil ||
		result.StructuredContent["close_effect_verified"] != true {
		return result
	}
	failurePhase, _ := result.StructuredContent["failure_phase"].(string)
	if failurePhase == "post_action_observation" ||
		failurePhase == "final_observation" {
		result.IsError = false
		delete(result.StructuredContent, "failure_code")
		delete(result.StructuredContent, "failure_phase")
		delete(result.StructuredContent, "observation_failure_code")
		delete(result.StructuredContent, "post_action_observation_status")
		clearVerifiedCloseUncertainty(result.StructuredContent, nil)
	}
	filtered := result.Content[:0]
	for _, item := range result.Content {
		if item.Type == "text" &&
			strings.HasPrefix(
				strings.TrimSpace(item.Text),
				"ax_diff:\n effect: target_window_absent",
			) {
			continue
		}
		filtered = append(filtered, item)
	}
	result.Content = append(filtered, contentItem{
		Type: "text",
		Text: "close_effect: verified target_window_absent; " +
			`observe="none" returned no final tree or screenshot.`,
	})
	result.StructuredContent["full_tree_returned"] = false
	result.StructuredContent["tree_reference_only"] = false
	result.StructuredContent["compact_action_observation"] = false
	result.StructuredContent["final_observation_kind"] = "none"
	result.StructuredContent["final_observation_returned"] = false
	result.StructuredContent["observation_required"] = false
	result.StructuredContent["next_identical_observation_required"] = false
	return result
}

func (s *service) a11yBatchFinalObservation(
	context *a11yBatchContext,
	obs observation,
	allowClosedWindowLifecycle bool,
) toolCallResult {
	if context == nil || obs.resolvedMode() == observeNone {
		return toolCallResult{}
	}
	includeScreenshot := obs.resolvedMode() == observeScreenshot
	request := axServiceRequest{
		Operation:          axServiceObserve,
		App:                context.appToken,
		IncludeScreenshot:  &includeScreenshot,
		TargetWindowHandle: context.windowHandle,
		SourceRef:          context.windowRef,
		Scope:              a11yScopeWindow,
		TreeView:           treeViewWorking,
	}
	snapshot, notes, result := s.refreshSnapshot(context.appToken, request)
	if result.IsError {
		if current, ok := s.finalObservationContinuation(context, result); ok {
			request.TargetWindowHandle = current.WindowHandle
			request.SourceRef = current.Ref
			snapshot, notes, result = s.refreshSnapshot(context.appToken, request)
			if !result.IsError {
				rendered := s.renderA11yBatchFinalObservation(snapshot, notes, obs)
				if rendered.StructuredContent == nil {
					rendered.StructuredContent = map[string]any{}
				}
				rendered.StructuredContent["source_window_status"] = "gone"
				rendered.StructuredContent["final_observation_window_ref"] =
					current.Ref
				rendered.StructuredContent["final_observation_followed_window_transition"] =
					true
				return rendered
			}
		}
		if allowClosedWindowLifecycle &&
			closedWindowLifecycleProven(context, result) {
			return closedWindowFinalObservation(context, obs)
		}
		if result.StructuredContent == nil {
			result.StructuredContent = map[string]any{}
		}
		result.StructuredContent["failure_phase"] = "final_observation"
		result.StructuredContent["final_observation_returned"] = false
		result.StructuredContent["observation_required"] = true
		return result
	}
	return s.renderA11yBatchFinalObservation(snapshot, notes, obs)
}

func (s *service) recoverA11yBatchObservation(
	context *a11yBatchContext,
	failed toolCallResult,
	obs observation,
) toolCallResult {
	if s == nil || context == nil || obs.resolvedMode() == observeNone {
		return failed
	}
	if failed.StructuredContent == nil {
		return failed
	}
	if failed.StructuredContent["focus_preparation"] != nil || failed.StructuredContent["pointer_preparation"] != nil {
		return failed
	}
	inputEmitted := axEvidenceReportsInputEmission(failed.StructuredContent)
	partial, _ := failed.StructuredContent["partial_input_possible"].(bool)
	unknown, _ := failed.StructuredContent["mutation_unknown"].(bool)
	if !inputEmitted && !partial && !unknown {
		return failed
	}

	targetWindowHandle := context.windowHandle
	sourceRef := context.windowRef
	if currentRef, _ :=
		failed.StructuredContent["current_window_ref"].(string); isShortAXWindowRef(currentRef) {
		if current, err := s.windowRefRegistry.Resolve(currentRef, ""); err == nil &&
			current.AppRef == context.appRef &&
			validInternalWindowHandle(current.WindowHandle) {
			targetWindowHandle = current.WindowHandle
			sourceRef = current.Ref
		}
	}
	if sourceStatus, _ :=
		failed.StructuredContent["source_window_status"].(string); sourceStatus == "gone" &&
		sourceRef == context.windowRef {
		targetWindowHandle = ""
		sourceRef = ""
	}

	includeScreenshot := obs.resolvedMode() == observeScreenshot
	request := axServiceRequest{
		Operation:          axServiceObserve,
		App:                context.appToken,
		IncludeScreenshot:  &includeScreenshot,
		TargetWindowHandle: targetWindowHandle,
		SourceRef:          sourceRef,
		Scope:              a11yScopeWindow,
		TreeView:           treeViewWorking,
	}
	snapshot, notes, observationResult := s.refreshSnapshot(
		context.appToken,
		request,
	)
	appRootRetryAttempted := false
	targetedFailureCode := ""
	if (targetWindowHandle != "" || sourceRef != "") &&
		a11yRecoveryObservationCanRetryAtAppRoot(observationResult) {
		appRootRetryAttempted = true
		targetedFailureCode, _ =
			observationResult.StructuredContent["failure_code"].(string)
		request.TargetWindowHandle = ""
		request.SourceRef = ""
		snapshot, notes, observationResult = s.refreshSnapshot(
			context.appToken,
			request,
		)
		if !observationResult.IsError {
			notes = append(
				[]string{
					"[recovery] The window-targeted read became stale; " +
						"the current application window was read instead.",
				},
				notes...,
			)
		}
	}
	if !observationResult.IsError {
		observationResult = s.renderA11yBatchFinalObservation(
			snapshot,
			append(
				[]string{
					"[recovery] Fresh current state after an uncertain mutation. " +
						"This observation does not attribute the state to the failed action.",
				},
				notes...,
			),
			obs,
		)
	}
	merged := mergeA11yBatchRecoveryObservation(failed, observationResult)
	if appRootRetryAttempted && merged.StructuredContent != nil {
		merged.StructuredContent["recovery_app_root_retry_attempted"] = true
		if targetedFailureCode != "" {
			merged.StructuredContent["recovery_targeted_failure_code"] =
				targetedFailureCode
		}
	}
	return merged
}

func a11yRecoveryObservationCanRetryAtAppRoot(result toolCallResult) bool {
	if !result.IsError || result.StructuredContent == nil {
		return false
	}
	code, _ := result.StructuredContent["failure_code"].(string)
	switch code {
	case "stale_window_ref", "stale_target", "observation_incomplete", "state_unavailable":
		return true
	default:
		return false
	}
}

func mergeA11yBatchRecoveryObservation(
	failed,
	observationResult toolCallResult,
) toolCallResult {
	originalFailureCode, _ := failed.StructuredContent["failure_code"].(string)
	originalFailurePhase, _ := failed.StructuredContent["failure_phase"].(string)
	merged := mergeA11yBatchFinalObservation(failed, observationResult)
	if merged.StructuredContent == nil {
		merged.StructuredContent = map[string]any{}
	}
	merged.StructuredContent["recovery_observation_attempted"] = true
	returned, _ := merged.StructuredContent["final_observation_returned"].(bool)
	merged.StructuredContent["recovery_observation_returned"] = returned
	merged.StructuredContent["cause_unattributed"] = true
	merged.StructuredContent["task_effect_verified"] = false
	if originalFailureCode != "" {
		merged.StructuredContent["failure_code"] = originalFailureCode
	}
	if originalFailurePhase != "" {
		merged.StructuredContent["failure_phase"] = originalFailurePhase
	}
	if returned {
		merged.StructuredContent["observation_required"] = false
		merged.StructuredContent["next_identical_observation_required"] = false
		merged.StructuredContent["post_action_observation_status"] =
			"recovered_current_state"
		if continuation, ok :=
			merged.StructuredContent["continuation"].(map[string]any); ok &&
			strings.TrimSpace(fmt.Sprint(continuation["state"])) ==
				"observe_required" {
			delete(merged.StructuredContent, "continuation")
		}
	} else if code, _ :=
		observationResult.StructuredContent["failure_code"].(string); code != "" {
		merged.StructuredContent["recovery_observation_failure_code"] = code
	}
	return merged
}

func (s *service) finalObservationContinuation(
	context *a11yBatchContext,
	result toolCallResult,
) (*AXWindowRefEntry, bool) {
	if s == nil || context == nil || !result.IsError ||
		result.StructuredContent == nil {
		return nil, false
	}
	code, _ := result.StructuredContent["failure_code"].(string)
	sourceStatus, _ := result.StructuredContent["source_window_status"].(string)
	inventoryComplete, _ :=
		result.StructuredContent["recovery_inventory_complete"].(bool)
	currentRef, _ := result.StructuredContent["current_window_ref"].(string)
	if code != "stale_window_ref" ||
		sourceStatus != "gone" ||
		!inventoryComplete ||
		!isShortAXWindowRef(currentRef) ||
		currentRef == context.windowRef {
		return nil, false
	}
	current, err := s.windowRefRegistry.Resolve(currentRef, "")
	if err != nil ||
		current.AppRef == "" ||
		current.AppRef != context.appRef ||
		!validInternalWindowHandle(current.WindowHandle) {
		return nil, false
	}
	return current, true
}

func (s *service) renderA11yBatchFinalObservation(
	snapshot *appSnapshot,
	notes []string,
	obs observation,
) toolCallResult {
	if snapshot == nil {
		result := textResult(
			"final_observation_unavailable: AX returned no snapshot after the until check",
			true,
		)
		result.StructuredContent = map[string]any{
			"failure_code":               "observation_incomplete",
			"failure_phase":              "final_observation",
			"final_observation_returned": false,
			"final_observation_kind":     "unavailable",
			"observation_required":       true,
		}
		return result
	}
	if obs.resolvedMode() == observeScreenshot {
		result, _ := renderAXScreenshotObservation(snapshot, notes)
		result.axProofSnapshot = snapshot
		result.axProofNotes = append([]string(nil), notes...)
		return result
	}

	result, deliveredLines, treeReturned, compact, fullTree :=
		s.renderAXDiffOrCompactObservation(snapshot, notes)
	if !treeReturned {
		return result
	}
	advanced := s.rememberDeliveredActionObservation(
		snapshot,
		result,
		deliveredLines,
		true,
		fullTree,
		compact,
	)
	if result.StructuredContent == nil {
		result.StructuredContent = map[string]any{}
	}
	result.StructuredContent["full_tree_returned"] = fullTree
	result.StructuredContent["tree_reference_only"] = false
	result.StructuredContent["final_observation_returned"] = true
	result.StructuredContent["next_identical_observation_required"] = false
	result.StructuredContent["compact_action_observation"] = compact
	result.StructuredContent["delivered_snapshot_baseline_advanced"] = advanced
	addTreeProjectionMetadata(&result, snapshot)
	result.axProofSnapshot = snapshot
	result.axProofNotes = append([]string(nil), notes...)
	return result
}

func renderAXScreenshotObservation(
	snapshot *appSnapshot,
	notes []string,
) (toolCallResult, bool) {
	result := snapshot.screenshotOnlyResult(notes)
	if result.StructuredContent == nil {
		result.StructuredContent = map[string]any{}
	}
	returned := snapshot != nil && snapshot.ScreenshotPNGBase64 != ""
	result.StructuredContent["final_observation_returned"] = returned
	result.StructuredContent["screenshot_returned"] = returned
	if returned {
		result.StructuredContent["final_observation_kind"] = "screenshot"
	} else {
		result.StructuredContent["final_observation_kind"] = "unavailable"
	}
	result.StructuredContent["observation_required"] = !returned
	result.StructuredContent["next_identical_observation_required"] = !returned
	return result, returned
}

func (s *service) renderAXDiffOrCompactObservation(
	snapshot *appSnapshot,
	notes []string,
) (toolCallResult, []string, bool, bool, bool) {
	baseline := s.deliveredSnapshotFor(snapshot)
	if diff, ok := incrementalTree(baseline, snapshot); ok {
		result := snapshot.resultWithTreeOverride(notes, diff)
		if result.StructuredContent == nil {
			result.StructuredContent = map[string]any{}
		}
		result.StructuredContent["final_observation_kind"] = "diff"
		return result, diff, true, false, false
	}
	if contentRevisionChanged(baseline, snapshot) {
		if lines := boundedWorkingObservationLines(snapshot); len(lines) > 0 {
			result := snapshot.resultWithTreeOverride(notes, lines)
			if result.StructuredContent == nil {
				result.StructuredContent = map[string]any{}
			}
			result.StructuredContent["final_observation_kind"] = "working_tree"
			result.StructuredContent["content_transition_working_tree"] = true
			return result, lines, true, false, true
		}
	}
	lines := compactActionObservationLines(snapshot, baseline)
	if len(lines) > 0 {
		result := snapshot.resultWithTreeOverride(notes, lines)
		if result.StructuredContent == nil {
			result.StructuredContent = map[string]any{}
		}
		kind := "focused"
		if lines[0] == "content:" {
			kind = "content"
		}
		result.StructuredContent["final_observation_kind"] = kind
		return result, lines, true, true, false
	}
	lines = boundedWorkingObservationLines(snapshot)
	if len(lines) > 0 {
		result := snapshot.resultWithTreeOverride(notes, lines)
		if result.StructuredContent == nil {
			result.StructuredContent = map[string]any{}
		}
		result.StructuredContent["working_tree_fallback"] = true
		result.StructuredContent["final_observation_kind"] = "working_tree"
		return result, lines, true, false, true
	}
	result := snapshot.resultWithTreeOverride(notes, []string{
		"final_observation_unavailable: no bounded current AX subtree was available",
	})
	if result.StructuredContent == nil {
		result.StructuredContent = map[string]any{}
	}
	result.StructuredContent["full_tree_returned"] = false
	result.StructuredContent["tree_reference_only"] = false
	result.StructuredContent["compact_action_observation"] = false
	result.StructuredContent["delivered_snapshot_baseline_advanced"] = false
	result.StructuredContent["final_observation_returned"] = false
	result.StructuredContent["final_observation_kind"] = "unavailable"
	result.StructuredContent["observation_required"] = true
	result.StructuredContent["next_identical_observation_required"] = true
	return result, nil, false, false, false
}

func contentRevisionChanged(before, after *appSnapshot) bool {
	return before != nil &&
		after != nil &&
		validContentRevision(before.contentRevision) &&
		validContentRevision(after.contentRevision) &&
		before.contentRevision != after.contentRevision
}

func (s *service) renderAXWorkingObservation(
	snapshot *appSnapshot,
	notes []string,
) (toolCallResult, []string, bool, bool, bool) {
	if lines := boundedWorkingObservationLines(snapshot); len(lines) > 0 {
		result := snapshot.resultWithTreeOverride(notes, lines)
		if result.StructuredContent == nil {
			result.StructuredContent = map[string]any{}
		}
		result.StructuredContent["final_observation_kind"] = "working_tree"
		return result, lines, true, false, true
	}
	return s.renderAXDiffOrCompactObservation(snapshot, notes)
}

func boundedWorkingObservationLines(snapshot *appSnapshot) []string {
	if snapshot == nil {
		return nil
	}
	for _, line := range snapshot.TreeLines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && trimmed != "ax:" {
			return append([]string(nil), snapshot.TreeLines...)
		}
	}
	return nil
}

func mergeA11yBatchFinalObservation(
	actionResult,
	observationResult toolCallResult,
) toolCallResult {
	hadPriorError := actionResult.IsError
	inputEmitted := axEvidenceReportsInputEmission(
		actionResult.StructuredContent,
	)
	actionResult.Content = append(
		stripSupersededAXObservationPlaceholder(actionResult.Content),
		observationResult.Content...,
	)
	if actionResult.StructuredContent == nil {
		actionResult.StructuredContent = map[string]any{}
	}
	if inputEmitted {
		actionResult.StructuredContent["input_events_emitted"] = true
	}
	for key, value := range observationResult.StructuredContent {
		switch key {
		case "failure_code", "failure_phase":
			if actionResult.IsError {
				continue
			}
		case "preflight_only", "evidence_scope":
			if inputEmitted {
				continue
			}
		case "input_events_emitted", "partial_input_possible", "mutation_unknown":
			// These facts are monotonic across one call. A final observation
			// cannot erase input or uncertainty already recorded by the action.
			if existing, _ := actionResult.StructuredContent[key].(bool); existing {
				continue
			}
		}
		actionResult.StructuredContent[key] = value
	}
	if inputEmitted {
		actionResult.StructuredContent["preflight_only"] = false
		if unknown, _ := actionResult.StructuredContent["mutation_unknown"].(bool); unknown {
			actionResult.StructuredContent["evidence_scope"] =
				"post_action_observation"
		}
	}
	observationReturned, hasObservationStatus :=
		observationResult.StructuredContent["final_observation_returned"].(bool)
	observationUnavailable := observationResult.IsError ||
		(hasObservationStatus && !observationReturned)
	verifiedTargetAbsent, _ :=
		observationResult.StructuredContent["target_window_absent"].(bool)
	postconditionVerified, _ :=
		actionResult.StructuredContent["postcondition_verified"].(bool)
	if verifiedTargetAbsent && postconditionVerified {
		if _, present :=
			actionResult.StructuredContent["observation_required"]; !present {
			actionResult.StructuredContent["observation_required"] =
				!observationReturned
		}
		actionResult.StructuredContent["next_identical_observation_required"] = false
		clearVerifiedCloseUncertainty(actionResult.StructuredContent, nil)
		return actionResult
	}
	if observationUnavailable {
		actionResult.StructuredContent["final_observation_returned"] = false
		actionResult.StructuredContent["observation_required"] = true
		if inputEmitted && !hadPriorError {
			actionResult.IsError = false
			actionResult.StructuredContent["effect_unknown"] = true
			actionResult.StructuredContent["mutation_unknown"] = true
			actionResult.StructuredContent["post_action_observation_status"] =
				"unknown"
			if code, ok :=
				observationResult.StructuredContent["failure_code"].(string); ok {
				actionResult.StructuredContent["observation_failure_code"] = code
				delete(actionResult.StructuredContent, "failure_code")
			} else {
				actionResult.StructuredContent["observation_failure_code"] =
					"final_observation_unavailable"
			}
			delete(actionResult.StructuredContent, "failure_phase")
		} else if !hadPriorError {
			actionResult.IsError = true
		}
	}
	return actionResult
}

func stripSupersededAXObservationPlaceholder(
	content []contentItem,
) []contentItem {
	filtered := content[:0]
	for _, item := range content {
		if item.Type != "text" {
			filtered = append(filtered, item)
			continue
		}
		lines := strings.Split(item.Text, "\n")
		kept := lines[:0]
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "observe: none" ||
				strings.HasPrefix(trimmed, `(observe="none":`) {
				continue
			}
			kept = append(kept, line)
		}
		text := strings.TrimSpace(strings.Join(kept, "\n"))
		if text == "" {
			continue
		}
		item.Text = text
		filtered = append(filtered, item)
	}
	return filtered
}

func clearVerifiedCloseUncertainty(
	structuredContent map[string]any,
	actionEvidence map[string]any,
) {
	delete(structuredContent, "effect_unknown")
	delete(structuredContent, "mutation_unknown")
	delete(structuredContent, "cause_unattributed")
	if actionEvidence != nil {
		delete(actionEvidence, "mutation_unknown")
		delete(actionEvidence, "cause_unattributed")
	}
}
