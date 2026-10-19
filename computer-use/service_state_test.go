package main

import "testing"

// Two sessions on one desktop share the address space that refs resolve in,
// but each keeps its own record of what it was shown.
func TestSessionsShareDesktopStateButNotBaselines(t *testing.T) {
	desktop := newDesktopState(&recordingPlatformBackend{})
	first := newSessionService(desktop)
	second := newSessionService(desktop)

	if first.refRegistry != second.refRegistry ||
		first.windowRefRegistry != second.windowRefRegistry ||
		first.appRefRegistry != second.appRefRegistry {
		t.Fatal("sessions on one desktop must share the ref registries")
	}
	ref := first.refRegistry.Acquire("editor", "w2:"+repeatHex("a"), "button:Save", "button", "Save", "rev1")
	if _, err := second.refRegistry.Resolve(ref, ""); err != nil {
		t.Fatalf("ref issued to one session must resolve in another: %v", err)
	}

	first.rememberDeliveredTree("w1|rev1|working|window|")
	if second.observations.lastFullTree["w1|rev1|working|window|"] {
		t.Fatal("a tree delivered to one session must not count as delivered to another")
	}
	if first.findCursorRegistry == second.findCursorRegistry {
		t.Fatal("find cursors are per session")
	}
}

// GUI input from one session invalidates cached accessibility state for
// every session; the other session drops its own caches on its next call.
func TestGUIInvalidationReachesOtherSessions(t *testing.T) {
	desktop := newDesktopState(&recordingPlatformBackend{})
	first := newSessionService(desktop)
	second := newSessionService(desktop)
	second.exposedHiddenRefs["key"] = map[int]elementRecord{1: {Index: 1}}

	first.invalidateAccessibilitySnapshots()
	if len(second.exposedHiddenRefs) != 1 {
		t.Fatal("the other session's caches change only on its own next call")
	}
	second.callTool(publicToolAX, map[string]any{"mode": "bogus"})
	if len(second.exposedHiddenRefs) != 0 {
		t.Fatal("a session must drop caches that another session's GUI input made stale")
	}
}

func repeatHex(digit string) string {
	out := ""
	for len(out) < 32 {
		out += digit
	}
	return out
}
