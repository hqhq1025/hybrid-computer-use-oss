package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

func lookupBySelector(snapshot *appSnapshot, selector, hint string) (*elementRecord, error) {
	return lookupBySelectorFor(snapshot, selector, "", "")
}

func lookupBySelectorFor(snapshot *appSnapshot, selector, declared, tool string) (*elementRecord, error) {
	role, name, hasName, rest := parseSelector(selector)
	preds, predErr := parsePredicates(rest)
	if predErr != nil {
		e := &toolError{tool: tool, summary: fmt.Sprintf(
			"the selector %q has a trailing part I cannot read: %s.", selector, predErr)}
		e.add("Selector", strconv.Quote(selector))
		e.add("Snapshot", snapshotAge(snapshot))
		e.step("parsed selector as role=%q name=%q", role, name)
		e.step("left over after the name: %q", rest)
		e.next = selectorPredicateHelp
		return nil, e
	}
	// 身份来源有两级，**优先级照 Playwright 的 selector generator**：
	// 它给 role+accessible-name 打 100 分，给 label / alt-text 打 140–160
	// （分数越低越优先），都远好于 nth= 的 10000 分。
	//
	// 我们对应的两级是 name 与 description。加上 description 这一级是实测决定的：
	// 七个应用 704 个节点里，只按 role+name 能唯一指认的占 55%，
	// 加上 description 之后到 66%；GIMP 上从 57 个涨到 114 个（**翻一倍**），
	// 因为它 170 个节点里 95 个有 description、只有 61 个有 name。
	//
	// 顺带记一条**已经查实的死路**：AT-SPI 的 accessible_id（对应 Playwright
	// 打 1 分的 test-id）在这七个应用上**全是 0**，桌面上没有这一级可用。
	var byName, byDescription []elementRecord
	for _, record := range snapshot.Elements {
		if !selectorRoleMatches(&record, role) {
			continue
		}
		if len(preds) > 0 && !recordMatchesPredicates(&record, preds) {
			continue
		}
		if !hasName {
			// role 与谓词都没有时不能放行，否则空选择器匹配全树。
			// 只要有谓词，这条顾虑就不存在了——`[desc="View options"]`
			// 本身就是一条足够窄的身份，实测是 Nautilus 那三个同名
			// `toggle button Menu` 唯一能区分开的写法。
			if role == "" && len(preds) == 0 {
				continue
			}
			byName = append(byName, record)
			continue
		}
		if equalSemanticIdentity(record.Name, name) {
			byName = append(byName, record)
		} else if equalSemanticIdentity(record.Description, name) {
			byDescription = append(byDescription, record)
		}
	}
	matches := byName
	if len(matches) == 0 {
		// name 一个都没匹配上，才退到 description——高优先级有结果时不混入低优先级，
		// 否则一个精确的名字命中会被一堆描述命中稀释成"歧义"。
		matches = byDescription
	}
	if len(matches) == 1 {
		copy := matches[0]
		return &copy, nil
	}
	base := func(summary string) *toolError {
		e := &toolError{tool: tool, summary: summary}
		e.add("Selector", strconv.Quote(selector))
		if declared != "" {
			e.add("Declared", strconv.Quote(declared))
		}
		e.add("Snapshot", snapshotAge(snapshot))
		return e
	}
	if len(matches) == 0 {
		e := base(fmt.Sprintf("no element matches the selector %q.", selector)).
			step("parsed selector as role=%q name=%q%s", role, name, describePredicates(preds)).
			step("searched %d elements by name, then by desc=\"…\"", len(snapshot.Elements))
		e.log = append(e.log, closestElements(snapshot, declared+" "+name, 5)...)
		e.next = "selectors are written exactly as the snapshot renders them, e.g. `push button \"Save\"`, or just `\"Save\"` to match by name alone. The quoted part matches an element's name, or its desc=\"…\" when no name matches. If the UI has changed, call AX mode=observe again."
		if len(preds) > 0 {
			// 有谓词时最可能的原因是状态已经变了（比如那个框已经不再 checked），
			// 而不是名字抄错了。先说这一条，再说通用建议。
			e.next = "the predicates are matched against the element's current state, which may have changed since the snapshot — try the selector without them first, then call AX mode=observe to see the state now. " + e.next
		}
		// 快照小得离谱时**直接点破**，而不是把数字摆在那儿让 agent 自己看出来。
		//
		// 出处是第 44 题的轨迹：Chrome 跳到"网络错误"页之后，快照里只剩 1 个元素，
		// 选择器当然找不到地址栏。错误如实报了
		// `Snapshot: captured 13.5s ago, 1 elements`——数字是对的，但没人会停下来
		// 品这个 1 的含义。agent 白花一步重试了同一个选择器，才改用 ctrl+l 绕过去。
		//
		// 一位数的元素数只有两种可能：窗口还在加载，或者抓到了别的窗口。两种都不是
		// "名字抄错了"，而通用建议只谈后者，等于把它往错路上引。
		if n := len(snapshot.Elements); n > 0 && n < 10 {
			e.next = fmt.Sprintf("this snapshot has only %d element(s), which almost always means the window was still loading or a different window got captured — not that the selector is wrong. Call AX mode=observe again before trying another selector. ", n) + e.next
		}
		return nil, e
	}

	// 多匹配时**不挑一个**。Playwright 的 strict mode 就是这个纪律，
	// 维护者原话：sometimes there are two elements, and picking the first
	// "would just click the first element on the page… Locators on the other
	// side would throw and the user would directly know that its unexpected
	// instead of clicking on the wrong element."
	// 静默挑一个正是本项目一直在修的那类错误。
	e := base(fmt.Sprintf("the selector %q is ambiguous: it matches %d elements.",
		selector, len(matches)))
	e.step("parsed selector as role=%q name=%q%s", role, name, describePredicates(preds))
	e.step("matched %d elements:", len(matches))
	for i, record := range matches {
		if i == 6 {
			e.log = append(e.log, fmt.Sprintf("    … and %d more", len(matches)-6))
			break
		}
		// 有名字的才给 `aka <选择器>`——无名元素的"替代写法"就是下标本身，
		// 而下标已经在行首了，再写一遍是废话。
		line := fmt.Sprintf("    %-4d %s", record.Index, describeRecord(&record))
		if record.Name != "" {
			line = fmt.Sprintf("    %-4d %-26s aka %s %q",
				record.Index, describeRecord(&record), record.ControlType, record.Name)
		} else if record.Description != "" {
			line = fmt.Sprintf("    %-4d %-26s aka %s %q",
				record.Index, describeRecord(&record), record.ControlType, record.Description)
		}
		e.log = append(e.log, line)
	}
	if role != "" && !hasName {
		e.next = "narrow it by adding the name in quotes, e.g. `" + role + " \"Save\"`, or pass one of the indices above."
	} else {
		e.next = "narrow it by adding the role, e.g. `push button \"Save\"`, or pass one of the indices above."
	}
	// 同名同角色的元素只能靠 desc 区分——实测 Nautilus 里三个 `toggle button
	// "Menu"` 就是这种情况，唯一的区别是 desc="Show operations" / "View options"。
	// 候选列表里已经把它们的 desc 打出来了，这里只需要告诉调用方能拿它来筛。
	//
	// 例子从**真实候选**里拿，拿不到就不给：编一个 desc 出来会让调用方
	// 照抄一个不存在的值，那比不给建议更糟。
	if example := describeSelectorExample(matches); len(preds) == 0 && example != "" {
		e.next += " If the candidates above differ only by desc=\"…\", add it as a predicate: `" +
			example + "`."
	}
	return nil, e
}

