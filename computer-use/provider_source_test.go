//go:build linux

package main

// The tests in this file search the embedded Linux provider source for
// contract fragments: model-visible wording, request fields shared between Go
// and Python, and guards against removed behavior. They cannot see whether the
// code runs; prefer a runtime test under runtime_tests/ when the behavior can
// be exercised directly.

import (
	"reflect"
	"strings"
	"testing"
)

func TestConfirmationNotesStateTheirLimit(t *testing.T) {
	// "confirmed it applied" over-claims: readback proves that the control's
	// value changed, not that the application adopted it. Dialogs such as a
	// line-spacing combo box keep control state separate from document state
	// and commit only on OK/Apply.
	for _, fragment := range []string{
		"CONTROL changed, not that the application adopted the value",
		"require OK/Apply before the value takes effect",
	} {
		if !strings.Contains(linuxRuntimeScript, fragment) {
			t.Fatalf("confirmation notes must state what they do NOT prove: missing %q", fragment)
		}
	}
	if strings.Contains(linuxRuntimeScript, "confirmed it applied.") {
		t.Fatal("the old over-claiming wording must not come back")
	}
}

func TestDecisionRelevantStatesAreRendered(t *testing.T) {
	// Disabled, selected, expanded and checked states must appear in the tree;
	// without them the agent keeps clicking disabled controls and cannot tell
	// whether a drop-down or menu is open.
	for _, fragment := range []string{
		// Assert only that the function exists, not its signature, which
		// changes as parameters are added or removed.
		"def state_segment(",
		"NOTABLE_STATES",
	} {
		if !strings.Contains(linuxRuntimeScript, fragment) {
			t.Fatalf("tree must expose decision-relevant states: missing %q", fragment)
		}
	}
	// Render only the non-default side: marking every node "enabled" would
	// drown the signal.
	if strings.Contains(linuxRuntimeScript, `("ENABLED", "enabled")`) {
		t.Fatal("rendering the default side of every state would drown the signal")
	}
	// Never infer semantics from an absent state: Nautilus file icons lack
	// ENABLED/SENSITIVE yet are fully operable, and marking them disabled would
	// make the agent skip real targets.
	if strings.Contains(linuxRuntimeScript, `marks.append("disabled")`) {
		t.Fatal("inferring 'disabled' from a missing ENABLED state produces false positives on Linux")
	}
}

func TestTruncationIsPrioritisedAndVisible(t *testing.T) {
	// Depth-first truncation drops nodes arbitrarily in traversal order: early
	// subtrees fill the quota, later ones vanish entirely, and the agent never
	// learns that the tree was cut.
	for _, fragment := range []string{
		"BUDGET_PRESSURE_RATIO",
		"def is_structural_filler(record):",
		"node(s) omitted —",
		"server-owned node budget filled up",
	} {
		if !strings.Contains(linuxRuntimeScript, fragment) {
			t.Fatalf("truncation must be prioritised and reported: missing %q", fragment)
		}
	}
	if !strings.Contains(linuxRuntimeScript, "not interactable and/or not on screen") {
		t.Fatal("the omission notice must say what was dropped, not just how many")
	}
}

// TestObservationChannelsAreComplementary checks that the instructions present
// the accessibility tree and screenshots as complementary, not as primary and
// fallback. Some effective actions, such as saving a document, leave the
// accessibility tree byte-identical, which a screenshot resolves at a glance.
// Conversely, telling unnamed spin buttons apart is guesswork from pixels but
// deterministic from tree descriptions. The tree provides actionability and
// the screenshot observability; visual change still needs its own assertion.
// COMPUTER_USE_A11Y_SCREENSHOTS=0 disables target screenshots so the two
// setups can be compared.
func TestObservationChannelsAreComplementary(t *testing.T) {
	gui := findToolDefinition(t, "GUI")
	if !strings.Contains(gui.Description, "Full-desktop") {
		t.Fatal("gui must expose the independent full-desktop track")
	}
	if !strings.Contains(serverInstructions, "two complementary tools") {
		t.Fatal("server instructions must present the two channels as complementary, not as primary and fallback")
	}
	for _, fragment := range []string{
		"AX is app-scoped semantic observation and action",
		"GUI is full-desktop visual observation and absolute-pixel input",
		"evidence, not proof",
	} {
		if !strings.Contains(serverInstructions, fragment) {
			t.Fatalf("complementary evidence boundary missing %q", fragment)
		}
	}
	if !strings.Contains(linuxRuntimeScript, "def a11y_screenshots_enabled():") {
		t.Fatal("screenshot policy must be decided by one switchable predicate so both setups can be compared")
	}
	if !strings.Contains(linuxRuntimeScript, "Every call") &&
		!strings.Contains(gui.Description, "returns one final screenshot") {
		t.Fatal("gui must always return a screenshot")
	}
}

func TestLinuxClickMethodSafetyAndPlatformSupport(t *testing.T) {
	service := newService()

	result := service.click("Text Editor", "7", "", nil, nil, 1, "left", "app_post", observation{}, settlePlan{})
	if !result.IsError || result.Content[0].Text != "click_method 'app_post' is not supported on Linux" {
		t.Fatalf("app_post click result = %#v", result)
	}

	result = service.click("Text Editor", "7", "", nil, nil, 1, "left", "sky_click", observation{}, settlePlan{})
	if !result.IsError || result.Content[0].Text != "click_method 'sky_click' is not supported on Linux" {
		t.Fatalf("sky_click result = %#v", result)
	}

	// click rejects x/y; coordinate clicks belong to the GUI tool.
	x, y := 10.0, 20.0
	result = service.click("Text Editor", "7", "", &x, &y, 1, "left", "auto", observation{}, settlePlan{})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "use GUI") {
		t.Fatalf("click with x/y must point to GUI: %#v", result)
	}
	result = service.click("Text Editor", "", "", nil, nil, 1, "left", "auto", observation{}, settlePlan{})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "click requires target.ref") {
		t.Fatalf("click without target.ref must fail with an explicit error: %#v", result)
	}

	// GUI uses absolute coordinates of the full-desktop screenshot: they are
	// sent unchanged, rejected when out of bounds, and never offset by a
	// window origin or silently adjusted.
	for _, fragment := range []string{
		"def screen_absolute_point(",
		"is outside the full desktop",
		"normalized[\"x\"], normalized[\"y\"] = screen_absolute_point(",
	} {
		if !strings.Contains(linuxRuntimeScript, fragment) {
			t.Fatalf("GUI channel must validate and use absolute screen coordinates unchanged: missing %q", fragment)
		}
	}
}

