//go:build linux

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRuntimeCallsRouteByPlatformDomain(t *testing.T) {
	backend := &recordingPlatformBackend{
		accessibilityResponse: &ProviderAccessibilityResult{OK: true},
		desktopResponse:       &ProviderDesktopResult{OK: true},
		windowResponse:        &ProviderWindowResult{OK: true},
	}
	service := newServiceWithBackend(backend)
	tests := []struct {
		name string
		call func() error
		want string
	}{
		{name: "observe_apps", want: "observe_apps", call: func() error {
			_, err := service.callProviderObserveApps(ProviderObserveAppsRequest{
				ProtocolVersion: providerProtocolV2,
			})
			return err
		}},
		{name: "observe", want: "observe", call: func() error {
			_, err := service.callProviderObserve(ProviderObserveRequest{
				ProtocolVersion: providerProtocolV2, App: "Editor",
			})
			return err
		}},
		{name: "resolve_and_act", want: "resolve_and_act", call: func() error {
			_, err := service.callProviderAction(ProviderActionRequest{
				ProtocolVersion: providerProtocolV2,
				Intent:          AccessibilityClick,
				App:             "Editor",
				Target: &ProviderSemanticTarget{
					Selector: `button "Save"`,
					HasName:  true,
				},
				Click: &ProviderClickInput{},
			})
			return err
		}},
		{name: "desktop", want: "desktop_execute_batch", call: func() error {
			_, err := service.callProviderDesktop(ProviderDesktopRequest{
				ProtocolVersion: providerProtocolV2,
			})
			return err
		}},
		{name: "window_activate", want: "window_activate", call: func() error {
			_, err := service.callProviderWindowAction(ProviderWindowActionRequest{
				ProtocolVersion: providerProtocolV2,
				Action:          WindowActivate,
				App:             "Editor",
			})
			return err
		}},
		{name: "window_close", want: "window_close", call: func() error {
			_, err := service.callProviderWindowAction(ProviderWindowActionRequest{
				ProtocolVersion: providerProtocolV2,
				Action:          WindowClose,
				App:             "Editor",
			})
			return err
		}},
		{name: "window_list", want: "window_list", call: func() error {
			_, err := service.callProviderWindowList(ProviderWindowListRequest{
				ProtocolVersion: providerProtocolV2,
				App:             "Editor",
			})
			return err
		}},
	}
	for _, test := range tests {
		backend.calls = nil
		if err := test.call(); err != nil {
			t.Fatalf("%s call failed: %v", test.name, err)
		}
		if !reflect.DeepEqual(backend.calls, []string{test.want}) {
			t.Fatalf("%s routed to %v, want %s", test.name, backend.calls, test.want)
		}
	}
}

func TestAccessibilityOperationRoutesTypedWindowActions(t *testing.T) {
	backend := &recordingPlatformBackend{
		windowResponse: &ProviderWindowResult{
			OK: true,
			Mutation: &ProviderMutationFacts{
				InputEventsEmitted:  true,
				DesktopStateMutated: true,
			},
			ActionEvidence: &ProviderActionEvidence{
				InputEmitted: true,
			},
		},
	}
	service := newServiceWithBackend(backend)
	for _, action := range []WindowOperation{WindowActivate, WindowClose} {
		backend.calls = nil
		result, err := service.callProviderAccessibilityOperation(
			axServiceRequest{
				App:          "Editor",
				WindowAction: &action,
				SemanticTarget: &semanticTarget{
					Selector:    `window "Editor"`,
					Role:        "window",
					Name:        "Editor",
					HasName:     true,
					LocatorKind: AXLocatorProviderBound,
				},
			},
		)
		if err != nil {
			t.Fatalf("%s window action failed: %v", action, err)
		}
		if !reflect.DeepEqual(
			backend.calls,
			[]string{"window_" + string(action)},
		) {
			t.Fatalf("%s routed to %v", action, backend.calls)
		}
		if result == nil || !result.OK || !result.InputEventsEmitted ||
			!result.DesktopStateMutated || result.ActionEvidence == nil {
			t.Fatalf("%s result projection is incomplete: %#v", action, result)
		}
	}
}

