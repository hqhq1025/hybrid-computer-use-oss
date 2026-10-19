package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestContentScopeKeepsOneTopLevelContentSubtree(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"Browser\"",
			"\t\t1 tool bar \"Navigation\"",
			"\t\t\t2 push button \"Reload\" [has-click-action]",
			"\t\t3 document web \"Example\"",
			"\t\t\t4 heading \"Welcome\"",
			"\t\t\t5 document web \"Embedded\"",
			"\t\t\t\t6 link \"Continue\" [has-click-action]",
		},
		Elements: []elementRecord{
			{Index: 0, RuntimeID: []int{}, ControlType: "frame", Name: "Browser"},
			{Index: 1, RuntimeID: []int{0}, ControlType: "tool bar", Name: "Navigation"},
			{Index: 2, RuntimeID: []int{0, 0}, ControlType: "push button", Name: "Reload"},
			{
				Index: 3, RuntimeID: []int{1},
				ControlType:          "document web",
				LocalizedControlType: "documento web",
				Name:                 "Example",
			},
			{Index: 4, RuntimeID: []int{1, 0}, ControlType: "heading", Name: "Welcome"},
			{Index: 5, RuntimeID: []int{1, 1}, ControlType: "document web", Name: "Embedded"},
			{Index: 6, RuntimeID: []int{1, 1, 0}, ControlType: "link", Name: "Continue"},
		},
	}

	if err := applyA11yScope(snapshot, a11yScopeContent); err != nil {
		t.Fatalf("content scope failed: %v", err)
	}
	if snapshot.scopeRoot == nil || snapshot.scopeRoot.Index != 3 {
		t.Fatalf("selected content root = %#v, want index 3", snapshot.scopeRoot)
	}
	if got := scopedCanonicalElementCount(snapshot); got != 4 {
		t.Fatalf("scoped canonical count = %d, want 4", got)
	}
	text := typedTreeTextForTest(t, snapshot)
	for _, absent := range []string{"Navigation", "Reload", "0 frame"} {
		if strings.Contains(text, absent) {
			t.Fatalf("window chrome leaked into content scope via %q:\n%s", absent, text)
		}
	}
	for _, present := range []string{
		`document "Example"`,
		`heading "Welcome"`,
		`document "Embedded"`,
		`link "Continue"`,
	} {
		if !strings.Contains(text, present) {
			t.Fatalf("content subtree lost %q:\n%s", present, text)
		}
	}
	if len(snapshot.Elements) != 7 {
		t.Fatal("content scope must preserve the complete canonical action records")
	}
}

func TestContentScopeUsesFocusToDisambiguateTopLevelRoots(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"Split\"",
			"\t\t1 document web \"Left\"",
			"\t\t\t2 heading \"Left title\" [focused]",
			"\t\t3 document web \"Right\"",
			"\t\t\t4 heading \"Right title\"",
		},
		Elements: []elementRecord{
			{Index: 0, RuntimeID: []int{}, ControlType: "frame", Name: "Split"},
			{Index: 1, RuntimeID: []int{0}, ControlType: "document web", Name: "Left"},
			{Index: 2, RuntimeID: []int{0, 0}, ControlType: "heading", Name: "Left title", States: "[focused]"},
			{Index: 3, RuntimeID: []int{1}, ControlType: "document web", Name: "Right"},
			{Index: 4, RuntimeID: []int{1, 0}, ControlType: "heading", Name: "Right title"},
		},
	}

	if err := applyA11yScope(snapshot, a11yScopeContent); err != nil {
		t.Fatalf("focused content scope failed: %v", err)
	}
	if snapshot.scopeRoot == nil || snapshot.scopeRoot.Index != 1 {
		t.Fatalf("focused root = %#v, want left document", snapshot.scopeRoot)
	}
	if strings.Contains(typedTreeTextForTest(t, snapshot), "Right") {
		t.Fatal("unfocused sibling content root leaked into scoped tree")
	}
}

