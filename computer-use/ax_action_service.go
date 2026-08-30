package main

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

type actionElementResolution struct {
	snapshot  *appSnapshot
	element   *elementRecord
	semantic  *semanticTarget
	sourceRef string
	evidence  ActionabilityEvidence
}

func semanticTargetFromAddress(address string) (*semanticTarget, error) {
	selector := strings.TrimSpace(address)
	windowHandle := ""
	if isSemanticRef(selector) {
		decoded, scopedWindow, err := decodeSemanticRefScope(selector)
		if err != nil {
			return nil, fmt.Errorf("the semantic ref is malformed: %w", err)
		}
		selector = decoded
		windowHandle = scopedWindow
	}
	role, name, hasName, rest := parseSelector(selector)
	predicates, err := parsePredicates(rest)
	if err != nil {
		return nil, fmt.Errorf(
			"the selector %q has a trailing part I cannot read: %w. %s",
			selector, err, selectorPredicateHelp)
	}
	if strings.TrimSpace(role) == "" && !hasName && len(predicates) == 0 {
		return nil, fmt.Errorf("the semantic selector %q is empty", selector)
	}
	target := &semanticTarget{
		Selector:            selector,
		WindowHandle:        windowHandle,
		Role:                role,
		ProviderRoleAliases: providerAliasesForCanonicalRole(role),
		Name:                name,
		HasName:             hasName,
	}
	for _, predicate := range predicates {
		target.Predicates = append(target.Predicates, semanticTargetPredicate{
			Kind: predicate.kind, Value: predicate.value,
		})
	}
	return target, nil
}

func semanticTargetFromRefEntry(entry *AXRefEntry) (*semanticTarget, error) {
	if entry == nil {
		return nil, errors.New("the AX ref entry is missing")
	}
	var target *semanticTarget
	var err error
	if strings.TrimSpace(entry.SemanticTarget) != "" {
		target, err = semanticTargetFromAddress(entry.SemanticTarget)
		if err != nil {
			return nil, err
		}
	} else if entry.LocatorKind == AXLocatorProviderBound {
		target = &semanticTarget{
			Selector: selectorHead(string(entry.Role), entry.Name),
			Role:     string(entry.Role),
			Name:     entry.Name,
			HasName:  strings.TrimSpace(entry.Name) != "",
		}
	} else {
		return nil, errors.New("the AX ref has no stable target identity")
	}
	target.WindowHandle = firstNonEmpty(
		target.WindowHandle,
		entry.WindowHandle,
	)
	target.LocatorKind = entry.LocatorKind
	target.ProviderBusName = entry.ProviderBusName
	target.ProviderObjectPath = entry.ProviderObjectPath
	target.ProcessGeneration = entry.ProcessGeneration
	target.ProviderIdentityScope = entry.ProviderIdentityScope
	target.ProviderWindowBusName = entry.ProviderWindowBusName
	target.ProviderWindowObjectPath = entry.ProviderWindowObjectPath
	target.ProviderWindowProcessGeneration =
		entry.ProviderWindowProcessGeneration
	target.ProviderWindowRole = entry.ProviderWindowRole
	target.ProviderWindowName = entry.ProviderWindowName
	target.ProviderRoleAliases = providerAliasesForCanonicalRole(
		string(entry.Role),
	)
	target.RuntimeID = append([]int(nil), entry.RuntimeID...)
	target.RuntimeIDAliases = make([][]int, 0, len(entry.RuntimeIDAliases))
	for _, path := range entry.RuntimeIDAliases {
		target.RuntimeIDAliases = append(
			target.RuntimeIDAliases,
			append([]int(nil), path...),
		)
	}
	return target, nil
}

func (s *service) authorizeProviderRebind(
	app string,
	entry *AXRefEntry,
	target *semanticTarget,
) {
	if s == nil || entry == nil || target == nil ||
		entry.LocatorKind != AXLocatorSemantic ||
		!validInternalWindowHandle(entry.WindowHandle) {
		return
	}
	snapshot := s.currentSnapshot(app)
	coverage := axCoverageSummary{}
	if snapshot != nil {
		coverage = providerCoverageSummary(snapshot.Annotations)
	}
	if snapshot == nil || snapshot.axSnapshot == nil ||
		coverage.AcquisitionIncomplete > 0 ||
		coverage.ExpandableHidden > 0 ||
		snapshot.axSnapshot.Revision == "" ||
		snapshot.axSnapshot.Revision != entry.LastSeenRevision ||
		snapshot.WindowHandle != entry.WindowHandle ||
		snapshotLogicalWindowRef(snapshot) != entry.LogicalWindowRef {
		return
	}
	target.AllowProviderRebind = true
}

