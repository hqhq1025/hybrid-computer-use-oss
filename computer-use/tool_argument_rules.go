package main

import (
	"strings"
)

// Tool-specific wording for argument errors. The schema decides what is
// accepted; these rules only replace a generic explanation with the one
// the model can act on directly.

func init() {
	argumentRules[publicToolGUI] = toolArgumentRules{
		before: guiShapeProblem,
		refine: refineGUIArgumentError,
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
