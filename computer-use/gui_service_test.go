package main

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/jpeg"
	"reflect"
	"strings"
	"testing"
)

func testJPEGBase64(t *testing.T, width, height int) string {
	t.Helper()
	var encoded bytes.Buffer
	if err := jpeg.Encode(
		&encoded,
		image.NewRGBA(image.Rect(0, 0, width, height)),
		&jpeg.Options{Quality: 80},
	); err != nil {
		t.Fatalf("encode test JPEG: %v", err)
	}
	return base64.StdEncoding.EncodeToString(encoded.Bytes())
}

func TestGUIModelResponseKeepsLayoutProvenancePrivate(t *testing.T) {
	snapshot := &appSnapshot{
		ScreenshotPNGBase64: "png",
		CoordinateSpace:     "screen_image_px",
		FrameID:             "current-frame",
		ContentFrameID:      "content-frame",
		DesktopLayout: map[string]any{
			"activeWindow": "0x20",
			"windows":      []any{map[string]any{"id": "0x20"}},
		},
		Width:  1920,
		Height: 1080,
	}
	text := guiMetadata(snapshot, map[string]any{
		"dispatched":               1,
		"input_events_emitted":     true,
		"input_actions_dispatched": 1,
		"effect_unknown":           true,
		"quiescent":                true,
		"visual_change_observed":   false,
	})
	for _, fragment := range []string{
		`"coordinate_space":"screen_image_px"`,
		`"width":1920`,
		`"height":1080`,
		`"screenshot_returned":true`,
		`"dispatched":1`,
		`"input_actions_dispatched":1`,
		`"input_events_emitted":true`,
		`"effect_unknown":true`,
		`"quiescent":true`,
		`"visual_change_observed":false`,
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("compact GUI response missing %q: %s", fragment, text)
		}
	}
	for _, forbidden := range []string{
		"frame_id", "content_frame_id", "desktop_layout", "0x20",
		"screenshot_encoding", "screenshot_mime_type",
		"input_actions_emitted", "visual_change_percent",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("model response leaked private GUI provenance %q: %s", forbidden, text)
		}
	}
}

func TestCompactGUIResultPreservesUnknownEffect(t *testing.T) {
	compact := compactGUIResult(map[string]any{
		"dispatched":           1,
		"input_events_emitted": true,
		"effect_unknown":       true,
	})
	if compact["effect_unknown"] != true {
		t.Fatalf("GUI compact evidence hid effect_unknown: %#v", compact)
	}
}

