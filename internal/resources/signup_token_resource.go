package resources

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ resource.Resource              = &signupTokenResource{}
	_ resource.ResourceWithConfigure = &signupTokenResource{}
)

func init() { register(NewSignupTokenResource) }

// NewSignupTokenResource is a helper function to simplify the provider implementation.
func NewSignupTokenResource() resource.Resource {
	return &signupTokenResource{}
}

const (
	// signupTokenMaxTTL is Pocket ID's limit for a token's lifetime (dto
	// validation "ttl": more than one second, at most 31 days).
	signupTokenMaxTTL = 31 * 24 * time.Hour
	// signupTokenDefaultTTL and signupTokenDefaultUsageLimit are the defaults
	// this resource applies when they are not configured: the server's own
	// default lifetime and the Pocket ID interface's default of a single use.
	signupTokenDefaultTTL        = "1h"
	signupTokenDefaultUsageLimit = 1
)

var signupTokenGroupIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// signupTokenResource defines the resource implementation.
type signupTokenResource struct {
	client *client.Client
}

// signupTokenResourceModel maps the resource schema data.
type signupTokenResourceModel struct {
	ID           types.String `tfsdk:"id"`
	TTL          types.String `tfsdk:"ttl"`
	UsageLimit   types.Int64  `tfsdk:"usage_limit"`
	UserGroupIDs types.Set    `tfsdk:"user_group_ids"`
	Token        types.String `tfsdk:"token"`
	ExpiresAt    types.String `tfsdk:"expires_at"`
	CreatedAt    types.String `tfsdk:"created_at"`
	UsageCount   types.Int64  `tfsdk:"usage_count"`
	Expired      types.Bool   `tfsdk:"expired"`
}

func (r *signupTokenResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_signup_token"
}

