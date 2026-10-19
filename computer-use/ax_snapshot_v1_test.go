package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestRefRegistryIsMonotonicPersistentAndExplicitlyExpires(t *testing.T) {
	registry := NewRefRegistry(2)
	makeRef := func(name string) string {
		return registry.Acquire(
			"editor", "window-1",
			encodeScopedSemanticRef(
				`push button "`+name+`"`, "window-1",
			),
			"button", name, "rev",
		)
	}
	first := makeRef("One")
	second := makeRef("Two")
	if first != "r1" || second != "r2" {
		t.Fatalf("unexpected initial refs: %q %q", first, second)
	}
	if again := makeRef("One"); again != first {
		t.Fatalf("stable identity was reissued: %q -> %q", first, again)
	}
	third := makeRef("Three")
	if third != "r3" {
		t.Fatalf("registry reused an old id: %q", third)
	}
	if _, err := registry.Resolve(second, "editor"); err == nil ||
		!strings.Contains(err.Error(), "expired") {
		t.Fatalf("evicted ref did not report expired: %v", err)
	}
	if replacement := makeRef("Two"); replacement != "r4" {
		t.Fatalf("expired identity reused its old ref: %q", replacement)
	}
}

func TestRoleOntologyAndTypedStatesDoNotInventFacts(t *testing.T) {
	record := &elementRecord{
		ControlType: "internal frame",
		States:      "[focused checked]",
		Actions:     []string{"click", "set value"},
	}
	descriptor := canonicalRoleDescriptor(record.ControlType, nil)
	if descriptor.ModelRole != RoleGeneric {
		t.Fatalf("unknown provider role became %q", descriptor.ModelRole)
	}
	states := typedAXStates(record)
	if states.Active != TriTrue || states.Checked != CheckedTrue {
		t.Fatalf("typed states lost explicit facts: %#v", states)
	}
	if states.Disabled != TriUnknown {
		t.Fatalf("missing enabled/disabled was inferred as %q", states.Disabled)
	}
	capabilities := typedAXCapabilities(record, descriptor.ModelRole)
	if capabilities.Activate != ActivationSemantic || !capabilities.SetValue {
		t.Fatalf("actions were not separated into capabilities: %#v", capabilities)
	}
}

func TestDistillationPipelineReportsEveryStageAndPreservesTargets(t *testing.T) {
	snapshot := &appSnapshot{
		App:          appDescriptor{Name: "Editor"},
		WindowHandle: "window-1",
		SnapshotID:   "rev1",
		Elements: []elementRecord{
			{Index: 1, RuntimeID: []int{0}, ControlType: "frame", Name: "Editor"},
			{Index: 2, RuntimeID: []int{0, 0}, ControlType: "panel"},
			{Index: 3, RuntimeID: []int{0, 0, 0}, ControlType: "push button", Name: "Save", Actions: []string{"click"}},
		},
	}
	typed, err := buildAXSnapshotV1(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	NewRefRegistry(32).AssignSnapshotRefs(typed)
	if err := distillAXSnapshot(typed); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"normalize_whitespace",
		"remove_empty_decorative_leaves",
		"remove_unaddressable_controls",
		"coalesce_text",
		"unwrap_neutral_single_child",
		"remove_relation_proven_duplicate_labels",
		"suppress_redundant_cohort_properties",
		"summarize_homogeneous_collections",
		"shorten_named_descriptions",
		"apply_output_budget",
	}
	if len(typed.Distillation) != len(want) {
		t.Fatalf("stage reports=%d want=%d", len(typed.Distillation), len(want))
	}
	for index, report := range typed.Distillation {
		if report.Name != want[index] {
			t.Fatalf("stage %d=%q want %q", index, report.Name, want[index])
		}
		if report.TargetRecall != report.TargetCount || report.InvariantFailure != "" {
			t.Fatalf("stage invariant failed: %#v", report)
		}
	}
}

func TestBuildAndRenderAssignsRefsBeforeDistillation(t *testing.T) {
	service := newService()
	snapshot := &appSnapshot{
		App: appDescriptor{
			Name:              "Editor",
			ProviderBusName:   ":1.42",
			ProcessGeneration: "42",
		},
		WindowHandle: "w2:" + strings.Repeat("a", 32),
		SnapshotID:   "revision-1",
		Elements: []elementRecord{
			{
				Index: 1, RuntimeID: []int{0},
				ControlType: "frame", Name: "Editor",
			},
			{
				Index: 2, RuntimeID: []int{0, 0},
				ControlType: "push button", Name: "Save",
				Actions: []string{"click"},
			},
		},
	}
	if err := service.buildAndRenderAXSnapshotV1(
		snapshot,
		treeViewWorking,
	); err != nil {
		t.Fatal(err)
	}
	if snapshot.axSnapshot == nil {
		t.Fatal("working projection did not retain its typed snapshot")
	}
	button := snapshot.axSnapshot.ByIndex[2]
	if button == nil || button.Ref == "" || !button.AddressabilityEvaluated {
		t.Fatalf(
			"canonical button was not addressed before projection: app_ref=%q window_ref=%q button=%#v",
			snapshot.axSnapshot.AppRef,
			snapshot.axSnapshot.WindowRef,
			button,
		)
	}
	for _, report := range snapshot.axSnapshot.Distillation {
		if report.TargetCount == 0 || report.TargetRecall != report.TargetCount {
			t.Fatalf("distillation ran without assigned target truth: %#v", report)
		}
	}
	if !strings.Contains(
		strings.Join(snapshot.TreeLines, "\n"),
		`button "Save" [ref=`,
	) {
		t.Fatalf("working projection lost the assigned ref:\n%s",
			strings.Join(snapshot.TreeLines, "\n"))
	}
}

