package main

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// This file decodes the public AX arguments. The published input schema is
// flat (no oneOf), so it cannot say which fields belong to which mode or
// action type; those rules live here, and every rejection names the field
// and shows a corrected call. Decoding is strict: no case folding, aliases,
// or numeric strings. The services below receive only decoded values.

// Public AX error codes. Internal failure codes stay in telemetry; the model
// sees only these.
const (
	axCodeInvalidArgument  = "invalid_argument"
	axCodeStaleRef         = "stale_ref"
	axCodeWindowChanged    = "window_changed"
	axCodeUntilFailed      = "until_failed"
	axCodeUntilUnknown     = "until_unknown"
	axCodeEffectUnknown    = "effect_unknown"
	axCodeAppUnavailable   = "app_unavailable"
	axCodeRuntimeError     = "runtime_error"
	axCodeAmbiguousWindow  = "ambiguous_window"
	axCodeWindowNotFound   = "window_not_found"
	axCodeNoFocusedWindow  = "no_focused_window"
	a11yModeApps           = "apps"
	axDefaultFindPageSize  = 20
	axDefaultUntilTimeout  = 5000
	axMaxUntilStableMS     = 5000
	axPublicErrorCodeKey   = "public_error_code"
	axPublicErrorPathKey   = "public_error_path"
	axPublicErrorHintKey   = "public_error_hint"
	axPublicErrorDetailKey = "public_error_message"
)

// axArgError is a rejected argument. It is returned before any input.
// rule names the cross-field rule a schema-valid call broke; it is empty
// when the published schema rejects the call too.
type axArgError struct {
	code    string
	path    string
	message string
	hint    string
	rule    string
}

// Cross-field rules that the flat AX schema cannot express. The consistency
// test accepts a runtime rejection of a schema-valid call only with one of
// these codes.
const (
	axRuleFieldForMode    = "ax_field_for_mode"
	axRuleFieldForType    = "ax_field_for_action_type"
	axRuleRequiredForMode = "ax_required_for_mode"
	axRuleRequiredForType = "ax_required_for_action_type"
	axRuleOneOf           = "ax_exactly_one_of"
	axRuleRefKind         = "ax_ref_kind_for_action_type"
	axRuleObserveNone     = "ax_observe_none_needs_until"
	axRuleFindFilterPair  = "ax_find_filter_pair"
	axRuleValueMatch      = "ax_value_match"
	axRuleBlankValue      = "ax_blank_value"
)

func (e *axArgError) withRule(rule string) *axArgError {
	if e != nil {
		e.rule = rule
	}
	return e
}

func (e *axArgError) Error() string { return e.message }

func axInvalid(path, message, hint string) *axArgError {
	return &axArgError{
		code: axCodeInvalidArgument, path: path, message: message, hint: hint,
	}
}

// axRequest is one decoded AX call.
type axRequest struct {
	mode      string
	app       string
	windowRef string
	scope     string
	observe   string
	root      string
	query     map[string]any
	limit     int
	cursor    string
	actions   []a11yBatchAction
	until     map[string]any
}

var (
	axElementRefPattern = regexp.MustCompile(`^r[1-9][0-9]*$`)
	axWindowRefPattern  = regexp.MustCompile(`^w[1-9][0-9]*$`)
	axCursorPattern     = regexp.MustCompile(`^f[1-9][0-9]*$`)
	axRootPattern       = regexp.MustCompile(`^(focused|r[1-9][0-9]*)$`)
)

var axModeFields = map[string][]string{
	a11yModeApps:    {"mode"},
	a11yModeObserve: {"mode", "app", "window_ref", "scope", "observe", "root", "until"},
	a11yModeFind:    {"mode", "app", "window_ref", "scope", "query", "limit", "cursor"},
	a11yModeAct:     {"mode", "window_ref", "actions", "observe", "until"},
}

var axActionTypes = []string{
	"click", "type", "keypress", "set_value", "scroll", "activate", "close",
}

