package main

import (
	"strings"
	"testing"
)

func TestDecodeAXRequestAcceptsEachMode(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"apps":                 {"mode": "apps"},
		"observe app":          {"mode": "observe", "app": "a1"},
		"observe pid":          {"mode": "observe", "app": "1234"},
		"observe window":       {"mode": "observe", "window_ref": "w1", "root": "r5", "observe": "full"},
		"observe focused root": {"mode": "observe", "window_ref": "w1", "root": "focused"},
		"observe until": {"mode": "observe", "window_ref": "w1", "observe": "none",
			"until": map[string]any{"type": "title", "text": "Saved", "negate": true}},
		"observe until owns identity": {"mode": "observe",
			"until": map[string]any{"type": "value", "ref": "r3", "value": 5.0}},
		"find": {"mode": "find", "window_ref": "w1", "limit": 50.0,
			"query": map[string]any{"role": "button", "name_contains": "Save"}},
		"find cursor": {"mode": "find", "cursor": "f2", "limit": 10.0},
		"act": {"mode": "act", "observe": "tree", "window_ref": "w1",
			"actions": []any{map[string]any{"type": "click", "ref": "r1"}},
			"until":   map[string]any{"type": "exists", "selector": map[string]any{"role": "dialog"}}},
	} {
		if _, err := decodeAXRequest(args); err != nil {
			t.Fatalf("%s rejected: %#v", name, err)
		}
	}
}

