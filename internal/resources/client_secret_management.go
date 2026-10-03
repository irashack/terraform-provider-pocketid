package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// clientSecretsMinVersion is the first Pocket ID that lists, identifies and
// revokes single secrets of a client (GET and DELETE
// /oidc/clients/{id}/secrets[/{secretId}]).
const clientSecretsMinVersion = "2.14.0"

// clientSecretPrefixLength is how many leading characters of a secret Pocket ID
// keeps in clear text (model.OidcClientSecretPrefixLength, 4 from v2.14.0 to
// v2.17.0). clientSecretPrefix in its OIDC service stores no prefix for a
// secret of at most that length, nor for one migrated from the single-secret
// column of earlier versions.
const clientSecretPrefixLength = 4

// secretAction is what an apply does about the secret this resource holds.
type secretAction int

const (
	// secretKeep leaves the secret in state (or its absence) as it is.
	secretKeep secretAction = iota
	// secretGenerate creates a secret and stores it.
	secretGenerate
	// secretRevoke revokes the secret this resource generated and nulls it.
	secretRevoke
	// secretNone means the resource holds no secret, before and after.
	secretNone
)

// holdsSecret reports whether state records a secret this resource generated.
func holdsSecret(state clientResourceModel) bool {
	return !state.ClientSecret.IsNull() || !state.ClientSecretID.IsNull()
}

// generatesSecret reports whether a model asks this resource to hold a secret.
// A null generate_secret is state written before the attribute existed, which
// always generated one.
func generatesSecret(generate types.Bool) bool {
	return generate.IsNull() || generate.ValueBool()
}

// pendingRevocationKey is the private-state key that marks a secret this
// resource generated and must still revoke: a revocation that failed or was
// not reached after the client itself changed. It keeps the revocation in
// the next plan even when the state no longer shows a transition (the client
// is already public, or generate_secret is already false).
const pendingRevocationKey = "pending_secret_revocation"

// holdingMode reports whether a model asks this resource to hold a secret: a
// confidential client with generate_secret true (null in state written before
// the attribute existed).
func holdingMode(m clientResourceModel) bool {
	return generatesSecret(m.GenerateSecret) && !m.IsPublic.ValueBool()
}

// planSecretAction decides what an update does about the resource's secret,
// and false when the plan does not determine it yet.
//
// The resource holds a secret when the client is confidential and
// generate_secret is true. A secret is generated only on a transition into
// that: generate_secret from false to true, or is_public from true to false.
// A confidential client in state without a secret (imported, or created
// before Terraform managed it) keeps having none, so upgrading the provider
// or importing a client never creates a secret behind the user's back.
//
// A secret in state is revoked only on a requested transition out of it (the
// client becomes public, or generate_secret turns false) or when a revocation
// is pending from an earlier apply. A public client that kept the secret
// earlier providers stored when it was confidential is left alone: nothing in
// the plan asks for a change.
func planSecretAction(state, plan clientResourceModel, pending bool) (secretAction, bool) {
	if plan.GenerateSecret.IsUnknown() || plan.IsPublic.IsUnknown() {
		return secretKeep, false
	}
	holds := holdsSecret(state)
	if !holdingMode(plan) {
		leaving := holdingMode(state) || (generatesSecret(state.GenerateSecret) && !plan.GenerateSecret.ValueBool())
		switch {
		case holds && (leaving || pending):
			return secretRevoke, true
		case holds:
			return secretKeep, true
		}
		return secretNone, true
	}
	if !holds && (!generatesSecret(state.GenerateSecret) || state.IsPublic.ValueBool()) {
		return secretGenerate, true
	}
	return secretKeep, true
}

// errSecretNotIdentified means the secret this resource generated could not be
// told apart from the client's other secrets. Nothing was changed.
var errSecretNotIdentified = errors.New("the client secret stored in state could not be identified among the client's secrets")

// secretPrefix returns the prefix Pocket ID records for a secret value.
func secretPrefix(value string) string {
	if len(value) <= clientSecretPrefixLength {
		return ""
	}
	return value[:clientSecretPrefixLength]
}

