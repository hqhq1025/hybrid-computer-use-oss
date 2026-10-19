package main

import (
	"fmt"
	"sort"
	"strings"
)

type AXDiffKind string

const (
	AXDiffAdded           AXDiffKind = "added"
	AXDiffRemoved         AXDiffKind = "removed"
	AXDiffChanged         AXDiffKind = "changed"
	AXDiffMoved           AXDiffKind = "moved"
	AXDiffPropertyChanged AXDiffKind = "property_changed"
	AXDiffStateChanged    AXDiffKind = "state_changed"
)

type AXDiffChange struct {
	Kind            AXDiffKind
	Key             string
	Before          *AXNode
	After           *AXNode
	OrderChildren   []*AXNode
	OrderStart      int
	OrderEnd        int
	OrderMovedCount int
	RootOrder       bool
}

func axNodeDiffKey(node *AXNode) string {
	if node == nil {
		return ""
	}
	if node.Ref != "" {
		return node.Ref
	}
	if strings.Trim(node.ProviderIdentity, "\x00") != "" {
		return "provider:" + node.ProviderIdentity
	}
	return node.SemanticFingerprint
}

func axStateSignature(states AXStates) string {
	return strings.Join(renderAXStateTokens(states), ",")
}

func axPropertySignature(node *AXNode) string {
	if node == nil {
		return ""
	}
	parts := []string{string(node.Role), node.Name}
	for _, property := range renderAXProperties(node, AXRenderOptions{}) {
		parts = append(parts, property[0], property[1])
	}
	return strings.Join(parts, "\x00")
}

func axDiffLayoutWrapper(node *AXNode) bool {
	if node == nil || (node.Role != RoleGeneric && node.Role != "group") {
		return false
	}
	return !axNodeHasContent(node) &&
		!axNodeHasState(node) &&
		!axNodeHasCapability(node)
}

type axLCSMatch struct {
	before int
	after  int
}

func axNodeFullSignature(node *AXNode) string {
	if node == nil {
		return ""
	}
	return strings.Join([]string{
		string(node.Role),
		axStateSignature(node.States),
		axPropertySignature(node),
	}, "\x00")
}

func axNodeSubtreeSignature(node *AXNode, memo map[*AXNode]string) string {
	if node == nil {
		return ""
	}
	if signature, ok := memo[node]; ok {
		return signature
	}
	if strong := axDiffStrongKey(node); strong != "" {
		signature := "strong:" + strong
		memo[node] = signature
		return signature
	}
	parts := []string{axNodeFullSignature(node)}
	for _, child := range axVisibleDiffChildren(node.Children) {
		parts = append(parts, axNodeSubtreeSignature(child, memo))
	}
	signature := strings.Join(parts, "\x01")
	memo[node] = signature
	return signature
}

func axFullSignatureLCS(before, after []*AXNode) []axLCSMatch {
	rows := len(before) + 1
	cols := len(after) + 1
	memo := map[*AXNode]string{}
	beforeSignatures := make([]string, len(before))
	afterSignatures := make([]string, len(after))
	for index, node := range before {
		beforeSignatures[index] = axNodeSubtreeSignature(node, memo)
	}
	for index, node := range after {
		afterSignatures[index] = axNodeSubtreeSignature(node, memo)
	}
	table := make([][]int, rows)
	for row := range table {
		table[row] = make([]int, cols)
	}
	for row := len(before) - 1; row >= 0; row-- {
		for col := len(after) - 1; col >= 0; col-- {
			if beforeSignatures[row] == afterSignatures[col] {
				table[row][col] = table[row+1][col+1] + 1
			} else if table[row+1][col] >= table[row][col+1] {
				table[row][col] = table[row+1][col]
			} else {
				table[row][col] = table[row][col+1]
			}
		}
	}
	matches := []axLCSMatch{}
	for row, col := 0, 0; row < len(before) && col < len(after); {
		switch {
		case beforeSignatures[row] == afterSignatures[col]:
			matches = append(matches, axLCSMatch{before: row, after: col})
			row++
			col++
		case table[row+1][col] >= table[row][col+1]:
			row++
		default:
			col++
		}
	}
	return matches
}

