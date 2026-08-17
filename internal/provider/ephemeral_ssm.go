package provider

import (
	"context"
	"fmt"
	"strconv"

	"github.com/dfns/terraform-provider-tunnel/internal/libs"
	"github.com/dfns/terraform-provider-tunnel/internal/ssm"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
)

// Ensure provider defined types fully satisfy framework interfaces.
var _ ephemeral.EphemeralResource = &SSMEphemeral{}

func NewSSMEphemeral() ephemeral.EphemeralResource {
	return &SSMEphemeral{}
}

// SSMEphemeral defines the resource implementation.
type SSMEphemeral struct{}

func (d *SSMEphemeral) Metadata(ctx context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ssm"
}

func (d *SSMEphemeral) Schema(ctx context.Context, req ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Create a local AWS SSM tunnel to a remote host",

		Attributes: map[string]schema.Attribute{
			"target_host": schema.StringAttribute{
				MarkdownDescription: "The DNS name or IP address of the remote host. Required when `ssm_document` is unset or set to `AWS-StartPortForwardingSessionToRemoteHost`; omit when using a custom document that defines a fixed host.",
				Optional:            true,
			},
			"target_port": schema.Int64Attribute{
				MarkdownDescription: "The port number of the remote host. Required when `ssm_document` is unset or set to `AWS-StartPortForwardingSessionToRemoteHost`; omit when using a custom document that defines a fixed port.",
				Optional:            true,
			},
			"ssm_instance": schema.StringAttribute{
				MarkdownDescription: "Specify the exact Instance ID of the managed node to connect to for the session",
				Required:            true,
			},
			"ssm_document": schema.StringAttribute{
				MarkdownDescription: "Name of the SSM Session document to use for port forwarding. Defaults to `AWS-StartPortForwardingSessionToRemoteHost` when unset.",
				Optional:            true,
			},
			"ssm_profile": schema.StringAttribute{
				MarkdownDescription: "AWS profile name as set in credentials files. Can also be set using either the environment variables `AWS_PROFILE` or `AWS_DEFAULT_PROFILE`.",
				Optional:            true,
				Computed:            true,
			},
			"ssm_role_arn": schema.StringAttribute{
				MarkdownDescription: "ARN of an IAM role to assume.",
				Optional:            true,
				Computed:            true,
			},
			"ssm_region": schema.StringAttribute{
				MarkdownDescription: "AWS Region where the instance is located. The Region must be set. Can also be set using either the environment variables `AWS_REGION` or `AWS_DEFAULT_REGION`.",
				Optional:            true,
				Computed:            true,
			},
			"local_port": schema.Int64Attribute{
				MarkdownDescription: "The local port to listen on. If not set, a random free port is chosen.",
				Optional:            true,
				Computed:            true,
			},
			// Computed attributes
			"local_host": schema.StringAttribute{
				MarkdownDescription: "The DNS name or IP address of the local host",
				Computed:            true,
			},
		},
	}
}

func (d *SSMEphemeral) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	var data SSMModel

	// Read Terraform configuration data into the model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	tunnelCfg, awsCfg, diags := ssmConfig(ctx, &data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

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
