package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

const (
	publicToolAX  = "AX"
	publicToolGUI = "GUI"
)

const (
	guiMaxActions      = 64
	guiMaxScrollDelta  = 2000
	guiMaxDragPoints   = 256
	guiMaxKeys         = 8
	guiMaxKeyLength    = 64
	guiMaxTextLength   = 10000
	guiMaxTotalWait    = 20
	guiMaxSettleMS     = 3000
	axBatchMaxActions  = 64
	axBatchMaxText     = 20000
	maxClickCount      = 10
	maxScrollPages     = 100
	maxVerifyTimeout   = 60000
	maxActionText      = 10000
	maxMCPMessageBytes = 1 << 20
	maxDeliveredTrees  = 512
)

// serverInstructions is the MCP server description. It says how the two
// tools divide the work; call shapes live in the tool descriptions.
const serverInstructions = "" +
	"Two tools for the running desktop. AX reads and operates application windows " +
	"through accessibility: it lists windows, returns controls as rN refs, acts on " +
	"them, and waits with wait steps. GUI takes full-desktop screenshots and sends pixel " +
	"input. Use AX for named controls, values, menus, dialogs, windows, and keys. Use " +
	"GUI for pixels, canvases, dragging, and controls that AX does not expose; after " +
	"one targeted AX find misses a visible control, use GUI for it. Send all desktop " +
	"input (window activation, keys, text, mouse) through these tools, not shell input " +
	"utilities or scripted accessibility calls: the tools check the target window " +
	"before input and report what changed, and outside input makes their refs and " +
	"diffs stale. After GUI input, read AX again before reusing refs."

