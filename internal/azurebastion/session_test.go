package azurebastion

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestSessionProtocolContinuationAndCleanup(t *testing.T) {
	bastion := newFakeBastion(t, bastionOptions{})
	dialer := &captureDialer{conn: &scriptedWebSocket{}}
	client := newTestSession(t, bastion, validPlan(t))
	client.wsDialer = dialer

	for range 2 {
		if _, err := client.open(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	cleanupSession(t, client)

	tokens := bastion.tokenRequests()
	if len(tokens) != 2 {
		t.Fatalf("token requests = %d, want 2", len(tokens))
	}
	first := tokens[0].form
	for _, field := range []struct{ name, want string }{
		{"resourceId", validConfig().TargetResourceID},
		{"protocol", "tcptunnel"},
		{"workloadHostPort", "5432"},
		{"aztoken", testAccessToken},
	} {
		if got := first.Get(field.name); got != field.want {
			t.Errorf("%s = %q, want %q", field.name, got, field.want)
		}
	}
	if first.Get("token") != "" || tokens[0].nodeIDPresent {
		t.Error("first token request unexpectedly carried continuation state")
	}
	if tokens[1].form.Get("token") != "auth-1" || tokens[1].nodeID != testNodeID {
		t.Errorf("second token request missing continuation state: %+v", tokens[1])
	}

	deletes := bastion.deleteRequests()
	if len(deletes) != 1 {
		t.Fatalf("delete requests = %d, want 1", len(deletes))
	}
	if deletes[0].path != "/api/tokens/auth-2" || deletes[0].nodeID != testNodeID {
		t.Errorf("unexpected cleanup request: %+v", deletes[0])
	}

	dialed := dialer.dialed()
	if len(dialed) != len(tokens) {
		t.Fatalf("WebSocket dials = %d, want %d", len(dialed), len(tokens))
	}
	for i, rawURL := range dialed {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			t.Fatal(err)
		}
		wantToken := fmt.Sprintf("websocket-%d", i+1)
		if !strings.HasSuffix(parsed.Path, "/webtunnelv2/"+wantToken) {
			t.Errorf("WebSocket path = %q, want token %q", parsed.Path, wantToken)
		}
		if parsed.Query().Get("X-Node-Id") != testNodeID {
			t.Errorf("WebSocket node query = %q", parsed.RawQuery)
		}
	}
}

func TestSessionOpenAfterCleanupStartsFreshSession(t *testing.T) {
	bastion := newFakeBastion(t, bastionOptions{})
	client := newTestSession(t, bastion, validPlan(t))
	client.wsDialer = &captureDialer{conn: &scriptedWebSocket{}}

	if _, err := client.open(context.Background()); err != nil {
		t.Fatal(err)
	}
	cleanupSession(t, client)
	if _, err := client.open(context.Background()); err != nil {
		t.Fatal(err)
	}

	tokens := bastion.tokenRequests()
	if len(tokens) != 2 {
		t.Fatalf("token requests = %d, want 2", len(tokens))
	}
	reopened := tokens[1]
	if got := reopened.form.Get("token"); got != "" {
		t.Errorf("token request after cleanup reused deleted token %q", got)
	}
	if reopened.nodeIDPresent {
		t.Errorf("token request after cleanup carried X-Node-Id %q", reopened.nodeID)
	}
}

func TestSessionEscapesTokensInURLsExactlyOnce(t *testing.T) {
	// A token carrying a path separator must not split the path, nor be escaped twice.
	bastion := newFakeBastion(t, bastionOptions{tokenSuffix: "/x"})
	dialer := &captureDialer{conn: &scriptedWebSocket{}}
	client := newTestSession(t, bastion, validPlan(t))
	client.wsDialer = dialer

	if _, err := client.open(context.Background()); err != nil {
		t.Fatal(err)
	}
	cleanupSession(t, client)

	dialed := dialer.dialed()
	if len(dialed) != 1 || !strings.Contains(dialed[0], "/webtunnelv2/websocket-1%2Fx?") {
		t.Errorf("WebSocket URL did not escape the token exactly once: %v", dialed)
	}
	// The fake records the decoded path, so an intact round trip proves single escaping.
	deletes := bastion.deleteRequests()
	if len(deletes) != 1 || deletes[0].path != "/api/tokens/auth-1/x" {
		t.Errorf("cleanup URL did not escape the token exactly once: %+v", deletes)
	}
}

func TestSessionIPConnectForm(t *testing.T) {
	bastion := newFakeBastion(t, bastionOptions{})
	client := newTestSession(t, bastion, ipConnectPlan(t))
	client.wsDialer = &captureDialer{conn: &scriptedWebSocket{}}

	if _, err := client.open(context.Background()); err != nil {
		t.Fatal(err)
	}

	tokens := bastion.tokenRequests()
	if len(tokens) != 1 {
		t.Fatalf("token requests = %d, want 1", len(tokens))
	}
	if got := tokens[0].form.Get("hostname"); got != testTargetIP {
		t.Errorf("hostname = %q, want %q", got, testTargetIP)
	}
}

func TestSessionTokenFailureReportsStatusOnly(t *testing.T) {
	bastion := newFakeBastion(t, bastionOptions{failStatus: http.StatusBadRequest})
	client := newTestSession(t, bastion, validPlan(t))
	client.wsDialer = &captureDialer{}
	client.authToken = "old-auth"

	_, err := client.open(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTTP status 400") {
		t.Fatalf("unexpected error: %v", err)
	}
	// The response body is never surfaced, so no secret can ride out on it.
	if strings.Contains(err.Error(), testAccessToken) || strings.Contains(err.Error(), "old-auth") {
		t.Fatalf("error leaked a secret: %q", err)
	}
}

func TestSessionCredentialFailureDoesNotDial(t *testing.T) {
	dialer := &captureDialer{}
	client, err := newSessionClient(
		"https://example.invalid",
		validPlan(t),
		staticCredential{err: errors.New("no credential")},
	)
	if err != nil {
		t.Fatal(err)
	}
	client.wsDialer = dialer

	if _, err := client.open(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "acquire Azure credential") {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dialer.dialed()) != 0 {
		t.Fatal("WebSocket dialed after credential failure")
	}
}
