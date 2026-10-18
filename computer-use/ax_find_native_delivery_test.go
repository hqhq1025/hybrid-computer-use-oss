package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func findDeliverySnapshot(t testing.TB, value string) (*service, *appSnapshot) {
	t.Helper()
	svc := newServiceWithBackend(&recordingPlatformBackend{})
	snapshot := &appSnapshot{
		App:          appDescriptor{Name: "Independent", PID: 42, ProviderBusName: ":1.42", ProcessGeneration: "42"},
		WindowTitle:  "Selection",
		WindowHandle: "w2:" + strings.Repeat("a", 32),
		SnapshotID:   "find-delivery",
		Elements: []elementRecord{
			{Index: 0, RuntimeID: []int{0}, ControlType: "frame", Name: "Selection"},
			{Index: 1, RuntimeID: []int{0, 0}, ControlType: "entry", Name: "Container", Value: value},
			{Index: 2, RuntimeID: []int{0, 0, 0}, ControlType: "list item", Name: "Target", States: "[has-click-action]"},
		},
		scope: a11yScopeWindow,
	}
	if err := svc.buildAndRenderAXSnapshotV1(snapshot, treeViewWorking); err != nil {
		t.Fatal(err)
	}
	return svc, snapshot
}

func findDeliveryMany(t testing.TB, count int) (*service, *appSnapshot, []elementRecord) {
	t.Helper()
	svc, snapshot := findDeliverySnapshot(t, "")
	snapshot.Elements = snapshot.Elements[:2]
	for i := 0; i < count; i++ {
		snapshot.Elements = append(snapshot.Elements, elementRecord{
			Index: i + 2, RuntimeID: []int{0, 0, i}, ControlType: "list item",
			Name: fmt.Sprintf("Target %02d", i), States: "[has-click-action]",
			Description: strings.Repeat("d", 240),
		})
	}
	if err := svc.buildAndRenderAXSnapshotV1(snapshot, treeViewWorking); err != nil {
		t.Fatal(err)
	}
	return svc, snapshot, snapshot.Elements[2:]
}

