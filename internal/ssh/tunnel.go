package ssh

import (
	"context"
	"encoding/json"
	"errors"
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

	// Append ssh tunnel config environment variable to pass parameters to the child process
	cmd.Env = append(
		os.Environ(),
		fmt.Sprintf("%s=%s", libs.TunnelTypeEnv, TunnelType),
		fmt.Sprintf("%s=%s", libs.TunnelConfEnv, string(tunnelCfgJson)),
	)

	// Redirect stdout and stderr to log file
	cmd.Stdout = tunnelLogFile
	cmd.Stderr = tunnelLogFile

	// Run the command in the background
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	time.Sleep(5 * time.Second)

	if err = libs.CheckProcessExists(cmd.Process.Pid); err != nil {
		return nil, fmt.Errorf("tunnel process failed to start. check %s for more information", tunnelLogPath)
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

	sshTun.SetTunneledConnState(func(tun *sshtun.SSHTun, state *sshtun.TunneledConnState) {
		log.Printf("tunnel state: %+v", state)
	})

	sshTun.SetConnState(func(tun *sshtun.SSHTun, state sshtun.ConnState) {
		if state != sshtun.StateStarted {
			return
		}
		// Check if the tunnel works
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("localhost", strconv.Itoa(cfg.LocalPort)), 2*time.Second)
		if err != nil {
			log.Fatal(err)
		}
		if conn == nil {
			log.Fatal(errors.New("failed to connect to tunnel"))
		}
		_ = conn.Close()
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
