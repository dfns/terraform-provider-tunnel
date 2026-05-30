//go:build e2e

package e2e

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/dfns/terraform-provider-tunnel/internal/sshtest"
)

const sshTargetBody = "tunnel-e2e-ok"

// TestSSHTunnelE2E drives the tunnel_ssh data source end-to-end against an
// in-process SSH bastion that local-forwards to an in-process HTTP target, then
// asserts a real request flows through the tunnel. Runs on Linux, Windows and
// macOS so the OS-specific fork/process-watch path is exercised on each.
func TestSSHTunnelE2E(t *testing.T) {
	// Target the SSH tunnel forwards to.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, sshTargetBody)
	}))
	t.Cleanup(target.Close)
	targetPort := mustURLPort(t, target.URL)

	// Client key passed to the provider as PEM content, authorized on the server.
	keyPEM, authorizedKey := sshtest.GenerateClientKey(t)
	sshPort := sshtest.StartServer(t, authorizedKey)

	moduleDir := t.TempDir()

	config := fmt.Sprintf(`
terraform {
  required_providers {
    tunnel = {
      source = "dfns/tunnel"
    }
  }
}

data "tunnel_ssh" "t" {
  ssh_host    = "127.0.0.1"
  ssh_port    = %d
  ssh_user    = %q
  ssh_key     = <<EOT
%sEOT
  target_host = "127.0.0.1"
  target_port = %d
}

resource "terraform_data" "probe" {
  input = data.tunnel_ssh.t.local_port
  provisioner "local-exec" {
    command = "curl -fsS -o resp.txt http://${data.tunnel_ssh.t.local_host}:${data.tunnel_ssh.t.local_port}/"
  }
}

output "local_port" {
  value = data.tunnel_ssh.t.local_port
}
`, sshPort, sshtest.User, keyPEM, targetPort)

	terraformApply(t, moduleDir, config)

	got, err := os.ReadFile(filepath.Join(moduleDir, "resp.txt"))
	if err != nil {
		t.Fatalf("reading tunneled response: %v", err)
	}
	if string(got) != sshTargetBody {
		t.Fatalf("tunnel returned %q, want %q", got, sshTargetBody)
	}

	// terraform has exited, so the forked tunnel must self-terminate and free
	// its local port (the WatchProcess half of the cross-OS process handling).
	localPort, err := strconv.Atoi(terraformOutput(t, moduleDir, "local_port"))
	if err != nil {
		t.Fatalf("parsing local_port output: %v", err)
	}
	assertTunnelTerminated(t, "localhost", localPort)
}

func mustURLPort(t *testing.T, raw string) int {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parsing %q: %v", raw, err)
	}
	p, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parsing port from %q: %v", raw, err)
	}
	return p
}
