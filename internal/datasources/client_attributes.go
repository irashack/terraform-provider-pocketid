package datasources

import (
	"context"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// clientModel is one OIDC client as both client data sources report it.
type clientModel struct {
	ID                                  types.String             `tfsdk:"id"`
	Name                                types.String             `tfsdk:"name"`
	Description                         types.String             `tfsdk:"description"`
	CallbackURLs                        types.List               `tfsdk:"callback_urls"`
	LogoutCallbackURLs                  types.List               `tfsdk:"logout_callback_urls"`
	BackchannelLogoutURL                types.String             `tfsdk:"backchannel_logout_url"`
	IsPublic                            types.Bool               `tfsdk:"is_public"`
	PkceEnabled                         types.Bool               `tfsdk:"pkce_enabled"`
	PkceSupported                       types.Bool               `tfsdk:"pkce_supported"`
	RequiresReauthentication            types.Bool               `tfsdk:"requires_reauthentication"`
	RequiresPushedAuthorizationRequests types.Bool               `tfsdk:"requires_pushed_authorization_requests"`
	SkipConsent                         types.Bool               `tfsdk:"skip_consent"`
	LaunchURL                           types.String             `tfsdk:"launch_url"`
	HasLogo                             types.Bool               `tfsdk:"has_logo"`
	HasDarkLogo                         types.Bool               `tfsdk:"has_dark_logo"`
	ClientType                          types.String             `tfsdk:"client_type"`
	AccessTokenDurationMinutes          types.Int64              `tfsdk:"access_token_duration_minutes"`
	RefreshTokenDurationMinutes         types.Int64              `tfsdk:"refresh_token_duration_minutes"`
	IsGroupRestricted                   types.Bool               `tfsdk:"is_group_restricted"`
	AllowedUserGroups                   types.Set                `tfsdk:"allowed_user_groups"`
	FederatedIdentities                 []federatedIdentityModel `tfsdk:"federated_identities"`
	Secrets                             []clientSecretModel      `tfsdk:"secrets"`
}

// federatedIdentityModel is one federated identity of a client.
type federatedIdentityModel struct {
	Issuer           types.String `tfsdk:"issuer"`
	Subject          types.String `tfsdk:"subject"`
	Audience         types.String `tfsdk:"audience"`
	JWKS             types.String `tfsdk:"jwks"`
	PublicKeys       types.List   `tfsdk:"public_keys"`
	ReplayProtection types.Bool   `tfsdk:"replay_protection"`
}

// clientSecretModel describes one secret of a client, never its value.
type clientSecretModel struct {
	ID        types.String `tfsdk:"id"`
	Prefix    types.String `tfsdk:"prefix"`
	CreatedAt types.String `tfsdk:"created_at"`
	ExpiresAt types.String `tfsdk:"expires_at"`
	IsActive  types.Bool   `tfsdk:"is_active"`
}

// clientAttributes returns the attributes of one client; id is the given
// attribute (required on pocketid_client, computed in pocketid_clients).
func clientAttributes(id schema.StringAttribute) map[string]schema.Attribute {
	computedString := func(description string) schema.StringAttribute {
		return schema.StringAttribute{Description: description, Computed: true}
	}
	computedBool := func(description string) schema.BoolAttribute {
		return schema.BoolAttribute{Description: description, Computed: true}
	}
	return map[string]schema.Attribute{
		"id":                                     id,
		"name":                                   computedString("The display name of the OIDC client."),
		"description":                            computedString("The client's description; empty when it has none."),
		"callback_urls":                          schema.ListAttribute{Description: "List of allowed callback URLs for the OIDC client.", Computed: true, ElementType: types.StringType},
		"logout_callback_urls":                   schema.ListAttribute{Description: "List of allowed logout callback URLs for the OIDC client; null when it has none.", Computed: true, ElementType: types.StringType},
		"backchannel_logout_url":                 computedString("The OpenID Connect Back-Channel Logout URL of the client; null when it has none or the server predates Pocket ID 2.17.0."),
		"is_public":                              computedBool("Whether this is a public client (no client secret)."),
		"pkce_enabled":                           computedBool("Whether PKCE is enabled for this client."),
		"pkce_supported":                         computedBool("Whether Pocket ID saw this client use PKCE although PKCE is not enabled for it."),
		"requires_reauthentication":              computedBool("Whether this client requires reauthentication on each authorization."),
		"requires_pushed_authorization_requests": computedBool("Whether this client requires Pushed Authorization Requests (PAR); null when the server predates PAR."),
		"skip_consent":                           computedBool("Whether users are not asked to consent before signing in to this client."),
		"launch_url":                             computedString("The URL the Pocket ID dashboard opens for this client; null when it has none."),
		"has_logo":                               computedBool("Whether the client has a logo configured."),
		"has_dark_logo":                          computedBool("Whether the client has a logo for dark mode."),
		"client_type":                            computedString("How the client was registered: `standard`, or `cimd` for a client registered from a Client ID Metadata Document (Pocket ID 2.14.0 and later)."),
		"access_token_duration_minutes":          schema.Int64Attribute{Description: "Lifetime of the client's access tokens in minutes.", Computed: true},
		"refresh_token_duration_minutes":         schema.Int64Attribute{Description: "Lifetime of the client's refresh tokens in minutes.", Computed: true},
		"is_group_restricted":                    computedBool("Whether only members of `allowed_user_groups` may sign in to this client. True with no groups means nobody may."),
		"allowed_user_groups":                    schema.SetAttribute{Description: "IDs of the user groups whose members may use this client; null when it has none.", Computed: true, ElementType: types.StringType},
		"federated_identities": schema.ListNestedAttribute{
			Description: "Federated identities (workload identity federation) allowed to authenticate as this client.",
			Computed:    true,
			NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"issuer":            computedString("The issuer of the federated identity token."),
				"subject":           computedString("The expected subject; null when any is accepted."),
				"audience":          computedString("The expected audience; null when any is accepted."),
				"jwks":              computedString("URL of the JWKS used to validate the token; null when not set."),
				"public_keys":       schema.ListAttribute{Description: "Explicit public keys used to validate the token, each a JSON-encoded JWK; null when not set.", Computed: true, ElementType: jsontypes.NormalizedType{}},
				"replay_protection": computedBool("Whether a federated identity token may be used only once."),
			}},
		},
		"secrets": schema.ListNestedAttribute{
			Description: "The client's secrets (Pocket ID 2.14.0 and later), without their values, which Pocket ID returns only when a secret is created.",
			Computed:    true,
			NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"id":         computedString("The secret's ID."),
				"prefix":     computedString("The first characters of the secret, as the admin UI shows them; empty for a secret migrated from before Pocket ID 2.14.0."),
				"created_at": computedString("When the secret was created (RFC 3339)."),
				"expires_at": computedString("When the secret expires (RFC 3339); null when it never does."),
				"is_active":  computedBool("Whether the secret can still be used (it has not expired)."),
			}},
		},
	}
}

