package runner

import (
	"os"
	"strings"
	"testing"

	"github.com/dfns/terraform-provider-tunnel/internal/libs"
)

// TestStartTunnelErrors guards the binary's entry-point dispatch: a tunnel is
// only started once the config env, the parent PID argument and the tunnel type
// are all present and valid. The successful fork path is covered in the
// integration and e2e suites.
func TestStartTunnelErrors(t *testing.T) {
	tests := []struct {
		name    string
		tun     string
		conf    string
		setConf bool
		args    []string
		wantErr string
	}{
		{
			name:    "missing tunnel configuration",
			tun:     "ssh",
			setConf: false,
			args:    []string{"terraform-provider-tunnel", "1"},
			wantErr: "missing tunnel configuration",
		},
		{
			name:    "missing parent PID",
			tun:     "ssh",
			conf:    "{}",
			setConf: true,
			args:    []string{"terraform-provider-tunnel"},
			wantErr: "missing parent PID",
		},
		{
			name:    "invalid parent PID",
			tun:     "ssh",
			conf:    "{}",
			setConf: true,
			args:    []string{"terraform-provider-tunnel", "not-a-number"},
			wantErr: "invalid parent PID",
		},
		{
			name:    "unknown tunnel type",
			tun:     "bogus",
			conf:    "{}",
			setConf: true,
			args:    []string{"terraform-provider-tunnel", "1"},
			wantErr: "unknown tunnel type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setConf {
				t.Setenv(libs.TunnelConfEnv, tt.conf)
			} else {
				prev, had := os.LookupEnv(libs.TunnelConfEnv)
				_ = os.Unsetenv(libs.TunnelConfEnv)
				t.Cleanup(func() {
					if had {
						_ = os.Setenv(libs.TunnelConfEnv, prev)
					}
				})
			}

			origArgs := os.Args
			os.Args = tt.args
			t.Cleanup(func() { os.Args = origArgs })

			err := StartTunnel(tt.tun)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}
