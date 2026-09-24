package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

const (
	defaultSettleMS = 800
	maxSettleMS     = 3000
	settlePollMS    = 200
)

// expectTarget is a toolkit-independent element description. Both spellings
// can be copied from snapshot lines:
//   - field form: `role=dialog`, `name=Save`, `element_index=42`, joined by
//     commas for AND
//   - selector form: `push button "Save"`, the same syntax as AX selectors
type expectTarget struct {
	raw       string
	index     int // -1 when the target is not addressed by index
	role      string
	name      string
	exactRole bool
	exactName bool
	text      string
	state     string
	// loose is the fallback for a bare word: a match in the name,
	// description, value or placeholder suffices, because toolkits disagree
	// on which text field holds a phrase. Roles are excluded: matching a bare
	// word against roles let {"appears": "menu"} match an unrelated push
	// button, a false certainty for the agent. Roles need an explicit role=,
	// a closed vocabulary that needs no fuzzy matching.
	loose string
}

func (t expectTarget) empty() bool {
	return t.index < 0 && t.role == "" && t.name == "" &&
		t.text == "" && t.state == "" && t.loose == ""
}

func (t expectTarget) matches(record *elementRecord) bool {
	if record == nil {
		return false
	}
	if t.index >= 0 && record.Index != t.index {
		return false
	}
	role := t.role
	name := t.name
	if t.exactRole {
		if !strings.EqualFold(strings.TrimSpace(record.ControlType), strings.TrimSpace(t.role)) &&
			!strings.EqualFold(strings.TrimSpace(record.LocalizedControlType), strings.TrimSpace(t.role)) {
			return false
		}
		role = ""
	}
	if t.exactName {
		if !equalFoldedSemanticIdentity(record.Name, t.name) {
			return false
		}
		name = ""
	}
	if !recordMatches(record, role, name, t.text, t.state) {
		return false
	}
	if t.loose != "" {
		// Scan text fields only; ControlType and LocalizedControlType are
		// excluded on purpose (see the loose field).
		if !containsFold(record.Name, t.loose) &&
			!containsFold(record.Description, t.loose) &&
			!containsFold(record.Value, t.loose) &&
			!containsFold(record.Placeholder, t.loose) {
			return false
		}
	}
	return true
}

func (t expectTarget) find(snapshot *appSnapshot) *elementRecord {
	if snapshot == nil {
		return nil
	}
	for index := range snapshot.Elements {
		if t.matches(&snapshot.Elements[index]) {
			return &snapshot.Elements[index]
		}
	}
	return nil
}

func (t expectTarget) countMatches(snapshot *appSnapshot) int {
	if snapshot == nil {
		return 0
	}
	total := 0
	for index := range snapshot.Elements {
		if t.matches(&snapshot.Elements[index]) {
			total++
		}
	}
	return total
}

func (t expectTarget) matching(snapshot *appSnapshot) []*elementRecord {
	if snapshot == nil {
		return nil
	}
	matches := []*elementRecord{}
	for index := range snapshot.Elements {
		if t.matches(&snapshot.Elements[index]) {
			matches = append(matches, &snapshot.Elements[index])
		}
	}
	return matches
}

func (t expectTarget) roleCounts(snapshot *appSnapshot) map[string]int {
	counts := map[string]int{}
	for _, record := range t.matching(snapshot) {
		counts[record.ControlType]++
	}
	return counts
}

