package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"regexp"
	"strconv"
	"strings"
)

func (s *service) gui(actions any, settleMS int) toolCallResult {
	providerActions, err := decodeProviderDesktopActions(actions)
	if err != nil {
		return guiArgumentError(err, actions)
	}
	if err := checkGUIWaitBudget(providerActions); err != nil {
		return withArgumentRule(guiError(axCodeInvalidArgument, "actions", err.Error(), guiScreenshotCall), err)
	}
	if err := s.preflightPlatformGUIKeys(providerActions); err != nil {
		return guiArgumentError(err, actions)
	}
	if s.platformBackend().HostInfo().Platform == "windows" {
		if err := preflightWindowsGUIKeys(providerActions); err != nil {
			return guiArgumentError(err, actions)
		}
	}
	privateFrameID := ""
	if guiBatchUsesFrameProvenance(providerActions) {
		if s.guiSnapshot == nil || s.guiSnapshot.FrameID == "" {
			// Observe only: the provider wire rejects an empty batch, and an
			// explicit screenshot action is the one batch that emits no input.
			observed, err := s.callProviderDesktop(ProviderDesktopRequest{
				ProtocolVersion: providerProtocolV2,
				Actions:         []ProviderDesktopAction{{Type: "screenshot"}},
			})
			if err != nil {
				if result, rejected := runtimeRequestRejectionResult(err); rejected {
					return result
				}
				return guiError(axCodeRuntimeError, "", err.Error(), guiScreenshotCall)
			}
			// The internal screenshot is not one of the caller's actions.
			observed.GUIResult.Dispatched = nil
			result := s.guiResponse(*observed)
			if !result.IsError {
				result.IsError = true
				setGUIPublicError(&result, axCodeGUIFrameStale, "",
					"GUI coordinate/window actions require a prior GUI screenshot. "+
						"No actions were emitted; use the attached fresh screenshot.",
					guiScreenshotCall)
			}
			return result
		}
		privateFrameID = s.guiSnapshot.FrameID
	}
	response, err := s.callProviderDesktop(ProviderDesktopRequest{
		ProtocolVersion: providerProtocolV2,
		Actions:         providerActions,
		SettleMS:        settleMS,
		FrameID:         privateFrameID,
	})
	if err != nil {
		if result, rejected := runtimeRequestRejectionResult(err); rejected {
			// The provider rejected the request before dispatch.
			setGUIPublicError(&result, axCodeInvalidArgument, "", err.Error(), guiScreenshotCall)
			return result
		}
		var timeout *runtimeTimeoutError
		var transport *runtimeTransportError
		mayMutate := guiBatchCanEmitInput(providerActions)
		requestMayHaveReachedProvider := errors.As(err, &timeout) ||
			(errors.As(err, &transport) && transport.requestSent)
		mutationUnknown := mayMutate && requestMayHaveReachedProvider
		if mutationUnknown {
			s.invalidateAccessibilitySnapshots()
		}
		if timeout != nil || transport != nil {
			guiResult := ProviderGUIResult{MutationUnknown: &mutationUnknown}
			failureCode := "runtime_timeout"
			phase := "runtime"
			if timeout != nil && strings.TrimSpace(timeout.phase) != "" {
				phase = timeout.phase
			}
			if transport != nil {
				failureCode = "runtime_transport_failure"
				if strings.TrimSpace(transport.phase) != "" {
					phase = transport.phase
				}
			}
			explanation := "No desktop mutation is inferred from this failure."
			code := axCodeRuntimeError
			if mutationUnknown {
				explanation = "The GUI runtime may have emitted input or a window command " +
					"before the failure; inspect fresh desktop state before retrying."
				code = axCodeEffectUnknown
			}
			evidence := map[string]any{
				"failure_code":         failureCode,
				"failure_phase":        phase,
				"mutation_unknown":     mutationUnknown,
				"observation_required": mutationUnknown,
			}
			var result toolCallResult
			if s.guiSnapshot != nil {
				// The previous screenshot stays attached, as before; its
				// screen metadata says which image it is.
				result = s.guiSnapshot.textWithNotes(explanation, nil)
				for key, value := range guiMetadata(s.guiSnapshot, guiResult) {
					evidence[key] = value
				}
			} else {
				result = textResult(explanation, true)
				evidence["actions"] = compactGUIResult(guiResult)
			}
			result.IsError = true
			result.StructuredContent = evidence
			setGUIPublicError(&result, code, "", err.Error(), guiScreenshotCall)
			return result
		}
		// The provider failed before or without dispatch; nothing is
		// inferred about input.
		return guiError(axCodeRuntimeError, "", err.Error(), guiScreenshotCall)
	}
	return s.guiResponse(*response)
}

