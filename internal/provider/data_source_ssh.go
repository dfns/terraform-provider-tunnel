package provider

import (
	"context"
	"fmt"

	"github.com/dfns/terraform-provider-tunnel/internal/ssh"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
)

// Ensure provider defined types fully satisfy framework interfaces.
var _ datasource.DataSource = &SSHDataSource{}

func NewSSHDataSource() datasource.DataSource {
	return &SSHDataSource{}
}

// SSHDataSource defines the data source implementation.
type SSHDataSource struct{}

func (d *SSHDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ssh"
}

func (d *SSHDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Create a local SSH tunnel to a remote host",

		Attributes: map[string]schema.Attribute{
			"target_host": schema.StringAttribute{
				MarkdownDescription: "The DNS name or IP address of the remote host. Required when `target_port` is set; ignored when `target_socket` is set.",
				Optional:            true,
			},
			"target_port": schema.Int64Attribute{
				MarkdownDescription: "The TCP port of the remote host. Mutually exclusive with `target_socket`.",
				Optional:            true,
			},
			"target_socket": schema.StringAttribute{
				MarkdownDescription: "Path of a unix domain socket on the SSH bastion to forward to. Mutually exclusive with `target_port`.",
				Optional:            true,
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
			"ssh_password": schema.StringAttribute{
				MarkdownDescription: "The password to use for the SSH connection",
				Optional:            true,
				Sensitive:           true,
			},
			"ssh_key": schema.StringAttribute{
				MarkdownDescription: "The path to the private key file or the private key content to use for the SSH connection",
				Optional:            true,
				Sensitive:           true,
			},
			"ssh_key_passphrase": schema.StringAttribute{
				MarkdownDescription: "The passphrase for the private key file",
				Optional:            true,
				Sensitive:           true,
			},
			"local_host": schema.StringAttribute{
				MarkdownDescription: "The local address to listen on. Defaults to `localhost`.",
				Optional:            true,
				Computed:            true,
			},
			"local_port": schema.Int64Attribute{
				MarkdownDescription: "The local port to listen on. If not set, a random free port is chosen.",
				Optional:            true,
				Computed:            true,
			},
		},
	}
}

func (d *SSHDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data SSHModel

	// Read Terraform configuration data into the model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	cfg, diags := sshConfig(&data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := ssh.ForkRemoteTunnel(ctx, cfg)
	if err != nil {
		resp.Diagnostics.AddError("Failed to fork tunnel process", fmt.Sprintf("Error: %s", err))
		return
	}

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
