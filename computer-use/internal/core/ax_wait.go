package core

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hqhq1025/hybrid-computer-use-oss/computer-use/internal/contract"
)

// A wait step is one ordered step of an act batch. It sends no input and
// stops the batch, like a failed action, when its condition does not hold
// within timeout_ms. Forms:
//
//	{"type":"wait","window":"Export"}               a top-level window whose title contains the text appears
//	{"type":"wait","window":"Print","gone":true}    no such window remains (an exited app has none)
//	{"type":"wait","query":{...find query...}}      an element appears in the batch app's windows
//	{"type":"wait","ref":"r31","value":"14"}        an element condition: value, state, or text
//	{"type":"wait","text":"Saved"}                  text appears in the batch window
//
// gone inverts any form. The evaluation reuses the polling the earlier until
// predicate used: window titles, application-wide element search with find's
// matching, and live ref resolution.

const axWaitPollInterval = 400 * time.Millisecond

// axWait is one decoded wait step.
type axWait struct {
	window    string
	query     map[string]any
	ref       string
	value     string
	state     string
	text      string
	gone      bool
	timeoutMS int
}

func (w *axWait) describe() string {
	var subject string
	switch {
	case w.window != "":
		subject = "window " + strconv.Quote(w.window)
	case w.query != nil:
		subject = "element " + axWaitQueryText(w.query)
	case w.ref != "":
		subject = w.ref
		switch {
		case w.value != "":
			subject += " value " + strconv.Quote(w.value)
		case w.state != "":
			subject += " " + w.state
		case w.text != "":
			subject += " text " + strconv.Quote(w.text)
		}
	default:
		subject = "text " + strconv.Quote(w.text)
	}
	if w.gone {
		return subject + " gone"
	}
	return subject
}

func axWaitQueryText(query map[string]any) string {
	name := OptionalString(query, "name_contains")
	_, nameExact := query["name_equals"]
	if nameExact {
		name = OptionalString(query, "name_equals")
	}
	text := OptionalString(query, "text_contains")
	_, textExact := query["text_equals"]
	if textExact {
		text = OptionalString(query, "text_equals")
	}
	return describeQuery(OptionalString(query, "role"), name, text,
		OptionalString(query, "state"), nameExact, textExact)
}

// decodeAXWait checks one wait step. It needs exactly one of window, query,
// ref, or a bare text; value, state, and text qualify a ref.
func decodeAXWait(object map[string]any, path string) (*axWait, *axArgError) {
	wait := &axWait{timeoutMS: contract.AXDefaultWaitTimeout}
	example := `{"type":"wait","window":"Export"}`
	var err *axArgError
	if wait.window, err = axOptionalString(object, "window", path, true); err != nil {
		return nil, err
	}
	if wait.ref, err = axOptionalPattern(object, "ref", path, AXElementRefPattern, "rN"); err != nil {
		err.rule = axRuleRefKind
		if AXWindowRefPattern.MatchString(OptionalString(object, "ref")) {
			err.Message = `a wait ref is an element rN; for a window use {"type":"wait","window":"title"}`
			err.Hint = example
		}
		return nil, err
	}
	if wait.state, err = axOptionalString(object, "state", path, true); err != nil {
		return nil, err
	}
	// text is shared with type, whose text may be only spaces; a wait reads
	// it as a literal substring in the same way.
	if wait.text, err = axOptionalString(object, "text", path, false); err != nil {
		return nil, err
	}
	if _, present := object["text"]; present && wait.text == "" {
		return nil, axInvalid(path+".text", "text must not be empty", `{"type":"wait","text":"Saved"}`)
	}
	if raw, present := object["value"]; present {
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) == "" {
			return nil, axInvalid(path+".value", "value needs a non-blank string, a number, or a boolean",
				`{"type":"wait","ref":"r1","value":"14"}`).withRule(axRuleRequiredForType)
		}
		wait.value = value
	}
	if raw, present := object["query"]; present {
		if wait.query, err = decodeAXQuery(raw, path+".query"); err != nil {
			return nil, err
		}
	}
	if raw, present := object["gone"]; present {
		gone, ok := raw.(bool)
		if !ok {
			return nil, axInvalid(path+".gone", "gone must be true or false", "")
		}
		wait.gone = gone
	}
	if raw, present := object["timeout_ms"]; present {
		timeout, ok := axInteger(raw)
		if !ok || timeout < 1 || timeout > contract.MaxVerifyTimeout {
			return nil, axInvalid(path+".timeout_ms",
				fmt.Sprintf("timeout_ms must be an integer from 1 to %d", contract.MaxVerifyTimeout), "").
				withFix(axNumberEdit(path+".timeout_ms", raw, 1, contract.MaxVerifyTimeout, true))
		}
		wait.timeoutMS = timeout
	}
	subjects := 0
	for _, present := range []bool{wait.window != "", wait.query != nil, wait.ref != ""} {
		if present {
			subjects++
		}
	}
	if subjects == 0 && wait.text != "" {
		subjects = 1
	}
	if subjects != 1 {
		return nil, axInvalid(path, "wait needs exactly one of window, query, ref, or text", example).withRule(axRuleOneOf)
	}
	qualifiers := 0
	for _, value := range []string{wait.value, wait.state} {
		if value != "" {
			qualifiers++
		}
	}
	if wait.ref != "" && wait.text != "" {
		qualifiers++
	}
	if qualifiers > 0 && wait.ref == "" {
		field := "value"
		if wait.state != "" {
			field = "state"
		}
		return nil, axInvalid(path+"."+field, field+" qualifies an element; add ref, or use query to wait for an element",
			`{"type":"wait","ref":"r1","`+field+`":"..."}`).withRule(axRuleRequiredForType)
	}
	if qualifiers > 1 {
		return nil, axInvalid(path, "a ref wait checks one of value, state, or text",
			`{"type":"wait","ref":"`+wait.ref+`","value":"14"}`).withRule(axRuleOneOf)
	}
	return wait, nil
}

// axRejectRemovedUntil rejects a call that uses the removed until field (or
// an older spelling of it) and answers with the equivalent call written
// with wait steps.
func axRejectRemovedUntil(args map[string]any) *axArgError {
	path := ""
	for _, field := range axRemovedWaitFields {
		if _, present := args[field]; present {
			path = field
			break
		}
	}
	if actions, ok := args["actions"].([]any); ok && path == "" {
		for index, item := range actions {
			action, _ := item.(map[string]any)
			for _, field := range axRemovedWaitFields {
				if _, present := action[field]; present && path == "" {
					path = fmt.Sprintf("actions[%d].%s", index, field)
				}
			}
		}
	}
	if path == "" {
		return nil
	}
	field := path[strings.LastIndex(path, ".")+1:]
	hint, ok := axUntilCallAsWait(args)
	if ok {
		hint, ok = axRepairWaitHint(hint)
	}
	message := strconv.Quote(field) + " was removed; wait with a step of type \"wait\" in act actions, at the point in the order where the batch should wait"
	if !ok {
		hint = `{"mode":"act","actions":[{"type":"click","ref":"r1"},{"type":"wait","window":"title"}]}`
	}
	return axInvalid(path, message, hint)
}

