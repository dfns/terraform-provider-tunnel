// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// Ensure TunnelProvider satisfies various provider interfaces.
var _ provider.Provider = &TunnelProvider{}

// TunnelProvider defines the provider implementation.
type TunnelProvider struct {
	// version is set to the provider version on release, "dev" when the
	// provider is built and ran locally, and "test" when running acceptance
	// testing.
	version string
}

func (p *TunnelProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "tunnel"
	resp.Version = p.version
}

func (p *TunnelProvider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
}

func (p *TunnelProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
}

func (p *TunnelProvider) Resources(ctx context.Context) []func() resource.Resource {
	return nil
}

func (p *TunnelProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewSSMDataSource,
	}
}

func (p *TunnelProvider) EphemeralResources(ctx context.Context) []func() ephemeral.EphemeralResource {
	return []func() ephemeral.EphemeralResource{
		NewSSMEphemeral,
	}
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &TunnelProvider{
			version: version,
		}
	}
}
