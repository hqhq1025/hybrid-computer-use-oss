package main

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

type actionElementResolution struct {
	snapshot  *appSnapshot
	element   *elementRecord
	semantic  *semanticTarget
	sourceRef string
	evidence  ActionabilityEvidence
}

func semanticTargetFromAddress(address string) (*semanticTarget, error) {
	selector := strings.TrimSpace(address)
	windowHandle := ""
	if isSemanticRef(selector) {
		decoded, scopedWindow, err := decodeSemanticRefScope(selector)
		if err != nil {
			return nil, fmt.Errorf("the semantic ref is malformed: %w", err)
		}
		selector = decoded
		windowHandle = scopedWindow
	}
	role, name, hasName, rest := parseSelector(selector)
	predicates, err := parsePredicates(rest)
	if err != nil {
		return nil, fmt.Errorf(
			"the selector %q has a trailing part I cannot read: %w. %s",
			selector, err, selectorPredicateHelp)
	}
	if strings.TrimSpace(role) == "" && !hasName && len(predicates) == 0 {
		return nil, fmt.Errorf("the semantic selector %q is empty", selector)
	}
	target := &semanticTarget{
		Selector:            selector,
		WindowHandle:        windowHandle,
		Role:                role,
		ProviderRoleAliases: providerAliasesForCanonicalRole(role),
		Name:                name,
		HasName:             hasName,
	}
	for _, predicate := range predicates {
		target.Predicates = append(target.Predicates, semanticTargetPredicate{
			Kind: predicate.kind, Value: predicate.value,
		})
	}
	return target, nil
}

func semanticTargetFromRefEntry(entry *AXRefEntry) (*semanticTarget, error) {
	if entry == nil {
		return nil, errors.New("the AX ref entry is missing")
	}
	var target *semanticTarget
	var err error
	if entry.LocatorKind == AXLocatorProviderBound {
		target = &semanticTarget{
			Selector: selectorHead(string(entry.Role), entry.Name),
			Role:     firstNonEmpty(entry.ProviderRole, string(entry.Role)),
			Name:     entry.Name,
			HasName:  strings.TrimSpace(entry.Name) != "",
		}
	} else if strings.TrimSpace(entry.SemanticTarget) != "" {
		target, err = semanticTargetFromAddress(entry.SemanticTarget)
		if err != nil {
			return nil, err
		}
	} else {
		return nil, errors.New("the AX ref has no stable target identity")
	}
	if target.WindowHandle != "" && entry.WindowHandle != "" &&
		target.WindowHandle != entry.WindowHandle {
		return nil, errors.New("the AX ref selector belongs to another window")
	}
	// The selector is presentation text; exact provider checks need raw labels.
	if target.HasName && entry.ProviderBusName != "" &&
		entry.ProviderObjectPath != "" && entry.ProcessGeneration != "" {
		target.Name = entry.Name
	}
	target.WindowHandle = firstNonEmpty(
		target.WindowHandle,
		entry.WindowHandle,
	)
	target.LocatorKind = entry.LocatorKind
	target.ProviderBusName = entry.ProviderBusName
	target.ProviderObjectPath = entry.ProviderObjectPath
	target.ProcessGeneration = entry.ProcessGeneration
	target.ProviderIdentityScope = entry.ProviderIdentityScope
	target.AutomationID = entry.AutomationID
	target.ProviderWindowBusName = entry.ProviderWindowBusName
	target.ProviderWindowObjectPath = entry.ProviderWindowObjectPath
	target.ProviderWindowProcessGeneration =
		entry.ProviderWindowProcessGeneration
	target.ProviderWindowRole = entry.ProviderWindowRole
	target.ProviderWindowName = entry.ProviderWindowName
	target.ProviderRoleAliases = providerAliasesForCanonicalRole(
		string(entry.Role),
	)
	if entry.ProviderRole != "" {
		target.ProviderRoleAliases = append(target.ProviderRoleAliases, entry.ProviderRole)
	}
	target.RuntimeID = append([]int(nil), entry.RuntimeID...)
	target.RuntimeIDAliases = make([][]int, 0, len(entry.RuntimeIDAliases))
	for _, path := range entry.RuntimeIDAliases {
		target.RuntimeIDAliases = append(
			target.RuntimeIDAliases,
			append([]int(nil), path...),
		)
	}
	return target, nil
}

