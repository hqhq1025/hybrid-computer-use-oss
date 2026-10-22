//go:build windows || windows_sidecar_test

package provider

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hqhq1025/hybrid-computer-use-oss/computer-use/internal/core"
)

const maxWindowsProviderResponseBytes = 64 << 20
const maxWindowsProviderStderrBytes = 64 << 10
const maxWindowsProviderStderrLineBytes = 64 << 10
const windowsProviderShutdownGrace = 250 * time.Millisecond
const windowsProviderKillWait = 250 * time.Millisecond
const windowsProviderBaseTimeout = 30 * time.Second
const windowsProviderDesktopActionBudget = 500 * time.Millisecond
const windowsProviderDesktopWaitBudget = time.Second

var errWindowsProviderResponseTooLarge = errors.New(
	"Windows provider response exceeded the configured limit",
)

var (
	windowsProviderRequestSequence atomic.Uint64
	windowsProviderProcesses       windowsProviderProcessManager
	windowsProviderCommand         = func(path string) *exec.Cmd {
		return exec.Command(path)
	}
	windowsProviderWorkerStarter              = startWindowsProviderWorker
	windowsProviderDiagnosticWriter io.Writer = os.Stderr
)

type windowsProviderProcessManager struct {
	mu             sync.Mutex
	worker         *windowsProviderWorker
	environmentKey string
	startGate      chan struct{}
}

type windowsProviderWorker struct {
	cmd                *exec.Cmd
	stdin              io.WriteCloser
	stdout             *bufio.Reader
	stderr             *boundedProviderBuffer
	done               chan struct{}
	stderrDone         chan struct{}
	parentDeathBinding io.Closer
	bindingCloseOnce   sync.Once
	stopOnce           sync.Once
	waitErr            error
}

type windowsProviderReadResult struct {
	line []byte
	err  error
}

type windowsProviderWriteResult struct {
	written int
	err     error
}

type windowsProviderStartResult struct {
	worker *windowsProviderWorker
	err    error
}

func nextWindowsProviderRequestID() string {
	return fmt.Sprintf(
		"windows-%d-%d",
		time.Now().UnixNano(),
		windowsProviderRequestSequence.Add(1),
	)
}

func windowsProviderRuntimeTimeout(
	request core.ProviderWireRequest,
) time.Duration {
	timeout := windowsProviderBaseTimeout
	if raw := strings.TrimSpace(
		os.Getenv("COMPUTER_USE_RUNTIME_TIMEOUT_SECONDS"),
	); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			timeout = time.Duration(seconds) * time.Second
		}
	}
	if request.Operation != core.ProviderWireDesktopBatch ||
		request.Desktop == nil {
		return timeout
	}
	timeout += time.Duration(len(request.Desktop.Actions)) *
		windowsProviderDesktopActionBudget
	for _, action := range request.Desktop.Actions {
		if action.Type == "wait" {
			timeout += windowsProviderDesktopWaitBudget
		}
	}
	if request.Desktop.SettleMS > 0 {
		timeout += time.Duration(request.Desktop.SettleMS) * time.Millisecond
	}
	return timeout
}

func windowsProviderExecutable() (string, error) {
	if configured := strings.TrimSpace(
		os.Getenv("COMPUTER_USE_WINDOWS_PROVIDER"),
	); configured != "" {
		return configured, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	path := filepath.Join(
		filepath.Dir(executable),
		"computer-use-windows-provider.exe",
	)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf(
			"Windows provider artifact is unavailable at %s: %w",
			path,
			err,
		)
	}
	return path, nil
}

func runWindowsProvider(
	request core.ProviderWireRequest,
) (*core.ProviderWireResponse, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	return windowsProviderProcesses.run(request)
}

