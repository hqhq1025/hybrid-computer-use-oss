package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
)

type a11yAssertionSpec struct {
	assertionType string
	targetAddress string
	timeoutMS     int
	stableMS      int
}

func assertionScalarString(
	assertion map[string]any,
	key string,
) (string, string, bool) {
	raw, present := assertion[key]
	if !present || raw == nil {
		return "", "", false
	}
	switch value := raw.(type) {
	case string:
		if strings.TrimSpace(value) == "" {
			return "", "", false
		}
		return value, "string", true
	case json.Number:
		if _, ok := new(big.Rat).SetString(value.String()); !ok {
			return "", "", false
		}
		return value.String(), "number", true
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return "", "", false
		}
		return strconv.FormatFloat(value, 'f', -1, 64), "number", true
	case bool:
		return strconv.FormatBool(value), "boolean", true
	default:
		return "", "", false
	}
}

func parseA11yAssertionSpec(assertion map[string]any) (*a11yAssertionSpec, error) {
	if assertion == nil {
		return nil, errors.New("assertion must be an object")
	}
	timeout, err := optionalPositiveInt(assertion, "timeout_ms")
	if err != nil {
		return nil, err
	}
	timeoutMS := 5000
	if timeout != nil {
		timeoutMS = *timeout
	}
	if timeoutMS > maxVerifyTimeout {
		return nil, fmt.Errorf("timeout_ms must be at most %d", maxVerifyTimeout)
	}
	stable, err := optionalNonNegativeInt(assertion, "stable_ms", 5000)
	if err != nil {
		return nil, err
	}
	stableMS := 0
	if stable != nil {
		stableMS = *stable
	}
	assertionType := strings.ToLower(strings.TrimSpace(
		requiredString(assertion, "type"),
	))
	allowed := map[string]bool{
		"type": true, "stable_ms": true, "timeout_ms": true,
	}
	spec := &a11yAssertionSpec{
		assertionType: assertionType,
		timeoutMS:     timeoutMS,
		stableMS:      stableMS,
	}
	switch assertionType {
	case "exists":
		allowed["target"] = true
		allowed["value"] = true
		if optionalBool(assertion, "value") == nil {
			return nil, errors.New(
				`assertion type "exists" requires boolean value`,
			)
		}
	case "state":
		allowed["target"] = true
		allowed["state"] = true
		allowed["value"] = true
		if strings.TrimSpace(optionalString(assertion, "state")) == "" ||
			optionalBool(assertion, "value") == nil {
			return nil, errors.New(
				`assertion type "state" requires non-empty state and boolean value`,
			)
		}
	case "value_contains":
		allowed["target"] = true
		allowed["value"] = true
		_, kind, valid := assertionScalarString(assertion, "value")
		if !valid || kind != "string" {
			return nil, errors.New(
				`assertion type "value_contains" requires a non-empty string value`,
			)
		}
	case "value_equals":
		allowed["target"] = true
		allowed["value"] = true
		if _, _, valid := assertionScalarString(assertion, "value"); !valid {
			return nil, errors.New(
				`assertion type "value_equals" requires a string, number, or boolean value`,
			)
		}
	case "value":
		allowed["target"] = true
		allowed["value"] = true
		allowed["match"] = true
		_, kind, valid := assertionScalarString(assertion, "value")
		if !valid {
			return nil, errors.New(
				`assertion type "value" requires a string, number, or boolean value`,
			)
		}
		match := strings.ToLower(strings.TrimSpace(
			optionalString(assertion, "match"),
		))
		switch match {
		case "", "equals":
			spec.assertionType = "value_equals"
		case "contains":
			if kind != "string" {
				return nil, errors.New(
					`assertion type "value" with match "contains" requires a string value`,
				)
			}
			spec.assertionType = "value_contains"
		default:
			return nil, errors.New(
				`assertion type "value" match must be "equals" or "contains"`,
			)
		}
	case "text_contains":
		allowed["target"] = true
		allowed["text"] = true
		if strings.TrimSpace(optionalString(assertion, "text")) == "" {
			return nil, errors.New(
				`assertion type "text_contains" requires non-empty text`,
			)
		}
	case "window_title_contains":
		allowed["text"] = true
		allowed["exists"] = true
		if strings.TrimSpace(optionalString(assertion, "text")) == "" {
			return nil, errors.New(
				"assertion.text must contain at least one non-whitespace character",
			)
		}
	default:
		return nil, errors.New(
			"assertion.type must be exists, state, value, value_equals, value_contains, " +
				"text_contains, or window_title_contains",
		)
	}
	unexpected := make([]string, 0)
	for field := range assertion {
		if !allowed[field] {
			unexpected = append(unexpected, field)
		}
	}
	sort.Strings(unexpected)
	if len(unexpected) > 0 {
		return nil, fmt.Errorf(
			"assertion type %q has unsupported field(s): %s",
			assertionType,
			strings.Join(unexpected, ", "),
		)
	}
	if assertionType != "window_title_contains" {
		targetAddress, _, targetErr := a11yTargetAddress(assertion)
		if targetErr != nil {
			return nil, fmt.Errorf("assertion.target: %w", targetErr)
		}
		if strings.TrimSpace(targetAddress) == "" {
			return nil, errors.New("assertion.target is required")
		}
		spec.targetAddress = targetAddress
	}
	return spec, nil
}

