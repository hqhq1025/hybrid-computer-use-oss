package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestCallSequenceStopsAfterFirstToolError(t *testing.T) {
	output, hasError, err := runCallCommand([]string{
		"--calls",
		`[{"tool":"AX","args":{"mode":"invalid"}},{"tool":"GUI","args":{"actions":[]}}]`,
	}, newService())
	if err != nil {
		t.Fatal(err)
	}
	if !hasError {
		t.Fatal("expected hasError")
	}
	items, ok := output.([]map[string]any)
	if !ok {
		t.Fatalf("output type = %T", output)
	}
	if len(items) != 1 {
		t.Fatalf("sequence output count = %d, want 1", len(items))
	}
}

func TestReadArgumentsAcceptsJSONObject(t *testing.T) {
	args, err := readArguments(`{"app":"Text Editor","pages":2}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if args["app"] != "Text Editor" {
		t.Fatalf("app = %v", args["app"])
	}
	if args["pages"].(json.Number).String() != "2" {
		t.Fatalf("pages = %v", args["pages"])
	}
}

func TestCLIHelpMentionsLinuxRuntime(t *testing.T) {
	var out bytes.Buffer
	if err := runCLI([]string{"--help"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Computer Use for Linux") {
		t.Fatalf("help text did not mention Linux runtime:\n%s", out.String())
	}
	if strings.Contains(out.String(), "open-computer-use") {
		t.Fatalf("help text leaked the retired upstream command:\n%s", out.String())
	}
}

func TestRemovedCLICompatibilityCommandsStayRemoved(t *testing.T) {
	for _, command := range []string{"list-apps", "snapshot"} {
		var output bytes.Buffer
		err := runCLI([]string{command}, &output)
		if err == nil || !strings.Contains(err.Error(), "unknown command") {
			t.Fatalf("removed CLI command %q was accepted: err=%v output=%q",
				command, err, output.String())
		}
	}
}
