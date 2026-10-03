package resources

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

var (
	_ resource.Resource                = &clientSecretResource{}
	_ resource.ResourceWithConfigure   = &clientSecretResource{}
	_ resource.ResourceWithImportState = &clientSecretResource{}
	_ resource.ResourceWithModifyPlan  = &clientSecretResource{}
)

func init() { register(NewClientSecretResource) }

// NewClientSecretResource returns the pocketid_client_secret resource.
func NewClientSecretResource() resource.Resource {
	return &clientSecretResource{}
}

// clientSecretResource manages one secret of one OIDC client (Pocket ID
// 2.14.0+, where a client holds up to client.MaxClientSecrets of them). It
// only ever touches the secret it created: other secrets of the same client,
// including the one pocketid_client generates and the one Pocket ID 2.17
// creates with a client, are left alone.
type clientSecretResource struct {
	client *client.Client
}

type clientSecretResourceModel struct {
	ID              types.String `tfsdk:"id"`
	ClientID        types.String `tfsdk:"client_id"`
	ExpiresAt       types.String `tfsdk:"expires_at"`
	Secret          types.String `tfsdk:"secret"`
	SecretWO        types.String `tfsdk:"secret_wo"`
	SecretWOVersion types.String `tfsdk:"secret_wo_version"`
	Prefix          types.String `tfsdk:"prefix"`
	CreatedAt       types.String `tfsdk:"created_at"`
	IsActive        types.Bool   `tfsdk:"is_active"`
}

func (r *clientSecretResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_client_secret"
}

func (r *clientSecretResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages one secret of an OIDC client in Pocket ID 2.14.0 or later, so a client's secret can be rotated without replacing the client.",
		MarkdownDescription: "Manages one secret of an OIDC client in Pocket ID 2.14.0 or later, so a client's secret can be " +
			"rotated without replacing the client. A client can hold up to 20 secrets at once (expired ones included), and each " +
			"of them authenticates the client until it is revoked or expires. This resource only ever revokes the secret it " +
			"created: any other secret of the client, including the one `pocketid_client` generates, is left alone.\n\n" +
			"By default Pocket ID generates the value and it is stored in state as the sensitive `secret`. With `secret_wo` " +
			"and `secret_wo_version` (Terraform or OpenTofu 1.11 or later) you supply the value instead and nothing secret is " +
			"stored in state.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The ID Pocket ID gave the secret (a UUID).",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"client_id": schema.StringAttribute{
				Description: "The ID of the OIDC client the secret belongs to (`pocketid_client.<name>.id`). Changing it creates a new secret on the other client.",
				Required:    true,
				Validators:  []validator.String{clientRefIDValidator{}},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"expires_at": schema.StringAttribute{
				Description: "When the secret stops authenticating the client, as an RFC 3339 timestamp " +
					"(for example `2027-01-31T00:00:00Z`); it must be in the future when the secret is created. Unset, the secret " +
					"never expires. Pocket ID cannot change a secret's expiry, so a different time creates a new secret; the " +
					"same time written differently (another offset) changes nothing. An expired secret is not replaced " +
					"automatically: `is_active` turns false.",
				Optional:   true,
				Validators: []validator.String{clientSecretTimestampValidator{}},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplaceIf(clientSecretExpiryChanged,
						"A different expiry time requires a new secret.",
						"A different expiry time requires a new secret."),
				},
			},
			"secret": schema.StringAttribute{
				Description: "The secret's value when Pocket ID generated it (32 letters and digits). Null when `secret_wo` " +
					"supplied the value, and after an import, because Pocket ID returns a value only when the secret is created.",
				Computed:  true,
				Sensitive: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"secret_wo": schema.StringAttribute{
				Description: "A value for the secret that you supply, at least 16 printable ASCII characters. Write-only: it is " +
					"sent when the secret is created and never stored in plan or state. Requires `secret_wo_version`. Changing " +
					"only this value changes nothing; change `secret_wo_version` to create a secret with the new value. " +
					"Needs Terraform or OpenTofu 1.11 or later.",
				Optional:   true,
				Sensitive:  true,
				WriteOnly:  true,
				Validators: []validator.String{clientSecretValueValidator{}, stringvalidator.AlsoRequires(path.MatchRoot("secret_wo_version"))},
			},
			"secret_wo_version": schema.StringAttribute{
				Description: "Any string, required with `secret_wo`. Changing it creates a new secret with the current " +
					"`secret_wo` value (and, with `create_before_destroy`, revokes the old one afterwards).",
				Optional:   true,
				Validators: []validator.String{stringvalidator.AlsoRequires(path.MatchRoot("secret_wo")), stringvalidator.LengthAtLeast(1)},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"prefix": schema.StringAttribute{
				Description: "The secret's first 4 characters, which Pocket ID keeps in clear text and shows to tell secrets apart.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": schema.StringAttribute{
				Description: "When the secret was created (RFC 3339, UTC).",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"is_active": schema.BoolAttribute{
				Description: "Whether the secret still authenticates the client: false once `expires_at` has passed. Refreshed on every read.",
				Computed:    true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *clientSecretResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData))
		return
	}
	r.client = c
}

// ModifyPlan refuses, while planning a new secret, an expiry that is not in
// the future (Pocket ID would refuse it), and plans `secret` as null when
// the value is known to come from secret_wo.
func (r *clientSecretResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || !req.State.Raw.IsNull() {
		return
	}
	var plan, config clientSecretResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !plan.ExpiresAt.IsNull() && !plan.ExpiresAt.IsUnknown() {
		if expires, err := time.Parse(time.RFC3339, plan.ExpiresAt.ValueString()); err == nil && !expires.After(time.Now()) {
			resp.Diagnostics.AddAttributeError(path.Root("expires_at"), "Client secret expiry is not in the future",
				"A new secret's expires_at must be in the future; Pocket ID refuses any other.")
		}
	}
	// Only a value known to be supplied rules out a generated one. An
	// unknown secret_wo (say, a conditional decided during the apply) may
	// still turn out null, and then Pocket ID generates the value, so
	// secret stays unknown.
	if !config.SecretWO.IsNull() && !config.SecretWO.IsUnknown() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("secret"), types.StringNull())...)
	}
}

