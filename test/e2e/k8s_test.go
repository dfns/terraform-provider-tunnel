//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dfns/terraform-provider-tunnel/internal/libs"
)

// TestKubernetesTunnelE2E drives the tunnel_kubernetes data source against a
// real cluster (a kind cluster in CI). It forwards to the built-in CoreDNS
// service (kube-system/kube-dns) on its Prometheus metrics port and asserts a
// real HTTP request reaches the pod. Reusing CoreDNS means no workload to
// deploy. Skips if no kubeconfig is available (e.g. SSH-only runners).
func TestKubernetesTunnelE2E(t *testing.T) {
	kubeconfig := resolveKubeconfig(t)
	kubeContext := os.Getenv("E2E_KUBE_CONTEXT")

	// Auto-assigned local port: the provider picks a free port (the common case).
	t.Run("auto_port", func(t *testing.T) {
		runKubernetesTunnelCase(t, kubeconfig, kubeContext, 0)
	})

	// Explicit local port: the provider must honor a caller-supplied port rather
	// than allocating one (the local_port != 0 branch in the data source Read).
	t.Run("explicit_port", func(t *testing.T) {
		port, err := libs.GetFreePort()
		if err != nil {
			t.Fatalf("allocating free port: %v", err)
		}
		runKubernetesTunnelCase(t, kubeconfig, kubeContext, port)
	})
}

// runKubernetesTunnelCase applies a tunnel_kubernetes config, verifies a real
// request flows through the tunnel, and checks the forked process self-terminates.
// requestedPort == 0 lets the provider choose the local port; otherwise the
// provider must bind exactly requestedPort.
func runKubernetesTunnelCase(t *testing.T, kubeconfig, kubeContext string, requestedPort int) {
	t.Helper()

	contextLine := ""
	if kubeContext != "" {
		contextLine = fmt.Sprintf("\n    config_context = %q", kubeContext)
	}

	localPortLine := ""
	if requestedPort != 0 {
		localPortLine = fmt.Sprintf("\n  local_port   = %d", requestedPort)
	}

	moduleDir := t.TempDir()

	config := fmt.Sprintf(`
terraform {
  required_providers {
    tunnel = {
      source = "dfns/tunnel"
    }
  }
}

data "tunnel_kubernetes" "t" {
  namespace    = "kube-system"
  service_name = "kube-dns"
  target_port  = 9153%s
  kubernetes = {
    config_path = %q%s
  }
}

resource "terraform_data" "probe" {
  input = data.tunnel_kubernetes.t.local_port
  provisioner "local-exec" {
    command = "curl -fsS -o resp.txt http://${data.tunnel_kubernetes.t.local_host}:${data.tunnel_kubernetes.t.local_port}/metrics"
  }
}

output "local_port" {
  value = data.tunnel_kubernetes.t.local_port
}
`, localPortLine, filepath.ToSlash(kubeconfig), contextLine)

	terraformApply(t, moduleDir, config)

	got, err := os.ReadFile(filepath.Join(moduleDir, "resp.txt"))
	if err != nil {
		t.Fatalf("reading tunneled response: %v", err)
	}
	// CoreDNS exposes Prometheus metrics; the body must contain HELP markers.
	if !strings.Contains(string(got), "# HELP") {
		t.Fatalf("k8s tunnel /metrics response missing Prometheus markers; got %d bytes:\n%.300s", len(got), got)
	}

	localPort, err := strconv.Atoi(terraformOutput(t, moduleDir, "local_port"))
	if err != nil {
		t.Fatalf("parsing local_port output: %v", err)
	}
	if requestedPort != 0 && localPort != requestedPort {
		t.Fatalf("provider bound local_port %d, want the requested %d", localPort, requestedPort)
	}

	// terraform has exited, so the forked tunnel must self-terminate and free
	// its local port (the WatchProcess half of the cross-OS process handling).
	assertTunnelTerminated(t, "localhost", localPort)
}

// resolveKubeconfig returns the kubeconfig path from KUBECONFIG (first entry) or
// ~/.kube/config, skipping the test when neither exists.
func resolveKubeconfig(t *testing.T) string {
	t.Helper()

	if kc := os.Getenv("KUBECONFIG"); kc != "" {
		if first := strings.Split(kc, string(os.PathListSeparator))[0]; first != "" {
			return first
		}
	}

	if home, err := os.UserHomeDir(); err == nil {
		def := filepath.Join(home, ".kube", "config")
		if _, err := os.Stat(def); err == nil {
			return def
		}
	}

	t.Skip("no kubeconfig found (set KUBECONFIG or ~/.kube/config); skipping k8s E2E test")
	return ""
}