func TestGUIInputEmissionInvalidatesAccessibilitySnapshots(t *testing.T) {
	response := func(emitted bool) ProviderDesktopResult {
		return ProviderDesktopResult{
			OK: true,
			Snapshot: &appSnapshot{
				ScreenshotPNGBase64: testPNGBase64(t, 100, 80),
				CoordinateSpace:     "screen_image_px",
				FrameID:             "frame",
				Width:               100,
				Height:              80,
			},
			GUIResult: ProviderGUIResult{InputEventsEmitted: &emitted},
		}
	}

	service := newService()
	service.rememberSnapshot("Editor", expectSnapshot())
	service.guiResponse(response(false))
	if service.currentSnapshot("Editor") == nil {
		t.Fatal("pure GUI observation or pre-emission rejection must preserve accessibility snapshots")
	}

	service.guiResponse(response(true))
	if len(service.snapshots) != 0 || service.currentSnapshot("Editor") != nil {
		t.Fatal("input_events_emitted=true must invalidate every accessibility snapshot and expect baseline")
	}

	service.rememberSnapshot("Editor", expectSnapshot())
	partialFailure := response(false)
	partialFailure.OK = false
	partialFailure.Error = "input failed after partial emission"
	partialFailure.Mutation = &ProviderMutationFacts{InputEventsEmitted: true}
	service.guiResponse(partialFailure)
	if len(service.snapshots) != 0 {
		t.Fatal("top-level input_events_emitted=true on a partial GUI failure must also invalidate snapshots")
	}

	service.rememberSnapshot("Editor", expectSnapshot())
	partialOnly := response(false)
	partialOnly.OK = false
	partialOnly.Mutation = &ProviderMutationFacts{PartialInputPossible: true}
	service.guiResponse(partialOnly)
	if len(service.snapshots) != 0 {
		t.Fatal("partial_input_possible=true must invalidate snapshots even when no complete input action was counted")
	}

	service.rememberSnapshot("Editor", expectSnapshot())
	windowOnly := response(false)
	windowOnly.Mutation = &ProviderMutationFacts{DesktopStateMutated: true}
	service.guiResponse(windowOnly)
	if len(service.snapshots) != 0 {
		t.Fatal("desktop_state_mutated=true must invalidate snapshots independently of input events")
	}

	service.rememberSnapshot("Editor", expectSnapshot())
	unknown := response(false)
	unknown.Mutation = &ProviderMutationFacts{MutationUnknown: true}
	unknownResult := service.guiResponse(unknown)
	if len(service.snapshots) != 0 {
		t.Fatal("mutation_unknown=true must invalidate snapshots after GUI runtime timeout")
	}
	if !strings.Contains(resultText(unknownResult), `"mutation_unknown":true`) {
		t.Fatalf("top-level GUI mutation facts were hidden: %s",
			resultText(unknownResult))
	}

	field, ok := reflect.TypeOf(ProviderMutationFacts{}).FieldByName("InputEventsEmitted")
	if !ok || field.Tag.Get("json") != "input_events_emitted,omitempty" {
		t.Fatal("ProviderMutationFacts must decode input_events_emitted from partial failures")
	}
	if field, ok := reflect.TypeOf(ProviderMutationFacts{}).FieldByName("MutationUnknown"); !ok ||
		field.Tag.Get("json") != "mutation_unknown,omitempty" {
		t.Fatal("ProviderMutationFacts must decode GUI mutation_unknown")
	}
}

func TestGUIFailureWithoutSnapshotReturnsMutationEvidence(t *testing.T) {
	service := newService()
	result := service.guiResponse(ProviderDesktopResult{
		OK:    false,
		Error: "post-action screenshot failed",
		Failure: &ProviderFailure{
			Code:  "effect_unknown",
			Phase: "post_action_observation",
		},
		Mutation: &ProviderMutationFacts{
			InputEventsEmitted: true,
			MutationUnknown:    true,
		},
	})
	if result.StructuredContent["failure_code"] != "effect_unknown" {
		t.Fatalf("GUI failure code missing: %#v", result.StructuredContent)
	}
	actions, _ := result.StructuredContent["actions"].(map[string]any)
	if actions["input_events_emitted"] != true ||
		actions["mutation_unknown"] != true {
		t.Fatalf("GUI mutation evidence missing: %#v", result.StructuredContent)
	}
}

func TestGUIResponseAcceptsValidatedJPEGSnapshot(t *testing.T) {
	service := newService()
	result := service.guiResponse(ProviderDesktopResult{
		OK: true,
		Snapshot: &appSnapshot{
			FrameID:             "jpeg-frame",
			ScreenshotPNGBase64: testJPEGBase64(t, 100, 80),
			ScreenshotMimeType:  "image/jpeg",
			CoordinateSpace:     "screen_image_px",
			Width:               100,
			Height:              80,
		},
	})
	if result.IsError {
		t.Fatalf("valid JPEG snapshot was rejected: %#v", result)
	}
	if service.guiSnapshot == nil || service.guiSnapshot.FrameID != "jpeg-frame" {
		t.Fatalf("valid JPEG snapshot was not cached: %#v", service.guiSnapshot)
	}
}