func TestCollectionSummaryPreservesItemsWithAddressableDescendants(t *testing.T) {
	cases := []struct {
		name       string
		parentRole CanonicalRole
		itemRole   CanonicalRole
	}{
		{name: "list item", parentRole: "list", itemRole: "listitem"},
		{name: "grid cell", parentRole: "grid", itemRole: "gridcell"},
		{name: "tree item", parentRole: "tree", itemRole: "treeitem"},
		{name: "article", parentRole: "document", itemRole: "article"},
		{name: "option", parentRole: "list", itemRole: "option"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			parent := &AXNode{ID: "0", Role: testCase.parentRole}
			for index := 0; index < 12; index++ {
				item := &AXNode{
					ID:     fmt.Sprintf("0/%d", index),
					Role:   testCase.itemRole,
					Parent: parent,
				}
				if index == 6 {
					group := &AXNode{
						ID:     "0/6/0",
						Role:   "group",
						Parent: item,
					}
					target := &AXNode{
						ID:     "0/6/0/0",
						Role:   "link",
						Name:   "Open result",
						Ref:    "r7",
						Parent: group,
					}
					group.Children = []*AXNode{target}
					item.Children = []*AXNode{group}
				}
				parent.Children = append(parent.Children, item)
			}
			canonical := cloneAXForest([]*AXNode{parent}, nil)
			snapshot := &AXSnapshotV1{
				SchemaVersion:  "AXSnapshotV1",
				CanonicalRoots: canonical,
				Roots:          []*AXNode{parent},
			}
			before := countAXNodes(snapshot.Roots)
			if err := distillAXSnapshot(snapshot); err != nil {
				t.Fatalf("collection summarization lost a descendant target: %v", err)
			}
			if got := targetRecall(
				collectAXTargetIDs(snapshot.CanonicalRoots),
				snapshot.Roots,
			); got != 1 {
				t.Fatalf("descendant target recall=%d want 1", got)
			}
			foundTarget := false
			foundSummary := false
			var visit func([]*AXNode)
			visit = func(nodes []*AXNode) {
				for _, node := range nodes {
					foundTarget = foundTarget || node.ID == "0/6/0/0"
					foundSummary = foundSummary ||
						(node.Synthetic &&
							strings.Contains(node.Properties.Omitted, "collection role="))
					visit(node.Children)
				}
			}
			visit(snapshot.Roots)
			if !foundTarget {
				t.Fatal("addressable descendant was removed from the projection")
			}
			if !foundSummary || countAXNodes(snapshot.Roots) >= before {
				t.Fatalf(
					"target-free middle items were not summarized: before=%d after=%d",
					before,
					countAXNodes(snapshot.Roots),
				)
			}
		})
	}
}

func TestRendererMarksRetainedUnaddressableInteractiveRoles(t *testing.T) {
	cases := []*AXNode{
		{
			ID: "menu", Role: "menu", SemanticClass: "boundary",
			Name: "File", States: AXStates{Expanded: TriFalse},
			AddressabilityEvaluated: true,
		},
		{
			ID: "control", Role: "button", SemanticClass: "control",
			Name: "Save", AddressabilityEvaluated: true,
		},
		{
			ID: "item", Role: "listitem", SemanticClass: "item",
			Name: "Inbox", AddressabilityEvaluated: true,
		},
	}
	for _, node := range cases {
		if label := renderAXNodeLabel(node); !strings.Contains(
			label,
			"[addressable=false]",
		) {
			t.Fatalf("%s without ref lacked addressability marker: %q",
				node.Role, label)
		}
	}
	passive := &AXNode{
		ID: "group", Role: "group", SemanticClass: "structure",
		Name: "Layout", AddressabilityEvaluated: true,
	}
	if label := renderAXNodeLabel(passive); strings.Contains(
		label,
		"[addressable=false]",
	) {
		t.Fatalf("passive structure received an interaction marker: %q", label)
	}
	unknown := &AXNode{
		ID: "unknown", Role: "button", SemanticClass: "control",
		Name: "Pending",
	}
	if label := renderAXNodeLabel(unknown); !strings.Contains(
		label,
		"[addressable=unknown]",
	) {
		t.Fatalf("unassessed interaction lacked unknown marker: %q", label)
	}
}

func TestCoalesceTextPreservesNameContent(t *testing.T) {
	parent := &AXNode{ID: "parent", Role: "group"}
	first := &AXNode{ID: "first", Role: RoleText, Name: "Hello"}
	second := &AXNode{ID: "second", Role: RoleText, Name: "World"}
	parent.Children = []*AXNode{first, second}
	snapshot := &AXSnapshotV1{
		SchemaVersion:  "AXSnapshotV1",
		CanonicalRoots: []*AXNode{parent},
		Roots:          cloneAXForest([]*AXNode{parent}, nil),
		ByID:           map[string]*AXNode{},
		ByIndex:        map[int]*AXNode{},
	}
	if err := distillAXSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Roots) != 1 ||
		snapshot.Roots[0].Name != "Hello World" {
		t.Fatalf("coalesced text lost accessible names: %#v", snapshot.Roots)
	}
}

func TestSemanticDiffDoesNotCascadeAfterListInsertion(t *testing.T) {
	node := func(id, ref, fingerprint, name string) *AXNode {
		return &AXNode{
			ID: id, Ref: ref, Role: "listitem", Name: name,
			SemanticFingerprint: fingerprint,
		}
	}
	before := &AXSnapshotV1{Roots: []*AXNode{
		node("a", "r1", "f1:a", "A"),
		node("b", "r2", "f1:b", "B"),
	}}
	after := &AXSnapshotV1{Roots: []*AXNode{
		node("x", "r3", "f1:x", "X"),
		node("a2", "r1", "f1:a", "A"),
		node("b2", "r2", "f1:b", "B"),
	}}
	changes := diffAXSnapshots(before, after)
	if len(changes) != 1 || changes[0].Kind != AXDiffAdded ||
		changes[0].Key != "r3" {
		t.Fatalf("list insertion cascaded across stable items: %#v", changes)
	}
	if err := assertStableDiffIdentity(before, after); err != nil {
		t.Fatal(err)
	}
}

