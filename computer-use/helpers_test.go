package main

// Fixtures, test doubles and shorthands shared by several test files.

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func (s *service) verifyResolvedAXRefWithProbe(
	app,
	displayRef string,
	target *semanticTarget,
	goal verifyGoal,
	timeoutMS,
	stableMS int,
	probe func(ProviderActionRequest) (*ProviderAccessibilityResult, error),
) toolCallResult {
	return s.verifyResolvedAXRefWithProbes(
		app,
		displayRef,
		target,
		goal,
		timeoutMS,
		stableMS,
		probe,
		nil,
	)
}

// withActionOutcome applies an outcome without request facts, so no
// actionability evidence is attached.
func withActionOutcome(
	response toolCallResult,
	plan settlePlan,
	outcome settleOutcome,
	immediateSnapshot *appSnapshot,
	immediateNotes []string,
) toolCallResult {
	return withActionOutcomeForRequest(
		response,
		axServiceRequest{},
		plan,
		outcome,
		immediateSnapshot,
		immediateNotes,
	)
}

func lookupElement(snapshot *appSnapshot, elementIndex string) (*elementRecord, error) {
	return lookupElementFor(snapshot, elementIndex, "", "")
}

func rawLinesByIndex(snapshot *appSnapshot) map[int]string {
	if snapshot == nil {
		return nil
	}
	lines := snapshot.rawTreeLines
	if len(lines) == 0 {
		lines = snapshot.TreeLines
	}
	return indexTreeLines(lines)
}

// linesByIndex indexes rendered tree lines by their leading element index.
// Truncation lines such as "… 12 more" do not start with a number and are
// skipped.
func linesByIndex(snapshot *appSnapshot) map[int]string {
	if snapshot == nil {
		return nil
	}
	return indexTreeLines(snapshot.TreeLines)
}

func indexTreeLines(lines []string) map[int]string {
	out := map[int]string{}
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		head := trimmed
		if space := strings.IndexByte(trimmed, ' '); space > 0 {
			head = trimmed[:space]
		}
		index, err := strconv.Atoi(head)
		if err != nil {
			continue
		}
		if _, seen := out[index]; !seen {
			out[index] = line
		}
	}
	return out
}

func (registry *RefRegistry) Acquire(
	appIdentity,
	windowHandle,
	semanticTarget string,
	role CanonicalRole,
	name,
	revision string,
	providerIdentity ...string,
) string {
	return registry.AcquireTyped(
		AXLocatorSemantic,
		appIdentity,
		windowHandle,
		semanticTarget,
		role,
		name,
		revision,
		providerIdentity...,
	)
}

func runtimeReportedFailure(response ProviderAccessibilityResult) toolCallResult {
	return (*service)(nil).runtimeReportedFailure("", axServiceRequest{}, response)
}

func (s *service) expandShortAXRef(app, address string) (string, error) {
	address = strings.TrimSpace(address)
	if !isShortAXRef(address) {
		return address, nil
	}
	entry, err := s.resolveShortAXRef(app, address)
	if err != nil {
		return "", err
	}
	return entry.SemanticTarget, nil
}

func testPNGBase64(t *testing.T, width, height int) string {
	t.Helper()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatalf("encode test PNG: %v", err)
	}
	return base64.StdEncoding.EncodeToString(encoded.Bytes())
}

type recordingPlatformBackend struct {
	calls                  []string
	actionRequests         []ProviderActionRequest
	desktopRequests        []ProviderDesktopRequest
	observeRequests        []ProviderObserveRequest
	accessibilityResponse  *ProviderAccessibilityResult
	accessibilityResponses []ProviderAccessibilityResult
	desktopResponse        *ProviderDesktopResult
	windowResponse         *ProviderWindowResult
	err                    error
	processState           string
	windowKnown            bool
	windowAlive            bool
}

func (b *recordingPlatformBackend) accessibilityResult(
	call string,
) (ProviderAccessibilityResult, error) {
	b.calls = append(b.calls, call)
	if len(b.accessibilityResponses) > 0 {
		result := b.accessibilityResponses[0]
		b.accessibilityResponses = b.accessibilityResponses[1:]
		result.prepareForService()
		return result, b.err
	}
	if b.accessibilityResponse == nil {
		return ProviderAccessibilityResult{}, b.err
	}
	result := *b.accessibilityResponse
	result.prepareForService()
	return result, b.err
}

