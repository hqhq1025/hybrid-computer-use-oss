package main

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
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
	a11yModeAssert  = "assert_ax"
)

func parseA11yMode(args map[string]any) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(optionalString(args, "mode")))
	if mode == "" {
		return "", errors.New(
			"mode is required and must be observe, find, act, or assert_ax")
	}
	switch mode {
	case a11yModeObserve, a11yModeFind, a11yModeAct, a11yModeAssert:
		return mode, nil
	default:
		return "", fmt.Errorf(
			"mode must be observe, find, act, or assert_ax")
	}
}

func a11yModeFields(mode string, args map[string]any) map[string]bool {
	fields := map[string]bool{"mode": true}
	switch mode {
	case a11yModeObserve:
		if strings.ToLower(strings.TrimSpace(
			optionalString(args, "observation_target"),
		)) == "apps" {
			fields["observation_target"] = true
			return fields
		}
		for _, field := range []string{
			"observation_target", "app", "app_ref", "window_ref",
			"actions", "observe",
			"scope", "root",
		} {
			fields[field] = true
		}
	case a11yModeFind:
		fields["app"] = true
		fields["app_ref"] = true
		fields["window_ref"] = true
		fields["query"] = true
		fields["scope"] = true
		fields["cursor"] = true
		fields["limit"] = true
	case a11yModeAct:
		fields["window_ref"] = true
		fields["actions"] = true
		fields["observe"] = true
		fields["postcondition"] = true
	case a11yModeAssert:
		fields["app"] = true
		fields["app_ref"] = true
		fields["window_ref"] = true
		fields["assertion"] = true
		fields["scope"] = true
	}
	return fields
}

func (s *service) resolveA11yReadIdentity(
	args map[string]any,
	target string,
) (*a11yReadContext, error) {
	app := strings.TrimSpace(optionalString(args, "app"))
	appRef := strings.TrimSpace(optionalString(args, "app_ref"))
	windowRef := strings.TrimSpace(optionalString(args, "window_ref"))
	if app != "" && (appRef != "" || windowRef != "") {
		return nil, errors.New("app cannot be combined with app_ref or window_ref")
	}
	if appRef != "" && windowRef != "" {
		return nil, errors.New("use exactly one of app_ref or window_ref")
	}
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
			return nil, fmt.Errorf(
				"target %s belongs to window %s, not %s",
				target, entry.LogicalWindowRef, windowRef,
			)
		}
	}
	if windowRef != "" {
		entry, err := s.resolveShortAXWindowRefOwned(windowRef)
		if err != nil {
			return nil, err
		}
		if context.appRef != "" && entry.AppRef != context.appRef {
			return nil, fmt.Errorf(
				"target owner %s conflicts with window owner %s",
				context.appRef, entry.AppRef,
			)
		}
		context.appRef = entry.AppRef
		context.windowRef = entry.Ref
		context.windowHandle = entry.WindowHandle
	}
	if appRef != "" {
		if _, err := s.resolveShortAXAppRef(appRef); err != nil {
			return nil, err
		}
		if context.appRef != "" && context.appRef != appRef {
			return nil, fmt.Errorf(
				"target owner %s conflicts with app_ref %s",
				context.appRef, appRef,
			)
		}
		context.appRef = appRef
	}
	if context.appRef != "" {
		if app != "" {
			return nil, errors.New(
				"app cannot be combined with a ref-owned target",
			)
		}
		context.appToken = context.appRef
		return context, nil
	}
	if app != "" {
		context.appToken = app
		return context, nil
	}
	return nil, errors.New("one of app, app_ref, window_ref, or an owned rN target is required")
}

func validateA11yModeFields(mode string, args map[string]any) error {
	allowed := a11yModeFields(mode, args)
	illegal := make([]string, 0)
	for field := range args {
		if !allowed[field] {
			illegal = append(illegal, field)
		}
	}
	if len(illegal) == 0 {
		return nil
	}
	sort.Strings(illegal)
	quoted := make([]string, 0, len(illegal))
	for _, field := range illegal {
		quoted = append(quoted, strconv.Quote(field))
	}
	context := fmt.Sprintf("mode=%q", mode)
	if mode == a11yModeObserve &&
		strings.ToLower(strings.TrimSpace(
			optionalString(args, "observation_target"),
		)) == "apps" {
		context += ` with observation_target="apps"`
	}
	message := fmt.Sprintf(
		"%s does not accept fields: %s",
		context,
		strings.Join(quoted, ", "),
	)
	if mode == a11yModeAct {
		for _, field := range illegal {
			if field == "app" || field == "app_ref" {
				message += "; each rN/wN target already owns its application and " +
					"logical window. Copy refs exactly from observe/find. Use " +
					"top-level window_ref only for an all-untargeted keyboard batch"
				break
			}
		}
	}
	return errors.New(message)
}