func semanticTargetIntentMismatch(target *semanticTarget, declared string) string {
	declared = strings.TrimSpace(declared)
	if declared == "" || target == nil {
		return ""
	}
	name := strings.TrimSpace(target.Name)
	description := ""
	for _, predicate := range target.Predicates {
		if predicate.Kind == "desc" {
			description = predicate.Value
			if name == "" {
				name = predicate.Value
			}
		}
	}
	if name == "" || !containsASCIILetter(declared) {
		return ""
	}
	if len([]rune(name)) <= 2 && strings.TrimSpace(description) == "" {
		return ""
	}
	haystack := strings.ToLower(name + " " + description)
	specific := 0
	for _, token := range strings.FieldsFunc(strings.ToLower(declared), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len([]rune(token)) < 2 || genericIntentWords[token] {
			continue
		}
		specific++
		if strings.Contains(haystack, token) {
			return ""
		}
	}
	if specific == 0 {
		return ""
	}
	return fmt.Sprintf(
		"semantic selector %q does not match what you said you were targeting (%q). "+
			"No input was emitted. Use a selector/ref that names the intended control, "+
			"or correct the element description.",
		target.Selector, declared)
}

// resolveActionElement keeps numeric addresses bound to the cached snapshot.
// Semantic selectors and refs are only parsed here: the Python runtime resolves
// them against the live tree inside the same invocation that emits input.
func (s *service) resolveActionElement(
	app,
	address,
	declared,
	tool string,
) (*actionElementResolution, toolCallResult) {
	address = strings.TrimSpace(address)
	if address == "" {
		return nil, textResult(
			tool+" requires a current rN element ref or wN window ref.", true)
	}
	if isShortAXRef(address) {
		entry, err := s.resolveShortAXRef(app, address)
		if err != nil {
			return nil, textResult(tool+": "+err.Error(), true)
		}
		target, targetErr := semanticTargetFromRefEntry(entry)
		if targetErr != nil {
			return nil, textResult(tool+": "+targetErr.Error(), true)
		}
		s.authorizeProviderRebind(app, entry, target)
		return &actionElementResolution{
			snapshot:  s.currentSnapshot(app),
			semantic:  target,
			sourceRef: address,
			evidence:  initialActionabilityEvidence(tool, nil, target),
		}, toolCallResult{}
	}
	if isShortAXWindowRef(address) {
		if tool != "activate" && tool != "close" {
			return nil, textResult(
				tool+": window refs can only be used with activate or close",
				true,
			).withEvidenceScope("error_only")
		}
		entry, err := s.resolveShortAXWindowRef(app, address)
		if err != nil {
			return nil, textResult(
				tool+": stale_window; "+err.Error()+
					"; input_emitted=false; recovery=observe",
				true,
			).withEvidenceScope("error_only")
		}
		target, targetErr := semanticTargetFromAddress(entry.SemanticTarget)
		if targetErr != nil {
			return nil, textResult(
				tool+": invalid_target; input_emitted=false; recovery=observe",
				true,
			).withEvidenceScope("error_only")
		}
		target.WindowHandle = entry.WindowHandle
		target.ProviderWindowBusName = entry.ProviderWindowBusName
		target.ProviderWindowObjectPath = entry.ProviderWindowObjectPath
		target.ProviderWindowProcessGeneration =
			entry.ProviderWindowProcessGeneration
		target.ProviderWindowRole = entry.ProviderWindowRole
		target.ProviderWindowName = entry.ProviderWindowName
		return &actionElementResolution{
			snapshot:  s.currentSnapshot(app),
			semantic:  target,
			sourceRef: address,
			evidence:  initialActionabilityEvidence(tool, nil, target),
		}, toolCallResult{}
	}

	if !numericElementAddress(address) {
		target, err := semanticTargetFromAddress(address)
		if err != nil {
			return nil, textResult(tool+": "+err.Error(), true)
		}
		if mismatch := semanticTargetIntentMismatch(target, declared); mismatch != "" {
			result := textResult(mismatch, true)
			result.StructuredContent = map[string]any{
				"action_attempted":     false,
				"input_events_emitted": false,
			}
			return nil, result
		}
		snapshot := s.currentSnapshot(app)
		if target.WindowHandle == "" && snapshot != nil {
			target.WindowHandle = snapshot.WindowHandle
		}
		return &actionElementResolution{
			snapshot: snapshot,
			semantic: target,
			evidence: initialActionabilityEvidence(
				tool, nil, target,
			),
		}, toolCallResult{}
	}

	snapshot := s.currentSnapshot(app)
	if snapshot == nil {
		return nil, textResult(
			"No app state is available for "+app+
				". Run AX mode=observe or find and use the returned current rN ref.",
			true)
	}

	record, err := lookupElementFor(snapshot, address, declared, tool)
	if err != nil {
		result := textResult(err.Error(), true)
		result.StructuredContent = map[string]any{}
		result.StructuredContent["action_attempted"] = false
		result.StructuredContent["input_events_emitted"] = false
		return nil, result
	}
	if mismatch := elementIntentMismatch(record, declared); mismatch != "" {
		result := textResult(mismatch, true)
		result.StructuredContent = map[string]any{}
		result.StructuredContent["action_attempted"] = false
		result.StructuredContent["input_events_emitted"] = false
		return nil, result
	}
	if selector := semanticSelectorForRecord(snapshot, *record); selector != "" &&
		strings.TrimSpace(snapshot.WindowHandle) != "" {
		ref := encodeScopedSemanticRef(selector, snapshot.WindowHandle)
		target, targetErr := semanticTargetFromAddress(ref)
		if targetErr == nil {
			sourceIndex := record.Index
			target.SourceIndex = &sourceIndex
			return &actionElementResolution{
				snapshot: snapshot,
				semantic: target,
				evidence: initialActionabilityEvidence(
					tool, record, target,
				),
			}, toolCallResult{}
		}
	}
	return &actionElementResolution{
		snapshot: snapshot,
		element:  record,
		evidence: initialActionabilityEvidence(
			tool, record, nil,
		),
	}, toolCallResult{}
}

