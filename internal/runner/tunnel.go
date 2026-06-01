package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"

	k8s "github.com/dfns/terraform-provider-tunnel/internal/kubernetes"
	"github.com/dfns/terraform-provider-tunnel/internal/libs"
	"github.com/dfns/terraform-provider-tunnel/internal/ssh"
	"github.com/dfns/terraform-provider-tunnel/internal/ssm"
)

func StartTunnel(tun string) error {
	cfgJson := os.Getenv(libs.TunnelConfEnv)
	if cfgJson == "" {
		return errors.New("missing tunnel configuration")
	}
	if err := os.Unsetenv(libs.TunnelConfEnv); err != nil {
		return err
	}

	if len(os.Args) < 2 {
		return errors.New("missing parent PID")
	}
	parentPid, err := strconv.Atoi(os.Args[1])
	if err != nil {
		return fmt.Errorf("invalid parent PID: %w", err)
	}

	switch tun {
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
