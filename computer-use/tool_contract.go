package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
)

func requiredString(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

func optionalString(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

func requiredElementIndex(args map[string]any) string {
	return strings.TrimSpace(optionalElementIndex(args))
}

func a11yTargetAddress(args map[string]any) (string, bool, error) {
	raw, present := args["target"]
	if !present {
		return "", false, nil
	}
	target, ok := raw.(map[string]any)
	if !ok {
		return "", true, errors.New(
			`target must be an object: {"index": N}, {"ref": "rN"}, or {"selector": {...}}`,
		)
	}
	if len(target) != 1 {
		return "", true, errors.New(
			"target must contain exactly one of index, ref, or selector",
		)
	}
	if rawIndex, ok := target["index"]; ok {
		address := strings.TrimSpace(elementIndexString(rawIndex))
		if address == "" || !numericElementAddress(address) {
			return "", true, errors.New("target.index must be an integer")
		}
		index, _ := strconv.Atoi(address)
		if index < 0 {
			return "", true, errors.New("target.index must be non-negative")
		}
		return address, true, nil
	}
	if rawRef, ok := target["ref"]; ok {
		ref, ok := rawRef.(string)
		if !ok || strings.TrimSpace(ref) == "" {
			return "", true, errors.New("target.ref must be a non-empty rN or wN string")
		}
		ref = strings.TrimSpace(ref)
		if !isShortAXRef(ref) &&
			!isShortAXWindowRef(ref) {
			return "", true, errors.New(
				"target.ref must be an exact rN or wN copied from observe/find; " +
					"never invent placeholders such as r??",
			)
		}
		return ref, true, nil
	}
	rawSelector, ok := target["selector"]
	if !ok {
		return "", true, errors.New(
			"target must contain exactly one of index, ref, or selector",
		)
	}
	selector, ok := rawSelector.(map[string]any)
	if !ok {
		return "", true, errors.New("target.selector must be an object")
	}
	address, err := a11ySelectorAddress(selector)
	if err != nil {
		return "", true, fmt.Errorf("target.selector: %w", err)
	}
	return address, true, nil
}

func a11ySelectorAddress(selector map[string]any) (string, error) {
	allowed := map[string]bool{
		"role": true, "name": true, "description": true,
		"placeholder": true, "state": true,
	}
	for field := range selector {
		if !allowed[field] {
			return "", fmt.Errorf(
				"unknown field %q; use role, name, description, placeholder, or state",
				field,
			)
		}
	}
	if len(selector) == 0 {
		return "", errors.New("must contain at least one selector field")
	}
	for field := range allowed {
		if value, present := selector[field]; present {
			if _, ok := value.(string); !ok {
				return "", fmt.Errorf("%s must be a string", field)
			}
		}
	}
	role := strings.TrimSpace(optionalString(selector, "role"))
	name := strings.TrimSpace(optionalString(selector, "name"))
	address := selectorHead(role, name)
	predicates := make([]string, 0, 4)
	if description := strings.TrimSpace(
		optionalString(selector, "description"),
	); description != "" {
		predicates = append(predicates,
			"[desc="+strconv.Quote(description)+"]")
	}
	if placeholder := strings.TrimSpace(
		optionalString(selector, "placeholder"),
	); placeholder != "" {
		predicates = append(predicates,
			"[placeholder="+strconv.Quote(placeholder)+"]")
	}
	if state := strings.ToLower(strings.TrimSpace(
		optionalString(selector, "state"),
	)); state != "" {
		if !publicSelectorStateWords[state] {
			return "", fmt.Errorf(
				"unsupported state %q; use checked, expanded, selected, or focused",
				state,
			)
		}
		predicates = append(predicates, "["+state+"]")
	}
	if address == "" && len(predicates) == 0 {
		return "", errors.New("must contain a non-empty selector field")
	}
	return strings.TrimSpace(strings.Join(
		append([]string{address}, predicates...), " ",
	)), nil
}

func optionalElementIndex(args map[string]any) string {
	return elementIndexString(args["element_index"])
}

func checkedOptionalElementIndex(args map[string]any) (string, error) {
	value, provided := args["element_index"]
	if !provided {
		return "", nil
	}
	index := strings.TrimSpace(elementIndexString(value))
	if index == "" {
		return "", errors.New("must be an integer index or non-empty selector string")
	}
	return index, nil
}

func elementIndexString(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case json.Number:
		if integer, err := value.Int64(); err == nil {
			return strconv.FormatInt(integer, 10)
		}
		if float, err := value.Float64(); err == nil {
			return integerElementIndexFloat(float)
		}
	case float64:
		return integerElementIndexFloat(value)
	case int:
		return strconv.Itoa(value)
	case int64:
		return strconv.FormatInt(value, 10)
	}
	return ""
}

func integerElementIndexFloat(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value {
		return ""
	}
	return strconv.FormatInt(int64(value), 10)
}

func requiredFloat(args map[string]any, key string) *float64 {
	return optionalFloat(args, key)
}

func optionalFloat(args map[string]any, key string) *float64 {
	switch value := args[key].(type) {
	case float64:
		return &value
	case int:
		float := float64(value)
		return &float
	case json.Number:
		float, err := value.Float64()
		if err == nil {
			return &float
		}
	}
	return nil
}

func optionalTextLimit(args map[string]any, key string) (*textLimit, error) {
	value, ok := args[key]
	if !ok {
		return nil, nil
	}
	return textLimitFromValue(value, key)
}

func textLimitFromValue(value any, key string) (*textLimit, error) {
	if stringValue, ok := value.(string); ok {
		if strings.EqualFold(stringValue, "max") {
			return &textLimit{max: true}, nil
		}
		return nil, fmt.Errorf("%s must be a positive integer or max", key)
	}
	integer, err := positiveIntFromValue(value, key)
	if err != nil {
		return nil, fmt.Errorf("%s must be a positive integer or max", key)
	}
	return &textLimit{count: *integer}, nil
}

func optionalPositiveInt(args map[string]any, key string) (*int, error) {
	value, ok := args[key]
	if !ok {
		return nil, nil
	}
	return positiveIntFromValue(value, key)
}

func optionalNonNegativeInt(
	args map[string]any,
	key string,
	maximum int,
) (*int, error) {
	value, ok := args[key]
	if !ok {
		return nil, nil
	}
	invalid := fmt.Errorf(
		"%s must be a whole number between 0 and %d",
		key,
		maximum,
	)
	var integer int64
	switch typed := value.(type) {
	case int:
		integer = int64(typed)
	case float64:
		if !isWholeNumber(typed) {
			return nil, invalid
		}
		integer = int64(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return nil, invalid
		}
		integer = parsed
	default:
		return nil, invalid
	}
	if integer < 0 || integer > int64(maximum) {
		return nil, invalid
	}
	result := int(integer)
	return &result, nil
}

func positiveIntFromValue(value any, key string) (*int, error) {
	switch typed := value.(type) {
	case int:
		return positiveIntFromInt64(int64(typed), key)
	case float64:
		if !isWholeNumber(typed) {
			return nil, fmt.Errorf("%s must be a positive integer", key)
		}
		return positiveIntFromFloat64(typed, key)
	case json.Number:
		integer, err := typed.Int64()
		if err != nil {
			return nil, fmt.Errorf("%s must be a positive integer", key)
		}
		return positiveIntFromInt64(integer, key)
	default:
		return nil, fmt.Errorf("%s must be a positive integer", key)
	}
}

func positiveIntFromFloat64(value float64, key string) (*int, error) {
	if !isWholeNumber(value) || value <= 0 || value > float64(maxInt()) {
		return nil, fmt.Errorf("%s must be a positive integer", key)
	}
	integer := int(value)
	return &integer, nil
}

func positiveIntFromInt64(value int64, key string) (*int, error) {
	if value <= 0 || value > int64(maxInt()) {
		return nil, fmt.Errorf("%s must be a positive integer", key)
	}
	integer := int(value)
	return &integer, nil
}

func isWholeNumber(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && math.Trunc(value) == value
}

func maxInt() int {
	return int(^uint(0) >> 1)
}

func intValue(value *float64, fallback int) int {
	if value == nil {
		return fallback
	}
	return int(*value)
}

func floatValue(value *float64, fallback float64) float64 {
	if value == nil {
		return fallback
	}
	return *value
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func parseClickMethod(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return "auto", nil
	}
	for _, candidate := range clickMethodValues {
		if normalized == candidate {
			return normalized, nil
		}
	}
	return "", fmt.Errorf("Invalid click_method %q. Expected one of: %s", value, strings.Join(clickMethodValues, ", "))
}

func globalPointerFallbacksEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("COMPUTER_USE_ALLOW_GLOBAL_POINTER_FALLBACKS"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// 通道能力开关，对齐 Playwright 的 `--caps=vision`。
//
// `COMPUTER_USE_CHANNELS` 逗号分隔，默认三条全开：a11y, gui, keyboard。
// 关掉某条通道时，**它的工具根本不出现在 tools/list 里**——不是调用时才拒绝。
// 这个区别很要紧：模型看得见的工具会去试，试了被拒就是浪费一轮；
// 看不见就不会试。Playwright 把坐标做成 opt-in 能力也是这个道理。
//
// 用途有二：
//  1. `#29` 的 A/B 需要一个能真正**关掉**一条通道的开关，而不只是关掉截图
//  2. 部署到只信任语义动作的环境时，可以整条关掉坐标合成
func enabledChannels() map[string]bool {
	raw := strings.TrimSpace(os.Getenv("COMPUTER_USE_CHANNELS"))
	if raw == "" {
		return map[string]bool{"a11y": true, "gui": true, "keyboard": true}
	}
	enabled := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if part != "" {
			enabled[part] = true
		}
	}
	return enabled
}

// 每个工具属于哪条通道。list_apps 不属于任何通道（它只枚举应用），永远可用。
// appArgumentHelp 是**所有**接受 app 参数的工具共用的说明。
//
// 为什么值得写这么长：13 条真实 agent 轨迹里，**12 条**的开局都是
// `list_apps → get_app_state`——agent 不敢直接猜应用名，于是每道题都先花一步
// 去列一遍。还有一次直接报了 appNotFound("google-chrome")，而那个应用叫
// "Google Chrome"。
//
// 根因是**这台机器上的 a11y 应用名同时存在三种风格，没有任何规则能推出来**
// （实测）：
//
//	显示名     "Google Chrome"        进程却叫 chrome
//	二进制名   "vlc" "gedit" "code"   与进程同名
//	app-id     "org.gnome.Nautilus"   进程却叫 nautilus
//	           "org.gnome.Software"   进程却叫 snap-store
//
// 既然规则推不出来，就直接把规则告诉调用方，并说明匹配有多宽松——
// 让它敢于第一次就猜，猜错也能从错误里拿到候选名单。
// observeArgumentHelp 说明动作工具的观测预算开关。
//
// 为什么需要它：量过 67 条真实轨迹，**74% 的观测开销是动作的副产品**——
// 每个 click / press_key 都把整棵树重渲一遍再附一张图（动作附带的截图共 7779
// 张），而调用方连"这一步我不需要看"都无法表达。9 个动作工具里只有 click 有
// 一个 include_screenshot，其余 7 个没有任何观测控制参数。
// 昂贵的通道不可控，可控的通道不昂贵，这是把预算的控制权装在了错误的一侧。
//
// 默认值取 "diff" 而不是 "none"：完全不回状态会让不熟悉的调用方陷入盲操作。
// "diff" 就是这个工具此前一直在做的事（能省时给增量、不能省时给全量 + 按策略
// 带图），所以不传这个参数时行为逐字节不变。
const observeArgumentHelp = "How much observation to return after the action. " +
	"Defaults to \"diff\", which is exactly what this tool did before this " +
	"parameter existed, so leaving it out changes nothing.\n" +
	"  - \"none\": return only execution evidence, with no model-visible tree or " +
	"screenshot. Intermediate batch actions still perform the internal continuity " +
	"read required to re-resolve the next rN safely.\n" +
	"  - \"diff\" (default): return only what changed relative to the previous " +
	"tree state actually delivered to the model, falling back to the full tree when the change is too large for a " +
	"delta to be cheaper. A byte-identical tree returns only a compact zero-change " +
	"marker. No screenshot.\n" +
	"  - \"tree\": always the full accessibility tree, no screenshot.\n" +
	"  - \"screenshot\": the TARGET-WINDOW image plus the execution result, no tree. The right " +
	"choice for effects the tree cannot show: formatting, canvas drawing, video " +
	"position.\n" +
	"  - \"full\": the full tree AND the TARGET-WINDOW screenshot.\n" +
	"A11Y screenshots are visual evidence only. Their coordinates are window-relative " +
	"and must never be passed to the full-desktop `GUI` tool."

const getAppStateScreenshotHelp = "Whether get_app_state should also return the " +
	"TARGET-WINDOW screenshot. Defaults to false. The image is window-relative visual " +
	"evidence for the accessibility track and is never a coordinate source for gui."

const (
	observeNone       = "none"
	observeDiff       = "diff"
	observeTree       = "tree"
	observeScreenshot = "screenshot"
	observeFull       = "full"
)

var observeValues = []string{observeNone, observeDiff, observeTree, observeScreenshot, observeFull}
var a11yObserveValues = []string{observeDiff, observeTree, observeScreenshot, observeFull}
var a11yActObserveValues = []string{observeNone, observeDiff, observeScreenshot}

const a11yObserveArgumentHelp = "Returned evidence: diff (default current-state " +
	"changed semantic subtrees, with compact absent-ref lifecycle context), tree " +
	"(working AX), screenshot (target window), or full (tree and image)."

type observation struct {
	mode            string
	captureSnapshot *bool
}

func (o observation) resolvedMode() string {
	if o.mode == "" {
		return observeDiff
	}
	return o.mode
}

func (o observation) resolvedScreenshot() *bool {
	switch o.resolvedMode() {
	case observeScreenshot, observeFull:
		on := true
		return &on
	default:
		off := false
		return &off
	}
}

func parseObservation(args map[string]any) (observation, error) {
	obs := observation{}
	raw := strings.TrimSpace(strings.ToLower(optionalString(args, "observe")))
	if raw == "" {
		return obs, nil
	}
	// "notes" 是这个字段在内部使用过的旧名字，等价于 "none"。认它，但不宣传。
	if raw == "notes" {
		raw = observeNone
	}
	for _, value := range observeValues {
		if raw == value {
			obs.mode = raw
			return obs, nil
		}
	}
	return observation{}, fmt.Errorf(
		"invalid observe %q. Valid values are %s. Omit it for the default %q, which "+
			"returns only what changed since the previous tree state delivered to the model.",
		optionalString(args, "observe"), strings.Join(observeValues, ", "), observeDiff)
}

// expectArgumentHelp 是 expect 参数**给 LLM 的全部文档**，所以它必须自己讲完
// 所有谓词、目标语法、以及 verified 与 isError 的区别——调用方没有别处可查。
const expectArgumentHelp = "OPTIONAL. State what this action should CAUSE, and the " +
	"server will act, wait for the UI to settle, re-read the tree and come back with a " +
	"definite verdict instead of \"delivery was not verified\". Without it nothing " +
	"changes: you get the same unverified notes as before.\n" +
	"Predicates are phrased in toolkit-independent terms (role / name / value / state / focus); " +
	"pass one, or several to require all of them:\n" +
	"  • appears — the number of elements matching the full target increases after the action. " +
	"This is a CHANGE assertion, compared against the snapshot taken before the action; role, " +
	"name, and other specified target fields remain ANDed throughout the comparison. An " +
	"unchanged count reports predicate_verified:false because \"it exists\" is not \"a new " +
	"matching element appeared\". Use it for things the action creates — " +
	"a dialog, a menu, an error label — not for scenery that was on screen all along. " +
	"e.g. {\"appears\": \"role=dialog\"} or {\"appears\": \"role=push button,name=Save\"}\n" +
	"  • disappears — the number of elements matching the full target decreases after the " +
	"action, the way to wait for one of several matching controls to close. Symmetric with " +
	"appears: something that was never there cannot disappear, and that case reports " +
	"predicate_verified:false rather than a vacuous true. " +
	"e.g. {\"disappears\": \"element_index=42\"}\n" +
	"  • exists_after — a pure post-action presence assertion. Unlike appears, it does not " +
	"claim the element is new and does not require a pre-action baseline. " +
	"e.g. {\"exists_after\": \"role=dialog\"}\n" +
	"  • value_becomes — an object {\"element_index\": <index|selector|target>, \"contains\": \"…\"} " +
	"(substring, case-insensitive) or \"equals\": \"…\" (exact, whitespace-trimmed). This predicate " +
	"reads only the element's accessibility value; a match in name, description, or placeholder " +
	"does not satisfy it. The target must match exactly one observed element.\n" +
	"  • state_becomes — an object {\"element_index\": <index|selector|target>, \"state\": \"checked\"}. " +
	"The requested state must NOT hold in the pre-action snapshot and MUST hold after the action. " +
	"Use this only for a transition caused by the action. Prefix ! to require a transition to " +
	"absence. The target must match exactly one element in both snapshots.\n" +
	"  • state_is_after — the same object shape as state_becomes, but a pure post-action state " +
	"assertion. Use it when the state may already have held before the action.\n" +
	"  • focus_moves_to — some element matching the target reports the focused state. " +
	"e.g. {\"focus_moves_to\": \"role=entry\"}\n" +
	"  • focus_within — the focused element is the target itself or an observed descendant, " +
	"using the runtime's toolkit-independent accessibility path. " +
	"e.g. {\"focus_within\": \"role=dialog\"}\n" +
	"BASELINE. appears and disappears need a pre-action snapshot to compare against. The " +
	"server keeps the last one per app, so calling get_app_state before acting (which the " +
	"tools already ask you to do) is enough. Without one they report predicate_verified:false and say " +
	"so — they will not fall back to a plain presence check, because that is exactly the " +
	"false confirmation this design exists to avoid.\n" +
	"TARGET SYNTAX. " + expectTargetHelp + "\n" +
	"THE RESULT. A note tagged [expect] carries {\"predicate_verified\": true|false, " +
	"\"evidence\": \"…\", \"settled_after_ms\": N}. predicate_verified:false is NOT an error — " +
	"isError stays false because the predicate verdict is a fact about the observed tree, not " +
	"a mistake in your call. It does not imply delivery; the action notes may establish only " +
	"that input was requested or emitted. isError is still reserved for argument and addressing " +
	"problems (unknown app, unresolvable element_index, a malformed expect). When " +
	"predicate_verified is true, only the declared accessibility predicate held; it does not " +
	"confirm the intended effect or remove the need for any broader verification. Read " +
	"`evidence` before retrying: it reports what the tree actually shows."

const settleArgumentHelp = "How long, in milliseconds, to keep waiting for `expect` to come " +
	"true before giving up. Defaults to 800; ignored when expect is absent. This is a BUDGET, " +
	"not a sleep: the server polls and returns the instant the expectation holds, and reports " +
	"the real wait as settled_after_ms. The first check reuses the snapshot the action already " +
	"built, so the common case (the dialog is up by the time the action returns) costs nothing " +
	"extra and reports 0ms. Every later poll is a full accessibility-tree walk, so keep this " +
	"tight. Maximum 3000. If the predicate is still false after 3 seconds, use the returned " +
	"evidence and a fresh observation or assert_ax instead of extending or stacking waits."

// expectProperty 把谓词写进 schema 本身，而不是只写在描述里。
func expectProperty() map[string]any {
	target := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	return map[string]any{
		"type":        "object",
		"description": expectArgumentHelp,
		"properties": map[string]any{
			"appears": target("Target that must be present in the tree after the action AND " +
				"have a higher matching count than in the snapshot taken before it — this asserts " +
				"a CHANGE, not mere presence, and keeps all specified role/name fields ANDed. " +
				"An unchanged count reports predicate_verified:false. " +
				"e.g. \"role=dialog\" or \"push button \\\"Save\\\"\". " + expectTargetHelp),
			"disappears": target("Target whose matching count must decrease from the pre-action " +
				"snapshot to the post-action tree. All specified role/name fields remain ANDed. " +
				"A target that never matched cannot disappear and reports predicate_verified:false. " +
				"e.g. \"element_index=42\". " +
				expectTargetHelp),
			"exists_after": target("Target that must exist in the post-action tree. This is " +
				"presence only, not a claim that the target newly appeared, and it needs no " +
				"pre-action baseline. " + expectTargetHelp),
			"focus_moves_to": target("Target that must report the focused state after the " +
				"action, e.g. \"role=entry\". " + expectTargetHelp),
			"focus_within": target("Target that must either hold focus itself or contain the " +
				"focused element as an observed accessibility descendant. " + expectTargetHelp),
			"value_becomes": map[string]any{
				"type": "object",
				"description": "Assert one unambiguous element's value. contains is a case-insensitive " +
					"substring; equals is exact after trimming whitespace. Only the accessibility " +
					"value is examined. Pass one of them, not both. A target matching multiple " +
					"elements reports predicate_verified:false instead of choosing one.",
				"properties": map[string]any{
					"element_index": map[string]any{
						"anyOf": []any{
							map[string]any{"type": "string"},
							map[string]any{"type": "integer"},
						},
						"description": "Which element to read: an index from the tree, a selector, " +
							"or a target such as \"role=entry\". " + expectTargetHelp,
					},
					"contains": map[string]any{"type": "string", "description": "Expected substring of the value."},
					"equals":   map[string]any{"type": "string", "description": "Expected exact value."},
				},
				"required":             []string{"element_index"},
				"additionalProperties": false,
			},
			"state_becomes": map[string]any{
				"type": "object",
				"description": "Assert that one unambiguous target transitions into the requested " +
					"accessibility state. The state must not already hold in the pre-action snapshot.",
				"properties": map[string]any{
					"element_index": map[string]any{
						"anyOf": []any{
							map[string]any{"type": "string"},
							map[string]any{"type": "integer"},
						},
						"description": "Which element to inspect: an index, selector, or target. " + expectTargetHelp,
					},
					"state": map[string]any{
						"type":        "string",
						"description": "Expected state such as checked, focused, selected, or expanded. Prefix ! to require absence.",
					},
				},
				"required":             []string{"element_index", "state"},
				"additionalProperties": false,
			},
			"state_is_after": map[string]any{
				"type": "object",
				"description": "Assert one unambiguous target's reported accessibility state after " +
					"the action without claiming the action caused a transition.",
				"properties": map[string]any{
					"element_index": map[string]any{
						"anyOf": []any{
							map[string]any{"type": "string"},
							map[string]any{"type": "integer"},
						},
						"description": "Which element to inspect: an index, selector, or target. " + expectTargetHelp,
					},
					"state": map[string]any{
						"type":        "string",
						"description": "Expected state such as checked, focused, selected, or expanded. Prefix ! to require absence.",
					},
				},
				"required":             []string{"element_index", "state"},
				"additionalProperties": false,
			},
		},
		"additionalProperties": false,
	}
}

func settleProperty() map[string]any {
	return map[string]any{
		"type":        "integer",
		"minimum":     0,
		"maximum":     maxSettleMS,
		"description": settleArgumentHelp,
	}
}

// expectVerificationHelp 挂在**每一个**动作工具的描述末尾。
// 调用方是 LLM，描述就是它的全部文档：如果这段只写在某一个工具上，
// 其余工具对它就等于不存在。最后一句讲的是它与 `observe` 的关系：两个参数
// 正交，一个管返回什么观测，一个管验证什么结果，可以同时传。
const expectVerificationHelp = " VERIFICATION: this call is fire-and-forget by default and " +
	"says so (\"delivery was not verified\"). Pass `expect` to close the loop instead — the " +
	"server then acts, waits for the UI to settle (`settle_ms`, default 800), re-reads the " +
	"tree and returns {\"predicate_verified\": true|false, \"evidence\": \"…\", " +
	"\"settled_after_ms\": N}. predicate_verified describes only the declared accessibility " +
	"predicate, not action delivery or the broader intended effect. A false verdict is not a " +
	"call error, so isError stays false. Omitting `expect` leaves behaviour exactly as it was. " +
	"`expect` and `observe` are ORTHOGONAL and combine freely: `observe` decides how much " +
	"observation comes BACK (none/diff/tree/screenshot/full), `expect` decides what the server " +
	"VERIFIES before answering. observe=\"none\" with an `expect` still returns the full " +
	"{\"predicate_verified\", \"evidence\", \"settled_after_ms\"} verdict — the verdict is a conclusion, " +
	"not observation, so it is never suppressed — it just comes without a tree or an image."

const appArgumentHelp = "Bootstrap app/process name, app id, or PID; never a window title. " +
	"Prefer returned aN/wN/rN refs."

var toolChannel = map[string]string{
	"a11y": "a11y",
	"gui":  "gui",
}

// actionTools 是"会改变界面、因而会顺带产生一次观测"的那一组。
// observe 参数只对它们有意义——只读工具的观测本身就是它们的返回值。
var actionTools = map[string]bool{
	"a11y": true,
	"gui":  true,
}

func isActionTool(name string) bool { return actionTools[internalToolName(name)] }

func sortedChannels() []string {
	enabled := enabledChannels()
	names := make([]string, 0, len(enabled))
	for name := range enabled {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func toolIsEnabled(name string) bool {
	name = internalToolName(name)
	channel, known := toolChannel[name]
	if !known {
		return true
	}
	return enabledChannels()[channel]
}

func toolDefinitions() []toolDefinition {
	kept := make([]toolDefinition, 0, 2)
	for _, tool := range allToolDefinitions() {
		if tool.Name != publicToolAX && tool.Name != publicToolGUI {
			continue
		}
		if toolIsEnabled(tool.Name) {
			kept = append(kept, tool)
		}
	}
	return kept
}

func publicToolCallName(name string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "ax":
		return publicToolAX, true
	case "gui":
		return publicToolGUI, true
	default:
		return "", false
	}
}

func a11yStructuredSelectorProperty() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": "Exact AX identity fields, ANDed in one window.",
		"properties": map[string]any{
			"role":        stringProperty("Exact AX role."),
			"name":        stringProperty("Exact accessible name."),
			"description": stringProperty("Exact accessible description."),
			"placeholder": stringProperty("Exact accessible placeholder."),
			"state": enumStringProperty(
				"Optional distinguishing state.",
				[]string{"checked", "expanded", "selected", "focused"},
			),
		},
		"minProperties":        1,
		"additionalProperties": false,
	}
}

