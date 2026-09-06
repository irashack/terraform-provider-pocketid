package resources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/require"

	"github.com/Trozz/terraform-provider-pocketid/internal/client"
)

// This boundary regression exercises the real GET/merge/PUT/response path.
// Native fixture tests additionally run the full pinned upstream validator.
func TestApplicationConfigSMTPPreservesSettings(t *testing.T) {
	for _, omitted := range []types.String{types.StringNull(), types.StringUnknown()} {
		t.Run(omitted.String(), func(t *testing.T) {
			current := &client.ApplicationConfig{}
			value := reflect.ValueOf(current).Elem()
			for i := 0; i < value.NumField(); i++ {
				value.Field(i).SetString("existing-" + value.Type().Field(i).Name)
			}
			current.WebauthnUserVerification = "required"
			current.WebauthnAllowSyncedPasskeys = "false"
			current.WebauthnAuthenticatorAttachment = "cross-platform"
			current.CIMDURLAllowlist = `["https://trusted.example.invalid/client.json"]`
			body, err := json.Marshal(current)
			require.NoError(t, err)
			var expected map[string]string
			require.NoError(t, json.Unmarshal(body, &expected))
			vars := func(config map[string]string) []client.AppConfigVariable {
				result := make([]client.AppConfigVariable, 0, len(config))
				for key, value := range config {
					result = append(result, client.AppConfigVariable{Key: key, Value: value})
				}
				return result
			}
			puts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					require.Equal(t, "/api/application-configuration/all", r.URL.Path)
					require.NoError(t, json.NewEncoder(w).Encode(vars(expected)))
					return
				}
				require.Equal(t, http.MethodPut, r.Method)
				require.Equal(t, "/api/application-configuration", r.URL.Path)
				var payload map[string]string
				require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
				// v2.14 AppConfigUpdateDto requires all three WebAuthn strings.
				for _, key := range []string{"webauthnUserVerification", "webauthnAllowSyncedPasskeys", "webauthnAuthenticatorAttachment"} {
					if payload[key] == "" {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
				}
				puts++
				want := make(map[string]string, len(expected))
				for key, value := range expected {
					want[key] = value
				}
				want["smtpHost"] = "smtp.example.invalid"
				require.Equal(t, want, payload, "SMTP update must preserve every unrelated field")
				require.NoError(t, json.NewEncoder(w).Encode(vars(payload)))
			}))
			defer server.Close()
			c, err := client.NewClient(server.URL, "synthetic-token", false, 30)
			require.NoError(t, err)
			r := &applicationConfigResource{client: c}
			plan := &applicationConfigModel{}
			pv := reflect.ValueOf(plan).Elem()
			for i := 0; i < pv.NumField(); i++ {
				pv.Field(i).Set(reflect.ValueOf(omitted))
			}
			plan.SmtpHost = types.StringValue("smtp.example.invalid")
			var diags diag.Diagnostics
			r.applyConfig(context.Background(), plan, &diags)
			require.False(t, diags.HasError(), "%v", diags)
			require.Equal(t, 1, puts)
			want := *current
			want.SmtpHost = "smtp.example.invalid"
			var wantModel applicationConfigModel
			applicationConfigToModel(&want, &wantModel)
			require.Equal(t, wantModel, *plan)
		})
	}
}

func TestApplicationConfigExplicitValues(t *testing.T) {
	current := &client.ApplicationConfig{WebauthnUserVerification: "preferred", WebauthnAllowSyncedPasskeys: "true", WebauthnAuthenticatorAttachment: "any", CIMDURLAllowlist: `["https://old.example.invalid"]`}
	plan := &applicationConfigModel{WebauthnUserVerification: types.StringValue("required"), WebauthnAllowSyncedPasskeys: types.StringValue("false"), WebauthnAuthenticatorAttachment: types.StringValue("platform"), CIMDURLAllowlist: types.StringValue("")}
	cfg := modelToApplicationConfig(plan, current)
	require.Equal(t, "required", cfg.WebauthnUserVerification)
	require.Equal(t, "false", cfg.WebauthnAllowSyncedPasskeys)
	require.Equal(t, "platform", cfg.WebauthnAuthenticatorAttachment)
	require.Empty(t, cfg.CIMDURLAllowlist, "explicit empty must remain distinct from omitted")
}
