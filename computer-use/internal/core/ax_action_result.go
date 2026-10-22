package core

import (
	"fmt"
	"strings"
	"time"

	"github.com/hqhq1025/hybrid-computer-use-oss/computer-use/internal/contract"
)

func snapshotHasActionableElementRef(snapshot *AppSnapshot) bool {
	if snapshot == nil || snapshot.AXSnapshot == nil {
		return false
	}
	var hasRef func([]*AXNode) bool
	hasRef = func(nodes []*AXNode) bool {
		for _, node := range nodes {
			if node == nil {
				continue
			}
			if isShortAXRef(node.Ref) || hasRef(node.Children) {
				return true
			}
		}
		return false
	}
	return hasRef(snapshot.AXSnapshot.Roots)
}

type providerTopLevelIdentity struct {
	busName           string
	objectPath        string
	processGeneration string
	roleFamily        string
}

func snapshotProviderTopLevelIdentity(
	snapshot *AppSnapshot,
) (providerTopLevelIdentity, bool) {
	if snapshot == nil {
		return providerTopLevelIdentity{}, false
	}
	var selected providerTopLevelIdentity
	found := false
	for index := range snapshot.Elements {
		record := &snapshot.Elements[index]
		identity := providerTopLevelIdentity{
			busName:           strings.TrimSpace(record.ProviderWindowBusName),
			objectPath:        strings.TrimSpace(record.ProviderWindowObjectPath),
			processGeneration: strings.TrimSpace(record.ProviderWindowProcessGeneration),
			roleFamily:        windowRoleFamily(record.ProviderWindowRole),
		}
		if identity.busName != "" &&
			identity.objectPath != "" &&
			identity.processGeneration != "" &&
			identity.roleFamily != "" {
			if found && selected != identity {
				return providerTopLevelIdentity{}, false
			}
			selected = identity
			found = true
		}
	}
	return selected, found
}

func transientTopLevelSnapshot(snapshot *AppSnapshot) bool {
	if snapshot == nil {
		return false
	}
	if snapshot.WindowSelection != nil {
		for _, candidate := range snapshot.WindowSelection.Candidates {
			if candidate.Selected && candidate.Modal {
				return true
			}
		}
	}
	for _, window := range snapshot.Windows {
		if window.Selected && window.Modal {
			return true
		}
	}
	identity, ok := snapshotProviderTopLevelIdentity(snapshot)
	if !ok {
		return false
	}
	switch identity.roleFamily {
	case "dialog", "file chooser", "color chooser":
		return true
	default:
		return false
	}
}

func commitLikeTransientAction(request AXServiceRequest) bool {
	action, ok := request.Action.(axKeypressCommand)
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(action.Key)) {
	case "enter", "return", "escape", "esc":
		return true
	default:
		return false
	}
}

func provenGoneSourceReplacement(
	service *Service,
	result ToolCallResult,
) (*AXWindowRefEntry, bool) {
	if service == nil || !result.IsError || result.StructuredContent == nil {
		return nil, false
	}
	sourceStatus, _ :=
		result.StructuredContent["source_window_status"].(string)
	inventoryComplete, _ :=
		result.StructuredContent["recovery_inventory_complete"].(bool)
	currentRef, _ :=
		result.StructuredContent["current_window_ref"].(string)
	if sourceStatus != "gone" ||
		!inventoryComplete ||
		!isShortAXWindowRef(currentRef) {
		return nil, false
	}
	entry, err := service.WindowRefRegistry.Resolve(currentRef, "")
	if err != nil || entry == nil ||
		!validInternalWindowHandle(entry.WindowHandle) {
		return nil, false
	}
	return entry, true
}

var transientCommitProbeDelays = []time.Duration{
	250 * time.Millisecond,
	750 * time.Millisecond,
	2 * time.Second,
}

