package provider

import (
	"context"

	k8s "github.com/dfns/terraform-provider-tunnel/internal/kubernetes"
	"github.com/dfns/terraform-provider-tunnel/internal/libs"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type KubernetesModel struct {
	Namespace   types.String           `tfsdk:"namespace"`
	ServiceName types.String           `tfsdk:"service_name"`
	TargetPort  types.Int64            `tfsdk:"target_port"`
	LocalPort   types.Int64            `tfsdk:"local_port"`
	LocalHost   types.String           `tfsdk:"local_host"`
	Kubernetes  *KubernetesConfigModel `tfsdk:"kubernetes"`
}

type KubernetesConfigModel struct {
	Host                  types.String     `tfsdk:"host"`
	Username              types.String     `tfsdk:"username"`
	Password              types.String     `tfsdk:"password"`
	Insecure              types.Bool       `tfsdk:"insecure"`
	TLSServerName         types.String     `tfsdk:"tls_server_name"`
	ClientCertificate     types.String     `tfsdk:"client_certificate"`
	ClientKey             types.String     `tfsdk:"client_key"`
	ClusterCACertificate  types.String     `tfsdk:"cluster_ca_certificate"`
	ConfigPaths           types.List       `tfsdk:"config_paths"`
	ConfigPath            types.String     `tfsdk:"config_path"`
	ConfigContext         types.String     `tfsdk:"config_context"`
	ConfigContextAuthInfo types.String     `tfsdk:"config_context_auth_info"`
	ConfigContextCluster  types.String     `tfsdk:"config_context_cluster"`
	Token                 types.String     `tfsdk:"token"`
	ProxyURL              types.String     `tfsdk:"proxy_url"`
	Exec                  *ExecConfigModel `tfsdk:"exec"`
}

type ExecConfigModel struct {
	APIVersion types.String `tfsdk:"api_version"`
	Command    types.String `tfsdk:"command"`
	Env        types.Map    `tfsdk:"env"`
	Args       types.List   `tfsdk:"args"`
}

// kubernetesConfig builds the tunnel config and writes back the local endpoint
// the tunnel will bind, which Terraform records as computed state.
func kubernetesConfig(ctx context.Context, data *KubernetesModel) (k8s.TunnelConfig, diag.Diagnostics) {
	var diags diag.Diagnostics

	localPort := int(data.LocalPort.ValueInt64())
	if localPort == 0 {
		var err error
		localPort, err = libs.GetFreePort()
		if err != nil {
			diags.AddError("Failed to find open local port", err.Error())
			return k8s.TunnelConfig{}, diags
		}
	}
	data.LocalPort = types.Int64Value(int64(localPort))

	if data.LocalHost.IsNull() || data.LocalHost.ValueString() == "" {
		data.LocalHost = types.StringValue("localhost")
	}

	cfg := k8s.TunnelConfig{
		Namespace:   data.Namespace.ValueString(),
		ServiceName: data.ServiceName.ValueString(),
		TargetPort:  int(data.TargetPort.ValueInt64()),
		LocalHost:   data.LocalHost.ValueString(),
		LocalPort:   localPort,
	}

	if data.Kubernetes == nil {
		return cfg, diags
	}
	kube := data.Kubernetes

	cfg.Host = kube.Host.ValueString()
	cfg.Username = kube.Username.ValueString()
	cfg.Password = kube.Password.ValueString()
	cfg.Insecure = kube.Insecure.ValueBool()
	cfg.TLSServerName = kube.TLSServerName.ValueString()
	cfg.ClientCertificate = kube.ClientCertificate.ValueString()
	cfg.ClientKey = kube.ClientKey.ValueString()
	cfg.ClusterCACertificate = kube.ClusterCACertificate.ValueString()
	cfg.ConfigPath = kube.ConfigPath.ValueString()
	cfg.ConfigContext = kube.ConfigContext.ValueString()
	cfg.ConfigContextAuthInfo = kube.ConfigContextAuthInfo.ValueString()
	cfg.ConfigContextCluster = kube.ConfigContextCluster.ValueString()
	cfg.Token = kube.Token.ValueString()
	cfg.ProxyURL = kube.ProxyURL.ValueString()

	if !kube.ConfigPaths.IsNull() {
		var paths []string
		diags.Append(kube.ConfigPaths.ElementsAs(ctx, &paths, false)...)
		if diags.HasError() {
			return k8s.TunnelConfig{}, diags
		}
		cfg.ConfigPaths = paths
	}

	if kube.Exec != nil && !kube.Exec.APIVersion.IsNull() && !kube.Exec.Command.IsNull() {
		execCfg := &k8s.ExecConfig{
			APIVersion: kube.Exec.APIVersion.ValueString(),
			Command:    kube.Exec.Command.ValueString(),
		}

		if !kube.Exec.Env.IsNull() {
			var env map[string]string
			diags.Append(kube.Exec.Env.ElementsAs(ctx, &env, false)...)
			if diags.HasError() {
				return k8s.TunnelConfig{}, diags
			}
			execCfg.Env = env
		}

		if !kube.Exec.Args.IsNull() {
			var args []string
			diags.Append(kube.Exec.Args.ElementsAs(ctx, &args, false)...)
			if diags.HasError() {
				return k8s.TunnelConfig{}, diags
			}
			execCfg.Args = args
		}
		cfg.Exec = execCfg
	}

	return cfg, diags
}