func applyActionElementResolution(
	request *axServiceRequest,
	resolution *actionElementResolution,
) {
	if request == nil || resolution == nil {
		return
	}
	request.Element = resolution.element
	request.SemanticTarget = resolution.semantic
	request.SourceRef = resolution.sourceRef
	evidence := resolution.evidence
	request.Actionability = &evidence
	// A numeric element carries geometry from the exact cached snapshot it was
	// resolved from. A semantic target deliberately omits stale geometry: the
	// runtime computes current bounds while resolving the live node.
	if resolution.element != nil && resolution.snapshot != nil {
		request.WindowBounds = resolution.snapshot.WindowBounds
	}
}

func (s *service) click(app, elementIndex, declared string, x, y *float64, clickCount int, mouseButton, clickMethod string, obs observation, plan settlePlan) toolCallResult {
	if app == "" {
		return textResult("Missing required argument: app", true)
	}
	if x != nil || y != nil {
		// 通道要能从工具名上看出来。坐标点击**不定位任何元素**——谁在那个点上
		// 谁收到——所以它是另一个工具，不是 click 的一个参数变体。
		return textResult("AX click does not accept x/y. Use target.ref with a current rN; use GUI for full-desktop pixel coordinates.", true)
	}
	if elementIndex == "" {
		return textResult("click requires target.ref with a current rN. If find cannot issue a stable ref, use GUI with coordinates from its full-desktop screenshot.", true)
	}
	if clickMethod == "accessibility" && elementIndex == "" {
		return textResult("click_method 'accessibility' requires target.ref", true)
	}
	if clickMethod == "app_post" {
		return textResult("click_method 'app_post' is not supported on Linux", true)
	}
	if clickMethod == "sky_click" {
		return textResult("click_method 'sky_click' is not supported on Linux", true)
	}
	// 这里原本有一道 COMPUTER_USE_ALLOW_GLOBAL_POINTER_FALLBACKS 闸门，
	// 挡"把指针甩到屏幕上任意一点"。现在 click 必须带 element_index，
	// 裸坐标全部走 gui，这道闸门在这里已经不可达。
	//
	// 元素点击的合成兜底仍由 runtime.py 的 screen_point 从窗口相对 Frame
	// 换算到屏幕坐标。独立 GUI track 不走这条路径：gui 接受全桌面截图的
	// 屏幕绝对坐标，既不加窗口原点，也不夹紧到目标窗口。
	resolution, result := s.resolveActionElement(
		app, elementIndex, declared, "click")
	if result.IsError {
		return result
	}
	request := axServiceRequest{
		App: app,
		Action: axClickCommand{
			X: x, Y: y, ClickCount: clickCount,
			MouseButton: mouseButton, ClickMethod: clickMethod,
		},
		// nil = 按当前策略（带图）。调用方显式传 false 时才省。
		IncludeScreenshot: obs.resolvedScreenshot(),
		Observe:           obs.resolvedMode(),
		CaptureSnapshot:   obs.captureSnapshot,
	}
	applyActionElementResolution(&request, resolution)
	return s.actionResultSettling(app, request, plan)
}

