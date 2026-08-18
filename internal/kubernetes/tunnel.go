package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"

	"github.com/dfns/terraform-provider-tunnel/internal/libs"
	"k8s.io/client-go/kubernetes"
)

var TunnelType string = "kubernetes"

func ForkRemoteTunnel(ctx context.Context, cfg TunnelConfig) (*exec.Cmd, error) {
	logName := fmt.Sprintf("k8s-tunnel-%s-%s-%d.log", cfg.Namespace, cfg.ServiceName, cfg.TargetPort)
	return libs.ForkTunnel(ctx, TunnelType, logName, cfg)
}

func StartRemoteTunnel(ctx context.Context, cfgJSON string, parentPID int) error {
	var cfg TunnelConfig
	if err := json.Unmarshal([]byte(cfgJSON), &cfg); err != nil {
		return err
	}
	if err := libs.WatchProcess(parentPID); err != nil {
		return err
	}
	return runTunnel(ctx, cfg)
}

func runTunnel(ctx context.Context, cfg TunnelConfig) error {
	log.Printf(
		"starting tunnel: %s:%d -> %s/%s:%d",
		cfg.LocalHost, cfg.LocalPort, cfg.Namespace, cfg.ServiceName, cfg.TargetPort,
	)

	clientConfig, err := cfg.restConfig()
	if err != nil {
		return err
	}
	clientSet, err := kubernetes.NewForConfig(clientConfig)
	if err != nil {
		return fmt.Errorf("create k8s client: %w", err)
	}

	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	ep, err := resolveEndpoint(runCtx, clientSet, cfg)
	if err != nil {
		return err
	}
	forward, err := newPortForward(clientConfig, clientSet, cfg, ep)
	if err != nil {
		return err
	}

	defer log.Println("stopping tunnel")
	return forward.run(runCtx, libs.SignalReadyIfRequested)
}
