//go:build darwin

package provider

import (
	"testing"

	"github.com/hqhq1025/hybrid-computer-use-oss/computer-use/internal/core"
)

func TestDefaultServiceUsesDarwinBackend(t *testing.T) {
	service := core.NewService()
	if _, ok := service.Backend.(darwinRuntimeBackend); !ok {
		t.Fatalf(
			"default backend = %T, want darwinRuntimeBackend",
			service.Backend,
		)
	}
}