func findDeliveryWire(t *testing.T, svc *service, args map[string]any) toolCallResult {
	t.Helper()
	wire := handleMCPRequest(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "AX", "arguments": args},
	}, svc)
	encoded, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Result toolCallResult `json:"result"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	text := resultText(decoded.Result)
	budgetError := decoded.Result.IsError && (strings.Contains(text, "budget exhausted.") ||
		decoded.Result.StructuredContent["failure_code"] == "find_delivery_budget_exhausted")
	textLimit := maxInlineChars()
	if budgetError {
		textLimit = 512
	}
	if !utf8.ValidString(text) || len(text) > textLimit {
		t.Fatalf("final serialized MCP text broke its budget (%d bytes): %.400s", len(text), text)
	}
	// MAX_INLINE_CHARS remains a model-text budget, not a JSON wire-byte cap.
	// encoding/json may expand each content byte to six escaped bytes.
	if len(encoded) > 6*textLimit+4096 {
		t.Fatalf("wire overhead unbounded: wire=%d text=%d", len(encoded), len(text))
	}
	return decoded.Result
}

var findDeliveredTargets = regexp.MustCompile(`(?m)^ - match=(\d+) ref=("r\d+")$`)

func findDeliveryModelEvidence(t *testing.T, result toolCallResult) map[string]any {
	t.Helper()
	if result.StructuredContent != nil {
		return result.StructuredContent
	}
	for _, item := range result.Content {
		if item.Type == "text" && strings.HasPrefix(item.Text, `{"ok":`) {
			var evidence map[string]any
			if err := json.Unmarshal([]byte(item.Text), &evidence); err != nil {
				t.Fatal(err)
			}
			return evidence
		}
	}
	t.Fatal("missing model evidence")
	return nil
}

func TestFindDeliveryLiteralTruncationMarkerThroughTransport(t *testing.T) {
	for _, protocol := range []string{mcpProtocolLegacy, mcpProtocolStructured} {
		for _, structured := range []string{"0", "1"} {
			t.Run(protocol+"/"+structured, func(t *testing.T) {
				t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "24000")
				t.Setenv("COMPUTER_USE_STRUCTURED_CONTENT", structured)
				svc, snapshot, _ := findDeliveryMany(t, 3)
				snapshot.Elements[1].Value = "[TRUNCATED: context]"
				snapshot.Elements[3].Name = "[TRUNCATED: example]"
				snapshot.Elements[3].Value = "[TRUNCATED: value]"
				if err := svc.buildAndRenderAXSnapshotV1(snapshot, treeViewWorking); err != nil {
					t.Fatal(err)
				}
				backend := &recordingPlatformBackend{
					accessibilityResponse: &ProviderAccessibilityResult{OK: true, Snapshot: snapshot},
				}
				svc.backend, svc.protocolVersion = backend, protocol
				direct := findDeliveryWire(t, svc, map[string]any{
					"mode": "find", "app": "Independent",
					"query": map[string]any{"name_equals": "[TRUNCATED: example]", "role": "listitem"},
				})
				if direct.IsError || len(findDeliveredTargets.FindAllStringSubmatch(resultText(direct), -1)) != 1 {
					t.Fatalf("literal marker rejected exact match: %s", resultText(direct))
				}
				if !strings.Contains(resultText(direct), "[TRUNCATED: context]") ||
					findDeliveryModelEvidence(t, direct)["tree_complete"] == false {
					t.Fatalf("literal context was omitted or classified as truncation: %s", resultText(direct))
				}
				value := findDeliveryWire(t, svc, map[string]any{
					"mode": "find", "app": "Independent",
					"query": map[string]any{"text_equals": "[TRUNCATED: value]", "role": "listitem"},
				})
				if value.IsError || !strings.Contains(resultText(value), `matched_text="[TRUNCATED: value]"`) {
					t.Fatalf("literal matched value rejected: %s", resultText(value))
				}
				first := findDeliveryWire(t, svc, map[string]any{
					"mode": "find", "app": "Independent",
					"query": map[string]any{"role": "listitem"}, "limit": 1,
				})
				cursor, _ := findDeliveryModelEvidence(t, first)["next_cursor"].(string)
				if cursor == "" {
					t.Fatalf("missing first-page cursor: %s", resultText(first))
				}
				reads := len(backend.observeRequests)
				for ordinal := 2; ordinal <= 3; ordinal++ {
					page := findDeliveryWire(t, svc, map[string]any{"mode": "find", "cursor": cursor, "limit": 1})
					targets := findDeliveredTargets.FindAllStringSubmatch(resultText(page), -1)
					evidence := findDeliveryModelEvidence(t, page)
					if page.IsError || len(targets) != 1 || targets[0][1] != fmt.Sprint(ordinal) ||
						evidence["find_page_count"] != float64(1) || evidence["tree_complete"] == false {
						t.Fatalf("literal marker stuck later page %d: %s", ordinal, resultText(page))
					}
					cursor, _ = evidence["next_cursor"].(string)
				}
				if cursor != "" || len(backend.observeRequests) != reads {
					t.Fatal("literal marker pagination refreshed provider or failed to finish")
				}
			})
		}
	}
}

func TestFindDeliveryTypedBudgetStillRejectsActualCuts(t *testing.T) {
	t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "24000")
	makeResult := func(text string) toolCallResult {
		result := textResult(text, false)
		result.StructuredContent = map[string]any{"find_delivery_complete": true}
		return result
	}
	literal := makeResult("[TRUNCATED: example]")
	if !findDeliveryFits(literal) {
		t.Fatal("literal text was treated as a budget effect")
	}
	// Below the legacy byte cap, but above the final content budget after its
	// evidence reserve. This must still reject a genuine partial delivery.
	oversized := makeResult(strings.Repeat("x", 23000))
	if findDeliveryFits(oversized) {
		t.Fatal("actual final budget cut was accepted")
	}
	before := resultText(oversized)
	cut := enforceAXResponseBudget(oversized)
	if cut.StructuredContent["truncation_reason"] != "response_budget" ||
		resultText(cut) == before {
		t.Fatal("actual cut was not recorded as typed budget state")
	}
	repeated := enforceAXResponseBudget(cut)
	if repeated.StructuredContent["truncation_reason"] != "response_budget" ||
		findDeliveryFits(repeated) {
		t.Fatal("a second budget pass lost the prior real truncation")
	}
}

func findDeliveryFinal(result toolCallResult) toolCallResult {
	result = withA11yModeEvidence(result, a11yModeFind)
	result = enforceAXResponseBudget(result)
	result = normalizeAXEvidenceInvariants(result)
	return withAXTextEvidence(prepareAXEvidence(result))
}

func TestFindDeliveryLongAncestorThroughFinalEvidence(t *testing.T) {
	t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "24000")
	t.Setenv("COMPUTER_USE_OUTPUT_MODE", "stdout")
	value := strings.Repeat("x", 30000)
	svc, snapshot := findDeliverySnapshot(t, value)
	result := svc.renderFindPage(snapshot, snapshot.Elements[2:], 0, 2,
		`role~"listitem" name="Target"`, nil, false)
	final := findDeliveryFinal(result)
	text := resultText(final)
	if final.IsError || !strings.Contains(text, `listitem "Target"`) ||
		!strings.Contains(text, ` ref="r`) {
		t.Fatalf("find lost the matched target at the model boundary:\n%s", text)
	}
	if strings.Contains(text, "[TRUNCATED:") || len([]rune(text)) > maxInlineChars() {
		t.Fatalf("find delivery was not budgeted before the final response: %d", len([]rune(text)))
	}
	if snapshot.axSnapshot.ByIndex[1].Properties.Value != value ||
		snapshot.Elements[1].Value != value {
		t.Fatal("find delivery changed canonical data")
	}
}

