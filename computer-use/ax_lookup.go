package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type toolError struct {
	tool    string
	summary string
	fields  [][2]string
	log     []string
	next    string
}

func (e *toolError) add(name, value string) *toolError {
	if value != "" {
		e.fields = append(e.fields, [2]string{name, value})
	}
	return e
}

func (e *toolError) step(format string, args ...any) *toolError {
	e.log = append(e.log, fmt.Sprintf(format, args...))
	return e
}

func (e *toolError) Error() string {
	var b strings.Builder
	if e.tool != "" {
		b.WriteString(e.tool + ": ")
	}
	b.WriteString(e.summary)
	if len(e.fields) > 0 {
		width := 0
		for _, f := range e.fields {
			if len(f[0]) > width {
				width = len(f[0])
			}
		}
		b.WriteString("\n")
		for _, f := range e.fields {
			b.WriteString(fmt.Sprintf("\n%-*s %s", width+1, f[0]+":", f[1]))
		}
	}
	if len(e.log) > 0 {
		b.WriteString("\n\nCall log:")
		for _, line := range e.log {
			// 已经带缩进的是上一条的续行（候选列表那种），不再套 "- "。
			// 照 Playwright 的 call log 缩进规则：带前导空格的渲染成子行。
			if strings.HasPrefix(line, " ") {
				b.WriteString("\n  " + line)
			} else {
				b.WriteString("\n  - " + line)
			}
		}
	}
	if e.next != "" {
		b.WriteString("\n\nNext: " + e.next)
	}
	return b.String()
}

// snapshotAge 把"这份快照有多旧"写成一句话。
func snapshotAge(snapshot *appSnapshot) string {
	if snapshot == nil {
		return ""
	}
	indices := "no elements"
	if n := len(snapshot.Elements); n > 0 {
		indices = fmt.Sprintf("%d elements (indices %d..%d)", n,
			snapshot.Elements[0].Index, snapshot.Elements[n-1].Index)
	}
	if snapshot.capturedAt.IsZero() {
		return indices
	}
	return fmt.Sprintf("captured %.1fs ago, %s",
		time.Since(snapshot.capturedAt).Seconds(), indices)
}

