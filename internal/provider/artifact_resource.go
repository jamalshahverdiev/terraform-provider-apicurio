package provider

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
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
	_ resource.Resource                   = &artifactResource{}
	_ resource.ResourceWithConfigure      = &artifactResource{}
	_ resource.ResourceWithImportState    = &artifactResource{}
	_ resource.ResourceWithModifyPlan     = &artifactResource{}
	_ resource.ResourceWithValidateConfig = &artifactResource{}
)

// NewArtifactResource returns the apicurio_artifact resource.
func NewArtifactResource() resource.Resource { return &artifactResource{} }

type artifactResource struct {
	client *client.Client
}

type artifactModel struct {
	ID           types.String `tfsdk:"id"`
	GroupID      types.String `tfsdk:"group_id"`
	ArtifactID   types.String `tfsdk:"artifact_id"`
	ArtifactType types.String `tfsdk:"artifact_type"`
	Name         types.String `tfsdk:"name"`
	Description  types.String `tfsdk:"description"`
	Labels       types.Map    `tfsdk:"labels"`
	Owner        types.String `tfsdk:"owner"`
	Content      contentValue `tfsdk:"content"`
	ContentType  types.String `tfsdk:"content_type"`
	References   types.List   `tfsdk:"references"`
	Version      types.String `tfsdk:"version"`
	GlobalID     types.Int64  `tfsdk:"global_id"`
	ContentID    types.Int64  `tfsdk:"content_id"`
	CreatedOn    types.String `tfsdk:"created_on"`
	ModifiedOn   types.String `tfsdk:"modified_on"`
	ModifiedBy   types.String `tfsdk:"modified_by"`
}

func (r *artifactResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_artifact"
}

func (r *artifactResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An artifact and, when `content` is set, its latest version.\n\n" +
			"Without `content` the resource manages the artifact's metadata only (name, description, labels, owner); " +
			"versions are then created by `apicurio_artifact_version` resources or outside Terraform. " +
			"With `content`, the resource creates the first version on create and a new version whenever the " +
			"configured content (or `references`) stops being the same document as the stored latest version: " +
			"versions are immutable, so a change is always a new version, numbered by the registry. " +
			"A version created outside Terraform shows up as a diff of `content`, and the next apply creates " +
			"another version so that the latest version matches the configuration again. " +
			"Do not manage the versions of one artifact with both this resource's `content` and " +
			"`apicurio_artifact_version` resources.\n\n" +
			"The group is created by the registry when it does not exist (default server setting). " +
			"Deleting the artifact deletes all its versions and needs `apicurio.rest.deletion.artifact.enabled=true` on the server.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "`group_id/artifact_id`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"group_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Group of the artifact (no `/`). Changing it replaces the artifact.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 512),
					stringvalidator.RegexMatches(noSlashRe, "must not contain a slash"),
				},
			},
			"artifact_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Identifier of the artifact within the group (1 to 512 characters). Changing it replaces the artifact.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 512)},
			},
			"artifact_type": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Type of the artifact: `AVRO`, `PROTOBUF`, `JSON`, `KCONNECT`, `OPENAPI`, `ASYNCAPI`, `GRAPHQL`, " +
					"`WSDL`, `XSD`, `XML`, or any type the registry registers (checked against `GET /admin/config/artifactTypes`). " +
					"When unset, the registry detects it from `content`; it is then required when there is no `content`. Changing it replaces the artifact.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()},
			},
			"name":        schema.StringAttribute{Optional: true, MarkdownDescription: "Human-readable name."},
			"description": schema.StringAttribute{Optional: true, MarkdownDescription: "Description."},
			"labels": schema.MapAttribute{
				Optional:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Labels (key/value pairs) of the artifact.",
			},
			"owner": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Owner of the artifact. Defaults to the user the provider authenticates as.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"content": schema.StringAttribute{
				Optional:   true,
				Computed:   true,
				CustomType: contentType{},
				MarkdownDescription: "Content of the latest version, usually `file(\"schema.avsc\")`. When set, the latest version of " +
					"the artifact is kept equal to it (a change creates a new version). Compared as a document: reformatting " +
					"JSON or YAML, changing line endings or trailing whitespace is not a change. When unset, the attribute " +
					"shows the latest version's content without managing it. An empty string is refused.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"content_type": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Media type of `content`. Defaults by artifact type: `application/json` for `AVRO`, `JSON`, `KCONNECT` " +
					"and JSON `OPENAPI`/`ASYNCAPI`, `application/x-yaml` for YAML `OPENAPI`/`ASYNCAPI`, `application/x-protobuf` for " +
					"`PROTOBUF`, `application/graphql` for `GRAPHQL`, `application/xml` for `XSD`, `WSDL`, `XML`; sniffed from the " +
					"content when `artifact_type` is unset (the registry needs the right hint to detect the type). The registry " +
					"stores content by hash and reports the media type of the first upload of those bytes, so the configured value " +
					"is kept in state and changing only `content_type` does not create a new version.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"references": schema.ListNestedAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "References from the content to other artifact versions (the Avro named types, Protobuf imports or " +
					"JSON Schema `$ref`s it uses). Part of the version: a change creates a new version. Requires `content`, and " +
					"`artifact_type` should be set: the registry cannot detect the type of content whose named types live in other artifacts. " +
					"Order does not matter. When unset, a new version keeps the references of the stored latest version; set `[]` to drop them.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"group_id":    schema.StringAttribute{Required: true, MarkdownDescription: "Group of the referenced artifact."},
						"artifact_id": schema.StringAttribute{Required: true, MarkdownDescription: "Referenced artifact."},
						"version":     schema.StringAttribute{Required: true, MarkdownDescription: "Referenced version."},
						"name":        schema.StringAttribute{Required: true, MarkdownDescription: "Name the content uses for the reference (for example the fully qualified Avro type name)."},
					},
				},
				PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()},
			},
			"version": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Version id of the latest version (numbered by the registry). Null for an artifact without versions.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"global_id": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Global id of the latest version.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"content_id": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Content id of the latest version (shared by versions with identical content).",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"created_on": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Creation time (RFC 3339).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"modified_on": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Last modification time of the metadata (RFC 3339).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"modified_by": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "User of the last metadata modification.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *artifactResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