// identifySecret finds the secret whose value state holds among the client's
// secrets, by the prefix Pocket ID stores in clear text. It returns the
// secret's ID; "" and gone=true when no listed secret can be it (every secret
// has a recorded prefix and none matches), so the secret no longer exists.
// Anything else (no prefix to compare, two or more candidates, a secret
// without a recorded prefix that could be it) wraps errSecretNotIdentified.
func identifySecret(value string, secrets []client.ClientSecretMetadata) (id string, gone bool, err error) {
	prefix := secretPrefix(value)
	var matches, unknown []client.ClientSecretMetadata
	for _, secret := range secrets {
		switch secret.Prefix {
		case "":
			unknown = append(unknown, secret)
		case prefix:
			matches = append(matches, secret)
		}
	}
	switch {
	case prefix == "":
		return "", false, fmt.Errorf("%w: the stored value is too short to have a recorded prefix", errSecretNotIdentified)
	case len(matches) == 1 && len(unknown) == 0:
		return matches[0].ID, false, nil
	case len(matches) == 0 && len(unknown) == 0:
		return "", true, nil
	}
	return "", false, fmt.Errorf("%w: %d secrets have the same prefix as the stored value and %d have no recorded prefix", errSecretNotIdentified, len(matches), len(unknown))
}

// describeSecrets lists a client's secrets for a diagnostic: IDs, the prefix
// Pocket ID shows in its admin UI, and dates. Never a value.
func describeSecrets(secrets []client.ClientSecretMetadata) string {
	if len(secrets) == 0 {
		return "The client has no secrets."
	}
	sorted := append([]client.ClientSecretMetadata(nil), secrets...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].CreatedAt.Before(sorted[j].CreatedAt) })
	lines := make([]string, 0, len(sorted))
	for _, secret := range sorted {
		prefix := "no recorded prefix"
		if secret.Prefix != "" {
			prefix = fmt.Sprintf("prefix %q", secret.Prefix)
		}
		expiry := "never expires"
		if secret.ExpiresAt != nil {
			expiry = "expires " + secret.ExpiresAt.UTC().Format(time.RFC3339)
		}
		lines = append(lines, fmt.Sprintf("- %s (%s, created %s, %s)", secret.ID, prefix, secret.CreatedAt.UTC().Format(time.RFC3339), expiry))
	}
	return "The client's secrets:\n" + strings.Join(lines, "\n")
}

// heldSecretID returns the ID of the secret this resource generated, for
// revocation. It uses client_secret_id when state has it; state written before
// that attribute existed is matched by prefix against the client's secrets.
// gone=true means no such secret exists any more. It makes no change.
func (r *clientResource) heldSecretID(ctx context.Context, clientID string, state clientResourceModel) (id string, gone bool, err error) {
	if !state.ClientSecretID.IsNull() && state.ClientSecretID.ValueString() != "" {
		return state.ClientSecretID.ValueString(), false, nil
	}
	if state.ClientSecret.IsNull() {
		return "", true, nil
	}
	supported, err := r.client.VersionAtLeast(ctx, clientSecretsMinVersion)
	if err != nil {
		return "", false, fmt.Errorf("could not verify that the server can revoke a single client secret: %w", err)
	}
	if !supported {
		return "", false, fmt.Errorf("revoking the client secret this resource generated requires Pocket ID %s or later", clientSecretsMinVersion)
	}
	secrets, err := r.client.ListClientSecrets(ctx, clientID)
	if err != nil {
		return "", false, fmt.Errorf("could not list the client's secrets to identify the one stored in state: %w", err)
	}
	id, gone, err = identifySecret(state.ClientSecret.ValueString(), secrets)
	if err != nil {
		return "", false, fmt.Errorf("%w. %s Revoke the secrets you no longer need in Pocket ID, or remove the client from state and import it again", err, describeSecrets(secrets))
	}
	return id, gone, nil
}

