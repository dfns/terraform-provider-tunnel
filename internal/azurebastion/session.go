package azurebastion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/gorilla/websocket"
)

const (
	armScope                 = "https://management.azure.com/.default"
	defaultHTTPClientTimeout = 30 * time.Second
	tokenRefreshWindow       = 5 * time.Minute
	sessionCleanupTimeout    = 10 * time.Second
	// Bastion rejects continuation frames, so each write must fit one frame.
	// This matches the Azure CLI:
	// https://github.com/Azure/azure-cli-extensions/blob/273739924ff4cd31a56ac789e40c44d0e2fdd649/src/bastion/azext_bastion/tunnel.py
	frameSize = 4096
)

type webSocketConn interface {
	ReadMessage() (messageType int, p []byte, err error)
	WriteMessage(messageType int, data []byte) error
	Close() error
}

type webSocketDialer interface {
	DialContext(context.Context, string, http.Header) (webSocketConn, *http.Response, error)
}

// wsStream adapts WebSocket messages to a byte stream.
type wsStream struct {
	conn    webSocketConn
	pending []byte
}

func (s *wsStream) Read(p []byte) (int, error) {
	for len(s.pending) == 0 {
		messageType, payload, err := s.conn.ReadMessage()
		if err != nil {
			return 0, io.EOF
		}
		if messageType != websocket.BinaryMessage {
			continue
		}
		s.pending = payload
	}
	n := copy(p, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}

func (s *wsStream) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		chunk := min(len(p), frameSize)
		if err := s.conn.WriteMessage(websocket.BinaryMessage, p[:chunk]); err != nil {
			return written, err
		}
		written += chunk
		p = p[chunk:]
	}
	return written, nil
}

func (s *wsStream) Close() error { return s.conn.Close() }

type gorillaDialer struct {
	dialer *websocket.Dialer
}

func newWebSocketDialer() *websocket.Dialer {
	dialer := *websocket.DefaultDialer
	dialer.WriteBufferSize = frameSize
	return &dialer
}

func (d gorillaDialer) DialContext(ctx context.Context, target string, header http.Header) (webSocketConn, *http.Response, error) {
	return d.dialer.DialContext(ctx, target, header)
}

// sessionClient follows the Azure CLI Bastion data-plane protocol:
// https://github.com/Azure/azure-cli-extensions/blob/273739924ff4cd31a56ac789e40c44d0e2fdd649/src/bastion/azext_bastion/tunnel.py
type sessionClient struct {
	baseURL          string
	wsBaseURL        string
	targetResourceID string
	targetPort       int
	hostname         string
	credential       azcore.TokenCredential
	httpClient       *http.Client
	wsDialer         webSocketDialer

	mu        sync.Mutex
	authToken string
	nodeID    string

	credMu      sync.Mutex
	cachedToken azcore.AccessToken
}

type tokenResponse struct {
	AuthToken      string `json:"authToken"`
	NodeID         string `json:"nodeId"`
	WebsocketToken string `json:"websocketToken"`
}

func newSessionClient(endpoint string, plan tunnelPlan, credential azcore.TokenCredential) (*sessionClient, error) {
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return nil, errors.New("invalid Azure Bastion DNS endpoint")
	}
	scheme, wsScheme := "https", "wss"
	if parsed.Scheme == "http" {
		scheme, wsScheme = "http", "ws"
	}
	return &sessionClient{
		baseURL:          scheme + "://" + parsed.Host,
		wsBaseURL:        wsScheme + "://" + parsed.Host,
		targetResourceID: plan.TargetResourceID,
		targetPort:       plan.TargetPort,
		hostname:         plan.Hostname,
		credential:       credential,
		httpClient:       &http.Client{Timeout: defaultHTTPClientTimeout},
		wsDialer:         gorillaDialer{dialer: newWebSocketDialer()},
	}, nil
}

func (c *sessionClient) open(ctx context.Context) (webSocketConn, error) {
	accessToken, err := c.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	token, err := c.negotiate(ctx, accessToken)
	if err != nil {
		return nil, err
	}

	// Concatenated, not assigned to url.URL.Path, which would escape a second time.
	wsURL := c.wsBaseURL + "/webtunnelv2/" + url.PathEscape(token.WebsocketToken) +
		"?X-Node-Id=" + url.QueryEscape(token.NodeID)

	conn, response, err := c.wsDialer.DialContext(ctx, wsURL, nil)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		// Unwrapped: the dial URL embeds the WebSocket token.
		return nil, errors.New("connect Azure Bastion WebSocket")
	}
	return conn, nil
}

// accessToken avoids invoking external credentials for every local connection.
func (c *sessionClient) accessToken(ctx context.Context) (string, error) {
	c.credMu.Lock()
	defer c.credMu.Unlock()
	if time.Until(c.cachedToken.ExpiresOn) > tokenRefreshWindow {
		return c.cachedToken.Token, nil
	}
	token, err := c.credential.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{armScope}})
	if err != nil {
		return "", fmt.Errorf("acquire Azure credential: %w", err)
	}
	c.cachedToken = token
	return token.Token, nil
}

// Each negotiation consumes the previous token, so the request and update are atomic.
func (c *sessionClient) negotiate(ctx context.Context, accessToken string) (tokenResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	form := url.Values{
		"resourceId":       {c.targetResourceID},
		"protocol":         {"tcptunnel"},
		"workloadHostPort": {strconv.Itoa(c.targetPort)},
		// The data plane wants the raw token, with no Bearer prefix.
		"aztoken": {accessToken},
		"token":   {c.authToken},
	}
	if c.hostname != "" {
		form.Set("hostname", c.hostname)
	}

	tokenURL := c.baseURL + "/api/tokens"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("create Azure Bastion token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c.nodeID != "" {
		req.Header.Set("X-Node-Id", c.nodeID)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("send Azure Bastion token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("read Azure Bastion token response: %w", err)
	}
	var token tokenResponse
	if len(body) > 0 {
		_ = json.Unmarshal(body, &token)
	}
	if resp.StatusCode != http.StatusOK {
		return tokenResponse{}, fmt.Errorf("request Azure Bastion token: HTTP status %d", resp.StatusCode)
	}
	if token.AuthToken == "" || token.NodeID == "" || token.WebsocketToken == "" {
		return tokenResponse{}, errors.New("missing required fields in Azure Bastion token response")
	}

	c.authToken = token.AuthToken
	c.nodeID = token.NodeID
	return token, nil
}

// close detaches the continuation token before deleting it to remain idempotent.
func (c *sessionClient) close() error {
	c.mu.Lock()
	token, node := c.authToken, c.nodeID
	c.authToken, c.nodeID = "", ""
	c.mu.Unlock()
	if token == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), sessionCleanupTimeout)
	defer cancel()
	return c.deleteToken(ctx, token, node)
}

func (c *sessionClient) deleteToken(ctx context.Context, token, node string) error {
	if token == "" {
		return nil
	}

	deleteURL := c.baseURL + "/api/tokens/" + url.PathEscape(token)
	// Do not wrap errors that may contain the token-bearing URL.
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, deleteURL, nil)
	if err != nil {
		return errors.New("create Azure Bastion session cleanup request")
	}
	if node != "" {
		req.Header.Set("X-Node-Id", node)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return errors.New("send Azure Bastion session cleanup request")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("clean up Azure Bastion session: HTTP status %d", resp.StatusCode)
	}
	return nil
}
