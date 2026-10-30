package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	azb "github.com/dfns/terraform-provider-tunnel/internal/azurebastion"
	k8s "github.com/dfns/terraform-provider-tunnel/internal/kubernetes"
	"github.com/dfns/terraform-provider-tunnel/internal/ssh"
	"github.com/dfns/terraform-provider-tunnel/internal/ssm"
)

func StartTunnel(tun string, conf io.Reader) error {
	cfg, err := io.ReadAll(conf)
	if err != nil {
		return fmt.Errorf("read tunnel configuration: %w", err)
	}
	if len(cfg) == 0 {
		return errors.New("missing tunnel configuration")
	}
	cfgJson := string(cfg)

	if len(os.Args) < 2 {
		return errors.New("missing parent PID")
	}
	parentPid, err := strconv.Atoi(os.Args[1])
	if err != nil {
		return fmt.Errorf("invalid parent PID: %w", err)
	}

	switch tun {
	case azb.TunnelType:
		return azb.StartRemoteTunnel(context.Background(), cfgJson, parentPid)
	case ssh.TunnelType:
		return ssh.StartRemoteTunnel(context.Background(), cfgJson, parentPid)
	case ssm.TunnelType:
		return ssm.StartRemoteTunnel(context.Background(), cfgJson, parentPid)
	case k8s.TunnelType:
		return k8s.StartRemoteTunnel(context.Background(), cfgJson, parentPid)
	default:
		return errors.New("unknown tunnel type")
	}
}
