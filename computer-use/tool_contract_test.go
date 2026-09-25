package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestToolDefinitionCount(t *testing.T) {
	if got := len(toolDefinitions()); got != 2 {
		t.Fatalf("toolDefinitions() count = %d, want 2", got)
	}
	findToolDefinition(t, "AX")
	findToolDefinition(t, "GUI")
	for _, removed := range []string{"get_screenshot", "click_xy", "drag_xy"} {
		for _, tool := range toolDefinitions() {
			if tool.Name == removed {
				t.Fatalf("legacy GUI tool %q must not be exposed", removed)
			}
		}
	}
}

// TestCapabilityIsMeasuredNeverDeclaredByAppName checks that the server
// instructions never scope capability by application name. Declaring an app
// unsupported by name makes the agent give up on tasks the tools can perform;
// browsers, for example, often expose a complete accessibility tree.
// Reachability must be measured at runtime instead, and content missing from
// the accessibility tree only removes the AX channel for that content: the
// application shell stays addressable and the content can still be driven
// through GUI.
func TestCapabilityIsMeasuredNeverDeclaredByAppName(t *testing.T) {
	if !strings.Contains(
		serverInstructions,
		"content absent from the accessibility tree",
	) {
		t.Fatal("instructions must describe accessibility reachability without app exclusions")
	}
	// Regression guard: capability statements must not name specific apps or
	// drivers. App names that only cite the source of a measured number are
	// evidence, not capability branches, so they are not banned here.
	for _, banned := range []string{
		"Browsers are NOT handled",
		"Chrome", "Chromium", "Playwright", "browser-use", "CDP",
		"Thunderbird", "VLC",
	} {
		if strings.Contains(serverInstructions, banned) {
			t.Fatalf("server instructions must not scope capability by app name: found %q", banned)
		}
	}
}

// TestInstructionsOpenWithTheLiveSessionFact checks that the instructions first
// state that the session is attached to a running desktop, before describing
// the tools. Without that fact an agent may answer a request such as "show me
// X" from its own knowledge and never call a tool.
func TestInstructionsOpenWithTheLiveSessionFact(t *testing.T) {
	if !strings.HasPrefix(serverInstructions, "Control the already-running desktop") {
		t.Fatalf("instructions must open with the live desktop fact: %q", serverInstructions)
	}
	for _, fragment := range []string{"AX is", "GUI is"} {
		if !strings.Contains(serverInstructions, fragment) {
			t.Fatalf("instructions must name both control channels: missing %q", fragment)
		}
	}
}

func TestMCPInstructionsDocumentGlobalSynthesisFocusContract(t *testing.T) {
	if !strings.Contains(serverInstructions, "fallbacks use global desktop focus") {
		t.Fatal("MCP instructions must tell the agent that synthesis ignores the app argument")
	}
	if !strings.Contains(serverInstructions, "not background input") {
		t.Fatal("MCP instructions must not imply background delivery")
	}
}

// TestInstructionsDoNotClaimScreenshotsAreAlwaysCostlier checks that AX is
// recommended for actionability, not for being cheaper. Costs vary by app: a
// gedit tree measured about 350 tokens against about 1,000 for its window
// screenshot, while a file manager tree measured about 2,100 tokens against
// about 750. The instructions must not steer the agent with a claim that fails
// on content-rich applications.
func TestInstructionsDoNotClaimScreenshotsAreAlwaysCostlier(t *testing.T) {
	if !strings.Contains(serverInstructions, "Use AX for named controls") {
		t.Fatal("instructions should give semantic targeting as the reason to prefer AX")
	}
	if strings.Contains(serverInstructions, "it is cheap, precise") {
		t.Fatal("instructions must not claim the a11y tree is always cheaper than a screenshot")
	}
}

