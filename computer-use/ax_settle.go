package main

import (
	"encoding/json"
	"fmt"
)

func optionalSettleMS(args map[string]any) (int, error) {
	value, present := args["settle_ms"]
	if !present || value == nil {
		return defaultSettleMS, nil
	}
	invalid := fmt.Errorf("settle_ms must be a whole number of milliseconds between 0 and %d", maxSettleMS)
	var milliseconds int
	switch typed := value.(type) {
	case int:
		milliseconds = typed
	case float64:
		if !isWholeNumber(typed) || typed < 0 || typed > float64(maxSettleMS) {
			return 0, invalid
		}
		milliseconds = int(typed)
	case json.Number:
		integer, ok := jsonInteger(typed)
		if !ok || integer < 0 || integer > int64(maxSettleMS) {
			return 0, invalid
		}
		milliseconds = int(integer)
	default:
		return 0, invalid
	}
	if milliseconds < 0 || milliseconds > maxSettleMS {
		return 0, invalid
	}
	return milliseconds, nil
}