func axDiffStrongKey(node *AXNode) string {
	if node == nil {
		return ""
	}
	if node.Ref != "" {
		return "ref:" + node.Ref
	}
	if strings.Trim(node.ProviderIdentity, "\x00") != "" {
		return "provider:" + node.ProviderIdentity
	}
	return ""
}

func axSiblingMatchKey(node *AXNode) string {
	if strong := axDiffStrongKey(node); strong != "" {
		return strong
	}
	if semantic := strings.TrimSpace(node.SemanticFingerprint); semantic != "" {
		return "semantic:" + semantic
	}
	return ""
}

func axVisibleDiffChildren(nodes []*AXNode) []*AXNode {
	out := make([]*AXNode, 0, len(nodes))
	var appendNode func(*AXNode)
	appendNode = func(node *AXNode) {
		if axDiffLayoutWrapper(node) {
			for _, child := range node.Children {
				appendNode(child)
			}
			return
		}
		out = append(out, node)
	}
	for _, node := range nodes {
		appendNode(node)
	}
	return out
}

func matchAXSiblings(before, after []*AXNode) (map[int]int, map[int]bool, map[int]bool) {
	before = axVisibleDiffChildren(before)
	after = axVisibleDiffChildren(after)
	matched := map[int]int{}
	usedBefore := map[int]bool{}
	usedAfter := map[int]bool{}

	beforeByKey := map[string][]int{}
	afterByKey := map[string][]int{}
	for index, node := range before {
		if key := axSiblingMatchKey(node); key != "" {
			beforeByKey[key] = append(beforeByKey[key], index)
		}
	}
	for index, node := range after {
		if key := axSiblingMatchKey(node); key != "" {
			afterByKey[key] = append(afterByKey[key], index)
		}
	}
	for key, beforeIndices := range beforeByKey {
		afterIndices := afterByKey[key]
		if len(beforeIndices) == 1 && len(afterIndices) == 1 {
			matched[afterIndices[0]] = beforeIndices[0]
			usedBefore[beforeIndices[0]] = true
			usedAfter[afterIndices[0]] = true
		}
	}

	for key, beforeIndices := range beforeByKey {
		afterIndices := afterByKey[key]
		if len(beforeIndices) <= 1 || len(afterIndices) <= 1 {
			continue
		}
		memo := map[*AXNode]string{}
		beforeBySignature := map[string][]int{}
		afterBySignature := map[string][]int{}
		for _, index := range beforeIndices {
			if !usedBefore[index] {
				signature := axNodeSubtreeSignature(before[index], memo)
				beforeBySignature[signature] = append(
					beforeBySignature[signature],
					index,
				)
			}
		}
		for _, index := range afterIndices {
			if !usedAfter[index] {
				signature := axNodeSubtreeSignature(after[index], memo)
				afterBySignature[signature] = append(
					afterBySignature[signature],
					index,
				)
			}
		}
		for signature, oldIndices := range beforeBySignature {
			newIndices := afterBySignature[signature]
			if len(oldIndices) == 1 && len(newIndices) == 1 {
				matched[newIndices[0]] = oldIndices[0]
				usedBefore[oldIndices[0]] = true
				usedAfter[newIndices[0]] = true
			}
		}
		remainingBefore := []int{}
		remainingAfter := []int{}
		beforeGroup := []*AXNode{}
		afterGroup := []*AXNode{}
		for _, index := range beforeIndices {
			if !usedBefore[index] {
				remainingBefore = append(remainingBefore, index)
				beforeGroup = append(beforeGroup, before[index])
			}
		}
		for _, index := range afterIndices {
			if !usedAfter[index] {
				remainingAfter = append(remainingAfter, index)
				afterGroup = append(afterGroup, after[index])
			}
		}
		for _, pair := range axFullSignatureLCS(beforeGroup, afterGroup) {
			beforeIndex := remainingBefore[pair.before]
			afterIndex := remainingAfter[pair.after]
			matched[afterIndex] = beforeIndex
			usedBefore[beforeIndex] = true
			usedAfter[afterIndex] = true
		}
	}
	return matched, usedBefore, usedAfter
}

