package provider

import (
	"context"
	"fmt"
	"strconv"

	"github.com/dfns/terraform-provider-tunnel/internal/libs"
	"github.com/dfns/terraform-provider-tunnel/internal/ssm"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Ensure provider defined types fully satisfy framework interfaces.
var _ ephemeral.EphemeralResource = &SSMEphemeral{}

func NewSSMEphemeral() ephemeral.EphemeralResource {
	return &SSMEphemeral{}
}

// SSMEphemeral defines the resource implementation.
type SSMEphemeral struct{}

// SSMEphemeralModel describes the data source data model.
type SSMEphemeralModel struct {
	LocalHost   types.String `tfsdk:"local_host"`
	LocalPort   types.Int64  `tfsdk:"local_port"`
	SSMInstance types.String `tfsdk:"ssm_instance"`
	SSMProfile  types.String `tfsdk:"ssm_profile"`
	SSMRole     types.String `tfsdk:"ssm_role"`
	SSMRegion   types.String `tfsdk:"ssm_region"`
	TargetHost  types.String `tfsdk:"target_host"`
	TargetPort  types.Int64  `tfsdk:"target_port"`
}

func (d *SSMEphemeral) Metadata(ctx context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ssm"
}

func (d *SSMEphemeral) Schema(ctx context.Context, req ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Create a local AWS SSM tunnel to a remote host",

		Attributes: map[string]schema.Attribute{
			// Required attributes
			"target_host": schema.StringAttribute{
				MarkdownDescription: "The DNS name or IP address of the remote host",
				Required:            true,
			},
			"target_port": schema.Int64Attribute{
				MarkdownDescription: "The port number of the remote host",
				Required:            true,
			},
			"ssm_instance": schema.StringAttribute{
				MarkdownDescription: "Specify the exact Instance ID of the managed node to connect to for the session",
				Required:            true,
			},
			"ssm_profile": schema.StringAttribute{
				MarkdownDescription: "AWS profile name as set in credentials files. Can also be set using either the environment variables `AWS_PROFILE` or `AWS_DEFAULT_PROFILE`.",
				Optional:            true,
				Computed:            true,
			},
			"ssm_role": schema.StringAttribute{
				MarkdownDescription: "ARN of an IAM role to assume. Can also be set using the environment variable `AWS_ROLE_ARN`.",
				Optional:            true,
				Computed:            true,
			},
			"ssm_region": schema.StringAttribute{
				MarkdownDescription: "AWS Region where the instance is located. The Region must be set. Can also be set using either the environment variables `AWS_REGION` or `AWS_DEFAULT_REGION`.",
				Optional:            true,
				Computed:            true,
			},

			// Computed attributes
			"local_host": schema.StringAttribute{
				MarkdownDescription: "The DNS name or IP address of the local host",
				Computed:            true,
			},
			"local_port": schema.Int64Attribute{
				MarkdownDescription: "The local port number to use for the tunnel",
				Computed:            true,
			},
		},
	}
}

func (d *SSMEphemeral) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	var data SSMEphemeralModel

	// Read Terraform configuration data into the model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	// Get a free port for the local tunnel
	localPort, err := libs.GetFreePort()
	if err != nil {
		resp.Diagnostics.AddError("Failed to find open port", fmt.Sprintf("Error: %s", err))
		return
	}

	// Hardcoded in session manager plugin
	// see: https://github.com/aws/session-manager-plugin/blob/mainline/src/sessionmanagerplugin/session/portsession/muxportforwarding.go#L245
	data.LocalHost = types.StringValue("localhost")
	data.LocalPort = types.Int64Value(int64(localPort))

	tunnelCfg := ssm.TunnelConfig{
		LocalPort:   strconv.Itoa(localPort),
		SSMInstance: data.SSMInstance.ValueString(),
		SSMProfile:  data.SSMProfile.ValueString(),
		SSMRole:     data.SSMRole.ValueString(),
		SSMRegion:   data.SSMRegion.ValueString(),
		TargetHost:  data.TargetHost.ValueString(),
		TargetPort:  strconv.Itoa(int(data.TargetPort.ValueInt64())),
	}

	awsCfg, err := ssm.GetNewSDKConfig(ctx, tunnelCfg)
	if err != nil {
		resp.Diagnostics.AddError("Failed to initialize AWS SDK", fmt.Sprintf("Error: %s", err))
		return
	}

	tunnelCfg.SSMRegion = awsCfg.Region
	tunnelCfg.SSMProfile = ssm.GetSDKConfigProfile(awsCfg)
	tunnelCfg.SSMRole = ssm.GetSDKConfigRole(awsCfg)

	data.SSMRegion = types.StringValue(tunnelCfg.SSMRegion)
	data.SSMProfile = types.StringValue(tunnelCfg.SSMProfile)
	data.SSMRole = types.StringValue(tunnelCfg.SSMRole)

	cmd, err := ssm.ForkRemoteTunnel(ctx, awsCfg, tunnelCfg)
	if err != nil {
		resp.Diagnostics.AddError("Failed to fork tunnel process", fmt.Sprintf("Error: %s", err))
		return
	}

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.Result.Set(ctx, &data)...)
	resp.Private.SetKey(ctx, "tunnel_pid", []byte(strconv.Itoa(cmd.Process.Pid)))
}

func (d *SSMEphemeral) Close(ctx context.Context, req ephemeral.CloseRequest, resp *ephemeral.CloseResponse) {
	tunnelBytes, _ := req.Private.GetKey(ctx, "tunnel_pid")
	tunnelPID, err := strconv.Atoi(string(tunnelBytes))
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse tunnel PID", fmt.Sprintf("Error: %s", err))
		return
	}

	if err := libs.Interrupt(tunnelPID); err != nil {
		resp.Diagnostics.AddError("Failed to terminate tunnel process", fmt.Sprintf("Error: %s", err))
		return
	}
}