// axActionFields lists the fields each action type accepts besides type and
// until.
var axActionFields = map[string][]string{
	"click":     {"ref"},
	"type":      {"ref", "text"},
	"keypress":  {"ref", "keys"},
	"set_value": {"ref", "value"},
	"scroll":    {"ref", "direction", "pages"},
	"activate":  {"ref", "title"},
	"close":     {"ref", "title"},
}

// axRenamedFields maps field names from earlier interfaces, and names models
// commonly write, to the current field.
var axRenamedFields = map[string]string{
	"action":        "type",
	"target":        "ref",
	"postcondition": "until",
	"assertion":     "until",
	"key":           "keys",
	"app_ref":       "app",
}

func decodeAXRequest(args map[string]any) (*axRequest, *axArgError) {
	rawMode, present := args["mode"]
	if !present {
		return nil, axInvalid("mode", "mode is required: apps, observe, find, or act",
			`{"mode":"apps"}`)
	}
	mode, ok := rawMode.(string)
	if !ok || !containsString([]string{a11yModeApps, a11yModeObserve, a11yModeFind, a11yModeAct}, mode) {
		if mode == "assert_ax" {
			return nil, axInvalid("mode",
				`mode "assert_ax" was removed; use observe with until`,
				`{"mode":"observe","window_ref":"w1","until":{"type":"exists","ref":"r1"}}`)
		}
		return nil, axInvalid("mode", "mode must be apps, observe, find, or act; got "+
			compactJSON(rawMode), `{"mode":"apps"}`)
	}
	if err := axCheckFields(args, axModeFields[mode], "", "mode "+strconv.Quote(mode)); err != nil {
		return nil, err.withRule(axRuleFieldForMode)
	}
	request := &axRequest{mode: mode}
	var err *axArgError
	if request.app, err = axOptionalString(args, "app", "", true); err != nil {
		return nil, err
	}
	if request.windowRef, err = axOptionalPattern(args, "window_ref", "", axWindowRefPattern, "wN"); err != nil {
		return nil, err
	}
	if request.app != "" && request.windowRef != "" {
		return nil, axInvalid("window_ref", "use app or window_ref, not both; window_ref already identifies its app",
			`{"mode":"`+mode+`","window_ref":"`+request.windowRef+`"}`)
	}
	if request.scope, err = axOptionalEnum(args, "scope", "", a11yScopeValues); err != nil {
		return nil, err
	}
	if request.observe, err = axOptionalEnum(args, "observe", "", observeValues); err != nil {
		return nil, err
	}
	if request.root, err = axOptionalPattern(args, "root", "", axRootPattern, `"focused" or rN`); err != nil {
		return nil, err
	}
	if raw, present := args["until"]; present {
		if request.until, err = decodeAXPredicate(raw, "until"); err != nil {
			return nil, err
		}
	}
	switch mode {
	case a11yModeObserve:
		if request.observe == observeNone && request.until == nil {
			return nil, axInvalid("observe", `observe "none" returns nothing without until`,
				`{"mode":"observe","window_ref":"w1"}`).withRule(axRuleObserveNone)
		}
		if request.app == "" && request.windowRef == "" &&
			(request.until == nil || !isShortAXRef(optionalString(axUntilTarget(request.until), "ref"))) {
			return nil, axInvalid("app", "observe needs app or window_ref",
				`{"mode":"observe","app":"a1"}`).withRule(axRuleRequiredForMode)
		}
	case a11yModeFind:
		if err := decodeAXFind(args, request); err != nil {
			return nil, err
		}
	case a11yModeAct:
		raw, present := args["actions"]
		if !present {
			return nil, axInvalid("actions", "act requires actions",
				`{"mode":"act","actions":[{"type":"click","ref":"r1"}]}`).withRule(axRuleRequiredForMode)
		}
		if request.actions, err = decodeAXActions(raw); err != nil {
			return nil, err
		}
	}
	return request, nil
}

