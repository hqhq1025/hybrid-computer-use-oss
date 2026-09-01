//go:build darwin || windows

package main

import (
	"runtime"
	"strings"
	"testing"
)

func TestPortableRuntimeBundleContainsBothNativeImplementations(t *testing.T) {
	bundle := portableRuntimeBundle()
	for _, path := range []string{
		"runtime_portable.py",
		"native/common/provider_v1/wire_v2.py",
		"native/macos/provider_v1/backend.py",
		"native/windows/provider_v1/backend.py",
	} {
		if strings.TrimSpace(bundle[path]) == "" {
			t.Fatalf("portable runtime bundle is missing %s", path)
		}
	}
	if !strings.Contains(bundle["runtime_portable.py"], "native.common.provider_v1.wire_v2") {
		t.Fatal("portable entrypoint does not use the shared Provider V2 boundary")
	}
}

func TestPortableDefaultBackendAdvertisesCurrentPlatform(t *testing.T) {
	backend := defaultRuntimeBackend()
	info := backend.HostInfo()
	if info.Platform != runtime.GOOS {
		t.Fatalf("backend platform = %q, want %q", info.Platform, runtime.GOOS)
	}
	capabilities := info.Capabilities
	if !capabilities.Accessibility || !capabilities.DesktopCapture ||
		!capabilities.InputSynthesis || !capabilities.WindowManagement {
		t.Fatalf("portable backend capabilities are incomplete: %#v", capabilities)
	}
}
