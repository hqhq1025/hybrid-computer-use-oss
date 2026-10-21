//go:build darwin || darwin_sidecar_test

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	maxDarwinProviderResponseBytes = 64 << 20
	maxDarwinProviderStderrBytes   = 64 << 10
	darwinProviderShutdownGrace    = 2 * time.Second
	darwinProviderStartupLimit     = 10 * time.Second
	darwinProviderBaseTimeout      = 30 * time.Second
	// Native drag pacing takes 1.458s; this also covers paste consumption,
	// key delivery and click-to-keyboard barriers. Explicit waits are separate.
	darwinProviderDesktopActionBudget = 1500 * time.Millisecond
	darwinProviderLaunchPIDGrace      = 2 * time.Second
	darwinProviderAppName             = "ComputerUseProvider.app"
	darwinProviderExecutableName      = "computer-use-macos-provider"
	darwinProviderLibraryName         = "libComputerUseProviderKit.dylib"
)

var errDarwinProviderResponseTooLarge = errors.New(
	"macOS provider response exceeded the configured limit",
)

var (
	darwinProviderRequestSequence atomic.Uint64
	darwinProviderProcesses       darwinProviderProcessManager
	darwinProviderArtifactLookup  = darwinProviderExecutable
	darwinProviderWorkerStarter   = startDarwinProviderWorker
	darwinProviderRetrySleep      = time.Sleep
	darwinProviderOpenCommand     = func(bundlePath, socketPath, pidPath string) *exec.Cmd {
		return exec.Command(
			"/usr/bin/open",
			"-n",
			bundlePath,
			"--args",
			"--socket",
			socketPath,
			"--pid-file",
			pidPath,
		)
	}
)

type darwinProviderArtifact struct {
	bundlePath     string
	executablePath string
	libraryPath    string
	infoPlistPath  string
}

type darwinProviderProcessManager struct {
	mu          sync.Mutex
	worker      *darwinProviderWorker
	artifactKey string
}

type darwinProviderWorker struct {
	cmd         *exec.Cmd
	connection  io.ReadWriteCloser
	stdout      *bufio.Reader
	stderr      *boundedProviderBuffer
	done        chan struct{}
	stderrDone  chan struct{}
	providerPID int
	runtimeDir  string
	stopOnce    sync.Once
	waitErr     error
}

type darwinProviderReadResult struct {
	line []byte
	err  error
}

type darwinProviderWriteResult struct {
	written int
	err     error
}

func nextDarwinProviderRequestID() string {
	return fmt.Sprintf(
		"darwin-%d-%d",
		time.Now().UnixNano(),
		darwinProviderRequestSequence.Add(1),
	)
}

func darwinProviderRuntimeTimeout() time.Duration {
	return darwinProviderRequestTimeout(ProviderWireRequest{})
}

func darwinProviderRequestTimeout(request ProviderWireRequest) time.Duration {
	if raw := strings.TrimSpace(
		os.Getenv("COMPUTER_USE_RUNTIME_TIMEOUT_SECONDS"),
	); raw != "" {
		if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil &&
			seconds > 0 && seconds <= int64((1<<63-1)/time.Second) {
			// An explicit timeout remains the complete request deadline.
			return time.Duration(seconds) * time.Second
		}
	}
	timeout := darwinProviderBaseTimeout
	if request.Operation != ProviderWireDesktopBatch || request.Desktop == nil {
		return timeout
	}
	// Bound budgeting even for invalid requests; request validation owns rejection.
	actions := request.Desktop.Actions[:min(len(request.Desktop.Actions), guiMaxActions)]
	timeout += time.Duration(len(actions)) * darwinProviderDesktopActionBudget
	waits := 0
	for _, action := range actions {
		if action.Type == "wait" {
			waits++
		}
	}
	timeout += time.Duration(min(waits, guiMaxTotalWait)) * time.Second
	timeout += time.Duration(max(0, min(request.Desktop.SettleMS, guiMaxSettleMS))) * time.Millisecond
	return timeout
}

func darwinProviderStartupTimeout() time.Duration {
	timeout := darwinProviderRuntimeTimeout()
	if timeout > darwinProviderStartupLimit {
		return darwinProviderStartupLimit
	}
	return timeout
}