func (r *artifactResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg artifactModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !cfg.References.IsNull() && !cfg.References.IsUnknown() && len(cfg.References.Elements()) > 0 && cfg.Content.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("references"), "References Without Content",
			"`references` describe the content of a version; set `content` as well.")
	}
	if !cfg.Owner.IsNull() && !cfg.Owner.IsUnknown() && cfg.Owner.ValueString() == "" {
		resp.Diagnostics.AddAttributeError(path.Root("owner"), "Empty Owner", "The registry refuses an empty owner; leave `owner` unset to keep the server-assigned one.")
	}
	if !cfg.Content.IsNull() && !cfg.Content.IsUnknown() && cfg.Content.ValueString() == "" {
		resp.Diagnostics.AddAttributeError(path.Root("content"), "Empty Content",
			"The registry refuses an empty version content; leave `content` unset to manage only the artifact's metadata.")
	}
	if isSet(cfg.Content) && isSet(cfg.ArtifactType) {
		if err := validateContentForType(cfg.Content.ValueString(), cfg.ArtifactType.ValueString()); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("content"), "Invalid Artifact Content", err.Error())
		}
	}
}

// ModifyPlan marks the version attributes unknown when a new version will
// be created, the content type unknown when it follows the new content, and
// the modification timestamps unknown when any update is planned.
func (r *artifactResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var plan, state, cfg artifactModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	newVersion, diags := r.needsNewVersion(ctx, plan, state, cfg)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	metaChanged := artifactMetadataChanged(plan, state)
	set := func(p string, v any) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(p), v)...)
	}
	// The same document in another formatting is not a change: keep the stored
	// value in the plan so Terraform shows no diff.
	if !newVersion && isSet(cfg.Content) && !state.Content.IsNull() && !plan.Content.Equal(state.Content) {
		set("content", state.Content)
	}
	// The same references in another order are not a change either: keep the
	// stored order, which is what the registry reports back.
	if !newVersion && !cfg.References.IsNull() && !cfg.References.IsUnknown() && !state.References.IsNull() && !plan.References.Equal(state.References) {
		set("references", state.References)
	}
	if newVersion {
		set("version", types.StringUnknown())
		set("global_id", types.Int64Unknown())
		set("content_id", types.Int64Unknown())
		if cfg.ContentType.IsNull() {
			set("content_type", types.StringUnknown())
		}
		// With `references` unset the plan keeps the stored references
		// (UseStateForUnknown) and the new version is created with them.
	}
	if newVersion || metaChanged {
		set("modified_on", types.StringUnknown())
		set("modified_by", types.StringUnknown())
	}
}

