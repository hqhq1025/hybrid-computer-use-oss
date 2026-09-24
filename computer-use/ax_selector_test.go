package main

import (
	"strings"
	"testing"
)

func TestWorkingClickPredicateMatchesCanonicalState(t *testing.T) {
	predicates, err := parsePredicates("[click]")
	if err != nil {
		t.Fatalf("working click predicate did not parse: %v", err)
	}
	record := &elementRecord{
		ControlType: "push button",
		Name:        "Save",
		States:      "[focused has-click-action]",
	}
	if !recordMatchesPredicates(record, predicates) {
		t.Fatal("[click] must match the canonical has-click-action state")
	}
	if _, err := parsePredicates("[has-click-action]"); err == nil {
		t.Fatal("legacy has-click-action selector must be rejected")
	}
}

func TestStructuredSelectorAcceptsStandardOrProviderRole(t *testing.T) {
	snapshot := &appSnapshot{
		Elements: []elementRecord{
			{Index: 1, ControlType: "push button", Name: "Save"},
		},
	}
	for _, selector := range []string{`button "Save"`, `push button "Save"`} {
		record, err := lookupBySelector(snapshot, selector, "")
		if err != nil {
			t.Fatalf("selector %q failed: %v", selector, err)
		}
		if record.Index != 1 {
			t.Fatalf("selector %q resolved %#v", selector, record)
		}
	}
}

// TestSelectorFindsTheRealClosingQuote checks that the closing quote of a name
// is its real match rather than the last quote in the selector, including when
// the name contains quotes or backslashes. Taking the last quote only works
// without a suffix; a trailing [desc="..."] would be swallowed into the name.
func TestSelectorFindsTheRealClosingQuote(t *testing.T) {
	role, name, hasName, rest := parseSelector(`toggle button "Menu" [desc="View options"]`)
	if role != "toggle button" || name != "Menu" || !hasName {
		t.Fatalf("wrong closing quote: role=%q name=%q", role, name)
	}
	if rest != `[desc="View options"]` {
		t.Fatalf("wrong remainder: %q", rest)
	}

	// The renderer's quoted() escapes quotes as \", so parsing must unescape
	// them.
	_, name, _, rest = parseSelector(`static "say \"hi\"" [focused]`)
	if name != `say "hi"` {
		t.Fatalf("escapes were not restored: %q", name)
	}
	if rest != "[focused]" {
		t.Fatalf("wrong remainder with escapes: %q", rest)
	}

	// A `]` inside a desc value must not end the predicate.
	preds, err := parsePredicates(`[desc="a] b"]`)
	if err != nil || len(preds) != 1 || preds[0].value != "a] b" {
		t.Fatalf("a ] inside quotes must not end the predicate: %v %v", preds, err)
	}
}

func TestWorkingProjectionNormalizedSelectorRoundTrips(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 list \"Results\"",
			"\t\t1 list item \"2024 Sedan\\n  Low mileage\"",
		},
		Elements: []elementRecord{
			{Index: 0, ControlType: "list", Name: "Results"},
			{
				Index:       1,
				ControlType: "list item",
				Name:        "2024 Sedan\n  Low mileage",
			},
		},
	}
	applyWorkingTreeProjection(snapshot, treeViewWorking)
	if !strings.Contains(
		strings.Join(snapshot.TreeLines, "\n"),
		`list item "2024 Sedan Low mileage"`,
	) {
		t.Fatalf("working tree did not expose normalized identity: %v", snapshot.TreeLines)
	}
	record, err := lookupBySelectorFor(
		snapshot,
		`list item "2024 Sedan Low mileage"`,
		"",
		"test",
	)
	if err != nil || record == nil || record.Index != 1 {
		t.Fatalf("normalized working selector did not round-trip: record=%#v err=%v", record, err)
	}
}

func TestNormalizedSelectorAmbiguityFailsClosed(t *testing.T) {
	snapshot := &appSnapshot{Elements: []elementRecord{
		{Index: 1, ControlType: "entry", Name: "Account\tName"},
		{Index: 2, ControlType: "entry", Name: "Account Name"},
	}}
	record, err := lookupBySelectorFor(
		snapshot,
		`entry "Account Name"`,
		"",
		"test",
	)
	if err == nil || record != nil ||
		!strings.Contains(strings.ToLower(err.Error()), "ambiguous") {
		t.Fatalf("normalized identity collision did not fail closed: record=%#v err=%v", record, err)
	}
}
