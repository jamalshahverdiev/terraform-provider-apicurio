package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/jamalshahverdiev/terraform-provider-apicurio/internal/client"
)

var (
	_ resource.Resource                   = &branchResource{}
	_ resource.ResourceWithConfigure      = &branchResource{}
	_ resource.ResourceWithImportState    = &branchResource{}
	_ resource.ResourceWithModifyPlan     = &branchResource{}
	_ resource.ResourceWithValidateConfig = &branchResource{}
)

// NewBranchResource returns the apicurio_branch resource.
func NewBranchResource() resource.Resource { return &branchResource{} }

type branchResource struct {
	client *client.Client
}

type branchModel struct {
	ID            types.String `tfsdk:"id"`
	GroupID       types.String `tfsdk:"group_id"`
	ArtifactID    types.String `tfsdk:"artifact_id"`
	BranchID      types.String `tfsdk:"branch_id"`
	Description   types.String `tfsdk:"description"`
	Versions      types.List   `tfsdk:"versions"`
	SystemDefined types.Bool   `tfsdk:"system_defined"`
	Owner         types.String `tfsdk:"owner"`
	CreatedOn     types.String `tfsdk:"created_on"`
	ModifiedOn    types.String `tfsdk:"modified_on"`
	ModifiedBy    types.String `tfsdk:"modified_by"`
}

func (r *branchResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_branch"
}

func (r *branchResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A branch of an artifact: an ordered list of its versions. The system branches `latest` " +
			"(every non-draft version) and `drafts` are maintained by the registry and cannot be managed. " +
			"`versions` is replaced as a whole on every change; when unset, the resource leaves the list alone " +
			"and shows what the registry has. Deleting a version removes it from every branch.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "`group_id/artifact_id/branch_id`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"group_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Group of the artifact. Changing it replaces the branch.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 512), stringvalidator.RegexMatches(noSlashRe, "must not contain a slash")},
			},
			"artifact_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Artifact the branch belongs to. Changing it replaces the branch.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 512)},
			},
			"branch_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Branch id (`[a-zA-Z0-9._-+]`, 1 to 256 characters); `latest` and `drafts` are reserved. Changing it replaces the branch.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					stringvalidator.RegexMatches(versionIDRe, "must consist of alphanumeric characters or '._-+' and have 1 to 256 characters"),
					stringvalidator.NoneOf(client.BranchIDLatest, client.BranchIDDrafts),
				},
			},
			"description": schema.StringAttribute{Optional: true, MarkdownDescription: "Description of the branch."},
			"versions": schema.ListAttribute{
				Optional:            true,
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Version ids in branch order (oldest first, the way they were added). Every version must exist; duplicates are refused. Replaced as a whole when changed; unset leaves the registry's list alone.",
				PlanModifiers:       []planmodifier.List{listplanmodifier.UseStateForUnknown()},
			},
			"system_defined": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether the registry maintains the branch itself (always `false` for a managed branch).",
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"owner": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Owner of the branch.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
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

func (r *branchResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

func (r *branchResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg branchModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || cfg.Versions.IsNull() || cfg.Versions.IsUnknown() {
		return
	}
	versions := listToStrings(cfg.Versions)
	seen := map[string]bool{}
	for _, v := range versions {
		if v == "" {
			continue // unknown element at validation time
		}
		if !versionIDRe.MatchString(v) {
			resp.Diagnostics.AddAttributeError(path.Root("versions"), "Invalid Version ID",
				fmt.Sprintf("%q must consist of alphanumeric characters or '._-+' and have 1 to 256 characters.", v))
		}
		if seen[v] {
			resp.Diagnostics.AddAttributeError(path.Root("versions"), "Duplicate Version",
				fmt.Sprintf("Version %q is listed more than once; the registry refuses duplicates in a branch.", v))
		}
		seen[v] = true
	}
}

// ModifyPlan marks the modification timestamps unknown when an update is planned.
func (r *branchResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var plan, state branchModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !plan.Description.Equal(state.Description) || !plan.Versions.Equal(state.Versions) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("modified_on"), types.StringUnknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("modified_by"), types.StringUnknown())...)
	}
}

