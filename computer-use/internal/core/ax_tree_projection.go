package core

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/hqhq1025/hybrid-computer-use-oss/computer-use/internal/contract"
)

var renderedTreeElementRE = regexp.MustCompile(`^(\t+)(\d+)\s`)
var workingStructuralRoles = map[string]bool{
	"article":          true,
	"block quote":      true,
	"column header":    true,
	"description list": true,
	"description term": true,
	"filler":           true,
	"footer":           true,
	"form":             true,
	"glass pane":       true,
	"group":            true,
	"landmark":         true,
	"layered pane":     true,
	"list":             true,
	"main":             true,
	"panel":            true,
	"root pane":        true,
	"row header":       true,
	"scroll pane":      true,
	"separator":        true,
	"section":          true,
	"split pane":       true,
	"tool tip":         true,
	"unknown":          true,
	"viewport":         true,
}

var workingTextRoles = map[string]bool{
	"static": true,
	"text":   true,
}

var workingSemanticBoundaryRoles = map[string]bool{
	"alert":         true,
	"application":   true,
	"dialog":        true,
	"frame":         true,
	"list":          true,
	"list box":      true,
	"menu":          true,
	"menu bar":      true,
	"page tab list": true,
	"table":         true,
	"tree":          true,
	"tree table":    true,
	"window":        true,
}

type workingTreeNode struct {
	line                     string
	indent                   int
	record                   *ElementRecord
	parent                   *workingTreeNode
	children                 []*workingTreeNode
	notes                    []string
	descendantLeafText       string
	structuralValueRedundant bool
	forceKeep                bool
}

type projectedTreeNode struct {
	line     string
	record   *ElementRecord
	children []*projectedTreeNode
	notes    []string
}

type projectedTreeChunk struct {
	nodes         []*projectedTreeNode
	pendingHidden int
	totalHidden   int
	branches      int
}

func renderedTreeElement(line string) (int, int, bool) {
	match := renderedTreeElementRE.FindStringSubmatch(line)
	if len(match) != 3 {
		return 0, 0, false
	}
	index, err := strconv.Atoi(match[2])
	if err != nil {
		return 0, 0, false
	}
	return index, len(match[1]), true
}

func canonicalRole(record *ElementRecord) string {
	if record == nil {
		return ""
	}
	role := strings.TrimSpace(record.LocalizedControlType)
	if role == "" {
		role = strings.TrimSpace(record.ControlType)
	}
	return strings.ToLower(role)
}

func normalizedActionName(action string) string {
	var out strings.Builder
	for _, char := range strings.ToLower(action) {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			out.WriteRune(char)
		}
	}
	return out.String()
}

func meaningfulRecordActions(record *ElementRecord) bool {
	if record == nil {
		return false
	}
	for _, action := range record.Actions {
		normalized := normalizedActionName(action)
		if normalized != "clickancestor" && normalized != "showcontextmenu" {
			return true
		}
	}
	return false
}

func recordStateTokens(record *ElementRecord) []string {
	if record == nil {
		return nil
	}
	return strings.Fields(strings.Trim(
		strings.TrimSpace(record.States),
		"[]",
	))
}

func meaningfulWorkingStates(record *ElementRecord) bool {
	for _, state := range recordStateTokens(record) {
		if state != "has-click-action" {
			return true
		}
	}
	return false
}

func hasMeaningfulWorkingClick(record *ElementRecord) bool {
	if record == nil ||
		!roleHasMeaningfulClickCapability(canonicalRole(record)) {
		return false
	}
	for _, state := range recordStateTokens(record) {
		if state == "has-click-action" {
			return true
		}
	}
	return false
}

func workingSemanticBoundary(role string) bool {
	role = strings.TrimSpace(strings.ToLower(role))
	return workingSemanticBoundaryRoles[role] ||
		strings.HasPrefix(role, "document")
}