func decodeAXFind(args map[string]any, request *axRequest) *axArgError {
	var err *axArgError
	if request.cursor, err = axOptionalPattern(args, "cursor", "", axCursorPattern, "fN"); err != nil {
		return err
	}
	request.limit = axDefaultFindPageSize
	if raw, present := args["limit"]; present {
		limit, ok := axInteger(raw)
		if !ok || limit < 1 || limit > maxAXFindPageSize {
			return axInvalid("limit", fmt.Sprintf("limit must be an integer from 1 to %d", maxAXFindPageSize), "")
		}
		request.limit = limit
	}
	if request.cursor != "" {
		for _, field := range []string{"app", "window_ref", "scope", "query"} {
			if _, present := args[field]; present {
				return axInvalid(field, "a cursor continues an earlier find; send only mode, cursor, and limit",
					`{"mode":"find","cursor":"`+request.cursor+`"}`).withRule(axRuleFieldForMode)
			}
		}
		return nil
	}
	if request.app == "" && request.windowRef == "" {
		return axInvalid("app", "find needs app or window_ref, or a cursor from a truncated result",
			`{"mode":"find","window_ref":"w1","query":{"role":"button"}}`).withRule(axRuleRequiredForMode)
	}
	raw, present := args["query"]
	if !present {
		return axInvalid("query", "find requires query",
			`{"mode":"find","window_ref":"w1","query":{"name_contains":"Save"}}`).withRule(axRuleRequiredForMode)
	}
	query, ok := raw.(map[string]any)
	if !ok {
		return axInvalid("query", "query must be an object", `{"name_contains":"Save"}`)
	}
	filters := []string{"role", "name_equals", "name_contains", "text_equals", "text_contains", "state"}
	if err := axCheckFields(query, filters, "query", "query"); err != nil {
		return err
	}
	if len(query) == 0 {
		return axInvalid("query", "query needs at least one filter: "+strings.Join(filters, ", "),
			`{"name_contains":"Save"}`).withRule(axRuleRequiredForMode)
	}
	for _, field := range filters {
		if _, err := axOptionalString(query, field, "query", true); err != nil {
			return err
		}
	}
	for _, pair := range [][2]string{{"name_equals", "name_contains"}, {"text_equals", "text_contains"}} {
		if _, first := query[pair[0]]; first {
			if _, second := query[pair[1]]; second {
				return axInvalid("query."+pair[1], "use "+pair[0]+" or "+pair[1]+", not both", "").withRule(axRuleFindFilterPair)
			}
		}
	}
	request.query = query
	return nil
}

func decodeAXActions(raw any) ([]a11yBatchAction, *axArgError) {
	items, ok := raw.([]any)
	if !ok {
		return nil, axInvalid("actions", "actions must be an array of action objects",
			`[{"type":"click","ref":"r1"}]`)
	}
	if len(items) == 0 {
		return nil, axInvalid("actions", `act needs at least one action; use mode "observe" to read`,
			`[{"type":"click","ref":"r1"}]`)
	}
	if len(items) > a11yBatchMaxActions {
		return nil, axInvalid("actions", fmt.Sprintf("act accepts at most %d actions", a11yBatchMaxActions), "")
	}
	actions := make([]a11yBatchAction, 0, len(items))
	totalText := 0
	for index, item := range items {
		path := fmt.Sprintf("actions[%d]", index)
		object, ok := item.(map[string]any)
		if !ok {
			return nil, axInvalid(path, "each action must be an object", `{"type":"click","ref":"r1"}`)
		}
		action, err := decodeAXAction(object, path)
		if err != nil {
			return nil, err
		}
		if action.actionType == "close" && index != len(items)-1 {
			return nil, axInvalid(path, "close must be the last action; a closed window cannot take later input", "").withRule(argumentRuleCloseNotLast)
		}
		totalText += len([]rune(action.text)) + len([]rune(action.value))
		if totalText > a11yBatchMaxText {
			return nil, axInvalid(path, fmt.Sprintf("text and value together are limited to %d characters per call", a11yBatchMaxText), "").withRule(argumentRuleTextBudget)
		}
		actions = append(actions, action)
	}
	return actions, nil
}