// fillSecretID records client_secret_id for state written before the attribute
// existed, when the stored secret can be identified unambiguously. It never
// fails a refresh: without a match the attribute stays null and revocation
// identifies the secret again later.
func (r *clientResource) fillSecretID(ctx context.Context, state *clientResourceModel) {
	if !state.ClientSecretID.IsNull() || state.ClientSecret.IsNull() || state.ClientSecret.ValueString() == "" {
		return
	}
	supported, err := r.client.VersionAtLeast(ctx, clientSecretsMinVersion)
	if err != nil || !supported {
		return
	}
	secrets, err := r.client.ListClientSecrets(ctx, state.ID.ValueString())
	if err != nil {
		tflog.Debug(ctx, "Could not list client secrets to record client_secret_id", map[string]any{"id": state.ID.ValueString()})
		return
	}
	if id, gone, err := identifySecret(state.ClientSecret.ValueString(), secrets); err == nil && !gone {
		state.ClientSecretID = types.StringValue(id)
	}
}

// revokeClientSecret revokes one secret of a client. The DELETE is never
// retried. A failure counts as revoked only when Pocket ID confirms the secret
// is gone: its own not-found error for the secret, or a list that no longer
// contains it. The error names the secret's ID, never a value.
func (r *clientResource) revokeClientSecret(ctx context.Context, clientID, secretID string) error {
	err := r.client.DeleteClientSecret(ctx, clientID, secretID)
	if err == nil || client.IsNotFound(err, client.ResourceClientSecret) {
		return nil
	}
	if remaining, listErr := r.client.ListClientSecrets(ctx, clientID); listErr == nil {
		present := false
		for _, secret := range remaining {
			if secret.ID == secretID {
				present = true
				break
			}
		}
		if !present {
			return nil
		}
	}
	return fmt.Errorf("could not confirm revocation of client secret %s; it may still be valid until it is revoked or the client is deleted: %w", secretID, err)
}

// generateHeldSecret creates the secret this resource holds and records it in
// model. On failure model keeps no secret and the error says whether one may
// have been created.
func (r *clientResource) generateHeldSecret(ctx context.Context, model *clientResourceModel) error {
	secret, err := r.client.GenerateClientSecret(ctx, model.ID.ValueString(), nil)
	if err != nil {
		model.ClientSecret = types.StringNull()
		model.ClientSecretID = types.StringNull()
		if definitelyRejected(err) {
			return fmt.Errorf("the server refused to create a client secret, so none was created: %w", err)
		}
		return fmt.Errorf("the result of creating a client secret is uncertain: one may have been created, and its value is lost. It was not retried. The next apply will not create another while the client has a secret this resource cannot account for: %w", err)
	}
	model.ClientSecret = types.StringValue(secret.Value)
	model.ClientSecretID = optionalString(secret.ID)
	return nil
}

// secretOutcome is what an update's secret step leaves for later applies.
type secretOutcome struct {
	// pendingRevocation: a revocation is still due.
	pendingRevocation bool
	// uncertainGeneration is set when a generation's POST may have created
	// a secret whose value was lost; knownSecrets are the IDs the client had
	// just before it.
	uncertainGeneration bool
	knownSecrets        []string
}

// applySecretAction carries out an update's secret action after the client
// itself was written, and records the outcome in model. prior is the state
// before the update. When it fails, model holds what is confirmed: a secret
// whose revocation was not confirmed stays in state and the outcome marks its
// revocation as still due; after a failed generation generate_secret is
// recorded as false so that the next plan shows the generation again, and a
// generation that may have created a secret is marked uncertain, with the
// secrets the client had before, so that it is not simply repeated.
func (r *clientResource) applySecretAction(ctx context.Context, action secretAction, revokeID string, gone bool, prior clientResourceModel, model *clientResourceModel) (secretOutcome, error) {
	switch action {
	case secretKeep:
		model.ClientSecret, model.ClientSecretID = prior.ClientSecret, prior.ClientSecretID
	case secretRevoke:
		if !gone {
			tflog.Debug(ctx, "Revoking the client secret this resource generated", map[string]any{"id": model.ID.ValueString(), "secret_id": revokeID})
			if err := r.revokeClientSecret(ctx, model.ID.ValueString(), revokeID); err != nil {
				model.ClientSecret = prior.ClientSecret
				model.ClientSecretID = types.StringValue(revokeID)
				return secretOutcome{pendingRevocation: true}, err
			}
		}
		model.ClientSecret, model.ClientSecretID = types.StringNull(), types.StringNull()
	case secretGenerate:
		tflog.Debug(ctx, "Generating the client secret this resource holds", map[string]any{"id": model.ID.ValueString()})
		known, listed, err := r.secretIDsBeforeGeneration(ctx, model.ID.ValueString())
		if err != nil {
			model.ClientSecret, model.ClientSecretID = types.StringNull(), types.StringNull()
			model.GenerateSecret = types.BoolValue(false)
			return secretOutcome{}, err
		}
		if err := r.generateHeldSecret(ctx, model); err != nil {
			model.GenerateSecret = types.BoolValue(false)
			return secretOutcome{uncertainGeneration: listed && !definitelyRejected(err), knownSecrets: known}, err
		}
	default:
		model.ClientSecret, model.ClientSecretID = types.StringNull(), types.StringNull()
	}
	return secretOutcome{}, nil
}

