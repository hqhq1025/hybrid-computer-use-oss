package main

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func recordsService(t *testing.T) (*service, *appWideBackend) {
	t.Helper()
	t.Setenv("COMPUTER_USE_ERROR_CONTEXT", "off")
	s, backend, _ := untilScopeService(t, untilMainWindow(), untilDialogWindow())
	return s, backend
}

// observe in records returns every canonical element, and the same refs the
// text tree shows.
func TestObserveRecordsReturnElementsWithRefs(t *testing.T) {
	s, _ := recordsService(t)
	text := resultText(s.callForModel(publicToolAX, map[string]any{"mode": "observe", "window_ref": "w1"}, false))
	record := s.callRecord(publicToolAX, map[string]any{"mode": "observe", "window_ref": "w1"}, callOptions{})
	if !record.OK || record.Op != "ax.observe" || record.Window == nil || record.Window.Ref != "w1" {
		t.Fatalf("observe record: %+v", record)
	}
	refs := 0
	for _, element := range record.Elements {
		if element.Ref != "" {
			refs++
			if !strings.Contains(text, "[ref="+element.Ref+"]") {
				t.Fatalf("records ref %s is not in the text tree:\n%s", element.Ref, text)
			}
		}
	}
	if refs == 0 || record.SnapshotID == "" || record.TimingMS == nil {
		t.Fatalf("observe record has no refs, snapshot id or timing: %+v", record)
	}
}

// since reports changes against an earlier records snapshot; an unknown
// since is an argument error, not a silent full tree.
func TestObserveRecordsSinceDiff(t *testing.T) {
	s, _ := recordsService(t)
	first := s.callRecord(publicToolAX, map[string]any{"mode": "observe", "window_ref": "w1"}, callOptions{})
	second := s.callRecord(publicToolAX, map[string]any{"mode": "observe", "window_ref": "w1"}, callOptions{Since: first.SnapshotID})
	if !second.OK || second.Diff == nil || !second.Diff.Unchanged || second.Elements != nil {
		t.Fatalf("unchanged window must diff as unchanged: %+v", second)
	}
	before := []RecordElement{{Ref: "r1", Role: "button", Name: "Save"}, {Ref: "r2", Role: "button", Name: "Old"}}
	after := []RecordElement{{Ref: "r1", Role: "button", Name: "Save", States: map[string]string{"pressed": "true"}}, {Ref: "r3", Role: "button", Name: "New"}}
	diff := diffRecordElements("s1", before, after)
	if len(diff.Changed) != 1 || diff.Changed[0].Ref != "r1" || len(diff.Added) != 1 || len(diff.Removed) != 1 || diff.Unchanged {
		t.Fatalf("diff: %+v", diff)
	}
	missing := s.callRecord(publicToolAX, map[string]any{"mode": "observe", "window_ref": "w1"}, callOptions{Since: "s999"})
	if missing.OK || missing.Code != axCodeInvalidArgument || missing.Path != "since" {
		t.Fatalf("unknown since: %+v", missing)
	}
}

// find in records pages by limit alone and follows the cursor; the text
// budget never cuts records.
func TestFindRecordsPageWithCursor(t *testing.T) {
	t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "600")
	svc, snapshot, matches := findDeliveryMany(t, 30)
	svc.rememberSnapshot("Independent", snapshot)
	_ = matches
	svc.windowRefRegistry = NewWindowRefRegistry(8)
	args := map[string]any{"mode": "find", "app": "Independent", "query": map[string]any{"name_contains": "Target"}, "limit": 25}
	backend := &recordingPlatformBackend{accessibilityResponse: &ProviderAccessibilityResult{OK: true, Snapshot: snapshot}}
	svc.backend = backend
	first := svc.callRecord(publicToolAX, args, callOptions{})
	if !first.OK || first.Page == nil || first.Page.Total != 30 || first.Page.Returned != 25 || first.Page.NextCursor == "" {
		t.Fatalf("first page: ok=%t code=%s msg=%s page=%+v", first.OK, first.Code, first.Message, first.Page)
	}
	second := svc.callRecord(publicToolAX, map[string]any{"mode": "find", "cursor": first.Page.NextCursor}, callOptions{})
	if !second.OK || second.Page.Returned != 5 || second.Page.Offset != 25 || second.Page.NextCursor != "" {
		t.Fatalf("second page: %+v", second.Page)
	}
	seen := map[string]bool{}
	for _, element := range append(first.Matches, second.Matches...) {
		if element.Ref == "" || seen[element.Ref] {
			t.Fatalf("every match needs a distinct ref: %+v", element)
		}
		seen[element.Ref] = true
	}
}

// Records report the same public error as the text contract.
func TestRecordErrorsMatchTextEvidence(t *testing.T) {
	for _, args := range []map[string]any{
		{"mode": "act", "actions": []any{map[string]any{"ref": "w1", "action": "close"}}},
		{"mode": "observe", "window_ref": "w9"},
		{"mode": "find", "cursor": "f9"},
	} {
		s := newServiceWithBackend(&recordingPlatformBackend{})
		textLine := firstLine(resultText(newServiceWithBackend(&recordingPlatformBackend{}).callForModel(publicToolAX, args, false)))
		var evidence map[string]any
		if err := json.Unmarshal([]byte(textLine), &evidence); err != nil {
			t.Fatalf("evidence line: %v %q", err, textLine)
		}
		record := s.callRecord(publicToolAX, args, callOptions{})
		if record.OK || record.Code != evidence["code"] || record.Hint != evidence["hint"] {
			t.Fatalf("%v: record code=%s hint=%s, text %v", args, record.Code, record.Hint, evidence)
		}
	}
}

