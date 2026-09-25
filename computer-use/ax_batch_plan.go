package main

import (
	"errors"
	"fmt"
	"strings"
)

type a11yPlanExecutionClass string

const (
	a11yPlanSingleAction a11yPlanExecutionClass = "single_action"
	a11yPlanOrderedInput a11yPlanExecutionClass = "ordered_window_input"
)

type a11yPlanConsistency string

const (
	a11yPlanWindowIdentity  a11yPlanConsistency = "window_identity"
	a11yPlanCanonicalWindow a11yPlanConsistency = "canonical_window"
)

type a11yPlanTargetRequirement string

const (
	a11yPlanNoTarget      a11yPlanTargetRequirement = "none"
	a11yPlanElementTarget a11yPlanTargetRequirement = "element_ref"
	a11yPlanWindowTarget  a11yPlanTargetRequirement = "window_ref"
)

type a11yPlanBoundary string

const (
	a11yPlanNoBoundary            a11yPlanBoundary = "none"
	a11yPlanCanonicalBoundary     a11yPlanBoundary = "canonical_action"
	a11yPlanTargetedBoundary      a11yPlanBoundary = "targeted_action"
	a11yPlanPostconditionBoundary a11yPlanBoundary = "action_postcondition"
	a11yPlanWindowBoundary        a11yPlanBoundary = "window_transition"
	a11yPlanNextActionBoundary    a11yPlanBoundary = "next_action_requires_canonical"
	a11yPlanBatchGuardBoundary    a11yPlanBoundary = "batch_postcondition"
	a11yPlanTransactionBoundary   a11yPlanBoundary = "transaction_end"
)

type a11yPlannedStep struct {
	actionIndex     int
	actionType      string
	executionClass  a11yPlanExecutionClass
	target          a11yPlanTargetRequirement
	preConsistency  a11yPlanConsistency
	postConsistency a11yPlanConsistency
	boundary        a11yPlanBoundary
	orderedInputRun int
}

type a11yBatchExecutionPlan struct {
	windowRef             string
	windowHandle          string
	initialConsistency    a11yPlanConsistency
	requiresMutationLease bool
	finalObservation      string
	hasBatchPostcondition bool
	steps                 []a11yPlannedStep
}

func a11yPlanTargetForAction(action a11yBatchAction) (
	a11yPlanTargetRequirement,
	error,
) {
	target := strings.TrimSpace(action.targetRef)
	switch {
	case target == "":
		return a11yPlanNoTarget, nil
	case isShortAXRef(target):
		return a11yPlanElementTarget, nil
	case isShortAXWindowRef(target):
		return a11yPlanWindowTarget, nil
	default:
		return "", fmt.Errorf("target %q is not a current rN or wN ref", target)
	}
}

func a11yPlanDefaultBoundary(
	action a11yBatchAction,
	target a11yPlanTargetRequirement,
) a11yPlanBoundary {
	if action.actionType == "activate" || action.actionType == "close" {
		return a11yPlanWindowBoundary
	}
	if action.postcondition != nil {
		return a11yPlanPostconditionBoundary
	}
	if target != a11yPlanNoTarget {
		return a11yPlanTargetedBoundary
	}
	return a11yPlanCanonicalBoundary
}

func a11yPlanOrderedInputEligible(
	action a11yBatchAction,
	target a11yPlanTargetRequirement,
) bool {
	return action.actionType == "keypress" &&
		target == a11yPlanNoTarget &&
		action.postcondition == nil
}

// compileA11yBatchExecutionPlan runs only after public normalization, ownership
// binding, and postcondition preflight. It describes consistency boundaries;
// providers do not consume it until their batch contract can preserve the same
// per-action stop and emission evidence as the current path.
func compileA11yBatchExecutionPlan(
	context *a11yBatchContext,
	actions []a11yBatchAction,
	final observation,
	batchPostcondition map[string]any,
) (a11yBatchExecutionPlan, error) {
	if context == nil ||
		strings.TrimSpace(context.windowRef) == "" ||
		strings.TrimSpace(context.windowHandle) == "" {
		return a11yBatchExecutionPlan{}, errors.New(
			"AX execution plans require one bound logical window",
		)
	}
	if len(actions) == 0 {
		return a11yBatchExecutionPlan{}, errors.New(
			"AX execution plans require at least one action",
		)
	}

	plan := a11yBatchExecutionPlan{
		windowRef:             context.windowRef,
		windowHandle:          context.windowHandle,
		initialConsistency:    a11yPlanCanonicalWindow,
		requiresMutationLease: true,
		finalObservation:      final.resolvedMode(),
		hasBatchPostcondition: batchPostcondition != nil,
		steps:                 make([]a11yPlannedStep, 0, len(actions)),
	}

	nextRun := 0
	activeRun := 0
	for index, action := range actions {
		target, err := a11yPlanTargetForAction(action)
		if err != nil {
			return a11yBatchExecutionPlan{}, fmt.Errorf(
				"actions[%d]: %w", index, err,
			)
		}
		step := a11yPlannedStep{
			actionIndex:     index,
			actionType:      action.actionType,
			executionClass:  a11yPlanSingleAction,
			target:          target,
			preConsistency:  a11yPlanCanonicalWindow,
			postConsistency: a11yPlanCanonicalWindow,
			boundary:        a11yPlanDefaultBoundary(action, target),
		}
		if a11yPlanOrderedInputEligible(action, target) {
			if activeRun == 0 {
				nextRun++
				activeRun = nextRun
			}
			step.executionClass = a11yPlanOrderedInput
			step.preConsistency = a11yPlanWindowIdentity
			step.postConsistency = a11yPlanWindowIdentity
			step.boundary = a11yPlanNoBoundary
			step.orderedInputRun = activeRun
		} else {
			activeRun = 0
		}
		plan.steps = append(plan.steps, step)
	}

	for index := range plan.steps {
		step := &plan.steps[index]
		if step.executionClass != a11yPlanOrderedInput {
			continue
		}
		runEnds := index == len(plan.steps)-1 ||
			plan.steps[index+1].orderedInputRun != step.orderedInputRun
		if !runEnds {
			continue
		}
		step.postConsistency = a11yPlanCanonicalWindow
		switch {
		case index < len(plan.steps)-1:
			step.boundary = a11yPlanNextActionBoundary
		case batchPostcondition != nil:
			step.boundary = a11yPlanBatchGuardBoundary
		default:
			step.boundary = a11yPlanTransactionBoundary
		}
	}
	return plan, nil
}
