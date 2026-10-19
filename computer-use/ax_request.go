package main

import (
	"errors"
	"fmt"
	"strings"
)

type a11yReadContext struct {
	appToken     string
	appRef       string
	windowRef    string
	windowHandle string
}

const (
	a11yModeObserve = "observe"
	a11yModeFind    = "find"
	a11yModeAct     = "act"
	// a11yModeAssert names the internal predicate check that observe+until
	// and act until run. It is not a public mode.
	a11yModeAssert = "assert_ax"
)

// parseA11yMode returns the public mode of a call, or an error when it is
// missing or unknown. Only exact values are accepted.
func parseA11yMode(args map[string]any) (string, error) {
	mode, _ := args["mode"].(string)
	switch mode {
	case a11yModeApps, a11yModeObserve, a11yModeFind, a11yModeAct:
		return mode, nil
	}
	return "", errors.New("mode must be apps, observe, find, or act")
}

// resolveA11yReadIdentity binds a read to one app or window. app accepts an
// application name, app id, PID, or an aN ref from apps; window_ref names
// one window. A ref-owned until target supplies the identity when neither is
// given.
func (s *service) resolveA11yReadIdentity(
	app, windowRef, target string,
) (*a11yReadContext, error) {
	context := &a11yReadContext{}
	if isShortAXRef(target) {
		entry, err := s.resolveShortAXRefOwned(target)
		if err != nil {
			return nil, err
		}
		context.appRef = entry.AppRef
		context.windowRef = entry.LogicalWindowRef
		context.windowHandle = entry.WindowHandle
		if windowRef != "" && entry.LogicalWindowRef != windowRef {
			return nil, newArgumentRuleError(argumentRuleRefOwnsIdentity, fmt.Sprintf(
				"%s belongs to window %s, not %s; omit window_ref or use %s",
				target, entry.LogicalWindowRef, windowRef, entry.LogicalWindowRef,
			))
		}
	}
	if windowRef != "" {
		entry, err := s.resolveShortAXWindowRefOwned(windowRef)
		if err != nil {
			return nil, err
		}
		context.appRef = entry.AppRef
		context.windowRef = entry.Ref
		context.windowHandle = entry.WindowHandle
		context.appToken = entry.AppRef
		if context.appToken == "" {
			context.appToken = entry.AppIdentity
		}
		return context, nil
	}
	if isShortAXAppRef(app) {
		if _, err := s.resolveShortAXAppRef(app); err != nil {
			return nil, err
		}
		if context.appRef != "" && context.appRef != app {
			return nil, newArgumentRuleError(argumentRuleRefOwnsIdentity,
				fmt.Sprintf("%s belongs to app %s, not %s", target, context.appRef, app))
		}
		context.appRef = app
		context.appToken = app
		return context, nil
	}
	if context.appRef != "" {
		if app != "" {
			return nil, newArgumentRuleError(argumentRuleRefOwnsIdentity,
				target+" already identifies its app; omit app")
		}
		context.appToken = context.appRef
		return context, nil
	}
	if app == "" {
		return nil, errors.New("app or window_ref is required")
	}
	context.appToken = app
	return context, nil
}

func withA11yModeEvidence(
	result toolCallResult,
	mode string,
) toolCallResult {
	if result.StructuredContent == nil {
		result.StructuredContent = map[string]any{}
	}
	result.StructuredContent["ax_mode"] = mode
	return result
}

func (s *service) a11yRequest(
	args map[string]any,
	_ observation,
) toolCallResult {
	request, argErr := decodeAXRequest(args)
	if argErr != nil {
		requested := 0
		if actions, ok := args["actions"].([]any); ok {
			requested = len(actions)
		}
		return axArgErrorResult(argErr, requested)
	}
	return s.a11yDecodedRequest(request)
}

// a11yDecodedRequest runs an AX call whose arguments are already decoded.
// dispatch decodes each call once and passes the request here.
func (s *service) a11yDecodedRequest(request *axRequest) toolCallResult {
	obs := observation{mode: request.observe}
	switch request.mode {
	case a11yModeApps:
		return withA11yModeEvidence(
			s.listApps().withEvidenceScope("app_inventory"),
			a11yModeApps,
		)
	case a11yModeObserve:
		return s.a11yObserve(request, obs)
	case a11yModeFind:
		return s.a11yFind(request)
	default:
		return s.a11yAct(request, obs)
	}
}