func TestConsolidatedA11ySurfaceExposesOnlyA11yAndGUI(t *testing.T) {
	t.Setenv("COMPUTER_USE_CHANNELS", "")

	names := map[string]bool{}
	for _, tool := range toolDefinitions() {
		names[tool.Name] = true
	}
	for _, visible := range []string{"AX", "GUI"} {
		if !names[visible] {
			t.Fatalf("consolidated surface must keep %s visible", visible)
		}
	}
	for _, hiddenAlias := range []string{"a11y", "gui"} {
		if names[hiddenAlias] {
			t.Fatalf("compatibility alias %s must not be advertised", hiddenAlias)
		}
	}
	if len(names) != 2 {
		t.Fatalf("consolidated surface exposed unexpected tools: %#v", names)
	}

	for public, internal := range map[string]string{
		"AX":  "a11y",
		"GUI": "gui",
	} {
		if got := internalToolName(public); got != internal {
			t.Fatalf("public spelling %s normalized to %s, want %s",
				public, got, internal)
		}
	}
	for _, legacy := range []string{
		"a11y", "get_app_state", "find", "verify", "list_apps", "click",
		"invoke_element_action", "press_key", "scroll", "set_value", "type_text",
	} {
		if _, ok := publicToolCallName(legacy); ok {
			t.Fatalf("legacy public tool %q must be rejected", legacy)
		}
	}
}

func TestModelFacingContractsAreBenchmarkNeutral(t *testing.T) {
	payload := serverInstructions
	for _, definition := range allToolDefinitions() {
		encoded, err := json.Marshal(definition)
		if err != nil {
			t.Fatal(err)
		}
		payload += "\n" + string(encoded)
	}
	for _, forbidden := range []string{"OSWorld", "benchmark", "task_id"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("model-facing MCP contract contains benchmark-specific text %q", forbidden)
		}
	}
}

func TestGUISurfaceUsesFullDesktopImageCoordinates(t *testing.T) {
	for _, fragment := range []string{
		"two complementary tools",
		"GUI is full-desktop visual observation",
		"absolute-pixel input",
		"AX target-window screenshot coordinates are never GUI coordinates",
	} {
		if !strings.Contains(serverInstructions, fragment) {
			t.Fatalf("client instructions are missing the GUI/a11y split: %q", fragment)
		}
	}
	for _, stale := range []string{
		"Coordinates are one consistent space across everything",
		"GUI addresses a target by window-relative pixel coordinates",
	} {
		if strings.Contains(serverInstructions, stale) {
			t.Fatalf("client instructions still contain the old coordinate convention: %q", stale)
		}
	}

	gui := findToolDefinition(t, "GUI")
	if !strings.Contains(gui.Description, "Full-desktop") ||
		!strings.Contains(gui.Description, "returns one final screenshot") {
		t.Fatal("GUI must declare full-desktop image coordinates and a screenshot on every call")
	}
	for _, removed := range []string{"get_screenshot", "click_xy", "drag_xy"} {
		if strings.Contains(gui.Description, removed) {
			t.Fatalf("gui description must not advertise removed tool %q", removed)
		}
	}
}