func (r *clientSecretResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, config clientSecretResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	clientID := plan.ClientID.ValueString()

	supported, err := r.client.VersionAtLeast(ctx, client.ClientSecretsMinVersion)
	if err != nil {
		resp.Diagnostics.AddError("Cannot verify the Pocket ID version",
			"pocketid_client_secret needs Pocket ID "+client.ClientSecretsMinVersion+" or later, and the server's version could not be read; no secret was created: "+err.Error())
		return
	}
	if !supported {
		resp.Diagnostics.AddError("Pocket ID version not supported",
			"pocketid_client_secret needs Pocket ID "+client.ClientSecretsMinVersion+" or later, where a client can hold several secrets. "+
				"Older versions keep a single secret per client; manage it through pocketid_client. No secret was created.")
		return
	}

	opts := &client.ClientSecretOptions{}
	writeOnly := !config.SecretWO.IsNull()
	if writeOnly {
		if config.SecretWO.IsUnknown() {
			resp.Diagnostics.AddAttributeError(path.Root("secret_wo"), "Client secret value unknown",
				"secret_wo has no known value at apply time; no secret was created.")
			return
		}
		opts.Value = config.SecretWO.ValueString()
	}
	if !plan.ExpiresAt.IsNull() {
		expires, err := time.Parse(time.RFC3339, plan.ExpiresAt.ValueString())
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("expires_at"), "Invalid client secret expiry",
				"expires_at must be an RFC 3339 timestamp such as 2027-01-31T00:00:00Z; no secret was created.")
			return
		}
		if !expires.After(time.Now()) {
			resp.Diagnostics.AddAttributeError(path.Root("expires_at"), "Client secret expiry is not in the future",
				"expires_at must be in the future when the secret is created; Pocket ID refuses any other. No secret was created.")
			return
		}
		opts.ExpiresAt = &expires
	}

	// From the list before the POST to the list after an uncertain result,
	// no other secret change of this process may touch the client
	// (client_secret_lock.go).
	unlock := lockClientSecrets(clientID)
	defer unlock()

	// Check what Pocket ID would refuse, so the refusal can be explained;
	// its error for each of these is the same "validation_failed".
	current, err := r.client.GetClient(ctx, clientID)
	if err != nil {
		if client.IsNotFound(err, client.ResourceOIDCClient) {
			resp.Diagnostics.AddAttributeError(path.Root("client_id"), "OIDC client not found",
				"Pocket ID has no OIDC client "+clientID+"; no secret was created.")
			return
		}
		resp.Diagnostics.AddError("Error reading OIDC client", "Could not read OIDC client "+clientID+" before creating a secret; no secret was created: "+err.Error())
		return
	}
	if current.IsPublic {
		resp.Diagnostics.AddAttributeError(path.Root("client_id"), "Public clients have no secrets",
			"OIDC client "+clientID+" is a public client, and Pocket ID gives public clients no secrets; no secret was created.")
		return
	}
	before, err := r.client.ListClientSecrets(ctx, clientID)
	if err != nil {
		resp.Diagnostics.AddError("Error listing client secrets", "Could not list the secrets of OIDC client "+clientID+" before creating one; no secret was created: "+err.Error())
		return
	}
	if len(before) >= client.MaxClientSecrets {
		resp.Diagnostics.AddError("Client secret limit reached", clientSecretLimitDetail(clientID, before)+" No secret was created.")
		return
	}

	tflog.Debug(ctx, "Creating client secret", map[string]any{"client_id": clientID, "caller_supplied_value": writeOnly, "expires": opts.ExpiresAt != nil})
	created, err := r.client.CreateClientSecret(ctx, clientID, opts)
	if created != nil && clientSecretListed(before, created.ID) {
		// A new secret has a new ID. A response naming one the client
		// already held (a faulty or replayed answer) proves nothing about
		// what was created, and taking ownership of that ID would let a
		// later replacement revoke a secret this resource never created,
		// such as the one pocketid_client holds.
		r.reportFailedCreate(ctx, clientID, before,
			fmt.Errorf("the response named client secret %s, which the client already held before this request, as the new secret", created.ID), resp)
		return
	}
	if err != nil {
		if created != nil {
			// The secret exists, named by its (new) ID, but the rest of the
			// response is missing or unusable. Keep its identity in state,
			// and nothing the response said beyond it: Terraform marks the
			// resource tainted, and the next apply revokes and replaces it.
			identityOnly := errors.Is(err, client.ErrCreatedSecretMalformed)
			r.keepCreated(ctx, &plan, created, "", identityOnly, resp)
			summary, problem := "Client secret value not returned", "returned no value"
			if identityOnly {
				summary, problem = "Client secret response unusable", "the rest of its response could not be used, so only its ID is kept"
			}
			resp.Diagnostics.AddError(summary,
				"Pocket ID created client secret "+created.ID+" on OIDC client "+clientID+", but "+problem+". The secret is kept in state "+
					"so that the next apply revokes and replaces it; it was not retried.")
			return
		}
		r.reportFailedCreate(ctx, clientID, before, err, resp)
		return
	}

	// Verify that Pocket ID used what it was given: a server that ignored the
	// value or the expiry would leave a secret other than the one planned.
	var mismatch []string
	if writeOnly && created.Value != opts.Value {
		mismatch = append(mismatch, "it did not use the value from secret_wo")
	}
	if !sameExpiry(opts.ExpiresAt, created.ExpiresAt) {
		mismatch = append(mismatch, "its expiry is not the planned expires_at")
	}
	if len(mismatch) > 0 {
		value := created.Value
		if writeOnly {
			value = ""
		}
		r.keepCreated(ctx, &plan, created, value, false, resp)
		resp.Diagnostics.AddError("Client secret not created as planned",
			"Pocket ID created client secret "+created.ID+" on OIDC client "+clientID+", but "+strings.Join(mismatch, " and ")+". "+
				"The secret is kept in state so that the next apply revokes and replaces it.")
		return
	}

	value := created.Value
	if writeOnly {
		value = ""
	}
	r.keepCreated(ctx, &plan, created, value, false, resp)
}

