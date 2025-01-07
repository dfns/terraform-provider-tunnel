package ssm

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

const DEFAULT_SSM_ENV_NAME = "AWS_SSM_START_SESSION_RESPONSE"

type TunnelConfig struct {
	LocalPort   string
	SSMInstance string
	SSMProfile  string
	SSMRegion   string
	TargetHost  string
	TargetPort  string
}

type SessionParams struct {
	SessionId  string
	TokenValue string
	StreamUrl  string
}

func GetNewSDKConfig(ctx context.Context, cfg TunnelConfig) (aws.Config, error) {
	loadOptions := []func(*config.LoadOptions) error{}
	if cfg.SSMRegion != "" {
		loadOptions = append(loadOptions, config.WithRegion(cfg.SSMRegion))
	}
	if cfg.SSMProfile != "" {
		loadOptions = append(loadOptions, config.WithSharedConfigProfile(cfg.SSMProfile))
	}

	return config.LoadDefaultConfig(ctx, loadOptions...)
}

func GetSDKConfigProfile(awsCfg aws.Config) string {
	for _, cfg := range awsCfg.ConfigSources {
		if p, ok := cfg.(config.SharedConfig); ok {
			return p.Profile
		}
	}
	return ""
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

func StartTunnelSession(ctx context.Context, awsCfg aws.Config, cfg TunnelConfig) (SessionParams, error) {
	// Create SSM client
	ssmClient := ssm.NewFromConfig(awsCfg)

	// Make a request to start a session
	sessionInput := CreateSessionInput(cfg)
	sessionResponse, err := ssmClient.StartSession(ctx, &sessionInput)
	if err != nil {
		return SessionParams{}, err
	}

	return SessionParams{
		SessionId:  *sessionResponse.SessionId,
		TokenValue: *sessionResponse.TokenValue,
		StreamUrl:  *sessionResponse.StreamUrl,
	}, nil
}