func TestFindDeliveryActualTransportProtocols(t *testing.T) {
	for _, protocol := range []string{mcpProtocolLegacy, mcpProtocolStructured} {
		for _, structured := range []string{"0", "1"} {
			t.Run(protocol+"/"+structured, func(t *testing.T) {
				t.Setenv("COMPUTER_USE_STRUCTURED_CONTENT", structured)
				t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "24000")
				t.Setenv("COMPUTER_USE_OUTPUT_MODE", "stdout")
				svc, snapshot := findDeliverySnapshot(t, strings.Repeat("x", 30000))
				backend := &recordingPlatformBackend{
					accessibilityResponse: &ProviderAccessibilityResult{OK: true, Snapshot: snapshot},
				}
				svc.backend, svc.protocolVersion = backend, protocol
				result := findDeliveryWire(t, svc, map[string]any{
					"mode": "find", "app": "Independent",
					"query": map[string]any{"name_equals": "Target", "role": "listitem"}, "limit": 2,
				})
				text := resultText(result)
				if result.IsError || len(findDeliveredTargets.FindAllStringSubmatch(text, -1)) != 1 {
					t.Fatalf("transport lost unique matched target: %s", text)
				}
				if strings.Contains(text, "recover=find|root") {
					t.Fatal("successful find repeats unusable find recovery")
				}
				if protocol == mcpProtocolLegacy || structured == "0" {
					if result.StructuredContent != nil || !strings.Contains(text, `"find_page_count":1`) {
						t.Fatalf("legacy delivery evidence missing: %s", text)
					}
				} else if result.StructuredContent["find_page_count"] != float64(1) {
					t.Fatalf("structured delivery evidence missing: %#v", result.StructuredContent)
				}
				if len(backend.observeRequests) != 1 {
					t.Fatalf("find read count=%d", len(backend.observeRequests))
				}
				svc, snapshot, matches := findDeliveryMany(t, 8)
				svc.protocolVersion = protocol
				t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "3800")
				cursor := svc.findCursorRegistry.Create(snapshot, matches, 0, "listitem")
				page := findDeliveryWire(t, svc, map[string]any{"mode": "find", "cursor": cursor, "limit": 50})
				if page.IsError || len(findDeliveredTargets.FindAllStringSubmatch(resultText(page), -1)) == 0 {
					t.Fatalf("protocol lost budgeted cursor page: %s", resultText(page))
				}
			})
		}
	}
}