func TestContentScopeFailsClosedWithoutUniqueRoot(t *testing.T) {
	for name, snapshot := range map[string]*appSnapshot{
		"none": {
			TreeLines: []string{"\t0 frame \"Settings\"", "\t\t1 push button \"OK\""},
			Elements: []elementRecord{
				{Index: 0, RuntimeID: []int{}, ControlType: "frame", Name: "Settings"},
				{Index: 1, RuntimeID: []int{0}, ControlType: "push button", Name: "OK"},
			},
		},
		"ambiguous": {
			TreeLines: []string{
				"\t0 frame \"Split\"",
				"\t\t1 document web \"Left\"",
				"\t\t2 document web \"Right\"",
			},
			Elements: []elementRecord{
				{Index: 0, RuntimeID: []int{}, ControlType: "frame", Name: "Split"},
				{Index: 1, RuntimeID: []int{0}, ControlType: "document web", Name: "Left"},
				{Index: 2, RuntimeID: []int{1}, ControlType: "document web", Name: "Right"},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := applyA11yScope(snapshot, a11yScopeContent)
			if err == nil {
				t.Fatal("content scope must fail closed")
			}
			if !strings.Contains(err.Error(), `scope="content"`) ||
				!strings.Contains(err.Error(), `scope="window"`) {
				t.Fatalf("scope error is not actionable: %v", err)
			}
		})
	}
}

func TestContentScopeAndRootIgnoreLegacyTreeBudget(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{`\t0 frame "Browser"`},
		Elements: []elementRecord{
			{Index: 0, RuntimeID: []int{}, ControlType: "frame", Name: "Browser"},
			{Index: 1, RuntimeID: []int{0}, ControlType: "document web", Name: "Page"},
			{Index: 2, RuntimeID: []int{0, 0}, ControlType: "heading", Name: "Deep target"},
		},
	}
	if err := applyA11yScope(snapshot, a11yScopeContent); err != nil {
		t.Fatalf("canonical content scope depended on rendered text: %v", err)
	}
	if err := applyObservationRoot(snapshot, map[string]any{
		"selector": map[string]any{
			"role": "heading", "name": "Deep target",
		},
	}); err != nil {
		t.Fatalf("canonical root depended on rendered text: %v", err)
	}
	text := typedTreeTextForTest(t, snapshot)
	if !strings.Contains(text, `heading "Deep target"`) {
		t.Fatalf("typed root missing despite canonical record:\n%s", text)
	}
}

