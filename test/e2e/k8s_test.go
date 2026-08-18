//go:build e2e

package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dfns/terraform-provider-tunnel/internal/libs"
)

// Fixture workloads reuse the CoreDNS image the cluster is already running,
// configured as a bare Prometheus endpoint. That keeps these tests free of any
// external image to pull, pin, or keep up to date.
const (
	// The service exposes a different port, so a tunnel that ignores targetPort
	// cannot reach this one.
	fixtureMetricsPort = 8080
	fixtureServicePort = 80
	// Where the deliberately broken workload serves instead, leaving
	// fixtureMetricsPort closed so its readiness probe and any tunnel both fail.
	fixtureWrongPort = 8090
)

// kubeFixture addresses either a pre-existing service or a throwaway one.
type kubeFixture struct {
	kubeconfig  string
	kubeContext string
	namespace   string
	service     string
}

func existingServiceFixture(t *testing.T, namespace, service string) kubeFixture {
	t.Helper()
	return kubeFixture{
		kubeconfig:  resolveKubeconfig(t),
		kubeContext: os.Getenv("E2E_KUBE_CONTEXT"),
		namespace:   namespace,
		service:     service,
	}
}

func (f kubeFixture) inNamespace(namespace string) kubeFixture {
	f.namespace = namespace
	return f
}

// freshNamespaceFixture creates a throwaway namespace and tears it down with the
// test.
func freshNamespaceFixture(t *testing.T, namespace string) kubeFixture {
	t.Helper()
	fixture := existingServiceFixture(t, namespace, "tunnel-e2e")

	// Namespace creation and teardown cannot run inside the namespace itself.
	cluster := fixture.inNamespace("default")
	// A namespace left behind by an interrupted run would fail the create below.
	cluster.kubectl(t, "delete", "namespace", namespace, "--ignore-not-found", "--wait=true")
	cluster.kubectl(t, "create", "namespace", namespace)
	t.Cleanup(func() {
		bin, err := exec.LookPath("kubectl")
		if err != nil {
			return
		}
		args := cluster.kubectlArgs("delete", "namespace", namespace, "--ignore-not-found", "--wait=false")
		_ = exec.Command(bin, args...).Run()
	})

	return fixture
}

// resolveKubeconfig prefers the first KUBECONFIG entry over ~/.kube/config.
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

// kubectlArgs keeps Go helpers and Terraform's local-exec commands pointed at
// the same cluster and namespace.
func (f kubeFixture) kubectlArgs(args ...string) []string {
	flags := []string{"--kubeconfig", f.kubeconfig}
	if f.kubeContext != "" {
		flags = append(flags, "--context", f.kubeContext)
	}
	flags = append(flags, "--namespace", f.namespace)
	return append(flags, args...)
}

func (f kubeFixture) kubectl(t *testing.T, args ...string) string {
	t.Helper()
	return f.kubectlStdin(t, "", args...)
}

