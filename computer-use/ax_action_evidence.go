package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func unchangedNotes(windowFocusConfirmed bool, pixels pixelVerdict, attrsChanged bool) []string {
	notes := []string{}
	if attrsChanged {
		notes = append(notes, "The accessibility tree structure is unchanged, while accessibility text attributes changed (see the [text-attrs] note above). This reports only the observed attribute difference; compare it with the intended result.")
	}
	switch pixels {
	case pixelsChanged:
		notes = append(notes, "The accessibility tree is unchanged; visual change observed (see the [pixels] note above). This does not identify the cause, target, or semantic result. Inspect the screenshot or another independent source of truth.")
	case pixelsIdentical:
		note := "The accessibility tree is unchanged; no observed change in the window pixels."
		if windowFocusConfirmed {
			note += " The target application window was confirmed focused before synthesis, but control delivery and semantic effect remain unknown."
		}
		notes = append(notes, note)
	default:
		if !attrsChanged {
			note := "No accessibility change observed: window title, tree, focus and selection match the pre-action snapshot. Visual change was not measured, so control delivery and semantic effect remain unknown."
			if windowFocusConfirmed {
				note += " The target application window was confirmed focused before synthesis; that is not control-delivery evidence."
			}
			notes = append(notes, note)
		}
	}
	return notes
}

type pixelVerdict int

const (
	pixelsUnknown pixelVerdict = iota
	pixelsIdentical
	pixelsChanged
)

// readTextAttributeChange 看运行时有没有报出格式属性的变化。
//
// 这是**最强**的一档证据：它直接说出"justification 从 left 变成 right"，
// 而像素只能说"0.1% 变了"。有它在就不必再谈"树没变所以可能失败"。
// textAttributeChanges 比对两份快照里同一元素的格式属性。
//
// 这条通道是查 Playwright 时反推出来的：它的 aria snapshot 只渲染一小撮语义属性
// （[checked] [level=1]），"查任意样式属性"是 toHaveCSS 这类**断言**的事——
// **快照是给 agent 看的摘要，断言是精确查询，两者不必是同一套字段。**
//
// 顺着这条思路回头查 AT-SPI，才发现 Atspi.Text.get_default_attributes() 一直
// 能读到 justification / size / weight / fg-color。此前记进文档的
// "改段落对齐后 a11y 树字节不变"**是错的**——树不变是因为我们没取这些字段，
// 不是信息不存在。整场"像素噪声和光标闪烁同量级"的仗，本可以绕过去。
//
// 比对不花额外的 AT-SPI 调用：属性跟着 record 走，而动作前的快照 Go 侧本来
// 就缓存着。按 element 身份配对（automationId 优先，其次 role+name），
// 不按下标——下标每次快照都会重排。
// systemFontFamilies 是这台桌面上的**回退字体**。名字出现在 family-name 的
// 变化两端时，说明发生的是字体替换，而不是谁把文字改粗了。
var systemFontFamilies = map[string]bool{
	"ubuntu": true, "cantarell": true, "dejavu sans": true, "dejavu serif": true,
	"dejavu sans mono": true, "liberation sans": true, "liberation serif": true,
	"liberation mono": true, "noto sans": true, "noto serif": true,
	"freesans": true, "freeserif": true, "sans-serif": true, "serif": true,
	"monospace": true, "sans": true,
}

// withoutRepaintNoise 去掉**页面自己重绘**造成的属性变化，只留真正可能是
// 动作后果的那些。
//
// 为什么必须做：旧 note 曾把属性变化写成动作效果的直接因果证据。
// 在 nfl.com 上它捕到的是 `family-name: Ubuntu -> All-Pro Sans`、
// `size: 18pt -> 22.5pt`——网页字体**异步加载完了**，把回退字体换掉而已。
// 轨迹第 16 步 agent 只是按了个 ctrl+l，却被告知"动作生效了，证据是某个标题
// 的字体变了"。工具在替一件没发生的事作证，这比不给证据更坏。
//
// 两类噪声，判据都刻意保守：
//
//	字体替换   family-name 的任一端是系统回退字体。此时 size/weight/style 是
//	           新字体的度量带来的，一并丢掉——但**只在这个节点上**丢，别的
//	           节点该报还报，所以 LibreOffice 里真的加粗一段文字照样能报出来。
//	肉眼不可见 *-color 的每个通道都只差 ≤8/255。244,248,248 -> 246,249,249
//	           这种是抗锯齿/合成的舍入，看都看不出来，更不可能是谁点了什么。
func withoutRepaintNoise(diffs []string) []string {
	fontSwap := false
	for _, diff := range diffs {
		name, from, to, ok := splitDiff(diff)
		if ok && name == "family-name" &&
			(systemFontFamilies[strings.ToLower(from)] || systemFontFamilies[strings.ToLower(to)]) {
			fontSwap = true
			break
		}
	}
	kept := make([]string, 0, len(diffs))
	for _, diff := range diffs {
		name, from, to, ok := splitDiff(diff)
		if !ok {
			kept = append(kept, diff)
			continue
		}
		if fontSwap {
			switch name {
			case "family-name", "size", "weight", "style":
				continue
			}
		}
		if strings.HasSuffix(name, "color") && imperceptibleColorShift(from, to) {
			continue
		}
		kept = append(kept, diff)
	}
	return kept
}