func a11yTargetProperty() map[string]any {
	index := objectSchema(map[string]any{
		"index": map[string]any{
			"type":        "integer",
			"minimum":     0,
			"description": "Current-snapshot index; action 1 only.",
		},
	}, []string{"index"})
	ref := objectSchema(map[string]any{
		"ref": map[string]any{
			"type":        "string",
			"pattern":     "^(r[1-9][0-9]*|w[1-9][0-9]*)$",
			"description": "Current session ref; rN identifies an element and wN identifies one window.",
		},
	}, []string{"ref"})
	selector := objectSchema(map[string]any{
		"selector": a11yStructuredSelectorProperty(),
	}, []string{"selector"})
	return map[string]any{
		"type":        "object",
		"description": "One index, ref, or selector.",
		"oneOf":       []any{index, ref, selector},
	}
}

func a11yElementRefTargetProperty() map[string]any {
	return objectSchema(map[string]any{
		"ref": map[string]any{
			"type":        "string",
			"pattern":     "^r[1-9][0-9]*$",
			"description": "Current AX element ref.",
		},
	}, []string{"ref"})
}

func a11yAssertionElementTargetProperty() map[string]any {
	index := objectSchema(map[string]any{
		"index": map[string]any{
			"type":        "integer",
			"minimum":     0,
			"description": "Current read snapshot index.",
		},
	}, []string{"index"})
	ref := a11yElementRefTargetProperty()
	selector := objectSchema(map[string]any{
		"selector": a11yStructuredSelectorProperty(),
	}, []string{"selector"})
	return map[string]any{
		"type":        "object",
		"description": "One read-only element target. Window refs are not element targets.",
		"oneOf":       []any{index, ref, selector},
	}
}