// axRepairWaitHint returns hint unchanged when it decodes. A converted call
// keeps the values of the rejected predicate, so an out-of-range timeout or
// a blank name stays wrong after the conversion; the decoder's repairs fix
// such values as they do for any other call, so the hint always decodes.
func axRepairWaitHint(hint string) (string, bool) {
	decoded, ok := axDecodeHintJSON(hint)
	call, isObject := decoded.(map[string]any)
	if !ok || !isObject {
		return "", false
	}
	_, err := decodeAXCall(call)
	if err == nil {
		return hint, true
	}
	for step := 0; step < axMaxRepairSteps; step++ {
		if !axApplyRepair(&call, err) {
			return "", false
		}
		if _, err = decodeAXCall(call); err == nil {
			return axOrderedCall(call), true
		}
	}
	return "", false
}

// axUntilCallAsWait rewrites a call that used until into the same call with
// wait steps: an action's until follows that action, an act-level until
// follows the last action, and an observe until becomes an act call with
// only that wait step. It reports false when a predicate has no wait form.
func axUntilCallAsWait(args map[string]any) (string, bool) {
	mode, _ := args["mode"].(string)
	out := map[string]any{"mode": "act"}
	if ref, ok := args["window_ref"].(string); ok && mode == contract.AXModeAct {
		out["window_ref"] = ref
	}
	if observe, ok := args["observe"].(string); ok && mode == contract.AXModeAct {
		out["observe"] = observe
	}
	steps := []any{}
	if mode == contract.AXModeAct {
		items, _ := args["actions"].([]any)
		for _, item := range items {
			action, ok := item.(map[string]any)
			if !ok {
				return "", false
			}
			copied := map[string]any{}
			var predicate any
			for key, value := range action {
				if contract.ContainsString(axRemovedWaitFields, key) {
					predicate = value
					continue
				}
				copied[key] = value
			}
			steps = append(steps, copied)
			if predicate != nil {
				wait, ok := axUntilAsWait(predicate)
				if !ok {
					return "", false
				}
				steps = append(steps, wait)
			}
		}
	}
	for _, field := range axRemovedWaitFields {
		if predicate, present := args[field]; present {
			wait, ok := axUntilAsWait(predicate)
			if !ok {
				return "", false
			}
			steps = append(steps, wait)
		}
	}
	if len(steps) == 0 {
		return "", false
	}
	out["actions"] = steps
	return axOrderedCall(out), true
}

// axUntilAsWait converts one until predicate into a wait step.
func axUntilAsWait(raw any) (map[string]any, bool) {
	predicate, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}
	wait := map[string]any{"type": "wait"}
	negate, _ := predicate["negate"].(bool)
	if negate {
		wait["gone"] = true
	}
	if timeout, present := predicate["timeout_ms"]; present {
		wait["timeout_ms"] = timeout
	}
	kind, _ := predicate["type"].(string)
	ref, hasRef := predicate["ref"].(string)
	selector, hasSelector := predicate["selector"].(map[string]any)
	query := map[string]any{}
	if hasSelector {
		for field, value := range selector {
			switch field {
			case "role", "state":
				query[field] = value
			case "name":
				query["name_contains"] = value
			case "description", "placeholder":
				query["text_contains"] = value
			default:
				return nil, false
			}
		}
		if len(query) == 0 {
			return nil, false
		}
	}
	switch kind {
	case "title":
		text, _ := predicate["text"].(string)
		if text == "" {
			return nil, false
		}
		wait["window"] = text
	case "exists":
		switch {
		case hasRef:
			// A ref that should appear is already present; waiting for it
			// only makes sense as gone.
			wait["ref"] = ref
			if !negate {
				return nil, false
			}
		case hasSelector:
			wait["query"] = query
		default:
			return nil, false
		}
	case "state", "value", "text":
		field := kind
		value := predicate[field]
		if value == nil {
			return nil, false
		}
		if kind == "value" {
			if text, isText := value.(string); !isText || predicate["match"] == "contains" {
				if !isText {
					value = fmt.Sprint(value)
				}
				// A value wait compares the whole value; contains has no
				// element form, so it becomes a text wait on that element.
				if predicate["match"] == "contains" {
					field = "text"
				}
			} else {
				value = text
			}
		}
		switch {
		case hasRef:
			wait["ref"] = ref
			wait[field] = value
		case hasSelector && kind == "state":
			query["state"] = value
			wait["query"] = query
		case hasSelector && kind == "text":
			query["text_contains"] = value
			wait["query"] = query
		case !hasRef && !hasSelector && kind == "text":
			wait["text"] = value
		default:
			return nil, false
		}
	default:
		return nil, false
	}
	return wait, true
}

// axOrderedCall renders a call with mode, window_ref, and actions first, and
// each action with type first, so a hint reads like the documentation.
func axOrderedCall(call map[string]any) string {
	var out strings.Builder
	out.WriteByte('{')
	keys := []string{"mode", "window_ref", "actions", "observe"}
	written := 0
	for _, key := range keys {
		value, present := call[key]
		if !present {
			continue
		}
		if written > 0 {
			out.WriteByte(',')
		}
		written++
		out.WriteString(strconv.Quote(key) + ":")
		if key != "actions" {
			out.WriteString(contract.CompactJSON(value))
			continue
		}
		out.WriteByte('[')
		for index, item := range value.([]any) {
			if index > 0 {
				out.WriteByte(',')
			}
			action, _ := item.(map[string]any)
			out.WriteString(contract.OrderedJSON(action, []string{"type", "ref", "window", "query", "title"}))
		}
		out.WriteByte(']')
	}
	out.WriteByte('}')
	return out.String()
}

// runAXWait polls one wait step. It sends no input. The result is a
// predicate result: assertion_status "pass", a sampled false, or an error
// when the state could not be read.
func (s *Service) runAXWait(context *AXBatchContext, wait *axWait) ToolCallResult {
	switch {
	case wait.window != "":
		return s.waitForDesktopWindow(context, wait)
	case wait.query != nil:
		return s.waitForQuery(context, wait)
	case wait.ref != "":
		return s.waitForRef(context, wait)
	default:
		return s.waitForText(context, wait)
	}
}

