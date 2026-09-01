//go:build darwin

package main

import (
	"os"
	"syscall"
)

func portableProcessState(pid int) string {
	if pid <= 0 {
		return "missing"
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return "missing"
	}
	if err := process.Signal(syscall.Signal(0)); err != nil {
		return "missing"
	}
	return "running"
}

func defaultRuntimeBackend() PlatformBackend {
	return portableRuntimeBackend{platform: "darwin"}
}