// guiScreenshotCall is the GUI observation call. It is the hint for every
// GUI error whose correction is to look at the desktop again.
const guiScreenshotCall = `{"actions":[{"type":"screenshot"}]}`

const guiCodePermissionDenied = "permission_denied"

const guiPermissionHint = "Ask the user to grant the denied system permission to the current Computer Use Provider " +
	"(on macOS: Screen Recording or Accessibility in System Settings > Privacy & Security), " +
	"restart the provider, then take a fresh GUI screenshot before retrying input."

// guiPublicErrorCodes are the public codes a GUI result can carry. They are
// a subset of the error table.
var guiPublicErrorCodes = []string{
	axCodeInvalidArgument, axCodeGUIFrameStale, axCodeWindowChanged,
	axCodeEffectUnknown, axCodeRuntimeError, guiCodePermissionDenied,
}

// guiPublicFields are the leading fields of the GUI evidence line, in order:
// the AX evidence fields, then the returned screen and the action facts.
var guiPublicFields = append(append([]string{}, axPublicFields...), "screen", "actions")

// guiActionHints are the invalid_argument hints for an action of a known
// type: one valid action of that type.
var guiActionHints = map[string]string{
	"click":        `{"type":"click","x":640,"y":412}`,
	"double_click": `{"type":"double_click","x":640,"y":412}`,
	"move":         `{"type":"move","x":640,"y":412}`,
	"drag":         `{"type":"drag","path":[{"x":100,"y":200},{"x":300,"y":200}]}`,
	"scroll":       `{"type":"scroll","x":640,"y":412,"scroll_y":-3}`,
	"type":         `{"type":"type","text":"hello"}`,
	"keypress":     `{"type":"keypress","keys":["CTRL","S"]}`,
	"wait":         `{"type":"wait"}`,
}

// guiError is a GUI error result that carries its public code.
func guiError(code, path, message, hint string) toolCallResult {
	result := textResult(message, true).withEvidenceScope("error_only")
	setGUIPublicError(&result, code, path, message, hint)
	return result
}

// setGUIPublicError records the public error fields of a GUI result under
// the same private keys AX uses; prepareGUIEvidence renders them.
func setGUIPublicError(result *toolCallResult, code, path, message, hint string) {
	if result.StructuredContent == nil {
		result.StructuredContent = map[string]any{}
	}
	result.StructuredContent[axPublicErrorCodeKey] = code
	for key, value := range map[string]string{
		axPublicErrorPathKey:   path,
		axPublicErrorDetailKey: message,
		axPublicErrorHintKey:   hint,
	} {
		if value != "" {
			result.StructuredContent[key] = value
		}
	}
}

var (
	guiActionPathPattern   = regexp.MustCompile(`actions\[(\d+)\](\.[a-z_]+)?`)
	guiUnknownFieldPattern = regexp.MustCompile(`(?:unknown field|does not accept fields:) "([^"]+)"`)
)

