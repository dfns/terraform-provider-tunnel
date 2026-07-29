package ssh

import (
	"bytes"
	"context"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dfns/terraform-provider-tunnel/internal/libs"
	"github.com/dfns/terraform-provider-tunnel/internal/ssh/sshtest"
	"golang.org/x/crypto/ssh"
)

// TestForwardPassesHalfCloseThrough is the regression test for the bug that
// replaced rgzr/sshtun: a client that finished sending (CloseWrite) must still
// receive the reply the target writes afterwards. Tearing the connection down on
// the first EOF instead — what sshtun did — loses that reply, which is how a
// keep-alive connection closed by a Kubernetes API server killed an in-flight
// POST with "unexpected EOF".
func TestForwardPassesHalfCloseThrough(t *testing.T) {
	target := startTCPTarget(t, func(conn net.Conn) {
		request, err := io.ReadAll(conn)
		if err != nil {
			t.Errorf("target reading request: %v", err)
			return
		}
		if _, err := conn.Write(append([]byte("reply to "), request...)); err != nil {
			t.Errorf("target writing reply: %v", err)
		}
	})
	tun := startTunnel(t, "tcp", target)

	conn := tun.dial(t)
	if _, err := conn.Write([]byte("request")); err != nil {
		t.Fatalf("writing request: %v", err)
	}
	closeWrite(t, conn)

	reply, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("reading reply after half-close: %v", err)
	}
	if string(reply) != "reply to request" {
		t.Fatalf("reply = %q, want %q", reply, "reply to request")
	}
}

// TestForwardDeliversWholeResponseWhenTargetCloses covers the mirror image: the
// target closing must reach the client as a clean EOF after every byte it sent,
// not as a truncated stream.
func TestForwardDeliversWholeResponseWhenTargetCloses(t *testing.T) {
	payload := bytes.Repeat([]byte("0123456789abcdef"), 64*1024) // 1 MiB
	target := startTCPTarget(t, func(conn net.Conn) {
		if _, err := conn.Write(payload); err != nil {
			t.Errorf("target writing payload: %v", err)
		}
	})
	tun := startTunnel(t, "tcp", target)

	got, err := io.ReadAll(tun.dial(t))
	if err != nil {
		t.Fatalf("reading payload: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("read %d bytes, want %d", len(got), len(payload))
	}
}

// TestForwardReusesOneSSHConnection pins the second half of the fix: every
// forwarded connection is a channel on one SSH connection. sshtun re-dialed
// whenever the connection count returned to zero, and those repeated handshakes
// are what tripped the bastion's MaxStartups throttle.
func TestForwardReusesOneSSHConnection(t *testing.T) {
	tun := startTunnel(t, "tcp", startTCPTarget(t, echoUntilEOF))

	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := net.Dial("tcp", tun.addr)
			if err != nil {
				t.Errorf("dialing tunnel: %v", err)
				return
			}
			defer conn.Close()
			assertEcho(t, conn, []byte(strings.Repeat("x", i+1)))
		}()
	}
	wg.Wait()

	// Sequential connections too: a pool that closed on idle would re-dial here.
	for range 3 {
		conn := tun.dial(t)
		assertEcho(t, conn, []byte("sequential"))
		_ = conn.Close()
	}

	if got := tun.handshakes.Load(); got != 1 {
		t.Fatalf("SSH handshakes = %d, want 1", got)
	}
}

// TestForwardRedialsAfterConnectionLoss covers a bastion that drops the SSH
// connection mid-run: the listener must recover instead of failing every later
// connection on a dead handle.
func TestForwardRedialsAfterConnectionLoss(t *testing.T) {
	tun := startTunnel(t, "tcp", startTCPTarget(t, echoUntilEOF))

	first := tun.dial(t)
	assertEcho(t, first, []byte("before"))
	_ = first.Close()

	client, err := tun.clients.get(context.Background())
	if err != nil {
		t.Fatalf("get client: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("closing SSH connection: %v", err)
	}

	assertEcho(t, tun.dial(t), []byte("after"))
	if got := tun.handshakes.Load(); got != 2 {
		t.Fatalf("SSH handshakes = %d, want 2", got)
	}
}

// TestForwardKeepsSSHConnectionWhenTargetRefuses guards the retry in dialTarget:
// a refused target is the bastion answering, so the SSH connection stays and no
// second handshake happens.
func TestForwardKeepsSSHConnectionWhenTargetRefuses(t *testing.T) {
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refused := closed.Addr().String()
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	tun := startTunnel(t, "tcp", refused)

	conn := tun.dial(t)
	if _, err := io.ReadAll(conn); err != nil {
		t.Fatalf("reading from refused forward: %v", err)
	}
	if got := tun.handshakes.Load(); got != 1 {
		t.Fatalf("SSH handshakes = %d, want 1", got)
	}
}

// TestForwardCancellationInterruptsTargetDial guards shutdown while a bastion
// has received a channel-open request but has not accepted or rejected it.
func TestForwardCancellationInterruptsTargetDial(t *testing.T) {
	keyPEM, authorizedKey := sshtest.GenerateClientKey(t)
	channelOpened := make(chan struct{}, 1)
	sshPort := sshtest.StartServerWithChannelHandler(t, authorizedKey, func(ssh.NewChannel) {
		channelOpened <- struct{}{}
		// Deliberately leave the channel-open request unanswered.
	})
	clientCfg, err := clientConfig(TunnelConfig{SSHUser: sshtest.User, SSHKey: keyPEM})
	if err != nil {
		t.Fatal(err)
	}
	sshAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(sshPort))
	clients := &clientPool{dial: func(ctx context.Context) (*ssh.Client, error) {
		return dialSSH(ctx, sshAddr, clientCfg)
	}}
	t.Cleanup(clients.close)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	fwd := newForwarder(listener, clients, "tcp", "target.internal:443")
	served := make(chan error, 1)
	go func() { served <- fwd.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		fwd.Close()
	})

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	select {
	case <-channelOpened:
	case <-time.After(time.Second):
		t.Fatal("bastion did not receive the target channel-open request")
	}

	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve() = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve() did not return after cancellation")
	}
}

