package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

func requiredString(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

func optionalString(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

func a11yTargetAddress(args map[string]any) (string, bool, error) {
	raw, present := args["target"]
	if !present {
		return "", false, nil
	}
	target, ok := raw.(map[string]any)
	if !ok {
		return "", true, errors.New(
			`target must be an object: {"index": N}, {"ref": "rN"}, or {"selector": {...}}`,
		)
	}
	if len(target) != 1 {
		return "", true, errors.New(
			"target must contain exactly one of index, ref, or selector",
		)
	}
	if rawIndex, ok := target["index"]; ok {
		if _, isString := rawIndex.(string); isString {
			return "", true, errors.New("target.index must be an integer")
		}
		address := elementIndexString(rawIndex)
		if address == "" || !numericElementAddress(address) {
			return "", true, errors.New("target.index must be an integer")
		}
		index, _ := strconv.Atoi(address)
		if index < 0 {
			return "", true, errors.New("target.index must be non-negative")
		}
		return address, true, nil
	}
	if rawRef, ok := target["ref"]; ok {
		ref, ok := rawRef.(string)
		if !ok || ref == "" {
			return "", true, errors.New("target.ref must be a non-empty rN or wN string")
		}
		if !isShortAXRef(ref) &&
			!isShortAXWindowRef(ref) {
			return "", true, errors.New(
				"target.ref must be an exact rN or wN copied from observe/find; " +
					"never invent placeholders such as r??",
			)
		}
		return ref, true, nil
	}
	rawSelector, ok := target["selector"]
	if !ok {
		return "", true, errors.New(
			"target must contain exactly one of index, ref, or selector",
		)
	}
	selector, ok := rawSelector.(map[string]any)
	if !ok {
		return "", true, errors.New("target.selector must be an object")
	}
	address, err := a11ySelectorAddress(selector)
	if err != nil {
		return "", true, fmt.Errorf("target.selector: %w", err)
	}
	return address, true, nil
}

func a11ySelectorAddress(selector map[string]any) (string, error) {
	allowed := map[string]bool{
		"role": true, "name": true, "description": true,
		"placeholder": true, "state": true,
	}
	for field := range selector {
		if !allowed[field] {
			return "", fmt.Errorf(
				"unknown field %q; use role, name, description, placeholder, or state",
				field,
			)
		}
	}
	if len(selector) == 0 {
		return "", errors.New("must contain at least one selector field")
	}
	for field := range allowed {
		if value, present := selector[field]; present {
			text, ok := value.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return "", fmt.Errorf("%s must be a non-blank string", field)
			}
		}
	}
	role := strings.TrimSpace(optionalString(selector, "role"))
	if strings.ContainsAny(role, `[]"`) {
		return "", errors.New(`role must not contain [, ] or "`)
	}
	name := strings.TrimSpace(optionalString(selector, "name"))
	address := selectorHead(role, name)
	predicates := make([]string, 0, 4)
	if description := strings.TrimSpace(
		optionalString(selector, "description"),
	); description != "" {
		predicates = append(predicates,
			"[desc="+strconv.Quote(description)+"]")
	}
	if placeholder := strings.TrimSpace(
		optionalString(selector, "placeholder"),
	); placeholder != "" {
		predicates = append(predicates,
			"[placeholder="+strconv.Quote(placeholder)+"]")
	}
	if state := optionalString(selector, "state"); state != "" {
		if !publicSelectorStateWords[state] {
			return "", fmt.Errorf(
				"unsupported state %q; use checked, expanded, selected, or focused",
				state,
			)
		}
		predicates = append(predicates, "["+state+"]")
	}
	if address == "" && len(predicates) == 0 {
		return "", errors.New("must contain a non-empty selector field")
	}
	return strings.TrimSpace(strings.Join(
		append([]string{address}, predicates...), " ",
	)), nil
}

func elementIndexString(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case json.Number:
		if integer, err := value.Int64(); err == nil {
			return strconv.FormatInt(integer, 10)
		}
		if float, err := value.Float64(); err == nil {
			return integerElementIndexFloat(float)
		}
	case float64:
		return integerElementIndexFloat(value)
	case int:
		return strconv.Itoa(value)
	case int64:
		return strconv.FormatInt(value, 10)
	}
	return ""
}

