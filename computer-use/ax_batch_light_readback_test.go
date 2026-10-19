package main

import (
	"strings"
	"testing"
)

// rootOnlyBackend is a reportBackend whose post-action reads behave like the
// Linux provider's: they select the active modal window when there is one,
// and with max_tree_nodes=1 they return the window root and the window list
// only.
type rootOnlyBackend struct {
	*reportBackend
	// ignoreLimit reads the whole tree whatever the request asks, as the
	// code did before root-only reads.
	ignoreLimit   bool
	rootOnlyReads int
	fullReads     int
}

func (b *rootOnlyBackend) ResolveAndAct(request ProviderActionRequest) (ProviderAccessibilityResult, error) {
	if b.onAction != nil {
		b.onAction(b.reportBackend)
	}
	for _, window := range b.windows {
		if window.role == "dialog" && window.active {
			request.WindowHandle = window.handle
		}
	}
	onAction := b.onAction
	b.onAction = nil
	result, err := b.reportBackend.ResolveAndAct(request)
	b.onAction = onAction
	if result.Snapshot == nil {
		return result, err
	}
	if request.Observation.MaxTreeNodes == 1 && !b.ignoreLimit {
		b.rootOnlyReads++
		snapshot := *result.Snapshot
		snapshot.Elements = snapshot.Elements[:1]
		snapshot.TreeLines = snapshot.TreeLines[:1]
		snapshot.Annotations = []axProviderAnnotation{{
			ParentRuntimeID: []int{0}, Kind: "provider_omission",
			Count: 3, Reason: "budget",
		}}
		result.Snapshot = &snapshot
	} else {
		b.fullReads++
	}
	return result, err
}

func rootOnlyService(t *testing.T, ignoreLimit bool, windows ...reportWindow) (*service, *rootOnlyBackend) {
	t.Helper()
	s, report := reportService(t, windows...)
	backend := &rootOnlyBackend{reportBackend: report, ignoreLimit: ignoreLimit}
	s.backend = backend
	return s, backend
}

func keys(names ...string) []any {
	actions := []any{}
	for _, name := range names {
		actions = append(actions, map[string]any{"type": "keypress", "keys": []any{name}})
	}
	return actions
}

func TestLightReadbackOnlyBetweenRefLessInputs(t *testing.T) {
	key := axBatchAction{actionType: "keypress", key: "Tab"}
	text := axBatchAction{actionType: "type", text: "x"}
	click := axBatchAction{actionType: "click", targetRef: "r3"}
	refKey := axBatchAction{actionType: "keypress", key: "Tab", targetRef: "r2"}
	wait := axBatchAction{actionType: "wait", wait: &axWait{text: "x"}}
	for _, tc := range []struct {
		name    string
		actions []axBatchAction
		want    []bool
	}{
		// The read before the last action stays full: its notes compare
		// the trees around the last action.
		{"keys", []axBatchAction{key, text, key, text}, []bool{true, true, false, false}},
		{"two keys", []axBatchAction{key, key}, []bool{false, false}},
		// A click needs the element it names in the read before it.
		{"click next", []axBatchAction{key, click, key, key, key}, []bool{false, false, true, false, false}},
		{"ref key next", []axBatchAction{key, refKey, key, key, key}, []bool{false, false, true, false, false}},
		{"after a click", []axBatchAction{click, key, key, key}, []bool{false, true, false, false}},
		// A wait consults the latest read.
		{"wait next", []axBatchAction{key, wait, key, key, key}, []bool{false, false, true, false, false}},
		{"wait before last", []axBatchAction{key, key, key, wait, key}, []bool{true, true, false, false, false}},
	} {
		lastInput := -1
		for index, action := range tc.actions {
			if action.actionType != "wait" {
				lastInput = index
			}
		}
		for index := range tc.actions {
			if got := axBatchLightReadback(tc.actions, index, lastInput); got != tc.want[index] {
				t.Errorf("%s: step %d light=%v want %v", tc.name, index, got, tc.want[index])
			}
		}
	}
}

func TestKeyBatchUsesRootOnlyReadsBetweenActions(t *testing.T) {
	s, backend := rootOnlyService(t, false, reportMain())
	window, _ := reportObserve(t, s)
	result := s.callTool(publicToolAX, map[string]any{
		"mode": "act", "window_ref": window, "actions": keys("a", "b", "c", "d", "e"),
	})
	if result.IsError {
		t.Fatalf("batch failed: %s", resultText(result))
	}
	// Steps 1-3 read the root only; the read before the last action and
	// the final read are full.
	if backend.rootOnlyReads != 3 || backend.fullReads != 2 {
		t.Fatalf("root-only reads %d, full reads %d", backend.rootOnlyReads, backend.fullReads)
	}
	// The full ref map survives the root-only reads.
	if snapshot := s.currentSnapshot("Editor"); snapshot == nil || len(snapshot.Elements) != 4 {
		t.Fatalf("final snapshot is not a full read: %#v", snapshot)
	}
}

// A dialog that opens after a step must still stop the batch before the
// next key, although the read after that step covered the root only.
func TestRootOnlyReadStillStopsWhenTheWindowChanges(t *testing.T) {
	main := reportMain()
	s, backend := rootOnlyService(t, false, main)
	window, _ := reportObserve(t, s)
	actions := 0
	backend.onAction = func(b *reportBackend) {
		actions++
		if actions == 2 {
			b.windows[0].active = false
			b.windows = append(b.windows, reportWindow{
				handle: reportDialogHandle, role: "dialog", title: "Save changes?",
				path: "/window/2", active: true,
			})
		}
	}
	result := s.callTool(publicToolAX, map[string]any{
		"mode": "act", "window_ref": window, "actions": keys("a", "b", "c", "d", "e"),
	})
	text := resultText(result)
	if !result.IsError || !strings.Contains(text, "batch_window_changed") {
		t.Fatalf("batch did not stop at the window change:\n%s", text)
	}
	if actions != 2 {
		t.Fatalf("%d actions were sent; the batch must stop after the second", actions)
	}
	if backend.rootOnlyReads != 2 {
		t.Fatalf("the reads after steps 1 and 2 should be root-only, got %d", backend.rootOnlyReads)
	}
	batch, _ := result.StructuredContent["actions_completed_without_call_error"].(int)
	if batch != 2 {
		t.Fatalf("completed=%v want 2: %#v", result.StructuredContent["actions_completed_without_call_error"], result.StructuredContent)
	}
}

// The model-visible result of a key batch is the same with root-only reads
// between actions as with full reads.
func TestRootOnlyReadsKeepModelVisibleOutput(t *testing.T) {
	run := func(rootOnly bool) string {
		s, _ := rootOnlyService(t, !rootOnly, reportMain())
		window, _ := reportObserve(t, s)
		line, rest := visibleAct(t, s, map[string]any{
			"mode": "act", "window_ref": window, "actions": keys("a", "b", "c", "d"),
		})
		return line + "\n" + strings.Join(rest, "\n")
	}
	full, light := run(false), run(true)
	if full != light {
		t.Fatalf("model-visible output differs\nfull reads:\n%s\nroot-only reads:\n%s", full, light)
	}
}
