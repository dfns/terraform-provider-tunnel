package runner

import (
	"os"
	"strings"
	"testing"
)

// TestStartTunnelErrors guards the binary's entry-point dispatch: a tunnel is
// only started once the stdin config, the parent PID argument and the tunnel type
// are all present and valid. The successful fork path is covered in the
// integration and e2e suites.
func TestStartTunnelErrors(t *testing.T) {
	tests := []struct {
		name    string
		tun     string
		conf    string
		args    []string
		wantErr string
	}{
		{
			name:    "missing tunnel configuration",
			tun:     "ssh",
			args:    []string{"terraform-provider-tunnel", "1"},
			wantErr: "missing tunnel configuration",
		},
		{
			name:    "missing parent PID",
			tun:     "ssh",
			conf:    "{}",
			args:    []string{"terraform-provider-tunnel"},
			wantErr: "missing parent PID",
		},
		{
			name:    "invalid parent PID",
			tun:     "ssh",
			conf:    "{}",
			args:    []string{"terraform-provider-tunnel", "not-a-number"},
			wantErr: "invalid parent PID",
		},
		{
			name:    "unknown tunnel type",
			tun:     "bogus",
			conf:    "{}",
			args:    []string{"terraform-provider-tunnel", "1"},
			wantErr: "unknown tunnel type",
		},
		{
			name:    "Azure Bastion dispatch",
			tun:     "azure_bastion",
			conf:    "{}",
			args:    []string{"terraform-provider-tunnel", "1"},
			wantErr: "bastion_host_id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origArgs := os.Args
			os.Args = tt.args
			t.Cleanup(func() { os.Args = origArgs })

			err := StartTunnel(tt.tun, strings.NewReader(tt.conf))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}