func (s *Service) stabilizePostActionTransientCommit(
	app string,
	after *AppSnapshot,
	request AXServiceRequest,
) (*AppSnapshot, []string, bool, bool) {
	if s == nil ||
		(observation{mode: request.Observe}).resolvedMode() == contract.ObserveNone ||
		after == nil ||
		after.externalDesktopTransition ||
		!transientTopLevelSnapshot(after) ||
		!commitLikeTransientAction(request) {
		return after, nil, false, false
	}

	const transitionBudget = 3 * time.Second
	started := time.Now()
	sourceRef := snapshotLogicalWindowRef(after)
	if !isShortAXWindowRef(sourceRef) ||
		!validInternalWindowHandle(after.WindowHandle) {
		return after, nil, false, false
	}
	for _, delay := range transientCommitProbeDelays {
		remaining := transitionBudget - time.Since(started)
		if remaining <= 0 {
			break
		}
		if delay > remaining {
			delay = remaining
		}
		time.Sleep(delay)

		withoutScreenshot := false
		sourceProbeRequest := AXServiceRequest{
			Operation:          axServiceObserve,
			App:                app,
			IncludeScreenshot:  &withoutScreenshot,
			TargetWindowHandle: after.WindowHandle,
			SourceRef:          sourceRef,
			Scope:              contract.AXScopeWindow,
			TreeView:           contract.TreeViewWorking,
		}
		applyProjection(&sourceProbeRequest, after.projection)
		probe := s.isolatedProbe()
		_, _, sourceResult := probe.readSnapshot(app, sourceProbeRequest)
		replacement, proven :=
			provenGoneSourceReplacement(probe, sourceResult)
		if !proven {
			continue
		}
		finalRequest := AXServiceRequest{
			Operation:          axServiceObserve,
			App:                app,
			IncludeScreenshot:  request.IncludeScreenshot,
			ScreenshotScope:    request.ScreenshotScope,
			TargetWindowHandle: replacement.WindowHandle,
			SourceRef:          replacement.Ref,
			Scope:              contract.AXScopeWindow,
			TreeView:           contract.TreeViewWorking,
		}
		applyProjection(&finalRequest, after.projection)
		refreshed, notes, finalResult :=
			probe.readSnapshot(app, finalRequest)
		if finalResult.IsError || refreshed == nil ||
			snapshotLogicalWindowRef(refreshed) != replacement.Ref {
			return after, nil, true, false
		}
		refreshed.projection = projectionFromRequest(finalRequest)
		refreshed.actionExecution = after.actionExecution
		refreshed.externalDesktopTransition = after.externalDesktopTransition

		s.RefRegistry = probe.RefRegistry
		s.WindowRefRegistry = probe.WindowRefRegistry
		s.AppRefRegistry = probe.AppRefRegistry
		s.FindCursorRegistry.Reset()
		s.rememberSnapshot(app, refreshed)
		notes = append([]string{
			"[transition] A bounded no-input probe observed the transient " +
				"window finish and returned the current application window.",
		}, notes...)
		return refreshed, notes, true, true
	}
	return after, nil, true, false
}

func postActionWindowIdentityStabilizationTrigger(
	before,
	after *AppSnapshot,
	obs observation,
) string {
	if obs.resolvedMode() != contract.ObserveDiff ||
		before == nil ||
		after == nil {
		return ""
	}
	beforeProvider, beforeProviderOK :=
		snapshotProviderTopLevelIdentity(before)
	afterProvider, afterProviderOK :=
		snapshotProviderTopLevelIdentity(after)
	providerChanged := beforeProviderOK && afterProviderOK &&
		beforeProvider != afterProvider
	if isShortAXWindowRef(after.publicWindowRef) &&
		snapshotHasActionableElementRef(after) &&
		!providerChanged {
		return ""
	}
	if validInternalWindowHandle(before.WindowHandle) &&
		validInternalWindowHandle(after.WindowHandle) &&
		before.WindowHandle != after.WindowHandle {
		return "exact_handle_change"
	}
	if providerChanged {
		return "provider_window_change"
	}
	beforeTitle := normalizedTreeText(before.WindowTitle)
	afterTitle := normalizedTreeText(after.WindowTitle)
	if beforeTitle != "" && afterTitle != "" && beforeTitle != afterTitle {
		return "semantic_window_change"
	}
	return ""
}

