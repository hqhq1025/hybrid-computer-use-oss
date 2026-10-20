package main

import (
	"fmt"
	"strings"
	"testing"
)

type changingFinalObservationBackend struct {
	recordingPlatformBackend
	readNumber int
}

func (b *changingFinalObservationBackend) Observe(request ProviderObserveRequest) (ProviderAccessibilityResult, error) {
	b.observeRequests = append(b.observeRequests, request)
	b.readNumber++
	snapshot := mixedOrderedInputBatchTestSnapshot()
	snapshot.Elements[4].Name = fmt.Sprintf("Read %d", b.readNumber)
	snapshot.TreeLines[4] = fmt.Sprintf("\t\t4 status bar \"Read %d\"", b.readNumber)
	if request.Observation.IncludeScreenshot != nil && *request.Observation.IncludeScreenshot {
		snapshot.ScreenshotPNGBase64 = fmt.Sprintf("image-%d", b.readNumber)
		snapshot.ScreenshotMimeType = "image/png"
	}
	return ProviderAccessibilityResult{OK: true, Snapshot: snapshot}, nil
}

func TestTrailingWaitFullUsesOneFinalReadForTreeAndScreenshot(t *testing.T) {
	s, _, window, _ := orderedInputTestService(t)
	backend := &changingFinalObservationBackend{recordingPlatformBackend: recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{OK: true, Snapshot: mixedOrderedInputBatchTestSnapshot(),
			Mutation: &ProviderMutationFacts{InputEventsEmitted: true}},
	}}
	s.backend = backend
	result := s.axBatch([]any{
		map[string]any{"type": "keypress", "keys": []any{"ENTER"}},
		map[string]any{"type": "wait", "query": map[string]any{"role": "button", "name_contains": "Sav"}, "timeout_ms": 1.0},
	}, observation{mode: observeFull}, window)
	if result.IsError || backend.readNumber < 2 {
		t.Fatalf("wait did not complete before final read: reads=%d result=%s", backend.readNumber, resultText(result))
	}
	if !strings.Contains(resultText(result), fmt.Sprintf(`status "Read %d"`, backend.readNumber)) {
		t.Fatalf("tree did not use final read: %s", resultText(result))
	}
	images := 0
	for _, item := range result.Content {
		if item.Type == "image" {
			images++
			if item.Data != fmt.Sprintf("image-%d", backend.readNumber) {
				t.Fatalf("tree and image are from different reads: %q", item.Data)
			}
		}
	}
	if images != 1 {
		t.Fatalf("full returned %d images, want only the final read", images)
	}
}

func TestExplicitFinalObservationAcrossBatchPaths(t *testing.T) {
	for _, path := range []string{"mutation", "wait", "ordered", "mixed_ordered"} {
		for _, mode := range []string{observeTree, observeFull, observeScreenshot, observeDiff} {
			t.Run(path+"/"+mode, func(t *testing.T) {
				s, _, window, ref := orderedInputTestService(t)
				snapshot := mixedOrderedInputBatchTestSnapshot()
				snapshot.ScreenshotPNGBase64 = "final-image"
				snapshot.ScreenshotMimeType = "image/png"
				legacy := &recordingPlatformBackend{accessibilityResponse: &ProviderAccessibilityResult{
					OK: true, Snapshot: snapshot, Mutation: &ProviderMutationFacts{InputEventsEmitted: true},
				}}
				s.backend = legacy
				actions := []any{map[string]any{"type": "keypress", "keys": []any{"ENTER"}}}
				var ordered *orderedInputRecordingBackend
				if path == "wait" {
					actions = append(actions, map[string]any{"type": "wait", "query": map[string]any{"role": "button", "name_contains": "Sav"}, "timeout_ms": 1.0})
				}
				if path == "ordered" || path == "mixed_ordered" {
					ordered = &orderedInputRecordingBackend{recordingPlatformBackend: legacy,
						execute: func(request ProviderOrderedInputRequest) ProviderOrderedInputResult {
							result := testProviderOrderedInputSuccess(request)
							result.Snapshot = mixedOrderedInputBatchTestSnapshot()
							if request.FinalObservation.IncludeScreenshot != nil && *request.FinalObservation.IncludeScreenshot {
								result.Snapshot.ScreenshotPNGBase64 = "final-image"
								result.Snapshot.ScreenshotMimeType = "image/png"
							}
							return result
						},
					}
					s.backend = ordered
					if path == "mixed_ordered" {
						actions = append([]any{map[string]any{"type": "click", "ref": ref}}, actions...)
					}
				}
				// The second request has a delivered baseline. An explicit tree/full
				// must still deliver controls outside the focused subtree.
				repeats := 2
				if path == "mixed_ordered" {
					repeats = 1 // The fixture replaces the initial semantic ref after click.
				}
				for repeat := 0; repeat < repeats; repeat++ {
					result := s.axBatch(actions, observation{mode: mode}, window)
					if result.IsError {
						t.Fatalf("batch failed: %s", resultText(result))
					}
					text := resultText(result)
					if mode == observeTree || mode == observeFull {
						if !strings.Contains(text, `status "Ready"`) || result.StructuredContent["full_tree_returned"] != true {
							t.Fatalf("explicit %s omitted nonfocused controls on request %d: %s", mode, repeat+1, text)
						}
					}
					image := false
					for _, item := range result.Content {
						image = image || (item.Type == "image" && item.Data == "final-image")
					}
					if (mode == observeFull || mode == observeScreenshot) && !image {
						t.Fatalf("%s omitted final screenshot: %s", mode, text)
					}
					if mode == observeTree && image {
						t.Fatal("tree unexpectedly included screenshot")
					}
					if mode == observeScreenshot && strings.Contains(text, `status "Ready"`) {
						t.Fatal("screenshot unexpectedly included tree")
					}
					if mode == observeDiff && result.StructuredContent["full_tree_returned"] == true {
						t.Fatalf("default diff expanded to full tree: %s", text)
					}
					if path == "wait" && mode == observeFull {
						last := legacy.observeRequests[len(legacy.observeRequests)-1]
						if last.Observation.IncludeScreenshot == nil || !*last.Observation.IncludeScreenshot {
							t.Fatal("post-wait final read did not request its screenshot")
						}
					}
				}
			})
		}
	}
}

