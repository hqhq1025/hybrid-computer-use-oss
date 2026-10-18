package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func decodeTestArguments(t *testing.T, raw string) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var args map[string]any
	if err := decoder.Decode(&args); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return args
}

func TestPublishedInputSchemasCompile(t *testing.T) {
	for _, tool := range allToolDefinitions() {
		schema, err := compiledInputSchema(tool.Name)
		if err != nil || schema == nil {
			t.Fatalf("%s input schema did not compile: %v", tool.Name, err)
		}
		again, _ := compiledInputSchema(tool.Name)
		if again != schema {
			t.Fatalf("%s input schema was compiled twice", tool.Name)
		}
	}
}

// Each rejected call names one problem in the branch the caller chose and,
// when the call can be repaired mechanically, the corrected call itself.
func TestArgumentErrorsSelectTheIntendedBranch(t *testing.T) {
	for _, tc := range []struct {
		tool, args, path string
		want             []string
	}{
		// Shapes the models wrote in the 2026-10-17 panel run.
		{"AX", `{"mode":"act","actions":[{"ref":"r103","type":"click"}]}`, "/actions/0",
			[]string{`does not accept fields: "ref"`, `use {"type":"click","target":{"ref":"r103"}}`}},
		{"AX", `{"mode":"act","actions":[{"ref":"w1","action":"close"}]}`, "/actions/0/type",
			[]string{`use {"type":"close","target":{"ref":"w1"}}`}},
		{"AX", `{"mode":"act","actions":[{"type":"click","target":"r1"}]}`, "/actions/0/target",
			[]string{`must be an object like {"ref":"rN"}`, `use {"type":"click","target":{"ref":"r1"}}`}},
		// Probes from the schema/runtime audit.
		{"AX", `{"mode":"ACT","actions":[{"type":"click","target":{"ref":"r1"}}]}`, "/mode",
			[]string{`mode "ACT" is unsupported`, `case-sensitive: "act"`}},
		{"AX", `{"mode":"act","observe":"notes","actions":[{"type":"click","target":{"ref":"r1"}}]}`, "/observe",
			[]string{"observe must be none, diff, or screenshot"}},
		{"AX", `{"mode":"observe","app":"x","observe":"Diff"}`, "/observe",
			[]string{`observe "Diff" is unsupported`}},
		{"AX", `{"mode":"observe","app":"x","scope":"WINDOW"}`, "/scope",
			[]string{`scope "WINDOW" is unsupported`}},
		{"AX", `{"mode":"observe","app":"x","actions":[]}`, "",
			[]string{`mode="observe" does not accept fields: "actions"`}},
		{"AX", `{"mode":"find","app":"x","limit":5,"query":{"role":"button"}}`, "",
			[]string{`does not accept fields: "limit"`, `"query":{"limit":5,"role":"button"}`}},
		{"AX", `{"mode":"find","app":"x","query":{"name_contains":"","name_equals":"Save"}}`, "/query",
			[]string{"accepts only one of name_contains or name_equals"}},
		{"AX", `{"mode":"find","app":"x","query":{"role":5,"name_equals":"Save"}}`, "/query/role",
			[]string{"query.role must be a string, not a number"}},
		{"AX", `{"mode":"observe","window_ref":7}`, "/window_ref",
			[]string{"window_ref must be a string, not a number"}},
		{"AX", `{"mode":"observe","app":1234}`, "/app",
			[]string{"app must be a string"}},
		{"AX", `{"mode":"assert_ax","app":"x","assertion":{"type":"value_equals","target":{"ref":"r1"},"value":"a"}}`,
			"/assertion/type", []string{`assertion.type "value_equals" is unsupported`}},
		{"AX", `{"mode":"assert_ax","app":"x","assertion":{"type":"EXISTS","target":{"ref":"r1"},"value":true}}`,
			"/assertion/type", []string{`use {"type":"exists","target":{"ref":"r1"},"value":true}`}},
		{"AX", `{"mode":"assert_ax","app":"x","assertion":{"type":"exists","target":{"ref":"r1"},"value":"yes"}}`,
			"/assertion/value", []string{"assertion.value must be a boolean, not a string"}},
		{"AX", `{"mode":"act","actions":[{"type":"Click","target":{"ref":"r1"}}]}`, "/actions/0/type",
			[]string{`use {"type":"click","target":{"ref":"r1"}}`}},
		{"AX", `{"mode":"act","window_ref":"w1","actions":[{"type":"scroll","direction":"UP"}]}`,
			"/actions/0/direction", []string{`direction "UP" is unsupported`}},
		{"AX", `{"mode":"act","actions":[{"type":"click","target":{"index":0}}]}`, "/actions/0/target",
			[]string{`does not accept fields: "index"`}},
		{"AX", `{"mode":"observe","app":"x","root":{"ref":"w1"}}`, "/root/ref",
			[]string{"element ref rN"}},
		{"AX", `{"mode":"observe","app_ref":"a1","window_ref":"w2"}`, "",
			[]string{"accepts only one of app, app_ref, or window_ref"}},
		{"AX", `{"mode":"act","window_ref":"w1","actions":[{"type":"click","target":{"ref":"r1"}}]}`, "",
			[]string{"already owns its application"}},
		{"GUI", `{"actions":[{"type":"wait","ms":5}]}`, "/actions/0",
			[]string{`actions[0] wait does not accept fields: "ms"`}},
		{"GUI", `{"actions":[{"type":"click","x":"10","y":1}]}`, "/actions/0/x",
			[]string{"actions[0].x must be a number, not a string"}},
		{"GUI", `{"actions":[{"type":"wait"}],"settle_ms":"800"}`, "/settle_ms",
			[]string{"settle_ms must be an integer, not a string"}},
		{"GUI", `{"actions":[{"type":"click","x":1,"y":1,"button":"LEFT"}]}`, "/actions/0/button",
			[]string{`button "LEFT" is unsupported`}},
		{"GUI", `{"actions":[{"type":"scroll","x":1,"y":1,"scroll_y":0}]}`, "/actions/0/scroll_y",
			[]string{"scroll_y must not be 0"}},
	} {
		argErr := validateToolArguments(tc.tool, decodeTestArguments(t, tc.args))
		if argErr == nil {
			t.Errorf("%s %s was accepted", tc.tool, tc.args)
			continue
		}
		if argErr.Path != tc.path {
			t.Errorf("%s %s: path %q, want %q (%s)", tc.tool, tc.args, argErr.Path, tc.path, argErr.Message)
		}
		for _, fragment := range tc.want {
			if !strings.Contains(argErr.Message, fragment) {
				t.Errorf("%s %s: message %q lacks %q", tc.tool, tc.args, argErr.Message, fragment)
			}
		}
		for _, noise := range []string{"oneOf", "anyOf", "allOf", "$defs", "schema://", "jsonschema"} {
			if strings.Contains(argErr.Message, noise) {
				t.Errorf("%s %s: message leaks schema internals: %q", tc.tool, tc.args, argErr.Message)
			}
		}
	}
}

