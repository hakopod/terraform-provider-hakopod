package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestProviderServer(t *testing.T) {
	if _, err := providerserver.NewProtocol6WithError(New("test")())(); err != nil {
		t.Fatalf("provider server: %v", err)
	}
}

func TestProviderSchema(t *testing.T) {
	var resp provider.SchemaResponse
	New("test")().Schema(context.Background(), provider.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
		t.Fatalf("schema invalid: %v", diags)
	}
	if !resp.Schema.Attributes["api_key"].IsSensitive() {
		t.Fatal("api_key must be sensitive")
	}
}

func TestProviderConfigureMissingCredentials(t *testing.T) {
	t.Setenv("HAKOPOD_API_URL", "")
	t.Setenv("HAKOPOD_API_KEY", "")
	t.Setenv("HAKOPOD_WORKSPACE", "")

	p := New("test")()
	var s provider.SchemaResponse
	p.Schema(context.Background(), provider.SchemaRequest{}, &s)

	typ := s.Schema.Type().TerraformType(context.Background())
	raw := tftypes.NewValue(typ, map[string]tftypes.Value{
		"url":       tftypes.NewValue(tftypes.String, nil),
		"api_key":   tftypes.NewValue(tftypes.String, nil),
		"workspace": tftypes.NewValue(tftypes.String, nil),
	})
	var resp provider.ConfigureResponse
	p.Configure(context.Background(), provider.ConfigureRequest{Config: tfsdk.Config{Schema: s.Schema, Raw: raw}}, &resp)

	if resp.Diagnostics.ErrorsCount() != 2 {
		t.Fatalf("want 2 errors, got %v", resp.Diagnostics)
	}
	var all string
	for _, d := range resp.Diagnostics.Errors() {
		all += d.Summary() + " " + d.Detail() + "\n"
	}
	for _, want := range []string{"url", "HAKOPOD_API_URL", "api_key", "HAKOPOD_API_KEY"} {
		if !strings.Contains(all, want) {
			t.Errorf("diagnostics missing %q:\n%s", want, all)
		}
	}
	if resp.ResourceData != nil {
		t.Error("ResourceData set despite errors")
	}
}
