package main

import (
	"strings"
	"testing"
)

func TestHiddenSettlePollsDoNotConsumeDeliveredDiagnosticState(t *testing.T) {
	source := mainGoSource(t)
	start := strings.Index(source, "func (s *service) refreshSnapshotForSettle(")
	end := strings.Index(source[start:], "func settleReadRequest(")
	if start < 0 || end < 0 {
		t.Fatal("missing dedicated hidden settle refresh path")
	}
	body := source[start : start+end]
	if strings.Contains(body, "s.dedupeDiagnosticNotes(") ||
		strings.Contains(body, "s.lastDiagnostics") {
		t.Fatalf("hidden settle poll consumed model-facing diagnostic state:\n%s", body)
	}
	if !strings.Contains(body, "s.rememberSnapshot(app, snapshot)") {
		t.Fatal("hidden settle poll must still advance the action cache")
	}
}

func TestExpectIsOptionalAndMisuseIsACallError(t *testing.T) {
	// Without expect: fully backward compatible, no verification intent, and
	// the settle path stays disabled.
	plan, err := parseSettlePlan(map[string]any{"app": "gedit"})
	if err != nil {
		t.Fatalf("omitting expect must not be an error: %v", err)
	}
	if plan.expect != nil {
		t.Fatal("omitting expect must leave no verification intent")
	}
	if plan.settleMS != defaultSettleMS {
		t.Fatalf("default settle_ms = %d, want %d", plan.settleMS, defaultSettleMS)
	}

	// A malformed expect is a call error and must fail hard, which is distinct
	// from an unmet expectation.
	for _, bad := range []map[string]any{
		{"expect": "role=dialog"},
		{"expect": map[string]any{}},
		{"expect": map[string]any{"appearz": "role=dialog"}},
		{"expect": map[string]any{"appears": 3.0}},
		{"expect": map[string]any{"value_becomes": map[string]any{"element_index": "17"}}},
		{"expect": map[string]any{"value_becomes": map[string]any{"contains": "x"}}},
		{"expect": map[string]any{"value_becomes": map[string]any{"element_index": "17", "contains": ""}}},
		{"expect": map[string]any{"value_becomes": map[string]any{"element_index": "17", "contains": "a", "equals": "b"}}},
		{"expect": map[string]any{"state_becomes": map[string]any{"element_index": "17"}}},
		{"expect": map[string]any{"state_becomes": map[string]any{"state": "checked"}}},
		{"expect": map[string]any{"appears": "role=dialog"}, "settle_ms": -1.0},
		{"expect": map[string]any{"appears": "role=dialog"}, "settle_ms": float64(maxSettleMS + 1)},
	} {
		if _, err := parseSettlePlan(bad); err == nil {
			t.Fatalf("parseSettlePlan(%v) should fail", bad)
		}
	}

	// settle_ms = 0 is valid: only the snapshot taken right after the action is
	// checked, without waiting.
	plan, err = parseSettlePlan(map[string]any{
		"expect": map[string]any{"appears": "role=dialog"}, "settle_ms": 0.0,
	})
	if err != nil || plan.settleMS != 0 || plan.expect == nil {
		t.Fatalf("settle_ms=0 should be accepted: plan=%#v err=%v", plan, err)
	}
	plan, err = parseSettlePlan(map[string]any{
		"expect":    map[string]any{"appears": "role=dialog"},
		"settle_ms": float64(maxSettleMS),
	})
	if err != nil || plan.settleMS != 3000 {
		t.Fatalf("settle_ms=3000 should be accepted: plan=%#v err=%v", plan, err)
	}
}

