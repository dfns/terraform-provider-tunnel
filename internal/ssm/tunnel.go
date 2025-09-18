package ssm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	pluginSession "github.com/aws/session-manager-plugin/src/sessionmanagerplugin/session"
	_ "github.com/aws/session-manager-plugin/src/sessionmanagerplugin/session/portsession"
	"github.com/aws/smithy-go/ptr"
	"github.com/dfns/terraform-provider-tunnel/internal/libs"
)

var TunnelType string = "ssm"

func GetEndpoint(ctx context.Context, region string) (string, error) {
	resolver := ssm.NewDefaultEndpointResolverV2()
	endpoint, err := resolver.ResolveEndpoint(ctx, ssm.EndpointParameters{
		Region: ptr.String(region),
	})
	if err != nil {
		return "", err
	}
	return endpoint.URI.String(), nil
}

func ForkRemoteTunnel(ctx context.Context, awsCfg aws.Config, cfg TunnelConfig) (*exec.Cmd, error) {
	// First we start a session using AWS SDK
	// see https://github.com/aws/aws-cli/blob/master/awscli/customizations/sessionmanager.py#L104
	sessionParams, err := StartTunnelSession(ctx, awsCfg, cfg)
	if err != nil {
		return nil, err
	}
	sessionParamsJson, err := json.Marshal(sessionParams)
	if err != nil {
		return nil, err
	}

	tunnelCfgJson, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}

	// Open a log file for the tunnel
	tunnelLogPath := filepath.Join(os.TempDir(), fmt.Sprintf("ssm-tunnel-%s-%s.log", cfg.SSMInstance, cfg.TargetPort))
	tunnelLogFile, err := os.OpenFile(tunnelLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}

	// Prepare the command
	cmd := exec.Command(os.Args[0], strconv.Itoa(os.Getppid()))

	// Append special environment variable to pass session parameters to the child process
	// see https://github.com/aws/aws-cli/blob/master/awscli/customizations/sessionmanager.py#L140
	cmd.Env = append(
		os.Environ(),
		fmt.Sprintf("%s=%s", libs.TunnelTypeEnv, TunnelType),
		fmt.Sprintf("%s=%s", libs.TunnelConfEnv, string(tunnelCfgJson)),
		fmt.Sprintf("%s=%s", DEFAULT_SSM_ENV_NAME, string(sessionParamsJson)),
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

	sessionInput := CreateSessionInput(cfg)
	sessionInputJson, err := json.Marshal(sessionInput)
	if err != nil {
		return err
	}

	endpointUrl, err := GetEndpoint(ctx, cfg.SSMRegion)
	if err != nil {
		return err
	}

	args := []string{
		"session-manager-plugin",
		os.Getenv(DEFAULT_SSM_ENV_NAME),
		cfg.SSMRegion,
		"StartSession",
		cfg.SSMProfile,
		string(sessionInputJson),
		endpointUrl,
	}

	// call session-manager-plugin to start the tunnel
	pluginSession.ValidateInputAndStartSession(args, os.Stdout)

	return
}
