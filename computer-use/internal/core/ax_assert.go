package core

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/hqhq1025/hybrid-computer-use-oss/computer-use/internal/contract"
)

// Post-action readback is passive: it reports what changed after an action.
// Confirming a condition actively (has the save dialog closed?) otherwise
// means pulling another tree and comparing once, which gives the wrong answer
// when it runs a moment too early.
//
// An assertion is reliable because it re-checks within a timeout window. That matters more on the
// desktop, where UI changes raise no load event and a window can appear after
// the post-action settle wait has ended.
//
// Every poll is a full AT-SPI traversal, so the default timeout is 5 seconds
// with a 400ms interval, and the first sample always runs: one snapshot of a
// large app can take longer than the whole budget, and checking the deadline
// first would fail without sampling once.

type verifyGoal struct {
	state           string
	valueEquals     string
	valueEqualsKind string
	valueContains   string
	textContains    string
	exists          *bool
	// negated inverts a value or text predicate: wait until it is false.
	negated bool
}

func (g verifyGoal) empty() bool {
	return g.state == "" && g.valueEquals == "" && g.valueContains == "" &&
		g.textContains == "" && g.exists == nil
}

func (g verifyGoal) describe() string {
	parts := []string{}
	if g.exists != nil {
		if *g.exists {
			parts = append(parts, "to exist")
		} else {
			parts = append(parts, "to be gone")
		}
	}
	if g.state != "" {
		if bare, negated := strings.CutPrefix(g.state, "!"); negated {
			parts = append(parts, fmt.Sprintf("not to be %s", bare))
		} else {
			parts = append(parts, fmt.Sprintf("to be %s", g.state))
		}
	}
	if g.valueContains != "" {
		parts = append(parts, fmt.Sprintf("value to contain %q", g.valueContains))
	}
	if g.valueEquals != "" {
		parts = append(parts, fmt.Sprintf("value to equal %q", g.valueEquals))
	}
	if g.textContains != "" {
		parts = append(parts, fmt.Sprintf("text to contain %q", g.textContains))
	}
	if g.negated {
		return "not (" + strings.Join(parts, " and ") + ")"
	}
	return strings.Join(parts, " and ")
}

// check reports whether the goal holds and what was actually observed. The
// observation matters most: "assertion failed" carries no information, while
// "expected checked, observed enabled focused" does.
func (g verifyGoal) check(record *ElementRecord, lookupErr error) (bool, string) {
	if g.negated {
		positive := g
		positive.negated = false
		if record == nil {
			return false, "no element matches the selector"
		}
		ok, observed := positive.check(record, lookupErr)
		return !ok, observed
	}
	if record == nil {
		if g.exists != nil && !*g.exists {
			return true, "no element matches the selector"
		}
		reason := "no element matches the selector"
		if lookupErr != nil {
			reason = firstLine(lookupErr.Error())
		}
		return false, reason
	}
	observed := []string{fmt.Sprintf("resolved to %d %s", record.Index, describeRecord(record))}
	ok := true
	if g.exists != nil && !*g.exists {
		ok = false
		observed = append(observed, "but it is still present")
	}
	if g.state != "" {
		want := true
		bare := g.state
		if trimmed, negated := strings.CutPrefix(g.state, "!"); negated {
			want, bare = false, trimmed
		}
		has := containsFold(record.States, bare)
		if has != want {
			ok = false
		}
		// States already renders with brackets (`[enabled focused]` in the
		// tree); wrapping it again would print `[ [enabled focused]]`, so it
		// is shown exactly as the tree shows it.
		shown := strings.TrimSpace(record.States)
		if shown == "" {
			shown = "(no states reported)"
		}
		observed = append(observed, "states are "+shown)
	}
	if g.valueContains != "" {
		if !containsFold(record.Value, g.valueContains) {
			ok = false
		}
		observed = append(observed, fmt.Sprintf("value is %q", record.Value))
	}
	if g.valueEquals != "" {
		valueMatches := strings.TrimSpace(record.Value) ==
			strings.TrimSpace(g.valueEquals)
		switch g.valueEqualsKind {
		case "number":
			observed, observedOK := new(big.Rat).SetString(
				strings.TrimSpace(record.Value),
			)
			expected, expectedOK := new(big.Rat).SetString(
				strings.TrimSpace(g.valueEquals),
			)
			valueMatches = observedOK &&
				expectedOK &&
				observed.Cmp(expected) == 0
		case "boolean":
			observed, observedErr := strconv.ParseBool(
				strings.TrimSpace(record.Value),
			)
			expected, expectedErr := strconv.ParseBool(
				strings.TrimSpace(g.valueEquals),
			)
			valueMatches = observedErr == nil &&
				expectedErr == nil &&
				observed == expected
		}
		if !valueMatches {
			ok = false
		}
		observed = append(observed, fmt.Sprintf("value is %q", record.Value))
	}
	if g.textContains != "" {
		hit := containsFold(record.Name, g.textContains) ||
			containsFold(record.Description, g.textContains) ||
			containsFold(record.Value, g.textContains) ||
			containsFold(record.Placeholder, g.textContains)
		if !hit {
			ok = false
		}
		observed = append(observed, fmt.Sprintf("name=%q value=%q", record.Name, record.Value))
	}
	return ok, strings.Join(observed, ", ")
}