func (s *service) authorizeProviderRebind(
	app string,
	entry *AXRefEntry,
	target *semanticTarget,
) {
	if target != nil && entry != nil &&
		(strings.TrimSpace(entry.ProviderIdentityScope) == "process_instance" ||
			strings.TrimSpace(target.ProviderIdentityScope) == "process_instance") {
		target.AllowProviderRebind = false
		return
	}
	if s == nil || entry == nil || target == nil ||
		!validInternalWindowHandle(entry.WindowHandle) {
		return
	}
	stableProviderAnchor := entry.LocatorKind == AXLocatorProviderBound &&
		strings.TrimSpace(entry.AutomationID) != "" && len(entry.RuntimeID) > 0 &&
		entry.ProviderWindowBusName != "" && entry.ProviderWindowObjectPath != "" &&
		entry.ProviderWindowProcessGeneration != ""
	if stableProviderAnchor {
		// The native resolver must prove the same role, control ID, runtime
		// lineage and owner before replacing a transient provider wrapper.
		target.AllowProviderRebind = true
		return
	}
	if entry.LocatorKind != AXLocatorSemantic {
		return
	}
	snapshot := s.currentSnapshot(app)
	coverage := axCoverageSummary{}
	if snapshot != nil {
		coverage = providerCoverageSummary(snapshot.Annotations)
	}
	if snapshot == nil || snapshot.axSnapshot == nil ||
		coverage.AcquisitionIncomplete > 0 ||
		coverage.ExpandableHidden > 0 ||
		snapshot.axSnapshot.Revision == "" ||
		snapshot.axSnapshot.Revision != entry.LastSeenRevision ||
		snapshot.WindowHandle != entry.WindowHandle ||
		snapshotLogicalWindowRef(snapshot) != entry.LogicalWindowRef {
		return
	}
	target.AllowProviderRebind = true
}

func semanticTargetIntentMismatch(target *semanticTarget, declared string) string {
	declared = strings.TrimSpace(declared)
	if declared == "" || target == nil {
		return ""
	}
	name := strings.TrimSpace(target.Name)
	description := ""
	for _, predicate := range target.Predicates {
		if predicate.Kind == "desc" {
			description = predicate.Value
			if name == "" {
				name = predicate.Value
			}
		}
	}
	if name == "" || !containsASCIILetter(declared) {
		return ""
	}
	if len([]rune(name)) <= 2 && strings.TrimSpace(description) == "" {
		return ""
	}
	haystack := strings.ToLower(name + " " + description)
	specific := 0
	for _, token := range strings.FieldsFunc(strings.ToLower(declared), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len([]rune(token)) < 2 || genericIntentWords[token] {
			continue
		}
		specific++
		if strings.Contains(haystack, token) {
			return ""
		}
	}
	if specific == 0 {
		return ""
	}
	return fmt.Sprintf(
		"semantic selector %q does not match what you said you were targeting (%q). "+
			"No input was emitted. Use a selector/ref that names the intended control, "+
			"or correct the element description.",
		target.Selector, declared)
}

