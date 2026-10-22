//go:build windows

package provider

import (
	"testing"

	"github.com/hqhq1025/hybrid-computer-use-oss/computer-use/internal/contract"
	"github.com/hqhq1025/hybrid-computer-use-oss/computer-use/internal/core"
)

func TestDefaultServiceUsesWindowsBackend(t *testing.T) {
	service := core.NewService()
	if _, ok := service.Backend.(windowsRuntimeBackend); !ok {
		t.Fatalf(
			"default backend = %T, want windowsRuntimeBackend",
			service.Backend,
		)
	}
}

func TestWindowsPublicToolSurfaceRemainsAXAndGUI(t *testing.T) {
	tools := contract.ToolDefinitions()
	if len(tools) != 2 {
		t.Fatalf("public tool count = %d, want 2", len(tools))
	}
	if tools[0].Name != "AX" || tools[1].Name != "GUI" {
		t.Fatalf(
			"public tools = %q, %q; want AX, GUI",
			tools[0].Name,
			tools[1].Name,
		)
	}
}