func (s *service) a11yObserve(
	request *axRequest,
	obs observation,
) toolCallResult {
	if request.until != nil {
		return s.a11yObserveUntil(request, obs)
	}
	var root map[string]any
	switch {
	case request.root == "focused":
		root = map[string]any{"focused": true}
	case request.root != "":
		root = map[string]any{"ref": request.root}
	}
	var includeScreenshot *bool
	if obs.resolvedMode() == observeScreenshot ||
		obs.resolvedMode() == observeFull {
		enabled := true
		includeScreenshot = &enabled
	}
	readContext, identityErr := s.resolveA11yReadIdentity(request.app, request.windowRef, "")
	if identityErr != nil {
		return axIdentityErrorResult(identityErr)
	}
	if obs.mode == observeDiff && root == nil {
		return withA11yModeEvidence(
			s.a11yDiffObservation(readContext, request.scope).
				withEvidenceScope("independent_ui_read"),
			a11yModeObserve,
		)
	}
	// observe "full" always returns the whole tree, even when an earlier
	// result already delivered the identical state.
	forceFull := obs.resolvedMode() == observeFull
	result := s.getAppState(
		readContext.appToken,
		nil, nil, nil, nil,
		includeScreenshot,
		&forceFull,
		treeViewWorking,
		request.scope,
		root,
		readContext.windowHandle,
	).withEvidenceScope("independent_ui_read")
	return withA11yModeEvidence(result, a11yModeObserve)
}

// a11yObserveUntil waits for a predicate and then returns the requested
// observation. A predicate that stays false is a result, not an error.
func (s *service) a11yObserveUntil(
	request *axRequest,
	obs observation,
) toolCallResult {
	target := optionalString(axUntilTarget(request.until), "ref")
	readContext, identityErr := s.resolveA11yReadIdentity(request.app, request.windowRef, target)
	if identityErr != nil {
		return axIdentityErrorResult(identityErr)
	}
	check := s.runA11yAssertion(readContext, request.until, request.scope)
	check = withA11yModeEvidence(check, a11yModeObserve)
	status, _ := check.StructuredContent["assertion_status"].(string)
	passed := !check.IsError && status == "pass"
	predicateFalse := !check.IsError && status != "pass"
	if check.StructuredContent == nil {
		check.StructuredContent = map[string]any{}
	}
	check.StructuredContent["until_result"] = map[bool]string{true: "true", false: "false"}[passed]
	if check.IsError {
		check.StructuredContent["until_result"] = "unknown"
		return check
	}
	_ = predicateFalse
	if obs.resolvedMode() == observeNone || readContext.appToken == "" {
		return check
	}
	var includeScreenshot *bool
	if obs.resolvedMode() == observeScreenshot || obs.resolvedMode() == observeFull {
		enabled := true
		includeScreenshot = &enabled
	}
	var current toolCallResult
	if obs.resolvedMode() == observeDiff || obs.resolvedMode() == "" {
		current = s.a11yDiffObservation(readContext, request.scope)
	} else {
		forceFull := obs.resolvedMode() == observeFull
		current = s.getAppState(
			readContext.appToken,
			nil, nil, nil, nil,
			includeScreenshot,
			&forceFull,
			treeViewWorking,
			request.scope,
			nil,
			readContext.windowHandle,
		)
	}
	if current.IsError {
		// The predicate result stands; the follow-up read is extra evidence.
		check.Content = append(check.Content, contentItem{
			Type: "text",
			Text: "current state unavailable: " + firstLine(resultText(current)),
		})
		return check
	}
	check.Content = append(check.Content, current.Content...)
	for _, key := range []string{"tree_complete_for_model", "coverage_incomplete_nodes",
		"structural_budget_omitted_nodes", "final_observation_kind"} {
		if value, present := current.StructuredContent[key]; present {
			check.StructuredContent[key] = value
		}
	}
	return check
}

// a11yDiffObservation reads the window and renders it as a diff against the
// last tree delivered for it, falling back to the working tree.
func (s *service) a11yDiffObservation(
	readContext *a11yReadContext,
	scope string,
) toolCallResult {
	noScreenshot := false
	request := axServiceRequest{
		Operation:          axServiceObserve,
		App:                readContext.appToken,
		IncludeScreenshot:  &noScreenshot,
		TargetWindowHandle: readContext.windowHandle,
		SourceRef:          readContext.windowRef,
		Scope:              normalizedA11yScope(scope),
		TreeView:           treeViewWorking,
	}
	snapshot, notes, result := s.refreshSnapshot(readContext.appToken, request)
	if result.IsError {
		return result
	}
	if s.deliveredSnapshotFor(snapshot) == nil {
		// Nothing was delivered for this window yet, so there is no diff
		// baseline: return the tree.
		return s.getAppStateResult(snapshot, notes, false)
	}
	return s.renderA11yBatchFinalObservation(snapshot, notes, observation{mode: observeDiff})
}

