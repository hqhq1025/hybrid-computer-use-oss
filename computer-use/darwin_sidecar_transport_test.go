//go:build darwin || darwin_sidecar_test

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDarwinProviderArtifactRequiresAppBundle(t *testing.T) {
	if _, err := darwinProviderArtifactFromPath(
		"/tmp/computer-use-macos-provider",
	); err == nil {
		t.Fatal("loose macOS provider binary must be rejected")
	}
	artifact, err := darwinProviderArtifactFromPath(
		"/tmp/" + darwinProviderAppName,
	)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(artifact.bundlePath) != darwinProviderAppName {
		t.Fatalf("bundle path = %q", artifact.bundlePath)
	}
}

func TestDarwinProviderRequiresCompleteExecutableBundle(t *testing.T) {
	root := filepath.Join(t.TempDir(), darwinProviderAppName)
	executable := filepath.Join(
		root, "Contents", "MacOS", darwinProviderExecutableName,
	)
	library := filepath.Join(
		root, "Contents", "Frameworks", darwinProviderLibraryName,
	)
	info := filepath.Join(root, "Contents", "Info.plist")
	for path, data := range map[string]string{
		executable: "provider",
		library:    "library",
		info:       "plist",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	artifact := darwinProviderArtifact{
		bundlePath:     root,
		executablePath: executable,
		libraryPath:    library,
		infoPlistPath:  info,
	}
	if err := verifyDarwinProvider(artifact); err != nil {
		t.Fatalf("complete provider bundle failed: %v", err)
	}
	if err := os.Chmod(executable, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyDarwinProvider(artifact); err == nil {
		t.Fatal("non-executable provider must fail")
	}
	if err := os.Chmod(executable, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{executable, library, info} {
		if err := os.Rename(path, path+".saved"); err != nil {
			t.Fatal(err)
		}
		if err := verifyDarwinProvider(artifact); err == nil {
			t.Fatalf("missing component %s must fail", path)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := verifyDarwinProvider(artifact); err == nil {
			t.Fatalf("directory component %s must fail", path)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path+".saved", path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDarwinProviderResetRetryClassification(t *testing.T) {
	readOnly := []ProviderWireRequest{
		validDarwinObserveAppsRequest("apps"),
		func() ProviderWireRequest {
			request := newProviderWireRequest("observe", ProviderWireObserve)
			request.Observe = &ProviderObserveRequest{
				ProtocolVersion: providerProtocolV2,
				App:             "Editor",
			}
			return request
		}(),
		func() ProviderWireRequest {
			request := newProviderWireRequest("windows", ProviderWireWindowList)
			request.WindowList = &ProviderWindowListRequest{
				ProtocolVersion: providerProtocolV2,
				App:             "Editor",
			}
			return request
		}(),
		func() ProviderWireRequest {
			request := newProviderWireRequest("resolve", ProviderWireResolveAndAct)
			request.Action = &ProviderActionRequest{
				ProtocolVersion: providerProtocolV2,
				Intent:          AccessibilityResolveTarget,
				App:             "Editor",
				Target: &ProviderSemanticTarget{
					Selector: `button "Save"`,
					Role:     "button",
					Name:     "Save",
					HasName:  true,
				},
			}
			return request
		}(),
	}
	response := &ProviderWireResponse{
		Accessibility: &ProviderAccessibilityResult{OK: false},
	}
	for _, request := range readOnly {
		if !darwinProviderRequestCanRetryAfterReset(request, response) {
			t.Errorf("read-only operation %q was not retryable", request.Operation)
		}
	}

	mutation := newProviderWireRequest("click", ProviderWireResolveAndAct)
	mutation.Action = &ProviderActionRequest{
		ProtocolVersion: providerProtocolV2,
		Intent:          AccessibilityClick,
		App:             "Editor",
		Target: &ProviderSemanticTarget{
			Selector: `button "Save"`,
			Role:     "button",
			Name:     "Save",
			HasName:  true,
		},
		Click: &ProviderClickInput{ClickCount: 1},
	}
	if darwinProviderRequestCanRetryAfterReset(mutation, response) {
		t.Fatal("mutation request must never be replayed")
	}
	response.Accessibility.Mutation = &ProviderMutationFacts{
		MutationUnknown: true,
	}
	if darwinProviderRequestCanRetryAfterReset(readOnly[0], response) {
		t.Fatal("response with possible mutation must never be replayed")
	}
}

func TestDarwinProviderManagerRetriesReadOnlyResetAtMostOnce(t *testing.T) {
	request := validDarwinObserveAppsRequest("reset-read")
	responses := []*ProviderWireResponse{
		{
			Protocol:              providerProtocolName,
			SchemaVersion:         providerProtocolV2,
			RequestID:             request.RequestID,
			Operation:             request.Operation,
			ProviderResetRequired: true,
			Accessibility:         &ProviderAccessibilityResult{OK: false},
		},
		{
			Protocol:              providerProtocolName,
			SchemaVersion:         providerProtocolV2,
			RequestID:             request.RequestID,
			Operation:             request.Operation,
			ProviderResetRequired: true,
			Accessibility:         &ProviderAccessibilityResult{OK: false},
		},
	}
	originalLookup := darwinProviderArtifactLookup
	originalStarter := darwinProviderWorkerStarter
	darwinProviderArtifactLookup = func() (darwinProviderArtifact, error) {
		return darwinProviderArtifact{
			bundlePath:     "/tmp/" + darwinProviderAppName,
			executablePath: "/tmp/provider",
			libraryPath:    "/tmp/library",
			infoPlistPath:  "/tmp/Info.plist",
		}, nil
	}
	starts := 0
	darwinProviderWorkerStarter = func(
		darwinProviderArtifact,
	) (*darwinProviderWorker, error) {
		response := responses[starts]
		starts++
		data, err := json.Marshal(response)
		if err != nil {
			return nil, err
		}
		connection := &darwinTestConnection{
			writer: io.Discard,
			reader: strings.NewReader(string(data) + "\n"),
		}
		done := make(chan struct{})
		close(done)
		stderrDone := make(chan struct{})
		close(stderrDone)
		return &darwinProviderWorker{
			connection: connection,
			stdout:     bufio.NewReader(connection),
			stderr: &boundedProviderBuffer{
				limit: maxDarwinProviderStderrBytes,
			},
			done:       done,
			stderrDone: stderrDone,
		}, nil
	}
	t.Cleanup(func() {
		darwinProviderArtifactLookup = originalLookup
		darwinProviderWorkerStarter = originalStarter
		shutdownDarwinProviderProcess()
	})

	manager := darwinProviderProcessManager{}
	response, err := manager.run(request)
	if err != nil {
		t.Fatal(err)
	}
	if starts != 2 {
		t.Fatalf("provider starts = %d, want exactly 2", starts)
	}
	if response == nil || !response.ProviderResetRequired {
		t.Fatalf("second reset response = %#v", response)
	}
}

func TestDarwinProviderManagerNeverReplaysMutationReset(t *testing.T) {
	request := newProviderWireRequest("reset-mutation", ProviderWireResolveAndAct)
	request.Action = &ProviderActionRequest{
		ProtocolVersion: providerProtocolV2,
		Intent:          AccessibilityClick,
		App:             "Editor",
		Target: &ProviderSemanticTarget{
			Selector: `button "Save"`,
			Role:     "button",
			Name:     "Save",
			HasName:  true,
		},
		Click: &ProviderClickInput{ClickCount: 1},
	}
	wireResponse := ProviderWireResponse{
		Protocol:              providerProtocolName,
		SchemaVersion:         providerProtocolV2,
		RequestID:             request.RequestID,
		Operation:             request.Operation,
		ProviderResetRequired: true,
		Accessibility: &ProviderAccessibilityResult{
			OK: false,
			Mutation: &ProviderMutationFacts{
				MutationUnknown: true,
			},
		},
	}
	data, err := json.Marshal(wireResponse)
	if err != nil {
		t.Fatal(err)
	}
	originalLookup := darwinProviderArtifactLookup
	originalStarter := darwinProviderWorkerStarter
	darwinProviderArtifactLookup = func() (darwinProviderArtifact, error) {
		return darwinProviderArtifact{
			bundlePath:     "/tmp/" + darwinProviderAppName,
			executablePath: "/tmp/provider",
			libraryPath:    "/tmp/library",
			infoPlistPath:  "/tmp/Info.plist",
		}, nil
	}
	starts := 0
	darwinProviderWorkerStarter = func(
		darwinProviderArtifact,
	) (*darwinProviderWorker, error) {
		starts++
		connection := &darwinTestConnection{
			writer: io.Discard,
			reader: strings.NewReader(string(data) + "\n"),
		}
		done := make(chan struct{})
		close(done)
		stderrDone := make(chan struct{})
		close(stderrDone)
		return &darwinProviderWorker{
			connection: connection,
			stdout:     bufio.NewReader(connection),
			stderr: &boundedProviderBuffer{
				limit: maxDarwinProviderStderrBytes,
			},
			done:       done,
			stderrDone: stderrDone,
		}, nil
	}
	t.Cleanup(func() {
		darwinProviderArtifactLookup = originalLookup
		darwinProviderWorkerStarter = originalStarter
		shutdownDarwinProviderProcess()
	})

	manager := darwinProviderProcessManager{}
	response, err := manager.run(request)
	if err != nil {
		t.Fatal(err)
	}
	if starts != 1 {
		t.Fatalf("mutation provider starts = %d, want 1", starts)
	}
	if response == nil ||
		response.Accessibility == nil ||
		response.Accessibility.Mutation == nil ||
		!response.Accessibility.Mutation.MutationUnknown {
		t.Fatalf("mutation reset response = %#v", response)
	}
}

func TestDarwinProviderManagerRetriesStartupBeforeSendingRequest(t *testing.T) {
	request := validDarwinObserveAppsRequest("startup-retry")
	response := ProviderWireResponse{
		Protocol:      providerProtocolName,
		SchemaVersion: providerProtocolV2,
		RequestID:     request.RequestID,
		Operation:     request.Operation,
		Accessibility: &ProviderAccessibilityResult{OK: true},
	}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	originalLookup := darwinProviderArtifactLookup
	originalStarter := darwinProviderWorkerStarter
	darwinProviderArtifactLookup = func() (darwinProviderArtifact, error) {
		return darwinProviderArtifact{
			bundlePath:     "/tmp/" + darwinProviderAppName,
			executablePath: "/tmp/provider",
			libraryPath:    "/tmp/library",
			infoPlistPath:  "/tmp/Info.plist",
		}, nil
	}
	starts := 0
	darwinProviderWorkerStarter = func(
		darwinProviderArtifact,
	) (*darwinProviderWorker, error) {
		starts++
		if starts == 1 {
			return nil, errors.New("transient launch failure")
		}
		connection := &darwinTestConnection{
			writer: io.Discard,
			reader: strings.NewReader(string(data) + "\n"),
		}
		done := make(chan struct{})
		close(done)
		stderrDone := make(chan struct{})
		close(stderrDone)
		return &darwinProviderWorker{
			connection: connection,
			stdout:     bufio.NewReader(connection),
			stderr: &boundedProviderBuffer{
				limit: maxDarwinProviderStderrBytes,
			},
			done:       done,
			stderrDone: stderrDone,
		}, nil
	}
	t.Cleanup(func() {
		darwinProviderArtifactLookup = originalLookup
		darwinProviderWorkerStarter = originalStarter
		shutdownDarwinProviderProcess()
	})

	manager := darwinProviderProcessManager{}
	got, err := manager.run(request)
	if err != nil {
		t.Fatal(err)
	}
	if starts != 2 {
		t.Fatalf("provider starts = %d, want 2", starts)
	}
	if got == nil || got.Accessibility == nil || !got.Accessibility.OK {
		t.Fatalf("startup retry response = %#v", got)
	}
}

func TestDarwinProviderStartupRetriesAcrossLaunchServicesQuiescence(t *testing.T) {
	originalStarter := darwinProviderWorkerStarter
	originalSleep := darwinProviderRetrySleep
	starts := 0
	var waits []time.Duration
	darwinProviderWorkerStarter = func(
		darwinProviderArtifact,
	) (*darwinProviderWorker, error) {
		starts++
		if starts < 4 {
			return nil, fmt.Errorf("launch not admitted")
		}
		return &darwinProviderWorker{}, nil
	}
	darwinProviderRetrySleep = func(wait time.Duration) {
		waits = append(waits, wait)
	}
	t.Cleanup(func() {
		darwinProviderWorkerStarter = originalStarter
		darwinProviderRetrySleep = originalSleep
	})

	worker, err := startDarwinProviderWorkerWithRetry(
		darwinProviderArtifact{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if worker == nil {
		t.Fatal("startup returned no worker")
	}
	if starts != 4 {
		t.Fatalf("provider starts = %d, want 4", starts)
	}
	want := []time.Duration{
		250 * time.Millisecond,
		750 * time.Millisecond,
		1500 * time.Millisecond,
	}
	if len(waits) != len(want) {
		t.Fatalf("retry waits = %#v, want %#v", waits, want)
	}
	for index := range want {
		if waits[index] != want[index] {
			t.Fatalf("retry waits = %#v, want %#v", waits, want)
		}
	}
}

func TestDarwinProviderLaunchWithoutPIDFailsBeforeSocketDeadline(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "provider.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	done := make(chan struct{})
	close(done)
	worker := &darwinProviderWorker{
		done: done,
	}
	started := time.Now()
	connection, err := waitForDarwinProviderConnection(
		listener,
		worker,
		filepath.Join(t.TempDir(), "provider.pid"),
		time.Now().Add(time.Second),
		20*time.Millisecond,
	)
	if connection != nil {
		_ = connection.Close()
		t.Fatal("unexpected provider connection")
	}
	if err == nil ||
		!strings.Contains(err.Error(), "provider pid was not published") {
		t.Fatalf("startup error = %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 500*time.Millisecond {
		t.Fatalf("missing provider pid took %s to detect", elapsed)
	}
}

func TestDarwinProviderStartupTimeoutIsSeparatelyBounded(t *testing.T) {
	t.Setenv("COMPUTER_USE_RUNTIME_TIMEOUT_SECONDS", "30")
	if got := darwinProviderStartupTimeout(); got != 10*time.Second {
		t.Fatalf("default startup timeout = %s, want 10s", got)
	}
	t.Setenv("COMPUTER_USE_RUNTIME_TIMEOUT_SECONDS", "4")
	if got := darwinProviderStartupTimeout(); got != 4*time.Second {
		t.Fatalf("short startup timeout = %s, want 4s", got)
	}
}

func TestDarwinProviderTransportPreservesRequestSent(t *testing.T) {
	request := validDarwinObserveAppsRequest("request-sent")
	worker := &darwinProviderWorker{
		connection: &darwinTestConnection{
			writer: partialErrorWriter{},
			reader: strings.NewReader(""),
		},
		stdout: bufio.NewReader(strings.NewReader("")),
	}
	_, err := worker.call(request, time.Second)
	var transport *runtimeTransportError
	if !errors.As(err, &transport) {
		t.Fatalf("partial write error = %v, want transport error", err)
	}
	if !transport.requestSent || transport.phase != "write_request" {
		t.Fatalf(
			"transport evidence requestSent=%v phase=%q",
			transport.requestSent,
			transport.phase,
		)
	}
}

func TestDarwinProviderCallValidatesResponseIdentity(t *testing.T) {
	for _, field := range []string{"request_id", "operation"} {
		t.Run(field, func(t *testing.T) {
			request := validDarwinObserveAppsRequest("identity")
			response := ProviderWireResponse{
				Protocol:      providerProtocolName,
				SchemaVersion: providerProtocolV2,
				RequestID:     request.RequestID,
				Operation:     request.Operation,
				Accessibility: &ProviderAccessibilityResult{OK: true},
			}
			if field == "request_id" {
				response.RequestID = "other"
			} else {
				response.Operation = ProviderWireObserve
			}
			data, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			connection := &darwinTestConnection{
				writer: io.Discard,
				reader: strings.NewReader(string(data) + "\n"),
			}
			worker := &darwinProviderWorker{
				connection: connection,
				stdout:     bufio.NewReader(connection),
			}
			_, err = worker.call(request, time.Second)
			var transport *runtimeTransportError
			if !errors.As(err, &transport) || !transport.requestSent {
				t.Fatalf("%s mismatch error = %#v", field, err)
			}
			if field == "request_id" &&
				!strings.Contains(err.Error(), `request_id "other"`) {
				t.Fatalf("request-id diagnostic = %q", err)
			}
			if field == "operation" &&
				!strings.Contains(err.Error(), `operation "observe"`) {
				t.Fatalf("operation diagnostic = %q", err)
			}
		})
	}
}

func TestDarwinProviderCallRejectsUncorrelatedOrderedInputEvidence(
	t *testing.T,
) {
	ordered := darwinTestOrderedInputRequest("DOWN", "ENTER")
	request := newProviderWireRequest(
		"ordered-correlation",
		ProviderWireOrderedInput,
	)
	request.OrderedInput = &ProviderOrderedInputWireRequest{
		ProtocolVersion:             providerProtocolV2,
		ProviderOrderedInputRequest: ordered,
	}
	result := darwinTestOrderedInputSuccess(ordered)
	result.Steps[1].ActionIndex++
	response := ProviderWireResponse{
		Protocol:      providerProtocolName,
		SchemaVersion: providerProtocolV2,
		RequestID:     request.RequestID,
		Operation:     request.Operation,
		OrderedInput:  &result,
	}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	connection := &darwinTestConnection{
		writer: io.Discard,
		reader: strings.NewReader(string(data) + "\n"),
	}
	worker := &darwinProviderWorker{
		connection: connection,
		stdout:     bufio.NewReader(connection),
	}
	_, err = worker.call(request, time.Second)
	var transport *runtimeTransportError
	if !errors.As(err, &transport) ||
		transport.phase != "decode_response" ||
		!transport.requestSent ||
		!strings.Contains(err.Error(), "action_index") {
		t.Fatalf("uncorrelated ordered result error = %#v", err)
	}
}

func TestDarwinProviderInvalidEnvelopePreservesProviderReason(t *testing.T) {
	request := validDarwinObserveAppsRequest("identity")
	response := ProviderWireResponse{
		Protocol:      providerProtocolName,
		SchemaVersion: providerProtocolV2,
		RequestID:     "invalid",
		Operation:     ProviderWireObserveApps,
		Accessibility: &ProviderAccessibilityResult{
			OK:    false,
			Error: "request.action contains unknown fields: expected_window",
			Failure: &ProviderFailure{
				Code:          "invalid_request",
				Phase:         "precondition",
				FactsComplete: true,
			},
		},
	}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	connection := &darwinTestConnection{
		writer: io.Discard,
		reader: strings.NewReader(string(data) + "\n"),
	}
	worker := &darwinProviderWorker{
		connection: connection,
		stdout:     bufio.NewReader(connection),
	}
	_, err = worker.call(request, time.Second)
	var transport *runtimeTransportError
	if !errors.As(err, &transport) {
		t.Fatalf("invalid envelope error = %#v", err)
	}
	if !strings.Contains(err.Error(), "unknown fields: expected_window") {
		t.Fatalf("invalid envelope diagnostic = %q", err)
	}
}

func TestDarwinProviderResponseIsBounded(t *testing.T) {
	reader := bufio.NewReaderSize(
		strings.NewReader(strings.Repeat("x", 64)+"\n"),
		8,
	)
	line, err := readDarwinProviderLine(reader, 32)
	if !errors.Is(err, errDarwinProviderResponseTooLarge) {
		t.Fatalf("oversized response error = %v", err)
	}
	if line != nil {
		t.Fatalf("oversized response retained %d bytes", len(line))
	}
}

func TestReadDarwinProviderPIDRejectsInvalidFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provider.pid")
	for _, value := range []string{"", "not-a-pid", "0", "-1"} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readDarwinProviderPID(path); err == nil {
			t.Fatalf("pid file %q was accepted", value)
		}
	}
	if err := os.WriteFile(path, []byte("123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pid, err := readDarwinProviderPID(path)
	if err != nil || pid != 123 {
		t.Fatalf("pid = %d, err = %v", pid, err)
	}
}

func TestDarwinProviderStopWaitsForExactProviderProcess(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	stderrDone := make(chan struct{})
	close(stderrDone)
	worker := &darwinProviderWorker{
		cmd:         cmd,
		done:        done,
		stderrDone:  stderrDone,
		providerPID: cmd.Process.Pid,
	}
	go worker.wait()

	started := time.Now()
	worker.stop()
	if elapsed := time.Since(started); elapsed >= darwinProviderShutdownGrace {
		t.Fatalf("graceful provider stop took %s", elapsed)
	}
	if darwinProviderProcessExists(cmd.Process.Pid) {
		t.Fatalf("provider pid %d still exists after stop", cmd.Process.Pid)
	}
}

func TestDarwinProductionLaunchUsesNonWaitingLaunchServicesPrivateSocket(
	t *testing.T,
) {
	data, err := os.ReadFile("darwin_sidecar_transport.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, fragment := range []string{
		`"/usr/bin/open"`,
		`"-n"`,
		`net.Listen("unix", socketPath)`,
		`os.Chmod(socketPath, 0o600)`,
		`"--socket"`,
		`"--pid-file"`,
	} {
		if !strings.Contains(source, fragment) {
			t.Fatalf("macOS provider launcher missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		`"-W"`,
		"session_probe",
		"wait_for_change",
		"heartbeat",
		"screenshotBlob",
		"route_evidence",
		"--blob-dir",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("macOS transport retained obsolete contract %q", forbidden)
		}
	}
}

func TestDarwinProviderOpenCommandDoesNotWaitForApplicationExit(t *testing.T) {
	command := darwinProviderOpenCommand(
		"/tmp/"+darwinProviderAppName,
		"/tmp/provider.sock",
		"/tmp/provider.pid",
	)
	want := []string{
		"/usr/bin/open",
		"-n",
		"/tmp/" + darwinProviderAppName,
		"--args",
		"--socket",
		"/tmp/provider.sock",
		"--pid-file",
		"/tmp/provider.pid",
	}
	if strings.Join(command.Args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("open command args = %#v, want %#v", command.Args, want)
	}
}

func validDarwinObserveAppsRequest(requestID string) ProviderWireRequest {
	request := newProviderWireRequest(requestID, ProviderWireObserveApps)
	request.ObserveApps = &ProviderObserveAppsRequest{
		ProtocolVersion: providerProtocolV2,
	}
	return request
}

type darwinTestConnection struct {
	writer io.Writer
	reader io.Reader
}

func (connection *darwinTestConnection) Write(data []byte) (int, error) {
	return connection.writer.Write(data)
}

func (connection *darwinTestConnection) Read(data []byte) (int, error) {
	return connection.reader.Read(data)
}

func (*darwinTestConnection) Close() error {
	return nil
}

type partialErrorWriter struct{}

func (partialErrorWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	return 1, io.ErrUnexpectedEOF
}

func darwinTestOrderedInputRequest(keys ...string) ProviderOrderedInputRequest {
	handle := "w2:" + strings.Repeat("a", 32)
	capture := true
	request := ProviderOrderedInputRequest{
		ContractVersion: providerOrderedInputContractV1,
		App:             "a1",
		AppIdentity: &ProviderAppIdentity{
			ProviderBusName: "pid:42", ProcessGeneration: "pid:42:start:100",
		},
		WindowHandle: handle,
		ExpectedWindow: &ProviderWindowExpectation{
			WindowHandle: handle, ProviderBusName: "pid:42",
			ProcessGeneration: "pid:42:start:100", Role: "window", Name: "Document",
		},
		FinalObservation: ProviderObservationOptions{CaptureSnapshot: &capture},
	}
	for index, key := range keys {
		request.Steps = append(request.Steps, ProviderOrderedInputStep{
			ActionIndex: index + 4, Key: key,
		})
	}
	return request
}

func darwinTestOrderedInputSuccess(request ProviderOrderedInputRequest) ProviderOrderedInputResult {
	result := ProviderOrderedInputResult{
		ContractVersion: providerOrderedInputContractV1,
		OK:              true,
		Snapshot:        &ProviderSnapshot{WindowHandle: request.WindowHandle},
		Mutation:        ProviderMutationFacts{InputEventsEmitted: true, DesktopStateMutated: true},
	}
	for _, step := range request.Steps {
		result.Steps = append(result.Steps, ProviderOrderedInputStepResult{
			ActionIndex: step.ActionIndex, Status: ProviderOrderedInputCompleted,
			DispatchAttempted: true, Emission: ProviderOrderedInputEmissionComplete,
			WindowContinuity: ProviderOrderedInputWindowSame,
		})
	}
	return result
}
