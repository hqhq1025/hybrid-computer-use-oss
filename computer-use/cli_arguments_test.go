package main

import (
	"encoding/json"
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