func TestFocusedAndTargetRootPreserveCanonicalAddressSpace(t *testing.T) {
	focused := editorFormSnapshot()
	if err := applyObservationRoot(
		focused,
		map[string]any{"focused": true},
	); err != nil {
		t.Fatal(err)
	}
	if focused.observationRoot == nil || focused.observationRoot.Index != 1 {
		t.Fatalf("focused root = %#v, want nearby panel 1", focused.observationRoot)
	}
	text := typedTreeTextForTest(t, focused)
	if !strings.Contains(text, `textbox "Name"`) ||
		!strings.Contains(text, `button "Save"`) ||
		strings.Contains(text, `status "Ready"`) {
		t.Fatalf("focused subtree has wrong local context:\n%s", text)
	}
	if len(focused.Elements) != 5 {
		t.Fatal("root projection changed canonical elements")
	}

	target := editorFormSnapshot()
	if err := applyObservationRoot(
		target,
		map[string]any{
			"selector": map[string]any{
				"role": "push button",
				"name": "Save",
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	if target.observationRoot == nil || target.observationRoot.Index != 3 {
		t.Fatalf("target root = %#v, want Save button", target.observationRoot)
	}
	targetText := typedTreeTextForTest(t, target)
	if !strings.Contains(targetText, `button "Save"`) ||
		strings.Contains(targetText, `textbox "Name"`) {
		t.Fatalf("target subtree is not exact:\n%s", targetText)
	}
}

func TestObservationRootAcceptsExpandedShortRefInternally(t *testing.T) {
	snapshot := editorFormSnapshot()
	internalRef := encodeScopedSemanticRef(
		`push button "Save"`,
		snapshot.WindowHandle,
	)
	if _, err := decodeAXRequest(map[string]any{
		"mode": "observe", "window_ref": "w1", "root": internalRef,
	}); err == nil {
		t.Fatal("public observation root accepted an internal semantic ref")
	}
	if err := applyObservationRoot(
		snapshot,
		map[string]any{"ref": internalRef},
	); err != nil {
		t.Fatalf("expanded short ref did not resolve internally: %v", err)
	}
	if snapshot.observationRoot == nil || snapshot.observationRoot.Index != 3 {
		t.Fatalf(
			"expanded short-ref root = %#v, want Save button index 3",
			snapshot.observationRoot,
		)
	}
}

func TestRootProjectionFailsClosed(t *testing.T) {
	snapshot := editorFormSnapshot()
	err := applyObservationRoot(
		snapshot,
		map[string]any{
			"selector": map[string]any{
				"role": "push button",
				"name": "Missing",
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "no element matches") {
		t.Fatalf("missing root did not fail closed: %v", err)
	}
}

func TestObservationRootProviderBoundRefNarrowsSharedProviderIdentity(t *testing.T) {
	entry := &AXRefEntry{
		LocatorKind:        AXLocatorProviderBound,
		WindowHandle:       "pid7/win0/frame",
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/org/a11y/table/1",
		ProcessGeneration:  "pid:7:start:10",
		RuntimeID:          []int{0, 2},
	}
	snapshot := &appSnapshot{
		WindowHandle: "pid7/win0/frame",
		Elements: []elementRecord{
			{
				Index: 2, RuntimeID: []int{0, 1},
				ProviderBusName:    ":1.42",
				ProviderObjectPath: "/org/a11y/table/1",
				ProcessGeneration:  "pid:7:start:10",
			},
			{
				Index: 3, RuntimeID: []int{0, 2},
				ProviderBusName:    ":1.42",
				ProviderObjectPath: "/org/a11y/table/1",
				ProcessGeneration:  "pid:7:start:10",
			},
		},
	}
	record, err := resolveProviderBoundObservationRoot(snapshot, entry)
	if err != nil {
		t.Fatalf("resolve shared provider identity: %v", err)
	}
	if record.Index != 3 {
		t.Fatalf("resolved index=%d, want 3", record.Index)
	}
}

func TestObservationRootProviderBoundRefFailsClosed(t *testing.T) {
	entry := &AXRefEntry{
		LocatorKind:        AXLocatorProviderBound,
		WindowHandle:       "pid7/win0/frame",
		ProviderBusName:    ":1.42",
		ProviderObjectPath: "/org/a11y/table/1",
		ProcessGeneration:  "pid:7:start:10",
	}
	t.Run("missing", func(t *testing.T) {
		_, err := resolveProviderBoundObservationRoot(
			&appSnapshot{WindowHandle: "pid7/win0/frame"},
			entry,
		)
		if err == nil || !strings.Contains(err.Error(), "stale_element_ref") {
			t.Fatalf("missing provider root error=%v", err)
		}
	})
	t.Run("ambiguous", func(t *testing.T) {
		snapshot := &appSnapshot{
			WindowHandle: "pid7/win0/frame",
			Elements: []elementRecord{
				{
					Index: 2, RuntimeID: []int{0, 1},
					ProviderBusName:    ":1.42",
					ProviderObjectPath: "/org/a11y/table/1",
					ProcessGeneration:  "pid:7:start:10",
				},
				{
					Index: 3, RuntimeID: []int{0, 2},
					ProviderBusName:    ":1.42",
					ProviderObjectPath: "/org/a11y/table/1",
					ProcessGeneration:  "pid:7:start:10",
				},
			},
		}
		_, err := resolveProviderBoundObservationRoot(snapshot, entry)
		if err == nil || !strings.Contains(err.Error(), "ambiguous_target") {
			t.Fatalf("ambiguous provider root error=%v", err)
		}
	})
}

// Probe #18/#19: the published observe root reused the act target schema, so
// it advertised {"ref":"wN"}, which the runtime rejects, and an index
// described as "action 1 only". The root now uses the read-only element target.
func TestObserveRootSchemaMatchesRuntimeRefRule(t *testing.T) {
	properties := findToolDefinition(t, "AX").InputSchema["properties"].(map[string]any)
	root := properties["root"].(map[string]any)
	encoded, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if strings.Contains(text, "w[1-9]") {
		t.Fatalf("observe root schema still advertises wN refs: %s", text)
	}
	if strings.Contains(text, "action 1 only") {
		t.Fatalf("observe root index still claims to be act-only: %s", text)
	}
	observe := func(root string) *axArgError {
		_, err := decodeAXRequest(map[string]any{
			"mode": "observe", "window_ref": "w1", "root": root,
		})
		return err
	}
	if err := observe("w1"); err == nil {
		t.Fatal("runtime accepted a wN observe root")
	}
	if err := observe("r1"); err != nil {
		t.Fatalf("runtime rejected an rN observe root: %#v", err)
	}
}