func darwinProviderExecutable() (darwinProviderArtifact, error) {
	if configured := strings.TrimSpace(
		os.Getenv("COMPUTER_USE_MACOS_PROVIDER"),
	); configured != "" {
		return darwinProviderArtifactFromPath(configured)
	}
	executable, err := os.Executable()
	if err != nil {
		return darwinProviderArtifact{}, err
	}
	return darwinProviderArtifactFromPath(filepath.Join(
		filepath.Dir(executable),
		darwinProviderAppName,
	))
}

func darwinProviderArtifactFromPath(
	path string,
) (darwinProviderArtifact, error) {
	cleanPath := filepath.Clean(path)
	if filepath.Ext(cleanPath) != ".app" {
		bundleRoot := filepath.Dir(filepath.Dir(filepath.Dir(cleanPath)))
		if filepath.Base(bundleRoot) != darwinProviderAppName ||
			filepath.Base(filepath.Dir(cleanPath)) != "MacOS" {
			return darwinProviderArtifact{}, fmt.Errorf(
				"macOS provider must be the %s app bundle",
				darwinProviderAppName,
			)
		}
		cleanPath = bundleRoot
	}
	if filepath.Base(cleanPath) != darwinProviderAppName {
		return darwinProviderArtifact{}, fmt.Errorf(
			"macOS provider app = %q, want %q",
			filepath.Base(cleanPath),
			darwinProviderAppName,
		)
	}
	return darwinProviderArtifact{
		bundlePath: cleanPath,
		executablePath: filepath.Join(
			cleanPath, "Contents", "MacOS", darwinProviderExecutableName,
		),
		libraryPath: filepath.Join(
			cleanPath, "Contents", "Frameworks", darwinProviderLibraryName,
		),
		infoPlistPath: filepath.Join(cleanPath, "Contents", "Info.plist"),
	}, nil
}