func (s *Service) stabilizePostActionWindowIdentity(
	app string,
	before,
	after *AppSnapshot,
	request AXServiceRequest,
) (*AppSnapshot, []string, bool, bool, string) {
	obs := observation{mode: request.Observe}
	trigger := postActionWindowIdentityStabilizationTrigger(before, after, obs)
	if s == nil || trigger == "" {
		return after, nil, false, false, ""
	}
	expectedProvider, expectedProviderOK :=
		snapshotProviderTopLevelIdentity(after)
	if !expectedProviderOK {
		return after, nil, true, false, trigger
	}

	includeScreenshot := false
	targetWindowHandle := ""
	if trigger == "exact_handle_change" {
		targetWindowHandle = after.WindowHandle
	}
	refresh := AXServiceRequest{
		Operation:          axServiceObserve,
		App:                app,
		IncludeScreenshot:  &includeScreenshot,
		TargetWindowHandle: targetWindowHandle,
		Scope:              contract.AXScopeWindow,
		TreeView:           contract.TreeViewWorking,
	}
	applyProjection(&refresh, after.projection)
	probe := s.isolatedProbe()
	if trigger == "provider_window_change" &&
		isShortAXWindowRef(before.publicWindowRef) {
		probe.RefRegistry.MarkByWindow(
			before.publicWindowRef,
			AXRefSuperseded,
			"provider top-level window identity changed",
		)
	}
	refreshed, notes, result := probe.readSnapshot(app, refresh)
	refreshedProvider, refreshedProviderOK :=
		snapshotProviderTopLevelIdentity(refreshed)
	if result.IsError ||
		refreshed == nil ||
		!refreshedProviderOK ||
		refreshedProvider != expectedProvider ||
		!isShortAXWindowRef(refreshed.publicWindowRef) ||
		!snapshotHasActionableElementRef(refreshed) {
		return after, nil, true, false, trigger
	}
	if trigger == "exact_handle_change" &&
		refreshed.WindowHandle != after.WindowHandle {
		return after, nil, true, false, trigger
	}
	if trigger == "semantic_window_change" &&
		(!validInternalWindowHandle(refreshed.WindowHandle) ||
			refreshed.WindowHandle == before.WindowHandle) {
		return after, nil, true, false, trigger
	}
	if trigger == "provider_window_change" &&
		!validInternalWindowHandle(refreshed.WindowHandle) {
		return after, nil, true, false, trigger
	}

	refreshed.projection = projectionFromRequest(refresh)
	refreshed.actionExecution = after.actionExecution
	refreshed.externalDesktopTransition = after.externalDesktopTransition
	s.RefRegistry = probe.RefRegistry
	s.WindowRefRegistry = probe.WindowRefRegistry
	s.AppRefRegistry = probe.AppRefRegistry
	if trigger == "provider_window_change" {
		s.FindCursorRegistry.Reset()
	}
	s.rememberSnapshot(app, refreshed)
	notes = append([]string{
		"[identity] The new top-level window was refreshed once without input; " +
			"the current wN/rN refs below are actionable.",
	}, notes...)
	return refreshed, notes, true, true, trigger
}

func shouldStabilizePostActionContent(
	service *Service,
	after *AppSnapshot,
	request AXServiceRequest,
	identityStabilized bool,
) bool {
	if service == nil ||
		identityStabilized ||
		request.WindowAction != nil ||
		(observation{mode: request.Observe}).resolvedMode() != contract.ObserveDiff ||
		after == nil ||
		after.externalDesktopTransition {
		return false
	}
	baseline := service.deliveredSnapshotFor(after)
	if baseline == nil {
		return false
	}
	if diff, ok := incrementalTree(baseline, after); ok {
		return !incrementalObservationHasChanges(diff)
	}
	if contentRevisionChanged(baseline, after) {
		return false
	}
	return true
}

