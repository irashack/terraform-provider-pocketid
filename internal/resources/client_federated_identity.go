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

func (federatedReplayProtectionModifier) PlanModifyList(ctx context.Context, req planmodifier.ListRequest, resp *planmodifier.ListResponse) {
	if req.PlanValue.IsNull() || req.PlanValue.IsUnknown() || req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	// Prior values per identity key, in list order. Pocket ID accepts several
	// identities with the same key, so the nth planned occurrence of a key is
	// paired with the nth prior one; a first-match lookup would hand one
	// identity's setting to its twin. A null value (state written by a provider
	// that did not track the field, planned without a refresh) is recorded as
	// unknown so that apply resolves it from the server instead.
	prior := map[string][]types.Bool{}
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
			value, _ := object.Attributes()["replay_protection"].(types.Bool)
			if value.IsNull() {
				value = types.BoolUnknown()
			}
			prior[key] = append(prior[key], value)
		}
	}

	configured := req.ConfigValue.Elements()
	elements := req.PlanValue.Elements()
	planned := make([]attr.Value, len(elements))
	occurrence := map[string]int{}
	changed := false
	for i, element := range elements {
		planned[i] = element
		object, ok := element.(types.Object)
		if !ok || object.IsNull() || object.IsUnknown() {
			continue
		}
		key, keyKnown := federatedIdentityObjectKey(object)
		nth := occurrence[key]
		if keyKnown {
			occurrence[key]++ // counted even when set explicitly, to keep twins aligned
		}

		// Only an omitted attribute is ours to plan. A configured value that is
		// not known until apply stays unknown: planning over it contradicts the
		// configuration and Terraform rejects the plan.
		if i >= len(configured) {
			continue
		}
		configuredObject, ok := configured[i].(types.Object)
		if !ok || configuredObject.IsNull() || configuredObject.IsUnknown() {
			continue
		}
		if configuredValue, _ := configuredObject.Attributes()["replay_protection"].(types.Bool); !configuredValue.IsNull() {
			continue
		}
		if !keyKnown {
			continue // identity not known until apply; resolved there
		}

		value := types.BoolValue(newFederatedIdentityReplayProtection)
		if nth < len(prior[key]) {
			value = prior[key][nth]
		}
		if value.IsUnknown() {
			continue
		}

		attributes := make(map[string]attr.Value, len(object.Attributes()))
		for name, attribute := range object.Attributes() {
			attributes[name] = attribute
		}
		attributes["replay_protection"] = value
		rebuilt, diags := types.ObjectValue(object.AttributeTypes(ctx), attributes)
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

	list, diags := types.ListValue(req.PlanValue.ElementType(ctx), planned)
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

// resolveReplayProtections returns the value to send for each identity. A value
// still unknown at apply time is taken from the server's current identity with
// the same key and occurrence, so an update never changes a setting the plan
// could not see. current is nil when creating a client.
func resolveReplayProtections(identities []clientFederatedIdentityModel, current []client.OIDCClientFederatedIdentity) []bool {
	existing := map[string][]bool{}
	for _, identity := range current {
		key := federatedIdentityKey(identity.Issuer, identity.Subject, identity.Audience)
		existing[key] = append(existing[key], identity.ReplayProtection)
	}

	resolved := make([]bool, len(identities))
	occurrence := map[string]int{}
	for i, identity := range identities {
		key := federatedIdentityKey(identity.Issuer.ValueString(), identity.Subject.ValueString(), identity.Audience.ValueString())
		nth := occurrence[key]
		occurrence[key]++
		switch {
		case !identity.ReplayProtection.IsNull() && !identity.ReplayProtection.IsUnknown():
			resolved[i] = identity.ReplayProtection.ValueBool()
		case nth < len(existing[key]):
			resolved[i] = existing[key][nth]
		default:
			resolved[i] = newFederatedIdentityReplayProtection
		}
	}
	return resolved
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
	if req.ConfigValue.IsUnknown() {
		return
	}
	if req.ConfigValue.IsNull() {
		// A null element cannot be sent. Dropping it would shrink the list after
		// the mutation and could slip an empty list past the version gate.
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid federated identity public key", "A public key must not be null. Remove the element instead.")
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
	// member returns a string member, and whether it is present as a string.
	member := func(name string) (string, bool) {
		value, present := key[name]
		if !present {
			return "", true
		}
		var text string
		if json.Unmarshal(value, &text) != nil {
			return "", false
		}
		return text, true
	}

	kty, ok := member("kty")
	if !ok || kty == "" {
		return `The key's "kty" property must be a non-empty string.`
	}
	if kty == "oct" {
		return "Symmetric keys cannot verify a third party's signature; provide an asymmetric public key."
	}
	// RFC 7518 private parameters: "d" for RSA, EC and OKP keys, the rest for RSA CRT form.
	for _, private := range []string{"d", "p", "q", "dp", "dq", "qi", "oth", "k"} {
		if _, present := key[private]; present {
			return "The key contains private key material. Provide only the public JWK."
		}
	}
	if kid, ok := member("kid"); !ok || kid == "" {
		return `The key's "kid" property must be a non-empty string; Pocket ID uses it to select the verification key.`
	}
	if use, ok := member("use"); !ok || (use != "" && use != "sig") {
		return `The key's "use" must be "sig" or absent.`
	}
	// Public parameters of the key types RFC 7518 defines. Other types are left to the server.
	for _, required := range map[string][]string{"RSA": {"n", "e"}, "EC": {"crv", "x", "y"}, "OKP": {"crv", "x"}}[kty] {
		if value, ok := member(required); !ok || value == "" {
			return fmt.Sprintf("The %s key is missing its public %q parameter.", kty, required)
		}
	}
	return ""
}

// uniquePublicKeyIDValidator rejects two keys with the same "kid" in one
// identity, which Pocket ID refuses only at apply time.
type uniquePublicKeyIDValidator struct{}

func (uniquePublicKeyIDValidator) Description(context.Context) string {
	return `every key must have a distinct "kid"`
}

func (v uniquePublicKeyIDValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (uniquePublicKeyIDValidator) ValidateList(_ context.Context, req validator.ListRequest, resp *validator.ListResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	seen := map[string]int{}
	for i, element := range req.ConfigValue.Elements() {
		value, ok := element.(jsontypes.Normalized)
		if !ok || value.IsNull() || value.IsUnknown() {
			continue
		}
		var key struct {
			KeyID string `json:"kid"`
		}
		if json.Unmarshal([]byte(value.ValueString()), &key) != nil || key.KeyID == "" {
			continue // reported per element
		}
		if first, duplicate := seen[key.KeyID]; duplicate {
			// A key ID is public; the key itself is still not echoed.
			resp.Diagnostics.AddAttributeError(req.Path.AtListIndex(i), "Duplicate federated identity public key ID",
				fmt.Sprintf("Key %d has the same \"kid\" %q as key %d. Key IDs must be unique within an identity.", i+1, key.KeyID, first+1))
			continue
		}
		seen[key.KeyID] = i
	}
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
