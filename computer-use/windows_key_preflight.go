package main

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Match WindowsKeyMapping before a batch can emit its first input event.
var windowsKeyNames = func() map[string]bool {
	names := strings.Fields(
		"CTRL CONTROL SHIFT ALT SUPER WIN META ENTER RETURN ESC ESCAPE TAB SPACE SPACEBAR " +
			"BACKSPACE DELETE LEFT ARROWLEFT UP ARROWUP RIGHT ARROWRIGHT DOWN ARROWDOWN " +
			"HOME END PAGE_UP PAGEUP PAGE_DOWN PAGEDOWN SEMICOLON EQUAL EQUALS COMMA MINUS " +
			"PERIOD DOT SLASH BACKTICK GRAVE LEFT_BRACKET BRACKET_LEFT BACKSLASH " +
			"RIGHT_BRACKET BRACKET_RIGHT QUOTE APOSTROPHE")
	result := make(map[string]bool, len(names))
	for _, name := range names {
		result[name] = true
	}
	return result
}()

func validateWindowsKeypress(specification string) error {
	for _, token := range strings.Split(specification, "+") {
		normalized := strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(token)), "-", "_")
		if windowsKeyNames[normalized] {
			continue
		}
		// .NET char is one UTF-16 code unit, not a supplementary scalar.
		if key, size := utf8.DecodeRuneInString(normalized); size == len(normalized) && size > 0 && key <= 0xffff {
			continue
		}
		if strings.HasPrefix(normalized, "F") {
			number, err := strconv.Atoi(strings.TrimSpace(normalized[1:]))
			if err == nil && number >= 1 && number <= 24 {
				continue
			}
		}
		return fmt.Errorf("unsupported Windows key %q", token)
	}
	return nil
}

func preflightWindowsAXKeys(actions []a11yBatchAction) error {
	for index, action := range actions {
		if action.actionType == "keypress" {
			if err := validateWindowsKeypress(action.key); err != nil {
				return fmt.Errorf("actions[%d].keys: %w", index, err)
			}
		}
	}
	return nil
}

func preflightWindowsGUIKeys(actions []ProviderDesktopAction) error {
	for index, action := range actions {
		for _, key := range action.Keys {
			if err := validateWindowsKeypress(key); err != nil {
				return fmt.Errorf("actions[%d].keys: %w", index, err)
			}
		}
	}
	return nil
}
