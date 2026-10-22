package core

import (
	"fmt"
	"sort"
	"unicode/utf8"
)

type axBudgetCandidate struct {
	parentID string
	nodeIDs  []string
	depth    int
	priority int
	savings  int
}

func axStructuralTreeBudget() int {
	limit := 12000
	if configured := MaxInlineChars(); configured < limit {
		limit = configured
	}
	if limit < 128 {
		return 128
	}
	return limit
}

func axCriticalState(node *AXNode) bool {
	return axNodeHasState(node)
}

func axSubtreeContainsCriticalOrBoundary(node *AXNode) bool {
	if node == nil {
		return false
	}
	if node.ForceKeep || axCriticalState(node) ||
		isAXSemanticBoundary(node.Role) {
		return true
	}
	for _, child := range node.Children {
		if axSubtreeContainsCriticalOrBoundary(child) {
			return true
		}
	}
	return false
}

func axBudgetPriority(node *AXNode) int {
	if node == nil {
		return 0
	}
	priority := 10
	if node.Name != "" || node.Properties.Value != "" ||
		node.Properties.Description != "" {
		priority = 25
	}
	if axNodeHasCapability(node) {
		priority = 40
	}
	if node.Ref != "" {
		priority = 55
	}
	if axNodeHasState(node) {
		priority = 65
	}
	return priority
}

func axRenderedNodeChars(node *AXNode, depth int) int {
	if node == nil {
		return 0
	}
	properties := renderAXProperties(node, AXRenderOptions{})
	hasChildren := len(properties) > 0 || len(node.Children) > 0
	line := renderAXTreeNodeLine(node, depth, hasChildren)
	total := utf8.RuneCountInString(line) + 1
	for _, property := range properties {
		total += utf8.RuneCountInString(
			renderAXTreePropertyLine(property, depth+1),
		) + 1
	}
	for _, child := range node.Children {
		total += axRenderedNodeChars(child, depth+1)
	}
	return total
}

func axRenderedForestChars(nodes []*AXNode) int {
	total := len("ax:\n")
	for _, node := range nodes {
		total += axRenderedNodeChars(node, 1)
	}
	return total
}

func axBudgetSummaryText(nodes, targets int) string {
	return fmt.Sprintf(
		"budget nodes=%d targets=%d recover=find|root",
		nodes,
		targets,
	)
}

func newAXBudgetSummary(
	parent *AXNode,
	id string,
	nodes,
	targets int,
) *AXNode {
	return &AXNode{
		ID:                   id,
		Role:                 RoleGeneric,
		Synthetic:            true,
		Parent:               parent,
		BudgetOmittedNodes:   nodes,
		BudgetOmittedTargets: targets,
		Properties: AXProperties{
			Omitted: axBudgetSummaryText(nodes, targets),
		},
	}
}

func countAXTargets(nodes []*AXNode) int {
	count := 0
	for _, node := range nodes {
		if axNodeTargetTruth(node) {
			count++
		}
		count += countAXTargets(node.Children)
	}
	return count
}

func collectAXBudgetCandidates(
	nodes []*AXNode,
	depth int,
	out *[]axBudgetCandidate,
) {
	for _, parent := range nodes {
		collectAXBudgetCandidates(parent.Children, depth+1, out)
		run := []*AXNode{}
		flush := func() {
			if len(run) == 0 {
				return
			}
			nodeIDs := make([]string, 0, len(run))
			priority := 0
			rendered := 0
			omittedNodes := 0
			omittedTargets := 0
			for _, node := range run {
				nodeIDs = append(nodeIDs, node.ID)
				if current := axBudgetPriority(node); current > priority {
					priority = current
				}
				rendered += axRenderedNodeChars(node, depth+1)
				omittedNodes += countAXNodes([]*AXNode{node})
				omittedTargets += countAXTargets([]*AXNode{node})
			}
			summary := newAXBudgetSummary(
				parent,
				parent.ID+"/budget-estimate",
				omittedNodes,
				omittedTargets,
			)
			savings := rendered - axRenderedNodeChars(summary, depth+1)
			if savings > 0 {
				*out = append(*out, axBudgetCandidate{
					parentID: parent.ID,
					nodeIDs:  nodeIDs,
					depth:    depth,
					priority: priority,
					savings:  savings,
				})
			}
			run = nil
		}
		for _, child := range parent.Children {
			if child.Synthetic ||
				axSubtreeContainsCriticalOrBoundary(child) {
				flush()
				continue
			}
			run = append(run, child)
		}
		flush()
	}
}

func findAXNodeByID(nodes []*AXNode, id string) *AXNode {
	for _, node := range nodes {
		if node.ID == id {
			return node
		}
		if found := findAXNodeByID(node.Children, id); found != nil {
			return found
		}
	}
	return nil
}

func axBudgetParentMap(nodes []*AXNode) map[string]string {
	parents := map[string]string{}
	var visit func([]*AXNode, string)
	visit = func(current []*AXNode, parentID string) {
		for _, node := range current {
			if node == nil {
				continue
			}
			if parentID != "" {
				parents[node.ID] = parentID
			}
			visit(node.Children, node.ID)
		}
	}
	visit(nodes, "")
	return parents
}

