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

func TestCLIHelpMentionsNativeRuntimes(t *testing.T) {
	var out bytes.Buffer
	if err := runCLI([]string{"--help"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, platform := range []string{"Linux AT-SPI2", "macOS Accessibility", "Windows UI Automation"} {
		if !strings.Contains(out.String(), platform) {
			t.Fatalf("help text did not mention %s:\n%s", platform, out.String())
		}
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

// computer-use call prints the model-facing result: no structuredContent,
// and images as file paths rather than base64.
func TestCallPrintsModelFacingResultWithImageFiles(t *testing.T) {
	t.Setenv("COMPUTER_USE_OUTPUT_DIR", t.TempDir())
	dispatched := 1
	svc := newServiceWithBackend(&recordingPlatformBackend{desktopResponse: &ProviderDesktopResult{
		OK:        true,
		GUIResult: ProviderGUIResult{Dispatched: &dispatched},
		Snapshot: &appSnapshot{
			ScreenshotPNGBase64: testPNGBase64(t, 40, 30), CoordinateSpace: "screen_image_px",
			FrameID: "f", Width: 40, Height: 30,
		},
	}})
	output, _, err := runCallCommand([]string{"GUI", "--args", `{"actions":[{"type":"screenshot"}]}`}, svc)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "structuredContent") ||
		strings.Contains(string(encoded), `"data"`) ||
		!strings.Contains(string(encoded), `"path"`) {
		t.Fatalf("call output is not the model-facing result with image files: %s", encoded)
	}
}