func decodeAXAction(object map[string]any, path string) (a11yBatchAction, *axArgError) {
	action := a11yBatchAction{pages: 1}
	rawType, hasType := object["type"]
	actionType, _ := rawType.(string)
	if !hasType || !containsString(axActionTypes, actionType) {
		if hint := axActionShapeHint(object); hint != "" && !hasType {
			field := "type"
			for name := range object {
				if axRenamedFields[name] == "type" {
					field = name
				}
			}
			return action, axInvalid(path+"."+field, `actions use "type", not "`+field+`"`, hint)
		}
		message := "type must be one of " + strings.Join(axActionTypes, ", ")
		if hasType {
			message += "; got " + compactJSON(rawType)
		} else {
			message = "action needs type: " + strings.Join(axActionTypes, ", ")
		}
		return action, axInvalid(path+".type", message, axActionShapeHint(object))
	}
	action.actionType = actionType
	allowed := append([]string{"type", "until"}, axActionFields[actionType]...)
	if err := axCheckFields(object, allowed, path, actionType); err != nil {
		if renamed, known := axRenamedFields[strings.TrimPrefix(err.path, path+".")]; known && renamed != "" {
			err.hint = axActionShapeHint(object)
		}
		return action, err.withRule(axRuleFieldForType)
	}
	var err *axArgError
	refPattern := axElementRefPattern
	refShape := "rN"
	if actionType == "activate" || actionType == "close" {
		refPattern, refShape = axWindowRefPattern, "wN"
	}
	if action.targetRef, err = axOptionalPattern(object, "ref", path, refPattern, refShape); err != nil {
		err.rule = axRuleRefKind
		if actionType != "activate" && actionType != "close" && axWindowRefPattern.MatchString(optionalString(object, "ref")) {
			err.message = actionType + " takes an element ref rN; wN is only for activate and close"
		}
		if (actionType == "activate" || actionType == "close") && axElementRefPattern.MatchString(optionalString(object, "ref")) {
			err.message = actionType + " takes a window ref wN or a title; rN is an element"
			err.hint = `{"type":"` + actionType + `","title":"window title"}`
		}
		return action, err
	}
	switch actionType {
	case "click", "set_value":
		if action.targetRef == "" {
			return action, axInvalid(path+".ref", actionType+" needs ref: an rN copied from observe or find",
				`{"type":"`+actionType+`","ref":"r1"`+map[bool]string{true: `,"value":"..."`, false: ""}[actionType == "set_value"]+`}`).withRule(axRuleRequiredForType)
		}
		if actionType == "set_value" {
			raw, present := object["value"]
			value, ok := raw.(string)
			if !present || !ok {
				return action, axInvalid(path+".value", "set_value needs value as a string",
					`{"type":"set_value","ref":"`+action.targetRef+`","value":"..."}`).withRule(axRuleRequiredForType)
			}
			if len([]rune(value)) > maxActionText {
				return action, axInvalid(path+".value", fmt.Sprintf("value is limited to %d characters", maxActionText), "")
			}
			action.value = value
		}
	case "type":
		raw, present := object["text"]
		text, ok := raw.(string)
		if !present || !ok || text == "" {
			return action, axInvalid(path+".text", "type needs non-empty text", `{"type":"type","text":"hello"}`).withRule(axRuleRequiredForType)
		}
		if len([]rune(text)) > maxActionText {
			return action, axInvalid(path+".text", fmt.Sprintf("text is limited to %d characters", maxActionText), "")
		}
		action.text = text
	case "keypress":
		key, err := decodeAXKeys(object, path)
		if err != nil {
			return action, err.withRule(axRuleRequiredForType)
		}
		action.key = key
	case "scroll":
		raw, present := object["direction"]
		direction, ok := raw.(string)
		if !present || !ok || !containsString([]string{"up", "down", "left", "right"}, direction) {
			return action, axInvalid(path+".direction", "scroll needs direction: up, down, left, or right",
				`{"type":"scroll","direction":"down"}`).withRule(axRuleRequiredForType)
		}
		action.direction = direction
		if raw, present := object["pages"]; present {
			pages, ok := axNumber(raw)
			if !ok || pages <= 0 || pages > maxScrollPages {
				return action, axInvalid(path+".pages", fmt.Sprintf("pages must be a number greater than 0 and at most %d", maxScrollPages), "")
			}
			action.pages = pages
		}
	case "activate", "close":
		title, err := axOptionalString(object, "title", path, true)
		if err != nil {
			return action, err
		}
		if (title == "") == (action.targetRef == "") {
			return action, axInvalid(path, actionType+" needs exactly one of ref (wN) or title",
				`{"type":"`+actionType+`","title":"window title"}`).withRule(axRuleOneOf)
		}
		action.title = title
	}
	if raw, present := object["until"]; present {
		if action.postcondition, err = decodeAXPredicate(raw, path+".until"); err != nil {
			return action, err
		}
	}
	return action, nil
}

