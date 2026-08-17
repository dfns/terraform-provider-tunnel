package provider

import (
	"os/user"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestValidateSSHTarget exercises every branch of SSH target validation: the
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
			diags := validateSSHTarget(tt.targetHost, tt.socket, tt.port)
			switch {
			case tt.wantErr == "" && diags.HasError():
				t.Fatalf("unexpected diagnostics: %v", diags)
			case tt.wantErr != "" && !diags.HasError():
				t.Fatalf("expected a diagnostic mentioning %q, got none", tt.wantErr)
			case tt.wantErr != "":
				if detail := diags.Errors()[0].Detail(); !strings.Contains(detail, tt.wantErr) {
					t.Fatalf("diagnostic detail %q does not contain %q", detail, tt.wantErr)
				}
			}
		})
	}
}

func TestSSHConfigDefaults(t *testing.T) {
	currentUser, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	data := SSHModel{
		LocalHost:    types.StringNull(),
		LocalPort:    types.Int64Value(15432),
		SSHHost:      types.StringValue("bastion.internal"),
		SSHPort:      types.Int64Null(),
		SSHUser:      types.StringNull(),
		TargetHost:   types.StringValue("db.internal"),
		TargetPort:   types.Int64Value(5432),
		TargetSocket: types.StringNull(),
	}

	cfg, diags := sshConfig(&data)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if cfg.LocalHost != "localhost" || cfg.LocalPort != 15432 {
		t.Fatalf("local endpoint = %s:%d, want localhost:15432", cfg.LocalHost, cfg.LocalPort)
	}
	if cfg.SSHHost != "bastion.internal" || cfg.SSHPort != 22 || cfg.SSHUser != currentUser.Username {
		t.Fatalf("SSH endpoint defaults not applied: %+v", cfg)
	}
	if cfg.TargetHost != "db.internal" || cfg.TargetPort != 5432 || cfg.TargetSocket != "" {
		t.Fatalf("target not mapped: %+v", cfg)
	}
	if data.LocalHost.ValueString() != cfg.LocalHost ||
		data.LocalPort.ValueInt64() != int64(cfg.LocalPort) ||
		data.SSHPort.ValueInt64() != int64(cfg.SSHPort) ||
		data.SSHUser.ValueString() != cfg.SSHUser {
		t.Fatalf("model not updated: %+v", data)
	}
}
