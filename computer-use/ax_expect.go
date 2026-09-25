package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// AX input synthesis (key presses, typing and the coordinate fallback for
// clicks) goes through AT-SPI generate_*_event, which is fire-and-forget: it
// returns once the event is queued, with no way to know whether the target
// received it, so actions can only report that delivery was not verified. A
// separate verification call costs an extra round trip that agents rarely
// pay; under uncertainty they re-read the whole tree or retry blindly.
// Folding verification into the action closes that loop.
//
// Three boundaries hold:
//
//  1. predicate_verified=false is not isError. The action was sent and the
//     expectation was not met, which is a fact about the UI rather than a
//     call error. isError stays reserved for argument and addressing errors
//     (unknown app, unresolvable target, malformed expect); reporting an
//     unmet expectation as an error would make the agent change the call
//     instead of the next action.
//  2. Without expect, responses are unchanged; the parameter is purely
//     additive.
//  3. Predicates use only toolkit-independent semantics (role, name, value,
//     state and focus). No app-specific concept is introduced and nothing
//     branches on the app name: an app can be evidence, never a condition.
//
// settle polls instead of sleeping and reports the actual elapsed time. Every
// poll is a full AT-SPI traversal, so the default budget (defaultSettleMS) is
// short, and the first evaluation reuses the snapshot the action just built,
// so the common case costs no extra read.

func recordHasState(record *elementRecord, state string) bool {
	if record == nil {
		return false
	}
	for _, reported := range strings.Fields(strings.Trim(record.States, "[] \t\r\n")) {
		if strings.EqualFold(reported, strings.TrimSpace(state)) {
			return true
		}
	}
	return false
}

func renderedStates(record *elementRecord) string {
	if record == nil || strings.TrimSpace(record.States) == "" {
		return "(no states reported)"
	}
	return strings.TrimSpace(record.States)
}

// splitTopLevel splits text on sep, ignoring separators inside quotes: the
// comma in `name="Save, As"` belongs to the name, not a field boundary.
func splitTopLevel(text string, sep byte) []string {
	parts := []string{}
	current := strings.Builder{}
	inQuotes := false
	for index := 0; index < len(text); index++ {
		char := text[index]
		if char == '\\' && inQuotes && index+1 < len(text) {
			current.WriteByte(char)
			index++
			current.WriteByte(text[index])
			continue
		}
		if char == '"' {
			inQuotes = !inQuotes
		}
		if char == sep && !inQuotes {
			parts = append(parts, current.String())
			current.Reset()
			continue
		}
		current.WriteByte(char)
	}
	parts = append(parts, current.String())
	return parts
}

// splitExpectField splits `role=dialog` into ("role", "dialog"). A part is a
// field only when the left side of `=` is a plain identifier; otherwise the
// whole part is a value, so a quoted name containing `=`, such as `"a=b"`, is
// not misread as a field.
func splitExpectField(part string) (string, string, bool) {
	for index := 0; index < len(part); index++ {
		if part[index] == '"' {
			return "", "", false
		}
		if part[index] != '=' {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(part[:index]))
		if key == "" || !isIdentifier(key) {
			return "", "", false
		}
		return key, strings.TrimSpace(part[index+1:]), true
	}
	return "", "", false
}

func isIdentifier(text string) bool {
	for _, char := range text {
		if char >= 'a' && char <= 'z' {
			continue
		}
		if char == '_' {
			continue
		}
		return false
	}
	return text != ""
}

func unquoteExpectValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		return unescapeQuoted(value[1 : len(value)-1])
	}
	return value
}

const expectTargetHelp = "A target is written either as comma-separated fields — " +
	"`role=dialog`, `name=Save`, `element_index=42`, `text=…`, `state=checked`, " +
	"combinable as `role=push button,name=Save` (ANDed, substring, case-insensitive) — " +
	"or as a selector copied verbatim from a snapshot line, e.g. `push button \"Save\"`; " +
	"quoted selector role/name are exact (case-insensitive), so `\"OK\"` never matches " +
	"`\"Bookmark\"`. " +
	"A BARE WORD with no `=` and no quotes (e.g. `menu`) is matched against the TEXT " +
	"fields only — name, description, value, placeholder — and NEVER against the role. " +
	"To match a role you must say so: write `role=menu`, not `menu`. (This used to be " +
	"loose enough that `menu` matched a `push button \"Back\"` whose description happened " +
	"to contain the word, which is how a bare word produced a confident wrong verdict.)"

