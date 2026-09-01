//go:build windows

package main

import "os"

func fileOwnedByUID(_ os.FileInfo, _ int) bool {
	return false
}

func currentProcessUID() int {
	return -1
}