func TestLinuxRuntimeDocumentsATSPIAndFallbackBoundary(t *testing.T) {
	if !strings.Contains(linuxRuntimeScript, "Atspi") {
		t.Fatal("Linux runtime must use AT-SPI")
	}
	if !strings.Contains(linuxRuntimeScript, "generate_mouse_event") {
		t.Fatal("Linux runtime should keep coordinate input explicit and visible in the bridge")
	}
	if !strings.Contains(
		serverInstructions,
		"AX keyboard/pointer fallbacks use global desktop focus, not background input",
	) {
		t.Fatal("MCP instructions must document the Linux background-input boundary")
	}
}

func TestLinuxRuntimeTextLimitSupportsMaxMode(t *testing.T) {
	if !strings.Contains(linuxRuntimeScript, "DEFAULT_TEXT_LIMIT = 500") {
		t.Fatal("Linux runtime should define the shared 500 character text limit")
	}
	if !strings.Contains(linuxRuntimeScript, "text_limit=parse_text_limit(observation.get(\"text_limit\"), DEFAULT_TEXT_LIMIT)") {
		t.Fatal("Linux observe should pass text_limit into snapshot rendering")
	}
	if !strings.Contains(linuxRuntimeScript, "if isinstance(value, str) and value.lower() == \"max\"") {
		t.Fatal("Linux runtime should support max text limit mode")
	}
	if !strings.Contains(linuxRuntimeScript, "max_tree_nodes=positive_int(observation.get(\"max_tree_nodes\"), MAX_ELEMENTS)") {
		t.Fatal("Linux observe should pass max_tree_nodes into snapshot rendering")
	}
	if !strings.Contains(linuxRuntimeScript, "max_tree_depth=positive_int(observation.get(\"max_tree_depth\"), MAX_DEPTH)") {
		t.Fatal("Linux observe should pass max_tree_depth into snapshot rendering")
	}
	if !strings.Contains(linuxRuntimeScript, "text_limit + 1") {
		t.Fatal("Linux default truncation should read one extra character so it can append ellipsis")
	}
}

func TestLinuxRuntimeTreeBudgetDefaultsMatchMacOS(t *testing.T) {
	if !strings.Contains(linuxRuntimeScript, "MAX_ELEMENTS = 1200") {
		t.Fatal("Linux runtime should default to the shared 1200 node tree budget")
	}
	if !strings.Contains(linuxRuntimeScript, "MAX_DEPTH = 64") {
		t.Fatal("Linux runtime should default to the shared 64 level tree depth")
	}
}

func TestLinuxRuntimeSelectsEditableTargetByState(t *testing.T) {
	if !strings.Contains(linuxRuntimeScript, "state_contains(node, Atspi.StateType.EDITABLE)") {
		t.Fatal("Linux runtime must require the EDITABLE state when choosing a text target; interface presence alone matches hidden placeholder widgets")
	}
	if !strings.Contains(linuxRuntimeScript, "if len(focused) == 1:") {
		t.Fatal("Linux runtime should choose exactly one focused editable target")
	}
	if strings.Contains(linuxRuntimeScript, "if len(showing) == 1:") {
		t.Fatal("Linux runtime must not treat visibility as proof of input focus")
	}
	if strings.Contains(linuxRuntimeScript, "return find_first(root, is_editable)") {
		t.Fatal("Linux runtime must not fall back to the first editable-text interface in tree order")
	}
}

func TestLinuxRuntimeVerifiesTextInsertionLanded(t *testing.T) {
	if !strings.Contains(linuxRuntimeScript, "after_text = read_all_text(text_iface)") {
		t.Fatal("Linux runtime should re-read the complete text after insert_text")
	}
	if !strings.Contains(linuxRuntimeScript, "if after_text == expected:") ||
		!strings.Contains(
			linuxRuntimeScript,
			`return "effect_unknown", before, len(after_text), True`,
		) {
		t.Fatal("insert_text success and character growth are insufficient; the exact inserted payload must be confirmed")
	}
	if !strings.Contains(linuxRuntimeScript, "if after == payload:") ||
		!strings.Contains(
			linuxRuntimeScript,
			`if after != before:`+"\n"+`                return "effect_unknown"`,
		) {
		t.Fatal("set_value must confirm the requested value exactly instead of accepting any change")
	}
}

func TestLinuxRuntimeTypesAtCaretAndReplacesSelection(t *testing.T) {
	if !strings.Contains(linuxRuntimeScript, "def text_insertion_point(text_iface):") {
		t.Fatal("Linux runtime should resolve an insertion point instead of always appending")
	}
	if !strings.Contains(linuxRuntimeScript, "Atspi.Text.get_caret_offset(text_iface)") {
		t.Fatal("type_text should insert at the caret so it behaves like real typing")
	}
	if !strings.Contains(linuxRuntimeScript, "Atspi.EditableText.delete_text(") ||
		!strings.Contains(linuxRuntimeScript, "editable, selection[0], selection[1]") {
		t.Fatal("type_text should replace a non-empty selection, the way typing does")
	}
}