// runA11yAssertion evaluates one internal assertion against a read context.
func (s *service) runA11yAssertion(
	readContext *a11yReadContext,
	assertion map[string]any,
	scope string,
) toolCallResult {
	spec, err := parseA11yAssertionSpec(assertion)
	if err != nil {
		return textResult(err.Error(), true).withEvidenceScope("error_only")
	}
	if spec.assertionType == "window_title_contains" {
		windowTitle := strings.TrimSpace(optionalString(assertion, "text"))
		expected := true
		if exists := optionalBool(assertion, "exists"); exists != nil {
			expected = *exists
		}
		result := s.verifyWindowTitle(
			firstNonEmpty(readContext.appRef, readContext.appToken),
			windowTitle,
			expected,
			spec.timeoutMS,
			spec.stableMS,
		)
		return withA11yModeEvidence(
			result.withEvidenceScope(assertionEvidenceScope(result)),
			a11yModeAssert,
		)
	}
	goal := assertionGoal(assertion)
	target := spec.targetAddress
	if isShortAXRef(target) {
		entry, resolveErr := s.resolveShortAXRef(readContext.appToken, target)
		if resolveErr != nil {
			return axStaleRefResult(target, resolveErr)
		}
		liveTarget, targetErr := semanticTargetFromRefEntry(entry)
		if targetErr != nil {
			return axStaleRefResult(target, targetErr)
		}
		result := s.verifyResolvedAXRef(
			readContext.appToken,
			target,
			liveTarget,
			goal,
			spec.timeoutMS,
			spec.stableMS,
		)
		// The live resolver can fail to read some provider-bound elements
		// completely. One fresh canonical read of the window then decides
		// the predicate, as it does for an action's until.
		if a11yPostconditionNeedsCanonicalReconciliation(assertion, result) {
			result = s.reconcileA11yActionPostcondition(&a11yBatchContext{
				appToken:     firstNonEmpty(entry.AppRef, readContext.appToken),
				windowRef:    entry.LogicalWindowRef,
				windowHandle: entry.WindowHandle,
			}, assertion, result)
		}
		result = result.withEvidenceScope("independent_ui_read")
		return withA11yModeEvidence(result, a11yModeAssert)
	}
	result := s.verify(
		readContext.appToken,
		target,
		target,
		goal,
		spec.timeoutMS,
		spec.stableMS,
		scope,
		readContext.windowHandle,
	).withEvidenceScope("independent_ui_read")
	if result.StructuredContent != nil &&
		result.StructuredContent["assertion_sampled_live_ui"] == true {
		result.StructuredContent["evidence_scope"] = "independent_ui_read"
	}
	return withA11yModeEvidence(result, a11yModeAssert)
}

func (s *service) a11yFind(request *axRequest) toolCallResult {
	if request.cursor != "" {
		return withA11yModeEvidence(
			s.continueFind(request.cursor, request.limit).
				withEvidenceScope("cached_observation_page"),
			a11yModeFind,
		)
	}
	readContext, identityErr := s.resolveA11yReadIdentity(request.app, request.windowRef, "")
	if identityErr != nil {
		return axIdentityErrorResult(identityErr)
	}
	query := request.query
	name := optionalString(query, "name_contains")
	_, nameExact := query["name_equals"]
	if nameExact {
		name = optionalString(query, "name_equals")
	}
	text := optionalString(query, "text_contains")
	_, textExact := query["text_equals"]
	if textExact {
		text = optionalString(query, "text_equals")
	}
	result := s.find(
		readContext.appToken,
		optionalString(query, "role"),
		name,
		text,
		optionalString(query, "state"),
		request.limit,
		nameExact,
		textExact,
		request.scope,
		readContext.windowHandle,
	).withEvidenceScope("independent_ui_read")
	return withA11yModeEvidence(result, a11yModeFind)
}

func (s *service) a11yAct(
	request *axRequest,
	obs observation,
) toolCallResult {
	result := s.a11yBatchDecoded(
		request.actions,
		obs,
		request.until,
		request.windowRef,
	).withEvidenceScope("delivery_or_local_ui")
	return withA11yModeEvidence(result, a11yModeAct)
}

// axIdentityErrorResult reports a read identity that could not be bound.
func axIdentityErrorResult(err error) toolCallResult {
	var lifecycle *axRefLifecycleError
	if errors.As(err, &lifecycle) {
		return axStaleRefResult("", err)
	}
	if strings.Contains(err.Error(), "never issued") ||
		strings.Contains(err.Error(), "invalid short AX") ||
		strings.Contains(err.Error(), "expired") {
		return axStaleRefResult("", err)
	}
	argErr := axInvalid("app", err.Error(), `{"mode":"apps"}`)
	var rule *argumentRuleError
	if errors.As(err, &rule) {
		argErr.rule = rule.code
	}
	return axArgErrorResult(argErr, 0)
}

// axStaleRefResult reports a ref that no longer resolves. Nothing was sent.
func axStaleRefResult(ref string, err error) toolCallResult {
	message := err.Error()
	if ref != "" && !strings.Contains(message, ref) {
		message = ref + ": " + message
	}
	result := textResult(message, true)
	result.StructuredContent = map[string]any{
		axPublicErrorCodeKey:   axCodeStaleRef,
		axPublicErrorDetailKey: message,
		axPublicErrorHintKey:   `{"mode":"observe","window_ref":"wN"}`,
		"failure_code":         "stale_target",
		"input_events_emitted": false,
		"action_attempted":     false,
		"preflight_only":       true,
		"evidence_scope":       "error_only",
		"observation_required": true,
	}
	return result
}
