package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/jamalshahverdiev/terraform-provider-apicurio/internal/client"
)

var (
	_ resource.Resource                   = &groupResource{}
	_ resource.ResourceWithConfigure      = &groupResource{}
	_ resource.ResourceWithImportState    = &groupResource{}
	_ resource.ResourceWithModifyPlan     = &groupResource{}
	_ resource.ResourceWithValidateConfig = &groupResource{}
)

// NewGroupResource returns the apicurio_group resource.
func NewGroupResource() resource.Resource { return &groupResource{} }

type groupResource struct {
	client *client.Client
}

type groupModel struct {
	ID           types.String `tfsdk:"id"`
	GroupID      types.String `tfsdk:"group_id"`
	Description  types.String `tfsdk:"description"`
	Labels       types.Map    `tfsdk:"labels"`
	Owner        types.String `tfsdk:"owner"`
	ForceDestroy types.Bool   `tfsdk:"force_destroy"`
	CreatedOn    types.String `tfsdk:"created_on"`
	ModifiedOn   types.String `tfsdk:"modified_on"`
	ModifiedBy   types.String `tfsdk:"modified_by"`
}

func (r *groupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group"
}

func (r *groupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A group of artifacts. Groups are also created implicitly by the first artifact " +
			"stored in them (when `apicurio.storage.enable-automatic-group-creation` is on, the default); " +
			"such a group can be imported. The group `default` is implicit and cannot be managed.\n\n" +
			"Deleting a group deletes every artifact in it, which is why the resource refuses to destroy a " +
			"group that still contains artifacts unless `force_destroy` is `true`. The registry must have " +
			"`apicurio.rest.deletion.group.enabled=true` for the deletion to be allowed at all.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Same as `group_id`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"group_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Identifier of the group (1 to 512 characters, no `/`). `default` is reserved. Changing it replaces the group.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 512),
					stringvalidator.NoneOf("default"),
					stringvalidator.RegexMatches(noSlashRe, "must not contain a slash"),
				},
			},
			"description": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Description of the group.",
			},
			"labels": schema.MapAttribute{
				Optional:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Labels (key/value pairs) of the group.",
			},
			"owner": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Owner of the group. Defaults to the user the provider authenticates as (empty on a registry without " +
					"authentication). Apicurio Registry 3.0.x does not store the owner of a group; setting it there is reported as an error.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"force_destroy": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
				MarkdownDescription: "Delete the group even when it still contains artifacts (they are deleted with it). Defaults to `false`.",
			},
			"created_on": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Creation time (RFC 3339).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"modified_on": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Last modification time (RFC 3339).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"modified_by": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "User of the last modification.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *groupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

func (r *groupResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg groupModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !cfg.Owner.IsNull() && !cfg.Owner.IsUnknown() && cfg.Owner.ValueString() == "" {
		resp.Diagnostics.AddAttributeError(path.Root("owner"), "Empty Owner", "The registry refuses an empty owner; leave `owner` unset to keep the server-assigned one.")
	}
}

// ModifyPlan marks the modification timestamps unknown when an update is
// planned, so the refreshed values do not conflict with the plan.
func (r *groupResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var plan, state groupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if groupMetadataChanged(plan, state) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("modified_on"), types.StringUnknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("modified_by"), types.StringUnknown())...)
	}
}