func sortedRoleUnion(first, second map[string]int) []string {
	seen := map[string]bool{}
	for role := range first {
		seen[role] = true
	}
	for role := range second {
		seen[role] = true
	}
	roles := make([]string, 0, len(seen))
	for role := range seen {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return roles
}

func (t expectTarget) findFocused(snapshot *appSnapshot) *elementRecord {
	if snapshot == nil {
		return nil
	}
	for index := range snapshot.Elements {
		record := &snapshot.Elements[index]
		if t.matches(record) && containsFold(record.States, "focused") {
			return record
		}
	}
	return nil
}

func runtimePathContains(ancestor, descendant []int) bool {
	// The provider uses [] for the accessibility root path. JSON omits that
	// empty slice, but it still semantically contains every non-root
	// descendant.
	if len(ancestor) == 0 {
		return len(descendant) > 0
	}
	if len(descendant) < len(ancestor) {
		return false
	}
	for index := range ancestor {
		if ancestor[index] != descendant[index] {
			return false
		}
	}
	return true
}

var contentPlaneRoles = map[string]bool{
	"canvas":       true,
	"drawing area": true,
	"image map":    true,
	"page":         true,
	"terminal":     true,
	"tree table":   true,
	"web view":     true,
}

func isContentPlaneRole(role string) bool {
	role = strings.ToLower(strings.TrimSpace(role))
	return strings.HasPrefix(role, "document") || contentPlaneRoles[role]
}

func isContentPlaneRecord(record *elementRecord) bool {
	if record == nil {
		return false
	}
	return isContentPlaneRole(record.ControlType) ||
		isContentPlaneRole(record.LocalizedControlType)
}

func topLevelContentRoots(snapshot *appSnapshot) []*elementRecord {
	if snapshot == nil {
		return nil
	}
	candidates := make([]*elementRecord, 0)
	for index := range snapshot.Elements {
		record := &snapshot.Elements[index]
		if isContentPlaneRecord(record) {
			candidates = append(candidates, record)
		}
	}
	roots := make([]*elementRecord, 0, len(candidates))
	for _, candidate := range candidates {
		nested := false
		for _, possibleAncestor := range candidates {
			if candidate == possibleAncestor ||
				slices.Equal(candidate.RuntimeID, possibleAncestor.RuntimeID) {
				continue
			}
			if runtimePathContains(possibleAncestor.RuntimeID, candidate.RuntimeID) {
				nested = true
				break
			}
		}
		if !nested {
			roots = append(roots, candidate)
		}
	}
	return roots
}

func contentRootCandidatesDescription(roots []*elementRecord) string {
	parts := make([]string, 0, len(roots))
	for _, root := range roots {
		parts = append(parts, fmt.Sprintf(
			"%d %s %q",
			root.Index,
			canonicalRole(root),
			root.Name,
		))
	}
	return strings.Join(parts, "; ")
}

func selectContentScopeRoot(snapshot *appSnapshot) (*elementRecord, error) {
	roots := topLevelContentRoots(snapshot)
	switch len(roots) {
	case 0:
		return nil, errors.New(
			`scope="content" found no top-level content-plane accessibility node. ` +
				`Use scope="window" to inspect the complete application window or GUI ` +
				`when the required content is not exposed through accessibility`,
		)
	case 1:
		return roots[0], nil
	}

	focused := focusedRecord(snapshot)
	if focused != nil {
		matches := make([]*elementRecord, 0, 1)
		for _, root := range roots {
			if runtimePathContains(root.RuntimeID, focused.RuntimeID) {
				matches = append(matches, root)
			}
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
	}

	focus := "no canonical element reports focus"
	if focused != nil {
		focus = fmt.Sprintf(
			"focus is on %d %s %q outside a unique content root",
			focused.Index,
			canonicalRole(focused),
			focused.Name,
		)
	}
	return nil, fmt.Errorf(
		`scope="content" is ambiguous: %d top-level content-plane nodes are visible and %s. `+
			`Candidates: %s. Use scope="window" to choose the intended surface`,
		len(roots),
		focus,
		contentRootCandidatesDescription(roots),
	)
}

func recordInA11yScope(snapshot *appSnapshot, record *elementRecord) bool {
	if snapshot == nil || record == nil ||
		normalizedA11yScope(snapshot.scope) == a11yScopeWindow {
		return true
	}
	return snapshot.scopeRoot != nil &&
		runtimePathContains(snapshot.scopeRoot.RuntimeID, record.RuntimeID)
}

func scopedCanonicalElementCount(snapshot *appSnapshot) int {
	if snapshot == nil {
		return 0
	}
	if normalizedA11yScope(snapshot.scope) == a11yScopeContent {
		return snapshot.scopedCanonicalCount
	}
	return len(snapshot.Elements)
}

func leadingTabs(line string) int {
	count := 0
	for count < len(line) && line[count] == '\t' {
		count++
	}
	return count
}

func scopedTreeLines(
	snapshot *appSnapshot,
	root *elementRecord,
) ([]string, int, error) {
	if snapshot == nil || root == nil {
		return nil, 0, errors.New("content scope requires a snapshot and root")
	}
	records := make(map[int]*elementRecord, len(snapshot.Elements))
	count := 0
	for index := range snapshot.Elements {
		record := &snapshot.Elements[index]
		records[record.Index] = record
		if runtimePathContains(root.RuntimeID, record.RuntimeID) {
			count++
		}
	}

	rootIndent := -1
	for _, line := range snapshot.TreeLines {
		index, indent, ok := renderedTreeElement(line)
		if ok && index == root.Index {
			rootIndent = indent
			break
		}
	}
	if rootIndent < 0 {
		return nil, 0, fmt.Errorf(
			`scope="content" selected %d %s %q, but that root was not rendered within `+
				`the current provider budget. Use scope="window", mode=find, or a rooted observation`,
			root.Index,
			canonicalRole(root),
			root.Name,
		)
	}

	out := make([]string, 0, len(snapshot.TreeLines))
	includeFollowingNotes := false
	for _, line := range snapshot.TreeLines {
		index, indent, isElement := renderedTreeElement(line)
		if isElement {
			record := records[index]
			includeFollowingNotes = record != nil &&
				runtimePathContains(root.RuntimeID, record.RuntimeID)
			if !includeFollowingNotes {
				continue
			}
			newIndent := indent - rootIndent + 1
			if newIndent < 1 {
				newIndent = 1
			}
			out = append(out,
				strings.Repeat("\t", newIndent)+strings.TrimLeft(line, "\t"))
			continue
		}
		if !includeFollowingNotes || strings.TrimSpace(line) == "" {
			continue
		}
		indent = leadingTabs(line)
		if indent <= rootIndent {
			continue
		}
		newIndent := indent - rootIndent + 1
		out = append(out,
			strings.Repeat("\t", newIndent)+strings.TrimLeft(line, "\t"))
	}
	return out, count, nil
}

func recordsUnderRoot(
	snapshot *appSnapshot,
	root *elementRecord,
) []*elementRecord {
	if snapshot == nil || root == nil {
		return nil
	}
	records := make([]*elementRecord, 0)
	for index := range snapshot.Elements {
		record := &snapshot.Elements[index]
		if runtimePathContains(root.RuntimeID, record.RuntimeID) {
			records = append(records, record)
		}
	}
	return records
}

func contentRevisionForRecords(
	snapshot *appSnapshot,
	root *elementRecord,
) string {
	records := recordsUnderRoot(snapshot, root)
	if len(records) == 0 {
		return ""
	}
	var canonical strings.Builder
	for _, record := range records {
		depth := len(record.RuntimeID) - len(root.RuntimeID)
		if depth < 0 {
			depth = 0
		}
		canonical.WriteString(strings.Repeat("\t", depth))
		canonical.WriteString(canonicalRole(record))
		canonical.WriteByte('\x00')
		canonical.WriteString(normalizedTreeText(record.Name))
		canonical.WriteByte('\x00')
		canonical.WriteString(normalizedTreeText(record.Value))
		canonical.WriteByte('\x00')
		canonical.WriteString(normalizedTreeText(record.Description))
		canonical.WriteByte('\x00')
		canonical.WriteString(normalizedTreeText(record.Placeholder))
		canonical.WriteByte('\x00')
		canonical.WriteString(strings.TrimSpace(record.States))
		canonical.WriteByte('\x00')
		canonical.WriteString(strings.Join(record.Actions, "\x1f"))
		canonical.WriteByte('\n')
	}
	canonical.WriteString("selected_text=")
	canonical.WriteString(snapshot.SelectedText)
	sum := sha256.Sum256([]byte(canonical.String()))
	return fmt.Sprintf("c1:%x", sum[:12])
}

func validContentRevision(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != len("c1:")+24 ||
		!strings.HasPrefix(value, "c1:") {
		return false
	}
	for _, char := range value[len("c1:"):] {
		if !((char >= '0' && char <= '9') ||
			(char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func applyA11yScope(snapshot *appSnapshot, scope string) error {
	if snapshot == nil {
		return errors.New("cannot apply accessibility scope to a nil snapshot")
	}
	scope = normalizedA11yScope(scope)
	snapshot.scope = scope
	snapshot.scopeRoot = nil
	snapshot.scopedCanonicalCount = len(snapshot.Elements)
	snapshot.contentRoot = nil
	snapshot.contentCanonicalCount = 0
	snapshot.contentRevision = ""
	root, err := selectContentScopeRoot(snapshot)
	if err != nil {
		if scope == a11yScopeWindow {
			return nil
		}
		return err
	}
	count := len(recordsUnderRoot(snapshot, root))
	snapshot.contentRoot = root
	snapshot.contentCanonicalCount = count
	snapshot.contentRevision = contentRevisionForRecords(snapshot, root)
	if scope == a11yScopeWindow {
		return nil
	}
	snapshot.scopeRoot = root
	snapshot.scopedCanonicalCount = count
	return nil
}

func recordByIndex(snapshot *appSnapshot, index int) *elementRecord {
	if snapshot == nil {
		return nil
	}
	for recordIndex := range snapshot.Elements {
		record := &snapshot.Elements[recordIndex]
		if record.Index == index {
			return record
		}
	}
	return nil
}

func focusedObservationRoot(snapshot *appSnapshot) *elementRecord {
	focused := focusedRecord(snapshot)
	if focused == nil {
		return nil
	}
	// Prefer a small nearby ancestor so labels and sibling controls remain
	// visible. Do not turn one focused leaf into a whole-document replay.
	minDepth := len(focused.RuntimeID) - 3
	if minDepth < 1 {
		minDepth = 1
	}
	for depth := len(focused.RuntimeID) - 1; depth >= minDepth; depth-- {
		var ancestor *elementRecord
		for index := range snapshot.Elements {
			record := &snapshot.Elements[index]
			if len(record.RuntimeID) == depth &&
				runtimePathContains(record.RuntimeID, focused.RuntimeID) &&
				recordInA11yScope(snapshot, record) {
				ancestor = record
				break
			}
		}
		if ancestor == nil {
			continue
		}
		descendants := 0
		for index := range snapshot.Elements {
			record := &snapshot.Elements[index]
			if recordInA11yScope(snapshot, record) &&
				runtimePathContains(ancestor.RuntimeID, record.RuntimeID) {
				descendants++
			}
		}
		if descendants >= 2 && descendants <= 80 {
			return ancestor
		}
	}
	return focused
}

func resolveObservationRoot(
	snapshot *appSnapshot,
	root map[string]any,
) (*elementRecord, string, error) {
	if len(root) == 0 {
		return nil, "", nil
	}
	if root["focused"] == true {
		record := focusedObservationRoot(snapshot)
		if record == nil {
			return nil, "", errors.New(
				"root focused requested, but no canonical element reports focus",
			)
		}
		return record, "focused", nil
	}
	if rawEntry, present := root["_resolved_ref_entry"]; present {
		entry, ok := rawEntry.(*AXRefEntry)
		if !ok || entry == nil {
			return nil, "", errors.New(
				"root ref could not be resolved from the server registry",
			)
		}
		record, err := resolveProviderBoundObservationRoot(snapshot, entry)
		if err != nil {
			return nil, "", err
		}
		if !recordInA11yScope(snapshot, record) {
			return nil, "", fmt.Errorf(
				"root %d %s %q is outside scope=%q",
				record.Index,
				canonicalRole(record),
				record.Name,
				normalizedA11yScope(snapshot.scope),
			)
		}
		return record, "ref", nil
	}
	address := ""
	if rawRef, present := root["ref"]; present {
		ref, ok := rawRef.(string)
		if ok && isShortAXWindowRef(strings.TrimSpace(ref)) {
			return nil, "", errors.New(
				"root.ref must identify an element with rN; wN is only for window activate/close",
			)
		}
		if ok && isSemanticRef(strings.TrimSpace(ref)) {
			// Public roots only accept rN refs. The service expands a
			// validated rN to this trusted, window-bound representation before
			// resolving it against the fresh canonical tree.
			address = strings.TrimSpace(ref)
		}
	}
	if address == "" {
		var err error
		address, _, err = a11yTargetAddress(map[string]any{"target": root})
		if err != nil {
			return nil, "", err
		}
	}
	var record *elementRecord
	mode := "target"
	if numericElementAddress(address) {
		index, _ := strconv.Atoi(address)
		record = recordByIndex(snapshot, index)
		if record == nil {
			return nil, "", fmt.Errorf(
				"root index %d is not present in the fresh canonical snapshot",
				index,
			)
		}
	} else {
		target, err := semanticTargetFromAddress(address)
		if err != nil {
			return nil, "", err
		}
		if target.WindowHandle != "" &&
			target.WindowHandle != snapshot.WindowHandle {
			return nil, "", fmt.Errorf(
				"stale_window: root ref belongs to a different window; " +
					"observe or find the target again",
			)
		}
		record, err = lookupBySelector(snapshot, target.Selector, "")
		if err != nil {
			return nil, "", err
		}
	}
	if !recordInA11yScope(snapshot, record) {
		return nil, "", fmt.Errorf(
			"root %d %s %q is outside scope=%q",
			record.Index,
			canonicalRole(record),
			record.Name,
			normalizedA11yScope(snapshot.scope),
		)
	}
	return record, mode, nil
}

func sameRuntimePath(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func entryMatchesRuntimePath(entry *AXRefEntry, runtimeID []int) bool {
	if entry == nil {
		return false
	}
	if sameRuntimePath(entry.RuntimeID, runtimeID) {
		return true
	}
	for _, alias := range entry.RuntimeIDAliases {
		if sameRuntimePath(alias, runtimeID) {
			return true
		}
	}
	return false
}

func resolveProviderBoundObservationRoot(
	snapshot *appSnapshot,
	entry *AXRefEntry,
) (*elementRecord, error) {
	if snapshot == nil || entry == nil {
		return nil, errors.New("root ref cannot be resolved without a snapshot")
	}
	if strings.TrimSpace(entry.WindowHandle) == "" ||
		entry.WindowHandle != snapshot.WindowHandle {
		return nil, errors.New(
			"stale_window: root ref belongs to a different window; " +
				"observe or find the target again",
		)
	}
	matches := make([]*elementRecord, 0, 2)
	for index := range snapshot.Elements {
		record := &snapshot.Elements[index]
		if record.ProviderBusName == entry.ProviderBusName &&
			record.ProviderObjectPath == entry.ProviderObjectPath &&
			record.ProcessGeneration == entry.ProcessGeneration {
			matches = append(matches, record)
		}
	}
	if len(matches) > 1 &&
		(len(entry.RuntimeID) > 0 || len(entry.RuntimeIDAliases) > 0) {
		narrowed := make([]*elementRecord, 0, len(matches))
		for _, record := range matches {
			if entryMatchesRuntimePath(entry, record.RuntimeID) {
				narrowed = append(narrowed, record)
			}
		}
		matches = narrowed
	}
	switch len(matches) {
	case 0:
		return nil, errors.New(
			"stale_element_ref: the provider-bound root is no longer present; " +
				"use find to obtain a current ref",
		)
	case 1:
		return matches[0], nil
	default:
		return nil, errors.New(
			"ambiguous_target: the provider-bound root does not identify one " +
				"current canonical element; use find to obtain a current ref",
		)
	}
}

func applyObservationRoot(
	snapshot *appSnapshot,
	root map[string]any,
) error {
	if snapshot == nil {
		return errors.New("cannot apply observation root to a nil snapshot")
	}
	snapshot.observationRoot = nil
	snapshot.observationRootMode = ""
	snapshot.rootedCanonicalCount = 0
	record, mode, err := resolveObservationRoot(snapshot, root)
	if err != nil || record == nil {
		return err
	}
	snapshot.observationRoot = record
	snapshot.observationRootMode = mode
	snapshot.rootedCanonicalCount = len(recordsUnderRoot(snapshot, record))
	return nil
}

func projectedSubtreeLines(
	snapshot *appSnapshot,
	root *elementRecord,
) ([]string, error) {
	if snapshot == nil || root == nil {
		return nil, errors.New("subtree projection requires a snapshot and root")
	}
	if snapshot.axSnapshot == nil {
		return nil, errors.New("subtree projection requires AXSnapshotV1")
	}
	canonical := snapshot.axSnapshot.ByIndex[root.Index]
	if canonical == nil {
		return nil, fmt.Errorf(
			"subtree root %d is absent from AXSnapshotV1", root.Index,
		)
	}
	canonicalRoots := cloneAXForest([]*AXNode{canonical}, nil)
	typed := &AXSnapshotV1{
		SchemaVersion:  snapshot.axSnapshot.SchemaVersion,
		AppRef:         snapshot.axSnapshot.AppRef,
		AppIdentity:    snapshot.axSnapshot.AppIdentity,
		WindowRef:      snapshot.axSnapshot.WindowRef,
		WindowHandle:   snapshot.axSnapshot.WindowHandle,
		Revision:       snapshot.axSnapshot.Revision,
		CanonicalRoots: canonicalRoots,
		Roots:          cloneAXForest(canonicalRoots, nil),
		ByID:           snapshot.axSnapshot.ByID,
		ByIndex:        snapshot.axSnapshot.ByIndex,
	}
	if err := distillAXSnapshot(typed); err != nil {
		return nil, err
	}
	return RenderAXSnapshotYAML(typed, AXRenderOptions{RootKey: "ax"})
}
