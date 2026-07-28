package azurebastion

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/dfns/terraform-provider-tunnel/internal/libs"
)

func TestRelayBinaryData(t *testing.T) {
	clientSide, tunnelSide := net.Pipe()
	defer clientSide.Close()
	done := make(chan struct{})
	go func() { defer close(done); relay(tunnelSide, dialEchoWebSocket(t)) }()

	assertEcho(t, clientSide, []byte{0, 1, 2, 0xff, 'a'})

	_ = clientSide.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not stop after local close")
	}
}

// TestRelaySendsOneFramePerChunk guards the framing invariant frameSize
// documents. A chunk bigger than the WebSocket write buffer leaves as an initial
// frame plus continuation frames, and the Bastion data plane closes the
// connection rather than reassembling them — which only shows up under load, when
// reads actually fill the buffer.
func TestRelaySendsOneFramePerChunk(t *testing.T) {
	client, tunnelSide := tcpPair(t)
	remote := newRecordingWebSocket()
	done := make(chan struct{})
	go func() { defer close(done); relay(tunnelSide, remote) }()

	payload := bytes.Repeat([]byte("x"), 512*1024)
	go func() {
		if _, err := client.Write(payload); err != nil {
			t.Errorf("writing payload: %v", err)
		}
		if !libs.HalfClose(client) {
			t.Error("loopback connection cannot half-close")
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not finish sending the payload")
	}

	largest, total := remote.written()
	if total != len(payload) {
		t.Errorf("relayed %d bytes, want %d", total, len(payload))
	}
	if largest > frameSize {
		t.Errorf("largest message = %d bytes, want at most %d: gorilla fragments anything larger", largest, frameSize)
	}
}

// TestRelayHalfClosesLocalWhenRemoteEnds pins the graceful end of a connection
// the far end finished with: the client must receive every byte followed by a
// clean EOF, and the relay must keep reading from it until it closes its own side
// rather than resetting it mid-request.
func TestRelayHalfClosesLocalWhenRemoteEnds(t *testing.T) {
	client, tunnelSide := tcpPair(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		relay(tunnelSide, &scriptedWebSocket{frames: [][]byte{[]byte("payload")}})
	}()

	received, err := io.ReadAll(client)
	if err != nil {
		t.Fatalf("reading until EOF: %v", err)
	}
	if string(received) != "payload" {
		t.Fatalf("received %q, want %q", received, "payload")
	}

	select {
	case <-done:
		t.Fatal("relay tore the connection down before the client was done with its side")
	case <-time.After(100 * time.Millisecond):
	}

	_ = client.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not stop after the client closed")
	}
}

// TestServerKeepsOneSessionUntilShutdown pins the lifetime sessionClient.close
// describes: connections come and go on one session, which is deleted once, when the
// tunnel stops.
func TestServerKeepsOneSessionUntilShutdown(t *testing.T) {
	bastion := newFakeBastion(t, bastionOptions{echo: true})
	tun := startTunnel(t, newTestSession(t, bastion, validPlan(t)))

	first, second := tun.dial(t), tun.dial(t)
	assertEcho(t, first, []byte{0, 0, 0xff})
	assertEcho(t, second, []byte{1, 0, 0xff})
	_ = first.Close()
	_ = second.Close()

	// Long enough for both handlers to finish, so the tunnel really does go idle.
	time.Sleep(50 * time.Millisecond)
	if got := len(bastion.deleteRequests()); got != 0 {
		t.Fatalf("session deleted while the tunnel was still running (deletes = %d)", got)
	}
	assertEcho(t, tun.dial(t), []byte{2, 0, 0xff})

	tun.stop()

	tokens := bastion.tokenRequests()
	if len(tokens) != 3 {
		t.Fatalf("token requests = %d, want 3", len(tokens))
	}
	if tokens[1].form.Get("token") != "auth-1" || tokens[1].nodeID != testNodeID {
		t.Errorf("concurrent client did not continue the existing session: %+v", tokens[1])
	}
	if tokens[2].form.Get("token") != "auth-2" || tokens[2].nodeID != testNodeID {
		t.Errorf("client after an idle gap did not continue the existing session: %+v", tokens[2])
	}
	deletes := bastion.deleteRequests()
	if len(deletes) != 1 || deletes[0].path != "/api/tokens/auth-3" {
		t.Fatalf("cleanup on shutdown = %+v, want a single delete of auth-3", deletes)
	}
}

func TestServerKeepsListenerAfterLazyTokenFailure(t *testing.T) {
	bastion := newFakeBastion(t, bastionOptions{failStatus: http.StatusUnauthorized})
	tun := startTunnel(t, newTestSession(t, bastion, validPlan(t)))

	for attempt := 1; attempt <= 2; attempt++ {
		conn, err := net.Dial("tcp", tun.addr)
		if err != nil {
			t.Fatalf("listener stopped accepting after lazy failure: %v", err)
		}
		_ = conn.Close()
		waitFor(t, "token request", func() bool {
			return len(bastion.tokenRequests()) >= attempt
		})
	}
	if got := len(bastion.tokenRequests()); got != 2 {
		t.Fatalf("token requests = %d, want 2", got)
	}
	if got := len(bastion.deleteRequests()); got != 0 {
		t.Fatalf("cleanup attempted without a session (deletes = %d)", got)
	}
}
