package main

import (
	"strconv"
	"strings"
)

func (s *service) verifyResolvedAXRefWithProbe(
	app,
	displayRef string,
	target *semanticTarget,
	goal verifyGoal,
	timeoutMS,
	stableMS int,
	probe func(ProviderActionRequest) (*ProviderAccessibilityResult, error),
) toolCallResult {
	return s.verifyResolvedAXRefWithProbes(
		app,
		displayRef,
		target,
		goal,
		timeoutMS,
		stableMS,
		probe,
		nil,
	)
}

// withActionOutcome applies an outcome without request facts, so no
// actionability evidence is attached.
func withActionOutcome(
	response toolCallResult,
	plan settlePlan,
	outcome settleOutcome,
	immediateSnapshot *appSnapshot,
	immediateNotes []string,
) toolCallResult {
	return withActionOutcomeForRequest(
		response,
		axServiceRequest{},
		plan,
		outcome,
		immediateSnapshot,
		immediateNotes,
	)
}

func lookupElement(snapshot *appSnapshot, elementIndex string) (*elementRecord, error) {
	return lookupElementFor(snapshot, elementIndex, "", "")
}

func rawLinesByIndex(snapshot *appSnapshot) map[int]string {
	if snapshot == nil {
		return nil
	}
	lines := snapshot.rawTreeLines
	if len(lines) == 0 {
		lines = snapshot.TreeLines
	}
	return indexTreeLines(lines)
}

// linesByIndex indexes rendered tree lines by their leading element index.
// Truncation lines such as "… 12 more" do not start with a number and are
// skipped.
func linesByIndex(snapshot *appSnapshot) map[int]string {
	if snapshot == nil {
		return nil
	}
	return indexTreeLines(snapshot.TreeLines)
}

func indexTreeLines(lines []string) map[int]string {
	out := map[int]string{}
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		head := trimmed
		if space := strings.IndexByte(trimmed, ' '); space > 0 {
			head = trimmed[:space]
		}
		index, err := strconv.Atoi(head)
		if err != nil {
			continue
		}
		if _, seen := out[index]; !seen {
			out[index] = line
		}
	}
	return out
}

func (registry *RefRegistry) Acquire(
	appIdentity,
	windowHandle,
	semanticTarget string,
	role CanonicalRole,
	name,
	revision string,
	providerIdentity ...string,
) string {
	return registry.AcquireTyped(
		AXLocatorSemantic,
		appIdentity,
		windowHandle,
		semanticTarget,
		role,
		name,
		revision,
		providerIdentity...,
	)
}

func runtimeReportedFailure(response ProviderAccessibilityResult) toolCallResult {
	return (*service)(nil).runtimeReportedFailure("", axServiceRequest{}, response)
}

func (s *service) expandShortAXRef(app, address string) (string, error) {
	address = strings.TrimSpace(address)
	if !isShortAXRef(address) {
		return address, nil
	}
	entry, err := s.resolveShortAXRef(app, address)
	if err != nil {
		return "", err
	}
	return entry.SemanticTarget, nil
}
