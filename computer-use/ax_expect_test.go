package main

import (
	"strings"
	"testing"
)

func TestExpectTargetSyntaxIsToolkitIndependent(t *testing.T) {
	dialog := elementRecord{Index: 12, ControlType: "dialog", Name: "Save As"}
	entry := elementRecord{Index: 17, ControlType: "entry", Name: "File name", Value: "notes.txt", States: "[focused enabled]"}
	snapshot := expectSnapshot(dialog, entry)

	cases := []struct {
		target string
		want   int // expected element_index; -1 means no match
	}{
		{"role=dialog", 12},
		{"element_index=17", 17},
		{"17", 17},
		{`role=entry,name=File`, 17},
		{`entry "File name"`, 17},
		{"state=focused", 17},
		{"text=notes.txt", 17},
		{"role=slider", -1},
		{`role=dialog,name=Open`, -1},
	}
	for _, testCase := range cases {
		target, err := parseExpectTarget(testCase.target)
		if err != nil {
			t.Fatalf("parseExpectTarget(%q) error = %v", testCase.target, err)
		}
		record := target.find(snapshot)
		if testCase.want < 0 {
			if record != nil {
				t.Fatalf("%q should not match, but matched %d", testCase.target, record.Index)
			}
			continue
		}
		if record == nil || record.Index != testCase.want {
			t.Fatalf("%q should match %d, got %#v", testCase.target, testCase.want, record)
		}
	}

	// A misspelled field must fail hard; silently ignoring it would make the
	// check pass unconditionally.
	for _, bad := range []string{"", "rle=dialog", "element_index=abc", "  "} {
		if _, err := parseExpectTarget(bad); err == nil {
			t.Fatalf("parseExpectTarget(%q) should fail", bad)
		}
	}

	// A comma inside quotes is part of the name, not a field separator.
	target, err := parseExpectTarget(`name="Save, As"`)
	if err != nil {
		t.Fatalf("a comma inside quotes must not be treated as a separator: %v", err)
	}
	if target.name != "Save, As" {
		t.Fatalf("name = %q, want %q", target.name, "Save, As")
	}
}

