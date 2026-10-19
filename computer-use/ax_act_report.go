package main

import (
	"sort"
	"strconv"
	"strings"
)

// The act report is the first text of every act result after the evidence
// line. It states what the tool already knows from its own reads, so the
// model does not have to ask:
//
//	effect: r58 checkbox "Interface" checked=false→true
//	windows: opened w5 "Insert Sheet" (dialog, focused); closed w3 "Print"; focus w2
//
// One effect line per action that named an element ref, compared between
// the read before that action and the next read after it. The windows line
// compares the application's top-level windows before the first action with
// those after the last one. Both come from reads the batch makes anyway; the
// report adds no provider call.

const (
	axReportEffectPrefix  = "effect: "
	axReportWindowsPrefix = "windows: "
)

// actReportTrace records the cached snapshots an act batch passes through.
type actReportTrace struct {
	appToken  string
	windowRef string
	observe   string
	// start is the snapshot before any input; before[i] the snapshot just
	// before action i ran.
	start  *appSnapshot
	before map[int]*appSnapshot
}

func newActReportTrace(obs observation) *actReportTrace {
	return &actReportTrace{before: map[int]*appSnapshot{}, observe: obs.resolvedMode()}
}

func (t *actReportTrace) bind(s *service, context *a11yBatchContext) {
	if t == nil || context == nil {
		return
	}
	t.appToken = context.appToken
	t.windowRef = context.windowRef
	t.start = s.currentSnapshot(context.appToken)
}

func (t *actReportTrace) recordBefore(s *service, index int) {
	if t == nil || t.appToken == "" {
		return
	}
	t.before[index] = s.currentSnapshot(t.appToken)
}

// after returns the first snapshot read after action index, or nil when no
// read happened after it.
func (t *actReportTrace) after(s *service, index int) *appSnapshot {
	next := -1
	for candidate := range t.before {
		if candidate > index && (next < 0 || candidate < next) {
			next = candidate
		}
	}
	after := s.currentSnapshot(t.appToken)
	if next >= 0 {
		after = t.before[next]
	}
	if after == nil || after == t.before[index] {
		return nil
	}
	return after
}

// withActReport puts the report first in an act result. A call rejected
// before any input gets no report: nothing happened.
func (s *service) withActReport(
	result toolCallResult,
	actions []a11yBatchAction,
	trace *actReportTrace,
) toolCallResult {
	if trace == nil || trace.appToken == "" || result.StructuredContent == nil ||
		!axEvidenceReportsInputEmission(result.StructuredContent) {
		return result
	}
	completed := len(actions)
	if batch, ok := result.StructuredContent["a11y_batch_result"].(map[string]any); ok {
		if value, ok := intEvidence(batch["actions_completed_without_call_error"]); ok {
			completed = value
		}
	}
	lines := []string{}
	// The effect counts as observed only when every action named an element
	// and each of those elements visibly changed; a keypress or a no-change
	// action keeps the call's uncertainty.
	verified := completed == len(actions)
	for index, action := range actions {
		if !isShortAXRef(action.targetRef) {
			verified = false
			continue
		}
		if _, ran := trace.before[index]; !ran {
			continue
		}
		if index > completed {
			break
		}
		line, changed := s.actEffectLine(trace, index, action.targetRef)
		lines = append(lines, line)
		verified = verified && changed
	}
	lines = append(lines, s.actWindowsLine(trace, result))
	if verified && len(lines) > 1 {
		result.StructuredContent["target_effect_observed"] = true
	}
	result.Content = append(
		[]contentItem{{Type: "text", Text: strings.Join(lines, "\n")}},
		result.Content...,
	)
	return result
}

