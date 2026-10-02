package resources

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ resource.Resource              = &scimSyncResource{}
	_ resource.ResourceWithConfigure = &scimSyncResource{}
)

func init() { register(NewScimSyncResource) }

// scimSyncUUIDPattern is the form of a SCIM service provider ID.
var scimSyncUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// NewScimSyncResource is a helper function to simplify the provider implementation.
func NewScimSyncResource() resource.Resource {
	return &scimSyncResource{}
}

// scimSyncResource defines the resource implementation.
type scimSyncResource struct {
	client *client.Client
}

// scimSyncResourceModel maps the resource schema data.
type scimSyncResourceModel struct {
	ID                types.String `tfsdk:"id"`
	ServiceProviderID types.String `tfsdk:"service_provider_id"`
	Triggers          types.Map    `tfsdk:"triggers"`
	SyncedAt          types.String `tfsdk:"synced_at"`
}

func (r *scimSyncResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_scim_sync"
}

func (r *scimSyncResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Triggers a SCIM synchronization of one SCIM service provider in Pocket-ID.",
		MarkdownDescription: "Triggers a SCIM synchronization of one `pocketid_scim_service_provider`. This is an action " +
			"resource: applying it pushes the users and groups the client may see to the SCIM endpoint, and changing " +
			"`triggers` (or the service provider) forces a new sync (the resource is recreated). Pocket-ID runs the sync " +
			"inside the request, so the apply waits for it and fails if it fails; a failure can leave part of the work " +
			"applied. A sync of many users can take longer than the provider's `timeout` (30 seconds by default): " +
			"raise it if applies of this resource time out, and check the service provider's `last_synced_at` to see " +
			"whether the sync finished. A request is never repeated automatically.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Identifier of the sync resource (the ID of the SCIM service provider).",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"service_provider_id": schema.StringAttribute{
				MarkdownDescription: "The ID of the SCIM service provider to synchronize, " +
					"for example `pocketid_scim_service_provider.example.id`. Changing it runs a new sync.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.RegexMatches(scimSyncUUIDPattern, "must be a UUID (8-4-4-4-12 hexadecimal digits)"),
				},
			},
			"triggers": schema.MapAttribute{
				MarkdownDescription: "Arbitrary map of values that forces a new SCIM sync when it changes. Typically " +
					"wired to values that should be pushed to the SCIM endpoint, for example the members of a group. " +
					"If omitted, the sync runs only once (on create).",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.RequiresReplace(),
				},
			},
			"synced_at": schema.StringAttribute{
				MarkdownDescription: "Timestamp (RFC3339) of the most recent sync triggered by this resource.",
				Computed:            true,
			},
		},
	}
}

func (r *scimSyncResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *scimSyncResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan scimSyncResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := plan.ServiceProviderID.ValueString()
	tflog.Debug(ctx, "triggering SCIM sync", map[string]any{"service_provider_id": id})
	if err := r.client.SyncScimServiceProvider(ctx, id); err != nil {
		resp.Diagnostics.AddError("Error syncing SCIM service provider", scimSyncFailure(id, err))
		return
	}

	plan.ID = types.StringValue(id)
	plan.SyncedAt = types.StringValue(time.Now().UTC().Format(time.RFC3339))

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// scimSyncFailure words a failed sync for the reader: what is known about the
// SCIM endpoint's part of it, and whether the request got an answer at all.
func scimSyncFailure(id string, err error) string {
	detail := "Could not run the SCIM sync for service provider " + id + ": " + err.Error()
	var status *client.HTTPError
	switch {
	case client.IsNotFound(err, client.ResourceSCIMServiceProvider):
		detail += ". It no longer exists in Pocket-ID."
	case errors.As(err, &status) && status.StatusCode >= http.StatusInternalServerError:
		detail += ". Pocket-ID runs the sync inside the request and reports a failure of the SCIM endpoint as a server " +
			"error; its log names the cause. Part of the sync may have been applied."
	case !errors.As(err, &status):
		detail += ". The request got no answer, so the sync may still be running or may have finished: check " +
			"last_synced_at on the SCIM service provider before applying again, and raise the provider's timeout " +
			"if syncs are slow."
	}
	return detail
}

func (r *scimSyncResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	// A sync is an action with no readable server-side state; preserve prior state.
	var data scimSyncResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *scimSyncResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	// All configurable attributes force replacement, so Update is never expected.
	resp.Diagnostics.AddError(
		"Update not supported",
		"pocketid_scim_sync cannot be updated in place. Change the triggers map to run a new sync.",
	)
}

func (r *scimSyncResource) Delete(ctx context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
	// A sync cannot be undone; there is nothing to delete server-side.
	tflog.Trace(ctx, "removing pocketid_scim_sync from state (no server-side action)")
}