func (b *recordingPlatformBackend) ObserveApps(
	ProviderObserveAppsRequest,
) (ProviderAccessibilityResult, error) {
	return b.accessibilityResult("observe_apps")
}

func (b *recordingPlatformBackend) Observe(
	request ProviderObserveRequest,
) (ProviderAccessibilityResult, error) {
	b.observeRequests = append(b.observeRequests, request)
	return b.accessibilityResult("observe")
}

func (b *recordingPlatformBackend) ResolveAndAct(
	request ProviderActionRequest,
) (ProviderAccessibilityResult, error) {
	b.actionRequests = append(b.actionRequests, request)
	return b.accessibilityResult("resolve_and_act")
}

func (b *recordingPlatformBackend) ExecuteBatch(
	request ProviderDesktopRequest,
) (ProviderDesktopResult, error) {
	b.desktopRequests = append(b.desktopRequests, request)
	b.calls = append(b.calls, "desktop_execute_batch")
	if b.desktopResponse == nil {
		return ProviderDesktopResult{}, b.err
	}
	return *b.desktopResponse, b.err
}

func (b *recordingPlatformBackend) ListWindows(
	ProviderWindowListRequest,
) (ProviderWindowResult, error) {
	b.calls = append(b.calls, "window_list")
	if b.windowResponse == nil {
		return ProviderWindowResult{}, b.err
	}
	return *b.windowResponse, b.err
}

func (b *recordingPlatformBackend) ActOnWindow(
	request ProviderWindowActionRequest,
) (ProviderWindowResult, error) {
	b.calls = append(b.calls, "window_"+string(request.Action))
	if b.windowResponse == nil {
		return ProviderWindowResult{}, b.err
	}
	return *b.windowResponse, b.err
}

func (b *recordingPlatformBackend) WindowForPID(int) (bool, bool) {
	return b.windowKnown, b.windowAlive
}

func (*recordingPlatformBackend) HostInfo() HostBackendInfo {
	return HostBackendInfo{Platform: "test"}
}

func (*recordingPlatformBackend) RuntimeEnvironment(base []string) []string {
	return base
}

func (b *recordingPlatformBackend) ProcessState(int) string {
	return b.processState
}

func findToolDefinition(t *testing.T, name string) toolDefinition {
	t.Helper()
	for _, tool := range allToolDefinitions() {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("missing tool definition %q", name)
	return toolDefinition{}
}

func findA11ySchemaBranch(
	t *testing.T,
	mode string,
	observationTarget string,
) map[string]any {
	t.Helper()
	schema := findToolDefinition(t, "AX").InputSchema
	branches, ok := schema["oneOf"].([]any)
	if !ok {
		t.Fatalf("a11y schema must publish oneOf: %#v", schema)
	}
	for _, raw := range branches {
		branch, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		properties, ok := branch["properties"].(map[string]any)
		if !ok {
			continue
		}
		modeProperty, ok := properties["mode"].(map[string]any)
		if !ok || modeProperty["const"] != mode {
			continue
		}
		targetProperty, hasTarget := properties["observation_target"].(map[string]any)
		if observationTarget == "" && !hasTarget {
			return branch
		}
		if hasTarget && targetProperty["const"] == observationTarget {
			return branch
		}
	}
	t.Fatalf("a11y schema branch mode=%q target=%q not found",
		mode, observationTarget)
	return nil
}

func mainGoSource(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read Go source directory: %v", err)
	}
	names := make([]string, 0)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") {
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	var source strings.Builder
	for _, name := range names {
		data, readErr := os.ReadFile(name)
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		source.Write(data)
		source.WriteByte('\n')
	}
	return source.String()
}

// ---------------------------------------------------------------------------
// expect: act -> settle -> verify
// ---------------------------------------------------------------------------