// actEffectLine compares one element between the reads around its action.
// changed reports a visible change of the element.
func (s *service) actEffectLine(trace *actReportTrace, index int, ref string) (string, bool) {
	before := axSnapshotNodeByRef(trace.before[index], ref)
	afterSnapshot := trace.after(s, index)
	head := axReportEffectPrefix + ref
	if before != nil {
		head += " " + axReportNodeLabel(before)
	}
	if afterSnapshot == nil {
		reason := "no read after the action"
		if trace.observe == observeNone {
			reason = `observe "none" skipped the read after the action`
		}
		return head + " unknown (" + reason + ")", false
	}
	after := axSnapshotNodeByRef(afterSnapshot, ref)
	if after == nil {
		beforeHandle := ""
		if trace.before[index] != nil {
			beforeHandle = trace.before[index].WindowHandle
		}
		switch {
		case afterSnapshot.WindowHandle != beforeHandle && beforeHandle != "":
			return head + " unknown (the read after the action is of " +
				axReportSnapshotWindow(afterSnapshot) + ")", false
		case before != nil && afterSnapshot.axSnapshot != nil && afterSnapshot.axSnapshot.RefInventoryComplete:
			return head + " gone", true
		default:
			return head + " unknown (the read after the action did not include it)", false
		}
	}
	if before == nil {
		return axReportEffectPrefix + ref + " " + axReportNodeLabel(after) + " " +
			strings.Join(axReportNodeFields(after), " ") +
			" (not read before the action)", false
	}
	changes := axReportNodeChanges(before, after)
	if len(changes) == 0 {
		return head + " no visible change", false
	}
	return head + " " + strings.Join(changes, " "), true
}

func axSnapshotNodeByRef(snapshot *appSnapshot, ref string) *AXNode {
	if snapshot == nil || snapshot.axSnapshot == nil || ref == "" {
		return nil
	}
	var found *AXNode
	var visit func([]*AXNode)
	visit = func(nodes []*AXNode) {
		for _, node := range nodes {
			if node == nil || found != nil {
				continue
			}
			if node.Ref == ref {
				found = node
				return
			}
			visit(node.Children)
		}
	}
	visit(snapshot.axSnapshot.CanonicalRoots)
	if found == nil {
		visit(snapshot.axSnapshot.Roots)
	}
	return found
}

func axReportNodeLabel(node *AXNode) string {
	label := string(node.Role)
	if label == "" {
		label = string(RoleGeneric)
	}
	if node.Name != "" {
		label += " " + strconv.Quote(axReportClip(node.Name))
	}
	return label
}

// axReportNodeFields returns the element's state and value fields as
// name→value pairs. AT-SPI reports these states only when set, so an
// unreported boolean state reads as false.
func axReportNodeFieldMap(node *AXNode) map[string]string {
	fields := map[string]string{}
	states := node.States
	boolean := func(name string, value string) {
		switch value {
		case "true", "mixed":
			fields[name] = value
		default:
			fields[name] = "false"
		}
	}
	boolean("checked", string(states.Checked))
	boolean("expanded", string(states.Expanded))
	boolean("selected", string(states.Selected))
	boolean("pressed", string(states.Pressed))
	boolean("active", string(states.Active))
	boolean("disabled", string(states.Disabled))
	boolean("invalid", string(states.Invalid))
	if states.Level != nil {
		fields["level"] = strconv.Itoa(*states.Level)
	}
	fields["value"] = strconv.Quote(axReportClip(node.Properties.Value))
	fields["name"] = strconv.Quote(axReportClip(node.Name))
	return fields
}

func axReportNodeFields(node *AXNode) []string {
	fields := axReportNodeFieldMap(node)
	out := []string{}
	for _, key := range sortedStringKeys(fields) {
		value := fields[key]
		if key == "name" || value == "false" || value == `""` {
			continue
		}
		out = append(out, key+"="+value)
	}
	return out
}

func axReportNodeChanges(before, after *AXNode) []string {
	old, now := axReportNodeFieldMap(before), axReportNodeFieldMap(after)
	keys := map[string]string{}
	for key := range old {
		keys[key] = ""
	}
	for key := range now {
		keys[key] = ""
	}
	changes := []string{}
	for _, key := range sortedStringKeys(keys) {
		from, to := old[key], now[key]
		if from == to {
			continue
		}
		if from == "" {
			from = "none"
		}
		if to == "" {
			to = "none"
		}
		changes = append(changes, key+"="+from+"→"+to)
	}
	return changes
}

func sortedStringKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func axReportClip(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	return truncateRunes(text, 60)
}

// axReportWindow is one top-level window as the report names it.
type axReportWindow struct {
	key, ref, title, role string
	active, modal         bool
}

func (w axReportWindow) name() string {
	if w.ref != "" {
		return w.ref + " " + strconv.Quote(axReportClip(w.title))
	}
	return strconv.Quote(axReportClip(w.title)) + " (no ref)"
}

func axReportSnapshotWindow(snapshot *appSnapshot) string {
	title := axReportClip(snapshot.WindowTitle)
	if snapshot.publicWindowRef != "" {
		return snapshot.publicWindowRef + " " + strconv.Quote(title)
	}
	return strconv.Quote(title)
}

