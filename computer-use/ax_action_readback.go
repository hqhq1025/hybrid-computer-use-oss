package main

import (
	"fmt"
)

func incrementalTree(before, after *appSnapshot) ([]string, bool) {
	if before != nil && after != nil &&
		before.axSnapshot != nil && after.axSnapshot != nil {
		return incrementalAXSnapshot(before, after)
	}
	if before == nil || after == nil {
		return nil, false
	}
	if before.WindowHandle != "" && after.WindowHandle != "" &&
		before.WindowHandle != after.WindowHandle {
		return nil, false
	}
	if normalizedTreeView(before.projection.View) !=
		normalizedTreeView(after.projection.View) {
		return nil, false
	}
	if len(after.TreeLines) == 0 {
		return nil, false
	}
	old, now := before.TreeLines, after.TreeLines

	// 掐掉公共前后缀，剩下的中间段就是这次动作真正动过的地方。
	//
	// **第一版的判据是"前后行数必须完全相同"，这在 GUI 上几乎等于不生效。**
	// 实测 67 条轨迹、753 次动作，只命中 26.6%——因为菜单展开、对话框弹出、
	// 列表增删一行都会改变行数，而那恰恰是最常见的动作。行数一变就退回全量，
	// 于是"动作的副产品"吃掉了 87% 的观测开销。
	//
	// 前后缀掐掉之后，插入和删除都能被表达出来，不再要求行数相等。
	// 代价是"开头和结尾各变一行"这种分布会让中间段覆盖整棵树——那时按下面的
	// 比例判据退回全量，本来也该退。
	prefix := 0
	for prefix < len(old) && prefix < len(now) && old[prefix] == now[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(old)-prefix && suffix < len(now)-prefix &&
		old[len(old)-1-suffix] == now[len(now)-1-suffix] {
		suffix++
	}
	removed := old[prefix : len(old)-suffix]
	added := now[prefix : len(now)-suffix]

	// 长度相同时再按行比一次：中间段里往往只有个别行真的变了。同时在这里把
	// **行级差分的负压缩**拆掉——判据见 condenseLinePair。
	var changed []string
	if len(removed) == len(added) {
		var r, a []string
		for i := range added {
			if removed[i] == added[i] {
				continue
			}
			if summary, ok := condenseLinePair(removed[i], added[i]); ok {
				changed = append(changed, summary)
				continue
			}
			r = append(r, removed[i])
			a = append(a, added[i])
		}
		removed, added = r, a
	}

	if len(removed)+len(added)+len(changed) == 0 {
		return []string{
			fmt.Sprintf(
				"Incremental view: 0 changes across %d tree lines. The full tree is "+
					"byte-identical to the previous delivered tree state and is not repeated; every "+
					"previously displayed rN ref remains bound unless separately reported stale. "+
					"This is already a fresh post-action observation; request a rooted "+
					"observation only when the next decision needs omitted local context.",
				len(now),
			),
		}, true
	}
	// 变化超过三分之一就没必要绕弯，直接给全量更好读。
	//
	// 比的是**受影响的树行数**（两边取大），不是输出行数之和：一行被替换会
	// 产生 -旧 +新 两行输出，但树上只有一行变了。按和去比会把它当成两行，
	// 于是小树上的单行改动都被判成"变化太大"，增量再次形同虚设。
	affected := len(removed)
	if len(added) > affected {
		affected = len(added)
	}
	affected += len(changed)

	// 字节判据，与行数判据并列。
	//
	// 行数是**代价的坏代理**：一个把全部内容装在单个节点 value 里的控件
	// （终端、日志视图、大 value 文本域）在树上只占一行，行数判据看它只是
	// "1 行变了"，而字节上那一行可能是整棵树的 99%。两种失真都由此而来：
	//   - 3 行的终端树里改 1 行 → 1*3 >= 3，行数判据退回全量，把那一行的
	//     几千字符原样再发一遍；
	//   - 行数判据放行时，-旧 +新 两份全长内容加起来**比整棵树还大**，
	//     也就是评审里说的负压缩。
	// 所以下面先立一条硬不变量：增量的正文永远不会比全量更长；
	// 再让字节上明显更省的情况绕过行数判据。
	// 表头是固定开销，不计入比较——否则小树上任何增量都会被它压垮。
	payload := 0
	for _, line := range removed {
		payload += len(line) + 2
	}
	for _, line := range added {
		payload += len(line) + 2
	}
	for _, line := range changed {
		payload += len(line) + 1
	}
	full := 0
	for _, line := range now {
		full += len(line) + 1
	}
	if payload >= full {
		return nil, false
	}
	const (
		maxActionDiffChars   = 6000
		maxActionDiffPercent = 35
	)
	if payload > maxActionDiffChars ||
		(len(now) >= 50 &&
			full > 0 &&
			payload*100 > full*maxActionDiffPercent) {
		return nil, false
	}
	if affected*3 >= len(now) && payload*2 > full {
		return nil, false
	}

	out := make([]string, 0, len(removed)+len(added)+len(changed)+1)
	header := fmt.Sprintf(
		"Incremental view: %d line(s) gone, %d line(s) new, out of %d lines now in the "+
			"tree. Every line not shown here is byte-identical to the previous "+
			"delivered tree state. Existing rN refs remain bound unless separately reported "+
			"stale. Lines are prefixed - for "+
			"gone and + for new. This is already a fresh post-action observation; "+
			"use a rooted observe or find only when the next decision needs context "+
			"that is not present here.",
		len(removed), len(added), len(now))
	if len(changed) > 0 {
		header += fmt.Sprintf(
			" %d line(s) kept their identity and only changed content: those are "+
				"prefixed ~ and are shown as a single summary line each, with the "+
				"unchanged head kept for identification and the edit itself written "+
				"inline as [-removed][+added]. Nothing outside the marked span moved.",
			len(changed))
	}
	out = append(out, header)
	for _, line := range changed {
		out = append(out, line)
	}
	for _, line := range removed {
		out = append(out, "-"+line)
	}
	for _, line := range added {
		out = append(out, "+"+line)
	}
	return out, true
}

func condenseLinePair(before, after string) (string, bool) {
	// 阈值取一行普通树行的数倍。低于它时 -旧 +新 本来就便宜，
	// 压缩带来的元数据反而是净亏。
	const condenseMinBytes = 400
	if len(before) < condenseMinBytes && len(after) < condenseMinBytes {
		return "", false
	}
	oldRunes, newRunes := []rune(before), []rune(after)
	shorter := len(oldRunes)
	if len(newRunes) < shorter {
		shorter = len(newRunes)
	}
	head := 0
	for head < shorter && oldRunes[head] == newRunes[head] {
		head++
	}
	tail := 0
	for tail < shorter-head && oldRunes[len(oldRunes)-1-tail] == newRunes[len(newRunes)-1-tail] {
		tail++
	}
	oldMid := oldRunes[head : len(oldRunes)-tail]
	newMid := newRunes[head : len(newRunes)-tail]

	// 变化段占了大半时压缩没有收益，而且摘要会比原文更难读。
	if (len(oldMid)+len(newMid))*2 >= len(oldRunes)+len(newRunes) {
		return "", false
	}

	// 前缀的开头用来认元素（下标、role、name 都在那里），
	// 前缀的结尾用来定位改动，中间可以省。
	const identifyRunes = 120
	const contextRunes = 48
	leading := ""
	if head <= identifyRunes+contextRunes {
		leading = string(oldRunes[:head])
	} else {
		leading = string(oldRunes[:identifyRunes]) +
			fmt.Sprintf("…(%d chars unchanged)…", head-identifyRunes-contextRunes) +
			string(oldRunes[head-contextRunes:head])
	}
	trailing := ""
	if tail <= contextRunes {
		trailing = string(oldRunes[len(oldRunes)-tail:])
	} else {
		trailing = string(oldRunes[len(oldRunes)-tail:len(oldRunes)-tail+contextRunes]) +
			fmt.Sprintf("…(%d more unchanged)…", tail-contextRunes)
	}
	return fmt.Sprintf("~%s[-%s][+%s]%s  (content-only change at char %d; this line was %d chars, now %d)",
		leading, clipRunes(oldMid), clipRunes(newMid), trailing,
		head, len(oldRunes), len(newRunes)), true
}

// clipRunes 给变更段本身封顶。改动本身也可能是几千字符（终端刷屏一次就是），
// 那时首尾各留一段、中间说明省了多少，比原样倒出来有用。
func clipRunes(runes []rune) string {
	const keepEachSide = 240
	if len(runes) <= keepEachSide*2 {
		return string(runes)
	}
	return string(runes[:keepEachSide]) +
		fmt.Sprintf("…(%d chars elided)…", len(runes)-keepEachSide*2) +
		string(runes[len(runes)-keepEachSide:])
}

// observablyChanged 比较动作前后两份快照里 agent 能观察到的部分。
// 截图不参与比较：光标闪烁之类的像素噪音会让它永远"有变化"。

func compactActionObservationLines(
	snapshot *appSnapshot,
	baseline ...*appSnapshot,
) []string {
	if snapshot == nil {
		return nil
	}
	root := focusedObservationRoot(snapshot)
	if root == nil {
		return nil
	}
	label := "focus:"
	var delivered *appSnapshot
	if len(baseline) > 0 {
		delivered = baseline[0]
	}
	if snapshot.contentRoot != nil &&
		!runtimePathContains(
			snapshot.contentRoot.RuntimeID,
			root.RuntimeID,
		) &&
		(delivered == nil || contentRevisionChanged(delivered, snapshot)) {
		root = snapshot.contentRoot
		label = "content:"
	}
	lines, err := projectedSubtreeLines(snapshot, root)
	if err != nil || len(lines) == 0 {
		return nil
	}
	return append([]string{label}, lines...)
}