type toolDefinition struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	Annotations map[string]any `json:"annotations,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
}

// enabledChannels reads the channel capability switch. COMPUTER_USE_CHANNELS is a comma-separated list; by default
// all three channels (a11y, gui, keyboard) are enabled.
//
// A disabled channel's tools are absent from tools/list rather than rejected
// at call time: a model tries every tool it can see, and each rejection wastes
// a turn.
//
// The switch serves ablation experiments that need a channel truly disabled,
// not just its screenshots, and deployments that trust only semantic actions
// and want coordinate synthesis disabled entirely.
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

const (
	observeNone       = "none"
	observeDiff       = "diff"
	observeTree       = "tree"
	observeScreenshot = "screenshot"
	observeFull       = "full"
)

var observeValues = []string{observeDiff, observeTree, observeFull, observeScreenshot, observeNone}

// appArgumentHelp describes the app argument. Accessibility app names mix
// display names, binary names and app ids, and no rule derives one from
// another, so the argument accepts any of these or a PID. An aN is also
// accepted but not advertised: apps names windows by wN.
const appArgumentHelp = "App name, app id, or PID; use once, then window_ref."

// toolChannel maps each public tool to the channel that enables it. Tools
// outside the map belong to no channel and are always enabled.
var toolChannel = map[string]string{
	publicToolAX:  "a11y",
	publicToolGUI: "gui",
}

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

// publicToolCallName accepts exactly the advertised tool names. MCP tool
// names are case-sensitive, and a tolerated spelling would be one the
// schema never describes.
func publicToolCallName(name string) (string, bool) {
	switch name {
	case publicToolAX, publicToolGUI:
		return name, true
	default:
		return "", false
	}
}

// nonBlank matches a string with at least one non-space character; the
// runtime rejects blank strings in these fields.
const nonBlank = "\\S"

func nonBlankString(description string) map[string]any {
	property := map[string]any{"type": "string", "pattern": nonBlank}
	if description != "" {
		property["description"] = description
	}
	return property
}

// axInputSchema is the single flat AX schema. It has no oneOf: which
// fields belong to which mode or action type is checked by decodeAXRequest,
// which names the field and shows a corrected call. Keep it under the
// client compaction budget (TestAXSchemaSurvivesClientCompaction).
func axInputSchema() map[string]any {
	query := map[string]any{}
	for _, field := range axFindFilters {
		query[field] = nonBlankString("")
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"mode"},
		// window_ref already names its app.
		"not": map[string]any{"required": []string{"app", "window_ref"}},
		"$defs": map[string]any{"query": map[string]any{
			"type": "object", "additionalProperties": false, "properties": query,
			"description": "At least one filter; ANDed, case-insensitive.",
		}},
		"properties": map[string]any{
			"mode": map[string]any{
				"type": "string", "enum": []string{axModeApps, axModeObserve, axModeFind, axModeAct},
				"description": "apps lists confirmed top-level windows (wN, title, app, active) and reports incomplete inventories; observe returns a tree or diff with rN; find returns matching rN; act runs actions.",
			},
			"app": nonBlankString(appArgumentHelp),
			"window_ref": map[string]any{
				"type": "string", "pattern": "^w[1-9][0-9]*$",
				"description": "observe/find: window to read. act: omit when actions have refs, which name the window; it only sends ref-less keys/text to wN instead of the focused window.",
			},
			"scope": map[string]any{"type": "string", "enum": axScopeValues, "description": "observe/find; default window."},
			"observe": map[string]any{
				"type": "string", "enum": observeValues,
				"description": "Returned evidence. act: default diff; explicit tree returns the complete window tree, full adds its screenshot. observe: default tree. none only with act.",
			},
			"root": map[string]any{
				"type": "string", "pattern": "^(focused|r[1-9][0-9]*)$",
				"description": "observe: subtree at rN or the focused element.",
			},
			"query": map[string]any{"$ref": "#/$defs/query"},
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": maxAXFindPageSize, "description": "find page size; default 20."},
			"cursor": map[string]any{
				"type": "string", "pattern": "^f[1-9][0-9]*$",
				"description": "find: next_cursor from a truncated result; send with mode find only.",
			},
			"actions": map[string]any{
				"type": "array", "minItems": 1, "maxItems": axBatchMaxActions,
				"description": "act: ordered steps; a failed step stops the batch. Wait timeouts total at most 60000 ms; an omitted timeout counts as 5000 ms.",
				"items": map[string]any{
					"type": "object", "additionalProperties": false, "required": []string{"type"},
					"properties": map[string]any{
						"type": map[string]any{"type": "string", "enum": axActionTypes},
						"ref": map[string]any{
							"type": "string", "pattern": "^[rw][1-9][0-9]*$",
							"description": "rN element; wN for activate/close. Omit for keys/text to the focused window.",
						},
						"to_ref": map[string]any{
							"type": "string", "pattern": "^r[1-9][0-9]*$",
							"description": "select: range end; ref..to_ref spans text or cells.",
						},
						"title": nonBlankString("activate/close: unique window-title substring, instead of wN."),
						"text": map[string]any{
							"type": "string", "minLength": 1, "maxLength": maxActionText,
							"description": "wait: text in the ref element, or anywhere in the window. select: substring of ref to select.",
						},
						"keys": map[string]any{
							"type": "array", "minItems": 1, "maxItems": guiMaxKeys,
							"items":       map[string]any{"type": "string", "pattern": "^\\S(.*\\S)?$", "maxLength": guiMaxKeyLength},
							"description": "One chord, e.g. [\"CTRL\",\"S\"].",
						},
						"value":     map[string]any{"type": "string", "maxLength": maxActionText},
						"direction": map[string]any{"type": "string", "enum": []string{"up", "down", "left", "right"}},
						"pages":     map[string]any{"type": "number", "exclusiveMinimum": 0, "maximum": maxScrollPages},
						"window":    nonBlankString("wait: a top-level window whose title contains this, on the whole desktop."),
						"query":     map[string]any{"$ref": "#/$defs/query"},
						"state":     nonBlankString("wait: ref state, e.g. checked."),
						"gone":      map[string]any{"type": "boolean", "description": "wait until it is absent."},
						"timeout_ms": map[string]any{
							"type": "integer", "minimum": 1, "maximum": maxVerifyTimeout,
							"description": "wait: default 5000.",
						},
					},
				},
			},
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
			"pattern":   nonBlankPattern,
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
		"description": "One GUI action.",
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
			"description": "Ordered GUI actions.",
		},
		"settle_ms": integerRangeProperty(
			fmt.Sprintf(
				"Visual stillness: default %dms, max %dms. "+
					"Final screenshot stillness, not semantic success and not extra sleep. "+
					"After wait, usually omit it. At the cap inspect the screenshot or use an AX wait step.",
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
		// Some clients close the root object with additionalProperties:false.
		// Declare its field names here; the branches retain every constraint.
		"properties": map[string]any{
			"actions":   map[string]any{},
			"settle_ms": map[string]any{},
		},
		"oneOf": []any{observe, act},
	}
}

// axToolDescription is model-visible on every client, including ones that
// compact the input schema. Clients may truncate descriptions at 1,000 bytes.
const axToolDescription = "Accessibility for application windows. Modes: " +
	"apps lists confirmed windows (wN, title, app, active) and reports incomplete inventories; missing windows then remain unknown. observe returns the window tree with " +
	"rN refs. find returns matching rN. act runs ordered steps, checks them before input, " +
	"stops at the first failed step, and reports each target's change, windows " +
	"opened/closed/focused, and a diff. Copy rN/wN exactly; stale refs fail. keypress/type " +
	"without ref go to the focused window. select marks ref, its text substring, or ref..to_ref " +
	"(paragraphs, cells) and reports the selection read back. activate/close take a title substring or wN. " +
	"A wait step sends no input and fails the batch on timeout: window (title, any app), " +
	"query (as find), ref with value/state/text, or text; gone inverts. After a window " +
	"wait, keys go to that window. Skip waits the report answers. Errors return code and " +
	"hint. Examples:\n" +
	`{"mode":"act","actions":[{"type":"click","ref":"r12"},{"type":"wait","window":"Export"},{"type":"type","text":"q3.csv"},{"type":"keypress","keys":["ENTER"]}]}` + "\n" +
	`{"mode":"find","window_ref":"w2","query":{"role":"button","name_contains":"Save"}}`

var guiToolDescription = fmt.Sprintf("Full-desktop screenshot and pixel input. "+
	`{"actions":[{"type":"screenshot"}]} alone observes. Any other batch is checked before `+
	"input and returns one final screenshot. x/y are pixels of the latest returned full-desktop GUI "+
	"image (screen width x height), never of an AX window image. type/keypress go to the current focus, so click "+
	"the field first in the same batch. Batch the actions planned from one screenshot; end "+
	"the batch after an action that may open, close, or move a window, and plan the rest "+
	"from the returned image. GUI cannot address windows; AX activates and closes them by "+
	"title. wait is one second with no observation, at most %d s per batch. Errors return "+
	"code and hint like AX; gui_frame_stale means nothing was sent: plan again from the "+
	"returned screenshot. Example: "+
	`{"actions":[{"type":"click","x":640,"y":412},{"type":"type","text":"hello"},{"type":"keypress","keys":["ENTER"]}]}`,
	guiMaxTotalWait)

func allToolDefinitions() []toolDefinition {
	annotations := func(title string) map[string]any {
		return map[string]any{"title": title, "destructiveHint": true, "openWorldHint": false}
	}
	return []toolDefinition{
		{
			Name:        publicToolAX,
			Title:       "Accessibility",
			Description: axToolDescription,
			Annotations: annotations("Accessibility"),
			InputSchema: axInputSchema(),
		},
		{
			Name:        publicToolGUI,
			Title:       "Desktop screenshot and input",
			Description: guiToolDescription,
			Annotations: annotations("Desktop screenshot and input"),
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

func stringProperty(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

// nonBlankPattern requires at least one non-whitespace character. The AX
// runtime compares these strings after trimming, so a blank value can match
// nothing.
const nonBlankPattern = `\S`

func enumStringProperty(description string, values []string) map[string]any {
	property := stringProperty(description)
	property["enum"] = values
	return property
}

func numberProperty(description string) map[string]any {
	return map[string]any{"type": "number", "description": description}
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

func constStringProperty(value, description string) map[string]any {
	return map[string]any{
		"type":        "string",
		"const":       value,
		"description": description,
	}
}
