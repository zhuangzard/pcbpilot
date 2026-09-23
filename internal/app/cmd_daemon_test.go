package app

import (
	"io"
	"net"
	"testing"
)

// freeTCPPort returns a port that was free at call time (racy, fine for tests).
func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func TestPortFree(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	if portFree("127.0.0.1", port) {
		t.Errorf("port %d is bound but portFree reported free", port)
	}
	if !portFree("127.0.0.1", freeTCPPort(t)) {
		t.Error("a free port must report free")
	}
}

func TestEnsurePortAvailable_FreePort(t *testing.T) {
	if err := ensurePortAvailable("127.0.0.1", freeTCPPort(t), io.Discard); err != nil {
		t.Errorf("a free port should be available; got %v", err)
	}
}

// A FOREIGN process on the port (not an pcbpilot daemon) must be REFUSED headless
// (go test has no TTY), and must NOT be killed.
func TestEnsurePortAvailable_ForeignHeadlessRefuses(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	if err := ensurePortAvailable("127.0.0.1", port, io.Discard); err == nil {
		t.Fatal("expected refusal for a foreign process holding the port (headless)")
	}
	// The foreign listener must still be alive — we never kill non-easyeda procs.
	if portFree("127.0.0.1", port) {
		t.Error("foreign listener must NOT have been killed/freed")
	}
}
