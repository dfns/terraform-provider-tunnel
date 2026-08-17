package libs

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"runtime"
	"syscall"
	"time"

	ps "github.com/shirou/gopsutil/v4/process"
)

func WatchProcess(pid int) (err error) {
	parent, err := ps.NewProcess(int32(pid))
	if err != nil {
		return err
	}
	child, err := ps.NewProcess(int32(os.Getpid()))
	if err != nil {
		return err
	}
	go func() {
		for {
			running, err := parent.IsRunning()
			if err != nil {
				log.Printf("failed to check parent process: %v\n", err)
			} else if !running {
				log.Println("parent process exited")
				err := terminate(child)
				if err != nil {
					log.Printf("failed to terminate process: %v\n", err)
				}
				return
			}
			time.Sleep(2 * time.Second)
		}
	}()

	return nil
}

func CheckProcessExists(pid int) error {
	cmd, err := ps.NewProcess(int32(pid))
	if err != nil {
		return err
	}
	if stats, err := cmd.Status(); err == nil {
		for _, status := range stats {
			if status == ps.Zombie {
				return fmt.Errorf("process died")
			}
		}
		return nil
	}
	running, err := cmd.IsRunning()
	if err != nil {
		return err
	}
	if !running {
		return fmt.Errorf("process died")
	}

	return nil
}

func Interrupt(pid int) error {
	cmd, err := ps.NewProcess(int32(pid))
	if err != nil {
		return err
	}
	return terminate(cmd)
}

func terminate(proc *ps.Process) error {
	if runtime.GOOS == "windows" {
		return proc.Terminate()
	}
	return proc.SendSignal(syscall.SIGINT)
}

// SignalReadyWhenServing signals readiness once the local port accepts
// connections. Tunnels driven by a client library that blocks for the tunnel's
// lifetime cannot signal inline, so they observe their own listener instead.
func SignalReadyWhenServing(ctx context.Context, host string, port string) error {
	if err := waitForLocalPort(ctx, host, port); err != nil {
		return err
	}
	return SignalReadyIfRequested()
}

func waitForLocalPort(ctx context.Context, host string, port string) error {
	timeout := 30 * time.Second
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	addr := net.JoinHostPort(host, port)

	for {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		select {
		case <-waitCtx.Done():
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("wait for tunnel port: %w", err)
			}
			return fmt.Errorf("port %s not accepting connections after %s", port, timeout)
		case <-ticker.C:
		}
	}
}

func SignalReady(path string) error {
	return os.WriteFile(path, []byte("ready"), 0644)
}

// SignalReadyIfRequested is a no-op outside a forked tunnel.
func SignalReadyIfRequested() error {
	path := os.Getenv(TunnelReadyEnv)
	if path == "" {
		return nil
	}
	if err := SignalReady(path); err != nil {
		return fmt.Errorf("signal tunnel readiness: %w", err)
	}
	return nil
}

func WaitForReadyFile(ctx context.Context, pid int, path string) error {
	timeout := 30 * time.Second
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		if err := CheckProcessExists(pid); err != nil {
			return fmt.Errorf("process exited unexpectedly: %w", err)
		}
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("check tunnel readiness: %w", err)
		}
		select {
		case <-waitCtx.Done():
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("wait for tunnel readiness: %w", err)
			}
			return fmt.Errorf("tunnel not ready after %s", timeout)
		case <-ticker.C:
		}
	}
}
