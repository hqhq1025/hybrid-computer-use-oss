package main

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func sameCanonicalAddress(first, second elementRecord) bool {
	if first.ControlType != second.ControlType ||
		first.LocalizedControlType != second.LocalizedControlType ||
		first.Name != second.Name ||
		first.NameHash != second.NameHash ||
		first.AutomationID != second.AutomationID ||
		len(first.RuntimeID) != len(second.RuntimeID) {
		return false
	}
	for index := range first.RuntimeID {
		if first.RuntimeID[index] != second.RuntimeID[index] {
			return false
		}
	}
	return true
}

func (s *service) hiddenCanonicalAddressChangeNote(
	before,
	after *appSnapshot,
) string {
	if before == nil || after == nil {
		return ""
	}
	key := deliveredSnapshotKey(before)
	exposed := s.exposedHiddenRefs[key]
	if len(exposed) == 0 {
		return ""
	}
	afterRecords := map[int]elementRecord{}
	for _, record := range after.Elements {
		afterRecords[record.Index] = record
	}
	changed := []int{}
	rootChanged := before.WindowHandle != "" &&
		after.WindowHandle != "" &&
		before.WindowHandle != after.WindowHandle
	for index, first := range exposed {
		second, hadSecond := afterRecords[index]
		if !rootChanged && hadSecond && sameCanonicalAddress(first, second) {
			continue
		}
		changed = append(changed, index)
		delete(exposed, index)
	}
	if len(changed) == 0 {
		return ""
	}
	if len(exposed) == 0 {
		delete(s.exposedHiddenRefs, key)
	}
	return fmt.Sprintf(
		"[canonical-address] %d serializer-hidden ref(s) previously returned by find "+
			"were removed or rebound across this action. These refs are stale; call "+
			"find again before acting.",
		len(changed),
	)
}

// observablyChanged compares the parts of two snapshots that the agent can
// observe. Screenshots are excluded: pixel noise such as a blinking cursor
// would always register as a change.
func observablyChanged(before, after *appSnapshot) bool {
	if before == nil || after == nil {
		return true
	}
	if before.WindowTitle != after.WindowTitle ||
		before.FocusedSummary != after.FocusedSummary ||
		before.SelectedText != after.SelectedText {
		return true
	}
	if len(before.TreeLines) != len(after.TreeLines) {
		return true
	}
	for i := range before.TreeLines {
		if before.TreeLines[i] != after.TreeLines[i] {
			return true
		}
	}
	return false
}

