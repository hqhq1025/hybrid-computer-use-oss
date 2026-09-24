package main

import (
	"fmt"
	"time"
)

type runtimeTimeoutError struct {
	duration time.Duration
	phase    string
}

func (e *runtimeTimeoutError) Error() string {
	return fmt.Sprintf("Linux runtime timed out after %s", e.duration)
}

var _ error = (*runtimeTimeoutError)(nil)

type runtimeTransportError struct {
	operation   ProviderWireOperation
	phase       string
	requestSent bool
}

func (e *runtimeTransportError) Error() string {
	return fmt.Sprintf(
		"Linux provider transport failed during %s",
		e.operation,
	)
}

var _ error = (*runtimeTransportError)(nil)

func accessibilityOperationCanMutate(request axServiceRequest) bool {
	return request.Action != nil || request.WindowAction != nil
}

func (s *service) platformBackend() PlatformBackend {
	if s.backend != nil {
		return s.backend
	}
	return defaultRuntimeBackend()
}

func (s *service) callProviderObserveApps(
	request ProviderObserveAppsRequest,
) (*ProviderAccessibilityResult, error) {
	result, err := s.platformBackend().ObserveApps(request)
	result.prepareForService()
	return &result, err
}

func (s *service) callProviderObserve(
	request ProviderObserveRequest,
) (*ProviderAccessibilityResult, error) {
	result, err := s.platformBackend().Observe(request)
	result.prepareForService()
	return &result, err
}

func (s *service) callProviderAction(
	request ProviderActionRequest,
) (*ProviderAccessibilityResult, error) {
	result, err := s.platformBackend().ResolveAndAct(request)
	result.prepareForService()
	return &result, err
}

func (s *service) callProviderDesktop(
	request ProviderDesktopRequest,
) (*ProviderDesktopResult, error) {
	result, err := s.platformBackend().ExecuteBatch(request)
	return &result, err
}

func (s *service) callProviderWindowList(
	request ProviderWindowListRequest,
) (*ProviderWindowResult, error) {
	result, err := s.platformBackend().ListWindows(request)
	return &result, err
}

func (s *service) callProviderWindowAction(
	request ProviderWindowActionRequest,
) (*ProviderWindowResult, error) {
	result, err := s.platformBackend().ActOnWindow(request)
	return &result, err
}

func accessibilityResultFromWindow(
	result ProviderWindowResult,
) ProviderAccessibilityResult {
	accessibility := ProviderAccessibilityResult{
		OK:                        result.OK,
		Text:                      result.Text,
		Error:                     result.Error,
		Notes:                     result.Notes,
		Snapshot:                  result.Snapshot,
		Failure:                   result.Failure,
		RecoveryContext:           result.RecoveryContext,
		Mutation:                  result.Mutation,
		ActionEvidence:            result.ActionEvidence,
		LeaseState:                result.LeaseState,
		ExternalDesktopTransition: result.ExternalDesktopTransition,
	}
	accessibility.prepareForService()
	return accessibility
}

func (s *service) callProviderAccessibilityOperation(
	operation axServiceRequest,
) (*ProviderAccessibilityResult, error) {
	domains := 0
	if operation.Operation != "" {
		domains++
	}
	if operation.Action != nil {
		domains++
	}
	if operation.WindowAction != nil {
		domains++
	}
	if domains != 1 {
		return nil, fmt.Errorf(
			"accessibility request must select exactly one typed operation domain, got %d",
			domains,
		)
	}
	if operation.WindowAction != nil {
		result, err := s.callProviderWindowAction(
			providerWindowActionRequestFromOperation(operation),
		)
		accessibility := accessibilityResultFromWindow(*result)
		return &accessibility, err
	}
	if operation.Action != nil {
		return s.callProviderAction(
			providerActionRequestFromOperation(operation),
		)
	}
	switch operation.Operation {
	case axServiceObserveApps:
		return s.callProviderObserveApps(
			providerObserveAppsRequestFromOperation(operation),
		)
	case axServiceObserve:
		return s.callProviderObserve(
			providerObserveRequestFromOperation(operation),
		)
	default:
		return nil, fmt.Errorf(
			"unsupported accessibility service operation %q",
			operation.Operation,
		)
	}
}

func (s *service) runtimeTimeoutDiagnostic(snapshot *appSnapshot) string {
	if snapshot == nil || snapshot.App.PID <= 0 {
		return formatRuntimeTimeoutDiagnostic(0, false, false, false, false)
	}
	backend := s.platformBackend()
	pid := snapshot.App.PID
	processState := backend.ProcessState(pid)
	if processState == "missing" {
		return formatRuntimeTimeoutDiagnostic(pid, true, false, true, false)
	}
	if processState == "zombie" || processState == "exited" {
		return formatZombieRuntimeDiagnostic(pid)
	}
	windowKnown, windowAlive := backend.WindowForPID(pid)
	return formatRuntimeTimeoutDiagnostic(
		pid, true, true, windowKnown, windowAlive,
	)
}

func formatZombieRuntimeDiagnostic(pid int) string {
	return fmt.Sprintf(
		"a11y_responsive=false cached_pid_present=zombie "+
			"window_for_cached_pid=false cached_pid=%d. The cached process "+
			"generation is defunct and cannot become ready; do not keep retrying "+
			"the same PID. Re-observe application identity before any relaunch.",
		pid)
}

func formatRuntimeTimeoutDiagnostic(
	pid int,
	hasProcessIdentity bool,
	processAlive bool,
	windowKnown bool,
	windowAlive bool,
) string {
	if !hasProcessIdentity {
		return "a11y_responsive=false cached_pid_present=unknown " +
			"window_for_cached_pid=unknown. " +
			"The accessibility bridge did not answer, and this MCP session has no " +
			"cached process identity for an independent liveness check."
	}
	if !processAlive {
		return fmt.Sprintf(
			"a11y_responsive=false cached_pid_present=false "+
				"window_for_cached_pid=false cached_pid=%d. The cached PID no longer "+
				"exists; the application may have exited or restarted.",
			pid)
	}
	if !windowKnown {
		return fmt.Sprintf(
			"a11y_responsive=false cached_pid_present=true "+
				"window_for_cached_pid=unknown cached_pid=%d. The cached PID exists "+
				"but the accessibility bridge did not answer; "+
				"the bounded X11 window probe was unavailable.",
			pid)
	}
	if windowAlive {
		return fmt.Sprintf(
			"a11y_responsive=false cached_pid_present=true "+
				"window_for_cached_pid=true cached_pid=%d. The cached PID and a managed "+
				"X11 window exist, but the accessibility "+
				"bridge did not answer. Inspect the GUI instead of retrying the same "+
				"A11Y call indefinitely.",
			pid)
	}
	return fmt.Sprintf(
		"a11y_responsive=false cached_pid_present=true "+
			"window_for_cached_pid=false cached_pid=%d. The cached PID exists without "+
			"a managed X11 window; the application may be rebuilding its UI. A bounded "+
			"retry can be appropriate.",
		pid)
}
