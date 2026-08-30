package main

import (
	"strconv"
	"strings"
)

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

func resultText(result toolCallResult) string {
	var text strings.Builder
	for _, item := range result.Content {
		if item.Type == "text" {
			text.WriteString(item.Text)
		}
	}
	return text.String()
}

func replaceToolResultText(
	result toolCallResult,
	internal,
	public string,
) toolCallResult {
	if internal == "" || internal == public {
		return result
	}
	for index := range result.Content {
		if result.Content[index].Type != "text" {
			continue
		}
		result.Content[index].Text = strings.ReplaceAll(
			result.Content[index].Text,
			internal,
			public,
		)
	}
	return result
}

func isNoInputRecoverablePrecondition(result toolCallResult) bool {
	if !result.IsError || result.StructuredContent == nil {
		return false
	}
	code, _ := result.StructuredContent["failure_code"].(string)
	phase, _ := result.StructuredContent["failure_phase"].(string)
	emitted, hasEmissionFact := result.StructuredContent["input_events_emitted"].(bool)
	partial, hasPartialFact :=
		result.StructuredContent["partial_input_possible"].(bool)
	unknown, hasUnknownFact :=
		result.StructuredContent["mutation_unknown"].(bool)
	complete, _ := result.StructuredContent["failure_facts_complete"].(bool)
	if phase != "precondition" ||
		!hasEmissionFact || !hasPartialFact || !hasUnknownFact ||
		emitted || partial || unknown {
		return false
	}
	switch code {
	case "stale_element_ref":
		return complete
	case "observation_incomplete":
		// The provider may be unable to prove a complete target snapshot while
		// still reporting the mutation boundary itself exactly.
		return true
	default:
		return false
	}
}

func failedObservationAliases(app string, snapshot *appSnapshot) []string {
	aliases := []string{app}
	if snapshot != nil {
		aliases = append(aliases,
			snapshot.sourceApp,
			snapshot.App.Name,
			snapshot.App.BundleIdentifier,
			strconv.Itoa(snapshot.App.PID),
		)
	}
	return aliases
}

func (s *service) rememberFailedObservation(app string, snapshot *appSnapshot) {
	if len(s.pendingFailedObservations) >= maxDeliveredTrees {
		s.pendingFailedObservations = nil
	}
	entry := failedObservationEntry{
		query:   looseKey(app),
		aliases: map[string]bool{},
	}
	if snapshot != nil {
		entry.windowHandle = snapshot.WindowHandle
	}
	for _, alias := range failedObservationAliases(app, snapshot) {
		key := looseKey(alias)
		if key != "" {
			entry.aliases[key] = true
		}
	}
	s.pendingFailedObservations = append(s.pendingFailedObservations, entry)
}

func failedObservationMatches(
	entry failedObservationEntry,
	app string,
	snapshot *appSnapshot,
) bool {
	query := looseKey(app)
	if query != "" && entry.query != "" &&
		(query == entry.query ||
			strings.Contains(query, entry.query) ||
			strings.Contains(entry.query, query)) {
		return true
	}
	if snapshot != nil &&
		entry.windowHandle != "" &&
		snapshot.WindowHandle != "" {
		if entry.windowHandle == snapshot.WindowHandle {
			return true
		}
		return false
	}
	for _, alias := range failedObservationAliases(app, snapshot) {
		needle := looseKey(alias)
		if needle == "" {
			continue
		}
		for candidate := range entry.aliases {
			if needle == candidate ||
				strings.Contains(needle, candidate) ||
				strings.Contains(candidate, needle) {
				return true
			}
		}
	}
	return false
}

func (s *service) hasPendingFailedObservation(app string) bool {
	for _, entry := range s.pendingFailedObservations {
		if failedObservationMatches(entry, app, nil) {
			return true
		}
	}
	return false
}

func (s *service) consumeFailedObservation(app string, snapshot *appSnapshot) string {
	for index, entry := range s.pendingFailedObservations {
		if !failedObservationMatches(entry, app, snapshot) {
			continue
		}
		s.pendingFailedObservations = append(
			s.pendingFailedObservations[:index],
			s.pendingFailedObservations[index+1:]...,
		)
		return "[continuity] This explicit observation follows a stale-reference " +
			"action that failed during precondition checking. That action emitted " +
			"no input and did not cause any UI change. The current UI state is an " +
			"independent later observation. Do not describe any difference, dialog " +
			"disappearance, or completed state as possible success of the failed " +
			"action. For a transient dialog, call find and use the returned rN ref."
	}
	return ""
}