func (s *service) windowAction(
	app,
	elementIndex,
	declared,
	action string,
	obs observation,
	plan settlePlan,
) toolCallResult {
	if strings.TrimSpace(app) == "" {
		return textResult("Missing required argument: app", true)
	}
	if action != "activate" && action != "close" {
		return textResult("unsupported AX window action: "+action, true)
	}
	resolution, result := s.resolveActionElement(
		app, elementIndex, declared, action)
	if result.IsError {
		return result
	}
	windowOperation := WindowOperation(action)
	request := axServiceRequest{
		WindowAction: &windowOperation, App: app,
		IncludeScreenshot: obs.resolvedScreenshot(),
		Observe:           obs.resolvedMode(),
		CaptureSnapshot:   obs.captureSnapshot,
	}
	applyActionElementResolution(&request, resolution)
	return s.actionResultSettling(app, request, plan)
}

func (s *service) scroll(
	app, direction, elementIndex, declared, targetWindowHandle string,
	expectedWindow *ProviderWindowExpectation,
	pages float64,
	obs observation,
	plan settlePlan,
) toolCallResult {
	if app == "" {
		return textResult("Missing required argument: app", true)
	}
	// element_index **可以留空**：那表示"滚当前窗口"，落点取窗口中心。
	//
	// 轨迹证据：scroll 12 次调用错 4 次（33%），全部是
	//     scroll: no element matches the selector "document web"
	// ——agent 想滚的是整页，却被迫先说出"哪个元素是这一页"，
	// 而网页根节点的角色在不同站点上并不统一（document web / document frame /
	// section 都出现过），猜错就报错。
	//
	// 滚轮本来就作用在**指针下方的可滚动祖先**上（见 runtime.py 的
	// scroll_element），窗口中心几乎总是主内容区。要滚某个特定面板时再传
	// element_index，那条路径一个字没变。
	normalized := strings.ToLower(direction)
	if normalized != "up" && normalized != "down" && normalized != "left" && normalized != "right" {
		return textResult("Invalid scroll direction: "+direction, true)
	}
	if pages <= 0 {
		return textResult("pages must be > 0", true)
	}
	if elementIndex == "" {
		return s.actionResultSettling(app, axServiceRequest{
			App: app,
			Action: axScrollCommand{
				Direction: normalized, Pages: pages, WindowCentre: true,
			},
			TargetWindowHandle: targetWindowHandle,
			ExpectedWindow:     expectedWindow,
			IncludeScreenshot:  obs.resolvedScreenshot(), Observe: obs.resolvedMode(),
			CaptureSnapshot: obs.captureSnapshot,
		}, plan)
	}
	resolution, result := s.resolveActionElement(
		app, elementIndex, declared, "scroll")
	if result.IsError {
		return result
	}
	request := axServiceRequest{
		App:                app,
		Action:             axScrollCommand{Direction: normalized, Pages: pages},
		TargetWindowHandle: targetWindowHandle,
		IncludeScreenshot:  obs.resolvedScreenshot(), Observe: obs.resolvedMode(),
		CaptureSnapshot: obs.captureSnapshot,
	}
	applyActionElementResolution(&request, resolution)
	return s.actionResultSettling(app, request, plan)
}