func a11yWindowRefTargetProperty() map[string]any {
	return objectSchema(map[string]any{
		"ref": map[string]any{
			"type":        "string",
			"pattern":     "^w[1-9][0-9]*$",
			"description": "Current AX window ref.",
		},
	}, []string{"ref"})
}

func a11yActionBranch(
	actionType string,
	extra map[string]any,
	required []string,
) map[string]any {
	properties := map[string]any{
		"type": constStringProperty(
			actionType,
			"AX action type.",
		),
		"target":        a11yElementRefTargetProperty(),
		"postcondition": map[string]any{"$ref": "#/$defs/assertion"},
	}
	for field, property := range extra {
		properties[field] = property
	}
	return objectSchema(properties, append([]string{"type"}, required...))
}

func a11yActionProperty() map[string]any {
	return map[string]any{
		"description": "One AX action.",
		"oneOf": []any{
			objectSchema(map[string]any{
				"type": constStringProperty(
					"activate",
					"Activate one uniquely bound top-level window.",
				),
				"target":        a11yWindowRefTargetProperty(),
				"postcondition": map[string]any{"$ref": "#/$defs/assertion"},
			}, []string{"type", "target"}),
			objectSchema(map[string]any{
				"type": constStringProperty(
					"close",
					"Close one uniquely bound top-level window; this must be the final action.",
				),
				"target":        a11yWindowRefTargetProperty(),
				"postcondition": map[string]any{"$ref": "#/$defs/assertion"},
			}, []string{"type", "target"}),
			a11yActionBranch("click", nil, []string{"target"}),
			a11yActionBranch("keypress", map[string]any{
				"keys": map[string]any{
					"type":        "array",
					"description": "Keys emitted together, e.g. [\"CTRL\",\"S\"].",
					"minItems":    1,
					"maxItems":    guiMaxKeys,
					"items": map[string]any{
						"type":      "string",
						"minLength": 1,
						"maxLength": guiMaxKeyLength,
					},
				},
			}, []string{"keys"}),
			a11yActionBranch("scroll", map[string]any{
				"direction": enumStringProperty(
					"Scroll direction.",
					[]string{"up", "down", "left", "right"},
				),
				"pages": numberRangeProperty(
					"Number of pages. Defaults to 1.",
					0,
					maxScrollPages,
				),
			}, []string{"direction"}),
			a11yActionBranch("set_value", map[string]any{
				"value": boundedStringProperty(
					"Value to set.",
					maxActionText,
				),
			}, []string{"target", "value"}),
			a11yActionBranch("type", map[string]any{
				"text": boundedStringProperty(
					"Literal text to type.",
					maxActionText,
				),
			}, []string{"text"}),
		},
	}
}