func incrementalObservationHasChanges(lines []string) bool {
	if len(lines) == 0 {
		return false
	}
	sawNoChangeMarker := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "Incremental view: 0 changes"):
			sawNoChangeMarker = true
		case trimmed == "effect: no_ax_change":
			sawNoChangeMarker = true
		case strings.Contains(trimmed, "<changed>"),
			strings.Contains(trimmed, "<added>"),
			strings.Contains(trimmed, "<removed>"),
			strings.HasPrefix(trimmed, "added:"),
			strings.HasPrefix(trimmed, "removed:"),
			strings.HasPrefix(trimmed, "changed:"),
			strings.HasPrefix(trimmed, "effect: target_window_absent"):
			return true
		case strings.HasPrefix(trimmed, "Incremental view:"):
			return true
		}
	}
	return !sawNoChangeMarker
}

func (s *Service) postActionContentWindowHandle(
	snapshot *AppSnapshot,
) string {
	if snapshot == nil {
		return ""
	}
	if validInternalWindowHandle(snapshot.WindowHandle) {
		return snapshot.WindowHandle
	}
	windowRef := snapshotLogicalWindowRef(snapshot)
	if !isShortAXWindowRef(windowRef) {
		return ""
	}
	entry, err := s.WindowRefRegistry.Resolve(windowRef, "")
	if err != nil || !validInternalWindowHandle(entry.WindowHandle) {
		return ""
	}
	return entry.WindowHandle
}

func (s *Service) stabilizePostActionContent(
	app string,
	after *AppSnapshot,
	request AXServiceRequest,
	identityStabilized bool,
) (*AppSnapshot, []string, bool, bool) {
	if !shouldStabilizePostActionContent(
		s,
		after,
		request,
		identityStabilized,
	) {
		return after, nil, false, false
	}
	windowHandle := s.postActionContentWindowHandle(after)
	if windowHandle == "" {
		return after, nil, false, false
	}

	const retryDelay = 250 * time.Millisecond
	time.Sleep(retryDelay)
	includeScreenshot := false
	refresh := AXServiceRequest{
		Operation:          axServiceObserve,
		App:                app,
		IncludeScreenshot:  &includeScreenshot,
		TargetWindowHandle: windowHandle,
		SourceRef:          snapshotLogicalWindowRef(after),
		Scope:              contract.AXScopeWindow,
		TreeView:           contract.TreeViewWorking,
	}
	applyProjection(&refresh, after.projection)
	probe := s.isolatedProbe()
	refreshed, notes, result := probe.readSnapshot(app, refresh)
	if result.IsError ||
		refreshed == nil ||
		snapshotLogicalWindowRef(refreshed) != snapshotLogicalWindowRef(after) ||
		refreshed.SnapshotID == after.SnapshotID {
		return after, nil, true, false
	}
	semanticChange := contentRevisionChanged(after, refreshed)
	if diff, ok := incrementalTree(after, refreshed); ok {
		semanticChange = semanticChange ||
			incrementalObservationHasChanges(diff)
	}
	if !semanticChange {
		return after, nil, true, false
	}
	refreshed.projection = projectionFromRequest(refresh)
	refreshed.actionExecution = after.actionExecution
	refreshed.externalDesktopTransition = after.externalDesktopTransition
	s.RefRegistry = probe.RefRegistry
	s.WindowRefRegistry = probe.WindowRefRegistry
	s.AppRefRegistry = probe.AppRefRegistry
	s.rememberSnapshot(app, refreshed)
	notes = append([]string{
		"[content] The window changed after the first post-action read; " +
			"the result below is from one re-read 250 ms later.",
	}, notes...)
	return refreshed, notes, true, true
}