var knownExpectRoles = map[string]bool{
	"alert": true, "check box": true, "combo box": true, "dialog": true,
	"document": true, "document web": true, "entry": true, "frame": true,
	"heading": true, "link": true, "list": true, "list item": true,
	"menu": true, "menu item": true, "page tab": true, "page tab list": true,
	"paragraph": true, "push button": true, "radio button": true,
	"scroll bar": true, "slider": true, "spin button": true, "table": true,
	"table cell": true, "toggle button": true, "tree": true, "tree item": true,
	"window": true,
}

func parseExpectTarget(raw string) (expectTarget, error) {
	target := expectTarget{raw: strings.TrimSpace(raw), index: -1}
	if target.raw == "" {
		return target, errors.New("the target is empty. " + expectTargetHelp)
	}
	for _, part := range splitTopLevel(target.raw, ',') {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, isField := splitExpectField(part)
		if !isField {
			// A bare index such as `42`.
			if number, err := strconv.Atoi(part); err == nil {
				target.index = number
				continue
			}
			// A selector such as `push button "Save"`. Predicate suffixes are
			// rejected here: the field form of expect already covers them,
			// and combining the two syntaxes would only make them conflict.
			if strings.Contains(part, "\"") {
				role, name, hasName, rest := parseSelector(part)
				if strings.TrimSpace(rest) != "" {
					return target, fmt.Errorf(
						"I cannot read %q: the trailing part %q is not supported inside an expect target. %s",
						part, strings.TrimSpace(rest), expectTargetHelp)
				}
				if role != "" {
					target.role = role
					target.exactRole = true
				}
				if hasName {
					target.name = name
					target.exactName = true
				}
				continue
			}
			if knownExpectRoles[strings.ToLower(part)] {
				return target, fmt.Errorf(
					"%q is an accessibility role, not a text target. Write role=%s. "+
						"To match literal text instead, write text=%s or quote the name. %s",
					part, part, part, expectTargetHelp)
			}
			target.loose = part
			continue
		}
		switch key {
		case "element_index", "index":
			number, err := strconv.Atoi(unquoteExpectValue(value))
			if err != nil {
				return target, fmt.Errorf(
					"element_index must be an integer taken from the tree, got %q. "+
						"To address the element some other way use role=/name=/text= instead.", value)
			}
			target.index = number
		case "role":
			target.role = unquoteExpectValue(value)
		case "name":
			target.name = unquoteExpectValue(value)
		case "text":
			target.text = unquoteExpectValue(value)
		case "state":
			target.state = unquoteExpectValue(value)
		default:
			return target, fmt.Errorf(
				"unknown field %q. The supported fields are element_index, role, name, text and state. %s",
				key, expectTargetHelp)
		}
	}
	if target.empty() {
		return target, fmt.Errorf("the target %q names nothing to match on. %s",
			target.raw, expectTargetHelp)
	}
	return target, nil
}

// focusedRecord finds the focused node in the rendered tree of this snapshot.
//
// It deliberately ignores snapshot.FocusedSummary, which comes from a separate
// whole-window scan by the provider that is not subject to node-budget pruning
// and may belong to another window. Predicates are evaluated against Elements,
// so evidence must come from Elements too; mixing the sources produces
// self-contradicting evidence such as "nothing in the tree matches, focus is
// on X".
func focusedRecord(snapshot *appSnapshot) *elementRecord {
	if snapshot == nil {
		return nil
	}
	for index := range snapshot.Elements {
		if containsFold(snapshot.Elements[index].States, "focused") {
			return &snapshot.Elements[index]
		}
	}
	return nil
}