func axMovedSiblingIndices(matched map[int]int, afterCount int) map[int]bool {
	type match struct {
		after  int
		before int
	}
	sequence := make([]match, 0, len(matched))
	for afterIndex := 0; afterIndex < afterCount; afterIndex++ {
		if beforeIndex, ok := matched[afterIndex]; ok {
			sequence = append(sequence, match{after: afterIndex, before: beforeIndex})
		}
	}
	if len(sequence) < 2 {
		return nil
	}

	tailValues := make([]int, 0, len(sequence))
	tailPositions := make([]int, 0, len(sequence))
	previous := make([]int, len(sequence))
	for index := range previous {
		previous[index] = -1
	}
	for index, item := range sequence {
		position := sort.SearchInts(tailValues, item.before)
		if position == len(tailValues) {
			tailValues = append(tailValues, item.before)
			tailPositions = append(tailPositions, index)
		} else {
			tailValues[position] = item.before
			tailPositions[position] = index
		}
		if position > 0 {
			previous[index] = tailPositions[position-1]
		}
	}
	inOrder := map[int]bool{}
	for index := tailPositions[len(tailPositions)-1]; index >= 0; index = previous[index] {
		inOrder[sequence[index].after] = true
	}
	moved := map[int]bool{}
	for _, item := range sequence {
		if !inOrder[item.after] {
			moved[item.after] = true
		}
	}
	return moved
}

func diffAXSnapshots(before, after *AXSnapshotV1) []AXDiffChange {
	changes := []AXDiffChange{}
	var compareSiblings func(*AXNode, *AXNode, []*AXNode, []*AXNode)
	compareSiblings = func(
		beforeParent,
		afterParent *AXNode,
		beforeNodes,
		afterNodes []*AXNode,
	) {
		old := axVisibleDiffChildren(beforeNodes)
		now := axVisibleDiffChildren(afterNodes)
		matched, usedOld, usedNow := matchAXSiblings(old, now)
		moved := axMovedSiblingIndices(matched, len(now))
		if len(moved) > 0 {
			orderStart := len(now)
			orderEnd := -1
			for afterIndex := range moved {
				beforeIndex := matched[afterIndex]
				orderStart = min(orderStart, min(afterIndex, beforeIndex))
				orderEnd = max(orderEnd, max(afterIndex, beforeIndex))
			}
			change := AXDiffChange{
				Kind:            AXDiffMoved,
				OrderChildren:   now,
				OrderStart:      orderStart,
				OrderEnd:        orderEnd,
				OrderMovedCount: len(moved),
			}
			if beforeParent == nil || afterParent == nil {
				change.Key = "root-order"
				change.RootOrder = true
			} else {
				change.Key = axNodeDiffKey(afterParent)
				change.Before = beforeParent
				change.After = afterParent
			}
			changes = append(changes, AXDiffChange{
				Kind:            change.Kind,
				Key:             change.Key,
				Before:          change.Before,
				After:           change.After,
				OrderChildren:   change.OrderChildren,
				OrderStart:      change.OrderStart,
				OrderEnd:        change.OrderEnd,
				OrderMovedCount: change.OrderMovedCount,
				RootOrder:       change.RootOrder,
			})
		}
		for afterIndex, current := range now {
			beforeIndex, exists := matched[afterIndex]
			if !exists {
				changes = append(changes, AXDiffChange{
					Kind: AXDiffAdded, Key: axNodeDiffKey(current), After: current,
				})
				continue
			}
			previous := old[beforeIndex]
			stateBefore := axStateSignature(previous.States)
			stateAfter := axStateSignature(current.States)
			propBefore := axPropertySignature(previous)
			propAfter := axPropertySignature(current)
			switch {
			case stateBefore != stateAfter && propBefore == propAfter:
				changes = append(changes, AXDiffChange{
					Kind: AXDiffStateChanged, Key: axNodeDiffKey(current),
					Before: previous, After: current,
				})
			case propBefore != propAfter && stateBefore == stateAfter:
				changes = append(changes, AXDiffChange{
					Kind: AXDiffPropertyChanged, Key: axNodeDiffKey(current),
					Before: previous, After: current,
				})
			case stateBefore != stateAfter || propBefore != propAfter:
				changes = append(changes, AXDiffChange{
					Kind: AXDiffChanged, Key: axNodeDiffKey(current),
					Before: previous, After: current,
				})
			}
			compareSiblings(previous, current, previous.Children, current.Children)
		}
		for beforeIndex, previous := range old {
			if !usedOld[beforeIndex] {
				changes = append(changes, AXDiffChange{
					Kind: AXDiffRemoved, Key: axNodeDiffKey(previous), Before: previous,
				})
			}
		}
		_ = usedNow
	}
	compareSiblings(nil, nil, axDiffRoots(before), axDiffRoots(after))
	return changes
}

