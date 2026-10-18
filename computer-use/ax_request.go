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
	if _, present := args["mode"]; !present {
		return "", errors.New(
			"mode is required and must be observe, find, act, or assert_ax")
	}
	mode := optionalString(args, "mode")
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
		if optionalString(args, "observation_target") == "apps" {
			fields["observation_target"] = true
			return fields
		}
		for _, field := range []string{
			"observation_target", "app", "app_ref", "window_ref",
			"observe", "scope", "root",
		} {
			fields[field] = true
		}
	case a11yModeFind:
		// A cursor continues a prior find; only then does the top-level
		// limit apply. A new search takes its limit inside query.
		if _, continuing := args["cursor"]; continuing {
			fields["cursor"] = true
			fields["limit"] = true
			return fields
		}
		fields["app"] = true
		fields["app_ref"] = true
		fields["window_ref"] = true
		fields["query"] = true
		fields["scope"] = true
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
	app, appRef, windowRef, err := readIdentityArguments(args)
	if err != nil {
		return nil, err
	}
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
			return nil, newArgumentRuleError(argumentRuleRefOwnsIdentity, fmt.Sprintf(
				"target %s belongs to window %s, not %s",
				target, entry.LogicalWindowRef, windowRef,
			))
		}
	}
	if windowRef != "" {
		entry, err := s.resolveShortAXWindowRefOwned(windowRef)
		if err != nil {
			return nil, err
		}
		if context.appRef != "" && entry.AppRef != context.appRef {
			return nil, newArgumentRuleError(argumentRuleRefOwnsIdentity, fmt.Sprintf(
				"target owner %s conflicts with window owner %s",
				context.appRef, entry.AppRef,
			))
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
			return nil, newArgumentRuleError(argumentRuleRefOwnsIdentity, fmt.Sprintf(
				"target owner %s conflicts with app_ref %s",
				context.appRef, appRef,
			))
		}
		context.appRef = appRef
	}
	if context.appRef != "" {
		if app != "" {
			return nil, newArgumentRuleError(argumentRuleRefOwnsIdentity,
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

// readIdentityArguments reads app, app_ref and window_ref. A present field
// must be a non-blank string; refs must be exact short refs. An absent field
// is returned as "".
func readIdentityArguments(args map[string]any) (app, appRef, windowRef string, err error) {
	read := func(key string, valid func(string) bool, shape string) (string, error) {
		raw, present := args[key]
		if !present {
			return "", nil
		}
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) == "" || (valid != nil && !valid(value)) {
			return "", fmt.Errorf("%s must be %s", key, shape)
		}
		return value, nil
	}
	if app, err = read("app", nil, "a non-blank app name, app id, or PID string"); err != nil {
		return
	}
	app = strings.TrimSpace(app)
	if appRef, err = read("app_ref", isShortAXAppRef, "an app ref aN copied from observe"); err != nil {
		return
	}
	windowRef, err = read("window_ref", isShortAXWindowRef, "a window ref wN copied from observe/find")
	return
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
		optionalString(args, "observation_target") == "apps" {
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
		return s.a11yObserve(args, obs)
	case a11yModeFind:
		return s.a11yFind(args, obs)
	case a11yModeAssert:
		return s.a11yAssert(args, obs)
	case a11yModeAct:
		return s.a11yAct(args, obs)
	}
	return textResult("unreachable AX mode", true).
		withEvidenceScope("error_only")
}

func (s *service) a11yObserve(
	args map[string]any,
	obs observation,
) toolCallResult {
	target := "app"
	if raw, present := args["observation_target"]; present {
		target, _ = raw.(string)
	}
	switch target {
	case "apps":
		result := s.listApps().withEvidenceScope("app_inventory")
		result = withA11yModeEvidence(result, a11yModeObserve)
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
	result = withA11yModeEvidence(result, a11yModeObserve)
	return result
}

func (s *service) a11yFind(
	args map[string]any,
	obs observation,
) toolCallResult {
	if rawCursor, continuing := args["cursor"]; continuing {
		cursor, _ := rawCursor.(string)
		if !isShortAXFindCursor(cursor) {
			return textResult(
				"cursor must be a find cursor fN copied from find", true,
			).withEvidenceScope("error_only")
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
			a11yModeFind,
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
	for _, field := range []string{
		"role", "name_contains", "name_equals", "text_contains", "text_equals", "state",
	} {
		if raw, present := query[field]; present {
			if _, ok := raw.(string); !ok {
				return textResult(
					"find.query."+field+" must be a string", true,
				).withEvidenceScope("error_only")
			}
		}
	}
	for _, pair := range [][2]string{
		{"name_contains", "name_equals"}, {"text_contains", "text_equals"},
	} {
		_, first := query[pair[0]]
		_, second := query[pair[1]]
		if first && second {
			return textResult(
				"find.query accepts only one of "+pair[0]+" or "+pair[1],
				true,
			).withEvidenceScope("error_only")
		}
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
		resolvedLimit,
		nameExact,
		textExact,
		scope,
		readContext.windowHandle,
	).withEvidenceScope("independent_ui_read")
	return withA11yModeEvidence(result, a11yModeFind)
}

func (s *service) a11yAssert(
	args map[string]any,
	obs observation,
) toolCallResult {
	assertion, err := requiredObjectArgument(args, "assertion")
	if err != nil {
		return textResult(err.Error(), true).withEvidenceScope("error_only")
	}
	spec, err := parseA11yAssertionSpec(assertion)
	if err != nil {
		return textResult(err.Error(), true).withEvidenceScope("error_only")
	}
	scope, err := parseA11yScope(args)
	if err != nil {
		return textResult(err.Error(), true).withEvidenceScope("error_only")
	}
	timeoutMS := spec.timeoutMS
	stableMS := spec.stableMS
	assertionType := spec.assertionType
	targetAddress := spec.targetAddress
	readContext, identityErr := s.resolveA11yReadIdentity(args, targetAddress)
	if identityErr != nil {
		return withArgumentRule(textResult(
			"AX assertion target: "+identityErr.Error(), true,
		).withEvidenceScope("error_only"), identityErr)
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
		result = withA11yModeEvidence(result, a11yModeAssert)
		if result.StructuredContent == nil {
			result.StructuredContent = map[string]any{}
		}
		result.StructuredContent["assertion_scope"] =
			"accessibility_visible_window_title_only"
		result.StructuredContent["task_completion_verified"] = false
		return result
	}
	goal := spec.goal
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
		result = withA11yModeEvidence(result, a11yModeAssert)
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
	result = withA11yModeEvidence(result, a11yModeAssert)
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
}

func (s *service) a11yAct(
	args map[string]any,
	obs observation,
) toolCallResult {
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
	_, _, windowRef, err := readIdentityArguments(args)
	if err != nil {
		return a11yBatchValidationError(err.Error(), 0)
	}
	result := s.a11yBatch(
		rawActions,
		obs,
		postcondition,
		"",
		windowRef,
	).withEvidenceScope("delivery_or_local_ui")
	return withA11yModeEvidence(result, a11yModeAct)
}