func selectorRoleMatches(record *elementRecord, requested string) bool {
	requested = strings.ToLower(strings.TrimSpace(requested))
	if requested == "" {
		return true
	}
	provider := canonicalRole(record)
	return strings.EqualFold(provider, requested) ||
		strings.EqualFold(
			string(canonicalRoleDescriptor(provider, nil).ModelRole),
			requested,
		)
}

// parseSelector 拆出 role、name 与**末引号之后的残串**。
//
// 第四个返回值是补的一个真 bug：原来这里取到 name 就 return，
// `selector[end+1:]` 从头到尾没人看。于是
//
//	push button "Save" [checked]      -> role="push button" name="Save"
//	push button "OK" >> nth=0         -> role="push button" name="OK"
//
// 两条都**静默丢掉约束、照常匹配、还返回成功**。调用方以为自己加了限定，
// 实际拿到的是不带限定的结果——正是本项目反复在修的那类"工具骗 agent"。
//
// 残串交给 parsePredicates：认得的当约束执行，认不得的报错。
func parseSelector(selector string) (string, string, bool, string) {
	selector = strings.TrimSpace(selector)
	start := strings.Index(selector, "\"")
	// 角色段止于**第一个 `[` 或第一个 `"`，谁先来算谁**。
	// 少了这一判，`toggle button [desc="Show operations"]` 会把谓词值的
	// 那个引号当成名字的起点，解析成 role=`toggle button [desc=`——
	// 这是写测试时当场撞出来的。
	if bracket := strings.Index(selector, "["); bracket >= 0 &&
		(start < 0 || bracket < start) {
		return strings.TrimSpace(selector[:bracket]), "", false,
			strings.TrimSpace(selector[bracket:])
	}
	if start < 0 {
		// 没有引号也没有方括号时整串都是 role，不存在残串。
		return selector, "", false, ""
	}
	end := closingQuote(selector, start)
	if end <= start {
		return strings.TrimSpace(selector[:start]), "", false, ""
	}
	name := unescapeQuoted(selector[start+1 : end])
	return strings.TrimSpace(selector[:start]), name,
		true, strings.TrimSpace(selector[end+1:])
}