func TestGUISchemaPublishesRuntimeLimitsAndFocusBoundary(t *testing.T) {
	gui := findToolDefinition(t, "GUI")
	inputBranches := gui.InputSchema["oneOf"].([]any)
	if len(inputBranches) != 2 {
		t.Fatalf("GUI input oneOf branches=%d want 2", len(inputBranches))
	}
	observeProps := inputBranches[0].(map[string]any)["properties"].(map[string]any)
	observeActions := observeProps["actions"].(map[string]any)
	if observeActions["minItems"] != 1 || observeActions["maxItems"] != 1 {
		t.Fatal("GUI observation branch must require one screenshot action")
	}
	if _, ok := observeProps["settle_ms"]; ok {
		t.Fatal("GUI observation branch must not accept settle_ms")
	}
	actProps := inputBranches[1].(map[string]any)["properties"].(map[string]any)
	actions := actProps["actions"].(map[string]any)
	if got := actions["maxItems"]; got != guiMaxActions {
		t.Fatalf("gui actions maxItems = %v, want %d", got, guiMaxActions)
	}
	if got := actions["minItems"]; got != 1 {
		t.Fatalf("gui action branch minItems = %v, want 1", got)
	}
	action := actions["items"].(map[string]any)
	actionBranches := action["oneOf"].([]any)
	byType := map[string]map[string]any{}
	for _, raw := range actionBranches {
		branch := raw.(map[string]any)
		fields := branch["properties"].(map[string]any)
		actionType := fields["type"].(map[string]any)["const"].(string)
		byType[actionType] = fields
	}
	wantTypes := []string{
		"click", "double_click", "move", "drag",
		"scroll", "type", "keypress", "wait",
	}
	for _, actionType := range wantTypes {
		if _, ok := byType[actionType]; !ok {
			t.Fatalf("GUI action union missing %q", actionType)
		}
	}
	if len(byType) != len(wantTypes) {
		t.Fatalf("GUI action union has unexpected branches: %#v", byType)
	}

	path := byType["drag"]["path"].(map[string]any)
	if path["maxItems"] != guiMaxDragPoints {
		t.Fatalf("drag path maxItems = %v, want %d", path["maxItems"], guiMaxDragPoints)
	}
	keys := byType["keypress"]["keys"].(map[string]any)
	if keys["maxItems"] != guiMaxKeys {
		t.Fatalf("keypress maxItems = %v, want %d", keys["maxItems"], guiMaxKeys)
	}
	key := keys["items"].(map[string]any)
	if key["maxLength"] != guiMaxKeyLength {
		t.Fatalf("key maxLength = %v, want %d", key["maxLength"], guiMaxKeyLength)
	}
	keyDescription, _ := keys["description"].(string)
	for _, spelling := range []string{"PAGE_UP", "PAGEUP", "PAGE_DOWN", "PAGEDOWN"} {
		if !strings.Contains(keyDescription, spelling) {
			t.Fatalf("keypress description does not advertise %s: %q",
				spelling, keyDescription)
		}
	}
	text := byType["type"]["text"].(map[string]any)
	if text["maxLength"] != guiMaxTextLength {
		t.Fatalf("type text maxLength = %v, want %d", text["maxLength"], guiMaxTextLength)
	}
	for _, name := range []string{"scroll_x", "scroll_y"} {
		delta := byType["scroll"][name].(map[string]any)
		if delta["minimum"] != -guiMaxScrollDelta ||
			delta["maximum"] != guiMaxScrollDelta {
			t.Fatalf("%s range = [%v,%v], want [%d,%d]",
				name, delta["minimum"], delta["maximum"],
				-guiMaxScrollDelta, guiMaxScrollDelta)
		}
	}
	scrollBranch := func() map[string]any {
		for _, raw := range actionBranches {
			branch := raw.(map[string]any)
			fields := branch["properties"].(map[string]any)
			if fields["type"].(map[string]any)["const"] == "scroll" {
				return branch
			}
		}
		return nil
	}()
	if scrollBranch == nil || len(scrollBranch["anyOf"].([]any)) != 2 {
		t.Fatal("scroll schema must require a non-zero scroll_x or scroll_y")
	}
	clickButtons := byType["click"]["button"].(map[string]any)["enum"].([]string)
	if !reflect.DeepEqual(
		clickButtons,
		[]string{"left", "right", "wheel", "back", "forward"},
	) {
		t.Fatalf("GUI click buttons=%#v", clickButtons)
	}
	if _, ok := byType["double_click"]["button"]; ok {
		t.Fatal("native double_click must not accept button")
	}
	for _, actionType := range []string{
		"click", "double_click", "move", "drag", "scroll",
	} {
		if _, ok := byType[actionType]["keys"]; !ok {
			t.Fatalf("%s must accept optional held keys", actionType)
		}
	}
	if len(byType["wait"]) != 1 {
		t.Fatalf("wait must expose only type, got %#v", byType["wait"])
	}
	for _, text := range []string{gui.Description, serverInstructions} {
		if !strings.Contains(text, "current focus") &&
			!strings.Contains(text, "current input focus") {
			t.Fatal("gui type/keypress must state that they target only current input focus")
		}
	}
	if !strings.Contains(gui.Description, "validates before input") {
		t.Fatal("gui must publish whole-batch pre-validation")
	}
	if !strings.Contains(gui.Description, "one final screenshot") {
		t.Fatal("gui must state that one final screenshot covers the complete batch")
	}
	if !strings.Contains(gui.Description, "wait is one second") {
		t.Fatal("gui must publish fixed native wait semantics")
	}
	if !strings.Contains(
		gui.Description,
		fmt.Sprintf("max %ds/batch", guiMaxTotalWait),
	) {
		t.Fatal("gui must publish the cumulative wait budget")
	}
	settle := actProps["settle_ms"].(map[string]any)
	if settle["minimum"] != 0 || settle["maximum"] != guiMaxSettleMS {
		t.Fatalf("gui settle_ms range = [%v,%v], want [0,%d]",
			settle["minimum"], settle["maximum"], guiMaxSettleMS)
	}
	if !strings.Contains(gui.Description, "visual stillness, not semantic success") {
		t.Fatal("gui description must keep the settling evidence boundary")
	}
	if !strings.Contains(gui.Description, "omitted default") {
		t.Fatal("gui description must publish omitted settle_ms behavior")
	}
	for _, overclaim := range []string{"pixel-stable", "executed"} {
		if strings.Contains(gui.Description, overclaim) {
			t.Fatalf("gui description must not imply successful execution/effect with %q", overclaim)
		}
	}
	field, ok := reflect.TypeOf(ProviderDesktopRequest{}).FieldByName("SettleMS")
	if !ok || field.Tag.Get("json") != "settle_ms" {
		t.Fatal("ProviderDesktopRequest must preserve explicit settle_ms=0")
	}
	encoded, err := json.Marshal(ProviderDesktopRequest{
		ProtocolVersion: providerProtocolV2,
		Actions:         []ProviderDesktopAction{{Type: "screenshot"}},
		SettleMS:        0,
	})
	if err != nil || !strings.Contains(string(encoded), `"settle_ms":0`) {
		t.Fatalf("explicit settle_ms=0 was lost: %s err=%v", encoded, err)
	}
	if _, ok := actProps["frame_id"]; ok {
		t.Fatal("gui schema must keep frame provenance private")
	}
	if _, ok := actProps["expect_change"]; ok {
		t.Fatal("gui schema must not expose expect_change")
	}
	if _, ok := reflect.TypeOf(ProviderDesktopRequest{}).FieldByName("ExpectChange"); ok {
		t.Fatal("runtime transport must not carry expect_change")
	}
	field, ok = reflect.TypeOf(ProviderDesktopRequest{}).FieldByName("FrameID")
	if !ok || field.Tag.Get("json") != "frame_id,omitempty" {
		t.Fatal("ProviderDesktopRequest must carry private frame provenance")
	}
	for fieldName, jsonName := range map[string]string{
		"CoordinateSpace": "coordinateSpace,omitempty",
		"FrameID":         "frameId,omitempty",
		"ContentFrameID":  "contentFrameId,omitempty",
		"DesktopLayout":   "desktopLayout,omitempty",
		"Width":           "width,omitempty",
		"Height":          "height,omitempty",
	} {
		field, ok := reflect.TypeOf(appSnapshot{}).FieldByName(fieldName)
		if !ok || field.Tag.Get("json") != jsonName {
			t.Fatalf("appSnapshot.%s must preserve GUI metadata as %s", fieldName, jsonName)
		}
	}
	var snapshot appSnapshot
	if err := json.Unmarshal(
		[]byte(`{"coordinateSpace":"screen_image_px","frameId":"abc","contentFrameId":"pixels","width":1920,"height":1080}`),
		&snapshot,
	); err != nil {
		t.Fatal(err)
	}
	if snapshot.CoordinateSpace != "screen_image_px" || snapshot.FrameID != "abc" ||
		snapshot.ContentFrameID != "pixels" ||
		snapshot.Width != 1920 || snapshot.Height != 1080 {
		t.Fatalf("GUI metadata did not survive runtime decoding: %#v", snapshot)
	}
	if !strings.Contains(mainGoSource(t), `"coordinate_space":`) ||
		!strings.Contains(mainGoSource(t), `snapshot.CoordinateSpace`) {
		t.Fatal("gui metadata helper must normalize runtime coordinateSpace to public coordinate_space")
	}
	for actionType, fields := range byType {
		for _, forbidden := range []string{
			"window_id", "desktop_layout", "delta_x", "delta_y",
			"seconds", "expect_change",
		} {
			if _, ok := fields[forbidden]; ok {
				t.Fatalf("GUI %s schema leaked field %q", actionType, forbidden)
			}
		}
	}
}

