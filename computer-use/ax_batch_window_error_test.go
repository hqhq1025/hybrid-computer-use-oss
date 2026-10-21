package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAXCrossWindowPreflightReportsArgumentError(t *testing.T) {
	for _, kind := range []string{"element refs", "window ref action", "explicit window"} {
		t.Run(kind, func(t *testing.T) {
			s, app, _, first := sheetBatchService(t)
			handle := "w2:fedcba9876543210fedcba9876543210"
			secondWindow := s.windowRefRegistry.Acquire("libreoffice", handle, `dialog "Other"`, "c1",
				":1.84", "/window/2", "pid:10:start:100", "dialog", "Other", app)
			second := s.refRegistry.Acquire("libreoffice", handle, `button "OK"`, CanonicalRole("button"), "OK", "c1",
				":1.84", "/window/2/ok", "pid:10:start:100", ":1.84", "/window/2", "pid:10:start:100", "dialog", "Other", app, secondWindow)
			backend := &recordingPlatformBackend{}
			s.backend = backend
			actions := []any{
				map[string]any{"type": "click", "ref": first},
				map[string]any{"type": "click", "ref": second},
			}
			args := map[string]any{"mode": "act", "actions": actions}
			wantPath, wantHint := "actions[1].ref", "separate"
			if kind == "window ref action" {
				actions[1] = map[string]any{"type": "activate", "ref": secondWindow}
			}
			if kind == "explicit window" {
				args["window_ref"] = secondWindow
				args["actions"] = actions[:1]
				wantPath, wantHint = "window_ref", "Omit window_ref"
			}
			result := s.callForModel(publicToolAX, args, false)
			var evidence map[string]any
			if err := json.Unmarshal([]byte(result.Content[0].Text), &evidence); err != nil {
				t.Fatal(err)
			}
			if !result.IsError || evidence["code"] != axCodeInvalidArgument || evidence["path"] != wantPath {
				t.Fatalf("window conflict misclassified: %#v", evidence)
			}
			hint, _ := evidence["hint"].(string)
			if !strings.Contains(hint, wantHint) || !strings.Contains(hint, "window") {
				t.Fatalf("hint does not resolve the window conflict: %q", hint)
			}
			batch, _ := evidence["batch"].(map[string]any)
			if evidence["input_emitted"] != false || evidence["completed"] != float64(0) ||
				evidence["requested"] != float64(len(args["actions"].([]any))) || batch["rejected_before_first_action"] != true {
				t.Fatalf("window conflict lost no-input evidence: %#v", evidence)
			}
			if len(backend.calls) != 0 || len(backend.actionRequests) != 0 {
				t.Fatalf("preflight dispatched a provider call: %#v", backend.calls)
			}
		})
	}
}

func TestAXWindowPreflightUnknownRefKeepsStaleClassification(t *testing.T) {
	s, _, _, first := sheetBatchService(t)
	backend := &recordingPlatformBackend{}
	s.backend = backend
	result := s.callForModel(publicToolAX, map[string]any{
		"mode": "act", "actions": []any{
			map[string]any{"type": "click", "ref": first},
			map[string]any{"type": "click", "ref": "r99999"},
		},
	}, false)
	var evidence map[string]any
	if err := json.Unmarshal([]byte(result.Content[0].Text), &evidence); err != nil {
		t.Fatal(err)
	}
	if !result.IsError || evidence["code"] != axCodeStaleRef ||
		evidence["input_emitted"] != false || evidence["completed"] != float64(0) || len(backend.calls) != 0 {
		t.Fatalf("unknown ref lost its no-input stale classification: %#v", evidence)
	}
}