// closingQuote 找 start 处那个引号的配对引号，跳过 `\"` 转义。
//
// 原来这里用 strings.LastIndex，遇到 `[desc="…"]` 这种后面还有引号的
// 选择器会把整段吞进 name。既然现在残串要参与匹配，配对就必须是真配对。
func closingQuote(text string, start int) int {
	for i := start + 1; i < len(text); i++ {
		if text[i] == '\\' {
			i++
			continue
		}
		if text[i] == '"' {
			return i
		}
	}
	return -1
}

// unescapeQuoted 还原 runtime.py 里 quoted() 做的转义。
func unescapeQuoted(text string) string {
	var out strings.Builder
	for i := 0; i < len(text); i++ {
		if text[i] == '\\' && i+1 < len(text) {
			i++
			switch text[i] {
			case 'n':
				out.WriteByte('\n')
			case 'r':
				out.WriteByte('\r')
			case 't':
				out.WriteByte('\t')
			default:
				out.WriteByte(text[i])
			}
			continue
		}
		out.WriteByte(text[i])
	}
	return out.String()
}

// selectorPredicate 是选择器尾部方括号里的一条约束，语义是 AND。
type selectorPredicate struct {
	kind  string // state | desc | placeholder | action
	value string
}

// Public selectors accept only the compact working-projection spelling.
// Canonical records may retain provider-facing state words internally, but
// those are not a second public protocol.
//
// 刻意不收 [disabled]：state_segment 的注释里记着实测结论——Nautilus 的文件
// 图标根本不设 ENABLED/SENSITIVE 却完全可操作，我们既然不打印它，
// 就更不能让人拿它来筛。也不收 [level]，那是 aria 有我们没有的属性，
// 为了对齐而造一个查不到的谓词只会让 agent 白撞墙。
var selectorStateWords = map[string]bool{
	"checked":  true,
	"expanded": true,
	"selected": true,
	"focused":  true,
	"click":    true,
}

var publicSelectorStateWords = map[string]bool{
	"checked":  true,
	"expanded": true,
	"selected": true,
	"focused":  true,
}

const selectorPredicateHelp = "Supported trailing predicates are the ones the snapshot prints: " +
	"[checked] [expanded] [selected] [focused] [click] " +
	"[desc=\"…\"] [placeholder=\"…\"] [a=…]. " +
	"The leading index, the {x,y,w,h} box and the trailing : \"value\" are not part of a selector — drop them. " +
	"Chained (>>), positional (nth=) and layout (:right-of) selectors do not exist here."

// parsePredicates 把残串拆成约束。认不出来的一律报错，绝不跳过——
// 静默跳过就是这次要修的那个 bug 本身。
func parsePredicates(rest string) ([]selectorPredicate, error) {
	rest = strings.TrimSpace(rest)
	var out []selectorPredicate
	for i := 0; i < len(rest); {
		if rest[i] == ' ' {
			i++
			continue
		}
		if rest[i] != '[' {
			return nil, fmt.Errorf("expected `[` but found %q", rest[i:])
		}
		end := closingBracket(rest, i)
		if end < 0 {
			return nil, fmt.Errorf("`[` is never closed in %q", rest[i:])
		}
		group := strings.TrimSpace(rest[i+1 : end])
		i = end + 1
		parsed, err := parsePredicateGroup(group)
		if err != nil {
			return nil, err
		}
		out = append(out, parsed...)
	}
	return out, nil
}

