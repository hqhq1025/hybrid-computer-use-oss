package main

import (
	"embed"
	"strings"
)

var linuxProviderPaths = []string{
	"native/linux/provider_v1/foundation.py",
	"native/linux/provider_v1/x11_windows.py",
	"native/linux/provider_v1/atspi_inventory.py",
	"native/linux/provider_v1/ax_node_properties.py",
	"native/linux/provider_v1/ax_records.py",
	"native/linux/provider_v1/ax_snapshot.py",
	"native/linux/provider_v1/ax_virtual_table.py",
	"native/linux/provider_v1/action_evidence.py",
	"native/linux/provider_v1/app_resolver.py",
	"native/linux/provider_v1/window_resolver.py",
	"native/linux/provider_v1/window_diagnostics.py",
	"native/linux/provider_v1/ref_continuity.py",
	"native/linux/provider_v1/recovery.py",
	"native/linux/provider_v1/element_resolver.py",
	"native/linux/provider_v1/gui_capture.py",
	"native/linux/provider_v1/gui_dispatch.py",
	"native/linux/provider_v1/snapshot.py",
	"native/linux/provider_v1/actionability.py",
	"native/linux/provider_v1/gui_input.py",
	"native/linux/provider_v1/input_primitives.py",
	"native/linux/provider_v1/ax_actions.py",
	"native/linux/provider_v1/ax_dispatch.py",
	"native/linux/provider_v1/ax_snapshot_diagnostics.py",
	"native/linux/provider_v1/ax_target_notes.py",
	"native/linux/provider_v1/ax_transition_evidence.py",
	"native/linux/provider_v1/primitives.py",
	"native/linux/provider_v1/tree_walk.py",
	"native/linux/provider_v1/response_builder.py",
	"native/linux/provider_v1/wire_v2.py",
}

//go:embed runtime.py native/__init__.py native/linux/__init__.py native/linux/provider_v1/*.py
var linuxRuntimeFS embed.FS

// linuxRuntimeScript remains a searchable effective source for source-contract
// tests. Production execution materializes and imports the provider package.
var linuxRuntimeScript = linuxProviderSource()

func linuxProviderSource() string {
	var source strings.Builder
	for _, path := range linuxProviderPaths {
		data, err := linuxRuntimeFS.ReadFile(path)
		if err != nil {
			panic(err)
		}
		source.Write(data)
	}
	return source.String()
}

func linuxRuntimeBundle() map[string]string {
	paths := append([]string{
		"runtime.py",
		"native/__init__.py",
		"native/linux/__init__.py",
		"native/linux/provider_v1/__init__.py",
	}, linuxProviderPaths...)
	bundle := make(map[string]string, len(paths))
	for _, path := range paths {
		data, err := linuxRuntimeFS.ReadFile(path)
		if err != nil {
			panic(err)
		}
		bundle[path] = string(data)
	}
	return bundle
}