// secretIDsBeforeGeneration lists the client's secret IDs just before a
// generation, so that a generation whose result is lost can be reconciled
// later. listed is false on a server before 2.14.0, where a generation
// replaces the client's only secret and repeating it is harmless.
func (r *clientResource) secretIDsBeforeGeneration(ctx context.Context, clientID string) (ids []string, listed bool, err error) {
	supported, err := r.client.VersionAtLeast(ctx, clientSecretsMinVersion)
	if err != nil {
		return nil, false, fmt.Errorf("could not determine the server version before creating a client secret, so none was created: %w", err)
	}
	if !supported {
		return nil, false, nil
	}
	secrets, err := r.client.ListClientSecrets(ctx, clientID)
	if err != nil {
		return nil, false, fmt.Errorf("could not list the client's secrets before creating one, so none was created: %w", err)
	}
	ids = make([]string, 0, len(secrets))
	for _, secret := range secrets {
		ids = append(ids, secret.ID)
	}
	return ids, true, nil
}

// uncertainGenerationKey is the private-state key of a generation that may
// have created a secret whose value was lost. Its value lists the secret IDs
// the client had just before that POST.
const uncertainGenerationKey = "uncertain_secret_generation"

// uncertainGeneration is the value stored under uncertainGenerationKey.
type uncertainGeneration struct {
	KnownSecrets []string `json:"known_secrets"`
}

// readUncertainGeneration returns the recorded uncertain generation, if any.
func readUncertainGeneration(ctx context.Context, private privateGetter) (*uncertainGeneration, bool) {
	if private == nil {
		return nil, false
	}
	raw, _ := private.GetKey(ctx, uncertainGenerationKey)
	if len(raw) == 0 {
		return nil, false
	}
	var marker uncertainGeneration
	if json.Unmarshal(raw, &marker) != nil {
		// Unreadable, but present: treat every secret as unaccounted.
		return &uncertainGeneration{}, true
	}
	return &marker, true
}

// writeUncertainGeneration records or removes the marker.
func writeUncertainGeneration(ctx context.Context, private privateSetter, outcome secretOutcome) diag.Diagnostics {
	if !outcome.uncertainGeneration {
		return private.SetKey(ctx, uncertainGenerationKey, nil)
	}
	known := outcome.knownSecrets
	if known == nil {
		known = []string{}
	}
	raw, err := json.Marshal(uncertainGeneration{KnownSecrets: known})
	if err != nil {
		var diags diag.Diagnostics
		diags.AddError("Could not record an uncertain secret generation", err.Error())
		return diags
	}
	return private.SetKey(ctx, uncertainGenerationKey, raw)
}

// checkUncertainGeneration decides whether a generation may follow one whose
// result was lost: only when the client has no secret this resource cannot
// account for (none created since, or the operator revoked it). Otherwise it
// returns an error listing those secrets by ID and prefix. It makes no change.
func (r *clientResource) checkUncertainGeneration(ctx context.Context, clientID string, marker *uncertainGeneration) error {
	secrets, err := r.client.ListClientSecrets(ctx, clientID)
	if err != nil {
		return fmt.Errorf("an earlier apply may have created a client secret whose value was lost, and the client's secrets could not be listed to check: %w", err)
	}
	var unaccounted []client.ClientSecretMetadata
	for _, secret := range secrets {
		if !slices.Contains(marker.KnownSecrets, secret.ID) {
			unaccounted = append(unaccounted, secret)
		}
	}
	if len(unaccounted) == 0 {
		return nil
	}
	return fmt.Errorf("an earlier apply may have created a client secret whose value was lost. These secrets were created since and this resource cannot account for them. %s Revoke the ones that are not in use (in the Pocket ID admin UI), then apply again", describeSecrets(unaccounted))
}