func TestExpectSettlesByPollingAndReportsElapsed(t *testing.T) {
	spec, err := parseExpect(map[string]any{"expect": map[string]any{"appears": "role=dialog"}})
	if err != nil {
		t.Fatal(err)
	}
	plan := settlePlan{expect: spec, settleMS: 0}

	// Already satisfied right after the action: no extra walk is paid for, and
	// the elapsed time is reported as roughly zero.
	svc := newService()
	before := expectSnapshot(elementRecord{Index: 1, ControlType: "push button", Name: "Save"})
	ready := expectSnapshot(elementRecord{Index: 12, ControlType: "dialog", Name: "Save As"})
	outcome := svc.settle("gedit", plan, before, ready, nil, "")
	if !outcome.verified || outcome.polls != 0 {
		t.Fatalf("a match on the first read must not poll again: %#v", outcome)
	}
	if outcome.elapsedMS > 50 {
		t.Fatalf("a match on the first read should report about 0ms elapsed, got %dms", outcome.elapsedMS)
	}

	// Unsatisfied with a zero budget: the result is verified=false at once,
	// without blocking.
	outcome = svc.settle("gedit", plan, before, expectSnapshot(), nil, "")
	if outcome.verified || outcome.polls != 0 {
		t.Fatalf("a zero budget should give a negative verdict at once: %#v", outcome)
	}

	// predicate_verified:false is not isError, and it must not report delivery
	// as successful.
	actionNotes := []string{
		"[synthesis] Delivery to the intended target was not verified: input was emitted.",
	}
	note := expectNote(plan, outcome, axServiceRequest{Action: axClickCommand{}}, actionNotes)
	if !strings.Contains(note, `"predicate_verified":false`) ||
		!strings.Contains(note, `"settled_after_ms"`) ||
		!strings.Contains(note, "isError is false") ||
		!strings.Contains(note, "delivery to the intended target is unknown") {
		t.Fatalf("negative verdict note is missing required facts: %q", note)
	}
	if strings.Contains(note, "performed and delivered") {
		t.Fatalf("expect must not claim delivery when the action note says delivery was not verified: %q", note)
	}
	note = expectNote(
		plan,
		settleOutcome{
			snapshot:  &appSnapshot{SnapshotID: "settled-b"},
			verified:  true,
			evidence:  "dialog appeared",
			elapsedMS: 240,
		},
		axServiceRequest{Action: axClickCommand{}},
		actionNotes,
	)
	if !strings.Contains(note, `"predicate_verified":true`) ||
		!strings.Contains(note, "only the declared accessibility predicate") ||
		!strings.Contains(note, "240") {
		t.Fatalf("positive verdict note is missing required facts: %q", note)
	}
	if strings.Contains(note, "settled-b") ||
		strings.Contains(note, "observed_snapshot_id") {
		t.Fatalf("positive verdict leaked the internal snapshot identity: %q", note)
	}
	for _, overclaim := range []string{"effect is confirmed", "do not need a follow-up"} {
		if strings.Contains(strings.ToLower(note), overclaim) {
			t.Fatalf("predicate_verified:true must not be upgraded to a confirmed effect: %q", note)
		}
	}
}

func TestFollowupReadFailureIsVerificationIncompleteNotTimeout(t *testing.T) {
	plan := settlePlan{expect: &expectSpec{}, settleMS: 5000}
	outcome := settleOutcome{
		snapshot:               &appSnapshot{SnapshotID: "immediate-a"},
		verificationIncomplete: true,
		followupReadFailed:     true,
		evidence:               "follow-up read failed",
		elapsedMS:              120,
		polls:                  1,
	}
	note := expectNote(plan, outcome, axServiceRequest{Action: axClickCommand{}}, nil)
	for _, fragment := range []string{
		`"verification_complete":false`,
		`"followup_read_failed":true`,
		"verification is incomplete",
		"not a full-budget timeout verdict",
	} {
		if !strings.Contains(note, fragment) {
			t.Fatalf("incomplete verification note missing %q: %q", fragment, note)
		}
	}
	if strings.Contains(note, "within the 5000ms settle budget") {
		t.Fatalf("follow-up read failure was misreported as a full timeout: %q", note)
	}

	result := withActionOutcome(
		textResult("last snapshot", false),
		plan,
		outcome,
		outcome.snapshot,
		nil,
	)
	if result.StructuredContent["verification_complete"] != false ||
		result.StructuredContent["followup_read_failed"] != true {
		t.Fatalf("structured incomplete-verification facts missing: %#v",
			result.StructuredContent)
	}
}
