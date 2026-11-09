package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/jamalshahverdiev/terraform-provider-apicurio/internal/client"
)

var (
	_ resource.Resource                   = &artifactVersionResource{}
	_ resource.ResourceWithConfigure      = &artifactVersionResource{}
	_ resource.ResourceWithImportState    = &artifactVersionResource{}
	_ resource.ResourceWithModifyPlan     = &artifactVersionResource{}
	_ resource.ResourceWithValidateConfig = &artifactVersionResource{}
)

// versionIDRe is the registry's pattern for version and branch ids.
var versionIDRe = regexp.MustCompile(`^[a-zA-Z0-9._\-+]{1,256}$`)

// NewArtifactVersionResource returns the apicurio_artifact_version resource.
func NewArtifactVersionResource() resource.Resource { return &artifactVersionResource{} }

type artifactVersionResource struct {
	client *client.Client
}

type artifactVersionModel struct {
	ID           types.String `tfsdk:"id"`
	GroupID      types.String `tfsdk:"group_id"`
	ArtifactID   types.String `tfsdk:"artifact_id"`
	Version      types.String `tfsdk:"version"`
	Content      contentValue `tfsdk:"content"`
	ContentType  types.String `tfsdk:"content_type"`
	References   types.List   `tfsdk:"references"`
	Draft        types.Bool   `tfsdk:"draft"`
	State        types.String `tfsdk:"state"`
	Name         types.String `tfsdk:"name"`
	Description  types.String `tfsdk:"description"`
	Labels       types.Map    `tfsdk:"labels"`
	GlobalID     types.Int64  `tfsdk:"global_id"`
	ContentID    types.Int64  `tfsdk:"content_id"`
	ArtifactType types.String `tfsdk:"artifact_type"`
	Owner        types.String `tfsdk:"owner"`
	CreatedOn    types.String `tfsdk:"created_on"`
	ModifiedOn   types.String `tfsdk:"modified_on"`
	ModifiedBy   types.String `tfsdk:"modified_by"`
}

func (r *artifactVersionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_artifact_version"
}

func (r *artifactVersionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "One explicit, immutable version of an artifact: the GitOps style where `v1`, `v2`, ... are separate " +
			"resources, and the way to manage version metadata, state and drafts. The artifact must exist " +
			"(an `apicurio_artifact` without `content`, or imported).\n\n" +
			"Content is immutable once the version is not a draft: a change of `content` or `references` replaces the " +
			"resource (the version is deleted and created again under the same id, which needs " +
			"`apicurio.rest.deletion.artifact-version.enabled=true`). While the version is a `DRAFT`, content changes are " +
			"applied in place (`apicurio.rest.mutability.artifact-version-content.enabled=true` on the server). " +
			"Do not manage the versions of one artifact both with this resource and with `apicurio_artifact.content`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "`group_id/artifact_id/version`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"group_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Group of the artifact. Changing it replaces the version.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 512), stringvalidator.RegexMatches(noSlashRe, "must not contain a slash")},
			},
			"artifact_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Artifact the version belongs to. Changing it replaces the version.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 512)},
			},
			"version": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Version id (`[a-zA-Z0-9._-+]`, 1 to 256 characters), for example `1.0.0`. Numbered by the registry " +
					"(`1`, `2`, ...) when unset. Changing it replaces the version.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()},
				Validators:    []validator.String{stringvalidator.RegexMatches(versionIDRe, "must consist of alphanumeric characters or '._-+' and have 1 to 256 characters")},
			},
			"content": schema.StringAttribute{
				Required:   true,
				CustomType: contentType{},
				MarkdownDescription: "Content of the version, usually `file(\"schema.avsc\")`. Compared as a document (reformatting is not " +
					"a change). A change replaces the version unless it is a draft, where the content is updated in place. " +
					"The registry does not serve the content of a `DISABLED` version: after importing one, the configured content " +
					"is recorded as is and verified once the version is enabled again.",
			},
			"content_type": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Media type of `content`; defaults from the artifact type (see `apicurio_artifact`). The configured " +
					"value is kept in state (the registry reports the media type of the first upload of identical bytes).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"references": schema.ListNestedAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "References from the content to other artifact versions. Order does not matter. A change replaces the version (or updates a draft in place).",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"group_id":    schema.StringAttribute{Required: true, MarkdownDescription: "Group of the referenced artifact."},
						"artifact_id": schema.StringAttribute{Required: true, MarkdownDescription: "Referenced artifact."},
						"version":     schema.StringAttribute{Required: true, MarkdownDescription: "Referenced version."},
						"name":        schema.StringAttribute{Required: true, MarkdownDescription: "Name the content uses for the reference."},
					},
				},
				PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()},
			},
			"draft": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Create the version as a `DRAFT`: no rule is applied, the content can be changed in place, and the " +
					"version is not part of the `latest` branch until `state` moves it out of `DRAFT`. Defaults to `false`. " +
					"Changing it replaces the version.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace(), boolplanmodifier.UseStateForUnknown()},
			},
			"state": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "State of the version: `ENABLED`, `DISABLED`, `DEPRECATED` or `SUNSET` (Apicurio Registry 3.3 and later). " +
					"Defaults to `ENABLED`, or `DRAFT` for a draft. `DRAFT` cannot be configured (use `draft`), and a version never " +
					"returns to `DRAFT`. Leaving `DRAFT` applies the configured rules against the newest version of the artifact " +
					"that is not disabled (drafts included; the draft itself when nothing newer exists). " +
					"The content of a `DISABLED` version cannot be read from the registry; the provider keeps the last known content.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Validators:    []validator.String{stringvalidator.OneOf(client.StateEnabled, client.StateDisabled, client.StateDeprecated, client.StateSunset)},
			},
			"name":        schema.StringAttribute{Optional: true, MarkdownDescription: "Name of the version."},
			"description": schema.StringAttribute{Optional: true, MarkdownDescription: "Description of the version."},
			"labels": schema.MapAttribute{
				Optional:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Labels of the version.",
			},
			"global_id": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Global id of the version.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"content_id": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Content id (shared by versions with identical content).",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"artifact_type": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Type of the artifact.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"owner": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Owner of the version.",
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