// planSecretAttributes plans client_secret and client_secret_id. state is nil
// when the client is being created.
func planSecretAttributes(state *clientResourceModel, plan *clientResourceModel, pending bool) {
	unknown := func() { plan.ClientSecret, plan.ClientSecretID = types.StringUnknown(), types.StringUnknown() }
	null := func() { plan.ClientSecret, plan.ClientSecretID = types.StringNull(), types.StringNull() }
	if state == nil {
		switch {
		case plan.GenerateSecret.IsUnknown() || plan.IsPublic.IsUnknown():
			unknown()
		case plan.GenerateSecret.ValueBool() && !plan.IsPublic.ValueBool():
			unknown()
		default:
			null()
		}
		return
	}
	action, known := planSecretAction(*state, *plan, pending)
	switch {
	case !known || action == secretGenerate:
		unknown()
	case action == secretKeep:
		plan.ClientSecret, plan.ClientSecretID = state.ClientSecret, state.ClientSecretID
	default:
		null()
	}
}

// skipSecretAction records in model that an update stopped before its secret
// action, possibly after changing the client, so that the next plan shows
// the action again: the secret in state stays, a generation still to do
// leaves generate_secret false, and a revocation still to do is reported as
// pending.
func skipSecretAction(action secretAction, prior clientResourceModel, model *clientResourceModel) (pending bool) {
	model.ClientSecret, model.ClientSecretID = prior.ClientSecret, prior.ClientSecretID
	if action == secretGenerate {
		model.GenerateSecret = types.BoolValue(false)
	}
	return action == secretRevoke
}

// definitelyRejected reports whether a failed request certainly changed
// nothing: a 4xx answer (client.IsDefiniteRejection) to the request itself.
// An error wrapping client.ErrResultUnread is never one, whatever status it
// carries: the mutation was accepted and only reading its result failed (a
// 403 on the read-back, for example).
func definitelyRejected(err error) bool {
	return !errors.Is(err, client.ErrResultUnread) && client.IsDefiniteRejection(err)
}

// heldSecretPresent reports whether the secret state holds still exists on
// the server: by its exact ID when state has it, by its prefix otherwise. An
// error means it could not be established (the list failed, or the prefix
// is ambiguous).
func (r *clientResource) heldSecretPresent(ctx context.Context, state clientResourceModel) (bool, error) {
	secrets, err := r.client.ListClientSecrets(ctx, state.ID.ValueString())
	if err != nil {
		return false, err
	}
	if id := state.ClientSecretID; !id.IsNull() && !id.IsUnknown() && id.ValueString() != "" {
		for _, secret := range secrets {
			if secret.ID == id.ValueString() {
				return true, nil
			}
		}
		return false, nil
	}
	_, gone, err := identifySecret(state.ClientSecret.ValueString(), secrets)
	if err != nil {
		return false, err
	}
	return !gone, nil
}

// reconcilePendingRevocation settles a pending revocation on refresh: when
// the secret state still holds is confirmed gone (the revocation went through
// although its answer was lost), it is removed from state and the mark is
// cleared. A resource left in holding mode then records generate_secret =
// false, so that a configuration asking for a secret generates a new one. It
// returns whether the mark is still due; an unreadable list keeps it.
func (r *clientResource) reconcilePendingRevocation(ctx context.Context, state *clientResourceModel) (stillPending bool) {
	if !holdsSecret(*state) {
		return false
	}
	present, err := r.heldSecretPresent(ctx, *state)
	if err != nil || present {
		return true
	}
	tflog.Debug(ctx, "The client secret whose revocation was pending is gone", map[string]any{"id": state.ID.ValueString()})
	if holdingMode(*state) {
		state.GenerateSecret = types.BoolValue(false)
	}
	state.ClientSecret, state.ClientSecretID = types.StringNull(), types.StringNull()
	return false
}
