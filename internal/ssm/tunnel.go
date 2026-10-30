package ssm

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
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/dfns/terraform-provider-tunnel/internal/libs"
	"github.com/xtaci/smux"
)

var TunnelType string = "ssm"

func ForkRemoteTunnel(ctx context.Context, awsCfg aws.Config, cfg TunnelConfig) (*exec.Cmd, error) {
	// A snapshot rather than the provider chain: the child's only AWS call,
	// GenerateDataKey for KMS encryption, happens seconds after the fork.
	creds, err := awsCfg.Credentials.Retrieve(ctx)
	if err != nil {
		return nil, fmt.Errorf("retrieve AWS credentials: %w", err)
	}
	cfg.Credentials = &creds

	// The session is started here rather than in the child so that credential
	// resolution stays in the provider process, as the AWS CLI does before
	// handing the response to the plugin, last checked at
	// https://github.com/aws/aws-cli/blob/5ad8dc60682d72edf21be96f0a591402f91ee45e/awscli/customizations/sessionmanager.py
	ssmClient := ssm.NewFromConfig(awsCfg)
	sessionParams, err := startTunnelSession(ctx, ssmClient, cfg)
	if err != nil {
		return nil, err
	}
	cfg.SessionParams = &sessionParams

	logPort := cfg.TargetPort
	if logPort == "" {
		logPort = cfg.LocalPort
	}
	logName := fmt.Sprintf("ssm-tunnel-%s-%s.log", cfg.SSMInstance, logPort)

	cmd, err := libs.ForkTunnel(ctx, TunnelType, logName, cfg)
	if err != nil {
		if terr := terminateTunnelSession(ctx, ssmClient, sessionParams); terr != nil {
			log.Printf("failed to terminate SSM session %s: %v", sessionParams.SessionId, terr)
		}
	}
	return cmd, err
}

func StartRemoteTunnel(ctx context.Context, cfgJson string, parentPid int) error {
	var cfg TunnelConfig
	if err := json.Unmarshal([]byte(cfgJson), &cfg); err != nil {
		return err
	}
	if cfg.SessionParams == nil || cfg.Credentials == nil {
		return errors.New("missing SSM session parameters")
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	// Watch parent process lifecycle ie. main terraform process
	if err := libs.WatchProcess(parentPid); err != nil {
		return err
	}

	localAddr := net.JoinHostPort("localhost", cfg.LocalPort)
	listener, err := net.Listen("tcp", localAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", localAddr, err)
	}
	defer listener.Close()

	kmsClient := kms.NewFromConfig(aws.Config{
		Region:      cfg.SSMRegion,
		Credentials: credentials.StaticCredentialsProvider{Value: *cfg.Credentials},
	})
	dc, agentVersion, err := dial(ctx, *cfg.SessionParams, cfg.SSMInstance, kmsClient)
	if err != nil {
		return err
	}
	defer dc.Close()

	muxCfg, err := muxConfig(agentVersion)
	if err != nil {
		return err
	}
	mux, err := smux.Client(dc, muxCfg)
	if err != nil {
		return err
	}
	defer mux.Close()

	if err := libs.SignalReadyIfRequested(); err != nil {
		return err
	}
	log.Printf("SSM tunnel listening on %s", listener.Addr())

	err = libs.NewConnServer(listener, func(_ context.Context, conn net.Conn) {
		stream, err := mux.OpenStream()
		if err != nil {
			log.Printf("open SSM stream: %v", err)
			return
		}
		// smux refuses writes once the agent closed the stream, so a client
		// still holding its end open is cut off instead of waited on.
		libs.RelayDrain(conn, stream, time.Second)
	}).Serve(dc.Context())
	if err != nil || ctx.Err() != nil {
		return err
	}
	return context.Cause(dc.Context())
}

// muxConfig applies the plugin's agent version gates, which are strict.
func muxConfig(agentVersion string) (*smux.Config, error) {
	if !agentVersionAfter(agentVersion, "3.0.196.0") {
		return nil, fmt.Errorf("SSM agent %s too old for port forwarding, need > 3.0.196.0", agentVersion)
	}
	cfg := smux.DefaultConfig()
	cfg.KeepAliveDisabled = agentVersionAfter(agentVersion, "3.1.1511.0")
	return cfg, nil
}

// agentVersionAfter compares like the plugin, where versions that differ in
// length or are not numeric are never after.
func agentVersionAfter(version, than string) bool {
	a, b := strings.Split(version, "."), strings.Split(than, ".")
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, errX := strconv.Atoi(a[i])
		y, errY := strconv.Atoi(b[i])
		if errX != nil || errY != nil {
			return false
		}
		if x != y {
			return x > y
		}
	}
	return false
}
