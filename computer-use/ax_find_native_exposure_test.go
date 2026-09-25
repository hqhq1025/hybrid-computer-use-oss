package main

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// Keep complete canonical records but hide them from the working projection.
func findExposureFixture(t *testing.T, count int) (*service, *appSnapshot, []elementRecord) {
	t.Helper()
	svc, snapshot, matches := findDeliveryMany(t, count)
	root := *snapshot.axSnapshot.Roots[0]
	root.Children = nil
	snapshot.axSnapshot.Roots = []*AXNode{&root}
	if axProjectedIndices(snapshot.axSnapshot.Roots)[matches[0].Index] {
		t.Fatal("fixture must hide canonical matches")
	}
	return svc, snapshot, matches
}

var exposureRefLabels = regexp.MustCompile(`\[ref=(r[0-9]+)\]`)

func assertFindExposureDelivered(t *testing.T, svc *service, snapshot *appSnapshot, result toolCallResult) {
	t.Helper()
	want := map[int]elementRecord{}
	projected := axProjectedIndices(snapshot.axSnapshot.Roots)
	for _, label := range exposureRefLabels.FindAllStringSubmatch(resultText(result), -1) {
		entry := svc.refRegistry.entries[label[1]]
		if entry == nil {
			t.Fatalf("delivered ref missing registry entry: %s", label[1])
		}
		for _, record := range snapshot.Elements {
			if !projected[record.Index] && reflect.DeepEqual(record.RuntimeID, entry.RuntimeID) {
				want[record.Index] = record
			}
		}
	}
	got := svc.exposedHiddenRefs[deliveredSnapshotKey(snapshot)]
	if len(got) != len(want) {
		t.Fatalf("exposure count=%d, actually delivered hidden refs=%d\n%s", len(got), len(want), resultText(result))
	}
	for index, record := range want {
		if saved, ok := got[index]; !ok || !sameCanonicalAddress(record, saved) {
			t.Errorf("delivered hidden index %d lost canonical identity", index)
		}
	}
}

func TestFindExposureNoDeliveredRef(t *testing.T) {
	for _, kind := range []string{"unaddressable", "stabilization", "no_owner", "missing_node", "offscreen", "collapsed", "budget", "required_notes"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "24000")
			svc, snapshot, matches := findExposureFixture(t, 1)
			node := snapshot.axSnapshot.ByIndex[matches[0].Index]
			var notes []string
			switch kind {
			case "unaddressable":
				node.Ref, node.SemanticTarget, node.LocatorKind = "", "", ""
				node.SemanticIdentityAmbiguous = true
			case "stabilization":
				// Canonical records disagree with the would-be semantic selector.
				snapshot.Elements[2].Name = "Different canonical identity"
			case "no_owner":
				snapshot.axSnapshot.WindowRef = ""
			case "missing_node":
				delete(snapshot.axSnapshot.ByIndex, matches[0].Index)
			case "offscreen":
				off := false
				node.Showing = &off
			case "collapsed":
				node.Parent.States.Expanded = TriFalse
			case "budget":
				t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "1")
			case "required_notes":
				notes = []string{"[expect] " + strings.Repeat("required ", 4000)}
			}
			result := findDeliveryFinal(svc.renderFindPage(snapshot, matches, 0, 1, "target", notes, false))
			if strings.Contains(resultText(result), `target={"ref":`) {
				t.Fatalf("fixture unexpectedly delivered match ref: %s", resultText(result))
			}
			after := *snapshot
			after.Elements = snapshot.Elements[:2]
			if note := svc.hiddenCanonicalAddressChangeNote(snapshot, &after); note != "" {
				t.Fatalf("claimed undelivered ref: %s\n%s", note, resultText(result))
			}
		})
	}
}

func TestFindExposureBudgetLimitedPage(t *testing.T) {
	t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "2000")
	svc, snapshot, matches := findExposureFixture(t, 8)
	page := findDeliveryFinal(svc.renderFindPage(snapshot, matches, 0, 50, "target", nil, false))
	count, _ := intEvidence(findDeliveryModelEvidence(t, page)["find_page_count"])
	if page.IsError || count == 0 || count >= len(matches) {
		t.Fatalf("expected budget-limited page, got count=%d: %s", count, resultText(page))
	}
	assertFindExposureDelivered(t, svc, snapshot, page)
	cursor, _ := findDeliveryModelEvidence(t, page)["next_cursor"].(string)
	svc.exposedHiddenRefs = map[string]map[int]elementRecord{}
	t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "1")
	rejected := findDeliveryFinal(svc.continueFind(cursor, 50))
	if !rejected.IsError || len(svc.exposedHiddenRefs) != 0 {
		t.Fatalf("cached no-fit page recorded exposure: %s", resultText(rejected))
	}
}