func TestDecodeAXRequestRejectsWithPathAndHint(t *testing.T) {
	for name, test := range map[string]struct {
		args    map[string]any
		path    string
		message string
		hint    bool
	}{
		"missing mode": {map[string]any{"app": "Editor"}, "mode", "mode is required", true},
		"mode case":    {map[string]any{"mode": "OBSERVE"}, "mode", "mode must be", true},
		"assert_ax removed": {map[string]any{"mode": "assert_ax"}, "mode",
			"use observe with until", true},
		"observation_target removed": {map[string]any{"mode": "observe", "observation_target": "apps"},
			"observation_target", `mode "apps"`, true},
		"app_ref renamed": {map[string]any{"mode": "observe", "app_ref": "a1"},
			"app_ref", `use "app"`, false},
		"postcondition renamed": {map[string]any{"mode": "act", "postcondition": map[string]any{},
			"actions": []any{map[string]any{"type": "click", "ref": "r1"}}},
			"postcondition", `use "until"`, false},
		"raw acquisition controls": {map[string]any{"mode": "observe", "app": "x", "text_limit": 100.0},
			"text_limit", "does not accept", false},
		"apps takes nothing": {map[string]any{"mode": "apps", "app": "x"},
			"app", "does not accept", false},
		"app and window_ref": {map[string]any{"mode": "observe", "app": "x", "window_ref": "w1"},
			"window_ref", "not both", true},
		"observe without identity": {map[string]any{"mode": "observe"}, "app",
			"observe needs app or window_ref", true},
		"observe none without until": {map[string]any{"mode": "observe", "app": "x", "observe": "none"},
			"observe", "returns nothing without until", true},
		"observe value case": {map[string]any{"mode": "observe", "app": "x", "observe": "Diff"},
			"observe", "observe must be one of", false},
		"notes alias": {map[string]any{"mode": "act", "observe": "notes",
			"actions": []any{map[string]any{"type": "click", "ref": "r1"}}},
			"observe", "observe must be one of", false},
		"selector root removed": {map[string]any{"mode": "observe", "app": "x",
			"root": map[string]any{"selector": map[string]any{"role": "list"}}},
			"root", `root must be "focused" or rN`, false},
		"wN root": {map[string]any{"mode": "observe", "app": "x", "root": "w1"},
			"root", `root must be "focused" or rN`, false},
		"invented ref": {map[string]any{"mode": "act",
			"actions": []any{map[string]any{"type": "click", "ref": "r??"}}},
			"actions[0].ref", "copied exactly", false},
		"pid as number":      {map[string]any{"mode": "observe", "app": 1234.0}, "app", "non-blank string", false},
		"find without query": {map[string]any{"mode": "find", "app": "x"}, "query", "find requires query", true},
		"find empty query": {map[string]any{"mode": "find", "app": "x", "query": map[string]any{}},
			"query", "at least one filter", true},
		"find unknown filter": {map[string]any{"mode": "find", "app": "x", "query": map[string]any{"name": "Save"}},
			"query.name", "query does not accept", false},
		"find both name forms": {map[string]any{"mode": "find", "app": "x",
			"query": map[string]any{"name_equals": "Save", "name_contains": "Sa"}},
			"query.name_contains", "not both", false},
		"find blank filter": {map[string]any{"mode": "find", "app": "x", "query": map[string]any{"role": " "}},
			"query.role", "non-blank", false},
		"limit too large": {map[string]any{"mode": "find", "app": "x", "limit": 51.0,
			"query": map[string]any{"role": "button"}}, "limit", "from 1 to 50", false},
		"limit inside query": {map[string]any{"mode": "find", "app": "x",
			"query": map[string]any{"role": "button", "limit": 5.0}}, "query.limit", "query does not accept", false},
		"cursor with query": {map[string]any{"mode": "find", "cursor": "f1",
			"query": map[string]any{"role": "button"}}, "query", "send only mode, cursor, and limit", true},
		"cursor on observe": {map[string]any{"mode": "observe", "app": "x", "cursor": "f1"},
			"cursor", "does not accept", false},
		"act without actions": {map[string]any{"mode": "act"}, "actions", "act requires actions", true},
		"act with app": {map[string]any{"mode": "act", "app": "x",
			"actions": []any{map[string]any{"type": "click", "ref": "r1"}}}, "app", "does not accept", false},
		"until type": {map[string]any{"mode": "observe", "app": "x", "until": map[string]any{"type": "value_equals"}},
			"until.type", "until.type must be one of", true},
		"until two targets": {map[string]any{"mode": "observe", "app": "x",
			"until": map[string]any{"type": "exists", "ref": "r1", "selector": map[string]any{"role": "x"}}},
			"until", "exactly one of ref (rN) or selector", true},
		"until wN": {map[string]any{"mode": "observe", "app": "x",
			"until": map[string]any{"type": "exists", "ref": "w1"}}, "until.ref", `"type":"title"`, false},
		"until field of another type": {map[string]any{"mode": "observe", "app": "x",
			"until": map[string]any{"type": "title", "text": "x", "ref": "r1"}}, "until.ref", "does not accept", false},
		"until blank value": {map[string]any{"mode": "observe", "app": "x",
			"until": map[string]any{"type": "value", "ref": "r1", "value": "  "}}, "until.value", "non-blank", true},
		"until string boolean": {map[string]any{"mode": "observe", "app": "x",
			"until": map[string]any{"type": "exists", "ref": "r1", "negate": "yes"}}, "until.negate", "true or false", false},
		"until contains number": {map[string]any{"mode": "observe", "app": "x",
			"until": map[string]any{"type": "value", "ref": "r1", "value": 3.0, "match": "contains"}},
			"until.match", "needs a string value", false},
		"until timeout": {map[string]any{"mode": "observe", "app": "x",
			"until": map[string]any{"type": "title", "text": "x", "timeout_ms": 60001.0}},
			"until.timeout_ms", "from 1 to 60000", false},
		"until stable": {map[string]any{"mode": "observe", "app": "x",
			"until": map[string]any{"type": "title", "text": "x", "stable_ms": 5001.0}},
			"until.stable_ms", "from 0 to 5000", false},
		"until selector state": {map[string]any{"mode": "observe", "app": "x",
			"until": map[string]any{"type": "exists", "selector": map[string]any{"state": "active"}}},
			"until.selector.state", "checked, expanded, selected, or focused", false},
	} {
		_, err := decodeAXRequest(test.args)
		if err == nil || err.code != axCodeInvalidArgument || err.path != test.path ||
			!strings.Contains(err.message, test.message) || (err.hint != "") != test.hint {
			t.Fatalf("%s: got %#v", name, err)
		}
	}
}

// TestAXHintsAreValidCalls checks that every corrected call shown for a
// top-level mistake is itself accepted.
func TestAXHintsAreValidCalls(t *testing.T) {
	for _, args := range []map[string]any{
		{},
		{"mode": "assert_ax"},
		{"mode": "observe"},
		{"mode": "act"},
		{"mode": "find", "app": "x"},
		{"mode": "observe", "observation_target": "apps"},
	} {
		_, err := decodeAXRequest(args)
		if err == nil || err.hint == "" {
			t.Fatalf("%v: no hint: %#v", args, err)
		}
		if _, hintErr := decodeAXRequest(decodeJSONForTest(t, err.hint)); hintErr != nil {
			t.Fatalf("hint %s for %v is invalid: %#v", err.hint, args, hintErr)
		}
	}
}

