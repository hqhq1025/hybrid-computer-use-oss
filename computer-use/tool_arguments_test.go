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
