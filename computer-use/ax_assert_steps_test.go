package main

import (
	"errors"
	"strings"
	"testing"
)

func TestWindowTitlePollKeepsTitlesAcrossAFailedRead(t *testing.T) {
	poll := windowTitlePoll{lastTitles: []string{}}
	matched := poll.record("editor", "settings", &ProviderWindowResult{
		OK: true, WindowTitles: []string{"Main", "Settings"},
	}, nil)
	if len(matched) != 1 || !poll.sampledLiveUI {
		t.Fatalf("matched=%q poll=%#v", matched, poll)
	}
	poll.record("editor", "settings", nil, errors.New("bridge down"))
	if poll.lastError != "bridge down" || len(poll.lastTitles) != 2 {
		t.Fatalf("failed read lost the last complete titles: %#v", poll)
	}
	timeout := poll.timeoutResult("editor", "settings", true, 1, 2, 0)
	if !timeout.IsError ||
		timeout.StructuredContent["assertion_status"] != "observation_incomplete" ||
		timeout.StructuredContent["inventory_sample_complete"] != false {
		t.Fatalf("a timeout after a failed read must stay incomplete: %#v",
			timeout.StructuredContent)
	}
}

func TestWindowTitlePollCountsCompleteAppAbsenceAsALiveRead(t *testing.T) {
	poll := windowTitlePoll{lastTitles: []string{"Old"}}
	poll.record("a1", "settings", &ProviderWindowResult{
		Error: "gone",
		Failure: &ProviderFailure{
			Code: "stale_app_ref", Subject: "app", FactsComplete: true,
		},
	}, nil)
	if poll.lastError != "" || !poll.sampledLiveUI || poll.lastTitles != nil {
		t.Fatalf("complete app absence was not a live empty read: %#v", poll)
	}
	poll = windowTitlePoll{}
	poll.record("editor", "settings", &ProviderWindowResult{
		Error: "gone",
		Failure: &ProviderFailure{
			Code: "stale_app_ref", Subject: "app", FactsComplete: true,
		},
	}, nil)
	if poll.lastError == "" || poll.sampledLiveUI {
		t.Fatalf("absence of an app named without aN must stay an error: %#v", poll)
	}
}

func TestRefAssertionPollKeepsStatusOnSuccessfulResolve(t *testing.T) {
	poll := refAssertionPoll{lastStatus: "locator_missing"}
	sample := poll.classify(&ProviderAccessibilityResult{
		OK: true, ResolvedElement: &elementRecord{Name: "Save"},
	}, nil, verifyGoal{}, nil)
	if sample.record == nil || !sample.factsComplete ||
		poll.lastStatus != "locator_missing" || !poll.sampledLiveUI {
		t.Fatalf("sample=%#v poll=%#v", sample, poll)
	}
}

func TestRefAssertionPollUsesCanonicalProofOnlyForIncompleteReads(t *testing.T) {
	calls := 0
	proof := &axAssertionSnapshotProof{
		snapshot: &appSnapshot{}, record: &elementRecord{Name: "Save"},
	}
	probe := func() (*axAssertionSnapshotProof, toolCallResult) {
		calls++
		return proof, toolCallResult{}
	}
	incomplete := ProviderAccessibilityResult{
		Error:   "partial tree",
		Failure: &ProviderFailure{Code: "observation_incomplete"},
	}
	incomplete.prepareForService()
	poll := refAssertionPoll{lastStatus: "predicate_false"}
	sample := poll.classify(&incomplete, nil, verifyGoal{}, probe)
	if sample.proof != proof || sample.lookupErr != nil ||
		poll.lastStatus != "predicate_false" || sample.locatorFailed {
		t.Fatalf("canonical proof did not complete the sample: %#v %#v", sample, poll)
	}
	ambiguous := ProviderAccessibilityResult{
		Error: "two matches",
		Failure: &ProviderFailure{
			Code: "semantic_target_ambiguous", FactsComplete: true,
		},
	}
	ambiguous.prepareForService()
	sample = poll.classify(&ambiguous, nil, verifyGoal{}, probe)
	if !sample.locatorFailed || sample.proof != nil ||
		poll.lastStatus != "locator_ambiguous" || calls != 1 {
		t.Fatalf("ambiguity must stop the wait without a fallback read: %#v %#v calls=%d",
			sample, poll, calls)
	}
}

func TestWaitChangeReportComparesWindowsByHandle(t *testing.T) {
	first := verifyLookupBaseline{window: "Doc", handle: "h1", count: 0}
	e := &toolError{}
	addWaitChangeReport(e, first,
		&appSnapshot{WindowTitle: "Doc", WindowHandle: "h2"}, false)
	if len(e.fields) != 1 || e.fields[0][0] != "Window changed" {
		t.Fatalf("a same-titled replacement window was not reported: %#v", e.fields)
	}
	e = &toolError{}
	addWaitChangeReport(e, first,
		&appSnapshot{WindowTitle: "Doc", WindowHandle: "h1"}, true)
	if len(e.fields) != 1 || e.fields[0][0] != "Nothing changed" ||
		!strings.Contains(e.fields[0][1], "ambiguous") {
		t.Fatalf("unchanged ambiguous wait = %#v", e.fields)
	}
}