func decodeAXKeys(object map[string]any, path string) (string, *axArgError) {
	hint := `{"type":"keypress","keys":["CTRL","S"]}`
	raw, present := object["keys"]
	items, ok := raw.([]any)
	if !present || !ok {
		if text, isText := raw.(string); isText && text != "" {
			hint = `{"type":"keypress","keys":` + compactJSON(strings.Split(text, "+")) + `}`
		}
		return "", axInvalid(path+".keys", "keypress needs keys: an array naming one chord", hint)
	}
	if len(items) == 0 || len(items) > guiMaxKeys {
		return "", axInvalid(path+".keys", fmt.Sprintf("keys must name 1 to %d keys", guiMaxKeys), hint)
	}
	keys := make([]string, 0, len(items))
	for index, item := range items {
		key, ok := item.(string)
		if !ok || strings.TrimSpace(key) == "" || key != strings.TrimSpace(key) {
			return "", axInvalid(fmt.Sprintf("%s.keys[%d]", path, index), "each key must be a key name without surrounding spaces", hint)
		}
		if len([]rune(key)) > guiMaxKeyLength {
			return "", axInvalid(fmt.Sprintf("%s.keys[%d]", path, index), fmt.Sprintf("a key name is limited to %d characters", guiMaxKeyLength), "")
		}
		keys = append(keys, key)
	}
	return strings.Join(keys, "+"), nil
}

// decodeAXPredicate converts a public until predicate into the internal
// assertion object that the polling code evaluates.
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
	if !containsString(kinds, kind) {
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
			return nil, axInvalid(path+".negate", "negate must be true or false", "")
		}
		negate = value
	}
	assertion := map[string]any{}
	if rawTimeout, present := object["timeout_ms"]; present {
		timeout, ok := axInteger(rawTimeout)
		if !ok || timeout < 1 || timeout > maxVerifyTimeout {
			return nil, axInvalid(path+".timeout_ms", fmt.Sprintf("timeout_ms must be an integer from 1 to %d", maxVerifyTimeout), "")
		}
		assertion["timeout_ms"] = timeout
	}
	if rawStable, present := object["stable_ms"]; present {
		stable, ok := axInteger(rawStable)
		if !ok || stable < 0 || stable > axMaxUntilStableMS {
			return nil, axInvalid(path+".stable_ms", fmt.Sprintf("stable_ms must be an integer from 0 to %d", axMaxUntilStableMS), "")
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
				return nil, axInvalid(path+".match", `match must be "equals" or "contains"`, "")
			}
			if _, isString := value.(string); match == "contains" && !isString {
				return nil, axInvalid(path+".match", `match "contains" needs a string value`, "").withRule(axRuleValueMatch)
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
		return nil, axInvalid(path, "until type "+strconv.Quote(kind)+" needs exactly one of ref (rN) or selector",
			`{"type":"`+kind+`","ref":"r1"}`).withRule(axRuleOneOf)
	}
	if hasRef {
		text, ok := ref.(string)
		if !ok || !axElementRefPattern.MatchString(text) {
			message := "ref must be an element ref rN copied from observe or find"
			if ok && axWindowRefPattern.MatchString(text) {
				message = `ref must be an element rN; for a window use {"type":"title","text":"..."}`
			}
			return nil, axInvalid(path+".ref", message, "")
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
			return nil, axInvalid(path+".selector."+field, field+" must be a non-blank string", "")
		}
		if field == "state" && !publicSelectorStateWords[text] {
			return nil, axInvalid(path+".selector.state", "selector state must be checked, expanded, selected, or focused", "")
		}
	}
	if _, err := a11ySelectorAddress(fields); err != nil {
		return nil, axInvalid(path+".selector", "selector "+err.Error(), `{"role":"dialog","name":"Print"}`)
	}
	return map[string]any{"selector": fields}, nil
}