func (s *service) staleObservationRequired(
	app string,
	before *appSnapshot,
	failed toolCallResult,
) toolCallResult {
	s.rememberFailedObservation(app, before)
	s.invalidateAccessibilitySnapshots()
	if len(failed.Content) == 0 {
		failed.Content = []contentItem{{
			Type: "text",
			Text: "stale element reference; the requested action was not executed",
		}}
	}
	failed.Content[0].Text = strings.TrimSpace(failed.Content[0].Text) +
		"\n\nSTALE ELEMENT REFERENCE RECOVERY REQUIRED\n" +
		"action_outcome=not_executed\n" +
		"input_events_emitted=false\n" +
		"observation_required=true\n" +
		"This action did not run and did not cause any UI change. " +
		"No fresh UI observation was performed after this failure. " +
		"Call AX mode=observe or mode=find before another accessibility action. " +
		"Any state returned by that later observation is independent evidence " +
		"and must not be attributed to this failed action. Do not describe a " +
		"later disappearance or completed state as possible success."
	if failed.StructuredContent == nil {
		failed.StructuredContent = map[string]any{}
	}
	failed.StructuredContent["action_outcome"] = "not_executed"
	failed.StructuredContent["failed_action_input_emitted"] = false
	failed.StructuredContent["failed_action_caused_ui_change"] = false
	failed.StructuredContent["observation_required"] = true
	failed.StructuredContent["current_ui_returned"] = false
	failed.StructuredContent["recovery_read_attempted"] = false
	return failed
}

func (s *service) recoverNoInputPreconditionObservation(
	app string,
	before *appSnapshot,
	request axServiceRequest,
	failed toolCallResult,
) toolCallResult {
	obs := observation{mode: request.Observe}
	if obs.resolvedMode() == observeNone {
		return s.staleObservationRequired(app, before, failed)
	}

	s.invalidateAccessibilitySnapshots()
	includeScreenshot := obs.resolvedMode() == observeScreenshot
	refresh := axServiceRequest{
		Operation:         axServiceObserve,
		App:               app,
		IncludeScreenshot: &includeScreenshot,
		Scope:             a11yScopeWindow,
		TreeView:          treeViewWorking,
	}
	snapshot, notes, observationResult := s.refreshSnapshot(app, refresh)
	if !observationResult.IsError {
		observationResult = s.renderA11yBatchFinalObservation(
			snapshot,
			append(
				[]string{
					"[recovery] The requested action was not executed. " +
						"This is an independent current-state observation " +
						"after its target became stale or unreadable.",
				},
				notes...,
			),
			obs,
		)
	}

	returned, _ :=
		observationResult.StructuredContent["final_observation_returned"].(bool)
	if observationResult.IsError || !returned {
		required := s.staleObservationRequired(app, before, failed)
		required.StructuredContent["recovery_read_attempted"] = true
		required.StructuredContent["recovery_observation_returned"] = false
		if code, _ :=
			observationResult.StructuredContent["failure_code"].(string); code != "" {
			required.StructuredContent["recovery_observation_failure_code"] = code
		}
		return required
	}

	merged := mergeA11yBatchFinalObservation(failed, observationResult)
	if merged.StructuredContent == nil {
		merged.StructuredContent = map[string]any{}
	}
	merged.StructuredContent["action_outcome"] = "not_executed"
	merged.StructuredContent["failed_action_input_emitted"] = false
	merged.StructuredContent["failed_action_caused_ui_change"] = false
	merged.StructuredContent["current_ui_returned"] = true
	merged.StructuredContent["current_state_cause"] = "independent"
	merged.StructuredContent["recovery_read_attempted"] = true
	merged.StructuredContent["recovery_observation_attempted"] = true
	merged.StructuredContent["recovery_observation_returned"] = true
	merged.StructuredContent["observation_required"] = false
	merged.StructuredContent["next_identical_observation_required"] = false
	merged.StructuredContent["continuation"] = map[string]any{
		"state":              "inspect_current_state",
		"route":              "continue_from_observation",
		"retry_same_request": false,
	}
	return merged
}

func withFailedActionContinuity(
	result toolCallResult,
	followsFailedAction bool,
) toolCallResult {
	if !followsFailedAction {
		return result
	}
	if result.StructuredContent == nil {
		result.StructuredContent = map[string]any{}
	}
	result.StructuredContent["follows_no_input_stale_failure"] = true
	result.StructuredContent["failed_action_caused_current_state"] = false
	result.StructuredContent["current_state_cause"] = "independent"
	return result
}

// incrementalTree 在安全且划算时，用"只给变化的行"替代整棵树。
//
// 实测（gedit 轨迹）：无结构变化的步骤上增量能省 62%，其中若干步的 diff 直接是 0；
// 但有结构变化的步骤上增量反而**亏 7%**——增删两边都要付钱，加起来超过全量。
// 所以不能无条件用增量。
//
// 判据是行数不变。这一条同时解决了两个问题：
//  1. 成本：行数不变意味着只有内容变了，diff 必然小于全量
//  2. 正确性：#15 实测表明结构一变 element_index 就永久重排（gedit 上 26%），
//     而行数不变正是"没有结构变化"的充分信号，此时索引 0% 漂移，
//     agent 可以安全沿用上一轮观测里的索引
//
// 两个条件恰好对齐，不需要额外的稳定标识。
