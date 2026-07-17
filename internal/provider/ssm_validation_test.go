package provider

import (
	"strings"
	"testing"

	"github.com/dfns/terraform-provider-tunnel/internal/ssm"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestValidateSSMTunnel exercises target_host and target_port requirements for the
// default SSM document and the relaxed rules when a custom document is used.
func TestValidateSSMTunnel(t *testing.T) {
	tests := []struct {
		name        string
		targetHost  types.String
		targetPort  types.Int64
		ssmDocument types.String
		wantErrs    []string
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
			name:        "default document without host",
			targetPort:  types.Int64Value(5432),
			ssmDocument: types.StringNull(),
			wantErrs:    []string{"`target_host` is required"},
		},
		{
			name:        "default document without port",
			targetHost:  types.StringValue("db.internal"),
			ssmDocument: types.StringNull(),
			wantErrs:    []string{"`target_port` is required"},
		},
		{
			name:        "default document without host or port",
			ssmDocument: types.StringNull(),
			wantErrs:    []string{"`target_host` is required", "`target_port` is required"},
		},
		{
			name:        "default document with empty host",
			targetHost:  types.StringValue(""),
			targetPort:  types.Int64Value(5432),
			ssmDocument: types.StringNull(),
			wantErrs:    []string{"`target_host` is required"},
		},
		{
			name:        "default document with zero port",
			targetHost:  types.StringValue("db.internal"),
			targetPort:  types.Int64Value(0),
			ssmDocument: types.StringNull(),
			wantErrs:    []string{"`target_port` is required"},
		},
		{
			name:        "explicit default document without host or port",
			ssmDocument: types.StringValue(ssm.DefaultSSMDocument),
			wantErrs:    []string{"`target_host` is required", "`target_port` is required"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateSSMTunnel(tt.targetHost, tt.targetPort, tt.ssmDocument)
			switch {
			case len(tt.wantErrs) == 0 && len(errs) > 0:
				t.Fatalf("unexpected errors: %v", errs)
			case len(tt.wantErrs) > 0 && len(errs) != len(tt.wantErrs):
				t.Fatalf("got %d errors %v, want %d errors containing %v", len(errs), errs, len(tt.wantErrs), tt.wantErrs)
			default:
				for i, wantErr := range tt.wantErrs {
					if !strings.Contains(errs[i].Error(), wantErr) {
						t.Fatalf("error %q does not contain %q", errs[i], wantErr)
					}
				}
			}
		})
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
