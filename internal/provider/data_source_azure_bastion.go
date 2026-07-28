package provider

import (
	"context"
	"fmt"

	"github.com/dfns/terraform-provider-tunnel/internal/azurebastion"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
)

var _ datasource.DataSource = &AzureBastionDataSource{}

func NewAzureBastionDataSource() datasource.DataSource {
	return &AzureBastionDataSource{}
}

type AzureBastionDataSource struct{}

func (d *AzureBastionDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_azure_bastion"
}

func (d *AzureBastionDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Create a local TCP tunnel through Azure Bastion",
		Attributes: map[string]schema.Attribute{
			"bastion_host_id": schema.StringAttribute{
				MarkdownDescription: "Full Azure resource ID of the `Microsoft.Network/bastionHosts` resource.",
				Required:            true,
			},
			"target_resource_id": schema.StringAttribute{
				MarkdownDescription: "Full Azure resource ID of the tunnel target. Exactly one of `target_resource_id` and `target_ip_address` must be set.",
				Optional:            true,
			},
			"target_ip_address": schema.StringAttribute{
				MarkdownDescription: "Private IP address of the tunnel target. Requires Azure Bastion IP Connect and restricts `target_port` to 22 or 3389. Exactly one target selector must be set.",
				Optional:            true,
			},
			"target_port": schema.Int64Attribute{
				MarkdownDescription: "TCP port on the target resource.",
				Required:            true,
			},
			"local_host": schema.StringAttribute{
				MarkdownDescription: "Local address to listen on. Defaults to `localhost`. Binding a non-loopback address can expose the tunnel to other hosts.",
				Optional:            true,
				Computed:            true,
			},
			"local_port": schema.Int64Attribute{
				MarkdownDescription: "Local port to listen on. If not set, a random free port is chosen.",
				Optional:            true,
				Computed:            true,
			},
		},
	}
}

func (d *AzureBastionDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data AzureBastionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	cfg, err := azureBastionConfig(&data)
	if err != nil {
		resp.Diagnostics.AddError("Invalid Azure Bastion tunnel configuration", err.Error())
		return
	}
	if _, err := azurebastion.ForkRemoteTunnel(ctx, cfg); err != nil {
		resp.Diagnostics.AddError("Failed to start Azure Bastion tunnel", fmt.Sprintf("Error: %s", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
