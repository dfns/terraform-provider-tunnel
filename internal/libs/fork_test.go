package libs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const forkTestTunnelType = "libs-fork-test"

type forkTestConfig struct {
	PIDPath       string
	ReadyPathPath string
	EnvPath       string
	SignalReady   bool
}

func runForkTestChild() {
	var cfg forkTestConfig
	if err := json.NewDecoder(os.Stdin).Decode(&cfg); err != nil {
		os.Exit(2)
	}
	if cfg.PIDPath != "" {
		if err := os.WriteFile(cfg.PIDPath, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			os.Exit(2)
		}
	}
	if cfg.ReadyPathPath != "" {
		if err := os.WriteFile(cfg.ReadyPathPath, []byte(os.Getenv(TunnelReadyEnv)), 0600); err != nil {
			os.Exit(2)
		}
	}
	if cfg.EnvPath != "" {
		if err := os.WriteFile(cfg.EnvPath, []byte(strings.Join(os.Environ(), "\n")), 0600); err != nil {
			os.Exit(2)
		}
	}
	if cfg.SignalReady {
		if err := SignalReadyIfRequested(); err != nil {
			os.Exit(2)
		}
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestMain(m *testing.M) {
	if os.Getenv(TunnelTypeEnv) == forkTestTunnelType {
		runForkTestChild()
		return
	}
	os.Exit(m.Run())
}

func TestForkTunnelUsesInvocationUniqueReadinessPaths(t *testing.T) {
	t.Setenv(TunnelLogDirEnv, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	type result struct {
		cmd *exec.Cmd
		err error
	}
	results := make(chan result, 2)
	cfgs := make([]forkTestConfig, 2)
	for i := range cfgs {
		dir := t.TempDir()
		cfgs[i] = forkTestConfig{
			ReadyPathPath: filepath.Join(dir, "ready-path"),
			EnvPath:       filepath.Join(dir, "env"),
			SignalReady:   true,
		}
	}
	for i, cfg := range cfgs {
		go func() {
			cmd, err := ForkTunnel(
				ctx,
				forkTestTunnelType,
				fmt.Sprintf("fork-%d.log", i),
				cfg,
			)
			results <- result{cmd: cmd, err: err}
		}()
	}

	for range cfgs {
		got := <-results
		if got.cmd != nil {
			t.Cleanup(func() {
				_ = got.cmd.Process.Kill()
				_ = got.cmd.Wait()
			})
		}
		if got.err != nil {
			t.Errorf("ForkTunnel() error = %v", got.err)
		}
	}
	if t.Failed() {
		return
	}

	// The child acting on its config proves it arrived over stdin.
	first, err := os.ReadFile(cfgs[0].ReadyPathPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(cfgs[1].ReadyPathPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) == string(second) {
		t.Fatalf("concurrent forks shared readiness path %q", first)
	}

	for _, cfg := range cfgs {
		env, err := os.ReadFile(cfg.EnvPath)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(env), cfg.EnvPath) {
			t.Fatal("tunnel config leaked into the child environment")
		}
	}
}

func TestForkTunnelCancellationKillsAndReapsChild(t *testing.T) {
	t.Setenv(TunnelLogDirEnv, t.TempDir())
	pidPath := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	childStarted := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(pidPath); err == nil {
				cancel()
				childStarted <- nil
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		childStarted <- errors.New("child did not write its PID")
	}()

	_, err := ForkTunnel(
		ctx,
		forkTestTunnelType,
		"cancelled-fork.log",
		forkTestConfig{PIDPath: pidPath},
	)
	if startErr := <-childStarted; startErr != nil {
		t.Fatal(startErr)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ForkTunnel() error = %v, want context canceled", err)
	}

	pidBytes, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(pidBytes))
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckProcessExists(pid); err == nil {
		t.Fatalf("cancelled tunnel child %d is still running", pid)
	}
}