func a11yFindQueryProperty() map[string]any {
	query := map[string]any{
		"type":        "object",
		"description": "Bounded AX lookup. Supply at least one explicit filter; filters are ANDed and case-insensitive.",
		"properties": map[string]any{
			"role":          stringProperty("Role substring."),
			"name_contains": stringProperty("Accessible-name substring."),
			"name_equals":   stringProperty("Exact accessible name."),
			"text_contains": stringProperty("Substring across name, description, value, or placeholder."),
			"text_equals":   stringProperty("Exact match against one text-bearing field."),
			"state":         stringProperty("State substring."),
			"limit":         integerRangeProperty("Maximum matches to return. Defaults to 20.", 1, maxAXFindPageSize),
		},
		"additionalProperties": false,
		"anyOf": []any{
			map[string]any{"required": []string{"role"}},
			map[string]any{"required": []string{"name_contains"}},
			map[string]any{"required": []string{"name_equals"}},
			map[string]any{"required": []string{"text_contains"}},
			map[string]any{"required": []string{"text_equals"}},
			map[string]any{"required": []string{"state"}},
		},
		"allOf": []any{
			map[string]any{"not": map[string]any{"required": []string{"name_contains", "name_equals"}}},
			map[string]any{"not": map[string]any{"required": []string{"text_contains", "text_equals"}}},
		},
	}
	return query
}

