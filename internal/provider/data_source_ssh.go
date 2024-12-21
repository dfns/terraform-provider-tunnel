package provider

import (
	"context"
	"fmt"
	"os/user"

	"github.com/dfns/terraform-provider-tunnel/internal/libs"
	"github.com/dfns/terraform-provider-tunnel/internal/ssh"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Ensure provider defined types fully satisfy framework interfaces.
var _ datasource.DataSource = &SSHDataSource{}

func NewSSHDataSource() datasource.DataSource {
	return &SSHDataSource{}
}

// SSHDataSource defines the data source implementation.
type SSHDataSource struct{}

// SSHDataSourceModel describes the data source data model.
type SSHDataSourceModel struct {
	LocalHost  types.String `tfsdk:"local_host"`
	LocalPort  types.Int64  `tfsdk:"local_port"`
	SSHHost    types.String `tfsdk:"ssh_host"`
	SSHPort    types.Int64  `tfsdk:"ssh_port"`
	SSHUser    types.String `tfsdk:"ssh_user"`
	TargetHost types.String `tfsdk:"target_host"`
	TargetPort types.Int64  `tfsdk:"target_port"`
}

func (d *SSHDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ssh"
}

func (d *SSHDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Create a local SSH tunnel to a remote host",

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
			"ssh_host": schema.StringAttribute{
				MarkdownDescription: "The DNS name or IP address of the SSH bastion host",
				Required:            true,
			},
			"ssh_port": schema.Int64Attribute{
				MarkdownDescription: "The port number of the SSH bastion host",
				Optional:            true,
				Computed:            true,
			},
			"ssh_user": schema.StringAttribute{
				MarkdownDescription: "The username to use for the SSH connection",
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

func (d *SSHDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data SSHDataSourceModel

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

	data.LocalHost = types.StringValue("localhost")
	data.LocalPort = types.Int64Value(int64(localPort))

	if data.SSHUser.IsNull() {
		user, err := user.Current()
		if err != nil {
			resp.Diagnostics.AddError("Failed to get current user", fmt.Sprintf("Error: %s", err))
			return
		}
		data.SSHUser = types.StringValue(user.Username)
	}
	if data.SSHPort.IsNull() {
		data.SSHPort = types.Int64Value(22)
	}

	_, err = ssh.ForkRemoteTunnel(ctx, ssh.TunnelConfig{
		LocalPort:  localPort,
		SSHHost:    data.SSHHost.ValueString(),
		SSHPort:    int(data.SSHPort.ValueInt64()),
		SSHUser:    data.SSHUser.ValueString(),
		TargetHost: data.TargetHost.ValueString(),
		TargetPort: int(data.TargetPort.ValueInt64()),
	})
	if err != nil {
		resp.Diagnostics.AddError("Failed to fork tunnel process", fmt.Sprintf("Error: %s", err))
		return
	}

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
