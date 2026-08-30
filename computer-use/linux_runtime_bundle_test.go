//go:build linux

package main

import (
	"os"
	"sort"
	"strings"
	"testing"
)

func TestLinuxRuntimeBundleIsCompleteAndOrdered(t *testing.T) {
	bundle := linuxRuntimeBundle()
	wantPaths := []string{
		"native/__init__.py",
		"native/linux/__init__.py",
		"native/linux/provider_v1/__init__.py",
		"native/linux/provider_v1/action_evidence.py",
		"native/linux/provider_v1/actionability.py",
		"native/linux/provider_v1/app_resolver.py",
		"native/linux/provider_v1/atspi_inventory.py",
		"native/linux/provider_v1/ax_actions.py",
		"native/linux/provider_v1/ax_dispatch.py",
		"native/linux/provider_v1/ax_node_properties.py",
		"native/linux/provider_v1/ax_records.py",
		"native/linux/provider_v1/ax_snapshot.py",
		"native/linux/provider_v1/ax_snapshot_diagnostics.py",
		"native/linux/provider_v1/ax_target_notes.py",
		"native/linux/provider_v1/ax_transition_evidence.py",
		"native/linux/provider_v1/ax_virtual_table.py",
		"native/linux/provider_v1/element_resolver.py",
		"native/linux/provider_v1/foundation.py",
		"native/linux/provider_v1/gui_capture.py",
		"native/linux/provider_v1/gui_dispatch.py",
		"native/linux/provider_v1/gui_input.py",
		"native/linux/provider_v1/input_primitives.py",
		"native/linux/provider_v1/primitives.py",
		"native/linux/provider_v1/recovery.py",
		"native/linux/provider_v1/ref_continuity.py",
		"native/linux/provider_v1/response_builder.py",
		"native/linux/provider_v1/snapshot.py",
		"native/linux/provider_v1/tree_walk.py",
		"native/linux/provider_v1/window_diagnostics.py",
		"native/linux/provider_v1/window_resolver.py",
		"native/linux/provider_v1/wire_v2.py",
		"native/linux/provider_v1/x11_windows.py",
		"runtime.py",
	}
	gotPaths := make([]string, 0, len(bundle))
	for path := range bundle {
		gotPaths = append(gotPaths, path)
	}
	sort.Strings(gotPaths)
	if strings.Join(gotPaths, "\n") != strings.Join(wantPaths, "\n") {
		t.Fatalf("Linux runtime bundle paths differ:\ngot:\n%s\nwant:\n%s",
			strings.Join(gotPaths, "\n"), strings.Join(wantPaths, "\n"))
	}

	var sourceParts []string
	for _, path := range linuxProviderPaths {
		sourceParts = append(sourceParts, bundle[path])
	}
	wantSource := strings.Join(sourceParts, "")
	if linuxRuntimeScript != wantSource {
		t.Fatal("effective Linux runtime source does not match ordered fragments")
	}
}

func TestLinuxRuntimeEntrypointNamesEveryFragment(t *testing.T) {
	bundle := linuxRuntimeBundle()
	for _, name := range []string{
		"native.linux.provider_v1.wire_v2",
		"import main",
	} {
		if !strings.Contains(bundle["runtime.py"], name) {
			t.Fatalf("runtime.py does not reference %s", name)
		}
	}
	for _, forbidden := range []string{"ProviderFacade", "install_facade"} {
		if strings.Contains(bundle["runtime.py"], forbidden) ||
			strings.Contains(bundle["native/linux/provider_v1/__init__.py"], forbidden) {
			t.Fatalf("Linux runtime still references retired facade %q", forbidden)
		}
	}
}

func TestLinuxBackendUsesProviderV2WireWithoutLegacyPlatformRoundTrip(
	t *testing.T,
) {
	for _, path := range []string{
		"runtime_backend_linux.go",
		"linux_runtime_executor.go",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		source := string(data)
		for _, forbidden := range []string{
			"legacyRequestFromProvider",
			"legacyResponseFromProvider",
			"runLinuxRuntime",
		} {
			if strings.Contains(source, forbidden) {
				t.Fatalf(
					"%s still uses retired Linux platform adapter %q",
					path,
					forbidden,
				)
			}
		}
	}
}
