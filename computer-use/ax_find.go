package main

import (
	"fmt"
	"strconv"
	"strings"
)

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

// recordMatches reports whether a record satisfies the query; conditions
// combine with AND.
func recordMatches(record *elementRecord, role, name, text, state string) bool {
	if role != "" {
		modelRole := canonicalRoleDescriptor(
			ontologyProviderRole(record), nil,
		).ModelRole
		if !containsFold(record.ControlType, role) &&
			!containsFold(record.LocalizedControlType, role) &&
			!containsFold(string(modelRole), role) {
			return false
		}
	}
	if name != "" && !containsFold(record.Name, name) {
		return false
	}
	if state != "" && !containsFold(record.States, state) {
		return false
	}
	if text != "" {
		// text is the fallback when the caller does not know which field
		// holds the string, so it scans name, description, value and
		// placeholder. Toolkits disagree on the field: the same search box
		// exposes its hint as a placeholder in GTK and as the name in
		// Electron.
		if !containsFold(record.Name, text) &&
			!containsFold(record.Description, text) &&
			!containsFold(record.Value, text) &&
			!containsFold(record.Placeholder, text) {
			return false
		}
	}
	return true
}

func recordMatchesFind(
	record *elementRecord,
	role, name, text, state string,
	nameExact, textExact bool,
) bool {
	if record == nil {
		return false
	}
	if role != "" {
		modelRole := canonicalRoleDescriptor(
			ontologyProviderRole(record), nil,
		).ModelRole
		if !containsFold(record.ControlType, role) &&
			!containsFold(record.LocalizedControlType, role) &&
			!containsFold(string(modelRole), role) {
			return false
		}
	}
	if name != "" {
		if nameExact {
			if !strings.EqualFold(
				strings.TrimSpace(record.Name),
				strings.TrimSpace(name),
			) {
				return false
			}
		} else if !containsFold(record.Name, name) {
			return false
		}
	}
	if state != "" && !containsFold(record.States, state) {
		return false
	}
	if text != "" {
		fields := []string{
			record.Name,
			record.Description,
			record.Value,
			record.Placeholder,
		}
		hit := false
		for _, field := range fields {
			if textExact {
				hit = strings.EqualFold(
					strings.TrimSpace(field),
					strings.TrimSpace(text),
				)
			} else {
				hit = containsFold(field, text)
			}
			if hit {
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

func matchedFindTextField(
	record *elementRecord,
	text string,
	textExact bool,
) (string, string, bool) {
	if record == nil || text == "" {
		return "", "", false
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "name", value: record.Name},
		{name: "description", value: record.Description},
		{name: "value", value: record.Value},
		{name: "placeholder", value: record.Placeholder},
	} {
		matched := containsFold(field.value, text)
		if textExact {
			matched = strings.EqualFold(
				strings.TrimSpace(field.value),
				strings.TrimSpace(text),
			)
		}
		if matched {
			return field.name, clipRunes([]rune(field.value)), true
		}
	}
	return "", "", false
}

func describeQuery(
	role, name, text, state string,
	nameExact, textExact bool,
) string {
	parts := []string{}
	for _, pair := range [][2]string{
		{"role", role}, {"name", name}, {"text", text}, {"state", state},
	} {
		if pair[1] != "" {
			operator := "~"
			if (pair[0] == "name" && nameExact) ||
				(pair[0] == "text" && textExact) {
				operator = "="
			}
			parts = append(parts, fmt.Sprintf(
				"%s%s%q",
				pair[0],
				operator,
				pair[1],
			))
		}
	}
	return strings.Join(parts, " ")
}

func findProjectionDescription(snapshot *appSnapshot) (string, bool) {
	scope := ""
	if snapshot != nil &&
		normalizedA11yScope(snapshot.scope) == a11yScopeContent {
		scope = "content-scoped "
	}
	if snapshot != nil &&
		snapshot.projection.Prune != nil &&
		!*snapshot.projection.Prune {
		return "current " + scope + "unpruned snapshot", false
	}
	return "current " + scope + "pruned snapshot", true
}

func (s *service) find(
	app, role, name, text, state string,
	limit int,
	nameExact, textExact bool,
	scope string,
	targetWindowHandle ...string,
) toolCallResult {
	if app == "" {
		return textResult("Missing required argument: app", true)
	}
	if role == "" && name == "" && text == "" && state == "" {
		return textResult("find needs at least one of role, name, text, or state. For a fresh tree, call AX mode=observe.", true)
	}
	if limit <= 0 {
		limit = 20
	}
	noScreenshot := false
	request := axServiceRequest{
		Operation:         axServiceObserve,
		App:               app,
		IncludeScreenshot: &noScreenshot,
		Scope:             normalizedA11yScope(scope),
	}
	if len(targetWindowHandle) > 0 {
		request.TargetWindowHandle = strings.TrimSpace(targetWindowHandle[0])
	}
	snapshot, notes, result := s.refreshProjectedSnapshot(app, request)
	if result.IsError {
		return result
	}
	followsFailedAction := false
	if note := s.consumeFailedObservation(app, snapshot); note != "" {
		notes = append([]string{note}, notes...)
		followsFailedAction = true
	}
	notes = compactFindNotes(notes)

	matched := []elementRecord{}
	for _, record := range snapshot.Elements {
		if !recordInA11yScope(snapshot, &record) {
			continue
		}
		if recordMatchesFind(
			&record,
			role,
			name,
			text,
			state,
			nameExact,
			textExact,
		) {
			if field, value, ok :=
				matchedFindTextField(&record, text, textExact); ok {
				record.FindMatchedField = field
				record.FindMatchedText = value
			}
			matched = append(matched, record)
		}
	}

	query := describeQuery(role, name, text, state, nameExact, textExact)
	var out strings.Builder
	if len(matched) == 0 {
		fmt.Fprintf(
			&out,
			"window: %q\nmatches: 0 query=%s\n",
			snapshot.WindowTitle,
			strconv.Quote(query),
		)
		hintFrom := name
		if hintFrom == "" {
			hintFrom = text
		}
		if near := closestElements(snapshot, hintFrom, 3); len(near) > 0 {
			out.WriteString("closest:\n")
			for _, line := range near {
				out.WriteString("- " + strings.TrimSpace(line) + "\n")
			}
		}
		response := withFailedActionContinuity(
			snapshot.textWithNotes(out.String(), notes),
			followsFailedAction,
		)
		addTreeProjectionMetadata(&response, snapshot)
		response.StructuredContent["find_match_count"] = 0
		return response
	}
	return s.renderFindPage(
		snapshot,
		matched,
		0,
		limit,
		query,
		notes,
		followsFailedAction,
	)
}

func (s *service) continueFind(cursor string, limit int) toolCallResult {
	resolution, err := s.findCursorRegistry.Resolve(cursor)
	if err != nil {
		return textResult(
			"AX find cursor: "+err.Error(), true,
		).withEvidenceScope("error_only")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > maxAXFindPageSize {
		return textResult(fmt.Sprintf(
			"limit must be at most %d", maxAXFindPageSize,
		), true).withEvidenceScope("error_only")
	}
	entry := resolution.Cursor
	resultSet := resolution.ResultSet
	return s.renderFindPage(
		resultSet.Snapshot,
		resultSet.Matches,
		entry.Offset,
		limit,
		resultSet.Query,
		nil,
		false,
		entry.ResultSetID,
	)
}

func (s *service) renderFindPage(
	snapshot *appSnapshot,
	matched []elementRecord,
	offset,
	limit int,
	query string,
	notes []string,
	followsFailedAction bool,
	resultSetID ...string,
) toolCallResult {
	if snapshot == nil || snapshot.axSnapshot == nil {
		return textResult(
			"AX find cursor has no recoverable snapshot; run AX mode=find again",
			true,
		).withEvidenceScope("error_only")
	}
	if offset < 0 || offset >= len(matched) {
		return textResult(
			"AX find cursor is exhausted; run AX mode=find again",
			true,
		).withEvidenceScope("error_only")
	}
	end := offset + limit
	if end > len(matched) {
		end = len(matched)
	}
	shown := matched[offset:end]
	setID := ""
	if len(resultSetID) > 0 {
		setID = strings.TrimSpace(resultSetID[0])
	}
	frozenRefs := setID != ""
	projected := axProjectedIndices(snapshot.axSnapshot.Roots)
	if !frozenRefs && end < len(matched) {
		for index := range matched {
			record := &matched[index]
			if !projected[record.Index] {
				s.rememberExposedHiddenRef(snapshot, *record)
			}
		}
		allFindNodes := buildAXFindContext(snapshot, matched, projected)
		allFindSnapshot := newAXFindSnapshot(
			snapshot.axSnapshot, allFindNodes,
		)
		s.refRegistry.AssignSnapshotRefs(allFindSnapshot)
		s.stabilizeFindRefs(snapshot, allFindSnapshot)
		cursorSnapshot := *snapshot
		cursorSnapshot.axSnapshot = allFindSnapshot
		cursorSnapshot.Elements = append([]elementRecord(nil), matched...)
		snapshot = &cursorSnapshot
		projected = axProjectedIndices(allFindSnapshot.Roots)
		frozenRefs = true
	}
	var out strings.Builder
	fmt.Fprintf(&out, "window: %q\nmatches: %d query=%s",
		snapshot.WindowTitle, len(matched), strconv.Quote(query))
	fmt.Fprintf(&out, " range=%d..%d", offset+1, end)
	nextCursor := ""
	if end < len(matched) {
		if setID == "" {
			nextCursor = s.findCursorRegistry.Create(
				snapshot, matched, end, query,
			)
			if nextCursor != "" {
				if resolution, err := s.findCursorRegistry.Resolve(
					nextCursor,
				); err == nil && resolution.ResultSet != nil {
					snapshot = resolution.ResultSet.Snapshot
					setID = resolution.Cursor.ResultSetID
				}
			}
		} else {
			nextCursor = s.findCursorRegistry.Continue(setID, end)
		}
		if nextCursor != "" {
			fmt.Fprintf(&out, " next_cursor=%s", nextCursor)
		} else {
			out.WriteString(
				" continuation=unavailable_budget refine_query=true",
			)
		}
	}
	out.WriteString("\n")
	for index := range shown {
		record := &shown[index]
		if !projected[record.Index] {
			s.rememberExposedHiddenRef(snapshot, *record)
		}
	}
	findNodes := buildAXFindContext(snapshot, shown, projected)
	findSnapshot := newAXFindSnapshot(snapshot.axSnapshot, findNodes)
	if !frozenRefs {
		s.refRegistry.AssignSnapshotRefs(findSnapshot)
		s.stabilizeFindRefs(snapshot, findSnapshot)
	}
	findLines, renderErr := RenderAXSnapshotYAML(
		findSnapshot,
		AXRenderOptions{RootKey: "matches"},
	)
	if renderErr != nil {
		return textResult("AX find renderer failed: "+renderErr.Error(), true)
	}
	if len(findLines) > 0 {
		out.WriteString(strings.Join(findLines, "\n"))
		out.WriteByte('\n')
	}
	out.WriteString("addressability:\n")
	for matchIndex, record := range shown {
		node := axNodeByIndex(findNodes, record.Index)
		matchEvidence := ""
		if record.FindMatchedField != "" {
			matchEvidence = fmt.Sprintf(
				" matched_field=%s matched_text=%s",
				record.FindMatchedField,
				strconv.Quote(record.FindMatchedText),
			)
		}
		if node != nil && node.Ref != "" {
			fmt.Fprintf(
				&out,
				" - match=%d%s target={\"ref\":%q}\n",
				offset+matchIndex+1,
				matchEvidence,
				node.Ref,
			)
			continue
		}
		reason := "stable_identity_unavailable"
		if node != nil {
			_, reason, _ = axNodeRefEligibility(findSnapshot, node)
		}
		fmt.Fprintf(
			&out,
			" - match=%d%s addressable=false reason=%s; use nearby context, GUI, or a fresh observation\n",
			offset+matchIndex+1,
			matchEvidence,
			reason,
		)
	}
	response := withFailedActionContinuity(
		snapshot.textWithNotes(out.String(), notes),
		followsFailedAction,
	)
	addTreeProjectionMetadata(&response, snapshot)
	response.StructuredContent["find_match_count"] = len(matched)
	response.StructuredContent["find_page_start"] = offset + 1
	response.StructuredContent["find_page_count"] = len(shown)
	if nextCursor != "" {
		response.StructuredContent["next_cursor"] = nextCursor
	}
	return response
}

func (s *service) stabilizeFindRefs(
	source *appSnapshot,
	findSnapshot *AXSnapshotV1,
) {
	if s == nil || s.refRegistry == nil ||
		source == nil || findSnapshot == nil {
		return
	}
	var visit func([]*AXNode)
	visit = func(nodes []*AXNode) {
		for _, node := range nodes {
			if node.FindMatch && node.Ref != "" {
				entry := s.refRegistry.entries[node.Ref]
				if entry != nil && entry.LocatorKind == AXLocatorSemantic {
					_, err := lookupBySelector(
						source,
						strings.TrimSpace(entry.SemanticTarget),
						"",
					)
					if err != nil {
						node.Ref = ""
						if axNodeProviderIdentityComplete(node) {
							node.Ref = s.refRegistry.AcquireTyped(
								AXLocatorProviderBound,
								findSnapshot.AppIdentity,
								findSnapshot.WindowHandle,
								"",
								node.Role,
								node.Name,
								findSnapshot.Revision,
								node.ProviderBusName,
								node.ProviderObjectPath,
								node.ProcessGeneration,
								node.ProviderWindowBusName,
								node.ProviderWindowObjectPath,
								node.ProviderWindowProcessGeneration,
								node.ProviderWindowRole,
								node.ProviderWindowName,
								findSnapshot.AppRef,
								findSnapshot.WindowRef,
								node.ProviderIdentityScope,
							)
							s.refRegistry.BindRuntimePaths(
								node.Ref,
								node.RuntimeID,
								node.RuntimeIDAliases,
							)
						}
						if canonical := findSnapshot.ByID[node.ID]; canonical != nil {
							canonical.Ref = node.Ref
						}
					}
				}
			}
			visit(node.Children)
		}
	}
	visit(findSnapshot.Roots)
}

func axNodeByIndex(nodes []*AXNode, index int) *AXNode {
	for _, node := range nodes {
		if node.ProviderIndex == index {
			return node
		}
		if found := axNodeByIndex(node.Children, index); found != nil {
			return found
		}
	}
	return nil
}