// waitForDesktopWindow polls the top-level windows of the whole desktop:
// a window may belong to another process, and an application that exits and
// starts again has a new one.
func (s *Service) waitForDesktopWindow(context *AXBatchContext, wait *axWait) ToolCallResult {
	deadline := time.Now().Add(time.Duration(wait.timeoutMS) * time.Millisecond)
	attempts := 0
	lastError := ""
	for {
		attempts++
		inventory, failure := s.listDesktopWindows()
		matched := []axDesktopWindow{}
		titleMatches := 0
		if inventory == nil {
			lastError = firstLine(ResultText(failure))
		} else {
			lastError = ""
			for _, title := range inventory.titles {
				if containsFold(title, wait.window) {
					titleMatches++
				}
			}
			for _, window := range inventory.windows {
				if containsFold(window.title, wait.window) {
					matched = append(matched, window)
				}
			}
		}
		holds := titleMatches > 0
		if wait.gone {
			holds = !holds && inventory != nil
		}
		if holds {
			text := "wait: no window title contains " + strconv.Quote(wait.window)
			if !wait.gone {
				names := []string{}
				for _, window := range matched {
					names = append(names, window.ref+" "+strconv.Quote(window.title))
				}
				if len(names) == 0 {
					names = append(names, "titled "+strconv.Quote(wait.window)+" (no ref)")
				}
				text = "wait: window " + strings.Join(names, ", ")
			}
			result := axAssertionResult(text, false, "pass", attempts, 0)
			if len(matched) == 1 {
				result.StructuredContent["wait_window_ref"] = matched[0].ref
			}
			return result
		}
		if !time.Now().Before(deadline) {
			return s.windowWaitTimeout(context, wait, inventory, lastError, attempts)
		}
		time.Sleep(axWaitPollInterval)
	}
}

// windowWaitTimeout reports a window wait that did not hold. The message
// leads with the windows that are there now, each with its wN, focus
// first; when a title is close to the awaited one, the hint is the same
// wait for that title. The batch application's windows come before other
// applications', since the wait most often follows input to that
// application.
func (s *Service) windowWaitTimeout(
	context *AXBatchContext, wait *axWait, inventory *axWindowInventory, readError string, attempts int,
) ToolCallResult {
	head := fmt.Sprintf("wait_timeout: after %dms ", wait.timeoutMS)
	if inventory == nil || readError != "" {
		result := axAssertionResult(head+"the window list could not be read: "+readError, true,
			"observation_incomplete", attempts, 0)
		return result
	}
	if wait.gone {
		still := []string{}
		for _, window := range inventory.windows {
			if containsFold(window.title, wait.window) {
				still = append(still, window.ref+" "+strconv.Quote(window.title))
			}
		}
		if len(still) == 0 {
			// Only windows without a wN are still there.
			still = append(still, strconv.Quote(wait.window)+" (no wN)")
		}
		head += "still open: " + strings.Join(still, ", ")
	} else {
		head += "no window title contains " + strconv.Quote(wait.window)
	}
	appRef := ""
	if context != nil {
		appRef = context.AppRef
	}
	listed := axWaitWindowList(inventory, appRef, wait.window,
		axPublicMessageRunes-len([]rune(head))-len("; windows: "))
	result := axAssertionResult(head+"; windows: "+listed, false, "predicate_false", attempts, 0)
	for _, window := range inventory.windows {
		if window.active {
			result.StructuredContent["focused_window_ref"] = window.ref
		}
	}
	if !wait.gone {
		if near := axNearestWindowTitle(inventory, wait.window); near != "" {
			corrected := map[string]any{"type": "wait", "window": near}
			if wait.timeoutMS != contract.AXDefaultWaitTimeout {
				corrected["timeout_ms"] = wait.timeoutMS
			}
			result.StructuredContent["wait_hint"] = axHintJSON(corrected)
		}
	}
	return result
}

// axWaitWindowList names the windows of a listing as wN "title", the
// focused one first and marked, then the windows of appRef, then the rest;
// untitled windows are left out. It stops before budget runes.
func axWaitWindowList(inventory *axWindowInventory, appRef, wanted string, budget int) string {
	windows := append([]axDesktopWindow(nil), inventory.windows...)
	rank := func(window axDesktopWindow) int {
		switch {
		case window.active:
			return 0
		case appRef != "" && window.appRef == appRef:
			return 1
		case axWordOverlap(axSignificantWords(wanted), window.title) > 0:
			return 2
		}
		return 3
	}
	sort.SliceStable(windows, func(i, j int) bool { return rank(windows[i]) < rank(windows[j]) })
	parts := []string{}
	for _, window := range windows {
		if strings.TrimSpace(window.title) == "" {
			continue
		}
		entry := window.ref + " " + strconv.Quote(axReportClip(window.title))
		if window.active {
			entry += " (focused)"
		}
		if len(parts) == axWaitListedWindows ||
			len([]rune(strings.Join(append(parts, entry, "..."), ", "))) > budget {
			parts = append(parts, "...")
			break
		}
		parts = append(parts, entry)
	}
	if len(parts) == 0 {
		return "none with a title"
	}
	return strings.Join(parts, ", ")
}

const axWaitListedWindows = 4

// axNearestWindowTitle returns the title of the one window whose title is
// close to wanted but does not contain it: "Find and Replace" for "Find &
// Replace", "Move/Copy Sheet" for "Move or Copy Sheet". It returns "" when
// no title or several titles are close.
func axNearestWindowTitle(inventory *axWindowInventory, wanted string) string {
	words := axSignificantWords(wanted)
	if len(words) == 0 {
		return ""
	}
	best, bestScore, ties := "", 0.0, 0
	for _, window := range inventory.windows {
		title := strings.TrimSpace(window.title)
		if title == "" {
			continue
		}
		score := axWordOverlap(words, title)
		// A title that only shares one generic word ("Settings") is not
		// close to a three-word title.
		if score < 0.5 || (len(words) > 1 && score*float64(len(words)) < 2) {
			continue
		}
		switch {
		case score > bestScore:
			best, bestScore, ties = title, score, 1
		case score == bestScore && title != best:
			ties++
		}
	}
	if ties != 1 {
		return ""
	}
	return best
}