func a11yAssertionProperty() map[string]any {
	stable := integerRangeProperty(
		"Optional time the predicate must remain continuously true before PASS. Defaults to 0.",
		0,
		5000,
	)
	timeout := integerRangeProperty(
		"Polling budget in milliseconds. Defaults to 5000.",
		1,
		maxVerifyTimeout,
	)
	elementBranch := func(
		assertionType string,
		extra map[string]any,
		required []string,
	) map[string]any {
		properties := map[string]any{
			"type":       constStringProperty(assertionType, "AX assertion predicate type."),
			"target":     a11yAssertionElementTargetProperty(),
			"stable_ms":  stable,
			"timeout_ms": timeout,
		}
		for key, value := range extra {
			properties[key] = value
		}
		return objectSchema(properties, append([]string{"type", "target"}, required...))
	}
	windowText := stringProperty(
		"Case-insensitive title substring for a current top-level window owned by the resolved application.",
	)
	windowText["minLength"] = 1
	return map[string]any{
		"description": "Poll exactly one typed AX-visible predicate.",
		"oneOf": []any{
			elementBranch("exists", map[string]any{
				"value": booleanProperty("Whether the target must exist."),
			}, []string{"value"}),
			elementBranch("state", map[string]any{
				"state": stringProperty("Accessibility state name, such as checked, focused, selected, or expanded."),
				"value": booleanProperty("Whether the state must be present."),
			}, []string{"state", "value"}),
			func() map[string]any {
				branch := elementBranch("value", map[string]any{
					"value": map[string]any{
						"type":        []string{"string", "number", "boolean"},
						"minLength":   1,
						"description": "Required AX scalar value.",
					},
					"match": enumStringProperty(
						`Comparison mode. Defaults to "equals".`,
						[]string{"equals", "contains"},
					),
				}, []string{"value"})
				branch["allOf"] = []any{
					map[string]any{
						"if": map[string]any{
							"properties": map[string]any{
								"match": map[string]any{"const": "contains"},
							},
							"required": []string{"match"},
						},
						"then": map[string]any{
							"properties": map[string]any{
								"value": map[string]any{"type": "string"},
							},
						},
					},
				}
				return branch
			}(),
			elementBranch("text_contains", map[string]any{
				"text": stringProperty("Required substring across accessible name, description, value, or placeholder."),
			}, []string{"text"}),
			objectSchema(map[string]any{
				"type":       constStringProperty("window_title_contains", "Window-title predicate."),
				"text":       windowText,
				"exists":     booleanProperty("Defaults to true; false waits until no title contains the text."),
				"stable_ms":  stable,
				"timeout_ms": timeout,
			}, []string{"type", "text"}),
		},
	}
}

