package main

import (
	"strings"
	"testing"
)

func TestResultWithNotesPrependsNotesBeforeTree(t *testing.T) {
	snapshot := &appSnapshot{
		App:       appDescriptor{Name: "gedit", PID: 7},
		TreeLines: []string{"0 frame Doc"},
	}
	result := snapshot.resultWithNotes([]string{"first note", "second note"})
	text := result.Content[0].Text
	if !strings.HasPrefix(text, "Note: first note\nNote: second note\n") {
		t.Fatalf("notes should lead the result text, got %q", text)
	}
	if !strings.Contains(text, "0 frame Doc") {
		t.Fatal("the accessibility tree must still be present")
	}
	if snapshot.resultWithNotes(nil).Content[0].Text != snapshot.renderedText() {
		t.Fatal("an action without notes should render exactly as before")
	}
}

func TestSnapshotModelTextOmitsInternalTimeAndIdentity(t *testing.T) {
	snapshot := &appSnapshot{
		App:          appDescriptor{Name: "gedit", PID: 7},
		WindowTitle:  "notes.txt",
		WindowHandle: "pid7/win0/frame",
		SnapshotID:   "abc123",
		SampledAt:    "2000-01-01T00:00:00.000Z",
		TreeLines:    []string{"0 frame Doc"},
	}
	text := snapshot.renderedText()
	for _, fragment := range []string{
		"pid 7",
		"pid7/win0/frame",
		"id=abc123",
		"sampled_at=2000-01-01T00:00:00.000Z",
		"identical_to_previous=",
		"comparison_baseline=model_visible",
	} {
		if strings.Contains(text, fragment) {
			t.Fatalf("snapshot metadata leaked %q in %q", fragment, text)
		}
	}
	if !strings.HasPrefix(text, `window: "notes.txt"`) {
		t.Fatalf("compact window header missing: %q", text)
	}
}

func TestContinuityUsesLastCompleteDeliveredSnapshotInsteadOfHiddenCache(t *testing.T) {
	service := newService()
	visible := &appSnapshot{
		App:             appDescriptor{Name: "Editor", PID: 7},
		WindowHandle:    "pid7/win0/frame",
		publicWindowRef: "w1",
		SnapshotID:      "visible-a",
		sourceApp:       "editor",
		projection:      snapshotProjection{View: treeViewWorking},
	}
	service.rememberDeliveredSnapshot(visible)

	// A hidden observe:none/find/recovery sample may advance the action cache,
	// but it was never shown to the model and must not become the continuity
	// baseline.
	hidden := &appSnapshot{
		App:             appDescriptor{Name: "Editor", PID: 7},
		WindowHandle:    "pid7/win0/frame",
		publicWindowRef: "w1",
		SnapshotID:      "hidden-b",
		projection:      snapshotProjection{View: treeViewWorking},
	}
	service.rememberSnapshot("editor", hidden)

	current := &appSnapshot{
		App:             appDescriptor{Name: "Editor", PID: 7},
		WindowHandle:    "pid7/win0/frame",
		publicWindowRef: "w1",
		SnapshotID:      "hidden-b",
		projection:      snapshotProjection{View: treeViewWorking},
	}
	baseline := service.deliveredSnapshotFor(current)
	if baseline != visible {
		t.Fatalf("expected the delivered snapshot baseline, got %#v", baseline)
	}
	if notes := continuityNotes(baseline, current); len(notes) != 0 {
		t.Fatalf("current differs from the model-visible baseline: %v", notes)
	}
	if !current.comparedToPrevious || current.identicalToPrevious {
		t.Fatal("a hidden cache match must not be reported as model-visible equality")
	}
	if text := current.renderedText(); strings.Contains(
		text, "identical_to_previous",
	) {
		t.Fatalf("internal continuity metadata leaked into model text: %q", text)
	}
}

func TestScreenshotContentPreservesAdaptiveMimeType(t *testing.T) {
	snapshot := &appSnapshot{
		ScreenshotPNGBase64: "jpeg-data",
		ScreenshotMimeType:  "image/jpeg",
	}
	result := snapshot.screenshotOnlyResult(nil)
	if len(result.Content) != 2 || result.Content[1].Type != "image" ||
		result.Content[1].MimeType != "image/jpeg" {
		t.Fatalf("adaptive screenshot MIME was lost: %#v", result.Content)
	}
}

func TestMergePostconditionFinalObservationRemovesDeferredPlaceholder(t *testing.T) {
	action := (&appSnapshot{}).notesOnlyResult([]string{
		"action delivered",
		"until: PASS",
	})
	action.StructuredContent = map[string]any{
		"postcondition_verified": true,
	}
	observation := toolCallResult{
		Content: []contentItem{{Type: "text", Text: "final diff"}},
		StructuredContent: map[string]any{
			"final_observation_returned": true,
		},
	}
	merged := mergeA11yBatchFinalObservation(action, observation)
	text := resultText(merged)
	if strings.Contains(text, "observe: none") ||
		strings.Contains(text, `(observe="none":`) {
		t.Fatalf("deferred observation placeholder leaked: %q", text)
	}
	for _, expected := range []string{
		"action delivered",
		"until: PASS",
		"final diff",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("merged response lost %q: %q", expected, text)
		}
	}
	if merged.StructuredContent["postcondition_verified"] != true ||
		merged.StructuredContent["final_observation_returned"] != true {
		t.Fatalf("merged evidence is incomplete: %#v", merged.StructuredContent)
	}
}

func TestRenderedWindowRefsHideInternalIdentity(t *testing.T) {
	service := newService()
	handle := "w2:" + strings.Repeat("a", 32)
	snapshot := &appSnapshot{
		App:          appDescriptor{Name: "Google Chrome", PID: 145},
		WindowTitle:  "https://example.test/private?q=secret",
		WindowHandle: handle,
		SnapshotID:   "internal-revision",
		Elements: []elementRecord{{
			Index:       0,
			RuntimeID:   []int{0},
			ControlType: "frame",
			Name:        "https://example.test/private?q=secret",
		}},
		Windows: []topLevelWindow{{
			Title:  "https://example.test/private?q=secret",
			Role:   "frame",
			Handle: handle,
		}},
	}
	service.assignWindowRefs(snapshot)
	text := snapshot.renderedText()
	if !strings.Contains(text, "[ref=w1]") {
		t.Fatalf("short window ref missing: %q", text)
	}
	for _, forbidden := range []string{"w1:", "w2:", "s1:", "s2:"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("model-visible window text leaked %q: %q", forbidden, text)
		}
	}
}

func TestWindowWithoutActionRefDoesNotInviteDuplicateIdentity(t *testing.T) {
	text := (&appSnapshot{
		App:          appDescriptor{Name: "LibreOffice"},
		WindowTitle:  "Insert Table",
		WindowHandle: "private-window-handle",
	}).renderedText()
	for _, want := range []string{
		"window-action-ref=unavailable",
		"rN actions need no window_ref",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing ref-owned guidance %q in %q", want, text)
		}
	}
	if strings.Contains(text, "window-ref-unavailable=true") {
		t.Fatalf("ambiguous legacy window hint leaked: %q", text)
	}
}
