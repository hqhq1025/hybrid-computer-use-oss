//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"
)

type windowsRuntimeBackend struct{}

var _ PlatformBackend = windowsRuntimeBackend{}

func (windowsRuntimeBackend) ObserveApps(
	request ProviderObserveAppsRequest,
) (ProviderAccessibilityResult, error) {
	if err := validateProviderObserveAppsRequest(request); err != nil {
		return ProviderAccessibilityResult{}, err
	}
	wire := newProviderWireRequest(
		nextWindowsProviderRequestID(), ProviderWireObserveApps,
	)
	wire.ObserveApps = &request
	response, err := runWindowsProvider(wire)
	if response == nil || response.Accessibility == nil {
		return ProviderAccessibilityResult{}, err
	}
	return *response.Accessibility, err
}

func (windowsRuntimeBackend) Observe(
	request ProviderObserveRequest,
) (ProviderAccessibilityResult, error) {
	if err := validateProviderObserveRequest(request); err != nil {
		return ProviderAccessibilityResult{}, err
	}
	wire := newProviderWireRequest(
		nextWindowsProviderRequestID(), ProviderWireObserve,
	)
	wire.Observe = &request
	response, err := runWindowsProvider(wire)
	if response == nil || response.Accessibility == nil {
		return ProviderAccessibilityResult{}, err
	}
	return *response.Accessibility, err
}

func (windowsRuntimeBackend) ResolveAndAct(
	request ProviderActionRequest,
) (ProviderAccessibilityResult, error) {
	if err := validateProviderActionRequest(request); err != nil {
		return ProviderAccessibilityResult{}, err
	}
	wire := newProviderWireRequest(
		nextWindowsProviderRequestID(), ProviderWireResolveAndAct,
	)
	wire.Action = &request
	response, err := runWindowsProvider(wire)
	if response == nil || response.Accessibility == nil {
		return ProviderAccessibilityResult{}, err
	}
	return *response.Accessibility, err
}

func (windowsRuntimeBackend) ExecuteBatch(
	request ProviderDesktopRequest,
) (ProviderDesktopResult, error) {
	if err := validateProviderDesktopRequest(request); err != nil {
		return ProviderDesktopResult{}, err
	}
	wire := newProviderWireRequest(
		nextWindowsProviderRequestID(), ProviderWireDesktopBatch,
	)
	wire.Desktop = &request
	response, err := runWindowsProvider(wire)
	if response == nil || response.Desktop == nil {
		return ProviderDesktopResult{}, err
	}
	return *response.Desktop, err
}

func (windowsRuntimeBackend) ListWindows(
	request ProviderWindowListRequest,
) (ProviderWindowResult, error) {
	if err := validateProviderWindowListRequest(request); err != nil {
		return ProviderWindowResult{}, err
	}
	wire := newProviderWireRequest(
		nextWindowsProviderRequestID(), ProviderWireWindowList,
	)
	wire.WindowList = &request
	response, err := runWindowsProvider(wire)
	if response == nil || response.Window == nil {
		return ProviderWindowResult{}, err
	}
	return *response.Window, err
}

func (windowsRuntimeBackend) ActOnWindow(
	request ProviderWindowActionRequest,
) (ProviderWindowResult, error) {
	if err := validateProviderWindowActionRequest(request); err != nil {
		return ProviderWindowResult{}, err
	}
	wire := newProviderWireRequest(
		nextWindowsProviderRequestID(), ProviderWireWindowAction,
	)
	wire.WindowAction = &request
	response, err := runWindowsProvider(wire)
	if response == nil || response.Window == nil {
		return ProviderWindowResult{}, err
	}
	return *response.Window, err
}

func (windowsRuntimeBackend) WindowForPID(pid int) (known, alive bool) {
	if pid <= 0 {
		return true, false
	}
	switch (windowsRuntimeBackend{}).ProcessState(pid) {
	case "missing":
		return true, false
	case "running":
	default:
		return false, false
	}
	result, err := (windowsRuntimeBackend{}).ListWindows(
		ProviderWindowListRequest{
			ProtocolVersion: providerProtocolV2,
			App:             strconv.Itoa(pid),
		},
	)
	if err != nil || !result.OK {
		return false, false
	}
	return true, len(result.WindowTitles) > 0
}

func (windowsRuntimeBackend) HostInfo() HostBackendInfo {
	return HostBackendInfo{
		Platform: "windows",
		Capabilities: PlatformCapabilities{
			Accessibility:        true,
			DesktopCapture:       true,
			InputSynthesis:       true,
			WindowManagement:     true,
			HostSessionDiscovery: true,
			ResourceCorrelation:  true,
		},
	}
}

func (windowsRuntimeBackend) RuntimeEnvironment(base []string) []string {
	if base == nil {
		return os.Environ()
	}
	return append([]string(nil), base...)
}

func (windowsRuntimeBackend) ProcessState(pid int) string {
	if pid <= 0 {
		return "missing"
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd := exec.CommandContext(
		ctx,
		"tasklist.exe",
		"/FI",
		fmt.Sprintf("PID eq %d", pid),
		"/FO",
		"CSV",
		"/NH",
	)
	output := &boundedWindowsProviderBuffer{limit: maxWindowsTasklistOutputBytes}
	cmd.Stdout = output
	cmd.WaitDelay = 100 * time.Millisecond
	err := cmd.Run()
	if err != nil || ctx.Err() != nil {
		return "unknown"
	}
	data, truncated := output.snapshot()
	if truncated {
		return "unknown"
	}
	return windowsTasklistProcessState(data, pid)
}

func defaultRuntimeBackend() PlatformBackend {
	return windowsRuntimeBackend{}
}

func shutdownPlatformRuntime() {
	shutdownWindowsProviderProcess()
}
