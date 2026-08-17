//go:build integration

package integration

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/dfns/terraform-provider-tunnel/internal/libs"
)

// TestWatchProcessTerminatesOnParentExit is the heart of the fork model: a
// forked tunnel watches the terraform process and must tear itself down once
// that parent goes away. Here a watcher helper monitors a separate "parent"
// sleeper; killing the parent must make the watcher self-terminate, which we
// observe through its listening port going dead.
func TestWatchProcessTerminatesOnParentExit(t *testing.T) {
	parent := spawnHelper(t, modeSleeper)

	port, err := libs.GetFreePort()
	if err != nil {
		t.Fatalf("allocating free port: %v", err)
	}

	spawnHelper(t, modeWatcher,
		watchPIDEnv+"="+strconv.Itoa(parent.Process.Pid),
		listenPortEnv+"="+strconv.Itoa(port),
	)

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	requireEventually(t, 10*time.Second, func() error { return checkPortReachable(addr) },
		"watcher never started listening")

	// Control: while the watched parent is alive, the watcher must KEEP
	// listening. On Windows the Status() stub makes WatchProcess see a phantom
	// "parent exited" on its first poll and tear the watcher down within
	// milliseconds — this turns that into a deterministic failure instead of a
	// false pass at the teardown assertion below.
	requireStaysListening(t, addr, 3*time.Second)

	// Kill and reap the watched parent so it disappears from the OS.
	if err := parent.Process.Kill(); err != nil {
		t.Fatalf("killing parent: %v", err)
	}
	_ = parent.Wait()

	// WatchProcess polls every 2s; the watcher must notice and self-terminate.
	requireEventually(t, 20*time.Second, func() error { return checkPortClosed(addr) },
		"watcher still listening after its watched parent exited; it did not self-terminate")
}

// TestCheckProcessExists confirms a live child is reported as existing and a
// killed one as gone.
func TestCheckProcessExists(t *testing.T) {
	sleeper := spawnHelper(t, modeSleeper)
	pid := sleeper.Process.Pid

	requireEventually(t, 5*time.Second, func() error { return libs.CheckProcessExists(pid) },
		"live process reported as not existing")

	if err := sleeper.Process.Kill(); err != nil {
		t.Fatalf("killing sleeper: %v", err)
	}
	_ = sleeper.Wait()

	requireEventually(t, 5*time.Second, func() error { return checkProcessMissing(pid) },
		"process still reported alive after it was killed")
}

// TestInterruptTerminatesProcess confirms Interrupt actually stops the target
// process (the Close path the provider uses to tear a tunnel down).
func TestInterruptTerminatesProcess(t *testing.T) {
	sleeper := spawnHelper(t, modeSleeper)
	pid := sleeper.Process.Pid

	requireEventually(t, 5*time.Second, func() error { return libs.CheckProcessExists(pid) },
		"sleeper never became live")

	if err := libs.Interrupt(pid); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	_ = sleeper.Wait() // reap after the interrupt terminates it

	requireEventually(t, 5*time.Second, func() error { return checkProcessMissing(pid) },
		"process still alive after Interrupt")
}

// TestWaitForReadyFile covers the readiness handshake every tunnel fork uses:
// the child SignalReady writes the sentinel file and WaitForReadyFile returns
// once it appears, while a dead process aborts the wait promptly.
func TestWaitForReadyFile(t *testing.T) {
	t.Run("returns once the ready file exists", func(t *testing.T) {
		sleeper := spawnHelper(t, modeSleeper)

		path := filepath.Join(t.TempDir(), "ready")
		if err := libs.SignalReady(path); err != nil {
			t.Fatalf("SignalReady: %v", err)
		}
		if data, err := os.ReadFile(path); err != nil || string(data) != "ready" {
			t.Fatalf("SignalReady wrote %q (err %v), want \"ready\"", data, err)
		}

		if err := libs.WaitForReadyFile(context.Background(), sleeper.Process.Pid, path); err != nil {
			t.Fatalf("WaitForReadyFile: %v", err)
		}
	})

	t.Run("fails fast when the process has exited", func(t *testing.T) {
		sleeper := spawnHelper(t, modeSleeper)
		pid := sleeper.Process.Pid
		_ = sleeper.Process.Kill()
		_ = sleeper.Wait()

		path := filepath.Join(t.TempDir(), "ready") // never created

		start := time.Now()
		err := libs.WaitForReadyFile(context.Background(), pid, path)
		if err == nil {
			t.Fatal("expected an error once the process exited")
		}
		if elapsed := time.Since(start); elapsed > 10*time.Second {
			t.Fatalf("WaitForReadyFile did not fail fast: took %s", elapsed)
		}
	})
}