// waitForQuery polls every visible window of the batch application with
// find's matching rules.
func (s *Service) waitForQuery(context *AXBatchContext, wait *axWait) ToolCallResult {
	query := wait.query
	name := OptionalString(query, "name_contains")
	_, nameExact := query["name_equals"]
	if nameExact {
		name = OptionalString(query, "name_equals")
	}
	text := OptionalString(query, "text_contains")
	_, textExact := query["text_equals"]
	if textExact {
		text = OptionalString(query, "text_equals")
	}
	role, state := OptionalString(query, "role"), OptionalString(query, "state")
	seen := &axWaitSeen{}
	lookup := func(snapshot *AppSnapshot) (*ElementRecord, error) {
		if snapshot == nil {
			return nil, nil
		}
		seen.add(snapshot)
		for index := range snapshot.Elements {
			record := snapshot.Elements[index]
			if queryRecordMatches(snapshot, &record, role, name, text, state, nameExact, textExact) {
				// The first match answers "appeared"; a wait does not act,
				// so several matches are not ambiguous.
				return &record, nil
			}
		}
		return nil, nil
	}
	exists := !wait.gone
	result := s.waitOnApplication(context, lookup, axWaitQueryText(query), FirstNonEmpty(name, text),
		verifyGoal{exists: &exists}, wait.timeoutMS)
	return seen.explainTimeout(result, wait, newAXNearQuery(role, name, text, state), context)
}

// waitForText polls the batch window for an element whose name,
// description, value, or placeholder contains the text.
func (s *Service) waitForText(context *AXBatchContext, wait *axWait) ToolCallResult {
	seen := &axWaitSeen{}
	lookup := func(snapshot *AppSnapshot) (*ElementRecord, error) {
		if snapshot == nil {
			return nil, nil
		}
		seen.add(snapshot)
		for index := range snapshot.Elements {
			record := snapshot.Elements[index]
			if recordMatchesFind(&record, "", "", wait.text, "", false, false) {
				return &record, nil
			}
		}
		return nil, nil
	}
	exists := !wait.gone
	result := s.waitOnApplication(context, lookup, "text "+strconv.Quote(wait.text), wait.text,
		verifyGoal{exists: &exists}, wait.timeoutMS)
	return seen.explainTimeout(result, wait, newAXNearQuery("", "", wait.text, ""), context)
}

// axWaitSeen keeps the reads of the last poll of an element wait: the
// batch window, then the application's other windows when the batch
// window had no match.
type axWaitSeen struct {
	reads []*AppSnapshot
}

func (seen *axWaitSeen) add(snapshot *AppSnapshot) {
	if snapshot.AXSnapshot != nil {
		// The batch window is read first in every poll.
		seen.reads = seen.reads[:0]
	}
	seen.reads = append(seen.reads, snapshot)
}

// explainTimeout rewrites the first line of an element wait that timed out
// as the elements closest to the query: role, name, and ref when the read
// issued one, in the order of the ranking. A candidate that differs only
// in its role spelling becomes the corrected wait. The rest of the text,
// the poll log, stays.
func (seen *axWaitSeen) explainTimeout(
	result ToolCallResult, wait *axWait, query axNearQuery, context *AXBatchContext,
) ToolCallResult {
	status, _ := result.StructuredContent["assertion_status"].(string)
	if wait.gone || axPostconditionPassed(result) || len(result.Content) == 0 ||
		(status != "predicate_false" && status != "observation_incomplete") {
		return result
	}
	candidates := []axNearCandidate{}
	owner := map[*ElementRecord]*AppSnapshot{}
	for _, snapshot := range seen.reads {
		for _, candidate := range axNearElements(snapshot, query, 3) {
			owner[candidate.record] = snapshot
			candidates = append(candidates, candidate)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })
	subject := strings.TrimPrefix(axWaitSubject(wait), "element ")
	where := ""
	if context != nil && context.WindowRef != "" {
		where = " in " + context.WindowRef + " or its app's other windows"
	}
	line := fmt.Sprintf("wait_timeout: after %dms nothing matches %s%s", wait.timeoutMS, subject, where)
	if status == "observation_incomplete" {
		line += " (a window of the app could not be read)"
	}
	if len(candidates) == 0 {
		line += "; nothing close either"
	}
	listed := 0
	for _, candidate := range candidates {
		label := axNearLabel(owner[candidate.record], candidate.record)
		separator := ", "
		if listed == 0 {
			separator = "; closest: "
		}
		if len([]rune(line+separator+label)) > axPublicMessageRunes {
			break
		}
		line += separator + label
		listed++
		if listed == 3 {
			break
		}
	}
	// The poll log and the generic advice of the shared timeout text are
	// replaced by the candidates; whether the window changed during the
	// wait stays, in its short form.
	text := line
	for _, detail := range strings.Split(result.Content[0].Text, "\n") {
		for _, prefix := range []string{"Window changed:", "Tree changed:", "Nothing changed:"} {
			if strings.HasPrefix(detail, prefix) {
				detail, _, _ = strings.Cut(detail, " —")
				text += "\n" + strings.Join(strings.Fields(detail), " ")
			}
		}
	}
	result.Content[0].Text = text
	if len(candidates) > 0 && candidates[0].sameWords && wait.query != nil {
		best := candidates[0].record
		if corrected := axQueryFor(owner[best], best, wait.query); corrected != nil {
			hint := map[string]any{"type": "wait", "query": corrected}
			if wait.timeoutMS != contract.AXDefaultWaitTimeout {
				hint["timeout_ms"] = wait.timeoutMS
			}
			result.StructuredContent["wait_hint"] = axHintJSON(hint)
		}
	}
	return result
}

// axQueryFor returns query changed as little as possible to match record:
// the role as the tree shows it, and a name or text filter that no longer
// matches replaced by the record's name. It returns nil when no such query
// matches.
func axQueryFor(snapshot *AppSnapshot, record *ElementRecord, query map[string]any) map[string]any {
	corrected := map[string]any{}
	for key, value := range query {
		corrected[key] = value
	}
	if _, present := corrected["role"]; present {
		role := axRenderedRole(snapshot, record)
		if role == "" {
			role = canonicalRole(record)
		}
		corrected["role"] = role
	}
	for _, field := range []string{"name_contains", "name_equals", "text_contains", "text_equals"} {
		value, present := corrected[field].(string)
		if !present {
			continue
		}
		matches := map[string]bool{
			"name_contains": containsFold(record.Name, value),
			"name_equals":   strings.EqualFold(strings.TrimSpace(record.Name), strings.TrimSpace(value)),
		}
		if strings.HasPrefix(field, "text_") {
			_, _, matched := MatchedFindTextField(record, value, field == "text_equals")
			matches[field] = matched
		}
		if !matches[field] {
			if strings.TrimSpace(record.Name) == "" {
				return nil
			}
			delete(corrected, field)
			corrected["name_contains"] = record.Name
		}
	}
	name, _ := corrected["name_contains"].(string)
	_, nameExact := corrected["name_equals"]
	if nameExact {
		name, _ = corrected["name_equals"].(string)
	}
	text, _ := corrected["text_contains"].(string)
	_, textExact := corrected["text_equals"]
	if textExact {
		text, _ = corrected["text_equals"].(string)
	}
	role, _ := corrected["role"].(string)
	state, _ := corrected["state"].(string)
	if !queryRecordMatches(snapshot, record, role, name, text, state, nameExact, textExact) {
		return nil
	}
	return corrected
}

