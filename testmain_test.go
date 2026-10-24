package main

import (
	"os"
	"testing"
)

// TestMain makes every test network-local: engines started by tests bind to
// 127.0.0.1 only (no 0.0.0.0/:: listeners, no DHT, no UPnP/NAT-PMP), so running
// `go test` on Windows does not pop Windows Firewall "allow access" dialogs.
func TestMain(m *testing.M) {
	btLoopbackOnly = true
	os.Exit(m.Run())
}