func recordHasWorkingSignal(record *ElementRecord) bool {
	if record == nil {
		return false
	}
	return strings.TrimSpace(record.Name) != "" ||
		strings.TrimSpace(record.Value) != "" ||
		strings.TrimSpace(record.Description) != "" ||
		strings.TrimSpace(record.Placeholder) != "" ||
		meaningfulWorkingStates(record) ||
		meaningfulRecordActions(record) ||
		hasMeaningfulWorkingClick(record)
}

func duplicateWorkingIdentity(node *workingTreeNode) string {
	if node == nil || node.record == nil {
		return ""
	}
	record := node.record
	if strings.TrimSpace(record.Name) == "" {
		return ""
	}
	role := canonicalRole(record)
	if !roleHasMeaningfulClickCapability(role) &&
		!meaningfulWorkingStates(record) &&
		!meaningfulRecordActions(record) {
		return ""
	}
	return role + "\x00" + strings.ToLower(
		normalizedTreeText(record.Name),
	)
}

func workingAncestorPath(node *workingTreeNode) []*workingTreeNode {
	path := []*workingTreeNode{}
	for current := node; current != nil; current = current.parent {
		path = append(path, current)
	}
	slices.Reverse(path)
	return path
}

func markWorkingTreeDisambiguators(roots []*workingTreeNode) {
	groups := map[string][]*workingTreeNode{}
	var visit func(*workingTreeNode)
	visit = func(node *workingTreeNode) {
		if identity := duplicateWorkingIdentity(node); identity != "" {
			groups[identity] = append(groups[identity], node)
		}
		for _, child := range node.children {
			visit(child)
		}
	}
	for _, root := range roots {
		visit(root)
	}
	for _, duplicates := range groups {
		if len(duplicates) < 2 {
			continue
		}
		paths := make([][]*workingTreeNode, 0, len(duplicates))
		common := -1
		for _, node := range duplicates {
			path := workingAncestorPath(node)
			paths = append(paths, path)
			if common < 0 || len(path) < common {
				common = len(path)
			}
		}
		prefix := 0
		for prefix < common {
			candidate := paths[0][prefix]
			same := true
			for _, path := range paths[1:] {
				if path[prefix] != candidate {
					same = false
					break
				}
			}
			if !same {
				break
			}
			prefix++
		}
		for _, path := range paths {
			// The first node after the common ancestry is the smallest branch
			// boundary that distinguishes this duplicate target. Do not mark
			// the target itself; it is already intrinsically meaningful.
			if prefix < len(path)-1 {
				path[prefix].forceKeep = true
			}
		}
	}
}

func roleHasMeaningfulClickCapability(role string) bool {
	for _, fragment := range []string{
		"button",
		"check box",
		"combo box",
		"entry",
		"icon",
		"link",
		"list box",
		"list item",
		"menu item",
		"page tab",
		"radio",
		"scroll bar",
		"searchbox",
		"slider",
		"spin button",
		"textarea",
		"textfield",
		"toggle",
		"tree item",
	} {
		if strings.Contains(role, fragment) {
			return true
		}
	}
	return false
}

