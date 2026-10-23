//go:build windows || windows_sidecar_test

package provider

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/hqhq1025/hybrid-computer-use-oss/computer-use/internal/core"
)

func TestWindowsProviderBoundReadResetPreservesOriginalFailure(t *testing.T) {
	t.Setenv("COMPUTER_USE_WINDOWS_PROVIDER", currentWindowsTestExecutable(t))
	t.Setenv("GO_WANT_WINDOWS_PROVIDER_HELPER", "reset-bound-read")
	counter := t.TempDir() + "/starts"
	requests := t.TempDir() + "/requests"
	t.Setenv("WINDOWS_PROVIDER_START_COUNTER", counter)
	t.Setenv("WINDOWS_PROVIDER_REQUEST_COUNTER", requests)
	useWindowsProviderTestCommand(t)
	shutdownWindowsProviderProcess()
	t.Cleanup(shutdownWindowsProviderProcess)
	apps, err := runWindowsProvider(validWindowsObserveAppsRequest("discover"))
	if err != nil {
		t.Fatal(err)
	}
	request := core.NewProviderWireRequest("bound-read", core.ProviderWireObserve)
	request.Observe = &core.ProviderObserveRequest{
		ProtocolVersion: core.ProviderProtocolVersion, App: "Editor",
		WindowHandle: apps.Accessibility.Text,
	}
	response, err := runWindowsProvider(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Accessibility == nil || response.Accessibility.Failure == nil ||
		response.Accessibility.Failure.Code != "provider_internal_error" ||
		response.Accessibility.Failure.Phase != "execution" ||
		!response.Accessibility.Failure.FactsComplete ||
		response.Accessibility.Error != "UIA read timed out (0x800705B4)" ||
		!response.ProviderResetRequired {
		encoded, _ := json.Marshal(response)
		t.Fatalf("reset retry replaced the original read failure: %s", encoded)
	}
	if starts := providerHelperStartCount(counter); starts != 1 {
		t.Fatalf("identity-bound request started %d workers, want 1", starts)
	}
	data, err := os.ReadFile(requests)
	if err != nil || strings.Count(string(data), "bound-read\n") != 1 {
		t.Fatalf("bound request was replayed: requests=%q err=%v", data, err)
	}
	// Teardown still happened: the next unbound discovery starts a new
	// worker and can issue fresh identities instead of reviving old ones.
	apps, err = runWindowsProvider(validWindowsObserveAppsRequest("rediscover"))
	if err != nil || apps.Accessibility == nil || !apps.Accessibility.OK ||
		apps.Accessibility.Text == request.Observe.WindowHandle || providerHelperStartCount(counter) != 2 {
		t.Fatalf("fresh discovery after reset failed: response=%#v err=%v", apps, err)
	}
}

func TestWindowsProviderResetRetryRejectsWorkerBoundReadIdentities(t *testing.T) {
	const handle = "w2:0123456789abcdef0123456789abcdef"
	response := &core.ProviderWireResponse{ProviderResetRequired: true, Accessibility: &core.ProviderAccessibilityResult{}}
	observe := func() core.ProviderWireRequest {
		request := core.NewProviderWireRequest("observe", core.ProviderWireObserve)
		request.Observe = &core.ProviderObserveRequest{ProtocolVersion: core.ProviderProtocolVersion, App: "Editor"}
		return request
	}
	resolve := func() core.ProviderWireRequest {
		request := core.NewProviderWireRequest("resolve", core.ProviderWireResolveAndAct)
		request.Action = &core.ProviderActionRequest{
			ProtocolVersion: core.ProviderProtocolVersion, App: "Editor", Intent: core.AccessibilityResolveTarget,
			Target: &core.ProviderSemanticTarget{Selector: `table "Sheet"`, Role: "table", Name: "Sheet", HasName: true},
		}
		return request
	}
	windows := func() core.ProviderWireRequest {
		request := core.NewProviderWireRequest("windows", core.ProviderWireWindowList)
		request.WindowList = &core.ProviderWindowListRequest{ProtocolVersion: core.ProviderProtocolVersion, App: "Editor"}
		return request
	}
	for _, test := range []struct {
		name string
		base func() core.ProviderWireRequest
		bind func(*core.ProviderWireRequest)
	}{
		{"observe window", observe, func(r *core.ProviderWireRequest) { r.Observe.WindowHandle = handle }},
		{"window list", windows, func(r *core.ProviderWireRequest) { r.WindowList.WindowHandle = handle }},
		{"resolve window", resolve, func(r *core.ProviderWireRequest) { r.Action.WindowHandle = handle }},
		{"root target window", resolve, func(r *core.ProviderWireRequest) { r.Action.Target.WindowHandle = handle }},
		{"expected window", resolve, func(r *core.ProviderWireRequest) {
			r.Action.WindowHandle = handle
			r.Action.ExpectedWindow = &core.ProviderWindowExpectation{WindowHandle: handle, ProviderBusName: "pid:42", ProcessGeneration: "started:1", Role: "window"}
		}},
		{"captured element", resolve, func(r *core.ProviderWireRequest) {
			r.Action.Target = nil
			r.Action.Element = &core.ProviderElement{RuntimeID: []int{1, 2}}
		}},
		{"provider locator", resolve, func(r *core.ProviderWireRequest) { r.Action.Target.LocatorKind = core.AXLocatorProviderBound }},
		{"provider object", resolve, func(r *core.ProviderWireRequest) { r.Action.Target.ProviderObjectPath = "runtime:1.2" }},
		{"provider window object", resolve, func(r *core.ProviderWireRequest) { r.Action.Target.ProviderWindowObjectPath = "runtime:1" }},
		{"runtime id", resolve, func(r *core.ProviderWireRequest) { r.Action.Target.RuntimeID = []int{1, 2} }},
		{"runtime aliases", resolve, func(r *core.ProviderWireRequest) { r.Action.Target.RuntimeIDAliases = [][]int{{1, 2}} }},
		{"edit target", resolve, func(r *core.ProviderWireRequest) {
			if err := json.Unmarshal([]byte(`{"editTarget":{"providerBusName":"pid:42","providerObjectPath":"runtime:1.2","processGeneration":"started:1"}}`), r.Action.Target); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := test.base()
			if !windowsProviderRequestCanRetryAfterReset(request, response) {
				t.Fatal("unbound read lost reset retry")
			}
			test.bind(&request)
			if err := request.Validate(); err != nil {
				t.Fatal(err)
			}
			if windowsProviderRequestCanRetryAfterReset(request, response) {
				t.Fatalf("read with old worker identity was retryable: %#v", request)
			}
		})
	}
	request := observe()
	request.Observe.AppIdentity = &core.ProviderAppIdentity{ProviderBusName: "pid:42", ProcessGeneration: "started:1"}
	if !windowsProviderRequestCanRetryAfterReset(request, response) {
		t.Fatal("process identity alone is not worker state")
	}
}
