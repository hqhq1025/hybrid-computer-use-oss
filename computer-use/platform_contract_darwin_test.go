//go:build darwin

package main

import "testing"

func TestDefaultServiceUsesDarwinBackend(t *testing.T) {
	service := newService()
	if _, ok := service.backend.(darwinRuntimeBackend); !ok {
		t.Fatalf(
			"default backend = %T, want darwinRuntimeBackend",
			service.backend,
		)
	}
}

func TestDarwinPublicToolSurfaceRemainsAXAndGUI(t *testing.T) {
	tools := toolDefinitions()
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
