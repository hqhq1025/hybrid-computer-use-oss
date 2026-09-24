package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

func enforceAXImageOutputPolicy(
	name string,
	args map[string]any,
	result toolCallResult,
) toolCallResult {
	if internalToolName(name) != "a11y" {
		return result
	}
	if axRequestExplicitlyIncludesScreenshot(args) {
		return result
	}

	content := make([]contentItem, 0, len(result.Content))
	for _, item := range result.Content {
		if item.Type != "image" {
			content = append(content, item)
		}
	}
	result.Content = content
	return result
}

func axRequestExplicitlyIncludesScreenshot(args map[string]any) bool {
	mode, err := parseA11yMode(args)
	if err != nil {
		return false
	}
	switch mode {
	case a11yModeObserve:
		target := defaultString(
			strings.ToLower(strings.TrimSpace(
				optionalString(args, "observation_target"),
			)),
			"app",
		)
		if target != "app" {
			return false
		}
		obs, err := parseObservation(args)
		if err != nil {
			return false
		}
		return obs.resolvedMode() == observeScreenshot ||
			obs.resolvedMode() == observeFull
	case a11yModeAct:
		obs, err := parseObservation(args)
		if err != nil {
			return false
		}
		return obs.resolvedMode() == observeScreenshot
	default:
		return false
	}
}

func compactAXBatchResult(value any) map[string]any {
	full, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	compact := map[string]any{}
	failedIndex, failed := full["failed_action_index"]
	guardFailedIndex, guardFailed := full["guard_failed_action_index"]
	rejected, _ := full["rejected_before_first_action"].(bool)
	partial, _ := full["partial_sequence"].(bool)
	if failed && failedIndex != nil ||
		guardFailed && guardFailedIndex != nil ||
		rejected || partial {
		for _, key := range []string{
			"actions_requested",
			"actions_completed_without_call_error",
			"failed_action_index",
			"failed_action_type",
			"guard_failed_action_index",
			"guard_failed_action_number",
			"guard_failed_action_type",
			"stopped_before_action_index",
			"action_postcondition",
		} {
			if item, present := full[key]; present && item != nil {
				compact[key] = item
			}
		}
		if partial {
			compact["partial_sequence"] = true
		}
		if rejected {
			compact["rejected_before_first_action"] = true
		}
	}
	rawActions, _ := full["actions"].([]map[string]any)
	if rawActions == nil {
		if generic, ok := full["actions"].([]any); ok {
			rawActions = make([]map[string]any, 0, len(generic))
			for _, item := range generic {
				if action, ok := item.(map[string]any); ok {
					rawActions = append(rawActions, action)
				}
			}
		}
	}
	actions := make([]map[string]any, 0, len(rawActions))
	for _, action := range rawActions {
		entry := map[string]any{
			"index": action["action_index"],
		}
		actionability, _ := action["actionability"].(map[string]any)
		if actionability != nil {
			if resolution := strings.TrimSpace(fmt.Sprint(
				actionability["resolution"],
			)); resolution != "" && resolution != "unique" &&
				resolution != "<nil>" {
				entry["resolution"] = resolution
			}
			for _, key := range []string{
				"delivery_verified",
				"postcondition_verified",
			} {
				value := compactTriState(actionability[key])
				if value == false {
					entry[key] = value
				}
			}
			for _, key := range []string{
				"stable",
				"window_focus_verified",
				"target_focus_verified",
				"point_owner_verified",
				"enabled",
				"editable_verified",
			} {
				if value := compactTriState(actionability[key]); value == false {
					entry[key] = false
				}
			}
		}
		if callError, _ := action["call_error"].(bool); callError {
			entry["type"] = action["action_type"]
			nonDefaultStringEvidence(
				entry, action, "failure_code", "", "none",
			)
			nonDefaultStringEvidence(
				entry, action, "failure_phase", "", "none",
			)
		}
		if postcondition, ok := action["postcondition"].(map[string]any); ok {
			status := strings.TrimSpace(fmt.Sprint(postcondition["status"]))
			reconciled, _ :=
				postcondition["reconciled_by_canonical_observation"].(bool)
			if status != "" && status != "<nil>" &&
				(status != "pass" || reconciled) {
				entry["type"] = action["action_type"]
				entry["postcondition"] = postcondition
			}
		}
		if len(entry) > 1 {
			actions = append(actions, entry)
		}
	}
	if len(actions) > 0 {
		compact["actions"] = actions
	}
	if postcondition, ok := full["postcondition"].(map[string]any); ok &&
		len(postcondition) > 0 {
		compact["postcondition"] = postcondition
	}
	return compact
}

