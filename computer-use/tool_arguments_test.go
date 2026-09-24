package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestElementIndexAcceptsStringAndJSONNumber(t *testing.T) {
	args, err := readArguments(`{"app":"Text Editor","element_index":0}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := elementIndexString(args["element_index"]); got != "0" {
		t.Fatalf("numeric element_index = %q, want 0", got)
	}
	if got := elementIndexString("14"); got != "14" {
		t.Fatalf("string element_index = %q, want 14", got)
	}
	if got := elementIndexString(json.Number("1.5")); got != "" {
		t.Fatalf("fractional element_index = %q, want empty", got)
	}
}

func TestA11yStructuredTargetNormalizesWithoutAmbiguousFields(t *testing.T) {
	ref := "r17"
	cases := []struct {
		name   string
		target map[string]any
		want   string
	}{
		{
			name: "index",
			target: map[string]any{
				"index": float64(7),
			},
			want: "7",
		},
		{
			name: "ref",
			target: map[string]any{
				"ref": ref,
			},
			want: ref,
		},
		{
			name: "selector",
			target: map[string]any{
				"selector": map[string]any{
					"role":        "push button",
					"name":        "Save",
					"description": "Save document",
					"state":       "focused",
				},
			},
			want: `push button "Save" [desc="Save document"] [focused]`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, present, err := a11yTargetAddress(map[string]any{
				"target": testCase.target,
			})
			if err != nil || !present || got != testCase.want {
				t.Fatalf("address=%q present=%v err=%v want=%q",
					got, present, err, testCase.want)
			}
		})
	}

	for _, invalid := range []map[string]any{
		{"target": "Save"},
		{"target": map[string]any{}},
		{"target": map[string]any{"index": float64(1), "ref": ref}},
		{"target": map[string]any{"index": float64(1.5)}},
		{"target": map[string]any{"ref": "not-a-ref"}},
		{"target": map[string]any{"ref": encodeSemanticRef(`push button "Save"`)}},
		{"target": map[string]any{"ref": encodeScopedSemanticRef(`push button "Save"`, "w2:test")}},
		{"target": map[string]any{"selector": map[string]any{}}},
		{"target": map[string]any{
			"selector": map[string]any{"role:document": "document"},
		}},
		{"target": map[string]any{
			"selector": map[string]any{"action": "click"},
		}},
		{"target": map[string]any{
			"selector": map[string]any{"state": "click"},
		}},
	} {
		if _, _, err := a11yTargetAddress(invalid); err == nil {
			t.Fatalf("invalid target accepted: %#v", invalid)
		}
	}
}

// TestObserveAndExpectAreOrthogonal checks that observe controls how much
// observation is returned and expect controls which outcome is verified, with
// neither affecting the other. With observe="none" plus expect, neither tree
// nor screenshot is returned, but the verification result still is.
func TestObserveAndExpectAreOrthogonal(t *testing.T) {
	args := map[string]any{
		"app":       "gedit",
		"observe":   "none",
		"expect":    map[string]any{"appears": "role=dialog"},
		"settle_ms": 0.0,
	}
	obs, err := parseObservation(args)
	if err != nil {
		t.Fatalf("expect must not affect observe parsing: %v", err)
	}
	if obs.resolvedMode() != observeNone {
		t.Fatalf("observe should still be none, got %q", obs.resolvedMode())
	}
	plan, err := parseSettlePlan(args)
	if err != nil {
		t.Fatalf("observe must not affect expect parsing: %v", err)
	}
	if plan.expect == nil {
		t.Fatal("observe=none must not drop the verification intent")
	}

	// observe="none" returns notesOnlyResult and the verdict travels in the
	// notes, so it must appear in the result: it is a conclusion, not an
	// observation.
	svc := newService()
	outcome := svc.settle("gedit", plan, expectSnapshot(), expectSnapshot(), nil, "")
	if outcome.verified {
		t.Fatal("appears must not hold on empty snapshots")
	}
	snapshot := expectSnapshot()
	result := snapshot.notesOnlyResult([]string{
		expectNote(plan, outcome, axServiceRequest{Action: axClickCommand{}}, nil),
	})
	if len(result.Content) != 1 || result.Content[0].Type != "text" {
		t.Fatalf("observe=none must not return a tree or image: %#v", result.Content)
	}
	text := result.Content[0].Text
	if !strings.Contains(text, `"predicate_verified":false`) || !strings.Contains(text, "settled_after_ms") {
		t.Fatalf("observe=none must still return the verification verdict: %q", text)
	}
	if result.IsError {
		t.Fatal("predicate_verified:false must not set isError")
	}
	if strings.Contains(text, "element_index values stay valid") {
		t.Fatal("observe=none must not promise that removed or omitted old indices remain valid")
	}
	if !strings.Contains(text, "Existing rN refs remain usable") {
		t.Fatal("observe=none must describe the refreshed ref-lifecycle boundary")
	}
}

func TestPublicTargetRefAcceptsOnlyShortElementOrWindowRefs(t *testing.T) {
	for _, ref := range []string{"r1", "r42", "w1", "w9"} {
		if got, _, err := a11yTargetAddress(map[string]any{
			"target": map[string]any{"ref": ref},
		}); err != nil || got != ref {
			t.Fatalf("public ref %q rejected: got=%q err=%v", ref, got, err)
		}
	}
	for _, ref := range []string{
		"r0",
		"w0",
		"w1:payload",
		"s1:payload",
		"s2:payload",
		"w2:" + strings.Repeat("a", 32),
	} {
		if _, _, err := a11yTargetAddress(map[string]any{
			"target": map[string]any{"ref": ref},
		}); err == nil {
			t.Fatalf("internal or invalid ref %q was accepted", ref)
		}
	}
}
