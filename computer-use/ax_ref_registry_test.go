package main

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

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

func TestFinalObservationContinuationRejectsDifferentAppOwner(t *testing.T) {
	service, appRef, sourceWindowRef, _ := sheetBatchService(t)
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

func TestPostActionWindowIdentityStabilizationRejectedProbeIsTransactional(t *testing.T) {
	oldHandle := "w2:" + strings.Repeat("a", 32)
	newHandle := "w2:" + strings.Repeat("b", 32)
	before := windowTransitionSnapshot(oldHandle, "Document", true)
	immediate := windowTransitionSnapshot(oldHandle, "Rename", true)
	immediate.Elements[0].ControlType = "panel"
	sibling := windowTransitionSnapshot(newHandle, "Rename", true)
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

func TestIncompleteReadMarksRefForRevalidationNotTombstone(t *testing.T) {
	service, _, _, elementRef := sheetBatchService(t)
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