// guiErrorPath is the public path of a GUI argument error: the action as
// actions[N], and the field when the message names one, including a single
// unknown field ("unknown field \"ms\"" gives actions[0].ms).
func guiErrorPath(path, message string) string {
	match := guiActionPathPattern.FindStringSubmatch(path)
	if match == nil {
		return path
	}
	if match[2] == "" {
		if unknown := guiUnknownFieldPattern.FindAllStringSubmatch(message, -1); len(unknown) == 1 &&
			!strings.Contains(message, `", "`) {
			return match[0] + "." + unknown[0][1]
		}
	}
	return match[0]
}

// guiArgumentError is the invalid_argument result for a decoder or key
// preflight error; the error text names the action as actions[N].
func guiArgumentError(err error, actions any) toolCallResult {
	path := guiErrorPath(guiActionPathPattern.FindString(err.Error()), err.Error())
	args := map[string]any{"actions": actions}
	return guiError(axCodeInvalidArgument, path, err.Error(), guiArgumentHint(args, path))
}

// guiArgumentHint is the corrected call for an invalid GUI argument at path
// (actions[N].field, or a top-level field). For an action it is one valid
// action of the same type; for a top-level field it is the call without
// the rejected fields when that is valid; otherwise it is a screenshot.
func guiArgumentHint(args map[string]any, path string) string {
	actions, _ := args["actions"].([]any)
	if match := guiActionPathPattern.FindStringSubmatch(path); match != nil {
		index, _ := strconv.Atoi(match[1])
		if index < len(actions) {
			action, _ := actions[index].(map[string]any)
			return guiActionHint(action, strings.TrimPrefix(match[2], "."))
		}
		return guiActionHints["click"]
	}
	if path == "actions" || len(actions) == 0 {
		return guiScreenshotCall
	}
	trimmed := map[string]any{"actions": args["actions"]}
	if settle, present := args["settle_ms"]; present && path != "settle_ms" {
		trimmed["settle_ms"] = settle
	}
	if validateToolArguments(publicToolGUI, trimmed) == nil {
		if hint := orderedJSON(trimmed, []string{"actions", "type"}); len(hint) <= maxArgumentHintBytes {
			return hint
		}
	}
	return guiScreenshotCall
}

// guiActionHint corrects one rejected action. It keeps the caller's values
// where it can: first the action without the rejected field, then the
// action reduced to the fields of its type, and last one valid action of
// that type.
func guiActionHint(action map[string]any, field string) string {
	kind, _ := action["type"].(string)
	kind = strings.ToLower(strings.TrimSpace(kind))
	example := guiActionHints[kind]
	if example == "" {
		return guiActionHints["click"]
	}
	var candidates []map[string]any
	if field != "" && field != "type" {
		without := cloneObject(action)
		delete(without, field)
		candidates = append(candidates, without)
	}
	var shape map[string]any
	_ = json.Unmarshal([]byte(example), &shape)
	reduced := map[string]any{}
	for key := range shape {
		if value, present := action[key]; present {
			reduced[key] = value
		}
	}
	candidates = append(candidates, reduced)
	for _, candidate := range candidates {
		candidate["type"] = kind
		if validateToolArguments(publicToolGUI, map[string]any{"actions": []any{candidate}}) == nil {
			if hint := orderedJSON(candidate, []string{"type", "x", "y"}); len(hint) <= maxArgumentHintBytes {
				return hint
			}
		}
	}
	return example
}

// guiArgumentPath turns a JSON pointer such as /actions/0/button into the
// public path actions[0].button.
func guiArgumentPath(pointer string) string {
	var out strings.Builder
	for _, segment := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		if segment == "" {
			continue
		}
		if _, err := strconv.Atoi(segment); err == nil {
			out.WriteString("[" + segment + "]")
			continue
		}
		if out.Len() > 0 {
			out.WriteByte('.')
		}
		out.WriteString(segment)
	}
	return out.String()
}