func axDiffRoots(snapshot *AXSnapshotV1) []*AXNode {
	if snapshot == nil {
		return nil
	}
	if len(snapshot.CanonicalRoots) > 0 {
		return snapshot.CanonicalRoots
	}
	return snapshot.Roots
}

func projectedAXNodesByID(snapshot *AXSnapshotV1) map[string]*AXNode {
	out := map[string]*AXNode{}
	if snapshot == nil {
		return out
	}
	var visit func([]*AXNode)
	visit = func(nodes []*AXNode) {
		for _, node := range nodes {
			if node.ID != "" {
				out[node.ID] = node
			}
			visit(node.Children)
		}
	}
	visit(snapshot.Roots)
	return out
}

func axChangedAncestor(
	node *AXNode,
	changed map[string]bool,
) bool {
	for parent := node.Parent; parent != nil; parent = parent.Parent {
		if changed[parent.ID] {
			return true
		}
	}
	return false
}

func axSubtreeContainsChanged(node *AXNode, changed map[string]AXDiffKind) bool {
	if node == nil {
		return false
	}
	if _, ok := changed[node.ID]; ok {
		return true
	}
	for _, child := range node.Children {
		if axSubtreeContainsChanged(child, changed) {
			return true
		}
	}
	return false
}

func renderAXNodeLabelWithoutRef(node *AXNode) string {
	if node == nil {
		return ""
	}
	clone := *node
	clone.Ref = ""
	return renderAXNodeLabel(&clone)
}

func renderAXCurrentChangeBranch(
	lines *[]string,
	node *AXNode,
	depth int,
	position int,
	changed map[string]AXDiffKind,
	orderChanges map[string]AXDiffChange,
) {
	if node == nil {
		return
	}
	kind, directChange := changed[node.ID]
	properties := renderAXProperties(node, AXRenderOptions{})
	includeFullSubtree := directChange && kind == AXDiffAdded
	hasChangedChild := false
	for _, child := range node.Children {
		if axSubtreeContainsChanged(child, changed) {
			hasChangedChild = true
			break
		}
	}
	hasChildren := len(properties) > 0 || includeFullSubtree || hasChangedChild
	orderChange, orderChanged := orderChanges[node.ID]
	hasChildren = hasChildren || orderChanged
	prefix := ""
	if directChange {
		prefix = "<changed> "
	}
	label := renderAXNodeLabel(node)
	if position > 0 {
		label += fmt.Sprintf(" [position=%d]", position)
	}
	line := strings.Repeat(" ", depth) + "- " + prefix + label
	if hasChildren {
		line += ":"
	}
	*lines = append(*lines, line)
	if directChange {
		for _, property := range properties {
			*lines = append(*lines, renderAXTreePropertyLine(property, depth+1))
		}
	}
	if orderChanged {
		renderAXOrderChange(
			lines, depth+1, orderChange, changed, orderChanges,
		)
		return
	}
	if includeFullSubtree {
		var renderChildren func([]*AXNode, int)
		renderChildren = func(nodes []*AXNode, childDepth int) {
			for _, child := range nodes {
				childProperties := renderAXProperties(child, AXRenderOptions{})
				childHasChildren := len(childProperties) > 0 ||
					len(child.Children) > 0
				*lines = append(
					*lines,
					renderAXTreeNodeLine(child, childDepth, childHasChildren),
				)
				for _, property := range childProperties {
					*lines = append(
						*lines,
						renderAXTreePropertyLine(property, childDepth+1),
					)
				}
				renderChildren(child.Children, childDepth+1)
			}
		}
		renderChildren(node.Children, depth+1)
		return
	}
	for _, child := range node.Children {
		if axSubtreeContainsChanged(child, changed) {
			renderAXCurrentChangeBranch(
				lines, child, depth+1, 0, changed, orderChanges,
			)
		}
	}
}