// looseKey reduces an app name to lowercase letters and digits for lenient
// cache lookup. It must follow the same rule as loose() in the Linux provider;
// if the two diverged, an app could resolve but then miss the cache.
func looseKey(text string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(text) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func (s *service) currentSnapshot(app string) *appSnapshot {
	if snapshot, ok := s.snapshots[strings.ToLower(app)]; ok {
		return snapshot
	}
	// When the exact key misses, search again with the same lenient rule that
	// app-name resolution uses. Snapshots are cached under the resolved name
	// (for example "Google Chrome") while the next call may say "chrome"; a
	// strict lookup would report no app state right after a successful
	// observation.
	needle := looseKey(app)
	if needle == "" {
		return nil
	}
	var matched *appSnapshot
	for key, snapshot := range s.snapshots {
		tight := looseKey(key)
		if tight == "" {
			continue
		}
		if tight == needle || strings.Contains(tight, needle) || strings.Contains(needle, tight) {
			if matched == nil {
				matched = snapshot
				continue
			}
			if matched != snapshot {
				// Go map iteration order is deliberately unstable. Returning the
				// first of multiple snapshots would route an action nondeterministically.
				return nil
			}
		}
	}
	return matched
}

// readSnapshot captures a snapshot without writing the snapshot cache, so that
// read-only tools stay read-only. The cached snapshot is the address space for
// later actions, and replacing it is a side effect.
func (s *service) readSnapshot(app string, request axServiceRequest) (*appSnapshot, []string, toolCallResult) {
	if err := s.bindRuntimeAppIdentity(&request); err != nil {
		return nil, nil, textResult(
			"AX application target: "+err.Error(),
			true,
		).withEvidenceScope("error_only")
	}
	previous := s.currentSnapshot(app)
	diagnosticSnapshot := previous
	if diagnosticSnapshot == nil && strings.TrimSpace(app) == "" {
		diagnosticSnapshot = s.latestSnapshot()
	}
	if previous != nil && len(previous.Refs) > 0 {
		request.KnownRefs = previous.Refs
	}
	if expanded, err := s.expandObservationRootShortRef(
		app,
		request.ObservationRoot,
	); err != nil {
		return nil, nil, textResult(
			"AX observation root: "+err.Error(),
			true,
		).withEvidenceScope("error_only")
	} else {
		request.ObservationRoot = expanded
	}
	response, err := s.callProviderAccessibilityOperation(request)
	if err != nil {
		return nil, nil, s.runtimeFailureResult(request, diagnosticSnapshot, err)
	}
	if !response.OK {
		return nil, nil, s.runtimeReportedFailure(app, request, *response)
	}
	if isShortAXRef(request.SourceRef) && response.ResolvedElement != nil {
		allowProviderRebind := request.SemanticTarget != nil &&
			request.SemanticTarget.AllowProviderRebind
		s.refRegistry.RefreshProviderBinding(
			request.SourceRef,
			response.ResolvedElement,
			allowProviderRebind,
		)
	}
	if response.Snapshot == nil && response.ObservationSkipped &&
		accessibilityOperationCanMutate(request) {
		return &appSnapshot{
			sourceApp:                 app,
			actionExecution:           response.ActionEvidence,
			externalDesktopTransition: response.ExternalDesktopTransition,
			deliveryOnly:              true,
		}, response.Notes, toolCallResult{}
	}
	if response.Snapshot == nil {
		return nil, nil, s.missingRuntimeSnapshotResult(app, request, response)
	}
	// Tree paths are only stable inside one top-level window instance. If the
	// selected root changed, rerun once without old refs so a structurally
	// similar control in the new window cannot inherit an index from the old one.
	if !accessibilityOperationCanMutate(request) &&
		previous != nil && len(request.KnownRefs) > 0 &&
		previous.WindowHandle != "" && response.Snapshot.WindowHandle != "" &&
		previous.WindowHandle != response.Snapshot.WindowHandle {
		request.KnownRefs = nil
		response, err = s.callProviderAccessibilityOperation(request)
		if err != nil {
			return nil, nil, s.runtimeFailureResult(request, diagnosticSnapshot, err)
		}
		if !response.OK {
			return nil, nil, s.runtimeReportedFailure(app, request, *response)
		}
		if response.Snapshot == nil {
			return nil, nil, s.missingRuntimeSnapshotResult(app, request, response)
		}
	}
	snapshot, result := s.prepareProviderSnapshot(request, response)
	if result.IsError {
		return nil, nil, result
	}
	return snapshot, response.Notes, toolCallResult{}
}

func (s *service) prepareProviderSnapshot(
	request axServiceRequest,
	response *ProviderAccessibilityResult,
) (*appSnapshot, toolCallResult) {
	if response == nil || response.Snapshot == nil {
		return nil, textResult("provider snapshot preparation requires a canonical snapshot", true).
			withEvidenceScope("error_only")
	}
	response.Snapshot.rawTreeLines = append(
		[]string(nil),
		response.Snapshot.TreeLines...,
	)
	if accessibilityOperationCanMutate(request) && isShortAXRef(request.SourceRef) {
		response.Snapshot.actedRef = request.SourceRef
	}
	if err := applyAXScope(response.Snapshot, request.Scope); err != nil {
		return nil, textResult(err.Error(), true).
			withEvidenceScope("error_only")
	}
	if err := applyObservationRoot(
		response.Snapshot,
		request.ObservationRoot,
	); err != nil {
		return nil, textResult(err.Error(), true).
			withEvidenceScope("error_only")
	}
	baselineProjection := *response.Snapshot
	baselineProjection.TreeLines = append(
		[]string(nil),
		response.Snapshot.rawTreeLines...,
	)
	baselineProjection.rawTreeLines = nil
	// The baseline text projection is comparison evidence only. Apply its best-effort
	// scope on the clone, but never let missing provider text reject a typed
	// scope/root that resolved from canonical records.
	baselineRoot := response.Snapshot.scopeRoot
	if response.Snapshot.observationRoot != nil {
		baselineRoot = response.Snapshot.observationRoot
	}
	if baselineRoot != nil {
		if lines, _, baselineErr := scopedTreeLines(
			&baselineProjection, baselineRoot,
		); baselineErr == nil {
			baselineProjection.TreeLines = lines
		}
	}
	applyWorkingTreeProjection(&baselineProjection, request.TreeView)
	response.Snapshot.baselineProjectionLines = append(
		[]string(nil),
		baselineProjection.TreeLines...,
	)
	response.Snapshot.serializerOmitted =
		baselineProjection.serializerOmitted
	response.Snapshot.serializerBranches =
		baselineProjection.serializerBranches
	if err := s.buildAndRenderAXSnapshotV1(
		response.Snapshot,
		request.TreeView,
	); err != nil {
		var conflict *axProviderIdentityConflict
		if errors.As(err, &conflict) {
			return nil, s.providerIdentityConflictResult(request, response, conflict)
		}
		return nil, textResult(
			"AXSnapshotV1 pipeline failed: "+err.Error(),
			true,
		).withEvidenceScope("error_only")
	}
	response.Snapshot.actionExecution = response.ActionEvidence
	response.Snapshot.externalDesktopTransition =
		response.ExternalDesktopTransition
	return response.Snapshot, toolCallResult{}
}

func (s *service) providerIdentityConflictResult(
	request axServiceRequest,
	response *ProviderAccessibilityResult,
	conflict *axProviderIdentityConflict,
) toolCallResult {
	snapshot := response.Snapshot
	s.rememberFailedObservation(request.App, snapshot)
	s.pendingFailedObservations[len(s.pendingFailedObservations)-1].continuityNote =
		"[continuity] The previous accessibility acquisition contained contradictory " +
			"provider identity evidence. This independent observation does not establish " +
			"whether earlier input was accepted or persisted."
	if window := snapshotLogicalWindowRef(snapshot); window != "" {
		s.refRegistry.MarkByWindow(window, AXRefNeedsRevalidation, "provider_identity_conflict")
		s.windowRefRegistry.MarkLifecycle(window, AXRefNeedsRevalidation, "provider_identity_conflict")
	}
	s.invalidateAccessibilitySnapshots()
	failure := *response
	failure.prepareForService()
	emitted := failure.InputEventsEmitted ||
		(failure.ActionEvidence != nil && failure.ActionEvidence.InputEmitted)
	unknown := emitted || failure.PartialInputPossible || failure.DesktopStateMutated || failure.MutationUnknown
	failure.OK = false
	failure.Error = conflict.Error() + "; no trustworthy final accessibility observation is available. " +
		"Observe current state before further input; do not replay the action."
	failure.Snapshot = nil
	failure.Failure = &ProviderFailure{
		Code: "observation_incomplete", UnderlyingCode: "provider_identity_conflict",
		Phase: "observation", Subject: "element", FactsComplete: false,
	}
	failure.Mutation = &ProviderMutationFacts{
		InputEventsEmitted: emitted, PartialInputPossible: failure.PartialInputPossible,
		DesktopStateMutated: failure.DesktopStateMutated, MutationUnknown: unknown,
	}
	result := s.runtimeReportedFailure(request.App, request, failure).withEvidenceScope("error_only")
	result.StructuredContent["observation_required"] = true
	result.StructuredContent["final_observation_returned"] = false
	return result
}

func (s *service) missingRuntimeSnapshotResult(
	app string,
	request axServiceRequest,
	response *ProviderAccessibilityResult,
) toolCallResult {
	if response == nil {
		return textResult("The native provider did not return an app snapshot.", true).
			withEvidenceScope("error_only")
	}
	emitted := response.InputEventsEmitted ||
		response.PartialInputPossible ||
		response.DesktopStateMutated ||
		response.MutationUnknown
	if !emitted {
		return textResult("The native provider did not return an app snapshot.", true).
			withEvidenceScope("error_only")
	}
	response.OK = false
	response.Error = "post_action_observation_failed; the action outcome is unknown " +
		"because the native provider returned no final app snapshot; inspect fresh state " +
		"before retrying."
	response.Failure = &ProviderFailure{
		Code:           "effect_unknown",
		UnderlyingCode: "missing_post_action_snapshot",
		Phase:          "post_action_observation",
		FactsComplete:  false,
		Subject:        "element",
	}
	if isShortAXWindowRef(request.SourceRef) {
		response.Failure.Subject = "window"
	}
	if response.Mutation == nil {
		response.Mutation = &ProviderMutationFacts{}
	}
	response.Mutation.InputEventsEmitted = response.InputEventsEmitted
	response.Mutation.PartialInputPossible = response.PartialInputPossible
	response.Mutation.DesktopStateMutated = response.DesktopStateMutated
	response.Mutation.MutationUnknown = true
	response.prepareForService()
	return s.runtimeReportedFailure(app, request, *response)
}

func (s *service) expandObservationRootShortRef(
	app string,
	root map[string]any,
) (map[string]any, error) {
	if len(root) == 0 {
		return root, nil
	}
	raw, present := root["ref"]
	if !present {
		return root, nil
	}
	ref, ok := raw.(string)
	if !ok || !isShortAXRef(ref) {
		return root, nil
	}
	entry, err := s.resolveShortAXRef(app, ref)
	if err != nil {
		return nil, err
	}
	if entry.LocatorKind == AXLocatorProviderBound &&
		strings.TrimSpace(entry.ProviderBusName) != "" &&
		strings.TrimSpace(entry.ProviderObjectPath) != "" &&
		strings.TrimSpace(entry.ProcessGeneration) != "" {
		// Locator kind is authoritative. Provider-bound refs may retain a
		// semantic label for diagnostics, but it must never replace their exact
		// identity during rooted observation.
		return map[string]any{"_resolved_ref_entry": entry}, nil
	}
	expanded := assertionTargetFromRefEntry(entry)
	if strings.TrimSpace(expanded) != "" {
		return map[string]any{"ref": expanded}, nil
	}
	return nil, fmt.Errorf(
		"short AX ref %q has no stable observation target",
		ref,
	)
}

func (s *service) runtimeReportedFailure(
	app string,
	request axServiceRequest,
	response ProviderAccessibilityResult,
) toolCallResult {
	response.prepareForService()
	subject := strings.TrimSpace(strings.ToLower(response.FailureSubject))
	var sourceEntry *AXRefEntry
	if s != nil && isShortAXRef(request.SourceRef) {
		sourceEntry, _ = s.refRegistry.Resolve(request.SourceRef, "")
	}
	var sourceWindowEntry *AXWindowRefEntry
	if s != nil && isShortAXWindowRef(request.SourceRef) {
		sourceWindowEntry, _ = s.windowRefRegistry.Resolve(request.SourceRef, "")
	}
	s.syncRuntimeFailureLifecycle(request, response)
	return s.renderRuntimeReportedFailure(app, request, response, subject, sourceEntry, sourceWindowEntry)
}

func (s *service) syncRuntimeFailureLifecycle(request axServiceRequest, response ProviderAccessibilityResult) {
	response.prepareForService()
	subject := strings.TrimSpace(strings.ToLower(response.FailureSubject))
	if s != nil && isShortAXAppRef(request.App) && subject == "app" {
		switch {
		case response.FailureCode == "observation_incomplete" ||
			response.FailureCode == "ax_app_inventory_incomplete":
			s.markAppIdentityLifecycle(
				request.App, AXRefNeedsRevalidation, response.FailureCode,
			)
		case response.FailureCode == "stale_app_ref":
			lifecycle := AXRefStale
			if !response.FailureFactsComplete {
				lifecycle = AXRefNeedsRevalidation
			}
			s.markAppIdentityLifecycle(request.App, lifecycle, response.FailureCode)
		}
	}
	if s != nil && request.SourceRef != "" {
		switch {
		case isShortAXRef(request.SourceRef):
			if subject == "element" &&
				(response.FailureCode == "observation_incomplete" ||
					response.FailureCode == "semantic_target_scan_incomplete") {
				s.refRegistry.MarkNeedsRevalidation(
					request.SourceRef, response.FailureCode,
				)
			} else if subject == "element" &&
				request.SemanticTarget != nil &&
				request.SemanticTarget.LocatorKind == AXLocatorSemantic &&
				(response.FailureCode == "stale_element_ref" ||
					response.FailureCode == "stale_target") {
				s.refRegistry.MarkNeedsRevalidation(
					request.SourceRef, response.FailureCode,
				)
			} else if request.SemanticTarget != nil &&
				request.SemanticTarget.LocatorKind == AXLocatorProviderBound &&
				(response.FailureCode == "stale_element_ref" ||
					response.FailureCode == "stale_target") {
				if response.FailureFactsComplete {
					s.refRegistry.MarkStale(request.SourceRef, response.FailureCode)
				} else {
					s.refRegistry.MarkNeedsRevalidation(
						request.SourceRef, response.FailureCode,
					)
				}
			}
		case isShortAXWindowRef(request.SourceRef):
			if subject == "window" &&
				(response.FailureCode == "observation_incomplete" ||
					response.FailureCode == "semantic_target_scan_incomplete") {
				s.windowRefRegistry.MarkLifecycle(
					request.SourceRef, AXRefNeedsRevalidation,
					response.FailureCode,
				)
			} else if response.FailureCode == "stale_window_ref" ||
				response.FailureCode == "stale_window" ||
				(response.FailureCode == "stale_target" && subject == "window") {
				lifecycle := AXRefStale
				if !response.FailureFactsComplete {
					lifecycle = AXRefNeedsRevalidation
				}
				s.windowRefRegistry.MarkLifecycle(
					request.SourceRef, lifecycle, response.FailureCode,
				)
				if subject == "window" && response.FailureFactsComplete {
					s.refRegistry.MarkByWindow(request.SourceRef, lifecycle, response.FailureCode)
				}
			}
		}
	}
}

func (s *service) renderRuntimeReportedFailure(
	app string, request axServiceRequest, response ProviderAccessibilityResult,
	subject string, sourceEntry *AXRefEntry, sourceWindowEntry *AXWindowRefEntry,
) toolCallResult {
	result := textResult(publicRuntimeFailureText(response), true)
	if response.FailureCode == "" {
		return result
	}
	result.StructuredContent = map[string]any{
		"failure_code":           response.FailureCode,
		"failure_phase":          response.FailurePhase,
		"input_events_emitted":   response.InputEventsEmitted,
		"partial_input_possible": response.PartialInputPossible,
		"mutation_unknown":       response.MutationUnknown,
		"failure_facts_complete": response.FailureFactsComplete,
	}
	if response.UnderlyingFailureCode != "" {
		result.StructuredContent["underlying_failure_code"] =
			response.UnderlyingFailureCode
	}
	if subject != "" {
		result.StructuredContent["failure_subject"] = subject
	}
	continuation := map[string]any{
		"state":              "observe_required",
		"retry_same_request": false,
		"route":              "AX",
	}
	if response.FailureCode == "effect_unknown" {
		continuation["state"] = "verify_only"
	}
	result.StructuredContent["continuation"] = continuation
	if response.FocusPreparation != "" || response.PointerPreparation != "" {
		return preparationFailureResult(response, result)
	}
	s.attachRuntimeRecoveryContext(
		&result,
		request,
		sourceEntry,
		sourceWindowEntry,
		response.Recovery,
		continuation,
	)
	if s != nil && sourceEntry != nil && response.ReplacementWindow != nil &&
		replacementWindowContinuesSource(sourceEntry, response.ReplacementWindow) {
		replacement := response.ReplacementWindow
		windowRef := s.windowRefRegistry.Acquire(
			sourceEntry.AppIdentity,
			replacement.WindowHandle,
			"window",
			"",
			replacement.ProviderBusName,
			replacement.ProviderObjectPath,
			replacement.ProcessGeneration,
			replacement.Role,
			replacement.Title,
			sourceEntry.AppRef,
		)
		if windowRef != "" {
			result.StructuredContent["replacement_window_ref"] = windowRef
			result.StructuredContent["recovery"] = "find"
			continuation["state"] = "ready"
			continuation["fresh_window_ref"] = windowRef
			continuation["next_mode"] = "find"
			result.Content[0].Text += fmt.Sprintf(
				"\nreplacement_window_ref=%s recovery=find",
				windowRef,
			)
		}
	}
	if response.ActionEvidence != nil {
		result.StructuredContent["runtime_action_evidence"] =
			*response.ActionEvidence
	}
	if s != nil && response.FailureCode == "ax_app_ambiguous" {
		// Its candidates are application processes, not elements.
		s.describeAmbiguousApp(&result, response.Candidates)
		return result
	}
	if s != nil && len(response.Candidates) > 0 {
		snapshot := s.currentSnapshot(app)
		publicCandidates := make([]map[string]any, 0, len(response.Candidates))
		lines := []string{"Candidates:"}
		for _, candidate := range response.Candidates {
			if snapshot == nil {
				break
			}
			windowHandle := strings.TrimSpace(candidate.WindowHandle)
			if !validInternalWindowHandle(windowHandle) {
				continue
			}
			windowRef := ""
			for _, window := range snapshot.Windows {
				if window.Handle == windowHandle {
					windowRef = window.publicRef
					break
				}
			}
			if windowRef == "" {
				windowRef = s.windowRefRegistry.Acquire(
					appIdentityForSnapshot(snapshot),
					windowHandle,
					"window",
					snapshot.SnapshotID,
					"", "", "", "window", "",
					snapshot.publicAppRef,
				)
			}
			if windowRef == "" {
				continue
			}
			role := canonicalRoleDescriptor(candidate.Role, nil).ModelRole
			name := firstNonEmpty(candidate.Name, candidate.Description)
			selector := selectorHead(string(role), name)
			ref := s.refRegistry.AcquireTyped(
				AXLocatorProviderBound,
				appIdentityForSnapshot(snapshot),
				windowHandle,
				selector,
				role,
				name,
				snapshot.SnapshotID,
				candidate.ProviderBusName,
				candidate.ProviderObjectPath,
				candidate.ProcessGeneration,
				"", "", "", "", "",
				snapshot.publicAppRef,
				windowRef,
			)
			if ref == "" {
				continue
			}
			entry := map[string]any{
				"ref":        ref,
				"window_ref": windowRef,
				"path":       publicCandidatePath(candidate.Path),
			}
			publicCandidates = append(publicCandidates, entry)
			lines = append(lines, fmt.Sprintf(
				"- %s %s [window_ref=%s] path=%s",
				ref,
				selector,
				windowRef,
				publicCandidatePath(candidate.Path),
			))
		}
		if len(publicCandidates) > 0 {
			result.StructuredContent["candidates"] = publicCandidates
			result.StructuredContent["candidate_count"] = response.CandidateCount
			result.StructuredContent["candidates_truncated"] =
				response.CandidatesTruncated
			result.Content[0].Text += "\n" + strings.Join(lines, "\n") +
				"\nrecovery=choose_ref|find"
		}
	}
	return result
}

func (s *service) attachRuntimeRecoveryContext(
	result *toolCallResult,
	request axServiceRequest,
	sourceEntry *AXRefEntry,
	sourceWindowEntry *AXWindowRefEntry,
	recovery *ProviderRecoveryContext,
	continuation map[string]any,
) {
	if s == nil || result == nil || result.StructuredContent == nil ||
		recovery == nil {
		return
	}
	appStatus := strings.TrimSpace(strings.ToLower(recovery.SourceAppStatus))
	windowStatus := strings.TrimSpace(strings.ToLower(recovery.SourceWindowStatus))
	if appStatus != "" {
		result.StructuredContent["source_app_status"] = appStatus
	}
	if windowStatus != "" {
		result.StructuredContent["source_window_status"] = windowStatus
	}
	result.StructuredContent["recovery_inventory_complete"] =
		recovery.InventoryComplete

	appRef := ""
	appIdentity := ""
	windowRef := ""
	if sourceEntry != nil {
		appRef = sourceEntry.AppRef
		appIdentity = sourceEntry.AppIdentity
		windowRef = sourceEntry.LogicalWindowRef
	} else if sourceWindowEntry != nil {
		appRef = sourceWindowEntry.AppRef
		appIdentity = sourceWindowEntry.AppIdentity
		windowRef = sourceWindowEntry.Ref
	} else if isShortAXWindowRef(request.SourceRef) {
		if entry, err := s.windowRefRegistry.Resolve(request.SourceRef, ""); err == nil {
			appRef = entry.AppRef
			appIdentity = entry.AppIdentity
			windowRef = entry.Ref
		}
	}

	if recovery.InventoryComplete && appStatus == "exited" {
		if appRef != "" {
			s.markAppIdentityLifecycle(
				appRef,
				AXRefStale,
				"source_app_exited_after_input",
			)
		}
		result.StructuredContent["recovery"] = "observe_apps"
		continuation["state"] = "source_app_absent"
		continuation["next_mode"] = "observe"
		result.Content[0].Text +=
			"\nsource_app_status=exited source_window_status=gone recovery=observe_apps"
		return
	}

	if recovery.InventoryComplete && windowStatus == "gone" && windowRef != "" {
		s.windowRefRegistry.MarkLifecycle(
			windowRef,
			AXRefStale,
			"source_window_gone_after_input",
		)
		s.refRegistry.MarkByWindow(
			windowRef,
			AXRefStale,
			"source_window_gone_after_input",
		)
	}

	currentRef := ""
	currentOwnerMatches := false
	if appEntry, err := s.appRefRegistry.Resolve(appRef); err == nil &&
		recovery.CurrentWindow != nil {
		currentOwnerMatches =
			strings.TrimSpace(appEntry.ProviderBusName) ==
				strings.TrimSpace(recovery.CurrentWindow.ProviderBusName) &&
				strings.TrimSpace(appEntry.ProcessGeneration) ==
					strings.TrimSpace(recovery.CurrentWindow.ProcessGeneration)
	}
	if current := recovery.CurrentWindow; recovery.InventoryComplete &&
		current != nil && appRef != "" && appIdentity != "" &&
		validInternalWindowHandle(current.WindowHandle) &&
		currentOwnerMatches {
		currentRef = s.windowRefRegistry.Acquire(
			appIdentity,
			current.WindowHandle,
			selectorHead(
				string(canonicalRoleDescriptor(current.Role, nil).ModelRole),
				current.Title,
			),
			"",
			current.ProviderBusName,
			current.ProviderObjectPath,
			current.ProcessGeneration,
			current.Role,
			current.Title,
			appRef,
		)
	}
	if currentRef == "" && windowStatus == "present" && windowRef != "" {
		if _, err := s.windowRefRegistry.Resolve(windowRef, ""); err == nil {
			currentRef = windowRef
		}
	}
	if currentRef != "" {
		result.StructuredContent["current_window_ref"] = currentRef
		result.StructuredContent["recovery"] = "find"
		continuation["state"] = "ready"
		continuation["fresh_window_ref"] = currentRef
		continuation["next_mode"] = "find"
		result.Content[0].Text += fmt.Sprintf(
			"\nsource_app_status=%s source_window_status=%s "+
				"current_window_ref=%s recovery=find",
			appStatus,
			windowStatus,
			currentRef,
		)
	}
}

func publicCandidatePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	for _, pattern := range privateProviderIdentityPatterns {
		if pattern.MatchString(path) {
			return "semantic path unavailable"
		}
	}
	return path
}

