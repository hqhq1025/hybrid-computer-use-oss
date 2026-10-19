package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

type textLimit struct {
	max   bool
	count int
}

func (limit textLimit) runtimeValue() any {
	if limit.max {
		return "max"
	}
	return limit.count
}

func (s *service) getAppState(
	app string,
	textLimit *textLimit,
	maxTreeNodes, maxTreeDepth *int,
	prune, includeScreenshot, forceFull *bool,
	view, scope string,
	observationRoot map[string]any,
	targetWindowHandle ...string,
) toolCallResult {
	// An empty app is valid and selects the current foreground application.
	// Sessions usually begin by asking what is on screen, which would
	// otherwise take an app listing, a guessed name and a second call.
	request := axServiceRequest{
		Operation:       axServiceObserve,
		App:             app,
		TreeView:        normalizedTreeView(view),
		Scope:           normalizedA11yScope(scope),
		ObservationRoot: observationRoot,
	}
	if len(targetWindowHandle) > 0 {
		request.TargetWindowHandle = strings.TrimSpace(targetWindowHandle[0])
	}
	if textLimit != nil {
		request.TextLimit = textLimit.runtimeValue()
	}
	if maxTreeNodes != nil {
		request.MaxTreeNodes = *maxTreeNodes
	}
	if maxTreeDepth != nil {
		request.MaxTreeDepth = *maxTreeDepth
	}
	request.Prune = prune
	// AX returns only the semantic tree by default. A window screenshot is
	// opt-in visual evidence, never a source of GUI coordinates.
	if includeScreenshot == nil {
		off := false
		request.IncludeScreenshot = &off
	} else {
		request.IncludeScreenshot = includeScreenshot
	}
	snapshot, notes, followsFailedAction, result := s.observeAppSnapshot(app, request)
	if result.IsError {
		return result
	}
	// Observations carry diagnostics too: an app can be alive while exposing
	// an empty accessibility tree, and without a note the agent would read
	// "nothing visible" as "the UI is empty".
	return withFailedActionContinuity(s.getAppStateResult(
		snapshot,
		notes,
		forceFull != nil && *forceFull,
	), followsFailedAction)
}

// observeAppSnapshot refreshes the action snapshot of an app and settles a
// pending observation owed after a stale-ref failure. It is the read step of
// observe; text and records rendering differ only after it.
func (s *service) observeAppSnapshot(
	app string, request axServiceRequest,
) (*appSnapshot, []string, bool, toolCallResult) {
	snapshot, notes, result := s.refreshSnapshot(app, request)
	if result.IsError {
		return nil, nil, false, result
	}
	followsFailedAction := false
	if note := s.consumeFailedObservation(app, snapshot); note != "" {
		notes = append([]string{note}, notes...)
		followsFailedAction = true
	}
	return snapshot, notes, followsFailedAction, toolCallResult{}
}

func deliveredTreeKey(snapshot *appSnapshot) string {
	if snapshot == nil {
		return ""
	}
	windowRef := snapshotLogicalWindowRef(snapshot)
	if windowRef == "" {
		return ""
	}
	identity := snapshot.SnapshotID
	if normalizedA11yScope(snapshot.projection.Scope) == a11yScopeContent &&
		snapshot.contentRevision != "" {
		identity = snapshot.contentRevision
	}
	return windowRef + "|" + identity + "|" +
		normalizedTreeView(snapshot.projection.View) + "|" +
		normalizedA11yScope(snapshot.projection.Scope) + "|" +
		snapshot.projection.Root
}

func repeatedTreeReferenceLines(snapshot *appSnapshot) []string {
	if snapshot == nil {
		return nil
	}
	return treeUnchangedReferenceLines(len(snapshot.TreeLines))
}

// treeUnchangedReferenceLines replaces a tree the model already holds. The
// model's copy is the last full tree plus every diff delivered after it, so
// the reference says where the state came from and how to get it again.
func treeUnchangedReferenceLines(lineCount int) []string {
	return []string{fmt.Sprintf(
		"Tree unchanged: identical to the state of this window already "+
			"delivered by an earlier result (last full tree plus later diffs), "+
			"so its %d lines are not repeated; the rN refs shown there still "+
			"apply. Call {\"mode\":\"observe\",\"observe\":\"full\"} "+
			"to get the whole tree again.",
		lineCount,
	)}
}

