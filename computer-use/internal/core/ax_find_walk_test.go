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

func findWalkSnapshot(target string, omitted bool) ProviderAccessibilityResult {
	snapshot := &AppSnapshot{
		App:          AppDescriptor{Name: "Editor", PID: 7},
		WindowTitle:  "Editor",
		WindowHandle: "window-7",
		Elements: []ElementRecord{
			{Index: 0, RuntimeID: []int{0}, ControlType: "frame", Name: "Editor"},
			{Index: 1, RuntimeID: []int{0, 0}, ControlType: "push button", Name: target},
		},
	}
	if omitted {
		snapshot.Annotations = []axProviderAnnotation{{
			ParentRuntimeID: []int{0}, Kind: "provider_omission",
			Count: 900, Reason: "time",
		}}
	}
	return ProviderAccessibilityResult{OK: true, Snapshot: snapshot}
}

func findWithWalk(
	t *testing.T, platform, mode string, responses ...ProviderAccessibilityResult,
) ([]bool, string) {
	t.Helper()
	recording := &recordingPlatformBackend{accessibilityResponses: responses}
	var backend PlatformBackend = recording
	if platform == "linux" {
		backend = linuxRecordingBackend{recording}
	}
	s := NewServiceWithBackend(backend)
	args := map[string]any{"mode": mode, "app": "Editor"}
	if mode == contract.AXModeFind {
		args["query"] = map[string]any{"name_contains": "Save"}
	}
	result := s.CallForModel(contract.PublicToolAX, args, false)
	if result.IsError {
		t.Fatalf("%s failed: %s", mode, ResultText(result))
	}
	var walks []bool
	for _, request := range recording.observeRequests {
		walks = append(walks, request.Observation.FindWalk)
	}
	return walks, ResultText(result)
}

func TestLinuxFindWalksPastTheRenderBudgetOnlyWhenTheSnapshotHasNoMatch(t *testing.T) {
	for _, tc := range []struct {
		name, platform, mode string
		responses            []ProviderAccessibilityResult
		walks                []bool
		matches              string
	}{
		{"match in partial snapshot", "linux", contract.AXModeFind,
			[]ProviderAccessibilityResult{findWalkSnapshot("Save", true)},
			[]bool{false}, "matches: 1"},
		{"no match in complete snapshot", "linux", contract.AXModeFind,
			[]ProviderAccessibilityResult{findWalkSnapshot("Open", false)},
			[]bool{false}, "matches: 0"},
		{"no match in partial snapshot", "linux", contract.AXModeFind,
			[]ProviderAccessibilityResult{findWalkSnapshot("Open", true), findWalkSnapshot("Save", false)},
			[]bool{false, true}, "matches: 1"},
		{"other providers reject the field", "test", contract.AXModeFind,
			[]ProviderAccessibilityResult{findWalkSnapshot("Open", true)},
			[]bool{false}, "matches: 0"},
		{"observe", "linux", contract.AXModeObserve,
			[]ProviderAccessibilityResult{findWalkSnapshot("Open", true)},
			[]bool{false}, ""},
	} {
		walks, text := findWithWalk(t, tc.platform, tc.mode, tc.responses...)
		if len(walks) != len(tc.walks) {
			t.Fatalf("%s: find_walk per read %v, want %v", tc.name, walks, tc.walks)
		}
		for index := range walks {
			if walks[index] != tc.walks[index] {
				t.Fatalf("%s: find_walk per read %v, want %v", tc.name, walks, tc.walks)
			}
		}
		if !strings.Contains(text, tc.matches) {
			t.Fatalf("%s: want %q in %s", tc.name, tc.matches, text)
		}
	}
}

func TestFindWithMatchesReportsPartialCoverageWhenTheWalkStopped(t *testing.T) {
	for _, omitted := range []bool{true, false} {
		_, text := findWithWalk(t, "test", contract.AXModeFind, findWalkSnapshot("Save", omitted))
		if !strings.Contains(text, "matches: 1") {
			t.Fatalf("find failed: %s", text)
		}
		if partial := strings.Contains(text, `"coverage":"partial"`); partial != omitted {
			t.Fatalf("omitted=%v: partial coverage marker=%v in %s", omitted, partial, text)
		}
		if strings.Contains(text, `"target_resolution"`) {
			t.Fatalf("a find with matches must not claim the target is unresolved: %s", text)
		}
	}
}