func (s *service) markAppIdentityLifecycle(
	appRef string,
	lifecycle AXRefLifecycle,
	reason string,
) {
	if s == nil || strings.TrimSpace(appRef) == "" {
		return
	}
	s.appRefRegistry.MarkLifecycle(appRef, lifecycle, reason)
	s.windowRefRegistry.MarkByApp(appRef, lifecycle, reason)
	s.refRegistry.MarkByApp(appRef, lifecycle, reason)
}

var privateProviderIdentityPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:provider_identity_ambiguous|provider[^\s]*path)[^\n]*`),
	regexp.MustCompile(`/org/a11y/[^\s'",]+`),
	regexp.MustCompile(`(?i)\b(?:pid|process_generation)\s*[=:]\s*[^\s,;]+`),
	regexp.MustCompile(`\b:[0-9]+\.[0-9]+\b`),
	regexp.MustCompile(`\b(?:s[12]|w2):[A-Za-z0-9+/=_-]+\b`),
}

func publicRuntimeFailureText(response ProviderAccessibilityResult) string {
	text := strings.TrimSpace(response.Error)
	if strings.Contains(strings.ToLower(text), "provider_identity_ambiguous") {
		return "ambiguous_target: multiple accessibility projections matched; " +
			"choose one public candidate ref or observe again"
	}
	for _, pattern := range privateProviderIdentityPatterns {
		text = pattern.ReplaceAllString(
			text,
			"internal accessibility identity unavailable",
		)
	}
	for internal, public := range map[string]string{
		"provider_window_disappeared":   "target_window_changed",
		"provider_identity_disappeared": "target_identity_changed",
		"provider_identity_replaced":    "target_identity_changed",
		"provider_identity_changed":     "target_identity_changed",
	} {
		text = strings.ReplaceAll(text, internal, public)
	}
	return text
}

