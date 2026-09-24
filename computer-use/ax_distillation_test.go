package main

import (
	"strings"
	"testing"
)

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