func TestLinuxRuntimeRejectsOffscreenSentinelCoordinates(t *testing.T) {
	if !strings.Contains(linuxRuntimeScript, "abs(rect.x) > MAX_SANE_EXTENT or abs(rect.y) > MAX_SANE_EXTENT") {
		t.Fatal("unrendered widgets report INT_MIN origins; coordinate actions must not target them")
	}
	if !strings.Contains(linuxRuntimeScript, "MAX_SANE_EXTENT = 100000") {
		t.Fatal("Linux runtime should define the sane extent bound shared by size and origin checks")
	}
}

func TestLinuxRuntimeReportsExecutionPath(t *testing.T) {
	if !strings.Contains(linuxRuntimeScript, "UNVERIFIED_SYNTHESIS") {
		t.Fatal("Linux runtime should mark synthesis-based actions as unverified")
	}
	// A semantic call is not evidence of effect either: AT-SPI actions in apps
	// such as Nautilus, GIMP and VLC can report success and do nothing. The tool
	// must say so, or the agent moves on while the UI has not changed.
	if !strings.Contains(linuxRuntimeScript, "UNVERIFIED_SEMANTIC") {
		t.Fatal("Linux runtime should mark semantic AT-SPI actions as unverified too")
	}
	if !strings.Contains(linuxRuntimeScript, "that is not evidence the action took effect") {
		t.Fatal("semantic action notes must state that toolkit acceptance is not proof of effect")
	}
	for _, note := range []string{
		"Invoked the element's AT-SPI accessibility action. ",
		"Wrote the text through the AT-SPI editable-text API and read it back ",
		"fell back to ",
	} {
		if !strings.Contains(linuxRuntimeScript, note) {
			t.Fatalf("action notes must distinguish verified from best-effort paths: missing %q", note)
		}
	}
	if !strings.Contains(linuxRuntimeScript, "response[\"notes\"] = notes") {
		t.Fatal("Linux runtime should return the action notes to the Go bridge")
	}
}

func TestContentPlaneDetectionIsStructuralNotApplicationSpecific(t *testing.T) {
	// A tree that is structurally complete but lacks the target content must
	// raise a diagnostic. The criterion can only rest on role semantics, since
	// embedded content hosts span browsers, Electron, PDF viewers and webviews.
	for _, fragment := range []string{
		"def content_host_diagnostic(records):",
		"CONTENT_PLANE_ROLES",
		"EMBEDDED_CONTAINER_ROLES",
		"CONTENT PLANE UNCERTAIN",
	} {
		if !strings.Contains(linuxRuntimeScript, fragment) {
			t.Fatalf("content-plane detection missing %q", fragment)
		}
	}
	// The diagnostic must point to the other channel, or the caller has no way
	// forward.
	if !strings.Contains(linuxRuntimeScript, "Inspect the full-desktop screenshot") {
		t.Fatal("the uncertain diagnostic must direct the caller to visual evidence")
	}
}

func TestForegroundAlarmsAreFilteredAndGraded(t *testing.T) {
	// Foreground alarms must be filtered and graded: most windows that appear
	// to take the foreground are untitled pseudo-windows with no content, so
	// they are filtered out and the remaining alarm is worded as uncertain.
	for _, fragment := range []string{
		"def is_infrastructure_window(window):",
		"def accepts_user_input(app):",
		"if is_infrastructure_window(window):",
		"ANOTHER WINDOW MAY BE IN FRONT",
		"appNotUsable",
	} {
		if !strings.Contains(linuxRuntimeScript, fragment) {
			t.Fatalf("alarm filtering/grading missing %q", fragment)
		}
	}
	// Detection must rely on window shape, not a process-name allowlist, which
	// breaks on any other desktop environment.
	for _, fitted := range []string{`"xfdesktop"`, `"xfwm4"`, `"mutter"`, `"gnome-shell"`} {
		if strings.Contains(linuxRuntimeScript, "if "+fitted) {
			t.Fatalf("infrastructure detection must not branch on a process name: %s", fitted)
		}
	}
}

func TestLinuxRuntimeGuardsGlobalInputSynthesis(t *testing.T) {
	if !strings.Contains(linuxRuntimeScript, "def require_window_focus(window, what):") {
		t.Fatal("Linux runtime needs a focus guard before global input synthesis")
	}
	for _, guarded := range []string{
		"require_window_focus(window, \"keypress\")",
		"require_window_focus(window, \"type\")",
		"require_window_focus(window, \"scroll\")",
		"require_coordinate_delivery(window, x, y, \"click\")",
	} {
		if !strings.Contains(linuxRuntimeScript, guarded) {
			t.Fatalf("global synthesis path is unguarded: missing %s", guarded)
		}
	}
	if !strings.Contains(linuxRuntimeScript, "Refusing to synthesize") {
		t.Fatal("Linux runtime should fail loudly rather than deliver input to whichever window holds focus")
	}
	if strings.Contains(linuxRuntimeScript, `require_window_focus(window, "gui")`) {
		t.Fatal("gui must preserve the observed desktop stacking before absolute input")
	}
}

func TestLinuxRuntimeFocusesWindowThroughFocusableChild(t *testing.T) {
	if !strings.Contains(linuxRuntimeScript, "Atspi.StateType.FOCUSABLE") {
		t.Fatal("Linux runtime should look for a FOCUSABLE child; grab_focus on a GTK frame always fails")
	}
	if !strings.Contains(linuxRuntimeScript, "FOCUS_GRAB_CANDIDATES") {
		t.Fatal("Linux runtime should cap focus-grab attempts because grabbing moves real focus inside the app")
	}
	if !strings.Contains(linuxRuntimeScript, "Atspi.StateType.ACTIVE") {
		t.Fatal("Linux runtime should use the window ACTIVE state to decide whether synthesis is safe")
	}
}

func TestSourceDoesNotReplaySuccessfulSemanticClicks(t *testing.T) {
	source := mainGoSource(t)
	for _, forbidden := range []string{
		"retried the same element as a coordinate click",
		"shouldRetryWithSynthesis",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("a successful semantic click must not be replayed because its effect went unobserved: %q", forbidden)
		}
	}
}