// selectorHint extracts the part of a selector that is usable for fuzzy
// matching: `push button "Save"` gives `Save` and `"Calculate"` gives
// `Calculate`. Without quotes the whole string counts, so a selector such as
// `heading Results` still yields candidates.
func selectorHint(selector string) string {
	if start := strings.Index(selector, "\""); start >= 0 {
		if end := strings.LastIndex(selector, "\""); end > start {
			return selector[start+1 : end]
		}
	}
	return strings.TrimSpace(selector)
}

func firstLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return text[:index]
	}
	return text
}

func numericElementIndex(selector string) bool {
	_, err := strconv.Atoi(strings.TrimSpace(selector))
	return err == nil
}

func axAssertionResult(
	text string,
	isError bool,
	status string,
	attempts int,
	stableMS int,
) ToolCallResult {
	result := TextResult(text, isError)
	result.StructuredContent = map[string]any{
		"assertion_sampled_live_ui": true,
		"assertion_status":          status,
		"assertion_passed":          status == "pass",
		"assertion_attempts":        attempts,
		"assertion_stable_ms":       stableMS,
		"poll_attempted":            attempts > 0,
		"task_effect_verified":      false,
	}
	return result
}

func assertionNegativeResult(
	text string,
	status string,
	attempts int,
	stableMS int,
) ToolCallResult {
	text = strings.TrimSpace(text) +
		"\nassertion_passed=false retry_same_request=false"
	result := axAssertionResult(
		text,
		false,
		status,
		attempts,
		stableMS,
	)
	result.StructuredContent["retry_same_request"] = false
	result.StructuredContent["continuation"] = map[string]any{
		"state":              "predicate_false",
		"retry_same_request": false,
		"route":              "inspect_or_continue",
	}
	return result
}

func assertionEvidenceScope(result ToolCallResult) string {
	if sampled, _ := result.StructuredContent["assertion_sampled_live_ui"].(bool); sampled {
		return "independent_ui_read"
	}
	return "error_only"
}

func isAXAppUnavailableResult(result ToolCallResult) bool {
	if !result.IsError || result.StructuredContent == nil {
		return false
	}
	code, _ := result.StructuredContent["failure_code"].(string)
	return code == "ax_app_unavailable"
}

func addAssertionSnapshotMetadata(
	result *ToolCallResult,
	snapshot *AppSnapshot,
) {
	if result == nil || snapshot == nil {
		return
	}
	addTreeProjectionMetadata(result, snapshot)
	if result.StructuredContent == nil {
		result.StructuredContent = map[string]any{}
	}
	result.StructuredContent["current_window_title"] = snapshot.WindowTitle
}

func snapshotForScopedLookup(snapshot *AppSnapshot) *AppSnapshot {
	if snapshot == nil ||
		normalizedAXScope(snapshot.Scope) != contract.AXScopeContent {
		return snapshot
	}
	clone := *snapshot
	clone.Elements = make([]ElementRecord, 0, snapshot.scopedCanonicalCount)
	for _, record := range snapshot.Elements {
		if recordInAXScope(snapshot, &record) {
			clone.Elements = append(clone.Elements, record)
		}
	}
	return &clone
}

func (s *Service) verify(
	app,
	selector string,
	displaySelector string,
	goal verifyGoal,
	timeoutMS,
	stableMS int,
	scope string,
	targetWindowHandle ...string,
) ToolCallResult {
	if app == "" {
		return TextResult("Missing required argument: app", true)
	}
	if strings.TrimSpace(selector) == "" {
		return TextResult("wait requires ref or selector.", true)
	}
	if strings.TrimSpace(displaySelector) == "" {
		displaySelector = selector
	}
	if numericElementIndex(selector) {
		if s.hasPendingFailedObservation(app) {
			return TextResult(
				"wait cannot reuse an element index after a stale-reference failure; "+
					"observe or find again and use a newly returned rN.", true)
		}
		if s.currentSnapshot(app) == nil {
			return TextResult(
				"wait cannot use an element index without a current snapshot; "+
					"observe or find first.", true)
		}
	} else if _, err := semanticTargetFromAddress(selector); err != nil {
		result := TextResult(
			"wait selector invalid: "+err.Error(), true)
		result.StructuredContent = map[string]any{
			"assertion_status":     "locator_invalid",
			"task_effect_verified": false,
		}
		return result
	}
	if goal.empty() {
		return TextResult("wait needs a predicate.", true)
	}
	lookup := func(snapshot *AppSnapshot) (*ElementRecord, error) {
		return lookupElementFor(snapshot, selector, "", "wait")
	}
	return s.verifyLookup(app, lookup, displaySelector, selectorHint(selector),
		numericElementIndex(selector), goal, timeoutMS, stableMS, scope, targetWindowHandle...)
}