// A hint is only offered when it passes the schema; following it must not
// produce another static rejection.
func TestArgumentErrorHintsAreValidCalls(t *testing.T) {
	for _, raw := range []string{
		`{"mode":"act","actions":[{"ref":"r103","type":"click"}]}`,
		`{"mode":"act","actions":[{"ref":"w1","action":"close"}]}`,
		`{"mode":"act","actions":[{"type":"Click","target":{"ref":"r1"}}]}`,
		`{"mode":"find","app":"x","limit":5,"query":{"role":"button"}}`,
		`{"mode":"observe","target":"apps"}`,
	} {
		argErr := validateToolArguments(publicToolAX, decodeTestArguments(t, raw))
		if argErr == nil {
			t.Fatalf("%s was accepted", raw)
		}
		_, hint, found := strings.Cut(argErr.Message, "; use {")
		hint = "{" + hint
		if !found {
			t.Fatalf("%s: no hint in %q", raw, argErr.Message)
		}
		repaired := decodeTestArguments(t, hint)
		if _, isAction := repaired["type"]; isAction {
			whole := decodeTestArguments(t, raw)
			whole["actions"] = []any{repaired}
			repaired = whole
		}
		if again := validateToolArguments(publicToolAX, repaired); again != nil {
			t.Fatalf("%s: hint %s is rejected: %s", raw, hint, again.Message)
		}
	}
}

