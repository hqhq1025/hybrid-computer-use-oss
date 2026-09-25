//go:build windows_sidecar_test && !windows

package main

import (
	"io"
	"os/exec"
)

var windowsProviderParentDeathBinder = func(*exec.Cmd) (io.Closer, error) {
	return nopWriteCloser{}, nil
}
