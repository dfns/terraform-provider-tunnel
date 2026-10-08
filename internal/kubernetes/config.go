package kubernetes

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

type TunnelConfig struct {
	Namespace   string
	ServiceName string
	TargetPort  int
	LocalHost   string
	LocalPort   int

	// Kubernetes Configuration
	Host                  string
	Username              string
	Password              string
	Insecure              bool
	TLSServerName         string
	ClientCertificate     string
	ClientKey             string
	ClusterCACertificate  string
	ConfigPaths           []string
	ConfigPath            string
	ConfigContext         string
	ConfigContextAuthInfo string
	ConfigContextCluster  string
	Token                 string
	ProxyURL              string
	Exec                  *ExecConfig
}

type ExecConfig struct {
	APIVersion string
	Command    string
	Env        map[string]string
	Args       []string
}

// restConfig assembles the client configuration from kubeconfig files plus the
// explicit overrides carried in the tunnel config. Like the hashicorp/kubernetes
// provider, unset kubeconfig attributes fall back to KUBE_* env vars.
func (c TunnelConfig) restConfig() (*rest.Config, error) {
	configPaths, configPath := c.ConfigPaths, c.ConfigPath
	if len(configPaths) == 0 && configPath == "" {
		configPaths = filepath.SplitList(os.Getenv("KUBE_CONFIG_PATHS"))
		configPath = os.Getenv("KUBE_CONFIG_PATH")
	}

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if len(configPaths) > 0 {
		loadingRules.Precedence = make([]string, len(configPaths))
		for i, path := range configPaths {
			loadingRules.Precedence[i] = expandHome(path)
		}
	} else if configPath != "" {
		loadingRules.ExplicitPath = expandHome(configPath)
	}

	overrides := &clientcmd.ConfigOverrides{
		CurrentContext: cmp.Or(c.ConfigContext, os.Getenv("KUBE_CTX")),
		Context: clientcmdapi.Context{
			AuthInfo: cmp.Or(c.ConfigContextAuthInfo, os.Getenv("KUBE_CTX_AUTH_INFO")),
			Cluster:  cmp.Or(c.ConfigContextCluster, os.Getenv("KUBE_CTX_CLUSTER")),
		},
	}
	if c.Token != "" {
		overrides.AuthInfo.Token = c.Token
	}
	if c.Username != "" {
		overrides.AuthInfo.Username = c.Username
	}
	if c.Password != "" {
		overrides.AuthInfo.Password = c.Password
	}
	if c.ClientCertificate != "" {
		overrides.AuthInfo.ClientCertificateData = []byte(c.ClientCertificate)
	}
	if c.ClientKey != "" {
		overrides.AuthInfo.ClientKeyData = []byte(c.ClientKey)
	}
	if c.ClusterCACertificate != "" {
		overrides.ClusterInfo.CertificateAuthorityData = []byte(c.ClusterCACertificate)
	}
	if c.Host != "" {
		overrides.ClusterInfo.Server = c.Host
	}
	if c.Insecure {
		overrides.ClusterInfo.InsecureSkipTLSVerify = true
	}
	if c.TLSServerName != "" {
		overrides.ClusterInfo.TLSServerName = c.TLSServerName
	}
	if c.ProxyURL != "" {
		overrides.ClusterInfo.ProxyURL = c.ProxyURL
	}
	if c.Exec != nil {
		overrides.AuthInfo.Exec = &clientcmdapi.ExecConfig{
			APIVersion:      c.Exec.APIVersion,
			Command:         c.Exec.Command,
			Args:            c.Exec.Args,
			Env:             make([]clientcmdapi.ExecEnvVar, 0, len(c.Exec.Env)),
			InteractiveMode: clientcmdapi.IfAvailableExecInteractiveMode,
		}
		for k, v := range c.Exec.Env {
			overrides.AuthInfo.Exec.Env = append(overrides.AuthInfo.Exec.Env, clientcmdapi.ExecEnvVar{
				Name:  k,
				Value: v,
			})
		}
	}

	clientConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	return clientConfig, nil
}

// expandHome resolves a leading ~ since no shell sees these paths.
func expandHome(path string) string {
	rest, ok := strings.CutPrefix(path, "~")
	if !ok || (rest != "" && !os.IsPathSeparator(rest[0])) {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, rest)
}