func (r *signupTokenResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a signup token in Pocket-ID: a token that lets people register an account.",
		MarkdownDescription: "Manages a signup token in Pocket-ID: a token that lets people register an account, " +
			"optionally joining groups, until it expires or its uses run out. Pocket-ID has no way to change a token, so " +
			"every input forces a new token to be created. The token value is a secret: it is stored in the state as a " +
			"sensitive value, and anyone who has it can register until the token expires.\n\n" +
			"Pocket-ID deletes a token on its own once it has expired, and the API cannot tell an expired token from one an " +
			"administrator deleted. Such a token stays in the state with `expired = true` and the next plan does not create " +
			"a new one; use `terraform apply -replace=<address>` (or `-replace` with OpenTofu) to get a fresh token. " +
			"A token whose uses are used up is still listed until it expires, so it keeps `expired = false`; compare " +
			"`usage_count` with `usage_limit`.\n\n" +
			"There is no import: the token value is only known to the configuration that created it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The ID of the signup token (not the token value).",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"ttl": schema.StringAttribute{
				MarkdownDescription: "How long the token is valid, as a Go duration such as `24h` or `90m`: more than " +
					"1 second and at most `744h` (31 days). Defaults to `" + signupTokenDefaultTTL + "`. Changing it, even to an " +
					"equal duration written differently, creates a new token.",
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString(signupTokenDefaultTTL),
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{signupTokenTTLValidator{}},
			},
			"usage_limit": schema.Int64Attribute{
				MarkdownDescription: "How many people can register with the token, 1 to 100. Defaults to `1`. " +
					"Changing it creates a new token.",
				Optional: true,
				Computed: true,
				Default:  int64default.StaticInt64(signupTokenDefaultUsageLimit),
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
				Validators: []validator.Int64{int64validator.Between(1, 100)},
			},
			"user_group_ids": schema.SetAttribute{
				MarkdownDescription: "IDs of the groups people who register with the token join. Pocket-ID silently ignores an " +
					"ID that names no group; the provider fails when it did and deletes the token again. If that deletion " +
					"cannot be confirmed, the token (valid until it expires) is recorded as tainted so the next apply " +
					"deletes it. Changing the set creates a new token.",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Set{
					setplanmodifier.RequiresReplace(),
				},
				Validators: []validator.Set{
					uuidSetValidator{what: "user group IDs", pattern: signupTokenGroupIDPattern},
				},
			},
			"token": schema.StringAttribute{
				MarkdownDescription: "The signup token value that people present to register. Sensitive.",
				Computed:            true,
				Sensitive:           true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"expires_at": schema.StringAttribute{
				MarkdownDescription: "When the token expires, in RFC3339 format.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "When the token was created, in RFC3339 format.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"usage_count": schema.Int64Attribute{
				MarkdownDescription: "How many people have registered with the token, as of the last refresh. " +
					"Not updated once the token is `expired`.",
				Computed:      true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"expired": schema.BoolAttribute{
				MarkdownDescription: "Whether Pocket-ID no longer lists the token, as of the last refresh: it expired and was " +
					"purged, or an administrator deleted it. The resource stays in the state; see the resource description.",
				Computed:      true,
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *signupTokenResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	r.client = c
}

// validateSignupTokenTTL applies Pocket ID's rule for a signup token's lifetime.
func validateSignupTokenTTL(value string) error {
	ttl, err := time.ParseDuration(value)
	if err != nil {
		return errors.New("must be a Go duration such as \"24h\" or \"90m\"")
	}
	if ttl <= time.Second || ttl > signupTokenMaxTTL {
		return errors.New("must be longer than 1 second and at most 744h (31 days)")
	}
	return nil
}

type signupTokenTTLValidator struct{}

func (signupTokenTTLValidator) Description(context.Context) string {
	return "a Go duration longer than 1 second and at most 744h"
}

func (v signupTokenTTLValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (signupTokenTTLValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if err := validateSignupTokenTTL(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid ttl", "The ttl "+err.Error()+".")
	}
}

// signupTokenGroupSet returns the group IDs as a set. An empty result is null
// when prior was null (no groups configured) and an empty set otherwise, so
// the state matches the way the configuration said "no groups".
func signupTokenGroupSet(ids []string, prior types.Set) types.Set {
	if len(ids) == 0 && (prior.IsNull() || prior.IsUnknown()) {
		return types.SetNull(types.StringType)
	}
	elements := make([]attr.Value, 0, len(ids))
	for _, id := range ids {
		elements = append(elements, types.StringValue(id))
	}
	return types.SetValueMust(types.StringType, elements)
}

// signupTokenGroupDifference returns the IDs in a that are not in b, sorted.
func signupTokenGroupDifference(a, b []string) []string {
	inB := make(map[string]struct{}, len(b))
	for _, id := range b {
		inB[strings.ToLower(id)] = struct{}{}
	}
	var out []string
	for _, id := range a {
		if _, ok := inB[strings.ToLower(id)]; !ok {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func (r *signupTokenResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan signupTokenResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The configuration may have held an unknown value at plan time.
	if err := validateSignupTokenTTL(plan.TTL.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("ttl"), "Invalid ttl", "The ttl "+err.Error()+".")
		return
	}
	var requested []string
	if !plan.UserGroupIDs.IsNull() {
		resp.Diagnostics.Append(plan.UserGroupIDs.ElementsAs(ctx, &requested, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	sort.Strings(requested)

	tflog.Debug(ctx, "creating signup token", map[string]any{
		"ttl": plan.TTL.ValueString(), "usage_limit": plan.UsageLimit.ValueInt64(), "user_groups": len(requested),
	})
	created, err := r.client.CreateSignupToken(ctx, &client.SignupTokenCreateRequest{
		TTL:          plan.TTL.ValueString(),
		UsageLimit:   int(plan.UsageLimit.ValueInt64()),
		UserGroupIDs: requested,
	})
	if created == nil {
		detail := "Could not create the signup token: " + err.Error()
		// A rate limit is answered before the handler runs, so it, like any
		// other 4xx and an identifier refused before sending, is a definite "no".
		var rateLimited *client.RateLimitError
		if !client.IsDefiniteRejection(err) && !errors.As(err, &rateLimited) && !errors.Is(err, client.ErrInvalidIdentifier) {
			detail += ". The request may have reached Pocket-ID, so a token may have been created that this provider " +
				"cannot name; nothing is recorded, and it expires on its own after the ttl. List the tokens with the " +
				"pocketid_signup_tokens data source (or in the Pocket-ID interface) before applying again."
		}
		resp.Diagnostics.AddError("Error creating signup token", detail)
		return
	}

	// The token exists from here on: record it, whatever else is wrong, so
	// its ID and value are never lost while it may still be valid.
	plan.ID = types.StringValue(created.ID)
	plan.Token = types.StringValue(created.Token)
	plan.ExpiresAt = types.StringValue(created.ExpiresAt)
	plan.CreatedAt = types.StringValue(created.CreatedAt)
	plan.UsageCount = types.Int64Value(int64(created.UsageCount))
	plan.Expired = types.BoolValue(false)
	granted := created.UserGroupIDs()
	plan.UserGroupIDs = signupTokenGroupSet(granted, plan.UserGroupIDs)
	limitMatches := int64(created.UsageLimit) == plan.UsageLimit.ValueInt64()
	plan.UsageLimit = types.Int64Value(int64(created.UsageLimit))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)

	// Check what Pocket ID created against what was asked for. A token that
	// fails the check is a live registration credential with the wrong shape
	// (fewer groups than requested, or groups nobody asked for), so it is
	// deleted again rather than left to its ttl.
	var problems []string
	groupsUnknown := errors.Is(err, client.ErrInvalidIdentifier)
	switch {
	case groupsUnknown:
		problems = append(problems, "its answer listed a group whose ID could not be used, so the groups it joins are unknown")
	case err != nil:
		problems = append(problems, "its answer did not carry the token value")
	}
	var missing, unexpected []string
	if !groupsUnknown {
		missing = signupTokenGroupDifference(requested, granted)
		unexpected = signupTokenGroupDifference(granted, requested)
	}
	if len(missing) > 0 {
		problems = append(problems, "ignored these group IDs, which name no group: "+strings.Join(missing, ", "))
	}
	if len(unexpected) > 0 {
		problems = append(problems, "attached groups that were not requested: "+strings.Join(unexpected, ", "))
	}
	if !limitMatches {
		problems = append(problems, fmt.Sprintf("set a usage limit of %d, not the requested value", created.UsageLimit))
	}
	if len(problems) == 0 {
		return
	}

	summary := "Pocket-ID did not create the signup token as requested"
	what := "Pocket-ID created signup token " + created.ID + " but " + strings.Join(problems, "; and ") + ". "
	if cleanupErr := r.deleteSignupTokenConfirmed(ctx, created.ID); cleanupErr != nil {
		// Keep the ID (and the value, if there is one) in state: the token may
		// still be valid, and the taint makes the next apply delete it.
		resp.Diagnostics.AddError(summary,
			what+"One attempt to delete it again could not be confirmed ("+cleanupErr.Error()+"), so the token may still be "+
				"valid until it expires at "+created.ExpiresAt+". It is recorded in the state as tainted, so the next apply "+
				"deletes it; delete it in the Pocket-ID interface to revoke it sooner. Fix the configuration first.")
		return
	}
	resp.State.RemoveResource(ctx)
	resp.Diagnostics.AddError(summary,
		what+"The token was deleted again (confirmed against Pocket-ID's list) and nothing is recorded. Fix the configuration first.")
}

// deleteSignupTokenConfirmed makes one attempt to delete a token and confirms
// that Pocket ID no longer lists it. DELETE answers 204 whether or not the
// token existed, so the answer alone proves nothing; the list does. The error
// is nil only when the token is confirmed gone.
func (r *signupTokenResource) deleteSignupTokenConfirmed(ctx context.Context, id string) error {
	if err := r.client.DeleteSignupToken(ctx, id); err != nil {
		return err
	}
	tokens, err := r.client.ListSignupTokens(ctx)
	if err != nil {
		return fmt.Errorf("the list that confirms it could not be read: %w", err)
	}
	for i := range tokens {
		if client.SameUUID(tokens[i].ID, id) {
			return errors.New("the token is still listed after the deletion")
		}
	}
	return nil
}

// applyListed copies what Pocket ID lists onto the model.
func (r *signupTokenResource) applyListed(model *signupTokenResourceModel, listed *client.SignupToken) {
	// Later requests address the token in the server's spelling of its ID.
	model.ID = types.StringValue(listed.ID)
	model.ExpiresAt = types.StringValue(listed.ExpiresAt)
	model.CreatedAt = types.StringValue(listed.CreatedAt)
	model.UsageLimit = types.Int64Value(int64(listed.UsageLimit))
	model.UsageCount = types.Int64Value(int64(listed.UsageCount))
	model.UserGroupIDs = signupTokenGroupSet(listed.UserGroupIDs(), model.UserGroupIDs)
	model.Expired = types.BoolValue(false)
	if model.Token.IsNull() || model.Token.ValueString() == "" {
		model.Token = types.StringValue(listed.Token)
	}
}

func (r *signupTokenResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state signupTokenResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Pocket ID has no endpoint for one token, so read the list. Any failure
	// to read it is an error: only a list that was read and does not hold the
	// token says anything about the token.
	tokens, err := r.client.ListSignupTokens(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error reading signup tokens", "Could not list the signup tokens: "+err.Error())
		return
	}
	for i := range tokens {
		if client.SameUUID(tokens[i].ID, state.ID.ValueString()) {
			r.applyListed(&state, &tokens[i])
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
	}

	// Not listed: Pocket ID purged it when it expired, or an administrator
	// deleted it, and nothing tells the two apart. Either way the token no
	// longer works and recreating it is not what the configuration asked for,
	// so it stays in state, marked expired, and a plan stays empty.
	tflog.Debug(ctx, "signup token is no longer listed, keeping it in state as expired", map[string]any{"id": state.ID.ValueString()})
	state.Expired = types.BoolValue(true)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *signupTokenResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	// Every configurable attribute forces replacement, so Update is never expected.
	resp.Diagnostics.AddError(
		"Update not supported",
		"pocketid_signup_token cannot be updated in place: Pocket-ID has no way to change a token. Replace it instead.",
	)
}

func (r *signupTokenResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state signupTokenResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Pocket ID answers 204 for a token that is already gone, so there is no
	// not-found to interpret; any error is a real one.
	if err := r.client.DeleteSignupToken(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error deleting signup token", "Could not delete signup token "+state.ID.ValueString()+": "+err.Error())
	}
}
