package core

import (
	"testing"

	"github.com/hqhq1025/hybrid-computer-use-oss/computer-use/internal/contract"
)

func TestBackgroundRefBatchKeepsWindowWhenSiblingActive(t *testing.T) {
	main := reportMain()
	main.active = false
	s, backend := rootOnlyService(t, false, main, reportWindow{
		handle: reportOtherHandle, role: "frame", title: "Other document",
		path: "/window/other", active: true,
	})
	window, refs := reportObserve(t, s, "Size")
	backend.onAction = func(b *reportBackend) {
		b.value = "changed"
	}
	result := s.callTool(contract.PublicToolAX, map[string]any{
		"mode": "act", "actions": []any{
			map[string]any{"type": "set_value", "ref": refs["Size"], "value": "14"},
			map[string]any{"type": "set_value", "ref": refs["Size"], "value": "16"},
		},
	})
	if result.IsError {
		t.Fatalf("background ref batch failed: %s", ResultText(result))
	}
	if len(backend.actionRequests) != 2 {
		t.Fatalf("wanted both actions, got %d", len(backend.actionRequests))
	}
	for _, request := range backend.actionRequests {
		if request.Target == nil || request.Target.WindowHandle != main.handle {
			t.Fatalf("action reached sibling: %#v", request)
		}
	}
	snapshot := s.currentSnapshot("Editor")
	if snapshot == nil || snapshot.WindowHandle != main.handle || snapshot.publicWindowRef != window {
		t.Fatalf("final observation left the bound window: %#v", snapshot)
	}
	if backend.windows[0].active || !backend.windows[1].active {
		t.Fatal("background semantic writes moved focus")
	}
}