func TestDecodeAXPredicateMapsToInternalAssertions(t *testing.T) {
	for name, test := range map[string]struct {
		in   map[string]any
		want map[string]any
	}{
		"exists": {map[string]any{"type": "exists", "ref": "r1"},
			map[string]any{"type": "exists", "value": true}},
		"gone": {map[string]any{"type": "exists", "ref": "r1", "negate": true},
			map[string]any{"type": "exists", "value": false}},
		"state": {map[string]any{"type": "state", "ref": "r1", "state": "checked"},
			map[string]any{"type": "state", "state": "checked", "value": true}},
		"not state": {map[string]any{"type": "state", "ref": "r1", "state": "checked", "negate": true},
			map[string]any{"type": "state", "value": false}},
		"value": {map[string]any{"type": "value", "ref": "r1", "value": "14", "match": "contains"},
			map[string]any{"type": "value", "value": "14", "match": "contains"}},
		"text": {map[string]any{"type": "text", "ref": "r1", "text": "Done", "negate": true},
			map[string]any{"type": "text_contains", "text": "Done", "negate": true}},
		"title": {map[string]any{"type": "title", "text": "Saved", "timeout_ms": 900.0, "stable_ms": 100.0},
			map[string]any{"type": "window_title_contains", "text": "Saved", "exists": true,
				"timeout_ms": 900, "stable_ms": 100}},
	} {
		got, err := decodeAXPredicate(test.in, "until")
		if err != nil {
			t.Fatalf("%s: %#v", name, err)
		}
		for key, value := range test.want {
			if got[key] != value {
				t.Fatalf("%s: %s=%#v want %#v (%#v)", name, key, got[key], value, got)
			}
		}
		if _, specErr := parseA11yAssertionSpec(got); specErr != nil {
			t.Fatalf("%s: internal assertion rejected: %v", name, specErr)
		}
	}
}

func TestValuePredicateNegateInvertsTheGoal(t *testing.T) {
	assertion, err := decodeAXPredicate(map[string]any{
		"type": "value", "ref": "r1", "value": "busy", "negate": true,
	}, "until")
	if err != nil {
		t.Fatal(err)
	}
	goal := assertionGoal(assertion)
	if ok, _ := goal.check(&elementRecord{Value: "busy"}, nil); ok {
		t.Fatal("negated value predicate held while the value matched")
	}
	if ok, _ := goal.check(&elementRecord{Value: "idle"}, nil); !ok {
		t.Fatal("negated value predicate did not hold after the value changed")
	}
}

func TestReadIdentityDerivesOwnerFromElementRef(t *testing.T) {
	service, appRef, windowRef, elementRef := sheetBatchService(t)
	got, err := service.resolveA11yReadIdentity("", "", elementRef)
	if err != nil || got.appToken != appRef || got.windowRef != windowRef {
		t.Fatalf("owned assertion identity=%#v err=%v", got, err)
	}
	got, err = service.resolveA11yReadIdentity("", windowRef, elementRef)
	if err != nil || got.appToken != appRef ||
		got.windowRef != windowRef || got.windowHandle == "" {
		t.Fatalf("window-scoped assertion identity=%#v err=%v", got, err)
	}
	got, err = service.resolveA11yReadIdentity(appRef, "", "")
	if err != nil || got.appToken != appRef {
		t.Fatalf("app accepts aN: %#v err=%v", got, err)
	}
}

func TestReadIdentityRejectsMismatchedWindowRef(t *testing.T) {
	service, appRef, _, elementRef := sheetBatchService(t)
	other := service.windowRefRegistry.Acquire(
		"libreoffice", "w2:fedcba9876543210fedcba9876543210", `dialog "Question"`, "c1",
		":1.84", "/window/2", "pid:10:start:100", "dialog", "Question", appRef,
	)
	if _, err := service.resolveA11yReadIdentity("", other, elementRef); err == nil ||
		!strings.Contains(err.Error(), "omit window_ref") {
		t.Fatalf("mismatched window_ref accepted: %v", err)
	}
}
