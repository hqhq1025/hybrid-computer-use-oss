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
	reader := bufio.NewReaderSize(stdin, 64*1024)
	for {
		line, oversized, err := readMCPLine(reader)
		if len(bytes.TrimSpace(line)) > 0 || oversized {
			var response any
			if oversized {
				response = jsonRPCError(nil, -32600, fmt.Sprintf(
					"Invalid Request: message exceeds %d bytes", maxMCPMessageBytes))
			} else {
				response = handleMCPLine(bytes.TrimSpace(line), svc)
			}
			if response != nil {
				if encodeErr := encoder.Encode(response); encodeErr != nil {
					return encodeErr
				}
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read MCP input: %w", err)
		}
	}
}

// readMCPLine returns one newline-delimited message. A message longer than
// maxMCPMessageBytes is discarded through its newline so that one oversized
// request cannot end the session.
func readMCPLine(reader *bufio.Reader) ([]byte, bool, error) {
	var line []byte
	oversized := false
	for {
		chunk, err := reader.ReadSlice('\n')
		if !oversized {
			if len(line)+len(chunk) > maxMCPMessageBytes {
				oversized = true
				line = nil
			} else {
				line = append(line, chunk...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return line, oversized, err
	}
}

// handleMCPLine decodes one JSON-RPC message or batch and returns the value to
// write, or nil when nothing must be written (notifications and client
// responses).
func handleMCPLine(line []byte, svc *service) any {
	message, err := decodeMCPMessage(line)
	if err != nil {
		return jsonRPCError(nil, -32700, "Parse error: invalid JSON")
	}
	batch, isBatch := message.([]any)
	if !isBatch {
		if response := handleMCPMessage(message, svc); response != nil {
			return response
		}
		return nil
	}
	if len(batch) == 0 {
		return jsonRPCError(nil, -32600, "Invalid Request: empty batch")
	}
	var responses []any
	for _, item := range batch {
		if response := handleMCPMessage(item, svc); response != nil {
			responses = append(responses, response)
		}
	}
	if len(responses) == 0 {
		return nil
	}
	return responses
}

func handleMCPMessage(message any, svc *service) map[string]any {
	request, ok := message.(map[string]any)
	if !ok {
		return jsonRPCError(nil, -32600, "Invalid Request: message must be a JSON object")
	}
	id, hasID := request["id"]
	_, hasResult := request["result"]
	_, hasError := request["error"]
	if _, hasMethod := request["method"]; !hasMethod && (hasResult || hasError) {
		// A response from the client; this server sends no requests.
		return nil
	}
	if hasID && !validJSONRPCID(id) {
		return jsonRPCError(nil, -32600, "Invalid Request: id must be a string or number")
	}
	if !hasID {
		id = nil
	}
	if version, _ := request["jsonrpc"].(string); version != "2.0" {
		return requestError(hasID, id, -32600, `Invalid Request: jsonrpc must be "2.0"`)
	}
	if method, ok := request["method"].(string); !ok || method == "" {
		return requestError(hasID, id, -32600, "Invalid Request: method must be a non-empty string")
	}
	if params, present := request["params"]; present {
		if _, ok := params.(map[string]any); !ok {
			return requestError(hasID, id, -32602, "Invalid params: params must be an object")
		}
	}
	return handleMCPRequest(request, svc)
}

// requestError answers a malformed request; malformed notifications get no reply.
func requestError(hasID bool, id any, code int, message string) map[string]any {
	if !hasID {
		return nil
	}
	return jsonRPCError(id, code, message)
}

func validJSONRPCID(id any) bool {
	switch id.(type) {
	case string, json.Number:
		return true
	default:
		return false
	}
}

func decodeMCPMessage(line []byte) (any, error) {
	var message any
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.UseNumber()
	if err := decoder.Decode(&message); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	return message, nil
}

func decodeMCPRequest(line []byte) (map[string]any, error) {
	message, err := decodeMCPMessage(line)
	if err != nil {
		return nil, err
	}
	request, ok := message.(map[string]any)
	if !ok {
		return nil, errors.New("JSON-RPC message must be an object")
	}
	return request, nil
}

func handleMCPRequest(request map[string]any, svc *service) map[string]any {
	id, hasID := request["id"]
	method, _ := request["method"].(string)
	params, _ := request["params"].(map[string]any)
	if !hasID {
		// Notifications never receive a response. The server acts on none of
		// them; a request sent without an id is not executed.
		return nil
	}
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
	case "ping":
		return jsonRPCResult(id, map[string]any{})
	case "tools/list":
		return jsonRPCResult(id, map[string]any{"tools": toolDefinitions()})
	case "tools/call":
		name, ok := params["name"].(string)
		if !ok {
			return jsonRPCError(id, -32602, "Invalid params: name must be a string")
		}
		publicName, ok := publicToolCallName(name)
		if !ok {
			return jsonRPCError(id, -32602,
				fmt.Sprintf("Unknown tool %q; use AX or GUI", name))
		}
		arguments := map[string]any{}
		if raw, present := params["arguments"]; present && raw != nil {
			if arguments, ok = raw.(map[string]any); !ok {
				return jsonRPCError(id, -32602, "Invalid params: arguments must be an object")
			}
		}
		result := svc.callTool(publicName, arguments)
		isAX := publicName == publicToolAX
		if isAX {
			// Final budget metadata must exist before fallback text evidence is
			// serialized. Otherwise structured-only fields such as structural
			// omission counts disappear when the structured channel is removed.
			result = enforceAXResponseBudget(result)
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