// clientModelFromAPI maps a client as Pocket ID returned it.
func clientModelFromAPI(ctx context.Context, api client.OIDCClient) clientModel {
	optional := func(value string) types.String {
		if value == "" {
			return types.StringNull()
		}
		return types.StringValue(value)
	}
	minutes := func(value int64) types.Int64 {
		if value == 0 {
			return types.Int64Null()
		}
		return types.Int64Value(value)
	}
	stringList := func(values []string) types.List {
		if len(values) == 0 {
			return types.ListNull(types.StringType)
		}
		list, _ := types.ListValueFrom(ctx, types.StringType, values)
		return list
	}

	model := clientModel{
		ID:                                  types.StringValue(api.ID),
		Name:                                types.StringValue(api.Name),
		Description:                         types.StringValue(api.Description),
		CallbackURLs:                        stringList(api.CallbackURLs),
		LogoutCallbackURLs:                  stringList(api.LogoutCallbackURLs),
		BackchannelLogoutURL:                optional(api.BackchannelLogoutURL),
		IsPublic:                            types.BoolValue(api.IsPublic),
		PkceEnabled:                         types.BoolValue(api.PkceEnabled),
		PkceSupported:                       types.BoolValue(api.PkceSupported),
		RequiresReauthentication:            types.BoolValue(api.RequiresReauthentication),
		SkipConsent:                         types.BoolValue(api.SkipConsent),
		LaunchURL:                           optional(api.LaunchURL),
		HasLogo:                             types.BoolValue(api.HasLogo),
		HasDarkLogo:                         types.BoolValue(api.HasDarkLogo),
		ClientType:                          optional(api.ClientType),
		AccessTokenDurationMinutes:          minutes(api.AccessTokenDurationMinutes),
		RefreshTokenDurationMinutes:         minutes(api.RefreshTokenDurationMinutes),
		IsGroupRestricted:                   types.BoolValue(api.IsGroupRestricted),
		AllowedUserGroups:                   types.SetNull(types.StringType),
		RequiresPushedAuthorizationRequests: types.BoolNull(),
		FederatedIdentities:                 []federatedIdentityModel{},
		Secrets:                             []clientSecretModel{},
	}
	if api.RequiresPushedAuthorizationRequests != nil {
		model.RequiresPushedAuthorizationRequests = types.BoolValue(*api.RequiresPushedAuthorizationRequests)
	}
	if len(api.AllowedUserGroups) > 0 {
		ids := make([]string, 0, len(api.AllowedUserGroups))
		for _, group := range api.AllowedUserGroups {
			ids = append(ids, group.ID)
		}
		model.AllowedUserGroups, _ = types.SetValueFrom(ctx, types.StringType, ids)
	}
	for _, identity := range api.Credentials.FederatedIdentities {
		keys := types.ListNull(jsontypes.NormalizedType{})
		if len(identity.PublicKeys) > 0 {
			values := make([]attr.Value, 0, len(identity.PublicKeys))
			for _, key := range identity.PublicKeys {
				values = append(values, jsontypes.NewNormalizedValue(string(key)))
			}
			keys = types.ListValueMust(jsontypes.NormalizedType{}, values)
		}
		model.FederatedIdentities = append(model.FederatedIdentities, federatedIdentityModel{
			Issuer:           types.StringValue(identity.Issuer),
			Subject:          optional(identity.Subject),
			Audience:         optional(identity.Audience),
			JWKS:             optional(identity.JWKS),
			PublicKeys:       keys,
			ReplayProtection: types.BoolValue(identity.ReplayProtection),
		})
	}
	for _, secret := range api.Credentials.Secrets {
		expires := types.StringNull()
		if secret.ExpiresAt != nil {
			expires = types.StringValue(secret.ExpiresAt.UTC().Format(time.RFC3339))
		}
		model.Secrets = append(model.Secrets, clientSecretModel{
			ID:        types.StringValue(secret.ID),
			Prefix:    types.StringValue(secret.Prefix),
			CreatedAt: types.StringValue(secret.CreatedAt.UTC().Format(time.RFC3339)),
			ExpiresAt: expires,
			IsActive:  types.BoolValue(secret.IsActive),
		})
	}
	return model
}