func TestActionBoundsRejectUnboundedInput(t *testing.T) {
	service := newService()
	for name, args := range map[string]map[string]any{
		"click": {
			"app": "Editor", "element_index": "1",
			"click_count": float64(maxClickCount + 1),
		},
		"scroll": {
			"app": "Editor", "direction": "down",
			"pages": float64(maxScrollPages + 1),
		},
		"verify": {
			"app": "Editor", "element_index": "1", "exists": true,
			"timeout_ms": float64(maxVerifyTimeout + 1),
		},
		"type_text": {
			"app": "Editor", "text": strings.Repeat("x", maxActionText+1),
		},
		"set_value": {
			"app": "Editor", "element_index": "1",
			"value": strings.Repeat("x", maxActionText+1),
		},
	} {
		result := service.dispatch(name, args)
		if !result.IsError {
			t.Fatalf("%s accepted unbounded input", name)
		}
	}
}

func TestAppArgumentHelpDefinesStrictBootstrapIdentity(t *testing.T) {
	// Agents hesitate to guess app names because accessibility app names come
	// in three styles that no rule can predict:
	//     display name  "Google Chrome"        process is chrome
	//     binary name   "vlc" "gedit" "code"   same as the process
	//     app id        "org.gnome.Nautilus"   process is nautilus
	// Since no rule derives them, the help text states the accepted forms.
	for _, fragment := range []string{
		"app/process name", "app id", "PID",
	} {
		if !strings.Contains(appArgumentHelp, fragment) {
			t.Fatalf("app argument help is missing %q", fragment)
		}
	}
	if strings.Contains(appArgumentHelp, "window-title") {
		t.Fatal("app bootstrap must not accept document or dialog window titles")
	}
	// Every mode that accepts app must use this help text, not just one.
	tool := findToolDefinition(t, "AX")
	branches := tool.InputSchema["oneOf"].([]any)
	defs := tool.InputSchema["$defs"].(map[string]any)
	used := 0
	withApp := 0
	for _, raw := range branches {
		properties := raw.(map[string]any)["properties"].(map[string]any)
		if property, ok := properties["app"].(map[string]any); ok {
			withApp++
			if property["$ref"] == "#/$defs/app" {
				property = defs["app"].(map[string]any)
			}
			if property["description"] == appArgumentHelp {
				used++
			}
		}
	}
	if used != withApp || withApp != 3 {
		t.Fatalf("%d/%d AX mode branches with app use the shared help text, want 3/3", used, withApp)
	}
}