// splitDiff 把 "size: 18pt -> 22.5pt" 拆回三段。
func splitDiff(diff string) (name, from, to string, ok bool) {
	colon := strings.Index(diff, ": ")
	arrow := strings.Index(diff, " -> ")
	if colon < 0 || arrow < 0 || arrow < colon {
		return "", "", "", false
	}
	return diff[:colon], diff[colon+2 : arrow], diff[arrow+4:], true
}

// imperceptibleColorShift 判断两个 "r,g,b" 是否近到看不出来。
const colorNoiseThreshold = 8

func imperceptibleColorShift(from, to string) bool {
	a := strings.Split(from, ",")
	b := strings.Split(to, ",")
	if len(a) != 3 || len(b) != 3 {
		return false
	}
	for i := range a {
		x, err1 := strconv.Atoi(strings.TrimSpace(a[i]))
		y, err2 := strconv.Atoi(strings.TrimSpace(b[i]))
		if err1 != nil || err2 != nil {
			return false
		}
		if diff := x - y; diff > colorNoiseThreshold || diff < -colorNoiseThreshold {
			return false
		}
	}
	return true
}

func textAttributeChanges(before, after *appSnapshot) []string {
	if before == nil || after == nil {
		return nil
	}
	pathKey := func(runtimeID []int) string {
		if len(runtimeID) == 0 {
			return ""
		}
		parts := make([]string, 0, len(runtimeID))
		for _, n := range runtimeID {
			parts = append(parts, strconv.Itoa(n))
		}
		return strings.Join(parts, ".")
	}
	rolesByPath := map[string]string{}
	for _, record := range after.Elements {
		if key := pathKey(record.RuntimeID); key != "" {
			rolesByPath[key] = strings.ToLower(strings.TrimSpace(record.ControlType))
		}
	}
	// 按 runtimeId（树里的路径）找到候选，再校验 role/name/automationId。
	//
	// 第一版用 role+name，实测当场翻车：LibreOffice 侧栏有四个无名 `text` 节点，
	// key 全是 `text\x00""` 撞成一个，于是某个节点的悬停高亮（bg-color）
	// 被张冠李戴到别人头上，连空操作都报出"格式变了"。
	// 路径只有在树结构不变时才指向同一个元素；节点插入/删除后，同一路径可能
	// 已经换成另一个控件，所以路径不能单独充当身份。
	key := func(r elementRecord) string {
		path := pathKey(r.RuntimeID)
		if path == "" {
			return ""
		}
		return "path:" + path
	}
	old := map[string]elementRecord{}
	for _, record := range before.Elements {
		if k := key(record); k != "" && len(record.TextAttributes) > 0 {
			old[k] = record
		}
	}
	var changes []string
	seen := map[string]bool{}
	for _, record := range after.Elements {
		if len(record.TextAttributes) == 0 {
			continue
		}
		if !documentTextFormattingRecord(record, rolesByPath, pathKey) {
			continue
		}
		k := key(record)
		previous, ok := old[k]
		if !ok || seen[k] ||
			previous.ControlType != record.ControlType ||
			previous.Name != record.Name ||
			(previous.NameHash != "" || record.NameHash != "") &&
				previous.NameHash != record.NameHash ||
			(previous.AutomationID != "" || record.AutomationID != "") &&
				previous.AutomationID != record.AutomationID {
			continue
		}
		seen[k] = true
		var diffs []string
		for name, value := range record.TextAttributes {
			if was, had := previous.TextAttributes[name]; !had || was != value {
				from := was
				if !had {
					from = "unset"
				}
				if had && !plausibleTextAttribute(name, from) ||
					!plausibleTextAttribute(name, value) {
					continue
				}
				diffs = append(diffs, fmt.Sprintf("%s: %s -> %s", name, from, value))
			}
		}
		if len(diffs) == 0 {
			continue
		}
		diffs = withoutRepaintNoise(diffs)
		if len(diffs) == 0 {
			continue
		}
		sort.Strings(diffs)
		label := describeRecord(&record)
		changes = append(changes, fmt.Sprintf("%s — %s", label, strings.Join(diffs, "; ")))
	}
	sort.Strings(changes)
	return changes
}

func documentTextFormattingRecord(
	record elementRecord,
	rolesByPath map[string]string,
	pathKey func([]int) string,
) bool {
	role := strings.ToLower(strings.TrimSpace(record.ControlType))
	if role == "paragraph" || strings.HasPrefix(role, "document") {
		return true
	}
	if role != "text" {
		return false
	}
	for length := len(record.RuntimeID) - 1; length > 0; length-- {
		ancestorRole := rolesByPath[pathKey(record.RuntimeID[:length])]
		if ancestorRole == "paragraph" || strings.HasPrefix(ancestorRole, "document") {
			return true
		}
	}
	return false
}

