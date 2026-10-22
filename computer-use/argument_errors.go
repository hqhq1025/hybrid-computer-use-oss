package main

import (
	"errors"
	"strings"
)

// argumentErrorResult renders a rejected call like the tool's other preflight
// errors. An AX act rejection keeps the batch summary, which states that no
// action ran; a rejected top-level field keeps the plain field error.
func argumentErrorResult(tool string, args map[string]any, argErr *argumentError) toolCallResult {
	if tool == publicToolAX {
		requested := 0
		if actions, ok := args["actions"].([]any); ok {
			requested = len(actions)
		}
		path := strings.ReplaceAll(strings.TrimPrefix(argErr.Path, "/"), "/", ".")
		rejected := axInvalid(path, argErr.Message, "")
		// The AX decoder runs first and rejects what the schema rejects, so
		// this is a backstop; its hint is the minimal call for the mode.
		rejected.hint = axDerivedHint(args, rejected)
		result := axArgErrorResult(rejected, requested)
		result.StructuredContent["argument_error_code"] = argErr.Code
		result.StructuredContent["argument_error_path"] = argErr.Path
		return result
	}
	result := textResult(argErr.Message, true).withEvidenceScope("error_only")
	if tool == publicToolGUI {
		path := guiErrorPath(guiArgumentPath(argErr.Path), argErr.Message)
		if _, present := args["actions"]; !present && path == "" {
			path = "actions"
		}
		setGUIPublicError(&result, axCodeInvalidArgument, path, argErr.Message, guiArgumentHint(args, path))
		result = withGUIRequested(result, args["actions"])
	}
	result.StructuredContent["argument_error_code"] = argErr.Code
	result.StructuredContent["argument_error_path"] = argErr.Path
	return result
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