func (manager *windowsProviderProcessManager) run(
	request core.ProviderWireRequest,
) (*core.ProviderWireResponse, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()

	timeout := windowsProviderRuntimeTimeout(request)
	deadline := time.Now().Add(timeout)
	providerPath, err := windowsProviderExecutable()
	if err != nil {
		return nil, err
	}
	environment := os.Environ()
	environmentKey := providerPath + "\x00" +
		strings.Join(environment, "\x00")
	if manager.worker == nil || manager.environmentKey != environmentKey {
		manager.stopLocked()
		worker, startErr := manager.startWorkerWithin(
			providerPath,
			environment,
			deadline,
			timeout,
		)
		if startErr != nil {
			return nil, startErr
		}
		manager.worker = worker
		manager.environmentKey = environmentKey
	}

	response, err := manager.worker.call(
		request,
		windowsProviderRemainingTimeout(deadline),
	)
	if err != nil {
		var rejection *core.RuntimeRequestRejectionError
		if errors.As(err, &rejection) {
			return nil, err
		}
		manager.recordStderrLocked(request, err)
		manager.stopLocked()
		return nil, err
	}
	if response.ProviderResetRequired {
		manager.stopLocked()
		if windowsProviderRequestCanRetryAfterReset(request, response) {
			worker, startErr := manager.startWorkerWithin(
				providerPath,
				environment,
				deadline,
				timeout,
			)
			if startErr != nil {
				return nil, startErr
			}
			manager.worker = worker
			manager.environmentKey = environmentKey
			response, err = manager.worker.call(
				request,
				windowsProviderRemainingTimeout(deadline),
			)
			if err != nil {
				var rejection *core.RuntimeRequestRejectionError
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

func windowsProviderRemainingTimeout(deadline time.Time) time.Duration {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return time.Nanosecond
	}
	return remaining
}

func (manager *windowsProviderProcessManager) startWorkerWithin(
	path string,
	environment []string,
	deadline time.Time,
	timeout time.Duration,
) (*windowsProviderWorker, error) {
	if manager.startGate == nil {
		manager.startGate = make(chan struct{}, 1)
		manager.startGate <- struct{}{}
	}
	gateTimer := time.NewTimer(windowsProviderRemainingTimeout(deadline))
	defer gateTimer.Stop()
	select {
	case <-manager.startGate:
	case <-gateTimer.C:
		writeWindowsProviderLifecycleDiagnostic(
			"start_provider",
			timeout,
			"startup gate deadline exceeded",
		)
		return nil, &core.RuntimeTimeoutError{
			Duration: timeout,
			Platform: "Windows",
			Phase:    "start_provider",
		}
	}

	resultChannel := make(chan windowsProviderStartResult)
	cancel := make(chan struct{})
	go func() {
		defer func() {
			manager.startGate <- struct{}{}
		}()
		worker, err := windowsProviderWorkerStarter(
			path,
			environment,
		)
		result := windowsProviderStartResult{worker: worker, err: err}
		select {
		case resultChannel <- result:
		case <-cancel:
			if worker != nil {
				worker.stop()
			}
		}
	}()

	startTimer := time.NewTimer(windowsProviderRemainingTimeout(deadline))
	defer startTimer.Stop()
	select {
	case result := <-resultChannel:
		if !time.Now().Before(deadline) {
			close(cancel)
			if result.worker != nil {
				result.worker.stop()
			}
			writeWindowsProviderLifecycleDiagnostic(
				"start_provider",
				timeout,
				"provider startup completed after its deadline",
			)
			return nil, &core.RuntimeTimeoutError{
				Duration: timeout,
				Platform: "Windows",
				Phase:    "start_provider",
			}
		}
		return result.worker, result.err
	case <-startTimer.C:
		close(cancel)
		writeWindowsProviderLifecycleDiagnostic(
			"start_provider",
			timeout,
			"provider startup deadline exceeded",
		)
		return nil, &core.RuntimeTimeoutError{
			Duration: timeout,
			Platform: "Windows",
			Phase:    "start_provider",
		}
	}
}

func windowsProviderRequestCanRetryAfterReset(
	request core.ProviderWireRequest,
	response *core.ProviderWireResponse,
) bool {
	if response == nil || windowsProviderResponseMayHaveMutated(response) {
		return false
	}
	switch request.Operation {
	case core.ProviderWireObserveApps, core.ProviderWireObserve, core.ProviderWireWindowList:
		return true
	case core.ProviderWireResolveAndAct:
		return request.Action != nil &&
			request.Action.Intent == core.AccessibilityResolveTarget
	default:
		return false
	}
}

func windowsProviderResponseMayHaveMutated(
	response *core.ProviderWireResponse,
) bool {
	if response == nil {
		return false
	}
	var mutation *core.ProviderMutationFacts
	switch {
	case response.Accessibility != nil:
		mutation = response.Accessibility.Mutation
	case response.Desktop != nil:
		mutation = response.Desktop.Mutation
	case response.Window != nil:
		mutation = response.Window.Mutation
	}
	return core.ProviderMutationCanInvalidate(mutation)
}

func (manager *windowsProviderProcessManager) recordStderrLocked(
	request core.ProviderWireRequest,
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
		"[computer-use windows-provider request=%s operation=%s "+
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

func (manager *windowsProviderProcessManager) stopLocked() {
	if manager.worker != nil {
		manager.worker.stop()
	}
	manager.worker = nil
	manager.environmentKey = ""
}

func shutdownWindowsProviderProcess() {
	windowsProviderProcesses.mu.Lock()
	defer windowsProviderProcesses.mu.Unlock()
	windowsProviderProcesses.stopLocked()
}

func startWindowsProviderWorker(
	path string,
	environment []string,
) (*windowsProviderWorker, error) {
	cmd := windowsProviderCommand(path)
	cmd.Env = environment
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start Windows provider: %w", err)
	}
	parentDeathBinding, err := windowsProviderParentDeathBinder(cmd)
	if err != nil {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf(
			"bind Windows provider lifetime to MCP process: %w",
			err,
		)
	}
	worker := &windowsProviderWorker{
		cmd:                cmd,
		stdin:              stdin,
		stdout:             bufio.NewReader(stdout),
		stderr:             &boundedProviderBuffer{limit: maxWindowsProviderStderrBytes},
		done:               make(chan struct{}),
		stderrDone:         make(chan struct{}),
		parentDeathBinding: parentDeathBinding,
	}
	go worker.forwardStderr(stderr)
	go worker.wait()
	return worker, nil
}

func (worker *windowsProviderWorker) forwardStderr(stderr io.Reader) {
	defer close(worker.stderrDone)
	reader := bufio.NewReaderSize(io.TeeReader(stderr, worker.stderr), 4*1024)
	for {
		line, originalBytes, truncated, err :=
			readProviderDiagnosticLine(
				reader,
				maxWindowsProviderStderrLineBytes,
			)
		if len(line) > 0 || originalBytes > 0 {
			writeWindowsProviderProcessDiagnostic(
				string(line),
				originalBytes,
				truncated,
			)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				writeWindowsProviderProcessDiagnostic(
					fmt.Sprintf("provider stderr stream failed: %v", err),
					0,
					false,
				)
			}
			return
		}
	}
}

func writeWindowsProviderProcessDiagnostic(
	diagnostic string,
	originalBytes int,
	truncated bool,
) {
	payload, err := json.Marshal(map[string]any{
		"event":                     "windows_provider_stderr",
		"stage":                     "process_stderr",
		"diagnostic":                diagnostic,
		"diagnostic_original_bytes": originalBytes,
		"diagnostic_truncated":      truncated,
	})
	if err != nil {
		return
	}
	fmt.Fprintln(windowsProviderDiagnosticWriter, string(payload))
}

func writeWindowsProviderLifecycleDiagnostic(
	stage string,
	timeout time.Duration,
	diagnostic string,
) {
	payload, err := json.Marshal(map[string]any{
		"event":           "windows_provider_lifecycle",
		"stage":           stage,
		"timeout_ms":      timeout.Milliseconds(),
		"diagnostic":      diagnostic,
		"bounded_failure": true,
	})
	if err != nil {
		return
	}
	fmt.Fprintln(windowsProviderDiagnosticWriter, string(payload))
}

func (worker *windowsProviderWorker) wait() {
	if worker.cmd != nil {
		worker.waitErr = worker.cmd.Wait()
	}
	close(worker.done)
	if !worker.closeParentDeathBindingWithin(windowsProviderShutdownGrace) {
		writeWindowsProviderLifecycleDiagnostic(
			"close_parent_binding",
			windowsProviderShutdownGrace,
			"provider exit could not close parent-death binding before deadline",
		)
	}
}

func (worker *windowsProviderWorker) closeParentDeathBinding() {
	worker.bindingCloseOnce.Do(func() {
		if worker.parentDeathBinding != nil {
			_ = worker.parentDeathBinding.Close()
		}
	})
}

func (worker *windowsProviderWorker) closeParentDeathBindingWithin(
	timeout time.Duration,
) bool {
	if worker == nil || worker.parentDeathBinding == nil {
		return true
	}
	done := make(chan struct{})
	go func() {
		worker.closeParentDeathBinding()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func (worker *windowsProviderWorker) closeInputWithin(
	timeout time.Duration,
) bool {
	if worker == nil || worker.stdin == nil {
		return true
	}
	done := make(chan struct{})
	go func() {
		_ = worker.stdin.Close()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func (worker *windowsProviderWorker) killProcessWithin(
	timeout time.Duration,
) bool {
	if worker == nil || worker.cmd == nil || worker.cmd.Process == nil {
		return true
	}
	done := make(chan struct{})
	go func() {
		_ = worker.cmd.Process.Kill()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func (worker *windowsProviderWorker) call(
	request core.ProviderWireRequest,
	timeout time.Duration,
) (*core.ProviderWireResponse, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	payload = append(payload, '\n')

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	writeChannel := make(chan windowsProviderWriteResult, 1)
	go func() {
		written, writeErr := worker.stdin.Write(payload)
		writeChannel <- windowsProviderWriteResult{
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
			failure := fmt.Errorf("write provider request: %w", writeResult.err)
			if writeResult.written > 0 {
				return nil, &core.RuntimeTransportError{
					Platform:    "Windows",
					Operation:   request.Operation,
					Phase:       "write_request",
					RequestSent: true,
					Cause:       failure,
				}
			}
			return nil, failure
		}
	case <-timer.C:
		return nil, &core.RuntimeTimeoutError{
			Duration: timeout,
			Platform: "Windows",
			Phase:    "write_request",
		}
	}

	readChannel := make(chan windowsProviderReadResult, 1)
	go func() {
		line, readErr := readWindowsProviderLine(
			worker.stdout,
			maxWindowsProviderResponseBytes,
		)
		readChannel <- windowsProviderReadResult{line: line, err: readErr}
	}()

	var result windowsProviderReadResult
	select {
	case result = <-readChannel:
	case <-timer.C:
		return nil, &core.RuntimeTimeoutError{
			Duration: timeout,
			Platform: "Windows",
			Phase:    "read_response",
		}
	}
	if errors.Is(result.err, errWindowsProviderResponseTooLarge) {
		return nil, &core.RuntimeTransportError{
			Platform:    "Windows",
			Operation:   request.Operation,
			Phase:       "read_response",
			RequestSent: true,
			Cause:       result.err,
		}
	}
	if result.err != nil {
		return nil, &core.RuntimeTransportError{
			Platform:    "Windows",
			Operation:   request.Operation,
			Phase:       "read_response",
			RequestSent: true,
			Cause:       result.err,
		}
	}

	response, err := core.DecodeProviderWireResponse(result.line)
	if err != nil {
		return nil, &core.RuntimeTransportError{
			Platform:    "Windows",
			Operation:   request.Operation,
			Phase:       "decode_response",
			RequestSent: true,
			Cause:       err,
		}
	}
	if response.RequestID != request.RequestID {
		return nil, &core.RuntimeTransportError{
			Platform:    "Windows",
			Operation:   request.Operation,
			Phase:       "request_id_mismatch",
			RequestSent: true,
			Cause: errors.New(
				"provider returned a mismatched V2 request_id",
			),
		}
	}
	if response.Operation != request.Operation {
		return nil, &core.RuntimeTransportError{
			Platform:    "Windows",
			Operation:   request.Operation,
			Phase:       "operation_mismatch",
			RequestSent: true,
			Cause: errors.New(
				"provider returned a mismatched V2 operation",
			),
		}
	}
	if response.RequestRejection != nil {
		return nil, &core.RuntimeRequestRejectionError{
			Operation: request.Operation,
			Rejection: *response.RequestRejection,
		}
	}
	return &response, nil
}

func readWindowsProviderLine(
	reader *bufio.Reader,
	limit int,
) ([]byte, error) {
	if limit <= 0 {
		return nil, errWindowsProviderResponseTooLarge
	}
	line := make([]byte, 0, min(limit, 64*1024))
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) > limit-len(line) {
			return nil, errWindowsProviderResponseTooLarge
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

func (worker *windowsProviderWorker) stop() {
	if worker == nil {
		return
	}
	worker.stopOnce.Do(func() {
		if !worker.closeInputWithin(windowsProviderShutdownGrace) {
			writeWindowsProviderLifecycleDiagnostic(
				"close_stdin",
				windowsProviderShutdownGrace,
				"provider stdin close deadline exceeded",
			)
		}
		timer := time.NewTimer(windowsProviderShutdownGrace)
		defer timer.Stop()
		stopped := false
		select {
		case <-worker.done:
			stopped = true
		case <-timer.C:
			if !worker.killProcessWithin(windowsProviderKillWait) {
				writeWindowsProviderLifecycleDiagnostic(
					"kill_process",
					windowsProviderKillWait,
					"provider process termination deadline exceeded",
				)
			}
			killTimer := time.NewTimer(windowsProviderKillWait)
			select {
			case <-worker.done:
				stopped = true
			case <-killTimer.C:
				if !worker.closeParentDeathBindingWithin(
					windowsProviderShutdownGrace,
				) {
					writeWindowsProviderLifecycleDiagnostic(
						"close_parent_binding",
						windowsProviderShutdownGrace,
						"provider parent-death binding close deadline exceeded",
					)
				}
			}
			if !killTimer.Stop() {
				select {
				case <-killTimer.C:
				default:
				}
			}
		}
		if stopped && worker.stderrDone != nil {
			select {
			case <-worker.stderrDone:
			case <-time.After(windowsProviderShutdownGrace):
			}
		}
	})
}