// focusDescription states where focus is in this snapshot. It is the most
// useful fact when focus_moves_to fails: knowing where focus went tells the
// agent whether to press Tab or click again.
//
// When the focused node is absent from this tree, the wording must keep "the
// target does not match" apart from "focus was not observed here", and must
// say that FocusedSummary comes from a separate scan, not from the view the
// predicate was evaluated against.
func focusDescription(snapshot *appSnapshot) string {
	if snapshot == nil {
		return "(no snapshot)"
	}
	if record := focusedRecord(snapshot); record != nil {
		return renderElementAddress(record)
	}
	if summary := strings.TrimSpace(snapshot.FocusedSummary); summary != "" {
		return fmt.Sprintf(
			"no element in THIS snapshot's tree carries the focused state, so this observation "+
				"cannot say where focus is. A separate whole-window scan by the runtime reports %s, "+
				"but that node is not part of the tree this predicate was evaluated against — it was "+
				"omitted by the provider budget, or it belongs to another window. Call AX mode=observe and "+
				"look for [focused] in the tree before relying on focus here", summary)
	}
	return "nothing — no element in this snapshot's tree carries the focused state"
}

type expectPredicate struct {
	kind        string
	target      expectTarget
	contains    string
	equals      string
	hasContains bool
	hasEquals   bool
	state       string
}

func (p expectPredicate) describe() string {
	switch p.kind {
	case "appears":
		return fmt.Sprintf("an element matching %q to appear that was NOT there before the action", p.target.raw)
	case "disappears":
		return fmt.Sprintf("the element matching %q, which WAS there before the action, to be gone", p.target.raw)
	case "exists_after":
		return fmt.Sprintf("an element matching %q to exist after the action", p.target.raw)
	case "value_becomes":
		if p.hasEquals {
			return fmt.Sprintf("the value of %q to equal %q", p.target.raw, p.equals)
		}
		return fmt.Sprintf("the value of %q to contain %q", p.target.raw, p.contains)
	case "state_becomes":
		return fmt.Sprintf("the state of %q to include %q", p.target.raw, p.state)
	case "focus_moves_to":
		return fmt.Sprintf("focus to move to an element matching %q", p.target.raw)
	case "focus_within":
		return fmt.Sprintf("focus to be on %q or one of its descendants", p.target.raw)
	}
	return p.kind
}

// baselineLabel reports the evidence age without exposing the internal snapshot
// identity used for diffing and duplicate suppression.
func baselineLabel(before *appSnapshot) string {
	if before == nil {
		return "no pre-action snapshot"
	}
	parts := []string{}
	if !before.capturedAt.IsZero() {
		parts = append(parts, fmt.Sprintf("taken %dms before this action",
			int(time.Since(before.capturedAt)/time.Millisecond)))
	}
	if len(parts) == 0 {
		return "the pre-action snapshot"
	}
	return "the pre-action snapshot (" + strings.Join(parts, ", ") + ")"
}

// noBaselineEvidence explains a change assertion made without a pre-action
// snapshot. It never falls back to an existence check: appears and disappears
// assert a change, and without a baseline there is no change to show.
// Reporting "present now" as "just appeared" would be a false positive; an
// unverified result that explains how to obtain a baseline is more useful.
func noBaselineEvidence(kind, raw string, presentNow bool) string {
	state := "no element matches it now either"
	if presentNow {
		state = "an element matching it is present NOW"
	}
	return fmt.Sprintf(
		"%s is a CHANGE assertion and there was no pre-action snapshot of this app to compare "+
			"against, so novelty could not be established: %s, but this observation cannot tell "+
			"whether that was already true before the action. Call AX mode=observe once before the "+
			"action to give the server a baseline (%q was the target).",
		kind, state, raw)
}