func verifyDarwinProvider(artifact darwinProviderArtifact) error {
	for _, item := range []struct {
		label string
		path  string
	}{
		{label: "executable", path: artifact.executablePath},
		{label: "library", path: artifact.libraryPath},
		{label: "Info.plist", path: artifact.infoPlistPath},
	} {
		info, err := os.Stat(item.path)
		if err != nil {
			return fmt.Errorf("stat macOS provider %s: %w", item.label, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("macOS provider %s must be a regular file", item.label)
		}
		if item.label == "executable" && info.Mode().Perm()&0o111 == 0 {
			return errors.New("macOS provider executable has no execute permission")
		}
	}
	return nil
}

func runDarwinProvider(
	request ProviderWireRequest,
) (*ProviderWireResponse, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if request.Action != nil {
		action := *request.Action
		action.Diagnostics = providerDiagnosticContextFromLookup(os.Getenv)
		request.Action = &action
	}
	return darwinProviderProcesses.run(request)
}

func (manager *darwinProviderProcessManager) run(
	request ProviderWireRequest,
) (*ProviderWireResponse, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()

	artifact, err := darwinProviderArtifactLookup()
	if err != nil {
		return nil, err
	}
	artifactKey := strings.Join([]string{
		artifact.bundlePath,
		artifact.executablePath,
		artifact.libraryPath,
		artifact.infoPlistPath,
	}, "\x00")
	if manager.worker == nil || manager.artifactKey != artifactKey {
		manager.stopLocked()
		worker, startErr := startDarwinProviderWorkerWithRetry(artifact)
		if startErr != nil {
			return nil, startErr
		}
		manager.worker = worker
		manager.artifactKey = artifactKey
	}

	response, err := manager.worker.call(
		request,
		darwinProviderRequestTimeout(request),
	)
	if err != nil {
		var rejection *runtimeRequestRejectionError
		if errors.As(err, &rejection) {
			return nil, err
		}
		manager.recordStderrLocked(request, err)
		manager.stopLocked()
		return nil, err
	}
	if response.ProviderResetRequired {
		manager.stopLocked()
		if darwinProviderRequestCanRetryAfterReset(request, response) {
			worker, startErr := startDarwinProviderWorkerWithRetry(artifact)
			if startErr != nil {
				return nil, startErr
			}
			manager.worker = worker
			manager.artifactKey = artifactKey
			response, err = manager.worker.call(
				request,
				darwinProviderRequestTimeout(request),
			)
			if err != nil {
				var rejection *runtimeRequestRejectionError
				if errors.As(err, &rejection) {
					return nil, err
				}
				manager.recordStderrLocked(request, err)
				manager.stopLocked()
				return nil, err
			}
			if response.ProviderResetRequired {
				manager.stopLocked()
			}
		}
	}
	return response, nil
}

func startDarwinProviderWorkerWithRetry(
	artifact darwinProviderArtifact,
) (*darwinProviderWorker, error) {
	delays := []time.Duration{
		250 * time.Millisecond,
		750 * time.Millisecond,
		1500 * time.Millisecond,
		3 * time.Second,
	}
	failures := make([]string, 0, len(delays)+1)
	for attempt := 0; attempt <= len(delays); attempt++ {
		worker, err := darwinProviderWorkerStarter(artifact)
		if err == nil {
			return worker, nil
		}
		failures = append(
			failures,
			fmt.Sprintf("attempt %d: %v", attempt+1, err),
		)
		if attempt < len(delays) {
			darwinProviderRetrySleep(delays[attempt])
		}
	}
	return nil, fmt.Errorf(
		"macOS provider startup failed after %d attempts: %s",
		len(failures),
		strings.Join(failures, "; "),
	)
}

func darwinProviderRequestCanRetryAfterReset(
	request ProviderWireRequest,
	response *ProviderWireResponse,
) bool {
	if response == nil || darwinProviderResponseMayHaveMutated(response) {
		return false
	}
	switch request.Operation {
	case ProviderWireObserveApps, ProviderWireObserve, ProviderWireWindowList:
		return true
	case ProviderWireResolveAndAct:
		return request.Action != nil &&
			request.Action.Intent == AccessibilityResolveTarget
	case ProviderWireOrderedInput:
		return response.OrderedInput != nil &&
			response.OrderedInput.RetrySameRequest
	default:
		return false
	}
}

func darwinProviderResponseMayHaveMutated(
	response *ProviderWireResponse,
) bool {
	if response == nil {
		return false
	}
	var mutation *ProviderMutationFacts
	switch {
	case response.Accessibility != nil:
		mutation = response.Accessibility.Mutation
	case response.OrderedInput != nil:
		value := response.OrderedInput.Mutation
		mutation = &value
	case response.Desktop != nil:
		mutation = response.Desktop.Mutation
	case response.Window != nil:
		mutation = response.Window.Mutation
	}
	return providerMutationCanInvalidate(mutation)
}

func (manager *darwinProviderProcessManager) recordStderrLocked(
	request ProviderWireRequest,
	callErr error,
) {
	if manager.worker == nil || manager.worker.stderr == nil {
		return
	}
	data, truncated := manager.worker.stderr.snapshot()
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return
	}
	fmt.Fprintf(
		os.Stderr,
		"[computer-use macos-provider request=%s operation=%s "+
			"stderr_bytes=%d stderr_truncated=%t "+
			"transport_error=%q]\n%s\n",
		boundedProviderLabel(request.RequestID),
		boundedProviderLabel(string(request.Operation)),
		len(data),
		truncated,
		callErr.Error(),
		data,
	)
}

func (manager *darwinProviderProcessManager) stopLocked() {
	if manager.worker != nil {
		manager.worker.stop()
	}
	manager.worker = nil
	manager.artifactKey = ""
}

func shutdownDarwinProviderProcess() {
	darwinProviderProcesses.mu.Lock()
	defer darwinProviderProcesses.mu.Unlock()
	darwinProviderProcesses.stopLocked()
}