func a11yPostconditionPassed(result toolCallResult) bool {
	if result.IsError || result.StructuredContent == nil {
		return false
	}
	return strings.TrimSpace(fmt.Sprint(
		result.StructuredContent["assertion_status"],
	)) == "pass"
}

func (s *service) stopA11yBatchForActionPostcondition(
	context *a11yBatchContext,
	index,
	count int,
	action a11yBatchAction,
	actionEvidence []map[string]any,
	actionResult,
	postconditionResult toolCallResult,
	obs observation,
	hasBatchPostcondition bool,
) toolCallResult {
	classification, underlying :=
		classifyA11yPostconditionFailure(postconditionResult)
	stopped := actionResult
	stopped.IsError = true
	stopped.Content = append(stopped.Content, contentItem{
		Type: "text",
		Text: fmt.Sprintf(
			"action %d postcondition: %s",
			index+1,
			firstLine(resultText(postconditionResult)),
		),
	})
	if stopped.StructuredContent == nil {
		stopped.StructuredContent = map[string]any{}
	}
	stopped.StructuredContent["failure_code"] =
		"action_" + classification
	failurePhase := "action_postcondition"
	if index+1 < count {
		failurePhase = "between_actions"
	}
	stopped.StructuredContent["failure_phase"] = failurePhase
	stopped.StructuredContent["action_postcondition_failure_code"] =
		underlying
	stopped.StructuredContent["action_postcondition_verified"] = false
	stopped.StructuredContent["retry_same_request"] = false
	stopped.StructuredContent["task_effect_verified"] = false

	finalObservation := toolCallResult{}
	if obs.resolvedMode() != observeNone {
		if postconditionResult.axProofSnapshot != nil {
			finalObservation = s.renderA11yBatchFinalObservation(
				postconditionResult.axProofSnapshot,
				postconditionResult.axProofNotes,
				obs,
			)
		} else {
			finalObservation = s.a11yBatchFinalObservation(
				context,
				obs,
				action.actionType == "close",
			)
		}
		stopped = mergeA11yBatchFinalObservation(
			stopped,
			finalObservation,
		)
		finalObservationReturned, _ :=
			stopped.StructuredContent["final_observation_returned"].(bool)
		if !finalObservationReturned {
			stopped = s.recoverA11yBatchObservation(
				context,
				stopped,
				obs,
			)
		}
	}
	finalObservationReturned, _ :=
		stopped.StructuredContent["final_observation_returned"].(bool)
	stopped.StructuredContent["observation_required"] =
		!finalObservationReturned
	stopped.StructuredContent["next_identical_observation_required"] =
		!finalObservationReturned
	continuationState := "observation_required"
	continuationRoute := "observe"
	if classification == "postcondition_not_satisfied" {
		continuationState = "predicate_false"
	} else if finalObservationReturned {
		continuationState = "inspect_current_state"
	}
	if finalObservationReturned {
		continuationRoute = "continue_from_observation"
	}
	stopped.StructuredContent["continuation"] = map[string]any{
		"state":              continuationState,
		"retry_same_request": false,
		"route":              continuationRoute,
	}

	anyInputEmitted, anyPartialPossible, anyMutationUnknown :=
		batchMutationSummary(actionEvidence)
	facts := map[string]any{
		"actions_requested":                    count,
		"actions_completed_without_call_error": index + 1,
		"guard_failed_action_index":            index,
		"guard_failed_action_number":           index + 1,
		"guard_failed_action_type":             action.actionType,
		"stopped_before_action_index":          index + 1,
		"rejected_before_first_action":         false,
		"partial_sequence":                     index+1 < count,
		"final_observation_returned":           finalObservationReturned,
		"actions":                              actionEvidence,
		"any_input_emitted":                    anyInputEmitted,
		"partial_input_possible":               anyPartialPossible,
		"mutation_unknown":                     anyMutationUnknown,
		"action_postcondition": compactA11yPostconditionEvidence(
			action.postcondition,
			postconditionResult,
		),
	}
	if index+1 >= count {
		facts["stopped_before_action_index"] = nil
	}
	if hasBatchPostcondition {
		facts["postcondition"] = map[string]any{
			"status": "not_checked",
		}
	}
	return prependA11yBatchSummary(
		stopped,
		fmt.Sprintf(
			"A11Y BATCH STOPPED AFTER ACTION %d/%d (%s): its declared "+
				"postcondition did not pass, so %d later action(s) were not emitted. "+
				"A fresh current-state observation is included when available.",
			index+1,
			count,
			action.actionType,
			count-index-1,
		),
		facts,
	)
}

