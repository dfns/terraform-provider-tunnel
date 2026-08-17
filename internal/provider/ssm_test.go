package provider

import (
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/dfns/terraform-provider-tunnel/internal/ssm"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func minimalSSMModel() SSMModel {
	return SSMModel{
		LocalHost:   types.StringNull(),
		LocalPort:   types.Int64Value(14433),
		SSMInstance: types.StringValue("i-instanceid"),
		SSMDocument: types.StringNull(),
		SSMProfile:  types.StringNull(),
		SSMRoleARN:  types.StringNull(),
		SSMRegion:   types.StringNull(),
		TargetHost:  types.StringValue("db.internal"),
		TargetPort:  types.Int64Value(5432),
	}
}

func TestSSMTunnelConfigDefaults(t *testing.T) {
	data := minimalSSMModel()

	cfg, diags := ssmTunnelConfig(&data)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if cfg.LocalPort != "14433" {
		t.Fatalf("local port = %q, want 14433", cfg.LocalPort)
	}
	if cfg.SSMInstance != "i-instanceid" || cfg.TargetHost != "db.internal" || cfg.TargetPort != "5432" {
		t.Fatalf("target not mapped: %+v", cfg)
	}
	// The session manager plugin always binds localhost, so the model must say so
	// regardless of what the caller configured.
	if data.LocalHost.ValueString() != "localhost" {
		t.Fatalf("model local host = %q, want localhost", data.LocalHost.ValueString())
	}
	if data.LocalPort.ValueInt64() != 14433 {
		t.Fatalf("model local port = %d, want 14433", data.LocalPort.ValueInt64())
	}
}

func TestSSMTunnelConfigAllocatesFreeLocalPort(t *testing.T) {
	for _, tt := range []struct {
		name      string
		localPort types.Int64
	}{
		{name: "zero", localPort: types.Int64Value(0)},
		{name: "null", localPort: types.Int64Null()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := minimalSSMModel()
			data.LocalPort = tt.localPort

			cfg, diags := ssmTunnelConfig(&data)
			if diags.HasError() {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			if cfg.LocalPort == "0" || cfg.LocalPort == "" {
				t.Fatalf("no local port allocated: %q", cfg.LocalPort)
			}
			// Terraform persists the model as computed state, so it must carry the
			// port the tunnel actually binds.
			if data.LocalPort.ValueInt64() == 0 {
				t.Fatal("model local port not updated")
			}
		})
	}
}

// A custom document defines its own host and port, so the tunnel config carries
// neither and validation must not demand them.
func TestSSMTunnelConfigCustomDocument(t *testing.T) {
	data := minimalSSMModel()
	data.SSMDocument = types.StringValue("MyCustomPortForwardDocument")
	data.TargetHost = types.StringNull()
	data.TargetPort = types.Int64Null()

	cfg, diags := ssmTunnelConfig(&data)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if cfg.SSMDocument != "MyCustomPortForwardDocument" {
		t.Fatalf("document = %q, want MyCustomPortForwardDocument", cfg.SSMDocument)
	}
	if cfg.TargetHost != "" || cfg.TargetPort != "" {
		t.Fatalf("target should be left to the document: %+v", cfg)
	}
}

// TestValidateSSMTunnel exercises target_host and target_port requirements for
// the default SSM document and the relaxed rules when a custom document is used.
func TestValidateSSMTunnel(t *testing.T) {
	const (
		hostRequired = "target_host is required for the default SSM port-forwarding document"
		portRequired = "target_port is required for the default SSM port-forwarding document"
	)

	tests := []struct {
		name          string
		targetHost    types.String
		targetPort    types.Int64
		ssmDocument   types.String
		wantSummaries []string
	}{
		{
			name:        "default document with host and port is valid",
			targetHost:  types.StringValue("db.internal"),
			targetPort:  types.Int64Value(5432),
			ssmDocument: types.StringNull(),
		},
		{
			name:        "explicit default document with host and port is valid",
			targetHost:  types.StringValue("db.internal"),
			targetPort:  types.Int64Value(5432),
			ssmDocument: types.StringValue(ssm.DefaultSSMDocument),
		},
		{
			name:        "custom document without host or port is valid",
			ssmDocument: types.StringValue("My-Custom-PortForwardDoc"),
		},
		{
			name:        "custom document with host and port is valid",
			targetHost:  types.StringValue("db.internal"),
			targetPort:  types.Int64Value(5432),
			ssmDocument: types.StringValue("My-Custom-PortForwardDoc"),
		},
		{
			name:          "default document without host",
			targetPort:    types.Int64Value(5432),
			ssmDocument:   types.StringNull(),
			wantSummaries: []string{hostRequired},
		},
		{
			name:          "default document without port",
			targetHost:    types.StringValue("db.internal"),
			ssmDocument:   types.StringNull(),
			wantSummaries: []string{portRequired},
		},
		{
			name:          "default document without host or port",
			ssmDocument:   types.StringNull(),
			wantSummaries: []string{hostRequired, portRequired},
		},
		{
			name:          "default document with empty host",
			targetHost:    types.StringValue(""),
			targetPort:    types.Int64Value(5432),
			ssmDocument:   types.StringNull(),
			wantSummaries: []string{hostRequired},
		},
		{
			name:          "default document with zero port",
			targetHost:    types.StringValue("db.internal"),
			targetPort:    types.Int64Value(0),
			ssmDocument:   types.StringNull(),
			wantSummaries: []string{portRequired},
		},
		{
			name:          "explicit default document without host or port",
			ssmDocument:   types.StringValue(ssm.DefaultSSMDocument),
			wantSummaries: []string{hostRequired, portRequired},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := SSMModel{
				SSMDocument: tt.ssmDocument,
				TargetHost:  tt.targetHost,
				TargetPort:  tt.targetPort,
			}

			// Each missing attribute gets its own titled diagnostic, so assert on
			// the summaries Terraform will surface, in order.
			var summaries []string
			for _, d := range validateSSMTunnel(&data).Errors() {
				summaries = append(summaries, d.Summary())
				if !strings.Contains(d.Detail(), ssm.DefaultSSMDocument) {
					t.Errorf("detail %q does not name the default document", d.Detail())
				}
			}
			if !reflect.DeepEqual(summaries, tt.wantSummaries) {
				t.Fatalf("summaries = %v, want %v", summaries, tt.wantSummaries)
			}
		})
	}
}