func compactAXModelEvidence(
	full map[string]any,
	isError bool,
) map[string]any {
	compact := map[string]any{}
	mode := strings.TrimSpace(fmt.Sprint(full["ax_mode"]))
	coverageIncomplete := false
	if count, ok := intEvidence(full["coverage_incomplete_nodes"]); ok &&
		count > 0 {
		coverageIncomplete = true
	}

	treeComplete, hasTreeComplete := full["tree_complete_for_model"].(bool)
	if hasTreeComplete && !treeComplete && !coverageIncomplete {
		compact["tree_complete"] = false
	}
	structuralOmitted := false
	if count, ok := intEvidence(full["structural_budget_omitted_nodes"]); ok &&
		count > 0 {
		structuralOmitted = true
		compact["tree_complete"] = false
	}
	targetedCoverageFailure := coverageIncomplete &&
		axTargetedCoverageFailure(full, mode, isError)
	if coverageIncomplete &&
		(mode == a11yModeObserve || targetedCoverageFailure) {
		compact["coverage"] = "partial"
		compact["tree_complete"] = false
	}
	recover := map[string]any{}
	if structuralOmitted {
		recover["structural_budget"] = "find|root"
	}
	if targetedCoverageFailure {
		compact["target_resolution"] = "incomplete"
		if mode == a11yModeAct {
			recover["target"] = "find|GUI"
		} else {
			recover["target"] = "GUI"
		}
	}
	if len(recover) > 0 {
		compact["recover"] = recover
	}

	if batch := compactAXBatchResult(full["a11y_batch_result"]); len(batch) > 0 {
		compact["batch"] = batch
	}
	for _, key := range []string{
		"failure_code",
		"underlying_failure_code",
		"failure_phase",
		"failure_subject",
		"input_events_emitted",
		"partial_input_possible",
		"mutation_unknown",
		"effect_unknown",
		"observation_required",
		"observation_failure_code",
		"post_action_observation_status",
		"final_observation_returned",
		"next_identical_observation_required",
		"screenshot_returned",
		"target_window_absent",
		"close_effect_verified",
		"final_observation_kind",
		"source_window_status",
		"final_observation_window_ref",
		"final_observation_followed_window_transition",
		"window_identity_stabilized",
		"failure_facts_complete",
		"assertion_sampled_live_ui",
		"preflight_only",
		"predicate_verified",
		"postcondition_verified",
		"postcondition_recovered_by_final_observation",
		"postcondition_reconciled_by_canonical_observation",
		"replacement_window_ref",
		"recovery_observation_attempted",
		"recovery_observation_returned",
		"recovery_observation_failure_code",
		"action_outcome",
		"failed_action_input_emitted",
		"failed_action_caused_ui_change",
		"current_ui_returned",
		"current_state_cause",
		"recovery_read_attempted",
		"cause_unattributed",
		"recovery",
		"retry_same_request",
	} {
		if value, present := full[key]; present {
			if boolean, ok := value.(bool); ok && !boolean && !isError &&
				key != "input_events_emitted" &&
				key != "postcondition_verified" &&
				key != "final_observation_returned" &&
				key != "observation_required" &&
				key != "next_identical_observation_required" {
				continue
			}
			compact[key] = value
		}
	}
	if continuation, ok := full["continuation"].(map[string]any); ok {
		compactContinuation := map[string]any{}
		for _, key := range []string{
			"state",
			"retry_same_request",
			"route",
			"next_mode",
			"fresh_window_ref",
		} {
			if value, present := continuation[key]; present {
				compactContinuation[key] = value
			}
		}
		if len(compactContinuation) > 0 {
			compact["continuation"] = compactContinuation
		}
	}
	if isError {
		nonDefaultStringEvidence(
			compact, full, "evidence_scope", "", "error_only",
		)
	}
	return compact
}

func appendAXContinuationText(
	result toolCallResult,
	continuation map[string]any,
) toolCallResult {
	if len(continuation) == 0 {
		return result
	}
	parts := []string{}
	for _, key := range []string{
		"state",
		"retry_same_request",
		"route",
		"next_mode",
		"fresh_window_ref",
	} {
		if value, present := continuation[key]; present {
			parts = append(parts, fmt.Sprintf("%s=%v", key, value))
		}
	}
	if len(parts) == 0 {
		return result
	}
	line := "continuation: " + strings.Join(parts, " ")
	for _, item := range result.Content {
		if item.Type == "text" && strings.Contains(item.Text, line) {
			return result
		}
	}
	if len(result.Content) == 0 {
		result.Content = []contentItem{{Type: "text", Text: line}}
		return result
	}
	for index := len(result.Content) - 1; index >= 0; index-- {
		if result.Content[index].Type == "text" {
			text := strings.TrimSpace(result.Content[index].Text)
			if text == "" {
				result.Content[index].Text = line + "\n"
			} else {
				result.Content[index].Text = text + "\n" + line + "\n"
			}
			return result
		}
	}
	result.Content = append(result.Content, contentItem{Type: "text", Text: line + "\n"})
	return result
}

