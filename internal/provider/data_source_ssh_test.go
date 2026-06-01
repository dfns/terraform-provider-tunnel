package provider

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestValidateSSHTarget exercises every branch of the target validation: the
// two valid shapes (host+port, socket) and the three rejected ones (both set,
// neither set, port without a host).
func TestValidateSSHTarget(t *testing.T) {
	tests := []struct {
		name       string
		targetHost types.String
		socket     types.String
		port       types.Int64
		wantErr    string
	}{
		{
			name:       "port with host is valid",
			targetHost: types.StringValue("db.internal"),
			socket:     types.StringNull(),
			port:       types.Int64Value(5432),
		},
		{
			name:       "socket only is valid",
			targetHost: types.StringNull(),
			socket:     types.StringValue("/var/run/app.sock"),
			port:       types.Int64Null(),
		},
		{
			name:       "port and socket are mutually exclusive",
			targetHost: types.StringValue("db.internal"),
			socket:     types.StringValue("/var/run/app.sock"),
			port:       types.Int64Value(5432),
			wantErr:    "mutually exclusive",
		},
		{
			name:       "neither port nor socket set",
			targetHost: types.StringValue("db.internal"),
			socket:     types.StringNull(),
			port:       types.Int64Null(),
			wantErr:    "must be set",
		},
		{
			name:       "port without host",
			targetHost: types.StringNull(),
			socket:     types.StringNull(),
			port:       types.Int64Value(5432),
			wantErr:    "`target_host` is required",
		},
		{
			name:       "port with empty host",
			targetHost: types.StringValue(""),
			socket:     types.StringNull(),
			port:       types.Int64Value(5432),
			wantErr:    "`target_host` is required",
		},
		{
			name:       "empty socket counts as unset",
			targetHost: types.StringNull(),
			socket:     types.StringValue(""),
			port:       types.Int64Null(),
			wantErr:    "must be set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSSHTarget(tt.targetHost, tt.socket, tt.port)
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