func a11yObservationRootProperty() map[string]any {
	focused := objectSchema(map[string]any{
		"focused": map[string]any{
			"type":        "boolean",
			"const":       true,
			"description": "Return a bounded local subtree around the current focused element.",
		},
	}, []string{"focused"})
	target := a11yTargetProperty()
	branches := []any{focused}
	if targetBranches, ok := target["oneOf"].([]any); ok {
		branches = append(branches, targetBranches...)
	}
	return map[string]any{
		"description": "Optional focused or targeted local subtree.",
		"oneOf":       branches,
	}
}

func constStringProperty(value, description string) map[string]any {
	return map[string]any{
		"type":        "string",
		"const":       value,
		"description": description,
	}
}

func a11yInputSchema() map[string]any {
	constOnly := func(value string) map[string]any {
		return map[string]any{"type": "string", "const": value}
	}
	enumOnly := func(values []string) map[string]any {
		return map[string]any{"type": "string", "enum": values}
	}
	appProperty := map[string]any{"$ref": "#/$defs/app"}
	appRefProperty := map[string]any{"$ref": "#/$defs/app_ref"}
	windowRefProperty := map[string]any{"$ref": "#/$defs/window_ref"}
	readIdentity := []any{
		map[string]any{"$ref": "#/$defs/by_app"},
		map[string]any{"$ref": "#/$defs/by_app_ref"},
		map[string]any{"$ref": "#/$defs/by_window_ref"},
	}
	observeApp := objectSchema(map[string]any{
		"mode":               constOnly(a11yModeObserve),
		"observation_target": constOnly("app"),
		"app":                appProperty,
		"app_ref":            appRefProperty,
		"window_ref":         windowRefProperty,
		"observe": enumStringProperty(
			a11yObserveArgumentHelp,
			a11yObserveValues,
		),
		"scope": enumOnly(a11yScopeValues),
		"root":  a11yObservationRootProperty(),
	}, []string{"mode"})
	observeApp["oneOf"] = readIdentity

	observeApps := objectSchema(map[string]any{
		"mode":               constOnly(a11yModeObserve),
		"observation_target": constOnly("apps"),
	}, []string{"mode", "observation_target"})

	act := objectSchema(map[string]any{
		"mode": constOnly(a11yModeAct),
		"window_ref": map[string]any{
			"$ref": "#/$defs/window_ref",
		},
		"actions": map[string]any{
			"type": "array",
			"description": fmt.Sprintf(
				"Ordered same-window actions; maximum %d. Batch known steps; "+
					"split on unknown results. Guard a dependent next action with "+
					"the earlier action's postcondition.",
				a11yBatchMaxActions,
			),
			"minItems": 1,
			"maxItems": a11yBatchMaxActions,
			"items":    a11yActionProperty(),
		},
		"observe": enumStringProperty(
			"ACT ONLY: none|diff|screenshot; tree/full are invalid. Omit for a fresh "+
				"post-action diff. A bounded refresh may return a working tree or "+
				"lifecycle proof; reread only if required.",
			a11yActObserveValues,
		),
		"postcondition": map[string]any{
			"$ref":        "#/$defs/assertion",
			"description": "Poll one final AX predicate until pass or timeout, then sample the selected observation. Prefer this to a separate assert_ax.",
		},
	}, []string{"mode", "actions"})
	act["oneOf"] = []any{
		map[string]any{"$ref": "#/$defs/by_owned_ref"},
		map[string]any{"$ref": "#/$defs/by_window_keyboard"},
	}

	findSearch := objectSchema(map[string]any{
		"mode":       constOnly(a11yModeFind),
		"app":        appProperty,
		"app_ref":    appRefProperty,
		"window_ref": windowRefProperty,
		"query":      a11yFindQueryProperty(),
		"scope":      enumOnly(a11yScopeValues),
	}, []string{"mode", "query"})
	findSearch["oneOf"] = readIdentity
	findContinue := objectSchema(map[string]any{
		"mode": constOnly(a11yModeFind),
		"cursor": map[string]any{
			"type":        "string",
			"pattern":     "^f[1-9][0-9]*$",
			"description": "Continue the exact prior find observation.",
		},
		"limit": integerRangeProperty(
			"Maximum matches in this page. Defaults to 20.",
			1,
			maxAXFindPageSize,
		),
	}, []string{"mode", "cursor"})

	assertA11y := objectSchema(map[string]any{
		"mode":       constOnly(a11yModeAssert),
		"app":        appProperty,
		"app_ref":    appRefProperty,
		"window_ref": windowRefProperty,
		"assertion":  map[string]any{"$ref": "#/$defs/assertion"},
		"scope":      enumOnly(a11yScopeValues),
	}, []string{"mode", "assertion"})
	assertA11y["oneOf"] = []any{
		map[string]any{"$ref": "#/$defs/by_app"},
		map[string]any{"$ref": "#/$defs/by_app_ref"},
		map[string]any{"$ref": "#/$defs/by_window_ref"},
		map[string]any{"$ref": "#/$defs/by_owned_assertion_ref"},
	}

	return map[string]any{
		"type":        "object",
		"description": "Exactly one AX mode.",
		"$defs": map[string]any{
			"target":        a11yTargetProperty(),
			"assertion":     a11yAssertionProperty(),
			"app":           stringProperty(appArgumentHelp),
			"app_ref":       map[string]any{"type": "string", "pattern": "^a[1-9][0-9]*$"},
			"window_ref":    map[string]any{"type": "string", "pattern": "^w[1-9][0-9]*$"},
			"by_app":        map[string]any{"required": []string{"app"}},
			"by_app_ref":    map[string]any{"required": []string{"app_ref"}},
			"by_window_ref": map[string]any{"required": []string{"window_ref"}},
			"by_window_keyboard": map[string]any{
				"required": []string{"window_ref"},
				"properties": map[string]any{
					"actions": map[string]any{
						"not": map[string]any{
							"contains": map[string]any{
								"type":     "object",
								"required": []string{"target"},
							},
						},
					},
				},
			},
			"by_owned_ref": map[string]any{
				"not": map[string]any{"anyOf": []any{
					map[string]any{"required": []string{"app"}},
					map[string]any{"required": []string{"app_ref"}},
					map[string]any{"required": []string{"window_ref"}},
				}},
				"properties": map[string]any{
					"actions": map[string]any{
						"contains": map[string]any{
							"type":     "object",
							"required": []string{"target"},
							"properties": map[string]any{
								"target": map[string]any{
									"type":     "object",
									"required": []string{"ref"},
									"properties": map[string]any{
										"ref": map[string]any{
											"type":    "string",
											"pattern": "^(?:r|w)[1-9][0-9]*$",
										},
									},
								},
							},
						},
					},
				},
			},
			"by_owned_assertion_ref": map[string]any{
				"not": map[string]any{"anyOf": []any{
					map[string]any{"required": []string{"app"}},
					map[string]any{"required": []string{"app_ref"}},
					map[string]any{"required": []string{"window_ref"}},
				}},
				"properties": map[string]any{
					"assertion": map[string]any{
						"type":     "object",
						"required": []string{"target"},
						"properties": map[string]any{
							"target": map[string]any{
								"type":     "object",
								"required": []string{"ref"},
								"properties": map[string]any{
									"ref": map[string]any{
										"type":    "string",
										"pattern": "^r[1-9][0-9]*$",
									},
								},
							},
						},
					},
				},
			},
		},
		"oneOf": []any{
			observeApp,
			observeApps,
			findSearch,
			findContinue,
			act,
			assertA11y,
		},
	}
}