func deliveredSnapshotKey(snapshot *appSnapshot) string {
	if snapshot == nil {
		return ""
	}
	identity := snapshotLogicalWindowRef(snapshot)
	if identity == "" {
		return ""
	}
	return identity + "|" + normalizedTreeView(snapshot.projection.View) + "|" +
		normalizedA11yScope(snapshot.projection.Scope) + "|" +
		snapshot.projection.Root
}

func (s *service) rememberDeliveredTree(key string) {
	if key == "" {
		return
	}
	if len(s.observations.lastFullTree) >= maxDeliveredTrees &&
		!s.observations.lastFullTree[key] {
		// This is a short-lived per-task cache, not durable history. Clearing it
		// only permits a later full resend; it can never hide fresh evidence.
		s.observations.lastFullTree = map[string]bool{}
	}
	s.observations.lastFullTree[key] = true
}

func (s *service) rememberDeliveredSnapshot(snapshot *appSnapshot) {
	if snapshot == nil {
		return
	}
	if providerCoverageSummary(snapshot.Annotations).AcquisitionIncomplete > 0 {
		return
	}
	key := deliveredSnapshotKey(snapshot)
	if key == "" {
		return
	}
	if len(s.observations.lastComplete) >= maxDeliveredTrees*5 &&
		s.observations.lastComplete[key] == nil {
		s.observations.lastComplete = map[string]*appSnapshot{}
	}
	s.observations.lastComplete[key] = snapshot
}

func (s *service) deliveredSnapshotFor(snapshot *appSnapshot) *appSnapshot {
	key := deliveredSnapshotKey(snapshot)
	if key == "" {
		return nil
	}
	return s.observations.lastComplete[key]
}

// deliveredStateMatches reports whether the model already holds snapshot:
// the last state delivered for this window and projection, whether as a full
// tree or as a diff against an earlier delivered state, has the same provider
// snapshot ID and renders to the same tree lines. An action that returned a
// diff advances this baseline but not the full-tree ledger, so a following
// observe of an unchanged window would otherwise resend the whole tree.
func (s *service) deliveredStateMatches(snapshot *appSnapshot) bool {
	if snapshot == nil || snapshot.SnapshotID == "" {
		return false
	}
	delivered := s.deliveredSnapshotFor(snapshot)
	return delivered != nil &&
		delivered.SnapshotID == snapshot.SnapshotID &&
		delivered.contentRevision == snapshot.contentRevision &&
		slices.Equal(delivered.TreeLines, snapshot.TreeLines)
}

func snapshotLogicalWindowRef(snapshot *appSnapshot) string {
	if snapshot == nil {
		return ""
	}
	if isShortAXWindowRef(snapshot.publicWindowRef) {
		return snapshot.publicWindowRef
	}
	if snapshot.axSnapshot != nil &&
		isShortAXWindowRef(snapshot.axSnapshot.WindowRef) {
		return snapshot.axSnapshot.WindowRef
	}
	return ""
}

func (s *service) rememberExposedHiddenRef(
	snapshot *appSnapshot,
	record elementRecord,
) {
	key := deliveredSnapshotKey(snapshot)
	if key == "" {
		return
	}
	if len(s.exposedHiddenRefs) >= maxDeliveredTrees &&
		s.exposedHiddenRefs[key] == nil {
		s.exposedHiddenRefs = map[string]map[int]elementRecord{}
	}
	refs := s.exposedHiddenRefs[key]
	if refs == nil {
		refs = map[int]elementRecord{}
		s.exposedHiddenRefs[key] = refs
	}
	refs[record.Index] = record
}

func snapshotTextReturned(
	result toolCallResult,
	snapshot *appSnapshot,
	treeLines []string,
) bool {
	if snapshot == nil {
		return false
	}
	rendered := snapshot
	if treeLines != nil {
		clone := *snapshot
		clone.TreeLines = treeLines
		rendered = &clone
	}
	return strings.Contains(resultText(result), rendered.renderedText())
}