// Rejected arguments must not reach a provider or emit input.
func TestSchemaRejectionHappensBeforeAnyProviderCall(t *testing.T) {
	for _, tc := range []struct{ tool, args string }{
		{"AX", `{"mode":"act","actions":[{"ref":"r1","type":"click"}]}`},
		{"AX", `{"mode":"observe","app":"x","observe":"Diff"}`},
		{"AX", `{"mode":"assert_ax","app":"x","assertion":{"type":"exists","target":{"ref":"r1"},"value":"yes"}}`},
		{"GUI", `{"actions":[{"type":"click","x":1,"y":1,"delay":3}]}`},
		{"GUI", `{"actions":[{"type":"wait"}],"settle_ms":"800"}`},
	} {
		backend := &recordingPlatformBackend{}
		result := newServiceWithBackend(backend).callTool(tc.tool, decodeTestArguments(t, tc.args))
		if !result.IsError {
			t.Fatalf("%s %s was accepted", tc.tool, tc.args)
		}
		if len(backend.calls) != 0 || len(backend.desktopRequests) != 0 || len(backend.actionRequests) != 0 {
			t.Fatalf("%s %s reached the provider: %v", tc.tool, tc.args, backend.calls)
		}
		if result.StructuredContent["argument_error_code"] != argumentErrorInvalid {
			t.Fatalf("%s %s: missing argument error code: %#v", tc.tool, tc.args, result.StructuredContent)
		}
		visible := withTextEvidence(result)
		if strings.Contains(resultText(visible), "argument_error") {
			t.Fatalf("internal argument evidence reached the model: %q", resultText(visible))
		}
	}
}

func TestArgumentHintsStayShort(t *testing.T) {
	long := strings.Repeat("x", 2000)
	raw := `{"mode":"act","actions":[{"ref":"r1","type":"set_value","value":"` + long + `"}]}`
	argErr := validateToolArguments(publicToolAX, decodeTestArguments(t, raw))
	if argErr == nil {
		t.Fatal("misplaced ref was accepted")
	}
	if len(argErr.Message) > 400 {
		t.Fatalf("argument error repeats the long value: %d bytes", len(argErr.Message))
	}
}

// Recorded runs often name the action verb under "action" with a synonym
// ({"ref":"r1","action":"press"}). The hint maps it to the published action;
// the synonym itself stays invalid.
func TestArgumentHintMapsRecordedVerbSynonyms(t *testing.T) {
	for raw, hint := range map[string]string{
		`{"mode":"act","actions":[{"ref":"r1","action":"press"}]}`:           `use {"type":"click","target":{"ref":"r1"}}`,
		`{"mode":"act","actions":[{"action":"close_window","target":"w1"}]}`: `use {"type":"close","target":{"ref":"w1"}}`,
		// set_value takes value, not text; no valid call can be built, so
		// the message names the problem without a hint.
		`{"mode":"act","actions":[{"ref":"r8","action":"set_text","text":"anon"}]}`: `actions[0].type is required`,
	} {
		argErr := validateToolArguments(publicToolAX, decodeTestArguments(t, raw))
		if argErr == nil {
			t.Fatalf("%s was accepted", raw)
		}
		if !strings.Contains(argErr.Message, hint) {
			t.Fatalf("%s: message %q lacks %q", raw, argErr.Message, hint)
		}
	}
	if validateToolArguments(publicToolAX, decodeTestArguments(t,
		`{"mode":"act","actions":[{"type":"press","target":{"ref":"r1"}}]}`)) == nil {
		t.Fatal("a hint synonym became an accepted action type")
	}
}