// axWaitSubject names what an element wait waited for.
func axWaitSubject(wait *axWait) string {
	if wait.query != nil {
		return axWaitQueryText(wait.query)
	}
	return "text " + strconv.Quote(wait.text)
}

// axPublicMessageRunes is the length at which the evidence line cuts an
// error message (axPublicEvidence).
const axPublicMessageRunes = 300

func (s *Service) waitOnApplication(
	context *AXBatchContext,
	lookup axElementLookup,
	display, hint string,
	goal verifyGoal,
	timeoutMS int,
) ToolCallResult {
	app := FirstNonEmpty(context.AppRef, context.AppToken)
	result := s.verifyLookup(app, lookup, display, hint, false, goal, timeoutMS, 0,
		contract.AXScopeWindow, context.WindowHandle)
	return axWaitResultText(result)
}

// waitForRef polls one element ref for a value, state, or text, or for its
// absence.
func (s *Service) waitForRef(context *AXBatchContext, wait *axWait) ToolCallResult {
	predicate := map[string]any{"ref": wait.ref, "timeout_ms": float64(wait.timeoutMS)}
	switch {
	case wait.value != "":
		predicate["type"], predicate["value"] = "value", wait.value
	case wait.state != "":
		predicate["type"], predicate["state"] = "state", wait.state
	case wait.text != "":
		predicate["type"], predicate["text"] = "text", wait.text
	default:
		predicate["type"] = "exists"
	}
	if wait.gone {
		predicate["negate"] = true
	}
	assertion, argErr := decodeAXPredicate(predicate, "wait")
	if argErr != nil {
		return TextResult(argErr.Message, true)
	}
	if err := s.preflightAXPostcondition(context, assertion); err != nil {
		return axStaleRefResult(wait.ref, err)
	}
	if held, ok := s.waitHoldsInLatestRead(context, wait.ref, assertion); ok {
		return held
	}
	return axWaitResultText(s.runAXPostcondition(context, assertion))
}

// waitHoldsInLatestRead checks a ref wait against the read the previous
// step already made, the same read the effect line reports. When it holds
// there, the wait is answered without polling; the live resolver cannot
// read some elements completely and would otherwise poll to its deadline
// before a canonical read decides.
func (s *Service) waitHoldsInLatestRead(
	context *AXBatchContext,
	ref string,
	assertion map[string]any,
) (ToolCallResult, bool) {
	snapshot := s.currentSnapshot(context.AppToken)
	expected, err := s.RefRegistry.Resolve(ref, "")
	if snapshot == nil || err != nil {
		return ToolCallResult{}, false
	}
	record, err := s.exactRefRecordFromSnapshot(snapshot, ref, expected)
	if err != nil || record == nil {
		return ToolCallResult{}, false
	}
	goal := assertionGoal(assertion)
	held, observed := goal.check(record, nil)
	if !held {
		return ToolCallResult{}, false
	}
	return axAssertionResult(fmt.Sprintf("wait: %s %s. %s", ref, goal.describe(), observed),
		false, "pass", 1, 0), true
}

// axWaitResultText names a failed wait wait_timeout in its first line; the
// polling code's text says why it did not hold.
func axWaitResultText(result ToolCallResult) ToolCallResult {
	if len(result.Content) == 0 || result.Content[0].Type != "text" {
		return result
	}
	text := result.Content[0].Text
	for _, prefix := range []string{"wait: ", "wait false: ", "wait ", "PASS: "} {
		text = strings.TrimPrefix(text, prefix)
	}
	if axPostconditionPassed(result) {
		text = "wait: " + text
	} else if !strings.HasPrefix(text, "wait_timeout") {
		text = "wait_timeout: " + text
	}
	result.Content[0].Text = text
	return result
}

func axBatchOnlyWaits(actions []axBatchAction) bool {
	for _, action := range actions {
		if action.actionType != "wait" {
			return false
		}
	}
	return len(actions) > 0
}

// preflightAXWaitRef checks that a wait ref still names an element of the
// batch window before any input.
func (s *Service) preflightAXWaitRef(context *AXBatchContext, ref string) error {
	entry, err := s.resolveShortAXRefOwned(ref)
	if err != nil {
		return err
	}
	if context != nil && entry.WindowHandle != context.WindowHandle {
		return fmt.Errorf("%s belongs to window %s, not the batch window %s; wait for it in its own act call",
			ref, entry.LogicalWindowRef, context.WindowRef)
	}
	return nil
}

func axWaitEvidenceEntry(index int, result ToolCallResult) map[string]any {
	entry := map[string]any{
		"action_index":  index,
		"action_number": index + 1,
		"action_type":   "wait",
		"call_error":    !axPostconditionPassed(result),
	}
	if status, _ := result.StructuredContent["assertion_status"].(string); status != "" {
		entry["wait_status"] = status
	}
	return entry
}

// rebindAfterWindowWait moves the rest of the batch into the window a wait
// found: after {"type":"wait","window":"Export"}, keys and ref-less text go
// to that window, as when the call had named it. Element refs from the
// earlier window still fail on window identity.
func (s *Service) rebindAfterWindowWait(
	context *AXBatchContext,
	wait *axWait,
	result ToolCallResult,
) {
	if wait == nil || wait.window == "" || wait.gone {
		return
	}
	ref, _ := result.StructuredContent["wait_window_ref"].(string)
	if ref == "" || ref == context.WindowRef {
		return
	}
	entry, err := s.WindowRefRegistry.Resolve(ref, "")
	if err != nil || !validInternalWindowHandle(entry.WindowHandle) {
		return
	}
	context.WindowRef = entry.Ref
	context.WindowHandle = entry.WindowHandle
	context.AppRef = FirstNonEmpty(entry.AppRef, context.AppRef)
	context.AppToken = FirstNonEmpty(entry.AppRef, entry.AppIdentity, context.AppToken)
	context.boundByFocus = false
	context.expectedWindow = &ProviderWindowExpectation{
		WindowHandle:       entry.WindowHandle,
		ProviderBusName:    entry.ProviderWindowBusName,
		ProviderObjectPath: entry.ProviderWindowObjectPath,
		ProcessGeneration:  entry.ProviderWindowProcessGeneration,
		Role:               entry.ProviderWindowRole,
		Name:               entry.ProviderWindowName,
	}
	// One read of the new window makes it the current state of its app, so
	// the next step's window check and its diff start from that window.
	noScreenshot := false
	_, _, _ = s.refreshSnapshot(context.AppToken, AXServiceRequest{
		Operation:          axServiceObserve,
		App:                context.AppToken,
		IncludeScreenshot:  &noScreenshot,
		TargetWindowHandle: context.WindowHandle,
		SourceRef:          context.WindowRef,
		Scope:              contract.AXScopeWindow,
		TreeView:           contract.TreeViewWorking,
	})
}

