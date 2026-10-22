package core

import (
	"fmt"
	"strings"
)

const workingCollectionSummaryThreshold = 10

func projectedNodeCount(node *projectedTreeNode) int {
	if node == nil {
		return 0
	}
	count := 1
	for _, child := range node.children {
		count += projectedNodeCount(child)
	}
	return count
}

func projectedCollectionSignature(node *projectedTreeNode) string {
	if node == nil || node.record == nil {
		return ""
	}
	role := canonicalRole(node.record)
	if role == "" || workingSemanticBoundary(role) ||
		!collectionItemRole(role) {
		return ""
	}
	childRoles := make([]string, 0, len(node.children))
	for _, child := range node.children {
		if child == nil || child.record == nil {
			continue
		}
		childRoles = append(childRoles, canonicalRole(child.record))
	}
	return role + "|" + strings.Join(childRoles, ",")
}

func collectionItemRole(role string) bool {
	for _, fragment := range []string{
		"list item", "tree item", "table row", "row", "article", "card",
	} {
		if role == fragment || strings.Contains(role, fragment) {
			return true
		}
	}
	return false
}

func collectionContainer(record *ElementRecord, childCount int) bool {
	role := canonicalRole(record)
	if strings.HasPrefix(role, "document") {
		return true
	}
	for _, fragment := range []string{
		"list", "table", "tree", "page", "section", "group", "feed",
	} {
		if strings.Contains(role, fragment) {
			return true
		}
	}
	return false
}

func collectionItemMustRemainVisible(node *projectedTreeNode) bool {
	if node == nil || node.record == nil || len(node.notes) > 0 {
		return node != nil && len(node.notes) > 0
	}
	for _, state := range []string{
		"focused", "selected", "checked", "expanded", "active", "showing",
	} {
		if recordHasState(node.record, state) {
			return true
		}
	}
	return false
}

func summarizeHomogeneousChildren(
	parent *ElementRecord,
	children []*projectedTreeNode,
) ([]*projectedTreeNode, int) {
	if !collectionContainer(parent, len(children)) {
		return children, 0
	}
	positions := map[string][]int{}
	for index, child := range children {
		if signature := projectedCollectionSignature(child); signature != "" {
			positions[signature] = append(positions[signature], index)
		}
	}
	keep := make([]bool, len(children))
	summaryAt := map[int]*projectedTreeNode{}
	hiddenNodes := 0
	for signature, indices := range positions {
		if len(indices) < workingCollectionSummaryThreshold {
			continue
		}
		for order, index := range indices {
			if order < 3 || order >= len(indices)-2 ||
				collectionItemMustRemainVisible(children[index]) {
				keep[index] = true
			}
		}
		hiddenItems := 0
		firstHidden := -1
		for _, index := range indices {
			if keep[index] {
				continue
			}
			hiddenItems++
			hiddenNodes += projectedNodeCount(children[index])
			if firstHidden < 0 {
				firstHidden = index
			}
		}
		if hiddenItems == 0 {
			continue
		}
		role := strings.SplitN(signature, "|", 2)[0]
		summaryAt[firstHidden] = &projectedTreeNode{line: fmt.Sprintf(
			"(collection-summary role=%s items=%d shown=%d hidden=%d; "+
				"use AX mode=find to search all canonical items)",
			role,
			len(indices),
			len(indices)-hiddenItems,
			hiddenItems,
		)}
	}
	if hiddenNodes == 0 {
		return children, 0
	}
	out := make([]*projectedTreeNode, 0, len(children))
	for index, child := range children {
		if summary := summaryAt[index]; summary != nil {
			out = append(out, summary)
		}
		signature := projectedCollectionSignature(child)
		if len(positions[signature]) >= workingCollectionSummaryThreshold &&
			!keep[index] {
			continue
		}
		out = append(out, child)
	}
	return out, hiddenNodes
}