func assertionGoal(assertion map[string]any) verifyGoal {
	goal := verifyGoal{}
	switch strings.ToLower(strings.TrimSpace(requiredString(assertion, "type"))) {
	case "exists":
		goal.exists = optionalBool(assertion, "value")
	case "state":
		state := strings.TrimSpace(optionalString(assertion, "state"))
		if expected := optionalBool(assertion, "value"); expected != nil && !*expected {
			state = "!" + state
		}
		goal.state = state
	case "value_contains":
		goal.valueContains = optionalString(assertion, "value")
	case "value_equals":
		goal.valueEquals, goal.valueEqualsKind, _ =
			assertionScalarString(assertion, "value")
	case "text_contains":
		goal.textContains = optionalString(assertion, "text")
	}
	return goal
}

func a11yPostconditionNeedsCanonicalReconciliation(
	assertion map[string]any,
	assertionResult toolCallResult,
) bool {
	if !assertionResult.IsError || assertionResult.StructuredContent == nil {
		return false
	}
	_, underlyingCode := classifyA11yPostconditionFailure(assertionResult)
	spec, err := parseA11yAssertionSpec(assertion)
	return err == nil &&
		underlyingCode == "observation_incomplete" &&
		spec.stableMS == 0 &&
		isShortAXRef(spec.targetAddress)
}

func (s *service) reconcileA11yActionPostcondition(
	context *a11yBatchContext,
	assertion map[string]any,
	assertionResult toolCallResult,
) toolCallResult {
	if s == nil || context == nil ||
		!a11yPostconditionNeedsCanonicalReconciliation(
			assertion,
			assertionResult,
		) {
		return assertionResult
	}
	spec, _ := parseA11yAssertionSpec(assertion)
	expected, err := s.refRegistry.Resolve(spec.targetAddress, "")
	if err != nil {
		return assertionResult
	}
	expectedIdentity := *expected
	noScreenshot := false
	snapshot, notes, observationResult := s.refreshSnapshot(
		context.appToken,
		axServiceRequest{
			Operation:          axServiceObserve,
			App:                context.appToken,
			IncludeScreenshot:  &noScreenshot,
			TargetWindowHandle: context.windowHandle,
			SourceRef:          context.windowRef,
			Scope:              a11yScopeWindow,
			TreeView:           treeViewWorking,
		},
	)
	if observationResult.IsError || snapshot == nil {
		return assertionResult
	}
	return s.reconcileA11yPostconditionFromCanonicalSnapshot(
		assertion,
		assertionResult,
		snapshot,
		notes,
		&expectedIdentity,
	)
}