func renderAXOrderChange(
	lines *[]string,
	depth int,
	change AXDiffChange,
	changed map[string]AXDiffKind,
	orderChanges map[string]AXDiffChange,
) {
	children := change.OrderChildren
	start := max(0, change.OrderStart)
	end := min(len(children)-1, change.OrderEnd)
	if start > end || len(children) == 0 {
		*lines = append(
			*lines,
			strings.Repeat(" ", depth)+"- /order_changed: true",
		)
		return
	}

	const maxOrderItems = 12
	candidates := map[int]bool{}
	semanticChanges := []int{}
	for index := start; index <= end; index++ {
		candidates[index] = true
	}
	for index, child := range children {
		if axSubtreeContainsChanged(child, changed) {
			semanticChanges = append(semanticChanges, index)
			candidates[index] = true
		}
		if orderChanges[child.ID].Kind == AXDiffMoved {
			candidates[index] = true
		}
	}
	selected := candidates
	if len(candidates) > maxOrderItems {
		selected = map[int]bool{}
		addEdges := func(indices []int, limit int) {
			for left, right := 0, len(indices)-1; left <= right &&
				len(selected) < limit; left, right = left+1, right-1 {
				selected[indices[left]] = true
				if right != left && len(selected) < limit {
					selected[indices[right]] = true
				}
			}
		}
		if len(semanticChanges) > 0 {
			addEdges(semanticChanges, min(maxOrderItems, len(semanticChanges)))
		}
		orderedCandidates := make([]int, 0, len(candidates))
		for index := 0; index < len(children); index++ {
			if candidates[index] {
				orderedCandidates = append(orderedCandidates, index)
			}
		}
		addEdges(orderedCandidates, maxOrderItems)
		*lines = append(*lines, fmt.Sprintf(
			"%s- /order_changed: moved=%d range=%d..%d",
			strings.Repeat(" ", depth),
			change.OrderMovedCount,
			start+1,
			end+1,
		))
	} else {
		*lines = append(
			*lines,
			strings.Repeat(" ", depth)+"- /order_changed: true",
		)
	}

	omitted := len(candidates) - len(selected)
	semanticOmitted := 0
	for _, index := range semanticChanges {
		if !selected[index] {
			semanticOmitted++
		}
	}
	for index, child := range children {
		if !selected[index] {
			continue
		}
		if axSubtreeContainsChanged(child, changed) ||
			orderChanges[child.ID].Kind == AXDiffMoved {
			renderAXCurrentChangeBranch(
				lines,
				child,
				depth,
				index+1,
				changed,
				orderChanges,
			)
			continue
		}
		*lines = append(*lines, fmt.Sprintf(
			"%s- %s [position=%d]",
			strings.Repeat(" ", depth),
			renderAXNodeLabel(child),
			index+1,
		))
	}
	if omitted > 0 {
		*lines = append(*lines, fmt.Sprintf(
			"%s- /positions_omitted: %d",
			strings.Repeat(" ", depth),
			omitted,
		))
	}
	if semanticOmitted > 0 {
		*lines = append(*lines, fmt.Sprintf(
			"%s- /semantic_changes_omitted: %d recover=observe|find",
			strings.Repeat(" ", depth),
			semanticOmitted,
		))
	}
}

func canonicalAXRefs(snapshot *AXSnapshotV1) map[string]bool {
	refs := map[string]bool{}
	if snapshot == nil {
		return refs
	}
	var visit func([]*AXNode)
	visit = func(nodes []*AXNode) {
		for _, node := range nodes {
			if ref := strings.TrimSpace(node.Ref); ref != "" {
				refs[ref] = true
			}
			visit(node.Children)
		}
	}
	visit(axDiffRoots(snapshot))
	return refs
}

