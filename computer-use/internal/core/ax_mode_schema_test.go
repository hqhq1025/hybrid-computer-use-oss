package core

import (
	"encoding/json"
	"testing"

	"github.com/hqhq1025/hybrid-computer-use-oss/computer-use/internal/contract"
)

// These cases exercise the advertised mode contract independently of the
// runtime allowlists, including defaults and continuation-only find calls.
func TestAXModeSchemasMatchDecoderAndClients(t *testing.T) {
	original := findToolDefinition(t, contract.PublicToolAX).InputSchema
	converted := jsonRoundTrip(original).(map[string]any)
	converted["additionalProperties"] = false // OpenCode closes the root.
	normalized, err := marshalNoEscape(codexNormalize(codexSanitize(jsonRoundTrip(original))))
	if err != nil {
		t.Fatal(err)
	}
	var codex map[string]any
	if err := json.Unmarshal(normalized, &codex); err != nil {
		t.Fatal(err)
	}
	schemas, err := contract.CompileInputSchemas([]contract.ToolDefinition{
		{Name: "original", InputSchema: original},
		{Name: "opencode", InputSchema: converted},
		{Name: "codex", InputSchema: codex},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args  string
		valid bool
	}{
		{`{"mode":"apps"}`, true},
		{`{"mode":"observe","app":"Finder"}`, true},
		{`{"mode":"observe","window_ref":"w8","root":"focused","scope":"window","observe":"tree"}`, true},
		{`{"mode":"find","app":"Finder","query":{"role":"button"}}`, true},
		{`{"mode":"find","window_ref":"w8","scope":"window","query":{"name_contains":"Save"},"limit":50}`, true},
		{`{"mode":"find","cursor":"f1"}`, true},
		{`{"mode":"find","cursor":"f1","limit":1}`, true},
		{`{"mode":"act","actions":[{"type":"keypress","keys":["TAB"]}]}`, true},
		{`{"mode":"act","window_ref":"w8","observe":"none","actions":[{"type":"wait","text":"Saved"}]}`, true},
		{`{"mode":"apps","app":"Finder"}`, false},
		{`{"mode":"apps","limit":20}`, false},
		{`{"mode":"observe","window_ref":"w8","observe":"tree","scope":"window","limit":100}`, false},
		{`{"mode":"observe","window_ref":"w8","limit":20}`, false},
		{`{"mode":"observe","window_ref":"w8","query":{"role":"button"}}`, false},
		{`{"mode":"observe","window_ref":"w8","observe":"none"}`, false},
		{`{"mode":"observe"}`, false},
		{`{"mode":"observe","app":"Finder","window_ref":"w8"}`, false},
		{`{"mode":"find","app":"Finder"}`, false},
		{`{"mode":"find","query":{"role":"button"}}`, false},
		{`{"mode":"find","app":"Finder","query":{}}`, false},
		{`{"mode":"find","app":"Finder","query":{"role":"button"},"observe":"tree"}`, false},
		{`{"mode":"find","cursor":"f1","app":"Finder"}`, false},
		{`{"mode":"find","cursor":"f1","window_ref":"w8"}`, false},
		{`{"mode":"find","cursor":"f1","scope":"window"}`, false},
		{`{"mode":"find","cursor":"f1","query":{"role":"button"}}`, false},
		{`{"mode":"find","cursor":"f1","app":"Finder","query":{"role":"button"}}`, false},
		{`{"mode":"find","cursor":"f1","limit":51}`, false},
		{`{"mode":"act"}`, false},
		{`{"mode":"act","app":"Finder","actions":[{"type":"click","ref":"r1"}]}`, false},
		{`{"mode":"act","scope":"window","actions":[{"type":"click","ref":"r1"}]}`, false},
		{`{"mode":"assert_ax","window_ref":"w8"}`, false},
	} {
		t.Run(tc.args, func(t *testing.T) {
			args := decodeTestArguments(t, tc.args)
			_, decoderErr := DecodeAXRequest(args)
			if (decoderErr == nil) != tc.valid {
				t.Fatalf("decoder accepted=%v want %v: %v", decoderErr == nil, tc.valid, decoderErr)
			}
			for name, schema := range schemas {
				want := tc.valid
				// Codex normalizes away minProperties and numeric bounds even
				// below its compaction budget. The server still enforces both.
				if name == "codex" && (tc.args == `{"mode":"find","app":"Finder","query":{}}` || tc.args == `{"mode":"find","cursor":"f1","limit":51}`) {
					want = true
				}
				if err := schema.Validate(args); (err == nil) != want {
					t.Errorf("%s accepted=%v want %v: %v", name, err == nil, want, err)
				}
			}
		})
	}
}
