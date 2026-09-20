package resources

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Trozz/terraform-provider-pocketid/internal/client"
)

// federatedPublicKeysMinVersion is the first Pocket ID release that accepts
// explicit public keys on a federated identity.
const federatedPublicKeysMinVersion = "2.15.0"

// newFederatedIdentityReplayProtection is used for an identity Terraform has
// not managed before. It matches the Pocket ID admin UI, which enables replay
// protection on every identity it creates; the API's own zero value is false.
const newFederatedIdentityReplayProtection = true

// publicKeysListType is the type of the public_keys attribute. Elements compare
// by JSON semantics because Pocket ID re-encodes every key it stores.
var publicKeysListType = types.ListType{ElemType: jsontypes.NormalizedType{}}

// federatedIdentityKey identifies an identity independently of its list index,
// so inserting or reordering identities never moves a setting between them.
func federatedIdentityKey(issuer, subject, audience string) string {
	key, _ := json.Marshal([3]string{issuer, subject, audience})
	return string(key)
}

// federatedReplayProtectionModifier plans replay_protection when the
// configuration omits it: an identity already in state keeps its current
// value, and a new identity gets the admin UI's default. Pocket ID replaces the
// whole identity list on every client update, so the value must always be sent.
type federatedReplayProtectionModifier struct{}

func (federatedReplayProtectionModifier) Description(context.Context) string {
	return "Keeps replay_protection of an existing federated identity when omitted, and enables it for a new one."
}