func TestSemanticDiffUsesSequenceAlignmentForRepeatedIdentitylessItems(t *testing.T) {
	item := func(id string) *AXNode {
		return &AXNode{
			ID: id, Role: "listitem", Name: "Repeated",
			SemanticFingerprint: "f1:repeated",
		}
	}
	before := &AXSnapshotV1{Roots: []*AXNode{
		item("old-1"),
		item("old-2"),
		item("old-3"),
	}}
	after := &AXSnapshotV1{Roots: []*AXNode{
		item("new-item"),
		item("old-1-after"),
		item("old-2-after"),
		item("old-3-after"),
	}}
	changes := diffAXSnapshots(before, after)
	if len(changes) != 1 || changes[0].Kind != AXDiffAdded {
		t.Fatalf("repeated cohort insertion cascaded: %#v", changes)
	}
}

func TestSemanticDiffIgnoresWrapperMovesAndKeepsPropertyChange(t *testing.T) {
	parent := func(id, ref, fingerprint string, child *AXNode) *AXNode {
		node := &AXNode{
			ID: id, Ref: ref, Role: "group",
			SemanticFingerprint: fingerprint,
			Children:            []*AXNode{child},
		}
		child.Parent = node
		return node
	}
	beforeChild := &AXNode{
		ID: "item-before", Ref: "r3", Role: "button", Name: "Save",
		SemanticFingerprint: "f1:save",
	}
	afterChild := &AXNode{
		ID: "item-after", Ref: "r3", Role: "button", Name: "Save",
		SemanticFingerprint: "f1:save",
	}
	before := &AXSnapshotV1{Roots: []*AXNode{
		parent("left", "r1", "f1:left", beforeChild),
		{ID: "right", Ref: "r2", Role: "group", SemanticFingerprint: "f1:right"},
	}}
	after := &AXSnapshotV1{Roots: []*AXNode{
		{ID: "left2", Ref: "r1", Role: "group", SemanticFingerprint: "f1:left"},
		parent("right2", "r2", "f1:right", afterChild),
	}}
	changes := diffAXSnapshots(before, after)
	if len(changes) != 0 {
		t.Fatalf("anonymous wrapper churn leaked into semantic diff: %#v", changes)
	}

	propertyAfter := *afterChild
	propertyAfter.Properties.Description = "Document saved"
	propertyAfter.Parent = before.Roots[0]
	propertySnapshot := &AXSnapshotV1{Roots: []*AXNode{
		parent("left3", "r1", "f1:left", &propertyAfter),
		before.Roots[1],
	}}
	changes = diffAXSnapshots(before, propertySnapshot)
	foundProperty := false
	for _, change := range changes {
		if change.Key == "r3" && change.Kind == AXDiffPropertyChanged {
			foundProperty = true
		}
	}
	if !foundProperty {
		t.Fatalf("property change was not classified: %#v", changes)
	}
}

func TestTypedActionabilityEvidenceDoesNotClaimDelivery(t *testing.T) {
	evidence := initialActionabilityEvidence(
		"click",
		&elementRecord{
			ControlType: "push button",
			Actions:     []string{"click"},
		},
		nil,
	)
	result := toolCallResult{StructuredContent: map[string]any{
		"input_events_emitted": true,
	}}
	final := finalizeActionabilityEvidence(
		evidence,
		axServiceRequest{Action: axClickCommand{}},
		result,
		true,
		nil,
	)
	if final.InputEmitted != TriTrue ||
		final.DeliveryVerified != TriUnknown ||
		final.PostconditionVerified != TriUnknown {
		t.Fatalf("actionability overclaimed evidence: %#v", final)
	}
}

func TestStaticContentDoesNotReceiveRefsOrBlockCollectionSummary(t *testing.T) {
	list := &AXNode{
		ID:   "list",
		Role: "list",
	}
	for index := 0; index < 20; index++ {
		child := &AXNode{
			ID:             fmt.Sprintf("item-%d", index),
			Role:           "listitem",
			Name:           fmt.Sprintf("Static item %d", index),
			SemanticTarget: fmt.Sprintf("s2:static-%d", index),
			Parent:         list,
		}
		list.Children = append(list.Children, child)
	}
	button := &AXNode{
		ID:             "button",
		Role:           "button",
		Name:           "Apply",
		SemanticTarget: "s2:apply",
		Capabilities: AXCapabilities{
			Activate: ActivationSemantic,
		},
		Parent: list,
	}
	list.Children = append(list.Children, button)
	snapshot := &AXSnapshotV1{
		SchemaVersion:  "AXSnapshotV1",
		AppRef:         "a1",
		AppIdentity:    "test",
		WindowHandle:   "window",
		WindowRef:      "w1",
		Revision:       "revision",
		CanonicalRoots: []*AXNode{list},
		Roots:          cloneAXForest([]*AXNode{list}, nil),
		ByID:           map[string]*AXNode{},
		ByIndex:        map[int]*AXNode{},
	}
	NewRefRegistry(64).AssignSnapshotRefs(snapshot)
	projectedButton := snapshot.Roots[0].Children[len(snapshot.Roots[0].Children)-1]
	if projectedButton.Ref == "" {
		t.Fatal("interactive button did not receive a ref")
	}
	for _, child := range list.Children[:20] {
		if child.Ref != "" {
			t.Fatalf("static item received address cost: %#v", child)
		}
	}
	snapshot.Roots = cloneAXForest(snapshot.CanonicalRoots, nil)
	if err := distillAXSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	if got := countAXNodes(snapshot.Roots); got >= 22 {
		t.Fatalf("static collection was not compressed: %d nodes", got)
	}
}