func TestFindExposureVisibleMatchesStayOutOfHiddenLedger(t *testing.T) {
	t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "24000")
	svc, snapshot, matches := findDeliveryMany(t, 3)
	result := findDeliveryFinal(svc.renderFindPage(snapshot, matches, 0, 3, "target", nil, false))
	if result.IsError || len(findDeliveredTargets.FindAllStringSubmatch(resultText(result), -1)) != 3 {
		t.Fatalf("visible targets missing: %s", resultText(result))
	}
	if len(svc.exposedHiddenRefs) != 0 {
		t.Fatal("already projected targets entered hidden-ref ledger")
	}
}

func TestFindExposureRefWithoutContext(t *testing.T) {
	t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "24000")
	svc, snapshot, matches := findExposureFixture(t, 1)
	first := svc.renderFindPage(snapshot, matches, 0, 1, "target", nil, false)
	if first.IsError {
		t.Fatal(resultText(first))
	}
	// A cached result may have a target record but no context forest. The
	// atomic match unit remains sufficient delivery evidence on its own.
	cursor := svc.findCursorRegistry.Create(snapshot, matches, 0, "target")
	resolution, err := svc.findCursorRegistry.Resolve(cursor)
	if err != nil {
		t.Fatal(err)
	}
	cached := resolution.ResultSet.Snapshot
	cached.findHiddenRecords = map[int]elementRecord{matches[0].Index: matches[0]}
	cached.axSnapshot.Roots, cached.axSnapshot.CanonicalRoots = nil, nil
	svc.exposedHiddenRefs = map[string]map[int]elementRecord{}
	page := findDeliveryFinal(svc.continueFind(cursor, 1))
	if page.IsError || !strings.Contains(resultText(page), `target={"ref":`) {
		t.Fatalf("missing ref-only delivery: %s", resultText(page))
	}
	assertFindExposureDelivered(t, svc, snapshot, page)
}

func TestFindExposureCachedCanonicalIdentity(t *testing.T) {
	t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "24000")
	svc, snapshot, matches := findExposureFixture(t, 4)
	first := svc.renderFindPage(snapshot, matches, 0, 1, "target", nil, false)
	cursor, _ := first.StructuredContent["next_cursor"].(string)
	svc.exposedHiddenRefs = map[string]map[int]elementRecord{}
	before := *snapshot
	before.Elements = append([]elementRecord(nil), snapshot.Elements...)
	before.Elements[3].RuntimeID = append([]int(nil), snapshot.Elements[3].RuntimeID...)
	snapshot.Elements[3].Name = "Live replacement"
	snapshot.Elements[3].RuntimeID[2] = 999
	page := findDeliveryFinal(svc.continueFind(cursor, 1))
	if page.IsError || strings.Contains(resultText(page), "Live replacement") {
		t.Fatalf("cached delivery used live identity: %s", resultText(page))
	}
	assertFindExposureDelivered(t, svc, &before, page)
	note := svc.hiddenCanonicalAddressChangeNote(&before, snapshot)
	if !strings.Contains(note, "1 serializer-hidden ref(s)") {
		t.Fatalf("cached original identity no longer detects rebound: %s", note)
	}
}

func TestFindExposureFailurePreservesEarlierDelivery(t *testing.T) {
	t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "24000")
	svc, snapshot, matches := findExposureFixture(t, 1)
	page := findDeliveryFinal(svc.renderFindPage(snapshot, matches, 0, 1, "target", nil, false))
	assertFindExposureDelivered(t, svc, snapshot, page)
	count := len(svc.exposedHiddenRefs[deliveredSnapshotKey(snapshot)])
	t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "1")
	rejected := svc.renderFindPage(snapshot, matches, 0, 1, "target", nil, false)
	if !rejected.IsError || len(svc.exposedHiddenRefs[deliveredSnapshotKey(snapshot)]) != count {
		t.Fatal("failed later page erased earlier exposure")
	}
	after := *snapshot
	after.Elements = snapshot.Elements[:2]
	if note := svc.hiddenCanonicalAddressChangeNote(snapshot, &after); !strings.Contains(note, "1 serializer-hidden ref(s)") {
		t.Fatalf("earlier genuine delivery lost its invalidation: %s", note)
	}
}

func TestFindExposureCursorBudgetIncludesProvenance(t *testing.T) {
	t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "24000")
	svc, snapshot, matches := findExposureFixture(t, 3)
	page := svc.renderFindPage(snapshot, matches, 0, 1, "target", nil, false)
	cursor, _ := page.StructuredContent["next_cursor"].(string)
	resolution, err := svc.findCursorRegistry.Resolve(cursor)
	if err != nil {
		t.Fatal(err)
	}
	cached := resolution.ResultSet.Snapshot
	without := *cached
	without.findHiddenRecords = nil
	_, base := estimateFindResultSet(&without, matches)
	_, withProvenance := estimateFindResultSet(cached, matches)
	if withProvenance <= base {
		t.Fatal("retained hidden canonical records escaped cursor byte budget")
	}
	svc.findCursorRegistry.byteBudget = base
	if ref := svc.findCursorRegistry.Create(cached, matches, 1, "target"); ref != "" {
		t.Fatal("oversized provenance admitted to cursor cache")
	}
}