func (r *artifactVersionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

func (r *artifactVersionResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg artifactVersionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !cfg.Content.IsNull() && !cfg.Content.IsUnknown() && cfg.Content.ValueString() == "" {
		resp.Diagnostics.AddAttributeError(path.Root("content"), "Empty Content", "The registry refuses an empty version content.")
	}
}

// ModifyPlan decides between an in-place content update (drafts) and a
// replacement, keeps equivalent content and reference order, gates SUNSET on
// the server version and marks the computed attributes that will change.
func (r *artifactVersionResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var cfg artifactVersionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if isSet(cfg.State) && cfg.State.ValueString() == client.StateSunset && r.client != nil &&
		r.client.ServerVersion() != "" && !r.client.VersionAtLeast(3, 3) {
		resp.Diagnostics.AddAttributeError(path.Root("state"), "SUNSET Needs Apicurio Registry 3.3",
			fmt.Sprintf("The registry at %s runs version %s; the SUNSET state exists since 3.3.0.", r.client.ServerURL(), r.client.ServerVersion()))
		return
	}
	if req.State.Raw.IsNull() {
		return // create: everything computed is unknown
	}
	var plan, state artifactVersionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	set := func(p string, v any) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(p), v)...)
	}

	contentChanged := !state.Content.IsNull() && !contentEqual(cfg.Content.ValueString(), state.Content.ValueString())
	refsChanged := false
	if !cfg.References.IsNull() && !cfg.References.IsUnknown() {
		want, d := referencesToServer(ctx, plan.References)
		resp.Diagnostics.Append(d...)
		have, d := referencesToServer(ctx, state.References)
		resp.Diagnostics.Append(d...)
		refsChanged = !referencesEqual(want, have)
	}
	switch {
	case (contentChanged || refsChanged) && state.State.ValueString() == client.StateDraft:
		// in place: the content id follows the new content, the global id stays
		set("content_id", types.Int64Unknown())
		set("modified_on", types.StringUnknown())
		set("modified_by", types.StringUnknown())
	case contentChanged:
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root("content"))
	case refsChanged:
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root("references"))
	default:
		// Same document, other formatting: keep the state's text. An imported
		// DISABLED version has no known content; the configured one is recorded.
		if !state.Content.IsNull() && !plan.Content.Equal(state.Content) {
			set("content", state.Content)
		}
		if !cfg.References.IsNull() && !cfg.References.IsUnknown() && !state.References.IsNull() && !plan.References.Equal(state.References) {
			set("references", state.References)
		}
	}
	stateChanged := isSet(cfg.State) && cfg.State.ValueString() != state.State.ValueString()
	metaChanged := !plan.Name.Equal(state.Name) || !plan.Description.Equal(state.Description) || !plan.Labels.Equal(state.Labels)
	if stateChanged || metaChanged {
		set("modified_on", types.StringUnknown())
		set("modified_by", types.StringUnknown())
	}
	// Leaving DRAFT gives the content its real (deduplicated) content id on
	// Apicurio Registry 3.2 and later.
	if stateChanged && state.State.ValueString() == client.StateDraft {
		set("content_id", types.Int64Unknown())
	}
}

