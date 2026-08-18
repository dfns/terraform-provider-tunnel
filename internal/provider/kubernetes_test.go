package provider

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func minimalKubernetesModel() KubernetesModel {
	return KubernetesModel{
		Namespace:   types.StringValue("default"),
		ServiceName: types.StringValue("my-service"),
		TargetPort:  types.Int64Value(80),
		LocalPort:   types.Int64Value(18080),
		LocalHost:   types.StringNull(),
	}
}

func TestKubernetesConfigDefaults(t *testing.T) {
	data := minimalKubernetesModel()

	cfg, diags := kubernetesConfig(context.Background(), &data)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if cfg.LocalHost != "localhost" || cfg.LocalPort != 18080 {
		t.Fatalf("local endpoint = %s:%d, want localhost:18080", cfg.LocalHost, cfg.LocalPort)
	}
	if cfg.Namespace != "default" || cfg.ServiceName != "my-service" || cfg.TargetPort != 80 {
		t.Fatalf("target not mapped: %+v", cfg)
	}
	if data.LocalHost.ValueString() != cfg.LocalHost || data.LocalPort.ValueInt64() != int64(cfg.LocalPort) {
		t.Fatalf("model not updated: %+v", data)
	}
}

func TestKubernetesConfigRejectsInvalidTargetPort(t *testing.T) {
	for _, port := range []int64{-1, 0, 65536, 4294967297} {
		t.Run(fmt.Sprintf("port_%d", port), func(t *testing.T) {
			data := minimalKubernetesModel()
			data.TargetPort = types.Int64Value(port)

			cfg, diags := kubernetesConfig(context.Background(), &data)
			if !diags.HasError() {
				t.Fatalf("target_port %d accepted with config %+v", port, cfg)
			}
			if !strings.Contains(diags.Errors()[0].Detail(), "between 1 and 65535") {
				t.Fatalf("diagnostic = %v, want valid port range", diags)
			}
		})
	}
}

func TestKubernetesConfigRejectsInvalidLocalPort(t *testing.T) {
	for _, port := range []int64{-1, 65536, 4294967297} {
		t.Run(fmt.Sprintf("port_%d", port), func(t *testing.T) {
			data := minimalKubernetesModel()
			data.LocalPort = types.Int64Value(port)

			cfg, diags := kubernetesConfig(context.Background(), &data)
			if !diags.HasError() {
				t.Fatalf("local_port %d accepted with config %+v", port, cfg)
			}
			if !strings.Contains(diags.Errors()[0].Detail(), "between 1 and 65535") {
				t.Fatalf("diagnostic = %v, want valid port range", diags)
			}
		})
	}
}

// An empty local_host would otherwise bind every interface, exposing the tunnel
// beyond the machine running Terraform.
func TestKubernetesConfigRejectsEmptyLocalHost(t *testing.T) {
	data := minimalKubernetesModel()
	data.LocalHost = types.StringValue("")

	cfg, diags := kubernetesConfig(context.Background(), &data)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if cfg.LocalHost != "localhost" {
		t.Fatalf("local host = %q, want localhost", cfg.LocalHost)
	}
	if data.LocalHost.ValueString() != "localhost" {
		t.Fatalf("model local host = %q, want localhost", data.LocalHost.ValueString())
	}
}

func TestKubernetesConfigAllocatesFreeLocalPort(t *testing.T) {
	for _, tt := range []struct {
		name      string
		localPort types.Int64
	}{
		{name: "zero", localPort: types.Int64Value(0)},
		{name: "null", localPort: types.Int64Null()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := minimalKubernetesModel()
			data.LocalPort = tt.localPort

			cfg, diags := kubernetesConfig(context.Background(), &data)
			if diags.HasError() {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			if cfg.LocalPort == 0 {
				t.Fatal("no local port allocated")
			}
			// Terraform persists the model as computed state, so it must carry the
			// port the tunnel actually binds.
			if data.LocalPort.ValueInt64() != int64(cfg.LocalPort) {
				t.Fatalf("model local port = %d, want %d", data.LocalPort.ValueInt64(), cfg.LocalPort)
			}
		})
	}
}