func integerElementIndexFloat(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value {
		return ""
	}
	return strconv.FormatInt(int64(value), 10)
}

func optionalPositiveInt(args map[string]any, key string) (*int, error) {
	value, ok := args[key]
	if !ok {
		return nil, nil
	}
	return positiveIntFromValue(value, key)
}

func optionalNonNegativeInt(
	args map[string]any,
	key string,
	maximum int,
) (*int, error) {
	value, ok := args[key]
	if !ok {
		return nil, nil
	}
	invalid := fmt.Errorf(
		"%s must be a whole number between 0 and %d",
		key,
		maximum,
	)
	var integer int64
	switch typed := value.(type) {
	case int:
		integer = int64(typed)
	case float64:
		if !isWholeNumber(typed) {
			return nil, invalid
		}
		integer = int64(typed)
	case json.Number:
		parsed, ok := jsonInteger(typed)
		if !ok {
			return nil, invalid
		}
		integer = parsed
	default:
		return nil, invalid
	}
	if integer < 0 || integer > int64(maximum) {
		return nil, invalid
	}
	result := int(integer)
	return &result, nil
}

func positiveIntFromValue(value any, key string) (*int, error) {
	switch typed := value.(type) {
	case int:
		return positiveIntFromInt64(int64(typed), key)
	case float64:
		if !isWholeNumber(typed) {
			return nil, fmt.Errorf("%s must be a positive integer", key)
		}
		return positiveIntFromFloat64(typed, key)
	case json.Number:
		integer, ok := jsonInteger(typed)
		if !ok {
			return nil, fmt.Errorf("%s must be a positive integer", key)
		}
		return positiveIntFromInt64(integer, key)
	default:
		return nil, fmt.Errorf("%s must be a positive integer", key)
	}
}

// jsonInteger reads a JSON number as an integer. JSON Schema treats any
// number with a zero fractional part as an integer, so 5.0 and 5e3 count.
func jsonInteger(number json.Number) (int64, bool) {
	if integer, err := number.Int64(); err == nil {
		return integer, true
	}
	rational, ok := new(big.Rat).SetString(number.String())
	if !ok || !rational.IsInt() || !rational.Num().IsInt64() {
		return 0, false
	}
	return rational.Num().Int64(), true
}

func positiveIntFromFloat64(value float64, key string) (*int, error) {
	if !isWholeNumber(value) || value <= 0 || value > float64(maxInt()) {
		return nil, fmt.Errorf("%s must be a positive integer", key)
	}
	integer := int(value)
	return &integer, nil
}

func positiveIntFromInt64(value int64, key string) (*int, error) {
	if value <= 0 || value > int64(maxInt()) {
		return nil, fmt.Errorf("%s must be a positive integer", key)
	}
	integer := int(value)
	return &integer, nil
}

func isWholeNumber(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && math.Trunc(value) == value
}

func maxInt() int {
	return int(^uint(0) >> 1)
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

type observation struct {
	mode            string
	captureSnapshot *bool
}

func (o observation) resolvedMode() string {
	if o.mode == "" {
		return observeDiff
	}
	return o.mode
}

func (o observation) resolvedScreenshot() *bool {
	switch o.resolvedMode() {
	case observeScreenshot, observeFull:
		on := true
		return &on
	default:
		off := false
		return &off
	}
}

// parseObservation reads the optional observe argument. Values are exact:
// the published enum is the only accepted spelling.
func parseObservation(args map[string]any) (observation, error) {
	raw, present := args["observe"]
	if !present {
		return observation{}, nil
	}
	value, _ := raw.(string)
	for _, allowed := range observeValues {
		if value == allowed {
			return observation{mode: value}, nil
		}
	}
	return observation{}, fmt.Errorf(
		"invalid observe %s. Valid values are %s. Omit it for the default %q, which "+
			"returns only what changed since the previous tree state delivered to the model.",
		compactJSON(raw), strings.Join(observeValues, ", "), observeDiff)
}

// optionalBool returns the JSON boolean stored under key, or nil when the key
// is absent or holds any other type. Strings are never coerced: "yes" used to
// decode as false and silently inverted an assertion.
func optionalBool(args map[string]any, key string) *bool {
	flag, ok := args[key].(bool)
	if !ok {
		return nil
	}
	return &flag
}