func TestFindDeliveryBudgetPagesFreezeAndReplay(t *testing.T) {
	t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "3800")
	t.Setenv("COMPUTER_USE_STRUCTURED_CONTENT", "1")
	svc, snapshot, matches := findDeliveryMany(t, 8)
	first := svc.renderFindPage(snapshot, matches, 0, 50, `role~"listitem"`, nil, false)
	count, _ := intEvidence(first.StructuredContent["find_page_count"])
	if first.IsError || count < 1 || count >= len(matches) {
		t.Fatalf("expected budget-driven page even limit >= total: count=%d %s", count, resultText(first))
	}
	cursor, _ := first.StructuredContent["next_cursor"].(string)
	resolution, err := svc.findCursorRegistry.Resolve(cursor)
	if err != nil || resolution.Cursor.Offset != count {
		t.Fatalf("cursor does not start after complete delivered units: %#v %v", resolution, err)
	}
	targets := findDeliveredTargets.FindAllStringSubmatch(resultText(findDeliveryFinal(first)), -1)
	if len(targets) != count {
		t.Fatalf("page count=%d but delivered units=%d", count, len(targets))
	}
	// Fresh canonical state cannot alter a cached result set or its original refs.
	snapshot.axSnapshot.ByIndex[count+2].Name = "REPLACED LIVE"
	snapshot.Elements[count+2].Name = "REPLACED LIVE"
	backend := &recordingPlatformBackend{err: fmt.Errorf("cursor must not call provider")}
	svc.backend, svc.protocolVersion = backend, mcpProtocolStructured
	seen := map[string]bool{}
	for _, target := range targets {
		seen[target[1]] = true
	}
	for cursor != "" {
		args := map[string]any{"mode": "find", "cursor": cursor, "limit": 50}
		page := findDeliveryWire(t, svc, args)
		replay := findDeliveryWire(t, svc, args)
		got := findDeliveredTargets.FindAllStringSubmatch(resultText(page), -1)
		repeated := findDeliveredTargets.FindAllStringSubmatch(resultText(replay), -1)
		if page.IsError || !reflect.DeepEqual(got, repeated) || strings.Contains(resultText(page), "REPLACED LIVE") {
			t.Fatalf("cached replay changed targets: %s", resultText(page))
		}
		pageCount, _ := intEvidence(page.StructuredContent["find_page_count"])
		if pageCount != len(got) || pageCount == 0 {
			t.Fatalf("page metadata disagrees with delivered units: %#v", page)
		}
		for _, target := range got {
			if seen[target[1]] {
				t.Fatalf("duplicate ordinal %s", target[1])
			}
			seen[target[1]] = true
		}
		cursor, _ = page.StructuredContent["next_cursor"].(string)
		if cursor != "" {
			next, err := svc.findCursorRegistry.Resolve(cursor)
			if err != nil || next.Cursor.Offset != len(seen) {
				t.Fatalf("offset skipped undelivered match: %#v %v", next, err)
			}
		}
	}
	if len(seen) != 8 || len(backend.calls) != 0 {
		t.Fatalf("cached pagination coverage=%d backend calls=%v", len(seen), backend.calls)
	}
}