var spreadsheetRangeValue = regexp.MustCompile(`(?i)^[A-Z]{1,4}[0-9]+(?::[A-Z]{1,4}[0-9]+)?$`)

func plausibleTextAttribute(name, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 256 || strings.ContainsAny(value, "\r\n") {
		return false
	}
	if name == "family-name" && spreadsheetRangeValue.MatchString(value) {
		return false
	}
	if strings.HasSuffix(name, "color") {
		parts := strings.Split(value, ",")
		if len(parts) != 3 {
			return false
		}
		for _, part := range parts {
			channel, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil || channel < 0 || channel > 65535 {
				return false
			}
		}
	}
	return true
}

func readTextAttributeChange(notes []string) bool {
	for _, note := range notes {
		if strings.HasPrefix(note, "[text-attrs]") {
			return true
		}
	}
	return false
}

// readPixelVerdict 从运行时的 `[pixels]` Note 里读出屏幕到底变没变。
func readPixelVerdict(notes []string) pixelVerdict {
	for _, note := range notes {
		if !strings.HasPrefix(note, "[pixels]") {
			continue
		}
		if strings.Contains(note, "persistent_change=false") ||
			strings.Contains(note, "sampled_pixel_change=none") {
			return pixelsIdentical
		}

		return pixelsChanged
	}
	return pixelsUnknown
}

func compactFindNotes(notes []string) []string {
	return modelFacingAXNotes(notes)
}

func observeNoneNotes(notes []string) []string {
	out := make([]string, 0, len(notes))
	for _, note := range notes {
		phase, _ := observationPhaseAndPayload(note)
		category := diagnosticCategory(note)
		if phase == "immediate" && category != "" {
			continue
		}
		if strings.HasPrefix(note, "[expect]") ||
			strings.HasPrefix(note, "[semantic-resolution]") ||
			strings.HasPrefix(note, "[canonical-address]") ||
			strings.Contains(note, `"predicate_verified":`) ||
			category == "root_window_changed" ||
			category == "modal_dialog" ||
			category == "dialog_in_front" ||
			category == "foreign_modal" ||
			category == "foreign_window" {
			out = append(out, note)
		}
	}
	return out
}

// modelFacingAXNotes removes implementation diagnostics while preserving facts
// that can change the agent's next safe decision. Full explanations remain in
// operator telemetry and error-context artifacts.
func modelFacingAXNotes(notes []string) []string {
	out := make([]string, 0, len(notes))
	seen := map[string]bool{}
	appendOnce := func(value string) {
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		out = append(out, value)
	}
	for _, note := range notes {
		category := diagnosticCategory(note)
		if category == "" && strings.Contains(note, "diagnostic snapshot_unchanged") {
			category = "snapshot_unchanged"
		}
		if category == "" && strings.Contains(note, "diagnostic snapshot_busy") {
			category = "snapshot_busy"
		}
		switch category {
		case "snapshot_unchanged", "content_scope_unchanged":
			appendOnce("[state] semantic_state=unchanged")
			continue
		case "snapshot_busy":
			appendOnce("[state] semantic_state=unchanged busy=true recovery=wait_then_observe")
			continue
		case "root_window_changed":
			appendOnce("[state] window_changed=true indices_invalidated=true")
			continue
		case "modal_dialog":
			appendOnce("[state] blocking=modal_dialog")
			continue
		case "dialog_in_front":
			appendOnce("[state] blocking=dialog_in_front")
			continue
		case "content_plane_uncertain", "accessibility_content_uncertain":
			appendOnce("[state] ax_content=uncertain")
			continue
		case "foreign_modal":
			appendOnce("[state] blocking=foreign_modal")
			continue
		case "foreign_window":
			appendOnce("[state] foreground=other_window")
			continue
		}
		if strings.HasPrefix(note, "[semantic-resolution]") ||
			strings.HasPrefix(note, "[a11y] Resolved ") ||
			strings.HasPrefix(note, "[continuity]") ||
			strings.HasPrefix(note, "[diagnostic ") {
			continue
		}
		if strings.HasPrefix(note, "[pixels]") {
			if readPixelVerdict([]string{note}) == pixelsIdentical {
				appendOnce("[state] visual_change=false")
			} else {
				appendOnce("[state] visual_change=true")
			}
			continue
		}
		if strings.HasPrefix(note, "[a11y]") {
			if offset := strings.Index(note, "[state]"); offset >= 0 {
				fact := strings.TrimSpace(note[offset:])
				if end := strings.Index(fact, "."); end >= 0 {
					fact = strings.TrimSpace(fact[:end])
				}
				appendOnce(fact)
			}
			continue
		}
		if strings.HasPrefix(note, "The accessibility tree is unchanged") ||
			strings.HasPrefix(note, "No accessibility change observed") {
			appendOnce("[state] ax_change=false")
			continue
		}
		appendOnce(note)
	}
	return out
}