func TestExpectPredicatesReportWhatTheySaw(t *testing.T) {
	dialog := elementRecord{Index: 12, ControlType: "dialog", Name: "Save As"}
	entry := elementRecord{Index: 17, ControlType: "entry", Name: "File name", Value: "已保存 notes.txt", States: "[focused enabled]"}
	button := elementRecord{Index: 42, ControlType: "push button", Name: "Cancel"}
	checkbox := elementRecord{Index: 43, ControlType: "check box", Name: "Keep", States: "[checked enabled]"}
	row := elementRecord{Index: 44, ControlType: "list item", Name: "Result", States: "[selected enabled]"}
	snapshot := expectSnapshot(dialog, entry, button, checkbox, row)
	// Baseline: only the button exists before the action; the dialog and the
	// entry appear after it.
	before := expectSnapshot(button)

	must := func(args map[string]any) *expectSpec {
		spec, err := parseExpect(map[string]any{"expect": args})
		if err != nil {
			t.Fatalf("parseExpect(%v) error = %v", args, err)
		}
		return spec
	}

	// appears: an element appears only when the match count increases.
	ok, evidence := must(map[string]any{"appears": "role=dialog"}).evaluate(before, snapshot)
	if !ok || !strings.Contains(evidence, "Save As") || !strings.Contains(evidence, "increased from 0") {
		t.Fatalf("appears should match and report the increased match count: ok=%v evidence=%q", ok, evidence)
	}
	ok, evidence = must(map[string]any{"appears": "role=alert"}).evaluate(before, snapshot)
	if ok || !strings.Contains(evidence, "did not increase") {
		t.Fatalf("an unmet appears should say that nothing new matched: ok=%v evidence=%q", ok, evidence)
	}

	// disappears: present before the action and absent after it.
	ok, _ = must(map[string]any{"disappears": "role=dialog"}).evaluate(snapshot, before)
	if !ok {
		t.Fatal("an element present before and absent after the action should count as disappeared")
	}
	ok, evidence = must(map[string]any{"disappears": "element_index=42"}).evaluate(before, snapshot)
	if ok || !strings.Contains(evidence, "STILL present") {
		t.Fatalf("an element that remains should be reported as still present: ok=%v evidence=%q", ok, evidence)
	}

	// exists_after: asserts presence after the action only, without claiming
	// that the element is new and without needing a baseline.
	ok, evidence = must(map[string]any{"exists_after": "role=dialog"}).evaluate(nil, snapshot)
	if !ok || !strings.Contains(evidence, "exists in the post-action tree") {
		t.Fatalf("exists_after should allow a presence assertion without a baseline: ok=%v evidence=%q", ok, evidence)
	}

	// value_becomes
	ok, evidence = must(map[string]any{
		"value_becomes": map[string]any{"element_index": float64(17), "contains": "已保存"},
	}).evaluate(before, snapshot)
	if !ok || !strings.Contains(evidence, "已保存") {
		t.Fatalf("value_becomes should read the value: ok=%v evidence=%q", ok, evidence)
	}
	ok, evidence = must(map[string]any{
		"value_becomes": map[string]any{"element_index": "17", "contains": "未保存"},
	}).evaluate(before, snapshot)
	if ok || !strings.Contains(evidence, "does not contain") {
		t.Fatalf("a mismatched value should report the actual value: ok=%v evidence=%q", ok, evidence)
	}
	emptyValue := &appSnapshot{Elements: []elementRecord{
		{Index: 18, ControlType: "entry", Name: "Search", Value: ""},
	}}
	ok, evidence = must(map[string]any{
		"value_becomes": map[string]any{"element_index": "18", "equals": ""},
	}).evaluate(nil, emptyValue)
	if !ok {
		t.Fatalf("equals empty string should match an empty accessibility value: %q", evidence)
	}
	// value_becomes reads only the accessibility value; name, description or
	// placeholder text must not stand in for the control's value.
	ok, evidence = must(map[string]any{
		"value_becomes": map[string]any{"element_index": "12", "contains": "Save As"},
	}).evaluate(before, snapshot)
	if ok || !strings.Contains(evidence, `has value ""`) {
		t.Fatalf("a name-only match must not satisfy value_becomes: ok=%v evidence=%q", ok, evidence)
	}

	// state_is_after compares whole state tokens and explains absent states.
	for _, testCase := range []struct {
		index string
		state string
	}{
		{index: "17", state: "focused"},
		{index: "43", state: "checked"},
		{index: "44", state: "selected"},
	} {
		ok, evidence = must(map[string]any{
			"state_is_after": map[string]any{"element_index": testCase.index, "state": testCase.state},
		}).evaluate(before, snapshot)
		if !ok || !strings.Contains(evidence, "reports states") {
			t.Fatalf("state_is_after(%s) should match: ok=%v evidence=%q", testCase.state, ok, evidence)
		}
	}
	ok, evidence = must(map[string]any{
		"state_is_after": map[string]any{"element_index": "17", "state": "selected"},
	}).evaluate(before, snapshot)
	if ok || !strings.Contains(evidence, `without "selected"`) {
		t.Fatalf("an unreported state must not match: ok=%v evidence=%q", ok, evidence)
	}

	transitionBefore := expectSnapshot(
		elementRecord{Index: 43, ControlType: "check box", Name: "Keep", States: "[enabled]"},
	)
	ok, evidence = must(map[string]any{
		"state_becomes": map[string]any{"element_index": "43", "state": "checked"},
	}).evaluate(transitionBefore, snapshot)
	if !ok || !strings.Contains(evidence, "reports states") {
		t.Fatalf("state_becomes must recognize a real state transition: ok=%v evidence=%q", ok, evidence)
	}
	alreadyChecked := expectSnapshot(
		elementRecord{Index: 43, ControlType: "check box", Name: "Keep", States: "[checked enabled]"},
	)
	ok, evidence = must(map[string]any{
		"state_becomes": map[string]any{"element_index": "43", "state": "checked"},
	}).evaluate(alreadyChecked, snapshot)
	if ok || !strings.Contains(evidence, "already satisfied") {
		t.Fatalf("a state already satisfied before the action must not pass a postcondition off as a transition: ok=%v evidence=%q", ok, evidence)
	}

	ambiguousValues := expectSnapshot(
		elementRecord{Index: 50, ControlType: "entry", Name: "First", Value: "ready"},
		elementRecord{Index: 51, ControlType: "entry", Name: "Second", Value: "ready"},
	)
	ok, evidence = must(map[string]any{
		"value_becomes": map[string]any{"element_index": "role=entry", "equals": "ready"},
	}).evaluate(before, ambiguousValues)
	if ok || !strings.Contains(evidence, "2 elements match") ||
		!strings.Contains(evidence, "ambiguous") {
		t.Fatalf("value_becomes must not silently pick the first broad match: ok=%v evidence=%q", ok, evidence)
	}

	ambiguousStates := expectSnapshot(
		elementRecord{Index: 52, ControlType: "check box", Name: "First", States: "[checked]"},
		elementRecord{Index: 53, ControlType: "check box", Name: "Second", States: "[checked]"},
	)
	ok, evidence = must(map[string]any{
		"state_becomes": map[string]any{"element_index": "role=check box", "state": "checked"},
	}).evaluate(before, ambiguousStates)
	if ok || !strings.Contains(evidence, "2 elements match") ||
		!strings.Contains(evidence, "ambiguous") {
		t.Fatalf("state_becomes must not silently pick the first broad match: ok=%v evidence=%q", ok, evidence)
	}

	// focus_moves_to
	ok, _ = must(map[string]any{"focus_moves_to": "role=entry"}).evaluate(before, snapshot)
	if !ok {
		t.Fatal("focus_moves_to should hold when focus is on the entry")
	}
	ok, evidence = must(map[string]any{"focus_moves_to": "role=push button"}).evaluate(before, snapshot)
	if ok || !strings.Contains(evidence, "focus is on index=17") {
		t.Fatalf("when focus is not on the target, evidence must say where it is: ok=%v evidence=%q", ok, evidence)
	}

	// Multiple predicates are combined with AND.
	spec := must(map[string]any{"appears": "role=dialog", "focus_moves_to": "role=push button"})
	if ok, _ := spec.evaluate(before, snapshot); ok {
		t.Fatal("multiple predicates should pass only when all of them hold")
	}
}

