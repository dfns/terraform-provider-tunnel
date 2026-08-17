package provider

import (
	"context"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/dfns/terraform-provider-tunnel/internal/libs"
	"github.com/dfns/terraform-provider-tunnel/internal/ssm"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type SSMModel struct {
	LocalHost   types.String `tfsdk:"local_host"`
	LocalPort   types.Int64  `tfsdk:"local_port"`
	SSMInstance types.String `tfsdk:"ssm_instance"`
	SSMDocument types.String `tfsdk:"ssm_document"`
	SSMProfile  types.String `tfsdk:"ssm_profile"`
	SSMRoleARN  types.String `tfsdk:"ssm_role_arn"`
	SSMRegion   types.String `tfsdk:"ssm_region"`
	TargetHost  types.String `tfsdk:"target_host"`
	TargetPort  types.Int64  `tfsdk:"target_port"`
}

// validateSSMTunnel rejects configurations the default port-forwarding document
// cannot serve. Both problems are reported at once so a user missing host and
// port does not have to fix them one apply at a time.
func validateSSMTunnel(data *SSMModel) diag.Diagnostics {
	var diags diag.Diagnostics

	doc := data.SSMDocument.ValueString()
	if doc != "" && doc != ssm.DefaultSSMDocument {
		return diags
	}

	if data.TargetHost.IsNull() || data.TargetHost.ValueString() == "" {
		diags.AddError(
			"target_host is required for the default SSM port-forwarding document",
			"`target_host` is required when `ssm_document` is unset or set to `"+ssm.DefaultSSMDocument+"`",
		)
	}
	if data.TargetPort.IsNull() || data.TargetPort.ValueInt64() == 0 {
		diags.AddError(
			"target_port is required for the default SSM port-forwarding document",
			"`target_port` is required when `ssm_document` is unset or set to `"+ssm.DefaultSSMDocument+"`",
		)
	}

	return diags
}

func ssmTargetPortString(port types.Int64) string {
	if port.IsNull() || port.ValueInt64() == 0 {
		return ""
	}
	return strconv.Itoa(int(port.ValueInt64()))
}

// ssmTunnelConfig validates the model and maps it onto a tunnel config,
// allocating a local port when the caller left it unset.
func ssmTunnelConfig(data *SSMModel) (ssm.TunnelConfig, diag.Diagnostics) {
	diags := validateSSMTunnel(data)
	if diags.HasError() {
		return ssm.TunnelConfig{}, diags
	}

	localPort := int(data.LocalPort.ValueInt64())
	if localPort == 0 {
		var err error
		localPort, err = libs.GetFreePort()
		if err != nil {
			diags.AddError("Failed to find open local port", err.Error())
			return ssm.TunnelConfig{}, diags
		}
	}

	// Hardcoded in session manager plugin
	// see: https://github.com/aws/session-manager-plugin/blob/mainline/src/sessionmanagerplugin/session/portsession/muxportforwarding.go#L245
	data.LocalHost = types.StringValue("localhost")
	data.LocalPort = types.Int64Value(int64(localPort))

	return ssm.TunnelConfig{
		LocalPort:   strconv.Itoa(localPort),
		SSMInstance: data.SSMInstance.ValueString(),
		SSMDocument: data.SSMDocument.ValueString(),
		SSMProfile:  data.SSMProfile.ValueString(),
		SSMRoleARN:  data.SSMRoleARN.ValueString(),
		SSMRegion:   data.SSMRegion.ValueString(),
		TargetHost:  data.TargetHost.ValueString(),
		TargetPort:  ssmTargetPortString(data.TargetPort),
	}, diags
}

// applySSMSDKConfig records what the credential chain resolved on both the
// tunnel config and the model, so the forked child and Terraform state agree on
// the region, profile and role actually used.
func applySSMSDKConfig(data *SSMModel, cfg *ssm.TunnelConfig, awsCfg aws.Config) {
	cfg.SSMRegion = awsCfg.Region
	cfg.SSMProfile = ssm.GetSDKConfigProfile(awsCfg)

	// Only update SSMRoleARN if it wasn't explicitly provided
	if cfg.SSMRoleARN == "" {
		cfg.SSMRoleARN = ssm.GetSDKConfigRole(awsCfg)
	}

	data.SSMRegion = types.StringValue(cfg.SSMRegion)
	data.SSMProfile = types.StringValue(cfg.SSMProfile)
	data.SSMRoleARN = types.StringValue(cfg.SSMRoleARN)
}

// resolveSSMSDKConfig loads the AWS SDK config for the tunnel.
func resolveSSMSDKConfig(ctx context.Context, data *SSMModel, cfg *ssm.TunnelConfig) (aws.Config, diag.Diagnostics) {
	var diags diag.Diagnostics

	awsCfg, err := ssm.GetNewSDKConfig(ctx, *cfg)
	if err != nil {
		diags.AddError("Failed to initialize AWS SDK", err.Error())
		return aws.Config{}, diags
	}
	applySSMSDKConfig(data, cfg, awsCfg)

	return awsCfg, diags
}

// ssmConfig prepares everything ForkRemoteTunnel needs from the model.
func ssmConfig(ctx context.Context, data *SSMModel) (ssm.TunnelConfig, aws.Config, diag.Diagnostics) {
	cfg, diags := ssmTunnelConfig(data)
	if diags.HasError() {
		return ssm.TunnelConfig{}, aws.Config{}, diags
	}

	awsCfg, sdkDiags := resolveSSMSDKConfig(ctx, data, &cfg)
	diags.Append(sdkDiags...)
	if diags.HasError() {
		return ssm.TunnelConfig{}, aws.Config{}, diags
	}

	return cfg, awsCfg, diags
}
