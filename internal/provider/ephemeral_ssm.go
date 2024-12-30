package provider

import (
	"context"
	"fmt"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	aws_ssm "github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/dfns/terraform-provider-tunnel/internal/ssm"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	ps "github.com/shirou/gopsutil/v4/process"
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
	TargetHost  types.String `tfsdk:"target_host"`
	TargetPort  types.Int64  `tfsdk:"target_port"`
	LocalHost   types.String `tfsdk:"local_host"`
	LocalPort   types.Int64  `tfsdk:"local_port"`
	SSMInstance types.String `tfsdk:"ssm_instance"`
	SSMRegion   types.String `tfsdk:"ssm_region"`
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
			"ssm_region": schema.StringAttribute{
				MarkdownDescription: "AWS Region where the instance is located",
				Required:            true,
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
	localPort, err := GetFreePort()
	if err != nil {
		resp.Diagnostics.AddError("Failed to find open port", fmt.Sprintf("Error: %s", err))
		return
	}

	// Hardcoded in session manager plugin
	// see: https://github.com/aws/session-manager-plugin/blob/mainline/src/sessionmanagerplugin/session/portsession/muxportforwarding.go#L245
	data.LocalHost = types.StringValue("localhost")
	data.LocalPort = types.Int64Value(int64(localPort))

	forkResult, err := ssm.ForkRemoteTunnel(ctx, ssm.TunnelConfig{
		SSMRegion:   data.SSMRegion.ValueString(),
		SSMInstance: data.SSMInstance.ValueString(),
		TargetHost:  data.TargetHost.ValueString(),
		TargetPort:  strconv.Itoa(int(data.TargetPort.ValueInt64())),
		LocalPort:   strconv.Itoa(localPort),
	})
	if err != nil {
		resp.Diagnostics.AddError("Failed to fork tunnel process", fmt.Sprintf("Error: %s", err))
		return
	}

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.Result.Set(ctx, &data)...)
	resp.Private.SetKey(ctx, "tunnel_pid", []byte(strconv.Itoa(forkResult.Command.Process.Pid)))
	resp.Private.SetKey(ctx, "session_id", []byte(forkResult.Session.SessionId))
	resp.Private.SetKey(ctx, "ssm_region", []byte(data.SSMRegion.ValueString()))
}

func (d *SSMEphemeral) Close(ctx context.Context, req ephemeral.CloseRequest, resp *ephemeral.CloseResponse) {
	tunnelBytes, _ := req.Private.GetKey(ctx, "tunnel_pid")
	tunnelPID, err := strconv.Atoi(string(tunnelBytes))
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse tunnel PID", fmt.Sprintf("Error: %s", err))
		return
	}

	tunnel, err := ps.NewProcess(int32(tunnelPID))
	if err != nil {
		resp.Diagnostics.AddError("Failed to find tunnel process", fmt.Sprintf("Error: %s", err))
		return
	}

	if err := tunnel.Terminate(); err != nil {
		resp.Diagnostics.AddError("Failed to terminate tunnel process", fmt.Sprintf("Error: %s", err))
		return
	}

	sessionID, _ := req.Private.GetKey(ctx, "session_id")
	ssmRegion, _ := req.Private.GetKey(ctx, "ssm_region")
	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(string(ssmRegion)))
	if err != nil {
		resp.Diagnostics.AddError("Failed to load AWS config", fmt.Sprintf("Error: %s", err))
		return
	}

	ssmClient := aws_ssm.NewFromConfig(awsCfg)

	_, err = ssmClient.TerminateSession(ctx, &aws_ssm.TerminateSessionInput{
		SessionId: aws.String(string(sessionID)),
	})
	if err != nil {
		resp.Diagnostics.AddError("Failed to terminate SSM session", fmt.Sprintf("Error: %s", err))
		return
	}
}
