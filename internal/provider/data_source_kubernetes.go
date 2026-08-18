package provider

import (
	"context"

	k8s "github.com/dfns/terraform-provider-tunnel/internal/kubernetes"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSource = &KubernetesDataSource{}
)

func NewKubernetesDataSource() datasource.DataSource {
	return &KubernetesDataSource{}
}

type KubernetesDataSource struct{}

func (d *KubernetesDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes"
}

func (d *KubernetesDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Opens a port forward to a Kubernetes service.",
		Attributes: map[string]schema.Attribute{
			"namespace": schema.StringAttribute{
				Description: "The namespace of the service.",
				Required:    true,
			},
			"service_name": schema.StringAttribute{
				Description: "The name of the service to forward ports to. One ready pod is selected when the tunnel starts and is not re-selected, so the tunnel stops forwarding if that pod goes away.",
				Required:    true,
			},
			"target_port": schema.Int64Attribute{
				Description: "The port exposed by the service, between 1 and 65535. It is resolved to the pod's numeric or named `targetPort`; a port the service does not expose is forwarded directly to that port on the pod.",
				Required:    true,
			},
			"local_host": schema.StringAttribute{
				Description: "The local address to listen on (e.g., 127.0.0.1).",
				Optional:    true,
				Computed:    true,
			},
			"local_port": schema.Int64Attribute{
				Description: "The local port to listen on. If 0, a random port will be chosen.",
				Optional:    true,
				Computed:    true,
			},
			"kubernetes": schema.SingleNestedAttribute{
				Optional:    true,
				Description: "Kubernetes Configuration",
				Attributes: map[string]schema.Attribute{
					"host": schema.StringAttribute{
						Optional:    true,
						Description: "The hostname (in form of URI) of kubernetes master",
					},
					"username": schema.StringAttribute{
						Optional:    true,
						Description: "The username to use for HTTP basic authentication when accessing the Kubernetes master endpoint",
					},
					"password": schema.StringAttribute{
						Optional:    true,
						Sensitive:   true,
						Description: "The password to use for HTTP basic authentication when accessing the Kubernetes master endpoint.",
					},
					"insecure": schema.BoolAttribute{
						Optional:    true,
						Description: "Whether server should be accessed without verifying the TLS certificate.",
					},
					"tls_server_name": schema.StringAttribute{
						Optional:    true,
						Description: "Server name passed to the server for SNI and is used in the client to check server certificates against.",
					},
					"client_certificate": schema.StringAttribute{
						Optional:    true,
						Sensitive:   true,
						Description: "PEM-encoded client certificate for TLS authentication.",
					},
					"client_key": schema.StringAttribute{
						Optional:    true,
						Sensitive:   true,
						Description: "PEM-encoded client certificate key for TLS authentication.",
					},
					"cluster_ca_certificate": schema.StringAttribute{
						Optional:    true,
						Sensitive:   true,
						Description: "PEM-encoded root certificates bundle for TLS authentication.",
					},
					"config_paths": schema.ListAttribute{
						Optional:    true,
						ElementType: types.StringType,
						Description: "A list of paths to kube config files. Can be set with KUBE_CONFIG_PATHS environment variable.",
					},
					"config_path": schema.StringAttribute{
						Optional:    true,
						Description: "Path to the kube config file. Can be set with KUBE_CONFIG_PATH.",
					},
					"config_context": schema.StringAttribute{
						Optional:    true,
						Description: "Context to choose from the config file. Can be sourced from KUBE_CTX.",
					},
					"config_context_auth_info": schema.StringAttribute{
						Optional:    true,
						Description: "Authentication info context of the kube config (name of the kubeconfig user, --user flag in kubectl). Can be sourced from KUBE_CTX_AUTH_INFO.",
					},
					"config_context_cluster": schema.StringAttribute{
						Optional:    true,
						Description: "Cluster context of the kube config (name of the kubeconfig cluster, --cluster flag in kubectl). Can be sourced from KUBE_CTX_CLUSTER.",
					},
					"token": schema.StringAttribute{
						Optional:    true,
						Sensitive:   true,
						Description: "Token to authenticate a service account.",
					},
					"proxy_url": schema.StringAttribute{
						Optional:    true,
						Description: "URL to the proxy to be used for all API requests.",
					},
					"exec": schema.SingleNestedAttribute{
						Optional:    true,
						Description: "Exec configuration for Kubernetes authentication",
						Attributes: map[string]schema.Attribute{
							"api_version": schema.StringAttribute{
								Required:    true,
								Description: "API version for the exec plugin.",
							},
							"command": schema.StringAttribute{
								Required:    true,
								Description: "Command to run for Kubernetes exec plugin",
							},
							"env": schema.MapAttribute{
								Optional:    true,
								Sensitive:   true,
								ElementType: types.StringType,
								Description: "Environment variables for the exec plugin",
							},
							"args": schema.ListAttribute{
								Optional:    true,
								Sensitive:   true,
								ElementType: types.StringType,
								Description: "Arguments for the exec plugin",
							},
						},
					},
				},
			},
		},
	}
}

func (d *KubernetesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data KubernetesModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tunnelCfg, diags := kubernetesConfig(ctx, &data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if _, err := k8s.ForkRemoteTunnel(ctx, tunnelCfg); err != nil {
		resp.Diagnostics.AddError("Failed to start tunnel", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