func TestFindDeliveryLongEscapedIdentityAndMatchedTail(t *testing.T) {
	t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "7000")
	long := strings.Repeat("界\\\"\n", 600) + "TAIL-IDENTITY"
	svc, snapshot := findDeliverySnapshot(t, "")
	snapshot.WindowTitle = long
	snapshot.Elements[0].Name = long
	snapshot.Elements[2].Name = long
	snapshot.Elements[2].ProviderBusName = ":1.42"
	snapshot.Elements[2].ProviderObjectPath = "/independent/target"
	snapshot.Elements[2].ProcessGeneration = "42"
	snapshot.Elements[2].Value = strings.Repeat("v", 30000)
	snapshot.Elements[2].Description = strings.Repeat("d", 30000) + "TAIL-MATCH"
	if err := svc.buildAndRenderAXSnapshotV1(snapshot, treeViewWorking); err != nil {
		t.Fatal(err)
	}
	snapshot.axSnapshot.ByIndex[2].Properties.Omitted = "provider_children_unavailable"
	canonicalName := snapshot.axSnapshot.ByIndex[2].Name
	record := snapshot.Elements[2]
	field, text, ok := matchedFindTextField(&record, "TAIL-MATCH", false)
	if !ok || text != record.Description {
		t.Fatal("matched field was shortened before delivery")
	}
	record.FindMatchedField, record.FindMatchedText = field, text
	query := `name=` + long
	result := svc.renderFindPage(snapshot, []elementRecord{record}, 0, 2, query,
		[]string{"[expect] " + strings.Repeat("note ", 80)}, true)
	final := findDeliveryFinal(result)
	output := resultText(final)
	for _, want := range []string{
		"TAIL-IDENTITY", "TAIL-MATCH", "header_excerpt=true", "display_excerpt=name",
		"provider_children_unavailable", "matched_field=description", "matched_text_truncated=true",
		`"current_state_cause":"independent"`, ` ref="r`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("lost %q in final response: %s", want, output)
		}
	}
	if final.IsError || len(output) > maxInlineChars() || !utf8.ValidString(output) {
		t.Fatalf("invalid escaped/multibyte delivery: %s", output)
	}
	if snapshot.axSnapshot.ByIndex[2].Name != canonicalName ||
		snapshot.axSnapshot.ByIndex[2].Properties.Omitted != "provider_children_unavailable" {
		t.Fatal("excerpt mutated canonical identity or omission evidence")
	}
	target := findDeliveredTargets.FindStringSubmatch(output)
	var ref string
	if len(target) != 3 || json.Unmarshal([]byte(target[2]), &ref) != nil {
		t.Fatal("invalid target ref")
	}
	entry := svc.refRegistry.entries[ref]
	if entry == nil || entry.Name != record.Name || entry.WindowHandle != snapshot.WindowHandle {
		t.Fatalf("display excerpt changed ref identity: %#v", entry)
	}
	for _, protocol := range []string{mcpProtocolLegacy, mcpProtocolStructured} {
		for _, structured := range []string{"0", "1"} {
			t.Setenv("COMPUTER_USE_STRUCTURED_CONTENT", structured)
			svc.protocolVersion = protocol
			t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "7000")
			svc.backend = &recordingPlatformBackend{
				accessibilityResponse: &ProviderAccessibilityResult{OK: true, Snapshot: snapshot},
			}
			longTarget := findDeliveryWire(t, svc, map[string]any{
				"mode": "find", "app": "Independent",
				"query": map[string]any{"text_contains": "TAIL-MATCH", "role": "listitem"}, "limit": 2,
			})
			if longTarget.IsError || !strings.Contains(resultText(longTarget), "TAIL-MATCH") ||
				len(findDeliveredTargets.FindAllStringSubmatch(resultText(longTarget), -1)) != 1 {
				t.Fatalf("long identity/matched text lost in actual transport: %s", resultText(longTarget))
			}
			// Freeze the original complete identity through the production find path.
			t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "3600")
			svc2, pageSnapshot, matches := findDeliveryMany(t, 8)
			svc2.protocolVersion = protocol
			pageSnapshot.WindowTitle = long
			first := svc2.renderFindPage(pageSnapshot, matches, 0, 1, query, nil, false)
			cursor, ok := first.StructuredContent["next_cursor"].(string)
			if first.IsError || !ok {
				t.Fatalf("long header failed to produce cached page: %s", resultText(first))
			}
			page := findDeliveryWire(t, svc2, map[string]any{"mode": "find", "cursor": cursor, "limit": 50})
			if page.IsError || !strings.Contains(resultText(page), "header_excerpt=true") {
				t.Fatalf("long header lost at transport: %s", resultText(page))
			}
		}
	}
}