func replacementWindowContinuesSource(
	source *AXRefEntry,
	replacement *ProviderReplacementWindow,
) bool {
	if source == nil || replacement == nil {
		return false
	}
	return strings.TrimSpace(source.WindowHandle) != "" &&
		source.WindowHandle == strings.TrimSpace(replacement.WindowHandle) &&
		strings.TrimSpace(source.ProviderWindowBusName) != "" &&
		source.ProviderWindowBusName ==
			strings.TrimSpace(replacement.ProviderBusName) &&
		strings.TrimSpace(source.ProviderWindowProcessGeneration) != "" &&
		source.ProviderWindowProcessGeneration ==
			strings.TrimSpace(replacement.ProcessGeneration) &&
		windowRoleFamily(source.ProviderWindowRole) ==
			windowRoleFamily(replacement.Role)
}

func windowRoleFamily(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "frame", "window":
		return "window"
	case "alert", "dialog":
		return "dialog"
	default:
		return strings.ToLower(strings.TrimSpace(role))
	}
}

func (s *service) latestSnapshot() *appSnapshot {
	var latest *appSnapshot
	for _, snapshot := range s.snapshots {
		if snapshot == nil {
			continue
		}
		if latest == nil || snapshot.capturedAt.After(latest.capturedAt) {
			latest = snapshot
		}
	}
	return latest
}

