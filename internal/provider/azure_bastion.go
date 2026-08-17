package provider

import (
	"github.com/dfns/terraform-provider-tunnel/internal/azurebastion"
	"github.com/dfns/terraform-provider-tunnel/internal/libs"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type AzureBastionModel struct {
	BastionHostID    types.String `tfsdk:"bastion_host_id"`
	TargetResourceID types.String `tfsdk:"target_resource_id"`
	TargetIPAddress  types.String `tfsdk:"target_ip_address"`
	TargetPort       types.Int64  `tfsdk:"target_port"`
	LocalHost        types.String `tfsdk:"local_host"`
	LocalPort        types.Int64  `tfsdk:"local_port"`
}

func azureBastionConfig(data *AzureBastionModel) (azurebastion.TunnelConfig, diag.Diagnostics) {
	var diags diag.Diagnostics

	if data.LocalHost.IsNull() || data.LocalHost.ValueString() == "" {
		data.LocalHost = types.StringValue(azurebastion.DefaultLocalHost)
	}
	localPort := int(data.LocalPort.ValueInt64())
	if localPort == 0 {
		var err error
		localPort, err = libs.GetFreePort()
		if err != nil {
			diags.AddError("Failed to find open local port", err.Error())
			return azurebastion.TunnelConfig{}, diags
		}
		data.LocalPort = types.Int64Value(int64(localPort))
	}
	cfg := azurebastion.TunnelConfig{
		BastionHostID:    data.BastionHostID.ValueString(),
		TargetResourceID: data.TargetResourceID.ValueString(),
		TargetIPAddress:  data.TargetIPAddress.ValueString(),
		TargetPort:       int(data.TargetPort.ValueInt64()),
		LocalHost:        data.LocalHost.ValueString(),
		LocalPort:        localPort,
	}
	if err := cfg.Validate(); err != nil {
		diags.AddError("Invalid Azure Bastion tunnel configuration", err.Error())
		return azurebastion.TunnelConfig{}, diags
	}

	return cfg, diags
}
