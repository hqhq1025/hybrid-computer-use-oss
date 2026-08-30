package main

import (
	"embed"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

//go:embed testdata/provider-v2/*.json
var providerV2FixtureFS embed.FS

type providerV2FixtureManifest struct {
	SchemaVersion         int                      `json:"schema_version"`
	Protocol              string                   `json:"protocol"`
	RequiredOperations    []ProviderWireOperation  `json:"required_operations"`
	RequiredFailurePhases []string                 `json:"required_failure_phases"`
	Fixtures              []providerV2FixtureEntry `json:"fixtures"`
}

type providerV2FixtureEntry struct {
	Name      string `json:"name"`
	Direction string `json:"direction"`
	File      string `json:"file"`
}

type providerV2InvalidFixture struct {
	Name    string          `json:"name"`
	Request json.RawMessage `json:"request"`
}

func TestProviderV2FixtureCorpus(t *testing.T) {
	manifestData, err := providerV2FixtureFS.ReadFile(
		"testdata/provider-v2/manifest.json",
	)
	if err != nil {
		t.Fatal(err)
	}
	var manifest providerV2FixtureManifest
	if err := strictFixtureDecode(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	wantOperations := []ProviderWireOperation{
		ProviderWireObserveApps,
		ProviderWireObserve,
		ProviderWireResolveAndAct,
		ProviderWireDesktopBatch,
		ProviderWireWindowList,
		ProviderWireWindowAction,
	}
	if manifest.SchemaVersion != providerProtocolV2 {
		t.Fatalf("schema version = %d, want %d",
			manifest.SchemaVersion, providerProtocolV2)
	}
	if manifest.Protocol != providerProtocolName {
		t.Fatalf("protocol = %q, want %q",
			manifest.Protocol, providerProtocolName)
	}
	if !reflect.DeepEqual(manifest.RequiredOperations, wantOperations) {
		t.Fatalf("required operations = %v, want %v",
			manifest.RequiredOperations, wantOperations)
	}
	if !reflect.DeepEqual(
		manifest.RequiredFailurePhases,
		[]string{"precondition", "emission", "timeout"},
	) {
		t.Fatalf("required failure phases = %v",
			manifest.RequiredFailurePhases)
	}

	seenOperations := make(map[ProviderWireOperation]bool)
	seenFailurePhases := make(map[string]bool)
	for _, fixture := range manifest.Fixtures {
		fixture := fixture
		t.Run(fixture.Name, func(t *testing.T) {
			path := "testdata/provider-v2/" + fixture.File
			data, err := providerV2FixtureFS.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch fixture.Direction {
			case "request":
				request, err := DecodeProviderWireRequest(data)
				if err != nil {
					t.Fatal(err)
				}
				seenOperations[request.Operation] = true
				assertProviderFixtureRoundTrip(t, data, request)
			case "response":
				response, err := DecodeProviderWireResponse(data)
				if err != nil {
					t.Fatal(err)
				}
				seenOperations[response.Operation] = true
				switch {
				case response.Accessibility != nil &&
					response.Accessibility.Failure != nil:
					seenFailurePhases[response.Accessibility.Failure.Phase] = true
				case response.Desktop != nil &&
					response.Desktop.Failure != nil:
					seenFailurePhases[response.Desktop.Failure.Phase] = true
				case response.Window != nil &&
					response.Window.Failure != nil:
					seenFailurePhases[response.Window.Failure.Phase] = true
				}
				assertProviderFixtureRoundTrip(t, data, response)
			default:
				t.Fatalf("unknown fixture direction %q", fixture.Direction)
			}
		})
	}
	for _, operation := range manifest.RequiredOperations {
		if !seenOperations[operation] {
			t.Errorf("operation %q has no fixture", operation)
		}
	}
	for _, phase := range manifest.RequiredFailurePhases {
		if !seenFailurePhases[phase] {
			t.Errorf("failure phase %q has no fixture", phase)
		}
	}
}

func assertProviderFixtureRoundTrip(t *testing.T, original []byte, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	assertFixtureJSONEquivalent(t, original, encoded)
}

func TestProviderV2StrictDecodeRejectsUnknownFields(t *testing.T) {
	data := []byte(`{
		"protocol":"osw-provider-v2",
		"schema_version":2,
		"request_id":"req-unknown",
		"operation":"observe_apps",
		"observe_apps":{"protocol_version":2},
		"unexpected":true
	}`)
	_, err := DecodeProviderWireRequest(data)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("strict decode error = %v, want unknown field", err)
	}
}

func TestProviderV2SharedInvalidRequestCorpus(t *testing.T) {
	data, err := providerV2FixtureFS.ReadFile(
		"testdata/provider-v2/invalid-requests.json",
	)
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []providerV2InvalidFixture
	if err := strictFixtureDecode(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) == 0 {
		t.Fatal("invalid request corpus is empty")
	}
	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.Name, func(t *testing.T) {
			if _, err := DecodeProviderWireRequest(fixture.Request); err == nil {
				t.Fatalf(
					"Go Provider V2 decoder accepted invalid request: %s",
					fixture.Request,
				)
			}
		})
	}
}

