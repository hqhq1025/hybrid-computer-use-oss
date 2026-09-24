package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestMCPInitializeResponseContainsToolsCapability(t *testing.T) {
	request := map[string]any{
		"jsonrpc": "2.0",
		"id":      float64(1),
		"method":  "initialize",
		"params":  map[string]any{},
	}
	response := handleMCPRequest(request, newService())
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result: %#v", response)
	}
	capabilities := result["capabilities"].(map[string]any)
	if _, ok := capabilities["tools"]; !ok {
		t.Fatalf("missing tools capability: %#v", capabilities)
	}
	if result["instructions"] != serverInstructions {
		t.Fatal("initialize must return the self-contained static serverInstructions")
	}
}

func TestMCPDefaultsToModelVisibleTextEvidence(t *testing.T) {
	for _, tc := range []struct {
		requested      string
		wantNegotiated string
	}{
		{mcpProtocolLegacy, mcpProtocolLegacy},
		{mcpProtocolStructured, mcpProtocolStructured},
		{"2025-11-25", mcpProtocolStructured},
	} {
		svc := newService()
		init := handleMCPRequest(map[string]any{
			"jsonrpc": "2.0",
			"id":      float64(1),
			"method":  "initialize",
			"params": map[string]any{
				"protocolVersion": tc.requested,
			},
		}, svc)
		result := init["result"].(map[string]any)
		if result["protocolVersion"] != tc.wantNegotiated {
			t.Fatalf("requested %s negotiated %#v, want %s",
				tc.requested, result["protocolVersion"], tc.wantNegotiated)
		}

		call := handleMCPRequest(map[string]any{
			"jsonrpc": "2.0",
			"id":      float64(2),
			"method":  "tools/call",
			"params": map[string]any{
				"name": "AX",
				"arguments": map[string]any{
					"mode": "invalid",
				},
			},
		}, svc)
		callResult := call["result"].(toolCallResult)
		if callResult.StructuredContent != nil ||
			len(callResult.Content) == 0 {
			t.Fatalf("requested %s must receive text evidence by default: %#v",
				tc.requested, callResult)
		}
	}
}

func TestMCPStructuredContentIsExplicitOptIn(t *testing.T) {
	t.Setenv("COMPUTER_USE_STRUCTURED_CONTENT", "1")
	svc := newService()
	handleMCPRequest(map[string]any{
		"jsonrpc": "2.0",
		"id":      float64(1),
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": mcpProtocolStructured,
		},
	}, svc)
	call := handleMCPRequest(map[string]any{
		"jsonrpc": "2.0",
		"id":      float64(2),
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "click",
			"arguments": map[string]any{"element_index": "1"},
		},
	}, svc)
	result := call["result"].(toolCallResult)
	if result.StructuredContent == nil {
		t.Fatalf("2025-06-18 opt-in must preserve structuredContent: %#v", result)
	}
	for _, item := range result.Content {
		if strings.HasPrefix(item.Text, "[tool-evidence] ") {
			t.Fatalf("opt-in structured clients should not receive duplicate text evidence: %#v", result)
		}
	}
}

func TestMCPInitializeUsesProductOwnedServerName(t *testing.T) {
	response := handleMCPRequest(map[string]any{
		"jsonrpc": "2.0",
		"id":      float64(1),
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": mcpProtocolStructured,
		},
	}, newService())
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize result has unexpected type: %#v", response["result"])
	}
	serverInfo, ok := result["serverInfo"].(map[string]any)
	if !ok {
		t.Fatalf("initialize result has no serverInfo: %#v", result)
	}
	if got := serverInfo["name"]; got != "computer-use" {
		t.Fatalf("server name = %#v, want computer-use", got)
	}
}

func TestMCPRejectsLegacyToolCallsWithoutMigrationCompatibility(t *testing.T) {
	request := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "get_app_state",
			"arguments": map[string]any{},
		},
	}
	response := handleMCPRequest(request, newService())
	result := response["result"].(toolCallResult)
	if !result.IsError ||
		!strings.Contains(resultText(result), "use AX or GUI") {
		t.Fatalf("legacy MCP tool call was not rejected: %#v", response)
	}
}

func TestMCPFramingRecoversAtNewlineAndRejectsOversizedMessages(t *testing.T) {
	valid := `{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n"
	input := strings.NewReader("{not-json}\n" + valid)
	var output bytes.Buffer
	if err := runMCP(input, &output); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"code":-32700`) ||
		!strings.Contains(lines[1], `"id":1`) {
		t.Fatalf("newline framing did not recover cleanly: %q", output.String())
	}

	oversized := strings.NewReader(strings.Repeat("x", maxMCPMessageBytes+1) + "\n")
	if err := runMCP(oversized, io.Discard); err == nil {
		t.Fatal("oversized MCP message must terminate the connection")
	}
}

func TestMCPRequestDecodePreservesExactJSONNumbers(t *testing.T) {
	request, err := decodeMCPRequest([]byte(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` +
			`{"name":"AX","arguments":{"mode":"assert_ax","assertion":` +
			`{"type":"value","target":{"ref":"r1"},` +
			`"value":9007199254740993}}}}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	params := request["params"].(map[string]any)
	arguments := params["arguments"].(map[string]any)
	assertion := arguments["assertion"].(map[string]any)
	value, ok := assertion["value"].(json.Number)
	if !ok || value.String() != "9007199254740993" {
		t.Fatalf("MCP transport changed exact JSON number: %#v",
			assertion["value"])
	}
	if _, err := decodeMCPRequest([]byte(`{} {}`)); err == nil {
		t.Fatal("MCP transport accepted multiple JSON values")
	}
}
