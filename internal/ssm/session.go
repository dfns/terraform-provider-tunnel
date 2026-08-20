package ssm

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// Default SSM document for port forwarding.
const DefaultSSMDocument = "AWS-StartPortForwardingSessionToRemoteHost"

const sessionCleanupTimeout = 10 * time.Second

type TunnelConfig struct {
	LocalPort   string
	SSMInstance string
	SSMDocument string
	SSMProfile  string
	SSMRoleARN  string
	SSMRegion   string
	TargetHost  string
	TargetPort  string

	// SessionParams is set by the parent to hand the started session to the
	// forked child, and is unset everywhere else.
	SessionParams *SessionParams `json:",omitempty"`
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
	reqParams["localPortNumber"] = []string{cfg.LocalPort}

	docName := cfg.SSMDocument
	if docName == "" {
		docName = DefaultSSMDocument
	}

	if cfg.TargetHost != "" {
		reqParams["host"] = []string{cfg.TargetHost}
	}

	if cfg.TargetPort != "" {
		reqParams["portNumber"] = []string{cfg.TargetPort}
	}

	return ssm.StartSessionInput{
		Target:       aws.String(cfg.SSMInstance),
		DocumentName: aws.String(docName),
		Parameters:   reqParams,
	}
}

func startTunnelSession(ctx context.Context, ssmClient *ssm.Client, cfg TunnelConfig) (SessionParams, error) {
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

// terminateTunnelSession closes a session no plugin ever took over, which SSM
// would otherwise hold until its idle timeout. Cancellation is stripped from
// ctx because a done ctx is one way to reach this teardown, so the call gets a
// deadline of its own instead.
func terminateTunnelSession(ctx context.Context, ssmClient *ssm.Client, session SessionParams) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sessionCleanupTimeout)
	defer cancel()

	_, err := ssmClient.TerminateSession(ctx, &ssm.TerminateSessionInput{
		SessionId: aws.String(session.SessionId),
	})
	return err
}
