package main

import "testing"

func TestSelectAXBudgetPlanRejectsNestedCandidates(t *testing.T) {
	leaf := &AXNode{ID: "leaf", Role: "paragraph"}
	child := &AXNode{ID: "child", Role: "group", Children: []*AXNode{leaf}}
	leaf.Parent = child
	root := &AXNode{ID: "root", Role: "window", Children: []*AXNode{child}}
	child.Parent = root

	plan := selectAXBudgetPlan(
		[]*AXNode{root},
		[]axBudgetCandidate{
			{
				parentID: "root",
				nodeIDs:  []string{"child"},
				priority: 10,
				savings:  5,
			},
			{
				parentID: "child",
				nodeIDs:  []string{"leaf"},
				priority: 10,
				savings:  10,
			},
		},
		10,
	)
	if len(plan) != 1 || plan[0].nodeIDs[0] != "leaf" {
		t.Fatalf("nested candidates were selected together: %#v", plan)
	}
}

func TestStructuralBudgetTreatsForceKeepAsProtectedContext(t *testing.T) {
	node := &AXNode{ID: "context", Role: "paragraph", ForceKeep: true}
	if !axSubtreeContainsCriticalOrBoundary(node) {
		t.Fatal("force-kept context was eligible for structural omission")
	}
}