func (s *service) typeText(
	app, elementIndex, declared, targetWindowHandle string,
	expectedWindow *ProviderWindowExpectation,
	text string,
	obs observation,
	plan settlePlan,
) toolCallResult {
	if app == "" {
		return textResult("Missing required argument: app", true)
	}
	if text == "" {
		return textResult("Missing required argument: text", true)
	}
	if elementIndex == "" && strings.TrimSpace(declared) != "" {
		return textResult("targeted type requires target.ref; refusing to silently fall back to untargeted input", true)
	}
	request := axServiceRequest{
		App: app, Action: axTypeCommand{Text: text},
		TargetWindowHandle: targetWindowHandle,
		ExpectedWindow:     expectedWindow,
		IncludeScreenshot:  obs.resolvedScreenshot(), Observe: obs.resolvedMode(),
		CaptureSnapshot: obs.captureSnapshot,
	}
	if elementIndex != "" {
		var result toolCallResult
		var resolution *actionElementResolution
		resolution, result = s.resolveActionElement(
			app, elementIndex, declared, "type")
		if result.IsError {
			return result
		}
		applyActionElementResolution(&request, resolution)
	}
	return s.actionResultSettling(app, request, plan)
}

func (s *service) pressKey(
	app, elementIndex, declared, targetWindowHandle string,
	expectedWindow *ProviderWindowExpectation,
	key string,
	obs observation,
	plan settlePlan,
) toolCallResult {
	if app == "" {
		return textResult("Missing required argument: app", true)
	}
	if key == "" {
		return textResult("Missing required argument: key", true)
	}
	if elementIndex == "" && strings.TrimSpace(declared) != "" {
		return textResult("targeted keypress requires target.ref; refusing to silently fall back to untargeted input", true)
	}
	request := axServiceRequest{
		App: app, Action: axKeypressCommand{Key: key},
		TargetWindowHandle: targetWindowHandle,
		ExpectedWindow:     expectedWindow,
		IncludeScreenshot:  obs.resolvedScreenshot(), Observe: obs.resolvedMode(),
		CaptureSnapshot: obs.captureSnapshot,
	}
	if elementIndex != "" {
		var result toolCallResult
		var resolution *actionElementResolution
		resolution, result = s.resolveActionElement(
			app, elementIndex, declared, "keypress")
		if result.IsError {
			return result
		}
		applyActionElementResolution(&request, resolution)
	}
	return s.actionResultSettling(app, request, plan)
}

func (s *service) setValue(app, elementIndex, declared, value string, obs observation, plan settlePlan) toolCallResult {
	if app == "" {
		return textResult("Missing required argument: app", true)
	}
	if elementIndex == "" {
		return textResult("set_value requires target.ref with a current rN", true)
	}
	resolution, result := s.resolveActionElement(
		app, elementIndex, declared, "set_value")
	if result.IsError {
		return result
	}
	request := axServiceRequest{
		App: app, Action: axSetValueCommand{Value: value},
		IncludeScreenshot: obs.resolvedScreenshot(), Observe: obs.resolvedMode(),
		CaptureSnapshot: obs.captureSnapshot,
	}
	applyActionElementResolution(&request, resolution)
	return s.actionResultSettling(app, request, plan)
}

// windowFocusWasConfirmed 只记录合成前是否确认过目标应用窗口处于活动状态。
// 它不证明某个控件持有输入焦点，也不证明输入送达了某个控件。
// click 不在此列：它可能走的是语义通道，压根没经过窗口焦点确认。
//
// 措辞改过一次，因为它**说过头了**。真实 agent 轨迹里，ctrl+s 之后旧 Note
// 对无变化作了因果推断，而文件**其实存下来了**。agent 于是多花
// 截图 + 点 Save 按钮**两步去自证**，占那次 12 步里的 17%。
//
// 成因是这条判据的结构性盲区：格式类改动与文件状态**根本不进 a11y 树**
// （同一轮还实测到右对齐生效而树字节不变）。所以"树没变"在这里推不出
// "没生效"，只能推出"树看不见"。截图现在恒带，那才是这类效果的可判之处
// ——把 agent 指过去，而不是替它下一个会错的结论。
