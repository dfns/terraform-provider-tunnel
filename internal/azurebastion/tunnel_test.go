package azurebastion

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6"
)

func TestTunnelConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*TunnelConfig)
		wantErr string
	}{
		{name: "resource target"},
		{
			name:   "IP target",
			mutate: func(c *TunnelConfig) { *c = ipConnectConfig() },
		},
		{
			name: "both targets",
			mutate: func(c *TunnelConfig) {
				c.TargetIPAddress = testTargetIP
			},
			wantErr: "exactly one",
		},
		{
			name: "neither target",
			mutate: func(c *TunnelConfig) {
				c.TargetResourceID = ""
			},
			wantErr: "exactly one",
		},
		{
			name: "malformed resource target",
			mutate: func(c *TunnelConfig) {
				c.TargetResourceID = "vm"
			},
			wantErr: "target_resource_id",
		},
		{
			name: "malformed IP target",
			mutate: func(c *TunnelConfig) {
				*c = ipConnectConfig()
				c.TargetIPAddress = "not-an-ip"
			},
			wantErr: "valid IPv4 or IPv6",
		},
		{
			name: "custom port with IP",
			mutate: func(c *TunnelConfig) {
				*c = ipConnectConfig()
				c.TargetPort = 5432
			},
			wantErr: "22 or 3389",
		},
		{
			name: "target port out of range",
			mutate: func(c *TunnelConfig) {
				c.TargetPort = 65536
			},
			wantErr: "target_port",
		},
		{
			name: "local port out of range",
			mutate: func(c *TunnelConfig) {
				c.LocalPort = 0
			},
			wantErr: "local_port",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			if tt.mutate != nil {
				tt.mutate(&cfg)
			}
			err := cfg.Validate()
			if tt.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseBastionHostID(t *testing.T) {
	valid := validConfig().BastionHostID
	got, err := parseBastionHostID(valid)
	if err != nil {
		t.Fatal(err)
	}
	if got.SubscriptionID != "sub" || got.ResourceGroupName != "rg" || got.Name != "main" {
		t.Fatalf("unexpected parsed ID: %+v", got)
	}

	if _, err := parseBastionHostID("/SUBSCRIPTIONS/sub/RESOURCEGROUPS/rg/PROVIDERS/microsoft.network/BASTIONHOSTS/main"); err != nil {
		t.Errorf("ARM IDs are case insensitive: %v", err)
	}

	for _, invalid := range []string{
		"main",
		"/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/main",
		"/subscriptions/sub/providers/Microsoft.Network/bastionHosts/main",
		"/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/bastionHosts/",
		valid + "/child/value",
	} {
		if _, err := parseBastionHostID(invalid); err == nil {
			t.Errorf("expected %q to be rejected", invalid)
		}
	}
}

func TestValidateBastion(t *testing.T) {
	valid := bastionInfo{
		DNSName:         "bst-abc.bastion.azure.com",
		SKU:             "Standard",
		EnableTunneling: true,
		EnableIPConnect: true,
	}
	if err := validateBastion(valid, true); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		mutate  func(*bastionInfo)
		useIP   bool
		wantErr string
	}{
		{name: "unsupported SKU", mutate: func(b *bastionInfo) { b.SKU = "Basic" }, wantErr: "Standard or Premium"},
		{name: "tunneling disabled", mutate: func(b *bastionInfo) { b.EnableTunneling = false }, wantErr: "not enabled"},
		{name: "IP Connect disabled", mutate: func(b *bastionInfo) { b.EnableIPConnect = false }, useIP: true, wantErr: "IP Connect"},
		{name: "missing DNS", mutate: func(b *bastionInfo) { b.DNSName = "" }, wantErr: "no DNS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := valid
			tt.mutate(&info)
			err := validateBastion(info, tt.useIP)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestTunnelConfigResolveTarget(t *testing.T) {
	t.Run("resource ID", func(t *testing.T) {
		cfg := validConfig()
		plan := mustResolve(t, cfg)
		if plan.TargetResourceID != cfg.TargetResourceID || plan.Hostname != "" {
			t.Fatalf("resolve() = (%q, %q), want (%q, \"\")", plan.TargetResourceID, plan.Hostname, cfg.TargetResourceID)
		}
	})

	t.Run("IP Connect", func(t *testing.T) {
		plan := mustResolve(t, ipConnectConfig())
		wantID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/bh-hostConnect/" + testTargetIP
		if plan.TargetResourceID != wantID || plan.Hostname != testTargetIP {
			t.Fatalf("resolve() = (%q, %q), want (%q, %q)", plan.TargetResourceID, plan.Hostname, wantID, testTargetIP)
		}
	})
}

func TestTunnelConfigResolveLocalListener(t *testing.T) {
	// An empty local host must not fall through to a wildcard bind.
	for _, localHost := range []string{"", "  "} {
		t.Run("defaulted from "+strconv.Quote(localHost), func(t *testing.T) {
			cfg := validConfig()
			cfg.LocalHost = localHost
			plan := mustResolve(t, cfg)
			if plan.LocalHost != "localhost" {
				t.Fatalf("plan.LocalHost = %q, want %q", plan.LocalHost, "localhost")
			}
			if plan.LocalPort != cfg.LocalPort {
				t.Fatalf("plan.LocalPort = %d, want %d", plan.LocalPort, cfg.LocalPort)
			}
		})
	}

	t.Run("explicit host preserved", func(t *testing.T) {
		cfg := validConfig()
		cfg.LocalHost = "0.0.0.0"
		if plan := mustResolve(t, cfg); plan.LocalHost != "0.0.0.0" {
			t.Fatalf("plan.LocalHost = %q, want %q", plan.LocalHost, "0.0.0.0")
		}
	})
}

func TestDiscoverBastion(t *testing.T) {
	sku := armnetwork.BastionHostSKUNamePremium
	dns := "bst-abc.bastion.azure.com"
	enabled := true
	client := &fakeBastionGetter{
		response: armnetwork.BastionHostsClientGetResponse{
			BastionHost: armnetwork.BastionHost{
				SKU: &armnetwork.SKU{Name: &sku},
				Properties: &armnetwork.BastionHostPropertiesFormat{
					DNSName:         &dns,
					EnableTunneling: &enabled,
					EnableIPConnect: &enabled,
				},
			},
		},
	}
	id, err := parseBastionHostID(validConfig().BastionHostID)
	if err != nil {
		t.Fatal(err)
	}
	info, err := discoverBastion(context.Background(), client, id)
	if err != nil {
		t.Fatal(err)
	}
	if client.group != "rg" || client.name != "main" {
		t.Fatalf("ARM lookup used group=%q name=%q", client.group, client.name)
	}
	if info.DNSName != dns || info.SKU != "Premium" || !info.EnableTunneling || !info.EnableIPConnect {
		t.Fatalf("unexpected Bastion info: %+v", info)
	}

	client.err = errors.New("ARM denied")
	if _, err := discoverBastion(context.Background(), client, id); err == nil ||
		!strings.Contains(err.Error(), "read Azure Bastion resource") {
		t.Fatalf("unexpected ARM error: %v", err)
	}
}