// TestTreeRenderingReadsActionTableOnce checks that rendering a node queries
// its action table only once. Repeated queries make LibreOffice's ATK bridge
// log
//
//	(soffice): CRITICAL: impl_get_NActions: assertion 'ATK_IS_ACTION (user_data)' failed
//
// and under heavy use the application exits altogether. Deriving the
// [has-click-action] mark from a second query (record_for -> state_segment ->
// preferred_action_index) would double the per-node queries on a bridge that
// is known to be fragile.
func TestTreeRenderingReadsActionTableOnce(t *testing.T) {
	if !strings.Contains(linuxRuntimeScript, "def node_actions(") {
		t.Fatal("the action table must be read through a single entry point, node_actions()")
	}
	// record_for must not read the action table a second time.
	if strings.Contains(linuxRuntimeScript, "state_segment(node)\n") {
		t.Fatal("state_segment must not query the action table again; record_for must pass the mark in")
	}
	if !strings.Contains(linuxRuntimeScript, "actions, has_click_action = node_actions(node)") {
		t.Fatal("record_for must read the action table once and share it between both fields")
	}
}

// TestSynthesisOnlyToolsConfirmOnlyWindowFocus checks the limits of
// require_window_focus: before synthesis it only confirms that the target
// application's window is active. It neither identifies the focused control
// inside that window nor proves delivery to a specific control.
func TestSynthesisOnlyToolsConfirmOnlyWindowFocus(t *testing.T) {
	for _, tool := range []string{"keypress", "scroll"} {
		request := axServiceRequest{}
		switch tool {
		case "keypress":
			request.Action = axKeypressCommand{Key: "ENTER"}
		case "scroll":
			request.Action = axScrollCommand{Direction: "down", Pages: 1}
		}
		if !windowFocusWasConfirmed(request) {
			t.Fatalf("%s uses require_window_focus, so window focus should be confirmed", tool)
		}
	}
	if windowFocusWasConfirmed(axServiceRequest{Action: axClickCommand{}}) {
		t.Fatal("click may use the semantic channel and must not claim confirmed window focus")
	}
	if !strings.Contains(
		findToolDefinition(t, "AX").Description,
		"resolve live",
	) {
		t.Fatal("instructions should still steer toward element-targeted actions first")
	}
	if !strings.Contains(linuxRuntimeScript, "Delivery to the intended target was not verified") {
		t.Fatal("synthesis channel should still declare control-level delivery uncertainty")
	}
}

func TestOrdinaryTreesDoNotPaySemanticRefMarkupCost(t *testing.T) {
	source := mainGoSource(t)
	if strings.Contains(source, "decorateSemanticRefs(response.Snapshot)") {
		t.Fatal("ordinary get_app_state snapshots must not decorate every tree line with refs")
	}
	if !strings.Contains(source, "refRegistry") {
		t.Fatal("short refs must be owned by the service-level registry")
	}
}

func TestCoordinateNotesDeclareTheirExplicitSpace(t *testing.T) {
	// Coordinates in notes must use the space they declare. Reporting the
	// absolute click point next to window-relative element bounds is off by
	// exactly the window origin. The AX element fallback converts the absolute
	// point back to window-relative coordinates before reporting; GUI uses
	// screen_absolute_point and does not share the window-offset convention.
	if !strings.Contains(linuxRuntimeScript, "def window_relative(") {
		t.Fatal("runtime needs a function that converts absolute coordinates back to window-relative ones")
	}
	for _, fragment := range []string{
		// The AX element coordinate fallback stays window-relative.
		"coordinate click(s) at ({:.0f}, {:.0f}) in ",
		"window-relative pixels after bringing the window to the foreground",
	} {
		if !strings.Contains(linuxRuntimeScript, fragment) {
			t.Fatalf("a11y coordinate note must declare the window-relative space: missing %q", fragment)
		}
	}
	for _, fragment := range []string{
		"def screen_absolute_point(",
		"def validate_gui_action(action, bounds=None, native_bounds=None):",
		"def execute_gui_action(action):",
		"screen_absolute_point(",
	} {
		if !strings.Contains(linuxRuntimeScript, fragment) {
			t.Fatalf("GUI coordinates must be absolute screen coordinates without implicit offsets: missing %q", fragment)
		}
	}
	// Every reported coordinate must go through the conversion instead of
	// printing screen_point's return value directly.
	if strings.Contains(linuxRuntimeScript, `"{}".format(action, x, y, UNVERIFIED_SYNTHESIS)`) {
		t.Fatal("menu fallback still prints absolute screen coordinates directly")
	}
}

func TestRuntimeTimeoutSkipsRecursiveErrorContext(t *testing.T) {
	source := mainGoSource(t)
	if !strings.Contains(source, `strings.Contains(message, "runtime_timeout=true")`) {
		t.Fatal("a11y timeout errors must skip a second get_app_state error-context capture")
	}
}

func TestGUIFailureInvalidationUsesReportedFacts(t *testing.T) {
	source := mainGoSource(t)
	if strings.Contains(source, "if !response.OK && guiBatchCanEmitInput(actions)") {
		t.Fatal("pre-emission GUI rejection must not invalidate snapshots merely because the requested batch could have emitted input")
	}
	if !strings.Contains(source, "providerMutationCanInvalidate(response.Mutation)") {
		t.Fatal("GUI invalidation must use runtime-reported input/partial/desktop mutation facts")
	}
}

