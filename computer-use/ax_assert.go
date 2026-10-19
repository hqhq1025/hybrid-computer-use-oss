package main

import (
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
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
func (g verifyGoal) check(record *elementRecord, lookupErr error) (bool, string) {
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
) toolCallResult {
	result := textResult(text, isError)
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
) toolCallResult {
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

func assertionEvidenceScope(result toolCallResult) string {
	if sampled, _ := result.StructuredContent["assertion_sampled_live_ui"].(bool); sampled {
		return "independent_ui_read"
	}
	return "error_only"
}

// verifyWindowTitle polls the titles of every top-level window of app:
// documents, dialogs and popups alike. A title predicate asks whether the
// application shows such a window, so it is never narrowed to the window a
// batch acted in; that window is often the one the action closed or the one a
// new dialog covers.
func (s *service) verifyWindowTitle(
	app,
	titleSubstring string,
	expected bool,
	timeoutMS,
	stableMS int,
) toolCallResult {
	return s.verifyWindowTitleWithProbe(
		app,
		titleSubstring,
		expected,
		timeoutMS,
		stableMS,
		s.callProviderWindowList,
	)
}

func (s *service) verifyWindowTitleWithProbe(
	app,
	titleSubstring string,
	expected bool,
	timeoutMS,
	stableMS int,
	probe func(ProviderWindowListRequest) (*ProviderWindowResult, error),
) toolCallResult {
	if timeoutMS <= 0 {
		timeoutMS = 5000
	}
	deadline := time.Now().Add(time.Duration(timeoutMS) * time.Millisecond)
	var trueSince time.Time
	attempts := 0
	poll := windowTitlePoll{lastTitles: []string{}}
	for {
		attempts++
		operation := axServiceRequest{
			Operation: axServiceWindowList,
			App:       app,
		}
		// Closing an application's last window can retire its aN before this
		// poll; the read then names the same exact process.
		if err := s.bindPredicateAppIdentity(&operation); err != nil {
			return axAssertionResult(
				"wait: application target: "+err.Error(),
				true,
				"invalid_target",
				attempts,
				stableMS,
			)
		}
		response, err := probe(
			providerWindowListRequestFromOperation(operation),
		)
		matched := poll.record(app, titleSubstring, response, err)
		predicate := len(matched) > 0
		if !expected {
			predicate = !predicate && poll.lastError == ""
		}
		if predicate {
			now := time.Now()
			if trueSince.IsZero() {
				trueSince = now
			}
			stableFor := int(now.Sub(trueSince) / time.Millisecond)
			if stableMS == 0 || stableFor >= stableMS {
				return poll.passResult(
					app, titleSubstring, expected, matched, attempts, stableMS,
				)
			}
		} else {
			trueSince = time.Time{}
		}
		if !time.Now().Before(deadline) {
			return poll.timeoutResult(
				app, titleSubstring, expected, timeoutMS, attempts, stableMS,
			)
		}
		time.Sleep(400 * time.Millisecond)
	}
}

// windowTitlePoll keeps what the window-title polls have seen so far. The
// titles and the live-UI flag survive a failed poll; the error is replaced by
// every poll.
type windowTitlePoll struct {
	lastTitles    []string
	lastError     string
	sampledLiveUI bool
}

// record folds one window-list response into the poll and returns the
// titles that contain titleSubstring.
func (poll *windowTitlePoll) record(
	app,
	titleSubstring string,
	response *ProviderWindowResult,
	err error,
) []string {
	matched := []string{}
	if err != nil {
		poll.lastError = err.Error()
	} else if !response.OK {
		// An application that no longer exists, read from a complete
		// inventory, has no window with any title.
		completeAppAbsence := isShortAXAppRef(app) &&
			response.Failure != nil &&
			response.Failure.FactsComplete &&
			strings.EqualFold(response.Failure.Subject, "app") &&
			(response.Failure.Code == "stale_target" ||
				response.Failure.Code == "stale_app_ref" ||
				response.Failure.Code == "ax_app_unavailable")
		if completeAppAbsence {
			poll.lastError = ""
			poll.sampledLiveUI = true
			poll.lastTitles = nil
		} else {
			poll.lastError = response.Error
		}
	} else {
		poll.lastError = ""
		poll.sampledLiveUI = true
		poll.lastTitles = append([]string(nil), response.WindowTitles...)
		for _, title := range response.WindowTitles {
			if containsFold(title, titleSubstring) {
				matched = append(matched, title)
			}
		}
	}
	return matched
}

func (poll *windowTitlePoll) passResult(
	app,
	titleSubstring string,
	expected bool,
	matched []string,
	attempts,
	stableMS int,
) toolCallResult {
	status := "present"
	observed := matched
	if !expected {
		status = "absent"
		observed = poll.lastTitles
	}
	result := axAssertionResult(
		fmt.Sprintf(
			"PASS: application %q window title containing %q is %s. "+
				"Current window titles: %q.",
			app,
			titleSubstring,
			status,
			observed,
		),
		false,
		"pass",
		attempts,
		stableMS,
	)
	result.StructuredContent["window_titles"] = poll.lastTitles
	result.StructuredContent["window_title_matches"] = matched
	return result
}

// timeoutResult reports a predicate that never held. Only a timeout after a
// complete live read is a negative answer; any other timeout is incomplete
// observation.
func (poll *windowTitlePoll) timeoutResult(
	app,
	titleSubstring string,
	expected bool,
	timeoutMS,
	attempts,
	stableMS int,
) toolCallResult {
	expectation := "appear"
	if !expected {
		expectation = "disappear"
	}
	text := fmt.Sprintf(
		"wait false: no window title containing %q satisfied the "+
			"requested %s predicate within %dms.",
		titleSubstring,
		expectation,
		timeoutMS,
	)
	if poll.lastError != "" {
		text += "\nLast AX application lookup: " + firstLine(poll.lastError)
	}
	if len(poll.lastTitles) > 0 {
		limit := min(5, len(poll.lastTitles))
		text += fmt.Sprintf(
			"\nCurrent window candidates for %q: %q.",
			app,
			poll.lastTitles[:limit],
		)
	}
	result := toolCallResult{}
	if poll.sampledLiveUI && poll.lastError == "" {
		result = assertionNegativeResult(
			text,
			"window_title_timeout",
			attempts,
			stableMS,
		)
	} else {
		result = axAssertionResult(
			text,
			true,
			"observation_incomplete",
			attempts,
			stableMS,
		)
	}
	result.StructuredContent["assertion_sampled_live_ui"] =
		poll.sampledLiveUI
	result.StructuredContent["inventory_sample_complete"] =
		poll.sampledLiveUI && poll.lastError == ""
	result.StructuredContent["window_titles"] = poll.lastTitles
	return result
}

func isAXAppUnavailableResult(result toolCallResult) bool {
	if !result.IsError || result.StructuredContent == nil {
		return false
	}
	code, _ := result.StructuredContent["failure_code"].(string)
	return code == "ax_app_unavailable"
}

func addAssertionSnapshotMetadata(
	result *toolCallResult,
	snapshot *appSnapshot,
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

func snapshotForScopedLookup(snapshot *appSnapshot) *appSnapshot {
	if snapshot == nil ||
		normalizedAXScope(snapshot.scope) != axScopeContent {
		return snapshot
	}
	clone := *snapshot
	clone.Elements = make([]elementRecord, 0, snapshot.scopedCanonicalCount)
	for _, record := range snapshot.Elements {
		if recordInAXScope(snapshot, &record) {
			clone.Elements = append(clone.Elements, record)
		}
	}
	return &clone
}

func (s *service) verifyResolvedAXRef(
	app,
	displayRef string,
	target *semanticTarget,
	goal verifyGoal,
	timeoutMS,
	stableMS int,
) toolCallResult {
	var entry *AXRefEntry
	if resolved, err := s.refRegistry.Resolve(displayRef, ""); err == nil {
		entry = resolved
		s.authorizeProviderRebind(app, entry, target)
	}
	var snapshotProbe axAssertionSnapshotProbe
	if entry != nil &&
		strings.TrimSpace(entry.ProviderBusName) != "" &&
		strings.TrimSpace(entry.ProviderObjectPath) != "" &&
		strings.TrimSpace(entry.ProcessGeneration) != "" {
		snapshotProbe = func() (*axAssertionSnapshotProof, toolCallResult) {
			noScreenshot := false
			snapshot, notes, result := s.readProjectedSnapshot(
				app,
				axServiceRequest{
					Operation:          axServiceObserve,
					App:                app,
					IncludeScreenshot:  &noScreenshot,
					TargetWindowHandle: entry.WindowHandle,
					Scope:              axScopeWindow,
					TreeView:           treeViewWorking,
				},
			)
			if result.IsError {
				return nil, result
			}
			record, err := s.exactRefRecordFromSnapshot(
				snapshot,
				displayRef,
				entry,
			)
			if err != nil {
				return nil, textResult(err.Error(), true).
					withEvidenceScope("error_only")
			}
			return &axAssertionSnapshotProof{
				snapshot: snapshot,
				notes:    notes,
				record:   record,
			}, toolCallResult{}
		}
	}
	return s.verifyResolvedAXRefWithProbes(
		app,
		displayRef,
		target,
		goal,
		timeoutMS,
		stableMS,
		s.callProviderAction,
		snapshotProbe,
	)
}

func (s *service) exactRefRecordFromSnapshot(
	snapshot *appSnapshot,
	displayRef string,
	expected *AXRefEntry,
) (*elementRecord, error) {
	if s == nil || snapshot == nil || snapshot.axSnapshot == nil ||
		expected == nil {
		return nil, errors.New(
			"canonical assertion fallback has incomplete identity evidence",
		)
	}
	current, err := s.refRegistry.Resolve(displayRef, "")
	if err != nil {
		return nil, err
	}
	if current.Ref != expected.Ref ||
		current.IdentityKey != expected.IdentityKey ||
		current.LocatorKind != expected.LocatorKind ||
		current.AppRef != expected.AppRef ||
		current.AppIdentity != expected.AppIdentity ||
		current.LogicalWindowRef != expected.LogicalWindowRef ||
		current.WindowHandle != expected.WindowHandle {
		return nil, errors.New(
			"stale_element_ref: the canonical snapshot did not preserve the exact ref identity",
		)
	}
	if snapshot.WindowHandle != expected.WindowHandle ||
		snapshot.publicWindowRef != expected.LogicalWindowRef ||
		snapshot.publicAppRef != expected.AppRef {
		return nil, errors.New(
			"stale_window_ref: the canonical snapshot belongs to a different owner",
		)
	}
	var matches []*AXNode
	var visit func([]*AXNode)
	visit = func(nodes []*AXNode) {
		for _, node := range nodes {
			if node != nil && node.Ref == displayRef {
				matches = append(matches, node)
			}
			if node != nil {
				visit(node.Children)
			}
		}
	}
	visit(snapshot.axSnapshot.CanonicalRoots)
	if len(matches) != 1 || matches[0].SourceRecord == nil {
		return nil, errors.New(
			"observation_incomplete: the canonical snapshot did not contain exactly one current ref target",
		)
	}
	node := matches[0]
	if node.LocatorKind != expected.LocatorKind {
		return nil, errors.New(
			"stale_element_ref: the canonical ref target changed locator identity",
		)
	}
	record := node.SourceRecord
	if expected.LocatorKind == AXLocatorProviderBound {
		if current.ProviderBusName != expected.ProviderBusName ||
			current.ProviderObjectPath != expected.ProviderObjectPath ||
			current.ProcessGeneration != expected.ProcessGeneration ||
			current.ProviderIdentityScope != expected.ProviderIdentityScope ||
			record.ProviderBusName != expected.ProviderBusName ||
			record.ProviderObjectPath != expected.ProviderObjectPath ||
			record.ProcessGeneration != expected.ProcessGeneration ||
			record.ProviderIdentityScope != expected.ProviderIdentityScope {
			return nil, errors.New(
				"stale_element_ref: the canonical ref target changed provider identity",
			)
		}
	} else if strings.TrimSpace(current.SemanticTarget) !=
		strings.TrimSpace(expected.SemanticTarget) ||
		current.Role != expected.Role ||
		comparableTreeText(current.Name) != comparableTreeText(expected.Name) ||
		strings.TrimSpace(node.SemanticTarget) != strings.TrimSpace(expected.SemanticTarget) ||
		node.Role != expected.Role ||
		comparableTreeText(node.Name) != comparableTreeText(expected.Name) ||
		!runtimePathMatchesEntry(expected, node.RuntimeID) {
		return nil, errors.New(
			"stale_element_ref: the canonical ref target changed semantic lineage",
		)
	}
	return record, nil
}

type axAssertionSnapshotProof struct {
	snapshot *appSnapshot
	notes    []string
	record   *elementRecord
}

type axAssertionSnapshotProbe func() (
	*axAssertionSnapshotProof,
	toolCallResult,
)

func (s *service) verifyResolvedAXRefWithProbes(
	app,
	displayRef string,
	target *semanticTarget,
	goal verifyGoal,
	timeoutMS,
	stableMS int,
	probe func(ProviderActionRequest) (*ProviderAccessibilityResult, error),
	snapshotProbe axAssertionSnapshotProbe,
) toolCallResult {
	if target == nil {
		return axAssertionResult(
			"wait: the ref has no live target identity.",
			true, "invalid_target", 0, stableMS,
		)
	}
	if timeoutMS <= 0 {
		timeoutMS = 5000
	}
	deadline := time.Now().Add(time.Duration(timeoutMS) * time.Millisecond)
	var trueSince time.Time
	stableObserved := ""
	lastObserved := ""
	poll := refAssertionPoll{lastStatus: "predicate_false"}
	for attempt := 1; ; attempt++ {
		operation := axServiceRequest{
			Action:             axResolveTargetCommand{},
			App:                app,
			TargetWindowHandle: target.WindowHandle,
			SemanticTarget:     target,
			SourceRef:          displayRef,
		}
		if err := s.bindRuntimeAppIdentity(&operation); err != nil {
			return axAssertionResult(
				"wait: "+err.Error(),
				true, "invalid_target", attempt, stableMS,
			)
		}
		response, err := probe(
			providerActionRequestFromOperation(operation),
		)
		if response != nil {
			response.prepareForService()
		}
		sample := poll.classify(response, err, goal, snapshotProbe)
		if sample.locatorFailed {
			result := axAssertionResult(
				fmt.Sprintf(
					"wait %s: %s",
					poll.lastStatus,
					firstLine(sample.lookupErr.Error()),
				),
				true, poll.lastStatus, attempt, stableMS,
			)
			result.StructuredContent["assertion_sampled_live_ui"] =
				poll.sampledLiveUI
			return result
		}
		if !sample.factsComplete && sample.record == nil {
			lastObserved = "the live AX target could not be read completely"
			trueSince = time.Time{}
			stableObserved = ""
		} else {
			ok, observed := goal.check(sample.record, sample.lookupErr)
			lastObserved = observed
			if ok {
				now := time.Now()
				if stableMS == 0 {
					result := axAssertionResult(
						fmt.Sprintf(
							"PASS: %s %s. %s",
							displayRef, goal.describe(), observed,
						),
						false, "pass", attempt, stableMS,
					)
					return withCanonicalFallbackProof(result, sample.proof)
				}
				if trueSince.IsZero() || stableObserved != observed {
					trueSince = now
					stableObserved = observed
				}
				if int(now.Sub(trueSince)/time.Millisecond) >= stableMS {
					result := axAssertionResult(
						fmt.Sprintf(
							"PASS: %s %s and remained continuously true for %dms. %s",
							displayRef,
							goal.describe(),
							int(now.Sub(trueSince)/time.Millisecond),
							observed,
						),
						false, "pass", attempt, stableMS,
					)
					return withCanonicalFallbackProof(result, sample.proof)
				}
			} else {
				trueSince = time.Time{}
				stableObserved = ""
			}
		}
		if !time.Now().Before(deadline) {
			text := fmt.Sprintf(
				"wait %s: %s did not satisfy %s within %dms. Last observation: %s",
				poll.lastStatus,
				displayRef,
				goal.describe(),
				timeoutMS,
				lastObserved,
			)
			result := toolCallResult{}
			if poll.sampledLiveUI && poll.lastStatus == "predicate_false" {
				result = assertionNegativeResult(
					text, poll.lastStatus, attempt, stableMS,
				)
			} else {
				result = axAssertionResult(
					text, true, poll.lastStatus, attempt, stableMS,
				)
			}
			result.StructuredContent["assertion_sampled_live_ui"] =
				poll.sampledLiveUI
			return result
		}
		time.Sleep(400 * time.Millisecond)
	}
}

// refAssertionPoll carries the status of the latest classified sample and
// whether any sample read the live UI. A successful resolve leaves the
// status of the previous sample in place.
type refAssertionPoll struct {
	lastStatus    string
	sampledLiveUI bool
}

// refAssertionSample is one resolver read of the asserted ref.
type refAssertionSample struct {
	record        *elementRecord
	lookupErr     error
	factsComplete bool
	// proof is set when a canonical snapshot completed the sample.
	proof *axAssertionSnapshotProof
	// locatorFailed means the ref is ambiguous, stale or invalid, so
	// further polls cannot establish the predicate.
	locatorFailed bool
}

// classify turns one resolver response into the element the goal is
// checked against and updates the poll status.
func (poll *refAssertionPoll) classify(
	response *ProviderAccessibilityResult,
	err error,
	goal verifyGoal,
	snapshotProbe axAssertionSnapshotProbe,
) refAssertionSample {
	sample := refAssertionSample{factsComplete: true}
	switch {
	case err != nil:
		sample.lookupErr = err
		sample.factsComplete = false
		poll.lastStatus = "observation_incomplete"
	case response == nil:
		sample.lookupErr = errors.New("empty accessibility resolver response")
		sample.factsComplete = false
		poll.lastStatus = "observation_incomplete"
	case response.OK:
		sample.record = response.ResolvedElement
		poll.sampledLiveUI = true
		if sample.record == nil {
			sample.lookupErr = errors.New("live resolver returned no element")
			sample.factsComplete = false
			poll.lastStatus = "observation_incomplete"
		}
	default:
		poll.classifyFailure(&sample, *response, goal, snapshotProbe)
	}
	return sample
}

func (poll *refAssertionPoll) classifyFailure(
	sample *refAssertionSample,
	response ProviderAccessibilityResult,
	goal verifyGoal,
	snapshotProbe axAssertionSnapshotProbe,
) {
	sample.lookupErr = errors.New(firstNonEmpty(
		publicRuntimeFailureText(response),
		response.Error,
		response.FailureCode,
	))
	sample.factsComplete = response.FailureFactsComplete
	switch response.FailureCode {
	case "semantic_target_not_found":
		poll.lastStatus = "locator_missing"
	case "semantic_target_ambiguous":
		poll.lastStatus = "locator_ambiguous"
	case "stale_element_ref":
		poll.lastStatus = "locator_stale"
	case "observation_incomplete":
		poll.lastStatus = "observation_incomplete"
	default:
		poll.lastStatus = firstNonEmpty(
			response.FailureCode, "observation_incomplete",
		)
	}
	// A ref-scoped negative existence assertion asks whether the exact
	// previously issued object still exists, not whether another
	// role/name-equivalent object is present. A complete live inventory
	// may prove that the application generation, logical window, or
	// provider-bound element has disappeared. Treat that as a successful
	// absence sample. Incomplete reads remain unknown, and ambiguity
	// remains an error.
	if goal.isNegativeExistenceOnly() &&
		sample.factsComplete &&
		completeRefAbsenceFailure(response) {
		sample.record = nil
		poll.sampledLiveUI = true
		poll.lastStatus = "predicate_false"
		return
	}
	if response.FailureCode == "observation_incomplete" &&
		snapshotProbe != nil {
		// The resolver probe began before the assertion deadline, but a
		// provider tree scan may consume that entire interval before it
		// reports incomplete evidence. Complete that one compound sample
		// with a canonical read instead of returning unknown immediately
		// before the final observation proves the same ref. The fallback
		// remains bounded to one call for this polling attempt.
		if proof, fallbackResult := snapshotProbe(); proof != nil &&
			!fallbackResult.IsError &&
			proof.snapshot != nil &&
			proof.record != nil {
			sample.record = proof.record
			sample.lookupErr = nil
			sample.factsComplete = true
			poll.sampledLiveUI = true
			poll.lastStatus = "predicate_false"
			sample.proof = proof
		}
	}
	sample.locatorFailed = response.FailureCode == "semantic_target_ambiguous" ||
		response.FailureCode == "stale_element_ref" ||
		response.FailureCode == "semantic_target_invalid"
}

// withCanonicalFallbackProof attaches the canonical snapshot that completed
// a passing sample, so the caller can show the tree the pass was read from.
func withCanonicalFallbackProof(
	result toolCallResult,
	proof *axAssertionSnapshotProof,
) toolCallResult {
	if proof != nil {
		result.axProofSnapshot = proof.snapshot
		result.axProofNotes = append(
			[]string(nil),
			proof.notes...,
		)
		result.StructuredContent["assertion_canonical_fallback"] =
			true
	}
	return result
}

func (g verifyGoal) isNegativeExistenceOnly() bool {
	return g.exists != nil && !*g.exists &&
		g.state == "" &&
		g.valueEquals == "" &&
		g.valueContains == "" &&
		g.textContains == ""
}

func completeRefAbsenceFailure(response ProviderAccessibilityResult) bool {
	if !response.FailureFactsComplete {
		return false
	}
	switch response.FailureCode {
	case "semantic_target_not_found", "stale_element_ref":
		return true
	case "stale_target", "stale_app_ref", "stale_window":
		switch strings.ToLower(strings.TrimSpace(response.FailureSubject)) {
		case "app", "window", "element":
			return true
		}
	}
	return false
}

func (s *service) verify(
	app,
	selector string,
	displaySelector string,
	goal verifyGoal,
	timeoutMS,
	stableMS int,
	scope string,
	targetWindowHandle ...string,
) toolCallResult {
	if app == "" {
		return textResult("Missing required argument: app", true)
	}
	if strings.TrimSpace(selector) == "" {
		return textResult("wait requires ref or selector.", true)
	}
	if strings.TrimSpace(displaySelector) == "" {
		displaySelector = selector
	}
	if numericElementIndex(selector) {
		if s.hasPendingFailedObservation(app) {
			return textResult(
				"wait cannot reuse an element index after a stale-reference failure; "+
					"observe or find again and use a newly returned rN.", true)
		}
		if s.currentSnapshot(app) == nil {
			return textResult(
				"wait cannot use an element index without a current snapshot; "+
					"observe or find first.", true)
		}
	} else if _, err := semanticTargetFromAddress(selector); err != nil {
		result := textResult(
			"wait selector invalid: "+err.Error(), true)
		result.StructuredContent = map[string]any{
			"assertion_status":     "locator_invalid",
			"task_effect_verified": false,
		}
		return result
	}
	if goal.empty() {
		return textResult("wait needs a predicate.", true)
	}
	lookup := func(snapshot *appSnapshot) (*elementRecord, error) {
		return lookupElementFor(snapshot, selector, "", "wait")
	}
	return s.verifyLookup(app, lookup, displaySelector, selectorHint(selector),
		numericElementIndex(selector), goal, timeoutMS, stableMS, scope, targetWindowHandle...)
}

// axElementLookup finds the one element a predicate tests in a snapshot.
type axElementLookup func(*appSnapshot) (*elementRecord, error)

// verifyLookup polls the application that owns the read window until goal
// holds for the element lookup finds, or timeoutMS passes. A selector until
// and a wait step with a find query share it; only the lookup differs.
// numeric marks an element-index lookup, which is never searched in other
// windows. hint names what to list as the closest elements on a miss.
func (s *service) verifyLookup(
	app string,
	lookup axElementLookup,
	displaySelector, hint string,
	numeric bool,
	goal verifyGoal,
	timeoutMS,
	stableMS int,
	scope string,
	targetWindowHandle ...string,
) toolCallResult {
	if timeoutMS <= 0 {
		timeoutMS = 5000
	}

	noScreenshot := false
	deadline := time.Now().Add(time.Duration(timeoutMS) * time.Millisecond)
	attempts := []string{}
	// Whether the UI changed during the wait is the most useful fact on
	// failure. Reporting only that the requested condition never held leaves
	// the agent unable to tell whether to keep waiting or whether its last
	// action never took effect, so it keeps retrying with longer timeouts.
	var first verifyLookupBaseline
	var trueSince time.Time
	stableObserved := ""
	for attempt := 1; ; attempt++ {
		request := axServiceRequest{
			Operation:         axServiceObserve,
			App:               app,
			IncludeScreenshot: &noScreenshot,
			Scope:             normalizedAXScope(scope),
		}
		if len(targetWindowHandle) > 0 {
			request.TargetWindowHandle = strings.TrimSpace(targetWindowHandle[0])
		}
		read, result := s.readUntilApplication(request)
		if result.IsError {
			if isAXAppUnavailableResult(result) {
				elapsed := timeoutMS - int(time.Until(deadline).Milliseconds())
				attempts = append(attempts, fmt.Sprintf(
					"attempt %d at +%dms — application is not yet visible on the AX bus",
					attempt,
					elapsed,
				))
				if !time.Now().Before(deadline) {
					return appUnavailableWaitResult(
						app, timeoutMS, attempts, result, attempt, stableMS,
					)
				}
				time.Sleep(400 * time.Millisecond)
				continue
			}
			return result
		}
		// Resolve with lookupElementFor rather than lookupBySelectorFor: it
		// accepts numeric indices as well as selectors. A selector-only lookup
		// would treat an index such as "62" as a selector and keep reporting
		// no match while the element is present, a false negative that is
		// worse than no assertion.
		snapshot := read.primary
		if attempt == 1 {
			first = verifyLookupBaseline{
				window: snapshot.WindowTitle,
				handle: snapshot.WindowHandle,
				count:  scopedCanonicalElementCount(snapshot),
			}
		}
		record, lookupErr, incomplete := s.lookupAcrossWindows(
			read, lookup, numeric,
		)
		ok, observed := goal.check(record, lookupErr)
		if incomplete {
			// No match proves nothing while a visible window was unread.
			ok = false
			observed += "; a visible window of the application could not be read"
		}
		selectorAmbiguous := lookupErr != nil &&
			strings.Contains(strings.ToLower(lookupErr.Error()), "ambiguous")
		attempts = append(attempts, fmt.Sprintf("attempt %d at +%dms — %s",
			attempt, timeoutMS-int(time.Until(deadline).Milliseconds()), observed))
		if attempt == 1 && lookupErr != nil &&
			(numeric || selectorAmbiguous) {
			return locatorFailureWaitResult(
				observed, selectorAmbiguous, snapshot, attempt, stableMS,
			)
		}
		if ok {
			now := time.Now()
			if stableMS == 0 {
				return axAssertionResult(fmt.Sprintf(
					"PASS: %s %s.\n%s\nAssertions do not change the UI; this is a read of the live tree, not a replay of an earlier snapshot.",
					displaySelector, goal.describe(), observed),
					false, "pass", attempt, stableMS)
			}
			if trueSince.IsZero() || stableObserved != observed {
				trueSince = now
				stableObserved = observed
			}
			stableFor := int(now.Sub(trueSince) / time.Millisecond)
			if stableFor >= stableMS {
				return axAssertionResult(fmt.Sprintf(
					"PASS: %s %s and remained continuously true for %dms.\n%s\nAssertions do not change the UI; this is a read of the live tree, not a replay of an earlier snapshot.",
					displaySelector, goal.describe(), stableFor, observed),
					false, "pass", attempt, stableMS)
			}
			observed = fmt.Sprintf(
				"%s; predicate true for %dms/%dms required stability",
				observed,
				stableFor,
				stableMS,
			)
		} else {
			trueSince = time.Time{}
			stableObserved = ""
		}
		// Sample before checking the deadline so even the smallest budget gets
		// one complete live-state evaluation.
		if !time.Now().Before(deadline) {
			return verifyLookupTimeoutResult(verifyLookupTimeout{
				displaySelector:   displaySelector,
				hint:              hint,
				goal:              goal,
				timeoutMS:         timeoutMS,
				stableMS:          stableMS,
				attempt:           attempt,
				attempts:          attempts,
				first:             first,
				snapshot:          snapshot,
				windowRef:         read.windowRef,
				lookupErr:         lookupErr,
				ok:                ok,
				incomplete:        incomplete,
				selectorAmbiguous: selectorAmbiguous,
			})
		}
		time.Sleep(400 * time.Millisecond)
	}
}

// verifyLookupBaseline is the bound window as the first poll saw it; the
// timeout report compares the last poll against it.
type verifyLookupBaseline struct {
	window string
	handle string
	count  int
}

func appUnavailableWaitResult(
	app string,
	timeoutMS int,
	attempts []string,
	result toolCallResult,
	attempt,
	stableMS int,
) toolCallResult {
	text := fmt.Sprintf(
		"app_unavailable: %q did not become visible on the AX bus within %dms.\n",
		app,
		timeoutMS,
	)
	for _, line := range attempts {
		text += "  - " + line + "\n"
	}
	text += "Last AX availability result: " +
		firstLine(resultText(result))
	return axAssertionResult(
		strings.TrimSpace(text),
		true,
		"app_unavailable",
		attempt,
		stableMS,
	)
}

// lookupAcrossWindows runs lookup on the bound window and, when that finds
// no unambiguous miss to report, on the application's other visible
// windows. incomplete is true when nothing matched and some window could not
// be read, so the miss proves nothing.
func (s *service) lookupAcrossWindows(
	read *untilApplicationRead,
	lookup axElementLookup,
	numeric bool,
) (*elementRecord, error, bool) {
	record, lookupErr := lookup(read.lookup)
	incomplete := false
	if record == nil && !numeric &&
		!strings.Contains(strings.ToLower(fmt.Sprint(lookupErr)), "ambiguous") {
		// The bound window has no match: the effect may be a dialog
		// or popup in another window of the application.
		others, complete := s.readOtherWindows(read)
		if otherRecord, otherErr := lookup(others); otherRecord != nil ||
			strings.Contains(strings.ToLower(fmt.Sprint(otherErr)), "ambiguous") {
			record, lookupErr = otherRecord, otherErr
		}
		incomplete = !complete && record == nil
	}
	return record, lookupErr, incomplete
}

// locatorFailureWaitResult stops a wait whose first poll shows the locator
// cannot name one current element: polling it would never answer.
func locatorFailureWaitResult(
	observed string,
	selectorAmbiguous bool,
	snapshot *appSnapshot,
	attempt,
	stableMS int,
) toolCallResult {
	status := "locator_stale"
	if selectorAmbiguous {
		status = "locator_ambiguous"
	}
	result := axAssertionResult(
		fmt.Sprintf(
			"wait %s: %s\nThis locator did not identify one "+
				"current element, so polling it would not establish the "+
				"requested predicate. Obtain a fresh ref or use a more "+
				"specific structured selector.",
			status, observed,
		),
		true,
		status,
		attempt,
		stableMS,
	)
	addAssertionSnapshotMetadata(&result, snapshot)
	return result
}

// verifyLookupTimeout is the state of the last poll of a wait that timed
// out.
type verifyLookupTimeout struct {
	displaySelector   string
	hint              string
	goal              verifyGoal
	timeoutMS         int
	stableMS          int
	attempt           int
	attempts          []string
	first             verifyLookupBaseline
	snapshot          *appSnapshot
	windowRef         string
	lookupErr         error
	ok                bool
	incomplete        bool
	selectorAmbiguous bool
}

func verifyLookupTimeoutResult(t verifyLookupTimeout) toolCallResult {
	snapshot := t.snapshot
	e := &toolError{tool: "wait", summary: fmt.Sprintf(
		"expected %s %s, and it did not become true within %dms.",
		t.displaySelector, t.goal.describe(), t.timeoutMS)}
	e.add("Selector", strconv.Quote(t.displaySelector))
	e.add("Snapshot", snapshotAge(snapshot))
	addWaitChangeReport(e, t.first, snapshot, t.selectorAmbiguous)
	for _, line := range t.attempts {
		e.log = append(e.log, "  - "+line)
	}
	addWaitNextStep(e, t)
	status := "locator_missing"
	if t.lookupErr == nil {
		status = "predicate_false"
	}
	if t.ok && t.stableMS > 0 {
		status = "predicate_unstable"
		e.next = fmt.Sprintf(
			"the predicate became true but did not remain unchanged for "+
				"the requested %dms stability window before timeout. "+
				"Increase timeout_ms, reduce stable_ms, or assert a more "+
				"specific next-state condition.",
			t.stableMS,
		)
	}
	result := toolCallResult{}
	lookupErr := t.lookupErr
	// For an exists predicate a target that never appeared is the
	// answer: the predicate is false, not unknown.
	goal := t.goal
	existsOnly := goal.exists != nil && *goal.exists && goal.state == "" &&
		goal.valueEquals == "" && goal.valueContains == "" && goal.textContains == ""
	if status == "locator_missing" && existsOnly {
		status = "predicate_false"
		lookupErr = nil
	}
	if t.incomplete {
		status = "observation_incomplete"
	}
	if !t.incomplete && lookupErr == nil &&
		(status == "predicate_false" || status == "predicate_unstable") {
		result = assertionNegativeResult(
			e.Error(), status, t.attempt, t.stableMS,
		)
	} else {
		result = axAssertionResult(
			e.Error(), true, status, t.attempt, t.stableMS,
		)
	}
	addAssertionSnapshotMetadata(&result, snapshot)
	if t.windowRef != "" {
		result.StructuredContent["until_window_ref"] = t.windowRef
	}
	return result
}

// addWaitChangeReport reports whether the UI changed during the wait. This
// separates three situations: no AX-visible change, a change that is not the
// awaited one (the predicate may be the wrong test), and a different window
// (the target may live elsewhere). Windows are compared by instance handle,
// not title: a second same-titled window of one app can become the root
// without any title change while every element index is renumbered.
func addWaitChangeReport(
	e *toolError,
	first verifyLookupBaseline,
	snapshot *appSnapshot,
	selectorAmbiguous bool,
) {
	if (first.handle != "" && snapshot.WindowHandle != first.handle) ||
		snapshot.WindowTitle != first.window {
		e.add("Window changed", fmt.Sprintf(
			"%q → %q; current snapshot indices changed",
			first.window, snapshot.WindowTitle,
		))
	} else if scopedCanonicalElementCount(snapshot) != first.count {
		e.add("Tree changed", fmt.Sprintf("%d → %d elements, same window",
			first.count, scopedCanonicalElementCount(snapshot)))
	} else if selectorAmbiguous {
		e.add("Nothing changed", fmt.Sprintf(
			"same window, still %d elements — the selector remained ambiguous, "+
				"so no unique assertion target was identified and this does not "+
				"say whether the prior action took effect",
			first.count))
	} else {
		e.add("Nothing changed", fmt.Sprintf(
			"same window, still %d elements — no AX-visible structural "+
				"change was observed during the wait. This does not establish "+
				"whether a prior input was emitted or whether a visual, file, "+
				"document, or application effect occurred outside A11y",
			first.count))
	}
}

// addWaitNextStep chooses the next step by cause. When the target never
// appeared, it lists the closest current elements so the agent need not
// reread the whole tree to correct a guessed selector. When an exists:false
// wait times out without any UI change, the agent is probably using the
// disappearance of an old element as a proxy for navigation; single-page
// apps often keep the old node, so the condition may never hold, and waiting
// for something new is the better test. When the UI did change, the
// candidate list is more useful than that advice.
func addWaitNextStep(e *toolError, t verifyLookupTimeout) {
	snapshot := t.snapshot
	if t.selectorAmbiguous {
		e.next = "the selector remained ambiguous throughout the wait, so the wait " +
			"never identified one element to test. Use a fresh rN ref or a selector " +
			"copied from the current tree with enough role, name, and predicates to " +
			"match exactly one element. This failure does not prove " +
			"the requested state is false or that the prior action failed."
	} else if t.goal.exists != nil && !*t.goal.exists &&
		snapshot.WindowTitle == t.first.window && len(snapshot.Elements) == t.first.count {
		e.next = "waiting for an element to DISAPPEAR is a poor proxy for " +
			"\"the page moved on\": single-page apps routinely leave the old " +
			"node in the DOM, so this condition may never become true no " +
			"matter how long you wait. Wait for something NEW instead — a " +
			"heading you expect on the next screen, or the address bar's " +
			"value changing. The unchanged AX tree does not attribute the " +
			"cause of this timeout."
	} else if closest := closestElements(snapshot, t.hint, 5); len(closest) > 0 {
		e.log = append(e.log, "  closest elements currently in the tree:")
		for _, line := range closest {
			e.log = append(e.log, "  "+line)
		}
		e.next = "the element never appeared. The closest things actually in the tree are listed above — if one of them is what you meant, retry with its selector. Otherwise the target may be absent or the app may expose it under a different A11y name. This assertion does not attribute the cause."
	} else {
		e.next = "this is a real observation of the current tree, not a stale cache. The declared A11y predicate was not observed. Inspect a fresh tree, the visual channel, or durable state as appropriate; this assertion alone does not prove why the predicate was absent."
	}
}
