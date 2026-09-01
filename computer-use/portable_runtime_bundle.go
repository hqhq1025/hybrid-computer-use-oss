//go:build darwin || windows

package main

import "embed"

// Both providers are embedded so release layout and protocol tests can inspect
// one deterministic bundle. runtime_portable.py imports only the active OS.
//
//go:embed runtime_portable.py native/common/provider_v1/*.py native/macos/provider_v1/*.py native/windows/provider_v1/*.py
var portableRuntimeFS embed.FS

func portableRuntimeBundle() map[string]string {
	paths := []string{
		"runtime_portable.py",
		"native/common/provider_v1/__init__.py",
		"native/common/provider_v1/images.py",
		"native/common/provider_v1/wire_v2.py",
		"native/macos/provider_v1/__init__.py",
		"native/macos/provider_v1/backend.py",
		"native/windows/provider_v1/__init__.py",
		"native/windows/provider_v1/backend.py",
	}
	bundle := make(map[string]string, len(paths))
	for _, path := range paths {
		data, err := portableRuntimeFS.ReadFile(path)
		if err != nil {
			panic(err)
		}
		bundle[path] = string(data)
	}
	return bundle
}