func TestFindDeliveryUnaddressableAndTinyBudgets(t *testing.T) {
	svc, snapshot := findDeliverySnapshot(t, "")
	node := snapshot.axSnapshot.ByIndex[2]
	node.Ref, node.SemanticTarget, node.LocatorKind = "", "", ""
	node.SemanticIdentityAmbiguous = true
	result := svc.renderFindPage(snapshot, snapshot.Elements[2:], 0, 2, "target", nil, false)
	text := resultText(findDeliveryFinal(result))
	if result.IsError || !strings.Contains(text, "addressable=false reason=stable_identity_unavailable") ||
		strings.Contains(text, ` ref="r`) {
		t.Fatalf("unaddressability not delivered honestly: %s", text)
	}
	for _, budget := range []string{"1", "32", "128", "256"} {
		t.Run(budget, func(t *testing.T) {
			t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", budget)
			svc, snapshot := findDeliverySnapshot(t, "")
			result := svc.renderFindPage(snapshot, snapshot.Elements[2:], 0, 2, "target", nil, false)
			final := findDeliveryFinal(result)
			if !final.IsError || len(resultText(final)) > 512 ||
				strings.Contains(resultText(final), ` ref="r`) ||
				len(svc.findCursorRegistry.entries) != 0 || len(svc.findCursorRegistry.resultSets) != 0 {
				t.Fatalf("tiny budget exposed partial unit or cursor: %#v text=%s", final, resultText(final))
			}
			// No-delivery continuation must leave its input cursor unchanged.
			for _, protocol := range []string{mcpProtocolLegacy, mcpProtocolStructured} {
				for _, structured := range []string{"0", "1"} {
					t.Setenv("COMPUTER_USE_STRUCTURED_CONTENT", structured)
					svc.protocolVersion = protocol
					cursor := svc.findCursorRegistry.Create(snapshot, snapshot.Elements[2:], 0, "target")
					before := len(svc.findCursorRegistry.entries)
					page := findDeliveryWire(t, svc, map[string]any{"mode": "find", "cursor": cursor, "limit": 2})
					resolution, err := svc.findCursorRegistry.Resolve(cursor)
					if !page.IsError || err != nil || resolution.Cursor.Offset != 0 ||
						len(svc.findCursorRegistry.entries) != before {
						t.Fatalf("tiny budget advanced a cursor: %#v %v", page, err)
					}
					payload, _ := json.Marshal(page)
					if strings.Contains(string(payload), `"preflight_only":true`) ||
						!strings.Contains(resultText(page)+string(payload), "cached_observation_page") {
						t.Fatalf("cached no-fit error lost truthful scope: %s", payload)
					}
				}
			}
			for _, protocol := range []string{mcpProtocolLegacy, mcpProtocolStructured} {
				for _, structured := range []string{"0", "1"} {
					t.Setenv("COMPUTER_USE_STRUCTURED_CONTENT", structured)
					svc, snapshot := findDeliverySnapshot(t, "")
					backend := &recordingPlatformBackend{
						accessibilityResponse: &ProviderAccessibilityResult{OK: true, Snapshot: snapshot},
					}
					svc.backend, svc.protocolVersion = backend, protocol
					page := findDeliveryWire(t, svc, map[string]any{
						"mode": "find", "app": "Independent", "query": map[string]any{"name_equals": "Target"},
					})
					payload, _ := json.Marshal(page)
					if !page.IsError || len(backend.observeRequests) != 1 ||
						strings.Contains(string(payload), `"preflight_only":true`) ||
						!strings.Contains(resultText(page)+string(payload), "independent_ui_read") {
						t.Fatalf("sampled no-fit error lost truthful scope or reread: %s calls=%v", payload, backend.calls)
					}
				}
			}
		})
	}
}

func TestFindDeliveryDiagnosticsCannotBeSilentlyDropped(t *testing.T) {
	t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "1000")
	svc, snapshot := findDeliverySnapshot(t, "")
	result := svc.renderFindPage(snapshot, snapshot.Elements[2:], 0, 2, "target",
		[]string{"[expect] " + strings.Repeat("important ", 1000)}, true)
	if !result.IsError || result.StructuredContent["next_cursor"] != nil ||
		strings.Contains(resultText(findDeliveryFinal(result)), " ref=\"r") {
		t.Fatalf("oversized required diagnostics silently dropped: %s", resultText(result))
	}
}

