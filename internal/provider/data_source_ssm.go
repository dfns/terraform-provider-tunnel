package provider

import (
	"context"
	"fmt"
	"strconv"

	"github.com/dfns/terraform-provider-tunnel/internal/libs"
	"github.com/dfns/terraform-provider-tunnel/internal/ssm"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Ensure provider defined types fully satisfy framework interfaces.
var _ datasource.DataSource = &SSMDataSource{}

func NewSSMDataSource() datasource.DataSource {
	return &SSMDataSource{}
}

// SSMDataSource defines the data source implementation.
type SSMDataSource struct{}

// SSMDataSourceModel describes the data source data model.
type SSMDataSourceModel struct {
	TargetHost  types.String `tfsdk:"target_host"`
	TargetPort  types.Int64  `tfsdk:"target_port"`
	LocalHost   types.String `tfsdk:"local_host"`
	LocalPort   types.Int64  `tfsdk:"local_port"`
	SSMInstance types.String `tfsdk:"ssm_instance"`
	SSMRegion   types.String `tfsdk:"ssm_region"`
}

func (d *SSMDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ssm"
}

func (d *SSMDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
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

func (d *SSMDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data SSMDataSourceModel

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

	_, err = ssm.ForkRemoteTunnel(ctx, ssm.TunnelConfig{
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
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
