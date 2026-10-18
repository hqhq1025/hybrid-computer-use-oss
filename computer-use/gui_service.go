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
	"strings"
)

func (s *service) gui(actions any, settleMS int) toolCallResult {
	providerActions, err := decodeProviderDesktopActions(actions)
	if err != nil {
		return textResult(err.Error(), true).withEvidenceScope("error_only")
	}
	if err := checkGUIWaitBudget(providerActions); err != nil {
		return withArgumentRule(
			textResult(err.Error(), true).withEvidenceScope("error_only"), err)
	}
	if err := s.preflightPlatformGUIKeys(providerActions); err != nil {
		return textResult(err.Error(), true).withEvidenceScope("error_only")
	}
	if s.platformBackend().HostInfo().Platform == "windows" {
		if err := preflightWindowsGUIKeys(providerActions); err != nil {
			return textResult(err.Error(), true).withEvidenceScope("error_only")
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
				return textResult(err.Error(), true)
			}
			// The internal screenshot is not one of the caller's actions.
			observed.GUIResult.Dispatched = nil
			result := s.guiResponse(*observed)
			result.IsError = true
			if len(result.Content) > 0 {
				result.Content[0].Text =
					"GUI coordinate/window actions require a prior GUI screenshot. " +
						"No actions were emitted; use the attached fresh screenshot.\n" +
						result.Content[0].Text
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
			message := err.Error() + fmt.Sprintf(
				"\n%s=true mutation_unknown=%t. ",
				failureCode, mutationUnknown)
			if mutationUnknown {
				message += "The GUI runtime may have emitted input or a window command " +
					"before the failure; inspect fresh desktop state before retrying."
			} else {
				message += "No desktop mutation is inferred from this failure."
			}
			evidence := map[string]any{
				"failure_code":         failureCode,
				"failure_phase":        phase,
				"mutation_unknown":     mutationUnknown,
				"observation_required": mutationUnknown,
			}
			if s.guiSnapshot != nil {
				result := s.guiSnapshot.textWithNotes(
					guiMetadata(s.guiSnapshot, guiResult)+"\n"+message, nil)
				result.IsError = true
				result.StructuredContent = evidence
				return result
			}
			payload, _ := json.Marshal(map[string]any{
				"gui_result": emptyGUIResult(mutationUnknown),
			})
			result := textResult(string(payload)+"\n"+message, true)
			result.StructuredContent = evidence
			return result
		}
		return textResult(err.Error(), true)
	}
	return s.guiResponse(*response)
}

// emptyGUIResult is the full GUI evidence schema for a batch the runtime never
// reported on; only mutation_unknown can be true.
func emptyGUIResult(mutationUnknown bool) map[string]any {
	return map[string]any{
		"dispatched":               0,
		"input_events_emitted":     false,
		"input_actions_dispatched": 0,
		"mutation_unknown":         mutationUnknown,
		"partial_input_possible":   false,
		"effect_unknown":           false,
		"quiescent":                nil,
		"changed_since_before":     nil,
		"visual_change_percent":    nil,
		"visual_change_box":        nil,
		"visual_change_method":     nil,
		"visual_change_observed":   nil,
	}
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

func guiMetadata(snapshot *appSnapshot, guiResult ProviderGUIResult) string {
	if snapshot == nil {
		return ""
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
	metadata, err := json.Marshal(model)
	if err != nil {
		return ""
	}
	return string(metadata)
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
	s.exposedHiddenRefs = map[string]map[int]elementRecord{}
	s.findCursorRegistry.Reset()
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
	metadata := guiMetadata(response.Snapshot, guiResult)
	if metadata == "" {
		return textResult("GUI runtime returned invalid coordinate metadata.", true)
	}
	// Bind coordinates only to an observation that is valid and is about to be
	// delivered to the model. Rejected snapshots must never become provenance.
	s.guiSnapshot = response.Snapshot
	result := response.Snapshot.textWithNotes(metadata, nil)
	result.IsError = !response.OK
	if !response.OK {
		result.Content[0].Text =
			guiValidationErrorText(response) + "\n\n" + result.Content[0].Text
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
	if len(payload) > 0 {
		if encoded, err := json.Marshal(payload); err == nil {
			message = string(encoded) + "\n" + message
		}
	}
	result := textResult(message, true)
	if len(payload) > 0 {
		result.StructuredContent = payload
	}
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
