package provider

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/dfns/terraform-provider-tunnel/internal/libs"
	"github.com/dfns/terraform-provider-tunnel/internal/ssm"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func validateSSMTarget(targetHost, ssmDocument types.String) error {
	doc := ssmDocument.ValueString()
	if doc != "" && doc != ssm.DefaultSSMDocument {
		return nil
	}
	if targetHost.IsNull() || targetHost.ValueString() == "" {
		return errors.New("`target_host` is required when `ssm_document` is unset or set to `AWS-StartPortForwardingSessionToRemoteHost`")
	}
	return nil
}

// Ensure provider defined types fully satisfy framework interfaces.
var _ datasource.DataSource = &SSMDataSource{}

func NewSSMDataSource() datasource.DataSource {
	return &SSMDataSource{}
}

// SSMDataSource defines the data source implementation.
type SSMDataSource struct{}

// SSMDataSourceModel describes the data source data model.
type SSMDataSourceModel struct {
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

func (d *SSMDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ssm"
}

func (d *SSMDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Create a local AWS SSM tunnel to a remote host",

		Attributes: map[string]schema.Attribute{
			"target_host": schema.StringAttribute{
				MarkdownDescription: "The DNS name or IP address of the remote host. Required when `ssm_document` is unset or set to `AWS-StartPortForwardingSessionToRemoteHost`; omit when using a custom document that defines a fixed host.",
				Optional:            true,
			},
			"target_port": schema.Int64Attribute{
				MarkdownDescription: "The port number of the remote host",
				Required:            true,
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

func (d *SSMDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data SSMDataSourceModel

	// Read Terraform configuration data into the model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	if err := validateSSMTarget(data.TargetHost, data.SSMDocument); err != nil {
		resp.Diagnostics.AddError(
			"target_host is required for the default SSM port-forwarding document",
			err.Error(),
		)
		return
	}

	localPort := int(data.LocalPort.ValueInt64())
	if localPort == 0 {
		var err error
		localPort, err = libs.GetFreePort()
		if err != nil {
			resp.Diagnostics.AddError("Failed to find open port", fmt.Sprintf("Error: %s", err))
			return
		}
	}

	// Hardcoded in session manager plugin
	// see: https://github.com/aws/session-manager-plugin/blob/mainline/src/sessionmanagerplugin/session/portsession/muxportforwarding.go#L245
	data.LocalHost = types.StringValue("localhost")
	data.LocalPort = types.Int64Value(int64(localPort))

	tunnelCfg := ssm.TunnelConfig{
		LocalPort:   strconv.Itoa(localPort),
		SSMInstance: data.SSMInstance.ValueString(),
		SSMDocument: data.SSMDocument.ValueString(),
		SSMProfile:  data.SSMProfile.ValueString(),
		SSMRoleARN:  data.SSMRoleARN.ValueString(),
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

	// Only update SSMRoleARN if it wasn't explicitly provided
	if tunnelCfg.SSMRoleARN == "" {
		tunnelCfg.SSMRoleARN = ssm.GetSDKConfigRole(awsCfg)
	}

	data.SSMRegion = types.StringValue(tunnelCfg.SSMRegion)
	data.SSMProfile = types.StringValue(tunnelCfg.SSMProfile)
	data.SSMRoleARN = types.StringValue(tunnelCfg.SSMRoleARN)

	_, err = ssm.ForkRemoteTunnel(ctx, awsCfg, tunnelCfg)
	if err != nil {
		resp.Diagnostics.AddError("Failed to fork tunnel process", fmt.Sprintf("Error: %s", err))
		return
	}

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
