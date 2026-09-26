package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hakopod/terraform-provider-hakopod/internal/client"
)

var _ provider.Provider = (*hakopodProvider)(nil)

type hakopodProvider struct {
	version string
}

type providerModel struct {
	URL       types.String `tfsdk:"url"`
	APIKey    types.String `tfsdk:"api_key"`
	Workspace types.String `tfsdk:"workspace"`
}

// New returns a constructor for the hakopod provider.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &hakopodProvider{version: version}
	}
}

func (p *hakopodProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "hakopod"
	resp.Version = p.version
}

func (p *hakopodProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages Hakopod applications and virtual networks through the Hakopod API.",
		Attributes: map[string]schema.Attribute{
			"url": schema.StringAttribute{
				Optional:    true,
				Description: "Hakopod API URL. Defaults to the HAKOPOD_API_URL environment variable.",
			},
			"api_key": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Hakopod API key. Defaults to the HAKOPOD_API_KEY environment variable.",
			},
			"workspace": schema.StringAttribute{
				Optional:    true,
				Description: "Hakopod Cloud workspace ID. Defaults to the HAKOPOD_WORKSPACE environment variable. Leave unset for self-hosted installations.",
			},
		},
	}
}

// valueOrEnv returns the configured value, else the environment variable.
func valueOrEnv(v types.String, env string) string {
	if !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" {
		return v.ValueString()
	}
	return os.Getenv(env)
}

func (p *hakopodProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Unknown values (e.g. from another resource) cannot configure a client yet.
	for attr, v := range map[string]types.String{"url": cfg.URL, "api_key": cfg.APIKey, "workspace": cfg.Workspace} {
		if v.IsUnknown() {
			resp.Diagnostics.AddAttributeError(path.Root(attr), "Unknown provider configuration",
				"The provider cannot be configured with an unknown "+attr+" value. Set it statically or through its environment variable.")
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}

	url := valueOrEnv(cfg.URL, "HAKOPOD_API_URL")
	key := valueOrEnv(cfg.APIKey, "HAKOPOD_API_KEY")
	workspace := valueOrEnv(cfg.Workspace, "HAKOPOD_WORKSPACE")
	if url == "" {
		resp.Diagnostics.AddAttributeError(path.Root("url"), "Missing Hakopod API URL",
			"Set the provider's url attribute or the HAKOPOD_API_URL environment variable.")
	}
	if key == "" {
		resp.Diagnostics.AddAttributeError(path.Root("api_key"), "Missing Hakopod API key",
			"Set the provider's api_key attribute or the HAKOPOD_API_KEY environment variable.")
	}
	if resp.Diagnostics.HasError() {
		return
	}

	c, err := client.New(url, key, workspace, "terraform-provider-hakopod/"+p.version)
	if err != nil {
		resp.Diagnostics.AddError("Unable to create Hakopod client", err.Error())
		return
	}
	resp.ResourceData = c
	resp.DataSourceData = c
}

func (p *hakopodProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewApplicationResource,
		NewVirtualNetworkResource,
		NewProjectResource,
		NewSecretResource,
	}
}

func (p *hakopodProvider) DataSources(context.Context) []func() datasource.DataSource {
	return nil
}
