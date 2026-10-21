//go:build darwin || darwin_sidecar_test

package main

import (
	"bufio"
	"errors"
	"net"
	"testing"
	"time"
)

func TestDarwinClosedConnectionReportsUnsentRequest(t *testing.T) {
	connection, peer := net.Pipe()
	defer connection.Close()
	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}
	worker := &darwinProviderWorker{connection: connection, stdout: bufio.NewReader(connection)}
	_, err := worker.call(validDarwinObserveAppsRequest("closed-before-write"), time.Second)
	var transport *runtimeTransportError
	if !errors.As(err, &transport) || transport.requestSent || transport.phase != "write_request" {
		t.Fatalf("closed connection must report zero bytes sent: %#v", err)
	}
}