func requiredObjectArgument(
	args map[string]any,
	key string,
) (map[string]any, error) {
	value, present := args[key]
	if !present {
		return nil, fmt.Errorf("missing required argument: %s", key)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an object", key)
	}
	return object, nil
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
	obs observation,
) toolCallResult {
	mode, err := parseA11yMode(args)
	if err != nil {
		return textResult(err.Error(), true).withEvidenceScope("error_only")
	}
	if err := validateA11yModeFields(mode, args); err != nil {
		return textResult(err.Error(), true).withEvidenceScope("error_only")
	}

	switch mode {
	case a11yModeObserve:
		target := defaultString(
			strings.ToLower(strings.TrimSpace(
				optionalString(args, "observation_target"),
			)),
			"app",
		)
		switch target {
		case "apps":
			result := s.listApps().withEvidenceScope("app_inventory")
			result = withA11yModeEvidence(result, mode)
			if result.StructuredContent == nil {
				result.StructuredContent = map[string]any{}
			}
			result.StructuredContent["observation_target"] = "apps"
			return result
		case "app":
		default:
			return textResult(
				"observation_target must be app or apps", true,
			).withEvidenceScope("error_only")
		}
		if raw, present := args["actions"]; present {
			actions, ok := raw.([]any)
			if !ok {
				return textResult(
					"mode=\"observe\" actions must be an empty array when supplied",
					true,
				).withEvidenceScope("error_only")
			}
			if len(actions) != 0 {
				return textResult(
					"mode=\"observe\" cannot execute actions; use mode=\"act\"",
					true,
				).withEvidenceScope("error_only")
			}
		}
		if obs.resolvedMode() == observeNone {
			return textResult(
				"mode=\"observe\" cannot use observe=\"none\"",
				true,
			).withEvidenceScope("error_only")
		}
		view, err := parseTreeView(args)
		if err != nil {
			return textResult(err.Error(), true).withEvidenceScope("error_only")
		}
		scope, err := parseA11yScope(args)
		if err != nil {
			return textResult(err.Error(), true).withEvidenceScope("error_only")
		}
		root, err := parseObservationRoot(args["root"])
		if err != nil {
			return textResult(
				"root: "+err.Error(), true,
			).withEvidenceScope("error_only")
		}
		var includeScreenshot *bool
		if obs.resolvedMode() == observeScreenshot ||
			obs.resolvedMode() == observeFull {
			enabled := true
			includeScreenshot = &enabled
		}
		readContext, identityErr := s.resolveA11yReadIdentity(args, "")
		if identityErr != nil {
			return textResult(
				"AX observation target: "+identityErr.Error(), true,
			).withEvidenceScope("error_only")
		}
		result := s.getAppState(
			readContext.appToken,
			nil,
			nil,
			nil,
			nil,
			includeScreenshot,
			nil,
			view,
			scope,
			root,
			readContext.windowHandle,
		).withEvidenceScope("independent_ui_read")
		result = withA11yModeEvidence(result, mode)
		return result

	case a11yModeFind:
		if cursor := strings.TrimSpace(optionalString(args, "cursor")); cursor != "" {
			for _, field := range []string{
				"app", "app_ref", "window_ref", "query", "scope",
			} {
				if _, present := args[field]; present {
					return textResult(
						"AX find cursor continuation accepts only mode, cursor, and limit",
						true,
					).withEvidenceScope("error_only")
				}
			}
			limit, err := optionalPositiveInt(args, "limit")
			if err != nil {
				return textResult(err.Error(), true).withEvidenceScope("error_only")
			}
			resolvedLimit := 20
			if limit != nil {
				resolvedLimit = *limit
			}
			if resolvedLimit > maxAXFindPageSize {
				return textResult(fmt.Sprintf(
					"limit must be at most %d", maxAXFindPageSize,
				), true).withEvidenceScope("error_only")
			}
			return withA11yModeEvidence(
				s.continueFind(cursor, resolvedLimit).
					withEvidenceScope("cached_observation_page"),
				mode,
			)
		}
		readContext, identityErr := s.resolveA11yReadIdentity(args, "")
		if identityErr != nil {
			return textResult(
				"AX find target: "+identityErr.Error(), true,
			).withEvidenceScope("error_only")
		}
		query, err := requiredObjectArgument(args, "query")
		if err != nil {
			return textResult(err.Error(), true).withEvidenceScope("error_only")
		}
		allowedQueryFields := map[string]bool{
			"role": true, "name_contains": true, "name_equals": true,
			"text_contains": true, "text_equals": true, "state": true,
			"limit": true,
		}
		unexpectedQueryFields := []string{}
		for field := range query {
			if !allowedQueryFields[field] {
				unexpectedQueryFields = append(unexpectedQueryFields, field)
			}
		}
		sort.Strings(unexpectedQueryFields)
		if len(unexpectedQueryFields) > 0 {
			return textResult(
				"find.query has unsupported field(s): "+
					strings.Join(unexpectedQueryFields, ", ")+
					"; use name_equals/name_contains or text_equals/text_contains",
				true,
			).withEvidenceScope("error_only")
		}
		if optionalString(query, "name_contains") != "" &&
			optionalString(query, "name_equals") != "" {
			return textResult(
				"find.query accepts only one of name_contains or name_equals",
				true,
			).withEvidenceScope("error_only")
		}
		if optionalString(query, "text_contains") != "" &&
			optionalString(query, "text_equals") != "" {
			return textResult(
				"find.query accepts only one of text_contains or text_equals",
				true,
			).withEvidenceScope("error_only")
		}
		limit, err := optionalPositiveInt(query, "limit")
		if err != nil {
			return textResult(err.Error(), true).withEvidenceScope("error_only")
		}
		resolvedLimit := 20
		if limit != nil {
			resolvedLimit = *limit
		}
		if resolvedLimit > maxAXFindPageSize {
			return textResult(fmt.Sprintf(
				"find.query.limit must be at most %d", maxAXFindPageSize,
			), true).withEvidenceScope("error_only")
		}
		scope, err := parseA11yScope(args)
		if err != nil {
			return textResult(err.Error(), true).withEvidenceScope("error_only")
		}
		name := optionalString(query, "name_contains")
		nameExact := false
		if exact := optionalString(query, "name_equals"); exact != "" {
			name = exact
			nameExact = true
		}
		text := optionalString(query, "text_contains")
		textExact := false
		if exact := optionalString(query, "text_equals"); exact != "" {
			text = exact
			textExact = true
		}
		result := s.find(
			readContext.appToken,
			optionalString(query, "role"),
			name,
			text,
			optionalString(query, "state"),
			resolvedLimit,
			nameExact,
			textExact,
			scope,
			readContext.windowHandle,
		).withEvidenceScope("independent_ui_read")
		return withA11yModeEvidence(result, mode)

	case a11yModeAssert:
		assertion, err := requiredObjectArgument(args, "assertion")
		if err != nil {
			return textResult(err.Error(), true).withEvidenceScope("error_only")
		}
		spec, err := parseA11yAssertionSpec(assertion)
		if err != nil {
			return textResult(err.Error(), true).withEvidenceScope("error_only")
		}
		timeoutMS := spec.timeoutMS
		stableMS := spec.stableMS
		assertionType := spec.assertionType
		targetAddress := spec.targetAddress
		readContext, identityErr := s.resolveA11yReadIdentity(args, targetAddress)
		if identityErr != nil {
			return textResult(
				"AX assertion target: "+identityErr.Error(), true,
			).withEvidenceScope("error_only")
		}
		if assertionType == "window_title_contains" {
			windowTitle := strings.TrimSpace(
				optionalString(assertion, "text"),
			)
			expected := true
			if exists := optionalBool(assertion, "exists"); exists != nil {
				expected = *exists
			}
			result := s.verifyWindowTitle(
				readContext.appToken,
				windowTitle,
				expected,
				timeoutMS,
				stableMS,
				readContext.windowHandle,
			)
			result = result.withEvidenceScope(
				assertionEvidenceScope(result),
			)
			result = withA11yModeEvidence(result, mode)
			if result.StructuredContent == nil {
				result.StructuredContent = map[string]any{}
			}
			result.StructuredContent["assertion_scope"] =
				"accessibility_visible_window_title_only"
			result.StructuredContent["task_completion_verified"] = false
			return result
		}
		scope, err := parseA11yScope(args)
		if err != nil {
			return textResult(err.Error(), true).withEvidenceScope("error_only")
		}
		goal := verifyGoal{}
		switch assertionType {
		case "exists":
			goal.exists = optionalBool(assertion, "value")
		case "state":
			state := strings.TrimSpace(optionalString(assertion, "state"))
			expected := optionalBool(assertion, "value")
			if !*expected {
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
		target := targetAddress
		if target == "" {
			return textResult(
				"mode=\"assert_ax\" requires assertion.target", true,
			).withEvidenceScope("error_only")
		}
		displayTarget := target
		if isShortAXWindowRef(target) {
			return textResult(
				"assertion.target: wN identifies a window action target; "+
					"use top-level window_ref with "+
					"assertion.type=\"window_title_contains\" for window state",
				true,
			).withEvidenceScope("error_only")
		}
		if isShortAXRef(target) {
			entry, resolveErr := s.resolveShortAXRef(readContext.appToken, target)
			if resolveErr != nil {
				return textResult(
					"assertion.target: "+resolveErr.Error(), true,
				).withEvidenceScope("error_only")
			}
			liveTarget, targetErr := semanticTargetFromRefEntry(entry)
			if targetErr != nil {
				return textResult(
					"assertion.target: "+targetErr.Error(), true,
				).withEvidenceScope("error_only")
			}
			result := s.verifyResolvedAXRef(
				readContext.appToken,
				target,
				liveTarget,
				goal,
				timeoutMS,
				stableMS,
			).withEvidenceScope("independent_ui_read")
			result = withA11yModeEvidence(result, mode)
			if result.StructuredContent == nil {
				result.StructuredContent = map[string]any{}
			}
			result.StructuredContent["assertion_scope"] =
				"accessibility_visible_state_only"
			result.StructuredContent["task_completion_verified"] = false
			return result
		}
		result := s.verify(
			readContext.appToken,
			target,
			displayTarget,
			goal,
			timeoutMS,
			stableMS,
			scope,
			readContext.windowHandle,
		).withEvidenceScope("independent_ui_read")
		if displayTarget != target {
			result = replaceToolResultText(result, target, displayTarget)
		}
		result = withA11yModeEvidence(result, mode)
		if result.StructuredContent != nil &&
			result.StructuredContent["assertion_sampled_live_ui"] == true {
			result.StructuredContent["evidence_scope"] =
				"independent_ui_read"
		}
		if result.StructuredContent == nil {
			result.StructuredContent = map[string]any{}
		}
		result.StructuredContent["assertion_scope"] =
			"accessibility_visible_state_only"
		result.StructuredContent["task_completion_verified"] = false
		return result

	case a11yModeAct:
		switch obs.resolvedMode() {
		case observeNone, observeDiff, observeScreenshot:
		default:
			return a11yBatchValidationError(
				"mode=\"act\" observe must be none, diff, or screenshot. "+
					"No action was executed. Retry this same act with observe omitted "+
					"or set to diff; do not add a separate observe first.", 0)
		}
		rawActions, present := args["actions"]
		if !present {
			return a11yBatchValidationError(
				"mode=\"act\" requires actions", 0)
		}
		var postcondition map[string]any
		if raw, present := args["postcondition"]; present {
			var ok bool
			postcondition, ok = raw.(map[string]any)
			if !ok {
				return a11yBatchValidationError(
					"postcondition must be an AX assertion object", 0,
				)
			}
		}
		result := s.a11yBatch(
			"",
			rawActions,
			obs,
			postcondition,
			"",
			optionalString(args, "window_ref"),
		).withEvidenceScope("delivery_or_local_ui")
		return withA11yModeEvidence(result, mode)
	}
	return textResult("unreachable AX mode", true).
		withEvidenceScope("error_only")
}
