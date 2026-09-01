//go:build darwin || windows

package main

// portableRuntimeBackend is the macOS/Windows implementation of the private
// Provider V2 boundary. The public AX and GUI contracts remain owned by the
// common Go service; only native discovery, capture and input live below this
// boundary.
type portableRuntimeBackend struct {
	platform string
}

var _ PlatformBackend = portableRuntimeBackend{}

func (backend portableRuntimeBackend) call(
	operation ProviderWireOperation,
	payload func(*ProviderWireRequest),
) (*ProviderWireResponse, error) {
	wire := newProviderWireRequest(nextPortableProviderRequestID(), operation)
	payload(&wire)
	return runPortableProvider(wire, backend)
}

func (backend portableRuntimeBackend) ObserveApps(
	request ProviderObserveAppsRequest,
) (ProviderAccessibilityResult, error) {
	if err := validateProviderObserveAppsRequest(request); err != nil {
		return ProviderAccessibilityResult{}, err
	}
	response, err := backend.call(ProviderWireObserveApps, func(wire *ProviderWireRequest) {
		wire.ObserveApps = &request
	})
	if response == nil || response.Accessibility == nil {
		return ProviderAccessibilityResult{}, err
	}
	return *response.Accessibility, err
}

func (backend portableRuntimeBackend) Observe(
	request ProviderObserveRequest,
) (ProviderAccessibilityResult, error) {
	if err := validateProviderObserveRequest(request); err != nil {
		return ProviderAccessibilityResult{}, err
	}
	response, err := backend.call(ProviderWireObserve, func(wire *ProviderWireRequest) {
		wire.Observe = &request
	})
	if response == nil || response.Accessibility == nil {
		return ProviderAccessibilityResult{}, err
	}
	return *response.Accessibility, err
}

func (backend portableRuntimeBackend) ResolveAndAct(
	request ProviderActionRequest,
) (ProviderAccessibilityResult, error) {
	if err := validateProviderActionRequest(request); err != nil {
		return ProviderAccessibilityResult{}, err
	}
	response, err := backend.call(ProviderWireResolveAndAct, func(wire *ProviderWireRequest) {
		wire.Action = &request
	})
	if response == nil || response.Accessibility == nil {
		return ProviderAccessibilityResult{}, err
	}
	return *response.Accessibility, err
}

func (backend portableRuntimeBackend) ExecuteBatch(
	request ProviderDesktopRequest,
) (ProviderDesktopResult, error) {
	if err := validateProviderDesktopRequest(request); err != nil {
		return ProviderDesktopResult{}, err
	}
	response, err := backend.call(ProviderWireDesktopBatch, func(wire *ProviderWireRequest) {
		wire.Desktop = &request
	})
	if response == nil || response.Desktop == nil {
		return ProviderDesktopResult{}, err
	}
	return *response.Desktop, err
}

func (backend portableRuntimeBackend) ListWindows(
	request ProviderWindowListRequest,
) (ProviderWindowResult, error) {
	if err := validateProviderWindowListRequest(request); err != nil {
		return ProviderWindowResult{}, err
	}
	response, err := backend.call(ProviderWireWindowList, func(wire *ProviderWireRequest) {
		wire.WindowList = &request
	})
	if response == nil || response.Window == nil {
		return ProviderWindowResult{}, err
	}
	return *response.Window, err
}

func (backend portableRuntimeBackend) ActOnWindow(
	request ProviderWindowActionRequest,
) (ProviderWindowResult, error) {
	if err := validateProviderWindowActionRequest(request); err != nil {
		return ProviderWindowResult{}, err
	}
	response, err := backend.call(ProviderWireWindowAction, func(wire *ProviderWireRequest) {
		wire.WindowAction = &request
	})
	if response == nil || response.Window == nil {
		return ProviderWindowResult{}, err
	}
	return *response.Window, err
}

func (backend portableRuntimeBackend) HostInfo() HostBackendInfo {
	return HostBackendInfo{
		Platform: backend.platform,
		Capabilities: PlatformCapabilities{
			Accessibility:        true,
			DesktopCapture:       true,
			InputSynthesis:       true,
			WindowManagement:     true,
			HostSessionDiscovery: true,
			ResourceCorrelation:  false,
		},
	}
}

func (portableRuntimeBackend) RuntimeEnvironment(base []string) []string {
	return base
}

func (backend portableRuntimeBackend) WindowForPID(pid int) (bool, bool) {
	state := backend.ProcessState(pid)
	if state == "missing" || state == "exited" || state == "zombie" {
		return true, false
	}
	return false, false
}

func (backend portableRuntimeBackend) ProcessState(pid int) string {
	return portableProcessState(pid)
}

func shutdownPlatformRuntime() {
	shutdownPortableProviderProcess()
}
