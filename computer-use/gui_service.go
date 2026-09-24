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
	privateFrameID := ""
	if guiBatchUsesFrameProvenance(actions) {
		if s.guiSnapshot == nil || s.guiSnapshot.FrameID == "" {
			observed, err := s.callProviderDesktop(ProviderDesktopRequest{
				ProtocolVersion: providerProtocolV2,
				Actions:         []ProviderDesktopAction{},
			})
			if err != nil {
				return textResult(err.Error(), true)
			}
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
	providerActions, err := decodeProviderDesktopActions(actions)
	if err != nil {
		return textResult(err.Error(), true)
	}
	response, err := s.callProviderDesktop(ProviderDesktopRequest{
		ProtocolVersion: providerProtocolV2,
		Actions:         providerActions,
		SettleMS:        settleMS,
		FrameID:         privateFrameID,
		LeaseState:      s.guiLeaseState,
	})
	if err != nil {
		var timeout *runtimeTimeoutError
		var transport *runtimeTransportError
		mayMutate := guiBatchCanMutateDesktop(actions)
		requestMayHaveReachedProvider := errors.As(err, &timeout) ||
			(errors.As(err, &transport) && transport.requestSent)
		mutationUnknown := mayMutate && requestMayHaveReachedProvider
		if mutationUnknown {
			s.invalidateAccessibilitySnapshots()
		}
		if timeout != nil || transport != nil {
			guiResult := emptyGUIResult()
			guiResult["mutation_unknown"] = mutationUnknown
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
			payload, _ := json.Marshal(map[string]any{"gui_result": guiResult})
			result := textResult(string(payload)+"\n"+message, true)
			result.StructuredContent = evidence
			return result
		}
		return textResult(err.Error(), true)
	}
	return s.guiResponse(*response)
}

func emptyGUIResult() map[string]any {
	return map[string]any{
		"dispatched":               0,
		"input_events_emitted":     false,
		"input_actions_dispatched": 0,
		"mutation_unknown":         false,
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

func guiBatchUsesFrameProvenance(actions any) bool {
	items, ok := actions.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		action, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(optionalString(action, "type"))) {
		case "click", "double_click", "move", "drag", "scroll":
			return true
		}
	}
	return false
}

func guiBatchCanEmitInput(actions any) bool {
	items, ok := actions.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		action, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(optionalString(action, "type"))) {
		case "click", "double_click", "move", "drag", "scroll",
			"type", "keypress":
			return true
		}
	}
	return false
}

func guiBatchCanMutateDesktop(actions any) bool {
	return guiBatchCanEmitInput(actions)
}

func guiMetadata(snapshot *appSnapshot, guiResult map[string]any) string {
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

func compactGUIResult(full map[string]any) map[string]any {
	out := map[string]any{}
	if count, ok := intEvidence(full["dispatched"]); ok && count > 0 {
		out["dispatched"] = count
	}
	if count, ok := intEvidence(full["input_actions_dispatched"]); ok &&
		count > 0 {
		out["input_actions_dispatched"] = count
	}
	for _, key := range []string{
		"input_events_emitted",
		"partial_input_possible",
		"mutation_unknown",
		"effect_unknown",
	} {
		if value, ok := full[key].(bool); ok && value {
			out[key] = true
		}
	}
	for _, key := range []string{
		"quiescent",
		"visual_change_observed",
	} {
		if value, ok := full[key].(bool); ok {
			out[key] = value
		}
	}
	return out
}

func guiInvalidatesAccessibility(guiResult map[string]any) bool {
	emitted, _ := guiResult["input_events_emitted"].(bool)
	partial, _ := guiResult["partial_input_possible"].(bool)
	unknown, _ := guiResult["mutation_unknown"].(bool)
	return emitted || partial || unknown
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

func (s *service) applyRuntimeGUILease(response *ProviderDesktopResult) {
	if response == nil {
		return
	}
	if response.LeaseState != "" {
		s.guiLeaseState = response.LeaseState
		return
	}
	if response.GUIResult.LeaseState != "" {
		s.guiLeaseState = response.GUIResult.LeaseState
	}
}

func (s *service) guiResponse(response ProviderDesktopResult) toolCallResult {
	s.applyRuntimeGUILease(&response)
	guiResult := response.GUIResult.modelMap()
	if guiResult == nil {
		guiResult = map[string]any{}
	}
	mergeProviderMutationIntoGUIResult(guiResult, response.Mutation)
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
	if _, format, err := image.Decode(bytes.NewReader(raw)); err != nil {
		return fmt.Errorf("invalid image payload")
	} else if "image/"+format != actualMIME {
		return fmt.Errorf("image type changed while decoding")
	}
	// Older providers may omit MIME while still returning a valid supported
	// image. Canonicalize the detected type before this snapshot is cached or
	// rendered so JPEG bytes can never be mislabeled as PNG.
	snapshot.ScreenshotMimeType = actualMIME
	return nil
}

func mergeProviderMutationIntoGUIResult(
	guiResult map[string]any,
	mutation *ProviderMutationFacts,
) {
	if guiResult == nil || mutation == nil {
		return
	}
	for key, value := range map[string]bool{
		"input_events_emitted":   mutation.InputEventsEmitted,
		"partial_input_possible": mutation.PartialInputPossible,
		"mutation_unknown":       mutation.MutationUnknown,
	} {
		if value {
			guiResult[key] = true
		}
	}
}

func guiObservationFailure(
	response ProviderDesktopResult,
	guiResult map[string]any,
	defaultMessage string,
) toolCallResult {
	mayHaveMutated := providerMutationCanInvalidate(response.Mutation) ||
		guiInvalidatesAccessibility(guiResult)
	if mayHaveMutated {
		guiResult["mutation_unknown"] = true
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
