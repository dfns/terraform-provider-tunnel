package provider

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestAzureBastionConfigDefaultsAndValidation(t *testing.T) {
	data := AzureBastionModel{
		BastionHostID:    types.StringValue("/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/bastionHosts/main"),
		TargetResourceID: types.StringValue("/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/vm"),
		TargetIPAddress:  types.StringNull(),
		TargetPort:       types.Int64Value(5432),
		LocalHost:        types.StringNull(),
		LocalPort:        types.Int64Value(15432),
	}
	cfg, err := azureBastionConfig(&data)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LocalHost != "localhost" || cfg.LocalPort != 15432 {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	if data.LocalHost.ValueString() != cfg.LocalHost || data.LocalPort.ValueInt64() != int64(cfg.LocalPort) {
		t.Fatalf("model not updated: %+v", data)
	}

	data.TargetIPAddress = types.StringValue("10.0.1.4")
	if _, err := azureBastionConfig(&data); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("expected mutually exclusive target error, got %v", err)
	}
}

func TestAzureBastionConfigAllocatesFreeLocalPort(t *testing.T) {
	for _, tt := range []struct {
		name      string
		localPort types.Int64
	}{
		{name: "zero", localPort: types.Int64Value(0)},
		{name: "null", localPort: types.Int64Null()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := AzureBastionModel{
				BastionHostID:    types.StringValue("/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/bastionHosts/main"),
				TargetResourceID: types.StringValue("/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/vm"),
				TargetIPAddress:  types.StringNull(),
				TargetPort:       types.Int64Value(5432),
				LocalHost:        types.StringNull(),
				LocalPort:        tt.localPort,
			}
			cfg, err := azureBastionConfig(&data)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.LocalPort == 0 {
				t.Fatal("no local port allocated")
			}
			// Terraform persists the model as computed state, so it must carry the
			// port the tunnel actually binds.
			if data.LocalPort.ValueInt64() != int64(cfg.LocalPort) {
				t.Fatalf("model local port = %d, want %d", data.LocalPort.ValueInt64(), cfg.LocalPort)
			}
		})
	}
}
