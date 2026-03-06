package ssh

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dfns/terraform-provider-tunnel/internal/libs"
	"github.com/rgzr/sshtun"
)

var TunnelType string = "ssh"

type TunnelConfig struct {
	LocalPort        int
	SSHHost          string
	SSHKey           string
	SSHKeyPassphrase string
	SSHPassword      string
	SSHPort          int
	SSHUser          string
	TargetHost       string
	TargetPort       int
}

func ForkRemoteTunnel(ctx context.Context, cfg TunnelConfig) (*exec.Cmd, error) {
	tunnelCfgJson, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}

	// Open a log file for the tunnel
	tunnelLogPath := filepath.Join(os.TempDir(), fmt.Sprintf("ssh-tunnel-%s-%d.log", cfg.SSHHost, cfg.TargetPort))
	tunnelLogFile, err := os.OpenFile(tunnelLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}

	// Prepare the command
	cmd := exec.Command(os.Args[0], strconv.Itoa(os.Getppid()))

	// Create a temp file path for the ready signal (use PID for uniqueness)
	readyFilePath := filepath.Join(os.TempDir(), fmt.Sprintf("ssh-tunnel-ready-%d", os.Getpid()))

	// Append ssh tunnel config environment variable to pass parameters to the child process
	cmd.Env = append(
		os.Environ(),
		fmt.Sprintf("%s=%s", libs.TunnelTypeEnv, TunnelType),
		fmt.Sprintf("%s=%s", libs.TunnelConfEnv, string(tunnelCfgJson)),
		fmt.Sprintf("%s=%s", libs.TunnelReadyEnv, readyFilePath),
	)

	// Redirect stdout and stderr to log file
	cmd.Stdout = tunnelLogFile
	cmd.Stderr = tunnelLogFile

	// Run the command in the background
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	if err = libs.WaitForReadyFile(cmd.Process.Pid, readyFilePath); err != nil {
		return nil, fmt.Errorf("%w. check %s for more information", err, tunnelLogPath)
	}

	return cmd, nil
}

func StartRemoteTunnel(ctx context.Context, cfgJson string, parentPid int) (err error) {
	var cfg TunnelConfig
	if err := json.Unmarshal([]byte(cfgJson), &cfg); err != nil {
		return err
	}

	// Watch parent process lifecycle ie. main terraform process
	err = libs.WatchProcess(parentPid)
	if err != nil {
		return err
	}

	log.Printf("starting tunnel: localhost:%d - %s:%d - %s:%d", cfg.LocalPort, cfg.SSHHost, cfg.SSHPort, cfg.TargetHost, cfg.TargetPort)

	sshTun := sshtun.New(cfg.LocalPort, cfg.SSHHost, cfg.TargetPort)
	sshTun.SetPort(cfg.SSHPort)
	sshTun.SetUser(cfg.SSHUser)
	sshTun.SetRemoteHost(cfg.TargetHost)

	if cfg.SSHPassword != "" {
		sshTun.SetPassword(cfg.SSHPassword)
	}

	if cfg.SSHKey != "" {
		if _, err := os.Stat(cfg.SSHKey); err == nil {
			if cfg.SSHKeyPassphrase != "" {
				sshTun.SetEncryptedKeyFile(cfg.SSHKey, cfg.SSHKeyPassphrase)
			} else {
				sshTun.SetKeyFile(cfg.SSHKey)
			}
		} else {
			if cfg.SSHKeyPassphrase != "" {
				sshTun.SetEncryptedKeyReader(strings.NewReader(cfg.SSHKey), cfg.SSHKeyPassphrase)
			} else {
				sshTun.SetKeyReader(strings.NewReader(cfg.SSHKey))
			}
		}
	}

	// Channel to signal when the tunneled connection is fully established
	// (SSH handshake complete + remote endpoint connected)
	tunnelReady := make(chan struct{}, 1)

	sshTun.SetTunneledConnState(func(tun *sshtun.SSHTun, state *sshtun.TunneledConnState) {
		log.Printf("tunneled conn state: %+v", state)
		if state.Ready {
			select {
			case tunnelReady <- struct{}{}:
			default:
			}
		}
	})

	sshTun.SetConnState(func(tun *sshtun.SSHTun, state sshtun.ConnState) {
		switch state {
		case sshtun.StateStarting:
			log.Println("tunnel connecting...")
		case sshtun.StateStarted:
			log.Println("tunnel listener ready, probing connection...")
			// Probe the tunnel in a goroutine to trigger the SSH handshake.
			// The local listener accepts immediately, but ssh.Dial happens lazily
			// when the first connection is handled. We wait for TunneledConnState
			// with Ready=true to know the full tunnel (SSH + remote) is established.
			go func() {
				conn, err := net.DialTimeout("tcp", net.JoinHostPort("localhost", strconv.Itoa(cfg.LocalPort)), 30*time.Second)
				if err != nil {
					log.Printf("tunnel probe dial failed: %v", err)
					return
				}
				// Wait for the SSH handshake and remote connection to complete
				<-tunnelReady
				log.Println("tunnel connected")
				if readyPath := os.Getenv(libs.TunnelReadyEnv); readyPath != "" {
					if err := libs.SignalReady(readyPath); err != nil {
						log.Printf("failed to signal readiness: %v", err)
					}
				}
				// Keep probe connection alive to maintain the SSH session,
				// preventing re-authentication for subsequent connections.
				// Cleaned up when the tunnel process exits.
				_ = conn
			}()
		case sshtun.StateStopped:
			log.Println("tunnel stopped")
		}
	})

	// Handle interrupt signal
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		<-c
		log.Println("stopping tunnel: received interrupt signal")
		sshTun.Stop()
	}()

	if err = sshTun.Start(ctx); err != nil {
		log.Printf("tunnel error: %v", err)
	}

	return
}
