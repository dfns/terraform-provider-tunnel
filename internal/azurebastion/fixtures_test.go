package azurebastion

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v10"
	"github.com/gorilla/websocket"
)

const (
	testAccessToken = "access-secret"
	testNodeID      = "node-1"
	testTargetIP    = "10.0.1.4"
)

func validConfig() TunnelConfig {
	return TunnelConfig{
		BastionHostID:    "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/bastionHosts/main",
		TargetResourceID: "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/vm",
		TargetPort:       5432,
		LocalHost:        "localhost",
		LocalPort:        15432,
	}
}

// ipConnectConfig targets a private IP, which the data plane only allows on
// ports 22 and 3389.
func ipConnectConfig() TunnelConfig {
	cfg := validConfig()
	cfg.TargetResourceID = ""
	cfg.TargetIPAddress = testTargetIP
	cfg.TargetPort = 22
	return cfg
}

func validPlan(t *testing.T) tunnelPlan {
	t.Helper()
	return mustResolve(t, validConfig())
}

func ipConnectPlan(t *testing.T) tunnelPlan {
	t.Helper()
	return mustResolve(t, ipConnectConfig())
}

func mustResolve(t *testing.T, cfg TunnelConfig) tunnelPlan {
	t.Helper()
	plan, err := cfg.resolve()
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

type staticCredential struct {
	token string
	err   error
}

func (c staticCredential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: c.token, ExpiresOn: time.Now().Add(time.Hour)}, c.err
}

type captureDialer struct {
	mu   sync.Mutex
	urls []string
	conn webSocketConn
}

func (d *captureDialer) DialContext(_ context.Context, target string, _ http.Header) (webSocketConn, *http.Response, error) {
	d.mu.Lock()
	d.urls = append(d.urls, target)
	d.mu.Unlock()
	return d.conn, nil, nil
}

func (d *captureDialer) dialed() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.urls)
}

// scriptedWebSocket replays frames and then ends, standing in for a far end that
// finishes while the local client is still connected. Its zero value ends
// immediately, which is all a test that only cares about the token exchange needs.
type scriptedWebSocket struct {
	frames [][]byte
}

func (s *scriptedWebSocket) ReadMessage() (int, []byte, error) {
	if len(s.frames) == 0 {
		return 0, nil, io.EOF
	}
	frame := s.frames[0]
	s.frames = s.frames[1:]
	return websocket.BinaryMessage, frame, nil
}

func (s *scriptedWebSocket) WriteMessage(int, []byte) error { return nil }
func (s *scriptedWebSocket) Close() error                   { return nil }

// recordingWebSocket accepts everything written to it, keeping the size of each
// message, and stays readable until it is closed.
type recordingWebSocket struct {
	closed chan struct{}
	once   sync.Once

	mu      sync.Mutex
	largest int
	total   int
}

func newRecordingWebSocket() *recordingWebSocket {
	return &recordingWebSocket{closed: make(chan struct{})}
}

func (w *recordingWebSocket) ReadMessage() (int, []byte, error) {
	<-w.closed
	return 0, nil, io.EOF
}

func (w *recordingWebSocket) WriteMessage(_ int, data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.total += len(data)
	w.largest = max(w.largest, len(data))
	return nil
}

func (w *recordingWebSocket) Close() error {
	w.once.Do(func() { close(w.closed) })
	return nil
}

func (w *recordingWebSocket) written() (largest, total int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.largest, w.total
}

// tcpPair returns the two ends of a loopback connection: unlike net.Pipe, these
// are real sockets, so they can half-close.
func tcpPair(t *testing.T) (client, server net.Conn) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- conn
	}()
	client, err = net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server, ok := <-accepted
	if !ok {
		t.Fatal("accepting loopback connection")
	}
	t.Cleanup(func() { _ = server.Close() })
	return client, server
}

type fakeBastionGetter struct {
	response armnetwork.BastionHostsClientGetResponse
	err      error
	group    string
	name     string
}

func (f *fakeBastionGetter) Get(
	_ context.Context,
	group string,
	name string,
	_ *armnetwork.BastionHostsClientGetOptions,
) (armnetwork.BastionHostsClientGetResponse, error) {
	f.group = group
	f.name = name
	return f.response, f.err
}

