package panelupgrade

import (
	"net"
	"os"
	"testing"
)

func TestDifferentLoopbackAddressIsNotProbeEndpoint(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skip(err)
	}
	defer listener.Close()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	if ownsPort(os.Getpid(), port) {
		t.Fatal("127.0.0.2 listener accepted for a 127.0.0.1 readiness probe")
	}
}

func TestReadinessRequiresListenerOwnedByExpectedProcess(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	if !ownsPort(os.Getpid(), port) {
		t.Fatal("own real listening socket not found")
	}
	if ownsPort(1, port) {
		t.Fatal("unrelated PID accepted")
	}
}