// stopAXBatchAtWait ends a batch at a wait that did not hold. The steps
// before it ran; none after it were sent. The current state follows.
func (s *Service) stopAXBatchAtWait(
	context *AXBatchContext,
	actions []axBatchAction,
	index int,
	waitResult ToolCallResult,
	actionEvidence []map[string]any,
	waitNotes []string,
	obs observation,
) ToolCallResult {
	anyInputEmitted, anyPartialPossible, anyMutationUnknown := batchMutationSummary(actionEvidence)
	message := firstLine(ResultText(waitResult))
	if !strings.HasPrefix(message, "wait_timeout") {
		message = "wait_timeout: " + message
	}
	detail := strings.TrimSpace(strings.TrimPrefix(ResultText(waitResult), firstLine(ResultText(waitResult))))
	unreadable := waitResult.IsError
	if unreadable && !strings.Contains(message, "could not be read") {
		message += " (the state could not be read completely while waiting)"
	}
	stopped := TextResult(message, true)
	if len(waitNotes) > 0 {
		stopped.Content = append([]ContentItem{{Type: "text", Text: strings.Join(waitNotes, "\n")}}, stopped.Content...)
	}
	if detail != "" {
		stopped.Content = append(stopped.Content, ContentItem{Type: "text", Text: detail})
	}
	hint := `{"mode":"observe","window_ref":"` + FirstNonEmpty(context.WindowRef, "wN") + `"}`
	if ref, _ := waitResult.StructuredContent["until_window_ref"].(string); isShortAXWindowRef(ref) {
		hint = `{"mode":"observe","window_ref":"` + ref + `"}`
	}
	if wait := actions[index].wait; wait != nil && wait.window != "" {
		// The message lists the windows; the focused one is where the
		// application went instead.
		hint = `{"mode":"apps"}`
		if ref, _ := waitResult.StructuredContent["focused_window_ref"].(string); ref != "" {
			hint = `{"mode":"observe","window_ref":"` + ref + `"}`
		}
	}
	if corrected, _ := waitResult.StructuredContent["wait_hint"].(string); corrected != "" {
		hint = corrected
	}
	stopped.StructuredContent = map[string]any{
		contract.AXPublicErrorCodeKey:   contract.AXCodeWaitTimeout,
		contract.AXPublicErrorPathKey:   fmt.Sprintf("actions[%d]", index),
		contract.AXPublicErrorDetailKey: message,
		contract.AXPublicErrorHintKey:   hint,
		"failure_code":                  "wait_timeout",
		"failure_phase":                 "wait",
		"input_events_emitted":          anyInputEmitted,
		"partial_input_possible":        anyPartialPossible,
		"mutation_unknown":              anyMutationUnknown,
		"retry_same_request":            false,
		"a11y_batch_result": map[string]any{
			"actions_requested":                    len(actions),
			"actions_completed_without_call_error": index,
			"failed_action_index":                  index,
			"failed_action_type":                   "wait",
			"rejected_before_first_action":         index == 0,
			"partial_sequence":                     index > 0,
			"actions":                              actionEvidence,
		},
	}
	if obs.resolvedMode() == contract.ObserveNone {
		return stopped
	}
	current := s.axBatchFinalObservation(context, obs, false)
	if current.IsError {
		stopped.Content = append(stopped.Content, ContentItem{
			Type: "text",
			Text: "current state unavailable: " + firstLine(ResultText(current)),
		})
		stopped.StructuredContent["observation_required"] = true
		return stopped
	}
	stopped.Content = append(stopped.Content, current.Content...)
	stopped.StructuredContent["final_observation_returned"] = true
	for _, key := range []string{"final_observation_kind", "tree_complete_for_model",
		"coverage_incomplete_nodes", "structural_budget_omitted_nodes"} {
		if value, present := current.StructuredContent[key]; present {
			stopped.StructuredContent[key] = value
		}
	}
	return stopped
}

// axBatchContinuityStop stops before the next input when the batch window
// changed or cannot be confirmed.
func (s *Service) axBatchContinuityStop(
	context *AXBatchContext,
	actions []axBatchAction,
	index int,
	actionEvidence []map[string]any,
	obs observation,
) (ToolCallResult, bool) {
	continuity := s.axBatchWindowContinuity(context)
	if continuity == axWindowSame {
		return ToolCallResult{}, false
	}
	anyInputEmitted, anyPartialPossible, anyMutationUnknown := batchMutationSummary(actionEvidence)
	failureCode := "stale_target"
	messageCode := "batch_window_changed"
	if continuity == axWindowUnknown {
		failureCode = "observation_incomplete"
		messageCode = "batch_window_unknown"
	}
	recovery, hint := s.axBatchContinuityRecovery(context)
	stopped := TextResult(
		fmt.Sprintf(
			"%s: step %d/%d completed, but the logical window is %s "+
				"before step %d. The remaining steps were not attempted. %s",
			messageCode, index+1, len(actions), continuity, index+2, recovery,
		),
		true,
	)
	stopped.StructuredContent = map[string]any{
		contract.AXPublicErrorHintKey:          hint,
		"failure_code":                         failureCode,
		"failure_phase":                        "between_actions",
		"window_continuity":                    string(continuity),
		"actions_requested":                    len(actions),
		"actions_completed_without_call_error": index + 1,
		"stopped_before_action_index":          index + 1,
		"rejected_before_first_action":         false,
		"partial_sequence":                     true,
		"final_observation_returned":           false,
		"actions":                              actionEvidence,
		"any_input_emitted":                    anyInputEmitted,
		"input_events_emitted":                 anyInputEmitted,
		"partial_input_possible":               anyPartialPossible,
		"mutation_unknown":                     anyMutationUnknown || anyInputEmitted || anyPartialPossible,
		"observation_required":                 true,
		"retry_same_request":                   false,
	}
	return s.recoverAXBatchObservation(context, stopped, obs), true
}

