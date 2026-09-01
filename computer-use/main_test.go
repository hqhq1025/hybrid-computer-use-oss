package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testPNGBase64(t *testing.T, width, height int) string {
	t.Helper()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatalf("encode test PNG: %v", err)
	}
	return base64.StdEncoding.EncodeToString(encoded.Bytes())
}

func testJPEGBase64(t *testing.T, width, height int) string {
	t.Helper()
	var encoded bytes.Buffer
	if err := jpeg.Encode(
		&encoded,
		image.NewRGBA(image.Rect(0, 0, width, height)),
		&jpeg.Options{Quality: 80},
	); err != nil {
		t.Fatalf("encode test JPEG: %v", err)
	}
	return base64.StdEncoding.EncodeToString(encoded.Bytes())
}

type recordingPlatformBackend struct {
	calls                  []string
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
	ProviderActionRequest,
) (ProviderAccessibilityResult, error) {
	return b.accessibilityResult("resolve_and_act")
}

func (b *recordingPlatformBackend) ExecuteBatch(
	ProviderDesktopRequest,
) (ProviderDesktopResult, error) {
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

func TestDefaultServiceUsesPlatformBackend(t *testing.T) {
	service := newService()
	want := runtime.GOOS
	if want != "linux" && want != "darwin" && want != "windows" {
		want = "unsupported"
	}
	if got := service.backend.HostInfo().Platform; got != want {
		t.Fatalf("default backend platform = %q, want %q (%T)", got, want, service.backend)
	}
}

func TestRuntimeCallsRouteByPlatformDomain(t *testing.T) {
	backend := &recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{OK: true},
		desktopResponse:       &ProviderDesktopResult{OK: true},
		windowResponse:        &ProviderWindowResult{OK: true},
	}
	service := newServiceWithBackend(backend)
	tests := []struct {
		name string
		call func() error
		want string
	}{
		{name: "observe_apps", want: "observe_apps", call: func() error {
			_, err := service.callProviderObserveApps(ProviderObserveAppsRequest{
				ProtocolVersion: providerProtocolV2,
			})
			return err
		}},
		{name: "observe", want: "observe", call: func() error {
			_, err := service.callProviderObserve(ProviderObserveRequest{
				ProtocolVersion: providerProtocolV2, App: "Editor",
			})
			return err
		}},
		{name: "resolve_and_act", want: "resolve_and_act", call: func() error {
			_, err := service.callProviderAction(ProviderActionRequest{
				ProtocolVersion: providerProtocolV2,
				Intent:          AccessibilityClick,
				App:             "Editor",
				Target: &ProviderSemanticTarget{
					Selector: `button "Save"`,
					HasName:  true,
				},
				Click: &ProviderClickInput{},
			})
			return err
		}},
		{name: "desktop", want: "desktop_execute_batch", call: func() error {
			_, err := service.callProviderDesktop(ProviderDesktopRequest{
				ProtocolVersion: providerProtocolV2,
			})
			return err
		}},
		{name: "window_activate", want: "window_activate", call: func() error {
			_, err := service.callProviderWindowAction(ProviderWindowActionRequest{
				ProtocolVersion: providerProtocolV2,
				Action:          WindowActivate,
				App:             "Editor",
			})
			return err
		}},
		{name: "window_close", want: "window_close", call: func() error {
			_, err := service.callProviderWindowAction(ProviderWindowActionRequest{
				ProtocolVersion: providerProtocolV2,
				Action:          WindowClose,
				App:             "Editor",
			})
			return err
		}},
		{name: "window_list", want: "window_list", call: func() error {
			_, err := service.callProviderWindowList(ProviderWindowListRequest{
				ProtocolVersion: providerProtocolV2,
				App:             "Editor",
			})
			return err
		}},
	}
	for _, test := range tests {
		backend.calls = nil
		if err := test.call(); err != nil {
			t.Fatalf("%s call failed: %v", test.name, err)
		}
		if !reflect.DeepEqual(backend.calls, []string{test.want}) {
			t.Fatalf("%s routed to %v, want %s", test.name, backend.calls, test.want)
		}
	}
}

func TestListAppsPreservesProviderDisplayTextAndInjectsRef(t *testing.T) {
	backend := &recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{
			OK: true,
			Apps: []appDescriptor{{
				Name:              "vlc",
				DisplayText:       `vlc [window="master.m3u8 - VLC media player", pid=202]`,
				InputCapable:      true,
				ProviderBusName:   ":1.42",
				ProcessGeneration: "pid:202:start:100",
			}},
		},
	}
	service := newServiceWithBackend(backend)
	result := service.listApps()
	text := resultText(result)
	if !strings.Contains(
		text,
		`vlc [ref=a1] [window="master.m3u8 - VLC media player", pid=202]`,
	) {
		t.Fatalf("provider display text was not preserved: %q", text)
	}
}

func TestAccessibilityOperationRoutesTypedWindowActions(t *testing.T) {
	backend := &recordingPlatformBackend{
		windowResponse: &ProviderWindowResult{
			OK: true,
			Mutation: &ProviderMutationFacts{
				InputEventsEmitted:  true,
				DesktopStateMutated: true,
			},
			ActionEvidence: &ProviderActionEvidence{
				InputEmitted: true,
			},
		},
	}
	service := newServiceWithBackend(backend)
	for _, action := range []WindowOperation{WindowActivate, WindowClose} {
		backend.calls = nil
		result, err := service.callProviderAccessibilityOperation(
			axServiceRequest{
				App:          "Editor",
				WindowAction: &action,
				SemanticTarget: &semanticTarget{
					Selector:    `window "Editor"`,
					Role:        "window",
					Name:        "Editor",
					HasName:     true,
					LocatorKind: AXLocatorProviderBound,
				},
			},
		)
		if err != nil {
			t.Fatalf("%s window action failed: %v", action, err)
		}
		if !reflect.DeepEqual(
			backend.calls,
			[]string{"window_" + string(action)},
		) {
			t.Fatalf("%s routed to %v", action, backend.calls)
		}
		if result == nil || !result.OK || !result.InputEventsEmitted ||
			!result.DesktopStateMutated || result.ActionEvidence == nil {
			t.Fatalf("%s result projection is incomplete: %#v", action, result)
		}
	}
}

func TestAccessibilityOperationRejectsConflictingTypedDomains(t *testing.T) {
	service := newServiceWithBackend(&recordingPlatformBackend{})
	action := WindowActivate
	_, err := service.callProviderAccessibilityOperation(axServiceRequest{
		Operation:    axServiceObserve,
		WindowAction: &action,
	})
	if err == nil || !strings.Contains(
		err.Error(),
		"exactly one typed operation domain",
	) {
		t.Fatalf("conflicting typed domains did not fail closed: %v", err)
	}
}

func TestRuntimeTimeoutDiagnosticUsesInjectedBackend(t *testing.T) {
	backend := &recordingPlatformBackend{
		processState: "running",
		windowKnown:  true,
		windowAlive:  true,
	}
	service := newServiceWithBackend(backend)
	got := service.runtimeTimeoutDiagnostic(&appSnapshot{
		App: appDescriptor{PID: 42},
	})
	if !strings.Contains(got, "cached_pid_present=true") ||
		!strings.Contains(got, "window_for_cached_pid=true") {
		t.Fatalf("timeout diagnostic did not use backend facts: %q", got)
	}
}

func TestToolDefinitionCount(t *testing.T) {
	if got := len(toolDefinitions()); got != 2 {
		t.Fatalf("toolDefinitions() count = %d, want 2", got)
	}
	findToolDefinition(t, "AX")
	findToolDefinition(t, "GUI")
	for _, removed := range []string{"get_screenshot", "click_xy", "drag_xy"} {
		for _, tool := range toolDefinitions() {
			if tool.Name == removed {
				t.Fatalf("legacy GUI tool %q must not be exposed", removed)
			}
		}
	}
}

func TestConfirmationNotesStateTheirLimit(t *testing.T) {
	// "confirmed it applied" 是过度承诺：回读确认的是控件的值变了，
	// 不是应用采纳了这个值。实测中把行距 combo 写成 "Double" 会回读成功，
	// 但文档的 line-height 纹丝不动——对话框把控件状态与文档状态分开，
	// 只在 OK/Apply 时提交。
	for _, fragment := range []string{
		"CONTROL changed, not that the application adopted the value",
		"require OK/Apply before the value takes effect",
	} {
		if !strings.Contains(linuxRuntimeScript, fragment) {
			t.Fatalf("confirmation notes must state what they do NOT prove: missing %q", fragment)
		}
	}
	if strings.Contains(linuxRuntimeScript, "confirmed it applied.") {
		t.Fatal("the old over-claiming wording must not come back")
	}
}

func TestDecisionRelevantStatesAreRendered(t *testing.T) {
	// 禁用、选中、展开、勾选这几类状态此前完全不在树里。agent 因此会对着
	// 禁用控件反复点击，也看不出下拉/菜单到底展开没有。
	for _, fragment := range []string{
		// 断言函数存在即可，不钉签名——签名会因为参数增减而变，
		// 那不是这条测试要保护的东西。
		"def state_segment(",
		"NOTABLE_STATES",
	} {
		if !strings.Contains(linuxRuntimeScript, fragment) {
			t.Fatalf("tree must expose decision-relevant states: missing %q", fragment)
		}
	}
	// 只渲染"非默认"的一侧：每个节点都标 enabled 会淹没信号。
	if strings.Contains(linuxRuntimeScript, `("ENABLED", "enabled")`) {
		t.Fatal("rendering the default side of every state would drown the signal")
	}
	// 绝不从状态缺失反推语义：Nautilus 的文件图标不设 ENABLED/SENSITIVE 却完全
	// 可操作，标成 disabled 会让 agent 跳过真实目标。
	if strings.Contains(linuxRuntimeScript, `marks.append("disabled")`) {
		t.Fatal("inferring 'disabled' from a missing ENABLED state produces false positives on Linux")
	}
	if !strings.Contains(linuxRuntimeScript, "绝不从状态的缺失反推语义") {
		t.Fatal("the rule against inferring from absent states must stay documented in the source")
	}
}

func TestTruncationIsPrioritisedAndVisible(t *testing.T) {
	// 深度优先截断等于按遍历顺序随机丢弃：先到的占满配额，后面的整片消失，
	// 而且 agent 完全不知道树被砍过。
	for _, fragment := range []string{
		"BUDGET_PRESSURE_RATIO",
		"def is_structural_filler(record):",
		"node(s) omitted —",
		"server-owned node budget filled up",
	} {
		if !strings.Contains(linuxRuntimeScript, fragment) {
			t.Fatalf("truncation must be prioritised and reported: missing %q", fragment)
		}
	}
	if !strings.Contains(linuxRuntimeScript, "not interactable and/or not on screen") {
		t.Fatal("the omission notice must say what was dropped, not just how many")
	}
}

func TestAXObserveRejectsGuessedWholeTreeGeometry(t *testing.T) {
	service := newService()
	result := service.dispatch(
		"a11y",
		map[string]any{
			"mode":  "observe",
			"app":   "editor",
			"boxes": true,
		},
	)
	if !result.IsError {
		t.Fatalf("guessed boxes argument must fail closed: %#v", result)
	}
	text := resultText(result)
	if !strings.Contains(text, `mode="observe" does not accept fields: "boxes"`) {
		t.Fatalf("AX observe field rejection is not actionable: %q", text)
	}
	legacy := service.dispatch("get_app_state", map[string]any{"app": "editor"})
	if !legacy.IsError ||
		!strings.Contains(resultText(legacy), `unsupportedTool("get_app_state")`) {
		t.Fatalf("hidden get_app_state entrypoint was restored: %#v", legacy)
	}
}

func TestWorkingTreeProjectionKeepsCanonicalRefsWhileCompressingLayout(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"Editor\"",
			"\t\t1 section [actions=clickAncestor]",
			"\t\t\t2 panel [actions=clickAncestor]",
			"\t\t\t\t3 push button \"Save\" [has-click-action]",
		},
		Elements: []elementRecord{
			{Index: 0, ControlType: "frame", Name: "Editor"},
			{Index: 1, ControlType: "section", Actions: []string{"clickAncestor"}},
			{Index: 2, ControlType: "panel", Actions: []string{"clickAncestor"}},
			{Index: 3, ControlType: "push button", Name: "Save", States: "[has-click-action]"},
		},
	}

	applyWorkingTreeProjection(snapshot, treeViewWorking)

	text := strings.Join(snapshot.TreeLines, "\n")
	if strings.Contains(text, "1 section") || strings.Contains(text, "2 panel") {
		t.Fatalf("neutral layout wrappers leaked into working view:\n%s", text)
	}
	if !strings.Contains(text, `3 push button "Save"`) {
		t.Fatalf("actionable descendant was lost:\n%s", text)
	}
	if snapshot.serializerOmitted != 2 || len(snapshot.Elements) != 4 {
		t.Fatalf("projection changed canonical records: %#v", snapshot)
	}
	raw := rawLinesByIndex(snapshot)
	if !strings.Contains(raw[1], "clickAncestor") || raw[3] == "" {
		t.Fatalf("raw canonical lines were not retained: %#v", raw)
	}
}

func TestWorkingTreeProjectionCompactsClickCapability(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"Page\" [has-click-action]",
			"\t\t1 section [has-click-action]",
			"\t\t\t2 heading \"Title\" [has-click-action]",
			"\t\t\t3 push button \"Continue\" [has-click-action]",
		},
		Elements: []elementRecord{
			{Index: 0, ControlType: "frame", Name: "Page", States: "[has-click-action]"},
			{Index: 1, ControlType: "section", States: "[has-click-action]"},
			{Index: 2, ControlType: "heading", Name: "Title", States: "[has-click-action]"},
			{Index: 3, ControlType: "push button", Name: "Continue", States: "[has-click-action]"},
		},
	}

	applyWorkingTreeProjection(snapshot, treeViewWorking)

	text := strings.Join(snapshot.TreeLines, "\n")
	if strings.Contains(text, "1 section") {
		t.Fatalf("ubiquitous click marker kept a neutral section:\n%s", text)
	}
	if strings.Contains(linesByIndex(snapshot)[2], "click") {
		t.Fatalf("heading retained non-informative click marker:\n%s", text)
	}
	if !strings.Contains(linesByIndex(snapshot)[3], "[click]") {
		t.Fatalf("button lost compact click capability:\n%s", text)
	}
	if strings.Contains(linesByIndex(snapshot)[3], "has-click-action") {
		t.Fatalf("working view retained verbose click capability:\n%s", text)
	}
}

func TestWorkingTreeProjectionDropsEmptyNoiseButKeepsSemanticBoundaries(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"Browser\"",
			"\t\t1 document web \"Page\"",
			"\t\t\t2 heading",
			"\t\t\t3 notification",
			"\t\t\t4 image [has-click-action]",
			"\t\t\t5 panel",
			"\t\t\t\t6 push button [has-click-action]",
			"\t\t\t7 menu",
			"\t\t\t8 table",
		},
		Elements: []elementRecord{
			{Index: 0, ControlType: "frame", Name: "Browser"},
			{Index: 1, ControlType: "document web", Name: "Page"},
			{Index: 2, ControlType: "heading"},
			{Index: 3, ControlType: "notification"},
			{Index: 4, ControlType: "image", States: "[has-click-action]"},
			{Index: 5, ControlType: "panel"},
			{Index: 6, ControlType: "push button", States: "[has-click-action]"},
			{Index: 7, ControlType: "menu"},
			{Index: 8, ControlType: "table"},
		},
	}

	applyWorkingTreeProjection(snapshot, treeViewWorking)

	text := strings.Join(snapshot.TreeLines, "\n")
	for _, hidden := range []string{
		"2 heading",
		"3 notification",
		"4 image",
		"5 panel",
	} {
		if strings.Contains(text, hidden) {
			t.Fatalf("empty noise node %q leaked into working view:\n%s", hidden, text)
		}
	}
	for _, kept := range []string{
		`1 document web "Page"`,
		"6 push button [click]",
		"7 menu",
		"8 table",
	} {
		if !strings.Contains(text, kept) {
			t.Fatalf("semantic boundary/action %q was lost:\n%s", kept, text)
		}
	}
	if len(snapshot.Elements) != 9 || snapshot.serializerOmitted != 4 {
		t.Fatalf("working projection changed canonical records: %#v", snapshot)
	}
	if !strings.Contains(text,
		"[hidden=4 canonical=9 branches=0; find/ref/action retain hidden nodes]") {
		t.Fatalf("compact omission ledger missing:\n%s", text)
	}
}

func TestWorkingTreeProjectionRestoresDuplicateTargetBranchAncestors(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"App\"",
			"\t\t1 panel",
			"\t\t\t2 panel",
			"\t\t\t\t3 push button \"Close\" [has-click-action]",
			"\t\t4 panel",
			"\t\t\t5 panel",
			"\t\t\t\t6 push button \"Close\" [has-click-action]",
		},
		Elements: []elementRecord{
			{Index: 0, ControlType: "frame", Name: "App"},
			{Index: 1, ControlType: "panel"},
			{Index: 2, ControlType: "panel"},
			{Index: 3, ControlType: "push button", Name: "Close", States: "[has-click-action]"},
			{Index: 4, ControlType: "panel"},
			{Index: 5, ControlType: "panel"},
			{Index: 6, ControlType: "push button", Name: "Close", States: "[has-click-action]"},
		},
	}

	applyWorkingTreeProjection(snapshot, treeViewWorking)

	text := strings.Join(snapshot.TreeLines, "\n")
	for _, branch := range []string{"1 panel", "4 panel"} {
		if !strings.Contains(text, branch) {
			t.Fatalf("duplicate Close target lost branch disambiguator %q:\n%s",
				branch, text)
		}
	}
	for _, transparent := range []string{"2 panel", "5 panel"} {
		if strings.Contains(text, transparent) {
			t.Fatalf("single-chain wrapper %q should remain transparent:\n%s",
				transparent, text)
		}
	}
}

func TestWorkingTreeProjectionCompactsSmallBranchWrapperIntoGroup(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"App\"",
			"\t\t1 panel",
			"\t\t\t2 push button \"Save\" [has-click-action]",
			"\t\t\t3 push button \"Cancel\" [has-click-action]",
		},
		Elements: []elementRecord{
			{Index: 0, ControlType: "frame", Name: "App"},
			{Index: 1, ControlType: "panel"},
			{Index: 2, ControlType: "push button", Name: "Save", States: "[has-click-action]"},
			{Index: 3, ControlType: "push button", Name: "Cancel", States: "[has-click-action]"},
		},
	}

	applyWorkingTreeProjection(snapshot, treeViewWorking)

	text := strings.Join(snapshot.TreeLines, "\n")
	if strings.Contains(text, "1 panel") {
		t.Fatalf("the raw empty layout wrapper leaked into the model view: %#v",
			snapshot.TreeLines)
	}
	if !strings.Contains(text,
		"(layout-group role=panel branches=2 hidden=1)") {
		t.Fatalf("small multi-branch hierarchy was not preserved compactly: %#v",
			snapshot.TreeLines)
	}
	if snapshot.serializerBranches != 1 || snapshot.serializerOmitted != 1 {
		t.Fatalf("unexpected compact group accounting: %#v", snapshot)
	}
}

func TestWorkingTreeProjectionDoesNotRewriteMetadataShapedUserText(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"Page\"",
			"\t\t1 heading \"Policy [actions=clickAncestor] [has-click-action]\" [has-click-action] [desc=\"Keep [actions=showContextMenu] and ] text\"]",
			"\t\t2 static [actions=clickAncestor]: \"Value [actions=clickAncestor] [has-click-action]\"",
		},
		Elements: []elementRecord{
			{Index: 0, ControlType: "frame", Name: "Page"},
			{
				Index:       1,
				ControlType: "heading",
				Name:        "Policy [actions=clickAncestor] [has-click-action]",
				States:      "[has-click-action]",
				Description: "Keep [actions=showContextMenu] and ] text",
			},
			{
				Index:       2,
				ControlType: "static",
				Value:       "Value [actions=clickAncestor] [has-click-action]",
				Actions:     []string{"clickAncestor"},
			},
		},
	}

	applyWorkingTreeProjection(snapshot, treeViewWorking)

	text := strings.Join(snapshot.TreeLines, "\n")
	for _, userText := range []string{
		`Policy [actions=clickAncestor] [has-click-action]`,
		`desc="Keep [actions=showContextMenu] and ] text"`,
		`Value [actions=clickAncestor] [has-click-action]`,
	} {
		if !strings.Contains(text, userText) {
			t.Fatalf("working projection rewrote quoted user text %q:\n%s", userText, text)
		}
	}
	if strings.Contains(linesByIndex(snapshot)[1], `"] [has-click-action]`) {
		t.Fatalf("heading retained the top-level click marker:\n%s", text)
	}
	if got := linesByIndex(snapshot)[2]; got !=
		`		2 static: "Value [actions=clickAncestor] [has-click-action]"` {
		t.Fatalf("static metadata cleanup changed user text or spacing: %q", got)
	}
}

func TestWorkingClickPredicateMatchesCanonicalState(t *testing.T) {
	predicates, err := parsePredicates("[click]")
	if err != nil {
		t.Fatalf("working click predicate did not parse: %v", err)
	}
	record := &elementRecord{
		ControlType: "push button",
		Name:        "Save",
		States:      "[focused has-click-action]",
	}
	if !recordMatchesPredicates(record, predicates) {
		t.Fatal("[click] must match the canonical has-click-action state")
	}
	if _, err := parsePredicates("[has-click-action]"); err == nil {
		t.Fatal("legacy has-click-action selector must be rejected")
	}
}

func TestWorkingTreeCompactsActionsAndTableCoordinates(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"Sheet\"",
			"\t\t1 table cell A1 (R0C0) Revenue [actions=edit,jump]",
		},
		Elements: []elementRecord{
			{Index: 0, ControlType: "frame", Name: "Sheet"},
			{
				Index:       1,
				ControlType: "table cell",
				Name:        "A1",
				Actions:     []string{"edit", "jump"},
			},
		},
	}

	applyWorkingTreeProjection(snapshot, treeViewWorking)

	line := linesByIndex(snapshot)[1]
	if strings.Contains(line, "(R0C0)") {
		t.Fatalf("working table line retained duplicate zero-based coordinates: %q", line)
	}
	if !strings.Contains(line, "A1") || !strings.Contains(line, "[a=edit,jump]") {
		t.Fatalf("working table line lost canonical address or actions: %q", line)
	}
	raw := rawLinesByIndex(snapshot)[1]
	if !strings.Contains(raw, "(R0C0)") ||
		!strings.Contains(raw, "[actions=edit,jump]") {
		t.Fatalf("full diagnostic line was modified: %q", raw)
	}

	record, err := lookupElement(snapshot, `table cell "A1" [a=edit]`)
	if err != nil || record.Index != 1 {
		t.Fatalf("compact action selector failed: %v %#v", err, record)
	}
	if _, err := lookupElement(snapshot, `table cell "A1" [actions=jump]`); err == nil {
		t.Fatal("legacy actions selector must be rejected")
	}
}

func TestWorkingTreeProjectionRemovesDuplicateTextContributions(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"Page\"",
			"\t\t1 paragraph \"Download the app ahead of your visit\"",
			"\t\t\t2 static \"Download the app\" [actions=clickAncestor]",
			"\t\t\t3 static \"ahead of your visit\" [actions=clickAncestor]",
		},
		Elements: []elementRecord{
			{Index: 0, ControlType: "frame", Name: "Page"},
			{Index: 1, ControlType: "paragraph", Name: "Download the app ahead of your visit"},
			{Index: 2, ControlType: "static", Name: "Download the app", Actions: []string{"clickAncestor"}},
			{Index: 3, ControlType: "static", Name: "ahead of your visit", Actions: []string{"clickAncestor"}},
		},
	}

	applyWorkingTreeProjection(snapshot, treeViewWorking)

	text := strings.Join(snapshot.TreeLines, "\n")
	if strings.Contains(text, "2 static") || strings.Contains(text, "3 static") {
		t.Fatalf("duplicate text contributions remain:\n%s", text)
	}
	if !strings.Contains(text, `1 paragraph "Download the app ahead of your visit"`) {
		t.Fatalf("aggregate text was lost:\n%s", text)
	}
}

func TestWorkingTreeProjectionDoesNotDeleteSubstringLookalikes(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"Page\"",
			"\t\t1 paragraph \"10 foobar\"",
			"\t\t\t2 static \"1\"",
			"\t\t\t3 static \"foo\"",
		},
		Elements: []elementRecord{
			{Index: 0, ControlType: "frame", Name: "Page"},
			{Index: 1, ControlType: "paragraph", Name: "10 foobar"},
			{Index: 2, ControlType: "static", Name: "1"},
			{Index: 3, ControlType: "static", Name: "foo"},
		},
	}

	applyWorkingTreeProjection(snapshot, treeViewWorking)

	text := strings.Join(snapshot.TreeLines, "\n")
	if !strings.Contains(text, `2 static "1"`) ||
		!strings.Contains(text, `3 static "foo"`) {
		t.Fatalf("substring lookalikes were incorrectly deduplicated:\n%s", text)
	}
}

func TestWorkingTreeProjectionChecksHigherAncestorsForDuplicateText(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"Page\"",
			"\t\t1 paragraph \"Download the app ahead of your visit\"",
			"\t\t\t2 section \"wrapper\"",
			"\t\t\t\t3 static \"Download the app\"",
		},
		Elements: []elementRecord{
			{Index: 0, ControlType: "frame", Name: "Page"},
			{
				Index: 1, ControlType: "paragraph",
				Name: "Download the app ahead of your visit",
			},
			{Index: 2, ControlType: "section", Name: "wrapper"},
			{Index: 3, ControlType: "static", Name: "Download the app"},
		},
	}

	applyWorkingTreeProjection(snapshot, treeViewWorking)

	if strings.Contains(
		strings.Join(snapshot.TreeLines, "\n"),
		`3 static "Download the app"`,
	) {
		t.Fatal("a higher ancestor's duplicate fragment should be removed")
	}
}

func TestWorkingTreeProjectionDropsReconstructableStructuralValue(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"Page\"",
			"\t\t1 section: \"Alpha Beta\"",
			"\t\t\t2 static \"Alpha\"",
			"\t\t\t3 static \"Beta\"",
		},
		Elements: []elementRecord{
			{Index: 0, ControlType: "frame", Name: "Page"},
			{Index: 1, ControlType: "section", Value: "Alpha Beta"},
			{Index: 2, ControlType: "static", Name: "Alpha"},
			{Index: 3, ControlType: "static", Name: "Beta"},
		},
	}

	applyWorkingTreeProjection(snapshot, treeViewWorking)

	text := strings.Join(snapshot.TreeLines, "\n")
	if strings.Contains(text, "1 section") {
		t.Fatalf("reconstructable structural aggregate remains:\n%s", text)
	}
	if !strings.Contains(text, `2 static "Alpha"`) ||
		!strings.Contains(text, `3 static "Beta"`) {
		t.Fatalf("leaf text was lost:\n%s", text)
	}
}

func TestWorkingTreeProjectionPreservesMeaningfulPunctuation(t *testing.T) {
	for _, test := range []struct {
		parent string
		child  string
	}{
		{parent: "-1.5", child: "15"},
		{parent: "A/B", child: "A B"},
		{parent: "foo.bar", child: "foo bar"},
	} {
		snapshot := &appSnapshot{
			TreeLines: []string{
				"\t0 frame \"Page\"",
				fmt.Sprintf("\t\t1 section: %q", test.parent),
				fmt.Sprintf("\t\t\t2 static %q", test.child),
			},
			Elements: []elementRecord{
				{Index: 0, ControlType: "frame", Name: "Page"},
				{Index: 1, ControlType: "section", Value: test.parent},
				{Index: 2, ControlType: "static", Name: test.child},
			},
		}

		applyWorkingTreeProjection(snapshot, treeViewWorking)

		if !strings.Contains(strings.Join(snapshot.TreeLines, "\n"), "1 section") {
			t.Fatalf(
				"punctuation-bearing provider value %q was treated as %q:\n%s",
				test.parent,
				test.child,
				strings.Join(snapshot.TreeLines, "\n"),
			)
		}
	}
}

func TestWorkingTreeProjectionKeepsProviderOnlyStructuralValue(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"Page\"",
			"\t\t1 section: \"Provider-only description\"",
			"\t\t\t2 push button \"Continue\" [has-click-action]",
		},
		Elements: []elementRecord{
			{Index: 0, ControlType: "frame", Name: "Page"},
			{Index: 1, ControlType: "section", Value: "Provider-only description"},
			{Index: 2, ControlType: "push button", Name: "Continue", States: "[has-click-action]"},
		},
	}

	applyWorkingTreeProjection(snapshot, treeViewWorking)

	if !strings.Contains(strings.Join(snapshot.TreeLines, "\n"), "1 section") {
		t.Fatal("provider-only structural text must remain visible")
	}
}

func TestWorkingTreeProjectionSummarizesLargeBranchGaps(t *testing.T) {
	lines := []string{"\t0 frame \"Page\"", "\t\t1 section [actions=clickAncestor]"}
	elements := []elementRecord{
		{Index: 0, ControlType: "frame", Name: "Page"},
		{Index: 1, ControlType: "section", Actions: []string{"clickAncestor"}},
	}
	next := 2
	for branch := 0; branch < 2; branch++ {
		indent := 3
		for level := 0; level < 8; level++ {
			lines = append(lines,
				strings.Repeat("\t", indent)+fmt.Sprintf(
					"%d section [actions=clickAncestor]", next))
			elements = append(elements, elementRecord{
				Index: next, ControlType: "section",
				Actions: []string{"clickAncestor"},
			})
			next++
			indent++
		}
		lines = append(lines,
			strings.Repeat("\t", indent)+fmt.Sprintf(
				"%d push button \"Action %d\" [has-click-action]", next, branch))
		elements = append(elements, elementRecord{
			Index: next, ControlType: "push button",
			Name:   fmt.Sprintf("Action %d", branch),
			States: "[has-click-action]",
		})
		next++
	}
	snapshot := &appSnapshot{TreeLines: lines, Elements: elements}

	applyWorkingTreeProjection(snapshot, treeViewWorking)

	text := strings.Join(snapshot.TreeLines, "\n")
	if !strings.Contains(text,
		"(layout-group role=section branches=2 hidden=17)") {
		t.Fatalf("large branch gap was not summarized:\n%s", text)
	}
	if snapshot.serializerBranches != 1 || snapshot.serializerOmitted != 17 {
		t.Fatalf("unexpected omission ledger: %#v", snapshot)
	}
}

func TestFullTreeViewPreservesRawProviderProjection(t *testing.T) {
	lines := []string{
		"\t0 frame \"Editor\"",
		"\t\t1 section [actions=clickAncestor]",
	}
	snapshot := &appSnapshot{
		TreeLines: append([]string(nil), lines...),
		Elements: []elementRecord{
			{Index: 0, ControlType: "frame", Name: "Editor"},
			{Index: 1, ControlType: "section", Actions: []string{"clickAncestor"}},
		},
	}

	applyWorkingTreeProjection(snapshot, treeViewFull)

	if !reflect.DeepEqual(snapshot.TreeLines, lines) ||
		snapshot.serializerOmitted != 0 {
		t.Fatalf("full view was modified: %#v", snapshot)
	}
}

func TestContentScopeKeepsOneTopLevelContentSubtree(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"Browser\"",
			"\t\t1 tool bar \"Navigation\"",
			"\t\t\t2 push button \"Reload\" [has-click-action]",
			"\t\t3 document web \"Example\"",
			"\t\t\t4 heading \"Welcome\"",
			"\t\t\t5 document web \"Embedded\"",
			"\t\t\t\t6 link \"Continue\" [has-click-action]",
		},
		Elements: []elementRecord{
			{Index: 0, RuntimeID: []int{}, ControlType: "frame", Name: "Browser"},
			{Index: 1, RuntimeID: []int{0}, ControlType: "tool bar", Name: "Navigation"},
			{Index: 2, RuntimeID: []int{0, 0}, ControlType: "push button", Name: "Reload"},
			{
				Index: 3, RuntimeID: []int{1},
				ControlType:          "document web",
				LocalizedControlType: "documento web",
				Name:                 "Example",
			},
			{Index: 4, RuntimeID: []int{1, 0}, ControlType: "heading", Name: "Welcome"},
			{Index: 5, RuntimeID: []int{1, 1}, ControlType: "document web", Name: "Embedded"},
			{Index: 6, RuntimeID: []int{1, 1, 0}, ControlType: "link", Name: "Continue"},
		},
	}

	if err := applyA11yScope(snapshot, a11yScopeContent); err != nil {
		t.Fatalf("content scope failed: %v", err)
	}
	if snapshot.scopeRoot == nil || snapshot.scopeRoot.Index != 3 {
		t.Fatalf("selected content root = %#v, want index 3", snapshot.scopeRoot)
	}
	if got := scopedCanonicalElementCount(snapshot); got != 4 {
		t.Fatalf("scoped canonical count = %d, want 4", got)
	}
	text := typedTreeTextForTest(t, snapshot)
	for _, absent := range []string{"Navigation", "Reload", "0 frame"} {
		if strings.Contains(text, absent) {
			t.Fatalf("window chrome leaked into content scope via %q:\n%s", absent, text)
		}
	}
	for _, present := range []string{
		`document "Example"`,
		`heading "Welcome"`,
		`document "Embedded"`,
		`link "Continue"`,
	} {
		if !strings.Contains(text, present) {
			t.Fatalf("content subtree lost %q:\n%s", present, text)
		}
	}
	if len(snapshot.Elements) != 7 {
		t.Fatal("content scope must preserve the complete canonical action records")
	}
}

func TestContentScopeUsesFocusToDisambiguateTopLevelRoots(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"Split\"",
			"\t\t1 document web \"Left\"",
			"\t\t\t2 heading \"Left title\" [focused]",
			"\t\t3 document web \"Right\"",
			"\t\t\t4 heading \"Right title\"",
		},
		Elements: []elementRecord{
			{Index: 0, RuntimeID: []int{}, ControlType: "frame", Name: "Split"},
			{Index: 1, RuntimeID: []int{0}, ControlType: "document web", Name: "Left"},
			{Index: 2, RuntimeID: []int{0, 0}, ControlType: "heading", Name: "Left title", States: "[focused]"},
			{Index: 3, RuntimeID: []int{1}, ControlType: "document web", Name: "Right"},
			{Index: 4, RuntimeID: []int{1, 0}, ControlType: "heading", Name: "Right title"},
		},
	}

	if err := applyA11yScope(snapshot, a11yScopeContent); err != nil {
		t.Fatalf("focused content scope failed: %v", err)
	}
	if snapshot.scopeRoot == nil || snapshot.scopeRoot.Index != 1 {
		t.Fatalf("focused root = %#v, want left document", snapshot.scopeRoot)
	}
	if strings.Contains(typedTreeTextForTest(t, snapshot), "Right") {
		t.Fatal("unfocused sibling content root leaked into scoped tree")
	}
}

func TestContentScopeFailsClosedWithoutUniqueRoot(t *testing.T) {
	for name, snapshot := range map[string]*appSnapshot{
		"none": {
			TreeLines: []string{"\t0 frame \"Settings\"", "\t\t1 push button \"OK\""},
			Elements: []elementRecord{
				{Index: 0, RuntimeID: []int{}, ControlType: "frame", Name: "Settings"},
				{Index: 1, RuntimeID: []int{0}, ControlType: "push button", Name: "OK"},
			},
		},
		"ambiguous": {
			TreeLines: []string{
				"\t0 frame \"Split\"",
				"\t\t1 document web \"Left\"",
				"\t\t2 document web \"Right\"",
			},
			Elements: []elementRecord{
				{Index: 0, RuntimeID: []int{}, ControlType: "frame", Name: "Split"},
				{Index: 1, RuntimeID: []int{0}, ControlType: "document web", Name: "Left"},
				{Index: 2, RuntimeID: []int{1}, ControlType: "document web", Name: "Right"},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := applyA11yScope(snapshot, a11yScopeContent)
			if err == nil {
				t.Fatal("content scope must fail closed")
			}
			if !strings.Contains(err.Error(), `scope="content"`) ||
				!strings.Contains(err.Error(), `scope="window"`) {
				t.Fatalf("scope error is not actionable: %v", err)
			}
		})
	}
}

func TestContentScopeAndRootIgnoreLegacyTreeBudget(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{`\t0 frame "Browser"`},
		Elements: []elementRecord{
			{Index: 0, RuntimeID: []int{}, ControlType: "frame", Name: "Browser"},
			{Index: 1, RuntimeID: []int{0}, ControlType: "document web", Name: "Page"},
			{Index: 2, RuntimeID: []int{0, 0}, ControlType: "heading", Name: "Deep target"},
		},
	}
	if err := applyA11yScope(snapshot, a11yScopeContent); err != nil {
		t.Fatalf("canonical content scope depended on rendered text: %v", err)
	}
	if err := applyObservationRoot(snapshot, map[string]any{
		"selector": map[string]any{
			"role": "heading", "name": "Deep target",
		},
	}); err != nil {
		t.Fatalf("canonical root depended on rendered text: %v", err)
	}
	text := typedTreeTextForTest(t, snapshot)
	if !strings.Contains(text, `heading "Deep target"`) {
		t.Fatalf("typed root missing despite canonical record:\n%s", text)
	}
}

func TestFindAcceptsCanonicalModelRole(t *testing.T) {
	record := &elementRecord{
		ControlType:          "entry",
		LocalizedControlType: "entrada",
		Name:                 "Name",
	}
	if !recordMatchesFind(
		record, "textbox", "Name", "", "", true, false,
	) {
		t.Fatal("find rejected canonical textbox role for provider entry")
	}
}

func TestContentScopeHasIndependentCacheIdentityAndDoesNotPersist(t *testing.T) {
	window := &appSnapshot{
		WindowHandle:    "0x1",
		publicWindowRef: "w1",
		SnapshotID:      "same",
		App:             appDescriptor{Name: "App", PID: 7},
		projection: snapshotProjection{
			View:  treeViewWorking,
			Scope: a11yScopeWindow,
		},
	}
	content := *window
	content.projection.Scope = a11yScopeContent
	if deliveredTreeKey(window) == deliveredTreeKey(&content) ||
		deliveredSnapshotKey(window) == deliveredSnapshotKey(&content) {
		t.Fatal("window and content projections must not share delivery caches")
	}

	request := axServiceRequest{Scope: a11yScopeContent}
	applyProjection(&request, snapshotProjection{
		View:  treeViewFull,
		Scope: a11yScopeWindow,
	})
	if request.Scope != a11yScopeContent {
		t.Fatal("a cached projection must not overwrite request-local content scope")
	}
}

func TestContentRevisionIgnoresWindowWideElementIndices(t *testing.T) {
	first := contentRevisionForLines([]string{
		"\t3 document web \"Example\"",
		"\t\t4 heading \"Welcome\" [focused]",
	}, "selected")
	second := contentRevisionForLines([]string{
		"\t21 document web \"Example\"",
		"\t\t42 heading \"Welcome\" [focused]",
	}, "selected")
	if first == "" || first != second || !validContentRevision(first) {
		t.Fatalf("index-independent revisions differ: %q %q", first, second)
	}
	changed := contentRevisionForLines([]string{
		"\t21 document web \"Example\"",
		"\t\t42 heading \"Results\" [focused]",
	}, "selected")
	if changed == first {
		t.Fatal("semantic content change must change the content revision")
	}
	selectionChanged := contentRevisionForLines([]string{
		"\t21 document web \"Example\"",
		"\t\t42 heading \"Welcome\" [focused]",
	}, "different")
	if selectionChanged == first {
		t.Fatal("selected text change must change the content revision")
	}
}

func TestContentRevisionDrivesContentTreeDeliveryIdentity(t *testing.T) {
	first := &appSnapshot{
		WindowHandle:    "0x1",
		publicWindowRef: "w1",
		SnapshotID:      "full-a",
		contentRevision: "c1:0123456789abcdef01234567",
		projection: snapshotProjection{
			View:  treeViewWorking,
			Scope: a11yScopeContent,
		},
	}
	second := *first
	second.SnapshotID = "full-b"
	if deliveredTreeKey(first) != deliveredTreeKey(&second) {
		t.Fatal("browser chrome-only snapshot changes must not resend identical content")
	}
	second.contentRevision = "c1:89abcdef0123456701234567"
	if deliveredTreeKey(first) == deliveredTreeKey(&second) {
		t.Fatal("changed scoped content must have a new delivery identity")
	}
}

func TestFindExactMatchingHandlesShortLabels(t *testing.T) {
	records := []elementRecord{
		{ControlType: "check box", Name: "L"},
		{ControlType: "label", Name: "Large"},
		{ControlType: "label", Name: "Sale"},
		{ControlType: "entry", Placeholder: "To"},
		{ControlType: "label", Name: "Tokyo"},
	}
	count := func(name, text string, nameExact, textExact bool) int {
		total := 0
		for index := range records {
			if recordMatchesFind(
				&records[index],
				"",
				name,
				text,
				"",
				nameExact,
				textExact,
			) {
				total++
			}
		}
		return total
	}
	if got := count("L", "", false, false); got != 3 {
		t.Fatalf("substring name matches=%d want 3", got)
	}
	if got := count("L", "", true, false); got != 1 {
		t.Fatalf("exact name matches=%d want 1", got)
	}
	if got := count("", "To", false, false); got != 2 {
		t.Fatalf("substring text matches=%d want 2", got)
	}
	if got := count("", "To", false, true); got != 1 {
		t.Fatalf("exact text matches=%d want 1", got)
	}
}

func TestScopedLookupExcludesApplicationChrome(t *testing.T) {
	snapshot := &appSnapshot{
		scope: a11yScopeContent,
		scopeRoot: &elementRecord{
			Index: 2, RuntimeID: []int{1}, ControlType: "document web",
		},
		scopedCanonicalCount: 2,
		Elements: []elementRecord{
			{
				Index: 1, RuntimeID: []int{0},
				ControlType: "push button", Name: "Search",
			},
			{
				Index: 2, RuntimeID: []int{1},
				ControlType: "document web", Name: "Page",
			},
			{
				Index: 3, RuntimeID: []int{1, 0},
				ControlType: "push button", Name: "Search",
			},
		},
	}
	scoped := snapshotForScopedLookup(snapshot)
	if len(scoped.Elements) != 2 {
		t.Fatalf("scoped lookup elements=%d want 2", len(scoped.Elements))
	}
	record, err := lookupElementFor(
		scoped,
		`push button "Search"`,
		"",
		"verify",
	)
	if err != nil || record.Index != 3 {
		t.Fatalf("content-scoped selector did not resolve page control: %#v %v",
			record, err)
	}
}

func TestWorkingTreeProjectionAttachesIndentedNotesByTheirDepth(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"App\"",
			"\t\t1 menu \"File\" [has-click-action]",
			"\t\t\t2 menu item \"Open\" [has-click-action]",
			"\t\t\t(3 items not listed; open the menu)",
		},
		Elements: []elementRecord{
			{Index: 0, ControlType: "frame", Name: "App"},
			{Index: 1, ControlType: "menu", Name: "File", States: "[has-click-action]"},
			{Index: 2, ControlType: "menu item", Name: "Open", States: "[has-click-action]"},
		},
	}

	applyWorkingTreeProjection(snapshot, treeViewWorking)

	text := strings.Join(snapshot.TreeLines, "\n")
	want := "\t\t\t(3 items not listed; open the menu)"
	if !strings.Contains(text, want) {
		t.Fatalf("menu note was attached to the wrong hierarchy level:\n%s", text)
	}
}

// 能力必须是**测出来的**，不能按应用名声明。
//
// 这条曾经写成"Browsers are NOT handled by these tools. Chrome 和 Chromium
// 由另一个控制平面（Playwright/browser-use over CDP）驱动，别对它们调
// get_app_state / click / type_text"。它是整份文档里唯一一句**在 agent 看之前
// 就替它下结论**的话，而且是按应用名下的——正是本仓库在代码里明令禁止的那种
// 分支，只不过写在散文里。
//
// v16 54mini 的实测把它判死了：
//
//   - 70 道题里 29 道**一个动作都没有**（占失败 60 道的 48%）。其中 14 道的
//     结束语是"这些工具不处理浏览器 / 我在这里无法驱动它"——直接复述这段话；
//     另外 14 道连一次工具调用都没有，把 GUI 任务当成信息问答答掉了。
//   - 而 5 道成功的 chrome 任务**全部**是直接 get_app_state + set_value/click
//     驱动 Chrome 完成的，快照回来 600+ 个元素、页面内容俱全。也就是说
//     "a11y 树默认关闭"这个前提在本部署里是假的，照做的放弃，不照做的做成了。
//   - 它承诺的那个"另一个控制平面"在会话里根本不存在：轨迹显示 agent 反复
//     搜索 browser-use / playwright 工具，一个也没找到，然后收工。
//
// 所以规则反过来：**不按名字排除任何应用**，可达性由 list_apps 的输入能力标记
// 与快照的 CONTENT PLANE MISSING 当场测出来，并且内容不可达只吃掉那部分内容的
// a11y 通道，不吃掉整个应用（外壳仍有 element_index，内容仍可走 gui）。
func TestCapabilityIsMeasuredNeverDeclaredByAppName(t *testing.T) {
	if !strings.Contains(
		serverInstructions,
		"content absent from the accessibility tree",
	) {
		t.Fatal("instructions must describe accessibility reachability without app exclusions")
	}
	// 回归护栏：范围声明里不得再出现具体应用/驱动名。
	// LibreOffice / gedit 之类仍然允许，它们出现在**实测数字**的出处里
	// （"a file manager tree ~2100"、"gedit ~350"），那是证据不是分支。
	for _, banned := range []string{
		"Browsers are NOT handled",
		"Chrome", "Chromium", "Playwright", "browser-use", "CDP",
		"Thunderbird", "VLC",
	} {
		if strings.Contains(serverInstructions, banned) {
			t.Fatalf("server instructions must not scope capability by app name: found %q", banned)
		}
	}
}

// 零动作失败的另一半：agent 从未意识到自己接在一台**正在运行的桌面**上。
// 14/29 条轨迹一次工具都没调，把"给我看某某东西"当成检索题答掉并宣布完成。
// 开场白因此必须先陈述环境事实（会话已挂在一台开着应用的桌面上、只有这些
// 工具能看见它），再讲工具怎么用。
func TestInstructionsOpenWithTheLiveSessionFact(t *testing.T) {
	if !strings.HasPrefix(serverInstructions, "Control the already-running desktop") {
		t.Fatalf("instructions must open with the live desktop fact: %q", serverInstructions)
	}
	for _, fragment := range []string{"AX is", "GUI is"} {
		if !strings.Contains(serverInstructions, fragment) {
			t.Fatalf("instructions must name both control channels: missing %q", fragment)
		}
	}
}

// 观测的两条通道是**互补**，不是主备。
//
// 这条曾经写成"两条独立轨道、截图是 fallback"，`build_snapshot` 也默认不带图，
// 理由是 a11y 轨不该顺带付截图的钱。成本数字至今成立，**但"独立"这个前提被
// 实测推翻了**（手动跑 OSWorld Impress 任务，见 docs/exec-plans/active/
// 20260730-impress-manual-run-findings.md）：
//
//   - 右对齐与保存都真的生效了，a11y 树却字节不变，于是两个动作都被判成
//     "送达但被忽略"——**两次假阴性，都是一张截图一眼判掉的**。
//   - 反过来，"四个没有名字的 spin button 里哪个是 Position Y" 靠截图只能凭
//     标签的空间邻近去猜，靠树的 description 是确定的。
//
// 所以树给**可操作性**，截图给**可观察性**；视觉变化仍需单独断言。
// #29 的 A/B 靠 COMPUTER_USE_A11Y_SCREENSHOTS=0 关掉再比。
func TestObservationChannelsAreComplementary(t *testing.T) {
	gui := findToolDefinition(t, "GUI")
	if !strings.Contains(gui.Description, "Full-desktop") {
		t.Fatal("gui must expose the independent full-desktop track")
	}
	if !strings.Contains(serverInstructions, "two complementary tools") {
		t.Fatal("server instructions 必须把两条通道讲成互补而不是主备")
	}
	for _, fragment := range []string{
		"AX is app-scoped semantic observation and action",
		"GUI is full-desktop visual observation and absolute-pixel input",
		"evidence, not proof",
	} {
		if !strings.Contains(serverInstructions, fragment) {
			t.Fatalf("complementary evidence boundary missing %q", fragment)
		}
	}
	if !strings.Contains(linuxRuntimeScript, "def a11y_screenshots_enabled():") {
		t.Fatal("截图策略必须集中在一个可开关的判据里，供 #29 做 A/B")
	}
	if !strings.Contains(linuxRuntimeScript, "Every call") &&
		!strings.Contains(gui.Description, "returns one final screenshot") {
		t.Fatal("gui must always return a screenshot")
	}
}

func TestLinuxClickMethodSafetyAndPlatformSupport(t *testing.T) {
	service := newService()

	result := service.click("Text Editor", "7", "", nil, nil, 1, "left", "app_post", observation{}, settlePlan{})
	if !result.IsError || result.Content[0].Text != "click_method 'app_post' is not supported on Linux" {
		t.Fatalf("app_post click result = %#v", result)
	}

	result = service.click("Text Editor", "7", "", nil, nil, 1, "left", "sky_click", observation{}, settlePlan{})
	if !result.IsError || result.Content[0].Text != "click_method 'sky_click' is not supported on Linux" {
		t.Fatalf("sky_click result = %#v", result)
	}

	// click 不再接受 x/y——坐标点击是另一条通道，另一个工具。
	x, y := 10.0, 20.0
	result = service.click("Text Editor", "7", "", &x, &y, 1, "left", "auto", observation{}, settlePlan{})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "use GUI") {
		t.Fatalf("click 收到 x/y 应当指向 gui: %#v", result)
	}
	result = service.click("Text Editor", "", "", nil, nil, 1, "left", "auto", observation{}, settlePlan{})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "click requires target.ref") {
		t.Fatalf("click 缺 target.ref 应当明确报错: %#v", result)
	}

	// 原来的 COMPUTER_USE_ALLOW_GLOBAL_POINTER_FALLBACKS 闸门整个禁掉了
	// 无锚点坐标点击，代价是 GUI 通道默认不可用。现在 GUI 通道使用全桌面截图
	// 的屏幕绝对坐标：原样发送，越界报错，既不加窗口偏移也不静默改点。
	if strings.Contains(serverInstructions, "COMPUTER_USE_ALLOW_GLOBAL_POINTER_FALLBACKS") {
		t.Fatal("闸门已移除，instructions 不该再提它")
	}
	for _, fragment := range []string{
		"def screen_absolute_point(",
		"is outside the full desktop",
		"normalized[\"x\"], normalized[\"y\"] = screen_absolute_point(",
	} {
		if !strings.Contains(linuxRuntimeScript, fragment) {
			t.Fatalf("GUI 通道必须原样使用并校验屏幕绝对坐标: 缺 %q", fragment)
		}
	}

	t.Setenv("COMPUTER_USE_ALLOW_GLOBAL_POINTER_FALLBACKS", "yes")
	if !globalPointerFallbacksEnabled() {
		t.Fatal("global pointer authorization should accept yes")
	}
}

func TestCallSequenceStopsAfterFirstToolError(t *testing.T) {
	output, hasError, err := runCallCommand([]string{
		"--calls",
		`[{"tool":"AX","args":{"mode":"invalid"}},{"tool":"GUI","args":{"actions":[]}}]`,
	}, newService())
	if err != nil {
		t.Fatal(err)
	}
	if !hasError {
		t.Fatal("expected hasError")
	}
	items, ok := output.([]map[string]any)
	if !ok {
		t.Fatalf("output type = %T", output)
	}
	if len(items) != 1 {
		t.Fatalf("sequence output count = %d, want 1", len(items))
	}
}

func TestReadArgumentsAcceptsJSONObject(t *testing.T) {
	args, err := readArguments(`{"app":"Text Editor","pages":2}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if args["app"] != "Text Editor" {
		t.Fatalf("app = %v", args["app"])
	}
	if args["pages"].(json.Number).String() != "2" {
		t.Fatalf("pages = %v", args["pages"])
	}
}

func TestElementIndexAcceptsStringAndJSONNumber(t *testing.T) {
	args, err := readArguments(`{"app":"Text Editor","element_index":0}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := optionalElementIndex(args); got != "0" {
		t.Fatalf("numeric element_index = %q, want 0", got)
	}
	if got := optionalElementIndex(map[string]any{"element_index": "14"}); got != "14" {
		t.Fatalf("string element_index = %q, want 14", got)
	}
	if got := optionalElementIndex(map[string]any{"element_index": json.Number("1.5")}); got != "" {
		t.Fatalf("fractional element_index = %q, want empty", got)
	}
}

func TestMCPInitializeResponseContainsToolsCapability(t *testing.T) {
	request := map[string]any{
		"jsonrpc": "2.0",
		"id":      float64(1),
		"method":  "initialize",
		"params":  map[string]any{},
	}
	response := handleMCPRequest(request, newService())
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result: %#v", response)
	}
	capabilities := result["capabilities"].(map[string]any)
	if _, ok := capabilities["tools"]; !ok {
		t.Fatalf("missing tools capability: %#v", capabilities)
	}
	if result["instructions"] != serverInstructions {
		t.Fatal("initialize must return the self-contained static serverInstructions")
	}
}

func TestMCPDefaultsToModelVisibleTextEvidence(t *testing.T) {
	for _, tc := range []struct {
		requested      string
		wantNegotiated string
	}{
		{mcpProtocolLegacy, mcpProtocolLegacy},
		{mcpProtocolStructured, mcpProtocolStructured},
		{"2025-11-25", mcpProtocolStructured},
	} {
		svc := newService()
		init := handleMCPRequest(map[string]any{
			"jsonrpc": "2.0",
			"id":      float64(1),
			"method":  "initialize",
			"params": map[string]any{
				"protocolVersion": tc.requested,
			},
		}, svc)
		result := init["result"].(map[string]any)
		if result["protocolVersion"] != tc.wantNegotiated {
			t.Fatalf("requested %s negotiated %#v, want %s",
				tc.requested, result["protocolVersion"], tc.wantNegotiated)
		}

		call := handleMCPRequest(map[string]any{
			"jsonrpc": "2.0",
			"id":      float64(2),
			"method":  "tools/call",
			"params": map[string]any{
				"name": "AX",
				"arguments": map[string]any{
					"mode": "invalid",
				},
			},
		}, svc)
		callResult := call["result"].(toolCallResult)
		if callResult.StructuredContent != nil ||
			len(callResult.Content) == 0 {
			t.Fatalf("requested %s must receive text evidence by default: %#v",
				tc.requested, callResult)
		}
	}
}

func TestMCPStructuredContentIsExplicitOptIn(t *testing.T) {
	t.Setenv("COMPUTER_USE_STRUCTURED_CONTENT", "1")
	svc := newService()
	handleMCPRequest(map[string]any{
		"jsonrpc": "2.0",
		"id":      float64(1),
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": mcpProtocolStructured,
		},
	}, svc)
	call := handleMCPRequest(map[string]any{
		"jsonrpc": "2.0",
		"id":      float64(2),
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "click",
			"arguments": map[string]any{"element_index": "1"},
		},
	}, svc)
	result := call["result"].(toolCallResult)
	if result.StructuredContent == nil {
		t.Fatalf("2025-06-18 opt-in must preserve structuredContent: %#v", result)
	}
	for _, item := range result.Content {
		if strings.HasPrefix(item.Text, "[tool-evidence] ") {
			t.Fatalf("opt-in structured clients should not receive duplicate text evidence: %#v", result)
		}
	}
}

func TestCLIHelpMentionsSupportedRuntimes(t *testing.T) {
	var out bytes.Buffer
	if err := runCLI([]string{"--help"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Linux, macOS, and Windows") {
		t.Fatalf("help text did not mention supported runtimes:\n%s", out.String())
	}
	if strings.Contains(out.String(), "open-computer-use") {
		t.Fatalf("help text leaked the retired upstream command:\n%s", out.String())
	}
}

func TestMCPInitializeUsesProductOwnedServerName(t *testing.T) {
	response := handleMCPRequest(map[string]any{
		"jsonrpc": "2.0",
		"id":      float64(1),
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": mcpProtocolStructured,
		},
	}, newService())
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize result has unexpected type: %#v", response["result"])
	}
	serverInfo, ok := result["serverInfo"].(map[string]any)
	if !ok {
		t.Fatalf("initialize result has no serverInfo: %#v", result)
	}
	if got := serverInfo["name"]; got != "computer-use" {
		t.Fatalf("server name = %#v, want computer-use", got)
	}
}

func TestLinuxRuntimeDocumentsATSPIAndFallbackBoundary(t *testing.T) {
	if !strings.Contains(linuxRuntimeScript, "Atspi") {
		t.Fatal("Linux runtime must use AT-SPI")
	}
	if !strings.Contains(linuxRuntimeScript, "generate_mouse_event") {
		t.Fatal("Linux runtime should keep coordinate input explicit and visible in the bridge")
	}
	if !strings.Contains(
		serverInstructions,
		"AX keyboard/pointer fallbacks use global desktop focus, not background input",
	) {
		t.Fatal("MCP instructions must document the Linux background-input boundary")
	}
}

func TestLinuxRuntimeTextLimitSupportsMaxMode(t *testing.T) {
	if !strings.Contains(linuxRuntimeScript, "DEFAULT_TEXT_LIMIT = 500") {
		t.Fatal("Linux runtime should define the shared 500 character text limit")
	}
	if !strings.Contains(linuxRuntimeScript, "text_limit=parse_text_limit(observation.get(\"text_limit\"), DEFAULT_TEXT_LIMIT)") {
		t.Fatal("Linux observe should pass text_limit into snapshot rendering")
	}
	if !strings.Contains(linuxRuntimeScript, "if isinstance(value, str) and value.lower() == \"max\"") {
		t.Fatal("Linux runtime should support max text limit mode")
	}
	if !strings.Contains(linuxRuntimeScript, "max_tree_nodes=positive_int(observation.get(\"max_tree_nodes\"), MAX_ELEMENTS)") {
		t.Fatal("Linux observe should pass max_tree_nodes into snapshot rendering")
	}
	if !strings.Contains(linuxRuntimeScript, "max_tree_depth=positive_int(observation.get(\"max_tree_depth\"), MAX_DEPTH)") {
		t.Fatal("Linux observe should pass max_tree_depth into snapshot rendering")
	}
	if !strings.Contains(linuxRuntimeScript, "text_limit + 1") {
		t.Fatal("Linux default truncation should read one extra character so it can append ellipsis")
	}
}

func TestLinuxRuntimeTreeBudgetDefaultsMatchMacOS(t *testing.T) {
	if !strings.Contains(linuxRuntimeScript, "MAX_ELEMENTS = 1200") {
		t.Fatal("Linux runtime should default to the shared 1200 node tree budget")
	}
	if !strings.Contains(linuxRuntimeScript, "MAX_DEPTH = 64") {
		t.Fatal("Linux runtime should default to the shared 64 level tree depth")
	}
}

func TestLinuxRuntimeEnvironmentDiscoversDesktopSession(t *testing.T) {
	runtimeDir := shortTempDir(t)
	listenUnixSocket(t, filepath.Join(runtimeDir, "bus"))
	listenUnixSocket(t, filepath.Join(runtimeDir, "wayland-0"))

	env := envSliceToMap(linuxRuntimeEnvironmentFrom(
		[]string{"PATH=/usr/bin"},
		os.Getuid(),
		[]map[string]string{{
			"XDG_RUNTIME_DIR":     runtimeDir,
			"DISPLAY":             ":1",
			"XAUTHORITY":          "/tmp/computer-use-xauth",
			"XDG_SESSION_TYPE":    "wayland",
			"XDG_CURRENT_DESKTOP": "GNOME",
		}},
	))

	if got := env["XDG_RUNTIME_DIR"]; got != runtimeDir {
		t.Fatalf("XDG_RUNTIME_DIR = %q, want %q", got, runtimeDir)
	}
	if got, want := env["DBUS_SESSION_BUS_ADDRESS"], "unix:path="+filepath.Join(runtimeDir, "bus"); got != want {
		t.Fatalf("DBUS_SESSION_BUS_ADDRESS = %q, want %q", got, want)
	}
	if got := env["WAYLAND_DISPLAY"]; got != "wayland-0" {
		t.Fatalf("WAYLAND_DISPLAY = %q, want wayland-0", got)
	}
	if got := env["DISPLAY"]; got != ":1" {
		t.Fatalf("DISPLAY = %q, want :1", got)
	}
	if got := env["XDG_CURRENT_DESKTOP"]; got != "GNOME" {
		t.Fatalf("XDG_CURRENT_DESKTOP = %q, want GNOME", got)
	}
}

func TestLinuxRuntimeEnvironmentCanonicalizesRuntimeBus(t *testing.T) {
	runtimeDir := shortTempDir(t)
	listenUnixSocket(t, filepath.Join(runtimeDir, "bus"))

	env := envSliceToMap(linuxRuntimeEnvironmentFrom(
		[]string{
			"XDG_RUNTIME_DIR=" + runtimeDir,
			"DBUS_SESSION_BUS_ADDRESS=unix:path=" + filepath.Join(runtimeDir, "bus") + ",guid=stale",
		},
		os.Getuid(),
		nil,
	))

	if got, want := env["DBUS_SESSION_BUS_ADDRESS"], "unix:path="+filepath.Join(runtimeDir, "bus"); got != want {
		t.Fatalf("DBUS_SESSION_BUS_ADDRESS = %q, want %q", got, want)
	}
}

func listenUnixSocket(t *testing.T, path string) {
	t.Helper()
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen unix socket %s: %v", path, err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		_ = os.Remove(path)
	})
}

func TestLinuxRuntimeSelectsEditableTargetByState(t *testing.T) {
	if !strings.Contains(linuxRuntimeScript, "state_contains(node, Atspi.StateType.EDITABLE)") {
		t.Fatal("Linux runtime must require the EDITABLE state when choosing a text target; interface presence alone matches hidden placeholder widgets")
	}
	if !strings.Contains(linuxRuntimeScript, "if len(focused) == 1:") {
		t.Fatal("Linux runtime should choose exactly one focused editable target")
	}
	if strings.Contains(linuxRuntimeScript, "if len(showing) == 1:") {
		t.Fatal("Linux runtime must not treat visibility as proof of input focus")
	}
	if strings.Contains(linuxRuntimeScript, "return find_first(root, is_editable)") {
		t.Fatal("Linux runtime must not fall back to the first editable-text interface in tree order")
	}
}

func TestLinuxRuntimeVerifiesTextInsertionLanded(t *testing.T) {
	if !strings.Contains(linuxRuntimeScript, "after_text = read_all_text(text_iface)") {
		t.Fatal("Linux runtime should re-read the complete text after insert_text")
	}
	if !strings.Contains(linuxRuntimeScript, "if after_text == expected:") ||
		!strings.Contains(
			linuxRuntimeScript,
			`return "effect_unknown", before, len(after_text), True`,
		) {
		t.Fatal("insert_text success and character growth are insufficient; the exact inserted payload must be confirmed")
	}
	if !strings.Contains(linuxRuntimeScript, "if after == payload:") ||
		!strings.Contains(
			linuxRuntimeScript,
			`if after != before:`+"\n"+`                return "effect_unknown"`,
		) {
		t.Fatal("set_value must confirm the requested value exactly instead of accepting any change")
	}
}

func TestLinuxRuntimeTypesAtCaretAndReplacesSelection(t *testing.T) {
	if !strings.Contains(linuxRuntimeScript, "def text_insertion_point(text_iface):") {
		t.Fatal("Linux runtime should resolve an insertion point instead of always appending")
	}
	if !strings.Contains(linuxRuntimeScript, "Atspi.Text.get_caret_offset(text_iface)") {
		t.Fatal("type_text should insert at the caret so it behaves like real typing")
	}
	if !strings.Contains(linuxRuntimeScript, "Atspi.EditableText.delete_text(") ||
		!strings.Contains(linuxRuntimeScript, "editable, selection[0], selection[1]") {
		t.Fatal("type_text should replace a non-empty selection, the way typing does")
	}
}

func TestLinuxRuntimeRejectsOffscreenSentinelCoordinates(t *testing.T) {
	if !strings.Contains(linuxRuntimeScript, "abs(rect.x) > MAX_SANE_EXTENT or abs(rect.y) > MAX_SANE_EXTENT") {
		t.Fatal("unrendered widgets report INT_MIN origins; coordinate actions must not target them")
	}
	if !strings.Contains(linuxRuntimeScript, "MAX_SANE_EXTENT = 100000") {
		t.Fatal("Linux runtime should define the sane extent bound shared by size and origin checks")
	}
}

func TestLinuxRuntimeReportsExecutionPath(t *testing.T) {
	if !strings.Contains(linuxRuntimeScript, "UNVERIFIED_SYNTHESIS") {
		t.Fatal("Linux runtime should mark synthesis-based actions as unverified")
	}
	// 语义调用同样不能当成"生效"的证据：实测 Nautilus / GIMP / VLC 三个应用的
	// AT-SPI 动作都会返回成功却什么都不做。工具必须把这一点说出来，
	// 否则 agent 会据此推进下一步，而真实界面还停在原地。
	if !strings.Contains(linuxRuntimeScript, "UNVERIFIED_SEMANTIC") {
		t.Fatal("Linux runtime should mark semantic AT-SPI actions as unverified too")
	}
	if !strings.Contains(linuxRuntimeScript, "that is not evidence the action took effect") {
		t.Fatal("semantic action notes must state that toolkit acceptance is not proof of effect")
	}
	for _, note := range []string{
		"Invoked the element's AT-SPI accessibility action. ",
		"Wrote the text through the AT-SPI editable-text API and read it back ",
		"fell back to ",
	} {
		if !strings.Contains(linuxRuntimeScript, note) {
			t.Fatalf("action notes must distinguish verified from best-effort paths: missing %q", note)
		}
	}
	if !strings.Contains(linuxRuntimeScript, "response[\"notes\"] = notes") {
		t.Fatal("Linux runtime should return the action notes to the Go bridge")
	}
}

func TestIncrementalTreeOnlyWhenSafeAndCheaper(t *testing.T) {
	// 这个测试原来的判据是**前后行数必须完全相同**，理由写着"结构一变
	// element_index 就永久重排，行数不变即无结构变化"。判断没错，但它是个
	// 过强的代理条件：菜单展开、对话框弹出、列表增删一行都会改变行数，
	// 而那恰恰是最常见的动作。实测 67 条轨迹、753 次动作，增量只命中 26.6%，
	// 于是"动作的副产品"吃掉了 87% 的观测开销。
	//
	// 真正的安全不变量比行数细一层：**下标就印在行文本里**（StableIndexer 按
	// 树内路径 + role + name 发号，见 runtime.py index_for）。所以
	//
	//     一行逐字节相同  ⇒  路径、role、name 都没变  ⇒  下标也没变
	//
	// 反过来，插入一个节点会让后面兄弟的路径全变、领到新下标，
	// 于是**它们的行文本也变了**，必然出现在 diff 里——不会被悄悄漏掉。
	// 而那种大范围重排会让变化行数超过三分之一，按下面的比例判据自动退回全量。
	// 也就是说这套机制在"索引重排"这件事上是自纠的。
	base := func(lines ...string) *appSnapshot { return &appSnapshot{TreeLines: lines} }

	unchanged, ok := incrementalTree(base("a", "b", "c"), base("a", "b", "c"))
	if !ok || len(unchanged) != 1 || !strings.Contains(unchanged[0], "0 changes") {
		t.Fatalf("完全相同的树应只返回零变化摘要，得到 ok=%v diff=%v", ok, unchanged)
	}

	diff, ok := incrementalTree(base("a", "b", "c", "d", "e", "f"),
		base("a", "b", "X", "d", "e", "f"))
	if !ok {
		t.Fatal("只有一行变化时应该走增量")
	}
	if len(diff) != 3 || diff[1] != "-c" || diff[2] != "+X" {
		t.Fatalf("一行被替换时应给出 -旧 +新，得到 %v", diff)
	}
	if !strings.Contains(diff[0], "Existing rN refs remain bound") {
		t.Fatal("表头必须说明未失效的短 ref 仍保持绑定")
	}

	// 纯插入：这正是老判据挡掉、而实际最常见的那一类。
	diff, ok = incrementalTree(base("a", "b", "c", "d", "e", "f", "g"),
		base("a", "b", "NEW", "c", "d", "e", "f", "g"))
	if !ok {
		t.Fatal("插入一行应该走增量——老判据在这里退回全量，占了绝大多数动作")
	}
	if len(diff) != 2 || diff[1] != "+NEW" {
		t.Fatalf("纯插入应只给出 +新行，得到 %v", diff)
	}

	// 纯删除。
	diff, ok = incrementalTree(base("a", "b", "GONE", "c", "d", "e", "f"),
		base("a", "b", "c", "d", "e", "f"))
	if !ok {
		t.Fatal("删除一行应该走增量")
	}
	if len(diff) != 2 || diff[1] != "-GONE" {
		t.Fatalf("纯删除应只给出 -旧行，得到 %v", diff)
	}

	if _, ok := incrementalTree(base("a", "b", "c"), base("X", "Y", "c")); ok {
		t.Fatal("变化占比过大时增量不划算，应回退全量")
	}
	if _, ok := incrementalTree(nil, base("a")); ok {
		t.Fatal("没有前一份快照时不能走增量")
	}
}

func TestIncrementalTreeRejectsRootOrSerializerViewChanges(t *testing.T) {
	before := &appSnapshot{
		WindowHandle: "pid1/win0/frame",
		TreeLines:    []string{"a", "b"},
		projection:   snapshotProjection{View: treeViewWorking},
	}
	otherRoot := &appSnapshot{
		WindowHandle: "pid1/win1/frame",
		TreeLines:    []string{"a", "b"},
		projection:   snapshotProjection{View: treeViewWorking},
	}
	if _, ok := incrementalTree(before, otherRoot); ok {
		t.Fatal("different root windows must never share an incremental address-space claim")
	}

	fullView := &appSnapshot{
		WindowHandle: "pid1/win0/frame",
		TreeLines:    []string{"a", "b"},
		projection:   snapshotProjection{View: treeViewFull},
	}
	if _, ok := incrementalTree(before, fullView); ok {
		t.Fatal("working and full serializer views must not be diffed as one tree")
	}
}

func TestActionResultFlagsUnchangedState(t *testing.T) {
	base := func() *appSnapshot {
		return &appSnapshot{
			WindowTitle:    "Doc",
			FocusedSummary: "a text field",
			SelectedText:   "",
			TreeLines:      []string{"0 frame Doc", "1 text hello"},
		}
	}
	if observablyChanged(base(), base()) {
		t.Fatal("identical snapshots must count as unchanged")
	}
	changedTree := base()
	changedTree.TreeLines = []string{"0 frame Doc", "1 text hello world"}
	if !observablyChanged(base(), changedTree) {
		t.Fatal("a different tree must count as changed")
	}
	shorterTree := base()
	shorterTree.TreeLines = []string{"0 frame Doc"}
	if !observablyChanged(base(), shorterTree) {
		t.Fatal("a tree with fewer lines must count as changed")
	}
	changedTitle := base()
	changedTitle.WindowTitle = "Doc *"
	if !observablyChanged(base(), changedTitle) {
		t.Fatal("a different window title must count as changed")
	}
	changedSelection := base()
	changedSelection.SelectedText = "hello"
	if !observablyChanged(base(), changedSelection) {
		t.Fatal("a different selection must count as changed")
	}
	changedFocus := base()
	changedFocus.FocusedSummary = "a button"
	if !observablyChanged(base(), changedFocus) {
		t.Fatal("a different focused element must count as changed")
	}
	// 截图必须排除在外：光标闪烁会让每次动作都"有变化"，信号就废了。
	sameButNewScreenshot := base()
	sameButNewScreenshot.ScreenshotPNGBase64 = "different-pixels"
	if observablyChanged(base(), sameButNewScreenshot) {
		t.Fatal("screenshot noise must not count as an observable change")
	}
}

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
		SampledAt:    "2026-08-02T10:00:00.000Z",
		TreeLines:    []string{"0 frame Doc"},
	}
	text := snapshot.renderedText()
	for _, fragment := range []string{
		"pid 7",
		"pid7/win0/frame",
		"id=abc123",
		"sampled_at=2026-08-02T10:00:00.000Z",
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

func TestIdenticalSnapshotSeparatesStillBusyFromTrimmedAway(t *testing.T) {
	previous := &appSnapshot{
		WindowHandle: "pid7/win0/frame",
		SnapshotID:   "same",
		capturedAt:   time.Now().Add(-2 * time.Second),
	}
	// 应用自报在忙 → 可以断言"再等等"。
	busy := &appSnapshot{
		WindowHandle: "pid7/win0/frame",
		SnapshotID:   "same",
		Busy:         &busySignal{ProgressElements: 1},
	}
	notes := continuityNotes(previous, busy)
	if len(notes) != 1 || !strings.Contains(notes[0], "APP REPORTS BUSY") {
		t.Fatalf("a busy app must be told apart from a trimmed tree, got %v", notes)
	}
	if !busy.identicalToPrevious || !busy.comparedToPrevious {
		t.Fatal("the equality verdict must reach the rendered snapshot")
	}

	// 没有忙碌证据时只能说"我不知道"，绝不能反过来断言动作失败：
	// 实测 LibreOffice Impress 上右对齐与保存都生效了，树却逐字未变。
	quiet := &appSnapshot{
		WindowHandle: "pid7/win0/frame",
		SnapshotID:   "same",
	}
	notes = continuityNotes(previous, quiet)
	if len(notes) != 1 || !strings.Contains(notes[0], "not proof your last action failed") {
		t.Fatalf("an unchanged tree is weak evidence, never proof, got %v", notes)
	}
	if !strings.Contains(notes[0], "AX mode=assert_ax") ||
		strings.Contains(notes[0], "`verify`") {
		t.Fatalf("continuity guidance leaked the hidden legacy assertion tool: %v", notes)
	}

	changed := &appSnapshot{WindowHandle: "pid7/win0/frame", SnapshotID: "other"}
	if notes := continuityNotes(previous, changed); len(notes) != 0 {
		t.Fatalf("a changed snapshot needs no note, got %v", notes)
	}
	if !changed.comparedToPrevious || changed.identicalToPrevious {
		t.Fatal("a changed snapshot must still record that a comparison happened")
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

func TestModelVisibleContinuityDetectsRootChangeAcrossHiddenSamples(t *testing.T) {
	service := newService()
	visible := &appSnapshot{
		App:             appDescriptor{Name: "Editor", PID: 7},
		WindowTitle:     "Document",
		WindowHandle:    "pid7/win0/frame",
		publicWindowRef: "w1",
		SnapshotID:      "visible-a",
		sourceApp:       "editor",
		projection:      snapshotProjection{View: treeViewWorking},
	}
	service.rememberDeliveredSnapshot(visible)

	hidden := &appSnapshot{
		App:             appDescriptor{Name: "Editor", PID: 7},
		WindowTitle:     "Document",
		WindowHandle:    "pid7/win1/frame",
		publicWindowRef: "w2",
		SnapshotID:      "hidden-b",
		projection:      snapshotProjection{View: treeViewWorking},
	}
	service.rememberSnapshot("editor", hidden)

	current := *hidden
	baseline := service.deliveredSnapshotFor(&current)
	if baseline != nil {
		t.Fatalf(
			"a different logical window must not reuse another window's continuity baseline: %#v",
			baseline,
		)
	}
}

func TestRepeatedDiagnosticsBecomeCompactEdgeUpdates(t *testing.T) {
	service := newService()
	full := "MODAL DIALOG: the tree below is \"Save As\", a modal dialog."

	first := service.dedupeDiagnosticNotes("Editor", []string{full})
	if len(first) != 1 || first[0] != full {
		t.Fatalf("first diagnostic occurrence must keep the full explanation: %v", first)
	}
	second := service.dedupeDiagnosticNotes("Editor", []string{full})
	if len(second) != 1 ||
		!strings.Contains(second[0], "[diagnostic modal_dialog] still present") ||
		len(second[0]) >= len(full)+80 {
		t.Fatalf("repeated diagnostic should be a compact edge update: %v", second)
	}
	service.dedupeDiagnosticNotes("Editor", nil)
	third := service.dedupeDiagnosticNotes("Editor", []string{full})
	if len(third) != 1 || third[0] != full {
		t.Fatalf("a diagnostic that disappeared and returned must be explained again: %v", third)
	}

	unchangedA := "SNAPSHOT UNCHANGED: same tree. 1.0s apart."
	unchangedB := "SNAPSHOT UNCHANGED: same tree. 3.0s apart."
	service.dedupeDiagnosticNotes("Editor", []string{unchangedA})
	repeated := service.dedupeDiagnosticNotes("Editor", []string{unchangedB})
	if len(repeated) != 1 || !strings.Contains(repeated[0], "diagnostic snapshot_unchanged") {
		t.Fatalf("volatile elapsed time must not defeat unchanged-diagnostic deduplication: %v", repeated)
	}
}

func TestGetAppStateSuppressesOnlyIdenticalRepeatedTrees(t *testing.T) {
	service := newService()
	snapshot := &appSnapshot{
		WindowTitle:         "Doc",
		WindowHandle:        "pid7/win0/frame",
		publicWindowRef:     "w1",
		SnapshotID:          "snapshot-a",
		TreeLines:           []string{"0 frame Doc", "1 paragraph Body"},
		comparedToPrevious:  true,
		identicalToPrevious: true,
	}
	first := service.getAppStateResult(snapshot, nil, false)
	if text := first.Content[0].Text; !strings.Contains(text, "1 paragraph Body") {
		t.Fatalf("a tree not yet delivered in full must be returned even when unchanged: %q", text)
	}

	compact := service.getAppStateResult(snapshot, nil, false)
	if text := compact.Content[0].Text; !strings.Contains(text, "Tree unchanged") ||
		strings.Contains(text, "1 paragraph Body") {
		t.Fatalf("default reads should reference an already-delivered identical tree: %q", text)
	}
	if compact.StructuredContent["tree_reference_only"] != true ||
		compact.StructuredContent["full_tree_returned"] != false {
		t.Fatalf("compact tree reference lacks structured delivery facts: %#v",
			compact.StructuredContent)
	}
	if compact.StructuredContent["tree_view"] != treeViewWorking {
		t.Fatalf("tree reference lost serializer view: %#v", compact.StructuredContent)
	}

	forced := service.getAppStateResult(snapshot, nil, true)
	if text := forced.Content[0].Text; !strings.Contains(text, "1 paragraph Body") {
		t.Fatalf("force_full=true must preserve its complete-tree contract: %q", text)
	}

	fullView := *snapshot
	fullView.projection.View = treeViewFull
	raw := service.getAppStateResult(&fullView, nil, false)
	if text := raw.Content[0].Text; !strings.Contains(text, "1 paragraph Body") {
		t.Fatalf("a different serializer view must not reuse the working-view ledger: %q", text)
	}

	changed := *snapshot
	changed.SnapshotID = "snapshot-b"
	changed.TreeLines = []string{"0 frame Doc", "1 paragraph New body"}
	fresh := service.getAppStateResult(&changed, nil, false)
	if text := fresh.Content[0].Text; !strings.Contains(text, "1 paragraph New body") {
		t.Fatalf("a changed snapshot must return the new full evidence: %q", text)
	}
}

func TestDeliveredTreeLedgerIsBounded(t *testing.T) {
	service := newService()
	for index := 0; index <= maxDeliveredTrees; index++ {
		service.rememberDeliveredTree(fmt.Sprintf("snapshot-%d", index))
	}
	if len(service.observations.lastFullTree) > maxDeliveredTrees {
		t.Fatalf(
			"delivered tree ledger grew without bound: %d",
			len(service.observations.lastFullTree),
		)
	}
}

func TestIncrementalBaselineIgnoresUndeliveredSnapshots(t *testing.T) {
	service := newService()
	delivered := &appSnapshot{
		App:             appDescriptor{Name: "Editor", PID: 7},
		WindowHandle:    "pid7/win0/frame",
		publicWindowRef: "w1",
		TreeLines:       []string{"\t0 frame \"Editor\""},
	}
	for index := 1; index < 20; index++ {
		delivered.TreeLines = append(
			delivered.TreeLines,
			fmt.Sprintf("\t\t%d paragraph \"Stable %d\"", index, index),
		)
	}
	delivered.TreeLines[10] = "\t\t10 paragraph \"Delivered\""
	hidden := *delivered
	hidden.TreeLines = append([]string(nil), delivered.TreeLines...)
	hidden.TreeLines[10] = "\t\t10 paragraph \"Hidden by find\""
	after := hidden
	after.TreeLines = append([]string(nil), hidden.TreeLines...)
	after.TreeLines[10] = "\t\t10 paragraph \"After action\""

	service.rememberDeliveredSnapshot(delivered)
	service.rememberSnapshot("Editor", &hidden)

	baseline := service.deliveredSnapshotFor(&after)
	if baseline != delivered {
		t.Fatalf("undelivered cache state replaced the model-visible baseline: %#v", baseline)
	}
	diff, ok := incrementalTree(baseline, &after)
	if !ok {
		t.Fatal("expected a compact diff against the delivered baseline")
	}
	text := strings.Join(diff, "\n")
	if !strings.Contains(text, "Delivered") ||
		!strings.Contains(text, "After action") ||
		strings.Contains(text, "Hidden by find") {
		t.Fatalf("diff used an undelivered baseline:\n%s", text)
	}
}

func TestHiddenCanonicalAddressChangesAreReported(t *testing.T) {
	service := newService()
	before := &appSnapshot{
		App:             appDescriptor{Name: "Editor", PID: 7},
		WindowHandle:    "pid7/win0/frame",
		publicWindowRef: "w1",
		TreeLines:       []string{"\t0 frame \"Editor\""},
		Elements: []elementRecord{
			{Index: 0, RuntimeID: []int{0}, ControlType: "frame", Name: "Editor"},
			{Index: 7, RuntimeID: []int{0, 1}, ControlType: "section", Name: "Old"},
		},
	}
	after := &appSnapshot{
		App:             appDescriptor{Name: "Editor", PID: 7},
		WindowHandle:    "pid7/win0/frame",
		publicWindowRef: "w1",
		TreeLines:       []string{"\t0 frame \"Editor\""},
		Elements: []elementRecord{
			{Index: 0, RuntimeID: []int{0}, ControlType: "frame", Name: "Editor"},
			{Index: 9, RuntimeID: []int{0, 1}, ControlType: "section", Name: "New"},
		},
	}
	service.rememberExposedHiddenRef(before, before.Elements[1])

	note := service.hiddenCanonicalAddressChangeNote(before, after)

	for _, fragment := range []string{
		"[canonical-address]",
		"1 serializer-hidden ref(s) previously returned by find",
		"call find again",
	} {
		if !strings.Contains(note, fragment) {
			t.Fatalf("hidden canonical address change note missing %q: %q", fragment, note)
		}
	}
}

func TestUnexposedHiddenCanonicalChangesStaySilent(t *testing.T) {
	service := newService()
	before := &appSnapshot{
		App:          appDescriptor{Name: "Editor", PID: 7},
		WindowHandle: "pid7/win0/frame",
		TreeLines:    []string{"\t0 frame \"Editor\""},
		Elements: []elementRecord{
			{Index: 0, RuntimeID: []int{0}, ControlType: "frame", Name: "Editor"},
			{Index: 7, RuntimeID: []int{0, 1}, ControlType: "section", Name: "Old"},
		},
	}
	after := &appSnapshot{
		App:          appDescriptor{Name: "Editor", PID: 7},
		WindowHandle: "pid7/win0/frame",
		TreeLines:    []string{"\t0 frame \"Editor\""},
		Elements: []elementRecord{
			{Index: 0, RuntimeID: []int{0}, ControlType: "frame", Name: "Editor"},
			{Index: 9, RuntimeID: []int{0, 1}, ControlType: "section", Name: "New"},
		},
	}

	if note := service.hiddenCanonicalAddressChangeNote(before, after); note != "" {
		t.Fatalf("unexposed serializer churn must stay out of model context: %q", note)
	}
}

func TestDeliveredTreeLedgerRequiresActualInlineDelivery(t *testing.T) {
	snapshot := &appSnapshot{
		WindowHandle:        "pid7/win0/frame",
		SnapshotID:          "large",
		TreeLines:           []string{"0 frame Doc", strings.Repeat("x", 500)},
		comparedToPrevious:  true,
		identicalToPrevious: true,
	}

	t.Run("inline truncation", func(t *testing.T) {
		t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "120")
		service := newService()
		first := service.getAppStateResult(snapshot, nil, false)
		if first.StructuredContent["tree_delivery_incomplete"] != true ||
			first.StructuredContent["full_tree_returned"] != false {
			t.Fatalf("truncated tree was falsely marked delivered: %#v", first.StructuredContent)
		}
		second := service.getAppStateResult(snapshot, nil, false)
		if second.StructuredContent["tree_reference_only"] == true {
			t.Fatalf("an incompletely delivered tree must not become a compact reference")
		}
	})

	t.Run("file output", func(t *testing.T) {
		t.Setenv("COMPUTER_USE_OUTPUT_MODE", "file")
		t.Setenv("COMPUTER_USE_OUTPUT_THRESHOLD", "1")
		t.Setenv("COMPUTER_USE_OUTPUT_DIR", t.TempDir())
		service := newService()
		result := service.getAppStateResult(snapshot, nil, false)
		if result.StructuredContent["tree_delivery_incomplete"] != true ||
			len(service.observations.lastFullTree) != 0 {
			t.Fatalf("file-only tree was falsely marked delivered: result=%#v ledger=%#v",
				result.StructuredContent, service.observations.lastFullTree)
		}
	})
}

func TestDocumentStateNeverEntersAXModelOrOperatorEvidence(t *testing.T) {
	dirty := true
	snapshot := &appSnapshot{
		App:         appDescriptor{Name: "Writer", PID: 77},
		WindowTitle: "report.docx",
		TreeLines:   []string{"ax:", ` - "document \"report.docx\""`},
		DocumentState: &documentState{
			IdentityStatus: "resolved",
			IdentityKind:   "file",
			IdentityValue:  "/home/user/report.docx",
			Dirty:          &dirty,
			DiskStatus:     "available",
			PathFingerprint: &resourceFingerprint{
				Dev: 1, Ino: 20, Size: 100, MtimeNS: 30,
			},
		},
	}
	text := snapshot.renderedText()
	for _, forbidden := range []string{
		"Document state:",
		"/home/user/report.docx",
		"dirty=",
		"inode",
		"mtime",
		"pid 77",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("filesystem/process evidence leaked %q: %s",
				forbidden, text)
		}
	}
	result := toolCallResult{
		Content:           []contentItem{{Type: "text", Text: text}},
		StructuredContent: map[string]any{},
	}
	addTreeProjectionMetadata(&result, snapshot)
	if result.StructuredContent["operator_document_state"] != nil {
		t.Fatalf("filesystem state entered AX telemetry: %#v",
			result.StructuredContent)
	}
	result = prepareAXEvidence(result)
	if result.StructuredContent["operator_document_state"] != nil {
		t.Fatalf("document state leaked into model evidence: %#v", result)
	}
}

func TestCompactAXActionContentDropsRoutineRouteNotes(t *testing.T) {
	result := compactAXActionContent(toolCallResult{
		Content: []contentItem{{Type: "text", Text: strings.Join([]string{
			"Note: [a11y] Resolved element 12 to button Save",
			"Note: [a11y][semantic] Invoked the element's AT-SPI accessibility action.",
			"Note: MODAL DIALOG: Preferences",
			"ax:",
			` - "dialog \"Preferences\""`,
		}, "\n")}},
	})
	text := resultText(result)
	if strings.Contains(text, "Resolved element") ||
		strings.Contains(text, "Invoked the element") {
		t.Fatalf("routine action narration leaked: %s", text)
	}
	if !strings.Contains(text, "MODAL DIALOG") ||
		!strings.Contains(text, `dialog \"Preferences\"`) {
		t.Fatalf("decision-relevant action evidence was removed: %s", text)
	}
}

func TestPersistAXTelemetryWritesPrivateJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ax-telemetry.jsonl")
	t.Setenv("COMPUTER_USE_AX_TELEMETRY_LOG", path)
	persistAXTelemetry(
		map[string]any{"mode": "observe", "scope": "content"},
		map[string]any{
			"distillation": []any{"complete"},
			"shadow_ast":   map[string]any{"canonical_nodes": 10},
		},
	)
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(payload), &entry); err != nil {
		t.Fatal(err)
	}
	telemetry, _ := entry["telemetry"].(map[string]any)
	if entry["mode"] != "observe" ||
		entry["scope"] != "content" ||
		telemetry["distillation"] == nil ||
		telemetry["shadow_ast"] == nil {
		t.Fatalf("operator telemetry log is incomplete: %#v", entry)
	}
}

func TestPersistAXTelemetryNormalizesEmittedActionBeforeWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ax-telemetry.jsonl")
	t.Setenv("COMPUTER_USE_AX_TELEMETRY_LOG", path)
	result := normalizeAXEvidenceInvariants(toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"preflight_only":   true,
			"evidence_scope":   "error_only",
			"mutation_unknown": true,
			"a11y_batch_result": map[string]any{
				"actions": []any{
					map[string]any{"input_emitted": true},
				},
			},
			"distillation": []any{"private"},
			"shadow_ast":   map[string]any{"canonical_nodes": 10},
		},
	})
	persistAXTelemetry(
		map[string]any{"mode": "act"},
		result.StructuredContent,
	)
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(payload), &entry); err != nil {
		t.Fatal(err)
	}
	telemetry, _ := entry["telemetry"].(map[string]any)
	if telemetry["input_events_emitted"] != true ||
		telemetry["preflight_only"] != false ||
		telemetry["evidence_scope"] != "post_action_observation" {
		t.Fatalf("persisted evidence invariants are inconsistent: %#v", telemetry)
	}
	if telemetry["distillation"] == nil || telemetry["shadow_ast"] == nil {
		t.Fatalf("private telemetry was lost during normalization: %#v", telemetry)
	}
}

func TestPersistAXTelemetryKeepsTruePreflightErrorOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ax-telemetry.jsonl")
	t.Setenv("COMPUTER_USE_AX_TELEMETRY_LOG", path)
	result := normalizeAXEvidenceInvariants(toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"preflight_only": true,
			"evidence_scope": "error_only",
			"failure_code":   "invalid_target",
		},
	})
	persistAXTelemetry(
		map[string]any{"mode": "act"},
		result.StructuredContent,
	)
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(payload), &entry); err != nil {
		t.Fatal(err)
	}
	telemetry, _ := entry["telemetry"].(map[string]any)
	if telemetry["preflight_only"] != true ||
		telemetry["evidence_scope"] != "error_only" ||
		telemetry["input_events_emitted"] != nil {
		t.Fatalf("true preflight evidence changed: %#v", telemetry)
	}
}

func TestRootWindowSwitchAnnouncesRenumbering(t *testing.T) {
	// 同一应用的第二个**同标题**窗口会让快照根静默切换，全部 element_index
	// 平移而返回文本上一个字不变。标题相同正是这条判据存在的理由。
	previous := &appSnapshot{WindowHandle: "pid7/win0/frame", WindowTitle: "Home", SnapshotID: "a"}
	current := &appSnapshot{WindowHandle: "pid7/win1/frame", WindowTitle: "Home", SnapshotID: "b"}

	notes := continuityNotes(previous, current)

	if len(notes) != 1 || !strings.Contains(notes[0], "ROOT WINDOW CHANGED") {
		t.Fatalf("a root window switch must be announced, got %v", notes)
	}
	if !strings.Contains(notes[0], "previous window are stale") {
		t.Fatal("the note must say refs owned by the previous window are stale")
	}
}

func TestContentPlaneDetectionIsStructuralNotApplicationSpecific(t *testing.T) {
	// P3：树结构完整、语义正常、只是缺少目标内容时必须报警，而判据只能落在
	// 角色语义上——嵌入式内容宿主横跨浏览器、Electron、PDF 查看器、webview。
	for _, fragment := range []string{
		"def content_host_diagnostic(records):",
		"CONTENT_PLANE_ROLES",
		"EMBEDDED_CONTAINER_ROLES",
		"CONTENT PLANE UNCERTAIN",
	} {
		if !strings.Contains(linuxRuntimeScript, fragment) {
			t.Fatalf("content-plane detection missing %q", fragment)
		}
	}
	// 处方必须指向另一条通道，否则报了警也没有出路。
	if !strings.Contains(linuxRuntimeScript, "Inspect the full-desktop screenshot") {
		t.Fatal("the uncertain diagnostic must direct the caller to visual evidence")
	}
}

func TestForegroundAlarmsAreFilteredAndGraded(t *testing.T) {
	// P4：实测 multi_apps 触发 3955 次前台抢占告警，97.7% 的"抢占者"是
	// 无标题零内容的伪窗口，而那条 Note 还附带硬处方。
	for _, fragment := range []string{
		"def is_infrastructure_window(window):",
		"def accepts_user_input(app):",
		"if is_infrastructure_window(window):",
		"ANOTHER WINDOW MAY BE IN FRONT",
		"appNotUsable",
	} {
		if !strings.Contains(linuxRuntimeScript, fragment) {
			t.Fatalf("alarm filtering/grading missing %q", fragment)
		}
	}
	// 判据必须是窗口形状，不是进程名白名单：按名字过滤换个桌面环境就失效。
	for _, fitted := range []string{`"xfdesktop"`, `"xfwm4"`, `"mutter"`, `"gnome-shell"`} {
		if strings.Contains(linuxRuntimeScript, "if "+fitted) {
			t.Fatalf("infrastructure detection must not branch on a process name: %s", fitted)
		}
	}
}

func TestLinuxRuntimeGuardsGlobalInputSynthesis(t *testing.T) {
	if !strings.Contains(linuxRuntimeScript, "def require_window_focus(window, what):") {
		t.Fatal("Linux runtime needs a focus guard before global input synthesis")
	}
	for _, guarded := range []string{
		"require_window_focus(window, \"keypress\")",
		"require_window_focus(window, \"type\")",
		"require_window_focus(window, \"scroll\")",
		"require_coordinate_delivery(window, x, y, \"click\")",
	} {
		if !strings.Contains(linuxRuntimeScript, guarded) {
			t.Fatalf("global synthesis path is unguarded: missing %s", guarded)
		}
	}
	if !strings.Contains(linuxRuntimeScript, "Refusing to synthesize") {
		t.Fatal("Linux runtime should fail loudly rather than deliver input to whichever window holds focus")
	}
	if strings.Contains(linuxRuntimeScript, `require_window_focus(window, "gui")`) {
		t.Fatal("gui must preserve the observed desktop stacking before absolute input")
	}
}

func TestLinuxRuntimeFocusesWindowThroughFocusableChild(t *testing.T) {
	if !strings.Contains(linuxRuntimeScript, "Atspi.StateType.FOCUSABLE") {
		t.Fatal("Linux runtime should look for a FOCUSABLE child; grab_focus on a GTK frame always fails")
	}
	if !strings.Contains(linuxRuntimeScript, "FOCUS_GRAB_CANDIDATES") {
		t.Fatal("Linux runtime should cap focus-grab attempts because grabbing moves real focus inside the app")
	}
	if !strings.Contains(linuxRuntimeScript, "Atspi.StateType.ACTIVE") {
		t.Fatal("Linux runtime should use the window ACTIVE state to decide whether synthesis is safe")
	}
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

func findA11yActionSchemaBranch(
	t *testing.T,
	actionType string,
) map[string]any {
	t.Helper()
	act := findA11ySchemaBranch(t, "act", "")
	actionItems := act["properties"].(map[string]any)["actions"].(map[string]any)["items"].(map[string]any)
	branches, ok := actionItems["oneOf"].([]any)
	if !ok {
		t.Fatalf("a11y actions must publish oneOf: %#v", actionItems)
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
		action, ok := properties["type"].(map[string]any)
		if ok && action["const"] == actionType {
			return branch
		}
	}
	t.Fatalf("a11y action schema branch %q not found", actionType)
	return nil
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	path, err := os.MkdirTemp("/tmp", "ocu-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(path)
	})
	return path
}

func TestMCPInstructionsDocumentGlobalSynthesisFocusContract(t *testing.T) {
	if !strings.Contains(serverInstructions, "fallbacks use global desktop focus") {
		t.Fatal("MCP instructions must tell the agent that synthesis ignores the app argument")
	}
	if !strings.Contains(serverInstructions, "not background input") {
		t.Fatal("MCP instructions must not imply background delivery")
	}
}

func TestSourceDoesNotReplaySuccessfulSemanticClicks(t *testing.T) {
	source := mainGoSource(t)
	for _, forbidden := range []string{
		"retried the same element as a coordinate click",
		"shouldRetryWithSynthesis",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("语义调用已返回成功时不得因观测盲区自动再点一次: %q", forbidden)
		}
	}
}

// 渲染一个节点时，动作表只允许问一次。
//
// LibreOffice 的 ATK 桥被反复问动作表会打出
//
//	(soffice): CRITICAL: impl_get_NActions: assertion 'ATK_IS_ACTION (user_data)' failed
//
// 密集调用下应用会整个退出（实测：对话框循环第 3~4 轮 soffice 消失，
// 随后 get_app_state 报 appNotFound）。早先为了给出 [has-click-action] 标记，
// record_for 经 state_segment -> preferred_action_index 又问了第二次，
// 等于在一个已知脆弱的桥上把每个节点的问询翻倍。
func TestTreeRenderingReadsActionTableOnce(t *testing.T) {
	if !strings.Contains(linuxRuntimeScript, "def node_actions(") {
		t.Fatal("动作表必须有一个一次读完的入口 node_actions()")
	}
	// record_for 里不得再出现第二次动作表读取。
	if strings.Contains(linuxRuntimeScript, "state_segment(node)\n") {
		t.Fatal("state_segment 不应自己再去问一次动作表，标记要由 record_for 传入")
	}
	if !strings.Contains(linuxRuntimeScript, "actions, has_click_action = node_actions(node)") {
		t.Fatal("record_for 应当一次读完动作表，两个字段共用")
	}
}

// require_window_focus 只确认合成前目标应用窗口处于活动状态。
// 它不识别窗口内的焦点控件，也不证明控件级送达。
func TestSynthesisOnlyToolsConfirmOnlyWindowFocus(t *testing.T) {
	for _, tool := range []string{"keypress", "scroll"} {
		request := axServiceRequest{}
		switch tool {
		case "keypress":
			request.Action = axKeypressCommand{Key: "ENTER"}
		case "scroll":
			request.Action = axScrollCommand{Direction: "down", Pages: 1}
		}
		if !windowFocusWasConfirmed(request) {
			t.Fatalf("%s 走 require_window_focus，窗口焦点应当可确认", tool)
		}
	}
	if windowFocusWasConfirmed(axServiceRequest{Action: axClickCommand{}}) {
		t.Fatal("click 可能走语义通道，不得声称确认过窗口焦点")
	}
	if !strings.Contains(
		findToolDefinition(t, "AX").Description,
		"resolve live",
	) {
		t.Fatal("instructions 应当仍然引导优先走元素定向")
	}
	if !strings.Contains(linuxRuntimeScript, "Delivery to the intended target was not verified") {
		t.Fatal("合成通道仍应声明目标控件级别的不确定性")
	}
}

// a11y 优先的理由是**可操作性**，不是普遍更便宜。
//
// 实测（#29）：gedit 树 349 token vs 窗口截图约 1014 视觉 token（截图贵 2.9x）；
// 文件管理器树 2135 token vs 截图 756（截图反而便宜 0.4x）。
// 早先的措辞暗示截图总是更贵，对内容丰富的应用是错的——工具不该拿一个
// 会被实测推翻的理由去引导 agent。
func TestInstructionsDoNotClaimScreenshotsAreAlwaysCostlier(t *testing.T) {
	if !strings.Contains(serverInstructions, "Use AX for named controls") {
		t.Fatal("应当把语义定位讲成 AX 优先的理由")
	}
	if strings.Contains(serverInstructions, "it is cheap, precise") {
		t.Fatal("不得再声称 a11y 树总是更便宜——实测在文件管理器上不成立")
	}
}

// 工具名本身就是引导：perform_secondary_action 读起来像 fallback，而 a11y
// 语义动作恰恰是首选路径。实测中通道选对与否直接决定任务成败，而名字是模型
// 选通道时最强的信号——比描述强。用户拍板改名（方案 C），接受与官方 schema
// 及 macOS/Windows 的分歧。
// 意图声明的交叉核对。抄自 Playwright 的 element 参数（"Human-readable element
// description"），它本意是可读性与权限，副作用正好是**意图可核对**。
//
// 修的是实测踩过的一类**静默**失败：F4 打开对话框后索引全部重排，用上一份快照
// 的下标调 click(element_index=5)，工具照点不误——本想点 Position Y，实际点到
// 菜单，把对象高度误改成 16.26cm，全程没有一条报错。
func TestElementIntentCrossCheck(t *testing.T) {
	spin := &elementRecord{Index: 5, ControlType: "spin button", Description: "Enter the vertical distance"}
	menu := &elementRecord{Index: 5, ControlType: "menu", Name: "Insert"}

	// 就是那次实测：声明"Position Y spin button"，下标却解析到 menu Insert。
	mismatch := elementIntentMismatch(menu, "Position Y spin button")
	if mismatch == "" {
		t.Fatal("下标解析到完全不同的控件时必须拒绝")
	}
	for _, fragment := range []string{`menu "Insert"`, "Position Y spin button", "earlier snapshot"} {
		if !strings.Contains(mismatch, fragment) {
			t.Fatalf("拒绝信息要把两边都说清并给出原因，缺 %q: %s", fragment, mismatch)
		}
	}

	// 回归：第一版把 role 也算进可匹配范围，被最常见的词击穿——声明里几乎必然
	// 有 "button"，role 里也几乎必然有 "button"，于是正要拦的情况照样放行。
	// 实测确认过：声明 "the Save button"、给的却是 New 的下标，当时放行了。
	if elementIntentMismatch(&elementRecord{Index: 8, ControlType: "push button", Name: "New",
		Description: "Create a new document"}, "the Save button") == "" {
		t.Fatal("通用词 button 不得让校验失效")
	}

	// 无名元素的身份常来自旁边的 label，自身属性里没有可比对的东西——
	// 实测 LibreOffice「位置和大小」的四个 spin button 全无名字，
	// "哪个是 Position Y" 只写在旁边的 label 上。这类**判不了就别判**。
	if got := elementIntentMismatch(spin, "Position Y spin button"); got != "" {
		t.Fatalf("无名元素不该校验（否则纯误伤）: %s", got)
	}

	// 同一句声明对上正确的元素时不得拒绝。
	if got := elementIntentMismatch(&elementRecord{ControlType: "push button", Name: "Save",
		Description: "Save the current file"}, "the Save button"); got != "" {
		t.Fatalf("声明与元素相符不该拒绝: %s", got)
	}

	// **误拒比漏过更糟**：被误拒的 agent 会以为目标根本不存在。
	// 以下都必须放行。
	for _, ok := range []struct {
		record   *elementRecord
		declared string
		why      string
	}{
		{menu, "", "没声明就是没开启这个校验，维持旧行为"},
		{menu, "插入菜单", "非 ASCII 声明比不了英文 role/name，判不了就别拒"},
		{menu, "the", "只有虚词，没有可比对的实词"},
		{&elementRecord{ControlType: "push button", Name: "OK"}, "OK", "名字完全命中"},
		{&elementRecord{ControlType: "push button", Name: "Save"}, "save button", "大小写不敏感"},
		{&elementRecord{ControlType: "table cell", Name: "A1"}, "the A1 cell", "虚词之外有实词命中"},
		{&elementRecord{ControlType: "spin button"}, "spin button with no name", "无名元素跳过校验"},
		{&elementRecord{ControlType: "push button", Name: "OK"}, "the button", "只有通用词，没有区分性实词"},
	} {
		if got := elementIntentMismatch(ok.record, ok.declared); got != "" {
			t.Fatalf("误拒（%s）: declared=%q -> %s", ok.why, ok.declared, got)
		}
	}
}

// element 必须出现在所有按 element_index 寻址的工具上，否则这道校验会有缺口，
// 而缺口正好落在 agent 最容易搞错的地方。
// 这两条都是**真实 agent 轨迹**（Claude Code 挂 MCP 跑 OSWorld Impress 任务，
// 官方评估器判 1.0）暴露出来的，不是设想的场景。
func TestNotesLearnedFromTheRealAgentRun(t *testing.T) {
	// ① ctrl+s 之后 "delivered-but-ignored" 说过头了：文件其实存下来了，
	//    agent 却因此多花截图 + 点 Save 两步去自证，占那次 12 步里的 17%。
	//    格式类改动与文件状态**根本不进 a11y 树**，所以"树没变"只能推出
	//    "树看不见"，推不出"没生效"。
	for _, fragment := range []string{
		"No accessibility change observed",
		"Visual change was not measured",
		"that is not control-delivery evidence",
	} {
		if !strings.Contains(unchangedSynthesisNote(), fragment) {
			t.Fatalf("送达但无变化的 Note 不得断言失败，缺 %q", fragment)
		}
	}

	// ② `unknown element_index "128"` 只有 27 个字符、零指引，而同样是"下标
	//    过期"，解析到别的控件时的报错却讲清了原因和出路。两条路径的帮助程度
	//    不该差这么多。
	snapshot := &appSnapshot{Elements: []elementRecord{{Index: 0}, {Index: 59}}}
	_, err := lookupElement(snapshot, "128")
	if err == nil {
		t.Fatal("下标不存在时必须报错")
	}
	// 照 Playwright 的错误形态：工具名前缀 + 结构化字段 + Call log + 下一步。
	// 替换掉的是 `unknown element_index "128"` 那种 27 字符零指引。
	for _, fragment := range []string{
		"indices 0..59",
		"Call log:",
		"resolving target.index=128",
		"Next:",
		"AX mode=observe or mode=find again",
		"returned rN ref",
	} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("下标不存在的报错要给出完整事实链与出路，缺 %q:\n%s", fragment, err)
		}
	}
	// 快照年龄是我们特有的必需信息：桌面上快照可能陈旧几十秒而 agent 毫无察觉。
	if !strings.Contains(err.Error(), "Snapshot:") {
		t.Fatalf("报错要说清快照有多旧:\n%s", err)
	}
}

// 把那句 Note 单独取出来，测试才不用去猜它藏在哪个分支里。
func unchangedSynthesisNote() string {
	return strings.Join(unchangedNotes(true, pixelsUnknown, false), " ")
}

// P4：选择器要能替代数字下标，语法与快照行**逐字一致**。
//
// 数字下标每次 get_app_state 都会重排，是 agent 最容易犯错的地方——意图声明
// 只是把那个错误变得**响亮**，没有消除它。选择器从快照行抄下来就能用。
func TestSelectorLookup(t *testing.T) {
	snapshot := &appSnapshot{Elements: []elementRecord{
		{Index: 4, ControlType: "push button", Name: "Save"},
		{Index: 8, ControlType: "push button", Name: "New"},
		{Index: 9, ControlType: "menu", Name: "Save"},
		{Index: 12, ControlType: "label", Name: ""},
	}}

	for _, ok := range []struct {
		selector string
		want     int
	}{
		{`push button "Save"`, 4},
		{`menu "Save"`, 9},
		{`push button "New"`, 8},
	} {
		record, err := lookupElement(snapshot, ok.selector)
		if err != nil {
			t.Fatalf("%s 应当命中: %v", ok.selector, err)
		}
		if record.Index != ok.want {
			t.Fatalf("%s 命中了 %d，期望 %d", ok.selector, record.Index, ok.want)
		}
	}

	// 歧义时**不许挑一个**——静默挑一个正是本项目一直在修的那类错误。
	_, err := lookupElement(snapshot, `"Save"`)
	if err == nil {
		t.Fatal(`"Save" 同时命中 push button 与 menu，必须报歧义`)
	}
	// 候选行的形态照 Playwright 的 strict mode 违规报错：下标 + 描述 + `aka` 一个
	// 可直接使用的替代选择器。
	for _, fragment := range []string{"ambiguous", "matches 2 elements",
		`4    push button "Save"`, `9    menu "Save"`, "aka", "adding the role"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("歧义报错要列出候选与收敛办法，缺 %q: %s", fragment, err)
		}
	}

	// 收敛建议要看**缺的是哪一半**：只给了 role 的选择器，该让它补名字，
	// 而不是补一个它已经给了的 role。
	_, err = lookupElement(snapshot, "push button")
	if err == nil {
		t.Fatal("只给 role 且命中多个时必须报歧义")
	}
	if !strings.Contains(err.Error(), `adding the name in quotes`) ||
		!strings.Contains(err.Error(), "push button \"Save\"") {
		t.Fatalf("只给 role 时应当建议补名字: %s", err)
	}

	// 找不到时要说清选择器怎么写。
	_, err = lookupElement(snapshot, `push button "Nope"`)
	if err == nil || !strings.Contains(err.Error(), "no element matches") {
		t.Fatalf("找不到时要明确报出来: %v", err)
	}

	// 数字下标仍然照旧。
	if record, err := lookupElement(snapshot, "8"); err != nil || record.Index != 8 {
		t.Fatalf("数字下标不得失效: %v", err)
	}
}

// P5：通道整体可关，且被关掉的工具**根本不出现在 tools/list 里**。
//
// 这个区别很要紧：模型看得见的工具会去试，试了被拒就是浪费一轮；看不见就不会试。
// Playwright 把坐标做成 opt-in 能力（--caps=vision）也是这个道理。
func TestChannelCapabilitySwitch(t *testing.T) {
	names := func() map[string]bool {
		out := map[string]bool{}
		for _, tool := range toolDefinitions() {
			out[tool.Name] = true
		}
		return out
	}

	t.Setenv("COMPUTER_USE_CHANNELS", "")
	all := names()
	for _, tool := range []string{"AX", "GUI"} {
		if !all[tool] {
			t.Fatalf("默认应当三条通道全开，缺 %s", tool)
		}
	}

	t.Setenv("COMPUTER_USE_CHANNELS", "a11y,keyboard")
	without := names()
	if without["GUI"] {
		t.Fatal("关掉 gui 通道后 GUI 不该还在 tools/list 里")
	}
	for _, kept := range []string{"AX"} {
		if !without[kept] {
			t.Fatalf("关掉 gui 不该波及 %s", kept)
		}
	}

	// 硬调也要拒，而且要说清是**通道被关了**，不是工具不存在。
	service := newService()
	result := service.callTool("gui", map[string]any{"actions": []any{}})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "channel, which is disabled") {
		t.Fatalf("被关通道的工具硬调要说清原因: %#v", result)
	}
}

func TestConsolidatedA11ySurfaceExposesOnlyA11yAndGUI(t *testing.T) {
	t.Setenv("COMPUTER_USE_CHANNELS", "")

	names := map[string]bool{}
	for _, tool := range toolDefinitions() {
		names[tool.Name] = true
	}
	for _, visible := range []string{"AX", "GUI"} {
		if !names[visible] {
			t.Fatalf("consolidated surface must keep %s visible", visible)
		}
	}
	for _, hiddenAlias := range []string{"a11y", "gui"} {
		if names[hiddenAlias] {
			t.Fatalf("compatibility alias %s must not be advertised", hiddenAlias)
		}
	}
	if len(names) != 2 {
		t.Fatalf("consolidated surface exposed unexpected tools: %#v", names)
	}

	for public, internal := range map[string]string{
		"AX":  "a11y",
		"GUI": "gui",
	} {
		if got := internalToolName(public); got != internal {
			t.Fatalf("public spelling %s normalized to %s, want %s",
				public, got, internal)
		}
	}
	for _, legacy := range []string{
		"a11y", "get_app_state", "find", "verify", "list_apps", "click",
		"invoke_element_action", "press_key", "scroll", "set_value", "type_text",
	} {
		if _, ok := publicToolCallName(legacy); ok {
			t.Fatalf("legacy public tool %q must be rejected", legacy)
		}
	}
}

func TestMCPRejectsLegacyToolCallsWithoutMigrationCompatibility(t *testing.T) {
	request := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "get_app_state",
			"arguments": map[string]any{},
		},
	}
	response := handleMCPRequest(request, newService())
	result := response["result"].(toolCallResult)
	if !result.IsError ||
		!strings.Contains(resultText(result), "use AX or GUI") {
		t.Fatalf("legacy MCP tool call was not rejected: %#v", response)
	}
}

func TestA11yReadModesKeepTheirEvidenceBoundaries(t *testing.T) {
	tool := findToolDefinition(t, "AX")
	for _, fragment := range []string{
		"AX proves only AX-visible facts",
		"not pixels/files/persistence/task completion",
	} {
		if !strings.Contains(tool.Description, fragment) {
			t.Fatalf("a11y description must bound assertion evidence: missing %q",
				fragment)
		}
	}

	if got := toolEvidenceScope("a11y", map[string]any{
		"mode": "observe", "observation_target": "apps",
	}); got != "app_inventory" {
		t.Fatalf("apps observation evidence scope=%q", got)
	}
	for _, args := range []map[string]any{
		{"mode": "observe"},
		{"mode": "find"},
		{"mode": "assert_ax"},
	} {
		if got := toolEvidenceScope("a11y", args); got != "independent_ui_read" {
			t.Fatalf("%v evidence scope=%q", args, got)
		}
	}
	if got := toolEvidenceScope("a11y", map[string]any{
		"mode": "act",
	}); got != "delivery_or_local_ui" {
		t.Fatalf("act evidence scope=%q", got)
	}

	err := validateA11yModeFields(a11yModeObserve, map[string]any{
		"mode":               "observe",
		"observation_target": "apps",
		"text_limit":         float64(100),
		"timeout_ms":         float64(5000),
	})
	if err == nil ||
		!strings.Contains(err.Error(), `"text_limit"`) ||
		!strings.Contains(err.Error(), `"timeout_ms"`) {
		t.Fatalf("apps inventory must report every invalid field together: %v",
			err)
	}
	err = validateA11yModeFields(a11yModeAct, map[string]any{
		"mode":       "act",
		"app":        "Editor",
		"actions":    []any{map[string]any{"type": "press_key", "key": "Tab"}},
		"text_limit": float64(100),
		"timeout_ms": float64(5000),
	})
	if err == nil ||
		!strings.Contains(err.Error(), `"text_limit"`) ||
		!strings.Contains(err.Error(), `"timeout_ms"`) {
		t.Fatalf("act must report every invalid field together: %v", err)
	}

	service := newService()
	rejected := service.a11yRequest(map[string]any{
		"mode":    "find",
		"app":     "Editor",
		"query":   map[string]any{"name": "Save"},
		"actions": []any{},
	}, observation{})
	if !rejected.IsError ||
		!strings.Contains(resultText(rejected), `mode="find" does not accept fields: "actions"`) {
		t.Fatalf("find must reject action fields before reading: %#v", rejected)
	}
}

func TestPreflightOnlyErrorsDoNotCaptureFreshUIContext(t *testing.T) {
	service := newService()
	result := service.callTool("a11y", map[string]any{
		"mode":    "find",
		"app":     "Editor",
		"query":   map[string]any{"name": "Save"},
		"actions": []any{},
	})
	if !result.IsError {
		t.Fatalf("invalid mixed-mode request must fail: %#v", result)
	}
	for key, want := range map[string]any{
		"evidence_scope":                 "error_only",
		"error_context_captured":         false,
		"error_context_fresh_ui_sampled": false,
		"preflight_only":                 true,
	} {
		if got := result.StructuredContent[key]; got != want {
			t.Fatalf("%s=%#v want %#v in %#v",
				key, got, want, result.StructuredContent)
		}
	}
	if strings.Contains(resultText(result), "error context") {
		t.Fatalf("preflight-only failure must not append fresh UI context: %s",
			resultText(result))
	}
}

// 像素比对是**独立于树**的第二个信号。两条合起来才有强弱之分。
//
// 起因是实测过的一次误判：ctrl+s 之后树字节不变，工具断言"送达但被忽略"，
// 而文件其实存下来了；agent 因此多花截图 + 点 Save 两步自证。
// 树看不见整类效果（格式改动、文件状态、画布像素），所以"树没变"推不出
// "没生效"——但配上"屏幕也没变"就推得出。
func TestUnchangedNotesReportOnlyObservedChange(t *testing.T) {
	changed := strings.Join(unchangedNotes(true, pixelsChanged, false), " ")
	if !strings.Contains(changed, "visual change observed") {
		t.Fatalf("屏幕变化只能报告 visual change observed: %s", changed)
	}

	identical := strings.Join(unchangedNotes(true, pixelsIdentical, false), " ")
	if !strings.Contains(identical, "no observed change") ||
		!strings.Contains(identical, "control delivery and semantic effect remain unknown") {
		t.Fatalf("屏幕不变只能报告 no observed change，并保留送达不确定性: %s", identical)
	}
	weaker := strings.Join(unchangedNotes(false, pixelsIdentical, false), " ")
	if !strings.Contains(weaker, "no observed change") {
		t.Fatalf("无窗口焦点证据时仍只报告观测结果: %s", weaker)
	}

	unknown := strings.Join(unchangedNotes(true, pixelsUnknown, false), " ")
	if !strings.Contains(unknown, "No accessibility change observed") ||
		!strings.Contains(unknown, "not control-delivery evidence") {
		t.Fatalf("像素比不了时只报告 a11y 观测和窗口焦点边界: %s", unknown)
	}
	for name, note := range map[string]string{
		"changed": changed, "identical": identical, "unknown": unknown,
		"attrs":         strings.Join(unchangedNotes(true, pixelsUnknown, true), " "),
		"attrs_changed": strings.Join(unchangedNotes(true, pixelsChanged, true), " "),
	} {
		for _, forbidden := range []string{"took effect", "did nothing", "input arrived and was ignored"} {
			if strings.Contains(strings.ToLower(note), forbidden) {
				t.Fatalf("%s note contains forbidden inference %q: %s", name, forbidden, note)
			}
		}
	}
	if attrsChanged := strings.Join(unchangedNotes(true, pixelsChanged, true), " "); !strings.Contains(attrsChanged, "text attributes changed") ||
		!strings.Contains(attrsChanged, "visual change observed") {
		t.Fatalf("属性与屏幕变化应分别报告，不得由一个分支吞掉另一个: %s", attrsChanged)
	}

	for _, c := range []struct {
		notes []string
		want  pixelVerdict
	}{
		{[]string{"[pixels] sampled_pixel_change=none: no sampled pixel difference persisted."}, pixelsIdentical},
		{[]string{"[pixels] persistent_change=false: legacy no sampled pixel difference persisted."}, pixelsIdentical},
		{[]string{"[pixels] 4.1% of the window changed on screen. Changes are concentrated in {1,2,3,4}."}, pixelsChanged},
		{[]string{"[pixels] The window changed size or position during this action"}, pixelsChanged},
		{[]string{"[a11y][semantic] something"}, pixelsUnknown},
		{nil, pixelsUnknown},
	} {
		if got := readPixelVerdict(c.notes); got != c.want {
			t.Fatalf("readPixelVerdict(%v) = %v, want %v", c.notes, got, c.want)
		}
	}
}

func TestFindOmitsInternalContinuityDiagnostics(t *testing.T) {
	notes := compactFindNotes([]string{
		"SNAPSHOT UNCHANGED: a long explanation that should not be repeated.",
		"[diagnostic snapshot_unchanged] still present; full explanation unchanged from the previous response.",
		"MODAL DIALOG: Save As",
	})
	joined := strings.Join(notes, "\n")
	for _, forbidden := range []string{
		"continuity", "diagnostic", "long explanation",
		"full explanation unchanged", "MODAL DIALOG",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("find leaked internal note %q: %v", forbidden, notes)
		}
	}
	if joined != "[state] semantic_state=unchanged\n[state] blocking=modal_dialog" {
		t.Fatalf("find must retain only compact decision state: %v", notes)
	}
}

func TestModelFacingAXNotesKeepDecisionFactsOnly(t *testing.T) {
	notes := modelFacingAXNotes([]string{
		`[semantic-resolution] fresh=true unique=true selector="Save"`,
		"ROOT WINDOW CHANGED: verbose internal identity explanation.",
		"SNAPSHOT UNCHANGED, APP REPORTS BUSY: verbose timing explanation.",
		"DIALOG IN FRONT: verbose explanation.",
		`[expect] {"predicate_verified":false}`,
	})
	joined := strings.Join(notes, "\n")
	for _, forbidden := range []string{
		"semantic-resolution", "ROOT WINDOW CHANGED",
		"SNAPSHOT UNCHANGED", "DIALOG IN FRONT", "verbose",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("model note leaked %q: %s", forbidden, joined)
		}
	}
	for _, required := range []string{
		"window_changed=true",
		"busy=true",
		"recovery=wait_then_observe",
		"blocking=dialog_in_front",
		"predicate_verified",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("model note lost %q: %s", required, joined)
		}
	}
}

func TestObserveNoneKeepsOnlyVerificationAndInvalidation(t *testing.T) {
	notes := observeNoneNotes([]string{
		`[a11y] Resolved element_index 7 to push button "Save".`,
		"[pixels] sampled_pixel_change=tiny: one point changed.",
		`[expect] {"predicate_verified":false,"settled_after_ms":0}`,
		"[canonical-address] serializer-hidden ref changed.",
		"ROOT WINDOW CHANGED: old refs are invalid.",
		"SNAPSHOT UNCHANGED: long explanation.",
	})
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, "predicate_verified") ||
		!strings.Contains(joined, "[canonical-address]") ||
		!strings.Contains(joined, "ROOT WINDOW CHANGED") {
		t.Fatalf("observe=none must keep verification and invalidation: %v", notes)
	}
	for _, forbidden := range []string{"Resolved element_index", "sampled_pixel_change", "SNAPSHOT UNCHANGED"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("observe=none leaked optional diagnostic %q: %v", forbidden, notes)
		}
	}
}

func TestObserveNoneKeepsSettledPhaseDiagnosticsOnly(t *testing.T) {
	notes := observeNoneNotes([]string{
		"[observation phase=immediate snapshot_id=a superseded_by=b] " +
			"ROOT WINDOW CHANGED: immediate dialog.",
		"[observation phase=settled snapshot_id=b] " +
			"MODAL DIALOG: final warning.",
	})
	joined := strings.Join(notes, "\n")
	if strings.Contains(joined, "immediate dialog") {
		t.Fatalf("observe=none retained a superseded immediate diagnostic: %v", notes)
	}
	if !strings.Contains(joined, "final warning") {
		t.Fatalf("observe=none dropped the settled final diagnostic: %v", notes)
	}
}

func TestStructuredEvidenceScopeDoesNotClaimTaskSuccess(t *testing.T) {
	result := textResult("ok", false).withEvidenceScope("delivery_or_local_ui")
	if result.StructuredContent["evidence_scope"] != "delivery_or_local_ui" {
		t.Fatalf("missing evidence scope: %#v", result.StructuredContent)
	}
	if verified, ok := result.StructuredContent["task_effect_verified"].(bool); !ok || verified {
		t.Fatalf("mutating tool result must not claim task effect: %#v", result.StructuredContent)
	}
	if _, exists := result.StructuredContent["tool_execution_succeeded"]; exists {
		t.Fatalf("generic success bits invite task-effect overclaim: %#v", result.StructuredContent)
	}
	for _, ambiguous := range []string{"operation_completed", "tool_call_completed"} {
		if _, exists := result.StructuredContent[ambiguous]; exists {
			t.Fatalf("ambiguous completion field %q must not be returned: %#v",
				ambiguous, result.StructuredContent)
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"structuredContent"`) {
		t.Fatalf("structured evidence must be serialized: %s", encoded)
	}

	failed := textResult("missing app", true).withEvidenceScope("delivery_or_local_ui")
	if failed.StructuredContent["evidence_scope"] != "error_only" {
		t.Fatalf("precondition errors must not claim observation or delivery: %#v", failed.StructuredContent)
	}
	if _, exists := failed.StructuredContent["tool_execution_succeeded"]; exists {
		t.Fatalf("isError already carries failure; no generic success bit should remain")
	}
	for _, ambiguous := range []string{"operation_completed", "tool_call_completed"} {
		if _, exists := failed.StructuredContent[ambiguous]; exists {
			t.Fatalf("failed results must not revive ambiguous completion field %q: %#v",
				ambiguous, failed.StructuredContent)
		}
	}

	stale := textResult("stale", true)
	stale.StructuredContent = map[string]any{"observation_required": true}
	stale = stale.withEvidenceScope("delivery_or_local_ui")
	if stale.StructuredContent["evidence_scope"] != "error_only" {
		t.Fatalf("a failed action must not claim fresh UI evidence: %#v",
			stale.StructuredContent)
	}
}

func TestTextEvidencePreservesTheOriginalToolContent(t *testing.T) {
	result := toolCallResult{
		Content: []contentItem{{Type: "text", Text: "full accessibility tree"}},
		StructuredContent: map[string]any{
			"task_effect_verified": false,
		},
	}
	visible := withTextEvidence(result)
	if len(visible.Content) != 2 ||
		!strings.HasPrefix(visible.Content[0].Text, "[tool-evidence] ") ||
		visible.Content[1].Text != "full accessibility tree" {
		t.Fatalf("text evidence hid or rewrote the original tool content: %#v", visible.Content)
	}
}

func TestRuntimeFailureFactsRemainStructured(t *testing.T) {
	result := runtimeReportedFailure(ProviderAccessibilityResult{
		OK:                   false,
		Error:                "stale",
		FailureCode:          "stale_element_ref",
		FailurePhase:         "precondition",
		InputEventsEmitted:   false,
		PartialInputPossible: false,
		FailureFactsComplete: true,
	})
	if result.StructuredContent["failure_code"] != "stale_element_ref" ||
		result.StructuredContent["failure_phase"] != "precondition" ||
		result.StructuredContent["input_events_emitted"] != false ||
		result.StructuredContent["partial_input_possible"] != false ||
		result.StructuredContent["mutation_unknown"] != false ||
		result.StructuredContent["failure_facts_complete"] != true {
		t.Fatalf("runtime failure facts were lost: %#v", result.StructuredContent)
	}
}

func TestCallToolAlwaysAddsEvidenceContract(t *testing.T) {
	service := newService()
	result := service.callTool("AX", map[string]any{
		"mode": "act",
		"actions": []any{map[string]any{
			"type":         "click",
			"target":       map[string]any{"ref": "r1"},
			"click_method": "not-a-method",
		}},
	})
	if !result.IsError || result.StructuredContent == nil {
		t.Fatalf("parameter errors must still carry the evidence contract: %#v", result)
	}
	if result.StructuredContent["evidence_scope"] != "error_only" ||
		result.StructuredContent["task_effect_verified"] != false {
		t.Fatalf("parameter error evidence contract is incomplete: %#v", result.StructuredContent)
	}
}

func TestErrorContextFreshReadIsStructuredEvidence(t *testing.T) {
	result := withErrorContextEvidence(
		textResult("failed; external context file written", true),
		true,
	)
	for key, want := range map[string]any{
		"error_context_captured":          true,
		"error_context_fresh_ui_sampled":  true,
		"cause_unattributed":              true,
		"error_context_fresh_state_cause": "unattributed",
		"evidence_scope":                  "error_with_external_fresh_ui_context",
	} {
		if got := result.StructuredContent[key]; got != want {
			t.Fatalf("%s=%#v, want %#v; all=%#v", key, got, want,
				result.StructuredContent)
		}
	}

	unpersisted := withErrorContextEvidence(textResult("failed", true), false)
	if unpersisted.StructuredContent["error_context_captured"] != false ||
		unpersisted.StructuredContent["error_context_fresh_ui_sampled"] != true ||
		unpersisted.StructuredContent["evidence_scope"] !=
			"error_with_unpersisted_fresh_ui_sample" {
		t.Fatalf("an unpersisted sample must remain visible in structured evidence: %#v",
			unpersisted.StructuredContent)
	}
}

func TestActionOutcomeSeparatesAttemptFromPredicateAndTaskEffect(t *testing.T) {
	withoutExpect := withActionOutcome(
		textResult("ok", false),
		settlePlan{},
		settleOutcome{},
		nil,
		nil,
	).withEvidenceScope("delivery_or_local_ui")
	if withoutExpect.StructuredContent["action_attempted"] != true ||
		withoutExpect.StructuredContent["predicate_checked"] != false {
		t.Fatalf("an unverified action must report attempt separately from predicate state: %#v",
			withoutExpect.StructuredContent)
	}
	if _, exists := withoutExpect.StructuredContent["predicate_verified"]; exists {
		t.Fatalf("no expect means no predicate verdict, not predicate_verified=false: %#v",
			withoutExpect.StructuredContent)
	}

	withFailedExpect := withActionOutcome(
		textResult("expect did not hold", false),
		settlePlan{expect: &expectSpec{}},
		settleOutcome{verified: false, elapsedMS: 800},
		nil,
		nil,
	).withEvidenceScope("delivery_or_local_ui")
	for key, want := range map[string]any{
		"action_attempted":     true,
		"predicate_checked":    true,
		"predicate_verified":   false,
		"settled_after_ms":     800,
		"task_effect_verified": false,
	} {
		got := withFailedExpect.StructuredContent[key]
		if got != want {
			t.Fatalf("%s = %#v, want %#v in %#v", key, got, want, withFailedExpect.StructuredContent)
		}
	}
	for _, ambiguous := range []string{"operation_completed", "tool_call_completed"} {
		if _, exists := withFailedExpect.StructuredContent[ambiguous]; exists {
			t.Fatalf("predicate failure must not coexist with %q: %#v",
				ambiguous, withFailedExpect.StructuredContent)
		}
	}
}

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

func TestHiddenSettlePollsDoNotConsumeDeliveredDiagnosticState(t *testing.T) {
	source := mainGoSource(t)
	start := strings.Index(source, "func (s *service) refreshSnapshotForSettle(")
	end := strings.Index(source[start:], "func settleReadRequest(")
	if start < 0 || end < 0 {
		t.Fatal("missing dedicated hidden settle refresh path")
	}
	body := source[start : start+end]
	if strings.Contains(body, "s.dedupeDiagnosticNotes(") ||
		strings.Contains(body, "s.lastDiagnostics") {
		t.Fatalf("hidden settle poll consumed model-facing diagnostic state:\n%s", body)
	}
	if !strings.Contains(body, "s.rememberSnapshot(app, snapshot)") {
		t.Fatal("hidden settle poll must still advance the action cache")
	}
}

// 选择器的第二身份来源：description。
//
// 优先级照 Playwright 的 selector generator——它给 role+accessible-name 打 100 分，
// 给 label / alt-text 打 140–160（分数越低越优先），都远好于 nth= 的 10000 分。
// 我们的 element_index 就是 nth，是它排名倒数第二的定位方式。
//
// 加 description 这一级是实测决定的：七个应用 704 个节点里，只按 role+name 能唯一
// 指认的占 55%，加上 description 之后到 66%；GIMP 上从 57 涨到 114（翻一倍）。
func TestSelectorFallsBackToDescription(t *testing.T) {
	snapshot := &appSnapshot{Elements: []elementRecord{
		{Index: 3, ControlType: "push button", Name: "", Description: "Go back"},
		{Index: 4, ControlType: "push button", Name: "Save", Description: "Save the current file"},
		{Index: 9, ControlType: "push button", Name: "", Description: "Go forward"},
	}}

	// 没有名字的按钮，靠 description 指认。
	record, err := lookupElement(snapshot, `push button "Go back"`)
	if err != nil || record.Index != 3 {
		t.Fatalf("应当按 description 命中 3：%v %v", record, err)
	}

	// **name 命中时不得被 description 稀释成歧义。**
	// "Save" 既是 4 号的 name，也出现在它自己的 description 里；如果两级混在一起，
	// 一个精确的名字命中就会被一堆描述命中拖成"歧义"。
	record, err = lookupElement(snapshot, `push button "Save"`)
	if err != nil || record.Index != 4 {
		t.Fatalf("name 命中优先，不该报歧义：%v %v", record, err)
	}

	// description 这一级同样要能报歧义，而不是静默挑一个。
	ambiguous := &appSnapshot{Elements: []elementRecord{
		{Index: 1, ControlType: "push button", Description: "Close"},
		{Index: 2, ControlType: "menu item", Description: "Close"},
	}}
	if _, err := lookupElement(ambiguous, `"Close"`); err == nil ||
		!strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("description 命中多个也要报歧义：%v", err)
	}
}

func TestSemanticRefRoundTripUsesUniqueSemanticIdentity(t *testing.T) {
	snapshot := &appSnapshot{
		WindowHandle: "pid145/win1/dialog",
		Elements: []elementRecord{
			{Index: 3, ControlType: "push button", Name: "Save"},
			{Index: 4, ControlType: "toggle button", Name: "Menu", Description: "View options"},
			{Index: 5, ControlType: "toggle button", Name: "Menu", Description: "Show operations"},
		},
	}

	for _, index := range []int{3, 4, 5} {
		record := snapshot.Elements[index-3]
		ref := semanticRefForRecord(snapshot, record)
		if !strings.HasPrefix(ref, scopedSemanticRefPrefix) {
			t.Fatalf("record %d did not receive a semantic ref: %q", index, ref)
		}
		_, windowHandle, err := decodeSemanticRefScope(ref)
		if err != nil || windowHandle != snapshot.WindowHandle {
			t.Fatalf("semantic ref lost window scope: %q %q err=%v",
				ref, windowHandle, err)
		}
		resolved, err := lookupElement(snapshot, ref)
		if err != nil || resolved.Index != index {
			t.Fatalf("semantic ref %q resolved to %#v, err=%v", ref, resolved, err)
		}
	}

	duplicate := &appSnapshot{Elements: []elementRecord{
		{Index: 1, ControlType: "push button", Name: "OK"},
		{Index: 2, ControlType: "push button", Name: "OK"},
	}}
	if ref := semanticRefForRecord(duplicate, duplicate.Elements[0]); ref != "" {
		t.Fatalf("ambiguous identity must not receive a semantic ref: %q", ref)
	}
}

func TestGeneratedSemanticRefPreservesDescriptionIdentitySource(t *testing.T) {
	snapshot := &appSnapshot{
		WindowHandle: "pid145/win1/dialog",
		Elements: []elementRecord{
			{
				Index:       3,
				ControlType: "list item",
				Description: "Open the contents of your desktop in a folder",
			},
		},
	}
	ref := semanticRefForRecord(snapshot, snapshot.Elements[0])
	if !strings.HasPrefix(ref, scopedSemanticRefPrefix) {
		t.Fatalf("description-only record did not receive a semantic ref: %q", ref)
	}
	selector, _, err := decodeSemanticRefScope(ref)
	if err != nil {
		t.Fatal(err)
	}
	target, err := semanticTargetFromAddress(selector)
	if err != nil {
		t.Fatal(err)
	}
	if target.HasName || target.Name != "" {
		t.Fatalf("description was recoded as name: %#v", target)
	}
	if len(target.Predicates) != 1 ||
		target.Predicates[0].Kind != "desc" ||
		target.Predicates[0].Value !=
			"Open the contents of your desktop in a folder" {
		t.Fatalf("description provenance was lost: %#v", target)
	}
	resolved, err := lookupElement(snapshot, ref)
	if err != nil || resolved.Index != 3 {
		t.Fatalf("description ref %q resolved to %#v, err=%v",
			ref, resolved, err)
	}
}

func TestProviderBoundDiagnosticSelectorPreservesFallbackSource(t *testing.T) {
	for _, test := range []struct {
		name   string
		record *elementRecord
		want   string
	}{
		{
			name: "description",
			record: &elementRecord{
				ControlType: "list item",
				Description: "Open the desktop folder",
			},
			want: `listitem [desc="Open the desktop folder"]`,
		},
		{
			name: "placeholder",
			record: &elementRecord{
				ControlType: "text",
				Placeholder: "Search files",
			},
			want: `text [placeholder="Search files"]`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			role := canonicalRoleDescriptor(
				test.record.ControlType, nil,
			).ModelRole
			if got := semanticDiagnosticSelector(role, test.record); got != test.want {
				t.Fatalf("selector=%q, want %q", got, test.want)
			}
		})
	}
}

func TestSemanticRefFailsClosedAfterIdentityBecomesAmbiguous(t *testing.T) {
	initial := &appSnapshot{
		WindowHandle: "pid145/win0/frame",
		Elements: []elementRecord{
			{Index: 1, ControlType: "push button", Name: "Save"},
		},
	}
	ref := semanticRefForRecord(initial, initial.Elements[0])
	current := &appSnapshot{
		WindowHandle: initial.WindowHandle,
		Elements: []elementRecord{
			{Index: 7, ControlType: "push button", Name: "Save"},
			{Index: 8, ControlType: "push button", Name: "Save"},
		},
	}
	if _, err := lookupElement(current, ref); err == nil ||
		!strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("semantic ref must fail closed after identity becomes ambiguous: %v", err)
	}
	current.WindowHandle = "pid145/win1/dialog"
	if _, err := lookupElement(current, ref); err == nil ||
		!strings.Contains(err.Error(), "different top-level window") {
		t.Fatalf("window-scoped semantic ref must fail across windows: %v", err)
	}
}

func TestOrdinaryTreesDoNotPaySemanticRefMarkupCost(t *testing.T) {
	source := mainGoSource(t)
	if strings.Contains(source, "decorateSemanticRefs(response.Snapshot)") {
		t.Fatal("ordinary get_app_state snapshots must not decorate every tree line with refs")
	}
	if !strings.Contains(source, "refRegistry") {
		t.Fatal("short refs must be owned by the service-level registry")
	}
}

func TestStructuredAXTreeReconstructsNearestRetainedAncestor(t *testing.T) {
	snapshot := &appSnapshot{
		Elements: []elementRecord{
			{Index: 1, RuntimeID: []int{0}, ControlType: "frame", Name: "Editor"},
			// Provider pruning omitted path 0.2, so this record must attach to 0.
			{Index: 2, RuntimeID: []int{0, 2, 4}, ControlType: "push button", Name: "Save"},
		},
	}
	tree, err := buildAXSnapshotV1(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.CanonicalRoots) != 1 || len(tree.CanonicalRoots[0].Children) != 1 {
		t.Fatalf("nearest retained ancestor was not reconstructed: %#v", tree.CanonicalRoots)
	}
	if tree.CanonicalRoots[0].Children[0].ProviderIndex != 2 {
		t.Fatalf("wrong child attached: %#v", tree.CanonicalRoots[0].Children[0])
	}
}

func TestStructuredAXYAMLSupportsValueAndChildren(t *testing.T) {
	root := &AXNode{
		ID:   "0",
		Role: "group",
		Name: "Status: current",
		Properties: AXProperties{
			Value: "ready\nnow",
		},
		Children: []*AXNode{{
			ID:   "0.0",
			Role: "button",
			Name: "Save #1",
			Ref:  "r7",
		}},
	}
	root.Children[0].Parent = root
	snapshot := &AXSnapshotV1{
		SchemaVersion: "AXSnapshotV1",
		AppIdentity:   "test",
		WindowHandle:  "window",
		Revision:      "rev",
		Roots:         []*AXNode{root},
	}
	lines, err := RenderAXSnapshotYAML(snapshot, AXRenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(lines, "\n")
	for _, fragment := range []string{
		"ax:",
		`group "Status: current":`,
		`button "Save #1" [ref=r7]`,
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("typed YAML missing %q:\n%s", fragment, text)
		}
	}
	if strings.Contains(text, "\t") {
		t.Fatalf("YAML indentation must not contain tabs:\n%s", text)
	}
	if strings.Contains(text, `/value: "ready\nnow"`) {
		t.Fatalf("working group leaked provider default value:\n%s", text)
	}
	rendered, err := RenderAXSnapshotYAML(snapshot, AXRenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		`/value: "ready\nnow"`,
		"/provider-role",
		"/provider-actions",
	} {
		if strings.Contains(strings.Join(rendered, "\n"), forbidden) {
			t.Fatalf("model renderer leaked %q:\n%s",
				forbidden, strings.Join(rendered, "\n"))
		}
	}
}

func TestShortAXRefExpandsToWindowScopedSemanticRef(t *testing.T) {
	registry := NewRefRegistry(8)
	semantic := encodeScopedSemanticRef(
		`push button "Save"`,
		"pid145/win0/frame",
	)
	ref := registry.Acquire(
		"editor", "pid145/win0/frame", semantic,
		"button", "Save", "rev1",
	)
	if ref != "r1" {
		t.Fatalf("first registry ref = %q, want r1", ref)
	}
	entry, err := registry.Resolve(ref, "editor")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(entry.SemanticTarget, scopedSemanticRefPrefix) {
		t.Fatalf("short ref did not expand to a window-scoped semantic ref: %q", entry.SemanticTarget)
	}
	target, err := semanticTargetFromAddress(entry.SemanticTarget)
	if err != nil {
		t.Fatal(err)
	}
	if target.WindowHandle != "pid145/win0/frame" || target.Name != "Save" {
		t.Fatalf("expanded target lost identity: %#v", target)
	}
	if again := registry.Acquire(
		"editor", "pid145/win0/frame", semantic,
		"button", "Save", "rev2",
	); again != ref {
		t.Fatalf("same identity was reissued as %q instead of %q", again, ref)
	}
}

func TestModelAXRoleAliasesAreStandardAndProviderIndependent(t *testing.T) {
	for provider, want := range map[string]string{
		"push button": "button",
		"entry":       "textbox",
		"check box":   "checkbox",
		"list item":   "listitem",
		"page tab":    "tab",
	} {
		if got := canonicalRoleDescriptor(provider, nil).ModelRole; string(got) != want {
			t.Fatalf("model role for %q = %q, want %q", provider, got, want)
		}
	}
	if got := canonicalRoleDescriptor("internal frame", nil).ModelRole; got != RoleGeneric {
		t.Fatalf("unknown provider role fabricated canonical role %q", got)
	}
}

func TestStructuredSelectorAcceptsStandardOrProviderRole(t *testing.T) {
	snapshot := &appSnapshot{
		Elements: []elementRecord{
			{Index: 1, ControlType: "push button", Name: "Save"},
		},
	}
	for _, selector := range []string{`button "Save"`, `push button "Save"`} {
		record, err := lookupBySelector(snapshot, selector, "")
		if err != nil {
			t.Fatalf("selector %q failed: %v", selector, err)
		}
		if record.Index != 1 {
			t.Fatalf("selector %q resolved %#v", selector, record)
		}
	}
}

func TestRelationPruningRequiresUniqueLabelTarget(t *testing.T) {
	snapshot := &appSnapshot{
		Elements: []elementRecord{
			{Index: 1, RuntimeID: []int{0}, ControlType: "frame", Name: "Dialog"},
			{
				Index: 2, RuntimeID: []int{0, 0}, ControlType: "label", Name: "Width",
				ProviderBusName: ":1.10", ProviderObjectPath: "/label/width",
				ProcessGeneration: "p1",
			},
			{
				Index: 3, RuntimeID: []int{0, 1}, ControlType: "spin button", Name: "Width",
				RelationReadComplete: true,
				NameSource:           "labelled_by",
				Relations: map[string][]axRelationTarget{
					"labelled-by": {{
						Role: "label", Name: "Width",
						ProviderBusName:    ":1.10",
						ProviderObjectPath: "/label/width",
						ProcessGeneration:  "p1",
					}},
				},
			},
		},
	}
	tree, err := buildAXSnapshotV1(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRefRegistry(32)
	registry.AssignSnapshotRefs(tree)
	if err := distillAXSnapshot(tree); err != nil {
		t.Fatal(err)
	}
	if axProjectedIndices(tree.Roots)[2] {
		t.Fatal("unique relation-linked duplicate label was not pruned")
	}
	snapshot.Elements = append(snapshot.Elements, elementRecord{
		Index: 4, RuntimeID: []int{0, 2}, ControlType: "label", Name: "Width",
		ProviderBusName: ":1.10", ProviderObjectPath: "/label/other",
		ProcessGeneration: "p1",
	})
	tree, err = buildAXSnapshotV1(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	registry.AssignSnapshotRefs(tree)
	if err := distillAXSnapshot(tree); err != nil {
		t.Fatal(err)
	}
	if axProjectedIndices(tree.Roots)[2] {
		t.Fatal("exact provider-linked duplicate label was not pruned")
	}
	snapshot.Elements[2].RelationReadComplete = false
	tree, err = buildAXSnapshotV1(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	registry.AssignSnapshotRefs(tree)
	if err := distillAXSnapshot(tree); err != nil {
		t.Fatal(err)
	}
	if !axProjectedIndices(tree.Roots)[2] {
		t.Fatal("incomplete relation evidence must retain the label")
	}
}

func testIncrementalAXSnapshot(
	before,
	after *appSnapshot,
) ([]string, bool) {
	const windowRef = "w1"
	before.publicWindowRef = windowRef
	after.publicWindowRef = windowRef
	if before.axSnapshot != nil {
		before.axSnapshot.WindowRef = windowRef
	}
	if after.axSnapshot != nil {
		after.axSnapshot.WindowRef = windowRef
	}
	return incrementalAXSnapshot(before, after)
}

func TestStructuredAXDiffUsesYAMLAndStableRefs(t *testing.T) {
	beforeNode := &AXNode{
		ID: "0", Role: "checkbox", Name: "Alerts", Ref: "r4",
		SemanticFingerprint: "f1:alerts",
		States:              AXStates{Checked: CheckedTrue},
	}
	afterNode := &AXNode{
		ID: "0", Role: "checkbox", Name: "Alerts", Ref: "r4",
		SemanticFingerprint: "f1:alerts",
		States:              AXStates{Checked: CheckedFalse},
	}
	before := &appSnapshot{
		WindowHandle: "window-1",
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{beforeNode},
			Roots:          []*AXNode{beforeNode},
		},
	}
	after := &appSnapshot{
		WindowHandle: "window-1",
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{afterNode},
			Roots:          []*AXNode{afterNode},
		},
	}
	diff, ok := testIncrementalAXSnapshot(before, after)
	if !ok {
		t.Fatal("one-node state change should produce a structured diff")
	}
	text := strings.Join(diff, "\n")
	if !strings.Contains(text, "ax_diff:") ||
		!strings.Contains(text, `<changed> checkbox "Alerts"`) ||
		!strings.Contains(text, `[ref=r4]`) ||
		strings.Contains(text, "\t") {
		t.Fatalf("unexpected structured diff:\n%s", text)
	}
	for _, forbidden := range []string{"/before:", "/after:", "state_changed", "unchanged:"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("legacy flat diff field %q leaked:\n%s", forbidden, text)
		}
	}
}

func TestChangedChunkDoesNotExposeProviderIdentity(t *testing.T) {
	beforeNode := &AXNode{
		ID: "0", Role: "button", Name: "Save",
		ProviderIdentity:    ":1.42\x00/org/a11y/private/save\x0099123",
		SemanticFingerprint: "f1:save",
		States:              AXStates{Active: TriFalse},
	}
	afterNode := *beforeNode
	afterNode.States.Active = TriTrue
	before := &appSnapshot{
		WindowHandle: "window-1",
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{beforeNode},
			Roots:          []*AXNode{beforeNode},
		},
	}
	after := &appSnapshot{
		WindowHandle: "window-1",
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{&afterNode},
			Roots:          []*AXNode{&afterNode},
		},
	}
	diff, ok := testIncrementalAXSnapshot(before, after)
	if !ok {
		t.Fatal("provider-backed state change should produce a diff")
	}
	text := strings.Join(diff, "\n")
	for _, forbidden := range []string{":1.42", "/org/a11y/", "provider:"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("private provider identity %q leaked:\n%s", forbidden, text)
		}
	}
}

func TestChangedChunkGroupsPageReplacementAndReportsAbsentRefs(t *testing.T) {
	oldDocument := &AXNode{
		ID: "old", Role: "document", Name: "General Settings",
		Ref: "r100", SemanticFingerprint: "f1:general",
	}
	oldButton := &AXNode{
		ID: "old/button", Role: "button", Name: "Apply",
		Ref: "r101", SemanticFingerprint: "f1:apply",
		Parent: oldDocument,
	}
	oldDocument.Children = []*AXNode{oldButton}
	newDocument := &AXNode{
		ID: "new", Role: "document", Name: "Privacy Settings",
		SemanticFingerprint: "f1:privacy",
	}
	newButton := &AXNode{
		ID: "new/button", Role: "checkbox", Name: "Do Not Track",
		Ref: "r115", SemanticFingerprint: "f1:dnt",
		Parent: newDocument,
	}
	newDocument.Children = []*AXNode{newButton}
	before := &appSnapshot{
		WindowHandle: "window-1",
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{oldDocument},
			Roots:          []*AXNode{oldDocument},
		},
	}
	after := &appSnapshot{
		WindowHandle: "window-1",
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{newDocument},
			Roots:          []*AXNode{newDocument},
		},
	}
	diff, ok := testIncrementalAXSnapshot(before, after)
	if !ok {
		t.Fatal("page replacement should produce a changed chunk")
	}
	text := strings.Join(diff, "\n")
	if strings.Count(text, "<changed>") != 1 ||
		!strings.Contains(text, `document "Privacy Settings"`) ||
		!strings.Contains(text, `checkbox "Do Not Track" [ref=r115]`) ||
		!strings.Contains(text, "absent_refs: [r100, r101]") ||
		!strings.Contains(text, "absent_ref_status: absent_from_current_snapshot") {
		t.Fatalf("page replacement was not grouped:\n%s", text)
	}
}

func TestIncompleteAcquisitionNeverClaimsRemovalOrAbsentRef(t *testing.T) {
	beforeDocument := &AXNode{
		ID: "doc", Role: "document", Name: "Settings",
		Ref: "r1", SemanticFingerprint: "f1:settings",
	}
	beforeButton := &AXNode{
		ID: "delete", Role: "button", Name: "Delete",
		Ref: "r2", SemanticFingerprint: "f1:delete",
		Parent: beforeDocument,
	}
	beforeDocument.Children = []*AXNode{beforeButton}
	afterDocument := *beforeDocument
	afterDocument.Children = nil
	before := &appSnapshot{
		WindowHandle: "window-1",
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{beforeDocument},
			Roots:          []*AXNode{beforeDocument},
		},
	}
	after := &appSnapshot{
		WindowHandle: "window-1",
		TreeLines: []string{
			"ax:",
			` - document "Settings" [ref=r1]:`,
			`  - text "` + strings.Repeat("current partial content ", 20) + `"`,
		},
		Annotations: []axProviderAnnotation{{
			Kind:   "provider_omission",
			Count:  1,
			Reason: "time_budget",
		}},
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{&afterDocument},
			Roots:          []*AXNode{&afterDocument},
		},
	}
	diff, ok := testIncrementalAXSnapshot(before, after)
	if !ok {
		t.Fatal("incomplete acquisition should return bounded positive evidence")
	}
	text := strings.Join(diff, "\n")
	for _, forbidden := range []string{
		"<removed>",
		"absent_refs:",
		"effect: no_ax_change",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("incomplete acquisition fabricated %q:\n%s", forbidden, text)
		}
	}
	if !strings.Contains(text, "observation_incomplete: true") ||
		!strings.Contains(text, "effect: no_confirmed_ax_change") {
		t.Fatalf("incomplete acquisition was not labelled honestly:\n%s", text)
	}
}

func TestIncompleteBaselineCannotCreatePositiveDiff(t *testing.T) {
	beforeDocument := &AXNode{
		ID: "doc", Role: "document", Name: "Settings",
		Ref: "r1", SemanticFingerprint: "f1:settings",
	}
	afterDocument := *beforeDocument
	afterButton := &AXNode{
		ID: "save", Role: "button", Name: "Save",
		Ref: "r2", SemanticFingerprint: "f1:save",
		Parent: &afterDocument,
	}
	afterDocument.Children = []*AXNode{afterButton}
	before := &appSnapshot{
		WindowHandle: "window-1",
		Annotations: []axProviderAnnotation{{
			Kind: "provider_omission", Count: 1, Reason: "provider_probe_error",
		}},
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{beforeDocument},
			Roots:          []*AXNode{beforeDocument},
		},
	}
	after := &appSnapshot{
		WindowHandle: "window-1",
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{&afterDocument},
			Roots:          []*AXNode{&afterDocument},
		},
	}
	if diff, ok := testIncrementalAXSnapshot(before, after); ok {
		t.Fatalf("incomplete baseline fabricated a positive diff: %#v", diff)
	}
}

func TestIncompleteSnapshotDoesNotReplaceCompleteDiffBaseline(t *testing.T) {
	service := newService()
	complete := &appSnapshot{
		sourceApp:       "editor",
		WindowHandle:    "window-1",
		publicWindowRef: "w1",
		projection:      snapshotProjection{View: treeViewWorking, Scope: a11yScopeWindow},
	}
	incomplete := *complete
	incomplete.Annotations = []axProviderAnnotation{{
		Kind: "provider_omission", Count: 1, Reason: "provider_probe_error",
	}}
	service.rememberDeliveredSnapshot(complete)
	service.rememberDeliveredSnapshot(&incomplete)
	if got := service.deliveredSnapshotFor(complete); got != complete {
		t.Fatalf("incomplete snapshot replaced complete diff baseline: got %#v want %#v",
			got, complete)
	}
}

func TestRemovedChildUsesCurrentParentNameAfterRename(t *testing.T) {
	beforeDocument := &AXNode{
		ID: "doc", Role: "document", Name: "Old title",
		Ref: "r1", SemanticFingerprint: "f1:document",
	}
	beforeText := &AXNode{
		ID: "warning", Role: "text", Name: "Old warning",
		SemanticFingerprint: "f1:warning",
		Parent:              beforeDocument,
	}
	beforeDocument.Children = []*AXNode{beforeText}
	afterDocument := *beforeDocument
	afterDocument.Name = "New title"
	afterDocument.Children = nil
	before := &appSnapshot{
		WindowHandle: "window-1",
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{beforeDocument},
			Roots:          []*AXNode{beforeDocument},
		},
	}
	after := &appSnapshot{
		WindowHandle: "window-1",
		TreeLines: []string{
			"ax:",
			` - document "New title" [ref=r1]:`,
			`  - text "` + strings.Repeat("current content ", 20) + `"`,
		},
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{&afterDocument},
			Roots:          []*AXNode{&afterDocument},
		},
	}
	diff, ok := testIncrementalAXSnapshot(before, after)
	if !ok {
		t.Fatal("parent rename plus child removal should produce a changed chunk")
	}
	text := strings.Join(diff, "\n")
	if strings.Contains(text, `document "Old title"`) {
		t.Fatalf("removed child was attached to the obsolete parent name:\n%s", text)
	}
	if !strings.Contains(text, `document "New title"`) ||
		!strings.Contains(text, `<removed> text "Old warning"`) {
		t.Fatalf("removed child lost current parent context:\n%s", text)
	}
}

func TestSiblingReorderProducesChangedChunk(t *testing.T) {
	parentBefore := &AXNode{
		ID: "list", Role: "list", Name: "Results",
		Ref: "r1", SemanticFingerprint: "f1:list",
	}
	firstBefore := &AXNode{
		ID: "first", Role: "listitem", Name: "First",
		Ref: "r2", SemanticFingerprint: "f1:first", Parent: parentBefore,
	}
	secondBefore := &AXNode{
		ID: "second", Role: "listitem", Name: "Second",
		Ref: "r3", SemanticFingerprint: "f1:second", Parent: parentBefore,
	}
	parentBefore.Children = []*AXNode{firstBefore, secondBefore}

	parentAfter := *parentBefore
	firstAfter := *firstBefore
	secondAfter := *secondBefore
	firstAfter.Parent = &parentAfter
	secondAfter.Parent = &parentAfter
	parentAfter.Children = []*AXNode{&secondAfter, &firstAfter}

	before := &appSnapshot{
		WindowHandle: "window-1",
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{parentBefore},
			Roots:          []*AXNode{parentBefore},
		},
	}
	after := &appSnapshot{
		WindowHandle: "window-1",
		TreeLines: []string{
			"ax:",
			` - list "Results" [ref=r1]:`,
			`  - listitem "Second" [ref=r3]`,
			`  - listitem "First" [ref=r2]`,
		},
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{&parentAfter},
			Roots:          []*AXNode{&parentAfter},
		},
	}
	diff, ok := testIncrementalAXSnapshot(before, after)
	if !ok {
		t.Fatal("sibling reorder should produce a bounded changed chunk")
	}
	text := strings.Join(diff, "\n")
	if strings.Contains(text, "effect: no_ax_change") ||
		!strings.Contains(text, `<changed> list "Results"`) ||
		!strings.Contains(text, "/order_changed: true") ||
		!strings.Contains(text, `listitem "Second" [ref=r3] [position=1]`) ||
		!strings.Contains(text, `listitem "First" [ref=r2] [position=2]`) {
		t.Fatalf("sibling reorder was reported as unchanged:\n%s", text)
	}
}

func TestSiblingReorderPreservesChangedChildProperties(t *testing.T) {
	parentBefore := &AXNode{
		ID: "list", Role: "list", Name: "Results",
		Ref: "r1", SemanticFingerprint: "f1:list",
	}
	textboxBefore := &AXNode{
		ID: "query", Role: "textbox", Name: "Query",
		Ref: "r2", SemanticFingerprint: "f1:query",
		Properties: AXProperties{Value: "old"}, Parent: parentBefore,
	}
	buttonBefore := &AXNode{
		ID: "save", Role: "button", Name: "Save",
		Ref: "r3", SemanticFingerprint: "f1:save", Parent: parentBefore,
	}
	parentBefore.Children = []*AXNode{textboxBefore, buttonBefore}

	parentAfter := *parentBefore
	textboxAfter := *textboxBefore
	textboxAfter.Properties.Value = "new"
	buttonAfter := *buttonBefore
	textboxAfter.Parent = &parentAfter
	buttonAfter.Parent = &parentAfter
	parentAfter.Children = []*AXNode{&buttonAfter, &textboxAfter}

	before := &appSnapshot{
		WindowHandle:    "window-1",
		publicWindowRef: "w1",
		axSnapshot: &AXSnapshotV1{
			WindowRef:      "w1",
			CanonicalRoots: []*AXNode{parentBefore},
			Roots:          []*AXNode{parentBefore},
		},
	}
	after := &appSnapshot{
		WindowHandle:    "window-1",
		publicWindowRef: "w1",
		TreeLines: []string{
			"ax:",
			` - list "Results" [ref=r1]:`,
			`  - button "Save" [ref=r3]`,
			`  - textbox "Query" [ref=r2]:`,
			`   - /value: new`,
		},
		axSnapshot: &AXSnapshotV1{
			WindowRef:      "w1",
			CanonicalRoots: []*AXNode{&parentAfter},
			Roots:          []*AXNode{&parentAfter},
		},
	}
	diff, ok := testIncrementalAXSnapshot(before, after)
	if !ok {
		t.Fatal("reorder plus property change should produce a changed chunk")
	}
	text := strings.Join(diff, "\n")
	if !strings.Contains(text, "/order_changed: true") ||
		!strings.Contains(text, `textbox "Query" [ref=r2] [position=2]`) ||
		!strings.Contains(text, "/value: new") {
		t.Fatalf("reorder swallowed the changed child property:\n%s", text)
	}
}

func TestInsertionDoesNotHideSurvivorReorder(t *testing.T) {
	beforeParent := &AXNode{
		ID: "list", Role: "list", Name: "Results",
		Ref: "r1", SemanticFingerprint: "f1:list",
	}
	afterParent := *beforeParent
	makeItem := func(id, name, ref string, parent *AXNode) *AXNode {
		return &AXNode{
			ID: id, Role: "listitem", Name: name, Ref: ref,
			SemanticFingerprint: "f1:" + id, Parent: parent,
		}
	}
	aBefore := makeItem("a", "A", "r2", beforeParent)
	bBefore := makeItem("b", "B", "r3", beforeParent)
	cBefore := makeItem("c", "C", "r4", beforeParent)
	beforeParent.Children = []*AXNode{aBefore, bBefore, cBefore}
	xAfter := makeItem("x", "X", "r5", &afterParent)
	cAfter := makeItem("c", "C", "r4", &afterParent)
	aAfter := makeItem("a", "A", "r2", &afterParent)
	bAfter := makeItem("b", "B", "r3", &afterParent)
	afterParent.Children = []*AXNode{xAfter, cAfter, aAfter, bAfter}

	before := &appSnapshot{
		publicWindowRef: "w1",
		axSnapshot: &AXSnapshotV1{
			WindowRef: "w1", CanonicalRoots: []*AXNode{beforeParent},
			Roots: []*AXNode{beforeParent},
		},
	}
	after := &appSnapshot{
		publicWindowRef: "w1",
		TreeLines:       []string{"ax:", ` - list "Results" [ref=r1]`},
		axSnapshot: &AXSnapshotV1{
			WindowRef: "w1", CanonicalRoots: []*AXNode{&afterParent},
			Roots: []*AXNode{&afterParent},
		},
	}
	diff, ok := testIncrementalAXSnapshot(before, after)
	if !ok {
		t.Fatal("insertion plus survivor reorder should produce a changed chunk")
	}
	text := strings.Join(diff, "\n")
	if !strings.Contains(text, "/order_changed: true") ||
		!strings.Contains(text, `<changed> listitem "X" [ref=r5] [position=1]`) ||
		!strings.Contains(text, `listitem "C" [ref=r4] [position=2]`) {
		t.Fatalf("insertion hid survivor reorder:\n%s", text)
	}
}

func TestLargeSiblingReorderUsesBoundedSummary(t *testing.T) {
	beforeParent := &AXNode{
		ID: "list", Role: "list", Name: "Results",
		Ref: "r1", SemanticFingerprint: "f1:list",
	}
	afterParent := *beforeParent
	for index := 0; index < 200; index++ {
		id := fmt.Sprintf("item-%03d", index)
		beforeParent.Children = append(beforeParent.Children, &AXNode{
			ID: id, Role: "textbox", Name: id,
			Ref:                 "r" + strconv.Itoa(index+2),
			SemanticFingerprint: "f1:" + id, Parent: beforeParent,
		})
	}
	for index := len(beforeParent.Children) - 1; index >= 0; index-- {
		clone := *beforeParent.Children[index]
		clone.Parent = &afterParent
		afterParent.Children = append(afterParent.Children, &clone)
	}
	before := &appSnapshot{
		publicWindowRef: "w1",
		axSnapshot: &AXSnapshotV1{
			WindowRef: "w1", CanonicalRoots: []*AXNode{beforeParent},
			Roots: []*AXNode{beforeParent},
		},
	}
	after := &appSnapshot{
		publicWindowRef: "w1",
		TreeLines:       []string{"ax:", ` - list "Results" [ref=r1]`},
		axSnapshot: &AXSnapshotV1{
			WindowRef: "w1", CanonicalRoots: []*AXNode{&afterParent},
			Roots: []*AXNode{&afterParent},
		},
	}
	diff, ok := testIncrementalAXSnapshot(before, after)
	if !ok {
		t.Fatal("large reorder should remain a bounded changed chunk")
	}
	text := strings.Join(diff, "\n")
	if len(text) > 6000 ||
		!strings.Contains(text, "/order_changed: moved=") ||
		!strings.Contains(text, "/positions_omitted:") {
		t.Fatalf("large reorder was not compacted:\n%s", text)
	}
}

func TestLargeSiblingReorderWithManyPropertyChangesStaysBounded(t *testing.T) {
	beforeParent := &AXNode{
		ID: "list", Role: "list", Name: "Results",
		Ref: "r1", SemanticFingerprint: "f1:list",
	}
	afterParent := *beforeParent
	for index := 0; index < 200; index++ {
		id := fmt.Sprintf("item-%03d", index)
		beforeParent.Children = append(beforeParent.Children, &AXNode{
			ID: id, Role: "textbox", Name: id,
			Ref:                 "r" + strconv.Itoa(index+2),
			SemanticFingerprint: "f1:" + id,
			Properties:          AXProperties{Value: "old"},
			Parent:              beforeParent,
		})
	}
	for index := len(beforeParent.Children) - 1; index >= 0; index-- {
		clone := *beforeParent.Children[index]
		if index < 80 {
			clone.Properties.Value = "new"
		}
		clone.Parent = &afterParent
		afterParent.Children = append(afterParent.Children, &clone)
	}
	before := &appSnapshot{
		publicWindowRef: "w1",
		axSnapshot: &AXSnapshotV1{
			WindowRef: "w1", CanonicalRoots: []*AXNode{beforeParent},
			Roots: []*AXNode{beforeParent},
		},
	}
	after := &appSnapshot{
		publicWindowRef: "w1",
		TreeLines:       []string{"ax:", ` - list "Results" [ref=r1]`},
		axSnapshot: &AXSnapshotV1{
			WindowRef: "w1", CanonicalRoots: []*AXNode{&afterParent},
			Roots: []*AXNode{&afterParent},
		},
	}
	diff, ok := testIncrementalAXSnapshot(before, after)
	if !ok {
		t.Fatal("large reorder plus property changes should stay incremental")
	}
	text := strings.Join(diff, "\n")
	if len(text) > 6000 ||
		!strings.Contains(text, "/order_changed: moved=") ||
		!strings.Contains(text, "/value: new") ||
		!strings.Contains(text, "/positions_omitted:") ||
		!strings.Contains(text, "/semantic_changes_omitted:") {
		t.Fatalf("large changed reorder was not compacted honestly:\n%s", text)
	}
}

func TestLargeSiblingReorderPrioritizesMiddlePropertyChange(t *testing.T) {
	beforeParent := &AXNode{
		ID: "list", Role: "list", Name: "Results",
		Ref: "r1", SemanticFingerprint: "f1:list",
	}
	afterParent := *beforeParent
	for index := 0; index < 200; index++ {
		id := fmt.Sprintf("item-%03d", index)
		beforeParent.Children = append(beforeParent.Children, &AXNode{
			ID: id, Role: "textbox", Name: id,
			Ref:                 "r" + strconv.Itoa(index+2),
			SemanticFingerprint: "f1:" + id,
			Properties:          AXProperties{Value: "old"},
			Parent:              beforeParent,
		})
	}
	for index := len(beforeParent.Children) - 1; index >= 0; index-- {
		clone := *beforeParent.Children[index]
		if index == 100 {
			clone.Properties.Value = "middle-change"
		}
		clone.Parent = &afterParent
		afterParent.Children = append(afterParent.Children, &clone)
	}
	before := &appSnapshot{
		publicWindowRef: "w1",
		axSnapshot: &AXSnapshotV1{
			WindowRef: "w1", CanonicalRoots: []*AXNode{beforeParent},
			Roots: []*AXNode{beforeParent},
		},
	}
	after := &appSnapshot{
		publicWindowRef: "w1",
		TreeLines:       []string{"ax:", ` - list "Results" [ref=r1]`},
		axSnapshot: &AXSnapshotV1{
			WindowRef: "w1", CanonicalRoots: []*AXNode{&afterParent},
			Roots: []*AXNode{&afterParent},
		},
	}
	diff, ok := testIncrementalAXSnapshot(before, after)
	if !ok {
		t.Fatal("large reorder plus middle property change should stay incremental")
	}
	text := strings.Join(diff, "\n")
	if !strings.Contains(text, `textbox "item-100"`) ||
		!strings.Contains(text, "/value: middle-change") {
		t.Fatalf("order sampling hid the only semantic change:\n%s", text)
	}
}

func TestCanonicalRootReorderProducesChangedChunk(t *testing.T) {
	firstBefore := &AXNode{
		ID: "first", Role: "document", Name: "First",
		Ref: "r1", SemanticFingerprint: "f1:first",
	}
	secondBefore := &AXNode{
		ID: "second", Role: "document", Name: "Second",
		Ref: "r2", SemanticFingerprint: "f1:second",
	}
	firstAfter := *firstBefore
	secondAfter := *secondBefore
	before := &appSnapshot{
		publicWindowRef: "w1",
		axSnapshot: &AXSnapshotV1{
			WindowRef:      "w1",
			CanonicalRoots: []*AXNode{firstBefore, secondBefore},
			Roots:          []*AXNode{firstBefore, secondBefore},
		},
	}
	after := &appSnapshot{
		publicWindowRef: "w1",
		TreeLines: []string{
			"ax:",
			` - document "Second" [ref=r2]`,
			` - document "First" [ref=r1]`,
		},
		axSnapshot: &AXSnapshotV1{
			WindowRef:      "w1",
			CanonicalRoots: []*AXNode{&secondAfter, &firstAfter},
			Roots:          []*AXNode{&secondAfter, &firstAfter},
		},
	}
	diff, ok := testIncrementalAXSnapshot(before, after)
	if !ok {
		t.Fatal("canonical root reorder should produce a changed chunk")
	}
	text := strings.Join(diff, "\n")
	if !strings.Contains(text, "<changed> root") ||
		!strings.Contains(text, "/order_changed: true") ||
		!strings.Contains(text, `document "Second" [ref=r2] [position=1]`) {
		t.Fatalf("root reorder was reported incorrectly:\n%s", text)
	}
}

func TestWeakDuplicateCohortDoesNotInventObjectContinuity(t *testing.T) {
	beforeParent := &AXNode{
		ID: "list-before", Role: "list", Name: "Options",
		SemanticFingerprint: "f1:list",
	}
	afterParent := &AXNode{
		ID: "list-after", Role: "list", Name: "Options",
		SemanticFingerprint: "f1:list",
	}
	makeOption := func(id, value string, parent *AXNode) *AXNode {
		return &AXNode{
			ID: id, Role: "textbox", Name: "Option",
			Properties:          AXProperties{Value: value},
			SemanticFingerprint: "f1:duplicate-option",
			Parent:              parent,
		}
	}
	beforeParent.Children = []*AXNode{
		makeOption("before-a", "A", beforeParent),
		makeOption("before-b", "B", beforeParent),
	}
	afterParent.Children = []*AXNode{
		makeOption("after-b", "B", afterParent),
		makeOption("after-c", "C", afterParent),
	}

	changes := diffAXSnapshots(
		&AXSnapshotV1{CanonicalRoots: []*AXNode{beforeParent}},
		&AXSnapshotV1{CanonicalRoots: []*AXNode{afterParent}},
	)
	added, removed, propertyChanged := 0, 0, 0
	for _, change := range changes {
		switch change.Kind {
		case AXDiffAdded:
			added++
		case AXDiffRemoved:
			removed++
		case AXDiffPropertyChanged, AXDiffStateChanged, AXDiffChanged:
			if change.After != nil && change.After.Name == "Option" {
				propertyChanged++
			}
		}
	}
	if added != 1 || removed != 1 || propertyChanged != 0 {
		t.Fatalf(
			"weak duplicate continuity was invented: added=%d removed=%d changed=%d changes=%#v",
			added,
			removed,
			propertyChanged,
			changes,
		)
	}
}

func TestWeakDuplicateContainersRequireMatchingSubtrees(t *testing.T) {
	beforeParent := &AXNode{
		ID: "before-root", Role: "list", Name: "Options",
		SemanticFingerprint: "f1:list",
	}
	afterParent := &AXNode{
		ID: "after-root", Role: "list", Name: "Options",
		SemanticFingerprint: "f1:list",
	}
	makeGroup := func(id, childName string, parent *AXNode) *AXNode {
		group := &AXNode{
			ID: id, Role: "group", Name: "Option",
			SemanticFingerprint: "f1:duplicate-group",
			Parent:              parent,
		}
		group.Children = []*AXNode{{
			ID: id + "-child", Role: "text", Name: childName,
			SemanticFingerprint: "f1:text:" + childName,
			Parent:              group,
		}}
		return group
	}
	beforeParent.Children = []*AXNode{
		makeGroup("before-a", "A", beforeParent),
		makeGroup("before-b", "B", beforeParent),
	}
	afterParent.Children = []*AXNode{
		makeGroup("after-b", "B", afterParent),
		makeGroup("after-c", "C", afterParent),
	}
	changes := diffAXSnapshots(
		&AXSnapshotV1{CanonicalRoots: []*AXNode{beforeParent}},
		&AXSnapshotV1{CanonicalRoots: []*AXNode{afterParent}},
	)
	added, removed := 0, 0
	for _, change := range changes {
		if change.After != nil && change.After.Role == "group" &&
			change.Kind == AXDiffAdded {
			added++
		}
		if change.Before != nil && change.Before.Role == "group" &&
			change.Kind == AXDiffRemoved {
			removed++
		}
	}
	if added != 1 || removed != 1 {
		t.Fatalf(
			"weak duplicate containers invented continuity: added=%d removed=%d changes=%#v",
			added,
			removed,
			changes,
		)
	}
}

func TestWeakDuplicateContainersUseDescendantStrongIdentity(t *testing.T) {
	beforeParent := &AXNode{
		ID: "before-root", Role: "list", Name: "Rows",
		SemanticFingerprint: "f1:list",
	}
	afterParent := &AXNode{
		ID: "after-root", Role: "list", Name: "Rows",
		SemanticFingerprint: "f1:list",
	}
	makeGroup := func(id, childRef string, parent *AXNode) *AXNode {
		group := &AXNode{
			ID: id, Role: "group", Name: "Row",
			SemanticFingerprint: "f1:duplicate-row",
			Parent:              parent,
		}
		group.Children = []*AXNode{{
			ID: id + "-child", Role: "button", Name: "Delete", Ref: childRef,
			SemanticFingerprint: "f1:delete",
			Parent:              group,
		}}
		return group
	}
	beforeParent.Children = []*AXNode{
		makeGroup("before-r2", "r2", beforeParent),
		makeGroup("before-r3", "r3", beforeParent),
	}
	afterParent.Children = []*AXNode{
		makeGroup("after-r3", "r3", afterParent),
		makeGroup("after-r2", "r2", afterParent),
	}
	changes := diffAXSnapshots(
		&AXSnapshotV1{CanonicalRoots: []*AXNode{beforeParent}},
		&AXSnapshotV1{CanonicalRoots: []*AXNode{afterParent}},
	)
	moved, added, removed := 0, 0, 0
	for _, change := range changes {
		switch change.Kind {
		case AXDiffMoved:
			moved++
		case AXDiffAdded:
			added++
		case AXDiffRemoved:
			removed++
		}
	}
	if moved == 0 || added != 0 || removed != 0 {
		t.Fatalf(
			"descendant strong identity was not preserved across reorder: moved=%d added=%d removed=%d changes=%#v",
			moved,
			added,
			removed,
			changes,
		)
	}
}

func TestRootReorderDoesNotDuplicateChangedDescendant(t *testing.T) {
	firstBefore := &AXNode{
		ID: "first", Role: "textbox", Name: "First",
		Ref: "r1", SemanticFingerprint: "f1:first",
		Properties: AXProperties{Value: "old"},
	}
	secondBefore := &AXNode{
		ID: "second", Role: "button", Name: "Second",
		Ref: "r2", SemanticFingerprint: "f1:second",
	}
	firstAfter := *firstBefore
	firstAfter.Properties.Value = "new"
	secondAfter := *secondBefore
	before := &appSnapshot{
		publicWindowRef: "w1",
		axSnapshot: &AXSnapshotV1{
			WindowRef:      "w1",
			CanonicalRoots: []*AXNode{firstBefore, secondBefore},
			Roots:          []*AXNode{firstBefore, secondBefore},
		},
	}
	after := &appSnapshot{
		publicWindowRef: "w1",
		TreeLines: []string{
			"ax:",
			` - button "Second" [ref=r2]`,
			` - textbox "First" [ref=r1]:`,
			`  - /value: new`,
		},
		axSnapshot: &AXSnapshotV1{
			WindowRef:      "w1",
			CanonicalRoots: []*AXNode{&secondAfter, &firstAfter},
			Roots:          []*AXNode{&secondAfter, &firstAfter},
		},
	}
	diff, ok := testIncrementalAXSnapshot(before, after)
	if !ok {
		t.Fatal("root reorder plus property change should produce a changed chunk")
	}
	text := strings.Join(diff, "\n")
	if count := strings.Count(text, `textbox "First" [ref=r1]`); count != 1 {
		t.Fatalf("root order chunk duplicated the changed node %d times:\n%s", count, text)
	}
	if !strings.Contains(text, "/value: new") {
		t.Fatalf("root order chunk lost the changed value:\n%s", text)
	}
}

func TestIncrementalAXSnapshotRequiresLogicalWindowIdentity(t *testing.T) {
	beforeNode := &AXNode{
		ID: "save", Role: "button", Name: "Save", Ref: "r1",
		SemanticFingerprint: "f1:save",
	}
	afterNode := &AXNode{
		ID: "delete", Role: "button", Name: "Delete", Ref: "r2",
		SemanticFingerprint: "f1:delete",
	}
	before := &appSnapshot{
		App: appDescriptor{Name: "Editor", PID: 7},
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{beforeNode},
			Roots:          []*AXNode{beforeNode},
		},
	}
	after := &appSnapshot{
		App: appDescriptor{Name: "Editor", PID: 7},
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{afterNode},
			Roots:          []*AXNode{afterNode},
		},
	}
	if diff, ok := incrementalAXSnapshot(before, after); ok {
		t.Fatalf(
			"snapshots without a reliable logical window ref must not be diffed: %#v",
			diff,
		)
	}

	before.publicWindowRef = "w1"
	after.publicWindowRef = "w2"
	if diff, ok := incrementalAXSnapshot(before, after); ok {
		t.Fatalf("different logical windows must not be diffed: %#v", diff)
	}
}

func TestIncompletePresentedSnapshotDoesNotDriveContinuity(t *testing.T) {
	service := newService()
	complete := &appSnapshot{
		App:             appDescriptor{Name: "Editor", PID: 7},
		publicWindowRef: "w1",
		SnapshotID:      "complete-a",
		sourceApp:       "editor",
		projection:      snapshotProjection{View: treeViewWorking},
	}
	partial := *complete
	partial.SnapshotID = "partial-b"
	partial.Annotations = []axProviderAnnotation{{
		Kind: "provider_omission", Count: 1, Reason: "provider_probe_error",
	}}
	service.rememberDeliveredSnapshot(complete)
	service.rememberDeliveredSnapshot(&partial)

	current := *complete
	baseline := service.deliveredSnapshotFor(&current)
	if baseline != complete {
		t.Fatalf(
			"partial observation replaced the last complete continuity baseline: got %#v want %#v",
			baseline,
			complete,
		)
	}
	notes := continuityNotes(baseline, &current)
	if len(notes) != 1 || !current.identicalToPrevious {
		t.Fatalf(
			"complete A -> partial B -> complete A must compare against A: notes=%v current=%#v",
			notes,
			current,
		)
	}
}

func TestIncompleteCurrentSnapshotCannotClaimUnchanged(t *testing.T) {
	previous := &appSnapshot{
		publicWindowRef: "w1",
		SnapshotID:      "same",
	}
	current := &appSnapshot{
		publicWindowRef: "w1",
		SnapshotID:      "same",
		Annotations: []axProviderAnnotation{{
			Kind: "provider_omission", Count: 1, Reason: "visible_cell_budget",
		}},
	}

	if notes := continuityNotes(previous, current); len(notes) != 0 {
		t.Fatalf("an incomplete current read cannot produce continuity claims: %v", notes)
	}
	if current.comparedToPrevious || current.identicalToPrevious {
		t.Fatalf(
			"an incomplete current read cannot be marked compared or unchanged: %#v",
			current,
		)
	}
}

func TestSnapshotCachesRequireLogicalWindowRef(t *testing.T) {
	first := &appSnapshot{
		App:        appDescriptor{Name: "Editor", PID: 7},
		SnapshotID: "first",
	}
	second := &appSnapshot{
		App:        appDescriptor{Name: "Editor", PID: 7},
		SnapshotID: "second",
	}
	if deliveredSnapshotKey(first) != "" || deliveredSnapshotKey(second) != "" {
		t.Fatal("app name and PID must not substitute for logical window identity")
	}
	service := newService()
	service.rememberDeliveredSnapshot(first)
	if got := service.deliveredSnapshotFor(second); got != nil {
		t.Fatalf("snapshots without wN must not share a diff baseline: %#v", got)
	}
}

func TestRemovedChildPrefersStrongParentIdentityOverReusedRuntimePath(t *testing.T) {
	beforeDocument := &AXNode{
		ID: "0", Role: "document", Name: "Old title",
		Ref: "r1", SemanticFingerprint: "f1:document",
	}
	beforeText := &AXNode{
		ID: "0/0", Role: "text", Name: "Old warning",
		SemanticFingerprint: "f1:warning",
		Parent:              beforeDocument,
	}
	beforeDocument.Children = []*AXNode{beforeText}

	unrelated := &AXNode{
		ID: "0", Role: "document", Name: "Unrelated document",
		SemanticFingerprint: "f1:unrelated",
	}
	currentDocument := &AXNode{
		ID: "1", Role: "document", Name: "New title",
		Ref: "r1", SemanticFingerprint: "f1:document",
	}
	before := &appSnapshot{
		WindowHandle: "window-1",
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{beforeDocument},
			Roots:          []*AXNode{beforeDocument},
		},
	}
	after := &appSnapshot{
		WindowHandle: "window-1",
		TreeLines: []string{
			"ax:",
			` - document "Unrelated document"`,
			` - document "New title" [ref=r1]`,
			`  - text "` + strings.Repeat("current content ", 20) + `"`,
		},
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{unrelated, currentDocument},
			Roots:          []*AXNode{unrelated, currentDocument},
		},
	}
	diff, ok := testIncrementalAXSnapshot(before, after)
	if !ok {
		t.Fatal("runtime-path reuse should still produce a bounded changed chunk")
	}
	text := strings.Join(diff, "\n")
	if strings.Contains(text, `document "Unrelated document":`+"\n  - <removed>") {
		t.Fatalf("removed child was attached through a reused runtime path:\n%s", text)
	}
	if !strings.Contains(text, `document "New title"`) ||
		!strings.Contains(text, `<removed> text "Old warning"`) {
		t.Fatalf("removed child did not follow the strong parent identity:\n%s", text)
	}
}

func TestRemovedChildDoesNotTrustWeakRuntimePathReuse(t *testing.T) {
	beforeGroup := &AXNode{
		ID: "0", Role: "group", Name: "Old panel",
		SemanticFingerprint: "f1:old-panel",
	}
	beforeText := &AXNode{
		ID: "0/0", Role: "text", Name: "Warning",
		SemanticFingerprint: "f1:warning", Parent: beforeGroup,
	}
	beforeGroup.Children = []*AXNode{beforeText}
	unrelated := &AXNode{
		ID: "0", Role: "group", Name: "Unrelated panel",
		SemanticFingerprint: "f1:unrelated-panel",
	}
	before := &appSnapshot{
		WindowHandle: "window-1",
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{beforeGroup},
			Roots:          []*AXNode{beforeGroup},
		},
	}
	after := &appSnapshot{
		WindowHandle: "window-1",
		TreeLines: []string{
			"ax:",
			` - group "Unrelated panel"`,
			`  - text "` + strings.Repeat("current content ", 20) + `"`,
		},
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{unrelated},
			Roots:          []*AXNode{unrelated},
		},
	}
	diff, ok := testIncrementalAXSnapshot(before, after)
	if !ok {
		t.Fatal("weak runtime-path reuse should produce bounded historical evidence")
	}
	text := strings.Join(diff, "\n")
	if strings.Contains(text, `group "Unrelated panel":`+"\n  - <removed>") {
		t.Fatalf("removed child was attached to an unrelated reused path:\n%s", text)
	}
	if !strings.Contains(text, `<removed> group "Old panel"`) {
		t.Fatalf("unproven parent continuity was not kept in removed history:\n%s", text)
	}
}

func TestChangedChunkUsesCanonicalTruthNotProjectionMembership(t *testing.T) {
	canonical := &AXNode{
		ID: "0", Role: "text", Name: "Stable",
		SemanticFingerprint: "f1:stable",
	}
	before := &appSnapshot{
		WindowHandle: "window-1",
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{canonical},
			Roots:          []*AXNode{canonical},
		},
	}
	after := &appSnapshot{
		WindowHandle: "window-1",
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{canonical},
			Roots:          nil,
		},
	}
	diff, ok := testIncrementalAXSnapshot(before, after)
	if !ok {
		t.Fatal("projection-only change should produce compact no-change evidence")
	}
	if text := strings.Join(diff, "\n"); text != "ax_diff:\n effect: no_ax_change" {
		t.Fatalf("projection churn fabricated semantic changes:\n%s", text)
	}
}

func TestChangedChunkFallsBackWhenItCostsMoreThanCurrentTree(t *testing.T) {
	beforeNode := &AXNode{
		ID: "0", Role: "document", Name: "Old",
		SemanticFingerprint: "f1:old",
	}
	afterNode := &AXNode{
		ID: "1", Role: "document", Name: strings.Repeat("New", 100),
		SemanticFingerprint: "f1:new",
	}
	before := &appSnapshot{
		WindowHandle: "window-1",
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{beforeNode},
			Roots:          []*AXNode{beforeNode},
		},
	}
	after := &appSnapshot{
		WindowHandle: "window-1",
		TreeLines:    []string{"ax:", ` - document "New"`},
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{afterNode},
			Roots:          []*AXNode{afterNode},
		},
	}
	if diff, ok := testIncrementalAXSnapshot(before, after); ok {
		t.Fatalf("oversized diff should fall back to current tree: %#v", diff)
	}
}

func TestTypedSnapshotNeverFallsBackToLegacyLineDiff(t *testing.T) {
	beforeNode := &AXNode{
		ID: "0", Role: "document", Name: "Old",
		SemanticFingerprint: "f1:old",
	}
	afterNode := &AXNode{
		ID: "1", Role: "document", Name: strings.Repeat("New", 100),
		SemanticFingerprint: "f1:new",
	}
	before := &appSnapshot{
		WindowHandle: "window-1",
		TreeLines:    []string{"old rendered line"},
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{beforeNode},
			Roots:          []*AXNode{beforeNode},
		},
	}
	after := &appSnapshot{
		WindowHandle: "window-1",
		TreeLines:    []string{"new rendered line"},
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{afterNode},
			Roots:          []*AXNode{afterNode},
		},
	}
	if diff, ok := incrementalTree(before, after); ok {
		t.Fatalf("typed oversized diff must request the current tree, not legacy text diff: %#v", diff)
	}
}

func TestChangedChunkKeepsSeparatedChangesInTreeOrder(t *testing.T) {
	beforeFirst := &AXNode{
		ID: "first", Role: "checkbox", Name: "First", Ref: "r2",
		SemanticFingerprint: "f1:first",
		States:              AXStates{Checked: CheckedFalse},
	}
	beforeSecond := &AXNode{
		ID: "second", Role: "checkbox", Name: "Second", Ref: "r9",
		SemanticFingerprint: "f1:second",
		States:              AXStates{Checked: CheckedFalse},
	}
	afterFirst := *beforeFirst
	afterFirst.States.Checked = CheckedTrue
	afterSecond := *beforeSecond
	afterSecond.States.Checked = CheckedTrue
	before := &appSnapshot{
		WindowHandle: "window-1",
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{beforeFirst, beforeSecond},
			Roots:          []*AXNode{beforeFirst, beforeSecond},
		},
	}
	after := &appSnapshot{
		WindowHandle: "window-1",
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{&afterFirst, &afterSecond},
			Roots:          []*AXNode{&afterFirst, &afterSecond},
		},
	}
	diff, ok := testIncrementalAXSnapshot(before, after)
	if !ok {
		t.Fatal("separated state changes should produce changed chunks")
	}
	text := strings.Join(diff, "\n")
	first := strings.Index(text, `checkbox "First"`)
	second := strings.Index(text, `checkbox "Second"`)
	if first < 0 || second < 0 || first >= second {
		t.Fatalf("changed chunks lost tree order:\n%s", text)
	}
}

func TestChangedChunkRemovedOnlySubtreeDoesNotRenderOldTarget(t *testing.T) {
	oldDialog := &AXNode{
		ID: "dialog", Role: "dialog", Name: "Confirm",
		Ref: "r30", SemanticFingerprint: "f1:confirm",
	}
	oldButton := &AXNode{
		ID: "dialog/remove", Role: "button", Name: "Remove",
		Ref: "r31", SemanticFingerprint: "f1:remove",
		Parent: oldDialog,
	}
	oldDialog.Children = []*AXNode{oldButton}
	stable := &AXNode{
		ID: "stable", Role: "document", Name: "Inbox",
		SemanticFingerprint: "f1:inbox",
	}
	before := &appSnapshot{
		WindowHandle: "window-1",
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{stable, oldDialog},
			Roots:          []*AXNode{stable, oldDialog},
		},
	}
	after := &appSnapshot{
		WindowHandle: "window-1",
		TreeLines: []string{
			"ax:",
			` - document "Inbox":`,
			`  - text "` + strings.Repeat("Current inbox context ", 20) + `"`,
		},
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{stable},
			Roots:          []*AXNode{stable},
		},
	}
	diff, ok := testIncrementalAXSnapshot(before, after)
	if !ok {
		t.Fatal("removed-only subtree should produce lifecycle evidence")
	}
	text := strings.Join(diff, "\n")
	if strings.Contains(text, `<changed> dialog "Confirm"`) ||
		strings.Contains(text, `<changed> button "Remove"`) {
		t.Fatalf("removed target was rendered as currently actionable:\n%s", text)
	}
	if !strings.Contains(text, "absent_refs: [r30, r31]") {
		t.Fatalf("removed refs were not reported:\n%s", text)
	}
}

func TestChangedChunkMovedRefIsNotReportedAbsent(t *testing.T) {
	beforeLeft := &AXNode{
		ID: "left", Role: "group", Name: "Left",
		SemanticFingerprint: "f1:left",
	}
	beforeRight := &AXNode{
		ID: "right", Role: "group", Name: "Right",
		SemanticFingerprint: "f1:right",
	}
	beforeButton := &AXNode{
		ID: "save-old", Role: "button", Name: "Save", Ref: "r5",
		SemanticFingerprint: "f1:save",
		Parent:              beforeLeft,
	}
	beforeLeft.Children = []*AXNode{beforeButton}
	afterLeft := &AXNode{
		ID: "left", Role: "group", Name: "Left",
		SemanticFingerprint: "f1:left",
	}
	afterRight := &AXNode{
		ID: "right", Role: "group", Name: "Right",
		SemanticFingerprint: "f1:right",
	}
	afterButton := &AXNode{
		ID: "save-new", Role: "button", Name: "Save", Ref: "r5",
		SemanticFingerprint: "f1:save",
		Parent:              afterRight,
	}
	afterRight.Children = []*AXNode{afterButton}
	before := &appSnapshot{
		WindowHandle: "window-1",
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{beforeLeft, beforeRight},
			Roots:          []*AXNode{beforeLeft, beforeRight},
		},
	}
	after := &appSnapshot{
		WindowHandle: "window-1",
		TreeLines: []string{
			"ax:",
			` - group "Left"`,
			` - group "Right":`,
			`  - button "Save" [ref=r5]`,
			`  - text "` + strings.Repeat("context ", 30) + `"`,
		},
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{afterLeft, afterRight},
			Roots:          []*AXNode{afterLeft, afterRight},
		},
	}
	diff, ok := testIncrementalAXSnapshot(before, after)
	if !ok {
		t.Fatal("moved current ref should produce a current changed chunk")
	}
	text := strings.Join(diff, "\n")
	if !strings.Contains(text, `button "Save" [ref=r5]`) {
		t.Fatalf("moved current ref was not rendered:\n%s", text)
	}
	if strings.Contains(text, "absent_refs: [r5]") {
		t.Fatalf("current ref was incorrectly reported absent:\n%s", text)
	}
}

func TestChangedChunkReportsRemovedVisibleTextWithParentContext(t *testing.T) {
	beforeDocument := &AXNode{
		ID: "doc", Role: "document", Name: "Checkout",
		SemanticFingerprint: "f1:checkout",
	}
	removedText := &AXNode{
		ID: "failure", Role: "text", Name: "Payment failed",
		SemanticFingerprint: "f1:payment-failed",
		Parent:              beforeDocument,
	}
	beforeDocument.Children = []*AXNode{removedText}
	afterDocument := &AXNode{
		ID: "doc", Role: "document", Name: "Checkout",
		SemanticFingerprint: "f1:checkout",
	}
	before := &appSnapshot{
		WindowHandle: "window-1",
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{beforeDocument},
			Roots:          []*AXNode{beforeDocument},
		},
	}
	after := &appSnapshot{
		WindowHandle: "window-1",
		TreeLines: []string{
			"ax:",
			` - document "Checkout":`,
			`  - text "` + strings.Repeat("current content ", 20) + `"`,
		},
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{afterDocument},
			Roots:          []*AXNode{afterDocument},
		},
	}
	diff, ok := testIncrementalAXSnapshot(before, after)
	if !ok {
		t.Fatal("removed visible text should produce a semantic removal chunk")
	}
	text := strings.Join(diff, "\n")
	if !strings.Contains(text, `document "Checkout":`) ||
		!strings.Contains(text, `<removed> text "Payment failed"`) {
		t.Fatalf("removed text lost semantic context:\n%s", text)
	}
}

func TestChangedParentAndChildRenderOneGroupedChunk(t *testing.T) {
	beforeGroup := &AXNode{
		ID: "group", Role: "group", Name: "Preferences",
		SemanticFingerprint: "f1:preferences",
		States:              AXStates{Expanded: TriFalse},
	}
	beforeChild := &AXNode{
		ID: "alerts", Role: "checkbox", Name: "Alerts", Ref: "r2",
		SemanticFingerprint: "f1:alerts",
		States:              AXStates{Checked: CheckedFalse},
		Parent:              beforeGroup,
	}
	beforeGroup.Children = []*AXNode{beforeChild}
	afterGroup := *beforeGroup
	afterGroup.States.Expanded = TriTrue
	afterChild := *beforeChild
	afterChild.States.Checked = CheckedTrue
	afterChild.Parent = &afterGroup
	afterGroup.Children = []*AXNode{&afterChild}
	before := &appSnapshot{
		WindowHandle: "window-1",
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{beforeGroup},
			Roots:          []*AXNode{beforeGroup},
		},
	}
	after := &appSnapshot{
		WindowHandle: "window-1",
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{&afterGroup},
			Roots:          []*AXNode{&afterGroup},
		},
	}
	diff, ok := testIncrementalAXSnapshot(before, after)
	if !ok {
		t.Fatal("parent and child state changes should produce a grouped chunk")
	}
	text := strings.Join(diff, "\n")
	if strings.Count(text, `group "Preferences"`) != 1 ||
		strings.Count(text, `checkbox "Alerts"`) != 1 {
		t.Fatalf("parent/child changes were duplicated:\n%s", text)
	}
	if !strings.Contains(text, ` - <changed> group "Preferences"`) ||
		!strings.Contains(text, `  - <changed> checkbox "Alerts"`) {
		t.Fatalf("parent/child changes lost hierarchy:\n%s", text)
	}
}

func TestSemanticDuplicatesAreMatchedOnlyWithinTheirParent(t *testing.T) {
	beforeLeft := &AXNode{
		ID: "left", Role: "group", Name: "Left",
		SemanticFingerprint: "f1:left",
	}
	beforeRight := &AXNode{
		ID: "right", Role: "group", Name: "Right",
		SemanticFingerprint: "f1:right",
	}
	oldItem := &AXNode{
		ID: "old-item", Role: "menuitem", Name: "Format",
		SemanticFingerprint: "f1:format",
		Parent:              beforeLeft,
	}
	beforeLeft.Children = []*AXNode{oldItem}
	afterLeft := *beforeLeft
	afterLeft.Children = nil
	afterRight := *beforeRight
	newItem := *oldItem
	newItem.ID = "new-item"
	newItem.Parent = &afterRight
	afterRight.Children = []*AXNode{&newItem}
	changes := diffAXSnapshots(
		&AXSnapshotV1{CanonicalRoots: []*AXNode{beforeLeft, beforeRight}},
		&AXSnapshotV1{CanonicalRoots: []*AXNode{&afterLeft, &afterRight}},
	)
	var added, removed int
	for _, change := range changes {
		switch change.Kind {
		case AXDiffAdded:
			added++
		case AXDiffRemoved:
			removed++
		}
	}
	if added != 1 || removed != 1 {
		t.Fatalf("cross-parent semantic duplicates were paired: %#v", changes)
	}
}

func TestSemanticTargetFromRefPreservesSelectorPredicates(t *testing.T) {
	ref := encodeScopedSemanticRef(
		`toggle button "Menu" [desc="View options"] [focused] [a=click]`,
		"pid145/win1/dialog",
	)
	target, err := semanticTargetFromAddress(ref)
	if err != nil {
		t.Fatal(err)
	}
	if target.Role != "toggle button" || target.Name != "Menu" || !target.HasName {
		t.Fatalf("semantic target head was not preserved: %#v", target)
	}
	if target.WindowHandle != "pid145/win1/dialog" {
		t.Fatalf("semantic target lost window scope: %#v", target)
	}
	got := map[string]string{}
	for _, predicate := range target.Predicates {
		got[predicate.Kind] = predicate.Value
	}
	for kind, want := range map[string]string{
		"desc": "View options", "state": "focused", "action": "click",
	} {
		if got[kind] != want {
			t.Fatalf("predicate %s = %q, want %q in %#v", kind, got[kind], want, target)
		}
	}
}

func TestObserveNoneKeepsFreshSemanticResolutionEvidence(t *testing.T) {
	notes := []string{
		"[semantic-resolution] semantic_ref was resolved against one fresh pre-action snapshot.",
		"SNAPSHOT UNCHANGED: verbose continuity text.",
	}
	kept := observeNoneNotes(notes)
	if len(kept) != 1 ||
		!strings.HasPrefix(kept[0], "[semantic-resolution]") {
		t.Fatalf("observe=none hid semantic resolution evidence: %#v", kept)
	}
}

func TestSemanticTargetIntentMismatchFailsBeforeRuntime(t *testing.T) {
	target, err := semanticTargetFromAddress(`push button "New"`)
	if err != nil {
		t.Fatal(err)
	}
	mismatch := semanticTargetIntentMismatch(target, "Save the document")
	if !strings.Contains(strings.ToLower(mismatch), "no input was emitted") {
		t.Fatalf("semantic intent mismatch must fail closed: %q", mismatch)
	}
	if mismatch := semanticTargetIntentMismatch(target, "Click New"); mismatch != "" {
		t.Fatalf("matching semantic intent was rejected: %q", mismatch)
	}
}

// 选择器尾部的约束**必须真的生效或真的报错**，不能静默丢弃。
//
// 这是修一个实测出来的 bug：parseSelector 原来取到 name 就 return，
// 末引号之后的内容从头到尾没人看。实测四条全部被吞：
//
//	push button "Save" [checked]              -> role="push button" name="Save" ok=true
//	push button "OK" >> nth=0                 -> role="push button" name="OK"   ok=true
//	menu item "Ruler" (12)                    -> role="menu item"   name="Ruler" ok=true
//	push button "Save" and then some garbage  -> role="push button" name="Save" ok=true
//
// 调用方以为自己加了限定，拿到的却是不带限定的结果——工具在骗 agent。
func TestSelectorPredicatesApplyOrFailLoudly(t *testing.T) {
	snapshot := &appSnapshot{Elements: []elementRecord{
		{Index: 1, ControlType: "check menu item", Name: "Ruler", States: " [checked]"},
		{Index: 2, ControlType: "check menu item", Name: "Ruler"},
		{Index: 5, ControlType: "toggle button", Name: "Menu", Description: "View options"},
		{Index: 6, ControlType: "toggle button", Name: "Menu", Description: "Show operations"},
		{Index: 8, ControlType: "push button", Name: "Go", Actions: []string{"click", "press"}},
		{Index: 9, ControlType: "entry", Placeholder: "Search files"},
		{Index: 11, ControlType: "list item", Name: "Row", States: " [selected focused]"},
	}}

	// 谓词真的把候选筛窄了：不带谓词是歧义，带上就唯一。
	if _, err := lookupElement(snapshot, `check menu item "Ruler"`); err == nil {
		t.Fatal("两个同名 Ruler，本该报歧义")
	}
	record, err := lookupElement(snapshot, `check menu item "Ruler" [checked]`)
	if err != nil || record.Index != 1 {
		t.Fatalf("[checked] 应当筛出 1 号：%v %v", record, err)
	}

	// 快照把多个状态打在**同一个方括号里**，所以一组要拆成多条约束。
	// agent 从行里原样抄下来的就是这个形状。
	if record, err = lookupElement(snapshot, `list item "Row" [selected focused]`); err != nil ||
		record.Index != 11 {
		t.Fatalf("一组多词应当逐词匹配：%v %v", record, err)
	}
	if _, err = lookupElement(snapshot, `list item "Row" [selected expanded]`); err == nil {
		t.Fatal("组里有一个词对不上就不该命中")
	}

	// 同名同角色只能靠 desc 区分——实测 Nautilus 的三个 toggle button "Menu"。
	if record, err = lookupElement(snapshot, `toggle button "Menu" [desc="View options"]`); err != nil ||
		record.Index != 5 {
		t.Fatalf("[desc=…] 应当筛出 5 号：%v %v", record, err)
	}
	// desc 单独用作身份（不给引号名字）也要能走通。
	if record, err = lookupElement(snapshot, `toggle button [desc="Show operations"]`); err != nil ||
		record.Index != 6 {
		t.Fatalf("只给 role + desc 也该唯一：%v %v", record, err)
	}
	if record, err = lookupElement(snapshot, `[placeholder="Search files"]`); err != nil ||
		record.Index != 9 {
		t.Fatalf("只给 placeholder 也该唯一：%v %v", record, err)
	}
	if _, err = lookupElement(snapshot, `push button "Go" [actions=press]`); err == nil {
		t.Fatal("legacy [actions=…] selector must be rejected")
	}
	if record, err = lookupElement(snapshot, `push button "Go" [a=press]`); err != nil ||
		record.Index != 8 {
		t.Fatalf("[a=…] 应当命中 8 号：%v %v", record, err)
	}

	// 认不出来的一律报错，且错误里要点名残串——这是这次修的核心。
	for _, selector := range []string{
		`push button "OK" >> nth=0`,
		`menu item "Ruler" (12)`,
		`push button "Save" and then some garbage`,
		`push button "Save" [disabled]`,
		`push button "Save" [level=2]`,
		`push button "Save" [desc=View options]`, // 值没加引号
		`push button "Save" [checked`,            // 括号没闭合
	} {
		_, err := lookupElement(snapshot, selector)
		if err == nil {
			t.Fatalf("%q 应当报错，而不是静默丢弃约束", selector)
		}
		if !strings.Contains(err.Error(), "trailing part I cannot read") &&
			!strings.Contains(err.Error(), "no element matches") {
			t.Fatalf("%q 的错误没说清是尾部读不懂：%v", selector, err)
		}
	}

	// 报错要把支持的写法摆出来，并当场宣告 >> / nth= / :right-of 不存在，
	// 否则模型会照着 Playwright 的记忆自己发明语法。
	_, err = lookupElement(snapshot, `push button "OK" >> nth=0`)
	for _, want := range []string{"[checked]", "desc=", "nth=", ">>", ":right-of"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误里缺少 %q：%v", want, err)
		}
	}

	// 谓词把候选筛空时，最可能的原因是状态变了而不是名字抄错，
	// 建议要先说这一条。
	_, err = lookupElement(snapshot, `check menu item "Ruler" [expanded]`)
	if err == nil || !strings.Contains(err.Error(), "may have changed since the snapshot") {
		t.Fatalf("筛空时应当提示状态可能已变：%v", err)
	}
	// 解析结果要回显，调用方才知道自己那串被读成了什么。
	if !strings.Contains(err.Error(), "predicates [expanded]") {
		t.Fatalf("Call log 里没回显解析到的谓词：%v", err)
	}
}

// 名字里带引号或反斜杠时，配对引号必须是**真配对**，不是最后一个引号。
//
// 原来用 strings.LastIndex 找收尾引号，在没有尾部内容时恰好正确；
// 一旦后面跟上 [desc="…"]，整段就会被吞进 name。
func TestSelectorFindsTheRealClosingQuote(t *testing.T) {
	role, name, hasName, rest := parseSelector(`toggle button "Menu" [desc="View options"]`)
	if role != "toggle button" || name != "Menu" || !hasName {
		t.Fatalf("配对引号找错了：role=%q name=%q", role, name)
	}
	if rest != `[desc="View options"]` {
		t.Fatalf("残串不对：%q", rest)
	}

	// 渲染侧 quoted() 会把引号转义成 \"，这里要还原回去。
	_, name, _, rest = parseSelector(`static "say \"hi\"" [focused]`)
	if name != `say "hi"` {
		t.Fatalf("转义还原错了：%q", name)
	}
	if rest != "[focused]" {
		t.Fatalf("带转义时残串不对：%q", rest)
	}

	// desc 的值里带 `]` 也要能闭合到正确的位置。
	preds, err := parsePredicates(`[desc="a] b"]`)
	if err != nil || len(preds) != 1 || preds[0].value != "a] b" {
		t.Fatalf("引号内的 ] 不该当成收尾：%v %v", preds, err)
	}
}

// 这条通道是查 Playwright 时反推出来的。它的 aria snapshot 只渲染一小撮语义属性，
// "查任意样式属性"是 toHaveCSS 这类**断言**的事——快照是摘要，断言是精确查询，
// 两者不必是同一套字段。顺着这条思路回头查 AT-SPI，才发现
// Atspi.Text.get_default_attributes() 一直能读到 justification / size / weight。
//
// **此前记进文档的"改段落对齐后 a11y 树字节不变"是错的**：树不变是因为我们
// 没取这些字段，不是信息不存在。真机验证 ctrl+r 报出 `justification: left -> right`。
func TestTextAttributeChanges(t *testing.T) {
	before := &appSnapshot{Elements: []elementRecord{
		{Index: 26, RuntimeID: []int{0, 0, 1}, ControlType: "paragraph",
			TextAttributes: map[string]string{"justification": "left", "weight": "400"}},
		{Index: 49, RuntimeID: []int{0, 0, 2}, ControlType: "text",
			TextAttributes: map[string]string{"bg-color": "64764,64764,64764"}},
	}}
	after := &appSnapshot{Elements: []elementRecord{
		{Index: 26, RuntimeID: []int{0, 0, 1}, ControlType: "paragraph",
			TextAttributes: map[string]string{"justification": "right", "weight": "400"}},
		{Index: 49, RuntimeID: []int{0, 0, 2}, ControlType: "text",
			TextAttributes: map[string]string{"bg-color": "64764,64764,64764"}},
	}}

	changes := textAttributeChanges(before, after)
	if len(changes) != 1 {
		t.Fatalf("只有段落变了，应当只报一条：%v", changes)
	}
	if !strings.Contains(changes[0], "justification: left -> right") {
		t.Fatalf("要说出什么变成了什么：%s", changes[0])
	}
	// 没变的属性不许混进来。
	if strings.Contains(changes[0], "weight") {
		t.Fatalf("没变的属性不该出现：%s", changes[0])
	}

	// 回归：第一版按 role+name 配对，而 LibreOffice 侧栏有四个无名 `text` 节点，
	// key 全撞成 `text\x00""`，于是某个节点的悬停高亮被张冠李戴到别人头上，
	// **连空操作都报出"格式变了"**。按 runtimeId 路径配对就没有这个问题。
	collide := &appSnapshot{Elements: []elementRecord{
		{Index: 1, RuntimeID: []int{0, 1}, ControlType: "text",
			TextAttributes: map[string]string{"bg-color": "a"}},
		{Index: 2, RuntimeID: []int{0, 2}, ControlType: "text",
			TextAttributes: map[string]string{"bg-color": "b"}},
	}}
	same := &appSnapshot{Elements: []elementRecord{
		{Index: 1, RuntimeID: []int{0, 1}, ControlType: "text",
			TextAttributes: map[string]string{"bg-color": "a"}},
		{Index: 2, RuntimeID: []int{0, 2}, ControlType: "text",
			TextAttributes: map[string]string{"bg-color": "b"}},
	}}
	if got := textAttributeChanges(collide, same); len(got) != 0 {
		t.Fatalf("同名无名节点不得互相串味：%v", got)
	}

	replaced := &appSnapshot{Elements: []elementRecord{
		{Index: 1, RuntimeID: []int{0, 1}, ControlType: "table cell", Name: "B1",
			TextAttributes: map[string]string{"family-name": "B2:B6"}},
	}}
	replacement := &appSnapshot{Elements: []elementRecord{
		{Index: 1, RuntimeID: []int{0, 1}, ControlType: "paragraph", Name: "Title",
			TextAttributes: map[string]string{"family-name": "Söhne"}},
	}}
	if got := textAttributeChanges(replaced, replacement); len(got) != 0 {
		t.Fatalf("同一路径换成另一个 role/name 后不得错配属性: %v", got)
	}

	implausible := &appSnapshot{Elements: []elementRecord{
		{Index: 26, RuntimeID: []int{0, 0, 1}, ControlType: "paragraph",
			TextAttributes: map[string]string{"family-name": "B2:B6"}},
	}}
	plausibleAfter := &appSnapshot{Elements: []elementRecord{
		{Index: 26, RuntimeID: []int{0, 0, 1}, ControlType: "paragraph",
			TextAttributes: map[string]string{"family-name": "Söhne"}},
	}}
	if got := textAttributeChanges(implausible, plausibleAfter); len(got) != 0 {
		t.Fatalf("单元格范围不得冒充字体族参与属性证据: %v", got)
	}

	uiBefore := &appSnapshot{Elements: []elementRecord{
		{Index: 8, RuntimeID: []int{0, 4}, ControlType: "push button", Name: "Tab",
			TextAttributes: map[string]string{"bg-color": "255,255,255"}},
	}}
	uiAfter := &appSnapshot{Elements: []elementRecord{
		{Index: 8, RuntimeID: []int{0, 4}, ControlType: "push button", Name: "Tab",
			TextAttributes: map[string]string{"bg-color": "0,120,215"}},
	}}
	if got := textAttributeChanges(uiBefore, uiAfter); len(got) != 0 {
		t.Fatalf("UI control highlight must not be presented as document formatting evidence: %v", got)
	}

	uiTextBefore := &appSnapshot{Elements: []elementRecord{
		{Index: 9, RuntimeID: []int{0, 5}, ControlType: "text", Name: "Sidebar label",
			TextAttributes: map[string]string{"fg-color": "0,0,0"}},
	}}
	uiTextAfter := &appSnapshot{Elements: []elementRecord{
		{Index: 9, RuntimeID: []int{0, 5}, ControlType: "text", Name: "Sidebar label",
			TextAttributes: map[string]string{"fg-color": "255,255,255"}},
	}}
	if got := textAttributeChanges(uiTextBefore, uiTextAfter); len(got) != 0 {
		t.Fatalf("text role without a document/paragraph ancestor is UI presentation, not document formatting: %v", got)
	}

	documentTextBefore := &appSnapshot{Elements: []elementRecord{
		{Index: 1, RuntimeID: []int{0, 1}, ControlType: "document text", Name: "Document"},
		{Index: 2, RuntimeID: []int{0, 1, 0}, ControlType: "text", Name: "Body",
			TextAttributes: map[string]string{"weight": "400"}},
	}}
	documentTextAfter := &appSnapshot{Elements: []elementRecord{
		{Index: 1, RuntimeID: []int{0, 1}, ControlType: "document text", Name: "Document"},
		{Index: 2, RuntimeID: []int{0, 1, 0}, ControlType: "text", Name: "Body",
			TextAttributes: map[string]string{"weight": "700"}},
	}}
	if got := textAttributeChanges(documentTextBefore, documentTextAfter); len(got) != 1 {
		t.Fatalf("text under a document ancestor must retain formatting evidence: %v", got)
	}

	// 缺一份快照时不许编。
	if got := textAttributeChanges(nil, after); got != nil {
		t.Fatalf("没有动作前的快照就不该报变化：%v", got)
	}
}

// 大输出落盘只回路径——照抄 playwright-mcp 的 --output-mode file|stdout。
//
// 它的解法不是压缩内容，是**把大输出写文件、只回一个路径**，agent 需要细节时
// 自己去读。LibreOffice 的树 17694 字符、VS Code 24731 字符，而 agent 通常
// 只关心其中一两个元素。
func TestOutputSpillsToFileOnlyWhenAskedAndOnlyWhenBig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COMPUTER_USE_OUTPUT_DIR", dir)

	big := strings.Repeat("a very long tree line\n", 500)

	// 默认 stdout：一个字都不落盘。
	t.Setenv("COMPUTER_USE_OUTPUT_MODE", "")
	if got := spillToFile("snapshot", big); got != "" {
		t.Fatalf("默认不该落盘：%s", got[:60])
	}

	// 开了 file 模式，但内容小于阈值时也不落盘——
	// 落盘把一次调用变成两次，对小树是净损失（gedit 的树只有 659 字符）。
	t.Setenv("COMPUTER_USE_OUTPUT_MODE", "file")
	if got := spillToFile("snapshot", "short tree"); got != "" {
		t.Fatalf("小输出不该落盘：%s", got)
	}

	replacement := spillToFile("snapshot", big)
	if replacement == "" {
		t.Fatal("大输出应当落盘")
	}
	for _, fragment := range []string{dir, "characters", "lines", "Read that file"} {
		if !strings.Contains(replacement, fragment) {
			t.Fatalf("替代文本要说清写到哪、有多大、怎么读，缺 %q：%s", fragment, replacement)
		}
	}
	// 替代文本必须**远小于**原文，否则这个机制毫无意义。
	if len(replacement) >= len(big)/4 {
		t.Fatalf("替代文本没省下东西：%d vs %d", len(replacement), len(big))
	}

	// 写出去的必须是原文，一个字节都不能少——agent 读它是为了拿全量。
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("应当只写出一个文件：%v %v", entries, err)
	}
	written, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil || string(written) != big {
		t.Fatalf("落盘内容与原文不一致")
	}
}

// 落盘失败绝不能让工具失败——原样回文本即可。
func TestSpillFailureFallsBackToInlineText(t *testing.T) {
	t.Setenv("COMPUTER_USE_OUTPUT_MODE", "file")
	t.Setenv("COMPUTER_USE_OUTPUT_DIR", "/proc/definitely-not-writable")
	if got := spillToFile("snapshot", strings.Repeat("x", 9000)); got != "" {
		t.Fatalf("写不成时应当退回内联，而不是给出一个不存在的路径：%s", got[:60])
	}
}

func TestFindFiltersAreAndedAndReuseTheRenderedLines(t *testing.T) {
	// find 的输出必须**逐字复用**树的渲染行，不能在 Go 里再写一份渲染器。
	// 两份实现迟早漂移，而 agent 只该学一套读法。
	snapshot := &appSnapshot{
		WindowTitle: "Untitled 1",
		Elements: []elementRecord{
			{Index: 3, ControlType: "push button", Name: "Save", States: "enabled focused"},
			{Index: 4, ControlType: "push button", Name: "Save As…", States: "enabled"},
			{Index: 5, ControlType: "toggle button", Name: "Bold", States: "enabled checked"},
			{Index: 6, ControlType: "text", Placeholder: "Search files"},
		},
		TreeLines: []string{
			"  3 push button \"Save\" [enabled focused]",
			"  4 push button \"Save As…\" [enabled]",
			"  5 toggle button \"Bold\" [enabled checked]",
			"  6 text [placeholder=\"Search files\"]",
			"  … 12 more nodes",
		},
	}

	lines := linesByIndex(snapshot)
	if len(lines) != 4 {
		t.Fatalf("行索引应跳过非数字开头的截断提示行，得到 %d 条", len(lines))
	}
	if lines[5] != "  5 toggle button \"Bold\" [enabled checked]" {
		t.Fatalf("行必须原样取自渲染结果，得到 %q", lines[5])
	}

	cases := []struct {
		role, name, text, state string
		want                    []int
	}{
		// role 用子串：agent 写 "button" 时两种 button 都该找到
		{role: "button", want: []int{3, 4, 5}},
		// 多条件是**与**，不是或
		{role: "button", state: "checked", want: []int{5}},
		{name: "save", want: []int{3, 4}},
		// text 要能落到 placeholder 上——同一个搜索框在 GTK 是 placeholder、
		// 在 Electron 是 name，agent 不该被迫知道是哪个
		{text: "search files", want: []int{6}},
		{role: "button", name: "nonexistent", want: nil},
	}
	for _, c := range cases {
		got := []int{}
		for i := range snapshot.Elements {
			if recordMatches(&snapshot.Elements[i], c.role, c.name, c.text, c.state) {
				got = append(got, snapshot.Elements[i].Index)
			}
		}
		if len(got) != len(c.want) {
			t.Fatalf("查询 %+v 期望 %v，得到 %v", c, c.want, got)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("查询 %+v 期望 %v，得到 %v", c, c.want, got)
			}
		}
	}
}

func TestVerifyReportsWhatItActuallySawNotJustFailure(t *testing.T) {
	// "断言失败"没有信息量。有信息量的是"期望 checked，实际 [enabled focused]"
	// ——agent 拿到后者才知道下一步该做什么。
	record := &elementRecord{Index: 5, ControlType: "toggle button", Name: "Bold", States: "enabled focused"}

	ok, observed := verifyGoal{state: "checked"}.check(record, nil)
	if ok {
		t.Fatal("元素没有 checked，断言不该通过")
	}
	if !strings.Contains(observed, "enabled focused") {
		t.Fatalf("失败时必须回报实际观测到的状态，得到 %q", observed)
	}

	// 取反语法
	if ok, _ := (verifyGoal{state: "!checked"}).check(record, nil); !ok {
		t.Fatal("!checked 对一个未选中的元素应当通过")
	}

	// exists:false 是"等对话框消失"的写法：元素找不到才算通过
	no := false
	if ok, _ := (verifyGoal{exists: &no}).check(nil, nil); !ok {
		t.Fatal("元素不存在时 exists:false 应当通过")
	}
	if ok, _ := (verifyGoal{exists: &no}).check(record, nil); ok {
		t.Fatal("元素还在时 exists:false 不该通过")
	}

	// 缺省超时下，断言不存在的元素要给出可行动的理由，而不是空的失败
	if ok, observed := (verifyGoal{state: "checked"}).check(nil, nil); ok || observed == "" {
		t.Fatalf("找不到元素时要给出理由，得到 ok=%v observed=%q", ok, observed)
	}
}

func TestFindReportsTheProjectionItActuallySearched(t *testing.T) {
	pruned := true
	unpruned := false
	for _, test := range []struct {
		snapshot  *appSnapshot
		wantLabel string
		canExpand bool
	}{
		{nil, "current pruned snapshot", true},
		{
			&appSnapshot{projection: snapshotProjection{Prune: &pruned}},
			"current pruned snapshot",
			true,
		},
		{
			&appSnapshot{projection: snapshotProjection{Prune: &unpruned}},
			"current unpruned snapshot",
			false,
		},
	} {
		label, canExpand := findProjectionDescription(test.snapshot)
		if label != test.wantLabel || canExpand != test.canExpand {
			t.Fatalf(
				"find projection = (%q, %t), want (%q, %t)",
				label,
				canExpand,
				test.wantLabel,
				test.canExpand,
			)
		}
	}
	if !strings.Contains(mainGoSource(t), "s.refreshProjectedSnapshot(app, request)") {
		t.Fatal("find must preserve an explicit get_app_state address space")
	}
}

func TestFindObservationBoundariesPreserveRealOmissionCauses(t *testing.T) {
	snapshot := &appSnapshot{
		rawTreeLines: []string{
			"\t0 frame \"Editor\"",
			"\t\t1 push button \"Save\"",
			"(9 node(s) omitted — 4 subtree root(s) not traversed: " +
				"max_tree_depth=3 was reached; 5 filtered)",
			"(provider probe errors: child_count=1; affected nodes were " +
				"retained when possible)",
			"\t\t(hidden-items=12; open menu, then observe)",
		},
	}

	boundaries := snapshotObservationBoundaries(snapshot)
	text := strings.Join(boundaries, "\n")

	if len(boundaries) != 3 {
		t.Fatalf("unexpected boundaries: %#v", boundaries)
	}
	for _, expected := range []string{
		"max_tree_depth=3 was reached",
		"provider probe errors: child_count=1",
		"hidden-items=12",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing omission cause %q in %q", expected, text)
		}
	}
	if strings.Contains(text, `push button "Save"`) {
		t.Fatalf("ordinary element lines are not observation boundaries: %q", text)
	}
	if strings.Contains(
		mainGoSource(t),
		"Observation boundaries from this same snapshot:",
	) {
		t.Fatal("find zero-match responses must not dump provider diagnostics")
	}
}

func TestFindRecoveryAdviceIsCauseSpecific(t *testing.T) {
	filtered := []string{
		"(35 node(s) omitted — 35 filtered: not interactable)",
	}
	single := findRecoveryAdvice(1, filtered, true)
	if strings.Contains(single, "Relax one filter") {
		t.Fatalf("a single-filter query cannot relax one filter: %q", single)
	}
	if !strings.Contains(single, "broader synonym") ||
		!strings.Contains(single, "rooted observation") {
		t.Fatalf("filtered recovery is incomplete: %q", single)
	}

	depth := []string{
		"(4 node(s) omitted — max_tree_depth=3 was reached)",
	}
	depthAdvice := findRecoveryAdvice(1, depth, true)
	if strings.Contains(depthAdvice, "prune=false") {
		t.Fatalf("depth omission must not expose removed prune controls: %q", depthAdvice)
	}
	if !strings.Contains(depthAdvice, "Observation boundaries") {
		t.Fatalf("depth recovery must point to the cause ledger: %q", depthAdvice)
	}

	multiple := findRecoveryAdvice(2, nil, true)
	if !strings.Contains(multiple, "Relax one filter") {
		t.Fatalf("multi-filter queries should allow relaxing one filter: %q", multiple)
	}
}

func TestModelFacingContractsAreBenchmarkNeutral(t *testing.T) {
	payload := serverInstructions
	for _, definition := range allToolDefinitions() {
		encoded, err := json.Marshal(definition)
		if err != nil {
			t.Fatal(err)
		}
		payload += "\n" + string(encoded)
	}
	for _, forbidden := range []string{"OSWorld", "benchmark", "task_id"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("model-facing MCP contract contains benchmark-specific text %q", forbidden)
		}
	}
}

func TestVerifyDoesNotDoubleWrapTheStates(t *testing.T) {
	// 树里渲染出来的 states 自带方括号，再包一层就成了 `[ [enabled]]`。
	record := &elementRecord{Index: 1, ControlType: "push button", Name: "Search", States: " [has-click-action]"}
	_, observed := verifyGoal{state: "checked"}.check(record, nil)
	if strings.Contains(observed, "[ [") || strings.Contains(observed, "]]") {
		t.Fatalf("states 不该被二次加括号，得到 %q", observed)
	}
}

func TestVerifyAcceptsNumericIndicesNotJustSelectors(t *testing.T) {
	// 实测踩到过：verify 走 lookupBySelectorFor，传下标 "62" 会连报 6 轮
	// "no element matches the selector" —— 元素一直在，断言却报不存在。
	// 断言工具给假阴性，比没有断言更糟。
	snapshot := &appSnapshot{Elements: []elementRecord{
		{Index: 62, ControlType: "text", Value: "baseline-marker", States: "[focused]"},
	}}
	record, err := lookupElementFor(snapshot, "62", "", "verify")
	if err != nil || record == nil {
		t.Fatalf("数字下标必须能解析，得到 record=%v err=%v", record, err)
	}
	if ok, _ := (verifyGoal{textContains: "baseline"}).check(record, nil); !ok {
		t.Fatal("解析到的记录应当满足 text_contains")
	}
}

func TestCoordinateNotesDeclareTheirExplicitSpace(t *testing.T) {
	// 实测踩到过：Nautilus 图标在树里是 {256,76,78,68}（中心 295,110），
	// 合成右键的 Note 却报 (384,159)——差的正是窗口原点 (89,49)。
	// a11y 元素兜底仍要把绝对落点换回窗口相对后再报告；gui 使用独立的
	// screen_absolute_point，不共享窗口偏移约定。
	if !strings.Contains(linuxRuntimeScript, "def window_relative(") {
		t.Fatal("需要一个把绝对坐标换回窗口相对的转换函数")
	}
	for _, fragment := range []string{
		// a11y 元素坐标兜底仍是窗口相对。
		"coordinate click(s) at ({:.0f}, {:.0f}) in ",
		"window-relative pixels after bringing the window to the foreground",
	} {
		if !strings.Contains(linuxRuntimeScript, fragment) {
			t.Fatalf("a11y 坐标 Note 必须声明窗口相对空间：缺 %q", fragment)
		}
	}
	for _, fragment := range []string{
		"def screen_absolute_point(",
		"def validate_gui_action(action, bounds=None, native_bounds=None):",
		"def execute_gui_action(action):",
		"screen_absolute_point(",
	} {
		if !strings.Contains(linuxRuntimeScript, fragment) {
			t.Fatalf("GUI 坐标必须是无隐式偏移的屏幕绝对空间：缺 %q", fragment)
		}
	}
	// 每一处报坐标的地方都要经过转换，不能直接打印 screen_point 的返回值
	if strings.Contains(linuxRuntimeScript, `"{}".format(action, x, y, UNVERIFIED_SYNTHESIS)`) {
		t.Fatal("menu 兜底仍在直接打印屏幕绝对坐标")
	}
}

func TestGUISurfaceUsesFullDesktopImageCoordinates(t *testing.T) {
	for _, fragment := range []string{
		"two complementary tools",
		"GUI is full-desktop visual observation",
		"absolute-pixel input",
		"AX target-window screenshot coordinates are never GUI coordinates",
	} {
		if !strings.Contains(serverInstructions, fragment) {
			t.Fatalf("下发给客户端的说明缺少 GUI/a11y 分叉：%q", fragment)
		}
	}
	for _, stale := range []string{
		"Coordinates are one consistent space across everything",
		"GUI addresses a target by window-relative pixel coordinates",
	} {
		if strings.Contains(serverInstructions, stale) {
			t.Fatalf("下发说明仍含旧坐标约定：%q", stale)
		}
	}

	gui := findToolDefinition(t, "GUI")
	if !strings.Contains(gui.Description, "Full-desktop") ||
		!strings.Contains(gui.Description, "returns one final screenshot") {
		t.Fatal("gui 必须声明全桌面图像坐标和每次返回截图")
	}
	for _, removed := range []string{"get_screenshot", "click_xy", "drag_xy"} {
		if strings.Contains(gui.Description, removed) {
			t.Fatalf("gui description must not advertise removed tool %q", removed)
		}
	}
}

func TestGUISchemaPublishesRuntimeLimitsAndFocusBoundary(t *testing.T) {
	gui := findToolDefinition(t, "GUI")
	inputBranches := gui.InputSchema["oneOf"].([]any)
	if len(inputBranches) != 2 {
		t.Fatalf("GUI input oneOf branches=%d want 2", len(inputBranches))
	}
	observeProps := inputBranches[0].(map[string]any)["properties"].(map[string]any)
	observeActions := observeProps["actions"].(map[string]any)
	if observeActions["minItems"] != 1 || observeActions["maxItems"] != 1 {
		t.Fatal("GUI observation branch must require one screenshot action")
	}
	if _, ok := observeProps["settle_ms"]; ok {
		t.Fatal("GUI observation branch must not accept settle_ms")
	}
	actProps := inputBranches[1].(map[string]any)["properties"].(map[string]any)
	actions := actProps["actions"].(map[string]any)
	if got := actions["maxItems"]; got != guiMaxActions {
		t.Fatalf("gui actions maxItems = %v, want %d", got, guiMaxActions)
	}
	if got := actions["minItems"]; got != 1 {
		t.Fatalf("gui action branch minItems = %v, want 1", got)
	}
	action := actions["items"].(map[string]any)
	actionBranches := action["oneOf"].([]any)
	byType := map[string]map[string]any{}
	for _, raw := range actionBranches {
		branch := raw.(map[string]any)
		fields := branch["properties"].(map[string]any)
		actionType := fields["type"].(map[string]any)["const"].(string)
		byType[actionType] = fields
	}
	wantTypes := []string{
		"click", "double_click", "move", "drag",
		"scroll", "type", "keypress", "wait",
	}
	for _, actionType := range wantTypes {
		if _, ok := byType[actionType]; !ok {
			t.Fatalf("GUI action union missing %q", actionType)
		}
	}
	if len(byType) != len(wantTypes) {
		t.Fatalf("GUI action union has unexpected branches: %#v", byType)
	}

	path := byType["drag"]["path"].(map[string]any)
	if path["maxItems"] != guiMaxDragPoints {
		t.Fatalf("drag path maxItems = %v, want %d", path["maxItems"], guiMaxDragPoints)
	}
	keys := byType["keypress"]["keys"].(map[string]any)
	if keys["maxItems"] != guiMaxKeys {
		t.Fatalf("keypress maxItems = %v, want %d", keys["maxItems"], guiMaxKeys)
	}
	key := keys["items"].(map[string]any)
	if key["maxLength"] != guiMaxKeyLength {
		t.Fatalf("key maxLength = %v, want %d", key["maxLength"], guiMaxKeyLength)
	}
	keyDescription, _ := keys["description"].(string)
	for _, spelling := range []string{"PAGE_UP", "PAGEUP", "PAGE_DOWN", "PAGEDOWN"} {
		if !strings.Contains(keyDescription, spelling) {
			t.Fatalf("keypress description does not advertise %s: %q",
				spelling, keyDescription)
		}
	}
	text := byType["type"]["text"].(map[string]any)
	if text["maxLength"] != guiMaxTextLength {
		t.Fatalf("type text maxLength = %v, want %d", text["maxLength"], guiMaxTextLength)
	}
	for _, name := range []string{"scroll_x", "scroll_y"} {
		delta := byType["scroll"][name].(map[string]any)
		if delta["minimum"] != -guiMaxScrollDelta ||
			delta["maximum"] != guiMaxScrollDelta {
			t.Fatalf("%s range = [%v,%v], want [%d,%d]",
				name, delta["minimum"], delta["maximum"],
				-guiMaxScrollDelta, guiMaxScrollDelta)
		}
	}
	scrollBranch := func() map[string]any {
		for _, raw := range actionBranches {
			branch := raw.(map[string]any)
			fields := branch["properties"].(map[string]any)
			if fields["type"].(map[string]any)["const"] == "scroll" {
				return branch
			}
		}
		return nil
	}()
	if scrollBranch == nil || len(scrollBranch["anyOf"].([]any)) != 2 {
		t.Fatal("scroll schema must require a non-zero scroll_x or scroll_y")
	}
	clickButtons := byType["click"]["button"].(map[string]any)["enum"].([]string)
	if !reflect.DeepEqual(
		clickButtons,
		[]string{"left", "right", "wheel", "back", "forward"},
	) {
		t.Fatalf("GUI click buttons=%#v", clickButtons)
	}
	if _, ok := byType["double_click"]["button"]; ok {
		t.Fatal("native double_click must not accept button")
	}
	for _, actionType := range []string{
		"click", "double_click", "move", "drag", "scroll",
	} {
		if _, ok := byType[actionType]["keys"]; !ok {
			t.Fatalf("%s must accept optional held keys", actionType)
		}
	}
	if len(byType["wait"]) != 1 {
		t.Fatalf("wait must expose only type, got %#v", byType["wait"])
	}
	for _, text := range []string{gui.Description, serverInstructions} {
		if !strings.Contains(text, "current focus") &&
			!strings.Contains(text, "current input focus") {
			t.Fatal("gui type/keypress must state that they target only current input focus")
		}
	}
	if !strings.Contains(gui.Description, "validates before input") {
		t.Fatal("gui must publish whole-batch pre-validation")
	}
	if !strings.Contains(gui.Description, "one final screenshot") {
		t.Fatal("gui must state that one final screenshot covers the complete batch")
	}
	if !strings.Contains(gui.Description, "wait is one second") {
		t.Fatal("gui must publish fixed native wait semantics")
	}
	if !strings.Contains(
		gui.Description,
		fmt.Sprintf("max %ds/batch", guiMaxTotalWait),
	) {
		t.Fatal("gui must publish the cumulative wait budget")
	}
	settle := actProps["settle_ms"].(map[string]any)
	if settle["minimum"] != 0 || settle["maximum"] != guiMaxSettleMS {
		t.Fatalf("gui settle_ms range = [%v,%v], want [0,%d]",
			settle["minimum"], settle["maximum"], guiMaxSettleMS)
	}
	if !strings.Contains(gui.Description, "visual stillness, not semantic success") {
		t.Fatal("gui description must keep the settling evidence boundary")
	}
	if !strings.Contains(gui.Description, "omitted default") {
		t.Fatal("gui description must publish omitted settle_ms behavior")
	}
	for _, overclaim := range []string{"pixel-stable", "executed"} {
		if strings.Contains(gui.Description, overclaim) {
			t.Fatalf("gui description must not imply successful execution/effect with %q", overclaim)
		}
	}
	field, ok := reflect.TypeOf(ProviderDesktopRequest{}).FieldByName("SettleMS")
	if !ok || field.Tag.Get("json") != "settle_ms" {
		t.Fatal("ProviderDesktopRequest must preserve explicit settle_ms=0")
	}
	encoded, err := json.Marshal(ProviderDesktopRequest{
		ProtocolVersion: providerProtocolV2,
		Actions:         []ProviderDesktopAction{{Type: "screenshot"}},
		SettleMS:        0,
	})
	if err != nil || !strings.Contains(string(encoded), `"settle_ms":0`) {
		t.Fatalf("explicit settle_ms=0 was lost: %s err=%v", encoded, err)
	}
	if _, ok := actProps["frame_id"]; ok {
		t.Fatal("gui schema must keep frame provenance private")
	}
	if _, ok := actProps["expect_change"]; ok {
		t.Fatal("gui schema must not expose expect_change")
	}
	if _, ok := reflect.TypeOf(ProviderDesktopRequest{}).FieldByName("ExpectChange"); ok {
		t.Fatal("runtime transport must not carry expect_change")
	}
	field, ok = reflect.TypeOf(ProviderDesktopRequest{}).FieldByName("FrameID")
	if !ok || field.Tag.Get("json") != "frame_id,omitempty" {
		t.Fatal("ProviderDesktopRequest must carry private frame provenance")
	}
	for fieldName, jsonName := range map[string]string{
		"CoordinateSpace": "coordinateSpace,omitempty",
		"FrameID":         "frameId,omitempty",
		"ContentFrameID":  "contentFrameId,omitempty",
		"DesktopLayout":   "desktopLayout,omitempty",
		"Width":           "width,omitempty",
		"Height":          "height,omitempty",
	} {
		field, ok := reflect.TypeOf(appSnapshot{}).FieldByName(fieldName)
		if !ok || field.Tag.Get("json") != jsonName {
			t.Fatalf("appSnapshot.%s must preserve GUI metadata as %s", fieldName, jsonName)
		}
	}
	var snapshot appSnapshot
	if err := json.Unmarshal(
		[]byte(`{"coordinateSpace":"screen_image_px","frameId":"abc","contentFrameId":"pixels","width":1920,"height":1080}`),
		&snapshot,
	); err != nil {
		t.Fatal(err)
	}
	if snapshot.CoordinateSpace != "screen_image_px" || snapshot.FrameID != "abc" ||
		snapshot.ContentFrameID != "pixels" ||
		snapshot.Width != 1920 || snapshot.Height != 1080 {
		t.Fatalf("GUI metadata did not survive runtime decoding: %#v", snapshot)
	}
	if !strings.Contains(mainGoSource(t), `"coordinate_space":`) ||
		!strings.Contains(mainGoSource(t), `snapshot.CoordinateSpace`) {
		t.Fatal("gui metadata helper must normalize runtime coordinateSpace to public coordinate_space")
	}
	for actionType, fields := range byType {
		for _, forbidden := range []string{
			"window_id", "desktop_layout", "delta_x", "delta_y",
			"seconds", "expect_change",
		} {
			if _, ok := fields[forbidden]; ok {
				t.Fatalf("GUI %s schema leaked field %q", actionType, forbidden)
			}
		}
	}
}

func TestA11yBatchModePublishesAndEnforcesSafetyContract(t *testing.T) {
	tool := findToolDefinition(t, "AX")
	for _, fragment := range []string{
		"Semantic observe/find/act/assert_ax",
		"act consumes refs only",
		"act never accepts app, selector, or index",
		"Split before window changes",
	} {
		if !strings.Contains(tool.Description, fragment) {
			t.Fatalf("a11y batch description missing %q", fragment)
		}
	}
	branches := tool.InputSchema["oneOf"].([]any)
	if len(branches) != 6 {
		t.Fatalf("a11y oneOf branch count=%d want 6", len(branches))
	}
	for _, raw := range branches {
		branch := raw.(map[string]any)
		properties := branch["properties"].(map[string]any)
		mode := properties["mode"].(map[string]any)
		if _, ok := mode["const"].(string); !ok {
			t.Fatalf("every a11y oneOf branch must use mode const: %#v", mode)
		}
	}
	observeBranch := findA11ySchemaBranch(t, "observe", "app")
	observeProps := observeBranch["properties"].(map[string]any)
	observePayload := observeProps["observe"].(map[string]any)
	if values, ok := observePayload["enum"].([]string); !ok ||
		!reflect.DeepEqual(values, a11yObserveValues) {
		t.Fatalf("a11y standalone observe enum=%#v", observePayload["enum"])
	}
	if slices.Contains(a11yObserveValues, observeNone) {
		t.Fatal("standalone AX observation must not advertise observe=none")
	}
	findBranch := findA11ySchemaBranch(t, "find", "")
	findProps := findBranch["properties"].(map[string]any)
	assertBranch := findA11ySchemaBranch(t, "assert_ax", "")
	assertProps := assertBranch["properties"].(map[string]any)
	for mode, properties := range map[string]map[string]any{
		"observe":   observeProps,
		"find":      findProps,
		"assert_ax": assertProps,
	} {
		scope, ok := properties["scope"].(map[string]any)
		if !ok {
			t.Fatalf("a11y %s schema must expose scope", mode)
		}
		if values, ok := scope["enum"].([]string); !ok ||
			!reflect.DeepEqual(values, a11yScopeValues) {
			t.Fatalf("a11y %s scope enum=%#v", mode, scope["enum"])
		}
	}
	query := findProps["query"].(map[string]any)
	queryProps := query["properties"].(map[string]any)
	for _, field := range []string{
		"name_contains", "name_equals", "text_contains", "text_equals",
	} {
		if _, ok := queryProps[field]; !ok {
			t.Fatalf("a11y find query must expose %s", field)
		}
	}
	limitProperty := queryProps["limit"].(map[string]any)
	if limitProperty["maximum"] != maxAXFindPageSize {
		t.Fatalf("find limit is not bounded: %#v", limitProperty)
	}
	cursorBranchFound := false
	for _, raw := range branches {
		properties := raw.(map[string]any)["properties"].(map[string]any)
		if _, ok := properties["cursor"]; !ok {
			continue
		}
		cursorBranchFound = true
		if _, hasQuery := properties["query"]; hasQuery {
			t.Fatal("cursor continuation must not also expose query")
		}
		if pattern := properties["cursor"].(map[string]any)["pattern"]; pattern != "^f[1-9][0-9]*$" {
			t.Fatalf("find cursor pattern=%v", pattern)
		}
	}
	if !cursorBranchFound {
		t.Fatal("AX find schema has no cursor continuation branch")
	}
	if _, present := assertProps["timeout_ms"]; present {
		t.Fatal("assert_ax timeout_ms must live inside assertion")
	}
	defs := tool.InputSchema["$defs"].(map[string]any)
	assertion := defs["assertion"].(map[string]any)
	if ref := assertProps["assertion"].(map[string]any)["$ref"]; ref != "#/$defs/assertion" {
		t.Fatalf("assert_ax does not reuse the shared assertion schema: %v", ref)
	}
	assertionBranches, ok := assertion["oneOf"].([]any)
	if !ok || len(assertionBranches) != 5 {
		t.Fatalf(
			"assertion schema must distinguish element and window title: %#v",
			assertion,
		)
	}
	valueBranch := assertionBranches[2].(map[string]any)
	valueConstraints, ok := valueBranch["allOf"].([]any)
	if !ok || len(valueConstraints) != 1 {
		t.Fatalf("value assertion schema does not constrain contains: %#v",
			valueBranch)
	}
	windowTitleBranch := assertionBranches[4].(map[string]any)
	windowTitleProps := windowTitleBranch["properties"].(map[string]any)
	for _, field := range []string{
		"type",
		"text",
		"exists",
		"stable_ms",
		"timeout_ms",
	} {
		if _, ok := windowTitleProps[field]; !ok {
			t.Fatalf("window title assertion must expose %s", field)
		}
	}
	elementTarget := assertionBranches[0].(map[string]any)["properties"].(map[string]any)["target"].(map[string]any)
	targetBranches := elementTarget["oneOf"].([]any)
	for _, raw := range targetBranches {
		properties := raw.(map[string]any)["properties"].(map[string]any)
		refProperty, ok := properties["ref"].(map[string]any)
		if !ok {
			continue
		}
		if pattern := refProperty["pattern"]; pattern != "^r[1-9][0-9]*$" {
			t.Fatalf("assert_ax element ref accepts window refs: %#v", refProperty)
		}
	}
	windowTitleContains := windowTitleProps["text"].(map[string]any)
	if windowTitleContains["minLength"] != 1 {
		t.Fatalf(
			"window title text minLength=%v, want 1",
			windowTitleContains["minLength"],
		)
	}
	appsBranch := findA11ySchemaBranch(t, "observe", "apps")
	appsProps := appsBranch["properties"].(map[string]any)
	for _, forbidden := range []string{
		"app", "actions", "observe", "text_limit", "timeout_ms",
		"max_tree_nodes", "max_tree_depth", "query", "assertion", "scope",
	} {
		if _, ok := appsProps[forbidden]; ok {
			t.Fatalf("observe/apps schema must not expose %s", forbidden)
		}
	}
	actBranch := findA11ySchemaBranch(t, "act", "")
	props := actBranch["properties"].(map[string]any)
	if ref := props["postcondition"].(map[string]any)["$ref"]; ref != "#/$defs/assertion" {
		t.Fatalf("act.postcondition does not reuse the shared assertion schema: %v", ref)
	}
	actions := props["actions"].(map[string]any)
	if actions["maxItems"] != a11yBatchMaxActions {
		t.Fatalf("a11y actions maxItems=%v want %d",
			actions["maxItems"], a11yBatchMaxActions)
	}
	if actions["minItems"] != 1 {
		t.Fatal("a11y act schema must require at least one action")
	}
	if _, ok := props["observe"]; !ok {
		t.Fatal("a11y batch must expose one final observe control")
	}
	actObserve := props["observe"].(map[string]any)
	description, _ := actObserve["description"].(string)
	for _, phrase := range []string{
		"ACT ONLY: none|diff|screenshot",
		"tree/full are invalid",
		"fresh post-action diff",
		"reread only if required",
	} {
		if !strings.Contains(description, phrase) {
			t.Fatalf("a11y act observe guidance missing %q: %q",
				phrase, description)
		}
	}
	if values, ok := actObserve["enum"].([]string); !ok ||
		!reflect.DeepEqual(values, a11yActObserveValues) {
		t.Fatalf("a11y act observe enum=%#v", actObserve["enum"])
	}
	for _, forbidden := range []string{observeTree, observeFull} {
		if slices.Contains(a11yActObserveValues, forbidden) {
			t.Fatalf("a11y act must not expose complete readback %q", forbidden)
		}
	}
	for _, forbidden := range []string{"expect", "settle_ms"} {
		if _, ok := props[forbidden]; ok {
			t.Fatalf("a11y batch beta must not expose action-local %s", forbidden)
		}
	}
	if _, ok := props["app"]; ok {
		t.Fatal("ref-only AX act must not expose app")
	}
	item := actions["items"].(map[string]any)
	actionBranches := item["oneOf"].([]any)
	gotTypes := make([]string, 0, len(actionBranches))
	for _, raw := range actionBranches {
		branch := raw.(map[string]any)
		fields := branch["properties"].(map[string]any)
		actionType := fields["type"].(map[string]any)
		branchTypes := []string{}
		if value, ok := actionType["const"].(string); ok {
			branchTypes = append(branchTypes, value)
		}
		if values, ok := actionType["enum"].([]string); ok {
			branchTypes = append(branchTypes, values...)
		}
		if len(branchTypes) == 0 {
			t.Fatalf("action branch has no const or enum type: %#v", actionType)
		}
		gotTypes = append(gotTypes, branchTypes...)
		if _, ok := fields["element"]; ok {
			t.Fatal("public action schema must not expose ambiguous element")
		}
		if _, ok := fields["element_index"]; ok {
			t.Fatal("public action schema must not expose ambiguous element_index")
		}
		if _, ok := fields["intent"]; ok {
			t.Fatal("public action schema must not expose misleading intent")
		}
		if _, ok := fields["target"]; !ok {
			t.Fatal("each public action schema must expose structured target")
		}
		if ref := fields["postcondition"].(map[string]any)["$ref"]; ref != "#/$defs/assertion" {
			t.Fatalf("action %v postcondition does not reuse assertion schema: %v",
				branchTypes, ref)
		}
		target := fields["target"].(map[string]any)
		targetFields := target["properties"].(map[string]any)
		if len(targetFields) != 1 || targetFields["ref"] == nil {
			t.Fatalf("public %v target is not ref-only: %#v", branchTypes, target)
		}
		pattern := targetFields["ref"].(map[string]any)["pattern"]
		for _, branchType := range branchTypes {
			want := "^r[1-9][0-9]*$"
			if branchType == "activate" || branchType == "close" {
				want = "^w[1-9][0-9]*$"
			}
			if pattern != want {
				t.Fatalf("%s target pattern=%v want %s", branchType, pattern, want)
			}
		}
		if slices.Contains(branchTypes, "keypress") {
			if _, ok := fields["keys"]; !ok {
				t.Fatal("public keypress schema must use GUI-compatible keys")
			}
			if _, ok := fields["key"]; ok {
				t.Fatal("public keypress schema must hide legacy key")
			}
		}
	}
	for _, windowAction := range []string{"activate", "close"} {
		count := 0
		for _, raw := range actionBranches {
			fields := raw.(map[string]any)["properties"].(map[string]any)
			if fields["type"].(map[string]any)["const"] == windowAction {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("%s must have one independent const branch, got %d",
				windowAction, count)
		}
	}
	for _, want := range []string{
		"activate", "close", "click", "keypress", "scroll", "set_value", "type",
	} {
		if !slices.Contains(gotTypes, want) {
			t.Fatalf("a11y action types missing %q: %#v", want, gotTypes)
		}
	}

	valid := []any{
		map[string]any{
			"type":   "click",
			"target": map[string]any{"ref": "r7"},
			"postcondition": map[string]any{
				"type": "state",
				"target": map[string]any{
					"ref": "r7",
				},
				"state": "selected",
				"value": true,
			},
		},
		map[string]any{
			"type": "keypress", "keys": []any{"CTRL", "L"},
		},
		map[string]any{
			"type": "type",
			"target": map[string]any{
				"ref": "r8",
			},
			"text": "Lovelace",
		},
	}
	normalized, err := normalizeA11yBatchActions(valid)
	if err != nil || len(normalized) != len(valid) {
		t.Fatalf("valid a11y batch rejected: actions=%#v err=%v",
			normalized, err)
	}
	if normalized[0].postcondition == nil {
		t.Fatal("action postcondition was not preserved by normalization")
	}
	if _, err := normalizeA11yBatchActions([]any{
		map[string]any{
			"type":          "click",
			"target":        map[string]any{"ref": "r7"},
			"postcondition": "selected",
		},
	}); err == nil || !strings.Contains(err.Error(), "postcondition must be an AX assertion object") {
		t.Fatalf("non-object action postcondition must be rejected: %v", err)
	}
	if _, err := normalizeA11yBatchActions([]any{
		map[string]any{"type": "keypress", "keys": []any{"TAB"}},
		map[string]any{
			"type":   "click",
			"target": map[string]any{"index": float64(7)},
		},
	}); err == nil || !strings.Contains(err.Error(), "not part of the public act contract") {
		t.Fatalf("numeric index must fail ref-only batch validation: %v", err)
	}
	if _, err := normalizeA11yBatchActions([]any{
		map[string]any{
			"type": "keypress", "keys": []any{"TAB"}, "observe": "none",
		},
	}); err == nil || !strings.Contains(err.Error(), "does not accept field") {
		t.Fatalf("per-action observe must be rejected: %v", err)
	}
	if _, err := normalizeA11yBatchActions([]any{}); err == nil {
		t.Fatal("the normal mutating-batch validator must reject an empty sequence")
	}
	tooMany := make([]any, a11yBatchMaxActions+1)
	for index := range tooMany {
		tooMany[index] = map[string]any{
			"type": "keypress", "keys": []any{"TAB"},
		}
	}
	if _, err := normalizeA11yBatchActions(tooMany); err == nil {
		t.Fatal("a11y batch action count must be bounded")
	}
	if _, err := normalizeA11yBatchActions([]any{
		map[string]any{
			"type": "type", "text": strings.Repeat("a", maxActionText),
		},
		map[string]any{
			"type": "type", "text": strings.Repeat("b", maxActionText),
		},
		map[string]any{
			"type": "type", "text": "c",
		},
	}); err == nil || !strings.Contains(err.Error(), "cumulative") {
		t.Fatalf("a11y batch cumulative text budget must be bounded: %v", err)
	}

	service := newService()
	rejected := service.a11yBatch(
		"", []any{map[string]any{"type": "keypress", "keys": []any{"TAB"}}},
		observation{}, nil,
	)
	if !rejected.IsError ||
		rejected.StructuredContent["action_attempted"] != false {
		t.Fatalf("prevalidation failure must prove no action attempt: %#v", rejected)
	}
	noObservation := service.a11yBatch(
		"Editor", []any{}, observation{mode: observeNone}, nil,
	)
	if !noObservation.IsError ||
		!strings.Contains(resultText(noObservation), `mode="act" requires at least one action`) {
		t.Fatalf("empty a11y act must reject and point to observe mode: %#v",
			noObservation)
	}

	source := mainGoSource(t)
	for _, fragment := range []string{
		`a11yBatchActionObservation(`,
		`if index == count-1 && !hasPostcondition`,
		`postcondition != nil || action.postcondition != nil`,
		`postcondition != nil || actionPostconditionChecked`,
		`s.executeA11yBatchAction(`,
		`"observation_required"] = true`,
	} {
		if !strings.Contains(source, fragment) {
			t.Fatalf("a11y batch execution contract missing %q", fragment)
		}
	}
}

func TestInvalidActTreeObservationTellsModelToRetrySameAct(t *testing.T) {
	service := newService()
	result := service.a11yRequest(map[string]any{
		"mode": "act",
		"actions": []any{map[string]any{
			"type":   "click",
			"target": map[string]any{"ref": "r1"},
		}},
	}, observation{mode: observeTree})
	text := resultText(result)
	if !result.IsError ||
		!strings.Contains(text, "No action was executed") ||
		!strings.Contains(text, "Retry this same act") ||
		!strings.Contains(text, "do not add a separate observe first") {
		t.Fatalf("invalid act observe recovery is misleading: %q", text)
	}
}

func TestAXModelOutputDropsProviderImagesUnlessExplicitlyRequested(t *testing.T) {
	imageResult := toolCallResult{Content: []contentItem{
		{Type: "text", Text: "semantic evidence"},
		{Type: "image", Data: "provider-overreturned-image", MimeType: "image/png"},
	}}
	tests := []struct {
		name      string
		tool      string
		args      map[string]any
		wantImage bool
	}{
		{
			name: "act default diff",
			tool: "AX",
			args: map[string]any{"mode": "act"},
		},
		{
			name: "act explicit diff",
			tool: "AX",
			args: map[string]any{"mode": "act", "observe": "diff"},
		},
		{
			name: "act none",
			tool: "AX",
			args: map[string]any{"mode": "act", "observe": "none"},
		},
		{
			name:      "act screenshot",
			tool:      "AX",
			args:      map[string]any{"mode": "act", "observe": "screenshot"},
			wantImage: true,
		},
		{
			name: "observe default diff",
			tool: "AX",
			args: map[string]any{"mode": "observe"},
		},
		{
			name: "observe explicit diff",
			tool: "AX",
			args: map[string]any{"mode": "observe", "observe": "diff"},
		},
		{
			name: "observe tree",
			tool: "AX",
			args: map[string]any{"mode": "observe", "observe": "tree"},
		},
		{
			name:      "observe screenshot",
			tool:      "AX",
			args:      map[string]any{"mode": "observe", "observe": "screenshot"},
			wantImage: true,
		},
		{
			name:      "observe full",
			tool:      "AX",
			args:      map[string]any{"mode": "observe", "observe": "full"},
			wantImage: true,
		},
		{
			name: "observe apps",
			tool: "AX",
			args: map[string]any{
				"mode":               "observe",
				"observation_target": "apps",
				"observe":            "screenshot",
			},
		},
		{
			name: "find",
			tool: "AX",
			args: map[string]any{"mode": "find"},
		},
		{
			name: "assert",
			tool: "AX",
			args: map[string]any{"mode": "assert_ax"},
		},
		{
			name:      "gui is unaffected",
			tool:      "GUI",
			args:      map[string]any{},
			wantImage: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := enforceAXImageOutputPolicy(test.tool, test.args, imageResult)
			hasImage := false
			for _, item := range got.Content {
				if item.Type == "image" {
					hasImage = true
				}
			}
			if hasImage != test.wantImage {
				t.Fatalf(
					"image visibility = %t, want %t; content=%#v",
					hasImage,
					test.wantImage,
					got.Content,
				)
			}
			if len(got.Content) == 0 || got.Content[0].Text != "semantic evidence" {
				t.Fatalf("image policy changed semantic content: %#v", got.Content)
			}
		})
	}
}

func TestCallToolAppliesAXImagePolicyAtSharedOutputBoundary(t *testing.T) {
	service := newServiceWithBackend(&recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{
			OK: true,
			Snapshot: &appSnapshot{
				App:                 appDescriptor{Name: "Editor", PID: 7},
				WindowTitle:         "Editor",
				WindowHandle:        "window-7",
				TreeLines:           []string{`ax:`, ` - window "Editor"`},
				ScreenshotPNGBase64: "provider-overreturned-image",
				ScreenshotMimeType:  "image/png",
			},
		},
	})

	result := service.callTool("AX", map[string]any{
		"app":     "Editor",
		"mode":    "observe",
		"observe": "diff",
	})
	if result.IsError {
		t.Fatalf("AX observe unexpectedly failed: %#v", result)
	}
	for _, item := range result.Content {
		if item.Type == "image" {
			t.Fatalf("shared AX output boundary leaked provider image: %#v", result.Content)
		}
	}
}

func TestMissingPostActionSnapshotPreservesMutationEvidence(t *testing.T) {
	backend := &recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{
			OK: true,
			Mutation: &ProviderMutationFacts{
				InputEventsEmitted:  true,
				DesktopStateMutated: true,
			},
		},
	}
	service := newServiceWithBackend(backend)
	_, _, result := service.readSnapshot("Editor", axServiceRequest{
		Action:    axClickCommand{},
		App:       "Editor",
		SourceRef: "r17",
	})
	if !result.IsError {
		t.Fatalf("missing post-action snapshot must be an error: %#v", result)
	}
	if result.StructuredContent["failure_code"] != "effect_unknown" ||
		result.StructuredContent["underlying_failure_code"] !=
			"missing_post_action_snapshot" ||
		result.StructuredContent["input_events_emitted"] != true ||
		result.StructuredContent["mutation_unknown"] != true {
		t.Fatalf("post-action mutation evidence was lost: %#v", result.StructuredContent)
	}
}

func TestMissingPostActionSnapshotPreservesUnknownMutationEvidence(t *testing.T) {
	backend := &recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{
			OK: true,
			Mutation: &ProviderMutationFacts{
				MutationUnknown: true,
			},
		},
	}
	service := newServiceWithBackend(backend)
	_, _, result := service.readSnapshot("Editor", axServiceRequest{
		Action:    axClickCommand{},
		App:       "Editor",
		SourceRef: "r17",
	})
	if !result.IsError ||
		result.StructuredContent["failure_code"] != "effect_unknown" ||
		result.StructuredContent["mutation_unknown"] != true {
		t.Fatalf("unknown post-action mutation evidence was lost: %#v", result)
	}
	if strings.Contains(resultText(result), "input was emitted") {
		t.Fatalf("unknown-only mutation text falsely claimed emission: %s",
			resultText(result))
	}
}

func TestMissingPostActionSnapshotPromotesActionEvidence(t *testing.T) {
	backend := &recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{
			OK:             true,
			ActionEvidence: &ProviderActionEvidence{InputEmitted: true},
		},
	}
	service := newServiceWithBackend(backend)
	_, _, result := service.readSnapshot("Editor", axServiceRequest{
		Action:    axClickCommand{},
		App:       "Editor",
		SourceRef: "r17",
	})
	if !result.IsError ||
		result.StructuredContent["failure_code"] != "effect_unknown" ||
		result.StructuredContent["input_events_emitted"] != true ||
		result.StructuredContent["mutation_unknown"] != true {
		t.Fatalf("action evidence was not promoted into mutation facts: %#v", result)
	}
}

func TestEffectUnknownFailurePromotesActionEvidence(t *testing.T) {
	service := newService()
	result := service.runtimeReportedFailure(
		"Editor",
		axServiceRequest{Action: axClickCommand{}, SourceRef: "r17"},
		ProviderAccessibilityResult{
			OK: false,
			Failure: &ProviderFailure{
				Code:  "effect_unknown",
				Phase: "post_action_observation",
			},
			ActionEvidence: &ProviderActionEvidence{InputEmitted: true},
		},
	)
	if result.StructuredContent["input_events_emitted"] != true ||
		result.StructuredContent["mutation_unknown"] != true {
		t.Fatalf("effect_unknown failure lost action evidence: %#v", result)
	}
}

func TestPrepareAXEvidenceKeepsUnderlyingFailureCode(t *testing.T) {
	result := toolCallResult{
		IsError: true,
		Content: []contentItem{{
			Type: "text",
			Text: "effect unknown",
		}},
		StructuredContent: map[string]any{
			"failure_code":            "effect_unknown",
			"underlying_failure_code": "missing_post_action_snapshot",
			"failure_phase":           "final_observation",
			"input_events_emitted":    true,
			"mutation_unknown":        true,
		},
	}
	visible := prepareAXEvidence(result)
	if visible.StructuredContent["underlying_failure_code"] !=
		"missing_post_action_snapshot" {
		t.Fatalf(
			"model output lost the underlying failure code: %#v",
			visible.StructuredContent,
		)
	}
}

func TestA11yStructuredTargetNormalizesWithoutAmbiguousFields(t *testing.T) {
	ref := "r17"
	cases := []struct {
		name   string
		target map[string]any
		want   string
	}{
		{
			name: "index",
			target: map[string]any{
				"index": float64(7),
			},
			want: "7",
		},
		{
			name: "ref",
			target: map[string]any{
				"ref": ref,
			},
			want: ref,
		},
		{
			name: "selector",
			target: map[string]any{
				"selector": map[string]any{
					"role":        "push button",
					"name":        "Save",
					"description": "Save document",
					"state":       "focused",
				},
			},
			want: `push button "Save" [desc="Save document"] [focused]`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, present, err := a11yTargetAddress(map[string]any{
				"target": testCase.target,
			})
			if err != nil || !present || got != testCase.want {
				t.Fatalf("address=%q present=%v err=%v want=%q",
					got, present, err, testCase.want)
			}
		})
	}

	for _, invalid := range []map[string]any{
		{"target": "Save"},
		{"target": map[string]any{}},
		{"target": map[string]any{"index": float64(1), "ref": ref}},
		{"target": map[string]any{"index": float64(1.5)}},
		{"target": map[string]any{"ref": "not-a-ref"}},
		{"target": map[string]any{"ref": encodeSemanticRef(`push button "Save"`)}},
		{"target": map[string]any{"ref": encodeScopedSemanticRef(`push button "Save"`, "w2:test")}},
		{"target": map[string]any{"selector": map[string]any{}}},
		{"target": map[string]any{
			"selector": map[string]any{"role:document": "document"},
		}},
		{"target": map[string]any{
			"selector": map[string]any{"action": "click"},
		}},
		{"target": map[string]any{
			"selector": map[string]any{"state": "click"},
		}},
	} {
		if _, _, err := a11yTargetAddress(invalid); err == nil {
			t.Fatalf("invalid target accepted: %#v", invalid)
		}
	}
}

func TestA11yBatchRejectsRemovedIntentField(t *testing.T) {
	_, err := normalizeA11yBatchActions([]any{
		map[string]any{
			"type":   "click",
			"intent": "the Save button",
			"target": map[string]any{
				"selector": map[string]any{
					"role": "push button",
					"name": "Save",
				},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), `does not accept field "intent"`) {
		t.Fatalf("removed intent field was accepted: %v", err)
	}
}

func TestA11yKeypressUsesOnlyPublicKeys(t *testing.T) {
	normalized, err := normalizeA11yBatchActions([]any{
		map[string]any{
			"type": "keypress", "keys": []any{"CTRL", "L"},
		},
	})
	if err != nil || len(normalized) != 1 {
		t.Fatalf("keypress rejected: %#v err=%v", normalized, err)
	}
	if got := normalized[0].key; got != "CTRL+L" {
		t.Fatalf("normalized key=%#v want CTRL+L", got)
	}
	if _, err := normalizeA11yBatchActions([]any{
		map[string]any{
			"type": "keypress", "key": "Tab",
		},
	}); err == nil || !strings.Contains(err.Error(), `does not accept field "key"`) {
		t.Fatalf("legacy key must fail preflight: %v", err)
	}
}

func TestAXRejectsAllRemovedCompatibilityForms(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"missing mode": {"app": "Editor"},
		"assert_a11y alias": {
			"mode": "assert_a11y",
			"app":  "Editor",
			"assertion": map[string]any{
				"window_title_contains": "Editor",
			},
		},
	} {
		if _, err := parseA11yMode(args); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	for name, action := range map[string]map[string]any{
		"element_index": {
			"type": "click", "element_index": 1,
		},
		"element": {
			"type": "click", "element": "Save",
		},
		"key": {
			"type": "keypress", "key": "TAB",
		},
	} {
		if _, err := normalizeA11yBatchActions([]any{action}); err == nil {
			t.Fatalf("legacy AX field %s was accepted", name)
		}
	}
}

func TestInvalidStructuredAssertionFailsBeforeFreshUIRead(t *testing.T) {
	service := newService()
	result := service.callTool("a11y", map[string]any{
		"mode": "assert_ax",
		"app":  "Editor",
		"assertion": map[string]any{
			"type": "exists",
			"target": map[string]any{
				"selector": map[string]any{"role:document": "document"},
			},
			"value": true,
		},
	})
	if !result.IsError ||
		!strings.Contains(resultText(result), "assertion.target") {
		t.Fatalf("invalid structured assertion must fail clearly: %#v", result)
	}
	for key, want := range map[string]any{
		"evidence_scope":                 "error_only",
		"error_context_captured":         false,
		"error_context_fresh_ui_sampled": false,
		"preflight_only":                 true,
	} {
		if got := result.StructuredContent[key]; got != want {
			t.Fatalf("%s=%#v want %#v in %#v",
				key, got, want, result.StructuredContent)
		}
	}
}

func TestActionBoundsRejectUnboundedInput(t *testing.T) {
	service := newService()
	for name, args := range map[string]map[string]any{
		"click": {
			"app": "Editor", "element_index": "1",
			"click_count": float64(maxClickCount + 1),
		},
		"scroll": {
			"app": "Editor", "direction": "down",
			"pages": float64(maxScrollPages + 1),
		},
		"verify": {
			"app": "Editor", "element_index": "1", "exists": true,
			"timeout_ms": float64(maxVerifyTimeout + 1),
		},
		"type_text": {
			"app": "Editor", "text": strings.Repeat("x", maxActionText+1),
		},
		"set_value": {
			"app": "Editor", "element_index": "1",
			"value": strings.Repeat("x", maxActionText+1),
		},
	} {
		result := service.dispatch(name, args)
		if !result.IsError {
			t.Fatalf("%s accepted unbounded input", name)
		}
	}
}

func TestMCPFramingRecoversAtNewlineAndRejectsOversizedMessages(t *testing.T) {
	valid := `{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n"
	input := strings.NewReader("{not-json}\n" + valid)
	var output bytes.Buffer
	if err := runMCP(input, &output); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"code":-32700`) ||
		!strings.Contains(lines[1], `"id":1`) {
		t.Fatalf("newline framing did not recover cleanly: %q", output.String())
	}

	oversized := strings.NewReader(strings.Repeat("x", maxMCPMessageBytes+1) + "\n")
	if err := runMCP(oversized, io.Discard); err == nil {
		t.Fatal("oversized MCP message must terminate the connection")
	}
}

func TestMCPRequestDecodePreservesExactJSONNumbers(t *testing.T) {
	request, err := decodeMCPRequest([]byte(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` +
			`{"name":"AX","arguments":{"mode":"assert_ax","assertion":` +
			`{"type":"value","target":{"ref":"r1"},` +
			`"value":9007199254740993}}}}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	params := request["params"].(map[string]any)
	arguments := params["arguments"].(map[string]any)
	assertion := arguments["assertion"].(map[string]any)
	value, ok := assertion["value"].(json.Number)
	if !ok || value.String() != "9007199254740993" {
		t.Fatalf("MCP transport changed exact JSON number: %#v",
			assertion["value"])
	}
	if _, err := decodeMCPRequest([]byte(`{} {}`)); err == nil {
		t.Fatal("MCP transport accepted multiple JSON values")
	}
}

func TestRuntimeTimeoutDiagnosticsSeparateProcessWindowAndBridgeState(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		needle string
	}{
		{
			name:   "no cached identity",
			text:   formatRuntimeTimeoutDiagnostic(0, false, false, false, false),
			needle: "a11y_responsive=false cached_pid_present=unknown window_for_cached_pid=unknown",
		},
		{
			name:   "process exited",
			text:   formatRuntimeTimeoutDiagnostic(42, true, false, true, false),
			needle: "a11y_responsive=false cached_pid_present=false window_for_cached_pid=false cached_pid=42",
		},
		{
			name:   "bridge hung with window",
			text:   formatRuntimeTimeoutDiagnostic(42, true, true, true, true),
			needle: "a11y_responsive=false cached_pid_present=true window_for_cached_pid=true cached_pid=42",
		},
		{
			name:   "process rebuilding window",
			text:   formatRuntimeTimeoutDiagnostic(42, true, true, true, false),
			needle: "a11y_responsive=false cached_pid_present=true window_for_cached_pid=false cached_pid=42",
		},
	}
	for _, testCase := range cases {
		if !strings.Contains(testCase.text, testCase.needle) {
			t.Fatalf("%s diagnostic missing %q: %s",
				testCase.name, testCase.needle, testCase.text)
		}
	}

	root := t.TempDir()
	if processPIDExists(123, root) {
		t.Fatal("missing proc pid must report false")
	}
	if err := os.Mkdir(filepath.Join(root, "123"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !processPIDExists(123, root) {
		t.Fatal("existing proc pid directory must report true")
	}
	if err := os.WriteFile(
		filepath.Join(root, "123", "stat"),
		[]byte("123 (vlc media player) Z 1 2 3 4"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if processPIDExists(123, root) || processPIDState(123, root) != "zombie" {
		t.Fatal("a zombie proc entry must not be treated as a live application")
	}
	if text := formatZombieRuntimeDiagnostic(123); !strings.Contains(
		text,
		"cached_pid_present=zombie",
	) || !strings.Contains(text, "cannot become ready") {
		t.Fatalf("zombie diagnostic is not actionable: %s", text)
	}
	if err := os.WriteFile(
		filepath.Join(root, "123", "stat"),
		[]byte("123 (normal app) S 1 2 3 4"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if !processPIDExists(123, root) || processPIDState(123, root) != "running" {
		t.Fatal("a non-zombie proc entry must remain live")
	}
	output := "0x01200007  0  123 host Window A\n0x01400007  0  999 host Window B\n"
	if !wmctrlOutputHasPID(output, 123) || wmctrlOutputHasPID(output, 321) {
		t.Fatal("wmctrl PID parser returned the wrong liveness result")
	}
}

func TestRuntimeTimeoutSkipsRecursiveErrorContext(t *testing.T) {
	source := mainGoSource(t)
	if !strings.Contains(source, `strings.Contains(message, "runtime_timeout=true")`) {
		t.Fatal("a11y timeout errors must skip a second get_app_state error-context capture")
	}
}

func TestRuntimeTimeoutSeparatesReadAndActionUncertainty(t *testing.T) {
	service := newService()
	snapshot := &appSnapshot{App: appDescriptor{PID: 42}}
	timeout := &runtimeTimeoutError{
		duration: 30 * time.Second,
		phase:    "read_response",
	}

	read := service.runtimeFailureResult(
		axServiceRequest{Operation: axServiceObserve}, snapshot, timeout)
	readText := read.Content[0].Text
	if !strings.Contains(readText, "runtime_timeout=true mutation_unknown=false") ||
		!strings.Contains(readText, "a11y_responsive=false") {
		t.Fatalf("pure tree read timeout should identify A11Y non-response: %s", readText)
	}
	if read.StructuredContent["failure_code"] != "runtime_timeout" ||
		read.StructuredContent["failure_phase"] != "read_response" ||
		read.StructuredContent["mutation_unknown"] != false {
		t.Fatalf("read timeout evidence is incomplete: %#v", read.StructuredContent)
	}

	action := service.runtimeFailureResult(
		axServiceRequest{Action: axClickCommand{}}, snapshot, timeout)
	actionText := action.Content[0].Text
	if !strings.Contains(actionText, "runtime_timeout=true mutation_unknown=true") ||
		!strings.Contains(actionText, "a11y_responsive=unknown") {
		t.Fatalf("action timeout must not blame A11Y or imply no mutation: %s", actionText)
	}
	if action.StructuredContent["failure_code"] != "runtime_timeout" ||
		action.StructuredContent["failure_phase"] != "read_response" ||
		action.StructuredContent["mutation_unknown"] != true ||
		action.StructuredContent["retry_same_request"] != false {
		t.Fatalf("action timeout evidence is incomplete: %#v", action.StructuredContent)
	}

	old := &appSnapshot{capturedAt: time.Now().Add(-time.Second)}
	newest := &appSnapshot{capturedAt: time.Now()}
	service.snapshots["old"] = old
	service.snapshots["new"] = newest
	if service.latestSnapshot() != newest {
		t.Fatal("foreground timeout should use the most recent cached snapshot identity")
	}

	source := mainGoSource(t)
	for _, marker := range []string{
		"cmd.Env = environment",
		"linuxX11WindowForPID(pid, backend.RuntimeEnvironment(nil))",
	} {
		if !strings.Contains(source, marker) {
			t.Fatalf("wmctrl timeout probe must use the backend desktop environment: missing %q", marker)
		}
	}
}

func TestGUIModelResponseKeepsLayoutProvenancePrivate(t *testing.T) {
	snapshot := &appSnapshot{
		ScreenshotPNGBase64: "png",
		CoordinateSpace:     "screen_image_px",
		FrameID:             "current-frame",
		ContentFrameID:      "content-frame",
		DesktopLayout: map[string]any{
			"activeWindow": "0x20",
			"windows":      []any{map[string]any{"id": "0x20"}},
		},
		Width:  1920,
		Height: 1080,
	}
	text := guiMetadata(snapshot, map[string]any{
		"dispatched":               1,
		"input_events_emitted":     true,
		"input_actions_dispatched": 1,
		"effect_unknown":           true,
		"quiescent":                true,
		"visual_change_observed":   false,
	})
	for _, fragment := range []string{
		`"coordinate_space":"screen_image_px"`,
		`"width":1920`,
		`"height":1080`,
		`"screenshot_returned":true`,
		`"dispatched":1`,
		`"input_actions_dispatched":1`,
		`"input_events_emitted":true`,
		`"effect_unknown":true`,
		`"quiescent":true`,
		`"visual_change_observed":false`,
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("compact GUI response missing %q: %s", fragment, text)
		}
	}
	for _, forbidden := range []string{
		"frame_id", "content_frame_id", "desktop_layout", "0x20",
		"screenshot_encoding", "screenshot_mime_type",
		"input_actions_emitted", "visual_change_percent",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("model response leaked private GUI provenance %q: %s", forbidden, text)
		}
	}
}

func TestCompactGUIResultPreservesUnknownEffect(t *testing.T) {
	compact := compactGUIResult(map[string]any{
		"dispatched":           1,
		"input_events_emitted": true,
		"effect_unknown":       true,
	})
	if compact["effect_unknown"] != true {
		t.Fatalf("GUI compact evidence hid effect_unknown: %#v", compact)
	}
}

func TestGUISuccessDoesNotAddStaticEvidencePrefix(t *testing.T) {
	service := newService()
	emitted := true
	effectUnknown := true
	quiescent := true
	dispatched := 1
	result := service.guiResponse(ProviderDesktopResult{
		OK: true,
		Snapshot: &appSnapshot{
			ScreenshotPNGBase64: testPNGBase64(t, 1920, 1080),
			CoordinateSpace:     "screen_image_px",
			Width:               1920,
			Height:              1080,
		},
		GUIResult: ProviderGUIResult{
			Dispatched:         &dispatched,
			InputEventsEmitted: &emitted,
			EffectUnknown:      &effectUnknown,
			Quiescent:          &quiescent,
		},
	})
	if result.StructuredContent != nil {
		t.Fatalf("routine GUI success returned static evidence: %#v",
			result.StructuredContent)
	}
	visible := withTextEvidence(result)
	if strings.Contains(resultText(visible), "[tool-evidence]") {
		t.Fatalf("routine GUI success added fixed evidence prefix: %q",
			resultText(visible))
	}
	source := mainGoSource(t)
	if !strings.Contains(source, `if internalName != "gui"`) {
		t.Fatal("callTool still applies the generic evidence wrapper to GUI")
	}
}

func TestLocalGUIErrorUsesStableSchema(t *testing.T) {
	result := localGUIError("missing frame")
	if !result.IsError {
		t.Fatal("local GUI error must set isError")
	}
	if result.Content[0].Text != "missing frame" {
		t.Fatalf("local GUI error leaked default telemetry: %q", result.Content[0].Text)
	}
}

func TestGUIInputEmissionInvalidatesAccessibilitySnapshots(t *testing.T) {
	response := func(emitted bool) ProviderDesktopResult {
		return ProviderDesktopResult{
			OK: true,
			Snapshot: &appSnapshot{
				ScreenshotPNGBase64: testPNGBase64(t, 100, 80),
				CoordinateSpace:     "screen_image_px",
				FrameID:             "frame",
				Width:               100,
				Height:              80,
			},
			GUIResult: ProviderGUIResult{InputEventsEmitted: &emitted},
		}
	}

	service := newService()
	service.rememberSnapshot("Editor", expectSnapshot())
	service.guiResponse(response(false))
	if service.currentSnapshot("Editor") == nil {
		t.Fatal("pure GUI observation or pre-emission rejection must preserve accessibility snapshots")
	}

	service.guiResponse(response(true))
	if len(service.snapshots) != 0 || service.currentSnapshot("Editor") != nil {
		t.Fatal("input_events_emitted=true must invalidate every accessibility snapshot and expect baseline")
	}

	service.rememberSnapshot("Editor", expectSnapshot())
	partialFailure := response(false)
	partialFailure.OK = false
	partialFailure.Error = "input failed after partial emission"
	partialFailure.Mutation = &ProviderMutationFacts{InputEventsEmitted: true}
	service.guiResponse(partialFailure)
	if len(service.snapshots) != 0 {
		t.Fatal("top-level input_events_emitted=true on a partial GUI failure must also invalidate snapshots")
	}

	service.rememberSnapshot("Editor", expectSnapshot())
	partialOnly := response(false)
	partialOnly.OK = false
	partialOnly.Mutation = &ProviderMutationFacts{PartialInputPossible: true}
	service.guiResponse(partialOnly)
	if len(service.snapshots) != 0 {
		t.Fatal("partial_input_possible=true must invalidate snapshots even when no complete input action was counted")
	}

	service.rememberSnapshot("Editor", expectSnapshot())
	windowOnly := response(false)
	windowOnly.Mutation = &ProviderMutationFacts{DesktopStateMutated: true}
	service.guiResponse(windowOnly)
	if len(service.snapshots) != 0 {
		t.Fatal("desktop_state_mutated=true must invalidate snapshots independently of input events")
	}

	service.rememberSnapshot("Editor", expectSnapshot())
	unknown := response(false)
	unknown.Mutation = &ProviderMutationFacts{MutationUnknown: true}
	unknownResult := service.guiResponse(unknown)
	if len(service.snapshots) != 0 {
		t.Fatal("mutation_unknown=true must invalidate snapshots after GUI runtime timeout")
	}
	if !strings.Contains(resultText(unknownResult), `"mutation_unknown":true`) {
		t.Fatalf("top-level GUI mutation facts were hidden: %s",
			resultText(unknownResult))
	}

	field, ok := reflect.TypeOf(ProviderMutationFacts{}).FieldByName("InputEventsEmitted")
	if !ok || field.Tag.Get("json") != "input_events_emitted,omitempty" {
		t.Fatal("ProviderMutationFacts must decode input_events_emitted from partial failures")
	}
	if field, ok := reflect.TypeOf(ProviderMutationFacts{}).FieldByName("MutationUnknown"); !ok ||
		field.Tag.Get("json") != "mutation_unknown,omitempty" {
		t.Fatal("ProviderMutationFacts must decode GUI mutation_unknown")
	}
}

func TestGUIFailureInvalidationUsesReportedFacts(t *testing.T) {
	source := mainGoSource(t)
	if strings.Contains(source, "if !response.OK && guiBatchCanEmitInput(actions)") {
		t.Fatal("pre-emission GUI rejection must not invalidate snapshots merely because the requested batch could have emitted input")
	}
	if !strings.Contains(source, "providerMutationCanInvalidate(response.Mutation)") {
		t.Fatal("GUI invalidation must use runtime-reported input/partial/desktop mutation facts")
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

func TestSnapshotProjectionIsPreservedAcrossActionAndSettleReads(t *testing.T) {
	prune := false
	boxes := true
	projection := snapshotProjection{
		TextLimit: "max", MaxTreeNodes: 321, MaxTreeDepth: 17,
		Prune: &prune, Boxes: &boxes,
	}
	request := axServiceRequest{Action: axClickCommand{}}
	applyProjection(&request, projection)
	if request.TextLimit != "max" || request.MaxTreeNodes != 321 ||
		request.MaxTreeDepth != 17 || request.Prune == nil || *request.Prune ||
		request.Boxes == nil || !*request.Boxes {
		t.Fatalf("projection was not preserved: %#v", request)
	}
	settle := settleReadRequest("Editor", nil, "window", projection)
	if settle.MaxTreeNodes != 321 || settle.MaxTreeDepth != 17 ||
		settle.Prune == nil || *settle.Prune {
		t.Fatalf("settle read lost the baseline projection: %#v", settle)
	}
	for _, fragment := range []string{
		`observation.get("max_tree_nodes")`,
		`observation.get("max_tree_depth")`,
		`observation.get("prune", True)`,
	} {
		if !strings.Contains(linuxRuntimeScript, fragment) {
			t.Fatalf("runtime action readback must preserve projection field %q", fragment)
		}
	}
}

func TestVerifyPreservesProjectionWithoutReplacingActionCache(t *testing.T) {
	prune := false
	boxes := true
	cached := &appSnapshot{
		App: appDescriptor{Name: "Editor", PID: 42},
		projection: snapshotProjection{
			TextLimit: "max", MaxTreeNodes: 321, MaxTreeDepth: 17,
			Prune: &prune, Boxes: &boxes,
		},
	}
	service := newService()
	service.rememberSnapshot("Editor", cached)

	request := axServiceRequest{Operation: axServiceObserve, App: "Editor"}
	applyProjection(&request, service.currentSnapshot("Editor").projection)
	if request.TextLimit != "max" || request.MaxTreeNodes != 321 ||
		request.MaxTreeDepth != 17 || request.Prune == nil || *request.Prune ||
		request.Boxes == nil || !*request.Boxes {
		t.Fatalf("read-only query lost the cached projection: %#v", request)
	}
	if service.currentSnapshot("Editor") != cached {
		t.Fatal("building a read-only request must not replace the action cache")
	}

	source := mainGoSource(t)
	for _, marker := range []string{
		"snapshot, _, result := s.readProjectedSnapshot(app, axServiceRequest{",
	} {
		if !strings.Contains(source, marker) {
			t.Fatalf("verify projection contract missing %q", marker)
		}
	}
	for _, forbidden := range []string{
		"retryRequest.Prune = &unpruned",
		"[search escalation]",
		`StructuredContent["auto_unpruned_retry"]`,
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("v53 find must not auto-escalate or report rejected metadata %q", forbidden)
		}
	}
}

func TestRootWindowChangeDropsOldStableReferences(t *testing.T) {
	source := mainGoSource(t)
	for _, fragment := range []string{
		"!accessibilityOperationCanMutate(request)",
		"previous.WindowHandle != response.Snapshot.WindowHandle",
		"request.KnownRefs = nil",
		"response, err = s.callProviderAccessibilityOperation(request)",
	} {
		if !strings.Contains(source, fragment) {
			t.Fatalf("root-window changes must force one fresh numbering pass: missing %q", fragment)
		}
	}
}

func TestMutatingAXRequestIsNeverReplayedAfterWindowChange(t *testing.T) {
	previous := expectSnapshot()
	previous.WindowHandle = "w2:" + strings.Repeat("a", 32)
	previous.Refs = map[string]elementRef{"r1": {Index: 1}}
	current := expectSnapshot()
	current.WindowHandle = "w2:" + strings.Repeat("b", 32)
	backend := &recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{
			OK:       true,
			Snapshot: current,
			Mutation: &ProviderMutationFacts{InputEventsEmitted: true},
		},
	}
	service := newServiceWithBackend(backend)
	service.rememberSnapshot("Editor", previous)
	_, _, result := service.readSnapshot("Editor", axServiceRequest{
		Action:    axClickCommand{},
		App:       "Editor",
		SourceRef: "r1",
	})
	if result.IsError {
		t.Fatalf("mutating request failed: %s", resultText(result))
	}
	if len(backend.calls) != 1 || backend.calls[0] != "resolve_and_act" {
		t.Fatalf("mutating request was replayed: %#v", backend.calls)
	}
}

func TestBatchMutationSummaryPreservesActionUnknown(t *testing.T) {
	emitted, partial, unknown := batchMutationSummary([]map[string]any{
		{
			"input_events_emitted": false,
			"mutation_unknown":     true,
		},
	})
	if emitted || partial || !unknown {
		t.Fatalf(
			"batch summary lost mutation uncertainty: emitted=%t partial=%t unknown=%t",
			emitted, partial, unknown,
		)
	}
}

func TestTransportFailurePreservesActionUncertainty(t *testing.T) {
	service := newService()
	result := service.runtimeFailureResult(
		axServiceRequest{Action: axClickCommand{}},
		nil,
		&runtimeTransportError{
			operation:   ProviderWireResolveAndAct,
			phase:       "decode_response",
			requestSent: true,
		},
	)
	if result.StructuredContent["failure_code"] != "runtime_transport_failure" ||
		result.StructuredContent["failure_phase"] != "decode_response" ||
		result.StructuredContent["mutation_unknown"] != true {
		t.Fatalf("transport uncertainty was lost: %#v", result.StructuredContent)
	}
}

func TestIncompleteSnapshotCannotAuthorizeProviderRebind(t *testing.T) {
	service := newService()
	windowHandle := "w2:" + strings.Repeat("a", 32)
	ref := service.refRegistry.AcquireTyped(
		AXLocatorSemantic,
		"editor",
		windowHandle,
		`button "Save"`,
		"button",
		"Save",
		"c1",
		":1.42", "/old/save", "99123",
		":1.42", "/window", "99123",
		"frame", "Editor", "a1", "w1",
	)
	snapshot := &appSnapshot{
		App:             appDescriptor{Name: "Editor"},
		WindowHandle:    windowHandle,
		publicWindowRef: "w1",
		Annotations: []axProviderAnnotation{{
			Kind: "omitted", Reason: "provider_timeout",
		}},
		axSnapshot: &AXSnapshotV1{
			Revision:     "c1",
			WindowHandle: windowHandle,
			WindowRef:    "w1",
		},
	}
	service.rememberSnapshot("editor", snapshot)
	resolution, result := service.resolveActionElement(
		"editor", ref, "", "click",
	)
	if result.IsError {
		t.Fatalf("semantic ref was rejected too early: %s", resultText(result))
	}
	if resolution.semantic == nil || resolution.semantic.AllowProviderRebind {
		t.Fatalf("incomplete snapshot authorized provider rebind: %#v",
			resolution.semantic)
	}
}

func TestGUIFailureWithoutSnapshotReturnsMutationEvidence(t *testing.T) {
	service := newService()
	result := service.guiResponse(ProviderDesktopResult{
		OK:    false,
		Error: "post-action screenshot failed",
		Failure: &ProviderFailure{
			Code:  "effect_unknown",
			Phase: "post_action_observation",
		},
		Mutation: &ProviderMutationFacts{
			InputEventsEmitted: true,
			MutationUnknown:    true,
		},
	})
	if result.StructuredContent["failure_code"] != "effect_unknown" {
		t.Fatalf("GUI failure code missing: %#v", result.StructuredContent)
	}
	actions, _ := result.StructuredContent["actions"].(map[string]any)
	if actions["input_events_emitted"] != true ||
		actions["mutation_unknown"] != true {
		t.Fatalf("GUI mutation evidence missing: %#v", result.StructuredContent)
	}
}

func TestGUIMissingObservationShapesPreserveMutationEvidence(t *testing.T) {
	truncatedPNG := testPNGBase64(t, 100, 80)
	truncatedPNG = truncatedPNG[:len(truncatedPNG)/2]
	cases := []ProviderDesktopResult{
		{
			OK:       true,
			Mutation: &ProviderMutationFacts{MutationUnknown: true},
		},
		{
			OK:       true,
			Mutation: &ProviderMutationFacts{InputEventsEmitted: true},
			Snapshot: &appSnapshot{
				CoordinateSpace: "screen_image_px",
				Width:           100,
				Height:          80,
			},
		},
		{
			OK:       true,
			Mutation: &ProviderMutationFacts{InputEventsEmitted: true},
			Snapshot: &appSnapshot{
				ScreenshotPNGBase64: "png",
				CoordinateSpace:     "screen_image_px",
			},
		},
		{
			OK:       true,
			Mutation: &ProviderMutationFacts{InputEventsEmitted: true},
			Snapshot: &appSnapshot{
				FrameID:             "undelivered-invalid-base64",
				ScreenshotPNGBase64: "not base64!",
				CoordinateSpace:     "screen_image_px",
				Width:               100,
				Height:              80,
			},
		},
		{
			OK:       true,
			Mutation: &ProviderMutationFacts{InputEventsEmitted: true},
			Snapshot: &appSnapshot{
				FrameID:             "undelivered-invalid-image",
				ScreenshotPNGBase64: base64.StdEncoding.EncodeToString([]byte("not-a-png")),
				CoordinateSpace:     "screen_image_px",
				Width:               100,
				Height:              80,
			},
		},
		{
			OK:       true,
			Mutation: &ProviderMutationFacts{InputEventsEmitted: true},
			Snapshot: &appSnapshot{
				FrameID:             "undelivered-truncated-image",
				ScreenshotPNGBase64: truncatedPNG,
				ScreenshotMimeType:  "image/png",
				CoordinateSpace:     "screen_image_px",
				Width:               100,
				Height:              80,
			},
		},
		{
			OK:       true,
			Mutation: &ProviderMutationFacts{InputEventsEmitted: true},
			Snapshot: &appSnapshot{
				FrameID:             "undelivered-size-mismatch",
				ScreenshotPNGBase64: testPNGBase64(t, 1, 1),
				CoordinateSpace:     "screen_image_px",
				Width:               100,
				Height:              80,
			},
		},
	}
	for index, response := range cases {
		service := newService()
		previous := &appSnapshot{
			FrameID:             "delivered-frame",
			ScreenshotPNGBase64: "old",
			CoordinateSpace:     "screen_image_px",
			Width:               100,
			Height:              80,
		}
		service.guiSnapshot = previous
		result := service.guiResponse(response)
		if !result.IsError ||
			result.StructuredContent["failure_code"] != "effect_unknown" ||
			result.StructuredContent["failure_phase"] !=
				"post_action_observation" ||
			result.StructuredContent["observation_required"] != true {
			t.Fatalf("case %d lost GUI observation failure evidence: %#v",
				index, result)
		}
		actions, _ := result.StructuredContent["actions"].(map[string]any)
		if actions["mutation_unknown"] != true {
			t.Fatalf("case %d lost GUI mutation uncertainty: %#v",
				index, result.StructuredContent)
		}
		if service.guiSnapshot != previous {
			t.Fatalf("case %d cached an undeliverable GUI snapshot", index)
		}
	}
}

func TestGUIResponseAcceptsValidatedJPEGSnapshot(t *testing.T) {
	service := newService()
	result := service.guiResponse(ProviderDesktopResult{
		OK: true,
		Snapshot: &appSnapshot{
			FrameID:             "jpeg-frame",
			ScreenshotPNGBase64: testJPEGBase64(t, 100, 80),
			ScreenshotMimeType:  "image/jpeg",
			CoordinateSpace:     "screen_image_px",
			Width:               100,
			Height:              80,
		},
	})
	if result.IsError {
		t.Fatalf("valid JPEG snapshot was rejected: %#v", result)
	}
	if service.guiSnapshot == nil || service.guiSnapshot.FrameID != "jpeg-frame" {
		t.Fatalf("valid JPEG snapshot was not cached: %#v", service.guiSnapshot)
	}
}

func TestGUIResponseCanonicalizesMissingJPEGMIME(t *testing.T) {
	service := newService()
	result := service.guiResponse(ProviderDesktopResult{
		OK: true,
		Snapshot: &appSnapshot{
			FrameID:             "jpeg-frame-without-mime",
			ScreenshotPNGBase64: testJPEGBase64(t, 100, 80),
			CoordinateSpace:     "screen_image_px",
			Width:               100,
			Height:              80,
		},
	})
	if result.IsError {
		t.Fatalf("valid JPEG snapshot without MIME was rejected: %#v", result)
	}
	if service.guiSnapshot == nil ||
		service.guiSnapshot.ScreenshotMimeType != "image/jpeg" {
		t.Fatalf("detected JPEG MIME was not cached: %#v", service.guiSnapshot)
	}
	if len(result.Content) < 2 || result.Content[1].MimeType != "image/jpeg" {
		t.Fatalf("detected JPEG MIME was not rendered: %#v", result.Content)
	}
}

func TestGUIResponseRejectsDeclaredMIMEThatDoesNotMatchImage(t *testing.T) {
	service := newService()
	previous := &appSnapshot{FrameID: "previous-frame"}
	service.guiSnapshot = previous
	result := service.guiResponse(ProviderDesktopResult{
		OK: true,
		Snapshot: &appSnapshot{
			FrameID:             "mismatched-mime",
			ScreenshotPNGBase64: testJPEGBase64(t, 100, 80),
			ScreenshotMimeType:  "image/png",
			CoordinateSpace:     "screen_image_px",
			Width:               100,
			Height:              80,
		},
	})
	if !result.IsError {
		t.Fatalf("mismatched image MIME was accepted: %#v", result)
	}
	if service.guiSnapshot != previous {
		t.Fatal("mismatched image MIME replaced the previous coordinate frame")
	}
}

func TestGUIFrameGuardAppliesOnlyToCoordinateActions(t *testing.T) {
	if guiBatchUsesFrameProvenance([]any{
		map[string]any{"type": "keypress", "keys": []any{"CTRL", "S"}},
		map[string]any{"type": "type", "text": "hello"},
	}) {
		t.Fatal("keyboard/text-only GUI batches must not depend on screenshot coordinates")
	}
	for _, action := range []map[string]any{
		{"type": "click", "x": 1.0, "y": 2.0},
		{"type": "drag", "path": []any{}},
		{"type": "scroll", "x": 1.0, "y": 2.0, "scroll_y": 1.0},
	} {
		if !guiBatchUsesFrameProvenance([]any{action}) {
			t.Fatalf("coordinate action must require frame guard: %#v", action)
		}
	}
	for _, action := range []map[string]any{
		{"type": "activate_window", "x": 100.0, "y": 100.0},
		{"type": "close_window", "x": 100.0, "y": 100.0},
	} {
		if guiBatchUsesFrameProvenance([]any{action}) {
			t.Fatalf("removed GUI window action must not enter frame provenance: %#v", action)
		}
	}
}

func TestGUISchemaUsesComputerScrollNamesAndExcludesWindowActions(t *testing.T) {
	service := newService()
	for _, args := range []map[string]any{
		{"actions": []any{map[string]any{
			"type": "scroll", "x": 1, "y": 2, "delta_y": 3,
		}}},
		{"actions": []any{map[string]any{
			"type": "right_click", "x": 1, "y": 2,
		}}},
		{"actions": []any{map[string]any{
			"type": "wait", "seconds": 0.2,
		}}},
		{"actions": []any{map[string]any{
			"type": "activate_window",
		}}},
		{"actions": []any{}, "settle_ms": 100},
		{"actions": []any{}, "expect_change": true},
	} {
		result := service.dispatch("gui", args)
		if !result.IsError {
			t.Fatalf("GUI accepted removed compatibility input: %#v", args)
		}
	}
}

func TestGUIObservationRejectsSettleBeforeRuntime(t *testing.T) {
	result := newService().dispatch("gui", map[string]any{
		"actions": []any{map[string]any{
			"type": "screenshot",
		}},
		"settle_ms": 100,
	})
	if !result.IsError {
		t.Fatalf("screenshot + settle_ms unexpectedly succeeded: %#v", result)
	}
	text := resultText(result)
	if !strings.Contains(text, "screenshot observation") ||
		!strings.Contains(text, "does not accept settle_ms") {
		t.Fatalf("wrong preflight error: %q", text)
	}
}

func TestGUIValidationErrorsListEveryRejectedAction(t *testing.T) {
	text := guiValidationErrorText(ProviderDesktopResult{
		Error: "gui batch failed static pre-validation",
		ValidationErrors: []map[string]any{
			{"index": float64(0), "type": "scroll", "error": "unsupported field delta_y"},
			{"index": float64(1), "type": "wait", "error": "unsupported field seconds"},
		},
	})
	for _, expected := range []string{
		`actions[0] type="scroll": unsupported field delta_y`,
		`actions[1] type="wait": unsupported field seconds`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("validation summary missing %q: %q", expected, text)
		}
	}
}

func TestRemovedCLICompatibilityCommandsStayRemoved(t *testing.T) {
	for _, command := range []string{"list-apps", "snapshot"} {
		var output bytes.Buffer
		err := runCLI([]string{command}, &output)
		if err == nil || !strings.Contains(err.Error(), "unknown command") {
			t.Fatalf("removed CLI command %q was accepted: err=%v output=%q",
				command, err, output.String())
		}
	}
}

func TestGUIValidationRejectsBeforeFreshScreenshot(t *testing.T) {
	field, ok := reflect.TypeOf(ProviderDesktopRequest{}).FieldByName("Actions")
	if !ok || field.Type != reflect.TypeOf([]ProviderDesktopAction{}) {
		t.Fatal("ProviderDesktopRequest must carry typed GUI actions")
	}
	if !strings.Contains(linuxRuntimeScript, "no screenshot was captured") {
		t.Fatal("runtime preflight must avoid fresh screenshots for invalid batches")
	}
}

func TestProviderSpecificSecondaryActionIsNotReachable(t *testing.T) {
	for _, source := range []string{linuxRuntimeScript, mainGoSource(t)} {
		for _, forbidden := range []string{
			`invoke_element_action`,
			`invoke_secondary_action`,
			`CONTEXT_MENU_ACTIONS`,
		} {
			if strings.Contains(source, forbidden) {
				t.Fatalf("provider-specific secondary action leaked into production: %q", forbidden)
			}
		}
	}
}

func TestActionsReResolveGeometryAtActionTime(t *testing.T) {
	// Playwright 的 locator 每次动作重新解析。我们其实早就在 find_element 里
	// 解析出了活节点，却仍拿 Go 缓存的旧记录去算坐标——同一次调用里两套事实。
	// 实测：VS Code 里由外部收起侧栏后，click(element_index=21) 合成在 (572,38)，
	// 正是缓存框 {547,28,50,19} 的中心，而界面那时已经重排。
	if !strings.Contains(linuxRuntimeScript, "def current_geometry(") {
		t.Fatal("动作路径必须在解析出活节点之后重新取一次几何")
	}
	if !strings.Contains(linuxRuntimeScript, "element_record, moved_note = current_geometry(") {
		t.Fatal("重算的几何必须真的被后续动作使用，而不是算完丢掉")
	}
	// 位移发生了就必须说——这是 agent 判断"我读到的坐标还能不能用"的唯一依据
	if !strings.Contains(linuxRuntimeScript, "The element MOVED since the snapshot") {
		t.Fatal("元素移动过要如实告知，否则 agent 会继续用快照里的旧坐标")
	}
	// All element-targeted paths share resolveActionElement. A uniquely named
	// current index is promoted to a same-window semantic target so the runtime
	// uses live geometry. Ambiguous or unnamed records retain strict cached-path
	// resolution.
	source := mainGoSource(t)
	for _, tool := range []string{
		`"click"`,
		`"scroll"`,
		`"set_value"`,
	} {
		if !strings.Contains(source, "resolveActionElement(") ||
			!strings.Contains(source, tool) {
			t.Fatalf("element-targeted action %s does not use the shared resolver", tool)
		}
	}
	if !strings.Contains(source, "func applyActionElementResolution(") ||
		!strings.Contains(source, "resolution.element != nil && resolution.snapshot != nil") ||
		!strings.Contains(source, "target.SourceIndex = &sourceIndex") {
		t.Fatal("target resolution must distinguish promoted semantic indices from strict path indices")
	}
}

func TestCurrentNumericIndexPromotesOnlyWhenSemanticIdentityIsUnique(t *testing.T) {
	service := newService()
	unique := &appSnapshot{
		App:          appDescriptor{Name: "Editor", PID: 7},
		WindowTitle:  "Question",
		WindowHandle: "pid7/win1/dialog",
		sourceApp:    "editor",
		Elements: []elementRecord{
			{Index: 5, RuntimeID: []int{1, 2}, ControlType: "push button", Name: "OK"},
		},
	}
	service.rememberSnapshot("editor", unique)
	resolution, result := service.resolveActionElement("editor", "5", "", "click")
	if result.IsError {
		t.Fatalf("unique current index was rejected: %#v", result)
	}
	if resolution.semantic == nil || resolution.element != nil {
		t.Fatalf("unique index was not promoted: %#v", resolution)
	}
	if resolution.semantic.WindowHandle != unique.WindowHandle ||
		resolution.semantic.SourceIndex == nil ||
		*resolution.semantic.SourceIndex != 5 {
		t.Fatalf("promotion lost window/index provenance: %#v", resolution.semantic)
	}

	ambiguous := &appSnapshot{
		App:          appDescriptor{Name: "Editor", PID: 7},
		WindowTitle:  "Question",
		WindowHandle: "pid7/win1/dialog",
		sourceApp:    "editor",
		Elements: []elementRecord{
			{Index: 5, RuntimeID: []int{1, 2}, ControlType: "push button", Name: "OK"},
			{Index: 6, RuntimeID: []int{1, 3}, ControlType: "push button", Name: "OK"},
		},
	}
	service.rememberSnapshot("editor", ambiguous)
	resolution, result = service.resolveActionElement("editor", "5", "", "click")
	if result.IsError {
		t.Fatalf("ambiguous current index should retain strict path resolution: %#v", result)
	}
	if resolution.element == nil || resolution.semantic != nil {
		t.Fatalf("ambiguous identity must not be promoted: %#v", resolution)
	}
}

func TestStructuredSelectorBindsToCurrentSnapshotWindow(t *testing.T) {
	service := newService()
	snapshot := &appSnapshot{
		App:          appDescriptor{Name: "Editor", PID: 7},
		WindowTitle:  "Insert Table",
		WindowHandle: "pid7/win2/dialog",
		sourceApp:    "editor",
	}
	service.rememberSnapshot("editor", snapshot)
	resolution, result := service.resolveActionElement(
		"editor",
		`push button "OK"`,
		"",
		"click",
	)
	if result.IsError || resolution == nil || resolution.semantic == nil {
		t.Fatalf("structured selector was rejected: resolution=%#v result=%#v", resolution, result)
	}
	if resolution.semantic.WindowHandle != snapshot.WindowHandle {
		t.Fatalf(
			"structured selector was not bound to current window: got %q want %q",
			resolution.semantic.WindowHandle,
			snapshot.WindowHandle,
		)
	}
}

func mainGoSource(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读不到 Go source directory: %v", err)
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
			t.Fatalf("读不到 %s: %v", name, readErr)
		}
		source.Write(data)
		source.WriteByte('\n')
	}
	return source.String()
}

func TestStabilityWaitPortsOnlyWhatLinuxCanSupport(t *testing.T) {
	// Playwright 动作前等三件事：visible、enabled、stable（外加命中测试）。
	// 这里**只**照搬 stable，另外两条不能照搬，理由都是本仓库自己的实测：
	//   enabled —— Nautilus 的文件图标根本不设 ENABLED / SENSITIVE，状态里
	//              只有 SHOWING/VISIBLE/FOCUSABLE，却带着 open / menu 两个动作、
	//              完全可点。拿它当门禁会让 agent 跳过可用目标。
	//   命中测试 —— 实测命中率 gedit 11/11、Nautilus 19/25、LibreOffice 12/25，
	//              当门禁会拦掉一半 LibreOffice 的正常点击。
	if !strings.Contains(linuxRuntimeScript, "def wait_until_stable(") {
		t.Fatal("动作前要等元素停止移动")
	}
	for _, fragment := range []string{
		"只做 stable，不做 enabled",
		"target_unstable",
	} {
		if !strings.Contains(linuxRuntimeScript, fragment) {
			t.Fatalf("缺少 %q：既要实现，也要写清为什么只port这一条", fragment)
		}
	}
	// 坐标 fallback 必须在不稳定时拒绝；语义动作不依赖几何，不走该门禁。
	if !strings.Contains(linuxRuntimeScript, "require_stable_coordinate_geometry") {
		t.Fatal("坐标动作缺少稳定几何门禁")
	}
	// 上限要可调：动画重的应用需要更久，追求低延迟的场景要能关掉
	if !strings.Contains(linuxRuntimeScript, "COMPUTER_USE_STABLE_TIMEOUT_MS") {
		t.Fatal("稳定等待的上限要可配置")
	}
}

func TestNoInputRecoverablePreconditionDetectionIsNarrow(t *testing.T) {
	stale := textResult(
		"stale_element_ref reason=path_changed_or_element_disappeared "+
			"element_index=6 role='push button' name='OK'; no input was emitted.",
		true,
	)
	stale.StructuredContent = map[string]any{
		"failure_code":           "stale_element_ref",
		"failure_phase":          "precondition",
		"input_events_emitted":   false,
		"partial_input_possible": false,
		"mutation_unknown":       false,
		"failure_facts_complete": true,
	}
	if !isNoInputRecoverablePrecondition(stale) {
		t.Fatal("the explicit precondition no-input stale-ref failure must require observation")
	}
	incomplete := stale
	incomplete.StructuredContent = map[string]any{}
	for key, value := range stale.StructuredContent {
		incomplete.StructuredContent[key] = value
	}
	incomplete.StructuredContent["failure_code"] = "observation_incomplete"
	incomplete.StructuredContent["failure_facts_complete"] = false
	if !isNoInputRecoverablePrecondition(incomplete) {
		t.Fatal("explicit no-mutation facts should allow one recovery observation")
	}
	for _, result := range []toolCallResult{
		textResult("stale_element_ref reason=path_changed_or_element_disappeared; no input was emitted", true),
		{IsError: true, StructuredContent: map[string]any{
			"failure_code":         "stale_element_ref",
			"input_events_emitted": false,
		}},
		{IsError: true, StructuredContent: map[string]any{
			"failure_code":           "stale_element_ref",
			"failure_phase":          "precondition",
			"input_events_emitted":   true,
			"partial_input_possible": false,
			"mutation_unknown":       false,
		}},
		{IsError: true, StructuredContent: map[string]any{
			"failure_code":           "stale_element_ref",
			"failure_phase":          "precondition",
			"input_events_emitted":   false,
			"partial_input_possible": true,
		}},
		{IsError: true, StructuredContent: map[string]any{
			"failure_code":         "stale_element_ref",
			"failure_phase":        "precondition",
			"input_events_emitted": false,
			"mutation_unknown":     true,
		}},
		{IsError: true, StructuredContent: map[string]any{
			"failure_code":           "stale_element_ref",
			"failure_phase":          "execution",
			"input_events_emitted":   false,
			"partial_input_possible": false,
			"mutation_unknown":       false,
		}},
		{IsError: false, StructuredContent: map[string]any{
			"failure_code":           "stale_element_ref",
			"failure_phase":          "precondition",
			"input_events_emitted":   false,
			"partial_input_possible": false,
			"mutation_unknown":       false,
		}},
		{IsError: true, StructuredContent: map[string]any{
			"failure_code":         "observation_incomplete",
			"failure_phase":        "precondition",
			"input_events_emitted": false,
			"mutation_unknown":     false,
		}},
		{IsError: true, StructuredContent: map[string]any{
			"failure_code":           "semantic_target_not_found",
			"failure_phase":          "precondition",
			"input_events_emitted":   false,
			"partial_input_possible": false,
			"mutation_unknown":       false,
			"failure_facts_complete": true,
		}},
	} {
		if isNoInputRecoverablePrecondition(result) {
			t.Fatalf("observation-only recovery must require a precondition failure with definitive no-input evidence: %#v", result)
		}
	}
}

func TestNoInputStaleFailureReturnsIndependentCurrentObservation(t *testing.T) {
	current := beta36WindowTransitionSnapshot(
		"w2:"+strings.Repeat("b", 32),
		"Current Document",
		true,
	)
	backend := &recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{
			OK:       true,
			Snapshot: current,
		},
	}
	service := newServiceWithBackend(backend)
	before := beta36WindowTransitionSnapshot(
		"w2:"+strings.Repeat("a", 32),
		"Old Dialog",
		true,
	)
	service.rememberSnapshot("editor", before)
	service.rememberDeliveredSnapshot(before)
	failed := textResult(
		"stale_element_ref: target disappeared; no input was emitted",
		true,
	)
	failed.StructuredContent = map[string]any{
		"failure_code":           "stale_element_ref",
		"failure_phase":          "precondition",
		"input_events_emitted":   false,
		"partial_input_possible": false,
		"mutation_unknown":       false,
		"failure_facts_complete": true,
	}

	result := service.recoverNoInputPreconditionObservation(
		"editor",
		before,
		axServiceRequest{Observe: observeDiff},
		failed,
	)
	if !result.IsError ||
		result.StructuredContent["failure_code"] != "stale_element_ref" ||
		result.StructuredContent["action_outcome"] != "not_executed" ||
		result.StructuredContent["failed_action_input_emitted"] != false ||
		result.StructuredContent["failed_action_caused_ui_change"] != false ||
		result.StructuredContent["current_ui_returned"] != true ||
		result.StructuredContent["current_state_cause"] != "independent" ||
		result.StructuredContent["recovery_read_attempted"] != true ||
		result.StructuredContent["final_observation_returned"] != true ||
		result.StructuredContent["observation_required"] != false {
		t.Fatalf("no-input recovery overstated or omitted evidence: %#v", result)
	}
	if len(backend.observeRequests) != 1 ||
		backend.observeRequests[0].WindowHandle != "" {
		t.Fatalf("recovery must make one app-root read: %#v", backend.observeRequests)
	}
	text := resultText(result)
	for _, fragment := range []string{
		"The requested action was not executed",
		"independent current-state observation",
		"Current Document",
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("recovered response is missing %q: %s", fragment, text)
		}
	}
	if service.hasPendingFailedObservation("editor") {
		t.Fatal("successful inline recovery must not leave an explicit-observe gate")
	}
}

func TestNoInputRecoveryRespectsObserveNone(t *testing.T) {
	backend := &recordingPlatformBackend{}
	service := newServiceWithBackend(backend)
	before := beta36WindowTransitionSnapshot(
		"w2:"+strings.Repeat("a", 32),
		"Old Dialog",
		true,
	)
	failed := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":           "stale_element_ref",
			"failure_phase":          "precondition",
			"input_events_emitted":   false,
			"partial_input_possible": false,
			"mutation_unknown":       false,
			"failure_facts_complete": true,
		},
	}
	result := service.recoverNoInputPreconditionObservation(
		"editor",
		before,
		axServiceRequest{Observe: observeNone},
		failed,
	)
	if result.StructuredContent["observation_required"] != true ||
		result.StructuredContent["recovery_read_attempted"] != false ||
		len(backend.observeRequests) != 0 {
		t.Fatalf("observe=none unexpectedly performed recovery: %#v", result)
	}
}

func TestNoInputRecoveryFailureKeepsExplicitObservationGate(t *testing.T) {
	backend := &recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{
			OK:    false,
			Error: "AX observation remained incomplete",
			Failure: &ProviderFailure{
				Code:          "observation_incomplete",
				Phase:         "precondition",
				FactsComplete: false,
			},
		},
	}
	service := newServiceWithBackend(backend)
	failed := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":           "stale_element_ref",
			"failure_phase":          "precondition",
			"input_events_emitted":   false,
			"partial_input_possible": false,
			"mutation_unknown":       false,
			"failure_facts_complete": true,
		},
	}
	result := service.recoverNoInputPreconditionObservation(
		"editor",
		nil,
		axServiceRequest{Observe: observeDiff},
		failed,
	)
	if result.StructuredContent["failure_code"] != "stale_element_ref" ||
		result.StructuredContent["observation_required"] != true ||
		result.StructuredContent["current_ui_returned"] != false ||
		result.StructuredContent["recovery_read_attempted"] != true ||
		result.StructuredContent["recovery_observation_returned"] != false ||
		result.StructuredContent["recovery_observation_failure_code"] !=
			"observation_incomplete" {
		t.Fatalf("failed recovery lost original failure or gate: %#v", result)
	}
}

func TestBatchErrorKeepsRecoveredNoInputObservation(t *testing.T) {
	source := mainGoSource(t)
	for _, fragment := range []string{
		"if !finalObservationReturned {",
		`result.StructuredContent["observation_required"] = true`,
		"A fresh independent current-state ",
		"observation follows. The failed action emitted no input ",
		"The failed action emitted no input",
	} {
		if !strings.Contains(source, fragment) {
			t.Fatalf("batch recovery integration is missing %q", fragment)
		}
	}
	result := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":                        "stale_element_ref",
			"final_observation_returned":          true,
			"observation_required":                false,
			"next_identical_observation_required": false,
			"action_outcome":                      "not_executed",
		},
	}
	if result.StructuredContent["final_observation_returned"] != true ||
		result.StructuredContent["observation_required"] != false {
		t.Fatalf("recovered action result fixture is invalid: %#v", result)
	}
}

func TestAXAppUnavailableDetectionUsesStructuredFailureCode(t *testing.T) {
	result := textResult("app is not visible yet", true)
	result.StructuredContent = map[string]any{
		"failure_code":         "ax_app_unavailable",
		"failure_phase":        "precondition",
		"input_events_emitted": false,
	}
	if !isAXAppUnavailableResult(result) {
		t.Fatal("assert_ax must recognize the structured temporary app-unavailable result")
	}
	result.StructuredContent["failure_code"] = "semantic_target_not_found"
	if isAXAppUnavailableResult(result) {
		t.Fatal("only ax_app_unavailable may enter the availability polling path")
	}
}

func TestStaleFailureRequiresExplicitObservationWithoutFreshState(t *testing.T) {
	service := newService()
	before := &appSnapshot{
		App:             appDescriptor{Name: "Editor", PID: 7},
		WindowTitle:     "Question",
		WindowHandle:    "pid7/win0/frame",
		publicWindowRef: "w1",
		SnapshotID:      "visible-a",
		sourceApp:       "editor",
		projection:      snapshotProjection{View: treeViewWorking},
	}
	service.rememberSnapshot("editor", before)
	service.rememberDeliveredSnapshot(before)
	failed := textResult(
		"stale_element_ref reason=path_changed_or_element_disappeared; no input was emitted.",
		true,
	)
	failed.StructuredContent = map[string]any{
		"failure_code":           "stale_element_ref",
		"failure_phase":          "precondition",
		"input_events_emitted":   false,
		"partial_input_possible": false,
		"mutation_unknown":       false,
		"failure_facts_complete": true,
	}
	result := service.staleObservationRequired("editor", before, failed).
		withEvidenceScope("delivery_or_local_ui")
	text := resultText(result)
	for _, fragment := range []string{
		"STALE ELEMENT REFERENCE RECOVERY REQUIRED",
		"action_outcome=not_executed",
		"input_events_emitted=false",
		"observation_required=true",
		"This action did not run and did not cause any UI change",
		"No fresh UI observation was performed after this failure",
		"Call AX mode=observe or mode=find",
		"Do not describe a later disappearance or completed state as possible success",
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("stale failure is missing %q:\n%s", fragment, text)
		}
	}
	for _, forbidden := range []string{
		"Fresh window:",
		"fresh_state_inlined",
		"identity-compatible candidate",
		"fresh_snapshot_id",
		"fresh_window_handle",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("stale failure leaked fresh UI state %q:\n%s", forbidden, text)
		}
	}
	if len(service.snapshots) != 0 {
		t.Fatalf("stale failure must invalidate the action cache: %#v", service.snapshots)
	}
	if len(service.observations.lastComplete) == 0 {
		t.Fatal("stale failure must preserve the last complete continuity baseline")
	}
	for key, want := range map[string]any{
		"action_outcome":                 "not_executed",
		"failed_action_input_emitted":    false,
		"failed_action_caused_ui_change": false,
		"observation_required":           true,
		"current_ui_returned":            false,
		"recovery_read_attempted":        false,
		"evidence_scope":                 "error_only",
	} {
		if got := result.StructuredContent[key]; got != want {
			t.Fatalf("%s=%#v, want %#v; all=%#v", key, got, want,
				result.StructuredContent)
		}
	}
}

func TestExplicitObservationSeparatesLaterStateFromFailedAction(t *testing.T) {
	service := newService()
	before := &appSnapshot{
		App:       appDescriptor{Name: "Editor", PID: 7},
		sourceApp: "editor",
	}
	service.rememberFailedObservation("editor", before)
	current := &appSnapshot{App: appDescriptor{Name: "Editor", PID: 7}}
	note := service.consumeFailedObservation("Editor", current)
	for _, fragment := range []string{
		"explicit observation follows a stale-reference action",
		"emitted no input",
		"did not cause any UI change",
		"independent later observation",
		"Do not describe any difference",
		"call find and use the returned rN ref",
	} {
		if !strings.Contains(note, fragment) {
			t.Fatalf("explicit recovery observation is missing %q: %q", fragment, note)
		}
	}
	if repeated := service.consumeFailedObservation("editor", current); repeated != "" {
		t.Fatalf("failed-action continuity note must be consumed once, got %q", repeated)
	}
	result := withFailedActionContinuity(textResult("fresh state", false), true)
	for key, want := range map[string]any{
		"follows_no_input_stale_failure":     true,
		"failed_action_caused_current_state": false,
		"current_state_cause":                "independent",
	} {
		if got := result.StructuredContent[key]; got != want {
			t.Fatalf("%s=%#v, want %#v; all=%#v", key, got, want,
				result.StructuredContent)
		}
	}
}

func TestFailedObservationLedgerConsumesOneIdentityAsAUnit(t *testing.T) {
	service := newService()
	writer := &appSnapshot{
		App:          appDescriptor{Name: "soffice", PID: 7},
		WindowHandle: "pid7/writer/frame",
		sourceApp:    "writer",
	}
	calc := &appSnapshot{
		App:          appDescriptor{Name: "soffice", PID: 7},
		WindowHandle: "pid7/calc/frame",
		sourceApp:    "calc",
	}
	service.rememberFailedObservation("writer", writer)
	service.rememberFailedObservation("calc", calc)

	if note := service.consumeFailedObservation("calc", calc); note == "" {
		t.Fatal("the matching Calc observation must consume its own ledger entry")
	}
	if len(service.pendingFailedObservations) != 1 {
		t.Fatalf("consumption must remove one complete entry, got %#v",
			service.pendingFailedObservations)
	}
	if service.pendingFailedObservations[0].query != "writer" {
		t.Fatalf("the Calc observation consumed the Writer entry: %#v",
			service.pendingFailedObservations)
	}
	if note := service.consumeFailedObservation("writer", writer); note == "" {
		t.Fatal("the independent Writer entry must remain pending")
	}
	if len(service.pendingFailedObservations) != 0 {
		t.Fatalf("all matched entries should now be consumed: %#v",
			service.pendingFailedObservations)
	}
}

func TestFailedObservationRequiresNamedQueryAcrossRootChange(t *testing.T) {
	service := newService()
	before := &appSnapshot{
		App:          appDescriptor{Name: "Google Chrome", PID: 7},
		WindowHandle: "pid7/win0/frame",
		sourceApp:    "Google Chrome",
	}
	service.rememberFailedObservation("Google Chrome", before)
	if !failedObservationMatches(
		service.pendingFailedObservations[0],
		"google",
		nil,
	) {
		t.Fatal("pending stale identity must honor the same loose app alias contract")
	}

	after := &appSnapshot{
		App:          appDescriptor{Name: "Google Chrome", PID: 7},
		WindowHandle: "pid7/win1/dialog",
	}
	if note := service.consumeFailedObservation("", after); note != "" {
		t.Fatal("an unnamed foreground observation must not guess across root windows")
	}
	if note := service.consumeFailedObservation("google", after); note == "" {
		t.Fatal("the original loose app query must consume the entry across a root change")
	}
}

func TestPendingStaleGateBlocksNumericVerify(t *testing.T) {
	service := newService()
	before := &appSnapshot{
		App:          appDescriptor{Name: "Editor", PID: 7},
		WindowHandle: "pid7/editor/frame",
		sourceApp:    "editor",
	}
	service.rememberFailedObservation("editor", before)

	for _, hiddenTool := range []string{"click", "get_app_state", "verify"} {
		result := service.callTool(hiddenTool, map[string]any{"app": "editor"})
		if !result.IsError ||
			!strings.Contains(resultText(result), "unsupportedTool") {
			t.Fatalf("hidden tool %q must remain unavailable: %#v", hiddenTool, result)
		}
	}

	exists := true
	goal := verifyGoal{exists: &exists}
	blocked := service.verify(
		"edit", "17", "17", goal, 10, 0, a11yScopeWindow)
	if !blocked.IsError ||
		!strings.Contains(resultText(blocked), "cannot reuse target.index") {
		t.Fatalf("the stale app's loose alias must block its old numeric index: %#v", blocked)
	}

	noCache := newService().verify(
		"editor", "17", "17", goal, 10, 0, a11yScopeWindow)
	if !noCache.IsError ||
		!strings.Contains(resultText(noCache), "without a current accessibility snapshot") {
		t.Fatalf("numeric verify must always be bound to a current app snapshot: %#v", noCache)
	}
}

func TestActionFailureRequiresExplicitObservationBeforeAnotherAction(t *testing.T) {
	source := mainGoSource(t)
	for _, fragment := range []string{
		"return s.recoverNoInputPreconditionObservation(",
		"s.pendingFailedObservations = append(",
		"s.hasPendingFailedObservation(app)",
		`result.StructuredContent["observation_required"] == true`,
		"The requested action was not executed",
	} {
		if !strings.Contains(source, fragment) {
			t.Fatalf("explicit stale recovery wiring is missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"inlineStaleRecovery",
		"fresh_state_inlined",
		"fresh_snapshot_id",
		"fresh_window_handle",
		"recoveryCandidateLines",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("stale failure still exposes implicit fresh state through %q", forbidden)
		}
	}
	if got := strings.Count(source, "s.consumeFailedObservation(app, snapshot)"); got != 2 {
		t.Fatalf("only explicit get_app_state and find should consume stale continuity, got %d call sites", got)
	}
}

func TestRuntimeStderrIsOperatorOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operator.jsonl")
	t.Setenv("COMPUTER_USE_TRACE_FILE", path)
	request := newProviderWireRequest("req-secret", ProviderWireObserve)
	diagnostic := "Traceback (most recent call last):\n  File \"/secret/runtime.py\""
	writeLinuxRuntimeDiagnostic(request, "stderr", diagnostic)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(data), &record); err != nil {
		t.Fatalf("operator trace must contain valid JSONL: %v: %q", err, data)
	}
	if record["diagnostic"] != diagnostic ||
		record["kind"] != "linux_provider_diagnostic" {
		t.Fatalf("operator trace must retain the full diagnostic: %#v", record)
	}

	sourceBytes, err := os.ReadFile("linux_runtime_executor.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(sourceBytes)
	for _, forbidden := range []string{
		"response.Accessibility.Notes = append",
		"response.Desktop.Notes = append",
		"response.Window.Notes = append",
		"Linux runtime failed: %s",
		"invalid V2 JSON: %w: %s",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("runtime stderr can still cross the model boundary through %q", forbidden)
		}
	}
}

func TestErrorContextIsOnByDefaultAndDisableable(t *testing.T) {
	// 和 --output-mode file 相反的默认值，理由也相反：那个改变的是成功路径上
	// 每次调用的形态，这个只在失败时触发——而失败时没有任何别的手段能事后复盘，
	// 响应里只剩一行错误，现场早就没了。
	t.Setenv("COMPUTER_USE_ERROR_CONTEXT", "")
	if !errorContextEnabled() {
		t.Fatal("失败现场默认要落盘")
	}
	t.Setenv("COMPUTER_USE_ERROR_CONTEXT", "off")
	if errorContextEnabled() {
		t.Fatal("显式关掉时必须真的关掉")
	}
	t.Setenv("COMPUTER_USE_ERROR_CONTEXT", "OFF")
	if errorContextEnabled() {
		t.Fatal("开关取值不该区分大小写")
	}
}

func TestErrorContextNeedsAnAppAndNeverMasksTheOriginalError(t *testing.T) {
	service := newServiceWithBackend(&recordingPlatformBackend{
		err: errors.New("snapshot unavailable"),
	})
	t.Setenv("COMPUTER_USE_ERROR_CONTEXT", "")
	// 没有 app 就没有现场可抓。这条同时防住 list_apps 这种无 app 的工具
	// 在失败时去做一次毫无意义的快照。
	if note, sampled := service.writeErrorContext("", "click", "boom"); note != "" || sampled {
		t.Fatalf("没有 app 时不该采样或落盘，得到 note=%q sampled=%v", note, sampled)
	}
	// 抓现场自己失败时返回空串，绝不制造第二层错误——一个诊断设施
	// 在诊断失败时又抛一个错，只会把原始错误埋掉。
	if note, sampled := service.writeErrorContext("no-such-app-xyzzy", "click", "boom"); note != "" || sampled {
		t.Fatalf("抓不到现场时要静默放弃，得到 note=%q sampled=%v", note, sampled)
	}
}

func TestTraceIsOffUnlessGivenAPath(t *testing.T) {
	// 打开的方式是给出路径，不是布尔开关：写到哪里是调用方的事，
	// 一个诊断设施不该自作主张地选目录。
	t.Setenv("COMPUTER_USE_TRACE_FILE", "")
	if tracePath() != "" {
		t.Fatal("没给路径就不该开 trace")
	}
	service := newService()
	// 关着的时候写任何东西都不该有副作用
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
	// 失败也要留一行：只记成功会让复盘时看到一串顺利的动作，
	// 而真正要查的那一步凭空消失。
	service.appendTrace("vlc", axServiceRequest{
		Action: axSetValueCommand{Value: "x"},
	}, before, nil, nil, true)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("trace 文件没写出来: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("两次动作应当有两行，得到 %d", len(lines))
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("第一行不是合法 JSON: %v", err)
	}
	beforeMap, _ := first["before"].(map[string]any)
	afterMap, _ := first["after"].(map[string]any)
	if beforeMap["window"] != "VLC media player" || afterMap["window"] != "Simple Preferences" {
		t.Fatalf("前后状态要能读出这次动作把界面带到了哪里，得到 %v -> %v", beforeMap, afterMap)
	}
	if beforeMap["elements"].(float64) != 92 || afterMap["elements"].(float64) != 61 {
		t.Fatal("元素数量是判断树是否重排的最便宜的信号，必须记")
	}
	args, _ := first["args"].(map[string]any)
	if args["element_name"] != "Preferences" {
		t.Fatalf("动作参数要记下作用在谁身上，得到 %v", args)
	}
	// 但**不能**把整份记录抄进去：一次基线 16 个动作，抄整棵树会让 trace
	// 比 transcript 还大。要细节有 error-context 那条路。
	if strings.Contains(lines[0], "runtimeId") || strings.Contains(lines[0], "treeLines") {
		t.Fatal("trace 只记摘要，不记整棵树")
	}

	var second map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("第二行不是合法 JSON: %v", err)
	}
	if second["failed"] != true {
		t.Fatal("失败的动作要标出来")
	}
}

func TestUnnamedWindowsStillGetAUsableLabel(t *testing.T) {
	// 实测（跑 OSWorld 第 3 题时从 trace 里看出来的）：agent 在 Chrome 的菜单
	// 之间穿行时，有 4 个动作的窗口标题是空的——`Window: ""`。菜单、下拉、popup
	// 很多都不设名字，而窗口标题是 agent 判断"我现在在哪儿"的第一信号。
	if !strings.Contains(linuxRuntimeScript, "def window_label(") {
		t.Fatal("无名窗口要有可用的替代标题")
	}
	if !strings.Contains(linuxRuntimeScript, "(unnamed {} containing {!r})") {
		t.Fatal("退而求其次要带上第一个有名字的子节点——无名 menu 下面第一项常常" +
			"就足以认出这是什么菜单")
	}
}

func TestComboBoxSelectionIsVisibleInTheTree(t *testing.T) {
	// 实测（OSWorld 第 4 题）：Chrome 打印对话框里 Margins / Destination / Scale
	// 三个 combo box 在树里**只有名字、没有值**，而任务恰恰要求"把边距设成 None"。
	// 看不见当前值，就既没法判断要不要改，也没法确认改成功了。
	//
	// 值不在 Value 接口上（Chrome 的 combo box 不实现它），也不在 Text 接口上
	// （实测读回来是一个对象替换符 `￼`）。它在后代 menu item 的 SELECTED 状态上，
	// 而且**下拉关着时也读得到**——这一点是实测确认的，否则就得先展开每个下拉框。
	if !strings.Contains(linuxRuntimeScript, "def selected_option_name(") {
		t.Fatal("下拉框的当前选中项要能读出来")
	}
	if !strings.Contains(linuxRuntimeScript, `role in ("combo box", "list box")`) {
		t.Fatal("这次扫描只该对下拉类角色做，别的角色不付这个代价")
	}
}

func TestFocusGuardHasAnX11FallbackForWindowsThatNeverGoActive(t *testing.T) {
	// GNOME 门户文件对话框（每一次另存为/打开都是它）状态位是 MODAL+VISIBLE，
	// **既没有 ACTIVE 也没有 SHOWING**；它的二级确认框（"文件已存在，是否替换"）
	// 更是**在 a11y 树里根本不存在**，却握着输入焦点。
	// 只认 ACTIVE 的焦点守卫会让整条 GUI 通道在这类对话框上完全不可用。
	if !strings.Contains(linuxRuntimeScript, "def x11_holds_focus(") {
		t.Fatal("AT-SPI 说不出话时要有一层 X11 旁证")
	}
	// 判据必须是**同进程**：那个二级对话框没有标题，也不是目标窗口本身。
	if !strings.Contains(linuxRuntimeScript, "_NET_WM_PID") {
		t.Fatal("旁证要比 pid，不能比标题——那个挡路的对话框没有标题")
	}
	// 但它只是兜底，不能顶替 AT-SPI 的判断
	shim := strings.Index(linuxRuntimeScript, "def x11_holds_focus(")
	active := strings.Index(linuxRuntimeScript, "if window_is_active(window):")
	if active < 0 || shim < 0 {
		t.Fatal("AT-SPI 的 ACTIVE 判断要保留在前面")
	}
}

func TestForeignForegroundAppIsCalledOutByName(t *testing.T) {
	// 实测：在 Chrome 里点 Save，弹出的保存对话框属于 xdg-desktop-portal-gnome，
	// 和 Chrome 是两个进程。get_app_state(chrome) 永远看不到它，
	// agent 会以为"点了 Save 什么都没发生"。
	if !strings.Contains(linuxRuntimeScript, "ANOTHER APP IS IN FRONT") {
		t.Fatal("前台窗口属于别的进程时要指名道姓地说出来")
	}
	if !strings.Contains(linuxRuntimeScript, "Call AX mode=observe with app=") {
		t.Fatal("光说『不是你』没用，要直接给出该问哪个 app")
	}
}

func TestSetValueFallsBackToSynthesisWhenTheApiRefuses(t *testing.T) {
	// 实测（OSWorld 第 8 题，改 Chrome 配置文件用户名）：
	// chrome://settings/manageProfile 里的 `entry "Name"` 明明 holds "Person 1"，
	// set_value 却报"不可设置"——Blink 的输入框不实现 AT-SPI 的 Value/EditableText。
	//
	// click 和 type_text 早就有"语义失败就降级到合成"，set_value 却只会硬失败，
	// 于是一个到处都是的控件类型整个不可用。
	if !strings.Contains(linuxRuntimeScript, "The AT-SPI value API refused this element") {
		t.Fatal("语义写值被拒时要降级到 聚焦→全选→打字，而不是硬失败")
	}
	// 降级必须说清它没有回读确认——语义路径有，合成路径没有，
	// 两条路给同一句保证就是骗人。
	if !strings.Contains(linuxRuntimeScript, "no read-back ") {
		t.Fatal("降级路径要声明自己没有回读确认")
	}
}

func TestFocusGuardMustNotGiveFalsePositives(t *testing.T) {
	// 实测（OSWorld 第 14 题，Chrome 加载解压扩展）：GNOME 门户对话框开着，
	// 焦点却漂回了发起它的 Chrome。我往对话框输路径，字符**全部打进了 Chrome**，
	// 而守卫放行了。
	//
	// 守卫的假阳性比误拒危险得多：误拒会让 agent 看到一条报错、换条路；
	// 假阳性让输入静默落到别的窗口，**没有任何迹象**——正是本仓库最恨的那类失败。
	if !strings.Contains(linuxRuntimeScript, `["wmctrl", "-a", title]`) {
		t.Fatal("抢焦点失败时要向窗口管理器再请求一次激活")
	}
	// 但请求成功 ≠ 焦点到位。必须重新确认，否则只是把假阳性换了个位置。
	after := linuxRuntimeScript[strings.Index(linuxRuntimeScript, `["wmctrl", "-a", title]`):]
	if !strings.Contains(after[:600], "window_is_active(window) or x11_holds_focus(window)") {
		t.Fatal("激活请求之后必须重新确认焦点，不能直接返回成功")
	}
}

func TestX11ArbitratesWhichWindowIsInFrontWhenAtspiCannotTell(t *testing.T) {
	// 这条修的是一个**根因**，不是症状。
	//
	// GNOME 门户的文件对话框和它的二级确认框（"文件已存在，是否替换？"）是同一个
	// 应用下的两个窗口，状态位**完全相同**：MODAL + VISIBLE，都没有 ACTIVE，
	// 也都没有 SHOWING。按顺序取第一个，就永远拿到文件选择器，
	// 而真正挡在前面、握着焦点的是那个无名确认框。
	//
	// 我一度断定"那个确认框根本不在 a11y 树里"——**结论是错的**。它在树里，
	// 2418 个节点，只是我们选错了窗口。
	if !strings.Contains(linuxRuntimeScript, "def x11_preferred_window(") {
		t.Fatal("AT-SPI 分不出高下时要让 X11 裁决")
	}
	// 判据必须是尺寸：位置不行（GTK4 把 a11y extents 报成 (0,0) 起点），
	// 标题不行（那个确认框没有标题）。尺寸行，减掉 _GTK_FRAME_EXTENTS 之后
	// 与 a11y 逐像素相等——实测 827x291 - (61,61,55,67) = 705x169。
	if !strings.Contains(linuxRuntimeScript, "_GTK_FRAME_EXTENTS") {
		t.Fatal("尺寸对比必须先减掉 GTK 的不可见阴影边框")
	}
	// 候选集合**不能**依赖 SHOWING。第一版就是因为只在 `MODAL and SHOWING`
	// 的列表上裁决，而门户窗口没有 SHOWING，于是一次都没跑到。
	if !strings.Contains(linuxRuntimeScript, "x11_preferred_window(windows)") {
		t.Fatal("裁决要在全部窗口上做，不能只在 modal+showing 的子集上做")
	}
	// 命中不唯一时要让原有顺序接管——裁决判据在自己含糊时不该硬裁。
	if !strings.Contains(linuxRuntimeScript, "return matched[0] if len(matched) == 1 else None") {
		t.Fatal("命中不唯一时必须放弃裁决")
	}
}

func TestAppNameMatchingIsSeparatorInsensitiveAndFailsWithCandidates(t *testing.T) {
	// 这两条都是从**真实 agent 的轨迹**里读出来的，不是我想出来的：
	//
	//   13 条轨迹里有一次直接报了 appNotFound("google-chrome")——而这台机器上
	//   那个应用叫 "Google Chrome"。agent 猜的名字完全合理，只是连字符对不上。
	//
	//   更贵的是连带影响：13 条轨迹里 **12 条**的开局都是
	//   `list_apps → get_app_state`。agent 不敢直接猜名字，于是每道题都先花
	//   一步去列应用。
	if !strings.Contains(linuxRuntimeScript, "def loose(") {
		t.Fatal("应用名比对要对分隔符不敏感：google-chrome 应当找到 Google Chrome")
	}
	// 失败时要给候选。一条死路错误和一条带候选的错误，对 agent 是两件事：
	// 前者要再来一轮，后者这一轮就知道该叫什么。
	if !strings.Contains(linuxRuntimeScript,
		"Applications currently visible to the ") {
		t.Fatal("appNotFound 要列出当前在跑的应用")
	}
	// 空串不能匹配一切——归一化之后 "" 会 in 任何字符串。
	if !strings.Contains(linuxRuntimeScript, "if tight_query and tight_name and (") {
		t.Fatal("归一化后的空串必须先被挡掉，否则空查询会匹配所有应用")
	}
}

func TestAppArgumentHelpDefinesStrictBootstrapIdentity(t *testing.T) {
	// 13 条真实轨迹里 **12 条**的开局是 `list_apps → get_app_state`——agent 不敢
	// 直接猜应用名。根因是这台机器上的 a11y 应用名同时存在三种风格，
	// 且**没有任何规则能推出来**（实测）：
	//     显示名   "Google Chrome"       进程叫 chrome
	//     二进制名 "vlc" "gedit" "code"  与进程同名
	//     app-id   "org.gnome.Nautilus"  进程叫 nautilus
	// 规则推不出来，就直接告诉调用方。
	for _, fragment := range []string{
		"app/process name", "app id", "PID",
	} {
		if !strings.Contains(appArgumentHelp, fragment) {
			t.Fatalf("app 参数说明里缺少 %q", fragment)
		}
	}
	if strings.Contains(appArgumentHelp, "window-title") {
		t.Fatal("app bootstrap must not accept document or dialog window titles")
	}
	// 每一个接受 app 的工具都要用这份说明，不能只改一处
	tool := findToolDefinition(t, "AX")
	branches := tool.InputSchema["oneOf"].([]any)
	defs := tool.InputSchema["$defs"].(map[string]any)
	used := 0
	withApp := 0
	for _, raw := range branches {
		properties := raw.(map[string]any)["properties"].(map[string]any)
		if property, ok := properties["app"].(map[string]any); ok {
			withApp++
			if property["$ref"] == "#/$defs/app" {
				property = defs["app"].(map[string]any)
			}
			if property["description"] == appArgumentHelp {
				used++
			}
		}
	}
	if used != withApp || withApp != 3 {
		t.Fatalf("AX 的 %d/%d 个含 app 的 mode 分支使用统一说明", used, withApp)
	}
}

func TestSnapshotCacheLookupIsAsLooseAsAppResolution(t *testing.T) {
	// 这个回归是我自己引入的：get_app_state 允许不传 app 之后，快照按解析出的
	// 真名（"Google Chrome"）缓存，而 agent 下一步动作写的是 "chrome"——
	// 于是它刚刚成功取过状态，紧接着被告知
	// "No app state is available for chrome. Run get_app_state before action tools."
	//
	// 对 agent 来说这是最迷惑的一类回答：它照做了，工具却说它没做。
	// 应用名解析已经是宽松的，缓存查找必须一样宽松，否则两套规则之间会漏出
	// 这种没人猜得到的缝。
	service := newService()
	service.rememberSnapshot("", &appSnapshot{App: appDescriptor{Name: "Google Chrome"}})
	for _, spelling := range []string{"chrome", "Chrome", "google-chrome", "google chrome"} {
		if service.currentSnapshot(spelling) == nil {
			t.Fatalf("用 %q 应当能查到刚缓存的 Google Chrome 快照", spelling)
		}
	}
	// 但**不能松到匹配别的应用**——那会让动作打到错误的目标上。
	if service.currentSnapshot("thunderbird") != nil {
		t.Fatal("宽松匹配不能把无关的应用名也匹上")
	}
}

func TestSnapshotCacheLooseLookupRequiresOneUniqueAddressSpace(t *testing.T) {
	service := newService()
	first := &appSnapshot{App: appDescriptor{Name: "Code"}}
	second := &appSnapshot{App: appDescriptor{Name: "Code Helper"}}
	service.snapshots["visual studio code"] = first
	service.snapshots["code helper"] = second
	if got := service.currentSnapshot("code"); got != nil {
		t.Fatalf("ambiguous loose app alias must fail closed, got %#v", got)
	}
}

func TestShortRefAcceptsTheSamePublicAppAliasThatProducedIt(t *testing.T) {
	service := newService()
	snapshot := &appSnapshot{
		App: appDescriptor{Name: "soffice"},
		axSnapshot: &AXSnapshotV1{
			AppIdentity: "soffice",
		},
	}
	service.rememberSnapshot("libreoffice", snapshot)
	ref := service.refRegistry.Acquire(
		"soffice",
		"window-1",
		`s2:window-bound-target`,
		"button",
		"Save",
		"revision-1",
	)
	expanded, err := service.expandShortAXRef("libreoffice", ref)
	if err != nil {
		t.Fatalf("same public app alias rejected its own ref: %v", err)
	}
	if expanded != `s2:window-bound-target` {
		t.Fatalf("expanded ref=%q", expanded)
	}
}

func TestRememberSnapshotRemovesAliasesToTheReplacedAddressSpace(t *testing.T) {
	for _, query := range []string{"chrome", ""} {
		service := newService()
		old := &appSnapshot{App: appDescriptor{Name: "Google Chrome", PID: 41}}
		service.snapshots["chrome"] = old
		service.snapshots["google chrome"] = old
		service.snapshots["old window title"] = old
		service.snapshots["41"] = old

		fresh := &appSnapshot{App: appDescriptor{Name: "Google Chrome", PID: 41}}
		service.rememberSnapshot(query, fresh)
		if got := service.currentSnapshot("old window title"); got != nil {
			t.Fatalf("query %q left an alias to the replaced snapshot: %#v", query, got)
		}
		for _, alias := range []string{"chrome", "google chrome", "41"} {
			if got := service.currentSnapshot(alias); got != fresh {
				t.Fatalf("query %q fresh alias %q resolved to %#v, want fresh snapshot",
					query, alias, got)
			}
		}
	}
}

func TestOversizedResponsesAreCappedInlineNotSpilled(t *testing.T) {
	// 轨迹证据（26 条真实轨迹，60 次观测类调用）：**8 次（13%）**的响应达到
	// 114–119KB，被 MCP 客户端整块换成
	//     <persisted-output> Output too large (118.1KB). Full output saved to: …
	// 而 OSWorld 跑测里 Read 是禁用的，于是那 8 次 agent **什么都没拿到**。
	//
	// --output-mode file 救不了这一条：那同样是一个读不了的指针。
	// 唯一有效的办法是让内容本来就装得下。
	t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "")
	small := strings.Repeat("line\n", 100)
	if capInline(small) != small {
		t.Fatal("小输出不该被动")
	}
	big := strings.Repeat("a fairly long tree line that costs bytes\n", 4000)
	capped := capInline(big)
	if len(capped) >= len(big) {
		t.Fatal("超限的输出必须被裁")
	}
	if len(capped) > maxInlineChars()+400 {
		t.Fatalf("裁完还是太长：%d", len(capped))
	}
	// **必须说清裁掉了多少**。静默截断会让 agent 以为自己看到了整棵树，
	// 那比丢失更糟。
	for _, fragment := range []string{"TRUNCATED", "of", "lines", "`find`"} {
		if !strings.Contains(capped, fragment) {
			t.Fatalf("截断说明里缺少 %q", fragment)
		}
	}
	// 阈值要可调，因为不同客户端的上限不同
	t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "500")
	if maxInlineChars() != 500 {
		t.Fatal("上限要可以由环境变量覆盖")
	}
}

func TestTreeBudgetIsMeasuredInCharactersNotOnlyNodes(t *testing.T) {
	// 根因链（全部实测）：
	//   1. MAX_ELEMENTS=1200 是**节点数**预算，而 MCP 客户端限制的是**字节**；
	//   2. chrome://flags 正好吃满 1200 节点、平均每行 112 字符 = 135327 字符；
	//   3. 客户端把超过约 100KB 的结果整块换成一个文件指针，而跑测里 Read 是
	//      禁用的——26 条真实轨迹里 **13%** 的观测调用因此 agent 什么都没拿到。
	// 节点数管不住字节：1200 个短节点 40KB，1200 个长节点 135KB。
	if !strings.Contains(linuxRuntimeScript, "MAX_RENDERED_CHARS") {
		t.Fatal("树预算要有以字符计的那一条")
	}
	// 必须**在渲染时**就停手，不能渲染完再截——那样开销白付
	// （GIMP 实测 8.45ms/节点，白付 800 个就是 6.7 秒）。
	if !strings.Contains(linuxRuntimeScript, `if budget["chars"] >= max_rendered_chars:`) {
		t.Fatal("字符预算要在遍历里生效，不能事后截断")
	}
	// 省略原因要分开说：笼统一句会让调用方以为提高 max_tree_nodes 就能看全，
	// 而字符预算耗尽时那样做只会让整份响应被客户端丢掉。
	if !strings.Contains(linuxRuntimeScript, "character budget was exhausted") {
		t.Fatal("字符预算耗尽要单独说明，不能和节点预算混为一谈")
	}
	// 处方必须**跟着原因走**。旧版对每一种省略都说 "raise max_tree_nodes to
	// see them"，而剪枝掉的那些是有意过滤的——调大参数一个都不会回来。
	// 实测这句提示出现 3912 次、覆盖全部 11 个 domain，而 agent 几乎从不调参：
	// 它既不知道该调多大，也无从判断调了有没有用。
	//
	// 节点预算真的吃满时，建议调大是对的，但必须给出**具体数字**。
	if !strings.Contains(linuxRuntimeScript, "server-owned node budget filled up") {
		t.Fatal("节点预算耗尽时要说明这是服务端预算，而不是让调用方猜参数")
	}
	// 剪枝过滤那一档必须给当前 AX 恢复路径，不能继续广告已删除参数。
	if !strings.Contains(linuxRuntimeScript, "AX mode=find searches canonical records") {
		t.Fatal("被 working projection 隐藏的节点必须指向 find/root")
	}
	for _, removed := range []string{
		"raising max_tree_nodes will not",
		"Raising max_tree_nodes does NOT help here",
		"Pass prune=false",
	} {
		if strings.Contains(linuxRuntimeScript, removed) {
			t.Fatalf("模型可见诊断仍广告已删除接口: %q", removed)
		}
	}
}

func TestUniversalContextMenuActionIsNotAnAffordance(t *testing.T) {
	// 实测 chrome://flags：`showContextMenu` 出现在**全部 1200 个节点**上——
	// Chrome 给整棵 DOM 都挂了它，因为任何元素都能右键。
	// 不排掉它，is_structural_filler 的"没有动作"判据永远不成立，剪枝完全失效。
	if !strings.Contains(linuxRuntimeScript, "UNIVERSAL_ACTIONS") {
		t.Fatal("人人都有的动作要单独归类并排除")
	}
	if !strings.Contains(linuxRuntimeScript, `UNIVERSAL_ACTIONS = {"showcontextmenu"}`) {
		t.Fatal("showContextMenu 必须在这一类里")
	}
}

func TestVerifyFailureSaysWhatDidChange(t *testing.T) {
	// 轨迹证据（OSWorld 第 28 题，delta.com 查航班）：agent 把
	// verify(exists=false) 当"等页面跳走"用了**四次**，全部超时，
	// 白烧 15+20+25+20 = 80 秒预算。
	//
	// 它反复重试是因为我们只说了"你要的那件事没发生"，没说"这段时间里
	// 发生了什么"——于是它无法判断该继续等，还是那一次点击根本没生效。
	//
	// 三种处境要分开说，因为下一步完全不同：
	//   窗口换了     → 你等的元素可能已经不在这个窗口里
	//   树变了       → 界面在动，但不是你等的那样，该换判据
	//   什么都没变   → 只说明 A11y 没观察到结构变化，不归因上一步
	for _, fragment := range []string{
		"Window changed", "Tree changed", "Nothing changed",
		"no AX-visible structural ",
		"whether a prior input was emitted",
	} {
		if !strings.Contains(mainGoSource(t), fragment) {
			t.Fatalf("verify 失败时要说清界面变没变：缺 %q", fragment)
		}
	}
	if strings.Contains(mainGoSource(t), "action before this probably did not land") {
		t.Fatal("unchanged A11y state must not be promoted into an action-delivery claim")
	}
}

func TestVerifyAmbiguityDoesNotOverclaimStateFailure(t *testing.T) {
	// v96 ecc2413d used selector `label`, which matched 12 elements. verify never
	// identified a target, but the old timeout text claimed the requested state
	// was genuinely false and that the prior action probably did not land. A
	// fresh snapshot immediately showed the requested Page 1 of 8 label.
	src := mainGoSource(t)
	for _, fragment := range []string{
		"the selector remained ambiguous",
		"no unique assertion target was identified",
		"never identified one element to test",
		"This failure does not prove",
	} {
		if !strings.Contains(src, fragment) {
			t.Fatalf("ambiguous verify must fail without denying the UI state: missing %q", fragment)
		}
	}
}

func TestTreeWalkHasAWallClockDeadline(t *testing.T) {
	// **借鉴 maka-cu 的 macOS 侧**：它的四条硬预算里有一条是 deadline，
	// 我们此前只有节点数和深度。
	//
	// 节点预算管不住时间，因为每个节点的开销差一个数量级：本仓库实测 GIMP
	// 完整 record_for 是 8.45ms/节点，3162 次调用、整棵树 38 秒，
	// **超过 Go 层 30 秒的超时**——a11y 通道在 GIMP 上默认直接不可用。
	// maka-cu 记着同型的教训（跨进程存取面板 23.6ms/节点、1500 节点 35 秒、
	// 宿主只给 20 秒）。
	//
	// 我们靠"先便宜探针再建记录"降低了常态成本，但那是针对某一种开销分布的
	// 优化；deadline 是与分布无关的兜底。两者不冲突。
	if !strings.Contains(linuxRuntimeScript, "TREE_DEADLINE_SECONDS") {
		t.Fatal("遍历要有墙钟上限")
	}
	// 根节点必须豁免：空树没有任何可寻址元素，调用方连"这是什么窗口"都答不上来。
	if !strings.Contains(linuxRuntimeScript,
		"if deadline is not None and records and time.monotonic() >= deadline:") {
		t.Fatal("超时判据要豁免根节点，否则可能返回一棵空树")
	}
	// 三种省略原因必须分开说：建议提高 max_tree_nodes 在时间耗尽时是错的建议。
	if !strings.Contains(linuxRuntimeScript, "time budget exhausted") {
		t.Fatal("时间预算耗尽要单独说明")
	}
}

func TestVerifyRedirectsTheDisappearanceIdiom(t *testing.T) {
	// 轨迹证据（第 28 题 delta.com）：agent 连着四次
	// verify(exists=false, 15/20/25/20s) 当"等页面跳转"用，全部超时，
	// 白烧 80 秒预算，最后放弃。
	//
	// 判据本身选错了：单页应用点完之后旧节点常常**留在 DOM 里**，
	// 于是那个条件永远不会满足，等多久都一样。工具知道这一点，就该说出来，
	// 而不是让它按 15→20→25 秒一路加码。
	src := mainGoSource(t)
	for _, fragment := range []string{
		"is a poor proxy for ",
		"leave the old ",
		"Wait for something NEW instead",
	} {
		if !strings.Contains(src, fragment) {
			t.Fatalf("exists:false 超时要指出这是个坏代理：缺 %q", fragment)
		}
	}
	// 只在**界面确实没动**时才这么说。界面动了说明它等的方向没错，
	// 那种情况列候选更有用——给错建议比不给建议更浪费。
	if !strings.Contains(src, "snapshot.WindowTitle == firstWindow && len(snapshot.Elements) == firstCount") {
		t.Fatal("这条建议要限定在界面没动的情形")
	}
}

func TestIntentGuardSkipsElementsWithVeryShortNames(t *testing.T) {
	// 实测（OSWorld 第 34 题，Macy's 筛选尺码）：agent 声明 "Size L checkbox"，
	// 元素是 `check box "L"`——**完全正确的操作被守卫拒了**。
	//
	// 因为分词会跳过长度 < 2 的词元（为了滤掉 "a"/"of" 这类噪声），
	// 而这个元素的名字整个就是一个字符 "L"，于是任何声明都匹配不上，
	// 守卫**必然**误拒。尺码、单选字母、表格列标题都是这一类。
	//
	// 守卫的价值是拦住"声明 Save 却给了 New 的下标"，代价是偶尔误伤；
	// 这里不是偶尔，是这一类元素上 100% 误伤。宁可漏判，不可必然误判。
	short := &elementRecord{Name: "L", ControlType: "check box"}
	if got := elementIntentMismatch(short, "Size L checkbox"); got != "" {
		t.Fatalf("单字符名字判不了，不该拒：%s", got)
	}
	if got := elementIntentMismatch(short, "the Save button"); got != "" {
		t.Fatalf("单字符名字上一律不判，包括本该不符的声明：%s", got)
	}
	// 但有描述时仍然判得了，不能一并放行
	withDesc := &elementRecord{Name: "L", ControlType: "check box", Description: "Large size filter"}
	if elementIntentMismatch(withDesc, "the Save button") == "" {
		t.Fatal("有描述时仍应校验")
	}
	// 正常长度的名字，原有行为不变
	normal := &elementRecord{Name: "Save", ControlType: "push button"}
	if elementIntentMismatch(normal, "the New button") == "" {
		t.Fatal("正常名字上守卫必须照常拦截")
	}
}

func TestScrollNoteDoesNotClaimTargetingItDidNotDo(t *testing.T) {
	// scroll 的 element_index 变成可选之后，原来那句
	//     "the position came from element_index, so this scroll IS targeted"
	// 在留空时**是假的**。工具骗 agent 一次，它后面所有推断都建立在假前提上。
	//
	// 判据必须是**显式标志**，不能靠推断：Go 侧合成的落点记录 index 是 0
	// 而不是 null，第一版靠 `index is not None` 判断，结果照样说"定位过"。
	if !strings.Contains(mainGoSource(t), "WindowCentre") {
		t.Fatal("滚窗口中心要有显式标志，不能让运行时去猜")
	}
	if !strings.Contains(linuxRuntimeScript, `action_input.get("window_centre")`) {
		t.Fatal("Note 的措辞要按这个显式标志分支")
	}
	if !strings.Contains(linuxRuntimeScript, "no target ref was given, so this is the centre") {
		t.Fatal("留空时要如实说落点是窗口中心")
	}
}

// TestWithoutRepaintNoise 钉住 [text-attrs] 不再替**没发生的事**作证。
//
// 出处是第 44 题的真实轨迹：agent 在第 16 步只按了个 ctrl+l，回答开头却是
// `[text-attrs] Formatting changed … heading "Friday, August 2nd" —
//
//	family-name: Ubuntu -> All-Pro Sans; size: 18pt -> 22.5pt …
//	This is direct evidence the action took effect`。
//
// 真相是 nfl.com 的网页字体异步加载完了，把回退字体 Ubuntu 换成了 All-Pro Sans，
// 与那次按键毫无关系。工具替一件没发生的事作了证——这比不给证据更坏。
func TestWithoutRepaintNoise(t *testing.T) {
	t.Run("网页字体加载不算证据", func(t *testing.T) {
		// 真实取自轨迹：字体换掉的同时 size/weight 也跟着变，那是新字体的度量。
		got := withoutRepaintNoise([]string{
			"family-name: Ubuntu -> All-Pro Sans",
			"size: 18pt -> 22.5pt",
			"weight: unset -> 700",
		})
		if len(got) != 0 {
			t.Fatalf("字体替换应当被整条滤掉，却留下了 %v", got)
		}
	})

	t.Run("肉眼看不出的颜色变化不算证据", func(t *testing.T) {
		got := withoutRepaintNoise([]string{"bg-color: 244,248,248 -> 246,249,249"})
		if len(got) != 0 {
			t.Fatalf("2/255 的色差应当被滤掉，却留下了 %v", got)
		}
	})

	t.Run("真的加粗照样报得出来", func(t *testing.T) {
		// 这条 note 的价值全在这里：LibreOffice 里加粗只改格式、不改结构，
		// 树看上去纹丝不动。滤噪声不能把它一起滤掉。
		got := withoutRepaintNoise([]string{"weight: 400 -> 700"})
		if len(got) != 1 {
			t.Fatalf("真实的加粗应当保留，却得到 %v", got)
		}
	})

	t.Run("明显的颜色变化保留", func(t *testing.T) {
		got := withoutRepaintNoise([]string{"fg-color: 0,0,0 -> 255,0,0"})
		if len(got) != 1 {
			t.Fatalf("黑变红应当保留，却得到 %v", got)
		}
	})

	t.Run("同一节点里字体没换时 size 变化保留", func(t *testing.T) {
		// fontSwap 是**按节点**判定的：没有字体替换的证据时，
		// size 变化仍然可能是谁调了字号。
		got := withoutRepaintNoise([]string{"size: 12pt -> 24pt"})
		if len(got) != 1 {
			t.Fatalf("没有字体替换时 size 应当保留，却得到 %v", got)
		}
	})
}

func TestIncrementalTreeDoesNotInflateSingleHugeLine(t *testing.T) {
	base := func(lines ...string) *appSnapshot { return &appSnapshot{TreeLines: lines} }
	body := strings.Repeat("log line abcdefghij ", 400)
	before := base("0 frame \"Terminal\"", "1 terminal \"\": \""+body+"tail\"")
	after := base("0 frame \"Terminal\"", "1 terminal \"\": \""+body+"TAIL-CHANGED\"")

	diff, ok := incrementalTree(before, after)
	if !ok {
		t.Fatal("单个巨型节点内容变化时应当能给出增量，而不是退回全量重发几千字符")
	}
	total := 0
	for _, line := range diff {
		total += len(line)
	}
	fullSize := 0
	for _, line := range after.TreeLines {
		fullSize += len(line)
	}
	if total >= fullSize {
		t.Fatalf("增量 %d 字符不该比全量 %d 字符更大——那正是负压缩", total, fullSize)
	}
	joined := strings.Join(diff, "\n")
	if !strings.Contains(joined, "~1 terminal") {
		t.Fatalf("摘要必须保留行首的 element_index 与 role，得到：%q", firstLine(joined))
	}
	if !strings.Contains(joined, "[-tail][+TAIL-CHANGED]") {
		t.Fatalf("摘要必须把变化写成 [-旧][+新]，得到：%v", diff)
	}
	if strings.Count(joined, body) != 0 {
		t.Fatal("未变化的那几千字符正文一个字都不该重发")
	}
}

// TestIncrementalTreeKeepsLineDiffForShortLines 保证短行仍走原来的 -/+ 形式。
func TestIncrementalTreeKeepsLineDiffForShortLines(t *testing.T) {
	base := func(lines ...string) *appSnapshot { return &appSnapshot{TreeLines: lines} }
	diff, ok := incrementalTree(base("a", "b", "c", "d", "e", "f"),
		base("a", "b", "X", "d", "e", "f"))
	if !ok {
		t.Fatal("短行的单行替换仍应走增量")
	}
	if len(diff) != 3 || diff[1] != "-c" || diff[2] != "+X" {
		t.Fatalf("短行不该被摘要化，应保持 -旧 +新，得到 %v", diff)
	}
}

func TestIncrementalTreeRejectsNearFullDynamicPageDiff(t *testing.T) {
	before := &appSnapshot{TreeLines: make([]string, 0, 120)}
	after := &appSnapshot{TreeLines: make([]string, 0, 120)}
	for index := 0; index < 120; index++ {
		before.TreeLines = append(
			before.TreeLines,
			fmt.Sprintf("%d list item \"Old vehicle %03d\"", index, index),
		)
		after.TreeLines = append(
			after.TreeLines,
			fmt.Sprintf("%d list item \"New vehicle %03d\"", index, index),
		)
	}
	if _, ok := incrementalTree(before, after); ok {
		t.Fatal("a near-full dynamic-page diff must use compact action context")
	}
}

// ---------------------------------------------------------------------------
// expect：act → settle → verify（P0）
// ---------------------------------------------------------------------------

func expectSnapshot(elements ...elementRecord) *appSnapshot {
	return &appSnapshot{
		App:       appDescriptor{Name: "gedit", PID: 7},
		TreeLines: []string{"0 frame Doc"},
		Elements:  elements,
	}
}

func TestExpectTargetSyntaxIsToolkitIndependent(t *testing.T) {
	dialog := elementRecord{Index: 12, ControlType: "dialog", Name: "Save As"}
	entry := elementRecord{Index: 17, ControlType: "entry", Name: "File name", Value: "notes.txt", States: "[focused enabled]"}
	snapshot := expectSnapshot(dialog, entry)

	cases := []struct {
		target string
		want   int // 期望命中的 element_index，-1 表示不该命中
	}{
		{"role=dialog", 12},
		{"element_index=17", 17},
		{"17", 17},
		{`role=entry,name=File`, 17},
		{`entry "File name"`, 17},
		{"state=focused", 17},
		{"text=notes.txt", 17},
		{"role=slider", -1},
		{`role=dialog,name=Open`, -1},
	}
	for _, testCase := range cases {
		target, err := parseExpectTarget(testCase.target)
		if err != nil {
			t.Fatalf("parseExpectTarget(%q) error = %v", testCase.target, err)
		}
		record := target.find(snapshot)
		if testCase.want < 0 {
			if record != nil {
				t.Fatalf("%q 不该命中，却命中了 %d", testCase.target, record.Index)
			}
			continue
		}
		if record == nil || record.Index != testCase.want {
			t.Fatalf("%q 应当命中 %d，实际 %#v", testCase.target, testCase.want, record)
		}
	}

	// 字段写错必须硬失败：悄悄忽略一个拼错的字段等于给出一个永远为真的验证。
	for _, bad := range []string{"", "rle=dialog", "element_index=abc", "  "} {
		if _, err := parseExpectTarget(bad); err == nil {
			t.Fatalf("parseExpectTarget(%q) 应当报错", bad)
		}
	}

	// 引号里的逗号属于名字，不是字段分隔符。
	target, err := parseExpectTarget(`name="Save, As"`)
	if err != nil {
		t.Fatalf("引号内的逗号不该被当成分隔符: %v", err)
	}
	if target.name != "Save, As" {
		t.Fatalf("name = %q, want %q", target.name, "Save, As")
	}
}

func TestExpectPredicatesReportWhatTheySaw(t *testing.T) {
	dialog := elementRecord{Index: 12, ControlType: "dialog", Name: "Save As"}
	entry := elementRecord{Index: 17, ControlType: "entry", Name: "File name", Value: "已保存 notes.txt", States: "[focused enabled]"}
	button := elementRecord{Index: 42, ControlType: "push button", Name: "Cancel"}
	checkbox := elementRecord{Index: 43, ControlType: "check box", Name: "Keep", States: "[checked enabled]"}
	row := elementRecord{Index: 44, ControlType: "list item", Name: "Result", States: "[selected enabled]"}
	snapshot := expectSnapshot(dialog, entry, button, checkbox, row)
	// 基线：动作前只有那个按钮，对话框与输入框都是动作之后才出现的。
	before := expectSnapshot(button)

	must := func(args map[string]any) *expectSpec {
		spec, err := parseExpect(map[string]any{"expect": args})
		if err != nil {
			t.Fatalf("parseExpect(%v) error = %v", args, err)
		}
		return spec
	}

	// appears：匹配数量增加才算出现。
	ok, evidence := must(map[string]any{"appears": "role=dialog"}).evaluate(before, snapshot)
	if !ok || !strings.Contains(evidence, "Save As") || !strings.Contains(evidence, "increased from 0") {
		t.Fatalf("appears 应当命中并报出匹配数量增加: ok=%v evidence=%q", ok, evidence)
	}
	ok, evidence = must(map[string]any{"appears": "role=alert"}).evaluate(before, snapshot)
	if ok || !strings.Contains(evidence, "did not increase") {
		t.Fatalf("appears 落空时应当说清没有匹配: ok=%v evidence=%q", ok, evidence)
	}

	// disappears：动作前有、动作后没有。
	ok, _ = must(map[string]any{"disappears": "role=dialog"}).evaluate(snapshot, before)
	if !ok {
		t.Fatal("动作前存在、动作后不存在应当算作已消失")
	}
	ok, evidence = must(map[string]any{"disappears": "element_index=42"}).evaluate(before, snapshot)
	if ok || !strings.Contains(evidence, "STILL present") {
		t.Fatalf("元素还在时应当报 still present: ok=%v evidence=%q", ok, evidence)
	}

	// exists_after：只断言动作后存在，不冒充新增，也不需要 baseline。
	ok, evidence = must(map[string]any{"exists_after": "role=dialog"}).evaluate(nil, snapshot)
	if !ok || !strings.Contains(evidence, "exists in the post-action tree") {
		t.Fatalf("exists_after 应当允许无 baseline 的存在性断言: ok=%v evidence=%q", ok, evidence)
	}

	// value_becomes
	ok, evidence = must(map[string]any{
		"value_becomes": map[string]any{"element_index": float64(17), "contains": "已保存"},
	}).evaluate(before, snapshot)
	if !ok || !strings.Contains(evidence, "已保存") {
		t.Fatalf("value_becomes 应当读到值: ok=%v evidence=%q", ok, evidence)
	}
	ok, evidence = must(map[string]any{
		"value_becomes": map[string]any{"element_index": "17", "contains": "未保存"},
	}).evaluate(before, snapshot)
	if ok || !strings.Contains(evidence, "does not contain") {
		t.Fatalf("值不符时应当报出实际值: ok=%v evidence=%q", ok, evidence)
	}
	emptyValue := &appSnapshot{Elements: []elementRecord{
		{Index: 18, ControlType: "entry", Name: "Search", Value: ""},
	}}
	ok, evidence = must(map[string]any{
		"value_becomes": map[string]any{"element_index": "18", "equals": ""},
	}).evaluate(nil, emptyValue)
	if !ok {
		t.Fatalf("equals empty string should match an empty accessibility value: %q", evidence)
	}
	// value_becomes 只读 accessibility value，不能拿 name/description/placeholder
	// 的文本冒充控件值。
	ok, evidence = must(map[string]any{
		"value_becomes": map[string]any{"element_index": "12", "contains": "Save As"},
	}).evaluate(before, snapshot)
	if ok || !strings.Contains(evidence, `has value ""`) {
		t.Fatalf("仅 name 命中不得满足 value_becomes: ok=%v evidence=%q", ok, evidence)
	}

	// state_is_after：按完整 state token 判断，支持常见状态和否定状态。
	for _, testCase := range []struct {
		index string
		state string
	}{
		{index: "17", state: "focused"},
		{index: "43", state: "checked"},
		{index: "44", state: "selected"},
	} {
		ok, evidence = must(map[string]any{
			"state_is_after": map[string]any{"element_index": testCase.index, "state": testCase.state},
		}).evaluate(before, snapshot)
		if !ok || !strings.Contains(evidence, "reports states") {
			t.Fatalf("state_is_after(%s) 应当命中: ok=%v evidence=%q", testCase.state, ok, evidence)
		}
	}
	ok, evidence = must(map[string]any{
		"state_is_after": map[string]any{"element_index": "17", "state": "selected"},
	}).evaluate(before, snapshot)
	if ok || !strings.Contains(evidence, `without "selected"`) {
		t.Fatalf("未报告的状态不得命中: ok=%v evidence=%q", ok, evidence)
	}

	transitionBefore := expectSnapshot(
		elementRecord{Index: 43, ControlType: "check box", Name: "Keep", States: "[enabled]"},
	)
	ok, evidence = must(map[string]any{
		"state_becomes": map[string]any{"element_index": "43", "state": "checked"},
	}).evaluate(transitionBefore, snapshot)
	if !ok || !strings.Contains(evidence, "reports states") {
		t.Fatalf("state_becomes 必须识别真实状态转换: ok=%v evidence=%q", ok, evidence)
	}
	alreadyChecked := expectSnapshot(
		elementRecord{Index: 43, ControlType: "check box", Name: "Keep", States: "[checked enabled]"},
	)
	ok, evidence = must(map[string]any{
		"state_becomes": map[string]any{"element_index": "43", "state": "checked"},
	}).evaluate(alreadyChecked, snapshot)
	if ok || !strings.Contains(evidence, "already satisfied") {
		t.Fatalf("动作前已经满足时不得把 postcondition 冒充 transition: ok=%v evidence=%q", ok, evidence)
	}

	ambiguousValues := expectSnapshot(
		elementRecord{Index: 50, ControlType: "entry", Name: "First", Value: "ready"},
		elementRecord{Index: 51, ControlType: "entry", Name: "Second", Value: "ready"},
	)
	ok, evidence = must(map[string]any{
		"value_becomes": map[string]any{"element_index": "role=entry", "equals": "ready"},
	}).evaluate(before, ambiguousValues)
	if ok || !strings.Contains(evidence, "2 elements match") ||
		!strings.Contains(evidence, "ambiguous") {
		t.Fatalf("value_becomes 不得静默选择第一个宽泛匹配: ok=%v evidence=%q", ok, evidence)
	}

	ambiguousStates := expectSnapshot(
		elementRecord{Index: 52, ControlType: "check box", Name: "First", States: "[checked]"},
		elementRecord{Index: 53, ControlType: "check box", Name: "Second", States: "[checked]"},
	)
	ok, evidence = must(map[string]any{
		"state_becomes": map[string]any{"element_index": "role=check box", "state": "checked"},
	}).evaluate(before, ambiguousStates)
	if ok || !strings.Contains(evidence, "2 elements match") ||
		!strings.Contains(evidence, "ambiguous") {
		t.Fatalf("state_becomes 不得静默选择第一个宽泛匹配: ok=%v evidence=%q", ok, evidence)
	}

	// focus_moves_to
	ok, _ = must(map[string]any{"focus_moves_to": "role=entry"}).evaluate(before, snapshot)
	if !ok {
		t.Fatal("焦点在 entry 上时 focus_moves_to 应当满足")
	}
	ok, evidence = must(map[string]any{"focus_moves_to": "role=push button"}).evaluate(before, snapshot)
	if ok || !strings.Contains(evidence, "focus is on index=17") {
		t.Fatalf("焦点不在目标上时要说清焦点在谁身上: ok=%v evidence=%q", ok, evidence)
	}

	// 多个谓词是"与"关系。
	spec := must(map[string]any{"appears": "role=dialog", "focus_moves_to": "role=push button"})
	if ok, _ := spec.evaluate(before, snapshot); ok {
		t.Fatal("多个谓词应当全部满足才算通过")
	}
}

func TestFocusWithinAcceptsFocusedDescendants(t *testing.T) {
	dialog := elementRecord{
		Index: 12, ControlType: "dialog", Name: "Save As", RuntimeID: []int{0, 4},
	}
	entry := elementRecord{
		Index: 17, ControlType: "entry", Name: "File name",
		RuntimeID: []int{0, 4, 2, 1}, States: "[focused enabled]",
	}
	outside := elementRecord{
		Index: 20, ControlType: "entry", Name: "Search",
		RuntimeID: []int{0, 7}, States: "[focused enabled]",
	}
	spec, err := parseExpect(map[string]any{
		"expect": map[string]any{"focus_within": "role=dialog"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ok, evidence := spec.evaluate(nil, expectSnapshot(dialog, entry))
	if !ok || !strings.Contains(evidence, "within index=12") {
		t.Fatalf("焦点落在目标后代时 focus_within 应满足: ok=%v evidence=%q", ok, evidence)
	}
	root := elementRecord{Index: 0, ControlType: "frame", Name: "Editor"}
	rootSpec, err := parseExpect(map[string]any{
		"expect": map[string]any{"focus_within": "role=frame"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ok, evidence = rootSpec.evaluate(nil, expectSnapshot(root, entry))
	if !ok {
		t.Fatalf("空 runtime path 表示根节点，必须包含有路径的焦点后代: %q", evidence)
	}
	ok, evidence = spec.evaluate(nil, expectSnapshot(dialog, outside))
	if ok || !strings.Contains(evidence, "outside") {
		t.Fatalf("焦点落在目标外时 focus_within 不应满足: ok=%v evidence=%q", ok, evidence)
	}
}

// TestAppearsIsAChangeAssertionNotAPresenceCheck 钉住这一版修掉的头号假阳性。
//
// 实测：62 次 verified=true 里 16–23% 是硬假阳性，最典型的一类是一直挂在那儿的
// 顶层窗口标题被判成"新出现"——一次应用内点击不可能让窗口标题新出现。成因是
// appears 只看动作之后那一份快照，判据是"存在"而不是"新增"。
// 假的确定性比 not verified 更危险：它让 agent 停止怀疑。
func TestAppearsIsAChangeAssertionNotAPresenceCheck(t *testing.T) {
	frame := elementRecord{Index: 0, ControlType: "frame", Name: "Files"}
	dialog := elementRecord{Index: 12, ControlType: "dialog", Name: "Save As"}

	must := func(args map[string]any) *expectSpec {
		spec, err := parseExpect(map[string]any{"expect": args})
		if err != nil {
			t.Fatalf("parseExpect(%v) error = %v", args, err)
		}
		return spec
	}

	// 动作前就在那儿的东西，动作后还在，**不算出现**。
	before := expectSnapshot(frame, dialog)
	after := expectSnapshot(frame, dialog)
	ok, evidence := must(map[string]any{"appears": "role=frame"}).evaluate(before, after)
	if ok {
		t.Fatalf("动作前已存在的元素不得判成 appears: evidence=%q", evidence)
	}
	if !strings.Contains(evidence, "did not increase") || !strings.Contains(evidence, "1 before, 1 after") {
		t.Fatalf("证据必须说明匹配数量没有增加: %q", evidence)
	}

	// 真的新增才算：动作前没有 dialog，动作后有。
	ok, evidence = must(map[string]any{"appears": "role=dialog"}).evaluate(
		expectSnapshot(frame), after)
	if !ok {
		t.Fatalf("真正的新增应当判 true: %q", evidence)
	}
	// 证据必须给出数量变化。element_index 只是现在的地址。
	if !strings.Contains(evidence, "increased from 0") {
		t.Fatalf("appears 的证据必须以匹配数量增加为据: %q", evidence)
	}
	if !strings.Contains(evidence, "only its current address") {
		t.Fatalf("证据不得让 element_index 冒充新增的证据: %q", evidence)
	}

	// 没有基线就没有"变化"可言。**不回落成存在性判定**：宁可 verified=false，
	// 也不给一个假的 true。
	ok, evidence = must(map[string]any{"appears": "role=dialog"}).evaluate(nil, after)
	if ok {
		t.Fatalf("没有动作前快照时不得判 true: %q", evidence)
	}
	if !strings.Contains(evidence, "no pre-action snapshot") || !strings.Contains(evidence, "AX mode=observe") {
		t.Fatalf("证据必须说清缺的是基线、以及怎么补上: %q", evidence)
	}

	// disappears 对称：动作前就没有的东西"消失"是空真命题，不得判 true。
	ok, evidence = must(map[string]any{"disappears": "role=alert"}).evaluate(before, after)
	if ok {
		t.Fatalf("从未出现过的目标不得判成 disappears: %q", evidence)
	}
	if !strings.Contains(evidence, "nothing to disappear") {
		t.Fatalf("空真命题必须被说破: %q", evidence)
	}
	// 动作前有、动作后没有，才算消失。
	ok, evidence = must(map[string]any{"disappears": "role=dialog"}).evaluate(
		before, expectSnapshot(frame))
	if !ok || !strings.Contains(evidence, "decreased from 1") {
		t.Fatalf("真正的消失应当判 true 并给出动作前的匹配: ok=%v evidence=%q", ok, evidence)
	}
}

func TestAppearsAndDisappearsUseRoleConstrainedMatchCounts(t *testing.T) {
	menuItem := elementRecord{Index: 3, ControlType: "menu item", Name: "Open"}
	dialogA := elementRecord{Index: 7, ControlType: "dialog", Name: "Open"}
	dialogB := elementRecord{Index: 9, ControlType: "dialog", Name: "Open"}

	target, err := parseExpectTarget("role=dialog,name=Open")
	if err != nil {
		t.Fatal(err)
	}
	appears := expectPredicate{kind: "appears", target: target}
	ok, evidence := appears.evaluate(
		expectSnapshot(menuItem, dialogA),
		expectSnapshot(menuItem, dialogA, dialogB),
	)
	if !ok || !strings.Contains(evidence, "increased from 1") {
		t.Fatalf("同名 menu item 不得串入 dialog 计数，且 1->2 应满足 appears: ok=%v evidence=%q", ok, evidence)
	}

	disappears := expectPredicate{kind: "disappears", target: target}
	ok, evidence = disappears.evaluate(
		expectSnapshot(menuItem, dialogA, dialogB),
		expectSnapshot(menuItem, dialogA),
	)
	if !ok || !strings.Contains(evidence, "decreased from 2") {
		t.Fatalf("同名 menu item 不得阻止 dialog 的 2->1 disappears: ok=%v evidence=%q", ok, evidence)
	}

	nameOnly, err := parseExpectTarget("name=Open")
	if err != nil {
		t.Fatal(err)
	}
	ok, evidence = (expectPredicate{kind: "appears", target: nameOnly}).evaluate(
		expectSnapshot(menuItem), expectSnapshot(menuItem, dialogA))
	if !ok || !strings.Contains(evidence, "dialog elements") ||
		!strings.Contains(evidence, "increased from 0") {
		t.Fatalf("未指定 role 时应按 role 分组识别新 dialog: ok=%v evidence=%q", ok, evidence)
	}
}

func TestChangePredicatesSeparateSameTextByRole(t *testing.T) {
	menu := elementRecord{Index: 1, ControlType: "menu item", Name: "Brightness-Contrast"}
	dialog := elementRecord{Index: 2, ControlType: "dialog", Name: "Brightness-Contrast"}
	target, err := parseExpectTarget("Brightness-Contrast")
	if err != nil {
		t.Fatal(err)
	}

	appears := expectPredicate{kind: "appears", target: target}
	ok, evidence := appears.evaluate(expectSnapshot(menu), expectSnapshot(menu, dialog))
	if !ok || !strings.Contains(evidence, "dialog elements") ||
		!strings.Contains(evidence, "increased from 0") {
		t.Fatalf("同名菜单项不得掩盖新 dialog: ok=%v evidence=%q", ok, evidence)
	}

	disappears := expectPredicate{kind: "disappears", target: target}
	ok, evidence = disappears.evaluate(expectSnapshot(menu, dialog), expectSnapshot(menu))
	if !ok || !strings.Contains(evidence, "dialog elements") ||
		!strings.Contains(evidence, "decreased from 1") {
		t.Fatalf("同名菜单项不得掩盖已关闭 dialog: ok=%v evidence=%q", ok, evidence)
	}
}

// TestBareExpectWordNeverMatchesARole 钉住第二类假阳性：
// `expect={"appears":"menu"}` 曾被 `push button "Back"` 命中，因为裸串的兜底
// 匹配把 ControlType 也算进去了。角色是一个封闭词表，必须用 role= 显式限定。
func TestBareExpectWordNeverMatchesARole(t *testing.T) {
	menu := elementRecord{Index: 3, ControlType: "menu", Name: "File"}
	entry := elementRecord{Index: 8, ControlType: "entry", Name: "Address and search bar"}
	snapshot := expectSnapshot(menu, entry)

	if _, err := parseExpectTarget("menu"); err == nil ||
		!strings.Contains(err.Error(), "Write role=menu") {
		t.Fatalf("裸角色必须 fail fast 并给出 role= 写法，实际 err=%v", err)
	}
	// 显式写角色才命中。
	explicit, err := parseExpectTarget("role=menu")
	if err != nil {
		t.Fatal(err)
	}
	if record := explicit.find(snapshot); record == nil || record.Index != 3 {
		t.Fatalf("role=menu 应当命中 3，实际 %#v", record)
	}

	if _, err := parseExpectTarget("dialog"); err == nil ||
		!strings.Contains(err.Error(), "Write role=dialog") {
		t.Fatalf("dialog 也必须显式写 role=，实际 err=%v", err)
	}

	// 裸串也不得横跨 role + name 两个字段拼起来匹配：单个字段整串包含才算。
	// 这正是 focus_moves_to 那四次全军覆没的成因——焦点摘要打印的是
	// `entry Address and search bar`，agent 原样贴回来，结构性必假。
	crossField, err := parseExpectTarget("entry Address and search bar")
	if err != nil {
		t.Fatal(err)
	}
	if record := crossField.find(snapshot); record != nil {
		t.Fatalf("裸串不得横跨 role 与 name 匹配，却命中了 %d", record.Index)
	}
	// 而带引号的规范写法（也正是现在打印出去的那种）必须命中。
	quotedForm, err := parseExpectTarget(`entry "Address and search bar"`)
	if err != nil {
		t.Fatal(err)
	}
	if record := quotedForm.find(snapshot); record == nil || record.Index != 8 {
		t.Fatalf(`entry "Address and search bar" 应当命中 8，实际 %#v`, record)
	}
}

// TestElementReferencesRoundTripThroughTheTargetParser 钉住渲染出口的统一：
// 凡是我们打印给 agent 的元素指称，agent 原样贴回 expect 都必须解析成同一个
// 元素。渲染器与解析器是同一份文法的两端，漂移一次就会让证据自相矛盾。
func TestElementReferencesRoundTripThroughTheTargetParser(t *testing.T) {
	records := []elementRecord{
		{Index: 4, ControlType: "push button", Name: "Save"},
		{Index: 5, ControlType: "entry", Name: "Address and search bar"},
		{Index: 6, ControlType: "panel", Name: "PageShape: Weekday in school"},
		{Index: 7, ControlType: "separator"}, // 无名元素
		{Index: 8, ControlType: "entry", Name: `say "hi"`},
	}
	snapshot := expectSnapshot(records...)
	for _, record := range records {
		for _, rendered := range []string{describeRecord(&record), renderElementAddress(&record)} {
			target, err := parseExpectTarget(rendered)
			if err != nil {
				t.Fatalf("渲染出去的 %q 回读失败: %v", rendered, err)
			}
			hit := target.find(snapshot)
			if hit == nil || hit.Index != record.Index {
				t.Fatalf("%q 回读后应当命中 element_index %d，实际 %#v",
					rendered, record.Index, hit)
			}
		}
	}

	// 焦点摘要走的也是这个渲染器，所以它同样回读得动。
	focused := elementRecord{Index: 5, ControlType: "entry",
		Name: "Address and search bar", States: "[focused enabled]"}
	live := expectSnapshot(focused)
	description := focusDescription(live)
	target, err := parseExpectTarget(description)
	if err != nil {
		t.Fatalf("焦点摘要 %q 回读失败: %v", description, err)
	}
	if hit := target.findFocused(live); hit == nil || hit.Index != 5 {
		t.Fatalf("焦点摘要回读后应当仍然命中那个焦点元素: %q -> %#v", description, hit)
	}
}

func TestQuotedExpectSelectorUsesExactRoleAndName(t *testing.T) {
	snapshot := expectSnapshot(
		elementRecord{Index: 1, ControlType: "push button", Name: "OK"},
		elementRecord{Index: 2, ControlType: "push button", Name: "Bookmark"},
		elementRecord{Index: 3, ControlType: "menu item", Name: "OK"},
	)
	target, err := parseExpectTarget(`push button "OK"`)
	if err != nil {
		t.Fatal(err)
	}
	matches := target.matching(snapshot)
	if len(matches) != 1 || matches[0].Index != 1 {
		t.Fatalf("quoted selector must not substring-match Bookmark or another role: %#v", matches)
	}

	substring, err := parseExpectTarget(`name=OK`)
	if err != nil {
		t.Fatal(err)
	}
	if got := substring.countMatches(snapshot); got != 3 {
		t.Fatalf("field query name= must retain documented substring semantics, got %d", got)
	}
}

// TestExpectEvidenceNeverContradictsItself 钉住第二个缺陷的核心不变量：
// 一条证据不得跨越两个观测源。
//
// 实测的原话是 `nothing in the tree matches "entry Address and search bar";
// focus is currently on entry Address and search bar` —— 前半句读的是剪枝后的
// Elements，后半句读的是运行时另一次独立的整窗扫描。两句话在同一条证据里
// 互相否定，agent 无从判断该信哪一句。389 个带树响应里 79 个（约 20%）处在
// "散文报告焦点在 X、树里没有任何节点带 focused"的状态。
func TestExpectEvidenceNeverContradictsItself(t *testing.T) {
	spec, err := parseExpect(map[string]any{
		"expect": map[string]any{"focus_moves_to": `entry "Address and search bar"`},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 焦点节点被剪枝掉了：树里既没有匹配项，也没有任何 focused 节点，
	// 而独立扫描却报得出焦点。这正是那句自相矛盾的证据的产地。
	pruned := expectSnapshot(elementRecord{Index: 1, ControlType: "frame", Name: "Browser"})
	pruned.FocusedSummary = `entry "Address and search bar"`
	ok, evidence := spec.evaluate(expectSnapshot(), pruned)
	if ok {
		t.Fatalf("焦点节点不在树里时不得判 true: %q", evidence)
	}
	if strings.Contains(evidence, "nothing in the tree matches") {
		t.Fatalf("不得输出会与下半句自相矛盾的'树里没有匹配项': %q", evidence)
	}
	// 必须把"目标不匹配"与"焦点节点未进入本次快照"分开说，
	// 并且点明那份摘要来自另一次扫描、不是判定所用的视图。
	for _, fragment := range []string{
		"cannot decide",
		"not part of the tree",
		"separate whole-window scan",
	} {
		if !strings.Contains(evidence, fragment) {
			t.Fatalf("证据缺少 %q，无法区分'不匹配'与'没观测到': %q", fragment, evidence)
		}
	}

	// 反过来：焦点节点确实在这份树里、只是不是目标，那就得说得死死的——
	// 焦点在谁身上，用的是同一份 Elements，不掺第二个观测源。
	elsewhere := expectSnapshot(
		elementRecord{Index: 2, ControlType: "push button", Name: "Reload", States: "[focused]"},
		elementRecord{Index: 3, ControlType: "entry", Name: "Address and search bar"},
	)
	elsewhere.FocusedSummary = `push button "Reload"`
	ok, evidence = spec.evaluate(expectSnapshot(), elsewhere)
	if ok {
		t.Fatalf("焦点不在目标上时不得判 true: %q", evidence)
	}
	if !strings.Contains(evidence, "focus is on index=2") {
		t.Fatalf("焦点证据必须取自判定所用的那份树: %q", evidence)
	}
	if strings.Contains(evidence, "cannot decide") {
		t.Fatalf("焦点节点就在树里时不该说'无法判断': %q", evidence)
	}
}

func TestExpectIsOptionalAndMisuseIsACallError(t *testing.T) {
	// 不传 expect：完全向后兼容，没有验证意图，settle 路径整个不启用。
	plan, err := parseSettlePlan(map[string]any{"app": "gedit"})
	if err != nil {
		t.Fatalf("不传 expect 不该报错: %v", err)
	}
	if plan.expect != nil {
		t.Fatal("不传 expect 时必须没有验证意图")
	}
	if plan.settleMS != defaultSettleMS {
		t.Fatalf("settle_ms 默认值 = %d, want %d", plan.settleMS, defaultSettleMS)
	}

	// 写错 expect 是**调用错误**，要硬失败——与"预期没达成"是两件事。
	for _, bad := range []map[string]any{
		{"expect": "role=dialog"},
		{"expect": map[string]any{}},
		{"expect": map[string]any{"appearz": "role=dialog"}},
		{"expect": map[string]any{"appears": 3.0}},
		{"expect": map[string]any{"value_becomes": map[string]any{"element_index": "17"}}},
		{"expect": map[string]any{"value_becomes": map[string]any{"contains": "x"}}},
		{"expect": map[string]any{"value_becomes": map[string]any{"element_index": "17", "contains": ""}}},
		{"expect": map[string]any{"value_becomes": map[string]any{"element_index": "17", "contains": "a", "equals": "b"}}},
		{"expect": map[string]any{"state_becomes": map[string]any{"element_index": "17"}}},
		{"expect": map[string]any{"state_becomes": map[string]any{"state": "checked"}}},
		{"expect": map[string]any{"appears": "role=dialog"}, "settle_ms": -1.0},
		{"expect": map[string]any{"appears": "role=dialog"}, "settle_ms": float64(maxSettleMS + 1)},
	} {
		if _, err := parseSettlePlan(bad); err == nil {
			t.Fatalf("parseSettlePlan(%v) 应当报错", bad)
		}
	}

	// settle_ms = 0 是合法的：只看动作刚结束那一份快照，不再等。
	plan, err = parseSettlePlan(map[string]any{
		"expect": map[string]any{"appears": "role=dialog"}, "settle_ms": 0.0,
	})
	if err != nil || plan.settleMS != 0 || plan.expect == nil {
		t.Fatalf("settle_ms=0 应当合法: plan=%#v err=%v", plan, err)
	}
	plan, err = parseSettlePlan(map[string]any{
		"expect":    map[string]any{"appears": "role=dialog"},
		"settle_ms": float64(maxSettleMS),
	})
	if err != nil || plan.settleMS != 3000 {
		t.Fatalf("settle_ms=3000 should be accepted: plan=%#v err=%v", plan, err)
	}
}

func TestProviderDesktopSettleBound(t *testing.T) {
	valid := ProviderDesktopRequest{
		ProtocolVersion: providerProtocolV2,
		Actions:         []ProviderDesktopAction{{Type: "screenshot"}},
		SettleMS:        guiMaxSettleMS,
	}
	if err := validateProviderDesktopRequest(valid); err != nil {
		t.Fatalf("provider settle_ms=%d should be accepted: %v", guiMaxSettleMS, err)
	}
	valid.SettleMS++
	if err := validateProviderDesktopRequest(valid); err == nil ||
		!strings.Contains(err.Error(), "between 0 and 3000") {
		t.Fatalf("provider settle_ms above limit error = %v", err)
	}
}

func TestExpectSettlesByPollingAndReportsElapsed(t *testing.T) {
	spec, err := parseExpect(map[string]any{"expect": map[string]any{"appears": "role=dialog"}})
	if err != nil {
		t.Fatal(err)
	}
	plan := settlePlan{expect: spec, settleMS: 0}

	// 动作刚结束就已经满足：一次额外遍历都不付，耗时如实报 0 上下。
	svc := newService()
	before := expectSnapshot(elementRecord{Index: 1, ControlType: "push button", Name: "Save"})
	ready := expectSnapshot(elementRecord{Index: 12, ControlType: "dialog", Name: "Save As"})
	outcome := svc.settle("gedit", plan, before, ready, nil, "")
	if !outcome.verified || outcome.polls != 0 {
		t.Fatalf("首轮命中不该再轮询: %#v", outcome)
	}
	if outcome.elapsedMS > 50 {
		t.Fatalf("首轮命中的耗时应当接近 0，实际 %dms", outcome.elapsedMS)
	}

	// 没满足且预算为 0：立刻收敛成 verified=false，不阻塞。
	outcome = svc.settle("gedit", plan, before, expectSnapshot(), nil, "")
	if outcome.verified || outcome.polls != 0 {
		t.Fatalf("预算为 0 时应当立刻给出否定结论: %#v", outcome)
	}

	// predicate_verified:false **不是** isError，且不得把 delivery 写死成成功。
	actionNotes := []string{
		"[synthesis] Delivery to the intended target was not verified: input was emitted.",
	}
	note := expectNote(plan, outcome, axServiceRequest{Action: axClickCommand{}}, actionNotes)
	if !strings.Contains(note, `"predicate_verified":false`) ||
		!strings.Contains(note, `"settled_after_ms"`) ||
		!strings.Contains(note, "isError is false") ||
		!strings.Contains(note, "delivery to the intended target is unknown") {
		t.Fatalf("否定结论的 Note 缺少必要事实: %q", note)
	}
	if strings.Contains(note, "performed and delivered") {
		t.Fatalf("动作 Note 明说 delivery 未验证时，expect 不得反向声称已送达: %q", note)
	}
	note = expectNote(
		plan,
		settleOutcome{
			snapshot:  &appSnapshot{SnapshotID: "settled-b"},
			verified:  true,
			evidence:  "dialog appeared",
			elapsedMS: 240,
		},
		axServiceRequest{Action: axClickCommand{}},
		actionNotes,
	)
	if !strings.Contains(note, `"predicate_verified":true`) ||
		!strings.Contains(note, "only the declared accessibility predicate") ||
		!strings.Contains(note, "240") {
		t.Fatalf("肯定结论的 Note 缺少必要事实: %q", note)
	}
	if strings.Contains(note, "settled-b") ||
		strings.Contains(note, "observed_snapshot_id") {
		t.Fatalf("肯定结论泄漏内部 snapshot identity: %q", note)
	}
	for _, overclaim := range []string{"effect is confirmed", "do not need a follow-up"} {
		if strings.Contains(strings.ToLower(note), overclaim) {
			t.Fatalf("predicate_verified:true 不得升级为效果确认: %q", note)
		}
	}
}

func TestFollowupReadFailureIsVerificationIncompleteNotTimeout(t *testing.T) {
	plan := settlePlan{expect: &expectSpec{}, settleMS: 5000}
	outcome := settleOutcome{
		snapshot:               &appSnapshot{SnapshotID: "immediate-a"},
		verificationIncomplete: true,
		followupReadFailed:     true,
		evidence:               "follow-up read failed",
		elapsedMS:              120,
		polls:                  1,
	}
	note := expectNote(plan, outcome, axServiceRequest{Action: axClickCommand{}}, nil)
	for _, fragment := range []string{
		`"verification_complete":false`,
		`"followup_read_failed":true`,
		"verification is incomplete",
		"not a full-budget timeout verdict",
	} {
		if !strings.Contains(note, fragment) {
			t.Fatalf("incomplete verification note missing %q: %q", fragment, note)
		}
	}
	if strings.Contains(note, "within the 5000ms settle budget") {
		t.Fatalf("follow-up read failure was misreported as a full timeout: %q", note)
	}

	result := withActionOutcome(
		textResult("last snapshot", false),
		plan,
		outcome,
		outcome.snapshot,
		nil,
	)
	if result.StructuredContent["verification_complete"] != false ||
		result.StructuredContent["followup_read_failed"] != true {
		t.Fatalf("structured incomplete-verification facts missing: %#v",
			result.StructuredContent)
	}
}

// TestObserveAndExpectAreOrthogonal 钉住合并后的关键不变量：
// observe 管"回多少观测"，expect 管"验什么结果"，两者互不干扰。
// 特别是 observe="none" + expect：树和图都不回，但验证结论必须照常返回。
func TestObserveAndExpectAreOrthogonal(t *testing.T) {
	args := map[string]any{
		"app":       "gedit",
		"observe":   "none",
		"expect":    map[string]any{"appears": "role=dialog"},
		"settle_ms": 0.0,
	}
	obs, err := parseObservation(args)
	if err != nil {
		t.Fatalf("expect 的存在不该影响 observe 的解析: %v", err)
	}
	if obs.resolvedMode() != observeNone {
		t.Fatalf("observe 应当仍是 none，得到 %q", obs.resolvedMode())
	}
	plan, err := parseSettlePlan(args)
	if err != nil {
		t.Fatalf("observe 的存在不该影响 expect 的解析: %v", err)
	}
	if plan.expect == nil {
		t.Fatal("observe=none 不该把验证意图一起省掉")
	}

	// observe="none" 的出口是 notesOnlyResult，验证结论走的是 notes，
	// 所以结论必须出现在返回里——它是结论，不是观测。
	svc := newService()
	outcome := svc.settle("gedit", plan, expectSnapshot(), expectSnapshot(), nil, "")
	if outcome.verified {
		t.Fatal("空快照上 appears 不该成立")
	}
	snapshot := expectSnapshot()
	result := snapshot.notesOnlyResult([]string{
		expectNote(plan, outcome, axServiceRequest{Action: axClickCommand{}}, nil),
	})
	if len(result.Content) != 1 || result.Content[0].Type != "text" {
		t.Fatalf("observe=none 不该回树或图: %#v", result.Content)
	}
	text := result.Content[0].Text
	if !strings.Contains(text, `"predicate_verified":false`) || !strings.Contains(text, "settled_after_ms") {
		t.Fatalf("observe=none 时验证结论仍必须返回: %q", text)
	}
	if result.IsError {
		t.Fatal("predicate_verified:false 不是 isError")
	}
	if strings.Contains(text, "element_index values stay valid") {
		t.Fatal("observe=none must not promise that removed or omitted old indices remain valid")
	}
	if !strings.Contains(text, "Existing rN refs remain usable") {
		t.Fatal("observe=none must describe the refreshed ref-lifecycle boundary")
	}
}

func TestGUIWaitDescriptionExplainsBarrierAndObservationBoundary(t *testing.T) {
	tool := findToolDefinition(t, "GUI")
	inputBranches := tool.InputSchema["oneOf"].([]any)
	actProps := inputBranches[1].(map[string]any)["properties"].(map[string]any)
	action := actProps["actions"].(map[string]any)["items"].(map[string]any)
	var waitDescription string
	for _, raw := range action["oneOf"].([]any) {
		fields := raw.(map[string]any)["properties"].(map[string]any)
		actionType := fields["type"].(map[string]any)
		if actionType["const"] == "wait" {
			waitDescription = actionType["description"].(string)
			if len(fields) != 1 {
				t.Fatalf("wait exposes non-native fields: %#v", fields)
			}
		}
	}
	for _, phrase := range []string{
		"exactly one second",
		"without an intermediate observation",
	} {
		if !strings.Contains(waitDescription, phrase) {
			t.Fatalf("gui wait description missing %q", phrase)
		}
	}
}

func beta14LocalSnapshot() *appSnapshot {
	return &appSnapshot{
		App:          appDescriptor{Name: "Editor", PID: 42},
		WindowTitle:  "Document",
		WindowHandle: "0x42",
		SnapshotID:   "beta14-local",
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

func TestPublicAXObserveUsesServerOwnedAcquisitionBudget(t *testing.T) {
	branch := findA11ySchemaBranch(t, "observe", "app")
	properties := branch["properties"].(map[string]any)
	for _, forbidden := range []string{
		"text_limit", "max_tree_nodes", "max_tree_depth", "prune", "force_full",
		"view",
	} {
		if _, present := properties[forbidden]; present {
			t.Fatalf("public AX observe still exposes %q", forbidden)
		}
	}
	if _, present := properties["root"]; !present {
		t.Fatal("public AX observe is missing the bounded root projection")
	}
	err := validateA11yModeFields(a11yModeObserve, map[string]any{
		"mode":           "observe",
		"text_limit":     100,
		"max_tree_nodes": 20,
		"max_tree_depth": 4,
		"prune":          false,
		"force_full":     true,
		"view":           "full",
	})
	if err == nil {
		t.Fatal("public AX observe accepted raw acquisition controls")
	}
	for _, field := range []string{
		"text_limit", "max_tree_nodes", "max_tree_depth", "prune", "force_full",
		"view",
	} {
		if !strings.Contains(err.Error(), strconv.Quote(field)) {
			t.Fatalf("validation did not report every illegal field: %v", err)
		}
	}
}

func TestFocusedAndTargetRootPreserveCanonicalAddressSpace(t *testing.T) {
	focused := beta14LocalSnapshot()
	if err := applyObservationRoot(
		focused,
		map[string]any{"focused": true},
	); err != nil {
		t.Fatal(err)
	}
	if focused.observationRoot == nil || focused.observationRoot.Index != 1 {
		t.Fatalf("focused root = %#v, want nearby panel 1", focused.observationRoot)
	}
	text := typedTreeTextForTest(t, focused)
	if !strings.Contains(text, `textbox "Name"`) ||
		!strings.Contains(text, `button "Save"`) ||
		strings.Contains(text, `status "Ready"`) {
		t.Fatalf("focused subtree has wrong local context:\n%s", text)
	}
	if len(focused.Elements) != 5 {
		t.Fatal("root projection changed canonical elements")
	}

	target := beta14LocalSnapshot()
	if err := applyObservationRoot(
		target,
		map[string]any{
			"selector": map[string]any{
				"role": "push button",
				"name": "Save",
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	if target.observationRoot == nil || target.observationRoot.Index != 3 {
		t.Fatalf("target root = %#v, want Save button", target.observationRoot)
	}
	targetText := typedTreeTextForTest(t, target)
	if !strings.Contains(targetText, `button "Save"`) ||
		strings.Contains(targetText, `textbox "Name"`) {
		t.Fatalf("target subtree is not exact:\n%s", targetText)
	}
}

func TestObservationRootAcceptsExpandedShortRefInternally(t *testing.T) {
	snapshot := beta14LocalSnapshot()
	internalRef := encodeScopedSemanticRef(
		`push button "Save"`,
		snapshot.WindowHandle,
	)
	if _, err := parseObservationRoot(map[string]any{"ref": internalRef}); err == nil {
		t.Fatal("public observation root accepted an internal semantic ref")
	}
	if err := applyObservationRoot(
		snapshot,
		map[string]any{"ref": internalRef},
	); err != nil {
		t.Fatalf("expanded short ref did not resolve internally: %v", err)
	}
	if snapshot.observationRoot == nil || snapshot.observationRoot.Index != 3 {
		t.Fatalf(
			"expanded short-ref root = %#v, want Save button index 3",
			snapshot.observationRoot,
		)
	}
}

func TestRootProjectionFailsClosed(t *testing.T) {
	snapshot := beta14LocalSnapshot()
	err := applyObservationRoot(
		snapshot,
		map[string]any{
			"selector": map[string]any{
				"role": "push button",
				"name": "Missing",
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "no element matches") {
		t.Fatalf("missing root did not fail closed: %v", err)
	}
}

func TestWorkingProjectionSummarizesHomogeneousCollections(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{"\t0 list \"Results\""},
		Elements: []elementRecord{{
			Index: 0, RuntimeID: []int{0}, ControlType: "list", Name: "Results",
		}},
	}
	for index := 1; index <= 12; index++ {
		states := "[has-click-action]"
		if index == 8 {
			states = "[selected has-click-action]"
		}
		snapshot.TreeLines = append(
			snapshot.TreeLines,
			fmt.Sprintf("\t\t%d list item \"Item %d\" %s", index, index, states),
		)
		snapshot.Elements = append(snapshot.Elements, elementRecord{
			Index:       index,
			RuntimeID:   []int{0, index - 1},
			ControlType: "list item",
			Name:        fmt.Sprintf("Item %d", index),
			States:      states,
		})
	}
	applyWorkingTreeProjection(snapshot, treeViewWorking)
	text := strings.Join(snapshot.TreeLines, "\n")
	if !strings.Contains(text, "collection-summary role=list item items=12") {
		t.Fatalf("collection was not summarized:\n%s", text)
	}
	if !strings.Contains(text, `list item "Item 8" [selected click]`) {
		t.Fatalf("selected collection member was hidden:\n%s", text)
	}
	if len(snapshot.Elements) != 13 {
		t.Fatal("collection summary removed canonical records")
	}
	if snapshot.serializerOmitted == 0 {
		t.Fatal("collection summary did not report serializer omissions")
	}
}

func TestWorkingProjectionNormalizesQuotedWhitespace(t *testing.T) {
	record := &elementRecord{
		Index:       1,
		ControlType: "list item",
		Name:        "2024 Sedan\n  Low mileage",
		Value:       "Dealer\t\tprice",
	}
	line := "\t1 list item \"2024 Sedan\\n  Low mileage\": \"Dealer\t\tprice\""
	cleaned := cleanWorkingTreeLine(line, record)
	if cleaned != "\t1 list item \"2024 Sedan Low mileage\": \"Dealer price\"" {
		t.Fatalf("working text whitespace was not normalized: %q", cleaned)
	}
	if record.Name != "2024 Sedan\n  Low mileage" ||
		record.Value != "Dealer\t\tprice" {
		t.Fatal("working projection normalization changed canonical provider values")
	}
}

func TestWorkingProjectionNormalizedSelectorRoundTrips(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 list \"Results\"",
			"\t\t1 list item \"2024 Sedan\\n  Low mileage\"",
		},
		Elements: []elementRecord{
			{Index: 0, ControlType: "list", Name: "Results"},
			{
				Index:       1,
				ControlType: "list item",
				Name:        "2024 Sedan\n  Low mileage",
			},
		},
	}
	applyWorkingTreeProjection(snapshot, treeViewWorking)
	if !strings.Contains(
		strings.Join(snapshot.TreeLines, "\n"),
		`list item "2024 Sedan Low mileage"`,
	) {
		t.Fatalf("working tree did not expose normalized identity: %v", snapshot.TreeLines)
	}
	record, err := lookupBySelectorFor(
		snapshot,
		`list item "2024 Sedan Low mileage"`,
		"",
		"test",
	)
	if err != nil || record == nil || record.Index != 1 {
		t.Fatalf("normalized working selector did not round-trip: record=%#v err=%v", record, err)
	}
}

func TestNormalizedSelectorAmbiguityFailsClosed(t *testing.T) {
	snapshot := &appSnapshot{Elements: []elementRecord{
		{Index: 1, ControlType: "entry", Name: "Account\tName"},
		{Index: 2, ControlType: "entry", Name: "Account Name"},
	}}
	record, err := lookupBySelectorFor(
		snapshot,
		`entry "Account Name"`,
		"",
		"test",
	)
	if err == nil || record != nil ||
		!strings.Contains(strings.ToLower(err.Error()), "ambiguous") {
		t.Fatalf("normalized identity collision did not fail closed: record=%#v err=%v", record, err)
	}
}

func TestWorkingProjectionSummarizesLargeStructurallyVaryingCards(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{"\t0 list \"Vehicles\""},
		Elements: []elementRecord{{
			Index: 0, RuntimeID: []int{0}, ControlType: "list", Name: "Vehicles",
		}},
	}
	for index := 1; index <= 24; index++ {
		snapshot.TreeLines = append(
			snapshot.TreeLines,
			fmt.Sprintf("\t\t%d list item \"Vehicle %d\"", index, index),
		)
		snapshot.Elements = append(snapshot.Elements, elementRecord{
			Index:       index,
			RuntimeID:   []int{0, index - 1},
			ControlType: "list item",
			Name:        fmt.Sprintf("Vehicle %d", index),
		})
		childIndex := 100 + index
		childRole := "static"
		if index%2 == 0 {
			childRole = "push button"
		}
		snapshot.TreeLines = append(
			snapshot.TreeLines,
			fmt.Sprintf("\t\t\t%d %s \"Card detail %d\"", childIndex, childRole, index),
		)
		snapshot.Elements = append(snapshot.Elements, elementRecord{
			Index:       childIndex,
			RuntimeID:   []int{0, index - 1, 0},
			ControlType: childRole,
			Name:        fmt.Sprintf("Card detail %d", index),
		})
	}
	applyWorkingTreeProjection(snapshot, treeViewWorking)
	text := strings.Join(snapshot.TreeLines, "\n")
	if count := strings.Count(
		text,
		"collection-summary role=list item items=12",
	); count != 2 {
		t.Fatalf("cards were not separated by structural shape: summaries=%d\n%s", count, text)
	}
	if len(snapshot.Elements) != 49 {
		t.Fatal("card summarization removed canonical records")
	}
}

func TestWorkingProjectionDoesNotSummarizeArbitraryLargeControlSet(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{"\t0 panel \"Commands\""},
		Elements: []elementRecord{{
			Index: 0, RuntimeID: []int{0}, ControlType: "panel", Name: "Commands",
		}},
	}
	for index := 1; index <= 24; index++ {
		snapshot.TreeLines = append(
			snapshot.TreeLines,
			fmt.Sprintf("\t\t%d push button \"Command %d\" [has-click-action]", index, index),
		)
		snapshot.Elements = append(snapshot.Elements, elementRecord{
			Index:       index,
			RuntimeID:   []int{0, index - 1},
			ControlType: "push button",
			Name:        fmt.Sprintf("Command %d", index),
			States:      "[has-click-action]",
		})
	}
	applyWorkingTreeProjection(snapshot, treeViewWorking)
	text := strings.Join(snapshot.TreeLines, "\n")
	if strings.Contains(text, "collection-summary") {
		t.Fatalf("arbitrary controls were summarized by count alone:\n%s", text)
	}
	for _, expected := range []string{`button "Command 1"`, `button "Command 12"`, `button "Command 24"`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("working tree hid %q:\n%s", expected, text)
		}
	}
}

func TestActionFallbackReturnsFocusedContextNotWholeTree(t *testing.T) {
	snapshot := beta14LocalSnapshot()
	typedTreeTextForTest(t, snapshot)
	lines := compactActionObservationLines(snapshot)
	text := strings.Join(lines, "\n")
	if !strings.Contains(text, "focus:") ||
		!strings.Contains(text, `textbox "Name"`) {
		t.Fatalf("compact action observation lacks focused context:\n%s", text)
	}
	if strings.Contains(text, `status "Ready"`) {
		t.Fatalf("compact action observation replayed unrelated window state:\n%s", text)
	}
}

func TestActionFallbackPrefersChangedContentWhenFocusIsNavigation(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"Settings\"",
			"\t\t1 tree \"Navigation\"",
			"\t\t\t2 tree item \"Composition\" [focused selected]",
			"\t\t3 document web \"Composition Settings\"",
			"\t\t\t4 checkbox \"Automatically quote\"",
		},
		Elements: []elementRecord{
			{Index: 0, RuntimeID: []int{0}, ControlType: "frame", Name: "Settings"},
			{Index: 1, RuntimeID: []int{0, 0}, ControlType: "tree", Name: "Navigation"},
			{Index: 2, RuntimeID: []int{0, 0, 0}, ControlType: "tree item", Name: "Composition", States: "[focused selected]"},
			{Index: 3, RuntimeID: []int{0, 1}, ControlType: "document web", Name: "Composition Settings"},
			{Index: 4, RuntimeID: []int{0, 1, 0}, ControlType: "check box", Name: "Automatically quote"},
		},
		scope:           a11yScopeWindow,
		contentRevision: "c1:changed",
	}
	typedTreeTextForTest(t, snapshot)
	snapshot.contentRoot = &snapshot.Elements[3]

	lines := compactActionObservationLines(snapshot, nil)
	text := strings.Join(lines, "\n")
	if !strings.Contains(text, "content:") ||
		!strings.Contains(text, `checkbox "Automatically quote"`) {
		t.Fatalf("navigation fallback omitted changed content:\n%s", text)
	}
	if strings.Contains(text, `tree item "Composition"`) {
		t.Fatalf("navigation fallback returned only the old focused node:\n%s", text)
	}
	service := newService()
	result, _, returned, compact, fullTree :=
		service.renderAXDiffOrCompactObservation(snapshot, nil)
	if !returned || !compact || fullTree ||
		result.StructuredContent["final_observation_kind"] != "content" {
		t.Fatalf("content fallback kind is inconsistent: %#v",
			result.StructuredContent)
	}
}

func TestUnchangedEmittedMutationKeepsEffectUnknown(t *testing.T) {
	result := toolCallResult{
		StructuredContent: map[string]any{
			"input_events_emitted":       true,
			"observation_required":       false,
			"final_observation_returned": true,
		},
	}
	result = markUnchangedMutationUncertainty(
		result,
		true,
		pixelsIdentical,
	)
	if result.StructuredContent["effect_unknown"] != true ||
		result.StructuredContent["mutation_unknown"] != true ||
		result.StructuredContent["observation_required"] != false {
		t.Fatalf("fresh unchanged mutation evidence is incomplete: %#v",
			result.StructuredContent)
	}
}

func TestChangedOrUnemittedActionDoesNotInventEffectUnknown(t *testing.T) {
	for _, tc := range []struct {
		name              string
		semanticUnchanged bool
		pixels            pixelVerdict
		inputEmitted      bool
	}{
		{name: "semantic change", inputEmitted: true},
		{name: "pixel change", semanticUnchanged: true, pixels: pixelsChanged, inputEmitted: true},
		{name: "no input", semanticUnchanged: true, pixels: pixelsIdentical},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := toolCallResult{StructuredContent: map[string]any{
				"input_events_emitted": tc.inputEmitted,
			}}
			result = markUnchangedMutationUncertainty(
				result,
				tc.semanticUnchanged,
				tc.pixels,
			)
			if result.StructuredContent["effect_unknown"] == true {
				t.Fatalf("invented uncertainty: %#v", result.StructuredContent)
			}
		})
	}
}

func TestFindTextMatchReportsOriginatingField(t *testing.T) {
	record := &elementRecord{
		Name:        "",
		Description: "SMTP user anonym-x2024@outlook.com",
		Value:       "unrelated",
	}
	field, value, ok := matchedFindTextField(
		record,
		"anonym-x2024",
		false,
	)
	if !ok || field != "description" ||
		value != record.Description {
		t.Fatalf("find match origin=%q %q %t", field, value, ok)
	}
}

func TestFindPageRendersOriginatingTextField(t *testing.T) {
	service := newServiceWithBackend(&recordingPlatformBackend{})
	snapshot := beta14LocalSnapshot()
	snapshot.Elements[3].Description = "Account anonym-x2024@outlook.com"
	if err := service.buildAndRenderAXSnapshotV1(
		snapshot,
		treeViewWorking,
	); err != nil {
		t.Fatal(err)
	}
	matched := snapshot.Elements[3]
	matched.FindMatchedField = "description"
	matched.FindMatchedText = matched.Description
	result := service.renderFindPage(
		snapshot,
		[]elementRecord{matched},
		0,
		1,
		`text_contains="anonym-x2024"`,
		nil,
		false,
	)
	text := resultText(result)
	if result.IsError ||
		!strings.Contains(text, "matched_field=description") ||
		!strings.Contains(text, strconv.Quote(matched.Description)) {
		t.Fatalf("find output hid match origin:\n%s", text)
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

func TestCompactActionObservationDoesNotAdvanceDeliveredTreeBaseline(t *testing.T) {
	service := newService()
	previous := beta14LocalSnapshot()
	previous.sourceApp = "editor"
	previous.publicWindowRef = "w1"
	previous.SnapshotID = "previous"
	previous.projection = snapshotProjection{View: treeViewWorking, Scope: a11yScopeWindow}
	service.rememberDeliveredSnapshot(previous)

	current := beta14LocalSnapshot()
	current.sourceApp = "editor"
	current.publicWindowRef = "w1"
	current.SnapshotID = "current"
	current.projection = snapshotProjection{View: treeViewWorking, Scope: a11yScopeWindow}
	current.TreeLines = append([]string(nil), previous.TreeLines...)
	current.TreeLines[1] = strings.Repeat("changed ", 900)
	compact := compactActionObservationLines(current)
	response := current.resultWithTreeOverride(nil, compact)
	if advanced := service.rememberDeliveredActionObservation(
		current,
		response,
		compact,
		true,
		false,
		true,
	); advanced {
		t.Fatal("focused compact context must not advance the complete delivered-tree baseline")
	}
	if got := service.deliveredSnapshotFor(current); got != previous {
		t.Fatalf("compact context replaced the model-visible baseline: got %#v want %#v", got, previous)
	}
}

func TestContentTransitionFallbackReturnsWorkingTree(t *testing.T) {
	service := newService()
	previous := beta14LocalSnapshot()
	previous.sourceApp = "editor"
	previous.publicWindowRef = "w1"
	previous.SnapshotID = "previous"
	previous.contentRevision = "c1:0123456789abcdef01234567"
	previous.projection = snapshotProjection{
		View: treeViewWorking, Scope: a11yScopeWindow,
	}
	previous.axSnapshot = nil
	service.rememberDeliveredSnapshot(previous)

	current := beta14LocalSnapshot()
	current.sourceApp = "editor"
	current.publicWindowRef = "w1"
	current.SnapshotID = "current"
	current.contentRevision = "c1:89abcdef0123456701234567"
	current.projection = snapshotProjection{
		View: treeViewWorking, Scope: a11yScopeWindow,
	}
	current.axSnapshot = nil
	current.TreeLines = []string{
		`ax:`,
		` - document "Settings":`,
		`  - checkbox "New panel control" [ref=r9]`,
	}

	result, lines, returned, compact, fullTree :=
		service.renderAXDiffOrCompactObservation(current, nil)
	if !returned || compact || !fullTree || len(lines) != len(current.TreeLines) {
		t.Fatalf("content transition fallback missing: returned=%t compact=%t full=%t lines=%#v",
			returned, compact, fullTree, lines)
	}
	if result.StructuredContent["content_transition_working_tree"] != true ||
		result.StructuredContent["final_observation_kind"] != "working_tree" ||
		!strings.Contains(resultText(result), `checkbox "New panel control"`) {
		t.Fatalf("content transition evidence is incomplete: %#v\n%s",
			result.StructuredContent, resultText(result))
	}
}

func TestFocusedFallbackTriggersOneContentStabilizationRead(t *testing.T) {
	service := newService()
	previous := beta14LocalSnapshot()
	previous.sourceApp = "editor"
	previous.WindowHandle = "w2:0123456789abcdef0123456789abcdef"
	previous.publicWindowRef = service.windowRefRegistry.Acquire(
		"editor",
		previous.WindowHandle,
		`frame "Editor"`,
		"c1",
	)
	if previous.publicWindowRef == "" {
		t.Fatal("test window ref was not issued")
	}
	previous.SnapshotID = "previous"
	previous.contentRevision = "c1:0123456789abcdef01234567"
	previous.projection = snapshotProjection{
		View: treeViewWorking, Scope: a11yScopeWindow,
	}
	previous.axSnapshot = nil
	service.rememberDeliveredSnapshot(previous)

	current := beta14LocalSnapshot()
	current.sourceApp = "editor"
	current.publicWindowRef = previous.publicWindowRef
	current.WindowHandle = previous.WindowHandle
	current.SnapshotID = "current"
	current.contentRevision = previous.contentRevision
	current.projection = previous.projection
	current.axSnapshot = nil
	current.TreeLines = []string{"ax:"}
	for index := 0; index < 60; index++ {
		current.TreeLines = append(
			current.TreeLines,
			fmt.Sprintf(` - group "Replacement %d"`, index),
		)
	}

	request := axServiceRequest{Observe: observeDiff}
	unchanged := *current
	unchanged.TreeLines = append([]string(nil), previous.TreeLines...)
	unchanged.contentRevision = "c1:fedcba9876543210fedcba98"
	if !shouldStabilizePostActionContent(
		service,
		&unchanged,
		request,
		settlePlan{},
		false,
	) {
		t.Fatal("hidden canonical change suppressed a zero-change diff refresh")
	}
	if !shouldStabilizePostActionContent(
		service,
		current,
		request,
		settlePlan{},
		false,
	) {
		t.Fatal("focused fallback did not request one bounded content refresh")
	}
	if shouldStabilizePostActionContent(
		service,
		current,
		request,
		settlePlan{expect: &expectSpec{}},
		false,
	) {
		t.Fatal("explicit postcondition settling must not receive an extra refresh")
	}
	windowAction := WindowActivate
	request.WindowAction = &windowAction
	if shouldStabilizePostActionContent(
		service,
		current,
		request,
		settlePlan{},
		false,
	) {
		t.Fatal("window actions must use window identity or lifecycle handling")
	}
	request.WindowAction = nil
	current.contentRevision = "c1:89abcdef0123456701234567"
	if shouldStabilizePostActionContent(
		service,
		current,
		request,
		settlePlan{},
		false,
	) {
		t.Fatal("proved content transition should render directly without stabilization")
	}

	current.WindowHandle = ""
	if got := service.postActionContentWindowHandle(current); got != previous.WindowHandle {
		t.Fatalf("window registry fallback=%q, want %q", got, previous.WindowHandle)
	}
}

func TestIncrementalObservationHasChangesSupportsBothRenderers(t *testing.T) {
	for _, unchanged := range [][]string{
		{"Incremental view: 0 changes across 3 tree lines."},
		{"ax_diff:", " effect: no_ax_change"},
	} {
		if incrementalObservationHasChanges(unchanged) {
			t.Fatalf("zero-change observation classified as changed: %#v", unchanged)
		}
	}
	for _, changed := range [][]string{
		{"Incremental view: 1 line(s) gone, 1 line(s) new."},
		{"ax_diff:", ` added: [button "Continue"]`},
		{
			"ax_diff:",
			" effect: no_ax_change",
			` changed: [button "Continue"]`,
		},
	} {
		if !incrementalObservationHasChanges(changed) {
			t.Fatalf("changed observation classified as unchanged: %#v", changed)
		}
	}
}

func TestFailedWindowTitlePollIsNotLiveUIEvidence(t *testing.T) {
	result := newService().verifyWindowTitleWithProbe(
		"chrome",
		"Settings",
		true,
		1,
		0,
		"",
		func(ProviderWindowListRequest) (*ProviderWindowResult, error) {
			return nil, errors.New("AX bridge unavailable")
		},
	)
	if !result.IsError {
		t.Fatal("all-failed window probes must return an error")
	}
	if sampled, _ := result.StructuredContent["assertion_sampled_live_ui"].(bool); sampled {
		t.Fatalf("failed probes were reported as live UI evidence: %#v", result.StructuredContent)
	}
	if scope := assertionEvidenceScope(result); scope != "error_only" {
		t.Fatalf("failed probes got evidence scope %q", scope)
	}
}

func TestCompletedNegativeAssertionIsAnObservationNotAProtocolError(t *testing.T) {
	result := assertionNegativeResult(
		"assert_ax predicate_false: current value is unchecked",
		"predicate_false",
		3,
		0,
	)
	if result.IsError {
		t.Fatalf("completed negative assertion became a tool error: %#v", result)
	}
	if result.StructuredContent["assertion_passed"] != false ||
		result.StructuredContent["retry_same_request"] != false ||
		result.StructuredContent["poll_attempted"] != true {
		t.Fatalf("negative assertion evidence is incomplete: %#v",
			result.StructuredContent)
	}
}

func TestPostconditionFalsePreservesSuccessfulMutationCall(t *testing.T) {
	action := toolCallResult{
		Content: []contentItem{{Type: "text", Text: "action delivered"}},
		StructuredContent: map[string]any{
			"input_events_emitted": true,
		},
	}
	assertion := assertionNegativeResult(
		"assert_ax predicate_false: current value is unchecked",
		"predicate_false",
		2,
		0,
	)
	merged := mergeA11yPostconditionResult(action, assertion)
	if merged.IsError {
		t.Fatalf("false postcondition converted a delivered mutation into a call error")
	}
	if merged.StructuredContent["postcondition_verified"] != false ||
		merged.StructuredContent["preflight_only"] != false ||
		merged.StructuredContent["evidence_scope"] != "post_action_observation" {
		t.Fatalf("postcondition evidence is dishonest: %#v",
			merged.StructuredContent)
	}
}

func TestPostconditionPassPreservesUnattributedMutationUncertainty(t *testing.T) {
	action := toolCallResult{
		StructuredContent: map[string]any{
			"input_events_emitted": true,
			"effect_unknown":       true,
			"mutation_unknown":     true,
		},
	}
	assertion := toolCallResult{
		StructuredContent: map[string]any{
			"assertion_status": "pass",
		},
	}
	merged := mergeA11yPostconditionResult(action, assertion)
	if merged.StructuredContent["postcondition_verified"] != true {
		t.Fatalf("passing postcondition was not recorded: %#v",
			merged.StructuredContent)
	}
	if merged.StructuredContent["effect_unknown"] != true ||
		merged.StructuredContent["mutation_unknown"] != true ||
		merged.StructuredContent["cause_unattributed"] != true {
		t.Fatalf("passing postcondition overclaimed action causality: %#v",
			merged.StructuredContent)
	}
}

func TestPassingPostconditionPreservesNestedActionUncertainty(t *testing.T) {
	structured := map[string]any{
		"input_events_emitted": true,
	}
	action := map[string]any{}
	preserveUnattributedPostconditionEvidence(structured, action)
	if action["mutation_unknown"] != true ||
		action["cause_unattributed"] != true ||
		structured["effect_unknown"] != true ||
		structured["mutation_unknown"] != true ||
		structured["cause_unattributed"] != true {
		t.Fatalf("postcondition erased nested uncertainty: structured=%#v action=%#v",
			structured, action)
	}
}

func TestPassingPostconditionKeepsIndependentTaskEffectProof(t *testing.T) {
	structured := map[string]any{
		"input_events_emitted": true,
		"task_effect_verified": true,
	}
	action := map[string]any{}
	preserveUnattributedPostconditionEvidence(structured, action)
	if _, present := structured["effect_unknown"]; present {
		t.Fatalf("independently verified effect became uncertain: %#v", structured)
	}
	if len(action) != 0 {
		t.Fatalf("independent effect proof altered action evidence: %#v", action)
	}
}

func TestVerifiedCloseClearsTopLevelAndNestedUncertainty(t *testing.T) {
	structured := map[string]any{
		"effect_unknown":     true,
		"mutation_unknown":   true,
		"cause_unattributed": true,
	}
	action := map[string]any{
		"mutation_unknown":   true,
		"cause_unattributed": true,
	}
	clearVerifiedCloseUncertainty(structured, action)
	for _, key := range []string{
		"effect_unknown",
		"mutation_unknown",
		"cause_unattributed",
	} {
		if _, present := structured[key]; present {
			t.Fatalf("verified close retained top-level %s: %#v",
				key, structured)
		}
		if _, present := action[key]; present {
			t.Fatalf("verified close retained action-level %s: %#v",
				key, action)
		}
	}
}

func TestAXFinalTextBudgetCoversAllTextItems(t *testing.T) {
	t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "240")
	result := toolCallResult{
		Content: []contentItem{
			{Type: "text", Text: strings.Repeat("a", 160)},
			{Type: "text", Text: strings.Repeat("b", 160)},
		},
		StructuredContent: map[string]any{"tree_view": "working"},
	}
	result = enforceAXResponseBudget(result)
	if chars := textContentChars(result); chars > 240 {
		t.Fatalf("final text has %d chars, exceeds advertised 240", chars)
	}
	if !strings.Contains(resultText(result), "[TRUNCATED:") {
		t.Fatalf("truncation was not reported: %q", resultText(result))
	}
	if result.StructuredContent["tree_complete_for_model"] != false ||
		result.StructuredContent["truncation_reason"] != "response_budget" {
		t.Fatalf("structured budget metadata is dishonest: %#v", result.StructuredContent)
	}
	if got := result.StructuredContent["response_chars"]; got != textContentChars(result) {
		t.Fatalf("response_chars=%v, actual=%d", got, textContentChars(result))
	}
}

func TestAXFinalTextBudgetReportsStructuralOmissionWithoutTextTruncation(t *testing.T) {
	t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "800")
	result := toolCallResult{
		Content: []contentItem{
			{Type: "text", Text: "ax:\n - \"window \\\"Editor\\\"\""},
		},
		StructuredContent: map[string]any{
			"tree_view":                 "working",
			"structural_budget_applied": true,
		},
	}
	result = enforceAXResponseBudget(result)
	if strings.Contains(resultText(result), "[response-budget]") {
		t.Fatalf("routine omission returned operator telemetry: %q",
			resultText(result))
	}
	if result.StructuredContent["tree_complete_for_model"] != false ||
		result.StructuredContent["truncation_reason"] != "structural_budget" {
		t.Fatalf(
			"structural budget metadata is dishonest: %#v",
			result.StructuredContent,
		)
	}
}

func TestLegacyTextEvidenceIncludesFinalStructuralBudgetMetadata(t *testing.T) {
	t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "800")
	result := toolCallResult{
		Content: []contentItem{
			{Type: "text", Text: "ax:\n - \"window \\\"Editor\\\"\""},
		},
		StructuredContent: map[string]any{
			"tree_view":                         "working",
			"structural_budget_applied":         true,
			"structural_budget_omitted_nodes":   17,
			"structural_budget_omitted_targets": 4,
		},
	}
	result = enforceAXResponseBudget(result)
	result = withTextEvidence(result)
	if len(result.Content) == 0 ||
		!strings.HasPrefix(result.Content[0].Text, "[tool-evidence] ") {
		t.Fatalf("legacy text evidence missing: %#v", result.Content)
	}
	line := strings.TrimPrefix(result.Content[0].Text, "[tool-evidence] ")
	var evidence map[string]any
	if err := json.Unmarshal([]byte(line), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence["truncation_reason"] != "structural_budget" ||
		evidence["tree_complete_for_model"] != false ||
		evidence["structural_budget_omitted_nodes"] != float64(17) ||
		evidence["structural_budget_omitted_targets"] != float64(4) {
		t.Fatalf("legacy evidence serialized before final budget: %#v", evidence)
	}
}

func TestPrepareAXEvidenceCompactsSuccessWithoutReturningTelemetry(t *testing.T) {
	result := toolCallResult{
		Content: []contentItem{{Type: "text", Text: "ax:\n - window \"Editor\""}},
		StructuredContent: map[string]any{
			"ax_mode":                 "observe",
			"canonical_element_count": 157,
			"tree_complete_for_model": true,
			"truncation_reason":       "none",
			"task_effect_verified":    false,
			"distillation": []any{
				map[string]any{"name": "normalize_whitespace"},
			},
			"shadow_ast": map[string]any{
				"projected_nodes": 128,
				"target_recall":   1,
			},
		},
	}
	result = prepareAXEvidence(result)
	if len(result.StructuredContent) != 0 {
		t.Fatalf("routine success should not return audit counters: %#v",
			result.StructuredContent)
	}
	for _, noisy := range []string{
		"distillation",
		"shadow_ast",
		"task_effect_verified",
		"truncation_reason",
		"tree_complete_for_model",
	} {
		if _, present := result.StructuredContent[noisy]; present {
			t.Fatalf("compact success leaked %s: %#v", noisy,
				result.StructuredContent)
		}
	}
	visible := withAXTextEvidence(result)
	if len(visible.Content) != 1 ||
		strings.HasPrefix(visible.Content[0].Text, "[ax] ") {
		t.Fatalf("routine success added an empty evidence prefix: %#v",
			visible.Content)
	}
}

func TestPrepareAXEvidenceReportsStructuralOmission(t *testing.T) {
	result := toolCallResult{
		Content: []contentItem{{Type: "text", Text: "ax:\n - /omitted: budget"}},
		StructuredContent: map[string]any{
			"ax_mode":                           "observe",
			"canonical_element_count":           430,
			"tree_complete_for_model":           false,
			"truncation_reason":                 "structural_budget",
			"structural_budget_applied":         true,
			"structural_budget_omitted_nodes":   310,
			"structural_budget_omitted_targets": 40,
			"shadow_ast": map[string]any{
				"projected_nodes": 120,
			},
		},
	}
	result = prepareAXEvidence(result)
	if result.StructuredContent["tree_complete"] != false ||
		result.StructuredContent["structural_budget_omitted_nodes"] != nil ||
		result.StructuredContent["structural_budget_omitted_targets"] != nil ||
		result.StructuredContent["truncation_reason"] != nil {
		t.Fatalf("structural omission facts were lost: %#v",
			result.StructuredContent)
	}
	recover, ok := result.StructuredContent["recover"].(map[string]any)
	if !ok || recover["structural_budget"] != "find|root" {
		t.Fatalf("structural omission recovery was lost: %#v", result.StructuredContent)
	}
}

func TestProviderOmissionRecoveryDistinguishesProjectionFromAcquisition(t *testing.T) {
	coverage := providerCoverageSummary(
		[]axProviderAnnotation{
			{Kind: "provider_omission", Count: 3, Reason: "filtered"},
			{Kind: "provider_omission", Count: 2, Reason: "pressure"},
			{Kind: "hidden_children", Count: 7, Reason: "collapsed_menu"},
			{
				Kind: "hidden_children", Count: 11,
				Reason: "provider_children_not_rendered",
			},
			{Kind: "provider_omission", Count: 4, Reason: "max_tree_depth"},
			{Kind: "provider_omission", Count: 5, Reason: "time_budget"},
			{
				Kind: "provider_omission", Count: 6,
				Reason: "filtered,max_tree_depth",
			},
		},
	)
	if coverage.ProjectionHidden != 5 ||
		coverage.ExpandableHidden != 18 ||
		coverage.AcquisitionIncomplete != 15 {
		t.Fatalf(
			"coverage classification: %#v",
			coverage,
		)
	}

	result := prepareAXEvidence(toolCallResult{
		Content: []contentItem{{Type: "text", Text: "ax: []"}},
		StructuredContent: map[string]any{
			"ax_mode":                   "observe",
			"projection_hidden_nodes":   5,
			"expandable_hidden_nodes":   18,
			"coverage_incomplete_nodes": 9,
			"tree_complete_for_model":   false,
		},
	})
	recover, ok := result.StructuredContent["recover"].(map[string]any)
	if ok && len(recover) > 0 {
		t.Fatalf("dishonest omission recovery: %#v", result.StructuredContent)
	}
	if result.StructuredContent["coverage"] != "partial" ||
		result.StructuredContent["tree_complete"] != false {
		t.Fatalf("coverage category was lost: %#v", result.StructuredContent)
	}
}

func TestTargetScopedCoverageRecoveryOnlyAfterTargetedFailure(t *testing.T) {
	tests := []struct {
		name        string
		full        map[string]any
		isError     bool
		wantPartial bool
		wantRecover bool
	}{
		{
			name: "find match suppresses unrelated coverage warning",
			full: map[string]any{
				"ax_mode":                   "find",
				"find_match_count":          1,
				"coverage_incomplete_nodes": 12,
				"tree_complete_for_model":   false,
			},
		},
		{
			name: "find miss exposes targeted recovery",
			full: map[string]any{
				"ax_mode":                   "find",
				"find_match_count":          0,
				"coverage_incomplete_nodes": 12,
				"tree_complete_for_model":   false,
			},
			wantPartial: true,
			wantRecover: true,
		},
		{
			name: "successful action suppresses unrelated coverage warning",
			full: map[string]any{
				"ax_mode":                   "act",
				"coverage_incomplete_nodes": 12,
				"tree_complete_for_model":   false,
				"input_events_emitted":      true,
			},
		},
		{
			name: "failed resolve exposes targeted recovery",
			full: map[string]any{
				"ax_mode":                   "act",
				"coverage_incomplete_nodes": 12,
				"tree_complete_for_model":   false,
				"failure_phase":             "resolve",
				"input_events_emitted":      false,
			},
			isError:     true,
			wantPartial: true,
			wantRecover: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := compactAXModelEvidence(test.full, test.isError)
			if got := result["coverage"] == "partial"; got != test.wantPartial {
				t.Fatalf("coverage partial=%t, want %t: %#v",
					got, test.wantPartial, result)
			}
			recover, _ := result["recover"].(map[string]any)
			got := recover["target"] == "GUI" ||
				recover["target"] == "find|GUI"
			if got != test.wantRecover {
				t.Fatalf("target recovery=%t, want %t: %#v",
					got, test.wantRecover, result)
			}
		})
	}
}

func TestAllModelRenderersHideProviderDiagnostics(t *testing.T) {
	snapshot := beta14LocalSnapshot()
	snapshot.Annotations = []axProviderAnnotation{{
		Kind:   "provider_omission",
		Count:  12,
		Reason: "time_budget",
		Detail: "internal traversal stopped",
	}}
	for _, view := range []string{treeViewWorking, treeViewFull} {
		typed, err := buildAXSnapshotV1(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if err := distillAXSnapshot(typed); err != nil {
			t.Fatal(err)
		}
		lines, err := RenderAXSnapshotYAML(
			typed,
			AXRenderOptions{
				RootKey: "ax",
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Join(lines, "\n")
		for _, forbidden := range []string{
			"provider_omission",
			"time_budget",
			"internal traversal stopped",
			"/provider-role",
			"/provider-actions",
		} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s renderer leaked %q:\n%s", view, forbidden, text)
			}
		}
	}
}

func TestModelContractHasBoundedFixedContext(t *testing.T) {
	t.Setenv("COMPUTER_USE_LEGACY_A11Y_TOOLS", "off")
	if len(serverInstructions) > 1500 {
		t.Fatalf("server instructions grew to %d bytes", len(serverInstructions))
	}
	encoded, err := json.Marshal(toolDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf(
		"model contract bytes: instructions=%d tools=%d total=%d",
		len(serverInstructions),
		len(encoded),
		len(serverInstructions)+len(encoded),
	)
	if len(encoded) > 25000 {
		t.Fatalf("public tool definitions grew to %d bytes", len(encoded))
	}
	if total := len(serverInstructions) + len(encoded); total > 26000 {
		t.Fatalf("total fixed model contract grew to %d bytes", total)
	}
}

func TestPublicModelContractDoesNotRecommendLegacyMutationTargets(t *testing.T) {
	encoded, err := json.Marshal(toolDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	publicContract := serverInstructions + "\n" + string(encoded)
	for _, forbidden := range []string{
		"element_index",
		`view:\"full\"`,
		"provider facts",
		"invoke_element_action",
	} {
		if strings.Contains(publicContract, forbidden) {
			t.Fatalf(
				"public model contract leaked legacy mutation guidance %q",
				forbidden,
			)
		}
	}
	for _, required := range []string{
		"act consumes refs only",
		"Element actions require rN",
		"window actions require wN",
	} {
		if !strings.Contains(publicContract, required) {
			t.Fatalf("public model contract is missing %q", required)
		}
	}
}

func TestPrepareAXEvidenceKeepsPreflightFailureFacts(t *testing.T) {
	result := toolCallResult{
		Content: []contentItem{{Type: "text", Text: "stale ref"}},
		IsError: true,
		StructuredContent: map[string]any{
			"ax_mode":                 "act",
			"failure_code":            "stale_element_ref",
			"failure_phase":           "precondition",
			"input_events_emitted":    false,
			"partial_input_possible":  false,
			"mutation_unknown":        false,
			"failure_facts_complete":  true,
			"task_effect_verified":    false,
			"response_budget_chars":   24000,
			"tree_complete_for_model": true,
		},
	}
	result = prepareAXEvidence(result)
	for key, expected := range map[string]any{
		"failure_code":           "stale_element_ref",
		"failure_phase":          "precondition",
		"input_events_emitted":   false,
		"partial_input_possible": false,
		"mutation_unknown":       false,
		"failure_facts_complete": true,
	} {
		if result.StructuredContent[key] != expected {
			t.Fatalf("preflight fact %s lost: %#v", key,
				result.StructuredContent)
		}
	}
	if _, present := result.StructuredContent["response_budget_chars"]; present {
		t.Fatalf("preflight evidence leaked normal telemetry: %#v",
			result.StructuredContent)
	}
}

func TestPrepareAXEvidenceKeepsPartialBatchFailure(t *testing.T) {
	result := toolCallResult{
		Content: []contentItem{{Type: "text", Text: "batch stopped"}},
		IsError: true,
		StructuredContent: map[string]any{
			"ax_mode": "act",
			"a11y_batch_result": map[string]any{
				"actions_requested":                    2,
				"actions_completed_without_call_error": 1,
				"failed_action_index":                  1,
				"failed_action_type":                   "click",
				"partial_sequence":                     true,
				"actions": []map[string]any{
					{
						"action_index": 0,
						"action_type":  "set_value",
						"call_error":   false,
						"actionability": map[string]any{
							"route":         "editable_text",
							"input_emitted": "true",
						},
					},
					{
						"action_index":  1,
						"action_type":   "click",
						"call_error":    true,
						"failure_code":  "stale_element_ref",
						"failure_phase": "precondition",
						"actionability": map[string]any{
							"resolution":    "failed",
							"route":         "unknown",
							"input_emitted": "false",
						},
					},
				},
			},
			"observation_required": true,
		},
	}
	result = prepareAXEvidence(result)
	batch, ok := result.StructuredContent["batch"].(map[string]any)
	if !ok || batch["partial_sequence"] != true ||
		batch["failed_action_index"] != 1 {
		t.Fatalf("partial batch summary is incomplete: %#v",
			result.StructuredContent)
	}
	actions, ok := batch["actions"].([]map[string]any)
	if !ok || len(actions) != 1 ||
		actions[0]["index"] != 1 ||
		actions[0]["failure_code"] != "stale_element_ref" ||
		actions[0]["failure_phase"] != "precondition" {
		t.Fatalf("ordered partial action evidence was lost: %#v", batch)
	}
	if _, present := actions[0]["route"]; present {
		t.Fatalf("provider route leaked into model evidence: %#v", actions[0])
	}
}

func TestPrepareAXEvidenceOmitsRoutineBatchDefaults(t *testing.T) {
	result := prepareAXEvidence(toolCallResult{
		Content: []contentItem{{Type: "text", Text: "batch result"}},
		StructuredContent: map[string]any{
			"ax_mode": "act",
			"a11y_batch_result": map[string]any{
				"actions_requested":                    1,
				"actions_completed_without_call_error": 1,
				"partial_sequence":                     false,
				"actions": []map[string]any{{
					"action_index": 0,
					"action_type":  "click",
					"call_error":   false,
					"actionability": map[string]any{
						"route":                  "semantic_action",
						"input_emitted":          true,
						"readback_sampled":       false,
						"delivery_verified":      "unknown",
						"postcondition_verified": "unknown",
					},
				}},
			},
		},
	})
	if _, present := result.StructuredContent["batch"]; present {
		t.Fatalf("routine successful batch leaked internal action details: %#v",
			result.StructuredContent)
	}
}

func TestModelVisibleEvidenceKeepsInternalHashesPrivate(t *testing.T) {
	internal := map[string]any{
		"snapshot_id":                             "abc123",
		"immediate_snapshot_id":                   "before123",
		"final_snapshot_id":                       "after123",
		"observed_snapshot_id":                    "observed123",
		"window_identity_stabilization_attempted": true,
		"window_identity_stabilization_trigger":   "semantic_window_change",
		"content_revision":                        "c1:deadbeef",
		"content_revision_before":                 "c1:before",
		"content_revision_after":                  "c1:after",
		"failure_code":                            "stale_element_ref",
	}
	visible := modelVisibleEvidence(internal)
	if visible["failure_code"] != "stale_element_ref" {
		t.Fatalf("public failure fact was lost: %#v", visible)
	}
	for key := range modelPrivateEvidenceKeys {
		if _, present := visible[key]; present {
			t.Fatalf("private identity %s leaked: %#v", key, visible)
		}
		if _, retained := internal[key]; !retained {
			t.Fatalf("internal identity %s was deleted", key)
		}
	}
}

func TestContentAndRepeatedTreeOutputKeepRevisionPrivate(t *testing.T) {
	snapshot := beta14LocalSnapshot()
	snapshot.contentRevision = "c1:deadbeef"
	snapshot.scope = a11yScopeContent
	snapshot.scopeRoot = &elementRecord{
		ControlType: "document web",
		Name:        "Example",
	}
	rendered := snapshot.renderedText()
	repeated := strings.Join(repeatedTreeReferenceLines(snapshot), "\n")
	for _, output := range []string{rendered, repeated} {
		for _, private := range []string{"c1:deadbeef", "revision:"} {
			if strings.Contains(output, private) {
				t.Fatalf("model output leaked %q:\n%s", private, output)
			}
		}
	}
	if !strings.Contains(repeated, "Tree unchanged:") {
		t.Fatalf("repeated-tree decision fact was lost: %q", repeated)
	}
}

func TestProviderBoundIdentitySemanticMutationSupersedesOldRef(t *testing.T) {
	registry := NewRefRegistry(16)
	first := registry.AcquireTyped(
		AXLocatorProviderBound,
		"editor", "window-1", `button "Save"`, "button", "Save", "c1",
		":1.42", "/org/a11y/atspi/accessible/77", "99123",
		":1.42", "/org/a11y/atspi/accessible/window/1", "99123",
		"", "", "", "w1",
	)
	renamed := registry.AcquireTyped(
		AXLocatorProviderBound,
		"editor", "window-9", `toggle button "Saved"`, "button", "Saved", "c2",
		":1.42", "/org/a11y/atspi/accessible/77", "99123",
		":1.42", "/org/a11y/atspi/accessible/window/1", "99123",
		"", "", "", "w1",
	)
	if first == "" || renamed == first {
		t.Fatalf("semantic mutation reused provider-bound ref: %q -> %q", first, renamed)
	}
	if _, err := registry.Resolve(first, "editor"); err == nil ||
		!strings.Contains(err.Error(), "superseded") {
		t.Fatalf("old semantic identity did not tombstone: %v", err)
	}
	entry, err := registry.Resolve(renamed, "editor")
	if err != nil {
		t.Fatal(err)
	}
	if entry.SemanticTarget != `toggle button "Saved"` || entry.Name != "Saved" {
		t.Fatalf("new semantic identity was not recorded: %#v", entry)
	}
	restarted := registry.AcquireTyped(
		AXLocatorProviderBound,
		"editor", "window-1", `toggle button "Saved"`, "button", "Saved", "c3",
		":1.99", "/org/a11y/atspi/accessible/77", "100001",
		":1.99", "/org/a11y/atspi/accessible/window/1", "100001",
		"", "", "", "w1",
	)
	if restarted == renamed {
		t.Fatal("process generation and bus change must allocate another ref")
	}
}

func TestStaleRefErrorDoesNotExposeInternalReason(t *testing.T) {
	registry := NewRefRegistry(16)
	ref := registry.AcquireTyped(
		AXLocatorProviderBound,
		"editor", "window-1", `button "Save"`, "button", "Save", "c1",
		":1.42", "/org/a11y/atspi/accessible/77", "99123",
		":1.42", "/org/a11y/atspi/accessible/window/1", "99123",
		"", "", "", "w1",
	)
	privateReason :=
		`provider :1.42 object /org/a11y/atspi/accessible/77 disappeared`
	registry.MarkStale(ref, privateReason)
	_, err := registry.Resolve(ref, "editor")
	if err == nil {
		t.Fatal("stale ref unexpectedly resolved")
	}
	if strings.Contains(err.Error(), ":1.42") ||
		strings.Contains(err.Error(), "/org/a11y/") ||
		strings.Contains(err.Error(), privateReason) {
		t.Fatalf("stale ref exposed internal identity: %v", err)
	}
	if !strings.Contains(err.Error(), "observe or find") {
		t.Fatalf("stale ref lost actionable recovery: %v", err)
	}
}

func TestProviderBoundRefDoesNotReviveAfterTombstone(t *testing.T) {
	registry := NewRefRegistry(16)
	provider := []string{
		":1.42", "/org/a11y/atspi/accessible/77", "99123",
		":1.42", "/org/a11y/atspi/accessible/window/1", "99123",
		"dialog", "Preferences", "a1", "w1",
	}
	first := registry.AcquireTyped(
		AXLocatorProviderBound,
		"editor",
		"w2:"+strings.Repeat("a", 32),
		`button "Save"`,
		"button",
		"Save",
		"c1",
		provider...,
	)
	if first == "" {
		t.Fatal("provider-bound ref was not issued")
	}
	registry.MarkStale(first, "provider_identity_disappeared")
	if _, err := registry.Resolve(first, "editor"); err == nil ||
		!strings.Contains(err.Error(), "stale") {
		t.Fatalf("tombstoned ref resolved: %v", err)
	}
	second := registry.AcquireTyped(
		AXLocatorProviderBound,
		"editor",
		"w2:"+strings.Repeat("a", 32),
		`button "Save"`,
		"button",
		"Save",
		"c2",
		provider...,
	)
	if second == first {
		t.Fatalf("reused provider identity revived stale ref %q", first)
	}
}

func TestProviderBoundShortRefCarriesLocatorKind(t *testing.T) {
	service := newService()
	ref := service.refRegistry.AcquireTyped(
		AXLocatorProviderBound,
		"editor",
		"w2:"+strings.Repeat("a", 32),
		`menu "Format"`,
		"menu",
		"Format",
		"c1",
		":1.42", "/format/1", "99123",
		":1.42", "/window/1", "99123",
		"frame", "Editor", "a1", "w1",
	)
	resolution, result := service.resolveActionElement(
		"editor", ref, "", "click",
	)
	if result.IsError {
		t.Fatalf("provider-bound ref rejected: %s", resultText(result))
	}
	if resolution.semantic == nil ||
		resolution.semantic.LocatorKind != AXLocatorProviderBound {
		t.Fatalf("locator kind was lost: %#v", resolution.semantic)
	}
	if resolution.sourceRef != ref {
		t.Fatalf("source ref=%q, want %q", resolution.sourceRef, ref)
	}
}

func TestProviderBoundShortRefCarriesPrivateRuntimePaths(t *testing.T) {
	service := newService()
	ref := service.refRegistry.AcquireTyped(
		AXLocatorProviderBound,
		"editor",
		"w2:"+strings.Repeat("a", 32),
		`menu "Format"`,
		"menu",
		"Format",
		"c1",
		":1.42", "/format/1", "99123",
		":1.42", "/window/1", "99123",
		"frame", "Editor", "a1", "w1",
	)
	service.refRegistry.BindRuntimePaths(
		ref,
		[]int{2, 4, 6},
		[][]int{{2, 4, 6}, {2, 7, 1}},
	)
	resolution, result := service.resolveActionElement(
		"editor", ref, "", "click",
	)
	if result.IsError {
		t.Fatalf("provider-bound ref rejected: %s", resultText(result))
	}
	if resolution.semantic == nil ||
		!slices.Equal(resolution.semantic.RuntimeID, []int{2, 4, 6}) ||
		len(resolution.semantic.RuntimeIDAliases) != 2 ||
		!slices.Contains(resolution.semantic.ProviderRoleAliases, "menu") ||
		!slices.Equal(
			resolution.semantic.RuntimeIDAliases[1],
			[]int{2, 7, 1},
		) {
		t.Fatalf("runtime paths were lost: %#v", resolution.semantic)
	}
}

func TestCurrentRevisionSemanticRefAuthorizesOneProviderRebind(t *testing.T) {
	service := newService()
	windowHandle := "w2:" + strings.Repeat("a", 32)
	ref := service.refRegistry.AcquireTyped(
		AXLocatorSemantic,
		"editor",
		windowHandle,
		`button "Save"`,
		"button",
		"Save",
		"c1",
		":1.42", "/old/save", "99123",
		":1.42", "/window", "99123",
		"frame", "Editor", "a1", "w1",
	)
	service.refRegistry.BindRuntimePaths(ref, []int{0, 0}, nil)
	snapshot := &appSnapshot{
		App:             appDescriptor{Name: "Editor"},
		WindowHandle:    windowHandle,
		publicWindowRef: "w1",
		axSnapshot: &AXSnapshotV1{
			Revision:     "c1",
			WindowHandle: windowHandle,
			WindowRef:    "w1",
		},
	}
	service.rememberSnapshot("editor", snapshot)

	resolution, result := service.resolveActionElement(
		"editor", ref, "", "click",
	)
	if result.IsError {
		t.Fatalf("current semantic ref rejected: %s", resultText(result))
	}
	if resolution.semantic == nil ||
		!resolution.semantic.AllowProviderRebind {
		t.Fatalf("current semantic ref did not authorize rebind: %#v", resolution.semantic)
	}

	snapshot.axSnapshot.Revision = "c2"
	resolution, result = service.resolveActionElement(
		"editor", ref, "", "click",
	)
	if result.IsError {
		t.Fatalf("stale revision was rejected too early: %s", resultText(result))
	}
	if resolution.semantic == nil ||
		resolution.semantic.AllowProviderRebind {
		t.Fatalf("stale revision authorized provider rebind: %#v", resolution.semantic)
	}
}

func TestProviderBindingRefreshAcceptsAuthorizedSemanticReplacement(t *testing.T) {
	registry := NewRefRegistry(16)
	windowHandle := "w2:" + strings.Repeat("a", 32)
	ref := registry.AcquireTyped(
		AXLocatorSemantic,
		"editor",
		windowHandle,
		`button "Save"`,
		"button",
		"Save",
		"c1",
		":1.42", "/old/save", "99123",
		":1.42", "/window", "99123",
		"frame", "Editor", "a1", "w1",
	)
	registry.BindRuntimePaths(ref, []int{0, 0}, nil)
	if !registry.RefreshProviderBinding(ref, &elementRecord{
		ControlType:        "push button",
		Name:               "Save",
		RuntimeID:          []int{0, 0},
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/new/save",
		ProcessGeneration:  "99123",
	}, true) {
		t.Fatal("authorized semantic replacement did not refresh provider binding")
	}
	entry, err := registry.Resolve(ref, "")
	if err != nil {
		t.Fatal(err)
	}
	if entry.ProviderObjectPath != "/new/save" {
		t.Fatalf("provider binding=%q, want /new/save", entry.ProviderObjectPath)
	}
}

func TestProviderBindingRefreshRejectsUnauthorizedSemanticReplacement(t *testing.T) {
	registry := NewRefRegistry(16)
	windowHandle := "w2:" + strings.Repeat("a", 32)
	ref := registry.AcquireTyped(
		AXLocatorSemantic,
		"editor",
		windowHandle,
		`button "Save"`,
		"button",
		"Save",
		"c1",
		":1.42", "/old/save", "99123",
		":1.42", "/window", "99123",
		"frame", "Editor", "a1", "w1",
	)
	registry.BindRuntimePaths(ref, []int{0, 0}, nil)
	if registry.RefreshProviderBinding(ref, &elementRecord{
		ControlType:        "push button",
		Name:               "Save",
		RuntimeID:          []int{0, 0},
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/new/save",
		ProcessGeneration:  "99123",
	}, false) {
		t.Fatal("unauthorized semantic replacement refreshed provider binding")
	}
}

func TestTableOwnerBoundRefsKeepCellsDistinctAndCarryScope(t *testing.T) {
	service := newService()
	provider := []string{
		":1.42", "/sheet/table", "99123",
		":1.42", "/window/1", "99123",
		"frame", "Book", "a1", "w1", "table_owner",
	}
	c2 := service.refRegistry.AcquireTyped(
		AXLocatorProviderBound,
		"calc",
		"private-window",
		`cell "C2"`,
		"cell",
		"C2",
		"c1",
		provider...,
	)
	d2 := service.refRegistry.AcquireTyped(
		AXLocatorProviderBound,
		"calc",
		"private-window",
		`cell "D2"`,
		"cell",
		"D2",
		"c1",
		provider...,
	)
	if c2 == "" || d2 == "" || c2 == d2 {
		t.Fatalf("table-owned cell refs are not distinct: C2=%q D2=%q", c2, d2)
	}
	service.refRegistry.BindRuntimePaths(c2, []int{0, 4, 1, 2}, nil)
	resolution, result := service.resolveActionElement(
		"", c2, "", "click",
	)
	if result.IsError {
		t.Fatalf("table-owned ref rejected: %s", resultText(result))
	}
	if resolution.semantic == nil ||
		resolution.semantic.ProviderIdentityScope != "table_owner" ||
		!slices.Equal(resolution.semantic.RuntimeID, []int{0, 4, 1, 2}) {
		t.Fatalf("table owner identity was lost: %#v", resolution.semantic)
	}
}

func TestProviderBoundShortRefDoesNotRequireSemanticSelector(t *testing.T) {
	service := newService()
	ref := service.refRegistry.AcquireTyped(
		AXLocatorProviderBound,
		"thunderbird",
		"private-window",
		"",
		"row",
		"Inbox",
		"c1",
		":1.42", "/messages/inbox", "99123",
		":1.42", "/window/1", "99123",
		"frame", "Inbox", "a1", "w1",
	)
	if ref == "" {
		t.Fatal("provider-bound ref was not issued")
	}
	resolution, result := service.resolveActionElement(
		"thunderbird", ref, "", "click",
	)
	if result.IsError {
		t.Fatalf("provider-only ref rejected: %s", resultText(result))
	}
	if resolution.semantic == nil ||
		resolution.semantic.LocatorKind != AXLocatorProviderBound ||
		resolution.semantic.ProviderObjectPath != "/messages/inbox" ||
		resolution.semantic.Role != "row" ||
		resolution.semantic.Name != "Inbox" {
		t.Fatalf("provider identity was not reconstructed: %#v", resolution.semantic)
	}
}

func TestShortRefCarriesOriginatingWindowIntoLiveResolution(t *testing.T) {
	service := newService()
	service.refRegistry.Acquire(
		"editor", "w2:stable-window", `button "Save"`,
		"button", "Save", "c1",
		":1.42", "/org/a11y/atspi/accessible/77", "99123",
		":1.42", "/org/a11y/atspi/accessible/window/1", "99123",
		"dialog", "Preferences",
	)
	resolution, result := service.resolveActionElement(
		"editor", "r1", "", "click",
	)
	if result.IsError {
		t.Fatalf("short ref was rejected: %#v", result)
	}
	if resolution.semantic == nil {
		t.Fatal("short ref did not produce a live semantic target")
	}
	if got := resolution.semantic.WindowHandle; got != "w2:stable-window" {
		t.Fatalf("short ref lost originating window: %q", got)
	}
	if resolution.semantic.ProviderWindowRole != "dialog" ||
		resolution.semantic.ProviderWindowName != "Preferences" {
		t.Fatalf("short ref lost provider window semantics: %#v", resolution.semantic)
	}
}

func TestAssertionShortRefDoesNotDoubleEncodeScopedTarget(t *testing.T) {
	scoped := encodeScopedSemanticRef(`entry "Name"`, "w2:window")
	entry := &AXRefEntry{
		SemanticTarget: scoped,
		WindowHandle:   "w2:window",
	}
	if got := assertionTargetFromRefEntry(entry); got != scoped {
		t.Fatalf("scoped target was double encoded: %q", got)
	}
	raw := &AXRefEntry{
		SemanticTarget: `entry "Name"`,
		WindowHandle:   "w2:window",
	}
	got := assertionTargetFromRefEntry(raw)
	selector, window, err := decodeSemanticRefScope(got)
	if err != nil || selector != `entry "Name"` || window != "w2:window" {
		t.Fatalf(
			"raw selector was not scoped once: selector=%q window=%q err=%v",
			selector, window, err,
		)
	}
}

func TestObservationRootShortRefExpandsThroughRegistry(t *testing.T) {
	service := newService()
	ref := service.refRegistry.Acquire(
		"editor",
		"w2:stable-window",
		`group "Features"`,
		CanonicalRole("group"),
		"Features",
		"c1",
		":1.42", "/org/a11y/atspi/accessible/features", "99123",
		":1.42", "/org/a11y/atspi/accessible/window/1", "99123",
		"frame", "Editor",
	)
	expanded, err := service.expandObservationRootShortRef(
		"editor",
		map[string]any{"ref": ref},
	)
	if err != nil {
		t.Fatal(err)
	}
	target, ok := expanded["ref"].(string)
	if !ok || !isSemanticRef(target) {
		t.Fatalf("root ref did not expand to a scoped semantic ref: %#v", expanded)
	}
	selector, window, err := decodeSemanticRefScope(target)
	if err != nil {
		t.Fatal(err)
	}
	if selector != `group "Features"` || window != "w2:stable-window" {
		t.Fatalf(
			"unexpected expanded root: selector=%q window=%q",
			selector,
			window,
		)
	}
}

func TestObservationRootProviderBoundRefResolvesCanonicalRecord(t *testing.T) {
	service := newService()
	ref := service.refRegistry.AcquireTyped(
		AXLocatorProviderBound,
		"Editor",
		"pid7/win0/frame",
		"",
		CanonicalRole("paragraph"),
		"Details",
		"c1",
		":1.42",
		"/org/a11y/paragraph/1",
		"pid:7:start:10",
		"",
		"",
		"",
		"",
		"",
		"a1",
		"w1",
		"",
	)
	entry := service.refRegistry.entries[ref]
	if entry == nil {
		t.Fatal("provider-bound ref was not assigned")
	}
	service.refRegistry.BindRuntimePaths(entry.Ref, []int{0, 1}, nil)

	root, err := service.expandObservationRootShortRef(
		"Editor",
		map[string]any{"ref": entry.Ref},
	)
	if err != nil {
		t.Fatalf("expand provider-bound root: %v", err)
	}
	snapshot := &appSnapshot{
		WindowHandle: "pid7/win0/frame",
		Elements: []elementRecord{
			{
				Index: 7, RuntimeID: []int{0, 1},
				ControlType: "paragraph", Name: "Details",
				ProviderBusName:    ":1.42",
				ProviderObjectPath: "/org/a11y/paragraph/1",
				ProcessGeneration:  "pid:7:start:10",
			},
		},
	}
	record, mode, err := resolveObservationRoot(snapshot, root)
	if err != nil {
		t.Fatalf("resolve provider-bound root: %v", err)
	}
	if record == nil || record.Index != 7 || mode != "ref" {
		t.Fatalf("resolved record=%#v mode=%q", record, mode)
	}
}

func TestObservationRootProviderBoundRefDoesNotFallBackToSemanticText(t *testing.T) {
	service := newService()
	service.refRegistry.entries["r1"] = &AXRefEntry{
		Ref: "r1", LocatorKind: AXLocatorProviderBound,
		Lifecycle: AXRefActive, AppIdentity: "Editor",
		WindowHandle:       "pid7/win0/frame",
		SemanticTarget:     `button "Repeated"`,
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/org/a11y/button/1",
		ProcessGeneration:  "pid:7:start:10",
	}
	root, err := service.expandObservationRootShortRef(
		"Editor",
		map[string]any{"ref": "r1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if root["_resolved_ref_entry"] == nil || root["ref"] != nil {
		t.Fatalf("provider-bound root fell back to semantic selector: %#v", root)
	}
}

func TestObservationRootProviderBoundRefNarrowsSharedProviderIdentity(t *testing.T) {
	entry := &AXRefEntry{
		LocatorKind:        AXLocatorProviderBound,
		WindowHandle:       "pid7/win0/frame",
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/org/a11y/table/1",
		ProcessGeneration:  "pid:7:start:10",
		RuntimeID:          []int{0, 2},
	}
	snapshot := &appSnapshot{
		WindowHandle: "pid7/win0/frame",
		Elements: []elementRecord{
			{
				Index: 2, RuntimeID: []int{0, 1},
				ProviderBusName:    ":1.42",
				ProviderObjectPath: "/org/a11y/table/1",
				ProcessGeneration:  "pid:7:start:10",
			},
			{
				Index: 3, RuntimeID: []int{0, 2},
				ProviderBusName:    ":1.42",
				ProviderObjectPath: "/org/a11y/table/1",
				ProcessGeneration:  "pid:7:start:10",
			},
		},
	}
	record, err := resolveProviderBoundObservationRoot(snapshot, entry)
	if err != nil {
		t.Fatalf("resolve shared provider identity: %v", err)
	}
	if record.Index != 3 {
		t.Fatalf("resolved index=%d, want 3", record.Index)
	}
}

func TestObservationRootProviderBoundRefFailsClosed(t *testing.T) {
	entry := &AXRefEntry{
		LocatorKind:        AXLocatorProviderBound,
		WindowHandle:       "pid7/win0/frame",
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/org/a11y/table/1",
		ProcessGeneration:  "pid:7:start:10",
	}
	t.Run("missing", func(t *testing.T) {
		_, err := resolveProviderBoundObservationRoot(
			&appSnapshot{WindowHandle: "pid7/win0/frame"},
			entry,
		)
		if err == nil || !strings.Contains(err.Error(), "stale_element_ref") {
			t.Fatalf("missing provider root error=%v", err)
		}
	})
	t.Run("ambiguous", func(t *testing.T) {
		snapshot := &appSnapshot{
			WindowHandle: "pid7/win0/frame",
			Elements: []elementRecord{
				{
					Index: 2, RuntimeID: []int{0, 1},
					ProviderBusName:    ":1.42",
					ProviderObjectPath: "/org/a11y/table/1",
					ProcessGeneration:  "pid:7:start:10",
				},
				{
					Index: 3, RuntimeID: []int{0, 2},
					ProviderBusName:    ":1.42",
					ProviderObjectPath: "/org/a11y/table/1",
					ProcessGeneration:  "pid:7:start:10",
				},
			},
		}
		_, err := resolveProviderBoundObservationRoot(snapshot, entry)
		if err == nil || !strings.Contains(err.Error(), "ambiguous_target") {
			t.Fatalf("ambiguous provider root error=%v", err)
		}
	})
}

func TestAmbiguousFindCandidatesReceiveProviderBoundRefs(t *testing.T) {
	snapshot := &AXSnapshotV1{
		AppIdentity:  "Editor",
		AppRef:       "a1",
		WindowHandle: "pid7/win0/frame",
		WindowRef:    "w1",
		Revision:     "c1",
		Roots: []*AXNode{
			{
				ID: "left", Role: CanonicalRole("text"), Name: "Repeated",
				FindMatch: true, LocatorKind: AXLocatorSemantic,
				SemanticTarget:     `text "Repeated" [focused]`,
				ProviderBusName:    ":1.42",
				ProviderObjectPath: "/org/a11y/label/1",
				ProcessGeneration:  "pid:7:start:10",
				RuntimeID:          []int{0, 1},
			},
			{
				ID: "right", Role: CanonicalRole("text"), Name: "Repeated",
				FindMatch: true, LocatorKind: AXLocatorSemantic,
				SemanticTarget:     `text "Repeated"`,
				ProviderBusName:    ":1.42",
				ProviderObjectPath: "/org/a11y/label/2",
				ProcessGeneration:  "pid:7:start:10",
				RuntimeID:          []int{0, 2},
			},
		},
		ByID: map[string]*AXNode{},
	}
	for _, node := range snapshot.Roots {
		snapshot.ByID[node.ID] = node
	}
	registry := NewRefRegistry(32)
	registry.AssignSnapshotRefs(snapshot)
	for _, node := range snapshot.Roots {
		if node.Ref == "" {
			t.Fatalf("ambiguous find candidate %q received no ref", node.ID)
		}
		entry := registry.entries[node.Ref]
		if entry == nil || entry.LocatorKind != AXLocatorProviderBound {
			t.Fatalf(
				"candidate %q locator=%v, want provider_bound",
				node.ID,
				entry,
			)
		}
	}
	if snapshot.Roots[0].Ref == snapshot.Roots[1].Ref {
		t.Fatal("distinct provider objects shared one ref")
	}
}

func TestFindCandidateDoesNotKeepInheritedUnsafeRef(t *testing.T) {
	left := &AXNode{
		ID: "left", Role: CanonicalRole("text"), Name: "Repeated",
		FindMatch: true, LocatorKind: AXLocatorSemantic,
		SemanticTarget: `text "Repeated"`, Ref: "r98",
	}
	right := &AXNode{
		ID: "right", Role: CanonicalRole("text"), Name: "Repeated",
		FindMatch: true, LocatorKind: AXLocatorSemantic,
		SemanticTarget: `text "Repeated"`, Ref: "r99",
	}
	snapshot := &AXSnapshotV1{
		AppIdentity: "Editor", AppRef: "a1",
		WindowHandle: "pid7/win0/frame", WindowRef: "w1",
		Revision: "c1", Roots: []*AXNode{left, right},
		ByID: map[string]*AXNode{"left": left, "right": right},
	}
	NewRefRegistry(32).AssignSnapshotRefs(snapshot)
	if left.Ref != "" || right.Ref != "" {
		t.Fatalf(
			"unsafe inherited refs remained visible: left=%q right=%q",
			left.Ref,
			right.Ref,
		)
	}
}

func TestStabilizeFindRefsUsesRootResolverTruth(t *testing.T) {
	service := newService()
	left := &AXNode{
		ID: "left", Role: CanonicalRole("button"), Name: "Repeated",
		FindMatch: true, LocatorKind: AXLocatorSemantic,
		SemanticTarget: `button "Repeated"`, Ref: "r1",
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/org/a11y/button/1",
		ProcessGeneration:  "pid:7:start:10",
		RuntimeID:          []int{0, 1},
	}
	service.refRegistry.entries["r1"] = &AXRefEntry{
		Ref: "r1", LocatorKind: AXLocatorSemantic,
		SemanticTarget: `button "Repeated"`,
		WindowHandle:   "pid7/win0/frame",
		Lifecycle:      AXRefActive,
	}
	source := &appSnapshot{
		WindowHandle: "pid7/win0/frame",
		Elements: []elementRecord{
			{
				Index: 1, ControlType: "push button", Name: "Repeated",
				RuntimeID: []int{0, 1},
			},
			{
				Index: 2, ControlType: "push button", Name: "Repeated",
				RuntimeID: []int{0, 2},
			},
		},
	}
	findSnapshot := &AXSnapshotV1{
		AppIdentity: "Editor", AppRef: "a1",
		WindowHandle: "pid7/win0/frame", WindowRef: "w1",
		Revision: "c1", Roots: []*AXNode{left},
		ByID: map[string]*AXNode{"left": left},
	}
	service.stabilizeFindRefs(source, findSnapshot)
	if left.Ref == "" {
		t.Fatal("ambiguous semantic ref was not replaced")
	}
	entry := service.refRegistry.entries[left.Ref]
	if entry == nil || entry.LocatorKind != AXLocatorProviderBound {
		t.Fatalf("replacement entry=%#v, want provider_bound", entry)
	}
}

func TestReplacementWindowContinuationRequiresSameLineage(t *testing.T) {
	source := &AXRefEntry{
		WindowHandle:                    "w2:native-1",
		ProviderWindowBusName:           ":1.20",
		ProviderWindowProcessGeneration: "pid-100-start-5",
		ProviderWindowRole:              "frame",
	}
	same := &ProviderReplacementWindow{
		WindowHandle:       "w2:native-1",
		ProviderBusName:    ":1.20",
		ProcessGeneration:  "pid-100-start-5",
		Role:               "window",
		ProviderObjectPath: "/new/provider/path",
	}
	if !replacementWindowContinuesSource(source, same) {
		t.Fatal("same native window lineage should permit find-only continuation")
	}
	for name, changed := range map[string]*ProviderReplacementWindow{
		"native window": {
			WindowHandle:      "w2:native-2",
			ProviderBusName:   ":1.20",
			ProcessGeneration: "pid-100-start-5",
			Role:              "window",
		},
		"provider bus": {
			WindowHandle:      "w2:native-1",
			ProviderBusName:   ":1.21",
			ProcessGeneration: "pid-100-start-5",
			Role:              "window",
		},
		"process generation": {
			WindowHandle:      "w2:native-1",
			ProviderBusName:   ":1.20",
			ProcessGeneration: "pid-100-start-6",
			Role:              "window",
		},
		"role family": {
			WindowHandle:      "w2:native-1",
			ProviderBusName:   ":1.20",
			ProcessGeneration: "pid-100-start-5",
			Role:              "dialog",
		},
	} {
		if replacementWindowContinuesSource(source, changed) {
			t.Fatalf("%s change must not be advertised as ready continuation", name)
		}
	}
}

func TestPrepareAXEvidenceKeepsContinuationSeparated(t *testing.T) {
	result := toolCallResult{
		Content: []contentItem{
			{Type: "text", Text: "stale_element_ref"},
			{Type: "text", Text: "recovery detail"},
		},
		StructuredContent: map[string]any{
			"continuation": map[string]any{
				"state":              "ready",
				"retry_same_request": false,
				"next_mode":          "find",
				"fresh_window_ref":   "w3",
			},
		},
	}
	result = prepareAXEvidence(result)
	joined := resultText(result)
	if strings.Contains(joined, "w3recovery") {
		t.Fatalf("continuation and following text were concatenated: %q", joined)
	}
}

func TestPrepareAXEvidenceCannotExposeEmittedInputAsPreflight(t *testing.T) {
	result := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"preflight_only":       true,
			"mutation_unknown":     true,
			"input_events_emitted": true,
			"a11y_batch_result": map[string]any{
				"actions": []any{
					map[string]any{"input_emitted": true},
				},
			},
		},
	}
	result = prepareAXEvidence(result)
	if preflight, _ := result.StructuredContent["preflight_only"].(bool); preflight {
		t.Fatalf("post-emission evidence remained preflight-only: %#v", result.StructuredContent)
	}
	if scope := result.StructuredContent["evidence_scope"]; scope != "post_action_observation" {
		t.Fatalf("post-emission unknown evidence scope = %#v", scope)
	}
}

func TestFindCanIssueProviderBoundRefWithoutKnownCapability(t *testing.T) {
	showing := true
	node := &AXNode{
		ID: "0/1", Role: "cell", Name: "C2",
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/org/a11y/cell/2/3",
		ProcessGeneration:  "99123",
		Showing:            &showing,
		Visible:            &showing,
	}
	snapshot := &AXSnapshotV1{
		AppRef:       "a1",
		AppIdentity:  "calc",
		WindowHandle: "window-1",
		WindowRef:    "w1",
		Revision:     "c1",
		Roots:        []*AXNode{node},
		ByID:         map[string]*AXNode{"0/1": node},
	}
	registry := NewRefRegistry(16)
	registry.AssignSnapshotRefs(snapshot)
	if node.Ref != "" {
		t.Fatalf("passive default node received ref %q", node.Ref)
	}
	node.FindMatch = true
	registry.AssignSnapshotRefs(snapshot)
	if node.Ref == "" {
		t.Fatal("explicit find match with complete provider identity did not receive an on-demand ref")
	}
	entry, err := registry.Resolve(node.Ref, "")
	if err != nil {
		t.Fatal(err)
	}
	if entry.LocatorKind != AXLocatorProviderBound ||
		entry.AppRef != "a1" || entry.LogicalWindowRef != "w1" {
		t.Fatalf("find ref lost provider-bound ownership: %#v", entry)
	}
}

func TestA1LikeCellDoesNotReceiveOwnerlessTypedSemanticIdentity(t *testing.T) {
	snapshot := &appSnapshot{
		App:             appDescriptor{Name: "Calc"},
		WindowHandle:    "private-window",
		publicAppRef:    "a1",
		publicWindowRef: "w1",
		SnapshotID:      "c1",
		Elements: []elementRecord{
			{
				Index: 1, RuntimeID: []int{0}, ControlType: "frame",
				Name: "Book.ods - LibreOffice Calc",
			},
			{
				Index: 2, RuntimeID: []int{0, 0}, ControlType: "table cell",
				Name: "C2", ProviderBusName: ":1.42",
				ProviderObjectPath: "/org/a11y/cell/2/3",
				ProcessGeneration:  "99123",
			},
		},
	}
	typed, err := buildAXSnapshotV1(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	node := typed.ByIndex[2]
	if node == nil {
		t.Fatal("C2 node missing")
	}
	if node.LocatorKind != AXLocatorProviderBound {
		t.Fatalf(
			"ownerless C2 locator=%q, want provider_bound",
			node.LocatorKind,
		)
	}
	if node.SemanticTarget == "" {
		t.Fatal("provider-bound C2 lost its diagnostic semantic selector")
	}
}

func TestFindPrefersSemanticRefWhenProviderIdentityIsIncomplete(t *testing.T) {
	showing := true
	node := &AXNode{
		ID:             "0/1",
		Role:           "cell",
		Name:           "C2",
		FindMatch:      true,
		LocatorKind:    AXLocatorSemantic,
		SemanticTarget: encodeScopedSemanticRef(`cell "C2"`, "private-window"),
		Showing:        &showing,
		Visible:        &showing,
	}
	snapshot := &AXSnapshotV1{
		AppRef:       "a1",
		AppIdentity:  "calc",
		WindowHandle: "private-window",
		WindowRef:    "w1",
		Revision:     "c1",
		Roots:        []*AXNode{node},
		ByID:         map[string]*AXNode{"0/1": node},
	}
	registry := NewRefRegistry(16)
	registry.AssignSnapshotRefs(snapshot)
	if node.Ref == "" {
		t.Fatal("unique semantic find match did not receive a ref")
	}
	entry, err := registry.Resolve(node.Ref, "")
	if err != nil {
		t.Fatal(err)
	}
	if entry.LocatorKind != AXLocatorSemantic ||
		entry.AppRef != "a1" || entry.LogicalWindowRef != "w1" {
		t.Fatalf("semantic find ref lost ownership: %#v", entry)
	}
}

func TestSemanticRefSurvivesProviderObjectRebuildInSameLogicalWindow(t *testing.T) {
	target := encodeScopedSemanticRef(`cell "C2"`, "private-window")
	registry := NewRefRegistry(16)
	first := registry.AcquireTyped(
		AXLocatorSemantic,
		"calc",
		"private-window",
		target,
		"cell",
		"C2",
		"c1",
		":1.42",
		"/org/a11y/cell/old",
		"99123",
		"",
		"",
		"",
		"",
		"",
		"a1",
		"w1",
	)
	second := registry.AcquireTyped(
		AXLocatorSemantic,
		"calc",
		"private-window",
		target,
		"cell",
		"C2",
		"c2",
		":1.42",
		"/org/a11y/cell/rebuilt",
		"99123",
		"",
		"",
		"",
		"",
		"",
		"a1",
		"w1",
	)
	if first == "" || second != first {
		t.Fatalf("provider rebuild changed semantic ref: %q -> %q", first, second)
	}
	entry, err := registry.Resolve(first, "")
	if err != nil {
		t.Fatalf("semantic ref became stale: %v", err)
	}
	if entry.ProviderObjectPath != "/org/a11y/cell/old" {
		t.Fatalf(
			"unverified provider binding changed to %q",
			entry.ProviderObjectPath,
		)
	}
}

func TestSemanticRefKeepsLogicalIdentityUntilLiveRebind(t *testing.T) {
	target := encodeScopedSemanticRef(`button "OK"`, "private-window")
	registry := NewRefRegistry(16)
	first := registry.AcquireTyped(
		AXLocatorSemantic,
		"editor",
		"private-window",
		target,
		"button",
		"OK",
		"c1",
		":1.42",
		"/org/a11y/dialog/old-ok",
		"99123",
		"",
		"",
		"",
		"",
		"",
		"a1",
		"w1",
	)
	second := registry.AcquireTyped(
		AXLocatorSemantic,
		"editor",
		"private-window",
		target,
		"button",
		"OK",
		"c2",
		":1.42",
		"/org/a11y/dialog/new-ok",
		"99123",
		"",
		"",
		"",
		"",
		"",
		"a1",
		"w1",
	)
	if first == "" || second != first {
		t.Fatalf("same logical target changed ref: %q -> %q", first, second)
	}
	entry, err := registry.Resolve(first, "")
	if err != nil {
		t.Fatalf("semantic ref became stale: %v", err)
	}
	if entry.ProviderObjectPath != "/org/a11y/dialog/old-ok" {
		t.Fatalf(
			"unverified provider binding changed to %q",
			entry.ProviderObjectPath,
		)
	}
}

func TestSemanticRefReusesLogicalIdentityAcrossSamePathProviderRebuild(t *testing.T) {
	showing := true
	oldOwner := &AXNode{
		ID: "0", Role: "dialog", Name: "Question", RuntimeID: []int{0},
	}
	oldNode := &AXNode{
		ID: "0/0", Role: "button", Name: "OK",
		SemanticClass:      "control",
		LocatorKind:        AXLocatorSemantic,
		SemanticTarget:     encodeScopedSemanticRef(`button "OK"`, "window-1"),
		RuntimeID:          []int{0, 0},
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/org/a11y/old-ok",
		ProcessGeneration:  "99123",
		Showing:            &showing,
		Visible:            &showing,
		Parent:             oldOwner,
	}
	oldOwner.Children = []*AXNode{oldNode}
	firstSnapshot := &AXSnapshotV1{
		AppRef:       "a1",
		AppIdentity:  "editor",
		WindowHandle: "window-1",
		WindowRef:    "w1",
		Revision:     "c1",
		Roots:        []*AXNode{oldOwner},
	}
	registry := NewRefRegistry(16)
	first := registry.AcquireNode(
		firstSnapshot, oldNode, AXLocatorSemantic,
	)
	if first == "" {
		t.Fatal("initial semantic ref was not issued")
	}

	newOwner := &AXNode{
		ID: "0", Role: "dialog", Name: "Question", RuntimeID: []int{0},
	}
	newNode := &AXNode{
		ID: "0/0", Role: "button", Name: "OK",
		LocatorKind:        AXLocatorSemantic,
		SemanticTarget:     oldNode.SemanticTarget,
		RuntimeID:          []int{0, 0},
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/org/a11y/rebuilt-ok",
		ProcessGeneration:  "99123",
		Showing:            &showing,
		Visible:            &showing,
		Parent:             newOwner,
	}
	newOwner.Children = []*AXNode{newNode}
	secondSnapshot := &AXSnapshotV1{
		AppRef:       "a1",
		AppIdentity:  "editor",
		WindowHandle: "window-1",
		WindowRef:    "w1",
		Revision:     "c2",
		Roots:        []*AXNode{newOwner},
	}
	second := registry.AcquireNode(
		secondSnapshot, newNode, AXLocatorSemantic,
	)
	if second != first {
		t.Fatalf("same logical target changed ref: %q -> %q", first, second)
	}
	entry, err := registry.Resolve(first, "")
	if err != nil {
		t.Fatalf("semantic ref became stale: %v", err)
	}
	if entry.ProviderObjectPath != "/org/a11y/old-ok" {
		t.Fatalf(
			"unverified provider binding changed to %q",
			entry.ProviderObjectPath,
		)
	}
}

func TestCompleteSnapshotReplacesSemanticRefOutsideRuntimeLineage(t *testing.T) {
	showing := true
	owner := &AXNode{
		ID: "0", Role: "dialog", Name: "Question", RuntimeID: []int{0},
	}
	oldNode := &AXNode{
		ID: "0/0", Role: "button", Name: "OK",
		SemanticClass:      "control",
		LocatorKind:        AXLocatorSemantic,
		SemanticTarget:     encodeScopedSemanticRef(`button "OK"`, "window-1"),
		RuntimeID:          []int{0, 0},
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/org/a11y/old-ok",
		ProcessGeneration:  "99123",
		Showing:            &showing,
		Visible:            &showing,
		Parent:             owner,
	}
	owner.Children = []*AXNode{oldNode}
	registry := NewRefRegistry(16)
	first := registry.AcquireNode(&AXSnapshotV1{
		AppRef: "a1", AppIdentity: "editor",
		WindowHandle: "window-1", WindowRef: "w1", Revision: "c1",
		Roots: []*AXNode{owner},
	}, oldNode, AXLocatorSemantic)

	replacementOwner := &AXNode{
		ID: "0", Role: "dialog", Name: "Question", RuntimeID: []int{0},
	}
	replacement := &AXNode{
		ID: "0/1", Role: "button", Name: "OK",
		LocatorKind:        AXLocatorSemantic,
		SemanticTarget:     oldNode.SemanticTarget,
		RuntimeID:          []int{0, 1},
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/org/a11y/new-ok",
		ProcessGeneration:  "99123",
		Showing:            &showing,
		Visible:            &showing,
		Parent:             replacementOwner,
	}
	replacementOwner.Children = []*AXNode{replacement}
	second := registry.AcquireNode(&AXSnapshotV1{
		AppRef: "a1", AppIdentity: "editor",
		WindowHandle: "window-1", WindowRef: "w1", Revision: "c2",
		Roots: []*AXNode{replacementOwner},
	}, replacement, AXLocatorSemantic)
	if second == first || second == "" {
		t.Fatalf("replacement provider lineage reused stale ref: %q -> %q", first, second)
	}
	if _, err := registry.Resolve(first, ""); err == nil ||
		!strings.Contains(err.Error(), "superseded") {
		t.Fatalf("old ref was not superseded: %v", err)
	}
	entry, err := registry.Resolve(second, "")
	if err != nil {
		t.Fatalf("fresh replacement ref is not actionable: %v", err)
	}
	if !slices.Equal(entry.RuntimeID, []int{0, 1}) {
		t.Fatalf("fresh runtime path=%v, want [0 1]", entry.RuntimeID)
	}
}

func TestIncompleteSnapshotDoesNotReplaceSemanticRefOutsideRuntimeLineage(t *testing.T) {
	showing := true
	oldNode := &AXNode{
		ID: "0/0", Role: "button", Name: "OK",
		LocatorKind:        AXLocatorSemantic,
		SemanticTarget:     encodeScopedSemanticRef(`button "OK"`, "window-1"),
		RuntimeID:          []int{0, 0},
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/org/a11y/old-ok",
		ProcessGeneration:  "99123",
		Showing:            &showing,
		Visible:            &showing,
	}
	registry := NewRefRegistry(16)
	first := registry.AcquireNode(&AXSnapshotV1{
		AppRef: "a1", AppIdentity: "editor",
		WindowHandle: "window-1", WindowRef: "w1", Revision: "c1",
	}, oldNode, AXLocatorSemantic)
	replacement := &AXNode{
		ID: "0/1", Role: "button", Name: "OK",
		LocatorKind:        AXLocatorSemantic,
		SemanticTarget:     oldNode.SemanticTarget,
		RuntimeID:          []int{0, 1},
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/org/a11y/new-ok",
		ProcessGeneration:  "99123",
		Showing:            &showing,
		Visible:            &showing,
	}
	second := registry.AcquireNode(&AXSnapshotV1{
		AppRef: "a1", AppIdentity: "editor",
		WindowHandle: "window-1", WindowRef: "w1", Revision: "c2",
		AcquisitionIncomplete:      true,
		IdentityCoverageIncomplete: true,
	}, replacement, AXLocatorSemantic)
	if second == first || second == "" {
		t.Fatalf("partial snapshot reused semantic ref: %q -> %q", first, second)
	}
	entry, err := registry.Resolve(second, "")
	if err != nil {
		t.Fatalf("provider-bound replacement ref is not usable: %v", err)
	}
	if entry.LocatorKind != AXLocatorProviderBound {
		t.Fatalf("partial snapshot locator=%q, want provider_bound", entry.LocatorKind)
	}
	oldEntry, err := registry.Resolve(first, "")
	if err != nil {
		t.Fatalf("partial evidence invalidated old ref: %v", err)
	}
	if oldEntry.LastSeenRevision != "c1" {
		t.Fatalf("partial evidence refreshed old ref to %q", oldEntry.LastSeenRevision)
	}
}

func TestCompleteInventoryTombstonesAbsentSemanticRef(t *testing.T) {
	showing := true
	oldNode := &AXNode{
		ID: "0/0", Role: "button", Name: "OK",
		SemanticClass:      "control",
		LocatorKind:        AXLocatorSemantic,
		SemanticTarget:     encodeScopedSemanticRef(`button "OK"`, "window-1"),
		RuntimeID:          []int{0, 0},
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/org/a11y/old-ok",
		ProcessGeneration:  "99123",
		Showing:            &showing,
		Visible:            &showing,
	}
	registry := NewRefRegistry(16)
	firstSnapshot := &AXSnapshotV1{
		AppRef: "a1", AppIdentity: "editor",
		WindowHandle: "window-1", WindowRef: "w1", Revision: "c1",
		RefInventoryComplete: true,
		Roots:                []*AXNode{oldNode},
		ByID:                 map[string]*AXNode{"0/0": oldNode},
	}
	registry.AssignSnapshotRefs(firstSnapshot)
	first := oldNode.Ref
	if first == "" {
		t.Fatal("initial semantic ref was not issued")
	}

	registry.AssignSnapshotRefs(&AXSnapshotV1{
		AppRef: "a1", AppIdentity: "editor",
		WindowHandle: "window-1", WindowRef: "w1", Revision: "c2",
		RefInventoryComplete: true,
		Roots:                []*AXNode{},
		ByID:                 map[string]*AXNode{},
	})
	if _, err := registry.Resolve(first, ""); err == nil ||
		!strings.Contains(err.Error(), "superseded") {
		t.Fatalf("absent ref was not tombstoned: %v", err)
	}

	newNode := *oldNode
	newNode.Ref = ""
	newNode.ProviderObjectPath = "/org/a11y/later-ok"
	thirdSnapshot := &AXSnapshotV1{
		AppRef: "a1", AppIdentity: "editor",
		WindowHandle: "window-1", WindowRef: "w1", Revision: "c3",
		RefInventoryComplete: true,
		Roots:                []*AXNode{&newNode},
		ByID:                 map[string]*AXNode{"0/0": &newNode},
	}
	registry.AssignSnapshotRefs(thirdSnapshot)
	if newNode.Ref == "" || newNode.Ref == first {
		t.Fatalf("later same-name object revived old ref: %q -> %q",
			first, newNode.Ref)
	}
}

func TestSemanticProviderRefreshRejectsDifferentRuntimeLineage(t *testing.T) {
	registry := NewRefRegistry(16)
	target := encodeScopedSemanticRef(`button "OK"`, "window-1")
	ref := registry.AcquireTyped(
		AXLocatorSemantic,
		"editor",
		"window-1",
		target,
		"button",
		"OK",
		"c1",
		":1.42",
		"/org/a11y/old-ok",
		"99123",
		"",
		"",
		"",
		"",
		"",
		"a1",
		"w1",
	)
	registry.BindRuntimePaths(ref, []int{0, 0}, [][]int{{0, 0}})
	record := &elementRecord{
		ControlType:        "push button",
		Name:               "OK",
		RuntimeID:          []int{0, 1},
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/org/a11y/new-ok",
		ProcessGeneration:  "99123",
	}
	if registry.RefreshProviderBinding(ref, record, true) {
		t.Fatal("semantic ref rebound outside its original runtime lineage")
	}
}

func TestFindCursorPagesAreBoundedAndStable(t *testing.T) {
	registry := NewFindCursorRegistry(4)
	snapshot := &appSnapshot{
		WindowTitle: "Results",
		axSnapshot: &AXSnapshotV1{
			SchemaVersion: "AXSnapshotV1",
			AppIdentity:   "app",
			WindowHandle:  "window",
			Revision:      "c1",
		},
	}
	matches := []elementRecord{{Index: 1}, {Index: 2}, {Index: 3}}
	cursor := registry.Create(snapshot, matches, 1, `role="button"`)
	if cursor != "f1" {
		t.Fatalf("cursor=%q want f1", cursor)
	}
	entry, err := registry.Resolve(cursor)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Cursor.Offset != 1 ||
		entry.ResultSet == nil ||
		len(entry.ResultSet.Matches) != 3 ||
		entry.ResultSet.Snapshot == snapshot ||
		entry.ResultSet.Snapshot.WindowTitle != snapshot.WindowTitle {
		t.Fatalf("cursor lost bound observation: %#v", entry)
	}
	nextCursor := registry.Continue(entry.Cursor.ResultSetID, 2)
	nextEntry, err := registry.Resolve(nextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if nextEntry.Cursor.ResultSetID != entry.Cursor.ResultSetID ||
		nextEntry.ResultSet != entry.ResultSet {
		t.Fatal("continued cursor did not share the immutable result set")
	}
	registry.Reset()
	if _, err := registry.Resolve(cursor); err == nil ||
		!strings.Contains(err.Error(), "expired") {
		t.Fatalf("reset cursor did not expire: %v", err)
	}
}

func TestFindCursorDoesNotReuseFreshProviderResultsAndExpiresByTTL(t *testing.T) {
	registry := NewFindCursorRegistry(4)
	registry.idleTTL = time.Millisecond
	snapshot := &appSnapshot{
		SnapshotID:   "snapshot-1",
		WindowHandle: "window-1",
		axSnapshot: &AXSnapshotV1{
			SchemaVersion: "AXSnapshotV1",
			WindowHandle:  "window-1",
			Revision:      "c1",
		},
	}
	matches := []elementRecord{{Index: 1}, {Index: 2}}
	first := registry.Create(snapshot, matches, 1, `role="button"`)
	second := registry.Create(snapshot, matches, 1, `role="button"`)
	firstResolution, err := registry.Resolve(first)
	if err != nil {
		t.Fatal(err)
	}
	secondResolution, err := registry.Resolve(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstResolution.Cursor.ResultSetID ==
		secondResolution.Cursor.ResultSetID ||
		len(registry.resultSets) != 2 {
		t.Fatal("fresh find incorrectly reused provider-backed result data")
	}
	registry.resultSets[firstResolution.Cursor.ResultSetID].LastUsedAt =
		time.Now().Add(-time.Second)
	if _, err := registry.Resolve(first); err == nil ||
		!strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired cursor remained usable: %v", err)
	}
}

func TestFindCursorExpiresAtAbsoluteDeadlineDespiteContinuedUse(t *testing.T) {
	registry := NewFindCursorRegistry(4)
	registry.idleTTL = time.Hour
	registry.absoluteTTL = time.Millisecond
	snapshot := &appSnapshot{
		axSnapshot: &AXSnapshotV1{SchemaVersion: "AXSnapshotV1"},
	}
	cursor := registry.Create(
		snapshot,
		[]elementRecord{{Index: 1}, {Index: 2}},
		1,
		`role="button"`,
	)
	resolution, err := registry.Resolve(cursor)
	if err != nil {
		t.Fatal(err)
	}
	resolution.ResultSet.CreatedAt = time.Now().Add(-time.Second)
	resolution.ResultSet.LastUsedAt = time.Now()
	if _, err := registry.Resolve(cursor); err == nil ||
		!strings.Contains(err.Error(), "expired") {
		t.Fatalf("absolute TTL did not expire cursor: %v", err)
	}
}

func TestFindCursorRejectsOversizedResultSet(t *testing.T) {
	registry := NewFindCursorRegistry(4)
	registry.nodeBudget = 1
	root := &AXNode{ID: "0", Name: "root"}
	root.Children = []*AXNode{{ID: "0/0", Name: "child", Parent: root}}
	snapshot := &appSnapshot{
		SnapshotID: "snapshot-1",
		axSnapshot: &AXSnapshotV1{CanonicalRoots: []*AXNode{root}},
	}
	if cursor := registry.Create(
		snapshot,
		[]elementRecord{{Index: 1}, {Index: 2}},
		1,
		`role="button"`,
	); cursor != "" {
		t.Fatalf("oversized result set received cursor %q", cursor)
	}
}

func TestAssertAXRefUsesFullLiveTargetIdentity(t *testing.T) {
	service := newService()
	target := &semanticTarget{
		Selector:            `button "Save"`,
		LocatorKind:         AXLocatorProviderBound,
		WindowHandle:        "private-window",
		ProviderBusName:     ":1.42",
		ProviderObjectPath:  "/org/a11y/save",
		ProcessGeneration:   "99123",
		Role:                "button",
		Name:                "Save",
		HasName:             true,
		ProviderRoleAliases: []string{"button", "push button"},
	}
	exists := true
	result := service.verifyResolvedAXRefWithProbe(
		"editor",
		"r17",
		target,
		verifyGoal{exists: &exists},
		1,
		0,
		func(request ProviderActionRequest) (*ProviderAccessibilityResult, error) {
			if request.Intent != AccessibilityResolveTarget ||
				request.WindowHandle != "private-window" ||
				request.Target.ProviderObjectPath != "/org/a11y/save" ||
				request.Target.LocatorKind != AXLocatorProviderBound {
				t.Fatalf("assert_ax degraded ref identity: %#v", request)
			}
			return &ProviderAccessibilityResult{
				OK: true,
				ResolvedElement: &elementRecord{
					Index:       7,
					ControlType: "push button",
					Name:        "Save",
				},
			}, nil
		},
	)
	if result.IsError ||
		result.StructuredContent["assertion_status"] != "pass" {
		t.Fatalf("live ref assertion did not pass: %#v", result)
	}
}

func TestAssertAXDoesNotTreatIncompleteAbsenceAsPass(t *testing.T) {
	service := newService()
	target := &semanticTarget{
		Selector:     `button "Save"`,
		LocatorKind:  AXLocatorSemantic,
		WindowHandle: "private-window",
		Role:         "button",
		Name:         "Save",
		HasName:      true,
	}
	exists := false
	result := service.verifyResolvedAXRefWithProbe(
		"editor",
		"r17",
		target,
		verifyGoal{exists: &exists},
		1,
		0,
		func(ProviderActionRequest) (*ProviderAccessibilityResult, error) {
			return &ProviderAccessibilityResult{
				OK:    false,
				Error: "AX bus read failed",
				Failure: &ProviderFailure{
					Code:          "observation_incomplete",
					FactsComplete: false,
				},
			}, nil
		},
	)
	if !result.IsError ||
		result.StructuredContent["assertion_status"] !=
			"observation_incomplete" {
		t.Fatalf("incomplete absence was treated as predicate truth: %#v", result)
	}
}

func TestAssertAXFallsBackToExactCanonicalSnapshot(t *testing.T) {
	service := newService()
	target := &semanticTarget{
		Selector:           `checkbox "Automatic quotes"`,
		LocatorKind:        AXLocatorProviderBound,
		WindowHandle:       "private-window",
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/org/a11y/quote",
		ProcessGeneration:  "99123",
		Role:               "checkbox",
		Name:               "Automatic quotes",
		HasName:            true,
	}
	record := &elementRecord{
		Index:              7,
		ControlType:        "check box",
		Name:               "Automatic quotes",
		States:             "[active]",
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/org/a11y/quote",
		ProcessGeneration:  "99123",
	}
	snapshot := &appSnapshot{
		WindowHandle: "private-window",
		Elements:     []elementRecord{*record},
	}
	result := service.verifyResolvedAXRefWithProbes(
		"editor",
		"r17",
		target,
		verifyGoal{state: "!checked"},
		1000,
		0,
		func(ProviderActionRequest) (*ProviderAccessibilityResult, error) {
			return &ProviderAccessibilityResult{
				OK:    false,
				Error: "AX resolver read was incomplete",
				Failure: &ProviderFailure{
					Code:          "observation_incomplete",
					FactsComplete: false,
				},
			}, nil
		},
		func() (*axAssertionSnapshotProof, toolCallResult) {
			return &axAssertionSnapshotProof{
				snapshot: snapshot,
				notes:    []string{"canonical fallback"},
				record:   record,
			}, toolCallResult{}
		},
	)
	if result.IsError ||
		result.StructuredContent["assertion_status"] != "pass" ||
		result.StructuredContent["assertion_canonical_fallback"] != true {
		t.Fatalf("exact canonical fallback did not prove the ref state: %#v", result)
	}
	if result.axProofSnapshot != snapshot ||
		len(result.axProofNotes) != 1 {
		t.Fatalf("canonical proof snapshot was not retained: %#v", result)
	}
}

func TestCanonicalAssertionFallbackRequiresTheSameIssuedRefIdentity(t *testing.T) {
	service := newService()
	showing := true
	record := &elementRecord{
		Index:              7,
		ControlType:        "check box",
		Name:               "Automatic quotes",
		States:             "[active]",
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/org/a11y/quote",
		ProcessGeneration:  "99123",
		Showing:            &showing,
		Visible:            &showing,
	}
	node := &AXNode{
		ID:                 "0/0",
		Role:               "checkbox",
		SemanticClass:      "control",
		Name:               record.Name,
		ProviderBusName:    record.ProviderBusName,
		ProviderObjectPath: record.ProviderObjectPath,
		ProcessGeneration:  record.ProcessGeneration,
		LocatorKind:        AXLocatorProviderBound,
		SourceRecord:       record,
		Showing:            &showing,
		Visible:            &showing,
	}
	tree := &AXSnapshotV1{
		AppRef:               "a1",
		AppIdentity:          "editor",
		WindowHandle:         "private-window",
		WindowRef:            "w1",
		Revision:             "c1",
		RefInventoryComplete: true,
		CanonicalRoots:       []*AXNode{node},
		Roots:                []*AXNode{node},
		ByID:                 map[string]*AXNode{"0/0": node},
	}
	service.refRegistry.AssignSnapshotRefs(tree)
	if node.Ref == "" {
		t.Fatal("fixture did not receive a ref")
	}
	entry, err := service.refRegistry.Resolve(node.Ref, "")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := &appSnapshot{
		App:             appDescriptor{Name: "editor"},
		WindowHandle:    "private-window",
		Elements:        []elementRecord{*record},
		publicAppRef:    "a1",
		publicWindowRef: "w1",
		axSnapshot:      tree,
	}
	resolved, err := service.exactRefRecordFromSnapshot(
		snapshot,
		node.Ref,
		entry,
	)
	if err != nil || resolved != record {
		t.Fatalf("exact ref identity was not accepted: record=%#v err=%v", resolved, err)
	}

	tests := []struct {
		name    string
		mutate  func()
		restore func()
	}{
		{
			name:    "different ref",
			mutate:  func() { node.Ref = "r999" },
			restore: func() { node.Ref = entry.Ref },
		},
		{
			name:    "different app ref",
			mutate:  func() { snapshot.publicAppRef = "a999" },
			restore: func() { snapshot.publicAppRef = entry.AppRef },
		},
		{
			name:    "different logical window",
			mutate:  func() { snapshot.publicWindowRef = "w999" },
			restore: func() { snapshot.publicWindowRef = entry.LogicalWindowRef },
		},
		{
			name:    "different native window",
			mutate:  func() { snapshot.WindowHandle = "other-window" },
			restore: func() { snapshot.WindowHandle = entry.WindowHandle },
		},
		{
			name:    "different provider bus",
			mutate:  func() { record.ProviderBusName = ":1.999" },
			restore: func() { record.ProviderBusName = entry.ProviderBusName },
		},
		{
			name: "different provider object",
			mutate: func() {
				record.ProviderObjectPath = "/org/a11y/other"
			},
			restore: func() {
				record.ProviderObjectPath = entry.ProviderObjectPath
			},
		},
		{
			name: "different process generation",
			mutate: func() {
				record.ProcessGeneration = "99999"
			},
			restore: func() {
				record.ProcessGeneration = entry.ProcessGeneration
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.mutate()
			defer test.restore()
			if _, err := service.exactRefRecordFromSnapshot(
				snapshot,
				entry.Ref,
				entry,
			); err == nil {
				t.Fatalf("%s was accepted", test.name)
			}
		})
	}

	t.Run("different app identity", func(t *testing.T) {
		changed := *entry
		changed.AppIdentity = "other-editor"
		if _, err := service.exactRefRecordFromSnapshot(
			snapshot,
			entry.Ref,
			&changed,
		); err == nil {
			t.Fatal("different app identity was accepted")
		}
	})
}

func TestCanonicalAssertionFallbackAcceptsSemanticProviderWrapperChurn(t *testing.T) {
	service := newService()
	showing := true
	record := &elementRecord{
		Index:                 7,
		RuntimeID:             []int{0, 4, 7},
		ControlType:           "check box",
		Name:                  "Automatic quotes",
		States:                "[active]",
		ProviderBusName:       ":1.42",
		ProviderObjectPath:    "/org/a11y/quote/old",
		ProcessGeneration:     "99123",
		ProviderIdentityScope: "provider-wrapper-old",
		Showing:               &showing,
		Visible:               &showing,
	}
	node := &AXNode{
		ID:                 "0/4/7",
		Role:               "checkbox",
		SemanticClass:      "control",
		Name:               record.Name,
		RuntimeID:          append([]int(nil), record.RuntimeID...),
		ProviderBusName:    record.ProviderBusName,
		ProviderObjectPath: record.ProviderObjectPath,
		ProcessGeneration:  record.ProcessGeneration,
		SemanticTarget:     `checkbox "Automatic quotes"`,
		LocatorKind:        AXLocatorSemantic,
		SourceRecord:       record,
		Showing:            &showing,
		Visible:            &showing,
	}
	tree := &AXSnapshotV1{
		AppRef:               "a1",
		AppIdentity:          "editor",
		WindowHandle:         "private-window",
		WindowRef:            "w1",
		Revision:             "c1",
		RefInventoryComplete: true,
		CanonicalRoots:       []*AXNode{node},
		Roots:                []*AXNode{node},
		ByID:                 map[string]*AXNode{"0/4/7": node},
	}
	service.refRegistry.AssignSnapshotRefs(tree)
	entry, err := service.refRegistry.Resolve(node.Ref, "")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := &appSnapshot{
		App:             appDescriptor{Name: "editor"},
		WindowHandle:    "private-window",
		Elements:        []elementRecord{*record},
		publicAppRef:    "a1",
		publicWindowRef: "w1",
		axSnapshot:      tree,
	}

	record.ProviderObjectPath = "/org/a11y/quote/rebuilt"
	record.ProviderIdentityScope = "provider-wrapper-rebuilt"
	node.ProviderObjectPath = record.ProviderObjectPath
	node.ProviderIdentityScope = record.ProviderIdentityScope
	if resolved, err := service.exactRefRecordFromSnapshot(
		snapshot,
		node.Ref,
		entry,
	); err != nil || resolved != record {
		t.Fatalf(
			"semantic ref rejected safe provider-wrapper churn: record=%#v err=%v",
			resolved,
			err,
		)
	}

	record.RuntimeID = []int{0, 9, 9}
	node.RuntimeID = append([]int(nil), record.RuntimeID...)
	if _, err := service.exactRefRecordFromSnapshot(
		snapshot,
		node.Ref,
		entry,
	); err == nil {
		t.Fatal("semantic ref accepted a different runtime lineage")
	}
}

func TestCanonicalAssertionFallbackAcceptsRegistryProviderRebind(t *testing.T) {
	service := newService()
	showing := true
	record := &elementRecord{
		Index:              7,
		RuntimeID:          []int{0, 4, 7},
		ControlType:        "check box",
		Name:               "Automatic quotes",
		States:             "[checked]",
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/org/a11y/quote/old",
		ProcessGeneration:  "99123",
		Showing:            &showing,
		Visible:            &showing,
	}
	node := &AXNode{
		ID:                 "0/4/7",
		Role:               "checkbox",
		SemanticClass:      "control",
		Name:               record.Name,
		RuntimeID:          append([]int(nil), record.RuntimeID...),
		ProviderBusName:    record.ProviderBusName,
		ProviderObjectPath: record.ProviderObjectPath,
		ProcessGeneration:  record.ProcessGeneration,
		SemanticTarget:     `checkbox "Automatic quotes"`,
		LocatorKind:        AXLocatorSemantic,
		SourceRecord:       record,
		Showing:            &showing,
		Visible:            &showing,
	}
	tree := &AXSnapshotV1{
		AppRef:               "a1",
		AppIdentity:          "editor",
		WindowHandle:         "private-window",
		WindowRef:            "w1",
		Revision:             "c1",
		RefInventoryComplete: true,
		CanonicalRoots:       []*AXNode{node},
		Roots:                []*AXNode{node},
		ByID:                 map[string]*AXNode{"0/4/7": node},
	}
	service.refRegistry.AssignSnapshotRefs(tree)
	current, err := service.refRegistry.Resolve(node.Ref, "")
	if err != nil {
		t.Fatal(err)
	}
	expected := *current
	record.ProviderObjectPath = "/org/a11y/quote/rebuilt"
	node.ProviderObjectPath = record.ProviderObjectPath
	if !service.refRegistry.RefreshProviderBinding(node.Ref, record, true) {
		t.Fatal("semantic provider rebind was rejected")
	}
	snapshot := &appSnapshot{
		App:             appDescriptor{Name: "editor"},
		WindowHandle:    "private-window",
		Elements:        []elementRecord{*record},
		publicAppRef:    "a1",
		publicWindowRef: "w1",
		axSnapshot:      tree,
	}
	if resolved, err := service.exactRefRecordFromSnapshot(
		snapshot,
		node.Ref,
		&expected,
	); err != nil || resolved != record {
		t.Fatalf("registry provider rebind broke semantic proof: record=%#v err=%v",
			resolved, err)
	}
}

func TestFinalObservationCompletesZeroStablePostcondition(t *testing.T) {
	service := newService()
	showing := true
	record := &elementRecord{
		Index:              7,
		RuntimeID:          []int{0, 4, 7},
		ControlType:        "check box",
		Name:               "Automatic quotes",
		States:             "[checked]",
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/org/a11y/quote/rebuilt",
		ProcessGeneration:  "99123",
		Showing:            &showing,
		Visible:            &showing,
	}
	node := &AXNode{
		ID:                 "0/4/7",
		Role:               "checkbox",
		SemanticClass:      "control",
		Name:               record.Name,
		RuntimeID:          append([]int(nil), record.RuntimeID...),
		ProviderBusName:    record.ProviderBusName,
		ProviderObjectPath: record.ProviderObjectPath,
		ProcessGeneration:  record.ProcessGeneration,
		SemanticTarget:     `checkbox "Automatic quotes"`,
		LocatorKind:        AXLocatorSemantic,
		SourceRecord:       record,
		Showing:            &showing,
		Visible:            &showing,
	}
	workingNode := *node
	tree := &AXSnapshotV1{
		AppRef:               "a1",
		AppIdentity:          "editor",
		WindowHandle:         "private-window",
		WindowRef:            "w1",
		Revision:             "c2",
		RefInventoryComplete: true,
		CanonicalRoots:       []*AXNode{node},
		Roots:                []*AXNode{&workingNode},
		ByID:                 map[string]*AXNode{"0/4/7": node},
	}
	service.refRegistry.AssignSnapshotRefs(tree)
	if node.Ref == "" || node.LocatorKind != AXLocatorSemantic {
		t.Fatalf("canonical ref metadata was not synchronized: %#v", node)
	}
	snapshot := &appSnapshot{
		App:             appDescriptor{Name: "editor"},
		WindowHandle:    "private-window",
		Elements:        []elementRecord{*record},
		publicAppRef:    "a1",
		publicWindowRef: "w1",
		axSnapshot:      tree,
	}
	assertion := map[string]any{
		"type":      "state",
		"target":    map[string]any{"ref": node.Ref},
		"state":     "checked",
		"value":     true,
		"stable_ms": float64(0),
	}
	incomplete := a11yAssertionResult(
		"assert_ax observation_incomplete: live AX target incomplete",
		true,
		"observation_incomplete",
		1,
		0,
	)
	spec, err := parseA11yAssertionSpec(assertion)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := service.refRegistry.Resolve(node.Ref, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.exactRefRecordFromSnapshot(
		snapshot,
		spec.targetAddress,
		entry,
	); err != nil {
		t.Fatalf("test fixture cannot prove exact ref: %v entry=%#v", err, entry)
	}
	finalObservation := toolCallResult{
		StructuredContent: map[string]any{
			"final_observation_returned": true,
		},
		axProofSnapshot: snapshot,
	}
	recovered := service.reconcileA11yPostconditionFromFinalObservation(
		assertion,
		incomplete,
		finalObservation,
	)
	if recovered.IsError ||
		recovered.StructuredContent["assertion_status"] != "pass" ||
		recovered.StructuredContent["postcondition_recovered_by_final_observation"] != true ||
		recovered.StructuredContent["postcondition_reconciled_by_canonical_observation"] != true ||
		recovered.StructuredContent["cause_unattributed"] != true {
		t.Fatalf("fresh final observation did not complete postcondition: %#v",
			recovered)
	}

	record.States = "[enabled]"
	reconciledFalse := service.reconcileA11yPostconditionFromFinalObservation(
		assertion,
		incomplete,
		finalObservation,
	)
	if reconciledFalse.IsError ||
		reconciledFalse.StructuredContent["assertion_status"] != "predicate_false" ||
		reconciledFalse.StructuredContent["postcondition_reconciled_by_canonical_observation"] != true {
		t.Fatalf("fresh final observation did not resolve false predicate: %#v",
			reconciledFalse)
	}

	expected, err := service.refRegistry.Resolve(node.Ref, "")
	if err != nil {
		t.Fatal(err)
	}
	absentTree := *tree
	absentTree.CanonicalRoots = nil
	absentTree.Roots = nil
	absentTree.ByID = map[string]*AXNode{}
	absentTree.RefInventoryComplete = true
	absentSnapshot := *snapshot
	absentSnapshot.Elements = nil
	absentSnapshot.axSnapshot = &absentTree
	for _, expectedExists := range []bool{false, true} {
		existsAssertion := map[string]any{
			"type":   "exists",
			"target": map[string]any{"ref": node.Ref},
			"value":  expectedExists,
		}
		got := service.reconcileA11yPostconditionFromCanonicalSnapshot(
			existsAssertion,
			incomplete,
			&absentSnapshot,
			nil,
			expected,
		)
		wantStatus := "pass"
		if expectedExists {
			wantStatus = "predicate_false"
		}
		if got.IsError ||
			got.StructuredContent["assertion_status"] != wantStatus {
			t.Fatalf("complete exact-ref absence with exists=%t: %#v",
				expectedExists, got)
		}
	}

	assertion["stable_ms"] = float64(100)
	unchanged := service.reconcileA11yPostconditionFromFinalObservation(
		assertion,
		incomplete,
		finalObservation,
	)
	if !unchanged.IsError {
		t.Fatalf("final observation bypassed stable_ms: %#v", unchanged)
	}

	expectedIdentity := *expected
	service.refRegistry.MarkStale(node.Ref, "test replacement")
	staleResult := service.reconcileA11yPostconditionFromCanonicalSnapshot(
		map[string]any{
			"type":   "exists",
			"target": map[string]any{"ref": node.Ref},
			"value":  false,
		},
		incomplete,
		&absentSnapshot,
		nil,
		&expectedIdentity,
	)
	if !staleResult.IsError ||
		staleResult.StructuredContent["postcondition_reconciled_by_canonical_observation"] == true {
		t.Fatalf("stale identity was incorrectly treated as proven absence: %#v",
			staleResult)
	}
}

func TestActionPostconditionReconciliationSamplesAfterPolling(t *testing.T) {
	service, appRef, windowRef, _ := candidateABatchService(t)
	handle := "w2:0123456789abcdef0123456789abcdef"
	snapshot := &appSnapshot{
		App: appDescriptor{
			Name:              "LibreOffice",
			PID:               10,
			ProviderBusName:   ":1.84",
			ProcessGeneration: "pid:10:start:100",
		},
		WindowTitle:  "Sheet",
		WindowHandle: handle,
		SnapshotID:   "c2",
		Elements: []elementRecord{
			{
				Index:                           0,
				RuntimeID:                       []int{0},
				ControlType:                     "frame",
				Name:                            "Sheet",
				States:                          "[active showing visible]",
				ProviderBusName:                 ":1.84",
				ProviderObjectPath:              "/window/1",
				ProcessGeneration:               "pid:10:start:100",
				ProviderWindowBusName:           ":1.84",
				ProviderWindowObjectPath:        "/window/1",
				ProviderWindowProcessGeneration: "pid:10:start:100",
				ProviderWindowRole:              "frame",
				ProviderWindowName:              "Sheet",
			},
			{
				Index:                           1,
				RuntimeID:                       []int{0, 0},
				ControlType:                     "button",
				Name:                            "Save",
				States:                          "[enabled showing visible]",
				Actions:                         []string{"click"},
				ProviderBusName:                 ":1.84",
				ProviderObjectPath:              "/window/1/save",
				ProcessGeneration:               "pid:10:start:100",
				ProviderWindowBusName:           ":1.84",
				ProviderWindowObjectPath:        "/window/1",
				ProviderWindowProcessGeneration: "pid:10:start:100",
				ProviderWindowRole:              "frame",
				ProviderWindowName:              "Sheet",
			},
		},
		scope: a11yScopeWindow,
	}
	backend := &recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{
			OK:       true,
			Snapshot: snapshot,
		},
	}
	service.backend = backend
	noScreenshot := false
	initial, _, initialResult := service.refreshSnapshot(
		appRef,
		axServiceRequest{
			Operation:          axServiceObserve,
			App:                appRef,
			IncludeScreenshot:  &noScreenshot,
			TargetWindowHandle: handle,
			SourceRef:          windowRef,
			Scope:              a11yScopeWindow,
			TreeView:           treeViewWorking,
		},
	)
	if initialResult.IsError || initial == nil ||
		initial.axSnapshot == nil ||
		len(initial.axSnapshot.CanonicalRoots) != 1 ||
		len(initial.axSnapshot.CanonicalRoots[0].Children) != 1 {
		t.Fatalf("initial canonical observation failed: %#v snapshot=%#v",
			initialResult, initial)
	}
	elementRef := initial.axSnapshot.CanonicalRoots[0].Children[0].Ref
	if elementRef == "" {
		t.Fatal("initial canonical observation did not issue an element ref")
	}
	snapshot.Elements[1].States = "[checked enabled showing visible]"
	backend.observeRequests = nil
	incomplete := a11yAssertionResult(
		"assert_ax observation_incomplete: live AX target incomplete",
		true,
		"observation_incomplete",
		1,
		0,
	)
	reconciled := service.reconcileA11yActionPostcondition(
		&a11yBatchContext{
			appRef:       appRef,
			appToken:     appRef,
			windowRef:    windowRef,
			windowHandle: handle,
		},
		map[string]any{
			"type":   "state",
			"target": map[string]any{"ref": elementRef},
			"state":  "checked",
			"value":  true,
		},
		incomplete,
	)
	if reconciled.IsError ||
		reconciled.StructuredContent["assertion_status"] != "pass" ||
		reconciled.StructuredContent["postcondition_reconciled_by_canonical_observation"] != true ||
		reconciled.StructuredContent["postcondition_recovered_by_final_observation"] == true {
		t.Fatalf("action guard did not reconcile from a fresh post-poll snapshot: %#v",
			reconciled)
	}
	if len(backend.observeRequests) != 1 {
		t.Fatalf("canonical reconciliation observe calls=%d, want 1",
			len(backend.observeRequests))
	}
}

func TestAssertAXCanonicalFallbackDoesNotBypassStability(t *testing.T) {
	service := newService()
	target := &semanticTarget{
		Selector:           `checkbox "Automatic quotes"`,
		LocatorKind:        AXLocatorProviderBound,
		WindowHandle:       "private-window",
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/org/a11y/quote",
		ProcessGeneration:  "99123",
		Role:               "checkbox",
		Name:               "Automatic quotes",
		HasName:            true,
	}
	record := &elementRecord{
		Index:              7,
		ControlType:        "check box",
		Name:               "Automatic quotes",
		States:             "[active]",
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/org/a11y/quote",
		ProcessGeneration:  "99123",
	}
	fallbackCalls := 0
	result := service.verifyResolvedAXRefWithProbes(
		"editor",
		"r17",
		target,
		verifyGoal{state: "!checked"},
		10,
		1,
		func(ProviderActionRequest) (*ProviderAccessibilityResult, error) {
			return &ProviderAccessibilityResult{
				OK:    false,
				Error: "AX resolver read was incomplete",
				Failure: &ProviderFailure{
					Code:          "observation_incomplete",
					FactsComplete: false,
				},
			}, nil
		},
		func() (*axAssertionSnapshotProof, toolCallResult) {
			fallbackCalls++
			if fallbackCalls == 1 {
				return &axAssertionSnapshotProof{
					snapshot: &appSnapshot{WindowHandle: "private-window"},
					record:   record,
				}, toolCallResult{}
			}
			return nil, textResult("fallback unavailable", true).
				withEvidenceScope("error_only")
		},
	)
	if !result.IsError ||
		result.StructuredContent["assertion_status"] == "pass" ||
		result.axProofSnapshot != nil {
		t.Fatalf("one fallback sample bypassed stable_ms: %#v", result)
	}
}

func TestAssertAXCanonicalFallbackCompletesStartedSampleAfterDeadline(t *testing.T) {
	service := newService()
	target := &semanticTarget{
		Selector:           `checkbox "Automatic quotes"`,
		LocatorKind:        AXLocatorProviderBound,
		WindowHandle:       "private-window",
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/org/a11y/quote",
		ProcessGeneration:  "99123",
		Role:               "checkbox",
		Name:               "Automatic quotes",
		HasName:            true,
	}
	record := &elementRecord{
		Index:              7,
		ControlType:        "check box",
		Name:               "Automatic quotes",
		States:             "[active]",
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/org/a11y/quote",
		ProcessGeneration:  "99123",
	}
	snapshot := &appSnapshot{
		WindowHandle: "private-window",
		Elements:     []elementRecord{*record},
	}
	fallbackCalls := 0
	result := service.verifyResolvedAXRefWithProbes(
		"editor",
		"r17",
		target,
		verifyGoal{state: "!checked"},
		1,
		0,
		func(ProviderActionRequest) (*ProviderAccessibilityResult, error) {
			time.Sleep(2 * time.Millisecond)
			return &ProviderAccessibilityResult{
				OK:    false,
				Error: "AX resolver read was incomplete",
				Failure: &ProviderFailure{
					Code:          "observation_incomplete",
					FactsComplete: false,
				},
			}, nil
		},
		func() (*axAssertionSnapshotProof, toolCallResult) {
			fallbackCalls++
			return &axAssertionSnapshotProof{
				snapshot: snapshot,
				record:   record,
			}, toolCallResult{}
		},
	)
	if result.IsError ||
		result.StructuredContent["assertion_status"] != "pass" ||
		result.StructuredContent["assertion_canonical_fallback"] != true {
		t.Fatalf("terminal canonical sample did not complete: %#v", result)
	}
	if fallbackCalls != 1 {
		t.Fatalf("fallback calls = %d, want 1", fallbackCalls)
	}
}

func TestAssertAXTreatsCompleteStaleRefAsAbsent(t *testing.T) {
	service := newService()
	target := &semanticTarget{
		Selector:     `button "Use Excel Format"`,
		LocatorKind:  AXLocatorProviderBound,
		WindowHandle: "private-dialog",
		Role:         "button",
		Name:         "Use Excel Format",
		HasName:      true,
	}
	exists := false
	result := service.verifyResolvedAXRefWithProbe(
		"editor",
		"r17",
		target,
		verifyGoal{exists: &exists},
		1,
		0,
		func(ProviderActionRequest) (*ProviderAccessibilityResult, error) {
			return &ProviderAccessibilityResult{
				OK:    false,
				Error: "stale_element_ref: provider object disappeared",
				Failure: &ProviderFailure{
					Code:          "stale_element_ref",
					Subject:       "element",
					FactsComplete: true,
				},
			}, nil
		},
	)
	if result.IsError ||
		result.StructuredContent["assertion_status"] != "pass" {
		t.Fatalf("complete stale ref did not prove exact ref absence: %#v", result)
	}
}

func TestAssertAXTreatsCompleteAppDisappearanceAsAbsent(t *testing.T) {
	service := newService()
	target := &semanticTarget{
		Selector:     `button "Close"`,
		LocatorKind:  AXLocatorSemantic,
		WindowHandle: "private-window",
		Role:         "button",
		Name:         "Close",
		HasName:      true,
	}
	exists := false
	result := service.verifyResolvedAXRefWithProbe(
		"editor",
		"r21",
		target,
		verifyGoal{exists: &exists},
		1,
		0,
		func(ProviderActionRequest) (*ProviderAccessibilityResult, error) {
			return &ProviderAccessibilityResult{
				OK:    false,
				Error: "stale_app_ref: process generation no longer exists",
				Failure: &ProviderFailure{
					Code:          "stale_target",
					Subject:       "app",
					FactsComplete: true,
				},
			}, nil
		},
	)
	if result.IsError ||
		result.StructuredContent["assertion_status"] != "pass" {
		t.Fatalf("complete app disappearance did not prove ref absence: %#v", result)
	}
}

func TestAssertAXDoesNotTreatAmbiguityAsAbsence(t *testing.T) {
	service := newService()
	target := &semanticTarget{
		Selector:     `button "OK"`,
		LocatorKind:  AXLocatorSemantic,
		WindowHandle: "private-window",
		Role:         "button",
		Name:         "OK",
		HasName:      true,
	}
	exists := false
	result := service.verifyResolvedAXRefWithProbe(
		"editor",
		"r22",
		target,
		verifyGoal{exists: &exists},
		1,
		0,
		func(ProviderActionRequest) (*ProviderAccessibilityResult, error) {
			return &ProviderAccessibilityResult{
				OK:    false,
				Error: "semantic target is ambiguous",
				Failure: &ProviderFailure{
					Code:          "semantic_target_ambiguous",
					Subject:       "element",
					FactsComplete: true,
				},
			}, nil
		},
	)
	if !result.IsError ||
		result.StructuredContent["assertion_status"] != "locator_ambiguous" {
		t.Fatalf("ambiguity was treated as absence: %#v", result)
	}
}

func TestA11yAssertionSpecOwnsTimeoutAndRejectsIndexPostcondition(t *testing.T) {
	assertion := map[string]any{
		"type":       "state",
		"target":     map[string]any{"ref": "r17"},
		"state":      "checked",
		"value":      true,
		"timeout_ms": float64(2500),
	}
	spec, err := parseA11yAssertionSpec(assertion)
	if err != nil {
		t.Fatal(err)
	}
	if spec.timeoutMS != 2500 || spec.targetAddress != "r17" {
		t.Fatalf("parsed assertion drifted: %#v", spec)
	}
	service := newService()
	if err := service.preflightA11yPostcondition(
		&a11yBatchContext{windowHandle: "window-1", windowRef: "w1"},
		map[string]any{
			"type":   "exists",
			"target": map[string]any{"index": float64(7)},
			"value":  true,
		},
	); err == nil || !strings.Contains(err.Error(), "not stable across mutations") {
		t.Fatalf("numeric postcondition target was not rejected: %v", err)
	}
}

func TestValueEqualsAssertionUsesExactTrimmedValue(t *testing.T) {
	assertion := map[string]any{
		"type":   "value",
		"target": map[string]any{"ref": "r17"},
		"value":  "5",
	}
	spec, err := parseA11yAssertionSpec(assertion)
	if err != nil {
		t.Fatal(err)
	}
	if spec.assertionType != "value_equals" {
		t.Fatalf("assertion type = %q", spec.assertionType)
	}
	containsSpec, err := parseA11yAssertionSpec(map[string]any{
		"type":   "value",
		"target": map[string]any{"ref": "r17"},
		"value":  "5",
		"match":  "contains",
	})
	if err != nil {
		t.Fatal(err)
	}
	if containsSpec.assertionType != "value_contains" {
		t.Fatalf("contains assertion type = %q", containsSpec.assertionType)
	}
	goal := verifyGoal{valueEquals: "5"}
	if ok, _ := goal.check(&elementRecord{Value: " 5 "}, nil); !ok {
		t.Fatal("trimmed exact value did not match")
	}
	if ok, _ := goal.check(&elementRecord{Value: "15"}, nil); ok {
		t.Fatal("substring incorrectly satisfied value_equals")
	}
}

func TestValueAssertionAcceptsAndNormalizesJSONScalars(t *testing.T) {
	for _, tc := range []struct {
		name     string
		expected any
		observed string
	}{
		{name: "number", expected: float64(100), observed: "100.0"},
		{name: "boolean", expected: true, observed: "TRUE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertion := map[string]any{
				"type":   "value",
				"target": map[string]any{"ref": "r17"},
				"value":  tc.expected,
			}
			spec, err := parseA11yAssertionSpec(assertion)
			if err != nil {
				t.Fatal(err)
			}
			if spec.assertionType != "value_equals" {
				t.Fatalf("assertion type = %q", spec.assertionType)
			}
			goal := assertionGoal(assertion)
			if ok, detail := goal.check(
				&elementRecord{Value: tc.observed},
				nil,
			); !ok {
				t.Fatalf("scalar value did not match: %s", detail)
			}
		})
	}
}

func TestNumericValueAssertionPreservesExactLargeIntegers(t *testing.T) {
	goal := verifyGoal{
		valueEquals:     "9007199254740993",
		valueEqualsKind: "number",
	}
	if ok, _ := goal.check(
		&elementRecord{Value: "9007199254740992"},
		nil,
	); ok {
		t.Fatal("distinct integers above float64 precision compared equal")
	}
	if ok, detail := goal.check(
		&elementRecord{Value: "9007199254740993.0"},
		nil,
	); !ok {
		t.Fatalf("equivalent exact decimal did not compare equal: %s", detail)
	}
	value, kind, ok := assertionScalarString(
		map[string]any{"value": json.Number("1e10000")},
		"value",
	)
	if !ok || kind != "number" || value != "1e10000" {
		t.Fatalf("exact JSON number was rejected or changed: %q %q %t",
			value, kind, ok)
	}
}

func TestValueContainsRejectsNonStringScalar(t *testing.T) {
	_, err := parseA11yAssertionSpec(map[string]any{
		"type":   "value",
		"target": map[string]any{"ref": "r17"},
		"value":  float64(100),
		"match":  "contains",
	})
	if err == nil || !strings.Contains(err.Error(), "requires a string value") {
		t.Fatalf("numeric contains assertion was not rejected clearly: %v", err)
	}
}

func TestPostconditionFailureClassificationPreservesUnderlyingCause(t *testing.T) {
	predicateFalse := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"assertion_status":          "predicate_false",
			"assertion_sampled_live_ui": true,
		},
	}
	classification, underlying := classifyA11yPostconditionFailure(
		predicateFalse,
	)
	if classification != "postcondition_not_satisfied" ||
		underlying != "predicate_false" {
		t.Fatalf(
			"predicate failure classification = %q, %q",
			classification,
			underlying,
		)
	}

	incomplete := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":              "observation_incomplete",
			"assertion_sampled_live_ui": false,
		},
	}
	classification, underlying = classifyA11yPostconditionFailure(incomplete)
	if classification != "postcondition_unknown" ||
		underlying != "observation_incomplete" {
		t.Fatalf(
			"infrastructure failure classification = %q, %q",
			classification,
			underlying,
		)
	}
}

func TestPostconditionFinalObservationUsesLatestSnapshot(t *testing.T) {
	service := newService()
	current := &appSnapshot{
		App:                 appDescriptor{Name: "Editor"},
		WindowTitle:         "Document",
		WindowHandle:        "window-1",
		SnapshotID:          "after",
		ScreenshotPNGBase64: "latest-image",
		ScreenshotMimeType:  "image/png",
	}
	result := service.renderA11yBatchFinalObservation(
		current,
		nil,
		observation{mode: observeScreenshot},
	)
	foundLatest := false
	for _, item := range result.Content {
		if item.Type == "image" && item.Data == "latest-image" {
			foundLatest = true
			break
		}
	}
	if !foundLatest {
		t.Fatalf("postcondition final screenshot did not use the latest snapshot: %#v",
			result.Content)
	}
	if result.StructuredContent["final_observation_returned"] != true {
		t.Fatalf("final observation evidence missing: %#v", result.StructuredContent)
	}
}

func TestFinalObservationContinuationRequiresCompleteGoneSourceAndSameApp(t *testing.T) {
	service, appRef, sourceWindowRef, _ := candidateABatchService(t)
	currentWindowRef := service.windowRefRegistry.Acquire(
		"Editor",
		"w2:11111111111111111111111111111111",
		`window "Document"`,
		"",
		":1.8",
		"/org/a11y/atspi/accessible/8",
		"pid:44:start:100",
		"frame",
		"Document",
		appRef,
	)
	context := &a11yBatchContext{
		appRef:       appRef,
		windowRef:    sourceWindowRef,
		appToken:     appRef,
		windowHandle: "w2:0123456789abcdef0123456789abcdef",
	}
	valid := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":                "stale_window_ref",
			"source_window_status":        "gone",
			"recovery_inventory_complete": true,
			"current_window_ref":          currentWindowRef,
		},
	}
	entry, ok := service.finalObservationContinuation(context, valid)
	if !ok || entry == nil || entry.Ref != currentWindowRef {
		t.Fatalf("valid final observation continuation rejected: %#v, %t", entry, ok)
	}

	for name, mutate := range map[string]func(map[string]any){
		"incomplete inventory": func(fields map[string]any) {
			fields["recovery_inventory_complete"] = false
		},
		"source not gone": func(fields map[string]any) {
			fields["source_window_status"] = "present"
		},
		"wrong failure": func(fields map[string]any) {
			fields["failure_code"] = "observation_incomplete"
		},
		"same window": func(fields map[string]any) {
			fields["current_window_ref"] = sourceWindowRef
		},
	} {
		t.Run(name, func(t *testing.T) {
			fields := map[string]any{}
			for key, value := range valid.StructuredContent {
				fields[key] = value
			}
			mutate(fields)
			if entry, ok := service.finalObservationContinuation(
				context,
				toolCallResult{IsError: true, StructuredContent: fields},
			); ok || entry != nil {
				t.Fatalf("unsafe continuation accepted: %#v", entry)
			}
		})
	}
}

func TestFinalObservationContinuationRejectsDifferentAppOwner(t *testing.T) {
	service, appRef, sourceWindowRef, _ := candidateABatchService(t)
	otherAppRef := service.appRefRegistry.Acquire(
		"Other",
		":1.9",
		"pid:45:start:200",
		"c1",
	)
	currentWindowRef := service.windowRefRegistry.Acquire(
		"Other",
		"w2:22222222222222222222222222222222",
		`window "Other"`,
		"",
		":1.9",
		"/org/a11y/atspi/accessible/9",
		"pid:45:start:200",
		"frame",
		"Other",
		otherAppRef,
	)
	context := &a11yBatchContext{
		appRef:       appRef,
		windowRef:    sourceWindowRef,
		appToken:     appRef,
		windowHandle: "w2:0123456789abcdef0123456789abcdef",
	}
	result := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":                "stale_window_ref",
			"source_window_status":        "gone",
			"recovery_inventory_complete": true,
			"current_window_ref":          currentWindowRef,
		},
	}
	if entry, ok := service.finalObservationContinuation(context, result); ok ||
		entry != nil {
		t.Fatalf("cross-app continuation accepted: %#v", entry)
	}
}

func TestFinalObservationFollowsVerifiedTransientWindowClosure(t *testing.T) {
	service, appRef, sourceWindowRef, _ := candidateABatchService(t)
	backend := &recordingPlatformBackend{
		accessibilityResponses: []ProviderAccessibilityResult{
			{
				OK:    false,
				Error: "stale_window_ref: source dialog is gone",
				Failure: &ProviderFailure{
					Code:          "stale_window_ref",
					Phase:         "precondition",
					FactsComplete: true,
					Subject:       "window",
				},
				RecoveryContext: &ProviderRecoveryContext{
					SourceAppStatus:    "alive",
					SourceWindowStatus: "gone",
					InventoryComplete:  true,
					CurrentWindow: &ProviderReplacementWindow{
						WindowHandle:       "w2:11111111111111111111111111111111",
						Title:              "Document",
						Role:               "frame",
						ProviderBusName:    ":1.84",
						ProviderObjectPath: "/window/current",
						ProcessGeneration:  "pid:10:start:100",
					},
				},
			},
			{
				OK: true,
				Snapshot: &ProviderSnapshot{
					App:                 appDescriptor{Name: "LibreOffice"},
					WindowTitle:         "Document",
					WindowHandle:        "w2:11111111111111111111111111111111",
					ScreenshotPNGBase64: "current-image",
					ScreenshotMimeType:  "image/png",
				},
			},
		},
	}
	service.backend = backend
	context := &a11yBatchContext{
		appRef:       appRef,
		windowRef:    sourceWindowRef,
		appToken:     appRef,
		windowHandle: "w2:0123456789abcdef0123456789abcdef",
	}
	result := service.a11yBatchFinalObservation(
		context,
		observation{mode: observeScreenshot},
		false,
	)
	if result.IsError ||
		result.StructuredContent["final_observation_returned"] != true ||
		result.StructuredContent["final_observation_followed_window_transition"] !=
			true {
		t.Fatalf("verified window transition did not return current observation: %#v",
			result)
	}
	if len(backend.observeRequests) != 2 {
		t.Fatalf("observe calls=%d, want 2", len(backend.observeRequests))
	}
	if backend.observeRequests[0].WindowHandle != context.windowHandle ||
		backend.observeRequests[1].WindowHandle !=
			"w2:11111111111111111111111111111111" {
		t.Fatalf("unexpected observation targets: %#v", backend.observeRequests)
	}
	if _, err := service.windowRefRegistry.Resolve(sourceWindowRef, ""); err == nil ||
		!strings.Contains(err.Error(), string(AXRefStale)) {
		t.Fatalf("gone source window ref was not tombstoned: %v", err)
	}
}

func TestFinalObservationReturnsLifecycleWhenClosedAppHasNoReplacementWindow(
	t *testing.T,
) {
	service, appRef, sourceWindowRef, _ := candidateABatchService(t)
	backend := &recordingPlatformBackend{
		accessibilityResponses: []ProviderAccessibilityResult{{
			OK:    false,
			Error: "stale_window_ref: source window is gone",
			Failure: &ProviderFailure{
				Code:          "stale_window_ref",
				Phase:         "precondition",
				FactsComplete: true,
				Subject:       "window",
			},
			RecoveryContext: &ProviderRecoveryContext{
				SourceAppStatus:    "absent",
				SourceWindowStatus: "gone",
				InventoryComplete:  true,
			},
		}},
	}
	service.backend = backend
	context := &a11yBatchContext{
		appRef:       appRef,
		windowRef:    sourceWindowRef,
		appToken:     appRef,
		windowHandle: "w2:0123456789abcdef0123456789abcdef",
	}
	result := service.a11yBatchFinalObservation(
		context,
		observation{mode: observeDiff},
		true,
	)
	if result.IsError ||
		result.StructuredContent["target_window_absent"] != true ||
		result.StructuredContent["final_observation_returned"] != true ||
		result.StructuredContent["observation_required"] == true ||
		result.StructuredContent["next_identical_observation_required"] != false {
		t.Fatalf("closed last window did not return lifecycle observation: %#v",
			result)
	}
	if !strings.Contains(resultText(result), "effect: target_window_absent") {
		t.Fatalf("closed-window lifecycle text missing: %q", resultText(result))
	}
}

func TestFinalObservationReturnsLifecycleWhenClosedAppRefIsStale(
	t *testing.T,
) {
	service, appRef, sourceWindowRef, _ := candidateABatchService(t)
	backend := &recordingPlatformBackend{
		accessibilityResponses: []ProviderAccessibilityResult{{
			OK:                   false,
			Error:                "stale_app_ref: process generation no longer exists",
			FailureCode:          "stale_app_ref",
			FailurePhase:         "precondition",
			FailureSubject:       "app",
			FailureFactsComplete: true,
		}},
	}
	service.backend = backend
	context := &a11yBatchContext{
		appRef:       appRef,
		windowRef:    sourceWindowRef,
		appToken:     appRef,
		windowHandle: "w2:0123456789abcdef0123456789abcdef",
	}
	result := service.a11yBatchFinalObservation(
		context,
		observation{mode: observeDiff},
		true,
	)
	if result.IsError ||
		result.StructuredContent["target_window_absent"] != true ||
		result.StructuredContent["final_observation_returned"] != true ||
		result.StructuredContent["observation_required"] == true {
		t.Fatalf("closed app ref did not produce lifecycle observation: %#v",
			result)
	}
}

func TestClosedWindowLifecycleRejectsIncompleteStaleAppFacts(t *testing.T) {
	context := &a11yBatchContext{windowRef: "w7"}
	for name, fields := range map[string]map[string]any{
		"incomplete facts": {
			"failure_code":           "stale_app_ref",
			"failure_subject":        "app",
			"failure_facts_complete": false,
		},
		"wrong subject": {
			"failure_code":           "stale_app_ref",
			"failure_subject":        "window",
			"failure_facts_complete": true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if closedWindowLifecycleProven(
				context,
				toolCallResult{IsError: true, StructuredContent: fields},
			) {
				t.Fatalf("unsafe stale-app lifecycle accepted: %#v", fields)
			}
		})
	}
}

func TestSuccessfulCloseRecoversLifecycleObservation(t *testing.T) {
	service, appRef, sourceWindowRef, _ := candidateABatchService(t)
	backend := &recordingPlatformBackend{
		accessibilityResponses: []ProviderAccessibilityResult{{
			OK:    false,
			Error: "stale_window_ref: source window is gone",
			Failure: &ProviderFailure{
				Code:          "stale_window_ref",
				Phase:         "precondition",
				FactsComplete: true,
				Subject:       "window",
			},
			RecoveryContext: &ProviderRecoveryContext{
				SourceAppStatus:    "absent",
				SourceWindowStatus: "gone",
				InventoryComplete:  true,
			},
		}},
	}
	service.backend = backend
	context := &a11yBatchContext{
		appRef:       appRef,
		windowRef:    sourceWindowRef,
		appToken:     appRef,
		windowHandle: "w2:0123456789abcdef0123456789abcdef",
	}
	actionResult := toolCallResult{
		StructuredContent: map[string]any{
			"final_observation_returned": false,
			"observation_required":       true,
			"input_events_emitted":       true,
			"effect_unknown":             true,
			"mutation_unknown":           true,
		},
	}
	result := service.recoverClosedWindowActionObservation(
		context,
		a11yBatchAction{actionType: "close"},
		actionResult,
		observation{mode: observeDiff},
	)
	if result.IsError ||
		result.StructuredContent["target_window_absent"] != true ||
		result.StructuredContent["final_observation_returned"] != true ||
		result.StructuredContent["observation_required"] != false {
		t.Fatalf("successful close did not recover lifecycle evidence: %#v", result)
	}
	if result.StructuredContent["effect_unknown"] == true ||
		result.StructuredContent["mutation_unknown"] == true {
		t.Fatalf("verified close retained superseded uncertainty: %#v", result)
	}
	if len(backend.observeRequests) != 1 {
		t.Fatalf("close recovery observations=%d, want 1", len(backend.observeRequests))
	}
}

func TestFinalObservationDoesNotInferCloseFromNonCloseWindowDisappearance(
	t *testing.T,
) {
	service, appRef, sourceWindowRef, _ := candidateABatchService(t)
	service.backend = &recordingPlatformBackend{
		accessibilityResponses: []ProviderAccessibilityResult{{
			OK:    false,
			Error: "stale_window_ref: source window is gone",
			Failure: &ProviderFailure{
				Code:          "stale_window_ref",
				Phase:         "precondition",
				FactsComplete: true,
				Subject:       "window",
			},
			RecoveryContext: &ProviderRecoveryContext{
				SourceAppStatus:    "alive",
				SourceWindowStatus: "gone",
				InventoryComplete:  true,
			},
		}},
	}
	context := &a11yBatchContext{
		appRef:       appRef,
		windowRef:    sourceWindowRef,
		appToken:     appRef,
		windowHandle: "w2:0123456789abcdef0123456789abcdef",
	}
	result := service.a11yBatchFinalObservation(
		context,
		observation{mode: observeDiff},
		false,
	)
	if !result.IsError ||
		result.StructuredContent["target_window_absent"] == true ||
		result.StructuredContent["observation_required"] != true {
		t.Fatalf("non-close disappearance became lifecycle evidence: %#v", result)
	}
}

func TestEmptyScreenshotDoesNotClaimFinalObservation(t *testing.T) {
	snapshot := &appSnapshot{
		App:          appDescriptor{Name: "Editor"},
		WindowTitle:  "Document",
		WindowHandle: "window-1",
	}
	result, returned := renderAXScreenshotObservation(snapshot, nil)
	if returned ||
		result.StructuredContent["final_observation_returned"] != false ||
		result.StructuredContent["screenshot_returned"] != false ||
		result.StructuredContent["final_observation_kind"] != "unavailable" ||
		result.StructuredContent["observation_required"] != true {
		t.Fatalf("empty screenshot overstated final observation: %#v",
			result.StructuredContent)
	}
}

func TestEmptyCompactFallbackDoesNotClaimFinalObservation(t *testing.T) {
	service := newService()
	snapshot := &appSnapshot{
		App:          appDescriptor{Name: "Editor"},
		WindowTitle:  "Document",
		WindowHandle: "window-1",
		TreeLines:    []string{"ax:"},
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: nil,
			Roots:          nil,
		},
	}
	result := service.renderA11yBatchFinalObservation(
		snapshot,
		nil,
		observation{mode: observeDiff},
	)
	if result.IsError {
		t.Fatalf("missing compact observation must not reverse an emitted action: %#v",
			result)
	}
	if result.StructuredContent["final_observation_returned"] != false ||
		result.StructuredContent["final_observation_kind"] != "unavailable" ||
		result.StructuredContent["observation_required"] != true ||
		result.StructuredContent["delivered_snapshot_baseline_advanced"] != false {
		t.Fatalf("empty compact fallback overstated final observation: %#v",
			result.StructuredContent)
	}
	if !strings.Contains(resultText(result), "final_observation_unavailable") {
		t.Fatalf("missing compact observation lacked recovery text: %q",
			resultText(result))
	}
}

func TestSharedDiffFallbackDoesNotClaimEmptyObservation(t *testing.T) {
	service := newService()
	snapshot := &appSnapshot{
		App:          appDescriptor{Name: "Editor"},
		WindowTitle:  "Document",
		WindowHandle: "window-1",
		TreeLines:    []string{"ax:"},
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: nil,
			Roots:          nil,
		},
	}
	result, lines, returned, compact, fullTree :=
		service.renderAXDiffOrCompactObservation(snapshot, nil)
	if returned || compact || fullTree || len(lines) != 0 {
		t.Fatalf("empty fallback was reported as an observation: returned=%t compact=%t full=%t lines=%#v",
			returned, compact, fullTree, lines)
	}
	if result.StructuredContent["final_observation_returned"] != false ||
		result.StructuredContent["observation_required"] != true ||
		result.StructuredContent["next_identical_observation_required"] != true {
		t.Fatalf("shared action fallback overstated observation evidence: %#v",
			result.StructuredContent)
	}
}

func TestSharedDiffFallbackReturnsFreshBoundedWorkingTree(t *testing.T) {
	service := newService()
	snapshot := beta14LocalSnapshot()
	for index := range snapshot.Elements {
		snapshot.Elements[index].States = strings.ReplaceAll(
			snapshot.Elements[index].States,
			"[focused]",
			"",
		)
	}
	typedTreeTextForTest(t, snapshot)
	snapshot.sourceApp = "editor"
	snapshot.publicWindowRef = "w2"
	snapshot.projection = snapshotProjection{
		View:  treeViewWorking,
		Scope: a11yScopeWindow,
	}

	result, lines, returned, compact, fullTree :=
		service.renderAXDiffOrCompactObservation(snapshot, nil)
	if !returned || compact || !fullTree || len(lines) <= 1 {
		t.Fatalf("fresh working tree fallback missing: returned=%t compact=%t full=%t lines=%#v",
			returned, compact, fullTree, lines)
	}
	if result.StructuredContent["working_tree_fallback"] != true ||
		result.StructuredContent["final_observation_kind"] != "working_tree" ||
		!strings.Contains(resultText(result), `button "Save"`) {
		t.Fatalf("fresh working tree fallback evidence is incomplete: %#v\n%s",
			result.StructuredContent, resultText(result))
	}
}

func TestSharedDiffFallbackIdentifiesFocusedObservation(t *testing.T) {
	service := newService()
	snapshot := beta14LocalSnapshot()
	typedTreeTextForTest(t, snapshot)
	result, lines, returned, compact, fullTree :=
		service.renderAXDiffOrCompactObservation(snapshot, nil)
	if !returned || !compact || fullTree || len(lines) == 0 ||
		result.StructuredContent["final_observation_kind"] != "focused" {
		t.Fatalf("focused observation kind is incomplete: result=%#v lines=%#v",
			result.StructuredContent, lines)
	}
}

func TestSharedDiffFallbackPrefersWorkingTreeForStabilizedWindow(t *testing.T) {
	service := newService()
	snapshot := beta14LocalSnapshot()
	typedTreeTextForTest(t, snapshot)

	result, lines, returned, compact, fullTree :=
		service.renderAXWorkingObservation(snapshot, nil)
	if !returned || compact || !fullTree || len(lines) <= 1 {
		t.Fatalf("stabilized working tree missing: returned=%t compact=%t full=%t lines=%#v",
			returned, compact, fullTree, lines)
	}
	if result.StructuredContent["final_observation_kind"] != "working_tree" ||
		!strings.Contains(resultText(result), `button "Save"`) {
		t.Fatalf("stabilized working tree is incomplete: %#v\n%s",
			result.StructuredContent, resultText(result))
	}
}

func beta36WindowTransitionSnapshot(
	handle,
	title string,
	withProviderIdentity bool,
) *appSnapshot {
	snapshot := beta14LocalSnapshot()
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

func TestPostActionWindowIdentityStabilizationReturnsActionableNewWindow(t *testing.T) {
	oldHandle := "w2:" + strings.Repeat("a", 32)
	newHandle := "w2:" + strings.Repeat("b", 32)
	before := beta36WindowTransitionSnapshot(oldHandle, "Document", true)
	immediate := beta36WindowTransitionSnapshot(newHandle, "Rename", true)
	immediate.Elements[0].ControlType = "panel"
	stable := beta36WindowTransitionSnapshot(newHandle, "Rename", true)
	actionEvidence := &RuntimeActionEvidence{Route: "semantic"}
	immediate.actionExecution = actionEvidence

	backend := &recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{
			OK:       true,
			Snapshot: stable,
		},
	}
	service := newServiceWithBackend(backend)
	if err := service.buildAndRenderAXSnapshotV1(
		immediate,
		treeViewWorking,
	); err != nil {
		t.Fatal(err)
	}
	if isShortAXWindowRef(immediate.publicWindowRef) ||
		snapshotHasActionableElementRef(immediate) {
		t.Fatalf("fixture unexpectedly started actionable: %#v", immediate)
	}
	service.rememberSnapshot("Editor", immediate)

	got, notes, attempted, stabilized, trigger :=
		service.stabilizePostActionWindowIdentity(
			"Editor",
			before,
			immediate,
			axServiceRequest{Observe: string(observeDiff)},
		)
	if !attempted || !stabilized {
		t.Fatalf("identity stabilization=%t/%t", attempted, stabilized)
	}
	if trigger != "exact_handle_change" {
		t.Fatalf("identity stabilization trigger=%q", trigger)
	}
	if got == immediate ||
		got.WindowHandle != newHandle ||
		!isShortAXWindowRef(got.publicWindowRef) ||
		!snapshotHasActionableElementRef(got) {
		t.Fatalf("stabilized snapshot is not actionable: %#v", got)
	}
	if got.actionExecution != actionEvidence {
		t.Fatal("stabilization lost the original action evidence")
	}
	if len(backend.observeRequests) != 1 ||
		backend.observeRequests[0].WindowHandle != newHandle {
		t.Fatalf("stabilization was not bound to the exact new window: %#v",
			backend.observeRequests)
	}
	if len(notes) == 0 ||
		!strings.Contains(notes[0], "refreshed once without input") {
		t.Fatalf("stabilization note=%#v", notes)
	}
}

func TestTransientCommitClassificationIsGeneralAndObservationBounded(t *testing.T) {
	dialog := beta36WindowTransitionSnapshot(
		"w2:"+strings.Repeat("a", 32),
		"Question",
		true,
	)
	for index := range dialog.Elements {
		dialog.Elements[index].ProviderWindowRole = "dialog"
	}
	for _, request := range []axServiceRequest{
		{
			Action:  axKeypressCommand{Key: "ENTER"},
			Observe: string(observeDiff),
		},
		{
			Action:  axKeypressCommand{Key: "Escape"},
			Observe: string(observeScreenshot),
		},
	} {
		if !transientTopLevelSnapshot(dialog) ||
			!commitLikeTransientAction(request) {
			t.Fatalf("generic transient commit was not classified: %#v", request)
		}
	}
	if commitLikeTransientAction(axServiceRequest{
		Action:  axKeypressCommand{Key: "Alt+Escape"},
		Observe: string(observeDiff),
	}) {
		t.Fatal("modified key chord was classified as a commit")
	}
	if commitLikeTransientAction(axServiceRequest{
		Action:  axKeypressCommand{Key: "TAB"},
		Observe: string(observeDiff),
	}) {
		t.Fatal("ordinary dialog navigation was classified as a commit")
	}
	modalFrame := beta36WindowTransitionSnapshot(
		"w2:"+strings.Repeat("b", 32),
		"Modal Frame",
		true,
	)
	for index := range modalFrame.Elements {
		modalFrame.Elements[index].ProviderWindowRole = "frame"
	}
	modalFrame.WindowSelection = &windowSelection{
		Candidates: []windowSelectionCandidate{{
			Selected: true,
			Modal:    true,
		}},
	}
	if !transientTopLevelSnapshot(modalFrame) {
		t.Fatal("selected modal frame was not classified as transient")
	}

	service := newServiceWithBackend(&recordingPlatformBackend{})
	got, notes, attempted, stabilized :=
		service.stabilizePostActionTransientCommit(
			"Editor",
			dialog,
			axServiceRequest{
				Action:  axKeypressCommand{Key: "ENTER"},
				Observe: string(observeNone),
			},
			settlePlan{},
		)
	if attempted || stabilized || got != dialog || len(notes) != 0 {
		t.Fatalf(
			"observe=none paid a final-observation probe: attempted=%t stabilized=%t notes=%#v",
			attempted,
			stabilized,
			notes,
		)
	}
}

func TestTransientCommitProbeReturnsReplacementWindow(t *testing.T) {
	oldHandle := "w2:" + strings.Repeat("a", 32)
	newHandle := "w2:" + strings.Repeat("b", 32)
	dialog := beta36WindowTransitionSnapshot(oldHandle, "Open File", true)
	document := beta36WindowTransitionSnapshot(newHandle, "Document", true)
	for index := range dialog.Elements {
		dialog.Elements[index].ProviderWindowRole = "dialog"
	}
	for index := range document.Elements {
		document.Elements[index].ProviderWindowRole = "frame"
		document.Elements[index].ProviderWindowObjectPath = "/document"
		document.Elements[index].ProviderWindowName = "Document"
	}
	actionEvidence := &RuntimeActionEvidence{Route: "keyboard"}
	dialog.actionExecution = actionEvidence

	service := newServiceWithBackend(&recordingPlatformBackend{})
	if err := service.buildAndRenderAXSnapshotV1(
		dialog,
		treeViewWorking,
	); err != nil {
		t.Fatal(err)
	}
	sourceWindowRef := snapshotLogicalWindowRef(dialog)
	appRef := dialog.publicAppRef
	if !isShortAXWindowRef(sourceWindowRef) || !isShortAXAppRef(appRef) {
		t.Fatalf("fixture refs missing: app=%q window=%q",
			appRef, sourceWindowRef)
	}
	backend := &recordingPlatformBackend{
		accessibilityResponses: []ProviderAccessibilityResult{
			{
				OK:    false,
				Error: "stale_window_ref: source dialog is gone",
				Failure: &ProviderFailure{
					Code:          "stale_window_ref",
					Phase:         "precondition",
					FactsComplete: true,
					Subject:       "window",
				},
				RecoveryContext: &ProviderRecoveryContext{
					SourceAppStatus:    "alive",
					SourceWindowStatus: "gone",
					InventoryComplete:  true,
					CurrentWindow: &ProviderReplacementWindow{
						WindowHandle:       newHandle,
						Title:              "Document",
						Role:               "frame",
						ProviderBusName:    ":1.42",
						ProviderObjectPath: "/document",
						ProcessGeneration:  "pid:42:start:100",
					},
				},
			},
			{OK: true, Snapshot: document},
		},
	}
	service.backend = backend
	service.rememberSnapshot("Editor", dialog)

	got, notes, attempted, stabilized :=
		service.stabilizePostActionTransientCommit(
			"Editor",
			dialog,
			axServiceRequest{
				Action:  axKeypressCommand{Key: "ENTER"},
				Observe: string(observeDiff),
			},
			settlePlan{},
		)
	if !attempted || !stabilized {
		t.Fatalf("transient transition status=%t/%t", attempted, stabilized)
	}
	if got == dialog ||
		got.WindowHandle != newHandle ||
		got.actionExecution != actionEvidence {
		t.Fatalf("replacement window was not returned: %#v", got)
	}
	if len(notes) == 0 ||
		!strings.Contains(notes[0], "bounded no-input probe") {
		t.Fatalf("transition note=%#v", notes)
	}
	if len(backend.observeRequests) != 2 {
		t.Fatalf("transition reads=%d, want source proof plus final read",
			len(backend.observeRequests))
	}
}

func TestTransientCommitProbeDoesNotAdoptSiblingWindow(t *testing.T) {
	oldDelays := transientCommitProbeDelays
	transientCommitProbeDelays = []time.Duration{0, 0, 0}
	t.Cleanup(func() {
		transientCommitProbeDelays = oldDelays
	})

	oldHandle := "w2:" + strings.Repeat("a", 32)
	dialog := beta36WindowTransitionSnapshot(oldHandle, "Open File", true)
	for index := range dialog.Elements {
		dialog.Elements[index].ProviderWindowRole = "dialog"
	}
	service := newServiceWithBackend(&recordingPlatformBackend{})
	if err := service.buildAndRenderAXSnapshotV1(
		dialog,
		treeViewWorking,
	); err != nil {
		t.Fatal(err)
	}
	sourceWindowRef := snapshotLogicalWindowRef(dialog)
	backend := &recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{
			OK:       true,
			Snapshot: dialog,
		},
	}
	service.backend = backend
	service.rememberSnapshot("Editor", dialog)

	got, notes, attempted, stabilized :=
		service.stabilizePostActionTransientCommit(
			"Editor",
			dialog,
			axServiceRequest{
				Action:  axKeypressCommand{Key: "ENTER"},
				Observe: string(observeDiff),
			},
			settlePlan{},
		)
	if !attempted || stabilized || got != dialog || len(notes) != 0 {
		t.Fatalf(
			"live source dialog was replaced by a sibling: attempted=%t stabilized=%t got=%#v notes=%#v",
			attempted,
			stabilized,
			got,
			notes,
		)
	}
	if len(backend.observeRequests) != 3 {
		t.Fatalf("source proof reads=%d, want 3", len(backend.observeRequests))
	}
	for _, request := range backend.observeRequests {
		if request.WindowHandle != oldHandle {
			t.Fatalf("probe escaped source window: %#v", backend.observeRequests)
		}
	}
	if _, err := service.windowRefRegistry.Resolve(sourceWindowRef, ""); err != nil {
		t.Fatalf("live source ref was invalidated: %v", err)
	}
}

func TestTransientCommitProbeDoesNotCommitFailedReplacementRead(t *testing.T) {
	oldDelays := transientCommitProbeDelays
	transientCommitProbeDelays = []time.Duration{0}
	t.Cleanup(func() {
		transientCommitProbeDelays = oldDelays
	})

	oldHandle := "w2:" + strings.Repeat("a", 32)
	newHandle := "w2:" + strings.Repeat("b", 32)
	dialog := beta36WindowTransitionSnapshot(oldHandle, "Open File", true)
	for index := range dialog.Elements {
		dialog.Elements[index].ProviderWindowRole = "dialog"
	}
	service := newServiceWithBackend(&recordingPlatformBackend{})
	if err := service.buildAndRenderAXSnapshotV1(
		dialog,
		treeViewWorking,
	); err != nil {
		t.Fatal(err)
	}
	sourceWindowRef := snapshotLogicalWindowRef(dialog)
	backend := &recordingPlatformBackend{
		accessibilityResponses: []ProviderAccessibilityResult{
			{
				OK:    false,
				Error: "stale_window_ref: source dialog is gone",
				Failure: &ProviderFailure{
					Code:          "stale_window_ref",
					Phase:         "precondition",
					FactsComplete: true,
					Subject:       "window",
				},
				RecoveryContext: &ProviderRecoveryContext{
					SourceAppStatus:    "alive",
					SourceWindowStatus: "gone",
					InventoryComplete:  true,
					CurrentWindow: &ProviderReplacementWindow{
						WindowHandle:       newHandle,
						Title:              "Document",
						Role:               "frame",
						ProviderBusName:    ":1.42",
						ProviderObjectPath: "/document",
						ProcessGeneration:  "pid:42:start:100",
					},
				},
			},
			{
				OK:    false,
				Error: "observation_incomplete: replacement read failed",
				Failure: &ProviderFailure{
					Code:          "observation_incomplete",
					Phase:         "observation",
					FactsComplete: false,
					Subject:       "window",
				},
			},
		},
	}
	service.backend = backend
	service.rememberSnapshot("Editor", dialog)

	got, notes, attempted, stabilized :=
		service.stabilizePostActionTransientCommit(
			"Editor",
			dialog,
			axServiceRequest{
				Action:  axKeypressCommand{Key: "ENTER"},
				Observe: string(observeDiff),
			},
			settlePlan{},
		)
	if !attempted || stabilized || got != dialog || len(notes) != 0 {
		t.Fatalf(
			"failed replacement read committed provisional state: attempted=%t stabilized=%t got=%#v notes=%#v",
			attempted,
			stabilized,
			got,
			notes,
		)
	}
	if _, err := service.windowRefRegistry.Resolve(sourceWindowRef, ""); err != nil {
		t.Fatalf("failed probe mutated live source registry: %v", err)
	}
	if len(backend.observeRequests) != 2 ||
		backend.observeRequests[0].WindowHandle != oldHandle ||
		backend.observeRequests[1].WindowHandle != newHandle {
		t.Fatalf("unexpected proof/read sequence: %#v", backend.observeRequests)
	}
}

func TestPostActionWindowIdentityStabilizationRejectsDifferentWindow(t *testing.T) {
	oldHandle := "w2:" + strings.Repeat("a", 32)
	newHandle := "w2:" + strings.Repeat("b", 32)
	otherHandle := "w2:" + strings.Repeat("c", 32)
	before := beta36WindowTransitionSnapshot(oldHandle, "Document", true)
	immediate := beta36WindowTransitionSnapshot(newHandle, "Rename", true)
	immediate.Elements[0].ControlType = "panel"
	other := beta36WindowTransitionSnapshot(otherHandle, "Other", true)

	backend := &recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{
			OK:       true,
			Snapshot: other,
		},
	}
	service := newServiceWithBackend(backend)
	service.rememberSnapshot("Editor", immediate)

	got, notes, attempted, stabilized, trigger :=
		service.stabilizePostActionWindowIdentity(
			"Editor",
			before,
			immediate,
			axServiceRequest{Observe: string(observeDiff)},
		)
	if !attempted || stabilized || got != immediate || len(notes) != 0 {
		t.Fatalf(
			"mismatched window was accepted: attempted=%t stabilized=%t got=%#v notes=%#v",
			attempted, stabilized, got, notes,
		)
	}
	if trigger != "exact_handle_change" {
		t.Fatalf("identity stabilization trigger=%q", trigger)
	}
}

func TestPostActionWindowIdentityStabilizationSkipsReadyWindow(t *testing.T) {
	oldHandle := "w2:" + strings.Repeat("a", 32)
	newHandle := "w2:" + strings.Repeat("b", 32)
	before := beta36WindowTransitionSnapshot(oldHandle, "Document", true)
	ready := beta36WindowTransitionSnapshot(newHandle, "Rename", true)
	service := newServiceWithBackend(&recordingPlatformBackend{})
	if err := service.buildAndRenderAXSnapshotV1(
		ready,
		treeViewWorking,
	); err != nil {
		t.Fatal(err)
	}

	got, notes, attempted, stabilized, trigger :=
		service.stabilizePostActionWindowIdentity(
			"Editor",
			before,
			ready,
			axServiceRequest{Observe: string(observeDiff)},
		)
	if attempted || stabilized || got != ready || len(notes) != 0 {
		t.Fatalf(
			"ready window caused an extra read: attempted=%t stabilized=%t notes=%#v",
			attempted, stabilized, notes,
		)
	}
	if trigger != "" {
		t.Fatalf("unexpected identity stabilization trigger=%q", trigger)
	}
}

func TestPostActionWindowIdentityStabilizationRecoversStaleActionHandle(t *testing.T) {
	oldHandle := "w2:" + strings.Repeat("a", 32)
	newHandle := "w2:" + strings.Repeat("b", 32)
	before := beta36WindowTransitionSnapshot(oldHandle, "Document", true)
	immediate := beta36WindowTransitionSnapshot(oldHandle, "Rename", true)
	immediate.Elements[0].ControlType = "panel"
	stable := beta36WindowTransitionSnapshot(newHandle, "Rename", true)

	backend := &recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{
			OK:       true,
			Snapshot: stable,
		},
	}
	service := newServiceWithBackend(backend)
	if err := service.buildAndRenderAXSnapshotV1(
		immediate,
		treeViewWorking,
	); err != nil {
		t.Fatal(err)
	}
	service.rememberSnapshot("Editor", immediate)

	got, notes, attempted, stabilized, trigger :=
		service.stabilizePostActionWindowIdentity(
			"Editor",
			before,
			immediate,
			axServiceRequest{Observe: string(observeDiff)},
		)
	if !attempted || !stabilized || trigger != "semantic_window_change" {
		t.Fatalf(
			"identity stabilization=%t/%t trigger=%q",
			attempted, stabilized, trigger,
		)
	}
	if got.WindowHandle != newHandle ||
		!isShortAXWindowRef(got.publicWindowRef) ||
		!snapshotHasActionableElementRef(got) {
		t.Fatalf("stabilized snapshot is not actionable: %#v", got)
	}
	if len(backend.observeRequests) != 1 ||
		backend.observeRequests[0].WindowHandle != "" {
		t.Fatalf(
			"stale-handle recovery must discover the current app window: %#v",
			backend.observeRequests,
		)
	}
	if len(notes) == 0 {
		t.Fatal("stabilization note missing")
	}
}

func TestPostActionWindowIdentityStabilizationRejectsOldHandleAfterTitleChange(t *testing.T) {
	oldHandle := "w2:" + strings.Repeat("a", 32)
	before := beta36WindowTransitionSnapshot(oldHandle, "Document", true)
	immediate := beta36WindowTransitionSnapshot(oldHandle, "Rename", true)
	immediate.Elements[0].ControlType = "panel"
	stale := beta36WindowTransitionSnapshot(oldHandle, "Rename", true)

	backend := &recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{
			OK:       true,
			Snapshot: stale,
		},
	}
	service := newServiceWithBackend(backend)
	service.rememberSnapshot("Editor", immediate)

	got, notes, attempted, stabilized, trigger :=
		service.stabilizePostActionWindowIdentity(
			"Editor",
			before,
			immediate,
			axServiceRequest{Observe: string(observeDiff)},
		)
	if !attempted || stabilized || got != immediate || len(notes) != 0 ||
		trigger != "semantic_window_change" {
		t.Fatalf(
			"stale source window was accepted: attempted=%t stabilized=%t trigger=%q got=%#v notes=%#v",
			attempted, stabilized, trigger, got, notes,
		)
	}
}

func TestPostActionWindowIdentityStabilizationRejectsSameTitleSibling(t *testing.T) {
	oldHandle := "w2:" + strings.Repeat("a", 32)
	newHandle := "w2:" + strings.Repeat("b", 32)
	before := beta36WindowTransitionSnapshot(oldHandle, "Document", true)
	immediate := beta36WindowTransitionSnapshot(oldHandle, "Rename", true)
	immediate.Elements[0].ControlType = "panel"
	sibling := beta36WindowTransitionSnapshot(newHandle, "Rename", true)
	for index := range sibling.Elements {
		sibling.Elements[index].ProviderWindowObjectPath = "/sibling-dialog"
	}

	backend := &recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{
			OK:       true,
			Snapshot: sibling,
		},
	}
	service := newServiceWithBackend(backend)
	service.rememberSnapshot("Editor", immediate)

	got, notes, attempted, stabilized, trigger :=
		service.stabilizePostActionWindowIdentity(
			"Editor",
			before,
			immediate,
			axServiceRequest{Observe: string(observeDiff)},
		)
	if !attempted || stabilized || got != immediate || len(notes) != 0 ||
		trigger != "semantic_window_change" {
		t.Fatalf(
			"same-title sibling was accepted: attempted=%t stabilized=%t trigger=%q got=%#v notes=%#v",
			attempted, stabilized, trigger, got, notes,
		)
	}
}

func TestPostActionWindowIdentityStabilizationRejectedProbeIsTransactional(t *testing.T) {
	oldHandle := "w2:" + strings.Repeat("a", 32)
	newHandle := "w2:" + strings.Repeat("b", 32)
	before := beta36WindowTransitionSnapshot(oldHandle, "Document", true)
	immediate := beta36WindowTransitionSnapshot(oldHandle, "Rename", true)
	immediate.Elements[0].ControlType = "panel"
	sibling := beta36WindowTransitionSnapshot(newHandle, "Rename", true)
	for index := range sibling.Elements {
		sibling.Elements[index].ProviderWindowObjectPath = "/sibling-dialog"
	}

	service := newServiceWithBackend(&recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{
			OK:         true,
			LeaseState: "probe-lease",
			Snapshot:   sibling,
		},
	})
	if err := service.buildAndRenderAXSnapshotV1(
		before,
		treeViewWorking,
	); err != nil {
		t.Fatal(err)
	}
	service.rememberSnapshot("Editor", immediate)
	service.guiLeaseState = "original-lease"
	refsBefore := service.refRegistry.clone()
	windowsBefore := service.windowRefRegistry.clone()
	appsBefore := service.appRefRegistry.clone()

	_, _, attempted, stabilized, _ :=
		service.stabilizePostActionWindowIdentity(
			"Editor",
			before,
			immediate,
			axServiceRequest{Observe: string(observeDiff)},
		)
	if !attempted || stabilized {
		t.Fatalf("rejected probe status=%t/%t", attempted, stabilized)
	}
	if !reflect.DeepEqual(service.refRegistry, refsBefore) ||
		!reflect.DeepEqual(service.windowRefRegistry, windowsBefore) ||
		!reflect.DeepEqual(service.appRefRegistry, appsBefore) {
		t.Fatal("rejected identity probe mutated a live ref registry")
	}
	if service.guiLeaseState != "original-lease" {
		t.Fatalf("rejected identity probe changed GUI lease to %q",
			service.guiLeaseState)
	}
}

func TestPostActionWindowIdentityStabilizationRecoversProviderReplacement(t *testing.T) {
	handle := "w2:" + strings.Repeat("a", 32)
	before := beta36WindowTransitionSnapshot(handle, "Rename", true)
	immediate := beta36WindowTransitionSnapshot(handle, "Rename", true)
	for index := range immediate.Elements {
		immediate.Elements[index].ProviderWindowObjectPath = "/replacement-dialog"
	}
	stable := beta36WindowTransitionSnapshot(handle, "Rename", true)
	for index := range stable.Elements {
		stable.Elements[index].ProviderWindowObjectPath = "/replacement-dialog"
	}

	service := newServiceWithBackend(&recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{
			OK:       true,
			Snapshot: stable,
		},
	})
	if err := service.buildAndRenderAXSnapshotV1(
		before,
		treeViewWorking,
	); err != nil {
		t.Fatal(err)
	}
	oldWindowRef := before.publicWindowRef
	oldElementRef := ""
	for _, entry := range service.refRegistry.entries {
		if entry != nil && entry.LogicalWindowRef == oldWindowRef {
			oldElementRef = entry.Ref
			break
		}
	}
	if oldWindowRef == "" || oldElementRef == "" {
		t.Fatalf("source refs missing: window=%q element=%q",
			oldWindowRef, oldElementRef)
	}
	cursor := service.findCursorRegistry.Create(
		before,
		[]elementRecord{before.Elements[3]},
		0,
		`button "OK"`,
	)
	if cursor == "" {
		t.Fatal("find cursor fixture was not created")
	}
	if err := service.buildAndRenderAXSnapshotV1(
		immediate,
		treeViewWorking,
	); err != nil {
		t.Fatal(err)
	}
	if !isShortAXWindowRef(immediate.publicWindowRef) ||
		!snapshotHasActionableElementRef(immediate) {
		t.Fatal("provider replacement fixture must expose apparently ready refs")
	}
	service.rememberSnapshot("Editor", immediate)

	got, _, attempted, stabilized, trigger :=
		service.stabilizePostActionWindowIdentity(
			"Editor",
			before,
			immediate,
			axServiceRequest{Observe: string(observeDiff)},
		)
	if !attempted || !stabilized || trigger != "provider_window_change" {
		t.Fatalf(
			"provider replacement recovery=%t/%t trigger=%q",
			attempted, stabilized, trigger,
		)
	}
	if got == immediate || !isShortAXWindowRef(got.publicWindowRef) ||
		!snapshotHasActionableElementRef(got) {
		t.Fatalf("provider replacement did not return actionable refs: %#v", got)
	}
	if _, err := service.refRegistry.Resolve(oldElementRef, ""); err == nil {
		t.Fatalf("old-window element ref %q remained active", oldElementRef)
	}
	if _, err := service.findCursorRegistry.Resolve(cursor); err == nil {
		t.Fatalf("find cursor %q survived provider replacement", cursor)
	}
}

func TestPostActionWindowIdentityStabilizationAcceptsTitleChangeWithSameProvider(t *testing.T) {
	oldHandle := "w2:" + strings.Repeat("a", 32)
	newHandle := "w2:" + strings.Repeat("b", 32)
	before := beta36WindowTransitionSnapshot(oldHandle, "Document", true)
	immediate := beta36WindowTransitionSnapshot(oldHandle, "Opening", true)
	immediate.Elements[0].ControlType = "panel"
	stable := beta36WindowTransitionSnapshot(newHandle, "Ready", true)
	for index := range stable.Elements {
		stable.Elements[index].ProviderWindowName = "Ready"
	}

	service := newServiceWithBackend(&recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{
			OK:       true,
			Snapshot: stable,
		},
	})
	service.rememberSnapshot("Editor", immediate)

	got, _, attempted, stabilized, trigger :=
		service.stabilizePostActionWindowIdentity(
			"Editor",
			before,
			immediate,
			axServiceRequest{Observe: string(observeDiff)},
		)
	if !attempted || !stabilized || trigger != "semantic_window_change" {
		t.Fatalf(
			"title-changing stabilization=%t/%t trigger=%q",
			attempted, stabilized, trigger,
		)
	}
	if got.WindowTitle != "Ready" {
		t.Fatalf("stabilized title=%q, want Ready", got.WindowTitle)
	}
}

func TestModelVisibleEvidenceReportsOnlySuccessfulWindowStabilization(t *testing.T) {
	visible := modelVisibleEvidence(map[string]any{
		"window_identity_stabilization_attempted": true,
		"window_identity_stabilization_trigger":   "semantic_window_change",
		"window_identity_stabilized":              true,
	})
	if visible["window_identity_stabilized"] != true {
		t.Fatalf("successful stabilization was hidden: %#v", visible)
	}
	if _, present := visible["window_identity_stabilization_attempted"]; present {
		t.Fatalf("internal stabilization attempt leaked: %#v", visible)
	}
	if _, present := visible["window_identity_stabilization_trigger"]; present {
		t.Fatalf("internal stabilization trigger leaked: %#v", visible)
	}
}

func TestBatchFinalObservationAdvancesFreshWorkingTreeBaseline(t *testing.T) {
	service := newService()
	snapshot := beta14LocalSnapshot()
	for index := range snapshot.Elements {
		snapshot.Elements[index].States = strings.ReplaceAll(
			snapshot.Elements[index].States,
			"[focused]",
			"",
		)
	}
	typedTreeTextForTest(t, snapshot)
	snapshot.sourceApp = "editor"
	snapshot.publicWindowRef = "w2"
	snapshot.projection = snapshotProjection{
		View:  treeViewWorking,
		Scope: a11yScopeWindow,
	}

	result := service.renderA11yBatchFinalObservation(
		snapshot,
		nil,
		observation{mode: observeDiff},
	)
	if result.StructuredContent["final_observation_returned"] != true ||
		result.StructuredContent["observation_required"] == true ||
		result.StructuredContent["full_tree_returned"] != true ||
		result.StructuredContent["delivered_snapshot_baseline_advanced"] != true {
		t.Fatalf("fresh working tree did not close the observation loop: %#v",
			result.StructuredContent)
	}
	if got := service.deliveredSnapshotFor(snapshot); got != snapshot {
		t.Fatalf("fresh working tree did not become the new diff baseline: got=%#v want=%#v",
			got, snapshot)
	}
}

func TestMergePostconditionFinalObservationRemovesDeferredPlaceholder(t *testing.T) {
	action := (&appSnapshot{}).notesOnlyResult([]string{
		"action delivered",
		"postcondition: PASS",
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
		"postcondition: PASS",
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

func TestStripSupersededObservationPlaceholderPreservesOtherLines(t *testing.T) {
	content := []contentItem{{
		Type: "text",
		Text: "Note: input emitted\n\n" +
			"(observe=\"none\": no final tree or screenshot was returned.)\n" +
			"failure detail",
	}}
	filtered := stripSupersededAXObservationPlaceholder(content)
	if len(filtered) != 1 {
		t.Fatalf("filtered content=%#v", filtered)
	}
	text := filtered[0].Text
	if strings.Contains(text, `observe="none"`) {
		t.Fatalf("placeholder survived: %q", text)
	}
	for _, want := range []string{"Note: input emitted", "failure detail"} {
		if !strings.Contains(text, want) {
			t.Fatalf("non-placeholder evidence %q was removed: %q", want, text)
		}
	}
}

func TestFinalObservationFailureDoesNotOverwritePostconditionFailure(t *testing.T) {
	action := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":         "postcondition_unknown",
			"failure_phase":        "postcondition",
			"input_events_emitted": true,
		},
	}
	observation := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":               "observation_incomplete",
			"failure_phase":              "final_observation",
			"final_observation_returned": false,
		},
	}
	merged := mergeA11yBatchFinalObservation(action, observation)
	if merged.StructuredContent["failure_code"] != "postcondition_unknown" ||
		merged.StructuredContent["failure_phase"] != "postcondition" ||
		!merged.IsError {
		t.Fatalf("final observation masked postcondition failure: %#v",
			merged.StructuredContent)
	}
}

func TestRecoveryObservationPreservesOriginalUncertainty(t *testing.T) {
	failed := toolCallResult{
		IsError: true,
		Content: []contentItem{{Type: "text", Text: "effect unknown"}},
		StructuredContent: map[string]any{
			"failure_code":         "effect_unknown",
			"failure_phase":        "action",
			"input_events_emitted": true,
			"mutation_unknown":     true,
			"observation_required": true,
			"continuation": map[string]any{
				"state":     "observe_required",
				"next_mode": "observe",
			},
		},
	}
	observation := toolCallResult{
		Content: []contentItem{{Type: "text", Text: "fresh current state"}},
		StructuredContent: map[string]any{
			"final_observation_returned": true,
			"observation_required":       false,
		},
	}
	merged := mergeA11yBatchRecoveryObservation(failed, observation)
	if !merged.IsError ||
		merged.StructuredContent["failure_code"] != "effect_unknown" ||
		merged.StructuredContent["failure_phase"] != "action" ||
		merged.StructuredContent["input_events_emitted"] != true ||
		merged.StructuredContent["mutation_unknown"] != true ||
		merged.StructuredContent["final_observation_returned"] != true ||
		merged.StructuredContent["observation_required"] != false ||
		merged.StructuredContent["recovery_observation_attempted"] != true ||
		merged.StructuredContent["recovery_observation_returned"] != true ||
		merged.StructuredContent["cause_unattributed"] != true {
		t.Fatalf("recovery observation corrupted action evidence: %#v", merged)
	}
	if _, present := merged.StructuredContent["continuation"]; present {
		t.Fatalf("successful recovery retained observe-required continuation: %#v",
			merged.StructuredContent)
	}
}

func TestRecoveryObservationFailureStillRequiresObservation(t *testing.T) {
	failed := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":         "effect_unknown",
			"failure_phase":        "action",
			"input_events_emitted": true,
			"mutation_unknown":     true,
			"observation_required": true,
		},
	}
	observation := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":               "observation_incomplete",
			"final_observation_returned": false,
			"observation_required":       true,
		},
	}
	merged := mergeA11yBatchRecoveryObservation(failed, observation)
	if merged.StructuredContent["failure_code"] != "effect_unknown" ||
		merged.StructuredContent["observation_required"] != true ||
		merged.StructuredContent["recovery_observation_returned"] != false ||
		merged.StructuredContent["recovery_observation_failure_code"] !=
			"observation_incomplete" {
		t.Fatalf("failed recovery lost continuation evidence: %#v", merged)
	}
}

func TestRecoveryObservationRetriesAtAppRootWhenReplacementRefStales(t *testing.T) {
	service, appRef, sourceWindowRef, _ := candidateABatchService(t)
	replacementHandle := "w2:11111111111111111111111111111111"
	replacementRef := service.windowRefRegistry.Acquire(
		"editor",
		replacementHandle,
		`window "Document"`,
		"c2",
		":1.84",
		"/app/window/document",
		"pid:10:start:100",
		"window",
		"Document",
		appRef,
	)
	current := beta14LocalSnapshot()
	current.App = appDescriptor{
		Name:              "Editor",
		ProviderBusName:   ":1.84",
		ProcessGeneration: "pid:10:start:100",
	}
	current.WindowTitle = "Document"
	current.WindowHandle = replacementHandle
	current.SnapshotID = "current"
	backend := &recordingPlatformBackend{
		accessibilityResponses: []ProviderAccessibilityResult{
			{
				OK:    false,
				Error: "stale_window_ref: replacement changed again",
				Failure: &ProviderFailure{
					Code:          "stale_window_ref",
					Phase:         "precondition",
					FactsComplete: true,
					Subject:       "window",
				},
			},
			{
				OK:       true,
				Snapshot: current,
			},
		},
	}
	service.backend = backend
	failed := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":         "stale_target",
			"failure_phase":        "between_actions",
			"input_events_emitted": true,
			"mutation_unknown":     true,
			"observation_required": true,
			"current_window_ref":   replacementRef,
		},
	}
	context := &a11yBatchContext{
		appRef:       appRef,
		appToken:     appRef,
		windowRef:    sourceWindowRef,
		windowHandle: "w2:0123456789abcdef0123456789abcdef",
	}

	result := service.recoverA11yBatchObservation(
		context,
		failed,
		observation{mode: observeDiff},
	)
	if !result.IsError {
		t.Fatalf("recovered observation must preserve the stopped batch error")
	}
	for key, expected := range map[string]any{
		"failure_code":                        "stale_target",
		"failure_phase":                       "between_actions",
		"final_observation_returned":          true,
		"observation_required":                false,
		"next_identical_observation_required": false,
		"recovery_observation_returned":       true,
		"recovery_app_root_retry_attempted":   true,
		"recovery_targeted_failure_code":      "stale_window_ref",
		"cause_unattributed":                  true,
	} {
		if result.StructuredContent[key] != expected {
			t.Fatalf("%s=%#v want %#v in %#v",
				key, result.StructuredContent[key], expected,
				result.StructuredContent)
		}
	}
	if len(backend.observeRequests) != 2 {
		t.Fatalf("observe calls=%d, want 2", len(backend.observeRequests))
	}
	if backend.observeRequests[0].WindowHandle != replacementHandle {
		t.Fatalf("first recovery was not replacement-targeted: %#v",
			backend.observeRequests[0])
	}
	if backend.observeRequests[1].WindowHandle != "" {
		t.Fatalf("second recovery was not app-rooted: %#v",
			backend.observeRequests[1])
	}
	if text := resultText(result); !strings.Contains(text, `button "Save"`) ||
		!strings.Contains(text, "window-targeted read became stale") {
		t.Fatalf("app-root recovery observation missing: %q", text)
	}
}

func TestRecoveryObservationDoesNotRetryUnrelatedFailureAtAppRoot(t *testing.T) {
	for _, code := range []string{"permission_denied", "invalid_argument"} {
		result := toolCallResult{
			IsError: true,
			StructuredContent: map[string]any{
				"failure_code": code,
			},
		}
		if a11yRecoveryObservationCanRetryAtAppRoot(result) {
			t.Fatalf("%s unexpectedly allowed app-root retry", code)
		}
	}
}

func TestRecoveryObservationPreservesNonObserveContinuation(t *testing.T) {
	failed := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":         "effect_unknown",
			"observation_required": true,
		},
	}
	observation := toolCallResult{
		StructuredContent: map[string]any{
			"final_observation_returned": true,
			"continuation": map[string]any{
				"state":     "ready",
				"route":     "GUI",
				"next_mode": "screenshot",
			},
		},
	}
	merged := mergeA11yBatchRecoveryObservation(failed, observation)
	continuation, ok := merged.StructuredContent["continuation"].(map[string]any)
	if !ok || continuation["route"] != "GUI" {
		t.Fatalf("successful recovery dropped unrelated continuation: %#v",
			merged.StructuredContent)
	}
}

func TestVerifiedCloseScreenshotDoesNotBecomeEffectUnknown(t *testing.T) {
	action := toolCallResult{
		StructuredContent: map[string]any{
			"input_events_emitted":   true,
			"postcondition_verified": true,
		},
	}
	observation := closedWindowFinalObservation(
		&a11yBatchContext{windowRef: "w7"},
		observation{mode: observeScreenshot},
	)
	merged := mergeA11yBatchFinalObservation(action, observation)
	if merged.IsError ||
		merged.StructuredContent["target_window_absent"] != true ||
		merged.StructuredContent["observation_required"] != true ||
		merged.StructuredContent["next_identical_observation_required"] != false {
		t.Fatalf("verified close lifecycle evidence was lost: %#v", merged)
	}
	if _, ok := merged.StructuredContent["effect_unknown"]; ok {
		t.Fatalf("verified close was marked effect_unknown: %#v",
			merged.StructuredContent)
	}
	if _, ok := merged.StructuredContent["mutation_unknown"]; ok {
		t.Fatalf("verified close was marked mutation_unknown: %#v",
			merged.StructuredContent)
	}
}

func TestMissingFinalObservationMarksEmittedMutationUnknown(t *testing.T) {
	action := toolCallResult{
		StructuredContent: map[string]any{
			"input_events_emitted": true,
			"mutation_unknown":     false,
		},
	}
	observation := toolCallResult{
		StructuredContent: map[string]any{
			"final_observation_returned": false,
			"observation_required":       true,
		},
	}
	merged := mergeA11yBatchFinalObservation(action, observation)
	if merged.IsError ||
		merged.StructuredContent["effect_unknown"] != true ||
		merged.StructuredContent["mutation_unknown"] != true ||
		merged.StructuredContent["observation_failure_code"] !=
			"final_observation_unavailable" {
		t.Fatalf("missing final observation lost uncertainty: %#v", merged)
	}
}

func TestMissingFinalObservationUsesNestedRuntimeEmissionEvidence(t *testing.T) {
	action := toolCallResult{
		StructuredContent: map[string]any{
			"runtime_action_evidence": RuntimeActionEvidence{
				Route:        "semantic_action",
				InputEmitted: true,
			},
			"actionability": ActionabilityEvidence{
				Route:        "semantic_action",
				InputEmitted: TriTrue,
			},
		},
	}
	observation := toolCallResult{
		StructuredContent: map[string]any{
			"final_observation_returned": false,
			"observation_required":       true,
		},
	}
	merged := mergeA11yBatchFinalObservation(action, observation)
	if merged.IsError ||
		merged.StructuredContent["input_events_emitted"] != true ||
		merged.StructuredContent["effect_unknown"] != true ||
		merged.StructuredContent["mutation_unknown"] != true {
		t.Fatalf("nested emission evidence was ignored: %#v", merged)
	}
}

func TestFinalObservationFailurePreservesSuccessfulAction(t *testing.T) {
	action := toolCallResult{
		Content: []contentItem{{Type: "text", Text: "action delivered"}},
		StructuredContent: map[string]any{
			"input_events_emitted": true,
			"mutation_unknown":     false,
		},
	}
	observation := toolCallResult{
		IsError: true,
		Content: []contentItem{{
			Type: "text",
			Text: "runtime_timeout: final AX observation timed out",
		}},
		StructuredContent: map[string]any{
			"failure_code":               "runtime_timeout",
			"failure_phase":              "final_observation",
			"final_observation_returned": false,
			"observation_required":       true,
		},
	}
	merged := mergeA11yBatchFinalObservation(action, observation)
	if merged.IsError {
		t.Fatalf("post-action observation failure reversed successful delivery: %#v",
			merged)
	}
	if merged.StructuredContent["input_events_emitted"] != true ||
		merged.StructuredContent["effect_unknown"] != true ||
		merged.StructuredContent["mutation_unknown"] != true ||
		merged.StructuredContent["observation_failure_code"] != "runtime_timeout" ||
		merged.StructuredContent["final_observation_returned"] != false ||
		merged.StructuredContent["observation_required"] != true {
		t.Fatalf("post-action observation evidence is incomplete: %#v",
			merged.StructuredContent)
	}
	if _, present := merged.StructuredContent["failure_code"]; present {
		t.Fatalf("observation failure was exposed as action failure: %#v",
			merged.StructuredContent)
	}
}

func TestProviderActionObservationCanSkipSnapshot(t *testing.T) {
	request := axServiceRequest{
		Action:  axKeypressCommand{Key: "ENTER"},
		Observe: observeNone,
	}
	options := providerObservationOptionsFromOperation(request)
	if options.CaptureSnapshot == nil || *options.CaptureSnapshot {
		t.Fatalf("observe=none action must disable provider snapshot capture: %#v",
			options)
	}

	request.Observe = observeDiff
	options = providerObservationOptionsFromOperation(request)
	if options.CaptureSnapshot == nil || !*options.CaptureSnapshot {
		t.Fatalf("observe=diff action must request provider snapshot capture: %#v",
			options)
	}

	capture := true
	request.Observe = observeNone
	request.CaptureSnapshot = &capture
	options = providerObservationOptionsFromOperation(request)
	if options.CaptureSnapshot == nil || !*options.CaptureSnapshot {
		t.Fatalf("internal continuity capture override was ignored: %#v", options)
	}

	windowAction := WindowOperation("activate")
	options = providerObservationOptionsFromOperation(axServiceRequest{
		WindowAction: &windowAction,
		Observe:      observeNone,
	})
	if options.CaptureSnapshot == nil || !*options.CaptureSnapshot {
		t.Fatalf("window actions must retain internal lifecycle observation: %#v",
			options)
	}

	options = providerObservationOptionsFromOperation(axServiceRequest{
		Operation: axServiceObserve,
	})
	if options.CaptureSnapshot != nil {
		t.Fatalf("read operations should use provider default capture behavior: %#v",
			options)
	}
}

func TestBatchFinalActionCapturesRequestedObservationDirectly(t *testing.T) {
	final := observation{mode: observeDiff}
	intermediate := a11yBatchActionObservation(0, 2, final, false)
	if intermediate.resolvedMode() != observeNone ||
		intermediate.captureSnapshot == nil ||
		!*intermediate.captureSnapshot {
		t.Fatalf("intermediate action observation=%#v", intermediate)
	}
	last := a11yBatchActionObservation(1, 2, final, false)
	if last.resolvedMode() != observeDiff || last.captureSnapshot != nil {
		t.Fatalf("final action did not own requested observation: %#v", last)
	}
	postconditionLast := a11yBatchActionObservation(1, 2, final, true)
	if postconditionLast.resolvedMode() != observeNone ||
		postconditionLast.captureSnapshot == nil ||
		*postconditionLast.captureSnapshot {
		t.Fatalf("postcondition final action captured the wrong time point: %#v",
			postconditionLast)
	}
}

func TestSkippedObservationDoesNotInventMutationUnknown(t *testing.T) {
	result := ProviderAccessibilityResult{
		OK:                 true,
		ObservationSkipped: true,
		ActionEvidence: &ProviderActionEvidence{
			InputEmitted: true,
		},
	}
	result.prepareForService()
	if !result.InputEventsEmitted {
		t.Fatal("delivery evidence was lost")
	}
	if result.MutationUnknown {
		t.Fatal("intentionally skipped observation was treated as failed observation")
	}
}

func TestFinalObservationCannotRewriteEmittedActionAsPreflight(t *testing.T) {
	action := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":         "postcondition_unknown",
			"failure_phase":        "postcondition",
			"input_events_emitted": true,
			"mutation_unknown":     true,
			"preflight_only":       false,
			"evidence_scope":       "post_action_observation",
		},
	}
	observation := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":               "observation_incomplete",
			"failure_phase":              "final_observation",
			"final_observation_returned": false,
			"preflight_only":             true,
			"evidence_scope":             "error_only",
		},
	}
	merged := mergeA11yBatchFinalObservation(action, observation)
	if merged.StructuredContent["preflight_only"] != false {
		t.Fatalf("emitted action was rewritten as preflight: %#v",
			merged.StructuredContent)
	}
	if merged.StructuredContent["evidence_scope"] != "post_action_observation" {
		t.Fatalf("post-action evidence scope was lost: %#v",
			merged.StructuredContent)
	}
}

func TestFinalObservationCannotClearMonotonicActionEvidence(t *testing.T) {
	action := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":           "postcondition_unknown",
			"failure_phase":          "postcondition",
			"input_events_emitted":   true,
			"partial_input_possible": true,
			"mutation_unknown":       true,
		},
	}
	observation := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"input_events_emitted":   false,
			"partial_input_possible": false,
			"mutation_unknown":       false,
		},
	}
	merged := mergeA11yBatchFinalObservation(action, observation)
	for _, key := range []string{
		"input_events_emitted",
		"partial_input_possible",
		"mutation_unknown",
	} {
		if merged.StructuredContent[key] != true {
			t.Fatalf("%s was cleared by final observation: %#v",
				key, merged.StructuredContent)
		}
	}
}

func TestPostconditionFailureDoesNotInventInputEmission(t *testing.T) {
	action := toolCallResult{
		StructuredContent: map[string]any{
			"input_events_emitted": false,
			"preflight_only":       true,
		},
	}
	assertion := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":              "observation_incomplete",
			"assertion_sampled_live_ui": false,
		},
	}
	merged := mergeA11yPostconditionResult(action, assertion)
	if merged.StructuredContent["input_events_emitted"] != false ||
		merged.StructuredContent["preflight_only"] != true {
		t.Fatalf("postcondition invented input emission: %#v",
			merged.StructuredContent)
	}
	if _, ok := merged.StructuredContent["mutation_unknown"]; ok {
		t.Fatalf("preflight-only failure invented mutation uncertainty: %#v",
			merged.StructuredContent)
	}
}

func TestCompleteInventoryKeepsPresentTemporarilyHiddenRef(t *testing.T) {
	showing := true
	node := &AXNode{
		ID: "0/0", Role: "button", Name: "Save",
		SemanticClass:      "control",
		LocatorKind:        AXLocatorSemantic,
		SemanticTarget:     encodeScopedSemanticRef(`button "Save"`, "window-1"),
		RuntimeID:          []int{0, 0},
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/org/a11y/save",
		ProcessGeneration:  "99123",
		Showing:            &showing,
		Visible:            &showing,
	}
	registry := NewRefRegistry(16)
	first := &AXSnapshotV1{
		AppRef: "a1", AppIdentity: "editor",
		WindowHandle: "window-1", WindowRef: "w1", Revision: "c1",
		RefInventoryComplete: true,
		Roots:                []*AXNode{node},
		ByID:                 map[string]*AXNode{"0/0": node},
	}
	registry.AssignSnapshotRefs(first)
	ref := node.Ref
	if ref == "" {
		t.Fatal("initial ref was not issued")
	}

	hidden := false
	hiddenNode := *node
	hiddenNode.Ref = ""
	hiddenNode.Showing = &hidden
	hiddenNode.Visible = &hidden
	second := &AXSnapshotV1{
		AppRef: "a1", AppIdentity: "editor",
		WindowHandle: "window-1", WindowRef: "w1", Revision: "c2",
		RefInventoryComplete: true,
		Roots:                []*AXNode{&hiddenNode},
		ByID:                 map[string]*AXNode{"0/0": &hiddenNode},
	}
	registry.AssignSnapshotRefs(second)
	entry, err := registry.Resolve(ref, "")
	if err != nil {
		t.Fatalf("present hidden node was incorrectly retired: %v", err)
	}
	if entry.LastSeenRevision != "c2" {
		t.Fatalf("present hidden node last seen revision=%q, want c2",
			entry.LastSeenRevision)
	}
}

func TestVerifiedCloseUsesLifecycleObservationInsteadOfClosedWindowRead(t *testing.T) {
	result := closedWindowFinalObservation(
		&a11yBatchContext{windowRef: "w7"},
		observation{mode: observeDiff},
	)
	if result.IsError {
		t.Fatalf("verified close lifecycle observation must not be an error: %#v", result)
	}
	text := resultText(result)
	if !strings.Contains(text, "effect: target_window_absent") ||
		!strings.Contains(text, "absent_refs: [w7]") {
		t.Fatalf("close lifecycle evidence is incomplete: %q", text)
	}
	if result.StructuredContent["target_window_absent"] != true ||
		result.StructuredContent["final_observation_returned"] != true ||
		result.StructuredContent["final_observation_kind"] != "lifecycle" {
		t.Fatalf("close lifecycle fields are incomplete: %#v", result.StructuredContent)
	}
}

func TestExistingCloseLifecycleGetsVerifiedOutcomeFields(t *testing.T) {
	service := newService()
	result := service.recoverClosedWindowActionObservation(
		&a11yBatchContext{windowRef: "w7"},
		a11yBatchAction{actionType: "close"},
		toolCallResult{
			IsError: true,
			Content: []contentItem{
				{Type: "text", Text: "final_observation_unavailable: stale app"},
				{Type: "text", Text: "ax_diff:\n effect: target_window_absent"},
			},
			StructuredContent: map[string]any{
				"target_window_absent":                true,
				"final_observation_returned":          true,
				"observation_required":                false,
				"next_identical_observation_required": false,
				"input_events_emitted":                true,
				"effect_unknown":                      true,
				"mutation_unknown":                    true,
				"observation_failure_code":            "stale_app_ref",
				"post_action_observation_status":      "unknown",
				"continuation":                        map[string]any{"state": "observe_required"},
			},
		},
		observation{mode: observeDiff},
	)
	if result.IsError ||
		result.StructuredContent["close_effect_verified"] != true ||
		result.StructuredContent["observation_required"] != false {
		t.Fatalf("verified close lifecycle was not finalized: %#v",
			result.StructuredContent)
	}
	if _, present := result.StructuredContent["task_effect_verified"]; present {
		t.Fatalf("close proof claimed whole-task success: %#v",
			result.StructuredContent)
	}
	for _, key := range []string{
		"effect_unknown",
		"mutation_unknown",
		"observation_failure_code",
		"post_action_observation_status",
		"continuation",
	} {
		if _, present := result.StructuredContent[key]; present {
			t.Fatalf("verified close retained superseded %s: %#v",
				key, result.StructuredContent)
		}
	}
	if text := resultText(result); strings.Contains(
		text,
		"final_observation_unavailable",
	) || !strings.Contains(text, "target_window_absent") {
		t.Fatalf("verified close retained superseded observation text: %q", text)
	}
}

func TestVerifiedCloseDoesNotClaimTargetScreenshot(t *testing.T) {
	result := finalizeVerifiedCloseLifecycle(closedWindowFinalObservation(
		&a11yBatchContext{windowRef: "w7"},
		observation{mode: observeScreenshot},
	))
	if result.IsError {
		t.Fatalf("missing screenshot after verified close must not reverse success: %#v", result)
	}
	if result.StructuredContent["final_observation_returned"] != false ||
		result.StructuredContent["screenshot_returned"] != false ||
		result.StructuredContent["target_window_absent"] != true ||
		result.StructuredContent["observation_required"] != true ||
		result.StructuredContent["next_identical_observation_required"] != false {
		t.Fatalf("closed-window screenshot evidence is dishonest: %#v", result.StructuredContent)
	}
	if result.StructuredContent["close_effect_verified"] != true {
		t.Fatalf("verified close effect was hidden: %#v", result.StructuredContent)
	}
}

func TestCloseObserveNoneStillVerifiesLifecycleWithoutReturningObservation(
	t *testing.T,
) {
	service, appRef, sourceWindowRef, _ := candidateABatchService(t)
	backend := &recordingPlatformBackend{
		accessibilityResponses: []ProviderAccessibilityResult{{
			OK:    false,
			Error: "stale_window_ref: source window is gone",
			Failure: &ProviderFailure{
				Code:          "stale_window_ref",
				Phase:         "precondition",
				FactsComplete: true,
				Subject:       "window",
			},
			RecoveryContext: &ProviderRecoveryContext{
				SourceAppStatus:    "absent",
				SourceWindowStatus: "gone",
				InventoryComplete:  true,
			},
		}},
	}
	service.backend = backend
	context := &a11yBatchContext{
		appRef:       appRef,
		windowRef:    sourceWindowRef,
		appToken:     appRef,
		windowHandle: "w2:0123456789abcdef0123456789abcdef",
	}
	actionResult := toolCallResult{
		IsError: true,
		Content: []contentItem{{
			Type: "text",
			Text: "close input emitted; final observation unavailable",
		}},
		StructuredContent: map[string]any{
			"failure_code":                   "stale_window_ref",
			"failure_phase":                  "post_action_observation",
			"final_observation_returned":     false,
			"observation_required":           true,
			"input_events_emitted":           true,
			"effect_unknown":                 true,
			"mutation_unknown":               true,
			"cause_unattributed":             true,
			"partial_input_possible":         false,
			"post_action_observation_status": "unknown",
		},
	}

	result := service.recoverClosedWindowActionObservation(
		context,
		a11yBatchAction{actionType: "close"},
		actionResult,
		observation{mode: observeNone},
	)
	if result.IsError ||
		result.StructuredContent["target_window_absent"] != true ||
		result.StructuredContent["close_effect_verified"] != true ||
		result.StructuredContent["final_observation_kind"] != "none" ||
		result.StructuredContent["final_observation_returned"] != false ||
		result.StructuredContent["observation_required"] != false ||
		result.StructuredContent["next_identical_observation_required"] != false {
		t.Fatalf("observe=none close lifecycle evidence is incomplete: %#v", result)
	}
	for _, key := range []string{
		"effect_unknown",
		"mutation_unknown",
		"cause_unattributed",
		"continuation",
		"observation_failure_code",
		"post_action_observation_status",
	} {
		if _, present := result.StructuredContent[key]; present {
			t.Fatalf("verified close retained superseded %s: %#v",
				key, result.StructuredContent)
		}
	}
	text := resultText(result)
	if strings.Contains(text, "ax_diff:") ||
		!strings.Contains(text, `observe="none" returned no final tree or screenshot`) {
		t.Fatalf("observe=none close output is misleading: %q", text)
	}
	if len(backend.observeRequests) != 1 {
		t.Fatalf("close lifecycle probes=%d, want 1", len(backend.observeRequests))
	}
}

func TestCloseObserveNoneDoesNotLeakReplacementWindowObservation(t *testing.T) {
	service, appRef, sourceWindowRef, _ := candidateABatchService(t)
	backend := &recordingPlatformBackend{
		accessibilityResponses: []ProviderAccessibilityResult{
			{
				OK:    false,
				Error: "stale_window_ref: source dialog is gone",
				Failure: &ProviderFailure{
					Code:          "stale_window_ref",
					Phase:         "precondition",
					FactsComplete: true,
					Subject:       "window",
				},
				RecoveryContext: &ProviderRecoveryContext{
					SourceAppStatus:    "alive",
					SourceWindowStatus: "gone",
					InventoryComplete:  true,
					CurrentWindow: &ProviderReplacementWindow{
						WindowHandle:       "w2:11111111111111111111111111111111",
						Title:              "Save Changes",
						Role:               "dialog",
						ProviderBusName:    ":1.84",
						ProviderObjectPath: "/window/save",
						ProcessGeneration:  "pid:10:start:100",
					},
				},
			},
			{
				OK: true,
				Snapshot: &ProviderSnapshot{
					App:          appDescriptor{Name: "Editor"},
					WindowTitle:  "Save Changes",
					WindowHandle: "w2:11111111111111111111111111111111",
				},
			},
		},
	}
	service.backend = backend
	context := &a11yBatchContext{
		appRef:       appRef,
		windowRef:    sourceWindowRef,
		appToken:     appRef,
		windowHandle: "w2:0123456789abcdef0123456789abcdef",
	}
	actionResult := toolCallResult{
		StructuredContent: map[string]any{
			"final_observation_returned": false,
			"input_events_emitted":       true,
		},
	}

	result := service.recoverClosedWindowActionObservation(
		context,
		a11yBatchAction{actionType: "close"},
		actionResult,
		observation{mode: observeNone},
	)
	if result.StructuredContent["close_effect_verified"] != false ||
		result.StructuredContent["final_observation_kind"] != "none" ||
		result.StructuredContent["final_observation_returned"] != false ||
		result.StructuredContent["observation_required"] != true ||
		result.StructuredContent["next_identical_observation_required"] != false {
		t.Fatalf("unverified observe=none close evidence is incomplete: %#v", result)
	}
	if strings.Contains(resultText(result), "Save Changes") {
		t.Fatalf("observe=none leaked replacement-window observation: %q",
			resultText(result))
	}
	continuation := result.StructuredContent["continuation"].(map[string]any)
	if continuation["route"] != "observe" ||
		continuation["retry_same_request"] != false {
		t.Fatalf("unverified close continuation=%#v", continuation)
	}
	if len(backend.observeRequests) != 2 {
		t.Fatalf("close lifecycle probes=%d, want 2", len(backend.observeRequests))
	}
}

func TestVerifiedClosePreservesIndependentActionUncertainty(t *testing.T) {
	result := finalizeVerifiedCloseLifecycle(toolCallResult{
		IsError: true,
		Content: []contentItem{{
			Type: "text",
			Text: "input emitted\nfinal_observation_unavailable: stale app\n" +
				"partial input remains possible",
		}},
		StructuredContent: map[string]any{
			"target_window_absent":       true,
			"final_observation_returned": true,
			"failure_code":               "effect_unknown",
			"failure_phase":              "action",
			"input_events_emitted":       true,
			"partial_input_possible":     true,
			"mutation_unknown":           true,
			"effect_unknown":             true,
			"observation_required":       false,
			"task_effect_verified":       true,
		},
	})
	if !result.IsError ||
		result.StructuredContent["close_effect_verified"] != true ||
		result.StructuredContent["failure_code"] != "effect_unknown" ||
		result.StructuredContent["partial_input_possible"] != true ||
		result.StructuredContent["mutation_unknown"] != true ||
		result.StructuredContent["effect_unknown"] != true {
		t.Fatalf("close proof erased independent action uncertainty: %#v", result)
	}
	if _, present := result.StructuredContent["task_effect_verified"]; present {
		t.Fatalf("close proof claimed whole-task success: %#v",
			result.StructuredContent)
	}
	text := resultText(result)
	if strings.Contains(text, "final_observation_unavailable:") ||
		!strings.Contains(text, "input emitted") ||
		!strings.Contains(text, "partial input remains possible") {
		t.Fatalf("close cleanup removed unrelated evidence: %q", text)
	}
}

func TestClosedWindowNegativeTitlePostconditionUsesOwningApp(t *testing.T) {
	backend := &recordingPlatformBackend{
		windowResponse: &ProviderWindowResult{
			OK:           true,
			WindowTitles: []string{"Document - Editor"},
		},
	}
	service, appRef, windowRef, _ := candidateABatchService(t)
	service.backend = backend
	result := service.runA11yPostcondition(
		&a11yBatchContext{
			appRef:       appRef,
			windowRef:    windowRef,
			appToken:     appRef,
			windowHandle: "private-window",
		},
		map[string]any{
			"type":       "window_title_contains",
			"text":       "Confirm Removal",
			"exists":     false,
			"timeout_ms": float64(1),
		},
		true,
	)
	if result.IsError ||
		result.StructuredContent["assertion_status"] != "pass" {
		t.Fatalf("negative close postcondition did not use app inventory: %#v", result)
	}
	if len(backend.calls) != 1 || backend.calls[0] != "window_list" {
		t.Fatalf("unexpected provider calls: %#v", backend.calls)
	}
}

func TestNegativeSelectorPostconditionPassesWhenBoundWindowIsCompletelyGone(
	t *testing.T,
) {
	assertion := map[string]any{
		"type": "exists",
		"target": map[string]any{
			"selector": map[string]any{
				"role": "button",
				"name": "Use Excel 2007-365 Format",
			},
		},
		"value": false,
	}
	spec, err := parseA11yAssertionSpec(assertion)
	if err != nil {
		t.Fatal(err)
	}
	result := toolCallResult{
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code":                "stale_window_ref",
			"source_window_status":        "gone",
			"recovery_inventory_complete": true,
		},
	}
	if !negativeSelectorPostconditionProvenByGoneWindow(
		spec,
		assertion,
		result,
	) {
		t.Fatal("complete bound-window disappearance did not prove selector absence")
	}
}

func TestNegativeSelectorPostconditionRejectsUnsafeWindowTransitionEvidence(
	t *testing.T,
) {
	baseAssertion := map[string]any{
		"type": "exists",
		"target": map[string]any{
			"selector": map[string]any{
				"role": "button",
				"name": "Confirm",
			},
		},
		"value": false,
	}
	for name, mutate := range map[string]func(
		map[string]any,
		map[string]any,
	){
		"incomplete inventory": func(
			_ map[string]any,
			fields map[string]any,
		) {
			fields["recovery_inventory_complete"] = false
		},
		"source still present": func(
			_ map[string]any,
			fields map[string]any,
		) {
			fields["source_window_status"] = "present"
		},
		"wrong failure": func(
			_ map[string]any,
			fields map[string]any,
		) {
			fields["failure_code"] = "observation_incomplete"
		},
		"positive exists": func(
			assertion map[string]any,
			_ map[string]any,
		) {
			assertion["value"] = true
		},
		"stable duration": func(
			assertion map[string]any,
			_ map[string]any,
		) {
			assertion["stable_ms"] = float64(500)
		},
		"ref target": func(
			assertion map[string]any,
			_ map[string]any,
		) {
			assertion["target"] = map[string]any{"ref": "r7"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			assertion := map[string]any{}
			for key, value := range baseAssertion {
				assertion[key] = value
			}
			fields := map[string]any{
				"failure_code":                "stale_window_ref",
				"source_window_status":        "gone",
				"recovery_inventory_complete": true,
			}
			mutate(assertion, fields)
			spec, err := parseA11yAssertionSpec(assertion)
			if err != nil {
				t.Fatal(err)
			}
			if negativeSelectorPostconditionProvenByGoneWindow(
				spec,
				assertion,
				toolCallResult{IsError: true, StructuredContent: fields},
			) {
				t.Fatalf("unsafe evidence proved selector absence: %#v %#v",
					assertion, fields)
			}
		})
	}
}

func TestNegativeWindowTitleAcceptsCompleteAppDisappearance(t *testing.T) {
	service, appRef, _, _ := candidateABatchService(t)
	result := service.verifyWindowTitleWithProbe(
		appRef,
		"Editor",
		false,
		1,
		0,
		"",
		func(ProviderWindowListRequest) (*ProviderWindowResult, error) {
			return &ProviderWindowResult{
				OK:    false,
				Error: "stale application identity",
				Failure: &ProviderFailure{
					Code:          "stale_target",
					Subject:       "app",
					FactsComplete: true,
				},
			}, nil
		},
	)
	if result.IsError ||
		result.StructuredContent["assertion_status"] != "pass" {
		t.Fatalf("complete app disappearance did not prove title absence: %#v", result)
	}
}

func TestPrepareAXEvidenceKeepsContinuationVisibleWithoutStructuredContent(t *testing.T) {
	result := toolCallResult{
		Content: []contentItem{{Type: "text", Text: "stale_element_ref"}},
		IsError: true,
		StructuredContent: map[string]any{
			"failure_code": "stale_element_ref",
			"continuation": map[string]any{
				"state":              "ready",
				"retry_same_request": false,
				"next_mode":          "find",
				"fresh_window_ref":   "w3",
			},
		},
	}
	prepared := prepareAXEvidence(result)
	text := resultText(prepared)
	for _, expected := range []string{
		"continuation:",
		"state=ready",
		"retry_same_request=false",
		"next_mode=find",
		"fresh_window_ref=w3",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("model-visible continuation lost %q: %q", expected, text)
		}
	}
	if _, ok := prepared.StructuredContent["continuation"]; !ok {
		t.Fatalf("compact structured continuation missing: %#v",
			prepared.StructuredContent)
	}
}

func TestFindCanIssueRefsAcrossPassiveCanonicalRoles(t *testing.T) {
	for _, role := range []CanonicalRole{
		"menu", "cell", "gridcell", "tab", "group", "listitem",
	} {
		t.Run(string(role), func(t *testing.T) {
			showing := true
			node := &AXNode{
				ID:                 "0/1",
				Role:               role,
				Name:               "Target",
				FindMatch:          true,
				ProviderBusName:    ":1.42",
				ProviderObjectPath: "/org/a11y/target/" + string(role),
				ProcessGeneration:  "99123",
				Showing:            &showing,
				Visible:            &showing,
			}
			snapshot := &AXSnapshotV1{
				AppRef:       "a1",
				AppIdentity:  "app",
				WindowHandle: "private-window",
				WindowRef:    "w1",
				Revision:     "c1",
				Roots:        []*AXNode{node},
				ByID:         map[string]*AXNode{"0/1": node},
			}
			registry := NewRefRegistry(16)
			registry.AssignSnapshotRefs(snapshot)
			if node.Ref == "" {
				t.Fatalf("find match role %q did not receive a ref", role)
			}
			entry, err := registry.Resolve(node.Ref, "")
			if err != nil {
				t.Fatal(err)
			}
			if entry.LocatorKind != AXLocatorProviderBound {
				t.Fatalf("find match role %q locator=%q, want provider_bound",
					role, entry.LocatorKind)
			}
		})
	}
}

func TestFindAddressabilityReportsUnavailableStableIdentity(t *testing.T) {
	node := &AXNode{
		ID:        "0/1",
		Role:      "cell",
		Name:      "C2",
		FindMatch: true,
	}
	snapshot := &AXSnapshotV1{
		AppRef:       "a1",
		AppIdentity:  "calc",
		WindowHandle: "private-window",
		WindowRef:    "w1",
		Revision:     "c1",
	}
	eligible, reason, _ := axNodeRefEligibility(snapshot, node)
	if eligible || reason != "stable_identity_unavailable" {
		t.Fatalf("eligibility=%v reason=%q, want stable_identity_unavailable",
			eligible, reason)
	}
}

func TestDefaultObserveCanIssueRefFromGeometryAndPointerEvidence(t *testing.T) {
	showing := true
	node := &AXNode{
		ID:             "0/1",
		Role:           "cell",
		Name:           "C2",
		LocatorKind:    AXLocatorSemantic,
		SemanticTarget: encodeScopedSemanticRef(`cell "C2"`, "private-window"),
		Showing:        &showing,
		Visible:        &showing,
		SourceRecord: &elementRecord{
			Frame:  &frame{X: 40, Y: 50, Width: 80, Height: 24},
			States: "[has-click-action]",
		},
	}
	snapshot := &AXSnapshotV1{
		AppRef:       "a1",
		AppIdentity:  "calc",
		WindowHandle: "private-window",
		WindowRef:    "w1",
		Revision:     "c1",
		Roots:        []*AXNode{node},
		ByID:         map[string]*AXNode{"0/1": node},
	}
	registry := NewRefRegistry(16)
	registry.AssignSnapshotRefs(snapshot)
	if node.Ref == "" {
		t.Fatal("visible target with stable identity, geometry, and pointer evidence did not receive a ref")
	}
}

func TestDefaultObserveSeparatesControlIdentityFromActionability(t *testing.T) {
	showing := true
	node := &AXNode{
		ID:             "0/1",
		Role:           "button",
		SemanticClass:  "control",
		Name:           "Save",
		LocatorKind:    AXLocatorSemantic,
		SemanticTarget: encodeScopedSemanticRef(`button "Save"`, "private-window"),
		Showing:        &showing,
		Visible:        &showing,
	}
	snapshot := &AXSnapshotV1{
		AppRef:       "a1",
		AppIdentity:  "app",
		WindowHandle: "private-window",
		WindowRef:    "w1",
		Revision:     "c1",
		Roots:        []*AXNode{node},
		ByID:         map[string]*AXNode{"0/1": node},
	}
	NewRefRegistry(16).AssignSnapshotRefs(snapshot)
	if node.Ref == "" {
		t.Fatal("stable visible control identity received no ref")
	}
	if axNodeHasCapability(node) {
		t.Fatal("test control unexpectedly has an executable capability")
	}
}

func TestDefaultObserveCanIssueRefToVisibleSelectableItem(t *testing.T) {
	showing := true
	node := &AXNode{
		ID:             "0/1",
		Role:           "treeitem",
		SemanticClass:  "item",
		Name:           "Inbox",
		LocatorKind:    AXLocatorSemantic,
		SemanticTarget: encodeScopedSemanticRef(`treeitem "Inbox"`, "private-window"),
		Showing:        &showing,
		Visible:        &showing,
		SourceRecord: &elementRecord{
			Frame: &frame{X: 40, Y: 50, Width: 80, Height: 24},
		},
	}
	snapshot := &AXSnapshotV1{
		AppRef:       "a1",
		AppIdentity:  "app",
		WindowHandle: "private-window",
		WindowRef:    "w1",
		Revision:     "c1",
		Roots:        []*AXNode{node},
		ByID:         map[string]*AXNode{"0/1": node},
	}
	NewRefRegistry(16).AssignSnapshotRefs(snapshot)
	if node.Ref == "" {
		t.Fatal("stable visible selectable item received no ref")
	}
}

func TestDefaultObserveDoesNotInferActionabilityFromRoleAndGeometry(t *testing.T) {
	showing := true
	node := &AXNode{
		ID:             "0/1",
		Role:           "group",
		SemanticClass:  "structure",
		Name:           "Static Group",
		LocatorKind:    AXLocatorSemantic,
		SemanticTarget: encodeScopedSemanticRef(`group "Static Group"`, "private-window"),
		Showing:        &showing,
		Visible:        &showing,
		SourceRecord: &elementRecord{
			Frame: &frame{X: 40, Y: 50, Width: 80, Height: 24},
		},
	}
	snapshot := &AXSnapshotV1{
		AppRef:       "a1",
		AppIdentity:  "app",
		WindowHandle: "private-window",
		WindowRef:    "w1",
		Revision:     "c1",
		Roots:        []*AXNode{node},
		ByID:         map[string]*AXNode{"0/1": node},
	}
	registry := NewRefRegistry(16)
	registry.AssignSnapshotRefs(snapshot)
	if node.Ref != "" {
		t.Fatalf("role and geometry alone incorrectly implied actionability: %q", node.Ref)
	}
}

func TestFindSnapshotPreservesAppAndWindowOwnership(t *testing.T) {
	source := &AXSnapshotV1{
		SchemaVersion:              "AXSnapshotV1",
		AppRef:                     "a7",
		AppIdentity:                "libreoffice",
		WindowHandle:               "private-window",
		WindowRef:                  "w9",
		Revision:                   "c42",
		AcquisitionIncomplete:      true,
		IdentityCoverageIncomplete: true,
		RefInventoryComplete:       true,
	}
	node := &AXNode{ID: "0/1", Role: "cell", Name: "C2"}
	findSnapshot := newAXFindSnapshot(source, []*AXNode{node})
	if findSnapshot == nil ||
		findSnapshot.AppRef != "a7" ||
		findSnapshot.WindowRef != "w9" ||
		findSnapshot.WindowHandle != "private-window" ||
		!findSnapshot.AcquisitionIncomplete ||
		!findSnapshot.IdentityCoverageIncomplete ||
		findSnapshot.RefInventoryComplete {
		t.Fatalf("find snapshot lost ownership: %#v", findSnapshot)
	}
}

func TestExpandableProviderOmissionBlocksSemanticRefIssuance(t *testing.T) {
	showing := true
	node := &AXNode{
		ID: "0/0", Role: "button", Name: "Save",
		SemanticClass:      "control",
		LocatorKind:        AXLocatorSemantic,
		SemanticTarget:     encodeScopedSemanticRef(`button "Save"`, "window-1"),
		RuntimeID:          []int{0, 0},
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/org/a11y/save",
		ProcessGeneration:  "99123",
		Showing:            &showing,
		Visible:            &showing,
	}
	snapshot := &AXSnapshotV1{
		AppRef: "a1", AppIdentity: "editor",
		WindowHandle: "window-1", WindowRef: "w1", Revision: "c1",
		IdentityCoverageIncomplete: true,
		Roots:                      []*AXNode{node},
		ByID:                       map[string]*AXNode{"0/0": node},
	}
	registry := NewRefRegistry(16)
	registry.AssignSnapshotRefs(snapshot)
	if node.Ref == "" {
		t.Fatal("complete provider identity should permit a provider-bound ref")
	}
	entry, err := registry.Resolve(node.Ref, "")
	if err != nil {
		t.Fatal(err)
	}
	if entry.LocatorKind != AXLocatorProviderBound {
		t.Fatalf("locator=%q want provider_bound", entry.LocatorKind)
	}
}

func TestFindAddressabilityExplainsHiddenTarget(t *testing.T) {
	hidden := false
	collapsed := AXNode{
		ID: "0", Role: "group", Name: "Collapsed",
		States: AXStates{Expanded: TriFalse},
	}
	node := &AXNode{
		ID: "0/1", Role: "button", Name: "Hidden",
		SemanticTarget:     `button "Hidden"`,
		Capabilities:       AXCapabilities{Activate: ActivationSemantic},
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/org/a11y/hidden",
		ProcessGeneration:  "99123",
		Showing:            &hidden,
		Visible:            &hidden,
		Parent:             &collapsed,
		FindMatch:          true,
	}
	collapsed.Children = []*AXNode{node}
	snapshot := &AXSnapshotV1{
		AppRef:       "a1",
		AppIdentity:  "editor",
		WindowHandle: "window-1",
		WindowRef:    "w1",
		Revision:     "c1",
		Roots:        []*AXNode{&collapsed},
		ByID:         map[string]*AXNode{"0": &collapsed, "0/1": node},
	}
	registry := NewRefRegistry(16)
	registry.AssignSnapshotRefs(snapshot)
	if node.Ref != "" {
		t.Fatalf("collapsed hidden find target received ref %q", node.Ref)
	}
	eligible, reason, _ := axNodeRefEligibility(snapshot, node)
	if eligible || reason != "hidden_under_collapsed_ancestor" {
		t.Fatalf("eligibility=%v reason=%q, want hidden_under_collapsed_ancestor",
			eligible, reason)
	}
}

func TestApplyRuntimeGUILeaseUsesFailureEvidence(t *testing.T) {
	service := newService()
	service.guiLeaseState = "fresh"
	service.applyRuntimeGUILease(&ProviderDesktopResult{
		OK:         false,
		LeaseState: "suspect",
		Error:      "post-action observation failed",
	})
	if service.guiLeaseState != "suspect" {
		t.Fatalf("failure response lease=%q, want suspect", service.guiLeaseState)
	}
}

func TestWindowRefRegistryUsesShortStableSessionRefs(t *testing.T) {
	registry := NewWindowRefRegistry(2)
	handleA := "w2:" + strings.Repeat("a", 32)
	handleB := "w2:" + strings.Repeat("b", 32)
	handleC := "w2:" + strings.Repeat("c", 32)

	first := registry.Acquire(
		"chrome", handleA, `frame "New Tab"`, "c1",
	)
	if first != "w1" {
		t.Fatalf("first window ref = %q, want w1", first)
	}
	renamed := registry.Acquire(
		"chrome", handleA, `frame "Search results"`, "c2",
	)
	if renamed != first {
		t.Fatalf("title/selector mutation changed logical window ref: %q -> %q",
			first, renamed)
	}
	second := registry.Acquire(
		"chrome", handleB, `dialog "Open File"`, "c2",
	)
	if second != "w2" {
		t.Fatalf("second window ref = %q, want w2", second)
	}
	if registry.Acquire(
		"chrome", handleC, `dialog "Save File"`, "c3",
	) != "w3" {
		t.Fatal("new native identity did not receive a new monotonic ref")
	}
	if _, err := registry.Resolve(first, "chrome"); err == nil ||
		!strings.Contains(err.Error(), "expired") {
		t.Fatalf("evicted ref must fail explicitly: %v", err)
	}
	if got := registry.Acquire(
		"chrome", "w2:"+strings.Repeat("d", 33), `dialog "Bad"`, "c4",
	); got != "" {
		t.Fatalf("malformed internal handle received public ref %q", got)
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

func TestPublicTargetRefAcceptsOnlyShortElementOrWindowRefs(t *testing.T) {
	for _, ref := range []string{"r1", "r42", "w1", "w9"} {
		if got, _, err := a11yTargetAddress(map[string]any{
			"target": map[string]any{"ref": ref},
		}); err != nil || got != ref {
			t.Fatalf("public ref %q rejected: got=%q err=%v", ref, got, err)
		}
	}
	for _, ref := range []string{
		"r0",
		"w0",
		"w1:payload",
		"s1:payload",
		"s2:payload",
		"w2:" + strings.Repeat("a", 32),
	} {
		if _, _, err := a11yTargetAddress(map[string]any{
			"target": map[string]any{"ref": ref},
		}); err == nil {
			t.Fatalf("internal or invalid ref %q was accepted", ref)
		}
	}
}

func TestAssertionRendererUsesPublicRef(t *testing.T) {
	result := toolCallResult{
		Content: []contentItem{{
			Type: "text",
			Text: `PASS: s2:internal value to contain "Etsy".`,
		}},
	}
	result = replaceToolResultText(result, "s2:internal", "r36")
	text := resultText(result)
	if !strings.Contains(text, "PASS: r36") ||
		strings.Contains(text, "s2:") {
		t.Fatalf("assertion text leaked internal ref: %q", text)
	}
}

func TestWorkingStructuralBudgetDefaultsToTwelveK(t *testing.T) {
	t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "")
	if got := axStructuralTreeBudget(); got != 12000 {
		t.Fatalf("working structural budget=%d, expected exactly 12000", got)
	}
	if maxInlineChars() != 24000 {
		t.Fatal("12k working budget must not lower the final 24k safety ceiling")
	}
}

func TestPrepareAXEvidenceKeepsAssertionTimeoutFacts(t *testing.T) {
	result := toolCallResult{
		Content: []contentItem{{Type: "text", Text: "assertion timed out"}},
		IsError: true,
		StructuredContent: map[string]any{
			"ax_mode":                   "assert_ax",
			"failure_code":              "assertion_timeout",
			"failure_phase":             "assertion",
			"assertion_sampled_live_ui": true,
			"preflight_only":            false,
			"predicate_verified":        false,
			"task_completion_verified":  false,
			"distillation":              []any{"debug-only"},
		},
	}
	result = prepareAXEvidence(result)
	if result.StructuredContent["failure_code"] != "assertion_timeout" ||
		result.StructuredContent["failure_phase"] != "assertion" ||
		result.StructuredContent["assertion_sampled_live_ui"] != true ||
		result.StructuredContent["predicate_verified"] != false {
		t.Fatalf("assertion timeout evidence was lost: %#v",
			result.StructuredContent)
	}
	if _, present := result.StructuredContent["distillation"]; present {
		t.Fatalf("assertion timeout leaked debug telemetry")
	}
}

func TestPrepareAXEvidenceKeepsRequestedPostcondition(t *testing.T) {
	result := toolCallResult{
		Content: []contentItem{{Type: "text", Text: "batch result"}},
		StructuredContent: map[string]any{
			"ax_mode": "act",
			"a11y_batch_result": map[string]any{
				"actions_requested":                    1,
				"actions_completed_without_call_error": 1,
				"actions": []map[string]any{
					{
						"action_index": 0,
						"action_type":  "click",
						"call_error":   false,
						"actionability": map[string]any{
							"route":                  "semantic_action",
							"input_emitted":          "true",
							"readback_sampled":       "true",
							"delivery_verified":      "true",
							"postcondition_verified": "true",
						},
					},
				},
				"postcondition": map[string]any{
					"status": "pass",
				},
			},
			"actionability": map[string]any{
				"stages": []any{"full telemetry only"},
			},
		},
	}
	result = prepareAXEvidence(result)
	batch := result.StructuredContent["batch"].(map[string]any)
	postcondition, ok := batch["postcondition"].(map[string]any)
	if !ok || postcondition["status"] != "pass" {
		t.Fatalf("requested postcondition evidence was lost: %#v", batch)
	}
	if _, present := batch["actions"]; present {
		t.Fatalf("routine per-action routing leaked with postcondition: %#v", batch)
	}
	if _, present := result.StructuredContent["actionability"]; present {
		t.Fatalf("top-level duplicate actionability leaked into compact evidence")
	}
}

func TestPrepareAXEvidenceKeepsUnknownFinalObservation(t *testing.T) {
	result := prepareAXEvidence(toolCallResult{
		Content: []contentItem{{Type: "text", Text: "action delivered"}},
		StructuredContent: map[string]any{
			"ax_mode":                         "act",
			"input_events_emitted":            true,
			"effect_unknown":                  true,
			"mutation_unknown":                true,
			"final_observation_returned":      false,
			"observation_required":            true,
			"observation_failure_code":        "runtime_timeout",
			"post_action_observation_status":  "unknown",
			"postcondition_verified":          false,
			"task_completion_verified":        false,
			"provider_internal_debug_payload": "hidden",
		},
	})
	for key, expected := range map[string]any{
		"input_events_emitted":           true,
		"effect_unknown":                 true,
		"mutation_unknown":               true,
		"final_observation_returned":     false,
		"observation_required":           true,
		"observation_failure_code":       "runtime_timeout",
		"post_action_observation_status": "unknown",
		"postcondition_verified":         false,
	} {
		if result.StructuredContent[key] != expected {
			t.Fatalf("%s=%#v want %#v in %#v",
				key, result.StructuredContent[key], expected,
				result.StructuredContent)
		}
	}
	if _, present := result.StructuredContent["provider_internal_debug_payload"]; present {
		t.Fatalf("provider debug payload leaked: %#v", result.StructuredContent)
	}
}

func TestPrepareAXEvidenceKeepsFinalObservationKind(t *testing.T) {
	result := prepareAXEvidence(toolCallResult{
		Content: []contentItem{{Type: "text", Text: "fresh current tree"}},
		StructuredContent: map[string]any{
			"ax_mode":                    "act",
			"final_observation_returned": true,
			"final_observation_kind":     "working_tree",
			"working_tree_fallback":      true,
		},
	})
	if result.StructuredContent["final_observation_kind"] != "working_tree" {
		t.Fatalf("final observation kind was hidden: %#v",
			result.StructuredContent)
	}
	if _, present := result.StructuredContent["working_tree_fallback"]; present {
		t.Fatalf("redundant fallback telemetry leaked: %#v",
			result.StructuredContent)
	}
}

func TestPrepareAXEvidenceKeepsFinalWindowTransition(t *testing.T) {
	result := prepareAXEvidence(toolCallResult{
		Content: []contentItem{{Type: "text", Text: "current window"}},
		StructuredContent: map[string]any{
			"ax_mode":                                           "act",
			"final_observation_returned":                        true,
			"source_window_status":                              "gone",
			"final_observation_window_ref":                      "w9",
			"final_observation_followed_window_transition":      true,
			"observation_required":                              false,
			"next_identical_observation_required":               false,
			"provider_internal_final_observation_window_handle": "hidden",
		},
	})
	for key, expected := range map[string]any{
		"final_observation_returned":                   true,
		"source_window_status":                         "gone",
		"final_observation_window_ref":                 "w9",
		"final_observation_followed_window_transition": true,
		"observation_required":                         false,
		"next_identical_observation_required":          false,
	} {
		if result.StructuredContent[key] != expected {
			t.Fatalf("%s=%#v want %#v in %#v",
				key, result.StructuredContent[key], expected,
				result.StructuredContent)
		}
	}
	if _, present :=
		result.StructuredContent["provider_internal_final_observation_window_handle"]; present {
		t.Fatalf("private window identity leaked: %#v", result.StructuredContent)
	}
}

func TestCompactBatchKeepsReconciledPassingActionGuard(t *testing.T) {
	result := compactAXBatchResult(map[string]any{
		"actions": []map[string]any{{
			"action_index": 0,
			"action_type":  "click",
			"postcondition": map[string]any{
				"status":                              "pass",
				"verified":                            true,
				"reconciled_by_canonical_observation": true,
				"cause_unattributed":                  true,
			},
		}},
	})
	actions, ok := result["actions"].([]map[string]any)
	if !ok || len(actions) != 1 {
		t.Fatalf("reconciled passing guard was hidden: %#v", result)
	}
	postcondition, _ := actions[0]["postcondition"].(map[string]any)
	if postcondition["reconciled_by_canonical_observation"] != true {
		t.Fatalf("reconciliation evidence was lost: %#v", actions[0])
	}
}

func TestPrepareAXEvidenceFullViewStillHidesDebugTelemetry(t *testing.T) {
	full := map[string]any{
		"ax_mode":      "observe",
		"tree_view":    "full",
		"distillation": []any{"complete-stage-report"},
		"shadow_ast":   map[string]any{"canonical_nodes": 10},
	}
	result := prepareAXEvidence(toolCallResult{
		Content:           []contentItem{{Type: "text", Text: "full tree"}},
		StructuredContent: full,
	})
	if result.StructuredContent["distillation"] != nil ||
		result.StructuredContent["shadow_ast"] != nil {
		t.Fatalf("full view leaked debug telemetry: %#v",
			result.StructuredContent)
	}
	if len(result.StructuredContent) != 0 {
		t.Fatalf("full view returned internal evidence: %#v",
			result.StructuredContent)
	}
}

func TestRootedAndWindowObservationsUseDifferentCaches(t *testing.T) {
	window := beta14LocalSnapshot()
	window.publicWindowRef = "w1"
	window.projection = snapshotProjection{
		View: treeViewWorking, Scope: a11yScopeWindow,
	}
	rooted := *window
	rooted.projection.Root = `{"focused":true}`
	if deliveredTreeKey(window) == deliveredTreeKey(&rooted) ||
		deliveredSnapshotKey(window) == deliveredSnapshotKey(&rooted) {
		t.Fatal("rooted and whole-window observations share delivery caches")
	}
}

func TestAppRefRegistryKeepsGenerationIdentity(t *testing.T) {
	registry := NewAppRefRegistry(8)
	first := registry.Acquire("LibreOffice", ":1.84", "pid:10:start:100", "c1")
	if first != "a1" {
		t.Fatalf("first app ref = %q, want a1", first)
	}
	if again := registry.Acquire(
		"LibreOffice Calc", ":1.84", "pid:10:start:100", "c2",
	); again != first {
		t.Fatalf("same provider generation changed ref: %q -> %q", first, again)
	}
	restarted := registry.Acquire(
		"LibreOffice", ":1.91", "pid:10:start:200", "c3",
	)
	if restarted == first {
		t.Fatalf("restarted app reused ref %q", first)
	}
}

func candidateABatchService(t *testing.T) (*service, string, string, string) {
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

func TestRefOwnedBatchDerivesOneWindowAndKeyboardInheritance(t *testing.T) {
	service, appRef, windowRef, elementRef := candidateABatchService(t)
	actions := []a11yBatchAction{
		{actionType: "click", targetRef: elementRef},
		{actionType: "keypress", key: "ENTER"},
	}
	context, err := service.deriveA11yBatchContext("", "", "", actions)
	if err != nil {
		t.Fatal(err)
	}
	if context.appRef != appRef || context.appToken != appRef ||
		context.windowRef != windowRef || context.windowHandle == "" {
		t.Fatalf("unexpected ref-owned context: %#v", context)
	}
}

func TestRefOwnedBatchScopesSelectorPostconditionToSameWindow(t *testing.T) {
	service, _, windowRef, elementRef := candidateABatchService(t)
	context, err := service.deriveA11yBatchContext(
		"", "", "", []a11yBatchAction{{
			actionType: "click",
			targetRef:  elementRef,
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if context.windowRef != windowRef {
		t.Fatalf("element ref did not preserve its owning window: %#v", context)
	}
	if err := service.preflightA11yPostcondition(
		context,
		map[string]any{
			"type": "exists",
			"target": map[string]any{
				"selector": map[string]any{
					"role": "text",
					"name": "Saved",
				},
			},
			"value": true,
		},
	); err != nil {
		t.Fatalf("selector postcondition rejected its inherited window: %v", err)
	}
}

func TestActionPostconditionsArePreflightedBeforeFirstInput(t *testing.T) {
	service, _, _, elementRef := candidateABatchService(t)
	backend := &recordingPlatformBackend{}
	service.backend = backend

	result := service.a11yBatch(
		"",
		[]any{
			map[string]any{
				"type":   "click",
				"target": map[string]any{"ref": elementRef},
			},
			map[string]any{
				"type": "keypress",
				"keys": []any{"ENTER"},
				"postcondition": map[string]any{
					"type": "state",
					"target": map[string]any{
						"ref": "r999999",
					},
					"state": "selected",
					"value": true,
				},
			},
		},
		observation{},
		nil,
	)
	if !result.IsError ||
		result.StructuredContent["action_attempted"] != false {
		t.Fatalf("invalid action guard must fail before input: %#v", result)
	}
	if len(backend.calls) != 0 {
		t.Fatalf("preflight failure reached provider: %#v", backend.calls)
	}
	if !strings.Contains(
		resultText(result),
		"actions[1].postcondition",
	) {
		t.Fatalf("preflight error did not identify action guard: %q",
			resultText(result))
	}
}

func TestActionPostconditionFailureStopsLaterActionsWithCurrentEvidence(t *testing.T) {
	service := newService()
	assertionSnapshot := &appSnapshot{
		App:                 appDescriptor{Name: "Editor"},
		WindowHandle:        "private-window",
		SnapshotID:          "c2",
		ScreenshotPNGBase64: testPNGBase64(t, 2, 2),
		ScreenshotMimeType:  "image/png",
		publicAppRef:        "a1",
		publicWindowRef:     "w1",
	}
	postcondition := toolCallResult{
		Content: []contentItem{{
			Type: "text",
			Text: "assertion_passed=false failure_code=predicate_false",
		}},
		IsError: true,
		StructuredContent: map[string]any{
			"assertion_status":          "fail",
			"failure_code":              "predicate_false",
			"assertion_sampled_live_ui": true,
		},
		axProofSnapshot: assertionSnapshot,
	}
	actionResult := toolCallResult{
		Content: []contentItem{{Type: "text", Text: "action delivered"}},
		StructuredContent: map[string]any{
			"input_events_emitted": true,
			"actionability": map[string]any{
				"input_emitted": "true",
			},
		},
	}
	action := a11yBatchAction{
		actionType: "click",
		postcondition: map[string]any{
			"type": "state",
			"target": map[string]any{
				"ref": "r1",
			},
			"state": "selected",
			"value": true,
		},
	}
	actionEvidence := []map[string]any{
		batchActionEvidenceEntry(0, "click", actionResult),
	}
	actionEvidence[0]["postcondition"] =
		compactA11yPostconditionEvidence(action.postcondition, postcondition)

	result := service.stopA11yBatchForActionPostcondition(
		&a11yBatchContext{windowRef: "w1"},
		0,
		2,
		action,
		actionEvidence,
		actionResult,
		postcondition,
		observation{mode: observeScreenshot},
		false,
	)
	if !result.IsError {
		t.Fatalf("failed action guard must fail the incomplete batch: %#v", result)
	}
	if text := resultText(result); strings.Contains(text, `observe="none"`) ||
		strings.Contains(text, "observe: none") {
		t.Fatalf("current guard observation retained superseded placeholder: %q", text)
	}
	for key, expected := range map[string]any{
		"failure_code":                        "action_postcondition_not_satisfied",
		"failure_phase":                       "between_actions",
		"observation_required":                false,
		"next_identical_observation_required": false,
		"final_observation_returned":          true,
	} {
		if result.StructuredContent[key] != expected {
			t.Fatalf("%s=%#v want %#v in %#v",
				key, result.StructuredContent[key], expected,
				result.StructuredContent)
		}
	}
	continuation := result.StructuredContent["continuation"].(map[string]any)
	if continuation["state"] != "predicate_false" ||
		continuation["route"] != "continue_from_observation" {
		t.Fatalf("false predicate continuation=%#v", continuation)
	}

	prepared := prepareAXEvidence(result)
	batch := prepared.StructuredContent["batch"].(map[string]any)
	if batch["guard_failed_action_index"] != 0 ||
		batch["stopped_before_action_index"] != 1 {
		t.Fatalf("guard stop position was hidden: %#v", batch)
	}
	actions := batch["actions"].([]map[string]any)
	if len(actions) != 1 || actions[0]["postcondition"] == nil {
		t.Fatalf("failed action guard evidence was hidden: %#v", batch)
	}
}

func TestUnknownActionPostconditionRecoversCurrentWindowAfterSourceDisappears(
	t *testing.T,
) {
	service, appRef, sourceWindowRef, _ := candidateABatchService(t)
	current := beta14LocalSnapshot()
	current.App = appDescriptor{
		Name:              "Editor",
		ProviderBusName:   ":1.84",
		ProcessGeneration: "pid:10:start:100",
	}
	current.WindowTitle = "Document"
	current.WindowHandle = "w2:11111111111111111111111111111111"
	current.SnapshotID = "current"
	backend := &recordingPlatformBackend{
		accessibilityResponses: []ProviderAccessibilityResult{
			{
				OK:    false,
				Error: "stale_window_ref: source dialog is gone",
				Failure: &ProviderFailure{
					Code:          "stale_window_ref",
					Phase:         "precondition",
					FactsComplete: true,
					Subject:       "window",
				},
				RecoveryContext: &ProviderRecoveryContext{
					SourceAppStatus:    "alive",
					SourceWindowStatus: "gone",
					InventoryComplete:  true,
				},
			},
			{
				OK:       true,
				Snapshot: current,
			},
		},
	}
	service.backend = backend
	context := &a11yBatchContext{
		appRef:       appRef,
		windowRef:    sourceWindowRef,
		appToken:     appRef,
		windowHandle: "w2:0123456789abcdef0123456789abcdef",
	}
	postcondition := toolCallResult{
		Content: []contentItem{{
			Type: "text",
			Text: "assert_ax observation_incomplete",
		}},
		IsError: true,
		StructuredContent: map[string]any{
			"assertion_status": "unknown",
			"failure_code":     "observation_incomplete",
		},
	}
	actionResult := toolCallResult{
		StructuredContent: map[string]any{
			"input_events_emitted": true,
		},
	}
	action := a11yBatchAction{
		actionType: "click",
		postcondition: map[string]any{
			"type": "state",
			"target": map[string]any{
				"ref": "r1",
			},
			"state": "selected",
			"value": true,
		},
	}
	actionEvidence := []map[string]any{
		batchActionEvidenceEntry(0, "click", actionResult),
	}

	result := service.stopA11yBatchForActionPostcondition(
		context,
		0,
		2,
		action,
		actionEvidence,
		actionResult,
		postcondition,
		observation{mode: observeDiff},
		false,
	)
	if !result.IsError {
		t.Fatalf("unknown action postcondition must remain a failed batch: %#v",
			result)
	}
	for key, expected := range map[string]any{
		"failure_code":                        "action_postcondition_unknown",
		"failure_phase":                       "between_actions",
		"final_observation_returned":          true,
		"observation_required":                false,
		"next_identical_observation_required": false,
		"recovery_observation_attempted":      true,
		"recovery_observation_returned":       true,
		"cause_unattributed":                  true,
	} {
		if result.StructuredContent[key] != expected {
			t.Fatalf("%s=%#v want %#v in %#v",
				key, result.StructuredContent[key], expected,
				result.StructuredContent)
		}
	}
	continuation := result.StructuredContent["continuation"].(map[string]any)
	if continuation["state"] != "inspect_current_state" ||
		continuation["route"] != "continue_from_observation" ||
		continuation["retry_same_request"] != false {
		t.Fatalf("recovered continuation=%#v", continuation)
	}
	if len(backend.observeRequests) != 2 {
		t.Fatalf("observe calls=%d, want 2", len(backend.observeRequests))
	}
	if backend.observeRequests[0].WindowHandle != context.windowHandle ||
		backend.observeRequests[1].WindowHandle != "" {
		t.Fatalf("unexpected recovery observation targets: %#v",
			backend.observeRequests)
	}
	if text := resultText(result); !strings.Contains(text, `button "Save"`) ||
		!strings.Contains(text, "Fresh current state after an uncertain mutation") {
		t.Fatalf("fresh recovery observation missing: %q", text)
	}
}

func TestUnknownActionPostconditionRequiresObservationWhenNoneReturned(t *testing.T) {
	service := newService()
	postcondition := toolCallResult{
		Content: []contentItem{{
			Type: "text",
			Text: "assertion unavailable",
		}},
		IsError: true,
		StructuredContent: map[string]any{
			"assertion_status": "unknown",
			"failure_code":     "runtime_timeout",
		},
	}
	actionResult := toolCallResult{
		StructuredContent: map[string]any{
			"input_events_emitted": true,
		},
	}
	action := a11yBatchAction{
		actionType:    "click",
		postcondition: map[string]any{"type": "exists"},
	}
	actionEvidence := []map[string]any{
		batchActionEvidenceEntry(0, "click", actionResult),
	}

	result := service.stopA11yBatchForActionPostcondition(
		&a11yBatchContext{windowRef: "w1"},
		0,
		1,
		action,
		actionEvidence,
		actionResult,
		postcondition,
		observation{mode: observeNone},
		false,
	)
	if result.StructuredContent["failure_phase"] != "action_postcondition" ||
		result.StructuredContent["observation_required"] != true {
		t.Fatalf("unknown final guard evidence=%#v", result.StructuredContent)
	}
	continuation := result.StructuredContent["continuation"].(map[string]any)
	if continuation["state"] != "observation_required" ||
		continuation["route"] != "observe" {
		t.Fatalf("unknown guard continuation=%#v", continuation)
	}
}

func TestAllUntargetedBatchRequiresWindowRef(t *testing.T) {
	service, _, _, _ := candidateABatchService(t)
	_, err := service.deriveA11yBatchContext(
		"LibreOffice", "", "", []a11yBatchAction{{
			actionType: "keypress",
			key:        "CTRL+S",
		}},
	)
	if err == nil || !strings.Contains(err.Error(), "requires window_ref") {
		t.Fatalf("expected window_ref rejection, got %v", err)
	}
}

func TestCrossWindowBatchFailsBeforeInput(t *testing.T) {
	service, appRef, _, firstRef := candidateABatchService(t)
	secondHandle := "w2:fedcba9876543210fedcba9876543210"
	secondRef := service.refRegistry.Acquire(
		"libreoffice", secondHandle, `button "OK"`, CanonicalRole("button"),
		"OK", "c1",
		":1.84", "/window/2/ok", "pid:10:start:100",
		":1.84", "/window/2", "pid:10:start:100", "dialog", "Question",
		appRef,
	)
	_, err := service.deriveA11yBatchContext(
		"", "", "", []a11yBatchAction{
			{actionType: "click", targetRef: firstRef},
			{actionType: "click", targetRef: secondRef},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "multiple logical windows") {
		t.Fatalf("expected cross-window rejection, got %v", err)
	}
}

func TestRefOwnedBatchRejectsDuplicateLegacyAppAddress(t *testing.T) {
	service, _, _, elementRef := candidateABatchService(t)
	_, err := service.deriveA11yBatchContext(
		"LibreOffice", "", "", []a11yBatchAction{{
			actionType: "click",
			targetRef:  elementRef,
		}},
	)
	if err == nil || !strings.Contains(err.Error(), "single application identity") {
		t.Fatalf("expected duplicate app-address rejection, got %v", err)
	}
}

func TestRefOwnedBatchRejectsDuplicateAppOrWindowRef(t *testing.T) {
	service, appRef, windowRef, elementRef := candidateABatchService(t)
	for name, identity := range map[string]struct {
		appRef    string
		windowRef string
	}{
		"app_ref":    {appRef: appRef},
		"window_ref": {windowRef: windowRef},
	} {
		_, err := service.deriveA11yBatchContext(
			"", identity.appRef, identity.windowRef, []a11yBatchAction{{
				actionType: "click",
				targetRef:  elementRef,
			}},
		)
		if err == nil ||
			!strings.Contains(err.Error(), "must omit app_ref and window_ref") {
			t.Fatalf("%s duplicate identity was accepted: %v", name, err)
		}
	}
}

func TestPublicBatchRejectsSelectorTargets(t *testing.T) {
	_, err := normalizeA11yBatchActions([]any{map[string]any{
		"type": "click",
		"target": map[string]any{
			"selector": map[string]any{"role": "button", "name": "Save"},
		},
	}})
	if err == nil || !strings.Contains(err.Error(), "not part of the public act contract") {
		t.Fatalf("expected ref-only rejection, got %v", err)
	}
}

func TestSetValueThenClickRemainsSchemaValid(t *testing.T) {
	actions, err := normalizeA11yBatchActions([]any{
		map[string]any{
			"type":   "set_value",
			"target": map[string]any{"ref": "r98"},
			"value":  "5",
		},
		map[string]any{
			"type":   "set_value",
			"target": map[string]any{"ref": "r99"},
			"value":  "2",
		},
		map[string]any{
			"type":   "click",
			"target": map[string]any{"ref": "r100"},
		},
	})
	if err != nil || len(actions) != 3 {
		t.Fatalf("set_value followed by click must remain schema-valid: actions=%#v err=%v", actions, err)
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
		"rN actions need no app_ref/window_ref",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing ref-owned guidance %q in %q", want, text)
		}
	}
	if strings.Contains(text, "window-ref-unavailable=true") {
		t.Fatalf("ambiguous legacy window hint leaked: %q", text)
	}
}

func TestSharedNativeHandleDoesNotRebindModalRefToDocument(t *testing.T) {
	registry := NewWindowRefRegistry(16)
	handle := "w2:0123456789abcdef0123456789abcdef"
	dialogRef := registry.Acquire(
		"soffice", handle, `dialog "Document Recovery"`, "c1",
		":1.84", "/app/dialog/recovery", "pid:10:start:100",
		"dialog", "Document Recovery", "a1",
	)
	documentRef := registry.Acquire(
		"soffice", handle, `window "slides.pptx - LibreOffice Impress"`, "c2",
		":1.84", "/app/window/document", "pid:10:start:100",
		"window", "slides.pptx - LibreOffice Impress", "a1",
	)
	if dialogRef == "" || documentRef == "" || dialogRef == documentRef {
		t.Fatalf("logical replacement reused window ref: dialog=%q document=%q", dialogRef, documentRef)
	}
	if _, err := registry.Resolve(dialogRef, ""); err == nil ||
		!strings.Contains(err.Error(), "logical_window_replaced") {
		t.Fatalf("old modal ref was not tombstoned: %v", err)
	}
}

func TestSharedNativeHandleDoesNotRebindChooserRefToDocument(t *testing.T) {
	registry := NewWindowRefRegistry(16)
	handle := "w2:0123456789abcdef0123456789abcdef"
	chooserRef := registry.Acquire(
		"soffice", handle, `filechooser "Save As"`, "c1",
		":1.84", "/app/chooser/save", "pid:10:start:100",
		"file chooser", "Save As", "a1",
	)
	documentRef := registry.Acquire(
		"soffice", handle, `window "draft.odt - LibreOffice Writer"`, "c2",
		":1.84", "/app/window/document", "pid:10:start:100",
		"window", "draft.odt - LibreOffice Writer", "a1",
	)
	if chooserRef == "" || documentRef == "" || chooserRef == documentRef {
		t.Fatalf("logical replacement reused chooser ref: chooser=%q document=%q",
			chooserRef, documentRef)
	}
	if _, err := registry.Resolve(chooserRef, ""); err == nil ||
		!strings.Contains(err.Error(), "logical_window_replaced") {
		t.Fatalf("old chooser ref was not tombstoned: %v", err)
	}
}

func TestNativeDocumentWindowSurvivesTitleAndProviderRebuild(t *testing.T) {
	registry := NewWindowRefRegistry(16)
	handle := "w2:0123456789abcdef0123456789abcdef"
	first := registry.Acquire(
		"soffice", handle, `window "draft.odt - LibreOffice Writer"`, "c1",
		":1.84", "/app/window/old", "pid:10:start:100",
		"window", "draft.odt - LibreOffice Writer", "a1",
	)
	second := registry.Acquire(
		"soffice", handle, `window "final.odt - LibreOffice Writer"`, "c2",
		":1.84", "/app/window/rebuilt", "pid:10:start:100",
		"window", "final.odt - LibreOffice Writer", "a1",
	)
	if first == "" || second != first {
		t.Fatalf("stable native document window changed ref: first=%q second=%q", first, second)
	}
}

func TestTransientWindowSurvivesTitleOnlyChange(t *testing.T) {
	registry := NewWindowRefRegistry(16)
	handle := "w2:0123456789abcdef0123456789abcdef"
	first := registry.Acquire(
		"soffice", handle, `dialog "Options - General"`, "c1",
		":1.84", "/app/dialog/options", "pid:10:start:100",
		"dialog", "Options - General", "a1",
	)
	second := registry.Acquire(
		"soffice", handle, `dialog "Options - View"`, "c2",
		":1.84", "/app/dialog/options", "pid:10:start:100",
		"dialog", "Options - View", "a1",
	)
	if first == "" || second != first {
		t.Fatalf(
			"title-only modal mutation changed logical window ref: %q -> %q",
			first,
			second,
		)
	}
}

func TestTransientWindowRejectsSameRoleProviderReplacement(t *testing.T) {
	registry := NewWindowRefRegistry(16)
	handle := "w2:0123456789abcdef0123456789abcdef"
	first := registry.Acquire(
		"soffice", handle, `dialog "Question"`, "c1",
		":1.84", "/app/dialog/old", "pid:10:start:100",
		"dialog", "Question", "a1",
	)
	second := registry.Acquire(
		"soffice", handle, `dialog "Question"`, "c2",
		":1.84", "/app/dialog/later", "pid:10:start:100",
		"dialog", "Question", "a1",
	)
	if first == "" || second == "" || second == first {
		t.Fatalf(
			"replacement dialog reused logical window ref: %q -> %q",
			first,
			second,
		)
	}
	if _, err := registry.Resolve(first, ""); err == nil ||
		!strings.Contains(err.Error(), "logical_window_replaced") {
		t.Fatalf("old dialog ref was not tombstoned: %v", err)
	}
}

func TestBatchContextCarriesPrivateWindowExpectation(t *testing.T) {
	service, _, windowRef, elementRef := candidateABatchService(t)
	context, err := service.deriveA11yBatchContext(
		"", "", "", []a11yBatchAction{
			{actionType: "click", targetRef: elementRef},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if context.windowRef != windowRef || context.expectedWindow == nil {
		t.Fatalf("batch context lacks expected window: %#v", context)
	}
	if context.expectedWindow.WindowHandle != context.windowHandle ||
		context.expectedWindow.ProviderBusName == "" ||
		context.expectedWindow.ProcessGeneration == "" {
		t.Fatalf("incomplete expected window: %#v", context.expectedWindow)
	}
}

func TestBatchStopsAfterConfirmedWindowTransition(t *testing.T) {
	service, appRef, _, _ := candidateABatchService(t)
	context := &a11yBatchContext{
		appRef:       appRef,
		appToken:     appRef,
		windowHandle: "w2:0123456789abcdef0123456789abcdef",
	}
	service.snapshots[appRef] = &appSnapshot{
		WindowHandle: "w2:fedcba9876543210fedcba9876543210",
	}
	if got := service.a11yBatchWindowContinuity(context); got != a11yWindowChanged {
		t.Fatal("confirmed logical-window transition was not detected")
	}
}

func TestBatchAllowsProviderRebindInsideSameNativeWindow(t *testing.T) {
	service, appRef, windowRef, _ := candidateABatchService(t)
	handle := "w2:0123456789abcdef0123456789abcdef"
	context := &a11yBatchContext{
		appRef:       appRef,
		appToken:     appRef,
		windowRef:    windowRef,
		windowHandle: handle,
	}
	service.snapshots[appRef] = &appSnapshot{
		WindowHandle:    handle,
		publicWindowRef: windowRef,
		App: appDescriptor{
			ProviderBusName:   ":1.84",
			ProcessGeneration: "pid:10:start:100",
		},
	}
	if got := service.a11yBatchWindowContinuity(context); got != a11yWindowSame {
		t.Fatal("provider rebind inside the same native window must keep wN valid")
	}
}

func TestBatchStopsWhenWindowContinuityCannotBeSampled(t *testing.T) {
	service, appRef, windowRef, _ := candidateABatchService(t)
	context := &a11yBatchContext{
		appRef:       appRef,
		appToken:     appRef,
		windowRef:    windowRef,
		windowHandle: "w2:0123456789abcdef0123456789abcdef",
	}
	if got := service.a11yBatchWindowContinuity(context); got != a11yWindowUnknown {
		t.Fatalf("missing refreshed snapshot was reported as %s", got)
	}
	service.snapshots[appRef] = &appSnapshot{}
	if got := service.a11yBatchWindowContinuity(context); got != a11yWindowUnknown {
		t.Fatalf("missing refreshed window handle was reported as %s", got)
	}
}

func TestBatchStopsWhenLogicalWindowChangesOnSameNativeHandle(t *testing.T) {
	service, appRef, oldWindowRef, _ := candidateABatchService(t)
	handle := "w2:0123456789abcdef0123456789abcdef"
	newWindowRef := service.windowRefRegistry.Acquire(
		"soffice", handle, `dialog "Question"`, "c2",
		":1.84", "/app/dialog/question", "pid:10:start:100",
		"dialog", "Question", appRef,
	)
	if newWindowRef == "" || newWindowRef == oldWindowRef {
		t.Fatalf(
			"logical replacement did not receive a new ref: old=%q new=%q",
			oldWindowRef, newWindowRef,
		)
	}
	service.snapshots[appRef] = &appSnapshot{
		WindowHandle:    handle,
		publicAppRef:    appRef,
		publicWindowRef: newWindowRef,
	}
	context := &a11yBatchContext{
		appRef:       appRef,
		appToken:     appRef,
		windowRef:    oldWindowRef,
		windowHandle: handle,
	}
	if got := service.a11yBatchWindowContinuity(context); got != a11yWindowChanged {
		t.Fatalf("same-handle logical replacement was reported as %s", got)
	}
}

func TestPublicActIdentityErrorExplainsRefOwnership(t *testing.T) {
	service := newService()
	result := service.a11yRequest(map[string]any{
		"mode":    "act",
		"app_ref": "a1",
		"actions": []any{map[string]any{
			"type":   "click",
			"target": map[string]any{"ref": "r1"},
		}},
	}, observation{})
	if !result.IsError ||
		!strings.Contains(result.Content[0].Text, "target already owns") {
		t.Fatalf("public identity error=%#v", result)
	}
}

func TestPublicActRejectsInventedRefWithRecoveryGuidance(t *testing.T) {
	service := newService()
	result := service.a11yRequest(map[string]any{
		"mode": "act",
		"actions": []any{map[string]any{
			"type":   "click",
			"target": map[string]any{"ref": "r??"},
		}},
	}, observation{})
	if !result.IsError ||
		!strings.Contains(result.Content[0].Text, "never invent placeholders") {
		t.Fatalf("invented ref error=%#v", result)
	}
}

func TestReadIdentityDerivesOwnerFromElementRef(t *testing.T) {
	service, appRef, windowRef, elementRef := candidateABatchService(t)
	got, err := service.resolveA11yReadIdentity(
		map[string]any{}, elementRef,
	)
	if err != nil || got.appToken != appRef || got.windowRef != windowRef {
		t.Fatalf("owned assertion identity=%#v err=%v", got, err)
	}
	got, err = service.resolveA11yReadIdentity(
		map[string]any{"window_ref": windowRef}, elementRef,
	)
	if err != nil || got.appToken != appRef ||
		got.windowRef != windowRef || got.windowHandle == "" {
		t.Fatalf("window-scoped assertion identity=%#v err=%v", got, err)
	}
}

func TestReadIdentityRejectsConflictingAddressForms(t *testing.T) {
	service, appRef, windowRef, _ := candidateABatchService(t)
	for _, args := range []map[string]any{
		{"app": "LibreOffice", "app_ref": appRef},
		{"app_ref": appRef, "window_ref": windowRef},
	} {
		if _, err := service.resolveA11yReadIdentity(args, ""); err == nil {
			t.Fatalf("conflicting read identity accepted: %#v", args)
		}
	}
}

func TestIncompleteReadMarksRefForRevalidationNotTombstone(t *testing.T) {
	service, _, _, elementRef := candidateABatchService(t)
	service.runtimeReportedFailure("", axServiceRequest{
		SourceRef: elementRef,
		SemanticTarget: &semanticTarget{
			LocatorKind: AXLocatorProviderBound,
		},
	}, ProviderAccessibilityResult{
		FailureCode:          "observation_incomplete",
		FailureSubject:       "element",
		FailureFactsComplete: false,
		Error:                "temporary AX read failure",
	})
	entry := service.refRegistry.entries[elementRef]
	if entry == nil || entry.Lifecycle != AXRefNeedsRevalidation {
		t.Fatalf("incomplete read lifecycle=%#v", entry)
	}
	reissued := service.refRegistry.Acquire(
		entry.AppIdentity,
		entry.WindowHandle,
		entry.SemanticTarget,
		entry.Role,
		entry.Name,
		"c2",
		entry.ProviderBusName,
		entry.ProviderObjectPath,
		entry.ProcessGeneration,
		entry.ProviderWindowBusName,
		entry.ProviderWindowObjectPath,
		entry.ProviderWindowProcessGeneration,
		entry.ProviderWindowRole,
		entry.ProviderWindowName,
		entry.AppRef,
		entry.LogicalWindowRef,
	)
	if reissued != elementRef ||
		service.refRegistry.entries[elementRef].Lifecycle != AXRefActive {
		t.Fatalf("complete rediscovery did not reactivate %s", elementRef)
	}
}

func TestSemanticStaleMarksRefForRevalidation(t *testing.T) {
	service, _, _, elementRef := candidateABatchService(t)
	service.runtimeReportedFailure("", axServiceRequest{
		SourceRef: elementRef,
		SemanticTarget: &semanticTarget{
			LocatorKind: AXLocatorSemantic,
		},
	}, ProviderAccessibilityResult{
		FailureCode:          "stale_element_ref",
		FailureSubject:       "element",
		FailureFactsComplete: true,
		Error:                "provider object changed",
	})
	entry := service.refRegistry.entries[elementRef]
	if entry == nil || entry.Lifecycle != AXRefNeedsRevalidation {
		t.Fatalf("semantic stale lifecycle=%#v", entry)
	}
}

func TestElementIncompleteDoesNotInvalidateOwnerRefs(t *testing.T) {
	service, appRef, windowRef, elementRef := candidateABatchService(t)
	service.runtimeReportedFailure("", axServiceRequest{
		App:       appRef,
		SourceRef: elementRef,
	}, ProviderAccessibilityResult{
		FailureCode:          "observation_incomplete",
		FailureSubject:       "element",
		FailureFactsComplete: false,
		Error:                "temporary element scan failure",
	})
	if _, err := service.appRefRegistry.Resolve(appRef); err != nil {
		t.Fatalf("element failure invalidated app ref: %v", err)
	}
	if _, err := service.windowRefRegistry.Resolve(windowRef, ""); err != nil {
		t.Fatalf("element failure invalidated window ref: %v", err)
	}
	if entry := service.refRegistry.entries[elementRef]; entry == nil ||
		entry.Lifecycle != AXRefNeedsRevalidation {
		t.Fatalf("element ref lifecycle=%#v", entry)
	}
}

func TestWindowIncompleteDoesNotInvalidateAppOrElement(t *testing.T) {
	service, appRef, windowRef, elementRef := candidateABatchService(t)
	service.runtimeReportedFailure("", axServiceRequest{
		App:       appRef,
		SourceRef: windowRef,
	}, ProviderAccessibilityResult{
		FailureCode:          "observation_incomplete",
		FailureSubject:       "window",
		FailureFactsComplete: false,
		Error:                "temporary window inventory failure",
	})
	if _, err := service.appRefRegistry.Resolve(appRef); err != nil {
		t.Fatalf("window failure invalidated app ref: %v", err)
	}
	if entry := service.windowRefRegistry.entries[windowRef]; entry == nil ||
		entry.Lifecycle != AXRefNeedsRevalidation {
		t.Fatalf("window ref lifecycle=%#v", entry)
	}
	if _, err := service.refRegistry.Resolve(elementRef, ""); err != nil {
		t.Fatalf("window failure invalidated unrelated element ref: %v", err)
	}
}

func TestStaleElementDoesNotAdvertiseDifferentWindowAsReady(t *testing.T) {
	service, _, oldWindowRef, elementRef := candidateABatchService(t)
	newHandle := "w2:fedcba9876543210fedcba9876543210"
	result := service.runtimeReportedFailure("", axServiceRequest{
		SourceRef: elementRef,
		SemanticTarget: &semanticTarget{
			LocatorKind: AXLocatorProviderBound,
		},
	}, ProviderAccessibilityResult{
		FailureCode:          "stale_element_ref",
		FailureSubject:       "element",
		FailureFactsComplete: true,
		Error:                "provider window disappeared",
		ReplacementWindow: &ProviderReplacementWindow{
			WindowHandle:       newHandle,
			Title:              "Question",
			Role:               "dialog",
			ProviderBusName:    ":1.84",
			ProviderObjectPath: "/dialog/2",
			ProcessGeneration:  "pid:10:start:100",
		},
	})
	replacement, _ := result.StructuredContent["replacement_window_ref"].(string)
	if replacement != "" {
		t.Fatalf("different window lineage was advertised as replacement=%q", replacement)
	}
	if oldWindowRef == "" {
		t.Fatal("fixture did not create the original window ref")
	}
}

func TestStaleElementReturnsSameLineageWindowForFindOnlyRecovery(t *testing.T) {
	service, _, oldWindowRef, elementRef := candidateABatchService(t)
	source := service.refRegistry.entries[elementRef]
	result := service.runtimeReportedFailure("", axServiceRequest{
		SourceRef: elementRef,
		SemanticTarget: &semanticTarget{
			LocatorKind: AXLocatorProviderBound,
		},
	}, ProviderAccessibilityResult{
		FailureCode:          "stale_element_ref",
		FailureSubject:       "element",
		FailureFactsComplete: true,
		Error:                "provider object changed",
		ReplacementWindow: &ProviderReplacementWindow{
			WindowHandle:       source.WindowHandle,
			Title:              "Sheet",
			Role:               "window",
			ProviderBusName:    source.ProviderWindowBusName,
			ProviderObjectPath: "/window/rebuilt",
			ProcessGeneration:  source.ProviderWindowProcessGeneration,
		},
	})
	replacement, _ := result.StructuredContent["replacement_window_ref"].(string)
	if replacement != oldWindowRef {
		t.Fatalf(
			"same-lineage replacement=%q want=%q source=%#v result=%#v",
			replacement, oldWindowRef, source, result.StructuredContent,
		)
	}
	if result.StructuredContent["recovery"] != "find" {
		t.Fatalf("recovery=%#v", result.StructuredContent["recovery"])
	}
}

func TestEffectUnknownReturnsFreshWindowWithoutClaimingSuccess(t *testing.T) {
	service, _, oldWindowRef, elementRef := candidateABatchService(t)
	newHandle := "w2:fedcba9876543210fedcba9876543210"
	result := service.runtimeReportedFailure("", axServiceRequest{
		SourceRef: elementRef,
	}, ProviderAccessibilityResult{
		FailureCode:          "effect_unknown",
		FailurePhase:         "post_action_observation",
		InputEventsEmitted:   true,
		MutationUnknown:      true,
		FailureFactsComplete: false,
		Error:                "post_action_observation_failed",
		Recovery: &ProviderRecoveryContext{
			SourceAppStatus:    "alive",
			SourceWindowStatus: "gone",
			InventoryComplete:  true,
			CurrentWindow: &ProviderReplacementWindow{
				WindowHandle:       newHandle,
				Title:              "Document Recovery",
				Role:               "dialog",
				ProviderBusName:    ":1.84",
				ProviderObjectPath: "/window/recovery",
				ProcessGeneration:  "pid:10:start:100",
			},
		},
	})
	if !result.IsError ||
		result.StructuredContent["failure_code"] != "effect_unknown" ||
		result.StructuredContent["mutation_unknown"] != true {
		t.Fatalf("effect-unknown honesty was lost: %#v", result)
	}
	currentRef, _ := result.StructuredContent["current_window_ref"].(string)
	if !isShortAXWindowRef(currentRef) || currentRef == oldWindowRef {
		t.Fatalf("fresh recovery window ref=%q old=%q", currentRef, oldWindowRef)
	}
	if _, err := service.windowRefRegistry.Resolve(oldWindowRef, ""); err == nil {
		t.Fatal("confirmed-gone source window remained active")
	}
	if _, err := service.refRegistry.Resolve(elementRef, ""); err == nil {
		t.Fatal("element owned by confirmed-gone window remained active")
	}
	continuation := result.StructuredContent["continuation"].(map[string]any)
	if continuation["next_mode"] != "find" ||
		continuation["fresh_window_ref"] != currentRef {
		t.Fatalf("recovery continuation=%#v", continuation)
	}
}

func TestEffectUnknownAppExitTombstonesOwnedIdentityGraph(t *testing.T) {
	service, appRef, windowRef, elementRef := candidateABatchService(t)
	result := service.runtimeReportedFailure("", axServiceRequest{
		SourceRef: elementRef,
	}, ProviderAccessibilityResult{
		FailureCode:          "effect_unknown",
		FailurePhase:         "post_action_observation",
		InputEventsEmitted:   true,
		MutationUnknown:      true,
		FailureFactsComplete: false,
		Error:                "post_action_observation_failed",
		Recovery: &ProviderRecoveryContext{
			SourceAppStatus:    "exited",
			SourceWindowStatus: "gone",
			InventoryComplete:  true,
		},
	})
	if result.StructuredContent["recovery"] != "observe_apps" {
		t.Fatalf("app-exit recovery=%#v", result.StructuredContent)
	}
	if _, err := service.appRefRegistry.Resolve(appRef); err == nil {
		t.Fatal("confirmed-exited app remained active")
	}
	if _, err := service.windowRefRegistry.Resolve(windowRef, ""); err == nil {
		t.Fatal("window owned by confirmed-exited app remained active")
	}
	if _, err := service.refRegistry.Resolve(elementRef, ""); err == nil {
		t.Fatal("element owned by confirmed-exited app remained active")
	}
}

func TestIncompleteRecoveryDoesNotTombstoneIdentityGraph(t *testing.T) {
	service, appRef, windowRef, elementRef := candidateABatchService(t)
	result := service.runtimeReportedFailure("", axServiceRequest{
		SourceRef: elementRef,
	}, ProviderAccessibilityResult{
		FailureCode:          "effect_unknown",
		FailurePhase:         "post_action_observation",
		InputEventsEmitted:   true,
		MutationUnknown:      true,
		FailureFactsComplete: false,
		Error:                "post_action_observation_failed",
		Recovery: &ProviderRecoveryContext{
			SourceAppStatus:    "alive",
			SourceWindowStatus: "unknown",
			InventoryComplete:  false,
		},
	})
	if result.StructuredContent["recovery_inventory_complete"] != false {
		t.Fatalf("incomplete recovery=%#v", result.StructuredContent)
	}
	if _, err := service.appRefRegistry.Resolve(appRef); err != nil {
		t.Fatalf("incomplete inventory invalidated app: %v", err)
	}
	if _, err := service.windowRefRegistry.Resolve(windowRef, ""); err != nil {
		t.Fatalf("incomplete inventory invalidated window: %v", err)
	}
	if _, err := service.refRegistry.Resolve(elementRef, ""); err != nil {
		t.Fatalf("incomplete inventory invalidated element: %v", err)
	}
	if _, exists := result.StructuredContent["current_window_ref"]; exists {
		t.Fatalf("incomplete inventory advertised current window: %#v", result)
	}
}

func TestIncompleteStaleWindowRefRequiresRevalidation(t *testing.T) {
	service, _, windowRef, _ := candidateABatchService(t)
	result := service.runtimeReportedFailure("", axServiceRequest{
		SourceRef: windowRef,
	}, ProviderAccessibilityResult{
		FailureCode:          "stale_window_ref",
		FailureSubject:       "window",
		FailureFactsComplete: false,
		Error:                "provider could not resolve the source window",
		Recovery: &ProviderRecoveryContext{
			SourceAppStatus:    "alive",
			SourceWindowStatus: "unknown",
			InventoryComplete:  false,
		},
	})
	if !result.IsError ||
		result.StructuredContent["failure_code"] != "stale_window_ref" {
		t.Fatalf("stale window failure evidence was lost: %#v", result)
	}
	if _, err := service.windowRefRegistry.Resolve(windowRef, ""); err == nil ||
		!strings.Contains(err.Error(), string(AXRefNeedsRevalidation)) {
		t.Fatalf("stale window ref remained active after incomplete failure: %v", err)
	}
}

func TestWorkingProjectionHidesUnaddressableControlLeaf(t *testing.T) {
	plugin := removeUnaddressableControlsPlugin()
	hidden := &AXNode{
		ID: "button", Role: "button", SemanticClass: "control",
		AddressabilityEvaluated: true,
		AddressabilityReason:    "not_currently_visible",
	}
	visible := &AXNode{
		ID: "button-ref", Role: "button", SemanticClass: "control",
		Name: "Open", Ref: "r1",
		AddressabilityEvaluated: true,
	}
	stateful := &AXNode{
		ID: "checkbox", Role: "checkbox", SemanticClass: "control",
		Name:                    "Enabled",
		States:                  AXStates{Checked: CheckedTrue},
		AddressabilityEvaluated: true,
	}
	named := &AXNode{
		ID: "named-button", Role: "button", SemanticClass: "control",
		Name:                    "Save",
		AddressabilityEvaluated: true,
		AddressabilityReason:    "stable_identity_unavailable",
	}
	roots, removed, _, err := plugin.Apply(
		&DistillationContext{Snapshot: &AXSnapshotV1{
			AppRef: "a1", WindowRef: "w1",
		}}, []*AXNode{hidden, visible, stateful, named},
	)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 || len(roots) != 3 ||
		roots[0].Ref != "r1" || roots[1].ID != "checkbox" ||
		roots[2].ID != named.ID {
		t.Fatalf("projection roots=%#v removed=%d", roots, removed)
	}
	if label := renderAXNodeLabel(stateful); !strings.Contains(
		label, "[addressable=false]",
	) {
		t.Fatalf("stateful label=%q", label)
	}
	if label := renderAXNodeLabel(named); !strings.Contains(
		label, "[addressable=false]",
	) {
		t.Fatalf("visible identity-incomplete control lacked marker: %q", label)
	}
}

func TestAssignSnapshotRefsRecordsAddressabilityReason(t *testing.T) {
	button := &AXNode{
		ID: "button", Role: "button", SemanticClass: "control",
		Name: "Save",
	}
	snapshot := &AXSnapshotV1{
		SchemaVersion:  "AXSnapshotV1",
		AppRef:         "a1",
		WindowRef:      "w1",
		CanonicalRoots: []*AXNode{button},
		Roots:          cloneAXForest([]*AXNode{button}, nil),
		ByID:           map[string]*AXNode{"button": button},
	}
	NewRefRegistry(8).AssignSnapshotRefs(snapshot)
	projected := snapshot.Roots[0]
	if projected.Ref != "" || projected.AddressabilityReason == "" {
		t.Fatalf("projected addressability=%#v", projected)
	}
	if button.AddressabilityReason != projected.AddressabilityReason {
		t.Fatalf("canonical reason=%q projected=%q",
			button.AddressabilityReason, projected.AddressabilityReason)
	}
}

func TestProjectedSubtreePreservesOwnerRefsForAddressability(t *testing.T) {
	button := &AXNode{
		ID: "button", Role: "button", SemanticClass: "control",
		Name:                    "Save",
		AddressabilityEvaluated: true,
		AddressabilityReason:    "stable_identity_unavailable",
	}
	typed := &AXSnapshotV1{
		SchemaVersion:  "AXSnapshotV1",
		AppRef:         "a7",
		WindowRef:      "w9",
		CanonicalRoots: []*AXNode{button},
		Roots:          cloneAXForest([]*AXNode{button}, nil),
		ByID:           map[string]*AXNode{"button": button},
		ByIndex:        map[int]*AXNode{1: button},
	}
	record := &elementRecord{Index: 1}
	snapshot := &appSnapshot{axSnapshot: typed}
	lines, err := projectedSubtreeLines(snapshot, record)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(lines, "\n")
	if !strings.Contains(text, `button "Save" [addressable=false]`) {
		t.Fatalf("subtree lost owner-scoped addressability:\n%s", text)
	}
}

func TestCompleteProviderAbsenceTombstonesRef(t *testing.T) {
	service, _, _, elementRef := candidateABatchService(t)
	entry := service.refRegistry.entries[elementRef]
	entry.LocatorKind = AXLocatorProviderBound
	service.runtimeReportedFailure("", axServiceRequest{
		SourceRef: elementRef,
		SemanticTarget: &semanticTarget{
			LocatorKind: AXLocatorProviderBound,
		},
	}, ProviderAccessibilityResult{
		FailureCode:          "stale_element_ref",
		FailureFactsComplete: true,
		Error:                "target absent after a complete scan",
	})
	if entry.Lifecycle != AXRefStale {
		t.Fatalf("complete absence lifecycle=%s", entry.Lifecycle)
	}
	if _, err := service.refRegistry.Resolve(elementRef, ""); err == nil {
		t.Fatal("tombstoned ref resolved")
	}
}

func TestAppTombstoneCascadesToOwnedWindowsAndElements(t *testing.T) {
	service, appRef, windowRef, elementRef := candidateABatchService(t)
	service.markAppIdentityLifecycle(appRef, AXRefStale, "process_restarted")
	if _, err := service.appRefRegistry.Resolve(appRef); err == nil {
		t.Fatal("stale app ref resolved")
	}
	if _, err := service.windowRefRegistry.Resolve(windowRef, ""); err == nil {
		t.Fatal("owned window survived stale app")
	}
	if _, err := service.refRegistry.Resolve(elementRef, ""); err == nil {
		t.Fatal("owned element survived stale app")
	}
}

func TestAmbiguityCandidateUsesItsOwnWindowRef(t *testing.T) {
	service, appRef, oldWindowRef, _ := candidateABatchService(t)
	oldHandle := "w2:0123456789abcdef0123456789abcdef"
	newHandle := "w2:fedcba9876543210fedcba9876543210"
	service.snapshots[appRef] = &appSnapshot{
		App: appDescriptor{
			Name:              "LibreOffice",
			ProviderBusName:   ":1.84",
			ProcessGeneration: "pid:10:start:100",
		},
		WindowHandle:    oldHandle,
		publicAppRef:    appRef,
		publicWindowRef: oldWindowRef,
		SnapshotID:      "c1",
	}
	result := service.runtimeReportedFailure(appRef, axServiceRequest{}, ProviderAccessibilityResult{
		Error:          "semantic_target_ambiguous",
		FailureCode:    "semantic_target_ambiguous",
		CandidateCount: 1,
		Candidates: []ProviderTargetCandidate{{
			Role:               "push button",
			Name:               "OK",
			Path:               `dialog "Question" > button "OK"`,
			WindowHandle:       newHandle,
			ProviderBusName:    ":1.84",
			ProviderObjectPath: "/dialog/ok",
			ProcessGeneration:  "pid:10:start:100",
		}},
	})
	candidates := result.StructuredContent["candidates"].([]map[string]any)
	gotWindow := candidates[0]["window_ref"].(string)
	if gotWindow == oldWindowRef {
		t.Fatalf("new-window candidate inherited old window ref %s", oldWindowRef)
	}
	entry, err := service.windowRefRegistry.Resolve(gotWindow, "")
	if err != nil || entry.WindowHandle != newHandle {
		t.Fatalf("candidate window binding=%#v err=%v", entry, err)
	}
}

func TestProviderIdentityDetailsStayOutOfPublicFailureText(t *testing.T) {
	response := ProviderAccessibilityResult{
		Error: "provider_identity_ambiguous path='/org/a11y/atspi/accessible/77' " +
			"bus=:1.84 pid=4242 process_generation=pid:4242:start:9 " +
			"window=w2:0123456789abcdef0123456789abcdef",
	}
	text := publicRuntimeFailureText(response)
	if strings.Contains(text, "/org/a11y/") ||
		strings.Contains(text, ":1.84") ||
		strings.Contains(text, "4242") ||
		strings.Contains(text, "w2:") ||
		strings.Contains(text, "provider_identity_ambiguous") {
		t.Fatalf("provider identity leaked: %q", text)
	}
}

func TestDelimitAXTextContentPreservesItemBoundaries(t *testing.T) {
	result := toolCallResult{
		Content: []contentItem{
			{Type: "text", Text: "observe: none"},
			{Type: "text", Text: "native transition"},
			{Type: "image", Data: "ignored"},
		},
	}
	result = delimitAXTextContent(result)
	if got := result.Content[0].Text; got != "observe: none\n" {
		t.Fatalf("first text item was not delimited: %q", got)
	}
	if got := result.Content[1].Text; got != "native transition\n" {
		t.Fatalf("second text item was not delimited: %q", got)
	}
}

func TestContinuationTextIncludesRoute(t *testing.T) {
	result := appendAXContinuationText(toolCallResult{
		Content: []contentItem{{Type: "text", Text: "observe: none\n"}},
	}, map[string]any{
		"state":              "ready",
		"retry_same_request": false,
		"route":              "GUI",
		"next_mode":          "screenshot",
	})
	text := result.Content[0].Text
	if !strings.Contains(text, "route=GUI") {
		t.Fatalf("continuation omitted route: %q", text)
	}
}

func TestPlatformNeutralModulesDoNotOwnLinuxExecution(t *testing.T) {
	files := []string{
		"main.go",
		"ax_batch.go",
		"ax_snapshot_store.go",
		"ax_action_service.go",
		"ax_action_evidence.go",
		"ax_trace.go",
		"ax_action_result.go",
		"ax_failure_recovery.go",
		"ax_snapshot_refresh.go",
		"ax_lookup.go",
		"ax_projection.go",
		"gui_service.go",
		"tool_contract.go",
		"mcp_transport.go",
		"runtime_backend.go",
		"runtime_protocol.go",
		"platform_contract.go",
	}
	forbidden := []string{
		`"os/exec"`,
		`"/proc/`,
		`exec.Command`,
		`xprop`,
		`wmctrl`,
	}
	for _, name := range files {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, marker := range forbidden {
			if strings.Contains(string(body), marker) {
				t.Fatalf("%s contains platform execution marker %q", name, marker)
			}
		}
	}
}
