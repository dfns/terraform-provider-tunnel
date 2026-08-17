package ssm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"

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
	// The session is started here rather than in the child so that credential
	// resolution stays in the provider process, as the AWS CLI does before
	// handing the response to the plugin, last checked at
	// https://github.com/aws/aws-cli/blob/5ad8dc60682d72edf21be96f0a591402f91ee45e/awscli/customizations/sessionmanager.py
	sessionParams, err := StartTunnelSession(ctx, awsCfg, cfg)
	if err != nil {
		return nil, err
	}
	cfg.SessionParams = &sessionParams

	logPort := cfg.TargetPort
	if logPort == "" {
		logPort = cfg.LocalPort
	}
	logName := fmt.Sprintf("ssm-tunnel-%s-%s.log", cfg.SSMInstance, logPort)

	return libs.ForkTunnel(ctx, TunnelType, logName, cfg)
}

func StartRemoteTunnel(ctx context.Context, cfgJson string, parentPid int) (err error) {
	var cfg TunnelConfig
	if err := json.Unmarshal([]byte(cfgJson), &cfg); err != nil {
		return err
	}
	if cfg.SessionParams == nil {
		return errors.New("missing SSM session parameters")
	}

	// Watch parent process lifecycle ie. main terraform process
	err = libs.WatchProcess(parentPid)
	if err != nil {
		return err
	}

	sessionParamsJson, err := json.Marshal(cfg.SessionParams)
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

	// Positional layout copied from the AWS CLI, last checked at
	// https://github.com/aws/aws-cli/blob/5ad8dc60682d72edf21be96f0a591402f91ee45e/awscli/customizations/sessionmanager.py
	// Newer CLIs hand the plugin an env var name here instead, but the vendored
	// plugin only reads the response itself from this argument.
	args := []string{
		"session-manager-plugin",
		string(sessionParamsJson),
		cfg.SSMRegion,
		"StartSession",
		cfg.SSMProfile,
		string(sessionInputJson),
		endpointUrl,
	}

	// The plugin blocks for the tunnel's lifetime and exposes no readiness hook,
	// so readiness is reported from the outside once it binds the local port.
	go func() {
		if err := libs.SignalReadyWhenServing(ctx, "localhost", cfg.LocalPort); err != nil {
			log.Printf("failed to signal tunnel readiness: %v", err)
		}
	}()

	// call session-manager-plugin to start the tunnel
	pluginSession.ValidateInputAndStartSession(args, os.Stdout)

	return
}