// read resolves each ref live and reports a stale ref without rebinding.
func TestReadRecordsResolveEachRef(t *testing.T) {
	s, _, _, ref := sheetBatchService(t)
	value := "42"
	backend := &recordingPlatformBackend{accessibilityResponse: &ProviderAccessibilityResult{
		OK: true, ResolvedElement: &elementRecord{ControlType: "push button", Name: "Save", Value: value, States: "[checked]"},
	}}
	s.backend = backend
	record := s.callRecord(publicToolAX, map[string]any{"mode": "read", "refs": []any{ref, "r999"}}, callOptions{})
	if !record.OK || len(record.Read) != 2 {
		t.Fatalf("read: %+v", record)
	}
	if !record.Read[0].OK || record.Read[0].Element.Name != "Save" || record.Read[0].Element.Value != value ||
		record.Read[0].Element.States["checked"] != "true" {
		t.Fatalf("live read: %+v", record.Read[0])
	}
	if record.Read[1].OK || record.Read[1].Code != axCodeStaleRef {
		t.Fatalf("never-issued ref must be stale_ref: %+v", record.Read[1])
	}
	if len(backend.actionRequests) != 1 || backend.actionRequests[0].Intent != AccessibilityResolveTarget {
		t.Fatalf("read must only resolve, never act: %+v", backend.actionRequests)
	}
	bad := s.callRecord(publicToolAX, map[string]any{"mode": "read", "refs": []any{}}, callOptions{})
	if bad.OK || bad.Code != axCodeInvalidArgument {
		t.Fatalf("empty refs: %+v", bad)
	}
}

// read has no text form: the MCP contract rejects it like any unknown mode.
func TestReadIsNotPartOfTheModelContract(t *testing.T) {
	result := newServiceWithBackend(&recordingPlatformBackend{}).callForModel(publicToolAX,
		map[string]any{"mode": "read", "refs": []any{"r1"}}, false)
	if !result.IsError || !strings.Contains(resultText(result), "invalid_argument") {
		t.Fatalf("text read must be rejected: %s", resultText(result))
	}
}

// A repeated request_id returns the first result and never runs the call
// again, even when the retry arrives while the first call is running.
func TestRequestIDRunsACallOnce(t *testing.T) {
	shared := &desktopShared{requestResults: map[string]storedRequest{}}
	runs := 0
	release := make(chan struct{})
	started := make(chan struct{})
	run := func() any {
		runs++
		close(started)
		<-release
		return Record{OK: true, Op: "ax.act", InputEmitted: true}
	}
	var wg sync.WaitGroup
	var first, second any
	var replayed bool
	wg.Add(1)
	go func() {
		defer wg.Done()
		first, _, _ = shared.replayOnce("id-1", "key", false, run)
	}()
	<-started
	wg.Add(1)
	go func() {
		defer wg.Done()
		second, replayed, _ = shared.replayOnce("id-1", "key", true, func() any { runs++; return nil })
	}()
	close(release)
	wg.Wait()
	if runs != 1 || !replayed || first.(Record).InputEmitted != second.(Record).InputEmitted {
		t.Fatalf("runs=%d replayed=%t", runs, replayed)
	}
	if _, _, err := shared.replayOnce("id-1", "other", true, run); err == nil {
		t.Fatal("the same request_id with another call must be rejected")
	}
}

// Every public code the core can return is in the published error table.
func TestEveryPublicErrorCodeIsPublished(t *testing.T) {
	core := []string{
		axCodeInvalidArgument, axCodeStaleRef, axCodeWindowChanged, axCodeWaitTimeout,
		axCodeEffectUnknown, axCodeAppUnavailable, axCodeRuntimeError,
		axCodeAmbiguousWindow, axCodeWindowNotFound, axCodeNoFocusedWindow, axCodeGUIFrameStale,
	}
	for _, code := range core {
		if _, ok := publicErrorCodes[code]; !ok {
			t.Fatalf("public code %s is missing from the schema error table", code)
		}
	}
	for code := range axPublicHints {
		if _, ok := publicErrorCodes[code]; !ok {
			t.Fatalf("hinted code %s is missing from the schema error table", code)
		}
	}
	source := mainGoSource(t)
	for _, line := range strings.Split(source, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "axCode") || !strings.Contains(line, "= \"") {
			continue
		}
		name, value, _ := strings.Cut(line, "=")
		value = strings.Trim(strings.TrimSpace(value), `"`)
		if strings.HasPrefix(strings.TrimSpace(name), "axCode") {
			if _, ok := publicErrorCodes[value]; !ok {
				t.Fatalf("code %s declared in core is not published", value)
			}
		}
	}
}

// GUI records carry the image path and screen size, never base64.
func TestGUIRecordWritesImageFile(t *testing.T) {
	t.Setenv("COMPUTER_USE_OUTPUT_DIR", t.TempDir())
	dispatched := 1
	s := newServiceWithBackend(&recordingPlatformBackend{desktopResponse: &ProviderDesktopResult{
		OK: true, GUIResult: ProviderGUIResult{Dispatched: &dispatched},
		Snapshot: &appSnapshot{ScreenshotPNGBase64: testPNGBase64(t, 40, 30), CoordinateSpace: "screen_image_px",
			FrameID: "f", Width: 40, Height: 30},
	}})
	record := s.callRecord(publicToolGUI, map[string]any{"actions": []any{map[string]any{"type": "screenshot"}}}, callOptions{})
	encoded, _ := json.Marshal(record)
	if !record.OK || record.Screen == nil || record.Screen.Width != 40 || record.Screen.Image == "" ||
		strings.Contains(string(encoded), "iVBOR") {
		t.Fatalf("gui record: %s", encoded)
	}
}