// axReportWindows lists the application's visible top-level windows in a
// snapshot, keyed by native handle.
func axReportWindows(snapshot *appSnapshot) []axReportWindow {
	if snapshot == nil {
		return nil
	}
	windows := []axReportWindow{}
	seen := map[string]bool{}
	for _, window := range snapshot.Windows {
		if !(window.Showing || window.Modal || window.Active || window.Selected) ||
			(window.Handle != "" && window.Handle == snapshot.ClosedWindowHandle) {
			continue
		}
		key := firstNonEmpty(window.Handle, window.publicRef, window.Title)
		if seen[key] {
			continue
		}
		seen[key] = true
		ref := window.publicRef
		if window.Selected && ref == "" {
			ref = snapshot.publicWindowRef
		}
		windows = append(windows, axReportWindow{
			key: key, ref: ref, title: window.Title,
			role: strings.TrimSpace(window.Role), active: window.Active, modal: window.Modal,
		})
	}
	if len(windows) == 0 && !snapshot.WindowAbsent && snapshot.WindowHandle != "" {
		// Providers without a window inventory still name the read window.
		windows = append(windows, axReportWindow{
			key: snapshot.WindowHandle, ref: snapshot.publicWindowRef, title: snapshot.WindowTitle,
		})
	}
	return windows
}

// actWindowsLine compares the application's windows before the first action
// with those after the last one.
func (s *service) actWindowsLine(trace *actReportTrace, result toolCallResult) string {
	closedBound := result.StructuredContent["target_window_absent"] == true ||
		result.StructuredContent["close_effect_verified"] == true
	final := s.currentSnapshot(trace.appToken)
	before := axReportWindows(trace.start)
	if final == nil || final == trace.start {
		if closedBound && trace.windowRef != "" {
			title := ""
			if trace.start != nil {
				title = trace.start.WindowTitle
			}
			return axReportWindowsPrefix + "closed " + trace.windowRef + " " + strconv.Quote(axReportClip(title))
		}
		reason := "no read after the actions"
		if trace.observe == observeNone {
			reason = `observe "none" skipped the read`
		}
		return axReportWindowsPrefix + "unknown (" + reason + ")"
	}
	if trace.start == nil {
		return axReportWindowsPrefix + "unknown (no read of this application before the actions)"
	}
	after := axReportWindows(final)
	beforeByKey := map[string]axReportWindow{}
	for _, window := range before {
		beforeByKey[window.key] = window
	}
	afterByKey := map[string]axReportWindow{}
	for _, window := range after {
		afterByKey[window.key] = window
	}
	parts := []string{}
	for _, window := range after {
		previous, existed := beforeByKey[window.key]
		if !existed {
			detail := []string{firstNonEmpty(window.role, "window")}
			if window.active || window.modal {
				detail = append(detail, "focused")
			}
			parts = append(parts, "opened "+window.name()+" ("+strings.Join(detail, ", ")+")")
			continue
		}
		if comparableTreeText(previous.title) != comparableTreeText(window.title) {
			parts = append(parts, "renamed "+window.name())
		}
	}
	for _, window := range before {
		if _, still := afterByKey[window.key]; !still {
			parts = append(parts, "closed "+window.name())
		}
	}
	focusBefore, focusAfter := axReportFocused(before), axReportFocused(after)
	if focusAfter != nil && (focusBefore == nil || focusBefore.key != focusAfter.key) {
		if _, opened := beforeByKey[focusAfter.key]; opened {
			parts = append(parts, "focus "+firstNonEmpty(focusAfter.ref, focusAfter.name()))
		}
	} else if focusAfter == nil && focusBefore != nil && len(after) > 0 {
		parts = append(parts, "focus left these windows")
	}
	if final.externalDesktopTransition {
		parts = append(parts, "a window of another application took focus")
	}
	if len(parts) == 0 {
		return axReportWindowsPrefix + "no change"
	}
	return axReportWindowsPrefix + strings.Join(parts, "; ")
}

func axReportFocused(windows []axReportWindow) *axReportWindow {
	for index := range windows {
		if windows[index].active {
			return &windows[index]
		}
	}
	return nil
}

// isActReportText reports whether a text item is the act report, which is
// never the error message of a failed call.
func isActReportText(text string) bool {
	return strings.HasPrefix(text, axReportEffectPrefix) ||
		strings.HasPrefix(text, axReportWindowsPrefix)
}