func (s *service) reconcileA11yPostconditionFromFinalObservation(
	assertion map[string]any,
	assertionResult,
	finalObservation toolCallResult,
) toolCallResult {
	if !assertionResult.IsError ||
		assertionResult.StructuredContent == nil ||
		finalObservation.IsError ||
		finalObservation.axProofSnapshot == nil {
		return assertionResult
	}
	if !a11yPostconditionNeedsCanonicalReconciliation(
		assertion,
		assertionResult,
	) {
		return assertionResult
	}
	reconciled := s.reconcileA11yPostconditionFromCanonicalSnapshot(
		assertion,
		assertionResult,
		finalObservation.axProofSnapshot,
		finalObservation.axProofNotes,
		nil,
	)
	if reconciled.StructuredContent != nil &&
		reconciled.StructuredContent["postcondition_reconciled_by_canonical_observation"] == true {
		reconciled.StructuredContent["postcondition_recovered_by_final_observation"] = true
	}
	return reconciled
}

func (s *service) reconcileA11yPostconditionFromCanonicalSnapshot(
	assertion map[string]any,
	assertionResult toolCallResult,
	snapshot *appSnapshot,
	notes []string,
	expected *AXRefEntry,
) toolCallResult {
	spec, err := parseA11yAssertionSpec(assertion)
	if err != nil || snapshot == nil {
		return assertionResult
	}
	if expected == nil {
		expected, err = s.refRegistry.Resolve(spec.targetAddress, "")
		if err != nil {
			return assertionResult
		}
	}
	record, recordErr := s.exactRefRecordFromSnapshot(
		snapshot,
		spec.targetAddress,
		expected,
	)
	if recordErr != nil {
		if !strings.HasPrefix(recordErr.Error(), "observation_incomplete:") ||
			snapshot.axSnapshot == nil ||
			!snapshot.axSnapshot.RefInventoryComplete ||
			snapshot.WindowHandle != expected.WindowHandle ||
			snapshot.publicWindowRef != expected.LogicalWindowRef ||
			snapshot.publicAppRef != expected.AppRef ||
			axSnapshotRefCount(snapshot.axSnapshot.CanonicalRoots, spec.targetAddress) != 0 {
			return assertionResult
		}
		record = nil
	}
	goal := assertionGoal(assertion)
	ok, observed := goal.check(record, nil)
	reconciled := toolCallResult{}
	if ok {
		reconciled = a11yAssertionResult(
			fmt.Sprintf(
				"PASS: %s %s in the action's fresh observation. %s",
				spec.targetAddress,
				goal.describe(),
				observed,
			),
			false,
			"pass",
			1,
			0,
		)
	} else {
		reconciled = assertionNegativeResult(
			fmt.Sprintf(
				"assert_ax predicate_false: %s did not satisfy %s in the "+
					"action's fresh observation. Last observation: %s",
				spec.targetAddress,
				goal.describe(),
				observed,
			),
			"predicate_false",
			1,
			0,
		)
	}
	reconciled.StructuredContent["postcondition_reconciled_by_canonical_observation"] = true
	reconciled.StructuredContent["cause_unattributed"] = true
	reconciled.axProofSnapshot = snapshot
	reconciled.axProofNotes = append([]string(nil), notes...)
	return reconciled
}

func axSnapshotRefCount(roots []*AXNode, ref string) int {
	count := 0
	var visit func([]*AXNode)
	visit = func(nodes []*AXNode) {
		for _, node := range nodes {
			if node == nil {
				continue
			}
			if node.Ref == ref {
				count++
			}
			visit(node.Children)
		}
	}
	visit(roots)
	return count
}

func (s *service) preflightA11yPostcondition(
	context *a11yBatchContext,
	assertion map[string]any,
) error {
	spec, err := parseA11yAssertionSpec(assertion)
	if err != nil {
		return err
	}
	if context == nil || strings.TrimSpace(context.windowHandle) == "" {
		return errors.New("batch window identity is unavailable")
	}
	if spec.assertionType == "window_title_contains" {
		if strings.TrimSpace(context.windowRef) == "" {
			return errors.New(
				"window_title_contains requires a current wN batch window",
			)
		}
		return nil
	}
	if numericElementIndex(spec.targetAddress) {
		return errors.New(
			"numeric indices are not stable across mutations; use rN or an exact read-only selector",
		)
	}
	if isShortAXWindowRef(spec.targetAddress) {
		return errors.New(
			"wN identifies a window; use window_title_contains for a window postcondition",
		)
	}
	if !isShortAXRef(spec.targetAddress) {
		return nil
	}
	entry, err := s.resolveShortAXRefOwned(spec.targetAddress)
	if err != nil {
		return err
	}
	if entry.WindowHandle != context.windowHandle {
		return fmt.Errorf(
			"target %s belongs to a different logical window",
			spec.targetAddress,
		)
	}
	return nil
}

