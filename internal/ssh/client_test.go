package ssh

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// startStalledSSHPeer leaves NewClientConn blocked in version exchange.
func startStalledSSHPeer(t *testing.T) (string, <-chan struct{}) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	accepted := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		close(accepted)
		defer conn.Close()
		_, _ = io.Copy(io.Discard, conn)
	}()
	return listener.Addr().String(), accepted
}

func TestDialSSHBoundsHandshake(t *testing.T) {
	addr, accepted := startStalledSSHPeer(t)
	cfg := &ssh.ClientConfig{
		User:            "test",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         200 * time.Millisecond,
	}

	_, err := dialSSH(context.Background(), addr, cfg)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dialSSH() error = %v, want context deadline exceeded", err)
	}
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("SSH peer never accepted the TCP connection")
	}
}

func TestDialSSHCancelsHandshake(t *testing.T) {
	addr, accepted := startStalledSSHPeer(t)
	cfg := &ssh.ClientConfig{
		User:            "test",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := dialSSH(ctx, addr, cfg)
		result <- err
	}()

	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("SSH peer never accepted the TCP connection")
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("dialSSH() error = %v, want context canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dialSSH did not stop after context cancellation")
	}
}