// evaluate reports whether the predicate holds and what was actually observed.
//
// It takes two snapshots, not one. appears and disappears assert a change, and
// the post-action snapshot alone can only say whether something exists: an
// always-present window title would then count as newly appeared. Existence is
// not addition, so the baseline is part of the verdict.
//
// Evidence must also come from a single observation. Mixing the pruned
// Elements with the provider's separate whole-window focus scan produces
// self-contradicting text such as "nothing in the tree matches, focus is on
// entry Address and search bar".
func (p expectPredicate) evaluate(before, after *appSnapshot) (bool, string) {
	switch p.kind {
	case "appears":
		afterCount := p.target.countMatches(after)
		if before == nil {
			return false, noBaselineEvidence("appears", p.target.raw, afterCount > 0)
		}
		beforeCount := p.target.countMatches(before)
		if p.target.role == "" {
			beforeRoles := p.target.roleCounts(before)
			afterRoles := p.target.roleCounts(after)
			for _, role := range sortedRoleUnion(beforeRoles, afterRoles) {
				if afterRoles[role] <= beforeRoles[role] {
					continue
				}
				roleTarget := p.target
				roleTarget.role = role
				record := roleTarget.find(after)
				return true, fmt.Sprintf(
					"the number of %s elements matching %q increased from %d in %s to %d "+
						"in the post-action tree. A current match is %s. Counts are separated "+
						"by role so an existing menu item cannot hide a newly appeared dialog",
					role, p.target.raw, beforeRoles[role], baselineLabel(before),
					afterRoles[role], renderElementAddress(record))
			}
		}
		if afterCount <= beforeCount {
			record := p.target.find(after)
			if record == nil {
				return false, fmt.Sprintf(
					"the number of elements matching %q did not increase: %d before, %d after; "+
						"no matching element is present in the post-action tree",
					p.target.raw, beforeCount, afterCount)
			}
			return false, fmt.Sprintf(
				"the number of elements matching %q did not increase: %d before, %d after. "+
					"A current match is %s, but its presence does not prove a new matching element appeared",
				p.target.raw, beforeCount, afterCount, renderElementAddress(record))
		}
		record := p.target.find(after)
		// The evidence is the increased match count, not the element index.
		// The provider's StableIndexer reuses indices while path, role and
		// name stay stable, so an index neither always changes nor proves
		// that an element was added.
		return true, fmt.Sprintf(
			"the number of elements matching %q increased from %d in %s to %d in the post-action "+
				"tree. A current match is %s; element_index %d is only its current address, while "+
				"the count increase is the evidence",
			p.target.raw, beforeCount, baselineLabel(before), afterCount,
			describeRecord(record), record.Index)

	case "disappears":
		afterCount := p.target.countMatches(after)
		if before == nil {
			return false, noBaselineEvidence("disappears", p.target.raw, afterCount > 0)
		}
		beforeCount := p.target.countMatches(before)
		if beforeCount == 0 {
			// The mirror of appears: absent before the action, so "absent now"
			// proves nothing.
			return false, fmt.Sprintf(
				"nothing matched %q in %s either, so there was nothing to disappear and this action "+
					"proved nothing. A disappears that was never present is a vacuous truth, and "+
					"reporting it as predicate_verified would be a false confirmation",
				p.target.raw, baselineLabel(before))
		}
		if p.target.role == "" {
			beforeRoles := p.target.roleCounts(before)
			afterRoles := p.target.roleCounts(after)
			for _, role := range sortedRoleUnion(beforeRoles, afterRoles) {
				if afterRoles[role] >= beforeRoles[role] {
					continue
				}
				roleTarget := p.target
				roleTarget.role = role
				existing := roleTarget.find(before)
				return true, fmt.Sprintf(
					"the number of %s elements matching %q decreased from %d in %s to %d "+
						"in the post-action tree. One pre-action match was %s. Counts are "+
						"separated by role so a remaining menu item cannot hide a closed dialog",
					role, p.target.raw, beforeRoles[role], baselineLabel(before),
					afterRoles[role], renderElementAddress(existing))
			}
		}
		existing := p.target.find(before)
		if afterCount < beforeCount {
			return true, fmt.Sprintf(
				"the number of elements matching %q decreased from %d in %s to %d in the "+
					"post-action tree. One pre-action match was %s",
				p.target.raw, beforeCount, baselineLabel(before), afterCount,
				renderElementAddress(existing))
		}
		record := p.target.find(after)
		return false, fmt.Sprintf(
			"the number of elements matching %q did not decrease: %d before, %d after. "+
				"A matching element is STILL present as %s",
			p.target.raw, beforeCount, afterCount, renderElementAddress(record))

	case "exists_after":
		record := p.target.find(after)
		if record == nil {
			return false, fmt.Sprintf("no element matching %q exists in the post-action tree", p.target.raw)
		}
		return true, fmt.Sprintf("%s exists in the post-action tree", renderElementAddress(record))

	case "value_becomes":
		matches := p.target.matching(after)
		if len(matches) == 0 {
			return false, fmt.Sprintf(
				"the target %q is not in the post-action tree at all, so its value could not be read "+
					"(it may have been pruned by the node budget rather than removed; element_index "+
					"is a snapshot address, so an index taken from an older tree can address a "+
					"different element when its path, role, or name changed)",
				p.target.raw)
		}
		if len(matches) > 1 {
			return false, fmt.Sprintf(
				"%d elements match %q in the post-action tree, so value_becomes is ambiguous. "+
					"Use element_index or add role/name fields until exactly one element matches",
				len(matches), p.target.raw)
		}
		record := matches[0]
		if p.hasEquals {
			if strings.TrimSpace(record.Value) == strings.TrimSpace(p.equals) {
				return true, fmt.Sprintf("%s has value %q", renderElementAddress(record), record.Value)
			}
			return false, fmt.Sprintf("%s has value %q, not %q",
				renderElementAddress(record), record.Value, p.equals)
		}
		if containsFold(record.Value, p.contains) {
			return true, fmt.Sprintf("%s now has value %q, which contains %q",
				renderElementAddress(record), record.Value, p.contains)
		}
		return false, fmt.Sprintf("%s has value %q, which does not contain %q",
			renderElementAddress(record), record.Value, p.contains)

	case "state_becomes", "state_is_after":
		matches := p.target.matching(after)
		if len(matches) == 0 {
			return false, fmt.Sprintf(
				"the target %q is not in the post-action tree, so its state could not be read",
				p.target.raw)
		}
		if len(matches) > 1 {
			return false, fmt.Sprintf(
				"%d elements match %q in the post-action tree, so %s is ambiguous. "+
					"Use element_index or add role/name fields until exactly one element matches",
				len(matches), p.target.raw, p.kind)
		}
		record := matches[0]
		want := true
		state := p.state
		if bare, negated := strings.CutPrefix(state, "!"); negated {
			want, state = false, bare
		}
		has := recordHasState(record, state)
		satisfied := has == want
		if p.kind == "state_is_after" {
			if satisfied {
				return true, fmt.Sprintf("%s reports states %s",
					renderElementAddress(record), renderedStates(record))
			}
			if want {
				return false, fmt.Sprintf("%s reports states %s, without %q",
					renderElementAddress(record), renderedStates(record), state)
			}
			return false, fmt.Sprintf("%s still reports state %q in %s",
				renderElementAddress(record), state, renderedStates(record))
		}
		if before == nil {
			return false, noBaselineEvidence("state_becomes", p.target.raw, satisfied)
		}
		beforeMatches := p.target.matching(before)
		if len(beforeMatches) != 1 {
			return false, fmt.Sprintf(
				"state_becomes needs exactly one matching element in the pre-action snapshot; "+
					"observed %d for %q", len(beforeMatches), p.target.raw)
		}
		beforeSatisfied := recordHasState(beforeMatches[0], state) == want
		if beforeSatisfied {
			return false, fmt.Sprintf(
				"%s already satisfied state %q in the pre-action snapshot, so the action "+
					"did not establish a new state transition",
				renderElementAddress(beforeMatches[0]), p.state)
		}
		if satisfied {
			return true, fmt.Sprintf("%s reports states %s",
				renderElementAddress(record), renderedStates(record))
		}
		if want {
			return false, fmt.Sprintf("%s reports states %s, without %q",
				renderElementAddress(record), renderedStates(record), state)
		}
		return false, fmt.Sprintf("%s still reports state %q in %s",
			renderElementAddress(record), state, renderedStates(record))

	case "focus_moves_to":
		if record := p.target.findFocused(after); record != nil {
			return true, fmt.Sprintf("focus is on %s", renderElementAddress(record))
		}
		// Both the verdict and the evidence use after.Elements. Classify the
		// failure first: "the focused node was not observed" and "the focused
		// node is not the target" are different facts, and mixing them yields
		// self-contradicting evidence.
		focused := focusedRecord(after)
		if focused == nil {
			return false, fmt.Sprintf(
				"this snapshot cannot decide whether focus moved to %q: %s. That is NOT the same as "+
					"'the target does not match' — it is 'the focused node was not observed here'",
				p.target.raw, focusDescription(after))
		}
		matches := p.target.countMatches(after)
		if matches == 0 {
			return false, fmt.Sprintf(
				"focus is on %s, and nothing in this tree matches %q at all — "+
					"so focus did not move to it, and the target may also be misspelled. Note that a "+
					"bare word matches name/description/value/placeholder only; to match a role write "+
					"role=…", renderElementAddress(focused), p.target.raw)
		}
		return false, fmt.Sprintf(
			"%d element(s) match %q but none of them carries the focused state; focus is on %s",
			matches, p.target.raw, renderElementAddress(focused))

	case "focus_within":
		focused := focusedRecord(after)
		if focused == nil {
			return false, fmt.Sprintf(
				"this snapshot cannot decide whether focus is within %q: %s",
				p.target.raw, focusDescription(after))
		}
		matches := p.target.matching(after)
		if len(matches) == 0 {
			return false, fmt.Sprintf(
				"focus is on %s, and no element matches the focus_within target %q",
				renderElementAddress(focused), p.target.raw)
		}
		for _, target := range matches {
			if target.Index == focused.Index || runtimePathContains(target.RuntimeID, focused.RuntimeID) {
				return true, fmt.Sprintf("focus is on %s within %s",
					renderElementAddress(focused), renderElementAddress(target))
			}
		}
		return false, fmt.Sprintf(
			"focus is on %s, outside the %d element(s) matching %q and their observed descendants",
			renderElementAddress(focused), len(matches), p.target.raw)
	}
	return false, fmt.Sprintf("unsupported predicate %q", p.kind)
}