// needsNewVersion reports whether the configured content or references
// differ from the stored latest version.
func (r *artifactResource) needsNewVersion(ctx context.Context, plan, state, cfg artifactModel) (bool, diagsT) {
	var diags diagsT
	if !isSet(cfg.Content) || cfg.Content.IsUnknown() {
		return false, diags
	}
	if state.Content.IsNull() || !contentEqual(cfg.Content.ValueString(), state.Content.ValueString()) {
		return true, diags
	}
	if !cfg.References.IsNull() && !cfg.References.IsUnknown() {
		want, d := referencesToServer(ctx, plan.References)
		diags.Append(d...)
		have, d := referencesToServer(ctx, state.References)
		diags.Append(d...)
		if !referencesEqual(want, have) {
			return true, diags
		}
	}
	return false, diags
}

func (r *artifactResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan artifactModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	groupID, artifactID := plan.GroupID.ValueString(), plan.ArtifactID.ValueString()
	ref := groupID + "/" + artifactID

	if isSet(plan.ArtifactType) {
		resp.Diagnostics.Append(r.checkArtifactType(ctx, plan.ArtifactType.ValueString())...)
	} else if !isSet(plan.Content) {
		resp.Diagnostics.AddAttributeError(path.Root("artifact_type"), "Artifact Type Required",
			"Set `artifact_type` when the artifact is created without `content`; the registry can only detect the type from content.")
	}
	if resp.Diagnostics.HasError() {
		return
	}
	labels, diags := labelsToServer(ctx, plan.Labels)
	resp.Diagnostics.Append(diags...)
	in := client.ArtifactCreate{
		ArtifactID:   artifactID,
		ArtifactType: plan.ArtifactType.ValueString(),
		Name:         plan.Name.ValueString(),
		Description:  plan.Description.ValueString(),
		Labels:       labels,
	}
	if isSet(plan.Content) {
		content, d := r.contentPayload(ctx, plan)
		resp.Diagnostics.Append(d...)
		in.FirstVersion = &client.VersionCreate{Content: content}
	}
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.client.CreateArtifact(ctx, groupID, in, client.CreateArtifactOptions{IfExists: client.IfExistsFail})
	if err != nil {
		switch {
		case errors.Is(err, client.ErrAlreadyExists):
			resp.Diagnostics.AddError("Apicurio Artifact Already Exists",
				fmt.Sprintf("Artifact %q already exists. Import it instead:\n\n  terraform import apicurio_artifact.<name> %s", ref, ref))
		case client.IsNotFound(err):
			resp.Diagnostics.AddError("Apicurio Group Does Not Exist",
				fmt.Sprintf("Group %q does not exist and this registry does not create groups automatically "+
					"(apicurio.storage.enable-automatic-group-creation=false). Create it with an apicurio_group resource first.\n\n%s", groupID, err))
		case isUnknownTypeError(err):
			resp.Diagnostics.AddAttributeError(path.Root("artifact_type"), "Apicurio Registry Could Not Detect the Artifact Type",
				fmt.Sprintf("%s\n\nSet `artifact_type` explicitly. Detection needs content the registry can parse on its own: "+
					"content with `references` (named types defined in other artifacts) and content without a recognisable "+
					"`content_type` hint (JSON, YAML, XML, Protobuf or GraphQL) cannot be detected.", err))
		default:
			resp.Diagnostics.AddError("Unable to Create Apicurio Artifact", err.Error())
		}
		return
	}
	if isSet(plan.Owner) {
		if err := r.client.UpdateArtifact(ctx, groupID, artifactID, client.ArtifactUpdate{
			Name: in.Name, Description: in.Description, Labels: labels, Owner: plan.Owner.ValueString(),
		}); err != nil {
			resp.Diagnostics.AddError("Unable to Set the Owner of Apicurio Artifact", err.Error())
			return
		}
	}
	state, found, diags := r.read(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.Diagnostics.AddError("Unable to Read Apicurio Artifact After Creation", fmt.Sprintf("Artifact %q was created but could not be read back.", ref))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *artifactResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior artifactModel
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
		tflog.Warn(ctx, "artifact no longer exists, removing it from state", map[string]any{"id": prior.ID.ValueString()})
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *artifactResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state, cfg artifactModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	groupID, artifactID := plan.GroupID.ValueString(), plan.ArtifactID.ValueString()
	labels, diags := labelsToServer(ctx, plan.Labels)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if artifactMetadataChanged(plan, state) {
		update := client.ArtifactUpdate{Name: plan.Name.ValueString(), Description: plan.Description.ValueString(), Labels: labels}
		switch {
		case isSet(plan.Owner):
			update.Owner = plan.Owner.ValueString()
		case isSet(state.Owner):
			update.Owner = state.Owner.ValueString()
		}
		if err := r.client.UpdateArtifact(ctx, groupID, artifactID, update); err != nil {
			if client.IsNotFound(err) {
				resp.Diagnostics.AddError("Apicurio Artifact Disappeared", fmt.Sprintf("Artifact %q no longer exists; run terraform apply again to recreate it.", plan.ID.ValueString()))
				resp.State.RemoveResource(ctx)
				return
			}
			resp.Diagnostics.AddError("Unable to Update Apicurio Artifact", err.Error())
			return
		}
	}
	newVersion, diags := r.needsNewVersion(ctx, plan, state, cfg)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if newVersion {
		content, d := r.contentPayload(ctx, plan)
		resp.Diagnostics.Append(d...)
		if resp.Diagnostics.HasError() {
			return
		}
		if _, err := r.client.CreateVersion(ctx, groupID, artifactID, client.VersionCreate{Content: content}, false); err != nil {
			var rv *client.RuleViolationError
			if errors.As(err, &rv) {
				resp.Diagnostics.AddAttributeError(path.Root("content"), "Apicurio Registry Refused the New Version", err.Error())
				return
			}
			if client.IsNotFound(err) {
				resp.Diagnostics.AddError("Apicurio Artifact Disappeared", fmt.Sprintf("Artifact %q no longer exists; run terraform apply again to recreate it.", plan.ID.ValueString()))
				resp.State.RemoveResource(ctx)
				return
			}
			resp.Diagnostics.AddError("Unable to Create a New Version of Apicurio Artifact", err.Error())
			return
		}
	}
	newState, found, diags := r.read(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.Diagnostics.AddError("Unable to Read Apicurio Artifact After Update", fmt.Sprintf("Artifact %q could not be read back.", plan.ID.ValueString()))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *artifactResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state artifactModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteArtifact(ctx, state.GroupID.ValueString(), state.ArtifactID.ValueString()); err != nil {
		if client.IsNotFound(err) {
			return
		}
		if detail, ok := deletionDisabledDetail(err); ok {
			resp.Diagnostics.AddError("Artifact Deletion Is Disabled on the Apicurio Registry", detail)
			return
		}
		resp.Diagnostics.AddError("Unable to Delete Apicurio Artifact", err.Error())
	}
}

func (r *artifactResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	groupID, artifactID, ok := splitGroupArtifact(req.ID)
	if !ok {
		resp.Diagnostics.AddError("Invalid Import ID", fmt.Sprintf("Expected `group_id/artifact_id`, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("group_id"), groupID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("artifact_id"), artifactID)...)
}

// checkArtifactType validates a configured type against the registry, which
// otherwise answers 500 (3.3) or 400 (earlier) for an unknown one.
func (r *artifactResource) checkArtifactType(ctx context.Context, artifactType string) diagsT {
	var diags diagsT
	known, err := r.client.ListArtifactTypes(ctx)
	if err != nil {
		diags.AddError("Unable to List the Artifact Types of the Apicurio Registry", err.Error())
		return diags
	}
	if !slices.Contains(known, artifactType) {
		diags.AddAttributeError(path.Root("artifact_type"), "Unknown Artifact Type",
			fmt.Sprintf("This registry does not know the artifact type %q. It supports: %s.", artifactType, strings.Join(known, ", ")))
	}
	return diags
}

// contentPayload builds the content of a version from the plan, with the
// default content type when none is configured.
func (r *artifactResource) contentPayload(ctx context.Context, plan artifactModel) (client.Content, diagsT) {
	var diags diagsT
	ct := plan.ContentType.ValueString()
	if !isSet(plan.ContentType) {
		ct = defaultContentType(plan.Content.ValueString(), plan.ArtifactType.ValueString())
	}
	refs, d := referencesToServer(ctx, plan.References)
	diags.Append(d...)
	return client.Content{Content: plan.Content.ValueString(), ContentType: ct, References: refs}, diags
}

// read fetches the artifact and its latest version and maps them to state,
// keeping the null/empty choices of prior.
func (r *artifactResource) read(ctx context.Context, prior artifactModel) (artifactModel, bool, diagsT) {
	var diags diagsT
	groupID, artifactID := prior.GroupID.ValueString(), prior.ArtifactID.ValueString()
	a, err := r.client.GetArtifact(ctx, groupID, artifactID)
	if err != nil {
		if client.IsNotFound(err) {
			return artifactModel{}, false, diags
		}
		diags.AddError("Unable to Read Apicurio Artifact", err.Error())
		return artifactModel{}, false, diags
	}
	labels, d := labelsFromServer(ctx, prior.Labels, a.Labels)
	diags.Append(d...)
	state := artifactModel{
		ID:           types.StringValue(groupID + "/" + artifactID),
		GroupID:      types.StringValue(groupID),
		ArtifactID:   types.StringValue(artifactID),
		ArtifactType: types.StringValue(a.ArtifactType),
		Name:         stringFromServer(prior.Name, a.Name),
		Description:  stringFromServer(prior.Description, a.Description),
		Labels:       labels,
		Owner:        types.StringValue(a.Owner),
		CreatedOn:    types.StringValue(a.CreatedOn),
		ModifiedOn:   types.StringValue(a.ModifiedOn),
		ModifiedBy:   types.StringValue(a.ModifiedBy),
		Content:      contentNull(),
		ContentType:  types.StringNull(),
		References:   types.ListNull(referenceObjectType),
		Version:      types.StringNull(),
		GlobalID:     types.Int64Null(),
		ContentID:    types.Int64Null(),
	}
	v, err := r.client.GetVersion(ctx, groupID, artifactID, client.BranchLatest)
	if err != nil {
		if client.IsNotFound(err) {
			return state, true, diags // an artifact without versions
		}
		diags.AddError("Unable to Read the Latest Version of Apicurio Artifact", err.Error())
		return artifactModel{}, false, diags
	}
	vc, err := r.client.GetVersionContent(ctx, groupID, artifactID, client.BranchLatest, client.ReferencesPreserve)
	if err != nil {
		diags.AddError("Unable to Read the Content of Apicurio Artifact", err.Error())
		return artifactModel{}, false, diags
	}
	refs, err := r.client.GetVersionReferences(ctx, groupID, artifactID, client.BranchLatest)
	if err != nil {
		diags.AddError("Unable to Read the References of Apicurio Artifact", err.Error())
		return artifactModel{}, false, diags
	}
	refList, d := referencesFromServer(ctx, prior.References, refs)
	diags.Append(d...)
	// The registry stores content by hash and reports the media type of the
	// first upload of those bytes, so a configured content_type is kept as the
	// upload hint it is; the same set of references keeps its configured order.
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
	state.Version = types.StringValue(v.Version)
	state.GlobalID = types.Int64Value(v.GlobalID)
	state.ContentID = types.Int64Value(v.ContentID)
	return state, true, diags
}

// artifactMetadataChanged reports whether the plan changes anything the
// registry stores in the artifact metadata (name, description, labels, owner).
func artifactMetadataChanged(plan, state artifactModel) bool {
	return !plan.Name.Equal(state.Name) ||
		!plan.Description.Equal(state.Description) ||
		!plan.Labels.Equal(state.Labels) ||
		(isSet(plan.Owner) && !plan.Owner.Equal(state.Owner))
}
