package main

import (
	"errors"
	"strings"
)

// Tool-specific wording for argument errors. The schema decides what is
// accepted; these rules only replace a generic explanation with the one
// the model can act on directly.

func init() {
	argumentRules[publicToolAX] = toolArgumentRules{
		identity: axIdentityProblem,
		refine:   refineAXArgumentError,
		// Verbs models wrote for AX actions in recorded runs; they only
		// shape the corrected-call hint.
		hintSynonyms: map[string]string{
			"press":        "click",
			"set_text":     "set_value",
			"close_window": "close",
			"focus":        "activate",
			"key":          "keypress",
		},
	}
	argumentRules[publicToolGUI] = toolArgumentRules{
		before: guiShapeProblem,
		refine: refineGUIArgumentError,
	}
}

const axRefOwnershipGuidance = "each rN/wN target already owns its application and " +
	"logical window. Copy refs exactly from observe/find. Use top-level " +
	"window_ref only for an all-untargeted keyboard batch"

// axIdentityProblem explains the read and act identity constraints, which
// the schema expresses as a oneOf of required-field sets.
func axIdentityProblem(args map[string]any, valid int) (string, string) {
	mode, _ := args["mode"].(string)
	present := []string{}
	for _, key := range []string{"app", "app_ref", "window_ref"} {
		if _, ok := args[key]; ok {
			present = append(present, key)
		}
	}
	if mode == a11yModeAct {
		if _, ok := args["window_ref"]; ok {
			return "", `mode="act" with window_ref accepts only untargeted keypress, ` +
				`type, and scroll actions; ` + axRefOwnershipGuidance
		}
		return "", "an all-untargeted batch requires window_ref; app names do not prove keyboard focus"
	}
	identity := "one of app, app_ref, or window_ref"
	if mode == a11yModeAssert {
		identity = "one of app, app_ref, window_ref, or an assertion.target rN"
	}
	if len(present) == 0 || valid == 0 && len(present) == 1 {
		return identity, "mode=" + quoteValue(mode) + " requires " + identity
	}
	return "", "mode=" + quoteValue(mode) + " accepts only one of app, app_ref, or window_ref; " +
		"it received " + joinWords(present, "and")
}

func refineAXArgumentError(args map[string]any, found *explanation) {
	mode, _ := args["mode"].(string)
	switch {
	case mode == a11yModeAct && found.keyword == "additionalProperties" && len(found.path) == 0 &&
		(strings.Contains(found.message, `"app"`) || strings.Contains(found.message, `"app_ref"`)):
		found.message += "; " + axRefOwnershipGuidance
		found.accepted = ""
	case mode == a11yModeAct && jsonPointer(found.path) == "/observe":
		found.message = `mode="act" observe must be none, diff, or screenshot. ` +
			"No action was executed. Retry this same act with observe omitted " +
			"or set to diff; do not add a separate observe first."
		found.hint = ""
	case mode == a11yModeAct && jsonPointer(found.path) == "/actions" &&
		found.keyword == "minItems":
		found.message = `mode="act" requires at least one action; use mode="observe" ` +
			"for a read-only observation"
	case mode == a11yModeFind && jsonPointer(found.path) == "/query" &&
		found.keyword == "additionalProperties":
		found.message += "; use name_equals/name_contains or text_equals/text_contains"
		found.accepted = ""
	}
}

// guiShapeProblem keeps the GUI observation rules readable: the schema
// splits a screenshot observation and an input batch into two branches,
// and the generic "exactly 1 item" message would hide why.
func guiShapeProblem(args map[string]any) *explanation {
	actions, ok := args["actions"].([]any)
	if !ok {
		if _, present := args["actions"]; !present {
			return &explanation{
				keyword: "required",
				message: `GUI requires actions; use {"actions":[{"type":"screenshot"}]} for an immediate observation`,
			}
		}
		return nil
	}
	if len(actions) == 0 {
		return &explanation{
			path:    []string{"actions"},
			keyword: "minItems",
			message: "GUI actions must not be empty; use one screenshot action for observation",
		}
	}
	screenshots := 0
	for _, item := range actions {
		if action, ok := item.(map[string]any); ok && action["type"] == "screenshot" {
			screenshots++
		}
	}
	if screenshots == 0 {
		return nil
	}
	if len(actions) > 1 {
		return &explanation{
			path:    []string{"actions"},
			keyword: "oneOf",
			message: "GUI screenshot must be the only action because no intermediate screenshot is returned",
		}
	}
	if _, present := args["settle_ms"]; present {
		return &explanation{
			path:    []string{"settle_ms"},
			keyword: "additionalProperties",
			message: "GUI screenshot observation does not accept settle_ms",
		}
	}
	return nil
}

func refineGUIArgumentError(args map[string]any, found *explanation) {
	if found.keyword == "additionalProperties" && len(found.path) == 0 {
		found.message = strings.Replace(found.message, "GUI does not accept fields:",
			"GUI accepts only actions and optional settle_ms; unsupported field(s):", 1)
		found.accepted = ""
	}
}

func quoteValue(value string) string {
	return compactJSON(value)
}

// Cross-field rules that no schema keyword expresses. The runtime enforces
// them after the schema gate, before any input, and marks the rejection with
// its rule code so the consistency test can tell a declared rule from drift.
const (
	argumentRuleCloseNotLast       = "close_not_last"
	argumentRuleTextBudget         = "text_budget"
	argumentRulePostconditionIndex = "postcondition_index_target"
	argumentRuleRefOwnsIdentity    = "ref_owns_identity"
	argumentRuleWaitBudget         = "wait_budget"
)

// argumentRuleError is a cross-field rule violation.
type argumentRuleError struct {
	code    string
	message string
}

func (e *argumentRuleError) Error() string { return e.message }

func newArgumentRuleError(code, message string) error {
	return &argumentRuleError{code: code, message: message}
}

// withArgumentRule records the rule code of err on result, if err is a rule
// violation; the code is internal evidence.
func withArgumentRule(result toolCallResult, err error) toolCallResult {
	var rule *argumentRuleError
	if !errors.As(err, &rule) {
		return result
	}
	if result.StructuredContent == nil {
		result.StructuredContent = map[string]any{}
	}
	result.StructuredContent["argument_error_code"] = rule.code
	return result
}
