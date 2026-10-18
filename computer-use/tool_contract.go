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
	guiMaxActions       = 64
	guiMaxScrollDelta   = 2000
	guiMaxDragPoints    = 256
	guiMaxKeys          = 8
	guiMaxKeyLength     = 64
	guiMaxTextLength    = 10000
	guiMaxTotalWait     = 20
	guiMaxSettleMS      = 3000
	a11yBatchMaxActions = 32
	a11yBatchMaxText    = 20000
	maxClickCount       = 10
	maxScrollPages      = 100
	maxVerifyTimeout    = 60000
	maxActionText       = 10000
	maxMCPMessageBytes  = 1 << 20
	maxDeliveredTrees   = 512
)

const serverInstructions = "" +
	"Control the already-running desktop with two complementary tools. AX is " +
	"app-scoped semantic observation and action; GUI is full-desktop visual observation " +
	"and absolute-pixel input. Use AX for named controls and GUI for pixels or content " +
	"absent from the accessibility tree. AX find uses explicit equals/contains fields. " +
	"Copy returned rN/wN refs exactly; never invent placeholders. A targeted ref owns " +
	"its app and window, so omit app, app_ref and window_ref. Use window_ref only when " +
	"every action is untargeted keyboard input. Batch only actions valid in one UI " +
	"generation; split after likely dialog, window or navigation changes. Window " +
	"activate/close uses wN and close must be last. AX action acceptance and visual " +
	"change are evidence, not proof of task success; AX cannot prove files, persistence, " +
	"network state or pixels. GUI keyboard/text uses current focus. AX keyboard/pointer " +
	"fallbacks use global desktop focus, not background input. After GUI mutation, " +
	"refresh AX before reusing semantic state. AX target-window screenshot coordinates " +
	"are never GUI coordinates; GUI coordinates use the exact delivered full-desktop image."

type toolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Annotations map[string]any `json:"annotations,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
}

const axToolProjectionContract = ""

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

var observeValues = []string{observeNone, observeDiff, observeTree, observeScreenshot, observeFull}
var a11yObserveValues = []string{observeDiff, observeTree, observeScreenshot, observeFull}
var a11yActObserveValues = []string{observeNone, observeDiff, observeScreenshot}

const a11yObserveArgumentHelp = "Returned evidence: diff (default current-state " +
	"changed semantic subtrees, with compact absent-ref lifecycle context), tree " +
	"(working AX), screenshot (target window), or full (tree and image)."

// appArgumentHelp describes the app argument shared by every tool that accepts
// one. Accessibility app names mix display names ("Google Chrome"), binary
// names ("gedit") and app ids ("org.gnome.Nautilus"), and no rule derives one
// from another, so the argument accepts any of these or a PID.
const appArgumentHelp = "Bootstrap app/process name, app id, or PID as a decimal string; " +
	"never a window title. Prefer returned aN/wN/rN refs."

// toolChannel maps each public tool to the channel that enables it. Tools
// outside the map belong to no channel and are always enabled.
var toolChannel = map[string]string{
	publicToolAX:  "a11y",
	publicToolGUI: "gui",
}

// actionTools are the tools that change the UI and therefore produce an
// observation as a by-product. The observe parameter applies only to them; for
// read-only tools the observation is the return value itself.
var actionTools = map[string]bool{
	publicToolAX:  true,
	publicToolGUI: true,
}

func isActionTool(name string) bool { return actionTools[name] }

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

func a11yStructuredSelectorProperty() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": "Exact AX identity fields, ANDed in one window.",
		"properties": map[string]any{
			"role": map[string]any{
				"type":        "string",
				"pattern":     selectorRolePattern,
				"description": "Exact AX role.",
			},
			"name":        nonBlankStringProperty("Exact accessible name."),
			"description": nonBlankStringProperty("Exact accessible description."),
			"placeholder": nonBlankStringProperty("Exact accessible placeholder."),
			"state": enumStringProperty(
				"Optional distinguishing state.",
				[]string{"checked", "expanded", "selected", "focused"},
			),
		},
		"minProperties":        1,
		"additionalProperties": false,
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
						"pattern":   nonBlankPattern,
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
				"text": func() map[string]any {
					text := boundedStringProperty("Literal text to type.", maxActionText)
					text["pattern"] = nonBlankPattern
					return text
				}(),
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
			nonEmptyFilter("role"),
			nonEmptyFilter("name_contains"),
			nonEmptyFilter("name_equals"),
			nonEmptyFilter("text_contains"),
			nonEmptyFilter("text_equals"),
			nonEmptyFilter("state"),
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
	windowText := nonBlankStringProperty(
		"Case-insensitive title substring for a current top-level window owned by the resolved application.",
	)
	return map[string]any{
		"description": "Poll exactly one typed AX-visible predicate.",
		"oneOf": []any{
			elementBranch("exists", map[string]any{
				"value": booleanProperty("Whether the target must exist."),
			}, []string{"value"}),
			elementBranch("state", map[string]any{
				"state": nonBlankStringProperty("Accessibility state name, such as checked, focused, selected, or expanded."),
				"value": booleanProperty("Whether the state must be present."),
			}, []string{"state", "value"}),
			func() map[string]any {
				branch := elementBranch("value", map[string]any{
					"value": map[string]any{
						"type":        []string{"string", "number", "boolean"},
						"pattern":     nonBlankPattern,
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
				"text": nonBlankStringProperty("Required substring across accessible name, description, value, or placeholder."),
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
	// A root is read-only, like an assertion target: rN only, and an index
	// addresses the snapshot this observe reads.
	target := a11yAssertionElementTargetProperty()
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
			"assertion":     a11yAssertionProperty(),
			"app":           nonBlankStringProperty(appArgumentHelp),
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
				"Full-desktop action batch; screenshot alone observes. "+
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

func stringProperty(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

// nonBlankPattern requires at least one non-whitespace character. The AX
// runtime compares these strings after trimming, so a blank value can match
// nothing.
const nonBlankPattern = `\S`

// selectorRolePattern is a non-blank role without [, ] or ". The role is the
// unquoted head of the internal selector grammar, so those characters would
// be read as predicates or a name.
const selectorRolePattern = `^[^\["\]]*[^\s\["\]][^\["\]]*$`

func nonBlankStringProperty(description string) map[string]any {
	property := stringProperty(description)
	property["pattern"] = nonBlankPattern
	return property
}

// nonEmptyFilter is one anyOf alternative of a find query: the filter is
// present and not empty.
func nonEmptyFilter(field string) map[string]any {
	return map[string]any{
		"required":   []string{field},
		"properties": map[string]any{field: map[string]any{"minLength": 1}},
	}
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

func booleanProperty(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}