// closestElements 找出与声明意图最像的几个元素，连同可直接使用的选择器一起给出。
// 对应 Playwright strict mode 违规时那份 "aka <可用的替代 locator>" 候选表。
func closestElements(snapshot *appSnapshot, declared string, limit int) []string {
	if snapshot == nil || strings.TrimSpace(declared) == "" {
		return nil
	}
	var tokens []string
	for _, token := range strings.FieldsFunc(strings.ToLower(declared), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len([]rune(token)) >= 2 && !genericIntentWords[token] {
			tokens = append(tokens, token)
		}
	}
	if len(tokens) == 0 {
		return nil
	}
	var out []string
	for _, record := range snapshot.Elements {
		if !recordInA11yScope(snapshot, &record) {
			continue
		}
		haystack := strings.ToLower(record.Name + " " + record.Description)
		hit := false
		for _, token := range tokens {
			if strings.Contains(haystack, token) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		line := fmt.Sprintf("    %-4d %s", record.Index, describeRecord(&record))
		if record.Name != "" {
			line = fmt.Sprintf("    %-4d %-26s aka %s %q",
				record.Index, describeRecord(&record), record.ControlType, record.Name)
		} else if record.Description != "" {
			line = fmt.Sprintf("    %-4d %-26s aka %s %q",
				record.Index, describeRecord(&record), record.ControlType, record.Description)
		}
		out = append(out, line)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func lookupElement(snapshot *appSnapshot, elementIndex string) (*elementRecord, error) {
	return lookupElementFor(snapshot, elementIndex, "", "")
}

// lookupElementFor 解析一个引用，失败时给出**完整的事实链**而不是一句话。
func lookupElementFor(snapshot *appSnapshot, elementIndex, declared, tool string) (*elementRecord, error) {
	fail := func(summary string) *toolError {
		e := &toolError{tool: tool, summary: summary}
		e.add("Requested", strconv.Quote(elementIndex))
		e.add("Declared", func() string {
			if declared == "" {
				return ""
			}
			return strconv.Quote(declared)
		}())
		e.add("Snapshot", snapshotAge(snapshot))
		return e
	}

	if isSemanticRef(elementIndex) {
		selector, windowHandle, err := decodeSemanticRefScope(elementIndex)
		if err != nil {
			e := fail("the semantic ref is malformed.").
				step("decoding the self-contained selector from %q", elementIndex)
			e.next = "call AX mode=find again and use the returned rN ref."
			return nil, e
		}
		if windowHandle != "" && snapshot.WindowHandle != windowHandle {
			e := fail(
				"stale_window: the target belongs to a different top-level window instance.",
			)
			e.next = "observe or find the target again in the current window."
			return nil, e
		}
		return lookupBySelectorFor(snapshot, selector, declared, tool)
	}

	index, err := strconv.Atoi(elementIndex)
	if err != nil {
		return lookupBySelectorFor(snapshot, elementIndex, declared, tool)
	}
	for _, record := range snapshot.Elements {
		if record.Index == index {
			copy := record
			return &copy, nil
		}
	}
	e := fail(fmt.Sprintf("target.index %d is not in the current snapshot.", index)).
		step("resolving target.index=%d against the snapshot of %q", index, snapshot.sourceApp).
		step("that snapshot has no such index")
	if candidates := closestElements(snapshot, declared, 5); len(candidates) > 0 {
		e.step("elements matching what you said you were targeting:")
		e.log = append(e.log, candidates...)
	}
	e.next = "call AX mode=observe or mode=find again and use the returned rN ref."
	return nil, e
}

const (
	semanticRefPrefix       = "s1:"
	scopedSemanticRefPrefix = "s2:"
)

type scopedSemanticRef struct {
	Selector     string `json:"selector"`
	WindowHandle string `json:"window"`
}

func encodeSemanticRef(selector string) string {
	return semanticRefPrefix +
		base64.RawURLEncoding.EncodeToString([]byte(selector))
}

func encodeScopedSemanticRef(selector, windowHandle string) string {
	selector = strings.TrimSpace(selector)
	windowHandle = strings.TrimSpace(windowHandle)
	if windowHandle == "" {
		return encodeSemanticRef(selector)
	}
	payload, err := json.Marshal(scopedSemanticRef{
		Selector:     selector,
		WindowHandle: windowHandle,
	})
	if err != nil {
		return ""
	}
	return scopedSemanticRefPrefix +
		base64.RawURLEncoding.EncodeToString(payload)
}

func isSemanticRef(value string) bool {
	return strings.HasPrefix(value, semanticRefPrefix) ||
		strings.HasPrefix(value, scopedSemanticRefPrefix)
}

func decodeSemanticRef(ref string) (string, error) {
	selector, _, err := decodeSemanticRefScope(ref)
	return selector, err
}

func decodeSemanticRefScope(ref string) (string, string, error) {
	prefix := ""
	switch {
	case strings.HasPrefix(ref, semanticRefPrefix):
		prefix = semanticRefPrefix
	case strings.HasPrefix(ref, scopedSemanticRefPrefix):
		prefix = scopedSemanticRefPrefix
	default:
		return "", "", errors.New("unsupported semantic ref version")
	}
	payload := strings.TrimPrefix(ref, prefix)
	if payload == "" {
		return "", "", errors.New("empty semantic ref")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", "", err
	}
	if prefix == scopedSemanticRefPrefix {
		var scoped scopedSemanticRef
		if err := json.Unmarshal(decoded, &scoped); err != nil {
			return "", "", fmt.Errorf("invalid scoped semantic ref: %w", err)
		}
		selector := strings.TrimSpace(scoped.Selector)
		windowHandle := strings.TrimSpace(scoped.WindowHandle)
		if selector == "" || len(selector) > 4096 {
			return "", "", errors.New("invalid semantic selector length")
		}
		if windowHandle == "" || len(windowHandle) > 512 {
			return "", "", errors.New("invalid semantic window scope")
		}
		return selector, windowHandle, nil
	}
	selector := strings.TrimSpace(string(decoded))
	if selector == "" || len(selector) > 4096 {
		return "", "", errors.New("invalid semantic selector length")
	}
	return selector, "", nil
}

func windowActionRole(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "frame", "window", "dialog", "alert",
		"file chooser", "color chooser":
		return true
	default:
		return false
	}
}

func windowActionSelector(snapshot *appSnapshot) string {
	if snapshot == nil || snapshot.WindowAbsent ||
		strings.TrimSpace(snapshot.WindowHandle) == "" {
		return ""
	}
	for index := range snapshot.Elements {
		record := &snapshot.Elements[index]
		if len(record.RuntimeID) != 1 || !windowActionRole(record.ControlType) {
			continue
		}
		selector := semanticSelectorForRecord(snapshot, *record)
		if selector == "" {
			selector = selectorHead(record.ControlType, record.Name)
		}
		return selector
	}
	return ""
}

func renderedWindowInventory(windows []topLevelWindow) []string {
	lines := make([]string, 0, len(windows))
	for _, window := range windows {
		if window.Selected {
			continue
		}
		role := strings.TrimSpace(window.Role)
		if role == "" {
			role = "window"
		}
		ref := window.publicRef
		if ref == "" {
			lines = append(lines, fmt.Sprintf(
				"  - %s %q [window-action-ref=unavailable]",
				role, window.Title,
			))
		} else {
			lines = append(lines, fmt.Sprintf(
				"  - %s %q [ref=%s]",
				role, window.Title, ref,
			))
		}
	}
	return lines
}

func selectorHead(role, name string) string {
	role = strings.TrimSpace(role)
	name = strings.TrimSpace(name)
	if name == "" {
		return role
	}
	if role == "" {
		return strconv.Quote(name)
	}
	return role + " " + strconv.Quote(name)
}

// semanticSelectorForRecord returns the shortest selector that uniquely
// resolves the record in this snapshot. It never uses index, path, geometry,
// state, or value because those are transient rather than semantic identity.
func semanticSelectorForRecord(
	snapshot *appSnapshot,
	record elementRecord,
) string {
	role := strings.TrimSpace(record.ControlType)
	candidates := []string{}
	if record.Name != "" {
		candidates = append(candidates, selectorHead(role, record.Name))
		if record.Description != "" &&
			record.Description != record.Name {
			candidates = append(candidates, fmt.Sprintf(
				"%s [desc=%s]",
				selectorHead(role, record.Name),
				strconv.Quote(record.Description),
			))
		}
		if record.Placeholder != "" {
			candidates = append(candidates, fmt.Sprintf(
				"%s [placeholder=%s]",
				selectorHead(role, record.Name),
				strconv.Quote(record.Placeholder),
			))
		}
	} else if record.Description != "" {
		candidates = append(candidates,
			fmt.Sprintf("%s [desc=%s]", role, strconv.Quote(record.Description)),
		)
	} else if record.Placeholder != "" {
		candidates = append(candidates,
			fmt.Sprintf("%s [placeholder=%s]", role, strconv.Quote(record.Placeholder)),
		)
	}
	for _, selector := range candidates {
		selector = strings.TrimSpace(selector)
		if selector == "" {
			continue
		}
		match, err := lookupBySelector(snapshot, selector, "")
		if err == nil && match.Index == record.Index {
			return selector
		}
	}
	return ""
}

func semanticRefForRecord(
	snapshot *appSnapshot,
	record elementRecord,
) string {
	selector := semanticSelectorForRecord(snapshot, record)
	if selector == "" {
		return ""
	}
	return encodeScopedSemanticRef(selector, snapshot.WindowHandle)
}

func elementIntentMismatch(record *elementRecord, declared string) string {
	declared = strings.TrimSpace(declared)
	if declared == "" || record == nil {
		return ""
	}
	// 只对**有名字**的元素校验。
	//
	// 第一版把 role 也算进可匹配范围，结果被最常见的词击穿：声明里几乎必然有
	// "button"，而 role 里也几乎必然有 "button"，于是"声明 Save 却给了 New 的
	// 下标"这种正要拦的情况照样放行（实测确认）。
	//
	// 反过来，无名元素的身份常常来自**旁边的标签**而不是自身属性——
	// 实测 LibreOffice 的「位置和大小」对话框，四个 spin button 全都没有名字，
	// "哪个是 Position Y" 只写在旁边的 label 上。这类元素上任何声明都匹配不了，
	// 硬校验就是纯误伤。**判不了就别判。**
	if strings.TrimSpace(record.Name) == "" {
		return ""
	}
	if !containsASCIILetter(declared) {
		// 声明是非 ASCII 文字（例如中文），而 name 通常是英文。判不了。
		return ""
	}
	// 名字太短时**同样判不了**，理由和无名元素一模一样。
	//
	// 实测一个筛选界面：agent 声明 "Size L checkbox"，
	// 元素是 `check box "L"`——**完全正确的操作，被守卫拒了**。
	// 因为下面的分词会跳过长度 < 2 的词元（那是为了滤掉 "a"/"of" 这类噪声），
	// 而这个元素的名字整个就是一个字符 "L"，于是任何声明都匹配不上，
	// 守卫**必然**误拒。
	//
	// 守卫的价值在于拦住"声明 Save 却给了 New 的下标"，代价是偶尔误伤；
	// 而这里不是"偶尔"——是这一类元素上 100% 误伤。尺码、单选字母、
	// 表格列标题都是这样。宁可漏判，不可必然误判。
	if len([]rune(strings.TrimSpace(record.Name))) <= 2 &&
		strings.TrimSpace(record.Description) == "" {
		return ""
	}
	haystack := strings.ToLower(record.Name + " " + record.Description)
	matched := false
	specific := 0
	for _, token := range strings.FieldsFunc(strings.ToLower(declared), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len([]rune(token)) < 2 || genericIntentWords[token] {
			continue
		}
		specific++
		if strings.Contains(haystack, token) {
			matched = true
			break
		}
	}
	if specific == 0 || matched {
		// 声明里没有区分性的实词（"the button"），或者对上了。
		return ""
	}
	return fmt.Sprintf(
		"target.index %d resolves to %s, which does not match the declared target (%q). "+
			"This usually means the index came from an earlier snapshot. "+
			"Call AX mode=observe or mode=find again and use a current target.ref, target.index, or target.selector.",
		record.Index, describeRecord(record), declared)
}

// 对任何元素都成立的词。拿它们判等于不判——而且第一版正是被 "button" 击穿的。
var genericIntentWords = map[string]bool{
	"the": true, "a": true, "an": true, "of": true, "in": true, "on": true,
	"for": true, "to": true, "this": true, "that": true, "and": true, "with": true,
	"button": true, "item": true, "box": true, "field": true, "menu": true,
	"list": true, "cell": true, "pane": true, "panel": true, "window": true,
	"dialog": true, "label": true, "icon": true, "tab": true, "bar": true,
	"view": true, "control": true, "element": true, "text": true, "entry": true,
	"checkbox": true, "check": true, "radio": true, "toggle": true, "spin": true,
	"push": true, "click": true, "select": true, "option": true,
}

func containsASCIILetter(text string) bool {
	for _, r := range text {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return true
		}
	}
	return false
}

// renderElementReference 是**所有**面向 agent 的元素指称的唯一出口：树行之外的
// find 结果、谓词证据、焦点摘要，全部经由这里。
//
// 唯一的硬性契约是**可回读**：这里吐出来的任何一串，原样贴回 expect 的 target
// （parseExpectTarget）都必须解析成同一个指称。三种形态各自对应 parser 的一条
// 分支——`push button "Save"` 走选择器，`"Save"` 走选择器的空角色分支，
// `role="push button"` 走字段式——所以三种都能回读。
//
// 这一条不是洁癖。此前焦点摘要走的是另一套渲染（runtime.py 的 role + " " + name，
// 无引号），于是我们一边告诉 agent "焦点在 entry Address and search bar"，
// 一边在它把这串贴进 expect 时判它不匹配：裸串横跨 role 与 name 两个字段，
// 结构性必假。我们打印的东西必须是我们自己认得的东西。
func renderElementReference(role, name string) string {
	role = strings.TrimSpace(role)
	name = strings.TrimSpace(name)
	switch {
	case role == "" && name == "":
		return "an element with neither a role nor a name"
	case name == "":
		// 无名元素不能写成散文（"an unnamed push button" 回读不了），
		// 写成字段式才既准确又能贴回去。
		return fmt.Sprintf("role=%q", role)
	case role == "":
		return fmt.Sprintf("%q", name)
	default:
		return fmt.Sprintf("%s %q", role, name)
	}
}

func describeRecord(record *elementRecord) string {
	if record == nil {
		return "(no element)"
	}
	return renderElementReference(record.ControlType, record.Name)
}

// renderElementAddress identifies one read-only target using the public field
// name. Mutation callers use rN refs instead of replaying this text.
func renderElementAddress(record *elementRecord) string {
	if record == nil {
		return "(no element)"
	}
	return fmt.Sprintf("index=%d, %s", record.Index, describeRecord(record))
}

// ---------------------------------------------------------------------------
// find：不用先把整棵树拉下来就能定位
//
// 这是我们与 Playwright 之间**身份级**的差距，不是体验问题。Playwright 的
// locator（`getByRole('button', {name:'OK'})`）从不要求你先 dump 一份 DOM；
// 我们此前只有"整棵拉下来再挑一个"这一条路——VS Code 的树 15342 字符，
// 为了点一个按钮让 agent 把这些读完，是纯粹的浪费。
//
// **必须说清它不省什么**：运行时那边照样遍历一整棵 AT-SPI 树，机器成本一分没少。
// 省的是 agent 的上下文。桌面上不存在 CSS 选择器那种能下推给引擎的查询，
// 遍历避不掉——这一点写进工具描述，免得调用方以为 find 比 get_app_state "快"。
// ---------------------------------------------------------------------------

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

// linesByIndex 把渲染好的树行按行首编号建索引。
//
// 刻意**复用 Python 渲染的原文**，而不在 Go 里再写一个渲染器：文法只有一份，
// 两份实现迟早会漂移，而 find 的输出和 get_app_state 的输出必须逐字同构——
// 否则 agent 得学两套读法。截断提示行（"… 12 more"）行首不是数字，自然被跳过。
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

func snapshotObservationBoundaries(snapshot *appSnapshot) []string {
	if snapshot == nil {
		return nil
	}
	lines := snapshot.rawTreeLines
	if len(lines) == 0 {
		lines = snapshot.TreeLines
	}
	markers := []string{
		"node(s) omitted —",
		"provider probe errors:",
		"contents not enumerated:",
		"hidden-items=",
		"render-failed path=",
		"empty cell(s) omitted;",
	}
	const maxBoundaries = 6
	const maxBoundaryChars = 1600
	out := []string{}
	seen := map[string]bool{}
	totalChars := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		matched := false
		for _, marker := range markers {
			if strings.Contains(trimmed, marker) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		if len(out) == maxBoundaries ||
			totalChars+len(trimmed) > maxBoundaryChars {
			out = append(
				out,
				"(additional observation boundaries omitted from this reply)",
			)
			break
		}
		seen[trimmed] = true
		out = append(out, trimmed)
		totalChars += len(trimmed)
	}
	return out
}

func findRecoveryAdvice(
	filterCount int,
	boundaries []string,
	canInspectFiltered bool,
) string {
	parts := []string{}
	if filterCount > 1 {
		parts = append(parts, "Relax one filter or try a broader synonym")
	} else {
		parts = append(parts, "Try a broader synonym or materialize the target UI")
	}
	hasFilteredOmission := false
	for _, boundary := range boundaries {
		lower := strings.ToLower(boundary)
		if strings.Contains(lower, " filtered:") ||
			strings.Contains(lower, " filtered out:") {
			hasFilteredOmission = true
			break
		}
	}
	if canInspectFiltered && hasFilteredOmission {
		parts = append(
			parts,
			"use AX mode=find or a rooted observation to inspect omitted canonical nodes",
		)
	}
	if len(boundaries) > 0 {
		parts = append(
			parts,
			"follow the cause-specific recovery in Observation boundaries below",
		)
	}
	return strings.Join(parts, "; ")
}
