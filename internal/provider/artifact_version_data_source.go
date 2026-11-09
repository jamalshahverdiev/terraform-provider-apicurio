package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/jamalshahverdiev/terraform-provider-apicurio/internal/client"
)

var (
	_ datasource.DataSource              = &artifactVersionDataSource{}
	_ datasource.DataSourceWithConfigure = &artifactVersionDataSource{}
)

// NewArtifactVersionDataSource returns the apicurio_artifact_version data source.
func NewArtifactVersionDataSource() datasource.DataSource { return &artifactVersionDataSource{} }

type artifactVersionDataSource struct {
	client *client.Client
}

type artifactVersionDataSourceModel struct {
	ID                 types.String `tfsdk:"id"`
	GroupID            types.String `tfsdk:"group_id"`
	ArtifactID         types.String `tfsdk:"artifact_id"`
	Version            types.String `tfsdk:"version"`
	ReferencesHandling types.String `tfsdk:"references_handling"`
	ResolvedVersion    types.String `tfsdk:"resolved_version"`
	State              types.String `tfsdk:"state"`
	Name               types.String `tfsdk:"name"`
	Description        types.String `tfsdk:"description"`
	Labels             types.Map    `tfsdk:"labels"`
	GlobalID           types.Int64  `tfsdk:"global_id"`
	ContentID          types.Int64  `tfsdk:"content_id"`
	ArtifactType       types.String `tfsdk:"artifact_type"`
	Owner              types.String `tfsdk:"owner"`
	Content            types.String `tfsdk:"content"`
	ContentType        types.String `tfsdk:"content_type"`
	References         types.List   `tfsdk:"references"`
	CreatedOn          types.String `tfsdk:"created_on"`
	ModifiedOn         types.String `tfsdk:"modified_on"`
	ModifiedBy         types.String `tfsdk:"modified_by"`
}

func (d *artifactVersionDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_artifact_version"
}

func (d *artifactVersionDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up one version of an artifact by id or by version expression. Fails when it does not exist. " +
			"The content of a `DISABLED` version is not served by the registry and is null.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, MarkdownDescription: "`group_id/artifact_id/<resolved version>`."},
			"group_id":    schema.StringAttribute{Required: true, MarkdownDescription: "Group of the artifact."},
			"artifact_id": schema.StringAttribute{Required: true, MarkdownDescription: "Artifact id."},
			"version": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "A version id, or `branch=<branch_id>` for the tip of a branch. Defaults to `branch=latest`, the newest " +
					"version that is neither a draft nor disabled.",
			},
			"references_handling": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "How references are rendered in `content`: `PRESERVE` (default, as stored), `DEREFERENCE` (referenced " +
					"content inlined) or `REWRITE` (reference URLs rewritten to the registry).",
				Validators: []validator.String{stringvalidator.OneOf(client.ReferencesPreserve, client.ReferencesDereference, client.ReferencesRewrite)},
			},
			"resolved_version": schema.StringAttribute{Computed: true, MarkdownDescription: "The version id the lookup resolved to."},
			"state":            schema.StringAttribute{Computed: true, MarkdownDescription: "State of the version."},
			"name":             schema.StringAttribute{Computed: true, MarkdownDescription: "Name."},
			"description":      schema.StringAttribute{Computed: true, MarkdownDescription: "Description."},
			"labels":           schema.MapAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Labels."},
			"global_id":        schema.Int64Attribute{Computed: true, MarkdownDescription: "Global id."},
			"content_id":       schema.Int64Attribute{Computed: true, MarkdownDescription: "Content id."},
			"artifact_type":    schema.StringAttribute{Computed: true, MarkdownDescription: "Type of the artifact."},
			"owner":            schema.StringAttribute{Computed: true, MarkdownDescription: "Owner."},
			"content":          schema.StringAttribute{Computed: true, MarkdownDescription: "Content, rendered per `references_handling`."},
			"content_type":     schema.StringAttribute{Computed: true, MarkdownDescription: "Media type of the content."},
			"references": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "References of the version.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"group_id":    schema.StringAttribute{Computed: true},
						"artifact_id": schema.StringAttribute{Computed: true},
						"version":     schema.StringAttribute{Computed: true},
						"name":        schema.StringAttribute{Computed: true},
					},
				},
			},
			"created_on":  schema.StringAttribute{Computed: true, MarkdownDescription: "Creation time (RFC 3339)."},
			"modified_on": schema.StringAttribute{Computed: true, MarkdownDescription: "Last modification time (RFC 3339)."},
			"modified_by": schema.StringAttribute{Computed: true, MarkdownDescription: "User of the last modification."},
		},
	}
}

