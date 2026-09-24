package main

import (
	"strconv"
	"strings"
	"testing"
)

func TestInvalidActTreeObservationTellsModelToRetrySameAct(t *testing.T) {
	service := newService()
	result := service.a11yRequest(map[string]any{
		"mode": "act",
		"actions": []any{map[string]any{
			"type":   "click",
			"target": map[string]any{"ref": "r1"},
		}},
	}, observation{mode: observeTree})
	text := resultText(result)
	if !result.IsError ||
		!strings.Contains(text, "No action was executed") ||
		!strings.Contains(text, "Retry this same act") ||
		!strings.Contains(text, "do not add a separate observe first") {
		t.Fatalf("invalid act observe recovery is misleading: %q", text)
	}
}

func TestAXRejectsAllRemovedCompatibilityForms(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"missing mode": {"app": "Editor"},
		"assert_a11y alias": {
			"mode": "assert_a11y",
			"app":  "Editor",
			"assertion": map[string]any{
				"window_title_contains": "Editor",
			},
		},
	} {
		if _, err := parseA11yMode(args); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	for name, action := range map[string]map[string]any{
		"element_index": {
			"type": "click", "element_index": 1,
		},
		"element": {
			"type": "click", "element": "Save",
		},
		"key": {
			"type": "keypress", "key": "TAB",
		},
	} {
		if _, err := normalizeA11yBatchActions([]any{action}); err == nil {
			t.Fatalf("legacy AX field %s was accepted", name)
		}
	}
}

func TestPublicAXObserveUsesServerOwnedAcquisitionBudget(t *testing.T) {
	branch := findA11ySchemaBranch(t, "observe", "app")
	properties := branch["properties"].(map[string]any)
	for _, forbidden := range []string{
		"text_limit", "max_tree_nodes", "max_tree_depth", "prune", "force_full",
		"view",
	} {
		if _, present := properties[forbidden]; present {
			t.Fatalf("public AX observe still exposes %q", forbidden)
		}
	}
	if _, present := properties["root"]; !present {
		t.Fatal("public AX observe is missing the bounded root projection")
	}
	err := validateA11yModeFields(a11yModeObserve, map[string]any{
		"mode":           "observe",
		"text_limit":     100,
		"max_tree_nodes": 20,
		"max_tree_depth": 4,
		"prune":          false,
		"force_full":     true,
		"view":           "full",
	})
	if err == nil {
		t.Fatal("public AX observe accepted raw acquisition controls")
	}
	for _, field := range []string{
		"text_limit", "max_tree_nodes", "max_tree_depth", "prune", "force_full",
		"view",
	} {
		if !strings.Contains(err.Error(), strconv.Quote(field)) {
			t.Fatalf("validation did not report every illegal field: %v", err)
		}
	}
}

func TestPublicActIdentityErrorExplainsRefOwnership(t *testing.T) {
	service := newService()
	result := service.a11yRequest(map[string]any{
		"mode":    "act",
		"app_ref": "a1",
		"actions": []any{map[string]any{
			"type":   "click",
			"target": map[string]any{"ref": "r1"},
		}},
	}, observation{})
	if !result.IsError ||
		!strings.Contains(result.Content[0].Text, "target already owns") {
		t.Fatalf("public identity error=%#v", result)
	}
}

func TestPublicActRejectsInventedRefWithRecoveryGuidance(t *testing.T) {
	service := newService()
	result := service.a11yRequest(map[string]any{
		"mode": "act",
		"actions": []any{map[string]any{
			"type":   "click",
			"target": map[string]any{"ref": "r??"},
		}},
	}, observation{})
	if !result.IsError ||
		!strings.Contains(result.Content[0].Text, "never invent placeholders") {
		t.Fatalf("invented ref error=%#v", result)
	}
}

func TestReadIdentityDerivesOwnerFromElementRef(t *testing.T) {
	service, appRef, windowRef, elementRef := sheetBatchService(t)
	got, err := service.resolveA11yReadIdentity(
		map[string]any{}, elementRef,
	)
	if err != nil || got.appToken != appRef || got.windowRef != windowRef {
		t.Fatalf("owned assertion identity=%#v err=%v", got, err)
	}
	got, err = service.resolveA11yReadIdentity(
		map[string]any{"window_ref": windowRef}, elementRef,
	)
	if err != nil || got.appToken != appRef ||
		got.windowRef != windowRef || got.windowHandle == "" {
		t.Fatalf("window-scoped assertion identity=%#v err=%v", got, err)
	}
}

func TestReadIdentityRejectsConflictingAddressForms(t *testing.T) {
	service, appRef, windowRef, _ := sheetBatchService(t)
	for _, args := range []map[string]any{
		{"app": "LibreOffice", "app_ref": appRef},
		{"app_ref": appRef, "window_ref": windowRef},
	} {
		if _, err := service.resolveA11yReadIdentity(args, ""); err == nil {
			t.Fatalf("conflicting read identity accepted: %#v", args)
		}
	}
}
