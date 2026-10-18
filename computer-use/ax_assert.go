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
	return strings.Join(parts, " and ")
}

// check reports whether the goal holds and what was actually observed. The
// observation matters most: "assertion failed" carries no information, while
// "expected checked, observed enabled focused" does.
func (g verifyGoal) check(record *elementRecord, lookupErr error) (bool, string) {
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

func a11yAssertionResult(
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
	result := a11yAssertionResult(
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

func (s *service) verifyWindowTitle(
	app,
	titleSubstring string,
	expected bool,
	timeoutMS,
	stableMS int,
	targetWindowHandle ...string,
) toolCallResult {
	windowHandle := ""
	if len(targetWindowHandle) > 0 {
		windowHandle = strings.TrimSpace(targetWindowHandle[0])
	}
	return s.verifyWindowTitleWithProbe(
		app,
		titleSubstring,
		expected,
		timeoutMS,
		stableMS,
		windowHandle,
		s.callProviderWindowList,
	)
}

func (s *service) verifyWindowTitleWithProbe(
	app,
	titleSubstring string,
	expected bool,
	timeoutMS,
	stableMS int,
	targetWindowHandle string,
	probe func(ProviderWindowListRequest) (*ProviderWindowResult, error),
) toolCallResult {
	if timeoutMS <= 0 {
		timeoutMS = 5000
	}
	deadline := time.Now().Add(time.Duration(timeoutMS) * time.Millisecond)
	var trueSince time.Time
	attempts := 0
	lastTitles := []string{}
	lastError := ""
	sampledLiveUI := false
	for {
		attempts++
		operation := axServiceRequest{
			Operation:          axServiceWindowList,
			App:                app,
			TargetWindowHandle: targetWindowHandle,
		}
		if err := s.bindRuntimeAppIdentity(&operation); err != nil {
			return a11yAssertionResult(
				"assert_ax application target: "+err.Error(),
				true,
				"invalid_target",
				attempts,
				stableMS,
			)
		}
		response, err := probe(
			providerWindowListRequestFromOperation(operation),
		)
		matched := []string{}
		if err != nil {
			lastError = err.Error()
		} else if !response.OK {
			completeAppAbsence := !expected &&
				isShortAXAppRef(app) &&
				response.Failure != nil &&
				response.Failure.FactsComplete &&
				strings.EqualFold(response.Failure.Subject, "app") &&
				(response.Failure.Code == "stale_target" ||
					response.Failure.Code == "stale_app_ref" ||
					response.Failure.Code == "ax_app_unavailable")
			if completeAppAbsence {
				lastError = ""
				sampledLiveUI = true
				lastTitles = nil
			} else {
				lastError = response.Error
			}
		} else {
			lastError = ""
			sampledLiveUI = true
			lastTitles = append([]string(nil), response.WindowTitles...)
			for _, title := range response.WindowTitles {
				if containsFold(title, titleSubstring) {
					matched = append(matched, title)
				}
			}
		}
		predicate := len(matched) > 0
		if !expected {
			predicate = !predicate && lastError == ""
		}
		if predicate {
			now := time.Now()
			if trueSince.IsZero() {
				trueSince = now
			}
			stableFor := int(now.Sub(trueSince) / time.Millisecond)
			if stableMS == 0 || stableFor >= stableMS {
				status := "present"
				observed := matched
				if !expected {
					status = "absent"
					observed = lastTitles
				}
				result := a11yAssertionResult(
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
				result.StructuredContent["window_titles"] = lastTitles
				result.StructuredContent["window_title_matches"] = matched
				return result
			}
		} else {
			trueSince = time.Time{}
		}
		if !time.Now().Before(deadline) {
			expectation := "appear"
			if !expected {
				expectation = "disappear"
			}
			text := fmt.Sprintf(
				"assert_ax window_title_timeout: no application-owned window "+
					"title containing %q satisfied the requested %s predicate "+
					"within %dms.",
				titleSubstring,
				expectation,
				timeoutMS,
			)
			if lastError != "" {
				text += "\nLast AX application lookup: " + firstLine(lastError)
			}
			if len(lastTitles) > 0 {
				limit := min(5, len(lastTitles))
				text += fmt.Sprintf(
					"\nCurrent window candidates for %q: %q.",
					app,
					lastTitles[:limit],
				)
			}
			result := toolCallResult{}
			if sampledLiveUI && lastError == "" {
				result = assertionNegativeResult(
					text,
					"window_title_timeout",
					attempts,
					stableMS,
				)
			} else {
				result = a11yAssertionResult(
					text,
					true,
					"observation_incomplete",
					attempts,
					stableMS,
				)
			}
			result.StructuredContent["assertion_sampled_live_ui"] =
				sampledLiveUI
			result.StructuredContent["inventory_sample_complete"] =
				sampledLiveUI && lastError == ""
			result.StructuredContent["window_titles"] = lastTitles
			return result
		}
		time.Sleep(400 * time.Millisecond)
	}
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
		normalizedA11yScope(snapshot.scope) != a11yScopeContent {
		return snapshot
	}
	clone := *snapshot
	clone.Elements = make([]elementRecord, 0, snapshot.scopedCanonicalCount)
	for _, record := range snapshot.Elements {
		if recordInA11yScope(snapshot, &record) {
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
					Scope:              a11yScopeWindow,
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
		return a11yAssertionResult(
			"assert_ax invalid_target: the AX ref has no live target identity.",
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
	lastStatus := "predicate_false"
	sampledLiveUI := false
	for attempt := 1; ; attempt++ {
		var sampleProof *axAssertionSnapshotProof
		operation := axServiceRequest{
			Action:             axResolveTargetCommand{},
			App:                app,
			TargetWindowHandle: target.WindowHandle,
			SemanticTarget:     target,
			SourceRef:          displayRef,
		}
		if err := s.bindRuntimeAppIdentity(&operation); err != nil {
			return a11yAssertionResult(
				"assert_ax invalid_target: "+err.Error(),
				true, "invalid_target", attempt, stableMS,
			)
		}
		response, err := probe(
			providerActionRequestFromOperation(operation),
		)
		if response != nil {
			response.prepareForService()
		}
		var record *elementRecord
		var lookupErr error
		factsComplete := true
		switch {
		case err != nil:
			lookupErr = err
			factsComplete = false
			lastStatus = "observation_incomplete"
		case response == nil:
			lookupErr = errors.New("empty accessibility resolver response")
			factsComplete = false
			lastStatus = "observation_incomplete"
		case response.OK:
			record = response.ResolvedElement
			sampledLiveUI = true
			if record == nil {
				lookupErr = errors.New("live resolver returned no element")
				factsComplete = false
				lastStatus = "observation_incomplete"
			}
		default:
			lookupErr = errors.New(firstNonEmpty(
				publicRuntimeFailureText(*response),
				response.Error,
				response.FailureCode,
			))
			factsComplete = response.FailureFactsComplete
			switch response.FailureCode {
			case "semantic_target_not_found":
				lastStatus = "locator_missing"
			case "semantic_target_ambiguous":
				lastStatus = "locator_ambiguous"
			case "stale_element_ref":
				lastStatus = "locator_stale"
			case "observation_incomplete":
				lastStatus = "observation_incomplete"
			default:
				lastStatus = firstNonEmpty(
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
				factsComplete &&
				completeRefAbsenceFailure(*response) {
				record = nil
				sampledLiveUI = true
				lastStatus = "predicate_false"
				break
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
					record = proof.record
					lookupErr = nil
					factsComplete = true
					sampledLiveUI = true
					lastStatus = "predicate_false"
					sampleProof = proof
				}
			}
			if response.FailureCode == "semantic_target_ambiguous" ||
				response.FailureCode == "stale_element_ref" ||
				response.FailureCode == "semantic_target_invalid" {
				result := a11yAssertionResult(
					fmt.Sprintf(
						"assert_ax %s: %s",
						lastStatus,
						firstLine(lookupErr.Error()),
					),
					true, lastStatus, attempt, stableMS,
				)
				result.StructuredContent["assertion_sampled_live_ui"] =
					sampledLiveUI
				return result
			}
		}
		if !factsComplete && record == nil {
			lastObserved = "the live AX target could not be read completely"
			trueSince = time.Time{}
			stableObserved = ""
		} else {
			ok, observed := goal.check(record, lookupErr)
			lastObserved = observed
			if ok {
				now := time.Now()
				if stableMS == 0 {
					result := a11yAssertionResult(
						fmt.Sprintf(
							"PASS: %s %s. %s",
							displayRef, goal.describe(), observed,
						),
						false, "pass", attempt, stableMS,
					)
					if sampleProof != nil {
						result.axProofSnapshot = sampleProof.snapshot
						result.axProofNotes = append(
							[]string(nil),
							sampleProof.notes...,
						)
						result.StructuredContent["assertion_canonical_fallback"] =
							true
					}
					return result
				}
				if trueSince.IsZero() || stableObserved != observed {
					trueSince = now
					stableObserved = observed
				}
				if int(now.Sub(trueSince)/time.Millisecond) >= stableMS {
					result := a11yAssertionResult(
						fmt.Sprintf(
							"PASS: %s %s and remained continuously true for %dms. %s",
							displayRef,
							goal.describe(),
							int(now.Sub(trueSince)/time.Millisecond),
							observed,
						),
						false, "pass", attempt, stableMS,
					)
					if sampleProof != nil {
						result.axProofSnapshot = sampleProof.snapshot
						result.axProofNotes = append(
							[]string(nil),
							sampleProof.notes...,
						)
						result.StructuredContent["assertion_canonical_fallback"] =
							true
					}
					return result
				}
			} else {
				trueSince = time.Time{}
				stableObserved = ""
			}
		}
		if !time.Now().Before(deadline) {
			text := fmt.Sprintf(
				"assert_ax %s: %s did not satisfy %s within %dms. Last observation: %s",
				lastStatus,
				displayRef,
				goal.describe(),
				timeoutMS,
				lastObserved,
			)
			result := toolCallResult{}
			if sampledLiveUI && lastStatus == "predicate_false" {
				result = assertionNegativeResult(
					text, lastStatus, attempt, stableMS,
				)
			} else {
				result = a11yAssertionResult(
					text, true, lastStatus, attempt, stableMS,
				)
			}
			result.StructuredContent["assertion_sampled_live_ui"] =
				sampledLiveUI
			return result
		}
		time.Sleep(400 * time.Millisecond)
	}
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
		return textResult("assert_ax requires a target.", true)
	}
	if strings.TrimSpace(displaySelector) == "" {
		displaySelector = selector
	}
	if numericElementIndex(selector) {
		if s.hasPendingFailedObservation(app) {
			return textResult(
				"assert_ax cannot reuse target.index after a stale-reference failure. "+
					"The previous snapshot address space was invalidated. Call AX mode=observe "+
					"or mode=find explicitly, then use a newly returned rN ref or a current "+
					"read-only target.", true)
		}
		if s.currentSnapshot(app) == nil {
			return textResult(
				"assert_ax cannot use target.index without a current accessibility "+
					"snapshot. Call AX mode=observe or mode=find first, then use a current "+
					"target.index, target.ref, or target.selector.", true)
		}
	} else if _, err := semanticTargetFromAddress(selector); err != nil {
		result := textResult(
			"assert_ax locator_invalid: "+err.Error(), true)
		result.StructuredContent = map[string]any{
			"assertion_status":     "locator_invalid",
			"task_effect_verified": false,
		}
		return result
	}
	if goal.empty() {
		return textResult("assert_ax needs something to assert: pass at least one of state, value_contains, value_equals, text_contains, or exists.", true)
	}
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
	var firstWindow string
	var firstHandle string
	var firstCount int
	var trueSince time.Time
	stableObserved := ""
	for attempt := 1; ; attempt++ {
		request := axServiceRequest{
			Operation:         axServiceObserve,
			App:               app,
			IncludeScreenshot: &noScreenshot,
			Scope:             normalizedA11yScope(scope),
		}
		if len(targetWindowHandle) > 0 {
			request.TargetWindowHandle = strings.TrimSpace(targetWindowHandle[0])
		}
		snapshot, _, result := s.readProjectedSnapshot(app, request)
		if result.IsError {
			if isAXAppUnavailableResult(result) {
				elapsed := timeoutMS - int(time.Until(deadline).Milliseconds())
				attempts = append(attempts, fmt.Sprintf(
					"attempt %d at +%dms — application is not yet visible on the AX bus",
					attempt,
					elapsed,
				))
				if !time.Now().Before(deadline) {
					text := fmt.Sprintf(
						"assert_ax app_unavailable: %q did not become visible on the AX bus within %dms.\n",
						app,
						timeoutMS,
					)
					for _, line := range attempts {
						text += "  - " + line + "\n"
					}
					text += "Last AX availability result: " +
						firstLine(resultText(result))
					return a11yAssertionResult(
						strings.TrimSpace(text),
						true,
						"app_unavailable",
						attempt,
						stableMS,
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
		if attempt == 1 {
			firstWindow, firstHandle, firstCount = snapshot.WindowTitle,
				snapshot.WindowHandle,
				scopedCanonicalElementCount(snapshot)
		}
		lookupSnapshot := snapshotForScopedLookup(snapshot)
		record, lookupErr := lookupElementFor(
			lookupSnapshot,
			selector,
			"",
			"assert_ax",
		)
		ok, observed := goal.check(record, lookupErr)
		selectorAmbiguous := lookupErr != nil &&
			strings.Contains(strings.ToLower(lookupErr.Error()), "ambiguous")
		attempts = append(attempts, fmt.Sprintf("attempt %d at +%dms — %s",
			attempt, timeoutMS-int(time.Until(deadline).Milliseconds()), observed))
		if attempt == 1 && lookupErr != nil &&
			(numericElementIndex(selector) || selectorAmbiguous) {
			status := "locator_stale"
			if selectorAmbiguous {
				status = "locator_ambiguous"
			}
			result := a11yAssertionResult(
				fmt.Sprintf(
					"assert_ax %s: %s\nThis locator did not identify one "+
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
		if ok {
			now := time.Now()
			if stableMS == 0 {
				return a11yAssertionResult(fmt.Sprintf(
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
				return a11yAssertionResult(fmt.Sprintf(
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
			e := &toolError{tool: "assert_ax", summary: fmt.Sprintf(
				"expected %s %s, and it did not become true within %dms.",
				displaySelector, goal.describe(), timeoutMS)}
			e.add("Selector", strconv.Quote(displaySelector))
			e.add("Snapshot", snapshotAge(snapshot))
			// Report whether the UI changed during the wait. This separates
			// three situations: no AX-visible change, a change that is not the
			// awaited one (the predicate may be the wrong test), and a
			// different window (the target may live elsewhere). Windows are
			// compared by instance handle, not title: a second same-titled
			// window of one app can become the root without any title change
			// while every element index is renumbered.
			if (firstHandle != "" && snapshot.WindowHandle != firstHandle) ||
				snapshot.WindowTitle != firstWindow {
				e.add("Window changed", fmt.Sprintf(
					"%q → %q; current snapshot indices changed",
					firstWindow, snapshot.WindowTitle,
				))
			} else if scopedCanonicalElementCount(snapshot) != firstCount {
				e.add("Tree changed", fmt.Sprintf("%d → %d elements, same window",
					firstCount, scopedCanonicalElementCount(snapshot)))
			} else if selectorAmbiguous {
				e.add("Nothing changed", fmt.Sprintf(
					"same window, still %d elements — the selector remained ambiguous, "+
						"so no unique assertion target was identified and this does not "+
						"say whether the prior action took effect",
					firstCount))
			} else {
				e.add("Nothing changed", fmt.Sprintf(
					"same window, still %d elements — no AX-visible structural "+
						"change was observed during the wait. This does not establish "+
						"whether a prior input was emitted or whether a visual, file, "+
						"document, or application effect occurred outside A11y",
					firstCount))
			}
			for _, line := range attempts {
				e.log = append(e.log, "  - "+line)
			}
			// Choose the next step by cause. When the target never appeared,
			// list the closest current elements so the agent need not reread
			// the whole tree to correct a guessed selector. When an
			// exists:false wait times out without any UI change, the agent is
			// probably using the disappearance of an old element as a proxy
			// for navigation; single-page apps often keep the old node, so the
			// condition may never hold, and waiting for something new is the
			// better test. When the UI did change, the candidate list is more
			// useful than that advice.
			if selectorAmbiguous {
				e.next = "the selector remained ambiguous throughout the wait, so assert_ax " +
					"never identified one element to test. Use a fresh rN ref or a selector " +
					"copied from the current tree with enough role, name, and predicates to " +
					"match exactly one element. This failure does not prove " +
					"the requested state is false or that the prior action failed."
			} else if goal.exists != nil && !*goal.exists &&
				snapshot.WindowTitle == firstWindow && len(snapshot.Elements) == firstCount {
				e.next = "waiting for an element to DISAPPEAR is a poor proxy for " +
					"\"the page moved on\": single-page apps routinely leave the old " +
					"node in the DOM, so this condition may never become true no " +
					"matter how long you wait. Wait for something NEW instead — a " +
					"heading you expect on the next screen, or the address bar's " +
					"value changing. The unchanged AX tree does not attribute the " +
					"cause of this timeout."
			} else if hint := closestElements(snapshot, selectorHint(selector), 5); len(hint) > 0 {
				e.log = append(e.log, "  closest elements currently in the tree:")
				for _, line := range hint {
					e.log = append(e.log, "  "+line)
				}
				e.next = "the element never appeared. The closest things actually in the tree are listed above — if one of them is what you meant, retry with its selector. Otherwise the target may be absent or the app may expose it under a different A11y name. This assertion does not attribute the cause."
			} else {
				e.next = "this is a real observation of the current tree, not a stale cache. The declared A11y predicate was not observed. Inspect a fresh tree, the visual channel, or durable state as appropriate; this assertion alone does not prove why the predicate was absent."
			}
			status := "locator_missing"
			if lookupErr == nil {
				status = "predicate_false"
			}
			if ok && stableMS > 0 {
				status = "predicate_unstable"
				e.next = fmt.Sprintf(
					"the predicate became true but did not remain unchanged for "+
						"the requested %dms stability window before timeout. "+
						"Increase timeout_ms, reduce stable_ms, or assert a more "+
						"specific next-state condition.",
					stableMS,
				)
			}
			result := toolCallResult{}
			if lookupErr == nil &&
				(status == "predicate_false" || status == "predicate_unstable") {
				result = assertionNegativeResult(
					e.Error(), status, attempt, stableMS,
				)
			} else {
				result = a11yAssertionResult(
					e.Error(), true, status, attempt, stableMS,
				)
			}
			addAssertionSnapshotMetadata(&result, snapshot)
			return result
		}
		time.Sleep(400 * time.Millisecond)
	}
}