type tokenRequest struct {
	form url.Values
	// nodeIDPresent distinguishes an absent X-Node-Id header from an empty one,
	// which is what separates a fresh session from a continued one.
	nodeID        string
	nodeIDPresent bool
}

type deleteRequest struct {
	path   string
	nodeID string
}

type bastionOptions struct {
	// failStatus, when non-zero, answers every token request with it.
	failStatus int
	// echo upgrades /webtunnelv2/ and echoes frames, so a test can drive real
	// traffic through the tunnel.
	echo bool
	// tokenSuffix is appended to every issued token, so a test can prove escaping
	// with characters that change meaning when they reach a URL raw.
	tokenSuffix string
}

// fakeBastion records every token and cleanup request so tests assert on the
// exchange rather than embedding assertions in the handler.
type fakeBastion struct {
	*httptest.Server

	mu      sync.Mutex
	tokens  []tokenRequest
	deletes []deleteRequest
}

func newFakeBastion(t *testing.T, opts bastionOptions) *fakeBastion {
	t.Helper()
	fake := &fakeBastion{}
	upgrader := websocket.Upgrader{}
	fake.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/tokens":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
				return
			}
			_, nodePresent := r.Header["X-Node-Id"]
			fake.mu.Lock()
			fake.tokens = append(fake.tokens, tokenRequest{
				form:          r.Form,
				nodeID:        r.Header.Get("X-Node-Id"),
				nodeIDPresent: nodePresent,
			})
			issued := len(fake.tokens)
			fake.mu.Unlock()

			if opts.failStatus != 0 {
				w.WriteHeader(opts.failStatus)
				return
			}
			_, _ = fmt.Fprintf(
				w,
				`{"authToken":"auth-%d%s","nodeId":%q,"websocketToken":"websocket-%d%s"}`,
				issued, opts.tokenSuffix, testNodeID, issued, opts.tokenSuffix,
			)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/tokens/"):
			fake.mu.Lock()
			fake.deletes = append(fake.deletes, deleteRequest{
				path:   r.URL.Path,
				nodeID: r.Header.Get("X-Node-Id"),
			})
			fake.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case opts.echo && r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/webtunnelv2/"):
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			echoFrames(conn)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fake.Close)
	return fake
}

func (f *fakeBastion) tokenRequests() []tokenRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.tokens)
}

func (f *fakeBastion) deleteRequests() []deleteRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.deletes)
}

// newTestSession leaves wsDialer at the production default so callers that need
// a double can override it.
func newTestSession(t *testing.T, bastion *fakeBastion, plan tunnelPlan) *sessionClient {
	t.Helper()
	client, err := newSessionClient(bastion.URL, plan, staticCredential{token: testAccessToken})
	if err != nil {
		t.Fatal(err)
	}
	client.httpClient = bastion.Client()
	return client
}

// cleanupSession mirrors the tunnel shutting down.
func cleanupSession(t *testing.T, client *sessionClient) {
	t.Helper()
	if err := client.close(); err != nil {
		t.Fatal(err)
	}
}

// tunnel is a server running against the fake bastion, with a shutdown a test can
// trigger itself when it needs to assert on what stopping does.
type tunnel struct {
	addr string
	stop func()
}

// startTunnel serves until stop is called, or until the test ends.
func startTunnel(t *testing.T, session *sessionClient) *tunnel {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	conns := newServer(listener, session)
	served := make(chan error, 1)
	go func() { served <- conns.Serve(ctx) }()

	// Ordered as StartRemoteTunnel orders it: the session is released only once Serve
	// has returned, which is after the last handler finished.
	stop := sync.OnceFunc(func() {
		cancel()
		conns.Close()
		if err := <-served; err != nil {
			t.Errorf("Serve() = %v", err)
		}
		if err := session.close(); err != nil {
			t.Errorf("releasing the session: %v", err)
		}
	})
	t.Cleanup(stop)

	return &tunnel{addr: listener.Addr().String(), stop: stop}
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

func echoFrames(conn *websocket.Conn) {
	for {
		messageType, payload, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if err := conn.WriteMessage(messageType, payload); err != nil {
			return
		}
	}
}

func dialEchoWebSocket(t *testing.T) webSocketConn {
	t.Helper()
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		echoFrames(conn)
	}))
	t.Cleanup(server.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func assertEcho(t *testing.T, conn net.Conn, payload []byte) {
	t.Helper()
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("echo = %v, want %v", got, payload)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