func delimitAXTextContent(result toolCallResult) toolCallResult {
	for index := range result.Content {
		if result.Content[index].Type != "text" {
			continue
		}
		if !strings.HasSuffix(result.Content[index].Text, "\n") {
			result.Content[index].Text += "\n"
		}
	}
	return result
}

func prepareAXEvidence(
	result toolCallResult,
) toolCallResult {
	result = delimitAXTextContent(result)
	result = normalizeAXEvidenceInvariants(result)
	if len(result.StructuredContent) == 0 {
		return result
	}
	full := result.StructuredContent
	if continuation, ok := full["continuation"].(map[string]any); ok {
		result = appendAXContinuationText(result, continuation)
	}
	result.StructuredContent = compactAXModelEvidence(full, result.IsError)
	return result
}

func normalizeAXEvidenceInvariants(
	result toolCallResult,
) toolCallResult {
	if len(result.StructuredContent) == 0 {
		return result
	}
	full := result.StructuredContent
	if axEvidenceReportsInputEmission(full) {
		full["input_events_emitted"] = true
		full["preflight_only"] = false
		if unknown, _ := full["mutation_unknown"].(bool); unknown {
			full["evidence_scope"] = "post_action_observation"
		}
	}
	return result
}

func axEvidenceReportsInputEmission(evidence map[string]any) bool {
	if evidence == nil {
		return false
	}
	if emitted, _ := evidence["input_events_emitted"].(bool); emitted {
		return true
	}
	switch runtimeEvidence := evidence["runtime_action_evidence"].(type) {
	case RuntimeActionEvidence:
		if runtimeEvidence.InputEmitted {
			return true
		}
	case *RuntimeActionEvidence:
		if runtimeEvidence != nil && runtimeEvidence.InputEmitted {
			return true
		}
	case map[string]any:
		if emitted, _ := runtimeEvidence["input_emitted"].(bool); emitted {
			return true
		}
	}
	switch actionability := evidence["actionability"].(type) {
	case ActionabilityEvidence:
		if actionability.InputEmitted == TriTrue {
			return true
		}
	case *ActionabilityEvidence:
		if actionability != nil && actionability.InputEmitted == TriTrue {
			return true
		}
	case map[string]any:
		if compactTriState(actionability["input_emitted"]) == true {
			return true
		}
	}
	batch, _ := evidence["a11y_batch_result"].(map[string]any)
	actions, _ := batch["actions"].([]map[string]any)
	for _, action := range actions {
		if axEvidenceReportsInputEmission(action) {
			return true
		}
	}
	if genericActions, ok := batch["actions"].([]any); ok {
		for _, item := range genericActions {
			action, _ := item.(map[string]any)
			if axEvidenceReportsInputEmission(action) {
				return true
			}
		}
	}
	if emitted, _ := evidence["input_emitted"].(bool); emitted {
		return true
	}
	return false
}

func persistAXTelemetry(
	args map[string]any,
	telemetry any,
) {
	path := strings.TrimSpace(
		os.Getenv("COMPUTER_USE_AX_TELEMETRY_LOG"),
	)
	if path == "" {
		path = "/tmp/computer-use-ax-telemetry.jsonl"
	}
	payload := map[string]any{
		"timestamp_utc": time.Now().UTC().Format(time.RFC3339Nano),
		"mode":          optionalString(args, "mode"),
		"scope":         optionalString(args, "scope"),
		"view":          optionalString(args, "view"),
		"telemetry":     telemetry,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "AX telemetry marshal failed: %v\n", err)
		return
	}
	handle, err := os.OpenFile(
		path,
		os.O_CREATE|os.O_APPEND|os.O_WRONLY,
		0600,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "AX telemetry log open failed: %v\n", err)
		return
	}
	defer handle.Close()
	if _, err := handle.Write(append(encoded, '\n')); err != nil {
		fmt.Fprintf(os.Stderr, "AX telemetry log write failed: %v\n", err)
	}
}
