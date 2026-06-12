//go:build integration

package integration

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dfns/terraform-provider-tunnel/internal/libs"
	"github.com/dfns/terraform-provider-tunnel/internal/ssh"
	"github.com/dfns/terraform-provider-tunnel/internal/sshtest"
)

const sshTargetBody = "tunnel-it-ok"

// startSSHTarget stands up an in-process HTTP target and SSH bastion (both in
// the test process), returning the bastion port, the target port, and the PEM
// client key authorized on the bastion. Shared by the SSH fork tests.
func startSSHTarget(t *testing.T) (sshPort, targetPort int, keyPEM string) {
	t.Helper()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, sshTargetBody)
	}))
	t.Cleanup(target.Close)

	u, err := url.Parse(target.URL)
	if err != nil {
		t.Fatalf("parsing target URL %q: %v", target.URL, err)
	}
	targetPort, err = strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parsing target port from %q: %v", target.URL, err)
	}

	keyPEM, authorizedKey := sshtest.GenerateClientKey(t)
	return sshtest.StartServer(t, authorizedKey), targetPort, keyPEM
}

// TestSSHForkRemoteTunnelLifecycle exercises the real fork contract without
// terraform: ssh.ForkRemoteTunnel re-execs this binary (which TestMain turns
// into a tunnel child via ssh.StartRemoteTunnel), packs the config and ready
// file into the environment, and blocks until the child signals readiness.
// We then push a real HTTP request through the forwarded local port, and tear
// the tunnel down with libs.Interrupt — the exact call the provider's Close
// uses — asserting the forked process releases its port.
func TestSSHForkRemoteTunnelLifecycle(t *testing.T) {
	sshPort, targetPort, keyPEM := startSSHTarget(t)

	localPort, err := libs.GetFreePort()
	if err != nil {
		t.Fatalf("allocating local port: %v", err)
	}

	cmd, err := ssh.ForkRemoteTunnel(context.Background(), ssh.TunnelConfig{
		LocalHost:  "127.0.0.1", // exercise the configurable bind address
		LocalPort:  localPort,
		SSHHost:    "127.0.0.1",
		SSHPort:    sshPort,
		SSHUser:    sshtest.User,
		SSHKey:     keyPEM,
		TargetHost: "127.0.0.1",
		TargetPort: targetPort,
	})
	if err != nil {
		t.Fatalf("ForkRemoteTunnel: %v", err)
	}
	// Safety net: make sure the forked child never outlives the test.
	t.Cleanup(func() {
		_ = libs.Interrupt(cmd.Process.Pid)
		_ = cmd.Wait()
	})

	// ForkRemoteTunnel only returns after the ready handshake, so the tunnel is
	// established: a request to the local port must reach the target through it.
	localAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort))
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s/", localAddr))
	if err != nil {
		t.Fatalf("request through tunnel: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("reading tunneled response: %v", err)
	}
	if string(body) != sshTargetBody {
		t.Fatalf("tunnel returned %q, want %q", body, sshTargetBody)
	}

	// Interrupt is the provider's Close path: the forked process must stop and
	// release its local port.
	if err := libs.Interrupt(cmd.Process.Pid); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	requireEventually(t, 15*time.Second, func() error { return checkPortClosed(localAddr) },
		"forked tunnel still listening after Interrupt; it did not tear down")
}

// TestSSHForkParentExitTeardown wires the full fork chain to the WatchProcess
// teardown that the Interrupt test cannot reach. ForkRemoteTunnel ties the
// child to its forker's parent (os.Getppid), so a controllable tree is needed:
//
//	test ── forkparent helper ── forker helper ── ssh.ForkRemoteTunnel ── tunnel
//	         (watched process)                                            (watches forkparent)
//
// Killing the forkparent helper simulates terraform exiting; the tunnel's own
// WatchProcess (not a synthetic stand-in) must notice and self-terminate,
// releasing the forwarded port. This is the production teardown path end-to-end.
func TestSSHForkParentExitTeardown(t *testing.T) {
	sshPort, targetPort, keyPEM := startSSHTarget(t)

	localPort, err := libs.GetFreePort()
	if err != nil {
		t.Fatalf("allocating local port: %v", err)
	}
	pidfile := filepath.Join(t.TempDir(), "tunnel.pid")

	parent := spawnHelper(t, modeForkParent,
		sshKeyEnv+"="+keyPEM,
		sshPortEnv+"="+strconv.Itoa(sshPort),
		localPortEnv+"="+strconv.Itoa(localPort),
		targetPortEnv+"="+strconv.Itoa(targetPort),
		forkerPidfileEnv+"="+pidfile,
	)
	// If parent-exit teardown is broken, the tunnel child would leak — kill it.
	t.Cleanup(func() {
		if pid := readPidfile(pidfile); pid > 0 {
			_ = libs.Interrupt(pid)
		}
	})

	// The tunnel must come up through the full fork chain.
	localAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort))
	requireEventually(t, 30*time.Second, func() error { return checkPortReachable(localAddr) },
		"tunnel never became reachable through the fork chain")

	// Simulate terraform dying. WatchProcess only fires once the watched PID is
	// fully gone, so the killed parent must also be reaped.
	if err := parent.Process.Kill(); err != nil {
		t.Fatalf("killing forkparent: %v", err)
	}
	_ = parent.Wait()

	// The tunnel's WatchProcess (2s poll) must self-terminate the forked child.
	requireEventually(t, 20*time.Second, func() error { return checkPortClosed(localAddr) },
		"tunnel still listening after its watched parent exited; WatchProcess did not tear it down")
}

// readPidfile reads a PID written by the forker helper, returning 0 if the file
// is absent or unparseable.
func readPidfile(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}
