package ssm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ssm"
	pluginSession "github.com/aws/session-manager-plugin/src/sessionmanagerplugin/session"
	_ "github.com/aws/session-manager-plugin/src/sessionmanagerplugin/session/portsession"
	"github.com/aws/smithy-go/ptr"
	ps "github.com/shirou/gopsutil/v4/process"
)

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