func guiActionProperty(includeScreenshot bool) map[string]any {
	point := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"x": numberProperty("X pixel in the exact returned full-desktop image"),
			"y": numberProperty("Y pixel in the exact returned full-desktop image"),
		},
		"required": []string{"x", "y"},
	}
	mouseKeys := map[string]any{
		"type":        "array",
		"description": "Held modifiers.",
		"maxItems":    guiMaxKeys,
		"items": map[string]any{
			"type": "string",
			"enum": []string{"CTRL", "CONTROL", "SHIFT", "ALT", "SUPER"},
		},
	}
	keyArray := map[string]any{
		"type":        "array",
		"description": "PAGEUP/PAGE_UP/PAGEDOWN/PAGE_DOWN.",
		"minItems":    1,
		"maxItems":    guiMaxKeys,
		"items": map[string]any{
			"type":      "string",
			"minLength": 1,
			"maxLength": guiMaxKeyLength,
		},
	}
	branch := func(
		actionType, description string,
		properties map[string]any,
		required []string,
	) map[string]any {
		fields := map[string]any{
			"type": constStringProperty(actionType, description),
		}
		for key, value := range properties {
			fields[key] = value
		}
		return objectSchema(fields, append([]string{"type"}, required...))
	}
	scroll := branch("scroll", "Scroll at a point.", map[string]any{
		"x":        numberProperty("Screenshot X pixel."),
		"y":        numberProperty("Screenshot Y pixel."),
		"scroll_x": map[string]any{"type": "number", "minimum": -guiMaxScrollDelta, "maximum": guiMaxScrollDelta},
		"scroll_y": map[string]any{"type": "number", "minimum": -guiMaxScrollDelta, "maximum": guiMaxScrollDelta},
		"keys":     mouseKeys,
	}, []string{"x", "y"})
	scroll["anyOf"] = []any{
		map[string]any{
			"required": []string{"scroll_x"},
			"properties": map[string]any{
				"scroll_x": map[string]any{"not": map[string]any{"const": 0}},
			},
		},
		map[string]any{
			"required": []string{"scroll_y"},
			"properties": map[string]any{
				"scroll_y": map[string]any{"not": map[string]any{"const": 0}},
			},
		},
	}
	branches := []any{}
	if includeScreenshot {
		branches = append(branches,
			branch("screenshot", "Capture the final full desktop; this action must be the only action in the call.", nil, nil),
		)
	}
	branches = append(branches,
		branch("click", "Click.", map[string]any{
			"x": numberProperty("Screenshot X pixel."),
			"y": numberProperty("Screenshot Y pixel."),
			"button": enumStringProperty(
				"Mouse button. Defaults to left.",
				[]string{"left", "right", "wheel", "back", "forward"},
			),
			"keys": mouseKeys,
		}, []string{"x", "y"}),
		branch("double_click", "Double click.", map[string]any{
			"x":    numberProperty("Screenshot X pixel."),
			"y":    numberProperty("Screenshot Y pixel."),
			"keys": mouseKeys,
		}, []string{"x", "y"}),
		branch("move", "Move pointer.", map[string]any{
			"x":    numberProperty("Screenshot X pixel."),
			"y":    numberProperty("Screenshot Y pixel."),
			"keys": mouseKeys,
		}, []string{"x", "y"}),
		branch("drag", "Drag path.", map[string]any{
			"path": map[string]any{
				"type": "array", "minItems": 2, "maxItems": guiMaxDragPoints,
				"items": point,
			},
			"keys": mouseKeys,
		}, []string{"path"}),
		scroll,
		branch("type", "Type literal text.", map[string]any{
			"text": map[string]any{"type": "string", "maxLength": guiMaxTextLength},
		}, []string{"text"}),
		branch("keypress", "Press one key chord.", map[string]any{
			"keys": keyArray,
		}, []string{"keys"}),
		branch("wait", "Wait exactly one second without an intermediate observation; do not stack waits.", nil, nil),
	)
	return map[string]any{
		"description": "One ComputerAction.",
		"oneOf":       branches,
	}
}

