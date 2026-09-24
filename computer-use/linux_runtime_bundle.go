//go:build linux

package main

import (
	"embed"
	"strings"
)

var linuxProviderPaths = []string{
	"native/linux/provider/foundation.py",
	"native/linux/provider/x11_windows.py",
	"native/linux/provider/atspi_inventory.py",
	"native/linux/provider/ax_node_properties.py",
	"native/linux/provider/ax_records.py",
	"native/linux/provider/ax_snapshot.py",
	"native/linux/provider/ax_virtual_table.py",
	"native/linux/provider/action_evidence.py",
	"native/linux/provider/app_resolver.py",
	"native/linux/provider/window_resolver.py",
	"native/linux/provider/window_diagnostics.py",
	"native/linux/provider/ref_continuity.py",
	"native/linux/provider/recovery.py",
	"native/linux/provider/element_resolver.py",
	"native/linux/provider/gui_capture.py",
	"native/linux/provider/gui_dispatch.py",
	"native/linux/provider/snapshot.py",
	"native/linux/provider/actionability.py",
	"native/linux/provider/gui_input.py",
	"native/linux/provider/input_primitives.py",
	"native/linux/provider/ax_actions.py",
	"native/linux/provider/ax_dispatch.py",
	"native/linux/provider/ax_snapshot_diagnostics.py",
	"native/linux/provider/ax_target_notes.py",
	"native/linux/provider/ax_transition_evidence.py",
	"native/linux/provider/primitives.py",
	"native/linux/provider/tree_walk.py",
	"native/linux/provider/response_builder.py",
	"native/linux/provider/wire_v2.py",
}

//go:embed runtime.py native/__init__.py native/linux/__init__.py native/linux/provider/*.py
var linuxRuntimeFS embed.FS

// linuxRuntimeScript is the concatenated provider source that source-contract
// tests search. Production execution materializes and imports the provider
// package instead.
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
		"native/linux/provider/__init__.py",
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
