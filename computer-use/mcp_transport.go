package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	err := runCLI(os.Args[1:], os.Stdout)
	shutdownPlatformRuntime()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runCLI(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stdout, helpText(""))
		return nil
	}

	switch args[0] {
	case "-h", "--help", "help":
		topic := ""
		if len(args) > 1 {
			topic = args[1]
		}
		fmt.Fprint(stdout, helpText(topic))
		return nil
	case "-v", "--version", "version":
		fmt.Fprintln(stdout, version)
		return nil
	case "mcp":
		return runMCP(os.Stdin, stdout)
	case "doctor":
		fmt.Fprintln(stdout, "Linux runtime: AT-SPI2 and GDK run against the signed-in desktop user's accessibility session. When the client starts without XDG_RUNTIME_DIR, DBUS_SESSION_BUS_ADDRESS, or display variables, computer-use tries to discover the same user's session from /run/user/<uid> and desktop processes.")
		return nil
	case "call":
		output, hasError, err := runCallCommand(args[1:], newService())
		if err != nil {
			return err
		}
		encoded, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, string(encoded))
		if hasError {
			return errors.New("tool call returned isError=true")
		}
		return nil
	default:
		return fmt.Errorf("unknown command: %s\n\n%s", args[0], helpText(""))
	}
}

func runCallCommand(args []string, svc *service) (any, bool, error) {
	if len(args) == 0 {
		return nil, false, errors.New("call requires a tool name or --calls/--calls-file")
	}

	var toolName, argsJSON, argsFile, callsJSON, callsFile string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch arg {
		case "--args":
			index++
			if index >= len(args) {
				return nil, false, errors.New("--args requires a value")
			}
			argsJSON = args[index]
		case "--args-file":
			index++
			if index >= len(args) {
				return nil, false, errors.New("--args-file requires a value")
			}
			argsFile = args[index]
		case "--calls":
			index++
			if index >= len(args) {
				return nil, false, errors.New("--calls requires a value")
			}
			callsJSON = args[index]
		case "--calls-file":
			index++
			if index >= len(args) {
				return nil, false, errors.New("--calls-file requires a value")
			}
			callsFile = args[index]
		default:
			if strings.HasPrefix(arg, "-") {
				return nil, false, fmt.Errorf("unknown call option: %s", arg)
			}
			if toolName != "" {
				return nil, false, errors.New("call accepts at most one tool name")
			}
			toolName = arg
		}
	}

	if callsJSON != "" || callsFile != "" {
		if toolName != "" || argsJSON != "" || argsFile != "" {
			return nil, false, errors.New("call sequence does not accept a tool name, --args, or --args-file")
		}
		calls, err := readCallSequence(callsJSON, callsFile)
		if err != nil {
			return nil, false, err
		}
		var outputs []map[string]any
		hasError := false
		for _, call := range calls {
			publicName, ok := publicToolCallName(call.Tool)
			if !ok {
				return nil, false, fmt.Errorf(
					"unsupported public tool %q; use AX or GUI", call.Tool)
			}
			result := svc.callTool(publicName, call.Args)
			outputs = append(outputs, map[string]any{"tool": publicName, "result": result})
			if result.IsError {
				hasError = true
				break
			}
		}
		return outputs, hasError, nil
	}

	if toolName == "" {
		return nil, false, errors.New("call requires a tool name or --calls/--calls-file")
	}
	arguments, err := readArguments(argsJSON, argsFile)
	if err != nil {
		return nil, false, err
	}
	publicName, ok := publicToolCallName(toolName)
	if !ok {
		return nil, false, fmt.Errorf(
			"unsupported public tool %q; use AX or GUI", toolName)
	}
	result := svc.callTool(publicName, arguments)
	return result, result.IsError, nil
}

type callSpec struct {
	Tool string
	Args map[string]any
}

func readArguments(inline, file string) (map[string]any, error) {
	if inline != "" && file != "" {
		return nil, errors.New("Use either inline JSON or a JSON file, not both")
	}
	if inline == "" && file == "" {
		return map[string]any{}, nil
	}
	source, err := readJSONSource(inline, file)
	if err != nil {
		return nil, err
	}
	var args map[string]any
	decoder := json.NewDecoder(strings.NewReader(source))
	decoder.UseNumber()
	if err := decoder.Decode(&args); err != nil {
		return nil, fmt.Errorf("Invalid JSON input: %w", err)
	}
	if args == nil {
		return nil, errors.New("--args must be a JSON object")
	}
	return args, nil
}

func readCallSequence(inline, file string) ([]callSpec, error) {
	if inline != "" && file != "" {
		return nil, errors.New("Use either --calls or --calls-file, not both")
	}
	source, err := readJSONSource(inline, file)
	if err != nil {
		return nil, err
	}
	var raw []map[string]any
	decoder := json.NewDecoder(strings.NewReader(source))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("Invalid JSON input: %w", err)
	}
	calls := make([]callSpec, 0, len(raw))
	for index, item := range raw {
		name, _ := item["tool"].(string)
		if name == "" {
			name, _ = item["name"].(string)
		}
		if name == "" {
			return nil, fmt.Errorf("call sequence item #%d requires a non-empty tool", index+1)
		}
		args, _ := item["args"].(map[string]any)
		if args == nil {
			args, _ = item["arguments"].(map[string]any)
		}
		if args == nil {
			args = map[string]any{}
		}
		calls = append(calls, callSpec{Tool: name, Args: args})
	}
	return calls, nil
}

func readJSONSource(inline, file string) (string, error) {
	if inline != "" {
		return inline, nil
	}
	if file == "" {
		return "", errors.New("JSON input is required")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

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
			// structuredContent was added after 2025-03-26 and remains optional.
			// Keep the richer internal result, but put the evidence on the
			// model-visible text channel before omitting the structured field.
			// Some clients prioritize structuredContent and hide sibling text
			// from the model, which made computer-use state appear unavailable.
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

func helpText(command string) string {
	switch command {
	case "mcp":
		return "Usage:\n  computer-use mcp\n\nStart the stdio MCP server.\n"
	case "call":
		return "Usage:\n  computer-use call <AX|GUI> [--args '<json-object>']\n  computer-use call --calls '<json-array>'\n\nOnly the public AX and GUI tools are accepted.\n"
	default:
		return `Computer Use for Linux

Usage:
  computer-use [command] [options]

Commands:
  mcp                  Start the stdio MCP server.
  doctor               Print Linux runtime notes.
  call <AX|GUI>         Call one public tool, or run a JSON array of calls.
  help [command]       Show general or command-specific help.
  version              Print the CLI version.

Notes:
  The Linux runtime uses AT-SPI2 semantic actions first, then best-effort
  coordinate/key synthesis. Run it in the signed-in desktop session.
`
	}
}