// Use the read that detected the change. A popup need not have a title or
// an addressable window, and closing a dialog can reveal an existing window.
func (s *Service) axBatchContinuityRecovery(context *AXBatchContext) (string, string) {
	var snapshot *AppSnapshot
	if s != nil && context != nil {
		snapshot = s.currentSnapshot(context.AppToken)
	}
	if snapshot == nil || snapshot.WindowAbsent {
		return "List current windows, inspect the intended window, then send only the remaining actions.", `{"mode":"apps"}`
	}
	if ref := snapshot.publicWindowRef; ref != "" {
		if entry, err := s.WindowRefRegistry.Resolve(ref, ""); err == nil && entry.WindowHandle == snapshot.WindowHandle {
			return "The latest read shows " + ref + ". Observe that window before sending only the remaining actions.",
				axHintJSON(map[string]any{"mode": "observe", "window_ref": ref})
		}
	}
	return "The latest window has no usable wN. Use GUI screenshot to inspect the window or popup before replanning the remaining actions.", GUIScreenshotCall
}

// axWaitOnly runs an act call whose steps are all waits. It sends no
// input, so it needs no focused window: the batch window comes from a ref
// or window_ref, or from the focused window when one exists. A window wait
// needs neither.
func (s *Service) axWaitOnly(
	actions []axBatchAction,
	obs observation,
	windowRef string,
) ToolCallResult {
	context := &AXBatchContext{}
	needsWindow := false
	for _, action := range actions {
		if action.wait.window == "" {
			needsWindow = true
		}
	}
	if needsWindow {
		var focus *axDesktopWindow
		if windowRef == "" && !axBatchHasRef(actions) {
			var argErr *axArgError
			focus, argErr = s.resolveFocusedWindow()
			if argErr != nil {
				argErr.Message = "a wait without ref or window_ref reads the focused window, and no window has focus; " +
					"give window_ref, or wait for a window by title. Windows:" + strings.TrimPrefix(argErr.Message[strings.Index(argErr.Message, "Windows:"):], "Windows:")
				argErr.Hint = `{"mode":"act","window_ref":"w1","actions":[{"type":"wait","text":"Saved"}]}`
				return axArgErrorResult(argErr, len(actions))
			}
		}
		bound, err := s.deriveAXBatchContext(windowRef, actions, focus)
		if err != nil {
			return axBatchPreflightError(err, len(actions))
		}
		context = bound
		for index, action := range actions {
			if action.wait.ref == "" {
				continue
			}
			if err := s.preflightAXWaitRef(context, action.wait.ref); err != nil {
				return axBatchPreflightError(fmt.Errorf("actions[%d].ref: %w", index, err), len(actions))
			}
		}
	}
	evidence := []map[string]any{}
	notes := []string{}
	for index, action := range actions {
		result := s.runAXWait(context, action.wait)
		evidence = append(evidence, axWaitEvidenceEntry(index, result))
		if !axPostconditionPassed(result) {
			if context.WindowHandle == "" {
				obs = observation{mode: contract.ObserveNone}
			}
			return s.stopAXBatchAtWait(context, actions, index, result, evidence, notes, obs)
		}
		notes = append(notes, firstLine(ResultText(result)))
		s.rebindAfterWindowWait(context, action.wait, result)
	}
	result := TextResult(strings.Join(notes, "\n"), false)
	result.StructuredContent = map[string]any{
		"input_events_emitted": false,
		"a11y_batch_result": map[string]any{
			"actions_requested":                    len(actions),
			"actions_completed_without_call_error": len(actions),
			"actions":                              evidence,
		},
	}
	if context.WindowHandle != "" && obs.resolvedMode() != contract.ObserveNone {
		current := s.axBatchFinalObservation(context, obs, false)
		if !current.IsError {
			result.Content = append(result.Content, current.Content...)
			for _, key := range []string{"final_observation_kind", "final_observation_returned"} {
				if value, present := current.StructuredContent[key]; present {
					result.StructuredContent[key] = value
				}
			}
		}
	}
	return result
}

// axReadbackOnlyFailure reports an action whose input was sent and whose
// only failure is the read after it.
func axReadbackOnlyFailure(result ToolCallResult) bool {
	code, _ := result.StructuredContent["failure_code"].(string)
	phase, _ := result.StructuredContent["failure_phase"].(string)
	return code == "effect_unknown" && phase == "post_action_observation" &&
		axEvidenceReportsInputEmission(result.StructuredContent)
}

// axClearReadbackFailure drops the facts of a read that failed after input
// once a wait has read the awaited state live. Facts about the input itself
// (input_events_emitted, partial_input_possible) stay.
func axClearReadbackFailure(structured map[string]any) {
	code, _ := structured["failure_code"].(string)
	observationCode, _ := structured["observation_failure_code"].(string)
	if code != "effect_unknown" && observationCode == "" {
		return
	}
	for _, key := range []string{"failure_code", "failure_phase", "failure_subject",
		"underlying_failure_code", "observation_failure_code", "post_action_observation_status",
		"continuation", "effect_unknown", "mutation_unknown", "recovery"} {
		delete(structured, key)
	}
	structured["observation_required"] = false
	structured["next_identical_observation_required"] = false
}