func TestExplicitFullRecoveryPreservesPartialBatch(t *testing.T) {
	s, app, window, _ := orderedInputTestService(t)
	snapshot := mixedOrderedInputBatchTestSnapshot()
	snapshot.ScreenshotPNGBase64 = "recovery-image"
	snapshot.ScreenshotMimeType = "image/png"
	backend := &recordingPlatformBackend{accessibilityResponse: &ProviderAccessibilityResult{OK: true, Snapshot: snapshot}}
	s.backend = backend
	batch := map[string]any{"actions_requested": 3, "actions_completed_without_call_error": 1, "first_failed_action_index": 1}
	failed := toolCallResult{IsError: true, StructuredContent: map[string]any{
		"failure_code": "effect_unknown", "failure_phase": "action",
		"partial_input_possible": true, "input_events_emitted": true,
		"a11y_batch_result": batch,
	}}
	result := s.recoverAXBatchObservation(&axBatchContext{
		appRef: app, appToken: app, windowRef: window, windowHandle: snapshot.WindowHandle,
	}, failed, observation{mode: observeFull})
	if !result.IsError || result.StructuredContent["failure_code"] != "effect_unknown" ||
		result.StructuredContent["partial_input_possible"] != true ||
		result.StructuredContent["recovery_observation_returned"] != true {
		t.Fatalf("recovery corrupted failed action evidence: %#v", result)
	}
	got := result.StructuredContent["a11y_batch_result"].(map[string]any)
	if got["actions_completed_without_call_error"] != 1 || got["first_failed_action_index"] != 1 {
		t.Fatalf("recovery lost completed prefix: %#v", got)
	}
	if len(backend.observeRequests) != 1 || backend.observeRequests[0].Observation.IncludeScreenshot == nil ||
		!*backend.observeRequests[0].Observation.IncludeScreenshot || !strings.Contains(resultText(result), `status "Ready"`) {
		t.Fatalf("recovery did not request and return full final state: %#v", result)
	}
	foundImage := false
	for _, item := range result.Content {
		foundImage = foundImage || item.Type == "image" && item.Data == "recovery-image"
	}
	if !foundImage {
		t.Fatal("recovery omitted final screenshot")
	}
}

func TestExplicitBatchObservationDeliversReissuedReferences(t *testing.T) {
	for _, mode := range []string{observeTree, observeFull} {
		s := newService()
		for _, ref := range []string{"r206", "r207", "r207"} {
			snapshot := deliveredDiffBaselineSnapshot("document", ` - window "Editor":`,
				fmt.Sprintf(`  - button "Save" [ref=%s]`, ref))
			snapshot.comparedToPrevious = true
			snapshot.identicalToPrevious = true
			result := s.renderAXBatchFinalObservation(snapshot, nil, observation{mode: mode})
			if !strings.Contains(resultText(result), "[ref="+ref+"]") || result.StructuredContent["tree_reference_only"] == true {
				t.Fatalf("explicit %s hid current ref %s: %s", mode, ref, resultText(result))
			}
		}
	}
}