func (s *service) runA11yPostcondition(
	context *a11yBatchContext,
	assertion map[string]any,
	targetWindowClosed bool,
) toolCallResult {
	args := map[string]any{
		"mode":      a11yModeAssert,
		"assertion": assertion,
	}
	spec, _ := parseA11yAssertionSpec(assertion)
	if spec != nil &&
		(spec.assertionType == "window_title_contains" ||
			!isShortAXRef(spec.targetAddress)) {
		windowTitleExpectedAbsent := spec.assertionType == "window_title_contains" &&
			optionalBool(assertion, "exists") != nil &&
			!*optionalBool(assertion, "exists")
		if targetWindowClosed && windowTitleExpectedAbsent &&
			context.appRef != "" {
			// The closed wN is intentionally stale after a verified close.
			// Re-enumerate the owning application instead of trying to read
			// through the vanished window identity. This still detects a
			// same-title sibling and therefore does not turn exact-window
			// disappearance into a broader title-absence claim.
			args["app_ref"] = context.appRef
		} else if context.windowRef != "" {
			args["window_ref"] = context.windowRef
		} else if context.appRef != "" {
			args["app_ref"] = context.appRef
		}
	}
	result := s.a11yRequest(args, observation{})
	if negativeSelectorPostconditionProvenByGoneWindow(
		spec,
		assertion,
		result,
	) {
		verified := a11yAssertionResult(
			"PASS: the exact selector is absent because its bound logical "+
				"window no longer exists and the live window inventory is complete.",
			false,
			"pass",
			1,
			0,
		)
		verified.StructuredContent["source_window_status"] = "gone"
		verified.StructuredContent["recovery_inventory_complete"] = true
		verified.StructuredContent["window_transition_proved_absence"] = true
		return verified
	}
	return result
}

func negativeSelectorPostconditionProvenByGoneWindow(
	spec *a11yAssertionSpec,
	assertion map[string]any,
	result toolCallResult,
) bool {
	if spec == nil ||
		spec.assertionType != "exists" ||
		spec.stableMS != 0 ||
		isShortAXRef(spec.targetAddress) ||
		optionalBool(assertion, "value") == nil ||
		*optionalBool(assertion, "value") ||
		!result.IsError ||
		result.StructuredContent == nil {
		return false
	}
	code, _ := result.StructuredContent["failure_code"].(string)
	sourceStatus, _ := result.StructuredContent["source_window_status"].(string)
	inventoryComplete, _ :=
		result.StructuredContent["recovery_inventory_complete"].(bool)
	return code == "stale_window_ref" &&
		sourceStatus == "gone" &&
		inventoryComplete
}