func guiInputSchema() map[string]any {
	observe := objectSchema(map[string]any{
		"actions": map[string]any{
			"type":     "array",
			"minItems": 1,
			"maxItems": 1,
			"items": objectSchema(map[string]any{
				"type": constStringProperty(
					"screenshot",
					"Capture the final full desktop; this must be the only action.",
				),
			}, []string{"type"}),
		},
	}, []string{"actions"})
	act := objectSchema(map[string]any{
		"actions": map[string]any{
			"type":        "array",
			"minItems":    1,
			"maxItems":    guiMaxActions,
			"items":       guiActionProperty(false),
			"description": "Ordered ComputerAction-compatible actions.",
		},
		"settle_ms": integerRangeProperty(
			fmt.Sprintf(
				"Visual stillness: default %dms, max %dms. "+
					"Final screenshot stillness, not extra sleep. After wait, usually omit it. "+
					"At the cap inspect the screenshot or use AX assert_ax.",
				defaultSettleMS,
				guiMaxSettleMS,
			),
			0,
			guiMaxSettleMS,
		),
	}, []string{"actions"})
	return map[string]any{
		"type":        "object",
		"description": "One screenshot action observes; otherwise run an input batch.",
		"oneOf":       []any{observe, act},
	}
}

func allToolDefinitions() []toolDefinition {
	return []toolDefinition{
		{
			Name: publicToolAX,
			Description: fmt.Sprintf(
				axToolProjectionContract+"Semantic observe/find/act/assert_ax. "+
					"Observe/find issue refs; act consumes refs only. "+
					"Reads use app, app_ref, or window_ref; continue find with fN. "+
					"Act returns current diff/tree/lifecycle; reread only if required. "+
					"Use batch or action postconditions for known results. "+
					"Element actions require rN, window actions require wN; act never accepts app, selector, or index. "+
					"Targeted act rejects app_ref/window_ref; refs own both. Never invent refs. "+
					"One wN per batch; targetless batches require window_ref. Stale refs never rebind. "+
					"Working output may hide controls; find searches canonical AX records. "+
					"Refs resolve live per action; continuity failure stops the batch. "+
					"Batch known same-window steps. Split before window changes or unknown next targets. "+
					"AX proves only AX-visible facts, not pixels/files/persistence/task completion. "+
					"Limits: %d actions, %d text chars.",
				a11yBatchMaxActions,
				a11yBatchMaxText,
			),
			Annotations: nil,
			InputSchema: a11yInputSchema(),
		},
		{
			Name: publicToolGUI,
			Description: fmt.Sprintf(
				"Full-desktop ComputerAction batch; screenshot alone observes. "+
					"Batch validates before input and returns one final screenshot. "+
					"Coordinates are delivered-image pixels. AX activates/closes windows. "+
					"type/keypress use current focus. wait is one second, no observation, max %ds/batch. "+
					"scroll_x/scroll_y are Computer deltas. settle_ms checks visual stillness, not semantic success; "+
					"omitted default %dms, max %dms.",
				guiMaxTotalWait,
				defaultSettleMS,
				guiMaxSettleMS,
			),
			Annotations: nil,
			InputSchema: guiInputSchema(),
		},
	}
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func defaultAnnotations() map[string]any {
	return map[string]any{"destructiveHint": false, "openWorldHint": false}
}

func readOnlyAnnotations() map[string]any {
	return map[string]any{"destructiveHint": false, "idempotentHint": true, "openWorldHint": false, "readOnlyHint": true}
}

func stringProperty(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func boundedStringProperty(description string, maxLength int) map[string]any {
	property := stringProperty(description)
	property["maxLength"] = maxLength
	return property
}

func enumStringProperty(description string, values []string) map[string]any {
	property := stringProperty(description)
	property["enum"] = values
	return property
}

func numberProperty(description string) map[string]any {
	return map[string]any{"type": "number", "description": description}
}

func numberRangeProperty(description string, minimum, maximum any) map[string]any {
	property := numberProperty(description)
	property["exclusiveMinimum"] = minimum
	property["maximum"] = maximum
	return property
}

func integerProperty(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

func integerRangeProperty(description string, minimum, maximum int) map[string]any {
	property := integerProperty(description)
	property["minimum"] = minimum
	property["maximum"] = maximum
	return property
}

func optionalBool(args map[string]any, key string) *bool {
	value, ok := args[key]
	if !ok {
		return nil
	}
	if flag, ok := value.(bool); ok {
		return &flag
	}
	if text, ok := value.(string); ok {
		flag := text == "true"
		return &flag
	}
	return nil
}

func booleanProperty(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

func positiveIntegerProperty(description string) map[string]any {
	return map[string]any{"type": "integer", "minimum": 1, "description": description}
}

func textLimitProperty(description string) map[string]any {
	return map[string]any{
		"anyOf": []any{
			map[string]any{"type": "integer", "minimum": 1},
			map[string]any{"type": "string", "enum": []string{"max"}},
		},
		"description": description,
	}
}
