package kubernetes

import (
	"fmt"

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
// explicit overrides carried in the tunnel config.
func (c TunnelConfig) restConfig() (*rest.Config, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if len(c.ConfigPaths) > 0 {
		loadingRules.Precedence = c.ConfigPaths
	} else if c.ConfigPath != "" {
		loadingRules.ExplicitPath = c.ConfigPath
	}

	overrides := &clientcmd.ConfigOverrides{}
	if c.ConfigContext != "" {
		overrides.CurrentContext = c.ConfigContext
	}
	if c.ConfigContextAuthInfo != "" {
		overrides.Context.AuthInfo = c.ConfigContextAuthInfo
	}
	if c.ConfigContextCluster != "" {
		overrides.Context.Cluster = c.ConfigContextCluster
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