func expectSnapshot(elements ...elementRecord) *appSnapshot {
	return &appSnapshot{
		App:       appDescriptor{Name: "gedit", PID: 7},
		TreeLines: []string{"0 frame Doc"},
		Elements:  elements,
	}
}

func editorFormSnapshot() *appSnapshot {
	return &appSnapshot{
		App:          appDescriptor{Name: "Editor", PID: 42},
		WindowTitle:  "Document",
		WindowHandle: "0x42",
		SnapshotID:   "editor-form",
		TreeLines: []string{
			"\t0 frame \"Document\"",
			"\t\t1 panel \"Form\"",
			"\t\t\t2 entry \"Name\" [focused]",
			"\t\t\t3 push button \"Save\" [click]",
			"\t\t4 status bar \"Ready\"",
		},
		Elements: []elementRecord{
			{Index: 0, RuntimeID: []int{0}, ControlType: "frame", Name: "Document"},
			{Index: 1, RuntimeID: []int{0, 0}, ControlType: "panel", Name: "Form"},
			{Index: 2, RuntimeID: []int{0, 0, 0}, ControlType: "entry", Name: "Name", States: "[focused]"},
			{Index: 3, RuntimeID: []int{0, 0, 1}, ControlType: "push button", Name: "Save", States: "[has-click-action]"},
			{Index: 4, RuntimeID: []int{0, 1}, ControlType: "status bar", Name: "Ready"},
		},
		scope: a11yScopeWindow,
	}
}

func typedTreeTextForTest(t *testing.T, snapshot *appSnapshot) string {
	t.Helper()
	typed, err := buildAXSnapshotV1(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	NewRefRegistry(128).AssignSnapshotRefs(typed)
	if err := distillAXSnapshot(typed); err != nil {
		t.Fatal(err)
	}
	lines, err := RenderAXSnapshotYAML(typed, AXRenderOptions{RootKey: "ax"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.axSnapshot = typed
	snapshot.TreeLines = lines
	return strings.Join(lines, "\n")
}

func windowTransitionSnapshot(
	handle,
	title string,
	withProviderIdentity bool,
) *appSnapshot {
	snapshot := editorFormSnapshot()
	snapshot.App = appDescriptor{
		Name:              "Editor",
		PID:               42,
		ProviderBusName:   ":1.42",
		ProcessGeneration: "pid:42:start:100",
	}
	snapshot.WindowTitle = title
	snapshot.WindowHandle = handle
	snapshot.SnapshotID = title
	snapshot.Elements[0].ControlType = "dialog"
	snapshot.Elements[0].Name = title
	snapshot.Elements[3].Name = "OK"
	if withProviderIdentity {
		for index := range snapshot.Elements {
			record := &snapshot.Elements[index]
			record.ProviderBusName = ":1.42"
			record.ProviderObjectPath = fmt.Sprintf("/dialog/%d", index)
			record.ProcessGeneration = "pid:42:start:100"
			record.ProviderWindowBusName = ":1.42"
			record.ProviderWindowObjectPath = "/dialog"
			record.ProviderWindowProcessGeneration = "pid:42:start:100"
			record.ProviderWindowRole = "dialog"
			record.ProviderWindowName = title
		}
	}
	return snapshot
}

func sheetBatchService(t *testing.T) (*service, string, string, string) {
	t.Helper()
	service := newService()
	appRef := service.appRefRegistry.Acquire(
		"LibreOffice", ":1.84", "pid:10:start:100", "c1",
	)
	handle := "w2:0123456789abcdef0123456789abcdef"
	windowRef := service.windowRefRegistry.Acquire(
		"libreoffice", handle, `frame "Sheet"`, "c1",
		":1.84", "/window/1", "pid:10:start:100", "frame", "Sheet",
		appRef,
	)
	elementRef := service.refRegistry.Acquire(
		"libreoffice", handle, `button "Save"`, CanonicalRole("button"),
		"Save", "c1",
		":1.84", "/window/1/save", "pid:10:start:100",
		":1.84", "/window/1", "pid:10:start:100", "frame", "Sheet",
		appRef, windowRef,
	)
	return service, appRef, windowRef, elementRef
}
