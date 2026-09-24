package main

import "testing"

func TestDefaultServiceUsesPlatformBackend(t *testing.T) {
	service := newService()
	if _, ok := service.backend.(linuxRuntimeBackend); !ok {
		t.Fatalf("default backend = %T, want linuxRuntimeBackend", service.backend)
	}
}