type expectSpec struct {
	preds []expectPredicate
}

func (spec *expectSpec) describe() string {
	parts := make([]string, 0, len(spec.preds))
	for _, pred := range spec.preds {
		parts = append(parts, pred.describe())
	}
	return strings.Join(parts, " and ")
}

func (spec *expectSpec) evaluate(before, after *appSnapshot) (bool, string) {
	ok := true
	parts := make([]string, 0, len(spec.preds))
	for _, pred := range spec.preds {
		hit, evidence := pred.evaluate(before, after)
		if !hit {
			ok = false
		}
		parts = append(parts, evidence)
	}
	return ok, strings.Join(parts, "; ")
}

var expectPredicateNames = []string{
	"appears", "disappears", "exists_after", "value_becomes",
	"state_becomes", "state_is_after", "focus_moves_to", "focus_within",
}

// parseExpect parses the expect argument. Malformed input is a hard failure
// (isError) because it is a call error, unlike an unmet expectation: the
// former requires fixing the call, the latter changing the next action.
// Silently ignoring a misspelled predicate would give the agent a check that
// always passes, which is worse than none.
func parseExpect(args map[string]any) (*expectSpec, error) {
	raw, present := args["expect"]
	if !present || raw == nil {
		return nil, nil
	}
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("expect must be an object, e.g. " +
			"{\"appears\": \"role=dialog\"} or {\"value_becomes\": {\"element_index\": 17, \"contains\": \"Saved\"}}. " +
			"Omit it entirely to keep the unverified behaviour.")
	}
	for key := range object {
		known := false
		for _, name := range expectPredicateNames {
			if key == name {
				known = true
				break
			}
		}
		if !known {
			return nil, fmt.Errorf(
				"expect has no predicate named %q. Supported predicates are %s.",
				key, strings.Join(expectPredicateNames, ", "))
		}
	}
	spec := &expectSpec{}
	// Iterate in a fixed order so that evidence strings are stable.
	for _, kind := range []string{"appears", "disappears", "exists_after", "focus_moves_to", "focus_within"} {
		value, has := object[kind]
		if !has || value == nil {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("expect.%s must be a string target such as \"role=dialog\". %s",
				kind, expectTargetHelp)
		}
		target, err := parseExpectTarget(text)
		if err != nil {
			return nil, fmt.Errorf("expect.%s: %s", kind, err)
		}
		spec.preds = append(spec.preds, expectPredicate{kind: kind, target: target})
	}
	if value, has := object["value_becomes"]; has && value != nil {
		fields, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("expect.value_becomes must be an object, e.g. " +
				"{\"element_index\": 17, \"contains\": \"Saved\"}.")
		}
		for key := range fields {
			if key != "element_index" && key != "contains" && key != "equals" {
				return nil, fmt.Errorf(
					"expect.value_becomes has no field %q. It takes element_index (required) "+
						"plus contains or equals.", key)
			}
		}
		targetText := elementIndexString(fields["element_index"])
		if strings.TrimSpace(targetText) == "" {
			return nil, errors.New("expect.value_becomes requires element_index — an index " +
				"from the tree, a selector, or a target such as \"role=entry\".")
		}
		target, err := parseExpectTarget(targetText)
		if err != nil {
			return nil, fmt.Errorf("expect.value_becomes.element_index: %s", err)
		}
		pred := expectPredicate{kind: "value_becomes", target: target}
		if contains, has := fields["contains"]; has && contains != nil {
			text, ok := contains.(string)
			if !ok {
				return nil, errors.New("expect.value_becomes.contains must be a string")
			}
			if text == "" {
				return nil, errors.New("expect.value_becomes.contains must not be empty; use equals:\"\" to assert an empty value")
			}
			pred.contains = text
			pred.hasContains = true
		}
		if equals, has := fields["equals"]; has && equals != nil {
			text, ok := equals.(string)
			if !ok {
				return nil, errors.New("expect.value_becomes.equals must be a string")
			}
			pred.equals = text
			pred.hasEquals = true
		}
		if !pred.hasContains && !pred.hasEquals {
			return nil, errors.New("expect.value_becomes needs something to compare against: " +
				"pass contains (substring, case-insensitive) or equals (exact, whitespace-trimmed).")
		}
		if pred.hasContains && pred.hasEquals {
			return nil, errors.New("expect.value_becomes takes contains OR equals, not both.")
		}
		spec.preds = append(spec.preds, pred)
	}
	for _, kind := range []string{"state_becomes", "state_is_after"} {
		value, has := object[kind]
		if !has || value == nil {
			continue
		}
		fields, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf(
				"expect.%s must be an object, e.g. {\"element_index\": 17, \"state\": \"checked\"}.",
				kind)
		}
		for key := range fields {
			if key != "element_index" && key != "state" {
				return nil, fmt.Errorf(
					"expect.%s has no field %q. It takes element_index and state.", kind, key)
			}
		}
		targetText := elementIndexString(fields["element_index"])
		if strings.TrimSpace(targetText) == "" {
			return nil, fmt.Errorf("expect.%s requires element_index — an index, selector, or target.", kind)
		}
		target, err := parseExpectTarget(targetText)
		if err != nil {
			return nil, fmt.Errorf("expect.%s.element_index: %s", kind, err)
		}
		state, ok := fields["state"].(string)
		if !ok || strings.TrimSpace(strings.TrimPrefix(state, "!")) == "" {
			return nil, fmt.Errorf("expect.%s.state must be a non-empty state such as checked, focused, selected, or !checked", kind)
		}
		spec.preds = append(spec.preds, expectPredicate{
			kind: kind, target: target, state: strings.TrimSpace(state),
		})
	}
	if len(spec.preds) == 0 {
		return nil, errors.New("expect was passed but names no predicate. Use one of " +
			strings.Join(expectPredicateNames, ", ") +
			" — or omit expect entirely to keep the unverified behaviour.")
	}
	return spec, nil
}