func TestGUIResponseCanonicalizesMissingJPEGMIME(t *testing.T) {
	service := newService()
	result := service.guiResponse(ProviderDesktopResult{
		OK: true,
		Snapshot: &appSnapshot{
			FrameID:             "jpeg-frame-without-mime",
			ScreenshotPNGBase64: testJPEGBase64(t, 100, 80),
			CoordinateSpace:     "screen_image_px",
			Width:               100,
			Height:              80,
		},
	})
	if result.IsError {
		t.Fatalf("valid JPEG snapshot without MIME was rejected: %#v", result)
	}
	if service.guiSnapshot == nil ||
		service.guiSnapshot.ScreenshotMimeType != "image/jpeg" {
		t.Fatalf("detected JPEG MIME was not cached: %#v", service.guiSnapshot)
	}
	if len(result.Content) < 2 || result.Content[1].MimeType != "image/jpeg" {
		t.Fatalf("detected JPEG MIME was not rendered: %#v", result.Content)
	}
}

func TestGUIResponseRejectsDeclaredMIMEThatDoesNotMatchImage(t *testing.T) {
	service := newService()
	previous := &appSnapshot{FrameID: "previous-frame"}
	service.guiSnapshot = previous
	result := service.guiResponse(ProviderDesktopResult{
		OK: true,
		Snapshot: &appSnapshot{
			FrameID:             "mismatched-mime",
			ScreenshotPNGBase64: testJPEGBase64(t, 100, 80),
			ScreenshotMimeType:  "image/png",
			CoordinateSpace:     "screen_image_px",
			Width:               100,
			Height:              80,
		},
	})
	if !result.IsError {
		t.Fatalf("mismatched image MIME was accepted: %#v", result)
	}
	if service.guiSnapshot != previous {
		t.Fatal("mismatched image MIME replaced the previous coordinate frame")
	}
}

func TestGUIFrameGuardAppliesOnlyToCoordinateActions(t *testing.T) {
	if guiBatchUsesFrameProvenance([]any{
		map[string]any{"type": "keypress", "keys": []any{"CTRL", "S"}},
		map[string]any{"type": "type", "text": "hello"},
	}) {
		t.Fatal("keyboard/text-only GUI batches must not depend on screenshot coordinates")
	}
	for _, action := range []map[string]any{
		{"type": "click", "x": 1.0, "y": 2.0},
		{"type": "drag", "path": []any{}},
		{"type": "scroll", "x": 1.0, "y": 2.0, "scroll_y": 1.0},
	} {
		if !guiBatchUsesFrameProvenance([]any{action}) {
			t.Fatalf("coordinate action must require frame guard: %#v", action)
		}
	}
	for _, action := range []map[string]any{
		{"type": "activate_window", "x": 100.0, "y": 100.0},
		{"type": "close_window", "x": 100.0, "y": 100.0},
	} {
		if guiBatchUsesFrameProvenance([]any{action}) {
			t.Fatalf("removed GUI window action must not enter frame provenance: %#v", action)
		}
	}
}

func TestGUIValidationErrorsListEveryRejectedAction(t *testing.T) {
	text := guiValidationErrorText(ProviderDesktopResult{
		Error: "gui batch failed static pre-validation",
		ValidationErrors: []map[string]any{
			{"index": float64(0), "type": "scroll", "error": "unsupported field delta_y"},
			{"index": float64(1), "type": "wait", "error": "unsupported field seconds"},
		},
	})
	for _, expected := range []string{
		`actions[0] type="scroll": unsupported field delta_y`,
		`actions[1] type="wait": unsupported field seconds`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("validation summary missing %q: %q", expected, text)
		}
	}
}

func TestApplyRuntimeGUILeaseUsesFailureEvidence(t *testing.T) {
	service := newService()
	service.guiLeaseState = "fresh"
	service.applyRuntimeGUILease(&ProviderDesktopResult{
		OK:         false,
		LeaseState: "suspect",
		Error:      "post-action observation failed",
	})
	if service.guiLeaseState != "suspect" {
		t.Fatalf("failure response lease=%q, want suspect", service.guiLeaseState)
	}
}
