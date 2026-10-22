package core

import (
	"encoding/json"
	"fmt"
	"github.com/hqhq1025/hybrid-computer-use-oss/computer-use/internal/contract"
	"math"
	"strconv"
	"strings"
)

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