func (f kubeFixture) kubectlStdin(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	bin, err := exec.LookPath("kubectl")
	if err != nil {
		t.Skip("kubectl not found in PATH; skipping k8s E2E test")
	}

	cmd := exec.Command(bin, f.kubectlArgs(args...)...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("kubectl %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

// deployment probes readiness on fixtureMetricsPort regardless of servePort, so a
// workload serving elsewhere is both unready and genuinely unreachable. The name
// controls sort order among the service's pods.
func (f kubeFixture) deployment(t *testing.T, name string, servePort int) {
	t.Helper()

	// CoreDNS needs a server block; the DNS port is unprivileged and unused.
	f.kubectlStdin(t, fmt.Sprintf(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: %[1]s
  namespace: %[2]s
data:
  Corefile: |
    .:5300 {
        errors
        prometheus :%[3]d
    }
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: tunnel-e2e
      deployment: %[1]s
  template:
    metadata:
      labels:
        app: tunnel-e2e
        deployment: %[1]s
    spec:
      terminationGracePeriodSeconds: 1
      containers:
        - name: metrics
          image: %[4]s
          args: ["-conf", "/etc/coredns/Corefile"]
          ports:
            - name: metrics
              containerPort: %[3]d
          volumeMounts:
            - name: corefile
              mountPath: /etc/coredns
          readinessProbe:
            httpGet:
              path: /metrics
              port: %[5]d
            periodSeconds: 2
            failureThreshold: 1
      volumes:
        - name: corefile
          configMap:
            name: %[1]s
`, name, f.namespace, servePort, f.corednsImage(t), fixtureMetricsPort), "apply", "-f", "-")
}

func (f kubeFixture) corednsImage(t *testing.T) string {
	t.Helper()
	image := f.inNamespace("kube-system").kubectl(t, "get", "deployment", "coredns", "-o",
		"jsonpath={.spec.template.spec.containers[0].image}")
	if image == "" {
		t.Skip("cluster has no coredns deployment to source a workload image from")
	}
	return image
}

// exposeService publishes a service port that differs from the container port.
// targetPort may be numeric or the container port's name.
func (f kubeFixture) exposeService(t *testing.T, targetPort string) {
	t.Helper()
	f.kubectlStdin(t, fmt.Sprintf(`
apiVersion: v1
kind: Service
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  selector:
    app: tunnel-e2e
  ports:
    - name: http
      port: %[3]d
      targetPort: %[4]s
`, f.service, f.namespace, fixtureServicePort, targetPort), "apply", "-f", "-")
}

func (f kubeFixture) waitForRollout(t *testing.T, deployment string) {
	t.Helper()
	f.kubectl(t, "rollout", "status", "deployment/"+deployment, "--timeout=180s")
}

// waitForRunningPod deliberately does not require readiness, which is how the
// broken workload is awaited. kubectl wait cannot do this: it fails outright
// while no pod matches yet.
func (f kubeFixture) waitForRunningPod(t *testing.T, deployment string) {
	t.Helper()
	deadline := time.Now().Add(180 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(f.podField(t, deployment, "status.phase"), "Running") {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("no running pod for deployment %s", deployment)
}

func (f kubeFixture) podNames(t *testing.T, deployment string) []string {
	t.Helper()
	return strings.Fields(f.podField(t, deployment, "metadata.name"))
}

// podsReady lets a test confirm a workload is unready rather than assume it.
func (f kubeFixture) podsReady(t *testing.T, deployment string) string {
	t.Helper()
	return f.kubectl(t, "get", "pods", "-l", "deployment="+deployment, "-o",
		`jsonpath={range .items[*]}{.status.conditions[?(@.type=="Ready")].status} {end}`)
}

func (f kubeFixture) podField(t *testing.T, deployment, field string) string {
	t.Helper()
	return f.kubectl(t, "get", "pods", "-l", "deployment="+deployment, "-o",
		fmt.Sprintf("jsonpath={.items[*].%s}", field))
}

// tunnelConfig appends the given resource blocks to the data source. A zero
// localPort lets the provider choose one.
func tunnelConfig(f kubeFixture, targetPort, localPort int, blocks ...string) string {
	localPortLine := ""
	if localPort != 0 {
		localPortLine = fmt.Sprintf("\n  local_port   = %d", localPort)
	}
	contextLine := ""
	if f.kubeContext != "" {
		contextLine = fmt.Sprintf("\n    config_context = %q", f.kubeContext)
	}

	return fmt.Sprintf(`
terraform {
  required_providers {
    tunnel = {
      source = "dfns/tunnel"
    }
  }
}

data "tunnel_kubernetes" "t" {
  namespace    = %[1]q
  service_name = %[2]q
  target_port  = %[3]d%[4]s
  kubernetes = {
    config_path = %[5]q%[6]s
  }
}

output "local_port" {
  value = data.tunnel_kubernetes.t.local_port
}
`, f.namespace, f.service, targetPort, localPortLine,
		filepath.ToSlash(f.kubeconfig), contextLine) + strings.Join(blocks, "")
}

func probe(name, output string) string {
	command := fmt.Sprintf(
		"curl -fsS --retry 15 --retry-delay 2 --retry-all-errors -o %s "+
			"http://${data.tunnel_kubernetes.t.local_host}:${data.tunnel_kubernetes.t.local_port}/metrics",
		output,
	)
	return fmt.Sprintf(`
resource "terraform_data" %[1]q {
  input = data.tunnel_kubernetes.t.local_port
  provisioner "local-exec" {
    command = %[2]q
  }
}
`, name, command)
}

// assertMetrics distinguishes a real response from an empty or error one.
func assertMetrics(t *testing.T, moduleDir, name string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(moduleDir, name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	if !strings.Contains(string(got), "# HELP") {
		t.Fatalf("%s is missing Prometheus markers; got %d bytes:\n%.300s", name, len(got), got)
	}
}

func localPortOutput(t *testing.T, moduleDir string) int {
	t.Helper()
	port, err := strconv.Atoi(terraformOutput(t, moduleDir, "local_port"))
	if err != nil {
		t.Fatalf("parsing local_port output: %v", err)
	}
	return port
}

// TestKubernetesTunnelE2E forwards to a service the tests did not create, which
// is the only coverage of a multi-port service managed by the cluster itself.
func TestKubernetesTunnelE2E(t *testing.T) {
	fixture := existingServiceFixture(t, "kube-system", "kube-dns")

	for _, tt := range []struct {
		name string
		// requestedPort == 0 lets the provider pick a free port (the common case);
		// otherwise it must bind exactly this one.
		requestedPort func(t *testing.T) int
	}{
		{name: "auto_port", requestedPort: func(*testing.T) int { return 0 }},
		{name: "explicit_port", requestedPort: func(t *testing.T) int {
			port, err := libs.GetFreePort()
			if err != nil {
				t.Fatalf("allocating free port: %v", err)
			}
			return port
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requested := tt.requestedPort(t)
			moduleDir := t.TempDir()
			terraformApply(t, moduleDir, tunnelConfig(fixture, 9153, requested,
				probe("probe", "resp.txt")))

			assertMetrics(t, moduleDir, "resp.txt")

			localPort := localPortOutput(t, moduleDir)
			if requested != 0 && localPort != requested {
				t.Fatalf("provider bound local_port %d, want the requested %d", localPort, requested)
			}
			// terraform has exited, so the forked tunnel must self-terminate and free
			// its local port (the WatchProcess half of the cross-OS process handling).
			assertTunnelTerminated(t, "localhost", localPort)
		})
	}
}

// TestKubernetesServicePortResolutionE2E covers the common 80 -> 8080 shape.
// Forwarding the service port straight to the pod would reach a closed port.
func TestKubernetesServicePortResolutionE2E(t *testing.T) {
	for _, tt := range []struct {
		name       string
		targetPort string
	}{
		{name: "numeric_target_port", targetPort: strconv.Itoa(fixtureMetricsPort)},
		{name: "named_target_port", targetPort: "metrics"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := freshNamespaceFixture(t, "tunnel-e2e-ports-"+strings.ReplaceAll(tt.name, "_", "-"))
			fixture.deployment(t, "serving", fixtureMetricsPort)
			fixture.exposeService(t, tt.targetPort)
			fixture.waitForRollout(t, "serving")

			moduleDir := t.TempDir()
			terraformApply(t, moduleDir, tunnelConfig(fixture, fixtureServicePort, 0,
				probe("probe", "resp.txt")))

			assertMetrics(t, moduleDir, "resp.txt")
		})
	}
}

// TestKubernetesSkipsUnreadyPodE2E puts a pod that sorts first but serves nothing
// behind the service, which is what selecting the first pod used to reach.
func TestKubernetesSkipsUnreadyPodE2E(t *testing.T) {
	fixture := freshNamespaceFixture(t, "tunnel-e2e-unready")
	// "aaa-" sorts ahead of "zzz-", so a first-pod selection lands on the broken
	// workload.
	fixture.deployment(t, "aaa-broken", fixtureWrongPort)
	fixture.deployment(t, "zzz-serving", fixtureMetricsPort)
	fixture.exposeService(t, strconv.Itoa(fixtureMetricsPort))
	fixture.waitForRollout(t, "zzz-serving")
	fixture.waitForRunningPod(t, "aaa-broken")

	moduleDir := t.TempDir()
	terraformApply(t, moduleDir, tunnelConfig(fixture, fixtureServicePort, 0,
		probe("probe", "resp.txt")))

	assertMetrics(t, moduleDir, "resp.txt")

	// Guard the fixture itself: the broken pod has to exist and be unready, or the
	// test would pass without exercising readiness filtering at all.
	broken := fixture.podNames(t, "aaa-broken")
	if len(broken) == 0 {
		t.Fatal("fixture produced no pod for the broken workload")
	}
	if ready := fixture.podsReady(t, "aaa-broken"); !strings.Contains(ready, "False") {
		t.Fatalf("broken workload pod readiness = %q, want False", ready)
	}
}