func TestModelContractHasBoundedFixedContext(t *testing.T) {
	if len(serverInstructions) > 1500 {
		t.Fatalf("server instructions grew to %d bytes", len(serverInstructions))
	}
	encoded, err := json.Marshal(toolDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf(
		"model contract bytes: instructions=%d tools=%d total=%d",
		len(serverInstructions),
		len(encoded),
		len(serverInstructions)+len(encoded),
	)
	if len(encoded) > 25000 {
		t.Fatalf("public tool definitions grew to %d bytes", len(encoded))
	}
	if total := len(serverInstructions) + len(encoded); total > 26000 {
		t.Fatalf("total fixed model contract grew to %d bytes", total)
	}
}

func TestPublicModelContractDoesNotRecommendLegacyMutationTargets(t *testing.T) {
	encoded, err := json.Marshal(toolDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	publicContract := serverInstructions + "\n" + string(encoded)
	for _, forbidden := range []string{
		"element_index",
		`view:\"full\"`,
		"provider facts",
		"invoke_element_action",
	} {
		if strings.Contains(publicContract, forbidden) {
			t.Fatalf(
				"public model contract leaked legacy mutation guidance %q",
				forbidden,
			)
		}
	}
	for _, required := range []string{
		"act consumes refs only",
		"Element actions require rN",
		"window actions require wN",
	} {
		if !strings.Contains(publicContract, required) {
			t.Fatalf("public model contract is missing %q", required)
		}
	}
}
