//go:build darwin || windows

package main

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
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxPortableProviderResponseBytes = 64 << 20

var (
	portableProviderSequence  atomic.Uint64
	portableProviderProcesses portableProviderProcessManager
)

type portableProviderProcessManager struct {
	mu             sync.Mutex
	worker         *portableProviderWorker
	environmentKey string
}

type portableProviderWorker struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   *bufio.Reader
	tempDir  string
	done     chan struct{}
	stopOnce sync.Once
}

func nextPortableProviderRequestID() string {
	return fmt.Sprintf("%s-%d-%d", runtime.GOOS, time.Now().UnixNano(), portableProviderSequence.Add(1))
}

func portableProviderTimeout() time.Duration {
	timeout := 30 * time.Second
	if raw := os.Getenv("COMPUTER_USE_RUNTIME_TIMEOUT_SECONDS"); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			timeout = time.Duration(seconds) * time.Second
		}
	}
	return timeout
}

func portablePythonCommand() (string, []string) {
	if configured := strings.TrimSpace(os.Getenv("COMPUTER_USE_PYTHON_BINARY")); configured != "" {
		return configured, nil
	}
	if runtime.GOOS == "windows" {
		if path, err := exec.LookPath("py"); err == nil {
			return path, []string{"-3"}
		}
	}
	for _, candidate := range []string{"python3", "python"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	return "python3", nil
}

func runPortableProvider(request ProviderWireRequest, host HostBackend) (*ProviderWireResponse, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	return portableProviderProcesses.run(request, host)
}

func (manager *portableProviderProcessManager) run(
	request ProviderWireRequest,
	host HostBackend,
) (*ProviderWireResponse, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()

	bin, prefix := portablePythonCommand()
	environment := host.RuntimeEnvironment(os.Environ())
	key := bin + "\x00" + strings.Join(prefix, "\x00") + "\x00" + strings.Join(environment, "\x00")
	if manager.worker == nil || manager.environmentKey != key {
		manager.stopLocked()
		worker, err := startPortableProviderWorker(bin, prefix, environment)
		if err != nil {
			return nil, err
		}
		manager.worker = worker
		manager.environmentKey = key
	}

	response, err := manager.worker.call(request, portableProviderTimeout())
	if err != nil {
		manager.stopLocked()
		return nil, err
	}
	if response.ProviderResetRequired {
		manager.stopLocked()
	}
	return response, nil
}

func startPortableProviderWorker(
	bin string,
	prefix []string,
	environment []string,
) (*portableProviderWorker, error) {
	tempDir, err := os.MkdirTemp("", "computer-use-native-*")
	if err != nil {
		return nil, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(tempDir)
		}
	}()
	for relative, contents := range portableRuntimeBundle() {
		path := filepath.Join(tempDir, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			return nil, err
		}
	}
	entrypoint := filepath.Join(tempDir, "runtime_portable.py")
	args := append(append([]string{}, prefix...), entrypoint, "--stdio")
	cmd := exec.Command(bin, args...)
	cmd.Env = environment
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	worker := &portableProviderWorker{
		cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout), tempDir: tempDir, done: make(chan struct{}),
	}
	cleanup = false
	go func() {
		_ = cmd.Wait()
		close(worker.done)
	}()
	return worker, nil
}

func (worker *portableProviderWorker) call(
	request ProviderWireRequest,
	timeout time.Duration,
) (*ProviderWireResponse, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	payload = append(payload, '\n')
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	writeDone := make(chan error, 1)
	go func() {
		written, writeErr := worker.stdin.Write(payload)
		if writeErr == nil && written != len(payload) {
			writeErr = io.ErrShortWrite
		}
		writeDone <- writeErr
	}()
	select {
	case err := <-writeDone:
		if err != nil {
			return nil, &runtimeTransportError{operation: request.Operation, phase: "write_request", platform: runtime.GOOS}
		}
	case <-deadline.C:
		return nil, &runtimeTimeoutError{duration: timeout, phase: "write_request", platform: runtime.GOOS}
	}

	readDone := make(chan struct {
		line []byte
		err  error
	}, 1)
	go func() {
		line, readErr := readPortableProviderLine(worker.stdout, maxPortableProviderResponseBytes)
		readDone <- struct {
			line []byte
			err  error
		}{line, readErr}
	}()
	var result struct {
		line []byte
		err  error
	}
	select {
	case result = <-readDone:
	case <-deadline.C:
		return nil, &runtimeTimeoutError{duration: timeout, phase: "read_response", platform: runtime.GOOS}
	}
	if result.err != nil {
		return nil, &runtimeTransportError{operation: request.Operation, phase: "read_response", requestSent: true, platform: runtime.GOOS}
	}
	response, err := DecodeProviderWireResponse(bytes.TrimSpace(result.line))
	if err != nil || response.RequestID != request.RequestID || response.Operation != request.Operation {
		return nil, &runtimeTransportError{operation: request.Operation, phase: "decode_response", requestSent: true, platform: runtime.GOOS}
	}
	return &response, nil
}

func readPortableProviderLine(reader *bufio.Reader, limit int) ([]byte, error) {
	line := make([]byte, 0, min(limit, 64*1024))
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) > limit-len(line) {
			return nil, errors.New("native provider response exceeded configured limit")
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

func (worker *portableProviderWorker) stop() {
	if worker == nil {
		return
	}
	worker.stopOnce.Do(func() {
		_ = worker.stdin.Close()
		select {
		case <-worker.done:
		case <-time.After(250 * time.Millisecond):
			if worker.cmd != nil && worker.cmd.Process != nil {
				_ = worker.cmd.Process.Kill()
			}
			<-worker.done
		}
		_ = os.RemoveAll(worker.tempDir)
	})
}

func (manager *portableProviderProcessManager) stopLocked() {
	if manager.worker != nil {
		manager.worker.stop()
	}
	manager.worker = nil
	manager.environmentKey = ""
}

func shutdownPortableProviderProcess() {
	portableProviderProcesses.mu.Lock()
	defer portableProviderProcesses.mu.Unlock()
	portableProviderProcesses.stopLocked()
}