func projectWorkingTreeNode(node *workingTreeNode) projectedTreeChunk {
	childGroups := make([]projectedTreeChunk, 0, len(node.children))
	flattened := []*projectedTreeNode{}
	totalHidden := 0
	branches := 0
	for _, child := range node.children {
		group := projectWorkingTreeNode(child)
		if len(group.nodes) > 0 {
			childGroups = append(childGroups, group)
			flattened = append(flattened, group.nodes...)
		}
		totalHidden += group.totalHidden
		branches += group.branches
	}
	pendingHidden := 1
	for _, group := range childGroups {
		pendingHidden += group.pendingHidden
	}
	if keepInWorkingTree(node) {
		summarized, collectionHidden := summarizeHomogeneousChildren(
			node.record,
			flattened,
		)
		return projectedTreeChunk{
			nodes: []*projectedTreeNode{{
				line:     cleanWorkingTreeLine(node.line, node.record),
				record:   node.record,
				children: summarized,
				notes:    append([]string(nil), node.notes...),
			}},
			totalHidden: totalHidden + collectionHidden,
			branches:    branches,
		}
	}

	totalHidden++
	if len(childGroups) >= 2 {
		return projectedTreeChunk{
			nodes: []*projectedTreeNode{{
				line: fmt.Sprintf(
					"(layout-group role=%s branches=%d hidden=%d)",
					canonicalRole(node.record),
					len(childGroups),
					pendingHidden,
				),
				children: flattened,
			}},
			totalHidden: totalHidden,
			branches:    branches + 1,
		}
	}
	return projectedTreeChunk{
		nodes:         flattened,
		pendingHidden: pendingHidden,
		totalHidden:   totalHidden,
		branches:      branches,
	}
}

func renderProjectedTreeNode(
	node *projectedTreeNode,
	depth int,
	out *[]string,
) {
	if node == nil {
		return
	}
	*out = append(*out, strings.Repeat("\t", depth)+strings.TrimLeft(node.line, "\t"))
	for _, note := range node.notes {
		*out = append(
			*out,
			strings.Repeat("\t", depth+1)+strings.TrimLeft(note, "\t"),
		)
	}
	for _, child := range node.children {
		renderProjectedTreeNode(child, depth+1, out)
	}
}

func applyWorkingTreeProjection(snapshot *AppSnapshot, view string) {
	if snapshot == nil {
		return
	}
	snapshot.rawTreeLines = append([]string(nil), snapshot.TreeLines...)
	snapshot.serializerOmitted = 0
	snapshot.serializerBranches = 0
	if normalizedTreeView(view) == contract.TreeViewFull {
		return
	}

	records := map[int]*ElementRecord{}
	for index := range snapshot.Elements {
		record := &snapshot.Elements[index]
		records[record.Index] = record
	}
	roots := []*workingTreeNode{}
	stack := []*workingTreeNode{}
	trailers := []string{}
	for _, line := range snapshot.rawTreeLines {
		index, indent, isElement := renderedTreeElement(line)
		if !isElement {
			noteIndent := len(line) - len(strings.TrimLeft(line, "\t"))
			target := len(stack) - 1
			for target >= 0 && stack[target].indent >= noteIndent {
				target--
			}
			if target >= 0 && noteIndent > 0 {
				stack[target].notes = append(
					stack[target].notes,
					line,
				)
			} else {
				trailers = append(trailers, line)
			}
			continue
		}
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		node := &workingTreeNode{
			line:   line,
			indent: indent,
			record: records[index],
		}
		if len(stack) > 0 {
			node.parent = stack[len(stack)-1]
			node.parent.children = append(node.parent.children, node)
		} else {
			roots = append(roots, node)
		}
		stack = append(stack, node)
	}

	projectedRoots := []*projectedTreeNode{}
	for _, root := range roots {
		populateWorkingTreeFacts(root)
	}
	markWorkingTreeDisambiguators(roots)
	for _, root := range roots {
		chunk := projectWorkingTreeNode(root)
		projectedRoots = append(projectedRoots, chunk.nodes...)
		snapshot.serializerOmitted += chunk.totalHidden
		snapshot.serializerBranches += chunk.branches
	}
	lines := []string{}
	for _, root := range projectedRoots {
		renderProjectedTreeNode(root, 1, &lines)
	}
	lines = append(lines, trailers...)
	if snapshot.serializerOmitted > 0 {
		lines = append(lines, fmt.Sprintf(
			"[hidden=%d canonical=%d branches=%d; find/ref/action retain hidden nodes]",
			snapshot.serializerOmitted,
			scopedCanonicalElementCount(snapshot),
			snapshot.serializerBranches,
		))
	}
	snapshot.TreeLines = lines
}