func TestAccessibilityOperationRejectsConflictingTypedDomains(t *testing.T) {
	service := newServiceWithBackend(&recordingPlatformBackend{})
	action := WindowActivate
	_, err := service.callProviderAccessibilityOperation(axServiceRequest{
		Operation:    axServiceObserve,
		WindowAction: &action,
	})
	if err == nil || !strings.Contains(
		err.Error(),
		"exactly one typed operation domain",
	) {
		t.Fatalf("conflicting typed domains did not fail closed: %v", err)
	}
}

func TestRuntimeTimeoutDiagnosticUsesInjectedBackend(t *testing.T) {
	backend := &recordingPlatformBackend{
		processState: "running",
		windowKnown:  true,
		windowAlive:  true,
	}
	service := newServiceWithBackend(backend)
	got := service.runtimeTimeoutDiagnostic(&appSnapshot{
		App: appDescriptor{PID: 42},
	})
	if !strings.Contains(got, "cached_pid_present=true") ||
		!strings.Contains(got, "window_for_cached_pid=true") {
		t.Fatalf("timeout diagnostic did not use backend facts: %q", got)
	}
}

func TestRuntimeTimeoutDiagnosticsSeparateProcessWindowAndBridgeState(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		needle string
	}{
		{
			name:   "no cached identity",
			text:   formatRuntimeTimeoutDiagnostic(0, false, false, false, false),
			needle: "a11y_responsive=false cached_pid_present=unknown window_for_cached_pid=unknown",
		},
		{
			name:   "process exited",
			text:   formatRuntimeTimeoutDiagnostic(42, true, false, true, false),
			needle: "a11y_responsive=false cached_pid_present=false window_for_cached_pid=false cached_pid=42",
		},
		{
			name:   "bridge hung with window",
			text:   formatRuntimeTimeoutDiagnostic(42, true, true, true, true),
			needle: "a11y_responsive=false cached_pid_present=true window_for_cached_pid=true cached_pid=42",
		},
		{
			name:   "process rebuilding window",
			text:   formatRuntimeTimeoutDiagnostic(42, true, true, true, false),
			needle: "a11y_responsive=false cached_pid_present=true window_for_cached_pid=false cached_pid=42",
		},
	}
	for _, testCase := range cases {
		if !strings.Contains(testCase.text, testCase.needle) {
			t.Fatalf("%s diagnostic missing %q: %s",
				testCase.name, testCase.needle, testCase.text)
		}
	}

	root := t.TempDir()
	if processPIDExists(123, root) {
		t.Fatal("missing proc pid must report false")
	}
	if err := os.Mkdir(filepath.Join(root, "123"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !processPIDExists(123, root) {
		t.Fatal("existing proc pid directory must report true")
	}
	if err := os.WriteFile(
		filepath.Join(root, "123", "stat"),
		[]byte("123 (vlc media player) Z 1 2 3 4"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if processPIDExists(123, root) || processPIDState(123, root) != "zombie" {
		t.Fatal("a zombie proc entry must not be treated as a live application")
	}
	if text := formatZombieRuntimeDiagnostic(123); !strings.Contains(
		text,
		"cached_pid_present=zombie",
	) || !strings.Contains(text, "cannot become ready") {
		t.Fatalf("zombie diagnostic is not actionable: %s", text)
	}
	if err := os.WriteFile(
		filepath.Join(root, "123", "stat"),
		[]byte("123 (normal app) S 1 2 3 4"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if !processPIDExists(123, root) || processPIDState(123, root) != "running" {
		t.Fatal("a non-zombie proc entry must remain live")
	}
	output := "0x01200007  0  123 host Window A\n0x01400007  0  999 host Window B\n"
	if !wmctrlOutputHasPID(output, 123) || wmctrlOutputHasPID(output, 321) {
		t.Fatal("wmctrl PID parser returned the wrong liveness result")
	}
}