func (s *service) runtimeFailureResult(
	request axServiceRequest,
	diagnosticSnapshot *appSnapshot,
	err error,
) toolCallResult {
	if result, rejected := runtimeRequestRejectionResult(err); rejected {
		return result
	}
	var timeout *runtimeTimeoutError
	var transport *runtimeTransportError
	if !errors.As(err, &timeout) && !errors.As(err, &transport) {
		return textResult(err.Error(), true)
	}
	phase := ""
	if timeout != nil {
		phase = strings.TrimSpace(timeout.phase)
	} else {
		phase = strings.TrimSpace(transport.phase)
	}
	if phase == "" {
		phase = "runtime"
	}
	if request.Operation == axServiceObserve {
		failureCode := "runtime_timeout"
		failureMarker := "runtime_timeout=true "
		if transport != nil {
			failureCode = "runtime_transport_failure"
			failureMarker = "runtime_transport_failure=true "
		}
		result := textResult(
			err.Error()+"\n"+failureMarker+"mutation_unknown=false "+
				s.runtimeTimeoutDiagnostic(diagnosticSnapshot), true)
		result.StructuredContent = map[string]any{
			"failure_code":         failureCode,
			"failure_phase":        phase,
			"input_events_emitted": false,
			"mutation_unknown":     false,
			"observation_required": true,
		}
		return result
	}
	mutationUnknown := accessibilityOperationCanMutate(request) &&
		(timeout != nil || (transport != nil && transport.requestSent))
	failureCode := "runtime_timeout"
	failureMarker := "runtime_timeout=true "
	if transport != nil {
		failureCode = "runtime_transport_failure"
		failureMarker = "runtime_transport_failure=true "
	}
	result := textResult(
		err.Error()+"\n"+failureMarker+
			fmt.Sprintf("mutation_unknown=%t ", mutationUnknown)+
			"a11y_responsive=unknown. This action runtime includes input, window, "+
			"pixel capture, settle, and A11Y work. Inspect fresh GUI state before "+
			"retrying whenever mutation_unknown=true.", true)
	result.StructuredContent = map[string]any{
		"failure_code":         failureCode,
		"failure_phase":        phase,
		"mutation_unknown":     mutationUnknown,
		"observation_required": true,
		"retry_same_request":   false,
	}
	return result
}