func TestFocusWithinAcceptsFocusedDescendants(t *testing.T) {
	dialog := elementRecord{
		Index: 12, ControlType: "dialog", Name: "Save As", RuntimeID: []int{0, 4},
	}
	entry := elementRecord{
		Index: 17, ControlType: "entry", Name: "File name",
		RuntimeID: []int{0, 4, 2, 1}, States: "[focused enabled]",
	}
	outside := elementRecord{
		Index: 20, ControlType: "entry", Name: "Search",
		RuntimeID: []int{0, 7}, States: "[focused enabled]",
	}
	spec, err := parseExpect(map[string]any{
		"expect": map[string]any{"focus_within": "role=dialog"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ok, evidence := spec.evaluate(nil, expectSnapshot(dialog, entry))
	if !ok || !strings.Contains(evidence, "within index=12") {
		t.Fatalf("focus_within should hold when focus is on a descendant of the target: ok=%v evidence=%q", ok, evidence)
	}
	root := elementRecord{Index: 0, ControlType: "frame", Name: "Editor"}
	rootSpec, err := parseExpect(map[string]any{
		"expect": map[string]any{"focus_within": "role=frame"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ok, evidence = rootSpec.evaluate(nil, expectSnapshot(root, entry))
	if !ok {
		t.Fatalf("an empty runtime path denotes the root and must contain focused descendants that have paths: %q", evidence)
	}
	ok, evidence = spec.evaluate(nil, expectSnapshot(dialog, outside))
	if ok || !strings.Contains(evidence, "outside") {
		t.Fatalf("focus_within must not hold when focus is outside the target: ok=%v evidence=%q", ok, evidence)
	}
}

// TestAppearsIsAChangeAssertionNotAPresenceCheck checks that appears asserts a
// change, not presence. Judging only the post-action snapshot would report a
// top-level window title that was there all along as newly appeared. False
// certainty is more dangerous than "not verified" because it stops the agent
// from doubting.
func TestAppearsIsAChangeAssertionNotAPresenceCheck(t *testing.T) {
	frame := elementRecord{Index: 0, ControlType: "frame", Name: "Files"}
	dialog := elementRecord{Index: 12, ControlType: "dialog", Name: "Save As"}

	must := func(args map[string]any) *expectSpec {
		spec, err := parseExpect(map[string]any{"expect": args})
		if err != nil {
			t.Fatalf("parseExpect(%v) error = %v", args, err)
		}
		return spec
	}

	// An element present before the action and still present after it has not
	// appeared.
	before := expectSnapshot(frame, dialog)
	after := expectSnapshot(frame, dialog)
	ok, evidence := must(map[string]any{"appears": "role=frame"}).evaluate(before, after)
	if ok {
		t.Fatalf("an element present before the action must not count as appeared: evidence=%q", evidence)
	}
	if !strings.Contains(evidence, "did not increase") || !strings.Contains(evidence, "1 before, 1 after") {
		t.Fatalf("evidence must say the match count did not increase: %q", evidence)
	}

	// Only a real addition counts: no dialog before the action, one after it.
	ok, evidence = must(map[string]any{"appears": "role=dialog"}).evaluate(
		expectSnapshot(frame), after)
	if !ok {
		t.Fatalf("a real addition should evaluate to true: %q", evidence)
	}
	// The evidence must give the count change; the element index is only the
	// current address.
	if !strings.Contains(evidence, "increased from 0") {
		t.Fatalf("appears evidence must rest on the increased match count: %q", evidence)
	}
	if !strings.Contains(evidence, "only its current address") {
		t.Fatalf("evidence must not present element_index as proof of an addition: %q", evidence)
	}

	// Without a baseline there is no change to report. The check must not fall
	// back to presence: verified=false is better than a false true.
	ok, evidence = must(map[string]any{"appears": "role=dialog"}).evaluate(nil, after)
	if ok {
		t.Fatalf("appears must not be true without a pre-action snapshot: %q", evidence)
	}
	if !strings.Contains(evidence, "no pre-action snapshot") || !strings.Contains(evidence, "AX mode=observe") {
		t.Fatalf("evidence must say the baseline is missing and how to capture one: %q", evidence)
	}

	// disappears is symmetric: an element absent before the action cannot
	// disappear, and the vacuous truth must not pass.
	ok, evidence = must(map[string]any{"disappears": "role=alert"}).evaluate(before, after)
	if ok {
		t.Fatalf("a target that never existed must not count as disappeared: %q", evidence)
	}
	if !strings.Contains(evidence, "nothing to disappear") {
		t.Fatalf("evidence must call out the vacuous truth: %q", evidence)
	}
	// Only an element present before and absent after has disappeared.
	ok, evidence = must(map[string]any{"disappears": "role=dialog"}).evaluate(
		before, expectSnapshot(frame))
	if !ok || !strings.Contains(evidence, "decreased from 1") {
		t.Fatalf("a real disappearance should be true and report the pre-action matches: ok=%v evidence=%q", ok, evidence)
	}
}

func TestAppearsAndDisappearsUseRoleConstrainedMatchCounts(t *testing.T) {
	menuItem := elementRecord{Index: 3, ControlType: "menu item", Name: "Open"}
	dialogA := elementRecord{Index: 7, ControlType: "dialog", Name: "Open"}
	dialogB := elementRecord{Index: 9, ControlType: "dialog", Name: "Open"}

	target, err := parseExpectTarget("role=dialog,name=Open")
	if err != nil {
		t.Fatal(err)
	}
	appears := expectPredicate{kind: "appears", target: target}
	ok, evidence := appears.evaluate(
		expectSnapshot(menuItem, dialogA),
		expectSnapshot(menuItem, dialogA, dialogB),
	)
	if !ok || !strings.Contains(evidence, "increased from 1") {
		t.Fatalf("a same-named menu item must not enter the dialog count, and 1->2 should satisfy appears: ok=%v evidence=%q", ok, evidence)
	}

	disappears := expectPredicate{kind: "disappears", target: target}
	ok, evidence = disappears.evaluate(
		expectSnapshot(menuItem, dialogA, dialogB),
		expectSnapshot(menuItem, dialogA),
	)
	if !ok || !strings.Contains(evidence, "decreased from 2") {
		t.Fatalf("a same-named menu item must not block the dialog's 2->1 disappears: ok=%v evidence=%q", ok, evidence)
	}

	nameOnly, err := parseExpectTarget("name=Open")
	if err != nil {
		t.Fatal(err)
	}
	ok, evidence = (expectPredicate{kind: "appears", target: nameOnly}).evaluate(
		expectSnapshot(menuItem), expectSnapshot(menuItem, dialogA))
	if !ok || !strings.Contains(evidence, "dialog elements") ||
		!strings.Contains(evidence, "increased from 0") {
		t.Fatalf("without a role, matches should be grouped by role to detect the new dialog: ok=%v evidence=%q", ok, evidence)
	}
}

func TestChangePredicatesSeparateSameTextByRole(t *testing.T) {
	menu := elementRecord{Index: 1, ControlType: "menu item", Name: "Brightness-Contrast"}
	dialog := elementRecord{Index: 2, ControlType: "dialog", Name: "Brightness-Contrast"}
	target, err := parseExpectTarget("Brightness-Contrast")
	if err != nil {
		t.Fatal(err)
	}

	appears := expectPredicate{kind: "appears", target: target}
	ok, evidence := appears.evaluate(expectSnapshot(menu), expectSnapshot(menu, dialog))
	if !ok || !strings.Contains(evidence, "dialog elements") ||
		!strings.Contains(evidence, "increased from 0") {
		t.Fatalf("a same-named menu item must not mask a new dialog: ok=%v evidence=%q", ok, evidence)
	}

	disappears := expectPredicate{kind: "disappears", target: target}
	ok, evidence = disappears.evaluate(expectSnapshot(menu, dialog), expectSnapshot(menu))
	if !ok || !strings.Contains(evidence, "dialog elements") ||
		!strings.Contains(evidence, "decreased from 1") {
		t.Fatalf("a same-named menu item must not mask a closed dialog: ok=%v evidence=%q", ok, evidence)
	}
}

// TestBareExpectWordNeverMatchesARole checks that a bare word never matches a
// role. A fallback match that included ControlType would let
// `expect={"appears":"menu"}` hit unrelated controls. Roles form a closed
// vocabulary and must be selected explicitly with role=.
func TestBareExpectWordNeverMatchesARole(t *testing.T) {
	menu := elementRecord{Index: 3, ControlType: "menu", Name: "File"}
	entry := elementRecord{Index: 8, ControlType: "entry", Name: "Address and search bar"}
	snapshot := expectSnapshot(menu, entry)

	if _, err := parseExpectTarget("menu"); err == nil ||
		!strings.Contains(err.Error(), "Write role=menu") {
		t.Fatalf("a bare role must fail fast and suggest the role= form, got err=%v", err)
	}
	// Only an explicit role matches.
	explicit, err := parseExpectTarget("role=menu")
	if err != nil {
		t.Fatal(err)
	}
	if record := explicit.find(snapshot); record == nil || record.Index != 3 {
		t.Fatalf("role=menu should match 3, got %#v", record)
	}

	if _, err := parseExpectTarget("dialog"); err == nil ||
		!strings.Contains(err.Error(), "Write role=dialog") {
		t.Fatalf("dialog must also require an explicit role=, got err=%v", err)
	}

	// A bare string must not match across the role and name fields either; a
	// single field has to contain the whole string. An unquoted summary such
	// as `entry Address and search bar` therefore never matches when pasted
	// back, which is why element references are printed in the quoted form.
	crossField, err := parseExpectTarget("entry Address and search bar")
	if err != nil {
		t.Fatal(err)
	}
	if record := crossField.find(snapshot); record != nil {
		t.Fatalf("a bare string must not match across role and name, but matched %d", record.Index)
	}
	// The canonical quoted form, which is what gets printed, must match.
	quotedForm, err := parseExpectTarget(`entry "Address and search bar"`)
	if err != nil {
		t.Fatal(err)
	}
	if record := quotedForm.find(snapshot); record == nil || record.Index != 8 {
		t.Fatalf(`entry "Address and search bar" should match 8, got %#v`, record)
	}
}

// TestElementReferencesRoundTripThroughTheTargetParser checks that every
// element reference printed for the agent resolves to the same element when
// pasted back into expect. The renderer and the parser are two ends of one
// grammar, and any drift would make the evidence contradict itself.
func TestElementReferencesRoundTripThroughTheTargetParser(t *testing.T) {
	records := []elementRecord{
		{Index: 4, ControlType: "push button", Name: "Save"},
		{Index: 5, ControlType: "entry", Name: "Address and search bar"},
		{Index: 6, ControlType: "panel", Name: "PageShape: Weekday in school"},
		{Index: 7, ControlType: "separator"}, // unnamed element
		{Index: 8, ControlType: "entry", Name: `say "hi"`},
	}
	snapshot := expectSnapshot(records...)
	for _, record := range records {
		for _, rendered := range []string{describeRecord(&record), renderElementAddress(&record)} {
			target, err := parseExpectTarget(rendered)
			if err != nil {
				t.Fatalf("rendered %q does not parse back: %v", rendered, err)
			}
			hit := target.find(snapshot)
			if hit == nil || hit.Index != record.Index {
				t.Fatalf("%q should resolve back to element_index %d, got %#v",
					rendered, record.Index, hit)
			}
		}
	}

	// The focus summary uses the same renderer, so it round-trips as well.
	focused := elementRecord{Index: 5, ControlType: "entry",
		Name: "Address and search bar", States: "[focused enabled]"}
	live := expectSnapshot(focused)
	description := focusDescription(live)
	target, err := parseExpectTarget(description)
	if err != nil {
		t.Fatalf("focus summary %q does not parse back: %v", description, err)
	}
	if hit := target.findFocused(live); hit == nil || hit.Index != 5 {
		t.Fatalf("parsed focus summary should still match the focused element: %q -> %#v", description, hit)
	}
}

// TestExpectEvidenceNeverContradictsItself checks that one piece of evidence
// never mixes two observation sources. A message such as `nothing in the tree
// matches "entry Address and search bar"; focus is currently on entry Address
// and search bar` reads its first half from the pruned Elements and its second
// half from a separate whole-window scan, so the halves contradict each other
// and the agent cannot tell which to trust.
func TestExpectEvidenceNeverContradictsItself(t *testing.T) {
	spec, err := parseExpect(map[string]any{
		"expect": map[string]any{"focus_moves_to": `entry "Address and search bar"`},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The focused node was pruned: the tree has neither a match nor any
	// focused node, while the separate scan still reports focus. This is the
	// situation that produces self-contradicting evidence.
	pruned := expectSnapshot(elementRecord{Index: 1, ControlType: "frame", Name: "Browser"})
	pruned.FocusedSummary = `entry "Address and search bar"`
	ok, evidence := spec.evaluate(expectSnapshot(), pruned)
	if ok {
		t.Fatalf("must not be true when the focused node is not in the tree: %q", evidence)
	}
	if strings.Contains(evidence, "nothing in the tree matches") {
		t.Fatalf("evidence must not say 'nothing in the tree matches', which its second half contradicts: %q", evidence)
	}
	// The evidence must separate "the target does not match" from "the focused
	// node is not in this snapshot", and state that the summary comes from a
	// separate scan rather than the view used for the decision.
	for _, fragment := range []string{
		"cannot decide",
		"not part of the tree",
		"separate whole-window scan",
	} {
		if !strings.Contains(evidence, fragment) {
			t.Fatalf("evidence is missing %q and cannot tell 'no match' from 'not observed': %q", fragment, evidence)
		}
	}

	// Conversely, when the focused node is in this tree but is not the target,
	// the evidence must say so definitively, naming the focused element from
	// the same Elements without a second observation source.
	elsewhere := expectSnapshot(
		elementRecord{Index: 2, ControlType: "push button", Name: "Reload", States: "[focused]"},
		elementRecord{Index: 3, ControlType: "entry", Name: "Address and search bar"},
	)
	elsewhere.FocusedSummary = `push button "Reload"`
	ok, evidence = spec.evaluate(expectSnapshot(), elsewhere)
	if ok {
		t.Fatalf("must not be true when focus is not on the target: %q", evidence)
	}
	if !strings.Contains(evidence, "focus is on index=2") {
		t.Fatalf("focus evidence must come from the tree used for the decision: %q", evidence)
	}
	if strings.Contains(evidence, "cannot decide") {
		t.Fatalf("evidence must not say 'cannot decide' when the focused node is in the tree: %q", evidence)
	}
}