// axActionShapeHint rewrites an action written with another interface's
// field names into the current shape, e.g. {"ref":"r1","action":"press"}
// becomes {"type":"click","ref":"r1"}.
func axActionShapeHint(object map[string]any) string {
	ref, _ := object["ref"].(string)
	if target, ok := object["target"].(map[string]any); ok && ref == "" {
		ref, _ = target["ref"].(string)
	} else if target, ok := object["target"].(string); ok && ref == "" {
		ref = target
	}
	verb, _ := object["type"].(string)
	if verb == "" {
		verb, _ = object["action"].(string)
	}
	if synonym, known := axActionVerbSynonyms[verb]; known {
		verb = synonym
	}
	if !containsString(axActionTypes, verb) {
		switch {
		case axWindowRefPattern.MatchString(ref) && strings.Contains(strings.ToLower(verb), "close"):
			verb = "close"
		case axWindowRefPattern.MatchString(ref):
			verb = "activate"
		case axElementRefPattern.MatchString(ref):
			verb = "click"
		case object["keys"] != nil || object["key"] != nil:
			verb = "keypress"
		case object["text"] != nil:
			verb = "type"
		default:
			return ""
		}
	}
	hint := map[string]any{"type": verb}
	if ref != "" {
		hint["ref"] = ref
	}
	for _, field := range axActionFields[verb] {
		if field == "ref" {
			continue
		}
		if value, present := object[field]; present {
			hint[field] = value
		}
	}
	if verb == "set_value" && hint["value"] == nil {
		if text, ok := object["text"].(string); ok {
			hint["value"] = text
		}
	}
	if verb == "keypress" && hint["keys"] == nil {
		if key, ok := object["key"].(string); ok && key != "" {
			hint["keys"] = strings.Split(key, "+")
		}
	}
	return orderedJSON(hint, []string{"type", "ref"})
}

// axActionVerbSynonyms are verbs models wrote for AX actions in recorded
// runs. They only shape the corrected-call hint; they are never accepted.
var axActionVerbSynonyms = map[string]string{
	"press":        "click",
	"invoke":       "click",
	"set_text":     "set_value",
	"close_window": "close",
	"focus":        "activate",
	"key":          "keypress",
	"press_key":    "keypress",
	"type_text":    "type",
}