func (r *branchResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan branchModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	groupID, artifactID, branchID := plan.GroupID.ValueString(), plan.ArtifactID.ValueString(), plan.BranchID.ValueString()
	ref := groupID + "/" + artifactID
	var versions []string
	if !plan.Versions.IsNull() && !plan.Versions.IsUnknown() {
		versions = listToStrings(plan.Versions)
		resp.Diagnostics.Append(r.checkVersionsExist(ctx, groupID, artifactID, versions)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	_, err := r.client.CreateBranch(ctx, groupID, artifactID, client.BranchCreate{BranchID: branchID, Description: plan.Description.ValueString(), Versions: versions})
	if err != nil {
		switch {
		case errors.Is(err, client.ErrAlreadyExists):
			resp.Diagnostics.AddError("Apicurio Branch Already Exists",
				fmt.Sprintf("Branch %q of artifact %q already exists. Import it instead:\n\n  terraform import apicurio_branch.<name> %s/%s", branchID, ref, ref, branchID))
		case client.IsNotFound(err):
			resp.Diagnostics.AddError("Apicurio Artifact Does Not Exist",
				fmt.Sprintf("Artifact %q (or one of the listed versions) does not exist; create it first.\n\n%s", ref, err))
		default:
			resp.Diagnostics.AddError("Unable to Create Apicurio Branch", err.Error())
		}
		return
	}
	state, found, diags := r.read(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.Diagnostics.AddError("Unable to Read Apicurio Branch After Creation", fmt.Sprintf("Branch %q of %q was created but could not be read back.", branchID, ref))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *branchResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior branchModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state, found, diags := r.read(ctx, prior)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		tflog.Warn(ctx, "branch no longer exists, removing it from state", map[string]any{"id": prior.ID.ValueString()})
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *branchResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state branchModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	groupID, artifactID, branchID := plan.GroupID.ValueString(), plan.ArtifactID.ValueString(), plan.BranchID.ValueString()
	if !plan.Description.Equal(state.Description) {
		if err := r.client.UpdateBranch(ctx, groupID, artifactID, branchID, plan.Description.ValueString()); err != nil {
			if client.IsNotFound(err) {
				resp.Diagnostics.AddError("Apicurio Branch Disappeared", fmt.Sprintf("Branch %q no longer exists; run terraform apply again to recreate it.", plan.ID.ValueString()))
				resp.State.RemoveResource(ctx)
				return
			}
			resp.Diagnostics.AddError("Unable to Update Apicurio Branch", err.Error())
			return
		}
	}
	if !plan.Versions.IsNull() && !plan.Versions.IsUnknown() && !plan.Versions.Equal(state.Versions) {
		versions := listToStrings(plan.Versions)
		resp.Diagnostics.Append(r.checkVersionsExist(ctx, groupID, artifactID, versions)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if err := r.client.ReplaceBranchVersions(ctx, groupID, artifactID, branchID, versions); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("versions"), "Unable to Replace the Versions of Apicurio Branch", err.Error())
			return
		}
	}
	newState, found, diags := r.read(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.Diagnostics.AddError("Unable to Read Apicurio Branch After Update", fmt.Sprintf("Branch %q could not be read back.", plan.ID.ValueString()))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *branchResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state branchModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.DeleteBranch(ctx, state.GroupID.ValueString(), state.ArtifactID.ValueString(), state.BranchID.ValueString())
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Unable to Delete Apicurio Branch", err.Error())
	}
}

func (r *branchResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	groupID, artifactID, branchID, ok := splitGroupArtifactVersion(req.ID)
	if !ok {
		resp.Diagnostics.AddError("Invalid Import ID", fmt.Sprintf("Expected `group_id/artifact_id/branch_id`, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("group_id"), groupID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("artifact_id"), artifactID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("branch_id"), branchID)...)
}

// checkVersionsExist refuses versions the artifact does not have: the
// registry answers a 500 (PUT) or a 404 (POST) for them.
func (r *branchResource) checkVersionsExist(ctx context.Context, groupID, artifactID string, versions []string) diagsT {
	var diags diagsT
	if len(versions) == 0 {
		return diags
	}
	existing, err := r.client.ListVersions(ctx, groupID, artifactID)
	if err != nil {
		if client.IsNotFound(err) {
			diags.AddError("Apicurio Artifact Does Not Exist", fmt.Sprintf("Artifact %q does not exist; create it before its branches.", groupID+"/"+artifactID))
			return diags
		}
		diags.AddError("Unable to List the Versions of Apicurio Artifact", err.Error())
		return diags
	}
	have := map[string]bool{}
	for _, v := range existing {
		have[v.Version] = true
	}
	var missing []string
	for _, v := range versions {
		if !have[v] {
			missing = append(missing, v)
		}
	}
	if len(missing) > 0 {
		diags.AddAttributeError(path.Root("versions"), "Unknown Versions",
			fmt.Sprintf("Artifact %q has no version(s) %s. Create them first (apicurio_artifact_version) and reference them, so Terraform orders the operations.",
				groupID+"/"+artifactID, strings.Join(missing, ", ")))
	}
	return diags
}

// read fetches the branch and its versions (the registry lists them
// newest-added first; the attribute shows branch order, oldest first).
func (r *branchResource) read(ctx context.Context, prior branchModel) (branchModel, bool, diagsT) {
	var diags diagsT
	groupID, artifactID, branchID := prior.GroupID.ValueString(), prior.ArtifactID.ValueString(), prior.BranchID.ValueString()
	b, err := r.client.GetBranch(ctx, groupID, artifactID, branchID)
	if err != nil {
		if client.IsNotFound(err) {
			return branchModel{}, false, diags
		}
		diags.AddError("Unable to Read Apicurio Branch", err.Error())
		return branchModel{}, false, diags
	}
	listed, err := r.client.ListBranchVersions(ctx, groupID, artifactID, branchID)
	if err != nil {
		diags.AddError("Unable to Read the Versions of Apicurio Branch", err.Error())
		return branchModel{}, false, diags
	}
	ids := make([]attr.Value, 0, len(listed))
	for i := len(listed) - 1; i >= 0; i-- {
		ids = append(ids, types.StringValue(listed[i].Version))
	}
	versions := types.ListValueMust(types.StringType, ids)
	if len(ids) == 0 && (prior.Versions.IsNull() || prior.Versions.IsUnknown()) {
		versions = types.ListNull(types.StringType)
	}
	return branchModel{
		ID:            types.StringValue(groupID + "/" + artifactID + "/" + branchID),
		GroupID:       types.StringValue(groupID),
		ArtifactID:    types.StringValue(artifactID),
		BranchID:      types.StringValue(branchID),
		Description:   stringFromServer(prior.Description, b.Description),
		Versions:      versions,
		SystemDefined: types.BoolValue(b.SystemDefined),
		Owner:         types.StringValue(b.Owner),
		CreatedOn:     types.StringValue(b.CreatedOn),
		ModifiedOn:    types.StringValue(b.ModifiedOn),
		ModifiedBy:    types.StringValue(b.ModifiedBy),
	}, true, diags
}
