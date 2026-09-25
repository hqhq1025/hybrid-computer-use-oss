package main

const axFindSiblingRadius = 2

func axFindContextMeaningful(node *AXNode) bool {
	if node == nil {
		return false
	}
	return node.Ref != "" ||
		axNodeHasContent(node) ||
		axNodeHasState(node) ||
		axNodeHasCapability(node) ||
		isAXSemanticBoundary(node.Role)
}

func isAXSemanticBoundary(role CanonicalRole) bool {
	switch role {
	case "window", "dialog", "document", "grid", "table", "tree",
		"treegrid", "list", "menu", "menubar", "toolbar", "tablist",
		"tabpanel", "form", "region", "group":
		return true
	default:
		return false
	}
}

func markAXFindPath(selected map[string]bool, node *AXNode) {
	for current := node; current != nil; current = current.Parent {
		selected[current.ID] = true
	}
}

func markAXFindNearby(
	selected map[string]bool,
	parent *AXNode,
	target *AXNode,
) {
	if parent == nil || target == nil {
		return
	}
	targetIndex := -1
	for index, child := range parent.Children {
		if child == target || child.ID == target.ID {
			targetIndex = index
			break
		}
	}
	if targetIndex < 0 {
		return
	}
	for direction := -1; direction <= 1; direction += 2 {
		found := 0
		for index := targetIndex + direction; index >= 0 && index < len(parent.Children) &&
			found < axFindSiblingRadius; index += direction {
			candidate := parent.Children[index]
			if !axFindContextMeaningful(candidate) {
				continue
			}
			selected[candidate.ID] = true
			found++
		}
	}
}

func markAXFindChildren(selected map[string]bool, target *AXNode) {
	if target == nil {
		return
	}
	found := 0
	for _, child := range target.Children {
		if !axFindContextMeaningful(child) {
			continue
		}
		selected[child.ID] = true
		found++
		if found == axFindSiblingRadius {
			return
		}
	}
}

func cloneAXFindContextForest(
	nodes []*AXNode,
	selected map[string]bool,
	matches map[string]bool,
	serializerHidden map[string]bool,
	parent *AXNode,
) []*AXNode {
	out := []*AXNode{}
	for _, canonical := range nodes {
		if !selected[canonical.ID] {
			continue
		}
		clone := *canonical
		clone.VisibilityEvidence = cloneVisibilityEvidence(canonical.VisibilityEvidence)
		clone.Parent = parent
		clone.Children = nil
		clone.Annotations = append(
			[]axProviderAnnotation(nil),
			canonical.Annotations...,
		)
		clone.FindMatch = matches[canonical.ID]
		clone.Children = cloneAXFindContextForest(
			canonical.Children,
			selected,
			matches,
			serializerHidden,
			&clone,
		)
		out = append(out, &clone)
	}
	return out
}

func buildAXFindContext(
	snapshot *appSnapshot,
	records []elementRecord,
	projectedIndices map[int]bool,
) []*AXNode {
	if snapshot == nil || snapshot.axSnapshot == nil {
		return nil
	}
	selected := map[string]bool{}
	matches := map[string]bool{}
	serializerHidden := map[string]bool{}
	for index := range records {
		record := &records[index]
		target := snapshot.axSnapshot.ByIndex[record.Index]
		if target == nil {
			continue
		}
		matches[target.ID] = true
		markAXFindPath(selected, target)
		markAXFindNearby(selected, target.Parent, target)
		markAXFindChildren(selected, target)
		if !projectedIndices[record.Index] {
			serializerHidden[target.ID] = true
		}
	}
	return cloneAXFindContextForest(
		snapshot.axSnapshot.CanonicalRoots,
		selected,
		matches,
		serializerHidden,
		nil,
	)
}

func newAXFindSnapshot(
	source *AXSnapshotV1,
	roots []*AXNode,
) *AXSnapshotV1 {
	if source == nil {
		return nil
	}
	snapshot := &AXSnapshotV1{
		SchemaVersion:              "AXSnapshotV1",
		AppRef:                     source.AppRef,
		AppIdentity:                source.AppIdentity,
		WindowHandle:               source.WindowHandle,
		WindowRef:                  source.WindowRef,
		Revision:                   source.Revision,
		AcquisitionIncomplete:      source.AcquisitionIncomplete,
		IdentityCoverageIncomplete: source.IdentityCoverageIncomplete,
		RefInventoryComplete:       false,
		CanonicalRoots:             roots,
		Roots:                      roots,
	}
	indexAXSnapshot(snapshot)
	return snapshot
}

func indexAXSnapshot(snapshot *AXSnapshotV1) {
	if snapshot == nil {
		return
	}
	snapshot.ByID = map[string]*AXNode{}
	snapshot.ByIndex = map[int]*AXNode{}
	var visit func([]*AXNode, *AXNode)
	visit = func(nodes []*AXNode, parent *AXNode) {
		for _, node := range nodes {
			if node == nil {
				continue
			}
			node.Parent = parent
			if node.ID != "" {
				snapshot.ByID[node.ID] = node
			}
			if node.ProviderIndex >= 0 {
				snapshot.ByIndex[node.ProviderIndex] = node
			}
			visit(node.Children, node)
		}
	}
	visit(snapshot.CanonicalRoots, nil)
}
