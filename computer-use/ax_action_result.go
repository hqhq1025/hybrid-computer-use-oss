package main

import (
	"fmt"
	"strings"
	"time"
)

func snapshotHasActionableElementRef(snapshot *appSnapshot) bool {
	if snapshot == nil || snapshot.axSnapshot == nil {
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
	return hasRef(snapshot.axSnapshot.Roots)
}

type providerTopLevelIdentity struct {
	busName           string
	objectPath        string
	processGeneration string
	roleFamily        string
}

func snapshotProviderTopLevelIdentity(
	snapshot *appSnapshot,
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

func transientTopLevelSnapshot(snapshot *appSnapshot) bool {
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

func commitLikeTransientAction(request axServiceRequest) bool {
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
	service *service,
	result toolCallResult,
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
	entry, err := service.windowRefRegistry.Resolve(currentRef, "")
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

func (s *service) stabilizePostActionTransientCommit(
	app string,
	after *appSnapshot,
	request axServiceRequest,
	plan settlePlan,
) (*appSnapshot, []string, bool, bool) {
	if s == nil ||
		plan.expect != nil ||
		(observation{mode: request.Observe}).resolvedMode() == observeNone ||
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
		sourceProbeRequest := axServiceRequest{
			Operation:          axServiceObserve,
			App:                app,
			IncludeScreenshot:  &withoutScreenshot,
			TargetWindowHandle: after.WindowHandle,
			SourceRef:          sourceRef,
			Scope:              a11yScopeWindow,
			TreeView:           treeViewWorking,
		}
		applyProjection(&sourceProbeRequest, after.projection)
		probe := *s
		probe.refRegistry = s.refRegistry.clone()
		probe.windowRefRegistry = s.windowRefRegistry.clone()
		probe.appRefRegistry = s.appRefRegistry.clone()
		_, _, sourceResult := probe.readSnapshot(app, sourceProbeRequest)
		replacement, proven :=
			provenGoneSourceReplacement(&probe, sourceResult)
		if !proven {
			continue
		}
		finalRequest := axServiceRequest{
			Operation:          axServiceObserve,
			App:                app,
			IncludeScreenshot:  request.IncludeScreenshot,
			ScreenshotScope:    request.ScreenshotScope,
			TargetWindowHandle: replacement.WindowHandle,
			SourceRef:          replacement.Ref,
			Scope:              a11yScopeWindow,
			TreeView:           treeViewWorking,
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

		s.refRegistry = probe.refRegistry
		s.windowRefRegistry = probe.windowRefRegistry
		s.appRefRegistry = probe.appRefRegistry
		s.guiLeaseState = probe.guiLeaseState
		s.findCursorRegistry.Reset()
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
	after *appSnapshot,
	obs observation,
) string {
	if obs.resolvedMode() != observeDiff ||
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

func (s *service) stabilizePostActionWindowIdentity(
	app string,
	before,
	after *appSnapshot,
	request axServiceRequest,
) (*appSnapshot, []string, bool, bool, string) {
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
	refresh := axServiceRequest{
		Operation:          axServiceObserve,
		App:                app,
		IncludeScreenshot:  &includeScreenshot,
		TargetWindowHandle: targetWindowHandle,
		Scope:              a11yScopeWindow,
		TreeView:           treeViewWorking,
	}
	applyProjection(&refresh, after.projection)
	probe := *s
	probe.refRegistry = s.refRegistry.clone()
	probe.windowRefRegistry = s.windowRefRegistry.clone()
	probe.appRefRegistry = s.appRefRegistry.clone()
	if trigger == "provider_window_change" &&
		isShortAXWindowRef(before.publicWindowRef) {
		probe.refRegistry.MarkByWindow(
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
	s.refRegistry = probe.refRegistry
	s.windowRefRegistry = probe.windowRefRegistry
	s.appRefRegistry = probe.appRefRegistry
	s.guiLeaseState = probe.guiLeaseState
	if trigger == "provider_window_change" {
		s.findCursorRegistry.Reset()
	}
	s.rememberSnapshot(app, refreshed)
	notes = append([]string{
		"[identity] The new top-level window was refreshed once without input; " +
			"the current wN/rN refs below are actionable.",
	}, notes...)
	return refreshed, notes, true, true, trigger
}

func shouldStabilizePostActionContent(
	service *service,
	after *appSnapshot,
	request axServiceRequest,
	plan settlePlan,
	identityStabilized bool,
) bool {
	if service == nil ||
		plan.expect != nil ||
		identityStabilized ||
		request.WindowAction != nil ||
		(observation{mode: request.Observe}).resolvedMode() != observeDiff ||
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

func (s *service) postActionContentWindowHandle(
	snapshot *appSnapshot,
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
	entry, err := s.windowRefRegistry.Resolve(windowRef, "")
	if err != nil || !validInternalWindowHandle(entry.WindowHandle) {
		return ""
	}
	return entry.WindowHandle
}

func (s *service) stabilizePostActionContent(
	app string,
	after *appSnapshot,
	request axServiceRequest,
	plan settlePlan,
	identityStabilized bool,
) (*appSnapshot, []string, bool, bool) {
	if !shouldStabilizePostActionContent(
		s,
		after,
		request,
		plan,
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
	refresh := axServiceRequest{
		Operation:          axServiceObserve,
		App:                app,
		IncludeScreenshot:  &includeScreenshot,
		TargetWindowHandle: windowHandle,
		SourceRef:          snapshotLogicalWindowRef(after),
		Scope:              a11yScopeWindow,
		TreeView:           treeViewWorking,
	}
	applyProjection(&refresh, after.projection)
	probe := *s
	probe.refRegistry = s.refRegistry.clone()
	probe.windowRefRegistry = s.windowRefRegistry.clone()
	probe.appRefRegistry = s.appRefRegistry.clone()
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
	s.refRegistry = probe.refRegistry
	s.windowRefRegistry = probe.windowRefRegistry
	s.appRefRegistry = probe.appRefRegistry
	s.guiLeaseState = probe.guiLeaseState
	s.rememberSnapshot(app, refreshed)
	notes = append([]string{
		"[content] The same logical window was refreshed once after a compact " +
			"post-action snapshot changed; the current working tree is included below.",
	}, notes...)
	return refreshed, notes, true, true
}

func (s *service) actionResult(app string, request axServiceRequest) toolCallResult {
	return s.actionResultSettling(app, request, settlePlan{})
}

func (s *service) actionResultSettling(app string, request axServiceRequest, plan settlePlan) toolCallResult {
	// 动作前的状态在 Go 侧已经缓存着了（动作工具契约要求先调 get_app_state，
	// 且每个动作自己也会刷新缓存），所以 before/after 比对不需要额外遍历一次树。
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
		// 失败也要留一行。只记成功的 trace 会让复盘时看到一串顺利的动作，
		// 而真正要查的那一步凭空消失。
		s.appendTrace(app, request, before, nil, nil, true)
		if request.SourceRef != "" &&
			request.SemanticTarget != nil &&
			request.SemanticTarget.LocatorKind == AXLocatorProviderBound {
			code, _ := result.StructuredContent["failure_code"].(string)
			if code == "stale_element_ref" || code == "stale_target" {
				s.refRegistry.MarkStale(request.SourceRef, code)
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
					plan,
					settleOutcome{},
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
	if snapshot.deliveryOnly {
		s.appendTrace(app, request, before, nil, notes, false)
		response := snapshot.notesOnlyResult(observeNoneNotes(notes))
		if response.StructuredContent == nil {
			response.StructuredContent = map[string]any{}
		}
		response.StructuredContent["final_observation_returned"] = false
		response.StructuredContent["observation_required"] = false
		response.StructuredContent["delivery_only"] = true
		return withActionOutcomeForRequest(
			response,
			request,
			plan,
			settleOutcome{},
			snapshot,
			notes,
		)
	}
	immediateSnapshot := snapshot
	immediateNotes := append([]string(nil), notes...)
	// act → settle → verify 的中段。放在这里（而不是所有 Note 攒完之后）是有讲究的：
	// 后面每一条判据——格式属性比对、"什么都没变"、增量树、以及 observe 决定发出去
	// 的那份树/图——都应当读**安置之后**的界面，否则会拿一份还没稳定下来的快照
	// 去下结论。
	var outcome settleOutcome
	if plan.expect != nil {
		outcome = s.settle(
			app, plan, before, snapshot, request.IncludeScreenshot, request.ScreenshotScope)
		if outcome.snapshot != nil {
			snapshot = outcome.snapshot
		}
		notes = settledActionNotes(immediateSnapshot, immediateNotes, outcome)
	}
	var transientCommitNotes []string
	var transientCommitStabilized bool
	snapshot, transientCommitNotes, _,
		transientCommitStabilized = s.stabilizePostActionTransientCommit(
		app,
		snapshot,
		request,
		plan,
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
		plan,
		identityStabilized,
	)
	notes = append(notes, contentStabilizationNotes...)
	if note := s.hiddenCanonicalAddressChangeNote(before, snapshot); note != "" {
		notes = append(notes, note)
	}
	// 格式属性的变化排在最前面：它说得出**什么变成了什么**，
	// 而像素只说得出**有事发生**、树只说得出**结构没变**。
	if changes := textAttributeChanges(before, snapshot); len(changes) > 0 {
		shown := changes
		if len(shown) > 4 {
			shown = append(append([]string{}, shown[:4]...),
				fmt.Sprintf("… and %d more", len(changes)-4))
		}
		notes = append([]string{"[text-attrs] Document text formatting changed, read from accessibility text attributes: " +
			strings.Join(shown, " | ") +
			". The snapshot renders structure, not formatting, so the tree can look unchanged while this does not. Treat this as supporting evidence rather than proof: a page can also restyle itself on its own. Obvious repaint noise (a web font replacing the fallback font, colour shifts too small to see) is already filtered out."}, notes...)
	}
	// verified 为真时整段"什么都没变"的讨论直接跳过：expect 是比树差分**更强**
	// 的证据，让两者同时说话只会自相矛盾（"没有可观测变化" vs "对话框出现了"）。
	// 顺带也不会去重试一个目标已经达成的动作。
	semanticUnchanged :=
		before != nil && !observablyChanged(before, snapshot) && !outcome.verified
	if semanticUnchanged {
		// 语义调用返回成功却什么都没发生——实测这不是罕见情况，Nautilus 文件
		// 图标的 `menu`、GIMP 图层 cell 的 `activate`、VLC 单选按钮的 `Toggle`
		// 都如此。此时 do_action 返回 True，
		// `auto` 的原有回落分支（只在返回 False 时触发）不会启动，
		// agent 手里没有任何一条能走通的路。
		//
		// 判据刻意只用**外部信号**：窗口标题、整棵树、焦点、选中。
		// 不读被操作节点自身的状态——VLC 那颗单选按钮 Toggle 之后 CHECKED
		// 真的翻转了、面板却不切换，读节点自身会把它判成"生效了"。
		//
		// **这个机制盖不住 VLC 那一类，要说清楚**：它只认"什么都没变"。
		// VLC 的案例是"状态变了、行为没变"——CHECKED 翻转会让树跟着变，
		// 于是这里判定"发生了变化"，不会重试。要接住那一类，需要知道
		// 这次动作**本该**造成什么后果（比如面板应当切换），
		// 那是任务级的语义，不是通用判据能给的。
		// 所以这里覆盖的是静默无操作，不是全部执行失败。
		//
		// 重复执行的风险是可控的：只有整棵树逐行相同、标题/焦点/选中都没变时
		// 才重试，也就是应用状态与动作前完全一致，此时再点一次等价于从同一
		// 状态点第一次。会漏判的只有"生效了但界面毫无痕迹"这一类，
		// 而当前的失败模式——静默无操作——是实测在 3 个应用上都会发生的。
		if windowFocusWasConfirmed(request) {
			// require_window_focus 只证明合成前目标应用窗口处于活动状态。
			// 它不识别窗口内的焦点控件，也不提供控件级送达或语义效果证据。
			notes = append(notes, unchangedNotes(true, readPixelVerdict(notes), readTextAttributeChange(notes))...)
		} else {
			notes = append(notes, unchangedNotes(false, readPixelVerdict(notes), readTextAttributeChange(notes))...)
		}
	}
	// expect 的结论写在最后，因为它是这次调用**最终的**答复：
	// 前面那些 Note 都是线索，这一条是结论。它进的是 notes，而 notes 每一档
	// observe 都会照发——包括 "none"。验证结论不是观测，省流量不该把结论也省掉。
	if plan.expect != nil {
		notes = append(notes, expectNote(plan, outcome, request, notes))
	}
	// 写在最后：此时 notes 已经攒齐（像素判据、状态回读、解析目标全在里面），
	// 而那些 Note 正是复盘时最想看的东西。
	s.appendTrace(app, request, before, snapshot, notes, false)
	// 观测预算在**最后**才生效：上面每一条证据（解析到了哪个元素、像素变了多少、
	// 有没有弹出模态框）都照常算完，observe 决定的只是**发出去多少**。
	// 服务端的快照缓存无论哪种模式都已经刷新过，所以 element_index 不会因为
	// 省流量而失效——这一点必须成立，否则 "none" 就成了另一个 P2。
	var response toolCallResult
	var deliveredLines []string
	treeWasReturned := false
	fullTreeReturned := false
	treeReferenceOnly := false
	compactActionObservation := false
	observationRequired := false
	switch (observation{mode: request.Observe}).resolvedMode() {
	case observeNone:
		response = snapshot.notesOnlyResult(observeNoneNotes(notes))
		if response.StructuredContent == nil {
			response.StructuredContent = map[string]any{}
		}
		response.StructuredContent["final_observation_kind"] = "none"
	case observeScreenshot:
		response, treeWasReturned = renderAXScreenshotObservation(snapshot, notes)
		observationRequired = !treeWasReturned
	case observeTree:
		if providerCoverageSummary(
			snapshot.Annotations,
		).AcquisitionIncomplete == 0 &&
			s.observations.lastFullTree[deliveredTreeKey(snapshot)] {
			deliveredLines = repeatedTreeReferenceLines(snapshot)
			response = snapshot.withoutScreenshot().resultWithTreeOverride(
				notes, deliveredLines)
			treeReferenceOnly = true
		} else {
			response = snapshot.withoutScreenshot().resultWithNotes(notes)
			fullTreeReturned = true
		}
		treeWasReturned = true
		if response.StructuredContent == nil {
			response.StructuredContent = map[string]any{}
		}
		response.StructuredContent["final_observation_kind"] = "tree"
	case observeFull:
		if providerCoverageSummary(
			snapshot.Annotations,
		).AcquisitionIncomplete == 0 &&
			s.observations.lastFullTree[deliveredTreeKey(snapshot)] {
			deliveredLines = repeatedTreeReferenceLines(snapshot)
			response = snapshot.resultWithTreeOverride(notes, deliveredLines)
			treeReferenceOnly = true
		} else {
			response = snapshot.resultWithNotes(notes)
			fullTreeReturned = true
		}
		treeWasReturned = true
		if response.StructuredContent == nil {
			response.StructuredContent = map[string]any{}
		}
		response.StructuredContent["final_observation_kind"] = "full"
	default:
		if identityStabilized || contentStabilized {
			response, deliveredLines, treeWasReturned,
				compactActionObservation, fullTreeReturned =
				s.renderAXWorkingObservation(snapshot, notes)
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
	response.StructuredContent["tree_reference_only"] = treeReferenceOnly
	response.StructuredContent["final_observation_returned"] = treeWasReturned
	if (observation{mode: request.Observe}).resolvedMode() == observeScreenshot {
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
		response.Content = append(response.Content, contentItem{
			Type: "text",
			Text: "A new native desktop window became active outside the current AX provider. " +
				"Do not repeat the AX action; take one fresh GUI screenshot.\n",
		})
	}
	response = withActionOutcomeForRequest(
		response,
		request,
		plan,
		outcome,
		immediateSnapshot,
		immediateNotes,
	)
	response = markUnchangedMutationUncertainty(
		response,
		semanticUnchanged,
		readPixelVerdict(notes),
	)
	return response
}

func markUnchangedMutationUncertainty(
	response toolCallResult,
	semanticUnchanged bool,
	pixels pixelVerdict,
) toolCallResult {
	if semanticUnchanged &&
		pixels != pixelsChanged &&
		axEvidenceReportsInputEmission(response.StructuredContent) {
		response.StructuredContent["effect_unknown"] = true
		response.StructuredContent["mutation_unknown"] = true
		response.StructuredContent["task_effect_verified"] = false
	}
	return response
}

func (s *service) rememberDeliveredActionObservation(
	snapshot *appSnapshot,
	response toolCallResult,
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
	response toolCallResult,
	request axServiceRequest,
	plan settlePlan,
	outcome settleOutcome,
	immediateSnapshot *appSnapshot,
	immediateNotes []string,
) toolCallResult {
	if response.StructuredContent == nil {
		response.StructuredContent = map[string]any{}
	}
	response.StructuredContent["action_attempted"] = true
	response.StructuredContent["predicate_checked"] = plan.expect != nil
	if plan.expect != nil {
		response.StructuredContent["predicate_verified"] = outcome.verified
		response.StructuredContent["verification_complete"] =
			!outcome.verificationIncomplete
		response.StructuredContent["followup_read_failed"] =
			outcome.followupReadFailed
		response.StructuredContent["settled_after_ms"] = outcome.elapsedMS
		response.StructuredContent["followup_snapshot_used"] =
			outcome.snapshotFromFollowup
		response.StructuredContent["response_snapshot_phase"] = "immediate"
		if immediateSnapshot != nil {
			response.StructuredContent["immediate_snapshot_id"] =
				immediateSnapshot.SnapshotID
		}
		if outcome.snapshot != nil {
			response.StructuredContent["final_snapshot_id"] =
				outcome.snapshot.SnapshotID
		}
		if outcome.snapshotFromFollowup {
			response.StructuredContent["response_snapshot_phase"] = "settled"
			if readPixelVerdict(immediateNotes) != pixelsUnknown {
				response.StructuredContent["pixel_evidence_phase"] =
					"immediate"
				response.StructuredContent["final_pixel_verdict"] =
					"unavailable"
			}
		} else if readPixelVerdict(immediateNotes) != pixelsUnknown {
			response.StructuredContent["pixel_evidence_phase"] =
				"immediate_and_final"
		}
	}
	if request.Actionability != nil {
		response = attachActionabilityEvidence(
			response,
			finalizeActionabilityEvidence(
				*request.Actionability,
				request,
				response,
				plan,
				outcome,
				true,
				immediateSnapshot.actionExecution,
			),
		)
	}
	return response
}

// withActionOutcome preserves the beta20 internal test seam. Production action
// paths call withActionOutcomeForRequest so typed actionability evidence has the
// exact request and target-resolution facts.