// resolveActionElement keeps numeric addresses bound to the cached snapshot.
// Semantic selectors and refs are only parsed here: the provider resolves them
// against the live tree inside the same invocation that emits input.
func (s *service) resolveActionElement(
	app,
	address,
	declared,
	tool string,
) (*actionElementResolution, toolCallResult) {
	address = strings.TrimSpace(address)
	if address == "" {
		return nil, textResult(
			tool+" requires a current rN element ref or wN window ref.", true)
	}
	if isShortAXRef(address) {
		entry, err := s.resolveShortAXRef(app, address)
		if err != nil {
			return nil, textResult(tool+": "+err.Error(), true)
		}
		target, targetErr := semanticTargetFromRefEntry(entry)
		if targetErr != nil {
			return nil, textResult(tool+": "+targetErr.Error(), true)
		}
		s.authorizeProviderRebind(app, entry, target)
		return &actionElementResolution{
			snapshot:  s.currentSnapshot(app),
			semantic:  target,
			sourceRef: address,
			evidence:  initialActionabilityEvidence(tool, nil, target),
		}, toolCallResult{}
	}
	if isShortAXWindowRef(address) {
		if tool != "activate" && tool != "close" {
			return nil, textResult(
				tool+": window refs can only be used with activate or close",
				true,
			).withEvidenceScope("error_only")
		}
		entry, err := s.resolveShortAXWindowRef(app, address)
		if err != nil {
			return nil, textResult(
				tool+": stale_window; "+err.Error()+
					"; input_emitted=false; recovery=observe",
				true,
			).withEvidenceScope("error_only")
		}
		target, targetErr := semanticTargetFromAddress(entry.SemanticTarget)
		if targetErr != nil {
			return nil, textResult(
				tool+": invalid_target; input_emitted=false; recovery=observe",
				true,
			).withEvidenceScope("error_only")
		}
		target.WindowHandle = entry.WindowHandle
		target.ProviderWindowBusName = entry.ProviderWindowBusName
		target.ProviderWindowObjectPath = entry.ProviderWindowObjectPath
		target.ProviderWindowProcessGeneration =
			entry.ProviderWindowProcessGeneration
		target.ProviderWindowRole = entry.ProviderWindowRole
		target.ProviderWindowName = entry.ProviderWindowName
		return &actionElementResolution{
			snapshot:  s.currentSnapshot(app),
			semantic:  target,
			sourceRef: address,
			evidence:  initialActionabilityEvidence(tool, nil, target),
		}, toolCallResult{}
	}

	if !numericElementAddress(address) {
		target, err := semanticTargetFromAddress(address)
		if err != nil {
			return nil, textResult(tool+": "+err.Error(), true)
		}
		if mismatch := semanticTargetIntentMismatch(target, declared); mismatch != "" {
			result := textResult(mismatch, true)
			result.StructuredContent = map[string]any{
				"action_attempted":     false,
				"input_events_emitted": false,
			}
			return nil, result
		}
		snapshot := s.currentSnapshot(app)
		if target.WindowHandle == "" && snapshot != nil {
			target.WindowHandle = snapshot.WindowHandle
		}
		return &actionElementResolution{
			snapshot: snapshot,
			semantic: target,
			evidence: initialActionabilityEvidence(
				tool, nil, target,
			),
		}, toolCallResult{}
	}

	snapshot := s.currentSnapshot(app)
	if snapshot == nil {
		return nil, textResult(
			"No app state is available for "+app+
				". Run AX mode=observe or find and use the returned current rN ref.",
			true)
	}

	record, err := lookupElementFor(snapshot, address, declared, tool)
	if err != nil {
		result := textResult(err.Error(), true)
		result.StructuredContent = map[string]any{}
		result.StructuredContent["action_attempted"] = false
		result.StructuredContent["input_events_emitted"] = false
		return nil, result
	}
	if mismatch := elementIntentMismatch(record, declared); mismatch != "" {
		result := textResult(mismatch, true)
		result.StructuredContent = map[string]any{}
		result.StructuredContent["action_attempted"] = false
		result.StructuredContent["input_events_emitted"] = false
		return nil, result
	}
	if selector := semanticSelectorForRecord(snapshot, *record); selector != "" &&
		strings.TrimSpace(snapshot.WindowHandle) != "" {
		ref := encodeScopedSemanticRef(selector, snapshot.WindowHandle)
		target, targetErr := semanticTargetFromAddress(ref)
		if targetErr == nil {
			sourceIndex := record.Index
			target.SourceIndex = &sourceIndex
			return &actionElementResolution{
				snapshot: snapshot,
				semantic: target,
				evidence: initialActionabilityEvidence(
					tool, record, target,
				),
			}, toolCallResult{}
		}
	}
	return &actionElementResolution{
		snapshot: snapshot,
		element:  record,
		evidence: initialActionabilityEvidence(
			tool, record, nil,
		),
	}, toolCallResult{}
}

func applyActionElementResolution(
	request *axServiceRequest,
	resolution *actionElementResolution,
) {
	if request == nil || resolution == nil {
		return
	}
	request.Element = resolution.element
	request.SemanticTarget = resolution.semantic
	request.SourceRef = resolution.sourceRef
	evidence := resolution.evidence
	request.Actionability = &evidence
	// A numeric element carries geometry from the exact cached snapshot it was
	// resolved from. A semantic target deliberately omits stale geometry: the
	// runtime computes current bounds while resolving the live node.
	if resolution.element != nil && resolution.snapshot != nil {
		request.WindowBounds = resolution.snapshot.WindowBounds
	}
}