// axCheckFields rejects fields outside allowed. A field renamed from an
// earlier interface names its replacement.
func axCheckFields(object map[string]any, allowed []string, path, owner string) *axArgError {
	unknown := []string{}
	for field := range object {
		if !containsString(allowed, field) {
			unknown = append(unknown, field)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	field := unknown[0]
	fieldPath := field
	if path != "" {
		fieldPath = path + "." + field
	}
	if replacement, renamed := axRenamedFields[field]; renamed && containsString(allowed, replacement) {
		return axInvalid(fieldPath, strconv.Quote(field)+" is not a field here; use "+strconv.Quote(replacement), "")
	}
	if field == "observation_target" {
		return axInvalid(fieldPath, `observation_target was removed; list windows with mode "apps"`, `{"mode":"apps"}`)
	}
	return axInvalid(fieldPath, owner+" does not accept "+strconv.Quote(field)+
		"; accepted: "+strings.Join(allowed, ", "), "")
}

func axOptionalString(object map[string]any, field, path string, nonBlank bool) (string, *axArgError) {
	raw, present := object[field]
	if !present {
		return "", nil
	}
	text, ok := raw.(string)
	if !ok || (nonBlank && strings.TrimSpace(text) == "") {
		return "", axInvalid(axJoinPath(path, field), field+" must be a non-blank string", "")
	}
	return text, nil
}

func axRequiredText(object map[string]any, field, path, hint string) (string, *axArgError) {
	text, err := axOptionalString(object, field, path, true)
	if err != nil {
		err.hint = hint
		return "", err
	}
	if text == "" {
		return "", axInvalid(axJoinPath(path, field), field+" is required", hint)
	}
	return text, nil
}

func axOptionalPattern(object map[string]any, field, path string, pattern *regexp.Regexp, shape string) (string, *axArgError) {
	raw, present := object[field]
	if !present {
		return "", nil
	}
	text, ok := raw.(string)
	if !ok || !pattern.MatchString(text) {
		return "", axInvalid(axJoinPath(path, field), field+" must be "+shape+
			", copied exactly from an earlier result; got "+compactJSON(raw), "")
	}
	return text, nil
}

func axOptionalEnum(object map[string]any, field, path string, values []string) (string, *axArgError) {
	raw, present := object[field]
	if !present {
		return "", nil
	}
	text, ok := raw.(string)
	if !ok || !containsString(values, text) {
		return "", axInvalid(axJoinPath(path, field), field+" must be one of "+strings.Join(values, ", ")+
			"; got "+compactJSON(raw), "")
	}
	return text, nil
}

func axJoinPath(path, field string) string {
	if path == "" {
		return field
	}
	return path + "." + field
}

func axInteger(raw any) (int, bool) {
	switch typed := raw.(type) {
	case int:
		return typed, true
	case float64:
		if typed != math.Trunc(typed) || math.Abs(typed) > 1e9 {
			return 0, false
		}
		return int(typed), true
	case json.Number:
		// JSON Schema treats 1.0 as an integer, so the decoder does too.
		value, err := typed.Float64()
		if err != nil || value != math.Trunc(value) || math.Abs(value) > 1e9 {
			return 0, false
		}
		return int(value), true
	}
	return 0, false
}

func axNumber(raw any) (float64, bool) {
	switch typed := raw.(type) {
	case int:
		return float64(typed), true
	case float64:
		return typed, !math.IsNaN(typed) && !math.IsInf(typed, 0)
	case json.Number:
		value, err := typed.Float64()
		return value, err == nil && !math.IsNaN(value) && !math.IsInf(value, 0)
	}
	return 0, false
}

func axUntilTarget(assertion map[string]any) map[string]any {
	target, _ := assertion["target"].(map[string]any)
	return target
}

// axArgErrorResult renders a rejected call. Nothing was sent.
func axArgErrorResult(err *axArgError, requested int) toolCallResult {
	result := textResult(err.message, true)
	result.StructuredContent = map[string]any{
		axPublicErrorCodeKey:   err.code,
		axPublicErrorPathKey:   err.path,
		axPublicErrorHintKey:   err.hint,
		axPublicErrorDetailKey: err.message,
		"argument_error_code":  firstNonEmpty(err.rule, err.code),
		"action_attempted":     false,
		"input_events_emitted": false,
		"preflight_only":       true,
		"evidence_scope":       "error_only",
		"a11y_batch_result": map[string]any{
			"actions_requested":                    requested,
			"actions_completed_without_call_error": 0,
			"rejected_before_first_action":         true,
		},
	}
	return result
}