func startDarwinProviderWorker(
	artifact darwinProviderArtifact,
) (*darwinProviderWorker, error) {
	if err := verifyDarwinProvider(artifact); err != nil {
		return nil, err
	}
	runtimeDir, err := os.MkdirTemp(
		"/tmp",
		"computer-use-macos-provider-*",
	)
	if err != nil {
		return nil, fmt.Errorf("create macOS provider runtime directory: %w", err)
	}
	cleanup := func() {
		_ = os.RemoveAll(runtimeDir)
	}
	socketPath := filepath.Join(runtimeDir, "provider.sock")
	pidPath := filepath.Join(runtimeDir, "provider.pid")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("listen for macOS provider: %w", err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = listener.Close()
		cleanup()
		return nil, fmt.Errorf("protect macOS provider socket: %w", err)
	}

	cmd := darwinProviderOpenCommand(
		artifact.bundlePath,
		socketPath,
		pidPath,
	)
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		_ = listener.Close()
		cleanup()
		return nil, fmt.Errorf("open macOS provider stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = listener.Close()
		cleanup()
		return nil, fmt.Errorf(
			"launch macOS provider app through LaunchServices: %w",
			err,
		)
	}
	worker := &darwinProviderWorker{
		cmd:        cmd,
		stderr:     &boundedProviderBuffer{limit: maxDarwinProviderStderrBytes},
		done:       make(chan struct{}),
		stderrDone: make(chan struct{}),
		runtimeDir: runtimeDir,
	}
	go worker.forwardStderr(stderrPipe)
	go worker.wait()

	deadline := time.Now().Add(darwinProviderStartupTimeout())
	if unixListener, ok := listener.(*net.UnixListener); ok {
		_ = unixListener.SetDeadline(deadline)
	}
	connection, err := waitForDarwinProviderConnection(
		listener,
		worker,
		pidPath,
		deadline,
		darwinProviderLaunchPIDGrace,
	)
	_ = listener.Close()
	_ = os.Remove(socketPath)
	if err != nil {
		worker.stop()
		return nil, fmt.Errorf(
			"accept macOS provider app connection: %w: %s",
			err,
			strings.TrimSpace(worker.stderrText()),
		)
	}
	providerPID, err := readDarwinProviderPIDUntil(pidPath, deadline)
	if err != nil {
		_ = connection.Close()
		if discoveredPID, pidErr := readDarwinProviderPID(pidPath); pidErr == nil {
			worker.providerPID = discoveredPID
		}
		worker.stop()
		return nil, fmt.Errorf("read macOS provider pid: %w", err)
	}
	worker.connection = connection
	worker.stdout = bufio.NewReader(connection)
	worker.providerPID = providerPID
	return worker, nil
}

type darwinProviderAcceptResult struct {
	connection net.Conn
	err        error
}

func waitForDarwinProviderConnection(
	listener net.Listener,
	worker *darwinProviderWorker,
	pidPath string,
	deadline time.Time,
	launchPIDGrace time.Duration,
) (net.Conn, error) {
	acceptResult := make(chan darwinProviderAcceptResult)
	acceptDone := make(chan struct{})
	defer close(acceptDone)
	go func() {
		connection, err := listener.Accept()
		result := darwinProviderAcceptResult{
			connection: connection,
			err:        err,
		}
		select {
		case acceptResult <- result:
		case <-acceptDone:
			// Startup can fail while Accept finishes. Only the receiver owns
			// a delivered connection; reclaim one that startup abandoned.
			if connection != nil {
				_ = connection.Close()
			}
		}
	}()

	startupTimer := time.NewTimer(time.Until(deadline))
	defer startupTimer.Stop()
	pidPoll := time.NewTicker(20 * time.Millisecond)
	defer pidPoll.Stop()

	launcherDone := worker.done
	var launchPIDTimer *time.Timer
	var launchPIDDeadline <-chan time.Time
	defer func() {
		if launchPIDTimer != nil {
			launchPIDTimer.Stop()
		}
	}()

	updateProviderPID := func() {
		if worker.providerPID > 0 {
			return
		}
		if providerPID, err := readDarwinProviderPID(pidPath); err == nil {
			worker.providerPID = providerPID
		}
	}

	for {
		select {
		case result := <-acceptResult:
			updateProviderPID()
			return result.connection, result.err
		case <-pidPoll.C:
			updateProviderPID()
			if worker.providerPID > 0 &&
				!darwinProviderProcessExists(worker.providerPID) {
				return nil, fmt.Errorf(
					"provider pid %d exited before connecting",
					worker.providerPID,
				)
			}
		case <-launcherDone:
			launcherDone = nil
			updateProviderPID()
			if worker.waitErr != nil {
				return nil, fmt.Errorf(
					"LaunchServices command failed: %w",
					worker.waitErr,
				)
			}
			if worker.providerPID == 0 {
				wait := launchPIDGrace
				if remaining := time.Until(deadline); wait > remaining {
					wait = remaining
				}
				if wait <= 0 {
					return nil, fmt.Errorf(
						"LaunchServices command exited but provider pid was not published",
					)
				}
				launchPIDTimer = time.NewTimer(wait)
				launchPIDDeadline = launchPIDTimer.C
			}
		case <-launchPIDDeadline:
			updateProviderPID()
			if worker.providerPID == 0 {
				return nil, fmt.Errorf(
					"LaunchServices command exited successfully but provider pid "+
						"was not published within %s",
					launchPIDGrace,
				)
			}
			launchPIDDeadline = nil
		case <-startupTimer.C:
			updateProviderPID()
			return nil, os.ErrDeadlineExceeded
		}
	}
}