// guiProviderFailure maps a failed runtime batch to its public code, path
// and message. Every provider marks a stale coordinate frame with a failure
// code (stale_gui_frame; stale_layout on Windows).
func guiProviderFailure(response ProviderDesktopResult, guiResult ProviderGUIResult) (code, path, message string) {
	failureCode := ""
	if response.Failure != nil {
		failureCode = response.Failure.Code
	}
	emitted := guiResultEmitted(guiResult)
	switch {
	case failureCode == "stale_gui_frame" || failureCode == "stale_layout":
		if emitted {
			// Earlier actions ran; the layout changed between them.
			return axCodeWindowChanged, "", response.Error
		}
		return axCodeGUIFrameStale, "", response.Error
	case len(response.ValidationErrors) > 0:
		// The published schema already rejects malformed actions, so these
		// are coordinate checks against the live desktop; the fix is to plan
		// again from a screenshot.
		first := response.ValidationErrors[0]
		index, _ := intEvidence(first["index"])
		path = fmt.Sprintf("actions[%d]", index)
		detail, _ := first["error"].(string)
		return axCodeInvalidArgument, path, path + ": " + detail
	case failureCode == "invalid_gui_action":
		return axCodeInvalidArgument, "", response.Error
	case emitted || isTrue(guiResult.MutationUnknown) ||
		isTrue(guiResult.PartialInputPossible) ||
		providerMutationCanInvalidate(response.Mutation):
		return axCodeEffectUnknown, "", response.Error
	case failureCode == guiCodePermissionDenied:
		return guiCodePermissionDenied, "", response.Error
	}
	return axCodeRuntimeError, "", response.Error
}

func guiProviderFailureHint(response ProviderDesktopResult) string {
	if response.Failure != nil && response.Failure.Code == guiCodePermissionDenied {
		return guiPermissionHint
	}
	return guiScreenshotCall
}

func guiResultEmitted(guiResult ProviderGUIResult) bool {
	return isTrue(guiResult.InputEventsEmitted) ||
		(guiResult.InputActionsDispatched != nil && *guiResult.InputActionsDispatched > 0)
}

// withGUIRequested records the number of requested actions, so that the
// evidence line reports completed and requested like an AX act, also when
// the call was rejected before any action (requested is then 0 for a
// missing or empty actions array). A screenshot observation has no counts.
func withGUIRequested(result toolCallResult, raw any) toolCallResult {
	actions, _ := raw.([]any)
	if len(actions) == 0 && !result.IsError {
		return result
	}
	if len(actions) == 1 {
		if action, _ := actions[0].(map[string]any); action["type"] == "screenshot" {
			return result
		}
	}
	if result.StructuredContent == nil {
		result.StructuredContent = map[string]any{}
	}
	result.StructuredContent["gui_requested"] = len(actions)
	return result
}

// prepareGUIEvidence compacts a GUI result into its public evidence: ok,
// code, path, message, hint, input_emitted, completed and requested (the
// AX fields), then screen and actions. The message is not repeated in the
// text that follows the evidence line.
func prepareGUIEvidence(result toolCallResult) toolCallResult {
	full := result.StructuredContent
	if full == nil {
		full = map[string]any{}
	}
	actions := map[string]any{}
	if raw, ok := full["actions"].(map[string]any); ok {
		for key, value := range raw {
			actions[key] = value
		}
	}
	emitted := actions["input_events_emitted"] == true
	if count, ok := intEvidence(actions["input_actions_dispatched"]); ok && count > 0 {
		emitted = true
	}
	public := map[string]any{"ok": !result.IsError}
	if result.IsError {
		code, _ := full[axPublicErrorCodeKey].(string)
		if code == "" {
			code = axCodeRuntimeError
			if full["provider_request_rejected"] == true || full["argument_error_code"] != nil {
				code = axCodeInvalidArgument
			}
		}
		public["code"] = code
		if path, _ := full[axPublicErrorPathKey].(string); path != "" {
			public["path"] = path
		}
		message, _ := full[axPublicErrorDetailKey].(string)
		if message == "" {
			for _, item := range result.Content {
				if item.Type == "text" && strings.TrimSpace(item.Text) != "" {
					message = item.Text
					break
				}
			}
		}
		message = firstLine(strings.TrimSpace(message))
		if message != "" {
			public["message"] = truncateRunes(message, 300)
			result.Content = withoutLeadingLine(result.Content, message)
		}
		hint, _ := full[axPublicErrorHintKey].(string)
		if hint == "" {
			hint = guiScreenshotCall
		}
		public["hint"] = hint
	}
	public["input_emitted"] = emitted
	if requested, ok := intEvidence(full["gui_requested"]); ok {
		completed, _ := intEvidence(actions["dispatched"])
		public["completed"] = completed
		public["requested"] = requested
	}
	// The public fields replace their internal spellings.
	delete(actions, "dispatched")
	delete(actions, "input_events_emitted")
	if screen, ok := full["screen"].(map[string]any); ok {
		public["screen"] = screen
	}
	if len(actions) > 0 {
		public["actions"] = actions
	}
	if full["observation_required"] == true {
		public["observation_required"] = true
	}
	result.StructuredContent = public
	return result
}