func axBudgetIsAncestor(
	parents map[string]string,
	ancestor,
	descendant string,
) bool {
	for current := parents[descendant]; current != ""; current = parents[current] {
		if current == ancestor {
			return true
		}
	}
	return false
}

func axBudgetCandidatesOverlap(
	parents map[string]string,
	left,
	right axBudgetCandidate,
) bool {
	for _, leftID := range left.nodeIDs {
		for _, rightID := range right.nodeIDs {
			if leftID == rightID ||
				axBudgetIsAncestor(parents, leftID, rightID) ||
				axBudgetIsAncestor(parents, rightID, leftID) {
				return true
			}
		}
	}
	return false
}

func selectAXBudgetPlan(
	roots []*AXNode,
	candidates []axBudgetCandidate,
	requiredSavings int,
) []axBudgetCandidate {
	if requiredSavings <= 0 || len(candidates) == 0 {
		return nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority < candidates[j].priority
		}
		return candidates[i].savings > candidates[j].savings
	})
	parents := axBudgetParentMap(roots)
	selected := make([]axBudgetCandidate, 0, len(candidates))
	savings := 0
	for _, candidate := range candidates {
		if savings >= requiredSavings {
			break
		}
		overlap := false
		for _, existing := range selected {
			if axBudgetCandidatesOverlap(parents, candidate, existing) {
				overlap = true
				break
			}
		}
		if overlap {
			continue
		}
		selected = append(selected, candidate)
		savings += candidate.savings
	}
	return selected
}

func mergeAdjacentAXBudgetSummaries(parent *AXNode) {
	if parent == nil {
		return
	}
	out := make([]*AXNode, 0, len(parent.Children))
	for _, child := range parent.Children {
		if child.BudgetOmittedNodes > 0 && len(out) > 0 {
			previous := out[len(out)-1]
			if previous.BudgetOmittedNodes > 0 {
				previous.BudgetOmittedNodes += child.BudgetOmittedNodes
				previous.BudgetOmittedTargets += child.BudgetOmittedTargets
				previous.Properties.Omitted = axBudgetSummaryText(
					previous.BudgetOmittedNodes,
					previous.BudgetOmittedTargets,
				)
				continue
			}
		}
		out = append(out, child)
	}
	parent.Children = out
}

func applyAXBudgetCandidate(
	roots []*AXNode,
	candidate axBudgetCandidate,
	sequence int,
) (int, int, int) {
	parent := findAXNodeByID(roots, candidate.parentID)
	if parent == nil {
		return 0, 0, 0
	}
	beforeChars := axRenderedNodeChars(parent, candidate.depth)
	remove := map[string]bool{}
	for _, id := range candidate.nodeIDs {
		remove[id] = true
	}
	children := make([]*AXNode, 0, len(parent.Children))
	omittedNodes := 0
	omittedTargets := 0
	inserted := false
	var summary *AXNode
	for _, child := range parent.Children {
		if !remove[child.ID] {
			children = append(children, child)
			continue
		}
		omittedNodes += countAXNodes([]*AXNode{child})
		omittedTargets += countAXTargets([]*AXNode{child})
		if !inserted {
			summary = newAXBudgetSummary(
				parent,
				fmt.Sprintf("%s/budget:%d", parent.ID, sequence),
				0,
				0,
			)
			children = append(children, summary)
			inserted = true
		}
	}
	if omittedNodes == 0 {
		return 0, 0, 0
	}
	if summary != nil {
		summary.BudgetOmittedNodes = omittedNodes
		summary.BudgetOmittedTargets = omittedTargets
		summary.Properties.Omitted = axBudgetSummaryText(
			omittedNodes,
			omittedTargets,
		)
	}
	parent.Children = children
	mergeAdjacentAXBudgetSummaries(parent)
	for _, child := range parent.Children {
		child.Parent = parent
	}
	afterChars := axRenderedNodeChars(parent, candidate.depth)
	return omittedNodes, omittedTargets, beforeChars - afterChars
}

func applyAXStructuralBudget(
	context *DistillationContext,
	roots []*AXNode,
) []*AXNode {
	if context == nil {
		return roots
	}
	budget := axStructuralTreeBudget()
	context.StageBudgetCharsBefore = axRenderedForestChars(roots)
	context.StageBudgetCharsAfter = context.StageBudgetCharsBefore
	if context.StageBudgetCharsBefore <= budget {
		return roots
	}
	candidates := []axBudgetCandidate{}
	collectAXBudgetCandidates(roots, 1, &candidates)
	plan := selectAXBudgetPlan(
		roots,
		candidates,
		context.StageBudgetCharsBefore-budget,
	)
	for index, candidate := range plan {
		removed, targets, savings := applyAXBudgetCandidate(
			roots,
			candidate,
			index+1,
		)
		if removed == 0 {
			continue
		}
		context.StageBudgetApplied = true
		context.StageBudgetOmittedNodes += removed
		context.StageBudgetOmittedTargets += targets
		_ = savings
	}
	// The plan is disjoint, so all changes are applied without repeated
	// whole-forest accounting. Render once for the authoritative result.
	context.StageBudgetCharsAfter = axRenderedForestChars(roots)
	context.StageBudgetExhausted = context.StageBudgetCharsAfter > budget
	return roots
}