func TestRefsSkipAnonymousAndCollapsedDescendants(t *testing.T) {
	root := &AXNode{ID: "root", Role: "window", Name: "App"}
	anonymous := &AXNode{
		ID:             "anonymous",
		Role:           "button",
		SemanticTarget: "s2:anonymous",
		Capabilities:   AXCapabilities{Activate: ActivationSemantic},
		Parent:         root,
	}
	menu := &AXNode{
		ID:     "menu",
		Role:   "menu",
		Name:   "File",
		States: AXStates{Expanded: TriFalse},
		Parent: root,
	}
	hidden := &AXNode{
		ID:             "hidden",
		Role:           "menuitem",
		Name:           "Save",
		SemanticTarget: "s2:hidden",
		Capabilities:   AXCapabilities{Activate: ActivationSemantic},
		Parent:         menu,
	}
	visible := &AXNode{
		ID:             "visible",
		Role:           "button",
		Name:           "Apply",
		SemanticTarget: "s2:visible",
		Capabilities:   AXCapabilities{Activate: ActivationSemantic},
		Parent:         root,
	}
	menu.Children = []*AXNode{hidden}
	root.Children = []*AXNode{anonymous, menu, visible}
	snapshot := &AXSnapshotV1{
		SchemaVersion: "AXSnapshotV1",
		AppRef:        "a1",
		AppIdentity:   "app",
		WindowHandle:  "window",
		WindowRef:     "w1",
		Revision:      "r1",
		Roots:         []*AXNode{root},
	}
	NewRefRegistry(16).AssignSnapshotRefs(snapshot)
	if anonymous.Ref != "" || hidden.Ref != "" {
		t.Fatalf("non-operable nodes received refs: anonymous=%q hidden=%q",
			anonymous.Ref, hidden.Ref)
	}
	if visible.Ref == "" {
		t.Fatal("visible identifiable control did not receive a ref")
	}
}