func TestSnapshotProjectionIsPreservedAcrossActionAndSettleReads(t *testing.T) {
	prune := false
	boxes := true
	projection := snapshotProjection{
		TextLimit: "max", MaxTreeNodes: 321, MaxTreeDepth: 17,
		Prune: &prune, Boxes: &boxes,
	}
	request := axServiceRequest{Action: axClickCommand{}}
	applyProjection(&request, projection)
	if request.TextLimit != "max" || request.MaxTreeNodes != 321 ||
		request.MaxTreeDepth != 17 || request.Prune == nil || *request.Prune ||
		request.Boxes == nil || !*request.Boxes {
		t.Fatalf("projection was not preserved: %#v", request)
	}
	settle := settleReadRequest("Editor", nil, "window", projection)
	if settle.MaxTreeNodes != 321 || settle.MaxTreeDepth != 17 ||
		settle.Prune == nil || *settle.Prune {
		t.Fatalf("settle read lost the baseline projection: %#v", settle)
	}
	for _, fragment := range []string{
		`observation.get("max_tree_nodes")`,
		`observation.get("max_tree_depth")`,
		`observation.get("prune", True)`,
	} {
		if !strings.Contains(linuxRuntimeScript, fragment) {
			t.Fatalf("runtime action readback must preserve projection field %q", fragment)
		}
	}
}

func TestRootWindowChangeDropsOldStableReferences(t *testing.T) {
	source := mainGoSource(t)
	for _, fragment := range []string{
		"!accessibilityOperationCanMutate(request)",
		"previous.WindowHandle != response.Snapshot.WindowHandle",
		"request.KnownRefs = nil",
		"response, err = s.callProviderAccessibilityOperation(request)",
	} {
		if !strings.Contains(source, fragment) {
			t.Fatalf("root-window changes must force one fresh numbering pass: missing %q", fragment)
		}
	}
}

func TestGUIValidationRejectsBeforeFreshScreenshot(t *testing.T) {
	field, ok := reflect.TypeOf(ProviderDesktopRequest{}).FieldByName("Actions")
	if !ok || field.Type != reflect.TypeOf([]ProviderDesktopAction{}) {
		t.Fatal("ProviderDesktopRequest must carry typed GUI actions")
	}
	if !strings.Contains(linuxRuntimeScript, "no screenshot was captured") {
		t.Fatal("runtime preflight must avoid fresh screenshots for invalid batches")
	}
}

func TestProviderSpecificSecondaryActionIsNotReachable(t *testing.T) {
	for _, source := range []string{linuxRuntimeScript, mainGoSource(t)} {
		for _, forbidden := range []string{
			`invoke_element_action`,
			`invoke_secondary_action`,
			`CONTEXT_MENU_ACTIONS`,
		} {
			if strings.Contains(source, forbidden) {
				t.Fatalf("provider-specific secondary action leaked into production: %q", forbidden)
			}
		}
	}
}

func TestActionsReResolveGeometryAtActionTime(t *testing.T) {
	// Like a Playwright locator, the target is re-resolved at action time.
	// find_element_at_snapshot_path already resolves the live node, so
	// computing coordinates from the cached Go record would mix two versions
	// of the UI in one call: after a sidebar collapses, the click would land
	// at the centre of the stale cached box even though the layout has
	// changed.
	if !strings.Contains(linuxRuntimeScript, "def current_geometry(") {
		t.Fatal("action path must re-read geometry after resolving the live node")
	}
	if !strings.Contains(linuxRuntimeScript, "element_record, moved_note = current_geometry(") {
		t.Fatal("recomputed geometry must be used by the action, not discarded")
	}
	// A move must be reported: it is the agent's only way to know whether the
	// coordinates it read are still valid.
	if !strings.Contains(linuxRuntimeScript, "The element MOVED since the snapshot") {
		t.Fatal("a moved element must be reported, or the agent keeps using stale snapshot coordinates")
	}
	// All element-targeted paths share resolveActionElement. A uniquely named
	// current index is promoted to a same-window semantic target so the runtime
	// uses live geometry. Ambiguous or unnamed records retain strict cached-path
	// resolution.
	source := mainGoSource(t)
	for _, tool := range []string{
		`"click"`,
		`"scroll"`,
		`"set_value"`,
	} {
		if !strings.Contains(source, "resolveActionElement(") ||
			!strings.Contains(source, tool) {
			t.Fatalf("element-targeted action %s does not use the shared resolver", tool)
		}
	}
	if !strings.Contains(source, "func applyActionElementResolution(") ||
		!strings.Contains(source, "resolution.element != nil && resolution.snapshot != nil") ||
		!strings.Contains(source, "target.SourceIndex = &sourceIndex") {
		t.Fatal("target resolution must distinguish promoted semantic indices from strict path indices")
	}
}

func TestStabilityWaitPortsOnlyWhatLinuxCanSupport(t *testing.T) {
	// Before an action Playwright waits for visible, enabled and stable, plus a
	// hit test. Only stable is ported; enabled and the hit test fail on Linux:
	//   enabled  - Nautilus file icons never set ENABLED/SENSITIVE (only
	//              SHOWING/VISIBLE/FOCUSABLE) yet expose open and menu actions
	//              and are fully clickable; gating on it skips usable targets.
	//   hit test - measured hit rates were 11/11 in gedit, 19/25 in Nautilus
	//              and 12/25 in LibreOffice, so gating on it would block about
	//              half of the valid clicks in LibreOffice.
	if !strings.Contains(linuxRuntimeScript, "def wait_until_stable(") {
		t.Fatal("actions must wait for the element to stop moving")
	}
	if !strings.Contains(linuxRuntimeScript, "target_unstable") {
		t.Fatal("an unstable target must be reported as target_unstable")
	}
	// The coordinate fallback must refuse while the target is unstable;
	// semantic actions do not depend on geometry and skip this gate.
	if !strings.Contains(linuxRuntimeScript, "require_stable_coordinate_geometry") {
		t.Fatal("coordinate actions must require stable geometry")
	}
	// The limit is configurable: animation-heavy apps need longer, and
	// latency-sensitive setups must be able to turn the wait off.
	if !strings.Contains(linuxRuntimeScript, "COMPUTER_USE_STABLE_TIMEOUT_MS") {
		t.Fatal("the stability wait limit must be configurable")
	}
}

