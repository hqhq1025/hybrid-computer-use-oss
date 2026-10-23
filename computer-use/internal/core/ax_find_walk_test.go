package core

import (
	"strings"
	"testing"

	"github.com/hqhq1025/hybrid-computer-use-oss/computer-use/internal/contract"
)

type linuxRecordingBackend struct {
	*recordingPlatformBackend
}

func (linuxRecordingBackend) HostInfo() HostBackendInfo {
	return HostBackendInfo{Platform: "linux"}
}

func findWalkRequests(t *testing.T, platform string, mode string) []bool {
	t.Helper()
	recording := &recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{OK: true, Snapshot: &AppSnapshot{
			App:          AppDescriptor{Name: "Editor", PID: 7},
			WindowTitle:  "Editor",
			WindowHandle: "window-7",
			Elements: []ElementRecord{
				{Index: 0, RuntimeID: []int{0}, ControlType: "frame", Name: "Editor"},
				{Index: 1, RuntimeID: []int{0, 0}, ControlType: "push button", Name: "Save"},
			},
		}},
	}
	var backend PlatformBackend = recording
	if platform == "linux" {
		backend = linuxRecordingBackend{recording}
	}
	s := NewServiceWithBackend(backend)
	args := map[string]any{"mode": mode, "app": "Editor"}
	if mode == contract.AXModeFind {
		args["query"] = map[string]any{"name_contains": "Save"}
	}
	if result := s.CallForModel(contract.PublicToolAX, args, false); result.IsError {
		t.Fatalf("%s failed: %s", mode, ResultText(result))
	}
	var walks []bool
	for _, request := range recording.observeRequests {
		walks = append(walks, request.Observation.FindWalk)
	}
	return walks
}

func TestFindRequestsTheProviderFindWalkOnlyOnLinux(t *testing.T) {
	for _, tc := range []struct {
		platform, mode string
		want           bool
	}{
		{"linux", contract.AXModeFind, true},
		{"linux", contract.AXModeObserve, false},
		{"test", contract.AXModeFind, false},
	} {
		walks := findWalkRequests(t, tc.platform, tc.mode)
		if len(walks) == 0 {
			t.Fatalf("%s %s: no provider observe request", tc.platform, tc.mode)
		}
		for _, walk := range walks {
			if walk != tc.want {
				t.Fatalf("%s %s: find_walk=%v, want %v", tc.platform, tc.mode, walk, tc.want)
			}
		}
	}
}

func TestFindWithMatchesReportsPartialCoverageWhenTheWalkStopped(t *testing.T) {
	for _, reason := range []string{"time", ""} {
		snapshot := &AppSnapshot{
			App:          AppDescriptor{Name: "Editor", PID: 7},
			WindowTitle:  "Editor",
			WindowHandle: "window-7",
			Elements: []ElementRecord{
				{Index: 0, RuntimeID: []int{0}, ControlType: "frame", Name: "Editor"},
				{Index: 1, RuntimeID: []int{0, 0}, ControlType: "push button", Name: "Save"},
			},
		}
		if reason != "" {
			snapshot.Annotations = []axProviderAnnotation{{
				ParentRuntimeID: []int{0}, Kind: "provider_omission",
				Count: 900, Reason: reason,
			}}
		}
		s := NewServiceWithBackend(&recordingPlatformBackend{
			accessibilityResponse: &ProviderAccessibilityResult{OK: true, Snapshot: snapshot},
		})
		result := s.CallForModel(contract.PublicToolAX, map[string]any{
			"mode": "find", "app": "Editor", "query": map[string]any{"name_contains": "Save"},
		}, false)
		text := ResultText(result)
		if result.IsError || !strings.Contains(text, "matches: 1") {
			t.Fatalf("find failed: %s", text)
		}
		partial := strings.Contains(text, `"coverage":"partial"`)
		if partial != (reason != "") {
			t.Fatalf("omission %q: partial coverage marker=%v in %s", reason, partial, text)
		}
		if strings.Contains(text, `"target_resolution"`) {
			t.Fatalf("a find with matches must not claim the target is unresolved: %s", text)
		}
	}
}