func (s *service) getAppStateResult(
	snapshot *appSnapshot,
	notes []string,
	forceFull bool,
) toolCallResult {
	if snapshot == nil {
		return snapshot.resultWithNotes(notes)
	}
	key := deliveredTreeKey(snapshot)
	delivered := key != "" && s.observations.lastFullTree[key]

	acquisitionComplete :=
		providerCoverageSummary(snapshot.Annotations).AcquisitionIncomplete == 0
	if acquisitionComplete && !forceFull &&
		((snapshot.identicalToPrevious && delivered) ||
			s.deliveredStateMatches(snapshot)) {
		result := snapshot.resultWithTreeOverride(
			notes,
			treeUnchangedReferenceLines(len(snapshot.TreeLines)),
		)
		result.StructuredContent = map[string]any{
			"snapshot_id":         snapshot.SnapshotID,
			"full_tree_returned":  false,
			"tree_reference_only": true,
		}
		addTreeProjectionMetadata(&result, snapshot)
		return result
	}

	result := snapshot.resultWithNotes(notes)
	fullTreeReturned := snapshotTextReturned(result, snapshot, nil)
	if key != "" && fullTreeReturned {
		s.rememberDeliveredSnapshot(snapshot)
		if providerCoverageSummary(snapshot.Annotations).AcquisitionIncomplete == 0 {
			s.rememberDeliveredTree(key)
		}
	}
	result.StructuredContent = map[string]any{
		"snapshot_id":         snapshot.SnapshotID,
		"full_tree_returned":  fullTreeReturned,
		"tree_reference_only": false,
	}
	addTreeProjectionMetadata(&result, snapshot)
	if !fullTreeReturned {
		result.StructuredContent["tree_delivery_incomplete"] = true
	}
	return result
}

func numericElementAddress(address string) bool {
	_, err := strconv.Atoi(strings.TrimSpace(address))
	return err == nil
}

func isShortAXRef(value string) bool {
	if len(value) < 2 || value[0] != 'r' {
		return false
	}
	index, err := strconv.Atoi(value[1:])
	return err == nil && index > 0
}

func isShortAXWindowRef(value string) bool {
	_, ok := parseMonotonicAXWindowRef(value)
	return ok
}

func isShortAXAppRef(value string) bool {
	_, ok := parseMonotonicAXAppRef(value)
	return ok
}

func (s *service) assignWindowRefs(snapshot *appSnapshot) {
	if s == nil || snapshot == nil || snapshot.WindowAbsent {
		return
	}
	appIdentity := appIdentityForSnapshot(snapshot)
	revision := snapshot.SnapshotID
	snapshot.publicAppRef = s.appRefRegistry.Acquire(
		snapshot.App.Name,
		snapshot.App.ProviderBusName,
		snapshot.App.ProcessGeneration,
		revision,
	)
	if selector := windowActionSelector(snapshot); selector != "" {
		var root *elementRecord
		for index := range snapshot.Elements {
			if len(snapshot.Elements[index].RuntimeID) == 1 &&
				windowActionRole(snapshot.Elements[index].ControlType) {
				root = &snapshot.Elements[index]
				break
			}
		}
		provider := []string{}
		if root != nil {
			provider = []string{
				root.ProviderWindowBusName,
				root.ProviderWindowObjectPath,
				root.ProviderWindowProcessGeneration,
				root.ProviderWindowRole,
				root.ProviderWindowName,
			}
		}
		for len(provider) < 5 {
			provider = append(provider, "")
		}
		snapshot.publicWindowRef = s.windowRefRegistry.Acquire(
			appIdentity,
			snapshot.WindowHandle,
			selector,
			revision,
			append(provider, snapshot.publicAppRef)...,
		)
	}
	for index := range snapshot.Windows {
		window := &snapshot.Windows[index]
		role := strings.TrimSpace(window.Role)
		if role == "" {
			role = "window"
		}
		window.publicRef = s.windowRefRegistry.Acquire(
			appIdentity,
			window.Handle,
			selectorHead(role, window.Title),
			revision,
			window.ProviderBusName,
			window.ProviderObjectPath,
			window.ProviderProcessGeneration,
			window.Role,
			window.Title,
			snapshot.publicAppRef,
		)
	}
}

