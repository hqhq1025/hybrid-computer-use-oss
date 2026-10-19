//go:build !linux

package main

import "net"

// peerIsSameUser relies on the socket's 0600 mode and 0700 directory on
// platforms without SO_PEERCRED.
func peerIsSameUser(net.Conn) bool { return true }