func (s *service) click(app, elementIndex, declared string, x, y *float64, clickCount int, mouseButton, clickMethod string, obs observation, plan settlePlan) toolCallResult {
	if app == "" {
		return textResult("Missing required argument: app", true)
	}
	if x != nil || y != nil {
		// The channel must be evident from the tool name. A coordinate click
		// targets no element (whatever lies under the point receives it), so
		// it belongs to GUI rather than being a variant of the AX click.
		return textResult("AX click does not accept x/y. Use target.ref with a current rN; use GUI for full-desktop pixel coordinates.", true)
	}
	if elementIndex == "" {
		return textResult("click requires target.ref with a current rN. If find cannot issue a stable ref, use GUI with coordinates from its full-desktop screenshot.", true)
	}
	if clickMethod == "accessibility" && elementIndex == "" {
		return textResult("click_method 'accessibility' requires target.ref", true)
	}
	if clickMethod == "app_post" {
		return textResult("click_method 'app_post' is not supported on Linux", true)
	}
	if clickMethod == "sky_click" {
		return textResult("click_method 'sky_click' is not supported on Linux", true)
	}
	// AX clicks always target an element; bare coordinates belong to GUI. The
	// synthesis fallback for element clicks converts the window-relative frame
	// to screen coordinates in the provider's screen_point. GUI does not use
	// this path: it takes absolute full-desktop screenshot coordinates, without
	// adding a window origin or clamping to a target window.
	resolution, result := s.resolveActionElement(
		app, elementIndex, declared, "click")
	if result.IsError {
		return result
	}
	request := axServiceRequest{
		App: app,
		Action: axClickCommand{
			X: x, Y: y, ClickCount: clickCount,
			MouseButton: mouseButton, ClickMethod: clickMethod,
		},

		IncludeScreenshot: obs.resolvedScreenshot(),
		Observe:           obs.resolvedMode(),
		CaptureSnapshot:   obs.captureSnapshot,
	}
	applyActionElementResolution(&request, resolution)
	return s.actionResultSettling(app, request, plan)
}

func (s *service) windowAction(
	app,
	elementIndex,
	declared,
	action string,
	obs observation,
	plan settlePlan,
) toolCallResult {
	if strings.TrimSpace(app) == "" {
		return textResult("Missing required argument: app", true)
	}
	if action != "activate" && action != "close" {
		return textResult("unsupported AX window action: "+action, true)
	}
	resolution, result := s.resolveActionElement(
		app, elementIndex, declared, action)
	if result.IsError {
		return result
	}
	windowOperation := WindowOperation(action)
	request := axServiceRequest{
		WindowAction: &windowOperation, App: app,
		IncludeScreenshot: obs.resolvedScreenshot(),
		Observe:           obs.resolvedMode(),
		CaptureSnapshot:   obs.captureSnapshot,
	}
	applyActionElementResolution(&request, resolution)
	return s.actionResultSettling(app, request, plan)
}

func (s *service) scroll(
	app, direction, elementIndex, declared, targetWindowHandle string,
	expectedWindow *ProviderWindowExpectation,
	pages float64,
	obs observation,
	plan settlePlan,
) toolCallResult {
	if app == "" {
		return textResult("Missing required argument: app", true)
	}
	// The target may be omitted, which scrolls the current window at its
	// centre. Scrolling a whole page would otherwise require naming the page
	// element, and the role of a web page's root varies across sites
	// (document web, document frame, section). The wheel acts on the
	// scrollable ancestor under the pointer (see scroll_element in the
	// provider), and the window centre is almost always the main content.
	// Pass a target to scroll a specific panel.
	normalized := strings.ToLower(direction)
	if normalized != "up" && normalized != "down" && normalized != "left" && normalized != "right" {
		return textResult("Invalid scroll direction: "+direction, true)
	}
	if pages <= 0 {
		return textResult("pages must be > 0", true)
	}
	if elementIndex == "" {
		return s.actionResultSettling(app, axServiceRequest{
			App: app,
			Action: axScrollCommand{
				Direction: normalized, Pages: pages, WindowCentre: true,
			},
			TargetWindowHandle: targetWindowHandle,
			ExpectedWindow:     expectedWindow,
			IncludeScreenshot:  obs.resolvedScreenshot(), Observe: obs.resolvedMode(),
			CaptureSnapshot: obs.captureSnapshot,
		}, plan)
	}
	resolution, result := s.resolveActionElement(
		app, elementIndex, declared, "scroll")
	if result.IsError {
		return result
	}
	request := axServiceRequest{
		App:                app,
		Action:             axScrollCommand{Direction: normalized, Pages: pages},
		TargetWindowHandle: targetWindowHandle,
		IncludeScreenshot:  obs.resolvedScreenshot(), Observe: obs.resolvedMode(),
		CaptureSnapshot: obs.captureSnapshot,
	}
	applyActionElementResolution(&request, resolution)
	return s.actionResultSettling(app, request, plan)
}