func (m federatedReplayProtectionModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (federatedReplayProtectionModifier) PlanModifyList(_ context.Context, req planmodifier.ListRequest, resp *planmodifier.ListResponse) {
	if req.PlanValue.IsNull() || req.PlanValue.IsUnknown() {
		return
	}

	// Index the prior identities by key. A null value (state written by a
	// provider that did not track the field, planned without a refresh) is
	// recorded as unknown so that apply resolves it from the server instead.
	prior := map[string]types.Bool{}
	if !req.StateValue.IsNull() && !req.StateValue.IsUnknown() {
		for _, element := range req.StateValue.Elements() {
			object, ok := element.(types.Object)
			if !ok || object.IsNull() || object.IsUnknown() {
				continue
			}
			key, ok := federatedIdentityObjectKey(object)
			if !ok {
				continue
			}
			if _, seen := prior[key]; seen {
				continue
			}
			value, _ := object.Attributes()["replay_protection"].(types.Bool)
			if value.IsNull() {
				value = types.BoolUnknown()
			}
			prior[key] = value
		}
	}

	elements := req.PlanValue.Elements()
	planned := make([]attr.Value, len(elements))
	changed := false
	for i, element := range elements {
		planned[i] = element
		object, ok := element.(types.Object)
		if !ok || object.IsNull() || object.IsUnknown() {
			continue
		}
		current, _ := object.Attributes()["replay_protection"].(types.Bool)
		if !current.IsUnknown() {
			continue // set explicitly in configuration
		}
		key, ok := federatedIdentityObjectKey(object)
		if !ok {
			continue // identity not known until apply; resolved there
		}
		value, exists := prior[key]
		if !exists {
			value = types.BoolValue(newFederatedIdentityReplayProtection)
		}
		if value.IsUnknown() {
			continue
		}

		attributes := make(map[string]attr.Value, len(object.Attributes()))
		for name, attribute := range object.Attributes() {
			attributes[name] = attribute
		}
		attributes["replay_protection"] = value
		rebuilt, diags := types.ObjectValue(object.AttributeTypes(context.Background()), attributes)
		resp.Diagnostics.Append(diags...)
		if diags.HasError() {
			return
		}
		planned[i] = rebuilt
		changed = true
	}
	if !changed {
		return
	}

	list, diags := types.ListValue(req.PlanValue.ElementType(context.Background()), planned)
	resp.Diagnostics.Append(diags...)
	if !diags.HasError() {
		resp.PlanValue = list
	}
}

// federatedIdentityObjectKey returns the identity key of a nested object, or
// false while any part of it is still unknown.
func federatedIdentityObjectKey(object types.Object) (string, bool) {
	parts := [3]string{}
	for i, name := range [3]string{"issuer", "subject", "audience"} {
		value, ok := object.Attributes()[name].(types.String)
		if !ok || value.IsUnknown() {
			return "", false
		}
		parts[i] = value.ValueString()
	}
	return federatedIdentityKey(parts[0], parts[1], parts[2]), true
}

// resolveReplayProtection returns the value to send for one identity. A value
// still unknown at apply time is taken from the server's current identity with
// the same key, so an update never changes a setting the plan could not see.
func resolveReplayProtection(identity clientFederatedIdentityModel, current []client.OIDCClientFederatedIdentity) bool {
	if !identity.ReplayProtection.IsNull() && !identity.ReplayProtection.IsUnknown() {
		return identity.ReplayProtection.ValueBool()
	}
	key := federatedIdentityKey(identity.Issuer.ValueString(), identity.Subject.ValueString(), identity.Audience.ValueString())
	for _, existing := range current {
		if federatedIdentityKey(existing.Issuer, existing.Subject, existing.Audience) == key {
			return existing.ReplayProtection
		}
	}
	return newFederatedIdentityReplayProtection
}

// federatedIdentitiesNeedServerValues reports whether any planned identity
// still has an unknown replay_protection, which apply resolves from the server.
func federatedIdentitiesNeedServerValues(ctx context.Context, list types.List) bool {
	if list.IsNull() || list.IsUnknown() {
		return false
	}
	var identities []clientFederatedIdentityModel
	if diags := list.ElementsAs(ctx, &identities, false); diags.HasError() {
		return false
	}
	for _, identity := range identities {
		if identity.ReplayProtection.IsUnknown() {
			return true
		}
	}
	return false
}

// credentialsUsePublicKeys reports whether any identity carries explicit keys.
func credentialsUsePublicKeys(credentials client.OIDCClientCredentials) bool {
	for _, identity := range credentials.FederatedIdentities {
		if len(identity.PublicKeys) > 0 {
			return true
		}
	}
	return false
}

// checkFederatedPublicKeysSupport refuses, before any mutation, to send
// public_keys to a server that would silently drop them.
func checkFederatedPublicKeysSupport(api *client.Client, credentials client.OIDCClientCredentials) error {
	if !credentialsUsePublicKeys(credentials) {
		return nil
	}
	supported, err := api.VersionAtLeast(federatedPublicKeysMinVersion)
	if err != nil {
		return fmt.Errorf("could not verify that the server supports federated identity public_keys: %w", err)
	}
	if !supported {
		return fmt.Errorf("federated identity public_keys requires Pocket ID %s or later; no mutation was attempted", federatedPublicKeysMinVersion)
	}
	return nil
}

// publicJWKValidator applies Pocket ID's rules for a federated identity key at
// plan time. Rejecting private material here also keeps it out of the plan,
// the state and the request.
type publicJWKValidator struct{}

func (publicJWKValidator) Description(context.Context) string {
	return `must be one public, asymmetric JWK with a "kid", usable for signatures`
}

func (v publicJWKValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (publicJWKValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if problem := publicJWKProblem(req.ConfigValue.ValueString()); problem != "" {
		// The value is never echoed: a rejected key may be a private one.
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid federated identity public key", problem)
	}
}

// publicJWKProblem returns why a JWK is unusable, or "" when it is acceptable.
func publicJWKProblem(raw string) string {
	var key map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &key); err != nil || key == nil {
		return "Each public key must be a single JSON object containing one JWK."
	}
	member := func(name string) string {
		var value string
		_ = json.Unmarshal(key[name], &value)
		return value
	}
	switch member("kty") {
	case "":
		return `The key is missing the "kty" property.`
	case "oct":
		return "Symmetric keys cannot verify a third party's signature; provide an asymmetric public key."
	}
	// RFC 7518 private parameters: "d" for RSA, EC and OKP keys, the rest for RSA CRT form.
	for _, private := range []string{"d", "p", "q", "dp", "dq", "qi", "oth", "k"} {
		if _, present := key[private]; present {
			return "The key contains private key material. Provide only the public JWK."
		}
	}
	if member("kid") == "" {
		return `The key is missing the "kid" property, which Pocket ID uses to select the verification key.`
	}
	if use := member("use"); use != "" && use != "sig" {
		return `The key's "use" must be "sig" or absent.`
	}
	return ""
}

// publicKeysFromAPI converts stored keys into the public_keys attribute value.
func publicKeysFromAPI(keys []json.RawMessage) types.List {
	if len(keys) == 0 {
		return types.ListNull(publicKeysListType.ElemType)
	}
	values := make([]attr.Value, 0, len(keys))
	for _, key := range keys {
		values = append(values, jsontypes.NewNormalizedValue(string(key)))
	}
	return types.ListValueMust(publicKeysListType.ElemType, values)
}

// publicKeysToAPI converts the public_keys attribute value into request keys.
func publicKeysToAPI(list types.List) []json.RawMessage {
	if list.IsNull() || list.IsUnknown() {
		return nil
	}
	keys := make([]json.RawMessage, 0, len(list.Elements()))
	for _, element := range list.Elements() {
		value, ok := element.(jsontypes.Normalized)
		if !ok || value.IsNull() || value.IsUnknown() {
			continue
		}
		keys = append(keys, json.RawMessage(value.ValueString()))
	}
	return keys
}