// TestForwardToUnixSocket exercises the target_socket path, which forwards over a
// direct-streamlocal channel rather than direct-tcpip.
func TestForwardToUnixSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "target.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go acceptLoop(listener, echoUntilEOF)

	tun := startTunnel(t, "unix", socket)
	assertEcho(t, tun.dial(t), []byte("over a socket"))
}

// TestRemoteConnCanHalfClose pins the assumption relay is built on: the
// connection x/crypto/ssh hands back for a forwarded channel can half-close. The
// concrete type is unexported, so this cannot be a compile-time assertion.
func TestRemoteConnCanHalfClose(t *testing.T) {
	target := startTCPTarget(t, echoUntilEOF)
	tun := startTunnel(t, "tcp", target)
	client, err := tun.clients.get(context.Background())
	if err != nil {
		t.Fatalf("get client: %v", err)
	}

	remote, err := client.Dial("tcp", target)
	if err != nil {
		t.Fatalf("dialing through SSH: %v", err)
	}
	defer remote.Close()
	if !libs.HalfClose(remote) {
		t.Fatalf("%T cannot half-close; relay would fall back to a full close", remote)
	}
}

func TestTargetEndpoint(t *testing.T) {
	tests := []struct {
		name        string
		cfg         TunnelConfig
		wantNetwork string
		wantAddress string
	}{
		{
			name:        "tcp target",
			cfg:         TunnelConfig{TargetHost: "db.internal", TargetPort: 5432},
			wantNetwork: "tcp",
			wantAddress: "db.internal:5432",
		},
		{
			name:        "ipv6 target is bracketed",
			cfg:         TunnelConfig{TargetHost: "::1", TargetPort: 443},
			wantNetwork: "tcp",
			wantAddress: "[::1]:443",
		},
		{
			name:        "socket wins over host and port",
			cfg:         TunnelConfig{TargetHost: "db.internal", TargetPort: 5432, TargetSocket: "/run/app.sock"},
			wantNetwork: "unix",
			wantAddress: "/run/app.sock",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			network, address := targetEndpoint(tt.cfg)
			if network != tt.wantNetwork || address != tt.wantAddress {
				t.Fatalf("targetEndpoint() = %q, %q; want %q, %q", network, address, tt.wantNetwork, tt.wantAddress)
			}
		})
	}
}

// TestRunTunnelReportsBadCredentials checks the readiness contract: a tunnel that
// cannot authenticate must fail instead of binding a listener that fails later.
func TestRunTunnelReportsBadCredentials(t *testing.T) {
	// An empty HOME with no agent keeps the default-credential fallback from
	// finding the developer's own keys.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")

	localPort, err := libs.GetFreePort()
	if err != nil {
		t.Fatal(err)
	}
	err = runTunnel(context.Background(), TunnelConfig{
		SSHHost:   "127.0.0.1",
		SSHPort:   1,
		LocalHost: "127.0.0.1",
		LocalPort: localPort,
	})
	if err == nil {
		t.Fatal("runTunnel() = nil, want an error")
	}
	if !strings.Contains(err.Error(), "no SSH credentials") {
		t.Fatalf("error = %v, want it to name the missing credentials", err)
	}
}

// TestRunTunnelReportsUnreachableTarget preserves the startup contract: ready
// means both the SSH connection and the configured target have been reached.
func TestRunTunnelReportsUnreachableTarget(t *testing.T) {
	keyPEM, authorizedKey := sshtest.GenerateClientKey(t)
	localPort, err := libs.GetFreePort()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	missingSocket := filepath.Join(t.TempDir(), "missing.sock")
	err = runTunnel(ctx, TunnelConfig{
		LocalHost:    "127.0.0.1",
		LocalPort:    localPort,
		SSHHost:      "127.0.0.1",
		SSHKey:       keyPEM,
		SSHPort:      sshtest.StartServer(t, authorizedKey),
		SSHUser:      sshtest.User,
		TargetSocket: missingSocket,
	})
	if err == nil {
		t.Fatal("runTunnel() = nil, want an unreachable-target error")
	}
	if !strings.Contains(err.Error(), "connect to SSH target "+missingSocket) {
		t.Fatalf("error = %v, want it to identify the unreachable target", err)
	}
}

func echoUntilEOF(conn net.Conn) {
	_, _ = io.Copy(conn, conn)
}

func assertEcho(t *testing.T, conn net.Conn, payload []byte) {
	t.Helper()
	if _, err := conn.Write(payload); err != nil {
		t.Errorf("writing %d bytes: %v", len(payload), err)
		return
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Errorf("reading echo: %v", err)
		return
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("echo = %q, want %q", got, payload)
	}
}
