package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func windowFocusWasConfirmed(request axServiceRequest) bool {
	switch request.operationName() {
	case "keypress", "scroll":
		return true
	}
	return false
}

// tracePath 每个动作写一行 JSONL 的目标文件。**默认关闭**。
//
// Playwright 的 trace viewer 是它最能打的运维特性：出了问题不用复现，
// 直接看当时每一步的前后状态。我们此前出问题只能回读 transcript，
// 而 transcript 里没有动作前后的完整状态。
//
// 默认关闭，因为它每步都要序列化两份树摘要，而绝大多数调用不需要事后复盘。
// 打开的方式是给出路径，而不是布尔开关：写到哪里是调用方的事，
// 一个诊断设施不该自作主张地选目录。
func tracePath() string {
	return strings.TrimSpace(os.Getenv("COMPUTER_USE_TRACE_FILE"))
}

// traceSummary 一份树的**摘要**，不是整棵树。
//
// 整棵树写进 trace 会让文件比 transcript 还大（VS Code 一棵树 15342 字符，
// 一次基线跑 17 个动作），而复盘时真正要看的是"前后有没有变、变了哪里"。
// 要看细节有 error-context 那条路。
func traceSummary(snapshot *appSnapshot) map[string]any {
	if snapshot == nil {
		return nil
	}
	return map[string]any{
		"window":   snapshot.WindowTitle,
		"elements": len(snapshot.Elements),
		"focused":  snapshot.FocusedSummary,
		"selected": snapshot.SelectedText,
	}
}

func (s *service) appendTrace(app string, request axServiceRequest, before, after *appSnapshot, notes []string, failed bool) {
	path := tracePath()
	if path == "" {
		return
	}
	record := map[string]any{
		"at":     time.Now().Format(time.RFC3339Nano),
		"tool":   request.operationName(),
		"app":    app,
		"failed": failed,
		"before": traceSummary(before),
		"after":  traceSummary(after),
		"notes":  notes,
	}
	// 动作参数照抄请求，但**不含快照**：request 里带着 Element 记录和
	// windowBounds，原样写进去会把每行撑到几 KB。
	args := map[string]any{}
	if request.Element != nil {
		args["element_index"] = request.Element.Index
		args["element_role"] = request.Element.ControlType
		args["element_name"] = request.Element.Name
	}
	if request.SemanticTarget != nil {
		args["semantic_selector"] = request.SemanticTarget.Selector
	}
	if request.SourceRef != "" {
		args["source_ref"] = request.SourceRef
	}
	if request.Action != nil {
		request.Action.appendTraceFields(args)
	}
	record["args"] = args

	line, err := json.Marshal(record)
	if err != nil {
		return
	}
	// 追加写，失败**静默放弃**：trace 是诊断设施，不该因为磁盘满就让一次
	// 正常的动作报错。这条纪律和 error-context 是同一条。
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if os.MkdirAll(dir, 0o755) != nil {
			return
		}
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	file.Write(append(line, '\n'))
}

func phaseBoundNotes(
	notes []string,
	phase string,
	_ *appSnapshot,
	supersededBy *appSnapshot,
) []string {
	if len(notes) == 0 {
		return nil
	}
	prefix := fmt.Sprintf("[observation phase=%s]", phase)
	if supersededBy != nil {
		prefix = fmt.Sprintf("[observation phase=%s superseded=true]", phase)
	}
	out := make([]string, 0, len(notes))
	for _, note := range notes {
		out = append(out, prefix+" "+note)
	}
	return out
}

func settledActionNotes(
	immediateSnapshot *appSnapshot,
	immediateNotes []string,
	outcome settleOutcome,
) []string {
	if !outcome.snapshotFromFollowup || outcome.snapshot == nil {
		return immediateNotes
	}
	notes := phaseBoundNotes(
		immediateNotes,
		"immediate",
		immediateSnapshot,
		outcome.snapshot,
	)
	notes = append(
		notes,
		phaseBoundNotes(
			outcome.snapshotNotes,
			"settled",
			outcome.snapshot,
			nil,
		)...,
	)
	if readPixelVerdict(immediateNotes) != pixelsUnknown {
		notes = append(notes,
			"[observation phase=settled] final_pixel_verdict=unavailable: pixel evidence was captured "+
				"for the immediate action snapshot and is not reused as evidence "+
				"for this later settled snapshot.")
	}
	return notes
}

// actionResult 是不带验证意图的动作出口。**保持原样**：不传 expect 的调用
// 走的是与本特性之前逐字相同的一条路径。
