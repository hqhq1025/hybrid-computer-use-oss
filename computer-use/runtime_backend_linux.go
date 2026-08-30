//go:build linux

package main

import "os"

type linuxRuntimeBackend struct{}

var _ PlatformBackend = linuxRuntimeBackend{}

func (backend linuxRuntimeBackend) ObserveApps(
	request ProviderObserveAppsRequest,
) (ProviderAccessibilityResult, error) {
	if err := validateProviderObserveAppsRequest(request); err != nil {
		return ProviderAccessibilityResult{}, err
	}
	wire := newProviderWireRequest(
		nextLinuxProviderRequestID(), ProviderWireObserveApps,
	)
	wire.ObserveApps = &request
	response, err := runLinuxProvider(wire, backend)
	if response == nil || response.Accessibility == nil {
		return ProviderAccessibilityResult{}, err
	}
	return *response.Accessibility, err
}

func (backend linuxRuntimeBackend) Observe(
	request ProviderObserveRequest,
) (ProviderAccessibilityResult, error) {
	if err := validateProviderObserveRequest(request); err != nil {
		return ProviderAccessibilityResult{}, err
	}
	wire := newProviderWireRequest(
		nextLinuxProviderRequestID(), ProviderWireObserve,
	)
	wire.Observe = &request
	response, err := runLinuxProvider(wire, backend)
	if response == nil || response.Accessibility == nil {
		return ProviderAccessibilityResult{}, err
	}
	return *response.Accessibility, err
}

func (backend linuxRuntimeBackend) ResolveAndAct(
	request ProviderActionRequest,
) (ProviderAccessibilityResult, error) {
	if err := validateProviderActionRequest(request); err != nil {
		return ProviderAccessibilityResult{}, err
	}
	wire := newProviderWireRequest(
		nextLinuxProviderRequestID(), ProviderWireResolveAndAct,
	)
	wire.Action = &request
	response, err := runLinuxProvider(wire, backend)
	if response == nil || response.Accessibility == nil {
		return ProviderAccessibilityResult{}, err
	}
	return *response.Accessibility, err
}

func (backend linuxRuntimeBackend) ExecuteBatch(
	request ProviderDesktopRequest,
) (ProviderDesktopResult, error) {
	if err := validateProviderDesktopRequest(request); err != nil {
		return ProviderDesktopResult{}, err
	}
	wire := newProviderWireRequest(
		nextLinuxProviderRequestID(), ProviderWireDesktopBatch,
	)
	wire.Desktop = &request
	response, err := runLinuxProvider(wire, backend)
	if response == nil || response.Desktop == nil {
		return ProviderDesktopResult{}, err
	}
	return *response.Desktop, err
}

func (backend linuxRuntimeBackend) ListWindows(
	request ProviderWindowListRequest,
) (ProviderWindowResult, error) {
	if err := validateProviderWindowListRequest(request); err != nil {
		return ProviderWindowResult{}, err
	}
	wire := newProviderWireRequest(
		nextLinuxProviderRequestID(), ProviderWireWindowList,
	)
	wire.WindowList = &request
	response, err := runLinuxProvider(wire, backend)
	if response == nil || response.Window == nil {
		return ProviderWindowResult{}, err
	}
	return *response.Window, err
}

func (backend linuxRuntimeBackend) ActOnWindow(
	request ProviderWindowActionRequest,
) (ProviderWindowResult, error) {
	if err := validateProviderWindowActionRequest(request); err != nil {
		return ProviderWindowResult{}, err
	}
	wire := newProviderWireRequest(
		nextLinuxProviderRequestID(), ProviderWireWindowAction,
	)
	wire.WindowAction = &request
	response, err := runLinuxProvider(wire, backend)
	if response == nil || response.Window == nil {
		return ProviderWindowResult{}, err
	}
	return *response.Window, err
}

func (backend linuxRuntimeBackend) WindowForPID(pid int) (known, alive bool) {
	return linuxX11WindowForPID(pid, backend.RuntimeEnvironment(nil))
}

func (linuxRuntimeBackend) HostInfo() HostBackendInfo {
	return HostBackendInfo{
		Platform: "linux",
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

func (linuxRuntimeBackend) RuntimeEnvironment(base []string) []string {
	if base == nil {
		base = os.Environ()
	}
	return linuxRuntimeEnvironment(base)
}

func (linuxRuntimeBackend) ProcessState(pid int) string {
	return processPIDState(pid, "/proc")
}

func defaultRuntimeBackend() PlatformBackend {
	return linuxRuntimeBackend{}
}

func shutdownPlatformRuntime() {
	shutdownLinuxProviderProcess()
}