func (d *artifactVersionDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

func (d *artifactVersionDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg artifactVersionDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	groupID, artifactID := cfg.GroupID.ValueString(), cfg.ArtifactID.ValueString()
	expr := client.BranchLatest
	if isSet(cfg.Version) {
		expr = cfg.Version.ValueString()
	}
	handling := client.ReferencesPreserve
	if isSet(cfg.ReferencesHandling) {
		handling = cfg.ReferencesHandling.ValueString()
	}
	v, err := d.client.GetVersion(ctx, groupID, artifactID, expr)
	if err != nil {
		if client.IsNotFound(err) {
			resp.Diagnostics.AddError("Apicurio Artifact Version Not Found",
				fmt.Sprintf("No version %q of artifact %q exists in the registry at %s.", expr, groupID+"/"+artifactID, d.client.ServerURL()))
			return
		}
		resp.Diagnostics.AddError("Unable to Read Apicurio Artifact Version", err.Error())
		return
	}
	labels, diags := labelsFromServer(ctx, types.MapNull(types.StringType), v.Labels)
	resp.Diagnostics.Append(diags...)
	if labels.IsNull() {
		labels = types.MapValueMust(types.StringType, map[string]attrValue{})
	}
	state := artifactVersionDataSourceModel{
		ID:                 types.StringValue(groupID + "/" + artifactID + "/" + v.Version),
		GroupID:            cfg.GroupID,
		ArtifactID:         cfg.ArtifactID,
		Version:            cfg.Version,
		ReferencesHandling: cfg.ReferencesHandling,
		ResolvedVersion:    types.StringValue(v.Version),
		State:              types.StringValue(v.State),
		Name:               types.StringValue(v.Name),
		Description:        types.StringValue(v.Description),
		Labels:             labels,
		GlobalID:           types.Int64Value(v.GlobalID),
		ContentID:          types.Int64Value(v.ContentID),
		ArtifactType:       types.StringValue(v.ArtifactType),
		Owner:              types.StringValue(v.Owner),
		CreatedOn:          types.StringValue(v.CreatedOn),
		ModifiedOn:         types.StringValue(v.ModifiedOn),
		ModifiedBy:         types.StringValue(v.ModifiedBy),
		Content:            types.StringNull(),
		ContentType:        types.StringNull(),
		References:         types.ListValueMust(referenceObjectType, []attrValue{}),
	}
	if v.State != client.StateDisabled {
		vc, err := d.client.GetVersionContent(ctx, groupID, artifactID, v.Version, handling)
		if err != nil {
			resp.Diagnostics.AddError("Unable to Read the Content of Apicurio Artifact Version", err.Error())
			return
		}
		refs, err := d.client.GetVersionReferences(ctx, groupID, artifactID, v.Version)
		if err != nil {
			resp.Diagnostics.AddError("Unable to Read the References of Apicurio Artifact Version", err.Error())
			return
		}
		refList, diags := referencesFromServer(ctx, state.References, refs)
		resp.Diagnostics.Append(diags...)
		state.Content = types.StringValue(string(vc.Content))
		state.ContentType = types.StringValue(mediaType(vc.ContentType))
		state.References = refList
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