func TestFindExposurePagesAndContext(t *testing.T) {
	for _, budget := range []string{"24000", "2000", "1100"} {
		t.Run(budget, func(t *testing.T) {
			t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", budget)
			svc, snapshot, matches := findExposureFixture(t, 8)
			first := findDeliveryFinal(svc.renderFindPage(snapshot, matches, 0, 1, "target", nil, false))
			if first.IsError {
				t.Fatalf("fixture page failed: %s", resultText(first))
			}
			if budget == "1100" && !strings.Contains(resultText(first), "context_omitted=true") {
				t.Fatal("small-budget fixture must omit context")
			}
			assertFindExposureDelivered(t, svc, snapshot, first)
			cursor, _ := findDeliveryModelEvidence(t, first)["next_cursor"].(string)
			if cursor == "" {
				t.Fatal("missing continuation")
			}
			// Check each cached page in isolation; it must carry original hidden
			// provenance without using live provider state or reissuing refs.
			svc.exposedHiddenRefs = map[string]map[int]elementRecord{}
			page := findDeliveryFinal(svc.continueFind(cursor, 1))
			if page.IsError {
				t.Fatalf("cached page failed: %s", resultText(page))
			}
			assertFindExposureDelivered(t, svc, snapshot, page)
			before := len(svc.exposedHiddenRefs[deliveredSnapshotKey(snapshot)])
			replay := findDeliveryFinal(svc.continueFind(cursor, 1))
			assertFindExposureDelivered(t, svc, snapshot, replay)
			if len(svc.exposedHiddenRefs[deliveredSnapshotKey(snapshot)]) != before {
				t.Fatal("replay duplicated exposure")
			}
		})
	}
}

func TestFindExposureGenuineChanges(t *testing.T) {
	for _, kind := range []string{"unchanged", "value_only", "removed", "rebound", "window"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "24000")
			svc, snapshot, matches := findExposureFixture(t, 1)
			// Isolate the matched ref from optional ancestor context.
			snapshot.axSnapshot.ByIndex[1].Ref = ""
			result := findDeliveryFinal(svc.renderFindPage(snapshot, matches, 0, 1, "target", nil, false))
			if result.IsError || len(findDeliveredTargets.FindAllStringSubmatch(resultText(result), -1)) != 1 {
				t.Fatalf("missing genuine delivered target: %s", resultText(result))
			}
			after := *snapshot
			after.Elements = append([]elementRecord(nil), snapshot.Elements...)
			switch kind {
			case "value_only":
				after.Elements[2].Value = "new value"
			case "removed":
				after.Elements = after.Elements[:2]
			case "rebound":
				after.Elements[2].RuntimeID = []int{0, 0, 99}
			case "window":
				after.WindowHandle = "w2:" + strings.Repeat("b", 32)
			}
			note := svc.hiddenCanonicalAddressChangeNote(snapshot, &after)
			wantChange := kind == "removed" || kind == "rebound" || kind == "window"
			if (note != "") != wantChange {
				t.Fatalf("change=%s note=%q", kind, note)
			}
			if wantChange && svc.hiddenCanonicalAddressChangeNote(snapshot, &after) != "" {
				t.Fatal("consumed invalidation reported twice")
			}
		})
	}
}

func TestFindExposureTransport(t *testing.T) {
	for _, protocol := range []string{mcpProtocolLegacy, mcpProtocolStructured} {
		for _, structured := range []string{"0", "1"} {
			t.Run(fmt.Sprintf("%s/%s", protocol, structured), func(t *testing.T) {
				t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "2000")
				t.Setenv("COMPUTER_USE_STRUCTURED_CONTENT", structured)
				svc, snapshot, matches := findExposureFixture(t, 8)
				first := svc.renderFindPage(snapshot, matches, 0, 1, "target", nil, false)
				cursor, _ := first.StructuredContent["next_cursor"].(string)
				svc.protocolVersion = protocol
				svc.exposedHiddenRefs = map[string]map[int]elementRecord{}
				result := findDeliveryWire(t, svc, map[string]any{"mode": "find", "cursor": cursor, "limit": 1})
				if result.IsError {
					t.Fatalf("transport rejected page: %s", resultText(result))
				}
				assertFindExposureDelivered(t, svc, snapshot, result)
			})
		}
	}
}
