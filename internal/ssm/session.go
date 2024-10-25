package ssm

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

const DEFAULT_SSM_ENV_NAME = "AWS_SSM_START_SESSION_RESPONSE"

type TunnelConfig struct {
	SSMRegion   string
	SSMInstance string
	TargetHost  string
	TargetPort  string
	LocalPort   string
}

func CreateSessionInput(cfg TunnelConfig) ssm.StartSessionInput {
	reqParams := make(map[string][]string)
	reqParams["portNumber"] = []string{cfg.TargetPort}
	reqParams["localPortNumber"] = []string{cfg.LocalPort}
	reqParams["host"] = []string{cfg.TargetHost}

	return ssm.StartSessionInput{
		Target:       aws.String(cfg.SSMInstance),
		DocumentName: aws.String("AWS-StartPortForwardingSessionToRemoteHost"),
		Parameters:   reqParams,
	}
}

func StartTunnelSession(ctx context.Context, cfg TunnelConfig) (map[string]string, error) {
	// Load AWS SDK config
	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	awsCfg.Region = cfg.SSMRegion

	// Create SSM client
	ssmClient := ssm.NewFromConfig(awsCfg)

	// Make a request to start a session
	sessionInput := CreateSessionInput(cfg)
	sessionResponse, err := ssmClient.StartSession(ctx, &sessionInput)
	if err != nil {
		return nil, err
	}

	resParams := make(map[string]string)
	resParams["SessionId"] = *sessionResponse.SessionId
	resParams["TokenValue"] = *sessionResponse.TokenValue
	resParams["StreamUrl"] = *sessionResponse.StreamUrl

	return resParams, nil
}