// closingBracket 找配对的 `]`，跳过引号内部的那些——
// desc 的值里带 `]` 完全可能（实测 Impress 的描述就是整句自然语言）。
func closingBracket(text string, start int) int {
	inQuote := false
	for i := start + 1; i < len(text); i++ {
		switch {
		case text[i] == '\\' && inQuote:
			i++
		case text[i] == '"':
			inQuote = !inQuote
		case text[i] == ']' && !inQuote:
			return i
		}
	}
	return -1
}

func parsePredicateGroup(group string) ([]selectorPredicate, error) {
	if group == "" {
		return nil, errors.New("`[]` is empty")
	}
	for _, prefix := range []string{"desc=", "placeholder=", "a="} {
		if !strings.HasPrefix(group, prefix) {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(group, prefix))
		kind := strings.TrimSuffix(prefix, "=")
		if kind == "a" {
			var out []selectorPredicate
			for _, one := range strings.Split(value, ",") {
				if one = strings.TrimSpace(one); one != "" {
					out = append(out, selectorPredicate{kind: "action", value: one})
				}
			}
			if len(out) == 0 {
				return nil, fmt.Errorf("`[%s]` lists no action", group)
			}
			return out, nil
		}
		if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
			return nil, fmt.Errorf("`[%s]` needs a quoted value, as the snapshot prints it", group)
		}
		return []selectorPredicate{{
			kind:  strings.TrimSuffix(kind, "="),
			value: unescapeQuoted(value[1 : len(value)-1]),
		}}, nil
	}
	// 状态在快照里是**一个方括号里的多个词**（`[selected focused]`），
	// 所以一组要拆成多条约束，不是一条。
	var out []selectorPredicate
	for _, word := range strings.Fields(group) {
		word = strings.ToLower(word)
		if !selectorStateWords[word] {
			return nil, fmt.Errorf("`[%s]` is not a predicate I know", group)
		}
		if word == "click" {
			word = "has-click-action"
		}
		out = append(out, selectorPredicate{kind: "state", value: word})
	}
	return out, nil
}

// describeSelectorExample 从候选里挑一个**真实存在**的 desc，拼成可直接抄用的
// 选择器。没有任何候选带 desc 时返回空串，由调用方决定不给这条建议。
func describeSelectorExample(matches []elementRecord) string {
	for _, record := range matches {
		if record.Description == "" || record.Description == record.Name {
			continue
		}
		head := record.ControlType
		if record.Name != "" {
			head += " " + strconv.Quote(record.Name)
		}
		return fmt.Sprintf("%s [desc=%s]", head, strconv.Quote(record.Description))
	}
	return ""
}

// describePredicates 把解析结果回显进 Call log。调用方要能一眼看出
// 自己那串方括号被读成了什么——尤其是在筛空的时候。
func describePredicates(preds []selectorPredicate) string {
	if len(preds) == 0 {
		return ""
	}
	parts := make([]string, 0, len(preds))
	for _, pred := range preds {
		if pred.kind == "state" {
			parts = append(parts, pred.value)
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%q", pred.kind, pred.value))
	}
	return " and predicates [" + strings.Join(parts, "] [") + "]"
}

func recordMatchesPredicates(record *elementRecord, preds []selectorPredicate) bool {
	for _, pred := range preds {
		switch pred.kind {
		case "state":
			// States 存的是渲染好的 " [checked expanded]"，按词比对，
			// 不能用 Contains——那样 "selected" 会命中 "unselected"。
			found := false
			for _, word := range strings.Fields(strings.Trim(record.States, " []")) {
				if strings.EqualFold(word, pred.value) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		case "desc":
			if !equalSemanticIdentity(record.Description, pred.value) {
				return false
			}
		case "placeholder":
			if !equalSemanticIdentity(record.Placeholder, pred.value) {
				return false
			}
		case "action":
			found := false
			for _, action := range record.Actions {
				if strings.EqualFold(action, pred.value) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	return true
}
