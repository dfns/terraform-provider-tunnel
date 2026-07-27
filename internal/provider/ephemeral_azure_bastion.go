package provider

import (
	"context"
	"fmt"
	"strconv"

	"github.com/dfns/terraform-provider-tunnel/internal/azurebastion"
	"github.com/dfns/terraform-provider-tunnel/internal/libs"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
)

var _ ephemeral.EphemeralResource = &AzureBastionEphemeral{}

func NewAzureBastionEphemeral() ephemeral.EphemeralResource {
	return &AzureBastionEphemeral{}
}

type AzureBastionEphemeral struct{}

func (d *AzureBastionEphemeral) Metadata(ctx context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_azure_bastion"
}

func (d *AzureBastionEphemeral) Schema(ctx context.Context, req ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
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

func (d *AzureBastionEphemeral) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
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
	cmd, err := azurebastion.ForkRemoteTunnel(ctx, cfg)
	if err != nil {
		resp.Diagnostics.AddError("Failed to start Azure Bastion tunnel", fmt.Sprintf("Error: %s", err))
		return
	}
	resp.Diagnostics.Append(resp.Result.Set(ctx, &data)...)
	resp.Private.SetKey(ctx, "tunnel_pid", []byte(strconv.Itoa(cmd.Process.Pid)))
}

func (d *AzureBastionEphemeral) Close(ctx context.Context, req ephemeral.CloseRequest, resp *ephemeral.CloseResponse) {
	tunnelBytes, _ := req.Private.GetKey(ctx, "tunnel_pid")
	tunnelPID, err := strconv.Atoi(string(tunnelBytes))
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse tunnel PID", fmt.Sprintf("Error: %s", err))
		return
	}
	if err := libs.Interrupt(tunnelPID); err != nil {
		resp.Diagnostics.AddError("Failed to terminate tunnel process", fmt.Sprintf("Error: %s", err))
	}
}
