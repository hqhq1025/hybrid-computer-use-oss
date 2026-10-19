package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The published input schema of each public tool is the contract for its
// arguments. validateToolArguments enforces it before any request reaches a
// tool service, so input that tools/list rejects is never acted on and
// input it accepts is never refused for static shape reasons. Rules that
// depend on live state (ref ownership, window continuity) or that span
// several fields without a schema keyword (close must be last, text
// budgets) stay in the services.

const (
	argumentErrorInvalid = "invalid_argument"

	// maxArgumentHintBytes bounds the corrected-call example appended to an
	// error, so a hint never repeats a long text argument back to the model.
	maxArgumentHintBytes = 300
)

// argumentError reports the first problem found in tool arguments. Message
// is model-visible. Path is the JSON Pointer of the offending value and Code
// classifies the rejection; both stay internal.
type argumentError struct {
	Code    string
	Path    string
	Message string
}

func (e *argumentError) Error() string { return e.Message }

var publicInputSchemas struct {
	once    sync.Once
	schemas map[string]*jsonschema.Schema
	err     error
}

// compiledInputSchema returns the compiled published input schema of a
// public tool, or nil for a name without one. All schemas are compiled once,
// on first use.
func compiledInputSchema(tool string) (*jsonschema.Schema, error) {
	publicInputSchemas.once.Do(func() {
		publicInputSchemas.schemas, publicInputSchemas.err =
			compileInputSchemas(allToolDefinitions())
	})
	if publicInputSchemas.err != nil {
		return nil, publicInputSchemas.err
	}
	return publicInputSchemas.schemas[tool], nil
}

func compileInputSchemas(tools []toolDefinition) (map[string]*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiled := make(map[string]*jsonschema.Schema, len(tools))
	for _, tool := range tools {
		encoded, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("encode %s input schema: %w", tool.Name, err)
		}
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
		if err != nil {
			return nil, fmt.Errorf("decode %s input schema: %w", tool.Name, err)
		}
		location := "schema://computer-use/" + tool.Name
		if err := compiler.AddResource(location, document); err != nil {
			return nil, fmt.Errorf("register %s input schema: %w", tool.Name, err)
		}
		schema, err := compiler.Compile(location)
		if err != nil {
			return nil, fmt.Errorf("compile %s input schema: %w", tool.Name, err)
		}
		compiled[tool.Name] = schema
	}
	return compiled, nil
}

// validateToolArguments checks arguments against the published input schema
// of a public tool. It returns nil for valid arguments and for a name that
// has no published schema; the dispatcher rejects unknown names itself.
func validateToolArguments(tool string, args map[string]any) *argumentError {
	schema, err := compiledInputSchema(tool)
	if err != nil {
		// A schema that does not compile is a build defect. Fail closed: no
		// call may run unvalidated.
		return &argumentError{
			Code:    argumentErrorInvalid,
			Message: tool + " input schema is unavailable: " + err.Error(),
		}
	}
	if schema == nil {
		return nil
	}
	instance := jsonInstance(args)
	if schema.Validate(instance) == nil {
		return nil
	}
	object, _ := instance.(map[string]any)
	explainer := schemaExplainer{tool: tool, args: object, rules: argumentRules[tool]}
	found := explainer.explainRoot(schema, object)
	return &argumentError{
		Code:    argumentErrorInvalid,
		Path:    jsonPointer(found.path),
		Message: found.text(),
	}
}

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

// jsonInstance returns args in the representation the validator accepts.
// Arguments decoded from JSON already have it; values built in Go (typed
// slices, structs) are normalized through a JSON round trip.
func jsonInstance(args map[string]any) any {
	if isJSONValue(args) {
		return args
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return args
	}
	decoded, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		return args
	}
	return decoded
}

