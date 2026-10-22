package provider

import (
	"testing"

	"github.com/hqhq1025/hybrid-computer-use-oss/computer-use/internal/core"
)

func TestDefaultServiceUsesPlatformBackend(t *testing.T) {
	service := core.NewService()
	if _, ok := service.Backend.(linuxRuntimeBackend); !ok {
		t.Fatalf("default backend = %T, want linuxRuntimeBackend", service.Backend)
	}
}