func TestFindDeliveryMissingCanonicalNodeAndRefFailure(t *testing.T) {
	t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "24000")
	_, snapshot := findDeliverySnapshot(t, "")
	node := snapshot.axSnapshot.ByIndex[2]
	node.FindMatch, node.Ref = true, ""
	unit := findMatchUnit(snapshot.axSnapshot, snapshot.Elements[2], 1)
	if !strings.Contains(unit, "reason=stable_ref_unavailable") {
		t.Fatalf("eligible but unissued ref has empty reason: %s", unit)
	}
	delete(snapshot.axSnapshot.ByIndex, 2)
	unit = findMatchUnit(snapshot.axSnapshot, snapshot.Elements[2], 1)
	if !strings.Contains(unit, "canonical_node_unavailable") || strings.Contains(unit, " ref=\"r") {
		t.Fatalf("missing canonical node received fabricated target: %s", unit)
	}
}

func TestFindDeliveryWireEscapingIsSeparateFromTextBudget(t *testing.T) {
	t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "24000")
	t.Setenv("COMPUTER_USE_STRUCTURED_CONTENT", "0")
	svc, snapshot, matches := findDeliveryMany(t, 20)
	for i := range matches {
		matches[i].Name = fmt.Sprintf("%02d", i) + strings.Repeat("<", 240)
		matches[i].Description = ""
		snapshot.Elements[i+2] = matches[i]
	}
	if err := svc.buildAndRenderAXSnapshotV1(snapshot, treeViewWorking); err != nil {
		t.Fatal(err)
	}
	svc.backend = &recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{OK: true, Snapshot: snapshot},
	}
	result := findDeliveryWire(t, svc, map[string]any{
		"mode": "find", "app": "Independent", "query": map[string]any{"role": "listitem"}, "limit": 20,
	})
	wire, _ := json.Marshal(jsonRPCResult(1, result))
	t.Logf("decoded_text_bytes=%d decoded_runes=%d encoded_wire_bytes=%d cap=%d",
		len(resultText(result)), len([]rune(resultText(result))), len(wire), maxInlineChars())
	if result.IsError || len(wire) <= maxInlineChars() {
		t.Fatalf("escaping fixture did not exercise independent wire overhead: %s", resultText(result))
	}
}

func TestFindDeliveryCursorStorageFailureAndExpiry(t *testing.T) {
	t.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "3800")
	svc, snapshot, matches := findDeliveryMany(t, 8)
	svc.findCursorRegistry.byteBudget = 1
	result := svc.renderFindPage(snapshot, matches, 0, 50, "target", nil, false)
	if result.IsError || !strings.Contains(resultText(result), "continuation=unavailable_budget") ||
		result.StructuredContent["next_cursor"] != nil {
		t.Fatalf("cursor storage failure hidden: %s", resultText(result))
	}
	svc.findCursorRegistry = NewFindCursorRegistry(1)
	first := svc.renderFindPage(snapshot, matches, 0, 1, "target", nil, false)
	cursor := first.StructuredContent["next_cursor"].(string)
	svc.renderFindPage(snapshot, matches, 0, 1, "target", nil, false)
	if page := svc.continueFind(cursor, 1); !page.IsError || !strings.Contains(resultText(page), "expired") {
		t.Fatalf("evicted cursor was usable: %s", resultText(page))
	}
	last := svc.renderFindPage(snapshot, matches, 0, 1, "target", nil, false)
	cursor = last.StructuredContent["next_cursor"].(string)
	resolution, _ := svc.findCursorRegistry.Resolve(cursor)
	resolution.ResultSet.CreatedAt = time.Now().Add(-defaultAXFindCursorAbsoluteTTL - time.Second)
	if page := svc.continueFind(cursor, 1); !page.IsError || !strings.Contains(resultText(page), "expired") {
		t.Fatalf("expired cursor was usable: %s", resultText(page))
	}
}

func BenchmarkFindDeliveryContext(b *testing.B) {
	b.Setenv("COMPUTER_USE_MAX_INLINE_CHARS", "24000")
	svc, snapshot, matches := findDeliveryMany(b, 50)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result := svc.renderFindPage(snapshot, matches, 0, 50, "target", nil, false)
		if result.IsError {
			b.Fatal(resultText(result))
		}
	}
}
