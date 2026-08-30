package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func optionalSettleMS(args map[string]any) (int, error) {
	value, present := args["settle_ms"]
	if !present || value == nil {
		return defaultSettleMS, nil
	}
	invalid := fmt.Errorf("settle_ms must be a whole number of milliseconds between 0 and %d", maxSettleMS)
	var milliseconds int
	switch typed := value.(type) {
	case int:
		milliseconds = typed
	case float64:
		if !isWholeNumber(typed) || typed < 0 || typed > float64(maxSettleMS) {
			return 0, invalid
		}
		milliseconds = int(typed)
	case json.Number:
		integer, err := typed.Int64()
		if err != nil {
			return 0, invalid
		}
		milliseconds = int(integer)
	case string:
		integer, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, invalid
		}
		milliseconds = integer
	default:
		return 0, invalid
	}
	if milliseconds < 0 || milliseconds > maxSettleMS {
		return 0, invalid
	}
	return milliseconds, nil
}

// settlePlan 是一次动作携带的验证意图。expect 为 nil 时整条路径**完全不启用**，
// 行为与加这个特性之前逐字相同。
type settlePlan struct {
	expect   *expectSpec
	settleMS int
}

func parseSettlePlan(args map[string]any) (settlePlan, error) {
	expect, err := parseExpect(args)
	if err != nil {
		return settlePlan{}, err
	}
	settleMS, err := optionalSettleMS(args)
	if err != nil {
		return settlePlan{}, err
	}
	return settlePlan{expect: expect, settleMS: settleMS}, nil
}

type settleOutcome struct {
	snapshot               *appSnapshot
	snapshotNotes          []string
	snapshotFromFollowup   bool
	verificationIncomplete bool
	followupReadFailed     bool
	verified               bool
	evidence               string
	elapsedMS              int
	polls                  int
}

func (s *service) refreshSnapshotForSettle(
	app string,
	request axServiceRequest,
) (*appSnapshot, []string, toolCallResult) {
	internalPrevious := s.currentSnapshot(app)
	if internalPrevious != nil {
		applyProjection(&request, internalPrevious.projection)
	}
	snapshot, notes, result := s.readSnapshot(app, request)
	if result.IsError {
		return nil, nil, result
	}
	snapshot.projection = projectionFromRequest(request)
	visiblePrevious := s.deliveredSnapshotFor(snapshot)
	notes = append(continuityNotes(visiblePrevious, snapshot), notes...)
	// Settle polls are hidden implementation reads. They must update the action
	// cache, but must not consume lastDiagnostics as though the model had seen
	// each intermediate response.
	s.rememberSnapshot(app, snapshot)
	return snapshot, notes, toolCallResult{}
}

func settleReadRequest(app string, includeScreenshot *bool, screenshotScope string, projection snapshotProjection) axServiceRequest {
	request := axServiceRequest{
		Operation:         axServiceObserve,
		App:               app,
		IncludeScreenshot: includeScreenshot,
		ScreenshotScope:   screenshotScope,
	}
	applyProjection(&request, projection)
	return request
}

// settle 轮询而不是死等：满足条件立刻返回，并报告实际耗时。
//
// 起点是**动作返回的那一刻**，不是动作发出的那一刻。理由是诚实性：
// 动作自己那一次 AT-SPI 遍历在 GIMP 上实测能到 30s，把它算进 settled_after_ms
// 会让这个数字变成"观测有多慢"而不是"界面多久稳定"，那是在误导调用方。
//
// 第一轮采样复用动作刚建好的快照，所以不付任何额外代价——
// 对话框已经弹出来的常见情况会在 0ms 处直接命中。
//
// includeScreenshot 与 screenshotScope 从动作请求原样带过来，让重取的那份
// 快照与 observe 想要的观测一致：observe="screenshot"/"full" 时重取仍然带图，
// "none"/"tree" 时仍然不带；GUI 动作始终重取全桌面，而不是在第一次轮询后
// 悄悄退回窗口截图。
//
// before 是**动作前**的那份快照，整个轮询过程里保持不变：它是 appears /
// disappears 的基线。轮询换的只是 after，基线换了就不叫基线了。
func (s *service) settle(app string, plan settlePlan, before, first *appSnapshot, includeScreenshot *bool, screenshotScope string) settleOutcome {
	start := time.Now()
	snapshot := first
	var snapshotNotes []string
	snapshotFromFollowup := false
	for attempt := 1; ; attempt++ {
		ok, evidence := plan.expect.evaluate(before, snapshot)
		elapsed := int(time.Since(start) / time.Millisecond)
		if ok {
			return settleOutcome{
				snapshot:             snapshot,
				snapshotNotes:        snapshotNotes,
				snapshotFromFollowup: snapshotFromFollowup,
				verified:             true,
				evidence:             evidence,
				elapsedMS:            elapsed,
				polls:                attempt - 1,
			}
		}
		if elapsed >= plan.settleMS {
			return settleOutcome{
				snapshot:             snapshot,
				snapshotNotes:        snapshotNotes,
				snapshotFromFollowup: snapshotFromFollowup,
				evidence:             evidence,
				elapsedMS:            elapsed,
				polls:                attempt - 1,
			}
		}
		pause := settlePollMS
		if remaining := plan.settleMS - elapsed; remaining < pause {
			pause = remaining
		}
		time.Sleep(time.Duration(pause) * time.Millisecond)
		next, nextNotes, result := s.refreshSnapshotForSettle(
			app, settleReadRequest(app, includeScreenshot, screenshotScope, before.projection))
		if result.IsError || next == nil {
			// 重新读树失败不该被吞掉，也不该盖住动作本身的结果：如实说明这个
			// 结论只基于最后一份成功取得的快照。
			basis := "the state captured immediately after the action"
			if snapshotFromFollowup {
				basis = "the last successful settled follow-up snapshot"
			}
			return settleOutcome{
				snapshot:               snapshot,
				snapshotNotes:          snapshotNotes,
				snapshotFromFollowup:   snapshotFromFollowup,
				verificationIncomplete: true,
				followupReadFailed:     true,
				evidence: evidence + " (a follow-up read of the tree failed, so this verdict " +
					"rests only on " + basis + ")",
				elapsedMS: int(time.Since(start) / time.Millisecond),
				polls:     attempt,
			}
		}
		snapshot = next
		snapshotNotes = nextNotes
		snapshotFromFollowup = true
	}
}