func isJSONValue(value any) bool {
	switch typed := value.(type) {
	case nil, bool, string, json.Number, float64, int:
		return true
	case map[string]any:
		for _, item := range typed {
			if !isJSONValue(item) {
				return false
			}
		}
		return true
	case []any:
		for _, item := range typed {
			if !isJSONValue(item) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// toolArgumentRules words errors that a schema keyword alone cannot explain
// well. Each hook runs only after the schema has rejected the arguments, so
// it can change the message but never what is accepted.
type toolArgumentRules struct {
	// before explains cross-branch problems ahead of the generic walk.
	before func(args map[string]any) *explanation
	// identity explains a failed top-level oneOf whose branches are
	// constraints rather than shapes, such as "exactly one identity field".
	// valid counts the matching branches. need is a noun phrase that can
	// follow "requires" ("one of app, app_ref, or window_ref"), or "" when
	// the problem is a conflict; message is the complete sentence.
	identity func(args map[string]any, valid int) (need, message string)
	// refine may rewrite the generic explanation.
	refine func(args map[string]any, found *explanation)
	// hintSynonyms maps verbs callers write to the published const value
	// they mean ("press" to "click"). They are used only to build a
	// corrected-call hint and never make an argument acceptable.
	hintSynonyms map[string]string
}

var argumentRules = map[string]toolArgumentRules{}

// explanation is one schema problem at an instance location.
type explanation struct {
	path    []string
	keyword string
	message string
	hint    string
	// accepted lists the fields an object declares; it is shown when no
	// corrected call could be built.
	accepted string
	// wrongBranch marks a const mismatch: the instance belongs to another
	// oneOf branch, so this branch's explanation is a poor choice.
	wrongBranch bool
}

func (e *explanation) text() string {
	switch {
	case e.hint != "":
		return e.message + "; use " + e.hint
	case e.accepted != "":
		return e.message + "; accepted fields: " + e.accepted
	default:
		return e.message
	}
}

type schemaExplainer struct {
	tool  string
	args  map[string]any
	rules toolArgumentRules
}

func (x *schemaExplainer) explainRoot(schema *jsonschema.Schema, args map[string]any) *explanation {
	var found *explanation
	if x.rules.before != nil {
		found = x.rules.before(args)
	}
	if found == nil {
		found = x.explain(schema, args, nil)
	}
	if found == nil {
		found = &explanation{message: x.tool + " arguments do not match the published input schema"}
	}
	if x.rules.refine != nil {
		x.rules.refine(args, found)
	}
	return found
}

// explain returns the first problem of value against schema, or nil when the
// value is valid. Object problems are reported in the order a caller fixes
// them: shape branch, unknown fields, missing fields, field values, then
// cross-field constraints.
func (x *schemaExplainer) explain(schema *jsonschema.Schema, value any, path []string) *explanation {
	if schema == nil || schema.Validate(value) == nil {
		return nil
	}
	if schema.Ref != nil {
		if found := x.explain(schema.Ref, value, path); found != nil {
			return found
		}
	}
	if schema.Types != nil && !typeAllowed(schema.Types.ToStrings(), value) {
		return x.typeMismatch(schema, value, path)
	}
	if schema.Const != nil && !jsonEqual(*schema.Const, value) {
		return valueMismatch(path, []any{*schema.Const}, value, true)
	}
	if schema.Enum != nil && !containsJSON(schema.Enum.Values, value) {
		return valueMismatch(path, schema.Enum.Values, value, false)
	}
	var found *explanation
	switch typed := value.(type) {
	case map[string]any:
		found = x.explainObject(schema, typed, path)
	case []any:
		found = x.explainArray(schema, typed, path)
	case string:
		found = explainString(schema, typed, path)
	default:
		found = explainNumber(schema, value, path)
	}
	if found != nil {
		return found
	}
	if found := x.explainCombinators(schema, value, path); found != nil {
		return found
	}
	return &explanation{
		path:    path,
		keyword: "schema",
		message: pathLabel(path, x.tool) + " does not match the published schema",
	}
}

func (x *schemaExplainer) explainObject(schema *jsonschema.Schema, object map[string]any, path []string) *explanation {
	if len(schema.OneOf) > 0 && shapeBranches(schema.OneOf) {
		if found := x.explainShapeOneOf(schema, object, path); found != nil {
			return found
		}
	}
	label := x.objectLabel(schema, object, path)
	missing := []string{}
	for _, key := range schema.Required {
		if _, present := object[key]; !present {
			missing = append(missing, key)
		}
	}
	if closed, ok := schema.AdditionalProperties.(bool); ok && !closed {
		unknown := []string{}
		for key := range object {
			if _, declared := schema.Properties[key]; !declared {
				unknown = append(unknown, key)
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			quoted := make([]string, len(unknown))
			for index, key := range unknown {
				quoted[index] = strconv.Quote(key)
			}
			found := &explanation{
				path:     path,
				keyword:  "additionalProperties",
				message:  label + " does not accept fields: " + strings.Join(quoted, ", "),
				accepted: strings.Join(sortedKeys(schema.Properties), ", "),
			}
			if len(missing) > 0 {
				found.message += "; it requires " + joinWords(missing, "and")
				found.accepted = ""
			}
			return found
		}
	}
	// A missing identity counts as a missing field; both are reported
	// together because the caller fixes them in one retry.
	need, conflict := x.identityProblem(schema, object, path)
	if need != "" {
		missing = append(missing, need)
	}
	if len(missing) > 0 {
		message := label + " requires " + joinWords(missing, "and")
		if conflict != "" && need == "" {
			message += "; " + conflict
		}
		return &explanation{path: path, keyword: "required", message: message}
	}
	// Scalar fields are explained before nested ones: they are cheaper to
	// fix, and a nested problem is often a consequence of the same mistake.
	keys := sortedKeys(object)
	sort.SliceStable(keys, func(i, j int) bool {
		return !isContainer(object[keys[i]]) && isContainer(object[keys[j]])
	})
	for _, key := range keys {
		if property := schema.Properties[key]; property != nil {
			if found := x.explain(property, object[key], appendPath(path, key)); found != nil {
				return found
			}
		}
	}
	if schema.MinProperties != nil && len(object) < *schema.MinProperties {
		return &explanation{
			path:    path,
			keyword: "minProperties",
			message: fmt.Sprintf("%s must contain at least %d field(s)", label, *schema.MinProperties),
		}
	}
	return nil
}

// identityProblem asks the tool rules to explain a failed top-level
// constraint oneOf. It returns empty strings when no rule applies.
func (x *schemaExplainer) identityProblem(schema *jsonschema.Schema, object map[string]any, path []string) (string, string) {
	if len(path) != 0 || x.rules.identity == nil || len(schema.OneOf) == 0 ||
		shapeBranches(schema.OneOf) || oneOfHolds(schema.OneOf, object) {
		return "", ""
	}
	return x.rules.identity(x.args, countValid(schema.OneOf, object))
}

func (x *schemaExplainer) explainArray(schema *jsonschema.Schema, items []any, path []string) *explanation {
	label := pathLabel(path, x.tool)
	minimum, maximum := -1, -1
	if schema.MinItems != nil {
		minimum = *schema.MinItems
	}
	if schema.MaxItems != nil {
		maximum = *schema.MaxItems
	}
	if (minimum >= 0 && len(items) < minimum) || (maximum >= 0 && len(items) > maximum) {
		keyword := "maxItems"
		if minimum >= 0 && len(items) < minimum {
			keyword = "minItems"
		}
		return &explanation{
			path:    path,
			keyword: keyword,
			message: label + " must contain " + countRange(minimum, maximum, "item"),
		}
	}
	if itemSchema := schema.Items2020; itemSchema != nil {
		for index, item := range items {
			if found := x.explain(itemSchema, item, appendPath(path, strconv.Itoa(index))); found != nil {
				return found
			}
		}
	}
	return nil
}

func explainString(schema *jsonschema.Schema, value string, path []string) *explanation {
	label := pathLabel(path, "")
	length := len([]rune(value))
	if schema.MinLength != nil && length < *schema.MinLength {
		message := label + " must not be empty"
		if *schema.MinLength > 1 {
			message = fmt.Sprintf("%s must have at least %d characters", label, *schema.MinLength)
		}
		return &explanation{path: path, keyword: "minLength", message: message}
	}
	if schema.MaxLength != nil && length > *schema.MaxLength {
		return &explanation{
			path:    path,
			keyword: "maxLength",
			message: fmt.Sprintf("%s accepts at most %d characters", label, *schema.MaxLength),
		}
	}
	if schema.Pattern != nil && !schema.Pattern.MatchString(value) {
		return &explanation{
			path:    path,
			keyword: "pattern",
			message: fmt.Sprintf("%s %s must be %s", label, strconv.Quote(value),
				patternDescription(schema.Pattern.String())),
		}
	}
	return nil
}

func explainNumber(schema *jsonschema.Schema, value any, path []string) *explanation {
	number, ok := jsonNumberRat(value)
	if !ok {
		return nil
	}
	low, high := "", ""
	failed := false
	if schema.Minimum != nil {
		low = "at least " + schema.Minimum.RatString()
		failed = failed || number.Cmp(schema.Minimum) < 0
	}
	if schema.ExclusiveMinimum != nil {
		low = "greater than " + schema.ExclusiveMinimum.RatString()
		failed = failed || number.Cmp(schema.ExclusiveMinimum) <= 0
	}
	if schema.Maximum != nil {
		high = "at most " + schema.Maximum.RatString()
		failed = failed || number.Cmp(schema.Maximum) > 0
	}
	if schema.ExclusiveMaximum != nil {
		high = "less than " + schema.ExclusiveMaximum.RatString()
		failed = failed || number.Cmp(schema.ExclusiveMaximum) >= 0
	}
	if !failed {
		return nil
	}
	bounds := low
	switch {
	case low == "":
		bounds = high
	case high != "" && strings.HasPrefix(low, "at least ") && strings.HasPrefix(high, "at most "):
		bounds = "between " + strings.TrimPrefix(low, "at least ") + " and " +
			strings.TrimPrefix(high, "at most ")
	case high != "":
		bounds = low + " and " + high
	}
	return &explanation{
		path:    path,
		keyword: "range",
		message: pathLabel(path, "") + " must be " + bounds,
	}
}

func (x *schemaExplainer) explainCombinators(schema *jsonschema.Schema, value any, path []string) *explanation {
	object, isObject := value.(map[string]any)
	label := pathLabel(path, x.tool)
	if isObject {
		label = x.objectLabel(schema, object, path)
	}
	if len(schema.OneOf) > 0 && !shapeBranches(schema.OneOf) && !oneOfHolds(schema.OneOf, value) {
		valid := countValid(schema.OneOf, value)
		if isObject {
			if _, message := x.identityProblem(schema, object, path); message != "" {
				return &explanation{path: path, keyword: "oneOf", message: message}
			}
		}
		keys := requiredAlternatives(schema.OneOf)
		message := label + " requires one of " + joinWords(keys, "or")
		if valid > 1 {
			message = label + " accepts only one of " + joinWords(keys, "or")
		}
		return &explanation{path: path, keyword: "oneOf", message: message}
	}
	if len(schema.AnyOf) > 0 && countValid(schema.AnyOf, value) == 0 {
		keys := []string{}
		single := true
		for _, branch := range schema.AnyOf {
			required := resolved(branch).Required
			if len(required) != 1 {
				single = false
				break
			}
			keys = append(keys, required[0])
		}
		if single && isObject {
			for index, key := range keys {
				if _, present := object[key]; present {
					return x.explain(schema.AnyOf[index], value, path)
				}
			}
			return &explanation{
				path:    path,
				keyword: "anyOf",
				message: label + " requires at least one of " + joinWords(keys, "or"),
			}
		}
		return x.explain(schema.AnyOf[0], value, path)
	}
	for _, branch := range schema.AllOf {
		if branch.Validate(value) == nil {
			continue
		}
		if not := resolved(branch).Not; not != nil && len(resolved(not).Required) > 1 {
			return &explanation{
				path:    path,
				keyword: "not",
				message: label + " accepts only one of " + joinWords(resolved(not).Required, "or"),
			}
		}
		return x.explain(branch, value, path)
	}
	if schema.Not != nil && schema.Not.Validate(value) == nil {
		not := resolved(schema.Not)
		if not.Const != nil {
			return &explanation{
				path:    path,
				keyword: "not",
				message: label + " must not be " + compactJSON(*not.Const),
			}
		}
		return &explanation{
			path:    path,
			keyword: "not",
			message: label + " uses a combination of fields the schema forbids",
		}
	}
	if schema.If != nil && schema.If.Validate(value) == nil && schema.Then != nil {
		return x.explain(schema.Then, value, path)
	}
	return nil
}

// explainShapeOneOf picks the branch the caller meant and explains it. A
// branch is chosen by its const properties (mode, type); without them, by
// which branch's problem is not a const mismatch and declares the most of
// the caller's fields.
func (x *schemaExplainer) explainShapeOneOf(schema *jsonschema.Schema, object map[string]any, path []string) *explanation {
	branches := schema.OneOf
	valid := countValid(branches, object)
	if valid == 1 {
		return nil
	}
	if valid > 1 {
		keys := requiredAlternatives(branches)
		return &explanation{
			path:    path,
			keyword: "oneOf",
			message: pathLabel(path, x.tool) + " accepts exactly one of " + joinWords(keys, "or"),
		}
	}
	var found *explanation
	if discriminators := constProperties(branches); len(discriminators) > 0 {
		if missing := missingDiscriminator(branches, discriminators, object, path); missing != nil {
			found = missing
		} else if branch := selectByDiscriminator(branches, discriminators, object); branch != nil {
			found = x.explain(branch, object, path)
		} else {
			found = discriminatorMismatch(branches, discriminators, object, path)
		}
	} else {
		found = x.closestBranch(branches, object, path)
	}
	if found != nil && found.hint == "" {
		if repaired, ok := repairObject(schema, object, x.rules.hintSynonyms); ok {
			if hint := orderedJSON(repaired, constPropertyNames(branches)); len(hint) <= maxArgumentHintBytes {
				found.hint = hint
			}
		}
	}
	return found
}

func (x *schemaExplainer) closestBranch(branches []*jsonschema.Schema, object map[string]any, path []string) *explanation {
	var best *explanation
	bestScore := -1 << 30
	for _, branch := range branches {
		found := x.explain(branch, object, path)
		if found == nil {
			continue
		}
		properties := resolved(branch).Properties
		score := 0
		for key := range object {
			if _, declared := properties[key]; declared {
				score += 4
			}
		}
		if found.wrongBranch {
			score -= 1000
		}
		score += len(properties)
		if score > bestScore {
			best, bestScore = found, score
		}
	}
	return best
}

func (x *schemaExplainer) typeMismatch(schema *jsonschema.Schema, value any, path []string) *explanation {
	expected := schema.Types.ToStrings()
	names := typeNames(expected)
	if example := objectExample(schema); example != "" {
		names = "an object like " + example
	}
	return &explanation{
		path:    path,
		keyword: "type",
		message: fmt.Sprintf("%s must be %s, not %s",
			pathLabel(path, x.tool), names, jsonTypeArticle(value)),
	}
}

// objectLabel names an object in messages: its path followed by the const
// values that selected its branch, or mode="act" style at the top level.
func (x *schemaExplainer) objectLabel(schema *jsonschema.Schema, object map[string]any, path []string) string {
	consts := []string{}
	for _, key := range sortedKeys(schema.Properties) {
		property := resolved(schema.Properties[key])
		if property.Const == nil {
			continue
		}
		if value, present := object[key]; present && jsonEqual(*property.Const, value) {
			if len(path) == 0 {
				consts = append(consts, key+"="+compactJSON(value))
			} else {
				consts = append(consts, fmt.Sprint(value))
			}
		}
	}
	if len(consts) == 0 {
		return pathLabel(path, x.tool)
	}
	if len(path) == 0 {
		return strings.Join(consts, " with ")
	}
	return pathLabel(path, x.tool) + " " + strings.Join(consts, " ")
}

func missingDiscriminator(branches []*jsonschema.Schema, discriminators []string, object map[string]any, path []string) *explanation {
	for _, key := range discriminators {
		values := constValues(branches, key)
		if constBranchCount(branches, key) < len(branches) {
			continue
		}
		if _, present := object[key]; present {
			continue
		}
		return &explanation{
			path:    appendPath(path, key),
			keyword: "required",
			message: pathLabel(appendPath(path, key), "") + " is required and must be " +
				joinWords(stringValues(values), "or"),
		}
	}
	return nil
}

func discriminatorMismatch(branches []*jsonschema.Schema, discriminators []string, object map[string]any, path []string) *explanation {
	for _, key := range discriminators {
		values := constValues(branches, key)
		value, present := object[key]
		if !present || containsJSON(values, value) {
			continue
		}
		found := valueMismatch(appendPath(path, key), values, value, false)
		found.wrongBranch = true
		return found
	}
	return &explanation{
		path:        path,
		keyword:     "oneOf",
		message:     pathLabel(path, "") + " combines fields from different forms",
		wrongBranch: true,
	}
}

func valueMismatch(path []string, allowed []any, value any, single bool) *explanation {
	label := pathLabel(path, "")
	found := &explanation{path: path, keyword: "enum", wrongBranch: single}
	text, isString := value.(string)
	switch {
	case single:
		found.keyword = "const"
		found.message = label + " must be " + compactJSON(allowed[0])
	case isString:
		found.message = label + " " + strconv.Quote(text) + " is unsupported; it must be " +
			joinWords(stringValues(allowed), "or")
	default:
		found.message = label + " must be one of " + joinWords(stringValues(allowed), "or")
	}
	if isString {
		for _, candidate := range allowed {
			exact, ok := candidate.(string)
			if ok && exact != text && strings.EqualFold(strings.TrimSpace(text), exact) {
				found.message += " (values are exact and case-sensitive: " + strconv.Quote(exact) + ")"
				break
			}
		}
	}
	return found
}

// repairObject applies the corrections a caller most often needs: a
// discriminator written under another key ({"action":"close"}), a field that
// belongs in a nested object ({"ref":"r1"} instead of {"target":{"ref":"r1"}}),
// a string where a one-field object is required, and case or surrounding
// whitespace in a const value. It reports the corrected object only when the
// result validates.
func repairObject(schema *jsonschema.Schema, object map[string]any, synonyms map[string]string) (map[string]any, bool) {
	branches := schema.OneOf
	discriminators := constProperties(branches)
	candidate := cloneObject(object)
	changed := false
	for round := 0; round < 3; round++ {
		if schema.Validate(candidate) == nil {
			return candidate, changed
		}
		progress := false
		for _, key := range discriminators {
			values := constValues(branches, key)
			if current, present := candidate[key]; present {
				if text, ok := current.(string); ok {
					if mapped := synonyms[text]; mapped != "" && containsJSON(values, mapped) {
						candidate[key] = mapped
						progress = true
						continue
					}
					for _, value := range values {
						exact, _ := value.(string)
						if exact != text && strings.EqualFold(strings.TrimSpace(text), exact) {
							candidate[key] = exact
							progress = true
						}
					}
				}
				continue
			}
			for _, other := range sortedKeys(candidate) {
				if declaredInAnyBranch(branches, other) {
					continue
				}
				text, ok := candidate[other].(string)
				if !ok {
					continue
				}
				if !containsJSON(values, text) && containsJSON(values, synonyms[text]) {
					text = synonyms[text]
				}
				if containsJSON(values, text) {
					candidate[key] = text
					delete(candidate, other)
					progress = true
					break
				}
			}
		}
		branch := selectByDiscriminator(branches, discriminators, candidate)
		if branch == nil {
			if !progress {
				break
			}
			changed = true
			continue
		}
		properties := resolved(branch).Properties
		for _, key := range sortedKeys(candidate) {
			if _, declared := properties[key]; declared {
				if text, ok := candidate[key].(string); ok {
					if wrapped, ok := wrapStringInObject(properties[key], text); ok {
						candidate[key] = wrapped
						progress = true
					}
				}
				continue
			}
			for _, parent := range sortedKeys(properties) {
				if !objectDeclares(properties[parent], key) {
					continue
				}
				nested, _ := candidate[parent].(map[string]any)
				if nested == nil {
					if _, present := candidate[parent]; present {
						continue
					}
					nested = map[string]any{}
				}
				if _, taken := nested[key]; taken {
					continue
				}
				nested = cloneObject(nested)
				nested[key] = candidate[key]
				candidate[parent] = nested
				delete(candidate, key)
				progress = true
				break
			}
		}
		if !progress {
			break
		}
		changed = true
	}
	return candidate, changed && schema.Validate(candidate) == nil
}

// wrapStringInObject turns "r1" into {"ref":"r1"} when the expected object
// has exactly one required field and the string is valid for it.
func wrapStringInObject(schema *jsonschema.Schema, text string) (map[string]any, bool) {
	schema = resolved(schema)
	candidates := []*jsonschema.Schema{schema}
	if len(schema.OneOf) > 0 {
		candidates = schema.OneOf
	}
	for _, candidate := range candidates {
		candidate = resolved(candidate)
		if len(candidate.Required) != 1 {
			continue
		}
		wrapped := map[string]any{candidate.Required[0]: text}
		if candidate.Validate(wrapped) == nil && schema.Validate(wrapped) == nil {
			return wrapped, true
		}
	}
	return nil, false
}

// objectExample renders a one-field object schema as {"ref":"rN"}, using the
// field's pattern for the placeholder.
func objectExample(schema *jsonschema.Schema) string {
	schema = resolved(schema)
	if schema.Types == nil || !containsString(schema.Types.ToStrings(), "object") ||
		len(schema.Required) != 1 {
		return ""
	}
	field := schema.Required[0]
	placeholder := "..."
	if property := schema.Properties[field]; property != nil && resolved(property).Pattern != nil {
		switch pattern := resolved(property).Pattern.String(); {
		case strings.HasPrefix(pattern, "^r"), strings.HasPrefix(pattern, "^(r"),
			strings.HasPrefix(pattern, "^(?:r"):
			placeholder = "rN"
		case strings.HasPrefix(pattern, "^w"):
			placeholder = "wN"
		case strings.HasPrefix(pattern, "^a"):
			placeholder = "aN"
		}
	}
	return "{" + strconv.Quote(field) + ":" + strconv.Quote(placeholder) + "}"
}

func objectDeclares(schema *jsonschema.Schema, key string) bool {
	schema = resolved(schema)
	if _, declared := schema.Properties[key]; declared {
		return true
	}
	for _, branch := range schema.OneOf {
		if _, declared := resolved(branch).Properties[key]; declared {
			return true
		}
	}
	return false
}

func declaredInAnyBranch(branches []*jsonschema.Schema, key string) bool {
	for _, branch := range branches {
		if _, declared := resolved(branch).Properties[key]; declared {
			return true
		}
	}
	return false
}

// selectByDiscriminator returns the branch whose const properties agree with
// the object, preferring branches whose consts are all present, then the
// most matched consts, then the most present required and declared fields.
func selectByDiscriminator(branches []*jsonschema.Schema, discriminators []string, object map[string]any) *jsonschema.Schema {
	var best *jsonschema.Schema
	bestScore := -1
	for _, branch := range branches {
		properties := resolved(branch).Properties
		compatible := true
		complete := 1
		matched := 0
		for _, key := range discriminators {
			property := properties[key]
			if property == nil || resolved(property).Const == nil {
				continue
			}
			value, present := object[key]
			if !present {
				if containsString(resolved(branch).Required, key) {
					complete = 0
				}
				continue
			}
			if !jsonEqual(*resolved(property).Const, value) {
				compatible = false
				break
			}
			matched++
		}
		if !compatible {
			continue
		}
		required, declared := 0, 0
		for _, key := range resolved(branch).Required {
			if _, present := object[key]; present {
				required++
			}
		}
		for key := range object {
			if _, ok := properties[key]; ok {
				declared++
			}
		}
		score := complete<<24 | matched<<16 | required<<8 | declared
		if score > bestScore {
			best, bestScore = branch, score
		}
	}
	return best
}

// constProperties lists the property names that carry a const in at least
// two branches; those are the fields that select a branch.
func constProperties(branches []*jsonschema.Schema) []string {
	counts := map[string]int{}
	for _, branch := range branches {
		for key, property := range resolved(branch).Properties {
			if resolved(property).Const != nil {
				counts[key]++
			}
		}
	}
	keys := []string{}
	for key, count := range counts {
		if count >= 2 {
			keys = append(keys, key)
		}
	}
	// Fields present in every branch decide first.
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	return keys
}

func constBranchCount(branches []*jsonschema.Schema, key string) int {
	count := 0
	for _, branch := range branches {
		if property := resolved(branch).Properties[key]; property != nil &&
			resolved(property).Const != nil {
			count++
		}
	}
	return count
}

func constPropertyNames(branches []*jsonschema.Schema) []string {
	return constProperties(branches)
}

// constValues returns the distinct const values of key in branch order.
func constValues(branches []*jsonschema.Schema, key string) []any {
	values := []any{}
	for _, branch := range branches {
		property := resolved(branch).Properties[key]
		if property == nil || resolved(property).Const == nil {
			continue
		}
		value := *resolved(property).Const
		if !containsJSON(values, value) {
			values = append(values, value)
		}
	}
	return values
}

func requiredAlternatives(branches []*jsonschema.Schema) []string {
	keys := []string{}
	for _, branch := range branches {
		for _, key := range resolved(branch).Required {
			if !containsString(keys, key) {
				keys = append(keys, key)
			}
		}
	}
	return keys
}

// shapeBranches reports whether the branches are alternative object shapes
// rather than constraints over one shape (such as "exactly one identity").
func shapeBranches(branches []*jsonschema.Schema) bool {
	for _, branch := range branches {
		if resolved(branch).Types == nil {
			return false
		}
	}
	return true
}

func oneOfHolds(branches []*jsonschema.Schema, value any) bool {
	return countValid(branches, value) == 1
}

func countValid(branches []*jsonschema.Schema, value any) int {
	count := 0
	for _, branch := range branches {
		if branch.Validate(value) == nil {
			count++
		}
	}
	return count
}

func resolved(schema *jsonschema.Schema) *jsonschema.Schema {
	for schema != nil && schema.Ref != nil && schema.Types == nil &&
		len(schema.Properties) == 0 && len(schema.OneOf) == 0 {
		schema = schema.Ref
	}
	return schema
}

func typeAllowed(types []string, value any) bool {
	actual := jsonType(value)
	for _, expected := range types {
		if expected == actual || (expected == "number" && actual == "integer") {
			return true
		}
	}
	return false
}

func jsonType(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		if number, ok := jsonNumberRat(typed); ok && number.IsInt() {
			return "integer"
		}
		return "number"
	}
}

func jsonTypeArticle(value any) string {
	switch jsonType(value) {
	case "null":
		return "null"
	case "integer", "number":
		return "a number"
	case "array", "object":
		return "an " + jsonType(value)
	default:
		return "a " + jsonType(value)
	}
}

func typeNames(types []string) string {
	names := make([]string, 0, len(types))
	for _, name := range types {
		switch name {
		case "array", "object", "integer":
			names = append(names, "an "+name)
		case "null":
			names = append(names, "null")
		default:
			names = append(names, "a "+name)
		}
	}
	return joinWords(names, "or")
}

func patternDescription(pattern string) string {
	switch pattern {
	case "^r[1-9][0-9]*$":
		return "an element ref rN copied from observe/find"
	case "^w[1-9][0-9]*$":
		return "a window ref wN copied from observe/find"
	case "^a[1-9][0-9]*$":
		return "an app ref aN copied from observe"
	case "^f[1-9][0-9]*$":
		return "a find cursor fN copied from find"
	case "^(?:r|w)[1-9][0-9]*$", "^(r[1-9][0-9]*|w[1-9][0-9]*)$":
		return "an rN or wN ref copied from observe/find"
	case `\S`:
		return "non-blank"
	}
	return "a string matching " + pattern
}

func countRange(minimum, maximum int, noun string) string {
	plural := func(count int) string {
		if count == 1 {
			return noun
		}
		return noun + "s"
	}
	switch {
	case minimum >= 0 && maximum >= 0 && minimum == maximum:
		return fmt.Sprintf("exactly %d %s", minimum, plural(minimum))
	case minimum > 0 && maximum >= 0:
		return fmt.Sprintf("%d to %d %s", minimum, maximum, plural(maximum))
	case maximum >= 0:
		return fmt.Sprintf("at most %d %s", maximum, plural(maximum))
	default:
		return fmt.Sprintf("at least %d %s", minimum, plural(minimum))
	}
}

// pathLabel renders an instance location as actions[0].target.ref; the
// top level is named after the tool.
func pathLabel(path []string, tool string) string {
	if len(path) == 0 {
		if tool == "" {
			return "arguments"
		}
		return tool
	}
	var label strings.Builder
	for index, segment := range path {
		if _, err := strconv.Atoi(segment); err == nil {
			label.WriteString("[" + segment + "]")
			continue
		}
		if index > 0 {
			label.WriteByte('.')
		}
		label.WriteString(segment)
	}
	return label.String()
}

func jsonPointer(path []string) string {
	if len(path) == 0 {
		return ""
	}
	escaped := make([]string, len(path))
	for index, segment := range path {
		segment = strings.ReplaceAll(segment, "~", "~0")
		escaped[index] = strings.ReplaceAll(segment, "/", "~1")
	}
	return "/" + strings.Join(escaped, "/")
}

func appendPath(path []string, segment string) []string {
	next := make([]string, len(path), len(path)+1)
	copy(next, path)
	return append(next, segment)
}

func joinWords(words []string, conjunction string) string {
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	case 2:
		return words[0] + " " + conjunction + " " + words[1]
	default:
		return strings.Join(words[:len(words)-1], ", ") + ", " + conjunction + " " + words[len(words)-1]
	}
}

func stringValues(values []any) []string {
	out := make([]string, len(values))
	for index, value := range values {
		if text, ok := value.(string); ok {
			out[index] = text
		} else {
			out[index] = compactJSON(value)
		}
	}
	return out
}

func sortedKeys[V any](object map[string]V) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsJSON(values []any, target any) bool {
	for _, value := range values {
		if jsonEqual(value, target) {
			return true
		}
	}
	return false
}

func jsonEqual(left, right any) bool {
	leftNumber, leftIsNumber := jsonNumberRat(left)
	rightNumber, rightIsNumber := jsonNumberRat(right)
	if leftIsNumber || rightIsNumber {
		return leftIsNumber && rightIsNumber && leftNumber.Cmp(rightNumber) == 0
	}
	return compactJSON(left) == compactJSON(right)
}

func isContainer(value any) bool {
	switch value.(type) {
	case map[string]any, []any:
		return true
	default:
		return false
	}
}

func jsonNumberRat(value any) (*big.Rat, bool) {
	switch typed := value.(type) {
	case json.Number:
		return new(big.Rat).SetString(typed.String())
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return nil, false
		}
		return new(big.Rat).SetFloat64(typed), true
	case int:
		return new(big.Rat).SetInt64(int64(typed)), true
	case int64:
		return new(big.Rat).SetInt64(typed), true
	default:
		return nil, false
	}
}

func cloneObject(object map[string]any) map[string]any {
	clone := make(map[string]any, len(object))
	for key, value := range object {
		clone[key] = value
	}
	return clone
}

func compactJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(encoded)
}

// orderedJSON encodes an object with its branch-selecting fields first, so a
// hint reads {"type":"click","target":{"ref":"r1"}}.
func orderedJSON(object map[string]any, first []string) string {
	var out strings.Builder
	out.WriteByte('{')
	written := 0
	write := func(key string) {
		if written > 0 {
			out.WriteByte(',')
		}
		out.WriteString(strconv.Quote(key))
		out.WriteByte(':')
		if nested, ok := object[key].(map[string]any); ok {
			out.WriteString(orderedJSON(nested, first))
		} else {
			out.WriteString(compactJSON(object[key]))
		}
		written++
	}
	for _, key := range first {
		if _, present := object[key]; present {
			write(key)
		}
	}
	for _, key := range sortedKeys(object) {
		if !containsString(first, key) {
			write(key)
		}
	}
	out.WriteByte('}')
	return out.String()
}