// withoutLeadingLine drops line from the start of the first text item, and
// the item itself when nothing else remains.
func withoutLeadingLine(content []contentItem, line string) []contentItem {
	for index, item := range content {
		if item.Type != "text" {
			continue
		}
		text := strings.TrimSpace(item.Text)
		if firstLine(text) != line {
			return content
		}
		rest := strings.TrimSpace(strings.TrimPrefix(text, line))
		out := append([]contentItem{}, content[:index]...)
		if rest != "" {
			out = append(out, contentItem{Type: "text", Text: rest})
		}
		return append(out, content[index+1:]...)
	}
	return content
}

func guiBatchUsesFrameProvenance(actions []ProviderDesktopAction) bool {
	for _, action := range actions {
		switch strings.ToLower(strings.TrimSpace(action.Type)) {
		case "click", "double_click", "move", "drag", "scroll":
			return true
		}
	}
	return false
}

func guiBatchCanEmitInput(actions []ProviderDesktopAction) bool {
	for _, action := range actions {
		switch strings.ToLower(strings.TrimSpace(action.Type)) {
		case "click", "double_click", "move", "drag", "scroll",
			"type", "keypress":
			return true
		}
	}
	return false
}

// guiMetadata is the screen and action evidence of a delivered screenshot.
func guiMetadata(snapshot *appSnapshot, guiResult ProviderGUIResult) map[string]any {
	if snapshot == nil {
		return nil
	}
	model := map[string]any{
		"screen": map[string]any{
			"coordinate_space":    snapshot.CoordinateSpace,
			"width":               snapshot.Width,
			"height":              snapshot.Height,
			"screenshot_returned": true,
		},
	}
	if compact := compactGUIResult(guiResult); len(compact) > 0 {
		model["actions"] = compact
	}
	return model
}

// compactGUIResult is the model-visible subset of the runtime's GUI evidence.
func compactGUIResult(full ProviderGUIResult) map[string]any {
	out := map[string]any{}
	if full.Dispatched != nil && *full.Dispatched > 0 {
		out["dispatched"] = *full.Dispatched
	}
	if full.InputActionsDispatched != nil && *full.InputActionsDispatched > 0 {
		out["input_actions_dispatched"] = *full.InputActionsDispatched
	}
	for key, value := range map[string]*bool{
		"input_events_emitted":   full.InputEventsEmitted,
		"partial_input_possible": full.PartialInputPossible,
		"mutation_unknown":       full.MutationUnknown,
		"effect_unknown":         full.EffectUnknown,
	} {
		if isTrue(value) {
			out[key] = true
		}
	}
	for key, value := range map[string]*bool{
		"quiescent":              full.Quiescent,
		"visual_change_observed": full.VisualChangeObserved,
	} {
		if value != nil {
			out[key] = *value
		}
	}
	return out
}