// readProjectedSnapshot samples live state without replacing the action cache.
//
// Assertions are read-only, but their queries still need the same projection
// as the cached action snapshot. Otherwise a default pruned read can silently
// replace or disagree with a preceding prune=false address space.
func (s *service) readProjectedSnapshot(app string, request axServiceRequest) (*appSnapshot, []string, toolCallResult) {
	if previous := s.currentSnapshot(app); previous != nil {
		applyProjection(&request, previous.projection)
	}
	return s.readSnapshot(app, request)
}

// refreshProjectedSnapshot samples live state in the existing address-space
// projection, then updates the action cache. find uses this path because every
// target it returns must be directly usable by the next action.
func (s *service) refreshProjectedSnapshot(app string, request axServiceRequest) (*appSnapshot, []string, toolCallResult) {
	if previous := s.currentSnapshot(app); previous != nil {
		applyProjection(&request, previous.projection)
	}
	return s.refreshSnapshot(app, request)
}

// continuityNotes describes how this snapshot relates to the previous one and
// fills in, in place, the snapshot fields that only the history-holding side
// can answer. It separates cases that otherwise look identical:
//
//  1. Whether an unchanged tree means that an asynchronous operation is still
//     running or that information was trimmed. Both look the same in the
//     tree, and a wrong guess costs a wasted retry.
//  2. Whether the root window changed. When an app opens a second window with
//     the same title, the root can switch silently and every element index
//     shifts while the returned text stays the same.
func continuityNotes(previous, current *appSnapshot) []string {
	if current == nil {
		return nil
	}
	current.comparedToPrevious = false
	current.identicalToPrevious = false
	current.previousSnapshotID = ""
	current.previousHandle = ""
	if previous == nil {
		return nil
	}
	if providerCoverageSummary(
		current.Annotations,
	).AcquisitionIncomplete > 0 {
		return nil
	}
	current.previousSnapshotID = previous.SnapshotID
	notes := []string{}
	if previous.WindowHandle != "" && current.WindowHandle != "" &&
		previous.WindowHandle != current.WindowHandle {
		current.previousHandle = previous.WindowHandle
		notes = append(notes, fmt.Sprintf(
			"ROOT WINDOW CHANGED: this snapshot is rooted at a different top-level "+
				"window than the previous model-visible snapshot for this app "+
				"(was titled %q; now titled %q). Window titles alone may not show "+
				"this — two windows of one app can carry the same title. Refs owned by "+
				"the previous window are stale; use the current rN/wN refs listed below.",
			previous.WindowTitle, current.WindowTitle))
		return notes
	}
	if previous.SnapshotID == "" || current.SnapshotID == "" {
		return notes
	}
	if normalizedAXScope(current.projection.Scope) == axScopeContent &&
		previous.contentRevision != "" && current.contentRevision != "" {
		current.comparedToPrevious = true
		current.identicalToPrevious =
			previous.contentRevision == current.contentRevision
		if !current.identicalToPrevious {
			return notes
		}
		gap := ""
		if !previous.capturedAt.IsZero() {
			gap = fmt.Sprintf(
				" %.1fs apart.",
				time.Since(previous.capturedAt).Seconds(),
			)
		}
		notes = append(notes, fmt.Sprintf(
			"CONTENT SCOPE UNCHANGED: the current content is identical to the previous "+
				"model-visible content for this window — same scoped "+
				"semantic tree and selected text.%s Browser/application chrome "+
				"outside this scope may have changed. This is not proof that a "+
				"visual, network, file, document, or application effect failed.",
			gap,
		))
		return notes
	}
	current.comparedToPrevious = true
	current.identicalToPrevious = previous.SnapshotID == current.SnapshotID
	if !current.identicalToPrevious {
		return notes
	}
	gap := ""
	if !previous.capturedAt.IsZero() {
		gap = fmt.Sprintf(" %.1fs apart.", time.Since(previous.capturedAt).Seconds())
	}
	// Report two tiers because the evidence differs in strength: an app that
	// reports busy is positive evidence for waiting, while its absence only
	// means "unknown" and never proves that the action failed.
	if current.Busy.busy() {
		notes = append(notes, fmt.Sprintf(
			"SNAPSHOT UNCHANGED, APP REPORTS BUSY: the current state is identical to the "+
				"previous model-visible state for this app "+
				"(same window, tree, focus and selection).%s "+
				"But the app is still working: the window carries an accessibility BUSY state "+
				"or exposes %d progress indicator(s). This is 'not finished yet', not "+
				"'nothing happened' and not 'information was trimmed away'. Wait and "+
				"call AX mode=observe again rather than repeating the action — repeating "+
				"it may apply it twice.",
			gap,
			current.Busy.ProgressElements))
		return notes
	}
	notes = append(notes, fmt.Sprintf(
		"SNAPSHOT UNCHANGED: the current state is identical to the previous one for this "+
			"app that was actually delivered to the model — same "+
			"window instance, same tree, same focus, same selection.%s "+
			"Nothing in the tree reports work in progress, but that is not proof your "+
			"last action failed: effects may not reach the accessibility tree. "+
			"Check the screenshot, or "+
			"wait for the specific element with an AX wait step before deciding to retry. "+
			"Note also what this line rules OUT: the content is not missing because a "+
			"budget trimmed it — this tree is the same one you already have.",
		gap))
	return notes
}