func TestProviderV2ValidationRejectsAmbiguousOrMismatchedPayloads(t *testing.T) {
	tests := []struct {
		name    string
		request ProviderWireRequest
		want    string
	}{
		{
			name: "missing payload",
			request: newProviderWireRequest(
				"req-missing",
				ProviderWireObserve,
			),
			want: "exactly one payload",
		},
		{
			name: "multiple payloads",
			request: func() ProviderWireRequest {
				request := newProviderWireRequest(
					"req-multiple",
					ProviderWireObserve,
				)
				request.Observe = &ProviderObserveRequest{
					ProtocolVersion: providerProtocolV2,
					App:             "Editor",
				}
				request.WindowList = &ProviderWindowListRequest{
					ProtocolVersion: providerProtocolV2,
					App:             "Editor",
				}
				return request
			}(),
			want: "exactly one payload",
		},
		{
			name: "operation mismatch",
			request: func() ProviderWireRequest {
				request := newProviderWireRequest(
					"req-mismatch",
					ProviderWireObserve,
				)
				request.WindowList = &ProviderWindowListRequest{
					ProtocolVersion: providerProtocolV2,
					App:             "Editor",
				}
				return request
			}(),
			want: "does not match",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			err := test.request.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestProviderV2DecoderRejectsTrailingJSON(t *testing.T) {
	_, err := DecodeProviderWireRequest(
		[]byte(`{"protocol":"osw-provider-v2","schema_version":2,` +
			`"request_id":"req","operation":"observe_apps",` +
			`"observe_apps":{"protocol_version":2}} {}`),
	)
	if err == nil {
		t.Fatal("strict fixture decoder accepted trailing JSON")
	}
}

func TestProviderV2ResponseDecoderRejectsUnknownSnapshotField(t *testing.T) {
	_, err := DecodeProviderWireResponse([]byte(
		`{"protocol":"osw-provider-v2","schema_version":2,` +
			`"request_id":"req","operation":"observe",` +
			`"accessibility":{"ok":true,"snapshot":{` +
			`"windowTitle":"Editor","unexpectedSelection":{}}}}`,
	))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("decoder error = %v, want unknown field", err)
	}
}

func TestProviderV2SnapshotAcceptsTypedWindowSelection(t *testing.T) {
	response, err := DecodeProviderWireResponse([]byte(
		`{"protocol":"osw-provider-v2","schema_version":2,` +
			`"request_id":"req","operation":"observe",` +
			`"accessibility":{"ok":true,"snapshot":{` +
			`"windowTitle":"Editor","windowSelection":{` +
			`"reason":"active","candidateCount":1,` +
			`"candidates":[{"index":0,"title":"Editor","selected":true}]}}}}`,
	))
	if err != nil {
		t.Fatalf("DecodeProviderWireResponse() error = %v", err)
	}
	selection := response.Accessibility.Snapshot.WindowSelection
	if selection == nil || selection.Reason != "active" {
		t.Fatalf("window selection = %#v, want reason active", selection)
	}
	if selection.CandidateCount != 1 ||
		len(selection.Candidates) != 1 ||
		!selection.Candidates[0].Selected {
		t.Fatalf("window selection candidates = %#v", selection)
	}
}

func TestProviderV2ResolvedElementAcceptsTypedRuntimeAliases(t *testing.T) {
	response, err := DecodeProviderWireResponse([]byte(
		`{"protocol":"osw-provider-v2","schema_version":2,` +
			`"request_id":"req","operation":"resolve_and_act",` +
			`"accessibility":{"ok":true,"resolved_element":{` +
			`"index":0,"runtimeId":[0,1],"runtimeAliases":[[0,1],[2,1]]}}}`,
	))
	if err != nil {
		t.Fatalf("DecodeProviderWireResponse() error = %v", err)
	}
	element := response.Accessibility.ResolvedElement
	if element == nil || len(element.RuntimeIDAliases) != 2 {
		t.Fatalf("resolved element aliases = %#v, want two aliases", element)
	}
}

func TestProviderV2ResponseAcceptsTypedRecoveryContext(t *testing.T) {
	response, err := DecodeProviderWireResponse([]byte(
		`{"protocol":"osw-provider-v2","schema_version":2,` +
			`"request_id":"req","operation":"resolve_and_act",` +
			`"accessibility":{"ok":false,` +
			`"failure":{"code":"effect_unknown",` +
			`"phase":"post_action_observation"},` +
			`"mutation":{"input_events_emitted":true,` +
			`"mutation_unknown":true},` +
			`"recovery_context":{"sourceAppStatus":"alive",` +
			`"sourceWindowStatus":"gone","inventoryComplete":true,` +
			`"currentWindow":{"windowHandle":` +
			`"w2:fedcba9876543210fedcba9876543210",` +
			`"title":"Document Recovery","role":"dialog",` +
			`"providerBusName":":1.42",` +
			`"providerObjectPath":"/org/a11y/window/1",` +
			`"processGeneration":"42:100"}}}}`,
	))
	if err != nil {
		t.Fatalf("DecodeProviderWireResponse() error = %v", err)
	}
	recovery := response.Accessibility.RecoveryContext
	if recovery == nil ||
		recovery.SourceAppStatus != "alive" ||
		recovery.SourceWindowStatus != "gone" ||
		!recovery.InventoryComplete ||
		recovery.CurrentWindow == nil ||
		recovery.CurrentWindow.Title != "Document Recovery" {
		t.Fatalf("recovery context = %#v", recovery)
	}
}

func TestProviderV2ResponseRejectsInvalidRecoveryContext(t *testing.T) {
	tests := []string{
		`{"protocol":"osw-provider-v2","schema_version":2,` +
			`"request_id":"req","operation":"resolve_and_act",` +
			`"accessibility":{"ok":false,"recovery_context":{` +
			`"sourceAppStatus":"maybe"}}}`,
		`{"protocol":"osw-provider-v2","schema_version":2,` +
			`"request_id":"req","operation":"resolve_and_act",` +
			`"accessibility":{"ok":false,"recovery_context":{` +
			`"sourceAppStatus":"present"}}}`,
		`{"protocol":"osw-provider-v2","schema_version":2,` +
			`"request_id":"req","operation":"resolve_and_act",` +
			`"accessibility":{"ok":false,"recovery_context":{` +
			`"sourceWindowStatus":"alive"}}}`,
		`{"protocol":"osw-provider-v2","schema_version":2,` +
			`"request_id":"req","operation":"resolve_and_act",` +
			`"accessibility":{"ok":false,"recovery_context":{` +
			`"sourceWindowStatus":"gone","inventoryComplete":true,` +
			`"currentWindow":{"windowHandle":"w2:not-private"}}}}`,
		`{"protocol":"osw-provider-v2","schema_version":2,` +
			`"request_id":"req","operation":"resolve_and_act",` +
			`"accessibility":{"ok":false,"recovery_context":{` +
			`"sourceAppStatus":"alive","sourceWindowStatus":"unknown",` +
			`"currentWindow":{"windowHandle":` +
			`"w2:fedcba9876543210fedcba9876543210"}}}}`,
	}
	for _, input := range tests {
		if _, err := DecodeProviderWireResponse([]byte(input)); err == nil {
			t.Fatalf("invalid recovery context accepted: %s", input)
		}
	}
}

func TestProviderV2ObserveStaleWindowRecoveryRoundTrip(t *testing.T) {
	response, err := DecodeProviderWireResponse([]byte(
		`{"protocol":"osw-provider-v2","schema_version":2,` +
			`"request_id":"req","operation":"observe",` +
			`"accessibility":{"ok":false,` +
			`"failure":{"code":"stale_window_ref","phase":"precondition",` +
			`"facts_complete":true,"subject":"window"},` +
			`"recovery_context":{"sourceAppStatus":"alive",` +
			`"sourceWindowStatus":"gone","inventoryComplete":true,` +
			`"currentWindow":{"windowHandle":` +
			`"w2:fedcba9876543210fedcba9876543210",` +
			`"title":"Document","role":"frame",` +
			`"providerBusName":":1.42",` +
			`"providerObjectPath":"/org/a11y/window/1",` +
			`"processGeneration":"42:100"}}}}`,
	))
	if err != nil {
		t.Fatalf("DecodeProviderWireResponse() error = %v", err)
	}
	recovery := response.Accessibility.RecoveryContext
	if recovery == nil ||
		recovery.SourceWindowStatus != "gone" ||
		recovery.CurrentWindow == nil ||
		recovery.CurrentWindow.Title != "Document" {
		t.Fatalf("observe recovery context = %#v", recovery)
	}
}

func TestProviderV2ResponseRejectsRecoveryOutsideEffectUnknown(t *testing.T) {
	tests := []string{
		`{"protocol":"osw-provider-v2","schema_version":2,` +
			`"request_id":"req","operation":"observe",` +
			`"accessibility":{"ok":false,"failure":{"code":"effect_unknown",` +
			`"phase":"post_action_observation"},"mutation":{` +
			`"input_events_emitted":true,"mutation_unknown":true},` +
			`"recovery_context":{"sourceAppStatus":"unknown"}}}`,
		`{"protocol":"osw-provider-v2","schema_version":2,` +
			`"request_id":"req","operation":"observe",` +
			`"accessibility":{"ok":false,"failure":{` +
			`"code":"stale_window_ref","phase":"precondition",` +
			`"facts_complete":false},"recovery_context":{` +
			`"sourceAppStatus":"unknown"}}}`,
		`{"protocol":"osw-provider-v2","schema_version":2,` +
			`"request_id":"req","operation":"observe",` +
			`"accessibility":{"ok":false,"failure":{` +
			`"code":"stale_window_ref","phase":"precondition",` +
			`"facts_complete":true},"mutation":{` +
			`"input_events_emitted":true},"recovery_context":{` +
			`"sourceAppStatus":"unknown"}}}`,
		`{"protocol":"osw-provider-v2","schema_version":2,` +
			`"request_id":"req","operation":"resolve_and_act",` +
			`"accessibility":{"ok":true,"failure":{"code":"effect_unknown",` +
			`"phase":"post_action_observation"},"mutation":{` +
			`"input_events_emitted":true,"mutation_unknown":true},` +
			`"recovery_context":{"sourceAppStatus":"unknown"}}}`,
		`{"protocol":"osw-provider-v2","schema_version":2,` +
			`"request_id":"req","operation":"resolve_and_act",` +
			`"accessibility":{"ok":false,"failure":{"code":"stale_element_ref",` +
			`"phase":"precondition"},"mutation":{"input_events_emitted":false},` +
			`"recovery_context":{"sourceAppStatus":"exited",` +
			`"sourceWindowStatus":"gone","inventoryComplete":true}}}`,
		`{"protocol":"osw-provider-v2","schema_version":2,` +
			`"request_id":"req","operation":"resolve_and_act",` +
			`"accessibility":{"ok":false,"failure":{"code":"effect_unknown",` +
			`"phase":"post_action_observation"},"mutation":{` +
			`"input_events_emitted":true,"mutation_unknown":false},` +
			`"recovery_context":{"sourceAppStatus":"unknown"}}}`,
	}
	for _, input := range tests {
		if _, err := DecodeProviderWireResponse([]byte(input)); err == nil {
			t.Fatalf("invalid recovery envelope accepted: %s", input)
		}
	}
}

func TestProviderV2ValidationRejectsPayloadVersionDrift(t *testing.T) {
	request := newProviderWireRequest("req-version", ProviderWireObserveApps)
	request.ObserveApps = &ProviderObserveAppsRequest{ProtocolVersion: 1}
	err := request.Validate()
	if err == nil || !strings.Contains(err.Error(), "payload protocol version") {
		t.Fatalf("validation error = %v, want payload protocol version", err)
	}
}

func TestProviderV2ActionRoundTripsExpectedWindow(t *testing.T) {
	request := newProviderWireRequest("req-window", ProviderWireResolveAndAct)
	request.Action = &ProviderActionRequest{
		ProtocolVersion: providerProtocolV2,
		Intent:          AccessibilityKeypress,
		App:             "Editor",
		WindowHandle:    "w2:0123456789abcdef0123456789abcdef",
		ExpectedWindow: &ProviderWindowExpectation{
			WindowHandle:       "w2:0123456789abcdef0123456789abcdef",
			ProviderBusName:    ":1.42",
			ProviderObjectPath: "/org/a11y/window/1",
			ProcessGeneration:  "42:100",
			Role:               "dialog",
			Name:               "Confirm",
		},
		Keypress: &ProviderKeypressInput{Key: "ENTER"},
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeProviderWireRequest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Action.ExpectedWindow == nil ||
		decoded.Action.ExpectedWindow.Name != "Confirm" {
		t.Fatalf("expected window lost in round trip: %#v", decoded.Action)
	}
}

func TestProviderV2ValidationRejectsInvalidTypedPayloads(t *testing.T) {
	tests := []struct {
		name    string
		request ProviderWireRequest
		want    string
	}{
		{
			name: "unknown accessibility intent",
			request: func() ProviderWireRequest {
				request := newProviderWireRequest(
					"req-action", ProviderWireResolveAndAct)
				request.Action = &ProviderActionRequest{
					ProtocolVersion: providerProtocolV2,
					Intent:          "unknown",
					App:             "Editor",
				}
				return request
			}(),
			want: "unsupported provider accessibility intent",
		},
		{
			name: "resolve target without target",
			request: func() ProviderWireRequest {
				request := newProviderWireRequest(
					"req-resolve", ProviderWireResolveAndAct)
				request.Action = &ProviderActionRequest{
					ProtocolVersion: providerProtocolV2,
					Intent:          AccessibilityResolveTarget,
					App:             "Editor",
				}
				return request
			}(),
			want: "requires an exact target",
		},
		{
			name: "type without text",
			request: func() ProviderWireRequest {
				request := newProviderWireRequest(
					"req-type", ProviderWireResolveAndAct)
				request.Action = &ProviderActionRequest{
					ProtocolVersion: providerProtocolV2,
					Intent:          AccessibilityType,
					App:             "Editor",
					WindowHandle:    "w1",
					Type:            &ProviderTypeInput{},
				}
				return request
			}(),
			want: "type text is required",
		},
		{
			name: "keypress without key",
			request: func() ProviderWireRequest {
				request := newProviderWireRequest(
					"req-keypress", ProviderWireResolveAndAct)
				request.Action = &ProviderActionRequest{
					ProtocolVersion: providerProtocolV2,
					Intent:          AccessibilityKeypress,
					App:             "Editor",
					WindowHandle:    "w1",
					Keypress:        &ProviderKeypressInput{},
				}
				return request
			}(),
			want: "keypress key is required",
		},
		{
			name: "set value without target",
			request: func() ProviderWireRequest {
				value := "Ada"
				request := newProviderWireRequest(
					"req-set-value", ProviderWireResolveAndAct)
				request.Action = &ProviderActionRequest{
					ProtocolVersion: providerProtocolV2,
					Intent:          AccessibilitySetValue,
					App:             "Editor",
					SetValue: &ProviderSetValueInput{
						Value: &value,
					},
				}
				return request
			}(),
			want: "set_value requires an exact target",
		},
		{
			name: "click with wrong typed input",
			request: func() ProviderWireRequest {
				request := newProviderWireRequest(
					"req-click", ProviderWireResolveAndAct)
				request.Action = &ProviderActionRequest{
					ProtocolVersion: providerProtocolV2,
					Intent:          AccessibilityClick,
					App:             "Editor",
					Target: &ProviderSemanticTarget{
						Selector: `button "Save"`,
						HasName:  true,
					},
					Keypress: &ProviderKeypressInput{Key: "ENTER"},
				}
				return request
			}(),
			want: "click requires click input only",
		},
		{
			name: "unknown desktop action",
			request: func() ProviderWireRequest {
				request := newProviderWireRequest(
					"req-desktop", ProviderWireDesktopBatch)
				request.Desktop = &ProviderDesktopRequest{
					ProtocolVersion: providerProtocolV2,
					Actions: []ProviderDesktopAction{{
						Type: "teleport",
					}},
				}
				return request
			}(),
			want: "unsupported action type",
		},
		{
			name: "window action without target",
			request: func() ProviderWireRequest {
				request := newProviderWireRequest(
					"req-window", ProviderWireWindowAction)
				request.WindowAction = &ProviderWindowActionRequest{
					ProtocolVersion: providerProtocolV2,
					Action:          WindowClose,
					App:             "Editor",
				}
				return request
			}(),
			want: "requires an exact target",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			err := test.request.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestProviderV2ActionDecodeRejectsIntentFieldMixing(t *testing.T) {
	_, err := DecodeProviderWireRequest([]byte(`{
		"protocol":"osw-provider-v2",
		"schema_version":2,
		"request_id":"req-mixed",
		"operation":"resolve_and_act",
		"action":{
			"protocol_version":2,
			"intent":"type",
			"app":"Editor",
			"window_handle":"w1",
			"input":{"text":"hello","key":"ENTER"}
		}
	}`))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("decode error = %v, want action-specific unknown field", err)
	}
}

func TestProviderV2WireExcludesGoOwnedPolicyFields(t *testing.T) {
	legacy := axServiceRequest{
		Operation: axServiceObserve,
		App:       "Editor",
		Observe:   observeFull,
		TreeView:  "canonical",
		Scope:     "content",
		SourceRef: "r7",
		KnownRefs: map[string]elementRef{"r7": {Index: 7, Role: "button"}},
		Action:    axScrollCommand{WindowCentre: true},
	}
	request := newProviderWireRequest("req-private", ProviderWireObserve)
	observe := providerObserveRequestFromOperation(legacy)
	request.Observe = &observe
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	payload, ok := wire["observe"].(map[string]any)
	if !ok {
		t.Fatalf("observe payload missing: %s", encoded)
	}
	observation, ok := payload["observation"].(map[string]any)
	if !ok {
		t.Fatalf("observation payload missing: %s", encoded)
	}
	for _, forbidden := range []string{
		"observe", "tree_view", "scope", "source_ref",
		"known_refs", "window_centre",
	} {
		if _, leaked := observation[forbidden]; leaked {
			t.Fatalf("provider wire leaked Go-owned field %s: %s",
				forbidden, encoded)
		}
	}

	sourceIndex := 7
	actionRequest := newProviderWireRequest(
		"req-action-private",
		ProviderWireResolveAndAct,
	)
	action := providerActionRequestFromOperation(axServiceRequest{
		Action: axResolveTargetCommand{},
		App:    "Editor",
		SemanticTarget: &semanticTarget{
			Selector:    `button "Save"`,
			SourceIndex: &sourceIndex,
			Role:        "button",
			Name:        "Save",
			HasName:     true,
		},
	})
	actionRequest.Action = &action
	actionBytes, err := json.Marshal(actionRequest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(actionBytes), "sourceIndex") {
		t.Fatalf("provider wire leaked Go-owned sourceIndex: %s", actionBytes)
	}
	if action.Target == nil ||
		action.Target.SourceIndex == nil ||
		*action.Target.SourceIndex != sourceIndex {
		t.Fatalf("typed provider request lost private source index: %#v", action)
	}
}