func guiInvalidatesAccessibility(guiResult ProviderGUIResult) bool {
	return isTrue(guiResult.InputEventsEmitted) ||
		isTrue(guiResult.PartialInputPossible) ||
		isTrue(guiResult.MutationUnknown)
}

func isTrue(value *bool) bool {
	return value != nil && *value
}

func guiValidationErrorText(response ProviderDesktopResult) string {
	if len(response.ValidationErrors) == 0 {
		return response.Error
	}
	lines := []string{response.Error + ":"}
	for _, item := range response.ValidationErrors {
		index, _ := intEvidence(item["index"])
		actionType, _ := item["type"].(string)
		message, _ := item["error"].(string)
		lines = append(lines, fmt.Sprintf(
			"- actions[%d] type=%q: %s",
			index, actionType, message,
		))
	}
	return strings.Join(lines, "\n")
}

func (s *service) invalidateAccessibilitySnapshots() {
	s.snapshots = map[string]*appSnapshot{}
	s.accessibilityEpoch++
	s.exposedHiddenRefs = map[string]map[int]elementRecord{}
	s.findCursorRegistry.Reset()
	s.seenAccessibilityEpoch = s.accessibilityEpoch
}

func (s *service) guiResponse(response ProviderDesktopResult) toolCallResult {
	guiResult := response.GUIResult
	mergeProviderMutationIntoGUIResult(&guiResult, response.Mutation)
	// GUI input can change any application without passing through the accessibility
	// track. Once the runtime confirms event emission, every cached tree and expect
	// baseline is stale, including responses that later report a partial failure.
	if providerMutationCanInvalidate(response.Mutation) ||
		guiInvalidatesAccessibility(guiResult) {
		s.invalidateAccessibilitySnapshots()
	}
	if response.Snapshot == nil {
		return guiObservationFailure(
			response,
			guiResult,
			"GUI runtime did not return the required full-desktop screenshot.",
		)
	}
	if response.Snapshot.ScreenshotPNGBase64 == "" {
		return guiObservationFailure(
			response,
			guiResult,
			"GUI runtime returned an empty full-desktop screenshot.",
		)
	}
	if response.Snapshot.CoordinateSpace == "" ||
		response.Snapshot.Width <= 0 || response.Snapshot.Height <= 0 {
		return guiObservationFailure(
			response,
			guiResult,
			"GUI runtime did not return the required coordinate_space/width/height metadata.",
		)
	}
	if err := validateGUISnapshotImage(response.Snapshot); err != nil {
		return guiObservationFailure(
			response,
			guiResult,
			"GUI runtime returned an unusable full-desktop screenshot: "+err.Error(),
		)
	}
	// Bind coordinates only to an observation that is valid and is about to be
	// delivered to the model. Rejected snapshots must never become provenance.
	s.guiSnapshot = response.Snapshot
	result := response.Snapshot.textWithNotes("", nil)
	result.Content = result.Content[1:]
	result.StructuredContent = guiMetadata(response.Snapshot, guiResult)
	result.IsError = !response.OK
	if !response.OK {
		result.Content = append([]contentItem{{Type: "text", Text: guiValidationErrorText(response)}},
			result.Content...)
		code, path, message := guiProviderFailure(response, guiResult)
		setGUIPublicError(&result, code, path, message, guiProviderFailureHint(response))
	}
	return result
}

