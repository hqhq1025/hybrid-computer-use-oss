package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

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

func TestFocusedFallbackTriggersOneContentStabilizationRead(t *testing.T) {
	service := newService()
	previous := editorFormSnapshot()
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

	current := editorFormSnapshot()
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

func TestPostActionWindowIdentityStabilizationReturnsActionableNewWindow(t *testing.T) {
	oldHandle := "w2:" + strings.Repeat("a", 32)
	newHandle := "w2:" + strings.Repeat("b", 32)
	before := windowTransitionSnapshot(oldHandle, "Document", true)
	immediate := windowTransitionSnapshot(newHandle, "Rename", true)
	immediate.Elements[0].ControlType = "panel"
	stable := windowTransitionSnapshot(newHandle, "Rename", true)
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
	dialog := windowTransitionSnapshot(
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
	modalFrame := windowTransitionSnapshot(
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

func TestTransientCommitProbeDoesNotAdoptSiblingWindow(t *testing.T) {
	oldDelays := transientCommitProbeDelays
	transientCommitProbeDelays = []time.Duration{0, 0, 0}
	t.Cleanup(func() {
		transientCommitProbeDelays = oldDelays
	})

	oldHandle := "w2:" + strings.Repeat("a", 32)
	dialog := windowTransitionSnapshot(oldHandle, "Open File", true)
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
	dialog := windowTransitionSnapshot(oldHandle, "Open File", true)
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
	before := windowTransitionSnapshot(oldHandle, "Document", true)
	immediate := windowTransitionSnapshot(newHandle, "Rename", true)
	immediate.Elements[0].ControlType = "panel"
	other := windowTransitionSnapshot(otherHandle, "Other", true)

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
	before := windowTransitionSnapshot(oldHandle, "Document", true)
	ready := windowTransitionSnapshot(newHandle, "Rename", true)
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
	before := windowTransitionSnapshot(oldHandle, "Document", true)
	immediate := windowTransitionSnapshot(oldHandle, "Rename", true)
	immediate.Elements[0].ControlType = "panel"
	stable := windowTransitionSnapshot(newHandle, "Rename", true)

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
	before := windowTransitionSnapshot(oldHandle, "Document", true)
	immediate := windowTransitionSnapshot(oldHandle, "Rename", true)
	immediate.Elements[0].ControlType = "panel"
	stale := windowTransitionSnapshot(oldHandle, "Rename", true)

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
	before := windowTransitionSnapshot(oldHandle, "Document", true)
	immediate := windowTransitionSnapshot(oldHandle, "Rename", true)
	immediate.Elements[0].ControlType = "panel"
	sibling := windowTransitionSnapshot(newHandle, "Rename", true)
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

func TestPostActionWindowIdentityStabilizationRecoversProviderReplacement(t *testing.T) {
	handle := "w2:" + strings.Repeat("a", 32)
	before := windowTransitionSnapshot(handle, "Rename", true)
	immediate := windowTransitionSnapshot(handle, "Rename", true)
	for index := range immediate.Elements {
		immediate.Elements[index].ProviderWindowObjectPath = "/replacement-dialog"
	}
	stable := windowTransitionSnapshot(handle, "Rename", true)
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
	before := windowTransitionSnapshot(oldHandle, "Document", true)
	immediate := windowTransitionSnapshot(oldHandle, "Opening", true)
	immediate.Elements[0].ControlType = "panel"
	stable := windowTransitionSnapshot(newHandle, "Ready", true)
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