func (s *service) resolveShortAXRef(
	app,
	address string,
) (*AXRefEntry, error) {
	if !isShortAXRef(address) {
		return nil, fmt.Errorf("%q is not a short AX ref", address)
	}
	if isShortAXAppRef(app) {
		entry, err := s.resolveShortAXRefOwned(address)
		if err != nil {
			return nil, err
		}
		if entry.AppRef != "" && entry.AppRef != strings.TrimSpace(app) {
			return nil, fmt.Errorf(
				"short AX ref %q belongs to app %q, not %q",
				address, entry.AppRef, app,
			)
		}
		return entry, nil
	}
	requestedIdentity := app
	if snapshot := s.currentSnapshot(app); snapshot != nil {
		if snapshot.axSnapshot != nil &&
			strings.TrimSpace(snapshot.axSnapshot.AppIdentity) != "" {
			requestedIdentity = snapshot.axSnapshot.AppIdentity
		} else if identity := appIdentityForSnapshot(snapshot); strings.TrimSpace(identity) != "" {
			requestedIdentity = identity
		}
	}
	return s.refRegistry.Resolve(address, requestedIdentity)
}

func (s *service) resolveShortAXRefOwned(address string) (*AXRefEntry, error) {
	if !isShortAXRef(address) {
		return nil, fmt.Errorf("%q is not a short AX ref", address)
	}
	return s.refRegistry.Resolve(address, "")
}

func (s *service) resolveShortAXWindowRef(
	app,
	address string,
) (*AXWindowRefEntry, error) {
	if !isShortAXWindowRef(address) {
		return nil, fmt.Errorf("%q is not a short AX window ref", address)
	}
	if isShortAXAppRef(app) {
		entry, err := s.resolveShortAXWindowRefOwned(address)
		if err != nil {
			return nil, err
		}
		if entry.AppRef != "" && entry.AppRef != strings.TrimSpace(app) {
			return nil, fmt.Errorf(
				"short AX window ref %q belongs to app %q, not %q",
				address, entry.AppRef, app,
			)
		}
		return entry, nil
	}
	requestedIdentity := app
	if snapshot := s.currentSnapshot(app); snapshot != nil {
		if snapshot.axSnapshot != nil &&
			strings.TrimSpace(snapshot.axSnapshot.AppIdentity) != "" {
			requestedIdentity = snapshot.axSnapshot.AppIdentity
		} else if identity := appIdentityForSnapshot(snapshot); identity != "" {
			requestedIdentity = identity
		}
	}
	return s.windowRefRegistry.Resolve(address, requestedIdentity)
}

func (s *service) resolveShortAXWindowRefOwned(
	address string,
) (*AXWindowRefEntry, error) {
	if !isShortAXWindowRef(address) {
		return nil, fmt.Errorf("%q is not a short AX window ref", address)
	}
	return s.windowRefRegistry.Resolve(address, "")
}

func (s *service) resolveShortAXAppRef(address string) (*AXAppRefEntry, error) {
	if !isShortAXAppRef(address) {
		return nil, fmt.Errorf("%q is not a short AX app ref", address)
	}
	return s.appRefRegistry.Resolve(address)
}

func (s *service) bindRuntimeAppIdentity(request *axServiceRequest) error {
	if request == nil || request.AppIdentity != nil ||
		!isShortAXAppRef(request.App) {
		return nil
	}
	entry, err := s.resolveShortAXAppRef(request.App)
	if err != nil {
		return err
	}
	request.AppIdentity = &ProviderAppIdentity{
		ProviderBusName:   entry.ProviderBusName,
		ProcessGeneration: entry.ProcessGeneration,
	}
	return nil
}

func assertionTargetFromRefEntry(entry *AXRefEntry) string {
	if entry == nil {
		return ""
	}
	target := strings.TrimSpace(entry.SemanticTarget)
	if target == "" || isSemanticRef(target) {
		return target
	}
	return encodeScopedSemanticRef(target, entry.WindowHandle)
}
