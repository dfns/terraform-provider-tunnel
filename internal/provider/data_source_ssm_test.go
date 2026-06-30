package provider

import (
	"strings"
	"testing"

	"github.com/dfns/terraform-provider-tunnel/internal/ssm"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestValidateSSMTarget exercises target_host requirements for the default SSM
// document and the relaxed rules when a custom document is used.
func TestValidateSSMTarget(t *testing.T) {
	tests := []struct {
		name        string
		targetHost  types.String
		ssmDocument types.String
		wantErr     string
	}{
		{
			name:        "default document with host is valid",
			targetHost:  types.StringValue("db.internal"),
			ssmDocument: types.StringNull(),
		},
		{
			name:        "explicit default document with host is valid",
			targetHost:  types.StringValue("db.internal"),
			ssmDocument: types.StringValue(ssm.DefaultSSMDocument),
		},
		{
			name:        "custom document without host is valid",
			ssmDocument: types.StringValue("My-Custom-PortForwardDoc"),
		},
		{
			name:        "custom document with host is valid",
			targetHost:  types.StringValue("db.internal"),
			ssmDocument: types.StringValue("My-Custom-PortForwardDoc"),
		},
		{
			name:        "default document without host",
			ssmDocument: types.StringNull(),
			wantErr:     "`target_host` is required",
		},
		{
			name:        "default document with empty host",
			targetHost:  types.StringValue(""),
			ssmDocument: types.StringNull(),
			wantErr:     "`target_host` is required",
		},
		{
			name:        "explicit default document without host",
			ssmDocument: types.StringValue(ssm.DefaultSSMDocument),
			wantErr:     "`target_host` is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSSMTarget(tt.targetHost, tt.ssmDocument)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}
