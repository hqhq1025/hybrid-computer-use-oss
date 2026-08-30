package main

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
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
	record                   *elementRecord
	parent                   *workingTreeNode
	children                 []*workingTreeNode
	notes                    []string
	descendantLeafText       string
	structuralValueRedundant bool
	forceKeep                bool
}

type projectedTreeNode struct {
	line     string
	record   *elementRecord
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

func canonicalRole(record *elementRecord) string {
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

func meaningfulRecordActions(record *elementRecord) bool {
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

func recordStateTokens(record *elementRecord) []string {
	if record == nil {
		return nil
	}
	return strings.Fields(strings.Trim(
		strings.TrimSpace(record.States),
		"[]",
	))
}

func meaningfulWorkingStates(record *elementRecord) bool {
	for _, state := range recordStateTokens(record) {
		if state != "has-click-action" {
			return true
		}
	}
	return false
}

func hasMeaningfulWorkingClick(record *elementRecord) bool {
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

func recordHasWorkingSignal(record *elementRecord) bool {
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

func normalizedTreeText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func recordText(record *elementRecord) string {
	if record == nil {
		return ""
	}
	if text := normalizedTreeText(record.Name); text != "" {
		return text
	}
	return normalizedTreeText(record.Value)
}

func comparableTreeText(value string) string {
	return strings.ToLower(normalizedTreeText(value))
}

func containsTreeTextFragment(container, fragment string) bool {
	containerRunes := []rune(comparableTreeText(container))
	fragmentRunes := []rune(comparableTreeText(fragment))
	if len(fragmentRunes) == 0 || len(fragmentRunes) > len(containerRunes) {
		return false
	}
	isWord := func(char rune) bool {
		return unicode.IsLetter(char) || unicode.IsDigit(char)
	}
	for start := 0; start+len(fragmentRunes) <= len(containerRunes); start++ {
		matched := true
		for offset := range fragmentRunes {
			if containerRunes[start+offset] != fragmentRunes[offset] {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		end := start + len(fragmentRunes)
		leftOK := start == 0 ||
			!isWord(containerRunes[start-1]) ||
			!isWord(fragmentRunes[0])
		rightOK := end == len(containerRunes) ||
			!isWord(containerRunes[end]) ||
			!isWord(fragmentRunes[len(fragmentRunes)-1])
		if leftOK && rightOK {
			return true
		}
	}
	return false
}

func populateWorkingTreeFacts(node *workingTreeNode) string {
	if node == nil {
		return ""
	}
	childTexts := []string{}
	for _, child := range node.children {
		if text := populateWorkingTreeFacts(child); text != "" {
			childTexts = append(childTexts, text)
		}
	}
	if len(childTexts) > 0 {
		node.descendantLeafText = strings.Join(childTexts, " ")
	} else {
		node.descendantLeafText = recordText(node.record)
	}
	if node.record != nil &&
		workingStructuralRoles[canonicalRole(node.record)] &&
		strings.TrimSpace(node.record.Name) == "" &&
		strings.TrimSpace(node.record.Value) != "" &&
		strings.TrimSpace(node.record.Description) == "" &&
		strings.TrimSpace(node.record.Placeholder) == "" &&
		!meaningfulWorkingStates(node.record) &&
		!meaningfulRecordActions(node.record) &&
		len(childTexts) > 0 {
		node.structuralValueRedundant =
			comparableTreeText(node.record.Value) ==
				comparableTreeText(strings.Join(childTexts, " "))
	}
	return node.descendantLeafText
}

func structuralValueIsRedundant(node *workingTreeNode) bool {
	return node != nil && node.structuralValueRedundant
}

func textContributionIsRedundant(node *workingTreeNode) bool {
	if node == nil || node.record == nil {
		return false
	}
	role := canonicalRole(node.record)
	if !workingTextRoles[role] ||
		meaningfulWorkingStates(node.record) ||
		meaningfulRecordActions(node.record) ||
		strings.TrimSpace(node.record.Description) != "" ||
		strings.TrimSpace(node.record.Placeholder) != "" {
		return false
	}
	text := recordText(node.record)
	if text == "" {
		return false
	}
	for parent := node.parent; parent != nil; parent = parent.parent {
		if structuralValueIsRedundant(parent) {
			continue
		}
		parentText := recordText(parent.record)
		if parentText == "" {
			continue
		}
		if parentText == text || containsTreeTextFragment(parentText, text) {
			return true
		}
	}
	return false
}

func keepInWorkingTree(node *workingTreeNode) bool {
	if node == nil || node.record == nil {
		return true
	}
	if node.parent == nil || len(node.notes) > 0 {
		return true
	}
	if node.forceKeep {
		return true
	}
	if textContributionIsRedundant(node) {
		return false
	}
	role := canonicalRole(node.record)
	if workingSemanticBoundary(role) {
		return true
	}
	if workingStructuralRoles[role] &&
		structuralValueIsRedundant(node) {
		return false
	}
	return recordHasWorkingSignal(node.record)
}

func rewriteTreeMetadata(
	line string,
	rewrite func(string) (string, bool),
) string {
	var out strings.Builder
	out.Grow(len(line))
	start := 0
	inQuote := false
	escaped := false
	for index := 0; index < len(line); index++ {
		switch {
		case escaped:
			escaped = false
		case line[index] == '\\' && inQuote:
			escaped = true
		case line[index] == '"':
			inQuote = !inQuote
		case line[index] == '[' && !inQuote:
			segmentEnd := index + 1
			segmentQuoted := false
			segmentEscaped := false
		segmentLoop:
			for ; segmentEnd < len(line); segmentEnd++ {
				switch {
				case segmentEscaped:
					segmentEscaped = false
				case line[segmentEnd] == '\\' && segmentQuoted:
					segmentEscaped = true
				case line[segmentEnd] == '"':
					segmentQuoted = !segmentQuoted
				case line[segmentEnd] == ']' && !segmentQuoted:
					content := line[index+1 : segmentEnd]
					replacement, changed := rewrite(content)
					if !changed {
						index = segmentEnd
						break segmentLoop
					}
					prefixEnd := index
					if replacement == "" &&
						prefixEnd > start &&
						line[prefixEnd-1] == ' ' {
						prefixEnd--
					}
					out.WriteString(line[start:prefixEnd])
					out.WriteString(replacement)
					start = segmentEnd + 1
					index = segmentEnd
					break segmentLoop
				}
			}
		}
	}
	if start == 0 {
		return line
	}
	out.WriteString(line[start:])
	return out.String()
}

func cleanWorkingTreeLine(line string, record *elementRecord) string {
	line = normalizeWorkingQuotedWhitespace(line)
	line = rewriteTreeMetadata(
		line,
		func(content string) (string, bool) {
			if !strings.HasPrefix(content, "actions=") {
				return "", false
			}
			kept := []string{}
			for _, action := range strings.Split(
				strings.TrimPrefix(content, "actions="),
				",",
			) {
				normalized := normalizedActionName(action)
				if normalized != "clickancestor" &&
					normalized != "showcontextmenu" {
					kept = append(kept, action)
				}
			}
			if len(kept) == 0 {
				return "", true
			}
			return "[a=" + strings.Join(kept, ",") + "]", true
		},
	)
	line = stripWorkingTableCoordinates(line, record)
	if record == nil {
		return line
	}
	showClickCapability :=
		roleHasMeaningfulClickCapability(canonicalRole(record))
	return rewriteTreeMetadata(
		line,
		func(content string) (string, bool) {
			if strings.Contains(content, "=") {
				return "", false
			}
			states := strings.Fields(content)
			found := false
			kept := []string{}
			for _, state := range states {
				if state == "has-click-action" {
					found = true
					if showClickCapability {
						kept = append(kept, "click")
					}
					continue
				}
				kept = append(kept, state)
			}
			if !found {
				return "", false
			}
			if len(kept) == 0 {
				return "", true
			}
			return "[" + strings.Join(kept, " ") + "]", true
		},
	)
}

func normalizeWorkingQuotedWhitespace(line string) string {
	if !strings.ContainsAny(line, "\\\r\n\t") {
		return line
	}
	var out strings.Builder
	var quoted strings.Builder
	inQuote := false
	for index := 0; index < len(line); index++ {
		current := line[index]
		if !inQuote {
			out.WriteByte(current)
			if current == '"' {
				inQuote = true
				quoted.Reset()
			}
			continue
		}
		if current == '\\' && index+1 < len(line) {
			next := line[index+1]
			if next == 'n' || next == 'r' || next == 't' {
				quoted.WriteByte(' ')
				index++
				continue
			}
			quoted.WriteByte(current)
			quoted.WriteByte(next)
			index++
			continue
		}
		if current != '"' {
			quoted.WriteByte(current)
			continue
		}
		out.WriteString(strings.Join(strings.Fields(quoted.String()), " "))
		out.WriteByte(current)
		inQuote = false
	}
	if inQuote {
		out.WriteString(quoted.String())
	}
	return out.String()
}

func normalizedSemanticWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func equalSemanticIdentity(left, right string) bool {
	return normalizedSemanticWhitespace(left) ==
		normalizedSemanticWhitespace(right)
}

func equalFoldedSemanticIdentity(left, right string) bool {
	return strings.EqualFold(
		normalizedSemanticWhitespace(left),
		normalizedSemanticWhitespace(right),
	)
}

func stripWorkingTableCoordinates(line string, record *elementRecord) string {
	if record == nil ||
		!strings.Contains(canonicalRole(record), "table cell") {
		return line
	}
	inQuote := false
	escaped := false
	for start := 0; start+4 < len(line); start++ {
		switch {
		case escaped:
			escaped = false
			continue
		case line[start] == '\\' && inQuote:
			escaped = true
			continue
		case line[start] == '"':
			inQuote = !inQuote
			continue
		case inQuote || !strings.HasPrefix(line[start:], " (R"):
			continue
		}
		endOffset := strings.IndexByte(line[start:], ')')
		if endOffset < 0 {
			return line
		}
		end := start + endOffset
		token := line[start+2 : end]
		if !validTableCoordinateToken(token) {
			continue
		}
		return line[:start] + line[end+1:]
	}
	return line
}

func validTableCoordinateToken(token string) bool {
	if len(token) < 4 || token[0] != 'R' {
		return false
	}
	index := 1
	for index < len(token) && token[index] >= '0' && token[index] <= '9' {
		index++
	}
	if index == 1 || index >= len(token) || token[index] != 'C' {
		return false
	}
	index++
	startColumn := index
	for index < len(token) && token[index] >= '0' && token[index] <= '9' {
		index++
	}
	return index > startColumn && index == len(token)
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

func collectionContainer(record *elementRecord, childCount int) bool {
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
	parent *elementRecord,
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

func applyWorkingTreeProjection(snapshot *appSnapshot, view string) {
	if snapshot == nil {
		return
	}
	snapshot.rawTreeLines = append([]string(nil), snapshot.TreeLines...)
	snapshot.serializerOmitted = 0
	snapshot.serializerBranches = 0
	if normalizedTreeView(view) == treeViewFull {
		return
	}

	records := map[int]*elementRecord{}
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
