//go:build integration

package integration

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dfns/terraform-provider-tunnel/internal/libs"
	"github.com/dfns/terraform-provider-tunnel/internal/runner"
	"github.com/dfns/terraform-provider-tunnel/internal/ssh"
	"github.com/dfns/terraform-provider-tunnel/internal/sshtest"
)

// Helper-process re-exec scaffold.
//
// To exercise the fork lifecycle and the process-lifecycle primitives against
// real OS processes, the test binary re-execs itself before any tests run. It
// recognizes two kinds of re-exec:
//
//   - The production fork: ForkRemoteTunnel runs exec.Command(os.Args[0], ppid)
//     with libs.TunnelTypeEnv set, exactly as the released binary does. TestMain
//     mirrors main.go's dispatch so the child becomes a real tunnel process.
//   - Helper modes (selected by helperModeEnv): generic sleeper/watcher children
//     used to drive the libs process primitives directly.
const (
	helperModeEnv = "TUNNEL_IT_HELPER_MODE"
	watchPIDEnv   = "TUNNEL_IT_WATCH_PID"
	listenPortEnv = "TUNNEL_IT_LISTEN_PORT"

	// Config passed down to the forker helper for the parent-exit teardown test.
	sshKeyEnv        = "TUNNEL_IT_SSH_KEY"
	sshPortEnv       = "TUNNEL_IT_SSH_PORT"
	localPortEnv     = "TUNNEL_IT_LOCAL_PORT"
	targetPortEnv    = "TUNNEL_IT_TARGET_PORT"
	forkerPidfileEnv = "TUNNEL_IT_FORKER_PIDFILE"

	modeSleeper    = "sleeper"
	modeWatcher    = "watcher"
	modeForkParent = "forkparent"
	modeForker     = "forker"
)

func TestMain(m *testing.M) {
	// Production dispatch path: when re-executed by ForkRemoteTunnel, behave like
	// the real provider binary's main() and run the requested tunnel in-process.
	if tun := os.Getenv(libs.TunnelTypeEnv); tun != "" {
		if err := runner.StartTunnel(tun); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	switch os.Getenv(helperModeEnv) {
	case modeSleeper:
		runSleeper()
	case modeForkParent:
		runForkParent()
	case modeForker:
		runForker()
	case modeWatcher:
		runWatcher()
	default:
		os.Exit(m.Run())
	}
}

// runForkParent is the process the forked tunnel watches. It stands in for
// terraform: it spawns the forker child (which forks the tunnel), reaps it, then
// stays alive. Killing this process is what must tear the tunnel down.
func runForkParent() {
	cmd := exec.Command(os.Args[0])
	// Override the inherited helper mode (Go's Getenv returns the first match,
	// so the old value must be removed, not just shadowed) — otherwise this child
	// would re-enter forkparent mode and fork forever.
	cmd.Env = append(envWithout(os.Environ(), helperModeEnv), helperModeEnv+"="+modeForker)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "forkparent: starting forker: %v\n", err)
		os.Exit(10)
	}
	// The forker exits as soon as it has forked the tunnel; reap it so it does
	// not linger as a zombie under us.
	_ = cmd.Wait()
	for {
		time.Sleep(time.Hour)
	}
}

// runForker forks a real SSH tunnel via ssh.ForkRemoteTunnel. Because
// ForkRemoteTunnel passes os.Getppid() to the child, the tunnel ends up watching
// this process's parent (the forkparent helper). It records the tunnel PID for
// cleanup and exits — the tunnel keeps running, watching the forkparent.
func runForker() {
	sshPort, _ := strconv.Atoi(os.Getenv(sshPortEnv))
	localPort, _ := strconv.Atoi(os.Getenv(localPortEnv))
	targetPort, _ := strconv.Atoi(os.Getenv(targetPortEnv))

	cmd, err := ssh.ForkRemoteTunnel(context.Background(), ssh.TunnelConfig{
		LocalPort:  localPort,
		SSHHost:    "127.0.0.1",
		SSHPort:    sshPort,
		SSHUser:    sshtest.User,
		SSHKey:     os.Getenv(sshKeyEnv),
		TargetHost: "127.0.0.1",
		TargetPort: targetPort,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "forker: ForkRemoteTunnel: %v\n", err)
		os.Exit(11)
	}

	if pidfile := os.Getenv(forkerPidfileEnv); pidfile != "" {
		_ = os.WriteFile(pidfile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
	}
	os.Exit(0)
}

// envWithout returns a copy of env with any entry for key removed.
func envWithout(env []string, key string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}

// runSleeper blocks until the process is signaled or killed by its parent. It
// stands in for any long-lived process whose lifecycle the primitives observe.
func runSleeper() {
	for {
		time.Sleep(time.Hour)
	}
}

// runWatcher binds a listener (so the parent test can observe liveness through
// the port) and then calls libs.WatchProcess on the watched PID. When that
// watched process exits, WatchProcess signals this process, which terminates
// and closes the listener — the observable teardown the fork model relies on.
func runWatcher() {
	watchPID, err := strconv.Atoi(os.Getenv(watchPIDEnv))
	if err != nil {
		os.Exit(2)
	}

	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", os.Getenv(listenPortEnv)))
	if err != nil {
		os.Exit(3)
	}

	if err := libs.WatchProcess(watchPID); err != nil {
		os.Exit(4)
	}

	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_ = conn.Close()
	}
}

// spawnHelper re-execs this test binary in the given helper mode with the extra
// environment provided, returning the started command. The helper is killed and
// reaped on test cleanup so no child outlives the test.
func spawnHelper(t *testing.T, mode string, env ...string) *exec.Cmd {
	t.Helper()

	cmd := exec.Command(os.Args[0])
	// Replace (not just append) the helper mode so an inherited value can't win:
	// Go's Getenv returns the first match for a duplicated key.
	cmd.Env = append(envWithout(os.Environ(), helperModeEnv), helperModeEnv+"="+mode)
	cmd.Env = append(cmd.Env, env...)
	// Surface helper output in the test log if something goes wrong.
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting %s helper: %v", mode, err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

// requireEventually polls check until it returns nil or the timeout elapses,
// failing with the last error so root causes surface in test output.
func requireEventually(t *testing.T, timeout time.Duration, check func() error, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		lastErr = check()
		if lastErr == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s: %v", msg, lastErr)
}

// requireConsistently polls check for d and fails immediately if it returns an
// error at any point.
func requireConsistently(t *testing.T, d time.Duration, check func() error, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if err := check(); err != nil {
			t.Fatalf("%s: %v", msg, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// requireStaysListening fails if addr stops accepting connections at any point
// within d. Used to assert a watched child does NOT tear down while its parent
// is still alive (on Windows the Status() stub makes WatchProcess do exactly
// that on its first poll).
func requireStaysListening(t *testing.T, addr string, d time.Duration) {
	t.Helper()
	requireConsistently(t, d, func() error { return checkPortReachable(addr) },
		"listener went away while its watched parent was still alive (premature teardown)")
}

// checkPortReachable verifies a TCP connection to addr succeeds.
func checkPortReachable(addr string) error {
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

func checkPortClosed(addr string) error {
	if err := checkPortReachable(addr); err != nil {
		return nil
	}
	return fmt.Errorf("listener at %s still accepts connections", addr)
}

func checkProcessMissing(pid int) error {
	if err := libs.CheckProcessExists(pid); err != nil {
		return nil
	}
	return fmt.Errorf("process %d still exists", pid)
}
