package libs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// ForkTunnel starts a background tunnel and waits for its listener to be ready.
func ForkTunnel(ctx context.Context, tunnelType, logName string, cfg any) (*exec.Cmd, error) {
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}

	logPath := TunnelLogPath(logName)
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	defer logFile.Close()

	// A per-fork marker prevents concurrent tunnels acknowledging each other.
	readyDir, err := os.MkdirTemp("", "terraform-provider-tunnel-ready-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(readyDir)
	readyPath := filepath.Join(readyDir, "ready")

	cmd := exec.Command(os.Args[0], strconv.Itoa(os.Getppid()))
	cmd.Env = append(
		os.Environ(),
		fmt.Sprintf("%s=%s", TunnelTypeEnv, tunnelType),
		fmt.Sprintf("%s=%s", TunnelConfEnv, string(cfgJSON)),
		fmt.Sprintf("%s=%s", TunnelReadyEnv, readyPath),
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if err := WaitForReadyFile(ctx, cmd.Process.Pid, readyPath); err != nil {
		// Failed startup must not leave a child that can bind the port later.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("%w. check %s for more information", err, logPath)
	}
	return cmd, nil
}