// renderAXContentStabilizedObservation returns the re-read window as a diff
// against the last state the model received for it, and falls back to the
// working tree only when no diff is available or the diff is not shorter.
func (s *Service) renderAXContentStabilizedObservation(
	snapshot *AppSnapshot,
	notes []string,
) (ToolCallResult, []string, bool, bool, bool) {
	if diff, ok := incrementalTree(s.deliveredSnapshotFor(snapshot), snapshot); ok {
		result := snapshot.resultWithTreeOverride(notes, diff)
		if result.StructuredContent == nil {
			result.StructuredContent = map[string]any{}
		}
		result.StructuredContent["final_observation_kind"] = "diff"
		return result, diff, true, false, false
	}
	return s.renderAXWorkingObservation(snapshot, notes)
}

func (s *Service) actionResult(app string, request AXServiceRequest) ToolCallResult {
	// The pre-action state is already cached (actions require a prior
	// observation, and every action refreshes the cache), so the before/after
	// comparison needs no extra tree traversal.
	before := s.currentSnapshot(app)
	if request.Actionability == nil {
		evidence := initialActionabilityEvidence(
			request.operationName(),
			request.Element,
			request.SemanticTarget,
		)
		request.Actionability = &evidence
	}
	snapshot, notes, result := s.refreshSnapshot(app, request)
	if result.IsError {
		if request.SemanticTarget != nil {
			if result.StructuredContent == nil {
				result.StructuredContent = map[string]any{}
			}
			result.StructuredContent["semantic_resolution_fresh"] = true
			result.StructuredContent["semantic_resolution_unique_required"] = true
			code, _ := result.StructuredContent["failure_code"].(string)
			result.StructuredContent["semantic_resolution_unique_match"] =
				code != "semantic_target_not_found" &&
					code != "semantic_target_ambiguous"
			if request.SemanticTarget.SourceIndex != nil {
				result.StructuredContent["numeric_index_promoted_to_semantic"] = true
				result.StructuredContent["promoted_numeric_index"] =
					*request.SemanticTarget.SourceIndex
			}
		}
		// Trace failures too. A trace of successes alone shows a smooth run
		// on review while the step that needs investigating is missing.
		s.appendTrace(app, request, before, nil, nil, true)
		if request.SourceRef != "" &&
			request.SemanticTarget != nil &&
			request.SemanticTarget.LocatorKind == AXLocatorProviderBound {
			code, _ := result.StructuredContent["failure_code"].(string)
			if code == "stale_element_ref" || code == "stale_target" {
				s.RefRegistry.MarkStale(request.SourceRef, code)
			}
		}
		if request.Actionability != nil {
			var actual *RuntimeActionEvidence
			if result.StructuredContent != nil {
				switch value := result.StructuredContent["runtime_action_evidence"].(type) {
				case RuntimeActionEvidence:
					actual = &value
				case *RuntimeActionEvidence:
					actual = value
				}
			}
			result = attachActionabilityEvidence(
				result,
				finalizeActionabilityEvidence(
					*request.Actionability,
					request,
					result,
					false,
					actual,
				),
			)
		}
		if isNoInputRecoverablePrecondition(result) {
			return s.recoverNoInputPreconditionObservation(
				app,
				before,
				request,
				result,
			)
		}
		s.invalidateAccessibilitySnapshots()
		return result
	}
	// A successful close may return an exact-window absence proof instead of
	// an AX tree. Re-reading that removed window would discard the proof.
	closedWindowHandle := request.TargetWindowHandle
	if request.SemanticTarget != nil && closedWindowHandle == "" {
		closedWindowHandle = request.SemanticTarget.WindowHandle
	}
	if request.WindowAction != nil && *request.WindowAction == WindowClose &&
		snapshot.WindowAbsent && closedWindowHandle != "" &&
		snapshot.ClosedWindowHandle == closedWindowHandle &&
		(request.SemanticTarget == nil || request.SemanticTarget.WindowHandle == "" ||
			request.SemanticTarget.WindowHandle == closedWindowHandle) {
		s.appendTrace(app, request, before, snapshot, notes, false)
		response := closedWindowFinalObservation(
			&AXBatchContext{WindowRef: request.SourceRef},
			observation{mode: request.Observe},
		)
		response = withActionOutcomeForRequest(response, request, snapshot)
		response = finalizeVerifiedCloseLifecycle(response)
		if request.Observe == contract.ObserveNone {
			response = omitVerifiedCloseFinalObservation(response)
		}
		return response
	}
	if snapshot.deliveryOnly {
		s.appendTrace(app, request, before, nil, notes, false)
		response := snapshot.notesOnlyResult(observeNoneNotes(notes))
		if response.StructuredContent == nil {
			response.StructuredContent = map[string]any{}
		}
		response.StructuredContent["final_observation_returned"] = false
		response.StructuredContent["observation_required"] = false
		response.StructuredContent["delivery_only"] = true
		return withActionOutcomeForRequest(response, request, snapshot)
	}
	immediateSnapshot := snapshot
	var transientCommitNotes []string
	var transientCommitStabilized bool
	snapshot, transientCommitNotes, _,
		transientCommitStabilized = s.stabilizePostActionTransientCommit(
		app,
		snapshot,
		request,
	)
	notes = append(notes, transientCommitNotes...)
	var stabilizationNotes []string
	var identityStabilizationAttempted, identityStabilized bool
	var identityStabilizationTrigger string
	if transientCommitStabilized {
		identityStabilized = true
		identityStabilizationTrigger = "transient_commit"
	} else {
		snapshot, stabilizationNotes, identityStabilizationAttempted,
			identityStabilized, identityStabilizationTrigger =
			s.stabilizePostActionWindowIdentity(
				app,
				before,
				snapshot,
				request,
			)
	}
	notes = append(notes, stabilizationNotes...)
	var contentStabilizationNotes []string
	var contentStabilizationAttempted, contentStabilized bool
	snapshot, contentStabilizationNotes, contentStabilizationAttempted,
		contentStabilized = s.stabilizePostActionContent(
		app,
		snapshot,
		request,
		identityStabilized,
	)
	notes = append(notes, contentStabilizationNotes...)
	// A root-only read cannot say which elements changed; the checks that
	// compare elements wait for the next full read.
	rootOnly := snapshot.lightReadback || (before != nil && before.lightReadback)
	if !rootOnly {
		if note := s.hiddenCanonicalAddressChangeNote(before, snapshot); note != "" {
			notes = append(notes, note)
		}
	}
	// Formatting changes come first: they say what changed into what, while
	// pixels only say that something happened and the tree only that its
	// structure did not change.
	if changes := textAttributeChanges(before, snapshot); !rootOnly && len(changes) > 0 {
		shown := changes
		if len(shown) > 4 {
			shown = append(append([]string{}, shown[:4]...),
				fmt.Sprintf("… and %d more", len(changes)-4))
		}
		notes = append([]string{"[text-attrs] Document text formatting changed, read from accessibility text attributes: " +
			strings.Join(shown, " | ") +
			". The snapshot renders structure, not formatting, so the tree can look unchanged while this does not. Treat this as supporting evidence rather than proof: a page can also restyle itself on its own. Obvious repaint noise (a web font replacing the fallback font, colour shifts too small to see) is already filtered out."}, notes...)
	}
	semanticUnchanged := before != nil && !observablyChanged(before, snapshot)
	if rootOnly {
		// Without both trees, only the window title, focus, selection and
		// pixels can show an effect. Treating an action as unchanged when
		// none of them moved flags at least every action the tree
		// comparison would flag, so uncertainty is never lost.
		semanticUnchanged = before != nil &&
			before.WindowTitle == snapshot.WindowTitle &&
			before.FocusedSummary == snapshot.FocusedSummary &&
			before.SelectedText == snapshot.SelectedText &&
			readPixelVerdict(notes) != pixelsChanged
	}
	if semanticUnchanged {
		// A semantic action can report success while nothing happens: some
		// toolkits accept actions such as `menu`, `activate` or `Toggle` and
		// do nothing. The check deliberately uses only external signals
		// (window title, tree, focus and selection), not the target node's
		// own state: a radio button's CHECKED state can flip while the panel
		// it controls stays unchanged, and reading the node would count that
		// as an effect. The check therefore detects silent no-ops only, not
		// every failed effect; judging whether the intended consequence
		// happened needs task-level knowledge. The response reports the
		// unchanged state and never replays the action.
		if windowFocusWasConfirmed(request) {
			// require_window_focus proves only that the target application
			// window was active before synthesis. It does not identify the
			// focused control and gives no control-level delivery or
			// semantic-effect evidence.
			notes = append(notes, unchangedNotes(true, readPixelVerdict(notes), readTextAttributeChange(notes))...)
		} else {
			notes = append(notes, unchangedNotes(false, readPixelVerdict(notes), readTextAttributeChange(notes))...)
		}
	}
	// Trace last, once notes are complete (pixel verdict, state readback and
	// resolved target), because those notes matter most on review.
	s.appendTrace(app, request, before, snapshot, notes, false)
	// The observation budget applies last: all evidence above is computed as
	// usual, and observe decides only how much is sent. The snapshot cache is
	// refreshed in every mode, so returning less never invalidates element
	// indices or refs.
	var response ToolCallResult
	var deliveredLines []string
	treeWasReturned := false
	fullTreeReturned := false
	compactActionObservation := false
	observationRequired := false
	switch (observation{mode: request.Observe}).resolvedMode() {
	case contract.ObserveNone:
		response = snapshot.notesOnlyResult(observeNoneNotes(notes))
		if response.StructuredContent == nil {
			response.StructuredContent = map[string]any{}
		}
		response.StructuredContent["final_observation_kind"] = "none"
	case contract.ObserveScreenshot:
		response, treeWasReturned = renderAXScreenshotObservation(snapshot, notes)
		observationRequired = !treeWasReturned
	case contract.ObserveTree:
		response = snapshot.withoutScreenshot().resultWithNotes(notes)
		fullTreeReturned = true
		treeWasReturned = true
		if response.StructuredContent == nil {
			response.StructuredContent = map[string]any{}
		}
		response.StructuredContent["final_observation_kind"] = "tree"
	case contract.ObserveFull:
		response = snapshot.resultWithNotes(notes)
		fullTreeReturned = true
		treeWasReturned = true
		if response.StructuredContent == nil {
			response.StructuredContent = map[string]any{}
		}
		response.StructuredContent["final_observation_kind"] = "full"
	default:
		if identityStabilized {
			response, deliveredLines, treeWasReturned,
				compactActionObservation, fullTreeReturned =
				s.renderAXWorkingObservation(snapshot, notes)
		} else if contentStabilized {
			response, deliveredLines, treeWasReturned,
				compactActionObservation, fullTreeReturned =
				s.renderAXContentStabilizedObservation(snapshot, notes)
		} else {
			response, deliveredLines, treeWasReturned,
				compactActionObservation, fullTreeReturned =
				s.renderAXDiffOrCompactObservation(snapshot, notes)
		}
		observationRequired = !treeWasReturned
	}
	deliveredBaselineAdvanced := s.rememberDeliveredActionObservation(
		snapshot,
		response,
		deliveredLines,
		treeWasReturned,
		fullTreeReturned,
		compactActionObservation,
	)
	if response.StructuredContent == nil {
		response.StructuredContent = map[string]any{}
	}
	response.StructuredContent["full_tree_returned"] = fullTreeReturned
	response.StructuredContent["tree_reference_only"] = false
	response.StructuredContent["final_observation_returned"] = treeWasReturned
	if (observation{mode: request.Observe}).resolvedMode() == contract.ObserveScreenshot {
		response.StructuredContent["screenshot_returned"] = treeWasReturned
	}
	response.StructuredContent["next_identical_observation_required"] =
		observationRequired
	response.StructuredContent["observation_required"] = observationRequired
	response.StructuredContent["compact_action_observation"] =
		compactActionObservation
	response.StructuredContent["delivered_snapshot_baseline_advanced"] =
		deliveredBaselineAdvanced
	if transientCommitStabilized {
		response.StructuredContent["transient_commit_stabilized"] = true
	}
	response.StructuredContent["window_identity_stabilization_attempted"] =
		identityStabilizationAttempted
	response.StructuredContent["content_stabilization_attempted"] =
		contentStabilizationAttempted
	if contentStabilized {
		response.StructuredContent["content_stabilized"] = true
	}
	if identityStabilizationAttempted {
		response.StructuredContent["window_identity_stabilization_trigger"] =
			identityStabilizationTrigger
	}
	if identityStabilized {
		response.StructuredContent["window_identity_stabilized"] = true
		if response.StructuredContent["final_observation_kind"] ==
			"working_tree" {
			response.StructuredContent["stabilized_window_working_tree"] = true
		}
	}
	if request.SemanticTarget != nil {
		response.StructuredContent["semantic_resolution_fresh"] = true
		response.StructuredContent["semantic_resolution_unique_required"] = true
		response.StructuredContent["semantic_resolution_unique_match"] = true
		if request.SemanticTarget.SourceIndex != nil {
			response.StructuredContent["numeric_index_promoted_to_semantic"] = true
			response.StructuredContent["promoted_numeric_index"] =
				*request.SemanticTarget.SourceIndex
		}
	}
	addTreeProjectionMetadata(&response, snapshot)
	if snapshot.externalDesktopTransition {
		if response.StructuredContent == nil {
			response.StructuredContent = map[string]any{}
		}
		response.StructuredContent["external_native_transition"] = true
		response.StructuredContent["continuation"] = map[string]any{
			"state":              "ready",
			"retry_same_request": false,
			"route":              "GUI",
			"next_mode":          "screenshot",
		}
		response.Content = append(response.Content, ContentItem{
			Type: "text",
			Text: "A new native desktop window became active outside the current AX provider. " +
				"Do not repeat the AX action; take one fresh GUI screenshot.\n",
		})
	}
	response = withActionOutcomeForRequest(response, request, immediateSnapshot)
	response = markUnchangedMutationUncertainty(
		response,
		semanticUnchanged,
		readPixelVerdict(notes),
	)
	return response
}