func compactAbsentRefs(changes []AXDiffChange, after *AXSnapshotV1) []string {
	seen := map[string]bool{}
	refs := []string{}
	current := canonicalAXRefs(after)
	var collect func(*AXNode)
	collect = func(node *AXNode) {
		if node == nil {
			return
		}
		if ref := strings.TrimSpace(node.Ref); ref != "" &&
			!current[ref] && !seen[ref] {
			seen[ref] = true
			refs = append(refs, ref)
		}
		for _, child := range node.Children {
			collect(child)
		}
	}
	for _, change := range changes {
		if change.Kind == AXDiffRemoved {
			collect(change.Before)
		}
	}
	sort.SliceStable(refs, func(i, j int) bool {
		left, leftOK := parseMonotonicAXRef(refs[i])
		right, rightOK := parseMonotonicAXRef(refs[j])
		if leftOK && rightOK {
			return left < right
		}
		return refs[i] < refs[j]
	})
	return refs
}

func renderAXRemovedChunks(
	lines *[]string,
	changes []AXDiffChange,
	before,
	after *AXSnapshotV1,
) {
	projected := projectedAXNodesByID(before)
	currentProjected := projectedAXNodesByID(after)
	currentByStrongKey := map[string]*AXNode{}
	for _, node := range currentProjected {
		if key := axDiffStrongKey(node); key != "" {
			if _, exists := currentByStrongKey[key]; exists {
				currentByStrongKey[key] = nil
			} else {
				currentByStrongKey[key] = node
			}
		}
	}
	removed := map[string]bool{}
	currentRefs := canonicalAXRefs(after)
	for _, change := range changes {
		if change.Kind == AXDiffRemoved && change.Before != nil {
			removed[change.Before.ID] = true
		}
	}
	type removedGroup struct {
		parent     *AXNode
		nodes      []*AXNode
		historical bool
	}
	groups := []removedGroup{}
	groupIndex := map[string]int{}
	rootRemoved := []*AXNode{}
	for _, change := range changes {
		if change.Kind != AXDiffRemoved || change.Before == nil ||
			axChangedAncestor(change.Before, removed) {
			continue
		}
		node := projected[change.Before.ID]
		if node == nil {
			continue
		}
		if node.Ref != "" && currentRefs[node.Ref] {
			continue
		}
		parent := node.Parent
		if parent != nil && !removed[parent.ID] {
			var currentParent *AXNode
			if strongKey := axDiffStrongKey(parent); strongKey != "" {
				currentParent = currentByStrongKey[strongKey]
			} else if candidate := currentProjected[parent.ID]; candidate != nil &&
				candidate.Role == parent.Role &&
				strings.TrimSpace(parent.SemanticFingerprint) != "" &&
				candidate.SemanticFingerprint == parent.SemanticFingerprint {
				// Runtime IDs are traversal paths, not stable object identities.
				// Use them only when no stronger identity exists and the semantic
				// identity still agrees.
				currentParent = candidate
			}
			historical := currentParent == nil
			if currentParent != nil {
				parent = currentParent
			}
			groupKey := parent.ID
			if historical {
				groupKey = "historical:" + groupKey
			}
			index, exists := groupIndex[groupKey]
			if !exists {
				index = len(groups)
				groupIndex[groupKey] = index
				groups = append(groups, removedGroup{
					parent:     parent,
					historical: historical,
				})
			}
			groups[index].nodes = append(groups[index].nodes, node)
			continue
		}
		rootRemoved = append(rootRemoved, node)
	}
	for _, group := range groups {
		context := ""
		if group.historical {
			context = "<historical-context> "
		}
		*lines = append(
			*lines,
			" - "+context+renderAXNodeLabelWithoutRef(group.parent)+":",
		)
		for _, node := range group.nodes {
			*lines = append(
				*lines,
				"  - <removed> "+renderAXNodeLabelWithoutRef(node),
			)
		}
	}
	for _, node := range rootRemoved {
		*lines = append(
			*lines,
			" - <removed> "+renderAXNodeLabelWithoutRef(node),
		)
	}
}

