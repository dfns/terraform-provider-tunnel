package libs

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestSignalReadyWhenServingSignalsOnceListenerAccepts(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	readyPath := filepath.Join(t.TempDir(), "ready")
	t.Setenv(TunnelReadyEnv, readyPath)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := SignalReadyWhenServing(ctx, host, port); err != nil {
		t.Fatalf("SignalReadyWhenServing() error = %v", err)
	}

	if data, err := os.ReadFile(readyPath); err != nil || string(data) != "ready" {
		t.Fatalf("ready file = %q (err %v), want \"ready\"", data, err)
	}
}

func TestSignalReadyWhenServingStaysSilentWhilePortIsClosed(t *testing.T) {
	port, err := GetFreePort()
	if err != nil {
		t.Fatal(err)
	}
	readyPath := filepath.Join(t.TempDir(), "ready")
	t.Setenv(TunnelReadyEnv, readyPath)

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	err = SignalReadyWhenServing(ctx, "127.0.0.1", strconv.Itoa(port))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SignalReadyWhenServing() error = %v, want deadline exceeded", err)
	}

	// A tunnel that never bound must not look ready to the parent.
	if _, err := os.Stat(readyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ready file stat error = %v, want not-exist", err)
	}
}
