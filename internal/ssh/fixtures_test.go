package ssh

import (
	"context"
	"net"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/dfns/terraform-provider-tunnel/internal/libs"
	"github.com/dfns/terraform-provider-tunnel/internal/ssh/sshtest"
	"golang.org/x/crypto/ssh"
)

// tunnel is a forwarder running against the in-process bastion, plus the handshake
// counter that proves how many SSH connections it needed.
type tunnel struct {
	addr       string
	handshakes *atomic.Int32
	clients    *clientPool
}

// startTunnel forwards a local port to target through a fresh in-process bastion,
// serving until the test ends.
func startTunnel(t *testing.T, network, target string) *tunnel {
	t.Helper()

	keyPEM, authorizedKey := sshtest.GenerateClientKey(t)
	sshAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(sshtest.StartServer(t, authorizedKey)))
	clientCfg, err := clientConfig(TunnelConfig{SSHUser: sshtest.User, SSHKey: keyPEM})
	if err != nil {
		t.Fatal(err)
	}

	var handshakes atomic.Int32
	clients := &clientPool{dial: func(ctx context.Context) (*ssh.Client, error) {
		handshakes.Add(1)
		return dialSSH(ctx, sshAddr, clientCfg)
	}}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	fwd := newForwarder(listener, clients, network, target)
	served := make(chan error, 1)
	go func() { served <- fwd.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		fwd.Close()
		clients.close()
		if err := <-served; err != nil {
			t.Errorf("serve() = %v", err)
		}
	})

	return &tunnel{addr: listener.Addr().String(), handshakes: &handshakes, clients: clients}
}

func (tun *tunnel) dial(t *testing.T) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", tun.addr)
	if err != nil {
		t.Fatalf("dialing tunnel: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func closeWrite(t *testing.T, conn net.Conn) {
	t.Helper()
	if !libs.HalfClose(conn) {
		t.Fatalf("%T cannot half-close", conn)
	}
}

// startTCPTarget serves handle on a random localhost port and returns its
// address.
func startTCPTarget(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go acceptLoop(listener, handle)
	return listener.Addr().String()
}

func acceptLoop(listener net.Listener, handle func(net.Conn)) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return // listener closed with the test
		}
		go func() {
			defer conn.Close()
			handle(conn)
		}()
	}
}
