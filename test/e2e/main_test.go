//go:build e2e

package e2e

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dfns/terraform-provider-tunnel/internal/libs"
)

// providerDir is the directory containing the freshly built provider binary.
// It is referenced by the Terraform dev_overrides config so that `terraform
// apply` loads our local build instead of the released provider.
var providerDir string

func TestMain(m *testing.M) {
	dir, err := buildProvider()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to build provider for e2e tests: %v\n", err)
		os.Exit(1)
	}
	providerDir = dir
	os.Exit(m.Run())
}

// repoRoot returns the module root, derived from this file's location so it is
// independent of the test's working directory.
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// buildProvider compiles the provider into a temp directory named
// terraform-provider-tunnel[.exe], matching the dev_overrides layout. We build
// with CGO disabled to mirror the released (goreleaser) binary users actually run.
func buildProvider() (string, error) {
	dir := filepath.Join(os.TempDir(), "terraform-provider-tunnel-e2e")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	bin := filepath.Join(dir, "terraform-provider-tunnel")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}

	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = repoRoot()
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("go build: %w", err)
	}
	return dir, nil
}

// requireTerraform returns the path to the terraform binary, skipping the test
// if it is not installed (e.g. local dev machines without Terraform).
func requireTerraform(t *testing.T) string {
	t.Helper()
	tf, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("terraform not found in PATH; skipping E2E test")
	}
	return tf
}

// writeDevOverrides writes a CLI config that points the tunnel provider at our
// local build. With dev_overrides in effect, `terraform apply` runs without
// `terraform init` and without a dependency lock file.
func writeDevOverrides(t *testing.T, dir string) string {
	t.Helper()
	// Use forward slashes so the path is valid inside an HCL string on Windows.
	content := fmt.Sprintf(`
provider_installation {
  dev_overrides {
    "dfns/tunnel" = %q
  }
  direct {}
}
`, filepath.ToSlash(providerDir))

	path := filepath.Join(dir, "dev.tfrc")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing dev_overrides: %v", err)
	}
	return path
}

// terraformApply writes the given config into moduleDir and runs
// `terraform apply -auto-approve`, failing the test on a non-zero exit code.
// The whole run shares one terraform process, so any tunnel forked while the
// data source is read stays alive until the local-exec verification has run.
func terraformApply(t *testing.T, moduleDir, config string) {
	t.Helper()
	tf := requireTerraform(t)

	if err := os.WriteFile(filepath.Join(moduleDir, "main.tf"), []byte(config), 0o644); err != nil {
		t.Fatalf("writing main.tf: %v", err)
	}
	tfrc := writeDevOverrides(t, moduleDir)
	logDir := filepath.Join(moduleDir, "tunnel-logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("creating tunnel log dir: %v", err)
	}

	cmd := exec.Command(tf, "apply", "-auto-approve", "-no-color")
	cmd.Dir = moduleDir
	cmd.Env = append(os.Environ(),
		"TF_CLI_CONFIG_FILE="+tfrc,
		"TF_IN_AUTOMATION=1",
		libs.TunnelLogDirEnv+"="+logDir,
	)

	out, err := cmd.CombinedOutput()
	t.Logf("terraform apply output:\n%s", out)
	if err != nil {
		dumpTunnelLogs(t, logDir)
		t.Fatalf("terraform apply failed: %v", err)
	}
}

// dumpTunnelLogs reads and logs forked-tunnel log files from the explicit log
// directory this test passed to the provider.
func dumpTunnelLogs(t *testing.T, logDir string) {
	t.Helper()
	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Logf("tunnel log dir %s: could not read: %v", logDir, err)
		return
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			paths = append(paths, filepath.Join(logDir, entry.Name()))
		}
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		t.Logf("tunnel log dir %s: no log files", logDir)
		return
	}

	for _, path := range paths {
		content, err := os.ReadFile(path)
		switch {
		case err != nil:
			t.Logf("tunnel log %s: could not read: %v", path, err)
		case len(content) == 0:
			// The child is torn down before flushing when the fork fails fast
			t.Logf("tunnel log %s: empty (child terminated before writing any output)", path)
		default:
			t.Logf("--- tunnel log %s (%d bytes) ---\n%s", path, len(content), content)
		}
	}
}

// terraformOutput reads a root-level output value from the applied state.
func terraformOutput(t *testing.T, moduleDir, name string) string {
	t.Helper()
	tf := requireTerraform(t)

	cmd := exec.Command(tf, "output", "-raw", name)
	cmd.Dir = moduleDir
	cmd.Env = append(os.Environ(),
		"TF_IN_AUTOMATION=1",
	)

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("terraform output -raw %s: %v", name, err)
	}
	return strings.TrimSpace(string(out))
}

// assertTunnelTerminated verifies the forked tunnel process tore itself down
// after terraform exited. Once the watched parent (terraform) is gone,
// WatchProcess interrupts the child, which closes its local listener — so the
// advertised local port becoming unreachable is the observable signal that the
// forked process self-terminated rather than leaking.
func assertTunnelTerminated(t *testing.T, localHost string, localPort int) {
	t.Helper()
	addr := net.JoinHostPort(localHost, strconv.Itoa(localPort))
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			return // listener gone → forked tunnel self-terminated
		}
		_ = conn.Close()
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("tunnel still accepting connections on %s after terraform exited; forked process did not self-terminate", addr)
}
