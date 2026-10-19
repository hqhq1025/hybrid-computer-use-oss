package main

import (
	"fmt"
	"strings"
)

func enforceAXImageOutputPolicy(
	name string,
	args map[string]any,
	result toolCallResult,
) toolCallResult {
	if name != publicToolAX {
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
	mode, err := parseAXMode(args)
	if err != nil {
		return false
	}
	switch mode {
	case axModeObserve:
		obs, err := parseObservation(args)
		if err != nil {
			return false
		}
		return obs.resolvedMode() == observeScreenshot ||
			obs.resolvedMode() == observeFull
	case axModeAct:
		obs, err := parseObservation(args)
		if err != nil {
			return false
		}
		return obs.resolvedMode() == observeScreenshot ||
			obs.resolvedMode() == observeFull
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
			for _, key := range []string{"focus_preparation", "pointer_preparation"} {
				nonDefaultStringEvidence(entry, action, key, "", "none")
			}
			nonDefaultStringEvidence(
				entry, action, "failure_code", "", "none",
			)
			nonDefaultStringEvidence(
				entry, action, "failure_phase", "", "none",
			)
		}
		if postcondition, ok := action["postcondition"].(map[string]any); ok {
			status := strings.TrimSpace(fmt.Sprint(postcondition["status"]))
			if status != "" && status != "<nil>" && status != "pass" {
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
	if mode == axModeFind {
		for _, key := range []string{
			"find_match_count", "find_page_start", "find_page_count", "next_cursor",
			"find_delivery_complete", "find_context_omitted", "find_display_excerpt",
		} {
			if value, present := full[key]; present {
				compact[key] = value
			}
		}
	}
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
		count > 0 && full["find_delivery_complete"] != true {
		structuralOmitted = true
		compact["tree_complete"] = false
	}
	targetedCoverageFailure := coverageIncomplete &&
		axTargetedCoverageFailure(full, mode, isError)
	if coverageIncomplete &&
		(mode == axModeObserve || targetedCoverageFailure) {
		compact["coverage"] = "partial"
		compact["tree_complete"] = false
	}
	recover := map[string]any{}
	if structuralOmitted {
		recover["structural_budget"] = "find|root"
	}
	if targetedCoverageFailure {
		compact["target_resolution"] = "incomplete"
		if mode == axModeAct {
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
		"focus_preparation",
		"pointer_preparation",
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

// renameAXWaitEvidence gives model-visible evidence the public name wait
// for what the code calls a postcondition: keys such as
// postcondition_verified would otherwise teach a removed field.
func renameAXWaitEvidence(value any) any {
	rename := func(text string) string {
		return strings.ReplaceAll(text, "postcondition", "wait")
	}
	// Nested maps can be shared with the internal evidence, which is read
	// after this call, so every level is copied, never edited in place.
	switch typed := value.(type) {
	case map[string]any:
		copied := make(map[string]any, len(typed))
		for key, item := range typed {
			copied[rename(key)] = renameAXWaitEvidence(item)
		}
		return copied
	case []map[string]any:
		copied := make([]map[string]any, len(typed))
		for index, item := range typed {
			copied[index], _ = renameAXWaitEvidence(item).(map[string]any)
		}
		return copied
	case []any:
		copied := make([]any, len(typed))
		for index, item := range typed {
			copied[index] = renameAXWaitEvidence(item)
		}
		return copied
	case string:
		return rename(typed)
	}
	return value
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
	if result.StructuredContent == nil {
		result.StructuredContent = map[string]any{}
	}
	full := result.StructuredContent
	if continuation, ok := full["continuation"].(map[string]any); ok {
		result = appendAXContinuationText(result, continuation)
	}
	compact := compactAXModelEvidence(full, result.IsError)
	if full["find_delivery_budget_exhausted"] == true {
		result.StructuredContent = compact
		result = boundFindBudgetFailure(result, fmt.Sprint(full["find_read_scope"]))
		compact = result.StructuredContent
	}
	// The public fields replace their internal spellings.
	delete(compact, "input_events_emitted")
	if batch, ok := compact["batch"].(map[string]any); ok {
		delete(batch, "actions_requested")
		delete(batch, "actions_completed_without_call_error")
		if len(batch) == 0 {
			delete(compact, "batch")
		}
	}
	if !result.IsError && axEffectVerified(full) {
		// A verified effect is not also unknown: a wait held, the target
		// visibly changed, or the closed window is gone.
		for _, key := range []string{"effect_unknown", "mutation_unknown", "cause_unattributed"} {
			delete(compact, key)
		}
	}
	compact, _ = renameAXWaitEvidence(compact).(map[string]any)
	for key, value := range axPublicEvidence(full, result.IsError, result.Content) {
		compact[key] = value
	}
	if message, _ := compact["message"].(string); message != "" && len(result.Content) > 0 &&
		result.Content[0].Type == "text" &&
		strings.TrimSpace(result.Content[0].Text) == message {
		// The message is already in the evidence line.
		result.Content = result.Content[1:]
	}
	result.StructuredContent = compact
	return result
}

// axEffectVerified reports whether the call's own reads verified its effect.
func axEffectVerified(full map[string]any) bool {
	return full["postcondition_verified"] == true ||
		full["wait_verified"] == true ||
		full["target_effect_observed"] == true ||
		full["close_effect_verified"] == true
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

// axPublicFields are the leading fields of the AX evidence line, in order.
var axPublicFields = []string{
	"ok", "code", "path", "message", "hint",
	"input_emitted", "completed", "requested",
}

// axPublicHints are the default corrected calls per error code.
var axPublicHints = map[string]string{
	axCodeStaleRef:       `{"mode":"find","window_ref":"wN","query":{"name_contains":"..."}}`,
	axCodeWindowChanged:  `{"mode":"observe","window_ref":"wN"}`,
	axCodeWaitTimeout:    `{"mode":"observe","window_ref":"wN"}`,
	axCodeEffectUnknown:  `{"mode":"observe","window_ref":"wN"}`,
	axCodeAppUnavailable: `{"mode":"apps"}`,
	axCodeRuntimeError:   `{"mode":"apps"}`,
}

// axPublicErrorCode maps internal failure facts to one public code.
func axPublicErrorCode(full map[string]any) string {
	if code, _ := full[axPublicErrorCodeKey].(string); code != "" {
		return code
	}
	failure, _ := full["failure_code"].(string)
	phase, _ := full["failure_phase"].(string)
	subject, _ := full["failure_subject"].(string)
	status, _ := full["assertion_status"].(string)
	switch {
	case failure == axCodeWindowChanged,
		failure == "stale_target" && (phase == "between_actions" || subject == "window"):
		return axCodeWindowChanged
	case failure == "wait_timeout":
		return axCodeWaitTimeout
	case failure == "effect_unknown":
		return axCodeEffectUnknown
	case failure == "ax_app_unavailable" || status == "app_unavailable":
		return axCodeAppUnavailable
	case failure == "ax_app_ambiguous":
		return axCodeInvalidArgument
	case strings.HasPrefix(failure, "stale_"), failure == "unknown_ref",
		failure == "semantic_target_not_found",
		failure == "semantic_target_ambiguous",
		failure == "invalid_target",
		status == "locator_stale", status == "locator_ambiguous",
		status == "invalid_target":
		return axCodeStaleRef
	}
	if axEvidenceReportsInputEmission(full) {
		if unknown, _ := full["mutation_unknown"].(bool); unknown {
			return axCodeEffectUnknown
		}
	}
	return axCodeRuntimeError
}

// axPublicEvidence returns the leading fields of the AX evidence line:
// ok, the public error code with path, message and hint, and input and
// progress counts.
func axPublicEvidence(full map[string]any, isError bool, content []contentItem) map[string]any {
	public := map[string]any{"ok": !isError}
	if isError {
		code := axPublicErrorCode(full)
		public["code"] = code
		if path, _ := full[axPublicErrorPathKey].(string); path != "" {
			public["path"] = path
		}
		message, _ := full[axPublicErrorDetailKey].(string)
		if message == "" {
			for _, item := range content {
				text := strings.TrimSpace(item.Text)
				// Window-resolution notes say where input went; they are
				// not the error.
				if item.Type != "text" || text == "" || isActReportText(text) ||
					strings.HasPrefix(text, "title ") || strings.HasPrefix(text, "bound_window: ") {
					continue
				}
				message = text
				break
			}
		}
		if message = firstLine(strings.TrimSpace(message)); message != "" {
			public["message"] = truncateRunes(message, 300)
		}
		hint, _ := full[axPublicErrorHintKey].(string)
		if hint == "" {
			hint = axPublicHints[code]
		}
		if hint != "" {
			public["hint"] = hint
		}
	}
	batch, _ := full["a11y_batch_result"].(map[string]any)
	public["input_emitted"] = axEvidenceReportsInputEmission(full)
	// Batch counts live in a11y_batch_result, or at the top level on a
	// stop between actions.
	for _, source := range []map[string]any{batch, full} {
		if source == nil {
			continue
		}
		if requested, ok := intEvidence(source["actions_requested"]); ok {
			public["requested"] = requested
			completed, _ := intEvidence(source["actions_completed_without_call_error"])
			public["completed"] = completed
			break
		}
	}
	return public
}