func TestKubernetesConfigMapsClusterCredentials(t *testing.T) {
	data := minimalKubernetesModel()
	data.Kubernetes = &KubernetesConfigModel{
		Host:                  types.StringValue("https://cluster.internal"),
		Username:              types.StringValue("admin"),
		Password:              types.StringValue("secret"),
		Insecure:              types.BoolValue(true),
		TLSServerName:         types.StringValue("cluster.example.com"),
		ClientCertificate:     types.StringValue("client-cert"),
		ClientKey:             types.StringValue("client-key"),
		ClusterCACertificate:  types.StringValue("ca-cert"),
		ConfigPath:            types.StringValue("~/.kube/config"),
		ConfigContext:         types.StringValue("my-context"),
		ConfigContextAuthInfo: types.StringValue("my-user"),
		ConfigContextCluster:  types.StringValue("my-cluster"),
		Token:                 types.StringValue("token"),
		ProxyURL:              types.StringValue("http://proxy.internal:3128"),
		ConfigPaths: types.ListValueMust(types.StringType, []attr.Value{
			types.StringValue("/a/config"),
			types.StringValue("/b/config"),
		}),
	}

	cfg, diags := kubernetesConfig(context.Background(), &data)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if cfg.Host != "https://cluster.internal" || cfg.Username != "admin" || cfg.Password != "secret" {
		t.Fatalf("endpoint credentials not mapped: %+v", cfg)
	}
	if !cfg.Insecure || cfg.TLSServerName != "cluster.example.com" {
		t.Fatalf("TLS settings not mapped: %+v", cfg)
	}
	if cfg.ClientCertificate != "client-cert" || cfg.ClientKey != "client-key" || cfg.ClusterCACertificate != "ca-cert" {
		t.Fatalf("certificates not mapped: %+v", cfg)
	}
	if cfg.ConfigPath != "~/.kube/config" || cfg.ConfigContext != "my-context" ||
		cfg.ConfigContextAuthInfo != "my-user" || cfg.ConfigContextCluster != "my-cluster" {
		t.Fatalf("kubeconfig selection not mapped: %+v", cfg)
	}
	if cfg.Token != "token" || cfg.ProxyURL != "http://proxy.internal:3128" {
		t.Fatalf("token or proxy not mapped: %+v", cfg)
	}
	if !reflect.DeepEqual(cfg.ConfigPaths, []string{"/a/config", "/b/config"}) {
		t.Fatalf("config paths = %v, want [/a/config /b/config]", cfg.ConfigPaths)
	}
	if cfg.Exec != nil {
		t.Fatalf("exec config = %+v, want nil", cfg.Exec)
	}
}

func TestKubernetesConfigMapsExecPlugin(t *testing.T) {
	data := minimalKubernetesModel()
	data.Kubernetes = &KubernetesConfigModel{
		ConfigPaths: types.ListNull(types.StringType),
		Exec: &ExecConfigModel{
			APIVersion: types.StringValue("client.authentication.k8s.io/v1beta1"),
			Command:    types.StringValue("aws"),
			Env: types.MapValueMust(types.StringType, map[string]attr.Value{
				"AWS_PROFILE": types.StringValue("prod"),
			}),
			Args: types.ListValueMust(types.StringType, []attr.Value{
				types.StringValue("eks"),
				types.StringValue("get-token"),
			}),
		},
	}

	cfg, diags := kubernetesConfig(context.Background(), &data)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if cfg.Exec == nil {
		t.Fatal("exec config not mapped")
	}
	if cfg.Exec.APIVersion != "client.authentication.k8s.io/v1beta1" || cfg.Exec.Command != "aws" {
		t.Fatalf("exec plugin not mapped: %+v", cfg.Exec)
	}
	if !reflect.DeepEqual(cfg.Exec.Env, map[string]string{"AWS_PROFILE": "prod"}) {
		t.Fatalf("exec env = %v, want map[AWS_PROFILE:prod]", cfg.Exec.Env)
	}
	if !reflect.DeepEqual(cfg.Exec.Args, []string{"eks", "get-token"}) {
		t.Fatalf("exec args = %v, want [eks get-token]", cfg.Exec.Args)
	}
}

// An exec block missing either required field is ignored rather than passed to
// client-go as a half-built plugin invocation.
func TestKubernetesConfigSkipsIncompleteExecPlugin(t *testing.T) {
	for _, tt := range []struct {
		name       string
		apiVersion types.String
		command    types.String
	}{
		{name: "no api version", apiVersion: types.StringNull(), command: types.StringValue("aws")},
		{name: "no command", apiVersion: types.StringValue("v1beta1"), command: types.StringNull()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := minimalKubernetesModel()
			data.Kubernetes = &KubernetesConfigModel{
				ConfigPaths: types.ListNull(types.StringType),
				Exec: &ExecConfigModel{
					APIVersion: tt.apiVersion,
					Command:    tt.command,
					Env:        types.MapNull(types.StringType),
					Args:       types.ListNull(types.StringType),
				},
			}

			cfg, diags := kubernetesConfig(context.Background(), &data)
			if diags.HasError() {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			if cfg.Exec != nil {
				t.Fatalf("exec config = %+v, want nil", cfg.Exec)
			}
		})
	}
}