func mergeA11yPostconditionResult(
	actionResult,
	assertionResult toolCallResult,
) toolCallResult {
	if assertionResult.StructuredContent == nil {
		return actionResult
	}
	status := strings.TrimSpace(fmt.Sprint(
		assertionResult.StructuredContent["assertion_status"],
	))
	postconditionText := firstLine(resultText(assertionResult))
	if !assertionResult.IsError && status != "pass" {
		postconditionText = "assertion_passed=false retry_same_request=false"
	}
	actionResult.Content = append(actionResult.Content, contentItem{
		Type: "text",
		Text: "postcondition: " + postconditionText,
	})
	if actionResult.StructuredContent == nil {
		actionResult.StructuredContent = map[string]any{}
	}
	actionResult.StructuredContent["postcondition_verified"] =
		!assertionResult.IsError && status == "pass"
	if !assertionResult.IsError && status == "pass" {
		preserveUnattributedPostconditionEvidence(
			actionResult.StructuredContent,
			nil,
		)
	}
	if recovered, _ := assertionResult.StructuredContent["postcondition_recovered_by_final_observation"].(bool); recovered {
		actionResult.StructuredContent["postcondition_recovered_by_final_observation"] = true
		actionResult.StructuredContent["cause_unattributed"] = true
	}
	inputEmitted := axEvidenceReportsInputEmission(
		actionResult.StructuredContent,
	)
	if inputEmitted {
		actionResult.StructuredContent["input_events_emitted"] = true
	}
	if inputEmitted {
		actionResult.StructuredContent["preflight_only"] = false
		actionResult.StructuredContent["evidence_scope"] =
			"post_action_observation"
	}
	if assertionResult.IsError {
		classification, underlying := classifyA11yPostconditionFailure(
			assertionResult,
		)
		actionResult.IsError = true
		actionResult.StructuredContent["failure_code"] = classification
		actionResult.StructuredContent["failure_phase"] = "postcondition"
		actionResult.StructuredContent["postcondition_failure_code"] = underlying
		if inputEmitted {
			actionResult.StructuredContent["input_events_emitted"] = true
			actionResult.StructuredContent["mutation_unknown"] = true
			actionResult.StructuredContent["preflight_only"] = false
			actionResult.StructuredContent["evidence_scope"] =
				"post_action_observation"
		}
	}
	if inputEmitted || axEvidenceReportsInputEmission(actionResult.StructuredContent) {
		actionResult.StructuredContent["preflight_only"] = false
		if unknown, _ := actionResult.StructuredContent["mutation_unknown"].(bool); unknown {
			actionResult.StructuredContent["evidence_scope"] =
				"post_action_observation"
		}
	}
	return actionResult
}

func preserveUnattributedPostconditionEvidence(
	structuredContent map[string]any,
	actionEvidence map[string]any,
) {
	if structuredContent == nil {
		return
	}
	if structuredContent["task_effect_verified"] == true ||
		!axEvidenceReportsInputEmission(structuredContent) {
		return
	}
	structuredContent["effect_unknown"] = true
	structuredContent["mutation_unknown"] = true
	structuredContent["cause_unattributed"] = true
	if actionEvidence != nil {
		actionEvidence["mutation_unknown"] = true
		actionEvidence["cause_unattributed"] = true
	}
}

func classifyA11yPostconditionFailure(
	result toolCallResult,
) (classification, underlying string) {
	if result.StructuredContent == nil {
		return "postcondition_unknown", "missing_assertion_evidence"
	}
	underlying, _ = result.StructuredContent["failure_code"].(string)
	underlying = strings.TrimSpace(underlying)
	if underlying == "" {
		underlying, _ = result.StructuredContent["assertion_status"].(string)
		underlying = strings.TrimSpace(underlying)
	}
	if underlying == "" {
		underlying = "unknown_assertion_failure"
	}
	sampled, _ := result.StructuredContent["assertion_sampled_live_ui"].(bool)
	switch underlying {
	case "predicate_false", "predicate_unstable", "locator_missing",
		"window_title_timeout":
		if sampled {
			return "postcondition_not_satisfied", underlying
		}
	}
	return "postcondition_unknown", underlying
}

func compactA11yPostconditionEvidence(
	assertion map[string]any,
	result toolCallResult,
) any {
	if assertion == nil {
		return nil
	}
	if result.StructuredContent == nil {
		return map[string]any{"status": "unknown"}
	}
	status := strings.TrimSpace(fmt.Sprint(
		result.StructuredContent["assertion_status"],
	))
	if status == "" {
		status = "unknown"
	}
	evidence := map[string]any{
		"status":   status,
		"verified": !result.IsError && status == "pass",
	}
	if recovered, _ := result.StructuredContent["postcondition_recovered_by_final_observation"].(bool); recovered {
		evidence["recovered_by_final_observation"] = true
		evidence["cause_unattributed"] = true
	}
	if reconciled, _ := result.StructuredContent["postcondition_reconciled_by_canonical_observation"].(bool); reconciled {
		evidence["reconciled_by_canonical_observation"] = true
		evidence["cause_unattributed"] = true
	}
	if result.IsError {
		classification, underlying := classifyA11yPostconditionFailure(result)
		evidence["failure_code"] = classification
		evidence["underlying_failure_code"] = underlying
	}
	return evidence
}