func (r *artifactVersionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan artifactVersionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	groupID, artifactID := plan.GroupID.ValueString(), plan.ArtifactID.ValueString()
	ref := groupID + "/" + artifactID

	art, err := r.client.GetArtifact(ctx, groupID, artifactID)
	if err != nil {
		if client.IsNotFound(err) {
			resp.Diagnostics.AddError("Apicurio Artifact Does Not Exist",
				fmt.Sprintf("Artifact %q does not exist. Create it first with an apicurio_artifact resource (without `content`) and reference it from this version.\n\n%s", ref, err))
			return
		}
		resp.Diagnostics.AddError("Unable to Read Apicurio Artifact", err.Error())
		return
	}
	labels, diags := labelsToServer(ctx, plan.Labels)
	resp.Diagnostics.Append(diags...)
	refs, diags := referencesToServer(ctx, plan.References)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ct := plan.ContentType.ValueString()
	if !isSet(plan.ContentType) {
		ct = defaultContentType(plan.Content.ValueString(), art.ArtifactType)
	}
	draft := !plan.Draft.IsNull() && !plan.Draft.IsUnknown() && plan.Draft.ValueBool()
	in := client.VersionCreate{
		Version:     plan.Version.ValueString(),
		Content:     client.Content{Content: plan.Content.ValueString(), ContentType: ct, References: refs},
		Name:        plan.Name.ValueString(),
		Description: plan.Description.ValueString(),
		Labels:      labels,
		IsDraft:     draft,
	}
	created, err := r.client.CreateVersion(ctx, groupID, artifactID, in, false)
	if err != nil {
		var rv *client.RuleViolationError
		switch {
		case errors.Is(err, client.ErrAlreadyExists):
			resp.Diagnostics.AddError("Apicurio Artifact Version Already Exists",
				fmt.Sprintf("Version %q of artifact %q already exists. Import it instead:\n\n  terraform import apicurio_artifact_version.<name> %s/%s",
					plan.Version.ValueString(), ref, ref, plan.Version.ValueString()))
		case errors.As(err, &rv):
			resp.Diagnostics.AddAttributeError(path.Root("content"), "Apicurio Registry Refused the Version", err.Error())
		default:
			resp.Diagnostics.AddError("Unable to Create Apicurio Artifact Version", err.Error())
		}
		return
	}
	version := created.Version
	plan.Version = types.StringValue(version)
	plan.Draft = types.BoolValue(draft)
	// A version created directly as DISABLED has no readable content, so read
	// keeps content_type and references as given: resolve them now.
	if plan.ContentType.IsUnknown() {
		plan.ContentType = types.StringValue(ct)
	}
	if plan.References.IsUnknown() {
		refList, d := referencesFromServer(ctx, plan.References, refs)
		resp.Diagnostics.Append(d...)
		if resp.Diagnostics.HasError() {
			return
		}
		plan.References = refList
	}

	// A configured state other than the one the version was created in.
	initial := client.StateEnabled
	if draft {
		initial = client.StateDraft
	}
	var stateDiags diagsT
	if isSet(plan.State) && plan.State.ValueString() != initial {
		stateDiags = r.setState(ctx, groupID, artifactID, version, plan.State.ValueString())
	}
	state, found, diags := r.read(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.Diagnostics.AddError("Unable to Read Apicurio Artifact Version After Creation", fmt.Sprintf("Version %q of %q was created but could not be read back.", version, ref))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	// Reported after the state is saved, so the created version is tracked.
	resp.Diagnostics.Append(stateDiags...)
}

func (r *artifactVersionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior artifactVersionModel
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
		tflog.Warn(ctx, "artifact version no longer exists, removing it from state", map[string]any{"id": prior.ID.ValueString()})
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *artifactVersionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state, cfg artifactVersionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	groupID, artifactID, version := plan.GroupID.ValueString(), plan.ArtifactID.ValueString(), plan.Version.ValueString()

	if !plan.Name.Equal(state.Name) || !plan.Description.Equal(state.Description) || !plan.Labels.Equal(state.Labels) {
		labels, diags := labelsToServer(ctx, plan.Labels)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		err := r.client.UpdateVersion(ctx, groupID, artifactID, version, client.VersionUpdate{
			Name: plan.Name.ValueString(), Description: plan.Description.ValueString(), Labels: labels,
		})
		if err != nil {
			if client.IsNotFound(err) {
				resp.Diagnostics.AddError("Apicurio Artifact Version Disappeared", fmt.Sprintf("Version %q no longer exists; run terraform apply again to recreate it.", plan.ID.ValueString()))
				resp.State.RemoveResource(ctx)
				return
			}
			resp.Diagnostics.AddError("Unable to Update Apicurio Artifact Version", err.Error())
			return
		}
	}

	// Content and references change in place only while the version is a
	// draft; otherwise ModifyPlan has turned the change into a replacement.
	if state.State.ValueString() == client.StateDraft {
		contentChanged := !contentEqual(plan.Content.ValueString(), state.Content.ValueString())
		want, diags := referencesToServer(ctx, plan.References)
		resp.Diagnostics.Append(diags...)
		have, diags := referencesToServer(ctx, state.References)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		if contentChanged || !referencesEqual(want, have) {
			ct := plan.ContentType.ValueString()
			if !isSet(plan.ContentType) {
				ct = defaultContentType(plan.Content.ValueString(), state.ArtifactType.ValueString())
			}
			err := r.client.UpdateVersionContent(ctx, groupID, artifactID, version, client.Content{Content: plan.Content.ValueString(), ContentType: ct, References: want})
			if err != nil {
				if detail, ok := operationDisabledDetail(err); ok {
					resp.Diagnostics.AddError("Draft Content Updates Are Disabled on the Apicurio Registry", detail)
					return
				}
				resp.Diagnostics.AddAttributeError(path.Root("content"), "Unable to Update the Content of the Draft Version", err.Error())
				return
			}
		}
	}

	if isSet(plan.State) && plan.State.ValueString() != state.State.ValueString() {
		resp.Diagnostics.Append(r.setState(ctx, groupID, artifactID, version, plan.State.ValueString())...)
		if resp.Diagnostics.HasError() {
			// keep the state in sync with the registry even though the transition failed
			if newState, found, diags := r.read(ctx, plan); found && !diags.HasError() {
				resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
			}
			return
		}
	}

	newState, found, diags := r.read(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.Diagnostics.AddError("Unable to Read Apicurio Artifact Version After Update", fmt.Sprintf("Version %q could not be read back.", plan.ID.ValueString()))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *artifactVersionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state artifactVersionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.DeleteVersion(ctx, state.GroupID.ValueString(), state.ArtifactID.ValueString(), state.Version.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		if detail, ok := operationDisabledDetail(err); ok {
			resp.Diagnostics.AddError("Version Deletion Is Disabled on the Apicurio Registry", detail)
			return
		}
		resp.Diagnostics.AddError("Unable to Delete Apicurio Artifact Version", err.Error())
	}
}

func (r *artifactVersionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	groupID, artifactID, version, ok := splitGroupArtifactVersion(req.ID)
	if !ok {
		resp.Diagnostics.AddError("Invalid Import ID", fmt.Sprintf("Expected `group_id/artifact_id/version`, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("group_id"), groupID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("artifact_id"), artifactID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("version"), version)...)
}

// setState moves the version to a state and renders the registry's refusals.
func (r *artifactVersionResource) setState(ctx context.Context, groupID, artifactID, version, state string) diagsT {
	var diags diagsT
	if err := r.client.UpdateVersionState(ctx, groupID, artifactID, version, state, false); err != nil {
		var rv *client.RuleViolationError
		var ae *client.APIError
		switch {
		case errors.As(err, &rv):
			diags.AddAttributeError(path.Root("state"), "Apicurio Registry Refused the State Transition",
				fmt.Sprintf("Leaving DRAFT applies the configured rules against the artifact's newest version.\n\n%s", err))
		case state == client.StateSunset && errors.As(err, &ae) && ae.Status == 400:
			diags.AddAttributeError(path.Root("state"), "SUNSET Needs Apicurio Registry 3.3",
				fmt.Sprintf("The registry refused the SUNSET state; it exists since Apicurio Registry 3.3.0.\n\n%s", err))
		default:
			diags.AddAttributeError(path.Root("state"), "Unable to Change the State of the Apicurio Artifact Version", err.Error())
		}
	}
	return diags
}

// read fetches the version and maps it to state, keeping the null/empty
// choices of prior. The content of a DISABLED version cannot be read (the
// registry answers 404), so the last known content stays in state.
func (r *artifactVersionResource) read(ctx context.Context, prior artifactVersionModel) (artifactVersionModel, bool, diagsT) {
	var diags diagsT
	groupID, artifactID, version := prior.GroupID.ValueString(), prior.ArtifactID.ValueString(), prior.Version.ValueString()
	v, err := r.client.GetVersion(ctx, groupID, artifactID, version)
	if err != nil {
		if client.IsNotFound(err) {
			return artifactVersionModel{}, false, diags
		}
		diags.AddError("Unable to Read Apicurio Artifact Version", err.Error())
		return artifactVersionModel{}, false, diags
	}
	labels, d := labelsFromServer(ctx, prior.Labels, v.Labels)
	diags.Append(d...)
	state := artifactVersionModel{
		ID:           types.StringValue(groupID + "/" + artifactID + "/" + v.Version),
		GroupID:      types.StringValue(groupID),
		ArtifactID:   types.StringValue(artifactID),
		Version:      types.StringValue(v.Version),
		Content:      prior.Content,
		ContentType:  prior.ContentType,
		References:   prior.References,
		Draft:        prior.Draft,
		State:        types.StringValue(v.State),
		Name:         stringFromServer(prior.Name, v.Name),
		Description:  stringFromServer(prior.Description, v.Description),
		Labels:       labels,
		GlobalID:     types.Int64Value(v.GlobalID),
		ContentID:    types.Int64Value(v.ContentID),
		ArtifactType: types.StringValue(v.ArtifactType),
		Owner:        types.StringValue(v.Owner),
		CreatedOn:    types.StringValue(v.CreatedOn),
		ModifiedOn:   types.StringValue(v.ModifiedOn),
		ModifiedBy:   types.StringValue(v.ModifiedBy),
	}
	if state.Draft.IsNull() || state.Draft.IsUnknown() {
		state.Draft = types.BoolValue(v.State == client.StateDraft)
	}
	if v.State == client.StateDisabled && !prior.Content.IsNull() && !prior.Content.IsUnknown() {
		return state, true, diags // content hidden by the registry; keep what we know
	}
	vc, err := r.client.GetVersionContent(ctx, groupID, artifactID, v.Version, client.ReferencesPreserve)
	if err != nil {
		if client.IsNotFound(err) && v.State == client.StateDisabled {
			state.Content = contentNull()
			state.ContentType = types.StringNull()
			state.References = types.ListNull(referenceObjectType)
			return state, true, diags
		}
		diags.AddError("Unable to Read the Content of Apicurio Artifact Version", err.Error())
		return artifactVersionModel{}, false, diags
	}
	refs, err := r.client.GetVersionReferences(ctx, groupID, artifactID, v.Version)
	if err != nil {
		diags.AddError("Unable to Read the References of Apicurio Artifact Version", err.Error())
		return artifactVersionModel{}, false, diags
	}
	refList, d := referencesFromServer(ctx, prior.References, refs)
	diags.Append(d...)
	if !prior.References.IsNull() && !prior.References.IsUnknown() {
		if priorRefs, d2 := referencesToServer(ctx, prior.References); !d2.HasError() && referencesEqual(priorRefs, refs) {
			refList = prior.References
		}
	}
	state.Content = newContent(string(vc.Content))
	state.ContentType = types.StringValue(mediaType(vc.ContentType))
	if isSet(prior.ContentType) {
		state.ContentType = prior.ContentType
	}
	state.References = refList
	return state, true, diags
}