func renderAXChangedChunks(
	changes []AXDiffChange,
	before,
	after *AXSnapshotV1,
	acquisitionComplete bool,
) []string {
	lines := []string{"ax_diff:"}
	if len(changes) == 0 {
		if acquisitionComplete {
			return append(lines, " effect: no_ax_change")
		}
		return append(
			lines,
			" effect: no_confirmed_ax_change",
			" observation_incomplete: true",
		)
	}
	projected := projectedAXNodesByID(after)
	changed := map[string]AXDiffKind{}
	orderChanges := map[string]AXDiffChange{}
	rootOrderChanges := []AXDiffChange{}
	for _, change := range changes {
		if change.Kind == AXDiffMoved {
			if change.RootOrder {
				rootOrderChanges = append(rootOrderChanges, change)
				continue
			}
			if change.After != nil {
				orderChanges[change.After.ID] = change
				if _, exists := changed[change.After.ID]; !exists {
					changed[change.After.ID] = change.Kind
				}
			}
			continue
		}
		if change.After != nil {
			changed[change.After.ID] = change.Kind
		}
	}
	for _, change := range rootOrderChanges {
		lines = append(lines, " - <changed> root:")
		renderAXOrderChange(&lines, 2, change, changed, orderChanges)
	}
	rootOrderRendered := len(rootOrderChanges) > 0
	for _, change := range changes {
		if change.After == nil || change.RootOrder {
			continue
		}
		if rootOrderRendered {
			continue
		}
		node := projected[change.After.ID]
		if node == nil {
			continue
		}
		hasChangedAncestor := false
		for parent := change.After.Parent; parent != nil; parent = parent.Parent {
			if _, ok := changed[parent.ID]; ok {
				hasChangedAncestor = true
				break
			}
		}
		if hasChangedAncestor {
			continue
		}
		renderAXCurrentChangeBranch(
			&lines, node, 1, 0, changed, orderChanges,
		)
	}
	if acquisitionComplete {
		renderAXRemovedChunks(&lines, changes, before, after)
		absent := compactAbsentRefs(changes, after)
		if len(absent) > 0 {
			lines = append(
				lines,
				" absent_refs: ["+strings.Join(absent, ", ")+"]",
				" absent_ref_status: absent_from_current_snapshot",
			)
		}
	} else {
		if len(lines) == 1 {
			lines = append(lines, " effect: no_confirmed_ax_change")
		}
		lines = append(lines, " observation_incomplete: true")
	}
	if len(lines) == 1 {
		lines = append(lines, " effect: changed_outside_working_projection")
	}
	return lines
}

func incrementalAXSnapshot(before, after *appSnapshot) ([]string, bool) {
	if before == nil || after == nil ||
		before.axSnapshot == nil || after.axSnapshot == nil {
		return nil, false
	}
	beforeWindowRef := snapshotLogicalWindowRef(before)
	afterWindowRef := snapshotLogicalWindowRef(after)
	if beforeWindowRef == "" || afterWindowRef == "" ||
		beforeWindowRef != afterWindowRef {
		return nil, false
	}
	if providerCoverageSummary(before.Annotations).AcquisitionIncomplete > 0 {
		return nil, false
	}
	changes := diffAXSnapshots(before.axSnapshot, after.axSnapshot)
	acquisitionComplete :=
		providerCoverageSummary(after.Annotations).AcquisitionIncomplete == 0
	lines := renderAXChangedChunks(
		changes,
		before.axSnapshot,
		after.axSnapshot,
		acquisitionComplete,
	)
	// The diff replaces the current tree only when it is shorter, whatever
	// its absolute size: a long diff on a longer tree still saves output and
	// keeps the changed nodes marked. A no-change diff is always returned.
	diffChars := len(strings.Join(lines, "\n"))
	currentChars := len(strings.Join(after.TreeLines, "\n"))
	if len(changes) > 0 && currentChars > 0 && diffChars >= currentChars {
		return nil, false
	}
	return lines, true
}
