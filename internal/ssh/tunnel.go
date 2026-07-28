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
	"strconv"
	"strings"

	"github.com/dfns/terraform-provider-tunnel/internal/libs"
	"golang.org/x/crypto/ssh"
)

var TunnelType string = "ssh"

type TunnelConfig struct {
	LocalHost        string
	LocalPort        int
	SSHHost          string
	SSHKey           string
	SSHKeyPassphrase string
	SSHPassword      string
	SSHPort          int
	SSHUser          string
	TargetHost       string
	TargetPort       int
	TargetSocket     string
}

func ForkRemoteTunnel(ctx context.Context, cfg TunnelConfig) (*exec.Cmd, error) {
	target := strconv.Itoa(cfg.TargetPort)
	if cfg.TargetSocket != "" {
		target = strings.ReplaceAll(cfg.TargetSocket, string(os.PathSeparator), "_")
	}
	logName := fmt.Sprintf("ssh-tunnel-%s-%s.log", cfg.SSHHost, target)
	return libs.ForkTunnel(ctx, TunnelType, logName, cfg)
}

func StartRemoteTunnel(ctx context.Context, cfgJson string, parentPid int) error {
	var cfg TunnelConfig
	if err := json.Unmarshal([]byte(cfgJson), &cfg); err != nil {
		return err
	}

	if err := libs.WatchProcess(parentPid); err != nil {
		return err
	}

	return runTunnel(ctx, cfg)
}

func runTunnel(ctx context.Context, cfg TunnelConfig) error {
	clientCfg, err := clientConfig(cfg)
	if err != nil {
		return err
	}

	sshPort := cfg.SSHPort
	if sshPort == 0 {
		sshPort = defaultSSHPort
	}
	localHost := cfg.LocalHost
	if localHost == "" {
		localHost = "localhost"
	}
	sshAddr := net.JoinHostPort(cfg.SSHHost, strconv.Itoa(sshPort))
	localAddr := net.JoinHostPort(localHost, strconv.Itoa(cfg.LocalPort))
	network, target := targetEndpoint(cfg)
	log.Printf("starting tunnel: %s - %s - %s", localAddr, sshAddr, target)

	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	clients := &clientPool{dial: func(ctx context.Context) (*ssh.Client, error) {
		return dialSSH(ctx, sshAddr, clientCfg)
	}}
	defer clients.close()

	// Authenticate before reporting readiness.
	if _, err := clients.get(runCtx); err != nil {
		return fmt.Errorf("connect to SSH bastion %s: %w", sshAddr, err)
	}
	log.Printf("connected to %s", sshAddr)

	listener, err := net.Listen("tcp", localAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", localAddr, err)
	}
	fwd := newForwarder(listener, clients, network, target)
	defer fwd.Close()

	if err := libs.SignalReadyIfRequested(); err != nil {
		return err
	}
	log.Printf("SSH tunnel listening on %s", listener.Addr())
	defer log.Println("stopping tunnel")

	return fwd.Serve(runCtx)
}

func targetEndpoint(cfg TunnelConfig) (network, address string) {
	if cfg.TargetSocket != "" {
		return "unix", cfg.TargetSocket
	}
	return "tcp", net.JoinHostPort(cfg.TargetHost, strconv.Itoa(cfg.TargetPort))
}