func (worker *darwinProviderWorker) forwardStderr(stderr io.Reader) {
	defer close(worker.stderrDone)
	_, _ = io.Copy(worker.stderr, stderr)
}

func (worker *darwinProviderWorker) wait() {
	if worker.cmd != nil {
		worker.waitErr = worker.cmd.Wait()
	}
	close(worker.done)
}

func (worker *darwinProviderWorker) stderrText() string {
	if worker == nil || worker.stderr == nil {
		return ""
	}
	data, _ := worker.stderr.snapshot()
	return string(data)
}

func (worker *darwinProviderWorker) call(
	request ProviderWireRequest,
	timeout time.Duration,
) (*ProviderWireResponse, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	payload = append(payload, '\n')

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	writeChannel := make(chan darwinProviderWriteResult, 1)
	go func() {
		written, writeErr := worker.connection.Write(payload)
		writeChannel <- darwinProviderWriteResult{
			written: written,
			err:     writeErr,
		}
	}()

	select {
	case writeResult := <-writeChannel:
		if writeResult.err != nil || writeResult.written != len(payload) {
			if writeResult.err == nil {
				writeResult.err = io.ErrShortWrite
			}
			return nil, &runtimeTransportError{
				platform:    "macOS",
				operation:   request.Operation,
				phase:       "write_request",
				requestSent: writeResult.written > 0,
				cause:       writeResult.err,
			}
		}
	case <-timer.C:
		return nil, &runtimeTimeoutError{
			duration: timeout,
			phase:    "write_request",
			platform: "macOS",
		}
	}

	readChannel := make(chan darwinProviderReadResult, 1)
	go func() {
		line, readErr := readDarwinProviderLine(
			worker.stdout,
			maxDarwinProviderResponseBytes,
		)
		readChannel <- darwinProviderReadResult{line: line, err: readErr}
	}()

	var result darwinProviderReadResult
	select {
	case result = <-readChannel:
	case <-timer.C:
		return nil, &runtimeTimeoutError{
			duration: timeout,
			phase:    "read_response",
			platform: "macOS",
		}
	}
	if result.err != nil {
		return nil, &runtimeTransportError{
			platform:    "macOS",
			operation:   request.Operation,
			phase:       "read_response",
			requestSent: true,
			cause:       result.err,
		}
	}
	response, err := DecodeProviderWireResponse(result.line)
	if err != nil {
		return nil, &runtimeTransportError{
			platform:    "macOS",
			operation:   request.Operation,
			phase:       "decode_response",
			requestSent: true,
			cause:       err,
		}
	}
	if response.RequestID != request.RequestID {
		cause := fmt.Errorf(
			"provider returned request_id %q for a different request",
			boundedProviderLabel(response.RequestID),
		)
		if response.RequestID == "invalid" &&
			response.Accessibility != nil &&
			strings.TrimSpace(response.Accessibility.Error) != "" {
			cause = fmt.Errorf(
				"provider rejected the request envelope: %s",
				boundedProviderLabel(
					response.Accessibility.Error,
				),
			)
		}
		return nil, &runtimeTransportError{
			platform:    "macOS",
			operation:   request.Operation,
			phase:       "request_id_mismatch",
			requestSent: true,
			cause:       cause,
		}
	}
	if response.Operation != request.Operation {
		return nil, &runtimeTransportError{
			platform:    "macOS",
			operation:   request.Operation,
			phase:       "operation_mismatch",
			requestSent: true,
			cause: fmt.Errorf(
				"provider returned operation %q",
				response.Operation,
			),
		}
	}
	if response.RequestRejection != nil {
		return nil, &runtimeRequestRejectionError{
			operation: request.Operation,
			rejection: *response.RequestRejection,
		}
	}
	if request.Operation == ProviderWireOrderedInput {
		if request.OrderedInput == nil || response.OrderedInput == nil {
			return nil, &runtimeTransportError{
				platform:    "macOS",
				operation:   request.Operation,
				phase:       "decode_response",
				requestSent: true,
				cause:       errors.New("ordered-input request or response payload is missing"),
			}
		}
		if err := response.OrderedInput.ValidateFor(
			request.OrderedInput.ProviderOrderedInputRequest,
		); err != nil {
			return nil, &runtimeTransportError{
				platform:    "macOS",
				operation:   request.Operation,
				phase:       "decode_response",
				requestSent: true,
				cause:       err,
			}
		}
	}
	return &response, nil
}

