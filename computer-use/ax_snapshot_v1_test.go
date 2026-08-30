package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestAXSnapshotV1SchemaIsRecursiveAndTyped(t *testing.T) {
	schema := AXSnapshotV1JSONSchema()
	defs, ok := schema["$defs"].(map[string]any)
	if !ok {
		t.Fatal("AXSnapshotV1 schema has no typed $defs")
	}
	for _, name := range []string{"node", "states", "capabilities", "properties"} {
		if defs[name] == nil {
			t.Fatalf("AXSnapshotV1 schema is missing %q", name)
		}
	}
	capabilities := defs["capabilities"].(map[string]any)
	capabilityProperties := capabilities["properties"].(map[string]any)
	if capabilityProperties["providerActions"] == nil {
		t.Fatal("AXSnapshotV1 schema omits raw provider actions")
	}
	node := defs["node"].(map[string]any)
	properties := node["properties"].(map[string]any)
	children := properties["children"].(map[string]any)
	items := children["items"].(map[string]any)
	if items["$ref"] != "#/$defs/node" {
		t.Fatalf("node children are not recursive: %#v", children)
	}
}

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

func TestAXSnapshotV1JSONRoundTripPreservesTypedModel(t *testing.T) {
	root := &AXNode{
		ID: "0", Role: "button", ProviderRole: "push button",
		Name: "Save", Ref: "r1", SemanticFingerprint: "f1:save",
		States:       AXStates{Active: TriTrue, Disabled: TriUnknown},
		Capabilities: AXCapabilities{Activate: ActivationSemantic},
	}
	snapshot := &AXSnapshotV1{
		SchemaVersion: "AXSnapshotV1",
		AppIdentity:   "editor",
		WindowHandle:  "window-1",
		Revision:      "rev1",
		Roots:         []*AXNode{root},
	}
	data, err := RenderAXSnapshotJSON(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var decoded AXSnapshotV1
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Roots[0].Role != "button" ||
		decoded.Roots[0].Capabilities.Activate != ActivationSemantic {
		t.Fatalf("typed JSON round-trip drifted: %#v", decoded.Roots[0])
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
		"remove_name_repeating_text",
		"suppress_redundant_cohort_properties",
		"restore_ambiguity_disambiguators",
		"summarize_homogeneous_collections",
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
		settlePlan{},
		settleOutcome{},
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

func TestRendererUsesPlaywrightStyleOneSpaceHierarchy(t *testing.T) {
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
		t.Fatalf("Playwright-style AX tree mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
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
		settlePlan{},
		settleOutcome{},
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
	entries := make([]map[string]any, 0, a11yBatchMaxActions)
	for index := 0; index < a11yBatchMaxActions; index++ {
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
	if len(entries) != a11yBatchMaxActions {
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
		settlePlan{},
		settleOutcome{},
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