func (s *service) refreshSnapshot(app string, request axServiceRequest) (*appSnapshot, []string, toolCallResult) {
	// Capture the previous snapshot first: continuity is defined by the
	// relation between this snapshot and the last one. Only the refresh path
	// reports continuity; readSnapshot does not write the cache, and an
	// independent read must not narrate AX history it took no part in.
	internalPrevious := s.currentSnapshot(app)
	if internalPrevious != nil && request.Operation != axServiceObserve {
		applyProjection(&request, internalPrevious.projection)
	}
	snapshot, notes, result := s.readSnapshot(app, request)
	if result.IsError {
		return nil, nil, result
	}
	return s.commitPreparedSnapshot(app, request, snapshot, notes)
}

func (s *service) commitPreparedSnapshot(
	app string, request axServiceRequest, snapshot *appSnapshot, notes []string,
) (*appSnapshot, []string, toolCallResult) {
	if snapshot == nil {
		return nil, nil, textResult("provider snapshot commit requires a canonical snapshot", true).
			withEvidenceScope("error_only")
	}
	snapshot.projection = projectionFromRequest(request)
	if request.LightReadback {
		snapshot.lightReadback = true
		// The root-only read carries one ref. Keep the full ref map of the
		// same window, so that the next full read assigns the indices it
		// would have assigned without this read.
		if previous := s.currentSnapshot(app); previous != nil &&
			previous.WindowHandle == snapshot.WindowHandle &&
			len(previous.Refs) > 0 {
			snapshot.Refs = previous.Refs
		}
	}
	visiblePrevious := s.deliveredSnapshotFor(snapshot)
	notes = append(continuityNotes(visiblePrevious, snapshot), notes...)
	notes = s.dedupeDiagnosticNotes(app, notes)
	if !snapshot.deliveryOnly {
		s.rememberSnapshot(app, snapshot)
	}
	return snapshot, notes, toolCallResult{}
}