func TestActionFailureRequiresExplicitObservationBeforeAnotherAction(t *testing.T) {
	source := mainGoSource(t)
	for _, fragment := range []string{
		"return s.recoverNoInputPreconditionObservation(",
		"s.pendingFailedObservations = append(",
		"s.hasPendingFailedObservation(app)",
		`result.StructuredContent["observation_required"] == true`,
		"The requested action was not executed",
	} {
		if !strings.Contains(source, fragment) {
			t.Fatalf("explicit stale recovery wiring is missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"inlineStaleRecovery",
		"fresh_state_inlined",
		"fresh_snapshot_id",
		"fresh_window_handle",
		"recoveryCandidateLines",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("stale failure still exposes implicit fresh state through %q", forbidden)
		}
	}
	if got := strings.Count(source, "s.consumeFailedObservation(app, snapshot)"); got != 2 {
		t.Fatalf("only explicit get_app_state and find should consume stale continuity, got %d call sites", got)
	}
}

func TestUnnamedWindowsStillGetAUsableLabel(t *testing.T) {
	// Many menus, drop-downs and popups have no name, which renders as
	// `Window: ""`, yet the window title is the agent's first cue for where it
	// is. Unnamed windows therefore need a substitute label.
	if !strings.Contains(linuxRuntimeScript, "def window_label(") {
		t.Fatal("unnamed windows need a usable substitute label")
	}
	if !strings.Contains(linuxRuntimeScript, "(unnamed {} containing {!r})") {
		t.Fatal("the substitute label should include the first named child; the first item " +
			"of an unnamed menu is often enough to identify the menu")
	}
}

func TestComboBoxSelectionIsVisibleInTheTree(t *testing.T) {
	// Combo boxes such as Margins, Destination and Scale in Chrome's print
	// dialog expose only a name, so the agent can neither decide whether to
	// change the value nor confirm a change.
	//
	// The value is not on the Value interface (Chrome's combo boxes do not
	// implement it) nor on the Text interface (it reads back as an object
	// replacement character). It is the SELECTED state of a descendant menu
	// item, which is readable even while the drop-down is closed, so no
	// drop-down has to be expanded first.
	if !strings.Contains(linuxRuntimeScript, "def selected_option_name(") {
		t.Fatal("the current selection of a combo box must be readable")
	}
	if !strings.Contains(linuxRuntimeScript, `role in ("combo box", "list box")`) {
		t.Fatal("selection scan must run only for combo and list box roles so other roles do not pay for it")
	}
}

func TestFocusGuardHasAnX11FallbackForWindowsThatNeverGoActive(t *testing.T) {
	// The GNOME portal file dialog, used for every Save As and Open, reports
	// MODAL+VISIBLE but neither ACTIVE nor SHOWING, and so does its secondary
	// confirmation ("file exists, replace?"), which holds input focus. A focus
	// guard that trusts only ACTIVE would make synthesized input unusable on
	// these dialogs.
	if !strings.Contains(linuxRuntimeScript, "def x11_holds_focus(") {
		t.Fatal("focus guard needs X11 corroboration when AT-SPI cannot tell")
	}
	// The criterion must be the owning process: the secondary dialog has no
	// title and is not the target window itself.
	if !strings.Contains(linuxRuntimeScript, "_NET_WM_PID") {
		t.Fatal("X11 check must compare the PID, not the title; the blocking dialog has no title")
	}
	// The X11 check is only a fallback and must not replace the AT-SPI check.
	shim := strings.Index(linuxRuntimeScript, "def x11_holds_focus(")
	active := strings.Index(linuxRuntimeScript, "if window_is_active(window):")
	if active < 0 || shim < 0 {
		t.Fatal("the AT-SPI ACTIVE check must stay in front of the X11 fallback")
	}
}

func TestForeignForegroundAppIsCalledOutByName(t *testing.T) {
	// A Save dialog opened from Chrome belongs to xdg-desktop-portal-gnome, a
	// separate process, so observing Chrome never shows it and the agent would
	// conclude that clicking Save did nothing.
	if !strings.Contains(linuxRuntimeScript, "ANOTHER APP IS IN FRONT") {
		t.Fatal("a foreground window owned by another process must be called out by name")
	}
	if !strings.Contains(linuxRuntimeScript, "Call AX mode=observe with app=") {
		t.Fatal("saying another app is in front is not enough; the note must name the app to observe")
	}
}

func TestSetValueFallsBackToSynthesisWhenTheApiRefuses(t *testing.T) {
	// Blink text inputs implement neither the AT-SPI Value nor the EditableText
	// interface, so set_value is refused on an entry that visibly holds a value,
	// such as the profile name in chrome://settings/manageProfile.
	//
	// Like click and type_text, set_value must fall back to synthesis when the
	// semantic path refuses; otherwise a ubiquitous control type is unusable.
	if !strings.Contains(linuxRuntimeScript, "The AT-SPI value API refused this element") {
		t.Fatal("a refused semantic set_value must fall back to focus, select all and type instead of failing")
	}
	// The fallback must state that it has no read-back confirmation. Only the
	// semantic path reads back, and giving both paths the same assurance would
	// mislead the agent.
	if !strings.Contains(linuxRuntimeScript, "no read-back ") {
		t.Fatal("fallback path must state that it has no read-back confirmation")
	}
}

func TestFocusGuardMustNotGiveFalsePositives(t *testing.T) {
	// While a GNOME portal dialog is open, focus can drift back to the app that
	// opened it, so text meant for the dialog is typed into that app.
	//
	// A false positive from the guard is far more dangerous than a false
	// refusal: a refusal shows the agent an error and lets it try another way,
	// while a false positive silently delivers input to another window.
	if !strings.Contains(linuxRuntimeScript, `["wmctrl", "-a", title]`) {
		t.Fatal("when taking focus fails, the window manager must be asked to activate the window again")
	}
	// A successful activation request does not mean focus arrived. It must be
	// re-checked, or the false positive merely moves elsewhere.
	after := linuxRuntimeScript[strings.Index(linuxRuntimeScript, `["wmctrl", "-a", title]`):]
	if !strings.Contains(after[:600], "window_is_active(window) or x11_holds_focus(window)") {
		t.Fatal("focus must be re-checked after the activation request instead of returning success directly")
	}
}

func TestX11ArbitratesWhichWindowIsInFrontWhenAtspiCannotTell(t *testing.T) {
	// The GNOME portal file dialog and its secondary confirmation ("file
	// exists, replace?") are two windows of the same application with
	// identical states: MODAL + VISIBLE, neither ACTIVE nor SHOWING. Taking the
	// first one in order always yields the file chooser, while the untitled
	// confirmation is the window in front that holds focus. The confirmation
	// is present in the accessibility tree; the root cause is picking the wrong
	// window, which X11 can arbitrate.
	if !strings.Contains(linuxRuntimeScript, "def x11_preferred_window(") {
		t.Fatal("X11 must arbitrate when AT-SPI cannot tell which window is in front")
	}
	// The criterion must be size. Position fails because GTK4 reports
	// accessibility extents with a (0,0) origin, and title fails because the
	// confirmation has none. After subtracting _GTK_FRAME_EXTENTS the X11 size
	// matches the accessibility size exactly, for example
	// 827x291 - (61,61,55,67) = 705x169.
	if !strings.Contains(linuxRuntimeScript, "_GTK_FRAME_EXTENTS") {
		t.Fatal("size comparison must first subtract GTK's invisible shadow frame")
	}
	// The candidate set must not depend on SHOWING: portal windows lack it, so
	// arbitrating only among `MODAL and SHOWING` windows would never run.
	if !strings.Contains(linuxRuntimeScript, "x11_preferred_window(windows)") {
		t.Fatal("arbitration must consider all windows, not only the modal+showing subset")
	}
	// Without a unique match the original order takes over; an ambiguous
	// criterion must not force a decision.
	if !strings.Contains(linuxRuntimeScript, "return matched[0] if len(matched) == 1 else None") {
		t.Fatal("arbitration must give up without a unique match")
	}
}

func TestAppNameMatchingIsSeparatorInsensitiveAndFailsWithCandidates(t *testing.T) {
	// App names must match regardless of separators: "google-chrome" is a
	// reasonable guess for an app registered as "Google Chrome". Strict
	// matching also teaches agents to spend a step listing apps before every
	// task instead of naming the app directly.
	if !strings.Contains(linuxRuntimeScript, "def loose(") {
		t.Fatal("app name matching must ignore separators: google-chrome should find Google Chrome")
	}
	// A failure must list candidates: a dead-end error costs the agent another
	// round, while an error with candidates gives it the right name at once.
	if !strings.Contains(linuxRuntimeScript,
		"Applications currently visible to the ") {
		t.Fatal("appNotFound must list the running applications")
	}
}

func TestTreeBudgetIsMeasuredInCharactersNotOnlyNodes(t *testing.T) {
	// MAX_ELEMENTS=1200 is a node budget, while MCP clients limit bytes. A page
	// such as chrome://flags fills all 1200 nodes at about 112 characters per
	// line (about 135,000 characters), and clients replace results above about
	// 100KB with a file pointer the agent may be unable to read. Node counts do
	// not bound bytes: 1200 short nodes are about 40KB, 1200 long ones 135KB.
	if !strings.Contains(linuxRuntimeScript, "MAX_RENDERED_CHARS") {
		t.Fatal("tree budget must include a character limit")
	}
	// The walk must stop while rendering; truncating afterwards wastes the cost
	// (about 8.45 ms per node in GIMP, so 800 wasted nodes take 6.7 seconds).
	if !strings.Contains(linuxRuntimeScript, `if budget["chars"] >= max_rendered_chars:`) {
		t.Fatal("character budget must apply during the walk, not by truncating afterwards")
	}
	// Omission reasons are reported separately. A generic notice suggests that
	// raising max_tree_nodes would show everything, but when the character
	// budget is exhausted that only makes the client drop the whole response.
	if !strings.Contains(linuxRuntimeScript, "character budget was exhausted") {
		t.Fatal("character budget exhaustion must be reported separately from the node budget")
	}
	// The hint must follow the cause. Pruned nodes are filtered on purpose, so
	// a generic "raise max_tree_nodes to see them" would bring none back, and
	// agents rarely tune such parameters anyway. When the node budget fills
	// up, the notice says it is a server-owned budget instead of asking the
	// caller to guess a parameter.
	if !strings.Contains(linuxRuntimeScript, "server-owned node budget filled up") {
		t.Fatal("node budget exhaustion must say the budget is server-owned instead of asking the caller to guess a parameter")
	}
	// Nodes hidden by pruning must point to the current AX recovery path, not
	// to removed parameters.
	if !strings.Contains(linuxRuntimeScript, "AX mode=find searches canonical records") {
		t.Fatal("nodes hidden by the working projection must point to find/root")
	}
	for _, removed := range []string{
		"raising max_tree_nodes will not",
		"Raising max_tree_nodes does NOT help here",
		"Pass prune=false",
	} {
		if strings.Contains(linuxRuntimeScript, removed) {
			t.Fatalf("model-visible diagnostics still advertise a removed interface: %q", removed)
		}
	}
}

func TestUniversalContextMenuActionIsNotAnAffordance(t *testing.T) {
	// Chrome attaches `showContextMenu` to every DOM node, since any element can
	// be right-clicked. Unless it is excluded, the "no actions" test in
	// is_structural_filler never holds and pruning stops working.
	if !strings.Contains(linuxRuntimeScript, "UNIVERSAL_ACTIONS") {
		t.Fatal("actions that every node has must be grouped separately and excluded")
	}
	if !strings.Contains(linuxRuntimeScript, `UNIVERSAL_ACTIONS = {"showcontextmenu"}`) {
		t.Fatal("showContextMenu must be in the universal action set")
	}
}

func TestVerifyFailureSaysWhatDidChange(t *testing.T) {
	// A verify timeout that only says the expected condition did not happen
	// leaves the agent unable to decide whether to keep waiting, so it retries
	// with ever longer timeouts. The failure must also say what changed in the
	// meantime, because each case calls for a different next step:
	//   window changed  -> the awaited element may be in another window
	//   tree changed    -> the UI is moving, but not as expected; change the check
	//   nothing changed -> AX saw no structural change; no claim is made about
	//                      the previous input
	for _, fragment := range []string{
		"Window changed", "Tree changed", "Nothing changed",
		"no AX-visible structural ",
		"whether a prior input was emitted",
	} {
		if !strings.Contains(mainGoSource(t), fragment) {
			t.Fatalf("verify failure must say whether the UI changed: missing %q", fragment)
		}
	}
	if strings.Contains(mainGoSource(t), "action before this probably did not land") {
		t.Fatal("unchanged A11y state must not be promoted into an action-delivery claim")
	}
}

func TestVerifyAmbiguityDoesNotOverclaimStateFailure(t *testing.T) {
	// A broad selector such as `label` can match many elements, so verify never
	// identifies a target. The timeout text must not then claim that the
	// requested state is false or that the prior action did not land; a fresh
	// snapshot may well show the requested element.
	src := mainGoSource(t)
	for _, fragment := range []string{
		"the selector remained ambiguous",
		"no unique assertion target was identified",
		"never identified one element to test",
		"This failure does not prove",
	} {
		if !strings.Contains(src, fragment) {
			t.Fatalf("ambiguous verify must fail without denying the UI state: missing %q", fragment)
		}
	}
}

func TestTreeWalkHasAWallClockDeadline(t *testing.T) {
	// Besides node count and depth, the tree walk needs a wall-clock deadline,
	// because per-node cost varies by an order of magnitude. A full record_for
	// in GIMP costs about 8.45 ms per node, so a 3162-node tree takes 38
	// seconds, beyond the 30-second Go timeout.
	//
	// Cheap probes before building records lower the usual cost, but only for
	// one cost distribution; the deadline is a backstop that holds for any.
	if !strings.Contains(linuxRuntimeScript, "TREE_DEADLINE_SECONDS") {
		t.Fatal("tree walk needs a wall-clock deadline")
	}
	// The root node is exempt: an empty tree has no addressable element and
	// cannot even tell the caller which window this is.
	if !strings.Contains(linuxRuntimeScript,
		"if deadline is not None and records and time.monotonic() >= deadline:") {
		t.Fatal("deadline check must exempt the root node, or the walk can return an empty tree")
	}
	// The three omission reasons are reported separately; suggesting a larger
	// max_tree_nodes is wrong advice when time ran out.
	if !strings.Contains(linuxRuntimeScript, "time budget exhausted") {
		t.Fatal("time budget exhaustion must be reported separately")
	}
}

func TestVerifyRedirectsTheDisappearanceIdiom(t *testing.T) {
	// Waiting for an old element to disappear (exists=false) is a poor proxy
	// for navigation: single-page apps often leave old nodes in the DOM, so the
	// condition never holds however long the agent waits. The tool knows this
	// and must say so instead of letting the agent keep raising the timeout.
	src := mainGoSource(t)
	for _, fragment := range []string{
		"is a poor proxy for ",
		"leave the old ",
		"Wait for something NEW instead",
	} {
		if !strings.Contains(src, fragment) {
			t.Fatalf("exists:false timeout must call the check a poor proxy: missing %q", fragment)
		}
	}
	// Say this only when the UI has not changed. If it changed, the agent is
	// waiting in the right direction and candidates are more useful; wrong
	// advice wastes more than no advice.
	if !strings.Contains(src, "snapshot.WindowTitle == firstWindow && len(snapshot.Elements) == firstCount") {
		t.Fatal("the hint must be limited to an unchanged UI")
	}
}

func TestScrollNoteDoesNotClaimTargetingItDidNotDo(t *testing.T) {
	// With the scroll target optional, a note such as "the position came from
	// element_index, so this scroll IS targeted" is false when no target was
	// given, and every later inference would rest on it.
	//
	// The distinction must be an explicit flag: the synthesized point record
	// built in Go has index 0, not null, so an `index is not None` check would
	// still claim targeting.
	if !strings.Contains(mainGoSource(t), "WindowCentre") {
		t.Fatal("scrolling at the window centre needs an explicit flag instead of a runtime guess")
	}
	if !strings.Contains(linuxRuntimeScript, `action_input.get("window_centre")`) {
		t.Fatal("note wording must branch on the explicit flag")
	}
	if !strings.Contains(linuxRuntimeScript, "no target ref was given, so this is the centre") {
		t.Fatal("without a target the note must say the scroll point is the window centre")
	}
}

func TestGUIWaitDescriptionExplainsBarrierAndObservationBoundary(t *testing.T) {
	tool := findToolDefinition(t, "GUI")
	inputBranches := tool.InputSchema["oneOf"].([]any)
	actProps := inputBranches[1].(map[string]any)["properties"].(map[string]any)
	action := actProps["actions"].(map[string]any)["items"].(map[string]any)
	var waitDescription string
	for _, raw := range action["oneOf"].([]any) {
		fields := raw.(map[string]any)["properties"].(map[string]any)
		actionType := fields["type"].(map[string]any)
		if actionType["const"] == "wait" {
			waitDescription = actionType["description"].(string)
			if len(fields) != 1 {
				t.Fatalf("wait exposes non-native fields: %#v", fields)
			}
		}
	}
	for _, phrase := range []string{
		"exactly one second",
		"without an intermediate observation",
	} {
		if !strings.Contains(waitDescription, phrase) {
			t.Fatalf("gui wait description missing %q", phrase)
		}
	}
}
