package provider_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	signupTokenResourceType = "pocketid_signup_token"
	signupProtocolTokenID   = "55555555-5555-4555-8555-555555555555"
	signupProtocolGroupID   = "66666666-6666-4666-8666-666666666666"
)

func signupProtocolSet(ids ...string) []tftypes.Value {
	out := make([]tftypes.Value, 0, len(ids))
	for _, id := range ids {
		out = append(out, tftypes.NewValue(tftypes.String, id))
	}
	return out
}

// A signup token that Pocket ID has purged (it expired) stays in state as
// expired and the next plan, under the unchanged configuration, is empty: no
// replacement, so no new registration credential is minted unprompted. This
// runs the real refresh and plan of the provider against a server whose list
// no longer holds the token.
func TestSignupToken_ExpiredTokenPlansEmpty(t *testing.T) {
	ctx := context.Background()
	server, schemas := scimSignupProtocolServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/signup-tokens" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data":       []any{},
				"pagination": map[string]any{"totalPages": 1, "totalItems": 0, "currentPage": 1, "itemsPerPage": 100},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	objectType := scimSignupObjectType(t, schemas, signupTokenResourceType)

	stored := scimProtocolObject(t, objectType, map[string]any{
		"id": signupProtocolTokenID, "ttl": "24h", "usage_limit": 3, "user_group_ids": signupProtocolSet(signupProtocolGroupID),
		"token": "signup-secret-value-0123", "expires_at": "2026-10-03T10:00:00Z", "created_at": "2026-10-02T10:00:00Z",
		"usage_count": 1, "expired": false,
	})
	read, err := server.ReadResource(ctx, &tfprotov6.ReadResourceRequest{TypeName: signupTokenResourceType, CurrentState: scimDynamic(t, objectType, stored)})
	require.NoError(t, err)
	require.Empty(t, read.Diagnostics)
	prior, err := read.NewState.Unmarshal(objectType)
	require.NoError(t, err)
	require.False(t, prior.IsNull(), "the expired token must stay in state")
	var priorValues map[string]tftypes.Value
	require.NoError(t, prior.As(&priorValues))
	var expired bool
	require.NoError(t, priorValues["expired"].As(&expired))
	assert.True(t, expired)

	configured := map[string]any{"ttl": "24h", "usage_limit": 3, "user_group_ids": signupProtocolSet(signupProtocolGroupID)}
	config := scimProtocolObject(t, objectType, configured)
	proposed := scimProtocolObject(t, objectType, map[string]any{
		"id": signupProtocolTokenID, "ttl": "24h", "usage_limit": 3, "user_group_ids": signupProtocolSet(signupProtocolGroupID),
		"token": "signup-secret-value-0123", "expires_at": "2026-10-03T10:00:00Z", "created_at": "2026-10-02T10:00:00Z",
		"usage_count": 1, "expired": true,
	})
	planned, err := server.PlanResourceChange(ctx, &tfprotov6.PlanResourceChangeRequest{
		TypeName:         signupTokenResourceType,
		PriorState:       read.NewState,
		ProposedNewState: scimDynamic(t, objectType, proposed),
		Config:           scimDynamic(t, objectType, config),
	})
	require.NoError(t, err)
	require.Empty(t, planned.Diagnostics)
	plannedValue, err := planned.PlannedState.Unmarshal(objectType)
	require.NoError(t, err)
	assert.True(t, prior.Equal(plannedValue), "the plan must equal the refreshed state")
	assert.Empty(t, planned.RequiresReplace)

	// Control: changing an input does replace it, so the empty plan above is
	// not just a plan that cannot see the token.
	changed := scimProtocolObject(t, objectType, map[string]any{"ttl": "24h", "usage_limit": 5, "user_group_ids": signupProtocolSet(signupProtocolGroupID)})
	proposedChanged := scimProtocolObject(t, objectType, map[string]any{
		"id": signupProtocolTokenID, "ttl": "24h", "usage_limit": 5, "user_group_ids": signupProtocolSet(signupProtocolGroupID),
		"token": "signup-secret-value-0123", "expires_at": "2026-10-03T10:00:00Z", "created_at": "2026-10-02T10:00:00Z",
		"usage_count": 1, "expired": true,
	})
	replan, err := server.PlanResourceChange(ctx, &tfprotov6.PlanResourceChangeRequest{
		TypeName:         signupTokenResourceType,
		PriorState:       read.NewState,
		ProposedNewState: scimDynamic(t, objectType, proposedChanged),
		Config:           scimDynamic(t, objectType, changed),
	})
	require.NoError(t, err)
	require.Empty(t, replan.Diagnostics)
	assert.NotEmpty(t, replan.RequiresReplace)
}
