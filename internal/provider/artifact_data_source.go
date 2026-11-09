package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/jamalshahverdiev/terraform-provider-apicurio/internal/client"
)

var (
	_ datasource.DataSource              = &artifactDataSource{}
	_ datasource.DataSourceWithConfigure = &artifactDataSource{}
)

// NewArtifactDataSource returns the apicurio_artifact data source.
func NewArtifactDataSource() datasource.DataSource { return &artifactDataSource{} }

type artifactDataSource struct {
	client *client.Client
}

type artifactDataSourceModel struct {
	ID           types.String `tfsdk:"id"`
	GroupID      types.String `tfsdk:"group_id"`
	ArtifactID   types.String `tfsdk:"artifact_id"`
	ArtifactType types.String `tfsdk:"artifact_type"`
	Name         types.String `tfsdk:"name"`
	Description  types.String `tfsdk:"description"`
	Labels       types.Map    `tfsdk:"labels"`
	Owner        types.String `tfsdk:"owner"`
	Version      types.String `tfsdk:"version"`
	GlobalID     types.Int64  `tfsdk:"global_id"`
	ContentID    types.Int64  `tfsdk:"content_id"`
	State        types.String `tfsdk:"state"`
	Content      types.String `tfsdk:"content"`
	ContentType  types.String `tfsdk:"content_type"`
	References   types.List   `tfsdk:"references"`
	CreatedOn    types.String `tfsdk:"created_on"`
	ModifiedOn   types.String `tfsdk:"modified_on"`
	ModifiedBy   types.String `tfsdk:"modified_by"`
}

func (d *artifactDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_artifact"
}

func (d *artifactDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up an artifact and its latest version. Fails when the artifact does not exist; " +
			"the version attributes are null for an artifact without versions.",
		Attributes: map[string]schema.Attribute{
			"id":            schema.StringAttribute{Computed: true, MarkdownDescription: "`group_id/artifact_id`."},
			"group_id":      schema.StringAttribute{Required: true, MarkdownDescription: "Group of the artifact."},
			"artifact_id":   schema.StringAttribute{Required: true, MarkdownDescription: "Identifier of the artifact."},
			"artifact_type": schema.StringAttribute{Computed: true, MarkdownDescription: "Type of the artifact."},
			"name":          schema.StringAttribute{Computed: true, MarkdownDescription: "Name."},
			"description":   schema.StringAttribute{Computed: true, MarkdownDescription: "Description."},
			"labels":        schema.MapAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Labels."},
			"owner":         schema.StringAttribute{Computed: true, MarkdownDescription: "Owner."},
			"version":       schema.StringAttribute{Computed: true, MarkdownDescription: "Version id of the latest version."},
			"global_id":     schema.Int64Attribute{Computed: true, MarkdownDescription: "Global id of the latest version."},
			"content_id":    schema.Int64Attribute{Computed: true, MarkdownDescription: "Content id of the latest version."},
			"state":         schema.StringAttribute{Computed: true, MarkdownDescription: "State of the latest version (`ENABLED`, `DISABLED`, `DEPRECATED`, `DRAFT`, `SUNSET`)."},
			"content":       schema.StringAttribute{Computed: true, MarkdownDescription: "Content of the latest version, as stored."},
			"content_type":  schema.StringAttribute{Computed: true, MarkdownDescription: "Media type of the content."},
			"references": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "References of the latest version.",
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

func (d *artifactDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

func (d *artifactDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg artifactDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	groupID, artifactID := cfg.GroupID.ValueString(), cfg.ArtifactID.ValueString()
	a, err := d.client.GetArtifact(ctx, groupID, artifactID)
	if err != nil {
		if client.IsNotFound(err) {
			resp.Diagnostics.AddError("Apicurio Artifact Not Found", fmt.Sprintf("No artifact %q exists in group %q of the registry at %s.", artifactID, groupID, d.client.ServerURL()))
			return
		}
		resp.Diagnostics.AddError("Unable to Read Apicurio Artifact", err.Error())
		return
	}
	labels, diags := labelsFromServer(ctx, types.MapNull(types.StringType), a.Labels)
	resp.Diagnostics.Append(diags...)
	if labels.IsNull() {
		labels = types.MapValueMust(types.StringType, map[string]attrValue{})
	}
	state := artifactDataSourceModel{
		ID:           types.StringValue(groupID + "/" + artifactID),
		GroupID:      types.StringValue(groupID),
		ArtifactID:   types.StringValue(artifactID),
		ArtifactType: types.StringValue(a.ArtifactType),
		Name:         types.StringValue(a.Name),
		Description:  types.StringValue(a.Description),
		Labels:       labels,
		Owner:        types.StringValue(a.Owner),
		CreatedOn:    types.StringValue(a.CreatedOn),
		ModifiedOn:   types.StringValue(a.ModifiedOn),
		ModifiedBy:   types.StringValue(a.ModifiedBy),
		Version:      types.StringNull(),
		GlobalID:     types.Int64Null(),
		ContentID:    types.Int64Null(),
		State:        types.StringNull(),
		Content:      types.StringNull(),
		ContentType:  types.StringNull(),
		References:   types.ListValueMust(referenceObjectType, []attrValue{}),
	}
	v, err := d.client.GetVersion(ctx, groupID, artifactID, client.BranchLatest)
	switch {
	case err == nil:
		vc, err := d.client.GetVersionContent(ctx, groupID, artifactID, client.BranchLatest, client.ReferencesPreserve)
		if err != nil {
			resp.Diagnostics.AddError("Unable to Read the Content of Apicurio Artifact", err.Error())
			return
		}
		refs, err := d.client.GetVersionReferences(ctx, groupID, artifactID, client.BranchLatest)
		if err != nil {
			resp.Diagnostics.AddError("Unable to Read the References of Apicurio Artifact", err.Error())
			return
		}
		refList, diags := referencesFromServer(ctx, state.References, refs)
		resp.Diagnostics.Append(diags...)
		state.Version = types.StringValue(v.Version)
		state.GlobalID = types.Int64Value(v.GlobalID)
		state.ContentID = types.Int64Value(v.ContentID)
		state.State = types.StringValue(v.State)
		state.Content = types.StringValue(string(vc.Content))
		state.ContentType = types.StringValue(mediaType(vc.ContentType))
		state.References = refList
	case client.IsNotFound(err):
		// an artifact without versions
	default:
		resp.Diagnostics.AddError("Unable to Read the Latest Version of Apicurio Artifact", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