func readDarwinProviderLine(
	reader *bufio.Reader,
	limit int,
) ([]byte, error) {
	if limit <= 0 {
		return nil, errDarwinProviderResponseTooLarge
	}
	line := make([]byte, 0, min(limit, 64*1024))
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) > limit-len(line) {
			return nil, errDarwinProviderResponseTooLarge
		}
		line = append(line, fragment...)
		switch {
		case err == nil:
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		default:
			return line, err
		}
	}
}

func readDarwinProviderPIDUntil(
	path string,
	deadline time.Time,
) (int, error) {
	var lastErr error
	for {
		pid, err := readDarwinProviderPID(path)
		if err == nil {
			return pid, nil
		}
		lastErr = err
		if !time.Now().Before(deadline) {
			return 0, lastErr
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readDarwinProviderPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf(
			"invalid pid %q",
			strings.TrimSpace(string(data)),
		)
	}
	return pid, nil
}

func terminateDarwinProvider(pid int) {
	if pid <= 0 {
		return
	}
	process, err := os.FindProcess(pid)
	if err == nil {
		_ = process.Signal(syscall.SIGTERM)
	}
}

func darwinProviderProcessExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func waitForDarwinProviderExit(pid int, timeout time.Duration) bool {
	if pid <= 0 {
		return true
	}
	deadline := time.Now().Add(timeout)
	for darwinProviderProcessExists(pid) {
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}

func waitForDarwinProviderCommand(
	done <-chan struct{},
	timeout time.Duration,
) bool {
	if done == nil {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func (worker *darwinProviderWorker) stop() {
	if worker == nil {
		return
	}
	worker.stopOnce.Do(func() {
		if worker.connection != nil {
			_ = worker.connection.Close()
		}
		terminateDarwinProvider(worker.providerPID)
		providerStopped := waitForDarwinProviderExit(
			worker.providerPID,
			darwinProviderShutdownGrace,
		)
		if !providerStopped {
			if worker.providerPID > 0 {
				if process, err := os.FindProcess(worker.providerPID); err == nil {
					_ = process.Kill()
				}
			}
			providerStopped = waitForDarwinProviderExit(
				worker.providerPID,
				darwinProviderShutdownGrace,
			)
		}
		commandStopped := waitForDarwinProviderCommand(
			worker.done,
			darwinProviderShutdownGrace,
		)
		if !commandStopped {
			if worker.cmd != nil && worker.cmd.Process != nil {
				_ = worker.cmd.Process.Kill()
			}
			commandStopped = waitForDarwinProviderCommand(
				worker.done,
				darwinProviderShutdownGrace,
			)
		}
		if commandStopped && worker.stderrDone != nil {
			select {
			case <-worker.stderrDone:
			case <-time.After(250 * time.Millisecond):
			}
		}
		if worker.runtimeDir != "" {
			_ = os.RemoveAll(worker.runtimeDir)
		}
	})
}
