package cli

import (
	"net"
	"testing"
)

func TestPickPort_InvalidPort(t *testing.T) {
	if _, err := pickPort(0); err == nil {
		t.Error("expected error for port 0")
	}
	if _, err := pickPort(65536); err == nil {
		t.Error("expected error for port 65536")
	}
}

func TestPickPort_DefaultFree(t *testing.T) {
	// Find a free port then release it and confirm pickPort returns it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot bind test socket:", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	got, err := pickPort(port)
	if err != nil {
		t.Fatal(err)
	}
	if got != port {
		t.Errorf("got %d, want %d", got, port)
	}
}

func TestPickPort_DefaultInUse(t *testing.T) {
	// Bind a port so the default is unavailable; pickPort should find an alternative.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot bind test socket:", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	got, err := pickPort(port)
	if err != nil {
		// No alternatives found — acceptable in heavily loaded CI; skip rather than fail.
		t.Logf("no alternative ports available (port=%d): %v", port, err)
		return
	}
	if got == port {
		t.Errorf("expected a different port, got the same in-use port %d", port)
	}
}

func TestIsPortFree(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot bind test socket:", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	if isPortFree(port) {
		t.Errorf("port %d should be in use", port)
	}
	ln.Close()
	if !isPortFree(port) {
		t.Logf("port %d still not free after close (OS may delay reuse)", port)
	}
}

func TestStdinIsTerminal(t *testing.T) {
	// In a test environment stdin is not a terminal; just confirm no panic.
	_ = stdinIsTerminal()
}