func TestExactProviderAliasesCollapseToOneCanonicalTarget(t *testing.T) {
	snapshot := &appSnapshot{
		WindowHandle:    "w2:" + strings.Repeat("a", 32),
		publicWindowRef: "w1",
		publicAppRef:    "a1",
		SnapshotID:      "c1",
		App: appDescriptor{
			Name:              "Editor",
			ProviderBusName:   ":1.42",
			ProcessGeneration: "99123",
		},
		Elements: []elementRecord{
			{
				Index: 0, RuntimeID: []int{0},
				ControlType: "frame", Name: "Editor",
			},
			{
				Index: 1, RuntimeID: []int{0, 0},
				ControlType: "menu", Name: "Format",
				Actions:            []string{"click"},
				States:             "[focused]",
				ProviderBusName:    ":1.42",
				ProviderObjectPath: "/org/a11y/format",
				ProcessGeneration:  "99123",
			},
			{
				Index: 2, RuntimeID: []int{0, 1},
				ControlType: "menu", Name: "Format",
				Actions:            []string{"click"},
				States:             "[selected]",
				ProviderBusName:    ":1.42",
				ProviderObjectPath: "/org/a11y/format",
				ProcessGeneration:  "99123",
			},
		},
	}
	tree, err := buildAXSnapshotV1(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(tree.CanonicalRoots[0].Children); got != 1 {
		t.Fatalf("exact provider aliases produced %d canonical children", got)
	}
	node := tree.CanonicalRoots[0].Children[0]
	if tree.ByIndex[1] != node || tree.ByIndex[2] != node {
		t.Fatal("provider index aliases did not resolve to one canonical node")
	}
	if len(node.RuntimeIDAliases) != 2 {
		t.Fatalf("runtime aliases=%v, want 2", node.RuntimeIDAliases)
	}
	if node.States.Active != TriUnknown ||
		node.States.Selected != TriUnknown {
		t.Fatalf("alias-only states were promoted into canonical facts: %#v", node.States)
	}
	if node.LocatorKind != AXLocatorProviderBound {
		t.Fatalf("ambiguous semantic target kind=%q, want provider_bound", node.LocatorKind)
	}
	registry := NewRefRegistry(16)
	registry.AssignSnapshotRefs(tree)
	if node.Ref == "" {
		t.Fatal("provider-bound ambiguous target did not receive a short ref")
	}
}

func TestSameNameDifferentProviderObjectsRemainDistinct(t *testing.T) {
	snapshot := &appSnapshot{
		WindowHandle:    "w2:" + strings.Repeat("b", 32),
		publicWindowRef: "w1",
		SnapshotID:      "c1",
		App:             appDescriptor{Name: "Editor"},
		Elements: []elementRecord{
			{Index: 0, RuntimeID: []int{0}, ControlType: "frame", Name: "Editor"},
			{
				Index: 1, RuntimeID: []int{0, 0},
				ControlType: "menu", Name: "Format", Actions: []string{"click"},
				ProviderBusName: ":1.42", ProviderObjectPath: "/format/1",
				ProcessGeneration: "99123",
			},
			{
				Index: 2, RuntimeID: []int{0, 1},
				ControlType: "menu", Name: "Format", Actions: []string{"click"},
				ProviderBusName: ":1.42", ProviderObjectPath: "/format/2",
				ProcessGeneration: "99123",
			},
		},
	}
	tree, err := buildAXSnapshotV1(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(tree.CanonicalRoots[0].Children); got != 2 {
		t.Fatalf("different provider objects collapsed to %d nodes", got)
	}
	for _, node := range tree.CanonicalRoots[0].Children {
		if node.LocatorKind != AXLocatorProviderBound {
			t.Fatalf("ambiguous node kind=%q, want provider_bound", node.LocatorKind)
		}
	}
}

func TestExactAliasStateConflictBecomesUnknown(t *testing.T) {
	node := &AXNode{States: AXStates{Selected: TriTrue}}
	mergeExactProviderAlias(node, &elementRecord{States: "[unselected]"})
	if node.States.Selected != TriUnknown {
		t.Fatalf("conflicting selected state=%q, want unknown", node.States.Selected)
	}
}

func TestRendererHidesPredictedRoutesAndCanonicalActionAliases(t *testing.T) {
	semantic := &AXNode{
		ID: "semantic", Role: "button", Name: "Save",
		SemanticFingerprint: "f1:semantic",
		Capabilities: AXCapabilities{
			Activate:        ActivationSemantic,
			Actions:         []string{"open-menu"},
			ProviderActions: []string{"click", "menu", "release"},
		},
	}
	fallback := &AXNode{
		ID: "fallback", Role: "button", Name: "Canvas control",
		SemanticFingerprint: "f1:fallback",
		Capabilities:        AXCapabilities{Activate: ActivationLiveGeometry},
	}
	snapshot := &AXSnapshotV1{
		SchemaVersion: "AXSnapshotV1",
		AppIdentity:   "test", WindowHandle: "window", Revision: "r1",
		Roots: []*AXNode{semantic, fallback},
	}
	lines, err := RenderAXSnapshotYAML(snapshot, AXRenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(lines, "\n")
	for _, forbidden := range []string{
		"/activate",
		"live_geometry_fallback",
		"/capabilities",
		"provider-actions",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("working projection leaked %q:\n%s", forbidden, text)
		}
	}
	full, err := RenderAXSnapshotYAML(snapshot, AXRenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"/provider-role",
		"/provider-actions",
		"release",
	} {
		if strings.Contains(strings.Join(full, "\n"), forbidden) {
			t.Fatalf("model renderer leaked %q:\n%s",
				forbidden, strings.Join(full, "\n"))
		}
	}
}

func TestCanonicalActionOntologyHidesUnknownProviderVocabulary(t *testing.T) {
	record := &elementRecord{
		Actions: []string{"click", "showContextMenu", "release"},
	}
	if got := typedProviderActions(record); !reflect.DeepEqual(
		got, []string{"click", "release", "showcontextmenu"},
	) {
		t.Fatalf("raw provider actions were not preserved: %#v", got)
	}
}

func TestRendererDeduplicatesFieldsAndPureIconText(t *testing.T) {
	node := &AXNode{
		ID: "button", Role: "button", Name: "Continue",
		SemanticFingerprint: "f1:button",
		Properties: AXProperties{
			Value:       " Continue ",
			Description: "Continue",
			Placeholder: "Continue",
		},
	}
	icon := &AXNode{
		ID: "icon", Role: "text", SemanticFingerprint: "f1:icon",
		Properties: AXProperties{Value: "\ueabc\u200b"},
	}
	snapshot := &AXSnapshotV1{
		SchemaVersion: "AXSnapshotV1",
		AppIdentity:   "test", WindowHandle: "window", Revision: "r1",
		Roots: []*AXNode{node, icon},
	}
	lines, err := RenderAXSnapshotYAML(snapshot, AXRenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(lines, "\n")
	for _, forbidden := range []string{
		"/value", "/description", "/placeholder", "\ueabc",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("working projection leaked %q:\n%s", forbidden, text)
		}
	}
	full, err := RenderAXSnapshotYAML(snapshot, AXRenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	fullText := strings.Join(full, "\n")
	for _, forbidden := range []string{
		`/value: " Continue "`,
		`/description: Continue`,
		`/placeholder: Continue`,
		"\ueabc",
		"/provider-role",
		"/provider-actions",
	} {
		if strings.Contains(fullText, forbidden) {
			t.Fatalf("model renderer leaked %q:\n%s", forbidden, fullText)
		}
	}
}

func TestRendererUsesOneSpaceHierarchy(t *testing.T) {
	window := &AXNode{ID: "window", Role: "window", Name: "Bills"}
	toolbar := &AXNode{
		ID: "toolbar", Role: "toolbar", Name: "Spaces Toolbar",
		Properties: AXProperties{Description: "Toggle spaces"},
		Parent:     window,
	}
	button := &AXNode{
		ID: "mail", Role: "button", Name: "Mail (Ctrl+1)", Ref: "r1",
		Parent: toolbar,
	}
	toolbar.Children = []*AXNode{button}
	window.Children = []*AXNode{toolbar}
	snapshot := &AXSnapshotV1{
		SchemaVersion: "AXSnapshotV1",
		AppIdentity:   "test", WindowHandle: "window", Revision: "r1",
		Roots: []*AXNode{window},
	}
	lines, err := RenderAXSnapshotYAML(snapshot, AXRenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"ax:",
		` - window "Bills":`,
		`  - toolbar "Spaces Toolbar":`,
		`   - /description: Toggle spaces`,
		`   - button "Mail (Ctrl+1)" [ref=r1]`,
	}, "\n")
	if got := strings.Join(lines, "\n"); got != want {
		t.Fatalf("AX tree mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRendererEscapesOnlyUserText(t *testing.T) {
	node := &AXNode{
		ID: "button", Role: "button", Name: "Save: \"draft\"\nnow", Ref: "r7",
		Properties: AXProperties{
			Description: "Line one\nLine two",
		},
	}
	snapshot := &AXSnapshotV1{
		SchemaVersion: "AXSnapshotV1",
		AppIdentity:   "test", WindowHandle: "window", Revision: "r1",
		Roots: []*AXNode{node},
	}
	lines, err := RenderAXSnapshotYAML(snapshot, AXRenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(lines, "\n")
	if strings.Contains(text, `- "button \`) {
		t.Fatalf("entire semantic node label was quoted again:\n%s", text)
	}
	for _, expected := range []string{
		`button "Save: \"draft\"\nnow" [ref=r7]:`,
		`/description: "Line one\nLine two"`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("escaped user text missing %q:\n%s", expected, text)
		}
	}
}

func TestCohortCompressionSuppressesOnlyRepeatedNonStateValues(t *testing.T) {
	parent := &AXNode{ID: "toolbar", Role: "toolbar", Name: "Tools"}
	for index := 0; index < 7; index++ {
		value := "0.0"
		if index == 6 {
			value = "exception"
		}
		child := &AXNode{
			ID:   fmt.Sprintf("button-%d", index),
			Role: "button",
			Name: fmt.Sprintf("Action %d", index),
			Ref:  fmt.Sprintf("r%d", index+1),
			Properties: AXProperties{
				Value: value,
			},
			Capabilities: AXCapabilities{Activate: ActivationSemantic},
			Parent:       parent,
		}
		parent.Children = append(parent.Children, child)
	}
	stateful := &AXNode{
		ID:   "slider",
		Role: "slider",
		Name: "Zoom",
		Ref:  "r8",
		Properties: AXProperties{
			Value: "0.0",
		},
		Capabilities: AXCapabilities{SetValue: true},
		Parent:       parent,
	}
	parent.Children = append(parent.Children, stateful)
	snapshot := &AXSnapshotV1{
		SchemaVersion:  "AXSnapshotV1",
		AppIdentity:    "test",
		WindowHandle:   "window",
		Revision:       "revision",
		CanonicalRoots: []*AXNode{parent},
		Roots:          cloneAXForest([]*AXNode{parent}, nil),
		ByID:           map[string]*AXNode{},
		ByIndex:        map[int]*AXNode{},
	}
	if err := distillAXSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 6; index++ {
		if snapshot.Roots[0].Children[index].Properties.Value != "" {
			t.Fatalf("repeated cohort value %d was not suppressed", index)
		}
	}
	if got := snapshot.Roots[0].Children[6].Properties.Value; got != "exception" {
		t.Fatalf("cohort exception was suppressed: %q", got)
	}
	if got := snapshot.Roots[0].Children[7].Properties.Value; got != "0.0" {
		t.Fatalf("decision-bearing value was suppressed: %q", got)
	}
	foundAudit := false
	for _, report := range snapshot.Distillation {
		if report.Name == "suppress_redundant_cohort_properties" {
			foundAudit = report.SuppressedProperties == 6
		}
	}
	if !foundAudit {
		t.Fatalf("cohort suppression was not audited: %#v", snapshot.Distillation)
	}
	if got := parent.Children[0].Properties.Value; got != "0.0" {
		t.Fatalf("canonical cohort value was mutated: %q", got)
	}
}

func TestFindContextReturnsMergedCanonicalPathAndNearbySiblings(t *testing.T) {
	window := &AXNode{ID: "window", Role: "window", Name: "Editor"}
	dialog := &AXNode{
		ID: "dialog", Role: "dialog", Name: "Preferences", Parent: window,
	}
	group := &AXNode{
		ID: "group", Role: "group", Name: "Document", Parent: dialog,
	}
	window.Children = []*AXNode{dialog}
	dialog.Children = []*AXNode{group}
	for index, name := range []string{
		"Before 1", "Before 2", "Save", "After 1", "After 2", "Unrelated",
	} {
		node := &AXNode{
			ID:            fmt.Sprintf("button-%d", index),
			Role:          "button",
			Name:          name,
			ProviderIndex: index + 1,
			Parent:        group,
			Capabilities: AXCapabilities{
				Activate: ActivationSemantic,
			},
		}
		if name == "Save" {
			node.Ref = "r7"
		}
		group.Children = append(group.Children, node)
	}
	snapshot := &appSnapshot{
		axSnapshot: &AXSnapshotV1{
			SchemaVersion:  "AXSnapshotV1",
			AppIdentity:    "editor",
			WindowHandle:   "window-1",
			Revision:       "revision",
			CanonicalRoots: []*AXNode{window},
			Roots:          cloneAXForest([]*AXNode{window}, nil),
			ByIndex: map[int]*AXNode{
				3: group.Children[2],
			},
		},
	}
	context := buildAXFindContext(
		snapshot,
		[]elementRecord{{Index: 3}},
		map[int]bool{},
	)
	findSnapshot := &AXSnapshotV1{
		SchemaVersion: "AXSnapshotV1",
		AppIdentity:   "editor",
		WindowHandle:  "window-1",
		Revision:      "revision",
		Roots:         context,
	}
	lines, err := RenderAXSnapshotYAML(
		findSnapshot,
		AXRenderOptions{RootKey: "matches"},
	)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(lines, "\n")
	for _, expected := range []string{
		`window "Editor"`,
		`dialog "Preferences"`,
		`group "Document"`,
		`button "Before 1"`,
		`button "Before 2"`,
		`button "Save" [ref=r7]`,
		`/match: "true"`,
		`button "After 1"`,
		`button "After 2"`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("find context omitted %q:\n%s", expected, text)
		}
	}
	for _, forbidden := range []string{
		"working_projection",
		"serializer_hidden",
		"provider_omission",
		"reason=",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("find context leaked internal annotation %q:\n%s", forbidden, text)
		}
	}
	if strings.Contains(text, `button "Unrelated"`) {
		t.Fatalf("find context included distant sibling:\n%s", text)
	}
}

func TestStructuralBudgetPreservesCriticalStateAndFindRecovery(t *testing.T) {
	t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "900")
	window := &AXNode{ID: "window", Role: "window", Name: "Editor"}
	toolbar := &AXNode{
		ID: "toolbar", Role: "toolbar", Name: "Commands", Parent: window,
	}
	window.Children = []*AXNode{toolbar}
	byIndex := map[int]*AXNode{}
	for index := 0; index < 40; index++ {
		node := &AXNode{
			ID:            fmt.Sprintf("command-%d", index),
			Role:          "button",
			Name:          fmt.Sprintf("Command %02d", index),
			Ref:           fmt.Sprintf("r%d", index+1),
			ProviderIndex: index + 1,
			Parent:        toolbar,
			Capabilities: AXCapabilities{
				Activate: ActivationSemantic,
			},
		}
		if index == 20 {
			node.States.Selected = TriTrue
		}
		toolbar.Children = append(toolbar.Children, node)
		byIndex[index+1] = node
	}
	snapshot := &AXSnapshotV1{
		SchemaVersion:  "AXSnapshotV1",
		AppIdentity:    "editor",
		WindowHandle:   "window-1",
		Revision:       "revision",
		CanonicalRoots: []*AXNode{window},
		Roots:          cloneAXForest([]*AXNode{window}, nil),
		ByID:           map[string]*AXNode{},
		ByIndex:        byIndex,
	}
	if err := distillAXSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	lines, err := RenderAXSnapshotYAML(snapshot, AXRenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(lines, "\n")
	if !strings.Contains(text, `button "Command 20" [selected=true] [ref=r21]`) {
		t.Fatalf("critical selected target was omitted:\n%s", text)
	}
	if !strings.Contains(text, "recover=find|root") {
		t.Fatalf("budget omission lacks recovery path:\n%s", text)
	}
	var budget DistillationStageReport
	for _, report := range snapshot.Distillation {
		if report.Name == "apply_output_budget" {
			budget = report
			break
		}
	}
	if !budget.BudgetApplied ||
		budget.BudgetOmittedNodes == 0 ||
		budget.BudgetOmittedTargets == 0 ||
		budget.TargetRecall >= budget.TargetCount ||
		budget.InvariantFailure != "" {
		t.Fatalf("structural budget report is invalid: %#v", budget)
	}
	if budget.BudgetCharsAfter > axStructuralTreeBudget() {
		t.Fatalf(
			"structural budget remained oversized: %d > %d",
			budget.BudgetCharsAfter,
			axStructuralTreeBudget(),
		)
	}
	app := &appSnapshot{axSnapshot: snapshot}
	context := buildAXFindContext(
		app,
		[]elementRecord{{Index: 2}},
		axProjectedIndices(snapshot.Roots),
	)
	findSnapshot := &AXSnapshotV1{
		SchemaVersion: "AXSnapshotV1",
		AppIdentity:   "editor",
		WindowHandle:  "window-1",
		Revision:      "revision",
		Roots:         context,
	}
	findLines, err := RenderAXSnapshotYAML(
		findSnapshot,
		AXRenderOptions{RootKey: "matches"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(
		strings.Join(findLines, "\n"),
		`button "Command 01" [ref=r2]`,
	) {
		t.Fatalf("budget-hidden target was not recoverable by find")
	}
}

func TestRoleOntologyHasUniqueProviderAliases(t *testing.T) {
	seen := map[string]CanonicalRole{}
	for _, descriptor := range roleOntology {
		for _, alias := range descriptor.ProviderAliases {
			key := strings.ToLower(strings.TrimSpace(alias))
			if previous, exists := seen[key]; exists {
				t.Fatalf(
					"provider alias %q maps to both %q and %q",
					key, previous, descriptor.ModelRole,
				)
			}
			seen[key] = descriptor.ModelRole
		}
	}
	if seen["tree table"] != "treegrid" {
		t.Fatalf("tree table role = %q, want treegrid", seen["tree table"])
	}
}

func TestActionabilityUsesRuntimeRouteInsteadOfActionTypeGuess(t *testing.T) {
	evidence := initialActionabilityEvidence("type", nil, nil)
	readback := true
	focus := true
	editable := true
	actual := &RuntimeActionEvidence{
		Route: "editable_text_api", FocusVerified: &focus,
		EditableVerified: &editable, ReadbackSampled: true,
		ReadbackVerified: &readback,
		InputEmitted:     true,
	}
	final := finalizeActionabilityEvidence(
		evidence,
		axServiceRequest{Action: axTypeCommand{}},
		toolCallResult{},
		true,
		actual,
	)
	if final.Route != "editable_text_api" ||
		final.DeliveryVerified != TriTrue ||
		final.FocusVerified != TriTrue ||
		final.EditableVerified != TriTrue {
		t.Fatalf("runtime route was not preserved: %#v", final)
	}
	for _, stage := range final.Stages {
		if stage.Stage == ActionSampleGeometry &&
			stage.Status != ActionStageNotRequired {
			t.Fatalf("semantic text write falsely claimed geometry: %#v", final)
		}
	}
}

func TestBatchEvidenceKeepsEveryOrderedAction(t *testing.T) {
	entries := make([]map[string]any, 0, axBatchMaxActions)
	for index := 0; index < axBatchMaxActions; index++ {
		result := toolCallResult{StructuredContent: map[string]any{
			"actionability": ActionabilityEvidence{
				Route:        "keyboard_synthesis",
				InputEmitted: TriTrue,
			},
		}}
		entries = append(
			entries,
			batchActionEvidenceEntry(index, "keypress", result),
		)
	}
	if len(entries) != axBatchMaxActions {
		t.Fatalf("batch evidence count=%d", len(entries))
	}
	for index, entry := range entries {
		if entry["action_index"] != index ||
			entry["action_number"] != index+1 ||
			entry["actionability"] == nil {
			t.Fatalf("batch evidence lost action %d: %#v", index, entry)
		}
	}
	payload, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) >= maxInlineChars() {
		t.Fatalf(
			"32-action evidence exceeds final text budget: %d bytes",
			len(payload),
		)
	}
}

func TestFailedActionPreservesPartialRuntimeRoute(t *testing.T) {
	evidence := initialActionabilityEvidence("click", nil, nil)
	focus := true
	actual := &RuntimeActionEvidence{
		Route: "coordinate_synthesis", GeometrySampled: true,
		FocusVerified: &focus, InputEmitted: true,
	}
	final := finalizeActionabilityEvidence(
		evidence,
		axServiceRequest{Action: axClickCommand{}},
		toolCallResult{StructuredContent: map[string]any{
			"failure_code":         "post_emit_failure",
			"input_events_emitted": true,
		}},
		false,
		actual,
	)
	if final.Route != "coordinate_synthesis" ||
		final.InputEmitted != TriTrue ||
		final.ReadbackSampled != TriUnknown {
		t.Fatalf("partial failure evidence was lost: %#v", final)
	}
}

func FuzzYAMLScalarRoundTripSurface(f *testing.F) {
	for _, seed := range []string{
		"", "null", "a: b", "# heading", "line\nbreak",
		`quoted "value"`, "中文", "- item", "true",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		rendered := yamlScalar(value)
		if strings.ContainsRune(rendered, '\x00') {
			t.Fatalf("YAML scalar leaked NUL: %q", rendered)
		}
		if !strings.HasPrefix(rendered, `"`) &&
			(strings.Contains(value, ": ") ||
				strings.HasPrefix(value, "#") ||
				strings.ContainsAny(value, "\r\n\t")) {
			t.Fatalf("unsafe scalar was not quoted: value=%q rendered=%q", value, rendered)
		}
	})
}

func assertStableDiffIdentity(before, after *AXSnapshotV1) error {
	changes := diffAXSnapshots(before, after)
	for _, change := range changes {
		if strings.Contains(change.Key, "/0:") ||
			strings.Contains(change.Key, "/1:") {
			return fmt.Errorf("positional diff identity leaked into key %q", change.Key)
		}
	}
	return nil
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
		TreeLines: withUnchangedWindowContext([]string{
			"ax:",
			` - list "Results" [ref=r1]:`,
			`  - listitem "Second" [ref=r3]`,
			`  - listitem "First" [ref=r2]`,
		}),
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
		TreeLines: withUnchangedWindowContext([]string{
			"ax:",
			` - list "Results" [ref=r1]:`,
			`  - button "Save" [ref=r3]`,
			`  - textbox "Query" [ref=r2]:`,
			`   - /value: new`,
		}),
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
		TreeLines:       withUnchangedWindowContext([]string{"ax:", ` - list "Results" [ref=r1]`}),
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
		TreeLines:       withUnchangedWindowContext([]string{"ax:", ` - list "Results" [ref=r1]`}),
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
		TreeLines:       withUnchangedWindowContext([]string{"ax:", ` - list "Results" [ref=r1]`}),
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
		TreeLines:       withUnchangedWindowContext([]string{"ax:", ` - list "Results" [ref=r1]`}),
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
		TreeLines: withUnchangedWindowContext([]string{
			"ax:",
			` - document "Second" [ref=r2]`,
			` - document "First" [ref=r1]`,
		}),
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
		TreeLines: withUnchangedWindowContext([]string{
			"ax:",
			` - button "Second" [ref=r2]`,
			` - textbox "First" [ref=r1]:`,
			`  - /value: new`,
		}),
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

// withUnchangedWindowContext appends the unchanged rest of a window to a
// fixture tree. A diff replaces the current tree only when it is shorter, and
// real windows carry far more unchanged nodes than these focused fixtures.
func withUnchangedWindowContext(lines []string) []string {
	out := append([]string(nil), lines...)
	for index := 0; index < 300; index++ {
		out = append(out, fmt.Sprintf(` - text "Unchanged context line %03d"`, index))
	}
	return out
}

func TestReorderDiffLargerThanTreeFallsBackToTree(t *testing.T) {
	beforeParent := &AXNode{
		ID: "list", Role: "list", Name: "Results",
		Ref: "r1", SemanticFingerprint: "f1:list",
	}
	first := &AXNode{ID: "a", Role: "listitem", Name: "A", Ref: "r2",
		SemanticFingerprint: "f1:a", Parent: beforeParent}
	second := &AXNode{ID: "b", Role: "listitem", Name: "B", Ref: "r3",
		SemanticFingerprint: "f1:b", Parent: beforeParent}
	beforeParent.Children = []*AXNode{first, second}
	afterParent := *beforeParent
	firstAfter, secondAfter := *first, *second
	firstAfter.Parent, secondAfter.Parent = &afterParent, &afterParent
	afterParent.Children = []*AXNode{&secondAfter, &firstAfter}
	before := &appSnapshot{axSnapshot: &AXSnapshotV1{
		CanonicalRoots: []*AXNode{beforeParent}, Roots: []*AXNode{beforeParent},
	}}
	after := &appSnapshot{
		TreeLines: []string{
			"ax:",
			` - list "Results" [ref=r1]:`,
			`  - listitem "B" [ref=r3]`,
			`  - listitem "A" [ref=r2]`,
		},
		axSnapshot: &AXSnapshotV1{
			CanonicalRoots: []*AXNode{&afterParent}, Roots: []*AXNode{&afterParent},
		},
	}
	if diff, ok := testIncrementalAXSnapshot(before, after); ok {
		t.Fatalf("a reorder diff longer than the tree must fall back to the tree:\n%s",
			strings.Join(diff, "\n"))
	}
}

func TestAXLocatorForRecordBindsA1NamedCellsToTheProviderObject(t *testing.T) {
	cell := &elementRecord{Name: "C2"}
	for _, tc := range []struct {
		name             string
		role             CanonicalRole
		semanticTarget   string
		providerIdentity string
		wantKind         AXLocatorKind
		wantTarget       string
	}{
		{"semantic button", "button", "sem", "", AXLocatorSemantic, "sem"},
		{"A1 cell with provider object", "cell", "sem", "pid", AXLocatorProviderBound, "sem"},
		{"A1 cell without provider object", "gridcell", "sem", "", "", ""},
		{"no semantic target, provider object", "cell", "", "pid", AXLocatorProviderBound, `cell "C2"`},
		{"no target, no provider object", "cell", "", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind, target := axLocatorForRecord(cell, tc.role, tc.semanticTarget, tc.providerIdentity)
			if kind != tc.wantKind || target != tc.wantTarget {
				t.Fatalf("axLocatorForRecord = (%q, %q), want (%q, %q)", kind, target, tc.wantKind, tc.wantTarget)
			}
		})
	}
}