// keepCreated records a created secret in state. value is stored as `secret`
// unless it is empty (a caller-supplied value, or none returned). With
// identityOnly only the ID is recorded: the response's other fields were not
// usable, so nothing else from it is stored.
func (r *clientSecretResource) keepCreated(ctx context.Context, plan *clientSecretResourceModel, created *client.ClientSecret, value string, identityOnly bool, resp *resource.CreateResponse) {
	plan.ID = types.StringValue(created.ID)
	if identityOnly {
		plan.Prefix = types.StringNull()
		plan.CreatedAt = types.StringNull()
		plan.IsActive = types.BoolNull()
		plan.Secret = types.StringNull()
		plan.SecretWO = types.StringNull()
		resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
		return
	}
	plan.Prefix = types.StringValue(created.Prefix)
	plan.CreatedAt = types.StringValue(formatSecretTime(created.CreatedAt))
	plan.IsActive = types.BoolValue(created.IsActive)
	if value == "" {
		plan.Secret = types.StringNull()
	} else {
		plan.Secret = types.StringValue(value)
	}
	if plan.ExpiresAt.IsNull() && created.ExpiresAt != nil {
		plan.ExpiresAt = types.StringValue(formatSecretTime(*created.ExpiresAt))
	}
	plan.SecretWO = types.StringNull()
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// reportFailedCreate explains a create that returned no secret. A definite
// rejection created nothing. Otherwise the secret may exist: nothing is
// retried, and the client's secrets are listed (IDs and prefixes, never
// values) with those that appeared during the attempt marked, so that the
// operator can import or revoke it.
func (r *clientSecretResource) reportFailedCreate(ctx context.Context, clientID string, before []client.ClientSecretMetadata, cause error, resp *resource.CreateResponse) {
	if client.IsDefiniteRejection(cause) {
		detail := "Pocket ID refused to create a secret for OIDC client " + clientID + " (" + cause.Error() + "); no secret was created."
		if client.IsNotFound(cause, client.ResourceOIDCClient) {
			detail = "OIDC client " + clientID + " no longer exists; no secret was created."
		} else if after, err := r.client.ListClientSecrets(ctx, clientID); err == nil && len(after) >= client.MaxClientSecrets {
			detail = clientSecretLimitDetail(clientID, after) + " Pocket ID refused another; no secret was created."
		}
		resp.Diagnostics.AddError("Error creating client secret", detail)
		return
	}

	detail := "The request to create a secret for OIDC client " + clientID + " failed (" + cause.Error() + ") after it was sent, " +
		"so a secret may have been created. The request was not retried. "
	after, err := r.client.ListClientSecrets(ctx, clientID)
	if err != nil {
		detail += "The client's secrets could not be listed afterwards (" + err.Error() + "); list them in Pocket ID before applying again."
		resp.Diagnostics.AddError("Client secret creation result uncertain", detail)
		return
	}
	known := map[string]bool{}
	for _, secret := range before {
		known[secret.ID] = true
	}
	var appeared int
	for _, secret := range after {
		if !known[secret.ID] {
			appeared++
		}
	}
	switch appeared {
	case 0:
		detail += "No new secret is listed on the client, so none was most likely created; check again before applying if the request may still be running."
	case 1:
		detail += "One secret appeared on the client during the attempt (marked below). If it is this one, import it as " +
			clientID + "/<secret_id> (its value cannot be recovered, so replace it afterwards) or revoke it in Pocket ID before applying again."
	default:
		detail += fmt.Sprintf("%d secrets appeared on the client during the attempt (marked below), so another process is also creating secrets; "+
			"find this one before applying again.", appeared)
	}
	detail += "\n\nSecrets on the client now:\n" + describeClientSecrets(after, known)
	resp.Diagnostics.AddError("Client secret creation result uncertain", detail)
}

// clientSecretListed reports whether a secret with this ID is in the list.
func clientSecretListed(secrets []client.ClientSecretMetadata, id string) bool {
	for _, secret := range secrets {
		if secret.ID == id {
			return true
		}
	}
	return false
}

func clientSecretLimitDetail(clientID string, secrets []client.ClientSecretMetadata) string {
	return fmt.Sprintf("OIDC client %s holds %d secrets, and Pocket ID allows at most %d per client, expired ones included. "+
		"Revoke secrets it no longer needs (in Pocket ID, or by destroying their pocketid_client_secret resources) first.\n\n"+
		"Secrets on the client:\n%s", clientID, len(secrets), client.MaxClientSecrets, describeClientSecrets(secrets, nil))
}

// describeClientSecrets lists secrets by ID and prefix, one per line, never
// with a value. Secrets whose IDs are not in known are marked as new (known
// nil marks none).
func describeClientSecrets(secrets []client.ClientSecretMetadata, known map[string]bool) string {
	if len(secrets) == 0 {
		return "  (none)"
	}
	var lines []string
	for _, secret := range secrets {
		prefix := secret.Prefix
		if prefix == "" {
			prefix = "(none)"
		}
		state := "active"
		if !secret.IsActive {
			state = "expired"
		}
		line := fmt.Sprintf("  - %s  prefix %s  created %s  %s", secret.ID, prefix, formatSecretTime(secret.CreatedAt), state)
		if known != nil && !known[secret.ID] {
			line += "  [new since this attempt]"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (r *clientSecretResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state clientSecretResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	clientID, secretID := state.ClientID.ValueString(), state.ID.ValueString()

	secrets, err := r.client.ListClientSecrets(ctx, clientID)
	if err != nil {
		if client.IsNotFound(err, client.ResourceOIDCClient) {
			tflog.Warn(ctx, "OIDC client no longer exists; removing its client secret from state", map[string]any{"client_id": clientID, "secret_id": secretID})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading client secret", "Could not list the secrets of OIDC client "+clientID+": "+err.Error())
		return
	}
	var found *client.ClientSecretMetadata
	for i := range secrets {
		if secrets[i].ID == secretID {
			found = &secrets[i]
			break
		}
	}
	if found == nil {
		tflog.Warn(ctx, "Client secret no longer exists; removing it from state", map[string]any{"client_id": clientID, "secret_id": secretID})
		resp.State.RemoveResource(ctx)
		return
	}

	state.Prefix = types.StringValue(found.Prefix)
	state.CreatedAt = types.StringValue(formatSecretTime(found.CreatedAt))
	state.IsActive = types.BoolValue(found.IsActive)
	// Keep the configured spelling of an unchanged expiry.
	switch {
	case found.ExpiresAt == nil:
		state.ExpiresAt = types.StringNull()
	case !namesTime(state.ExpiresAt, *found.ExpiresAt):
		state.ExpiresAt = types.StringValue(formatSecretTime(*found.ExpiresAt))
	}
	state.SecretWO = types.StringNull()
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update runs only when expires_at is written differently but names the
// same time (every other change replaces the secret). Pocket ID holds
// nothing to change, so the plan is recorded as it is.
func (r *clientSecretResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan clientSecretResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.SecretWO = types.StringNull()
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *clientSecretResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state clientSecretResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	clientID, secretID := state.ClientID.ValueString(), state.ID.ValueString()
	unlock := lockClientSecrets(clientID)
	defer unlock()

	tflog.Debug(ctx, "Revoking client secret", map[string]any{"client_id": clientID, "secret_id": secretID})
	if err := r.client.RevokeClientSecret(ctx, clientID, secretID); err != nil {
		resp.Diagnostics.AddError("Error revoking client secret",
			"The secret stays in state because its revocation is not confirmed. "+err.Error())
	}
}

func (r *clientSecretResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	clientID, secretID, ok := strings.Cut(req.ID, "/")
	if !ok || client.ValidateClientID(clientID) != nil || client.ValidateUUID("client secret", secretID) != nil {
		resp.Diagnostics.AddError("Unexpected import identifier",
			"Expected <client_id>/<secret_id>: an OIDC client ID, a slash, and the secret's ID (a UUID, as listed in Pocket ID).")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("client_id"), clientID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), secretID)...)
}

// formatSecretTime writes a time as RFC 3339 in UTC, with fractional
// seconds only when it has them.
func formatSecretTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// namesTime reports whether value is an RFC 3339 timestamp of the time t.
func namesTime(value types.String, t time.Time) bool {
	if value.IsNull() || value.IsUnknown() {
		return false
	}
	parsed, err := time.Parse(time.RFC3339, value.ValueString())
	return err == nil && parsed.Equal(t)
}

func sameExpiry(want, got *time.Time) bool {
	if want == nil || got == nil {
		return want == nil && got == nil
	}
	return want.Equal(*got)
}

// clientSecretExpiryChanged requires a new secret unless the planned and
// prior expires_at name the same time.
func clientSecretExpiryChanged(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
	if req.PlanValue.IsUnknown() || req.PlanValue.IsNull() || req.StateValue.IsNull() {
		resp.RequiresReplace = true
		return
	}
	planned, errPlanned := time.Parse(time.RFC3339, req.PlanValue.ValueString())
	prior, errPrior := time.Parse(time.RFC3339, req.StateValue.ValueString())
	resp.RequiresReplace = errPlanned != nil || errPrior != nil || !planned.Equal(prior)
}

// clientRefIDValidator checks an OIDC client ID against Pocket ID's rule
// (client.ValidateClientID) when the value is known.
type clientRefIDValidator struct{}

func (clientRefIDValidator) Description(context.Context) string {
	return "must be an OIDC client ID: 2 to 128 letters, digits, '.', '_' or '-'"
}

func (v clientRefIDValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (clientRefIDValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if err := client.ValidateClientID(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid OIDC client ID", err.Error())
	}
}

// clientSecretTimestampValidator requires an RFC 3339 timestamp.
type clientSecretTimestampValidator struct{}

func (clientSecretTimestampValidator) Description(context.Context) string {
	return "must be an RFC 3339 timestamp, such as 2027-01-31T00:00:00Z"
}

func (v clientSecretTimestampValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (clientSecretTimestampValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if _, err := time.Parse(time.RFC3339, req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid timestamp",
			"The value must be an RFC 3339 timestamp, such as 2027-01-31T00:00:00Z or 2027-01-31T09:00:00+02:00.")
	}
}

// clientSecretValueValidator applies Pocket ID's rule for a caller-supplied
// secret (binding "min=16,printascii") without ever repeating the value.
type clientSecretValueValidator struct{}

func (clientSecretValueValidator) Description(context.Context) string {
	return "must be at least 16 printable ASCII characters"
}

func (v clientSecretValueValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (clientSecretValueValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	value := req.ConfigValue.ValueString()
	if len(value) < 16 {
		resp.Diagnostics.AddAttributeError(req.Path, "Client secret value too short",
			"Pocket ID requires a client secret of at least 16 characters.")
		return
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] > 0x7e {
			resp.Diagnostics.AddAttributeError(req.Path, "Client secret value not printable ASCII",
				"Pocket ID requires a client secret made only of printable ASCII characters (space to tilde).")
			return
		}
	}
}
