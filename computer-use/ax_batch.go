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

type a11yBatchAction struct {
	actionType    string
	targetRef     string
	direction     string
	pages         float64
	text          string
	key           string
	value         string
	postcondition map[string]any
}

type a11yBatchContext struct {
	appRef         string
	windowRef      string
	appToken       string
	windowHandle   string
	expectedWindow *ProviderWindowExpectation
}

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

type a11yReadContext struct {
	appToken     string
	appRef       string
	windowRef    string
	windowHandle string
}

type a11yWindowContinuity string

const (
	a11yWindowSame    a11yWindowContinuity = "same"
	a11yWindowChanged a11yWindowContinuity = "changed"
	a11yWindowUnknown a11yWindowContinuity = "unknown"
)

func (s *service) deriveA11yBatchContext(
	legacyApp,
	explicitAppRef,
	explicitWindowRef string,
	actions []a11yBatchAction,
) (*a11yBatchContext, error) {
	if strings.TrimSpace(legacyApp) != "" &&
		(strings.TrimSpace(explicitAppRef) != "" ||
			strings.TrimSpace(explicitWindowRef) != "") {
		return nil, errors.New(
			"app cannot be combined with app_ref or window_ref; " +
				"the server-owned ref is the single application identity",
		)
	}
	context := &a11yBatchContext{}
	bindApp := func(ref, fallback string) error {
		ref = strings.TrimSpace(ref)
		fallback = strings.TrimSpace(fallback)
		if ref != "" {
			if context.appRef != "" && context.appRef != ref {
				return fmt.Errorf(
					"batch spans multiple application refs (%s and %s)",
					context.appRef, ref,
				)
			}
			context.appRef = ref
			context.appToken = ref
			return nil
		}
		if context.appToken == "" {
			context.appToken = fallback
		}
		return nil
	}
	bindWindow := func(ref, handle, appRef, fallbackApp string) error {
		ref = strings.TrimSpace(ref)
		handle = strings.TrimSpace(handle)
		if context.windowHandle != "" && context.windowHandle != handle {
			return fmt.Errorf(
				"batch spans multiple logical windows (%s and %s); split it before input",
				context.windowRef, ref,
			)
		}
		if context.windowRef == "" {
			context.windowRef = ref
			context.windowHandle = handle
		}
		return bindApp(appRef, fallbackApp)
	}

	if explicitAppRef != "" {
		entry, err := s.resolveShortAXAppRef(explicitAppRef)
		if err != nil {
			return nil, err
		}
		if err := bindApp(entry.Ref, entry.DisplayName); err != nil {
			return nil, err
		}
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
	hasOwnedShortTarget := false
	for index, action := range actions {
		address := strings.TrimSpace(action.targetRef)
		if address == "" {
			continue
		}
		hasTarget = true
		switch {
		case isShortAXRef(address):
			hasOwnedShortTarget = true
			entry, err := s.resolveShortAXRefOwned(address)
			if err != nil {
				return nil, fmt.Errorf("actions[%d].target: %w", index, err)
			}
			if err := bindWindow(
				entry.LogicalWindowRef,
				entry.WindowHandle,
				entry.AppRef,
				entry.AppIdentity,
			); err != nil {
				return nil, fmt.Errorf("actions[%d].target: %w", index, err)
			}
		case isShortAXWindowRef(address):
			hasOwnedShortTarget = true
			entry, err := s.resolveShortAXWindowRefOwned(address)
			if err != nil {
				return nil, fmt.Errorf("actions[%d].target: %w", index, err)
			}
			if err := bindWindow(
				entry.Ref, entry.WindowHandle, entry.AppRef, entry.AppIdentity,
			); err != nil {
				return nil, fmt.Errorf("actions[%d].target: %w", index, err)
			}
		default:
			return nil, fmt.Errorf(
				"actions[%d].target must use rN or wN", index,
			)
		}
	}
	if hasOwnedShortTarget &&
		(strings.TrimSpace(explicitAppRef) != "" ||
			strings.TrimSpace(explicitWindowRef) != "") {
		return nil, errors.New(
			"targeted AX actions must omit app_ref and window_ref; " +
				"each rN/wN target already owns its application and logical window",
		)
	}

	if context.windowHandle == "" && strings.TrimSpace(legacyApp) != "" && hasTarget {
		if snapshot := s.currentSnapshot(legacyApp); snapshot != nil &&
			snapshot.publicWindowRef != "" {
			if err := bindWindow(
				snapshot.publicWindowRef,
				snapshot.WindowHandle,
				snapshot.publicAppRef,
				appIdentityForSnapshot(snapshot),
			); err != nil {
				return nil, err
			}
		}
	}
	if context.windowHandle == "" {
		if !hasTarget {
			return nil, errors.New(
				"an all-untargeted batch requires window_ref; app names do not prove keyboard focus",
			)
		}
		return nil, errors.New(
			"targeted AX actions require an rN or wN ref returned by observe/find",
		)
	}
	if explicitAppRef != "" && context.appRef != explicitAppRef {
		return nil, fmt.Errorf(
			"app_ref %s conflicts with the batch window owner %s",
			explicitAppRef, context.appRef,
		)
	}
	if context.appToken == "" {
		context.appToken = strings.TrimSpace(legacyApp)
	}
	if strings.TrimSpace(legacyApp) != "" && hasOwnedShortTarget {
		return nil, errors.New(
			"app cannot be combined with a short ref target; " +
				"the server-owned ref is the single application identity",
		)
	}
	if context.appToken == "" {
		return nil, errors.New(
			"the batch window has no current application owner; observe it again",
		)
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
		if strings.Contains(err.Error(), "needs_revalidation") {
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

func prependA11yScopeNotice(
	result toolCallResult,
	text string,
) toolCallResult {
	// The stable tool contract owns channel boundaries. Repeating them in each
	// successful response obscures the state or action evidence.
	_ = text
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
			notice := "A11Y APPLICATION OBSERVATION: this lists only applications " +
				"currently visible through accessibility. It is not installed-software " +
				"inventory and absence does not prove an application is unavailable."
			return prependA11yScopeNotice(
				result,
				notice,
			)
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
		return prependA11yScopeNotice(
			result,
			"A11Y OBSERVATION: this reports only state visible through the "+
				"accessibility provider. It does not prove file contents, image "+
				"pixels, document formatting, persistence, or task completion.",
		)

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
			return prependA11yScopeNotice(
				result,
				"A11Y WINDOW ASSERTION SCOPE: this verdict covers only current "+
					"top-level window titles owned by the resolved AX application. "+
					"It does not prove file contents, persistence, or task completion.",
			)
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
			return prependA11yScopeNotice(
				result,
				"A11Y ASSERTION SCOPE: this verdict covers only the requested "+
					"AX-visible predicate. It does not verify document formatting, "+
					"image pixels, file contents, durable persistence, or whole-task completion.",
			)
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
		return prependA11yScopeNotice(
			result,
			"A11Y ASSERTION SCOPE: this verdict covers only the requested "+
				"AX-visible predicate. It does not verify document formatting, "+
				"image pixels, file contents, durable persistence, or whole-task completion.",
		)

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

func a11yBatchActionFields(tool string) map[string]bool {
	common := map[string]bool{
		"type": true, "target": true, "postcondition": true,
	}
	for _, field := range map[string][]string{
		"activate":  {},
		"close":     {},
		"click":     {},
		"keypress":  {"keys"},
		"scroll":    {"direction", "pages"},
		"set_value": {"value"},
		"type":      {"text"},
	}[tool] {
		common[field] = true
	}
	return common
}

func requiredA11yBatchString(
	action map[string]any,
	field string,
	allowEmpty bool,
) (string, error) {
	value, present := action[field]
	if !present {
		return "", fmt.Errorf("missing required field %q", field)
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", field)
	}
	if !allowEmpty && strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("%s must not be empty", field)
	}
	return text, nil
}

func normalizedA11yBatchKey(action map[string]any) (string, error) {
	rawKeys, hasKeys := action["keys"]
	if !hasKeys {
		return "", errors.New(`missing required field "keys"`)
	}

	items, ok := rawKeys.([]any)
	if !ok {
		typed, typedOK := rawKeys.([]string)
		if !typedOK {
			return "", errors.New("keys must be an array of key names")
		}
		items = make([]any, len(typed))
		for index, item := range typed {
			items[index] = item
		}
	}
	if len(items) == 0 || len(items) > guiMaxKeys {
		return "", fmt.Errorf(
			"keys must contain between 1 and %d key names", guiMaxKeys)
	}
	keys := make([]string, 0, len(items))
	for index, item := range items {
		key, ok := item.(string)
		if !ok || strings.TrimSpace(key) == "" {
			return "", fmt.Errorf(
				"keys[%d] must be a non-empty string", index)
		}
		key = strings.TrimSpace(key)
		if len([]rune(key)) > guiMaxKeyLength {
			return "", fmt.Errorf(
				"keys[%d] accepts at most %d characters",
				index, guiMaxKeyLength,
			)
		}
		keys = append(keys, key)
	}
	return strings.Join(keys, "+"), nil
}

func validateA11yBatchNumber(action map[string]any, field string) error {
	value, present := action[field]
	if !present {
		return nil
	}
	switch typed := value.(type) {
	case int:
		return nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return fmt.Errorf("%s must be a finite number", field)
		}
		return nil
	case json.Number:
		if _, err := typed.Float64(); err != nil {
			return fmt.Errorf("%s must be a number", field)
		}
		return nil
	default:
		return fmt.Errorf("%s must be a number", field)
	}
}

func normalizeA11yBatchActions(raw any) ([]a11yBatchAction, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, errors.New("actions must be an array")
	}
	if len(items) == 0 {
		return nil, errors.New(
			"actions must contain at least one action; use AX mode=\"observe\" for a read-only observation",
		)
	}
	if len(items) > a11yBatchMaxActions {
		return nil, fmt.Errorf(
			"actions accepts at most %d actions", a11yBatchMaxActions)
	}

	normalized := make([]a11yBatchAction, 0, len(items))
	totalText := 0
	for index, item := range items {
		action, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("actions[%d] must be an object", index)
		}
		tool, err := requiredA11yBatchString(action, "type", false)
		if err != nil {
			return nil, fmt.Errorf("actions[%d]: %w", index, err)
		}
		tool = strings.ToLower(strings.TrimSpace(tool))
		normalizedKey := ""
		switch tool {
		case "activate", "close", "click", "keypress", "scroll", "set_value", "type":
		default:
			return nil, fmt.Errorf(
				"actions[%d].type %q is unsupported; use activate, close, click, "+
					"keypress, scroll, set_value, or type",
				index, tool,
			)
		}
		allowed := a11yBatchActionFields(tool)
		for field := range action {
			if !allowed[field] {
				return nil, fmt.Errorf(
					"actions[%d] type %q does not accept field %q",
					index, tool, field,
				)
			}
		}
		elementIndex, _, err := a11yTargetAddress(action)
		if err != nil {
			return nil, fmt.Errorf(
				"actions[%d].target: %w", index, err)
		}
		if elementIndex != "" &&
			!isShortAXRef(elementIndex) &&
			!isShortAXWindowRef(elementIndex) {
			return nil, fmt.Errorf(
				"actions[%d].target must use a short ref returned by AX observe/find; "+
					"selector and index targets are not part of the public act contract",
				index,
			)
		}

		switch tool {
		case "activate", "close":
			if elementIndex == "" {
				return nil, fmt.Errorf(
					"actions[%d] %s requires target", index, tool)
			}
			if tool == "close" && index != len(items)-1 {
				return nil, fmt.Errorf(
					"actions[%d] close must be the final action", index)
			}
			if !isShortAXWindowRef(elementIndex) {
				return nil, fmt.Errorf(
					"actions[%d] %s requires target.ref matching wN",
					index, tool,
				)
			}
		case "click":
			if elementIndex == "" {
				return nil, fmt.Errorf(
					"actions[%d] click requires target", index)
			}
			if !isShortAXRef(elementIndex) {
				return nil, fmt.Errorf(
					"actions[%d] click requires target.ref matching rN", index)
			}
		case "keypress":
			normalizedKey, err = normalizedA11yBatchKey(action)
			if err != nil {
				return nil, fmt.Errorf("actions[%d]: %w", index, err)
			}
		case "scroll":
			direction, err := requiredA11yBatchString(
				action, "direction", false,
			)
			if err != nil {
				return nil, fmt.Errorf("actions[%d]: %w", index, err)
			}
			switch strings.ToLower(direction) {
			case "up", "down", "left", "right":
			default:
				return nil, fmt.Errorf(
					"actions[%d].direction must be up, down, left, or right",
					index,
				)
			}
			if err := validateA11yBatchNumber(action, "pages"); err != nil {
				return nil, fmt.Errorf("actions[%d]: %w", index, err)
			}
			pages := floatValue(optionalFloat(action, "pages"), 1)
			if pages <= 0 || pages > maxScrollPages {
				return nil, fmt.Errorf(
					"actions[%d].pages must be greater than 0 and at most %d",
					index, maxScrollPages,
				)
			}
		case "set_value":
			if elementIndex == "" {
				return nil, fmt.Errorf(
					"actions[%d] set_value requires target", index)
			}
			if !isShortAXRef(elementIndex) {
				return nil, fmt.Errorf(
					"actions[%d] set_value requires target.ref matching rN", index)
			}
			value, err := requiredA11yBatchString(action, "value", true)
			if err != nil {
				return nil, fmt.Errorf("actions[%d]: %w", index, err)
			}
			if len([]rune(value)) > maxActionText {
				return nil, fmt.Errorf(
					"actions[%d].value accepts at most %d characters",
					index, maxActionText,
				)
			}
			totalText += len([]rune(value))
		case "type":
			text, err := requiredA11yBatchString(action, "text", false)
			if err != nil {
				return nil, fmt.Errorf("actions[%d]: %w", index, err)
			}
			if len([]rune(text)) > maxActionText {
				return nil, fmt.Errorf(
					"actions[%d].text accepts at most %d characters",
					index, maxActionText,
				)
			}
			totalText += len([]rune(text))
		}
		if elementIndex != "" &&
			(tool == "keypress" || tool == "scroll" || tool == "type") &&
			!isShortAXRef(elementIndex) {
			return nil, fmt.Errorf(
				"actions[%d] %s target.ref must match rN", index, tool)
		}
		if totalText > a11yBatchMaxText {
			return nil, fmt.Errorf(
				"the cumulative text/value budget is %d characters",
				a11yBatchMaxText,
			)
		}
		var postcondition map[string]any
		if rawPostcondition, present := action["postcondition"]; present {
			postcondition, ok = rawPostcondition.(map[string]any)
			if !ok {
				return nil, fmt.Errorf(
					"actions[%d].postcondition must be an AX assertion object",
					index,
				)
			}
		}

		normalized = append(normalized, a11yBatchAction{
			actionType:    tool,
			targetRef:     elementIndex,
			direction:     strings.ToLower(optionalString(action, "direction")),
			pages:         floatValue(optionalFloat(action, "pages"), 1),
			text:          optionalString(action, "text"),
			key:           normalizedKey,
			value:         optionalString(action, "value"),
			postcondition: postcondition,
		})
	}
	return normalized, nil
}

func (s *service) executeA11yBatchAction(
	context *a11yBatchContext,
	action a11yBatchAction,
	obs observation,
) toolCallResult {
	const noDeclaredTarget = ""
	plan := settlePlan{}
	switch action.actionType {
	case "activate", "close":
		return s.windowAction(
			context.appToken,
			action.targetRef,
			noDeclaredTarget,
			action.actionType,
			obs,
			plan,
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
			plan,
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
			plan,
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
			plan,
		).withEvidenceScope("delivery_or_local_ui")
	case "set_value":
		return s.setValue(
			context.appToken,
			action.targetRef,
			noDeclaredTarget,
			action.value,
			obs,
			plan,
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
			plan,
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
		"A11Y BATCH REJECTED BEFORE ACTION 1: "+message+
			"\naction_outcome=not_executed\n"+
			"actions_completed_without_call_error=0\n"+
			"No accessibility action was attempted.",
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

func (s *service) a11yBatch(
	app string,
	rawActions any,
	obs observation,
	postcondition map[string]any,
	ownership ...string,
) toolCallResult {
	appRef := ""
	windowRef := ""
	if len(ownership) > 0 {
		appRef = ownership[0]
	}
	if len(ownership) > 1 {
		windowRef = ownership[1]
	}
	requested := 0
	if items, ok := rawActions.([]any); ok {
		requested = len(items)
	}
	items, ok := rawActions.([]any)
	if !ok {
		return a11yBatchValidationError(
			"actions must be an array", requested)
	}
	if len(items) == 0 {
		return a11yBatchValidationError(
			"mode=\"act\" requires at least one action; use mode=\"observe\" "+
				"for a read-only observation",
			0,
		)
	}
	actions, err := normalizeA11yBatchActions(rawActions)
	if err != nil {
		return a11yBatchValidationError(err.Error(), requested)
	}
	context, err := s.deriveA11yBatchContext(
		app, appRef, windowRef, actions,
	)
	if err != nil {
		return a11yBatchValidationError(err.Error(), requested)
	}
	if postcondition != nil {
		if err := s.preflightA11yPostcondition(context, postcondition); err != nil {
			return a11yBatchValidationError(
				"postcondition: "+err.Error(), requested,
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
			return a11yBatchValidationError(
				fmt.Sprintf(
					"actions[%d].postcondition: %s",
					index,
					err,
				),
				requested,
			)
		}
	}

	actionEvidence := make([]map[string]any, 0, len(actions))
	for index, action := range actions {
		actionObservation := a11yBatchActionObservation(
			index,
			len(actions),
			obs,
			postcondition != nil || action.postcondition != nil,
		)
		windowAction := action.actionType == "activate" ||
			action.actionType == "close"
		result := s.executeA11yBatchAction(
			context,
			action,
			actionObservation,
		)
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
		actionEntry := batchActionEvidenceEntry(
			index,
			action.actionType,
			result,
		)
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
			if !finalObservationReturned {
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
					"A11Y BATCH STOPPED AT ACTION %d/%d (%s). "+
						"%d earlier action call(s) returned without a call error. "+
						"This does not verify their delivery or task effect. "+
						"%s",
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
				action.actionType == "close",
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
		} else {
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
					action.actionType == "close",
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
	case "stale_window_ref", "stale_target", "observation_incomplete":
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
			"final_observation_unavailable: AX returned no snapshot after the postcondition",
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
