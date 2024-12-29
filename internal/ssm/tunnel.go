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

	"github.com/aws/aws-sdk-go-v2/service/ssm"
	pluginSession "github.com/aws/session-manager-plugin/src/sessionmanagerplugin/session"
	_ "github.com/aws/session-manager-plugin/src/sessionmanagerplugin/session/portsession"
	"github.com/aws/smithy-go/ptr"
	ps "github.com/shirou/gopsutil/v4/process"
)

type ForkRemoteResult struct {
	Command *exec.Cmd
	Session SessionParams
}

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

func WatchProcess(pid string) (err error) {
	pidInt, err := strconv.Atoi(pid)
	if err != nil {
		return fmt.Errorf("invalid PID: %v", err)
	}
	parent, err := ps.NewProcess(int32(pidInt))
	if err != nil {
		return err
	}
	child, err := ps.NewProcess(int32(os.Getpid()))
	if err != nil {
		return err
	}
	// pool for parent process liveliness every 2 seconds
	go func() {
		for {
			_, err := parent.Status()
			if err != nil {
				fmt.Printf("parent process exited: %v\n", err)
				if err := child.Terminate(); err != nil {
					fmt.Printf("failed to terminate process: %v\n", err)
				}
			}
			time.Sleep(2 * time.Second)
		}
	}()

	return nil
}

func ForkRemoteTunnel(ctx context.Context, cfg TunnelConfig) (*ForkRemoteResult, error) {
	// First we start a session using AWS SDK
	// see https://github.com/aws/aws-cli/blob/master/awscli/customizations/sessionmanager.py#L104
	sessionParams, err := StartTunnelSession(ctx, cfg)
	if err != nil {
		return nil, err
	}
	sessionParamsJson, err := json.Marshal(sessionParams)
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
	cmd := exec.Command(os.Args[0], cfg.SSMRegion, cfg.SSMInstance, cfg.TargetHost, cfg.TargetPort, cfg.LocalPort, strconv.Itoa(os.Getppid()))

	// Append special environment variable to pass session parameters to the child process
	// see https://github.com/aws/aws-cli/blob/master/awscli/customizations/sessionmanager.py#L140
	cmd.Env = append(os.Environ(), fmt.Sprintf("%s=%s", DEFAULT_SSM_ENV_NAME, string(sessionParamsJson)))

	// Redirect stdout and stderr to log file
	cmd.Stdout = tunnelLogFile
	cmd.Stderr = tunnelLogFile

	// Run the command in the background
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	time.Sleep(5 * time.Second)
	if cmd.ProcessState.ExitCode() != -1 {
		return nil, err
	}

	return &ForkRemoteResult{
		Command: cmd,
		Session: sessionParams,
	}, nil
}

func StartRemoteTunnel(ctx context.Context, cfg TunnelConfig, parentPid string) (err error) {
	// Watch parent process lifecycle ie. main terraform process
	err = WatchProcess(parentPid)
	if err != nil {
		return err
	}

	sessionInput := CreateSessionInput(cfg)
	sessionInputJson, err := json.Marshal(sessionInput)
	if err != nil {
		return err
	}

	profileName := ""
	endpointUrl, err := GetEndpoint(ctx, cfg.SSMRegion)
	if err != nil {
		return err
	}

	args := []string{
		"session-manager-plugin",
		DEFAULT_SSM_ENV_NAME,
		cfg.SSMRegion,
		"StartSession",
		profileName,
		string(sessionInputJson),
		endpointUrl,
	}

	// call session-manager-plugin to start the tunnel
	pluginSession.ValidateInputAndStartSession(args, os.Stdout)

	return
}
