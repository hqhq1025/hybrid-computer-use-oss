//go:build darwin

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"syscall"
)

type darwinRuntimeBackend struct{}

var _ PlatformBackend = darwinRuntimeBackend{}
var _ ProviderOrderedInputBackend = darwinRuntimeBackend{}

func (darwinRuntimeBackend) ObserveApps(
	request ProviderObserveAppsRequest,
) (ProviderAccessibilityResult, error) {
	if err := validateProviderObserveAppsRequest(request); err != nil {
		return ProviderAccessibilityResult{}, err
	}
	wire := newProviderWireRequest(
		nextDarwinProviderRequestID(), ProviderWireObserveApps,
	)
	wire.ObserveApps = &request
	response, err := runDarwinProvider(wire)
	if response == nil || response.Accessibility == nil {
		return ProviderAccessibilityResult{}, err
	}
	return *response.Accessibility, err
}

func (darwinRuntimeBackend) Observe(
	request ProviderObserveRequest,
) (ProviderAccessibilityResult, error) {
	if err := validateProviderObserveRequest(request); err != nil {
		return ProviderAccessibilityResult{}, err
	}
	wire := newProviderWireRequest(
		nextDarwinProviderRequestID(), ProviderWireObserve,
	)
	wire.Observe = &request
	response, err := runDarwinProvider(wire)
	if response == nil || response.Accessibility == nil {
		return ProviderAccessibilityResult{}, err
	}
	return *response.Accessibility, err
}

func (darwinRuntimeBackend) ResolveAndAct(
	request ProviderActionRequest,
) (ProviderAccessibilityResult, error) {
	if err := validateProviderActionRequest(request); err != nil {
		return ProviderAccessibilityResult{}, err
	}
	wire := newProviderWireRequest(
		nextDarwinProviderRequestID(), ProviderWireResolveAndAct,
	)
	wire.Action = &request
	if providerTargetDiagnosticEnabled(os.Getenv) {
		target, _ := json.Marshal(request.Target)
		fmt.Fprintf(
			os.Stderr,
			"[computer-use private-target-diagnostic request=%s intent=%s target=%s]\n",
			wire.RequestID,
			request.Intent,
			target,
		)
	}
	response, err := runDarwinProvider(wire)
	if response == nil || response.Accessibility == nil {
		return ProviderAccessibilityResult{}, err
	}
	return *response.Accessibility, err
}

func (darwinRuntimeBackend) ExecuteOrderedInput(
	request ProviderOrderedInputRequest,
) (ProviderOrderedInputResult, error) {
	if err := request.Validate(); err != nil {
		return ProviderOrderedInputResult{}, err
	}
	request.Diagnostics = providerDiagnosticContextFromLookup(os.Getenv)
	wire := newProviderWireRequest(
		nextDarwinProviderRequestID(), ProviderWireOrderedInput,
	)
	wire.OrderedInput = &ProviderOrderedInputWireRequest{
		ProtocolVersion:             providerProtocolV2,
		ProviderOrderedInputRequest: request,
	}
	response, err := runDarwinProvider(wire)
	if response == nil || response.OrderedInput == nil {
		return ProviderOrderedInputResult{}, err
	}
	return *response.OrderedInput, err
}

func (darwinRuntimeBackend) ExecuteBatch(
	request ProviderDesktopRequest,
) (ProviderDesktopResult, error) {
	wire := newProviderWireRequest(
		nextDarwinProviderRequestID(), ProviderWireDesktopBatch,
	)
	wire.Desktop = &request
	response, err := runDarwinProvider(wire)
	if response == nil || response.Desktop == nil {
		return ProviderDesktopResult{}, err
	}
	return *response.Desktop, err
}

func (darwinRuntimeBackend) ListWindows(
	request ProviderWindowListRequest,
) (ProviderWindowResult, error) {
	if err := validateProviderWindowListRequest(request); err != nil {
		return ProviderWindowResult{}, err
	}
	wire := newProviderWireRequest(
		nextDarwinProviderRequestID(), ProviderWireWindowList,
	)
	wire.WindowList = &request
	response, err := runDarwinProvider(wire)
	if response == nil || response.Window == nil {
		return ProviderWindowResult{}, err
	}
	return *response.Window, err
}

func (darwinRuntimeBackend) ActOnWindow(
	request ProviderWindowActionRequest,
) (ProviderWindowResult, error) {
	if err := validateProviderWindowActionRequest(request); err != nil {
		return ProviderWindowResult{}, err
	}
	wire := newProviderWireRequest(
		nextDarwinProviderRequestID(), ProviderWireWindowAction,
	)
	wire.WindowAction = &request
	response, err := runDarwinProvider(wire)
	if response == nil || response.Window == nil {
		return ProviderWindowResult{}, err
	}
	return *response.Window, err
}

func (backend darwinRuntimeBackend) WindowForPID(
	pid int,
) (known, alive bool) {
	return darwinTimeoutWindowForPID(pid, backend.ProcessState(pid))
}

func (darwinRuntimeBackend) HostInfo() HostBackendInfo {
	return HostBackendInfo{
		Platform: "darwin",
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

func (darwinRuntimeBackend) RuntimeEnvironment(base []string) []string {
	if base == nil {
		return os.Environ()
	}
	return append([]string(nil), base...)
}

func (darwinRuntimeBackend) ProcessState(pid int) string {
	if pid <= 0 {
		return "missing"
	}
	if err := syscall.Kill(pid, 0); err == nil {
		return "running"
	} else if errors.Is(err, syscall.ESRCH) {
		return "missing"
	}
	return "unknown"
}

func defaultRuntimeBackend() PlatformBackend {
	return darwinRuntimeBackend{}
}

func shutdownPlatformRuntime() {
	shutdownDarwinProviderProcess()
}