// Validation has to reach the caller through the config builder, or a rejected
// configuration would still be forked.
func TestSSMTunnelConfigSurfacesValidationDiagnostics(t *testing.T) {
	data := minimalSSMModel()
	data.TargetHost = types.StringNull()

	if _, diags := ssmTunnelConfig(&data); !diags.HasError() {
		t.Fatal("expected validation diagnostics, got none")
	}
}

func TestSSMTargetPortString(t *testing.T) {
	tests := []struct {
		name string
		port types.Int64
		want string
	}{
		{
			name: "null port",
			port: types.Int64Null(),
			want: "",
		},
		{
			name: "zero port",
			port: types.Int64Value(0),
			want: "",
		},
		{
			name: "valid port",
			port: types.Int64Value(5432),
			want: "5432",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ssmTargetPortString(tt.port); got != tt.want {
				t.Fatalf("ssmTargetPortString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestApplySSMSDKConfigRecordsResolvedValues(t *testing.T) {
	data := minimalSSMModel()
	cfg := ssm.TunnelConfig{}
	awsCfg := aws.Config{
		Region: "us-east-1",
		ConfigSources: []any{awsconfig.SharedConfig{
			Profile: "tunnel-profile",
			RoleARN: "arn:aws:iam::123456789012:role/from-shared-config",
		}},
	}

	applySSMSDKConfig(&data, &cfg, awsCfg)

	if cfg.SSMRegion != "us-east-1" || cfg.SSMProfile != "tunnel-profile" {
		t.Fatalf("tunnel config not back-filled: %+v", cfg)
	}
	if cfg.SSMRoleARN != "arn:aws:iam::123456789012:role/from-shared-config" {
		t.Fatalf("role ARN = %q, want the shared config role", cfg.SSMRoleARN)
	}
	// These attributes are Optional+Computed, so state has to record what the
	// credential chain actually resolved.
	if data.SSMRegion.ValueString() != cfg.SSMRegion ||
		data.SSMProfile.ValueString() != cfg.SSMProfile ||
		data.SSMRoleARN.ValueString() != cfg.SSMRoleARN {
		t.Fatalf("model not back-filled: %+v", data)
	}
}

func TestApplySSMSDKConfigKeepsExplicitRoleARN(t *testing.T) {
	const explicit = "arn:aws:iam::123456789012:role/explicit"
	data := minimalSSMModel()
	data.SSMRoleARN = types.StringValue(explicit)
	cfg := ssm.TunnelConfig{SSMRoleARN: explicit}
	awsCfg := aws.Config{
		Region: "us-east-1",
		ConfigSources: []any{awsconfig.SharedConfig{
			Profile: "tunnel-profile",
			RoleARN: "arn:aws:iam::123456789012:role/from-shared-config",
		}},
	}

	applySSMSDKConfig(&data, &cfg, awsCfg)

	if cfg.SSMRoleARN != explicit {
		t.Fatalf("role ARN = %q, want the explicitly configured %q", cfg.SSMRoleARN, explicit)
	}
	if data.SSMRoleARN.ValueString() != explicit {
		t.Fatalf("model role ARN = %q, want %q", data.SSMRoleARN.ValueString(), explicit)
	}
}