func (s *service) typeText(
	app, elementIndex, declared, targetWindowHandle string,
	expectedWindow *ProviderWindowExpectation,
	text string,
	obs observation,
	plan settlePlan,
) toolCallResult {
	if app == "" {
		return textResult("Missing required argument: app", true)
	}
	if text == "" {
		return textResult("Missing required argument: text", true)
	}
	if elementIndex == "" && strings.TrimSpace(declared) != "" {
		return textResult("targeted type requires target.ref; refusing to silently fall back to untargeted input", true)
	}
	request := axServiceRequest{
		App: app, Action: axTypeCommand{Text: text},
		TargetWindowHandle: targetWindowHandle,
		ExpectedWindow:     expectedWindow,
		IncludeScreenshot:  obs.resolvedScreenshot(), Observe: obs.resolvedMode(),
		CaptureSnapshot: obs.captureSnapshot,
	}
	if elementIndex != "" {
		var result toolCallResult
		var resolution *actionElementResolution
		resolution, result = s.resolveActionElement(
			app, elementIndex, declared, "type")
		if result.IsError {
			return result
		}
		applyActionElementResolution(&request, resolution)
	}
	return s.actionResultSettling(app, request, plan)
}

func (s *service) pressKey(
	app, elementIndex, declared, targetWindowHandle string,
	expectedWindow *ProviderWindowExpectation,
	key string,
	obs observation,
	plan settlePlan,
) toolCallResult {
	if app == "" {
		return textResult("Missing required argument: app", true)
	}
	if key == "" {
		return textResult("Missing required argument: key", true)
	}
	if elementIndex == "" && strings.TrimSpace(declared) != "" {
		return textResult("targeted keypress requires target.ref; refusing to silently fall back to untargeted input", true)
	}
	request := axServiceRequest{
		App: app, Action: axKeypressCommand{Key: key},
		TargetWindowHandle: targetWindowHandle,
		ExpectedWindow:     expectedWindow,
		IncludeScreenshot:  obs.resolvedScreenshot(), Observe: obs.resolvedMode(),
		CaptureSnapshot: obs.captureSnapshot,
	}
	if elementIndex != "" {
		var result toolCallResult
		var resolution *actionElementResolution
		resolution, result = s.resolveActionElement(
			app, elementIndex, declared, "keypress")
		if result.IsError {
			return result
		}
		applyActionElementResolution(&request, resolution)
	}
	return s.actionResultSettling(app, request, plan)
}

func (s *service) setValue(app, elementIndex, declared, value string, obs observation, plan settlePlan) toolCallResult {
	if app == "" {
		return textResult("Missing required argument: app", true)
	}
	if elementIndex == "" {
		return textResult("set_value requires target.ref with a current rN", true)
	}
	resolution, result := s.resolveActionElement(
		app, elementIndex, declared, "set_value")
	if result.IsError {
		return result
	}
	request := axServiceRequest{
		App: app, Action: axSetValueCommand{Value: value},
		IncludeScreenshot: obs.resolvedScreenshot(), Observe: obs.resolvedMode(),
		CaptureSnapshot: obs.captureSnapshot,
	}
	applyActionElementResolution(&request, resolution)
	return s.actionResultSettling(app, request, plan)
}