func (r *groupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan groupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	labels, diags := labelsToServer(ctx, plan.Labels)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	groupID := plan.GroupID.ValueString()
	_, err := r.client.CreateGroup(ctx, client.GroupCreate{
		GroupID:     groupID,
		Description: plan.Description.ValueString(),
		Labels:      labels,
	})
	if err != nil {
		if errors.Is(err, client.ErrAlreadyExists) {
			resp.Diagnostics.AddError("Apicurio Group Already Exists",
				fmt.Sprintf("Group %q already exists in the registry (groups are also created implicitly by their first artifact). "+
					"Import it instead:\n\n  terraform import apicurio_group.<name> %s", groupID, groupID))
			return
		}
		resp.Diagnostics.AddError("Unable to Create Apicurio Group", err.Error())
		return
	}
	if isSet(plan.Owner) {
		if err := r.client.UpdateGroup(ctx, groupID, client.GroupUpdate{
			Description: plan.Description.ValueString(),
			Labels:      labels,
			Owner:       plan.Owner.ValueString(),
		}); err != nil {
			resp.Diagnostics.AddError("Unable to Set the Owner of Apicurio Group", err.Error())
			return
		}
	}
	state, found, diags := r.read(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.Diagnostics.AddError("Unable to Read Apicurio Group After Creation", fmt.Sprintf("Group %q was created but could not be read back.", groupID))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(groupOwnerStored(plan, state)...)
}

func (r *groupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior groupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if prior.GroupID.IsNull() || prior.GroupID.ValueString() == "" {
		prior.GroupID = prior.ID // import by id
	}
	state, found, diags := r.read(ctx, prior)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		tflog.Warn(ctx, "group no longer exists, removing it from state", map[string]any{"group_id": prior.GroupID.ValueString()})
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *groupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state groupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	labels, diags := labelsToServer(ctx, plan.Labels)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	groupID := plan.GroupID.ValueString()
	// Only provider-side attributes (force_destroy) may have changed; then
	// the registry is left alone so its modification time does not move.
	if groupMetadataChanged(plan, state) {
		update := client.GroupUpdate{Description: plan.Description.ValueString(), Labels: labels}
		switch {
		case isSet(plan.Owner):
			update.Owner = plan.Owner.ValueString()
		case isSet(state.Owner):
			update.Owner = state.Owner.ValueString() // keep it on servers that replace the whole set (3.0.x)
		}
		if err := r.client.UpdateGroup(ctx, groupID, update); err != nil {
			if client.IsNotFound(err) {
				resp.Diagnostics.AddError("Apicurio Group Disappeared", fmt.Sprintf("Group %q no longer exists; run terraform apply again to recreate it.", groupID))
				resp.State.RemoveResource(ctx)
				return
			}
			resp.Diagnostics.AddError("Unable to Update Apicurio Group", err.Error())
			return
		}
	}
	newState, found, diags := r.read(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.Diagnostics.AddError("Unable to Read Apicurio Group After Update", fmt.Sprintf("Group %q could not be read back.", groupID))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
	resp.Diagnostics.Append(groupOwnerStored(plan, newState)...)
}

// groupOwnerStored reports a clear error when the registry accepted but did
// not store the configured owner. Apicurio Registry 3.0.x answers 204 to a
// group update with an owner and keeps the old one; 3.1 and later store it.
func groupOwnerStored(plan, state groupModel) diagsT {
	var diags diagsT
	if isSet(plan.Owner) && state.Owner.ValueString() != plan.Owner.ValueString() {
		diags.AddAttributeError(path.Root("owner"), "Owner Not Stored by This Registry",
			fmt.Sprintf("The registry accepted the update but kept the owner %q instead of %q. Apicurio Registry 3.0.x "+
				"ignores the owner of a group (3.1 and later store it). Remove `owner` from the configuration or upgrade the registry; "+
				"the group itself exists and is recorded in the state.", state.Owner.ValueString(), plan.Owner.ValueString()))
	}
	return diags
}

func (r *groupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state groupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	groupID := state.GroupID.ValueString()
	if !state.ForceDestroy.ValueBool() {
		artifacts, err := r.client.ListArtifacts(ctx, groupID)
		if err != nil && !client.IsNotFound(err) {
			resp.Diagnostics.AddError("Unable to Check the Artifacts of Apicurio Group", err.Error())
			return
		}
		if len(artifacts) > 0 {
			ids := make([]string, 0, len(artifacts))
			for i, a := range artifacts {
				if i == 5 {
					ids = append(ids, "...")
					break
				}
				ids = append(ids, a.ArtifactID)
			}
			resp.Diagnostics.AddError("Apicurio Group Still Contains Artifacts",
				fmt.Sprintf("Group %q contains %d artifact(s) (%s). Deleting the group would delete them too. "+
					"Remove them first, or set force_destroy = true to delete them with the group.", groupID, len(artifacts), joinMax(ids)))
			return
		}
	}
	if err := r.client.DeleteGroup(ctx, groupID); err != nil {
		if client.IsNotFound(err) {
			return
		}
		if detail, ok := deletionDisabledDetail(err); ok {
			resp.Diagnostics.AddError("Group Deletion Is Disabled on the Apicurio Registry", detail)
			return
		}
		resp.Diagnostics.AddError("Unable to Delete Apicurio Group", err.Error())
	}
}

func (r *groupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("group_id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("force_destroy"), false)...)
}

// read fetches the group and maps it to state, keeping the null/empty
// choices of prior.
func (r *groupResource) read(ctx context.Context, prior groupModel) (groupModel, bool, diagsT) {
	var diags diagsT
	groupID := prior.GroupID.ValueString()
	g, err := r.client.GetGroup(ctx, groupID)
	if err != nil {
		if client.IsNotFound(err) {
			return groupModel{}, false, diags
		}
		diags.AddError("Unable to Read Apicurio Group", err.Error())
		return groupModel{}, false, diags
	}
	labels, d := labelsFromServer(ctx, prior.Labels, g.Labels)
	diags.Append(d...)
	state := groupModel{
		ID:           types.StringValue(g.GroupID),
		GroupID:      types.StringValue(g.GroupID),
		Description:  stringFromServer(prior.Description, g.Description),
		Labels:       labels,
		Owner:        types.StringValue(g.Owner),
		ForceDestroy: prior.ForceDestroy,
		CreatedOn:    types.StringValue(g.CreatedOn),
		ModifiedOn:   types.StringValue(g.ModifiedOn),
		ModifiedBy:   types.StringValue(g.ModifiedBy),
	}
	if state.ForceDestroy.IsNull() || state.ForceDestroy.IsUnknown() {
		state.ForceDestroy = types.BoolValue(false)
	}
	return state, true, diags
}

// groupMetadataChanged reports whether the plan changes anything the
// registry stores (description, labels, owner).
func groupMetadataChanged(plan, state groupModel) bool {
	return !plan.Description.Equal(state.Description) ||
		!plan.Labels.Equal(state.Labels) ||
		(isSet(plan.Owner) && !plan.Owner.Equal(state.Owner))
}