// expectNote 是 expect 的唯一出口。第一段是机器可读的三元组
// （predicate_verified / evidence / settled_after_ms），后面是给 LLM 读的解释。
//
// predicate_verified 只回答声明的 accessibility predicate 是否成立。它既不证明
// 动作效果，也不补写运行时没有给出的 delivery 事实。
func expectNote(plan settlePlan, outcome settleOutcome, request axServiceRequest, actionNotes []string) string {
	payload, err := json.Marshal(map[string]any{
		"predicate_verified":    outcome.verified,
		"verification_complete": !outcome.verificationIncomplete,
		"followup_read_failed":  outcome.followupReadFailed,
		"evidence":              outcome.evidence,
		"settled_after_ms":      outcome.elapsedMS,
	})
	if err != nil {
		payload = []byte(fmt.Sprintf(`{"predicate_verified": %t, "settled_after_ms": %d}`,
			outcome.verified, outcome.elapsedMS))
	}
	cost := ""
	if outcome.polls > 0 {
		cost = fmt.Sprintf(" It cost %d extra tree read(s) to settle.", outcome.polls)
	}
	if outcome.verificationIncomplete {
		return "[expect] " + string(payload) + " — verification is incomplete because " +
			"a follow-up accessibility read failed. predicate_verified=false is not a " +
			"full-budget timeout verdict and does not show that the predicate stayed " +
			"false for the requested settle interval. The action result and last " +
			"successful snapshot remain available, but observe fresh state before " +
			"retrying or drawing a task-level conclusion." + cost
	}
	if outcome.verified {
		return "[expect] " + string(payload) + " — only the declared accessibility predicate " +
			"held in the observed tree. This does not confirm the action's intended effect, " +
			"delivery, or any state outside that predicate." + cost
	}
	emission := "The action was requested/emitted; delivery to the intended target is unknown."
	if windowFocusWasConfirmed(request) {
		emission = "The input was emitted after the target application window was confirmed focused; delivery to the intended control remains unknown."
	}
	for _, note := range actionNotes {
		if strings.Contains(note, "Delivery to the intended target was not verified") ||
			strings.Contains(note, "not evidence the action") {
			emission = "The action was requested/emitted; delivery to the intended target is unknown."
			break
		}
	}
	return "[expect] " + string(payload) + " — this is NOT a call error and isError is false: " +
		emission + " The declared accessibility predicate did not become true " +
		fmt.Sprintf("within the %dms settle budget. Expected %s. ", plan.settleMS, plan.expect.describe()) +
		"Read the evidence above before retrying: it says what the tree actually shows, which " +
		"usually tells you whether the action missed, whether the effect is one the tree cannot " +
		"represent (formatting, file state, canvas pixels — check the screenshot for those), or " +
		"whether the expectation itself was the wrong thing to wait for. Raising settle_ms only " +
		"helps for the first kind." + cost
}

// lookupBySelector 按 `<role> "<name>"` / `"<name>"` / `<role>` 找唯一元素。
//
// 语法刻意与快照行**逐字一致**：树里渲染的是 `4 push button "Save" [..]`，
// 那么选择器就写 `push button "Save"`——从行里抄下来即可，不用学第二套写法。
//
// 命中多个时**不挑一个**，而是把候选连同下标一起列出来让调用方收敛。
// 静默挑一个正是本项目一直在修的那类错误。
