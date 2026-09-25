package main

import (
	"strings"
	"testing"
)

func testA11yBatchPlanContext() *a11yBatchContext {
	return &a11yBatchContext{
		windowRef:    "w1",
		windowHandle: "private-window-1",
	}
}

func TestA11yBatchPlanGroupsOnlyUntargetedKeypresses(t *testing.T) {
	plan, err := compileA11yBatchExecutionPlan(
		testA11yBatchPlanContext(),
		[]a11yBatchAction{
			{actionType: "keypress", key: "DOWN"},
			{actionType: "keypress", key: "ENTER"},
			{actionType: "keypress", key: "A"},
		},
		observation{mode: observeDiff},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.initialConsistency != a11yPlanCanonicalWindow ||
		!plan.requiresMutationLease ||
		plan.finalObservation != observeDiff {
		t.Fatalf("unexpected plan header: %#v", plan)
	}
	for index, step := range plan.steps {
		if step.executionClass != a11yPlanOrderedInput ||
			step.preConsistency != a11yPlanWindowIdentity ||
			step.orderedInputRun != 1 {
			t.Fatalf("step %d was not in one ordered input run: %#v", index, step)
		}
	}
	if plan.steps[0].boundary != a11yPlanNoBoundary ||
		plan.steps[1].boundary != a11yPlanNoBoundary ||
		plan.steps[2].boundary != a11yPlanTransactionBoundary ||
		plan.steps[2].postConsistency != a11yPlanCanonicalWindow {
		t.Fatalf("ordered run has incorrect boundaries: %#v", plan.steps)
	}
}

func TestA11yBatchPlanSeparatesRunsAtCanonicalActions(t *testing.T) {
	plan, err := compileA11yBatchExecutionPlan(
		testA11yBatchPlanContext(),
		[]a11yBatchAction{
			{actionType: "click", targetRef: "r1"},
			{actionType: "keypress", key: "DOWN"},
			{actionType: "keypress", key: "ENTER"},
			{actionType: "set_value", targetRef: "r2", value: "6"},
			{actionType: "keypress", key: "TAB"},
		},
		observation{mode: observeNone},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	wantClasses := []a11yPlanExecutionClass{
		a11yPlanSingleAction,
		a11yPlanOrderedInput,
		a11yPlanOrderedInput,
		a11yPlanSingleAction,
		a11yPlanOrderedInput,
	}
	for index, want := range wantClasses {
		if plan.steps[index].executionClass != want {
			t.Fatalf("step %d class=%q, want %q", index,
				plan.steps[index].executionClass, want)
		}
	}
	if plan.steps[0].boundary != a11yPlanTargetedBoundary ||
		plan.steps[2].boundary != a11yPlanNextActionBoundary ||
		plan.steps[3].boundary != a11yPlanTargetedBoundary ||
		plan.steps[1].orderedInputRun != 1 ||
		plan.steps[2].orderedInputRun != 1 ||
		plan.steps[4].orderedInputRun != 2 {
		t.Fatalf("canonical actions did not split ordered runs: %#v", plan.steps)
	}
}

func TestA11yBatchPlanKeepsEffectSensitiveActionsOnFullPath(t *testing.T) {
	guard := map[string]any{
		"type":  "exists",
		"value": true,
	}
	actions := []a11yBatchAction{
		{actionType: "keypress", targetRef: "r1", key: "ENTER"},
		{actionType: "keypress", key: "TAB", postcondition: guard},
		{actionType: "type", text: "literal"},
		{actionType: "scroll", direction: "down"},
		{actionType: "activate", targetRef: "w1"},
	}
	plan, err := compileA11yBatchExecutionPlan(
		testA11yBatchPlanContext(),
		actions,
		observation{mode: observeScreenshot},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	for index, step := range plan.steps {
		if step.executionClass != a11yPlanSingleAction ||
			step.preConsistency != a11yPlanCanonicalWindow {
			t.Fatalf("effect-sensitive step %d entered ordered path: %#v",
				index, step)
		}
	}
	if plan.steps[0].target != a11yPlanElementTarget ||
		plan.steps[0].boundary != a11yPlanTargetedBoundary ||
		plan.steps[1].boundary != a11yPlanPostconditionBoundary ||
		plan.steps[2].boundary != a11yPlanCanonicalBoundary ||
		plan.steps[3].boundary != a11yPlanCanonicalBoundary ||
		plan.steps[4].target != a11yPlanWindowTarget ||
		plan.steps[4].boundary != a11yPlanWindowBoundary {
		t.Fatalf("effect-sensitive boundaries are wrong: %#v", plan.steps)
	}
}

func TestA11yBatchPlanEndsOrderedRunAtBatchPostcondition(t *testing.T) {
	plan, err := compileA11yBatchExecutionPlan(
		testA11yBatchPlanContext(),
		[]a11yBatchAction{{actionType: "keypress", key: "ENTER"}},
		observation{mode: observeDiff},
		map[string]any{"type": "exists"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.hasBatchPostcondition ||
		plan.steps[0].boundary != a11yPlanBatchGuardBoundary ||
		plan.steps[0].postConsistency != a11yPlanCanonicalWindow {
		t.Fatalf("batch postcondition did not close the ordered run: %#v", plan)
	}
}

func TestA11yBatchPlanRejectsUnboundOrMalformedInput(t *testing.T) {
	tests := []struct {
		name    string
		context *a11yBatchContext
		actions []a11yBatchAction
		want    string
	}{
		{
			name:    "nil context",
			context: nil,
			actions: []a11yBatchAction{{actionType: "keypress", key: "A"}},
			want:    "one bound logical window",
		},
		{
			name:    "empty actions",
			context: testA11yBatchPlanContext(),
			want:    "at least one action",
		},
		{
			name:    "malformed target",
			context: testA11yBatchPlanContext(),
			actions: []a11yBatchAction{{
				actionType: "click",
				targetRef:  `button "Save"`,
			}},
			want: "current rN or wN ref",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := compileA11yBatchExecutionPlan(
				test.context,
				test.actions,
				observation{},
				nil,
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want substring %q", err, test.want)
			}
		})
	}
}