func validateGUISnapshotImage(snapshot *appSnapshot) error {
	const (
		maxDimension = 16384
		maxPixels    = 64 * 1024 * 1024
	)
	if snapshot == nil {
		return errors.New("snapshot is missing")
	}
	declaredMIME := strings.TrimSpace(snapshot.ScreenshotMimeType)
	raw, err := base64.StdEncoding.DecodeString(snapshot.ScreenshotPNGBase64)
	if err != nil {
		return fmt.Errorf("invalid base64 payload")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("invalid image payload")
	}
	actualMIME := "image/" + format
	if declaredMIME != "" && declaredMIME != actualMIME {
		return fmt.Errorf(
			"declared image type %q does not match decoded type %q",
			declaredMIME, actualMIME,
		)
	}
	if actualMIME != "image/png" && actualMIME != "image/jpeg" {
		return fmt.Errorf("unsupported image type %q", actualMIME)
	}
	if config.Width != snapshot.Width || config.Height != snapshot.Height {
		return fmt.Errorf(
			"image dimensions %dx%d do not match declared dimensions %dx%d",
			config.Width, config.Height, snapshot.Width, snapshot.Height,
		)
	}
	if config.Width > maxDimension || config.Height > maxDimension ||
		int64(config.Width)*int64(config.Height) > maxPixels {
		return fmt.Errorf("image dimensions exceed the supported desktop limit")
	}
	// Older providers may omit MIME while still returning a valid supported
	// image. Canonicalize the detected type before this snapshot is cached or
	// rendered so JPEG bytes can never be mislabeled as PNG.
	snapshot.ScreenshotMimeType = actualMIME
	return nil
}

func mergeProviderMutationIntoGUIResult(
	guiResult *ProviderGUIResult,
	mutation *ProviderMutationFacts,
) {
	if mutation == nil {
		return
	}
	setTrue := func(field **bool, value bool) {
		if value {
			*field = &value
		}
	}
	setTrue(&guiResult.InputEventsEmitted, mutation.InputEventsEmitted)
	setTrue(&guiResult.PartialInputPossible, mutation.PartialInputPossible)
	setTrue(&guiResult.MutationUnknown, mutation.MutationUnknown)
}

func guiObservationFailure(
	response ProviderDesktopResult,
	guiResult ProviderGUIResult,
	defaultMessage string,
) toolCallResult {
	mayHaveMutated := providerMutationCanInvalidate(response.Mutation) ||
		guiInvalidatesAccessibility(guiResult)
	if mayHaveMutated {
		unknown := true
		guiResult.MutationUnknown = &unknown
	}
	visible := compactGUIResult(guiResult)
	payload := map[string]any{}
	if len(visible) > 0 {
		payload["actions"] = visible
	}
	if response.Failure != nil {
		if response.Failure.Code != "" {
			payload["failure_code"] = response.Failure.Code
		}
		if response.Failure.Phase != "" {
			payload["failure_phase"] = response.Failure.Phase
		}
	}
	message := guiValidationErrorText(response)
	if strings.TrimSpace(message) == "" {
		message = defaultMessage
	}
	if mayHaveMutated {
		if _, present := payload["failure_code"]; !present {
			payload["failure_code"] = "effect_unknown"
		}
		if _, present := payload["failure_phase"]; !present {
			payload["failure_phase"] = "post_action_observation"
		}
		payload["observation_required"] = true
	}
	result := textResult(message, true)
	result.StructuredContent = payload
	code, path, detail := guiProviderFailure(response, guiResult)
	if mayHaveMutated {
		code = axCodeEffectUnknown
	}
	if strings.TrimSpace(detail) == "" {
		detail = message
	}
	setGUIPublicError(&result, code, path, detail, guiProviderFailureHint(response))
	return result
}

// checkGUIWaitBudget enforces the cumulative wait limit that the GUI
// description states and the schema cannot express: each wait is one second
// and a batch may wait at most guiMaxTotalWait seconds.
func checkGUIWaitBudget(actions []ProviderDesktopAction) error {
	waits := 0
	for _, action := range actions {
		if action.Type == "wait" {
			waits++
		}
	}
	if waits > guiMaxTotalWait {
		return newArgumentRuleError(argumentRuleWaitBudget, fmt.Sprintf(
			"GUI batch waits %ds; the maximum is %ds per batch. No action was emitted; "+
				"split the batch and inspect the screenshot between waits",
			waits, guiMaxTotalWait))
	}
	return nil
}
