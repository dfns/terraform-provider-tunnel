package kubernetes

import (
	"os"
	"path/filepath"
	"testing"
)

const testKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: a
  cluster: {server: "https://a.example"}
- name: b
  cluster: {server: "https://b.example"}
users:
- name: u
  user: {token: t}
contexts:
- name: a
  context: {cluster: a, user: u}
- name: b
  context: {cluster: b, user: u}
current-context: a
`

// writeHomeKubeconfig isolates the home directory and kube env vars, then
// writes a kubeconfig reachable as ~/.kube/config.
func writeHomeKubeconfig(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	for _, env := range []string{"KUBECONFIG", "KUBE_CONFIG_PATH", "KUBE_CONFIG_PATHS", "KUBE_CTX", "KUBE_CTX_AUTH_INFO", "KUBE_CTX_CLUSTER"} {
		t.Setenv(env, "")
	}
	if err := os.MkdirAll(filepath.Join(home, ".kube"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".kube", "config"), []byte(testKubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRestConfigSelectsKubeconfig(t *testing.T) {
	tests := []struct {
		name     string
		cfg      TunnelConfig
		env      map[string]string
		wantHost string
	}{
		{
			name:     "config_path expands home",
			cfg:      TunnelConfig{ConfigPath: "~/.kube/config"},
			wantHost: "https://a.example",
		},
		{
			name:     "config_paths expands home",
			cfg:      TunnelConfig{ConfigPaths: []string{"~/.kube/config"}, ConfigContext: "b"},
			wantHost: "https://b.example",
		},
		{
			name:     "KUBE_CONFIG_PATH and KUBE_CTX back unset attributes",
			env:      map[string]string{"KUBE_CONFIG_PATH": "~/.kube/config", "KUBE_CTX": "b"},
			wantHost: "https://b.example",
		},
		{
			name:     "KUBE_CONFIG_PATHS and KUBE_CTX_CLUSTER back unset attributes",
			env:      map[string]string{"KUBE_CONFIG_PATHS": "~/.kube/config", "KUBE_CTX_CLUSTER": "b"},
			wantHost: "https://b.example",
		},
		{
			name:     "attributes win over env",
			cfg:      TunnelConfig{ConfigPath: "~/.kube/config", ConfigContext: "a"},
			env:      map[string]string{"KUBE_CONFIG_PATH": "/missing", "KUBE_CTX": "b"},
			wantHost: "https://a.example",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeHomeKubeconfig(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			restCfg, err := tt.cfg.restConfig()
			if err != nil {
				t.Fatalf("restConfig: %v", err)
			}
			if restCfg.Host != tt.wantHost {
				t.Fatalf("host = %s, want %s", restCfg.Host, tt.wantHost)
			}
		})
	}
}
