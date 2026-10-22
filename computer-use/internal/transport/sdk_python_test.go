package transport

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/hqhq1025/hybrid-computer-use-oss/computer-use/internal/contract"
	"github.com/hqhq1025/hybrid-computer-use-oss/computer-use/internal/core"
)

// The Python SDK and PTC adapter run against a daemon serving the fake
// desktop. This covers the drift guards: every SDK helper builds a call the
// core accepts, every schema error code has an SDK exception, and the PTC
// definitions come from the live schema.
func TestPythonSDKAgainstDaemon(t *testing.T) {
	python := "/usr/bin/python3"
	if _, err := os.Stat(python); err != nil {
		t.Skip("python3 is not installed")
	}
	t.Setenv("COMPUTER_USE_ERROR_CONTEXT", "off")
	t.Setenv("COMPUTER_USE_OUTPUT_DIR", t.TempDir())
	socket := shortSocket(t)
	server := newDaemon(socket, core.NewDesktopState(newFakeDesktopBackend()),
		core.DefaultRuntimeBackend().RuntimeEnvironment(os.Environ()), time.Minute)
	if err := server.listen(); err != nil {
		t.Fatal(err)
	}
	go server.serve()
	defer server.stop()
	script, err := filepath.Abs(filepath.Join("..", "..", "..", "sdk", "python", "tests", "test_sdk.py"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(python, script)
	command.Env = append(os.Environ(), "HCU_SOCKET="+socket)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("SDK tests failed: %v\n%s", err, output)
	}

	// Validate the actual Python PTC schema, including records-only read and
	// transport options inside every closed AX mode branch.
	command = exec.Command(python, "-c", `import json, sys
sys.path.insert(0, sys.argv[1])
from hcu_sdk import Desktop, SocketTransport, ptc
desktop = Desktop(SocketTransport(start=False))
print(json.dumps(ptc.tool_definitions(desktop.schema())))
desktop.close()
`, filepath.Dir(filepath.Dir(script)))
	command.Env = append(os.Environ(), "HCU_SOCKET="+socket)
	output, err = command.CombinedOutput()
	if err != nil {
		t.Fatalf("read PTC schema: %v\n%s", err, output)
	}
	var definitions []struct {
		Name   string         `json:"name"`
		Schema map[string]any `json:"input_schema"`
	}
	if err := json.Unmarshal(output, &definitions); err != nil {
		t.Fatal(err)
	}
	var tools []contract.ToolDefinition
	for _, definition := range definitions {
		tools = append(tools, contract.ToolDefinition{Name: definition.Name, InputSchema: definition.Schema})
	}
	schemas, err := contract.CompileInputSchemas(tools)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args  string
		valid bool
	}{
		{`{"mode":"apps","request_id":"a"}`, true},
		{`{"mode":"observe","window_ref":"w1","since":"s1"}`, true},
		{`{"mode":"find","window_ref":"w1","query":{"role":"button"},"request_id":"a"}`, true},
		{`{"mode":"find","cursor":"f1","limit":2,"request_id":"a"}`, true},
		{`{"mode":"act","actions":[{"type":"click","ref":"r1"}],"request_id":"a"}`, true},
		{`{"mode":"read","refs":["r1"],"request_id":"a"}`, true},
		{`{"mode":"read"}`, false},
		{`{"mode":"apps","refs":["r1"]}`, false},
		{`{"mode":"observe","window_ref":"w1","limit":20}`, false},
		{`{"mode":"find","cursor":"f1","window_ref":"w1","query":{"role":"button"}}`, false},
	} {
		var args map[string]any
		if err := json.Unmarshal([]byte(tc.args), &args); err != nil {
			t.Fatal(err)
		}
		if err := schemas["hcu_ax"].Validate(args); (err == nil) != tc.valid {
			t.Errorf("PTC %s accepted=%v want %v: %v", tc.args, err == nil, tc.valid, err)
		}
	}
}
