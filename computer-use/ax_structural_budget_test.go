package main

import "testing"

func TestWorkingStructuralBudgetDefaultsToTwelveK(t *testing.T) {
	t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "")
	if got := axStructuralTreeBudget(); got != 12000 {
		t.Fatalf("working structural budget=%d, expected exactly 12000", got)
	}
	if maxInlineChars() != 24000 {
		t.Fatal("12k working budget must not lower the final 24k safety ceiling")
	}
}
