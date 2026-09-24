package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	mcpProtocolLegacy     = "2025-03-26"
	mcpProtocolStructured = "2025-06-18"
)

func runMCP(stdin io.Reader, stdout io.Writer) error {
	svc := newService()
	encoder := json.NewEncoder(stdout)
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 64*1024), maxMCPMessageBytes)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		request, err := decodeMCPRequest(line)
		if err != nil {
			_ = encoder.Encode(jsonRPCError(nil, -32700, "Invalid JSON-RPC payload"))
			continue
		}
		response := handleMCPRequest(request, svc)
		if response != nil {
			if err := encoder.Encode(response); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("MCP input exceeds %d bytes or could not be read: %w",
			maxMCPMessageBytes, err)
	}
	return nil
}

func decodeMCPRequest(line []byte) (map[string]any, error) {
	var request map[string]any
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.UseNumber()
	if err := decoder.Decode(&request); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	return request, nil
}

func handleMCPRequest(request map[string]any, svc *service) map[string]any {
	id := request["id"]
	method, _ := request["method"].(string)
	params, _ := request["params"].(map[string]any)
	switch method {
	case "initialize":
		requested, _ := params["protocolVersion"].(string)
		svc.protocolVersion = negotiateMCPProtocol(requested)
		return jsonRPCResult(id, map[string]any{
			"protocolVersion": svc.protocolVersion,
			"serverInfo": map[string]any{
				"name":    "computer-use",
				"version": version,
			},
			"capabilities": map[string]any{"tools": map[string]any{"listChanged": false}},
			"instructions": serverInstructions,
		})
	case "notifications/initialized", "notifications/turn-ended":
		return nil
	case "ping":
		return jsonRPCResult(id, map[string]any{})
	case "tools/list":
		return jsonRPCResult(id, map[string]any{"tools": toolDefinitions()})
	case "tools/call":
		name, _ := params["name"].(string)
		publicName, ok := publicToolCallName(name)
		if !ok {
			return jsonRPCResult(id, textResult(
				fmt.Sprintf("unsupported public tool %q; use AX or GUI", name),
				true,
			).withEvidenceScope("error_only"))
		}
		arguments, _ := params["arguments"].(map[string]any)
		if arguments == nil {
			arguments = map[string]any{}
		}
		result := svc.callTool(publicName, arguments)
		isAX := publicName == publicToolAX
		if isAX {
			// Final budget metadata must exist before fallback text evidence is
			// serialized. Otherwise structured-only fields such as structural
			// omission counts disappear when the structured channel is removed.
			result = enforceAXResponseBudget(result)
			result = normalizeAXEvidenceInvariants(result)
			persistAXTelemetry(arguments, result.StructuredContent)
			result = prepareAXEvidence(result)
		}
		if svc.protocolVersion == mcpProtocolLegacy || !structuredContentEnabled() {
			// structuredContent was added after MCP revision 2025-03-26 and
			// remains optional. Keep the richer internal result, but put the
			// evidence on the model-visible text channel before omitting the
			// structured field. Some clients prioritize structuredContent and
			// hide sibling text from the model, which can make computer-use
			// state appear unavailable.
			if isAX {
				result = withAXTextEvidence(result)
			} else {
				result = withTextEvidence(result)
			}
			result.StructuredContent = nil
		}
		return jsonRPCResult(id, result)
	default:
		if method == "" {
			return nil
		}
		return jsonRPCError(id, -32601, "Method not found: "+method)
	}
}

func negotiateMCPProtocol(requested string) string {
	switch strings.TrimSpace(requested) {
	case mcpProtocolLegacy:
		return mcpProtocolLegacy
	case mcpProtocolStructured:
		return mcpProtocolStructured
	default:
		// Newer clients can negotiate down to the newest revision implemented
		// here; older/empty callers receive the legacy revision.
		if strings.TrimSpace(requested) > mcpProtocolStructured {
			return mcpProtocolStructured
		}
		return mcpProtocolLegacy
	}
}

func jsonRPCResult(id any, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
}

func jsonRPCError(id any, code int, message string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	}
}
