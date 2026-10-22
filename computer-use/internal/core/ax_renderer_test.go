package core

import (
	"strings"
	"testing"

	"github.com/hqhq1025/hybrid-computer-use-oss/computer-use/internal/contract"
)

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

func TestRelationPruningRequiresUniqueLabelTarget(t *testing.T) {
	snapshot := &AppSnapshot{
		Elements: []ElementRecord{
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
	if AXProjectedIndices(tree.Roots)[2] {
		t.Fatal("unique relation-linked duplicate label was not pruned")
	}
	snapshot.Elements = append(snapshot.Elements, ElementRecord{
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
	if AXProjectedIndices(tree.Roots)[2] {
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
	if !AXProjectedIndices(tree.Roots)[2] {
		t.Fatal("incomplete relation evidence must retain the label")
	}
}

func TestAllModelRenderersHideProviderDiagnostics(t *testing.T) {
	snapshot := editorFormSnapshot()
	snapshot.Annotations = []axProviderAnnotation{{
		Kind:   "provider_omission",
		Count:  12,
		Reason: "time_budget",
		Detail: "internal traversal stopped",
	}}
	for _, view := range []string{contract.TreeViewWorking, contract.TreeViewFull} {
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
