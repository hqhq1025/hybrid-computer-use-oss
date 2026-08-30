//go:build linux || darwin

package main

import (
	"os"
	"syscall"
)

func fileOwnedByUID(info os.FileInfo, uid int) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == uid
}
