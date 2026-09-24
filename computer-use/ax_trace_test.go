package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSettledActionNotesAreBoundToTheirSnapshotPhase(t *testing.T) {
	immediate := &appSnapshot{SnapshotID: "immediate-a"}
	final := &appSnapshot{SnapshotID: "settled-b"}
	immediateNotes := []string{
		"SNAPSHOT UNCHANGED: immediate tree stayed the same.",
		"[pixels] sampled_pixel_change=material.",
	}
	outcome := settleOutcome{
		snapshot:             final,
		snapshotNotes:        []string{"ROOT WINDOW CHANGED: final alert."},
		snapshotFromFollowup: true,
		verified:             true,
		elapsedMS:            548,
		polls:                1,
	}
	notes := settledActionNotes(immediate, immediateNotes, outcome)
	joined := strings.Join(notes, "\n")
	for _, fragment := range []string{
		"[observation phase=immediate superseded=true] SNAPSHOT UNCHANGED",
		"[observation phase=immediate superseded=true] [pixels]",
		"[observation phase=settled] ROOT WINDOW CHANGED",
		"final_pixel_verdict=unavailable",
	} {
		if !strings.Contains(joined, fragment) {
			t.Fatalf("phase-bound notes missing %q:\n%s", fragment, joined)
		}
	}
	for _, private := range []string{"immediate-a", "settled-b", "snapshot_id"} {
		if strings.Contains(joined, private) {
			t.Fatalf("phase-bound notes leaked private identity %q:\n%s", private, joined)
		}
	}
	for _, note := range notes {
		if strings.HasPrefix(note, "SNAPSHOT UNCHANGED") ||
			strings.HasPrefix(note, "[pixels]") {
			t.Fatalf("immediate evidence leaked without a phase binding: %q", note)
		}
	}

	result := withActionOutcome(
		textResult("settled", false),
		settlePlan{expect: &expectSpec{}},
		outcome,
		immediate,
		immediateNotes,
	)
	for key, want := range map[string]any{
		"followup_snapshot_used":  true,
		"response_snapshot_phase": "settled",
		"immediate_snapshot_id":   "immediate-a",
		"final_snapshot_id":       "settled-b",
		"pixel_evidence_phase":    "immediate",
		"final_pixel_verdict":     "unavailable",
	} {
		if got := result.StructuredContent[key]; got != want {
			t.Fatalf("%s=%#v, want %#v; all=%#v", key, got, want,
				result.StructuredContent)
		}
	}
}

func TestNoFollowupKeepsSingleSnapshotNotesUnchanged(t *testing.T) {
	notes := []string{"[pixels] sampled_pixel_change=none."}
	outcome := settleOutcome{
		snapshot:             &appSnapshot{SnapshotID: "immediate-a"},
		snapshotFromFollowup: false,
	}
	got := settledActionNotes(outcome.snapshot, notes, outcome)
	if !reflect.DeepEqual(got, notes) {
		t.Fatalf("single-snapshot response changed shape: got %v want %v", got, notes)
	}
}

func TestTraceIsOffUnlessGivenAPath(t *testing.T) {
	// Tracing is enabled by giving a path, not a boolean: where to write is the
	// caller's decision, and a diagnostic facility should not pick a directory.
	t.Setenv("COMPUTER_USE_TRACE_FILE", "")
	if tracePath() != "" {
		t.Fatal("tracing must stay off without a path")
	}
	service := newService()
	// While tracing is off, appending must have no side effects.
	service.appendTrace("gedit", axServiceRequest{Action: axClickCommand{}}, nil, nil, nil, false)
}

func TestTraceRecordsOneLinePerActionIncludingFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "trace.jsonl")
	t.Setenv("COMPUTER_USE_TRACE_FILE", path)
	service := newService()

	before := &appSnapshot{WindowTitle: "VLC media player", Elements: make([]elementRecord, 92)}
	after := &appSnapshot{WindowTitle: "Simple Preferences", Elements: make([]elementRecord, 61)}
	service.appendTrace("vlc", axServiceRequest{
		Action:  axClickCommand{},
		Element: &elementRecord{Index: 7, ControlType: "menu item", Name: "Preferences"},
	}, before, after, []string{"[a11y] Resolved element_index 7 to menu item 'Preferences'."}, false)
	// Failures get a line too; recording only successes would show a smooth run
	// in which the step under investigation is missing.
	service.appendTrace("vlc", axServiceRequest{
		Action: axSetValueCommand{Value: "x"},
	}, before, nil, nil, true)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("trace file was not written: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("two actions should produce two lines, got %d", len(lines))
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("first line is not valid JSON: %v", err)
	}
	beforeMap, _ := first["before"].(map[string]any)
	afterMap, _ := first["after"].(map[string]any)
	if beforeMap["window"] != "VLC media player" || afterMap["window"] != "Simple Preferences" {
		t.Fatalf("before/after state must show where the action took the UI, got %v -> %v", beforeMap, afterMap)
	}
	if beforeMap["elements"].(float64) != 92 || afterMap["elements"].(float64) != 61 {
		t.Fatal("trace must record element counts, the cheapest signal that the tree was rebuilt")
	}
	args, _ := first["args"].(map[string]any)
	if args["element_name"] != "Preferences" {
		t.Fatalf("action args must record the target element, got %v", args)
	}
	// The full record must not be copied, though: with whole trees the trace
	// would outgrow the transcript. Error context provides detail when needed.
	if strings.Contains(lines[0], "runtimeId") || strings.Contains(lines[0], "treeLines") {
		t.Fatal("trace must record a summary, not the whole tree")
	}

	var second map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("second line is not valid JSON: %v", err)
	}
	if second["failed"] != true {
		t.Fatal("failed actions must be marked")
	}
}
