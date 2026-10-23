package core

import (
	"strings"
	"testing"

	"github.com/hqhq1025/hybrid-computer-use-oss/computer-use/internal/contract"
)

func TestResolvedFocusConflictRetainsWindowIdentityWithoutClaimingAReadGap(t *testing.T) {
	snapshot := &AppSnapshot{
		App:          AppDescriptor{Name: "Editor", PID: 42},
		WindowTitle:  "Document",
		WindowHandle: "window-token",
		SnapshotID:   "focus-sample",
		Elements: []ElementRecord{
			{Index: 0, RuntimeID: []int{0}, ControlType: "window", Name: "Document", States: "focused"},
			{Index: 1, RuntimeID: []int{0, 0}, ControlType: "text", Name: "Paragraph"},
		},
		Annotations: []axProviderAnnotation{{
			ParentRuntimeID: []int{0}, Kind: axFocusEvidenceAnnotation,
			Reason: "focused_object_property_conflict",
			Detail: "Current focus matches this node; its earlier focus flag was false.",
		}},
		Scope: contract.AXScopeWindow,
	}
	root, _, err := resolveObservationRoot(snapshot, map[string]any{"focused": true})
	if err != nil || root == nil || root.Index != 0 || root.ControlType != "window" {
		t.Fatalf("an exact focused window became an editor focus or unavailable: %#v %v", root, err)
	}
	text := typedTreeTextForTest(t, snapshot)
	if !strings.Contains(text, "/focus-evidence:") || !strings.Contains(text, "earlier focus flag was false") {
		t.Fatalf("resolved conflict was hidden from the returned tree: %s", text)
	}
	if got := providerCoverageSummary(snapshot.Annotations); got.AcquisitionIncomplete != 0 {
		t.Fatalf("resolved focus conflict became unread nodes: %#v", got)
	}
	snapshot.Annotations = append(snapshot.Annotations, axProviderAnnotation{
		Kind: "omitted", Count: 1, Reason: "focused_element_not_sampled",
	})
	if got := providerCoverageSummary(snapshot.Annotations); got.AcquisitionIncomplete != 1 ||
		len(got.AcquisitionReasons) != 1 || got.AcquisitionReasons[0] != "focused_element_not_sampled" {
		t.Fatalf("a real acquisition gap was lost: %#v", got)
	}
}