func markUnchangedMutationUncertainty(
	response ToolCallResult,
	semanticUnchanged bool,
	pixels pixelVerdict,
) ToolCallResult {
	if semanticUnchanged &&
		pixels != pixelsChanged &&
		axEvidenceReportsInputEmission(response.StructuredContent) {
		response.StructuredContent["effect_unknown"] = true
		response.StructuredContent["mutation_unknown"] = true
		response.StructuredContent["task_effect_verified"] = false
	}
	return response
}

func (s *Service) rememberDeliveredActionObservation(
	snapshot *AppSnapshot,
	response ToolCallResult,
	deliveredLines []string,
	treeWasReturned,
	fullTreeReturned,
	compactActionObservation bool,
) bool {
	if !treeWasReturned || compactActionObservation ||
		!snapshotTextReturned(response, snapshot, deliveredLines) {
		return false
	}
	s.rememberDeliveredSnapshot(snapshot)
	if fullTreeReturned &&
		providerCoverageSummary(snapshot.Annotations).AcquisitionIncomplete == 0 {
		s.rememberDeliveredTree(deliveredTreeKey(snapshot))
	}
	return true
}

func withActionOutcomeForRequest(
	response ToolCallResult,
	request AXServiceRequest,
	immediateSnapshot *AppSnapshot,
) ToolCallResult {
	if response.StructuredContent == nil {
		response.StructuredContent = map[string]any{}
	}
	response.StructuredContent["action_attempted"] = true
	response.StructuredContent["predicate_checked"] = false
	if request.Actionability != nil {
		response = attachActionabilityEvidence(
			response,
			finalizeActionabilityEvidence(
				*request.Actionability,
				request,
				response,
				true,
				immediateSnapshot.actionExecution,
			),
		)
	}
	return response
}
