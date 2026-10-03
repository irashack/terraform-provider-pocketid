package provider_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-log/tflogtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pocketidprovider "github.com/irashack/terraform-provider-pocketid/internal/provider"
)

func configureWithLogs(t *testing.T, baseURL, token string) (*provider.ConfigureResponse, string) {
	t.Helper()
	t.Setenv("POCKETID_BASE_URL", "")
	t.Setenv("POCKETID_API_TOKEN", "")
	var logs bytes.Buffer
	ctx := tflogtest.RootLogger(context.Background(), &logs)
	p := pocketidprovider.New("test")()
	schemaResp := &provider.SchemaResponse{}
	p.Schema(ctx, provider.SchemaRequest{}, schemaResp)
	require.False(t, schemaResp.Diagnostics.HasError())
	objectType := tftypes.Object{AttributeTypes: map[string]tftypes.Type{
		"base_url": tftypes.String, "api_token": tftypes.String, "skip_tls_verify": tftypes.Bool, "timeout": tftypes.Number,
	}}
	config := tfsdk.Config{Schema: schemaResp.Schema, Raw: tftypes.NewValue(objectType, map[string]tftypes.Value{
		"base_url": tftypes.NewValue(tftypes.String, baseURL), "api_token": tftypes.NewValue(tftypes.String, token),
		"skip_tls_verify": tftypes.NewValue(tftypes.Bool, nil), "timeout": tftypes.NewValue(tftypes.Number, nil),
	})}
	resp := &provider.ConfigureResponse{}
	p.Configure(ctx, provider.ConfigureRequest{Config: config}, resp)
	return resp, logs.String()
}

// The base URL never reaches the provider's log (it can carry credentials),
// and one that contains the API key is refused before anything uses it,
// with a diagnostic that does not show it.
func TestProvider_ConfigureLogsNoBaseURL(t *testing.T) {
	const token = "Synthetic-Configure-Key-0123456789"
	t.Run("ordinary", func(t *testing.T) {
		resp, logs := configureWithLogs(t, "https://user:basic-auth-secret@pocketid.example.com", token)
		require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		assert.NotEmpty(t, logs)
		assert.NotContains(t, logs, "pocketid.example.com")
		assert.NotContains(t, logs, "basic-auth-secret")
		assert.NotContains(t, logs, token)
	})
	for name, baseURL := range map[string]string{
		"key in the path": "https://pocketid.example.com/" + token,
		"key as userinfo": "https://" + token + "@pocketid.example.com",
		"key, padded":     "https://pocketid.example.com/?" + token,
	} {
		t.Run(name, func(t *testing.T) {
			resp, logs := configureWithLogs(t, baseURL, " \t"+token+"\t ")
			require.True(t, resp.Diagnostics.HasError())
			assert.Contains(t, resp.Diagnostics.Errors()[0].Summary(), "Invalid Pocket-ID Base URL")
			for _, d := range resp.Diagnostics {
				assert.NotContains(t, d.Summary()+d.Detail(), token)
			}
			assert.NotContains(t, logs, token)
			assert.Nil(t, resp.ResourceData, "no client is configured")
		})
	}
}
