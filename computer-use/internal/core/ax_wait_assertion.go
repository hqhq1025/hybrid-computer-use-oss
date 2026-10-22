package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"

	"github.com/hqhq1025/hybrid-computer-use-oss/computer-use/internal/contract"
)

type axAssertionSpec struct {
	assertionType string
	targetAddress string
	timeoutMS     int
	stableMS      int
	goal          verifyGoal
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

func parseAXAssertionSpec(assertion map[string]any) (*axAssertionSpec, error) {
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
	if timeoutMS > contract.MaxVerifyTimeout {
		return nil, fmt.Errorf("timeout_ms must be at most %d", contract.MaxVerifyTimeout)
	}
	stable, err := optionalNonNegativeInt(assertion, "stable_ms", 5000)
	if err != nil {
		return nil, err
	}
	stableMS := 0
	if stable != nil {
		stableMS = *stable
	}
	assertionType, _ := assertion["type"].(string)
	allowed := map[string]bool{
		"type": true, "stable_ms": true, "timeout_ms": true,
	}
	spec := &axAssertionSpec{
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
		if strings.TrimSpace(OptionalString(assertion, "state")) == "" ||
			optionalBool(assertion, "value") == nil {
			return nil, errors.New(
				`assertion type "state" requires non-empty state and boolean value`,
			)
		}
	case "value":
		allowed["target"] = true
		allowed["value"] = true
		allowed["match"] = true
		allowed["negate"] = true
		_, kind, valid := assertionScalarString(assertion, "value")
		if !valid {
			return nil, errors.New(
				`assertion type "value" requires a string, number, or boolean value`,
			)
		}
		match := "equals"
		if raw, present := assertion["match"]; present {
			match, _ = raw.(string)
		}
		switch match {
		case "equals":
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
		allowed["negate"] = true
		if strings.TrimSpace(OptionalString(assertion, "text")) == "" {
			return nil, errors.New(
				`assertion type "text_contains" requires non-empty text`,
			)
		}
	case "window_title_contains":
		allowed["text"] = true
		allowed["exists"] = true
		if strings.TrimSpace(OptionalString(assertion, "text")) == "" {
			return nil, errors.New(
				"assertion.text must contain at least one non-whitespace character",
			)
		}
		if _, present := assertion["exists"]; present &&
			optionalBool(assertion, "exists") == nil {
			return nil, errors.New(
				`assertion type "window_title_contains" exists must be true or false`,
			)
		}
	default:
		return nil, errors.New(
			"assertion.type must be exists, state, value, text_contains, " +
				"or window_title_contains",
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
		targetAddress, _, targetErr := axTargetAddress(assertion)
		if targetErr != nil {
			return nil, fmt.Errorf("assertion.target: %w", targetErr)
		}
		if strings.TrimSpace(targetAddress) == "" {
			return nil, errors.New("assertion.target is required")
		}
		spec.targetAddress = targetAddress
	}
	spec.goal = assertionSpecGoal(spec.assertionType, assertion)
	return spec, nil
}

func axPostconditionPassed(result ToolCallResult) bool {
	if result.IsError || result.StructuredContent == nil {
		return false
	}
	return strings.TrimSpace(fmt.Sprint(
		result.StructuredContent["assertion_status"],
	)) == "pass"
}

// assertionGoal returns the predicate of a published assertion object.
func assertionGoal(assertion map[string]any) verifyGoal {
	spec, err := parseAXAssertionSpec(assertion)
	if err != nil {
		return verifyGoal{}
	}
	return spec.goal
}

// assertionSpecGoal builds the predicate from the normalized assertion type;
// "value" has already been resolved to value_equals or value_contains.
func assertionSpecGoal(assertionType string, assertion map[string]any) verifyGoal {
	goal := verifyGoal{}
	if negate := optionalBool(assertion, "negate"); negate != nil {
		goal.negated = *negate
	}
	switch assertionType {
	case "exists":
		goal.exists = optionalBool(assertion, "value")
	case "state":
		state := OptionalString(assertion, "state")
		if expected := optionalBool(assertion, "value"); expected != nil && !*expected {
			state = "!" + state
		}
		goal.state = state
	case "value_contains":
		goal.valueContains = OptionalString(assertion, "value")
	case "value_equals":
		goal.valueEquals, goal.valueEqualsKind, _ =
			assertionScalarString(assertion, "value")
	case "text_contains":
		goal.textContains = OptionalString(assertion, "text")
	}
	return goal
}

func axPostconditionNeedsCanonicalReconciliation(
	assertion map[string]any,
	assertionResult ToolCallResult,
) bool {
	if !assertionResult.IsError || assertionResult.StructuredContent == nil {
		return false
	}
	_, underlyingCode := classifyAXPostconditionFailure(assertionResult)
	spec, err := parseAXAssertionSpec(assertion)
	return err == nil &&
		underlyingCode == "observation_incomplete" &&
		spec.stableMS == 0 &&
		isShortAXRef(spec.targetAddress)
}

func (s *Service) reconcileAXActionPostcondition(
	context *AXBatchContext,
	assertion map[string]any,
	assertionResult ToolCallResult,
) ToolCallResult {
	if s == nil || context == nil ||
		!axPostconditionNeedsCanonicalReconciliation(
			assertion,
			assertionResult,
		) {
		return assertionResult
	}
	spec, _ := parseAXAssertionSpec(assertion)
	expected, err := s.RefRegistry.Resolve(spec.targetAddress, "")
	if err != nil {
		return assertionResult
	}
	expectedIdentity := *expected
	noScreenshot := false
	snapshot, notes, observationResult := s.refreshSnapshot(
		context.AppToken,
		AXServiceRequest{
			Operation:          axServiceObserve,
			App:                context.AppToken,
			IncludeScreenshot:  &noScreenshot,
			TargetWindowHandle: context.WindowHandle,
			SourceRef:          context.WindowRef,
			Scope:              contract.AXScopeWindow,
			TreeView:           contract.TreeViewWorking,
		},
	)
	if observationResult.IsError || snapshot == nil {
		return assertionResult
	}
	return s.reconcileAXPostconditionFromCanonicalSnapshot(
		assertion,
		assertionResult,
		snapshot,
		notes,
		&expectedIdentity,
	)
}

func (s *Service) reconcileAXPostconditionFromCanonicalSnapshot(
	assertion map[string]any,
	assertionResult ToolCallResult,
	snapshot *AppSnapshot,
	notes []string,
	expected *AXRefEntry,
) ToolCallResult {
	spec, err := parseAXAssertionSpec(assertion)
	if err != nil || snapshot == nil {
		return assertionResult
	}
	if expected == nil {
		expected, err = s.RefRegistry.Resolve(spec.targetAddress, "")
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
			snapshot.AXSnapshot == nil ||
			!snapshot.AXSnapshot.RefInventoryComplete ||
			snapshot.WindowHandle != expected.WindowHandle ||
			snapshot.publicWindowRef != expected.LogicalWindowRef ||
			snapshot.publicAppRef != expected.AppRef ||
			axSnapshotRefCount(snapshot.AXSnapshot.CanonicalRoots, spec.targetAddress) != 0 {
			return assertionResult
		}
		record = nil
	}
	goal := assertionGoal(assertion)
	ok, observed := goal.check(record, nil)
	reconciled := ToolCallResult{}
	if ok {
		reconciled = axAssertionResult(
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
				"wait false: %s did not satisfy %s in the "+
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

func (s *Service) preflightAXPostcondition(
	context *AXBatchContext,
	assertion map[string]any,
) error {
	spec, err := parseAXAssertionSpec(assertion)
	if err != nil {
		return err
	}
	if context == nil || strings.TrimSpace(context.WindowHandle) == "" {
		return errors.New("batch window identity is unavailable")
	}
	if spec.assertionType == "window_title_contains" {
		if strings.TrimSpace(context.WindowRef) == "" {
			return errors.New(
				"a title wait needs the window of the act call",
			)
		}
		return nil
	}
	if numericElementIndex(spec.targetAddress) {
		return contract.NewArgumentRuleError(contract.ArgumentRulePostconditionIndex,
			"numeric indices are not stable across mutations; use rN or an exact read-only selector",
		)
	}
	if isShortAXWindowRef(spec.targetAddress) {
		return errors.New(
			`wN identifies a window; for a window use {"type":"title","text":"..."}`,
		)
	}
	if !isShortAXRef(spec.targetAddress) {
		return nil
	}
	entry, err := s.resolveShortAXRefOwned(spec.targetAddress)
	if err != nil {
		return err
	}
	if entry.WindowHandle != context.WindowHandle {
		return fmt.Errorf(
			"target %s belongs to a different logical window",
			spec.targetAddress,
		)
	}
	return nil
}

// runAXPostcondition checks a predicate after a batch's earlier steps. A
// ref predicate reads that element. A selector or title predicate reads the
// application that owns the batch window, starting from that window, so a
// window the action closed or a dialog it opened does not hide the answer.
// The read never re-binds input.
func (s *Service) runAXPostcondition(
	context *AXBatchContext,
	assertion map[string]any,
) ToolCallResult {
	readContext := &axReadContext{}
	spec, _ := parseAXAssertionSpec(assertion)
	if spec != nil && isShortAXRef(spec.targetAddress) {
		if entry, err := s.resolveShortAXRefOwned(spec.targetAddress); err == nil {
			readContext.appToken = entry.AppRef
			readContext.windowRef = entry.LogicalWindowRef
			readContext.windowHandle = entry.WindowHandle
		}
	} else if spec != nil {
		readContext.appRef = context.AppRef
		readContext.appToken = FirstNonEmpty(context.AppRef, context.AppToken)
		readContext.windowRef = context.WindowRef
		readContext.windowHandle = context.WindowHandle
	}
	if readContext.appToken == "" {
		readContext.appToken = context.AppToken
	}
	return s.runAXAssertion(readContext, assertion, contract.AXScopeWindow)
}

func classifyAXPostconditionFailure(
	result ToolCallResult,
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
