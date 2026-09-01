//go:build windows

package main

import "os"

func portableProcessState(pid int) string {
	if pid <= 0 {
		return "missing"
	}
	if _, err := os.FindProcess(pid); err != nil {
		return "missing"
	}
	// Go's Windows Process handle does not expose a non-destructive signal-0
	// probe. Native provider observations remain the authoritative liveness fact.
	return "unknown"
}

func defaultRuntimeBackend() PlatformBackend {
	return portableRuntimeBackend{platform: "windows"}
}