// decodeAXPredicate converts a predicate written in the earlier until shape
// into the internal assertion object that the polling code evaluates. Wait
// steps build their ref conditions through it; it is not a public field.
func decodeAXPredicate(raw any, path string) (map[string]any, *axArgError) {
	object, ok := raw.(map[string]any)
	example := `{"type":"exists","ref":"r1"}`
	if !ok {
		return nil, axInvalid(path, "until must be an object", example)
	}
	fields := []string{"type", "ref", "selector", "state", "value", "match", "text", "negate", "timeout_ms", "stable_ms"}
	if err := axCheckFields(object, fields, path, "until"); err != nil {
		return nil, err
	}
	rawType := object["type"]
	kind, _ := rawType.(string)
	kinds := []string{"exists", "state", "value", "text", "title"}
	if !contract.ContainsString(kinds, kind) {
		return nil, axInvalid(path+".type", "until.type must be one of "+strings.Join(kinds, ", "), example)
	}
	perType := map[string][]string{
		"exists": {"ref", "selector"},
		"state":  {"ref", "selector", "state"},
		"value":  {"ref", "selector", "value", "match"},
		"text":   {"ref", "selector", "text"},
		"title":  {"text"},
	}
	common := []string{"type", "negate", "timeout_ms", "stable_ms"}
	if err := axCheckFields(object, append(common, perType[kind]...), path, "until type "+strconv.Quote(kind)); err != nil {
		return nil, err.withRule(axRuleFieldForType)
	}
	negate := false
	if rawNegate, present := object["negate"]; present {
		value, ok := rawNegate.(bool)
		if !ok {
			return nil, axInvalid(path+".negate", "negate must be true or false", "").withFix(axRemove(path + ".negate"))
		}
		negate = value
	}
	assertion := map[string]any{}
	if rawTimeout, present := object["timeout_ms"]; present {
		timeout, ok := axInteger(rawTimeout)
		if !ok || timeout < 1 || timeout > contract.MaxVerifyTimeout {
			return nil, axInvalid(path+".timeout_ms", fmt.Sprintf("timeout_ms must be an integer from 1 to %d", contract.MaxVerifyTimeout), "").
				withFix(axNumberEdit(path+".timeout_ms", rawTimeout, 1, contract.MaxVerifyTimeout, true))
		}
		assertion["timeout_ms"] = timeout
	}
	if rawStable, present := object["stable_ms"]; present {
		stable, ok := axInteger(rawStable)
		if !ok || stable < 0 || stable > contract.AXMaxUntilStableMS {
			return nil, axInvalid(path+".stable_ms", fmt.Sprintf("stable_ms must be an integer from 0 to %d", contract.AXMaxUntilStableMS), "").
				withFix(axNumberEdit(path+".stable_ms", rawStable, 0, contract.AXMaxUntilStableMS, true))
		}
		assertion["stable_ms"] = stable
	}
	if kind == "title" {
		text, err := axRequiredText(object, "text", path, `{"type":"title","text":"Saved"}`)
		if err != nil {
			return nil, err.withRule(axRuleRequiredForType)
		}
		assertion["type"] = "window_title_contains"
		assertion["text"] = text
		assertion["exists"] = !negate
		return assertion, nil
	}
	target, err := decodeAXPredicateTarget(object, path, kind)
	if err != nil {
		return nil, err
	}
	assertion["target"] = target
	switch kind {
	case "exists":
		assertion["type"] = "exists"
		assertion["value"] = !negate
	case "state":
		state, err := axRequiredText(object, "state", path, `{"type":"state","ref":"r1","state":"checked"}`)
		if err != nil {
			return nil, err.withRule(axRuleRequiredForType)
		}
		assertion["type"] = "state"
		assertion["state"] = state
		assertion["value"] = !negate
	case "value":
		value, present := object["value"]
		valid := present
		switch typed := value.(type) {
		case string:
			valid = valid && strings.TrimSpace(typed) != ""
		case bool:
		case json.Number:
			_, numErr := typed.Float64()
			valid = valid && numErr == nil
		case float64:
			valid = valid && !math.IsNaN(typed) && !math.IsInf(typed, 0)
		case int:
		default:
			valid = false
		}
		if !valid {
			return nil, axInvalid(path+".value", "value needs a non-blank string, a number, or a boolean",
				`{"type":"value","ref":"r1","value":"14"}`).withRule(axRuleRequiredForType)
		}
		assertion["type"] = "value"
		assertion["value"] = value
		if rawMatch, present := object["match"]; present {
			match, ok := rawMatch.(string)
			if !ok || (match != "equals" && match != "contains") {
				return nil, axInvalid(path+".match", `match must be "equals" or "contains"`, "").withFix(axRemove(path + ".match"))
			}
			if _, isString := value.(string); match == "contains" && !isString {
				return nil, axInvalid(path+".match", `match "contains" needs a string value`, "").
					withRule(axRuleValueMatch).withFix(axRemove(path + ".match"))
			}
			assertion["match"] = match
		}
		if negate {
			assertion["negate"] = true
		}
	case "text":
		text, err := axRequiredText(object, "text", path, `{"type":"text","ref":"r1","text":"Done"}`)
		if err != nil {
			return nil, err.withRule(axRuleRequiredForType)
		}
		assertion["type"] = "text_contains"
		assertion["text"] = text
		if negate {
			assertion["negate"] = true
		}
	}
	return assertion, nil
}

func decodeAXPredicateTarget(object map[string]any, path, kind string) (map[string]any, *axArgError) {
	ref, hasRef := object["ref"]
	selector, hasSelector := object["selector"]
	if hasRef == hasSelector {
		fix := axSet(path+".ref", "r1")
		if hasRef {
			fix = axRemove(path + ".selector")
		}
		return nil, axInvalid(path, "until type "+strconv.Quote(kind)+" needs exactly one of ref (rN) or selector",
			"").withRule(axRuleOneOf).withFix(fix)
	}
	if hasRef {
		text, ok := ref.(string)
		if !ok || !AXElementRefPattern.MatchString(text) {
			message := "ref must be an element ref rN copied from observe or find"
			if ok && AXWindowRefPattern.MatchString(text) {
				message = `ref must be an element rN; for a window use {"type":"title","text":"..."}`
			}
			return nil, axInvalid(path+".ref", message, "").withFix(axRemove(path + ".ref"))
		}
		return map[string]any{"ref": text}, nil
	}
	fields, ok := selector.(map[string]any)
	if !ok || len(fields) == 0 {
		return nil, axInvalid(path+".selector", "selector must be an object with at least one of role, name, description, placeholder, state",
			`{"role":"dialog","name":"Print"}`)
	}
	allowed := []string{"role", "name", "description", "placeholder", "state"}
	if err := axCheckFields(fields, allowed, path+".selector", "selector"); err != nil {
		return nil, err
	}
	for _, field := range allowed {
		value, present := fields[field]
		if !present {
			continue
		}
		text, ok := value.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return nil, axInvalid(path+".selector."+field, field+" must be a non-blank string", "").
				withFix(axRemove(path + ".selector." + field))
		}
		if field == "state" && !publicSelectorStateWords[text] {
			return nil, axInvalid(path+".selector.state", "selector state must be checked, expanded, selected, or focused", "").
				withFix(axRemove(path + ".selector.state"))
		}
	}
	if _, err := axSelectorAddress(fields); err != nil {
		return nil, axInvalid(path+".selector", "selector "+err.Error(), `{"role":"dialog","name":"Print"}`)
	}
	return map[string]any{"selector": fields}, nil
}

func axRequiredText(object map[string]any, field, path, hint string) (string, *axArgError) {
	text, err := axOptionalString(object, field, path, true)
	if err != nil {
		err.Hint = hint
		return "", err
	}
	if text == "" {
		return "", axInvalid(axJoinPath(path, field), field+" is required", hint)
	}
	return text, nil
}
