package ssm

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

const DEFAULT_SSM_ENV_NAME = "AWS_SSM_START_SESSION_RESPONSE"

type TunnelConfig struct {
	LocalPort   string
	SSMInstance string
	SSMProfile  string
	SSMRoleARN  string
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

	// Load base config first
	awsCfg, err := config.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		return aws.Config{}, err
	}

	// If role assumption is required, create STS client and configure assume role
	if cfg.SSMRoleARN != "" {
		stsClient := sts.NewFromConfig(awsCfg)
		assumeRoleProvider := stscreds.NewAssumeRoleProvider(stsClient, cfg.SSMRoleARN)
		awsCfg.Credentials = aws.NewCredentialsCache(assumeRoleProvider)
	}

	return awsCfg, nil
}

func GetSDKConfigProfile(awsCfg aws.Config) string {
	for _, cfg := range awsCfg.ConfigSources {
		if p, ok := cfg.(config.SharedConfig); ok {
			return p.Profile
		}
	}
	return ""
}

func GetSDKConfigRole(awsCfg aws.Config) string {
	for _, cfg := range awsCfg.ConfigSources {
		if p, ok := cfg.(config.SharedConfig); ok {
			return p.RoleARN
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