func observationPhaseAndPayload(note string) (string, string) {
	if !strings.HasPrefix(note, "[observation phase=") {
		return "", note
	}
	end := strings.Index(note, "] ")
	if end < 0 {
		return "", note
	}
	header := note[:end]
	phase := ""
	for _, field := range strings.Fields(strings.TrimPrefix(header, "[")) {
		if value, ok := strings.CutPrefix(field, "phase="); ok {
			phase = value
			break
		}
	}
	return phase, note[end+2:]
}

func diagnosticCategory(note string) string {
	_, note = observationPhaseAndPayload(note)
	for _, item := range []struct {
		prefix string
		key    string
	}{
		{"ROOT WINDOW CHANGED:", "root_window_changed"},
		{"CONTENT SCOPE UNCHANGED:", "content_scope_unchanged"},
		{"SNAPSHOT UNCHANGED, APP REPORTS BUSY:", "snapshot_busy"},
		{"SNAPSHOT UNCHANGED:", "snapshot_unchanged"},
		{"MODAL DIALOG:", "modal_dialog"},
		{"DIALOG IN FRONT:", "dialog_in_front"},
		{"CONTENT PLANE UNCERTAIN:", "content_plane_uncertain"},
		{"ACCESSIBILITY CONTENT UNCERTAIN:", "accessibility_content_uncertain"},
		{"ANOTHER APP IS IN FRONT:", "foreign_modal"},
		{"ANOTHER WINDOW MAY BE IN FRONT:", "foreign_window"},
	} {
		if strings.HasPrefix(note, item.prefix) {
			return item.key
		}
	}
	return ""
}

func (s *service) dedupeDiagnosticNotes(app string, notes []string) []string {
	key := strings.ToLower(strings.TrimSpace(app))
	previous := s.lastDiagnostics[key]
	current := map[string]string{}
	out := make([]string, 0, len(notes))
	for _, note := range notes {
		category := diagnosticCategory(note)
		if category == "" {
			out = append(out, note)
			continue
		}
		current[category] = note
		repeated := previous != nil && previous[category] == note
		if previous != nil &&
			(category == "snapshot_unchanged" ||
				category == "snapshot_busy" ||
				category == "content_scope_unchanged") &&
			previous[category] != "" {
			// These notes include a changing elapsed-time sentence. The diagnostic
			// state is still the same even when that duration changes.
			repeated = true
		}
		if repeated {
			out = append(out, fmt.Sprintf(
				"[diagnostic %s] still present; full explanation unchanged from the "+
					"previous response.", category))
			continue
		}
		out = append(out, note)
	}
	if len(current) == 0 {
		delete(s.lastDiagnostics, key)
	} else {
		s.lastDiagnostics[key] = current
	}
	return out
}

func (s *service) rememberSnapshot(query string, snapshot *appSnapshot) {
	// Replace every alias that pointed at the exact previous address space used
	// for this query. Otherwise `soffice` can refresh while an older title alias
	// still wins an exact lookup and revives stale indices.
	keys := []string{
		query,
		snapshot.publicAppRef,
		snapshot.App.Name,
		snapshot.App.BundleIdentifier,
		strconv.Itoa(snapshot.App.PID),
	}
	var previous *appSnapshot
	ambiguous := false
	for _, key := range keys {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		cached := s.snapshots[key]
		if cached == nil {
			continue
		}
		if previous == nil {
			previous = cached
		} else if previous != cached {
			ambiguous = true
			break
		}
	}
	if !ambiguous && previous != nil && previous != snapshot {
		for key, cached := range s.snapshots {
			if cached == previous {
				delete(s.snapshots, key)
			}
		}
	}
	snapshot.capturedAt = time.Now()
	snapshot.sourceApp = query
	for _, key := range keys {
		key = strings.ToLower(strings.TrimSpace(key))
		if key != "" {
			s.snapshots[key] = snapshot
		}
	}
}
