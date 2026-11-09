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
	_ datasource.DataSource              = &groupDataSource{}
	_ datasource.DataSourceWithConfigure = &groupDataSource{}
)

// NewGroupDataSource returns the apicurio_group data source.
func NewGroupDataSource() datasource.DataSource { return &groupDataSource{} }

type groupDataSource struct {
	client *client.Client
}

type groupDataSourceModel struct {
	ID          types.String `tfsdk:"id"`
	GroupID     types.String `tfsdk:"group_id"`
	Description types.String `tfsdk:"description"`
	Labels      types.Map    `tfsdk:"labels"`
	Owner       types.String `tfsdk:"owner"`
	CreatedOn   types.String `tfsdk:"created_on"`
	ModifiedOn  types.String `tfsdk:"modified_on"`
	ModifiedBy  types.String `tfsdk:"modified_by"`
}

func (d *groupDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group"
}

func (d *groupDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a group by id. Fails when the group does not exist.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, MarkdownDescription: "Same as `group_id`."},
			"group_id":    schema.StringAttribute{Required: true, MarkdownDescription: "Identifier of the group."},
			"description": schema.StringAttribute{Computed: true, MarkdownDescription: "Description of the group."},
			"labels":      schema.MapAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Labels of the group."},
			"owner":       schema.StringAttribute{Computed: true, MarkdownDescription: "Owner of the group."},
			"created_on":  schema.StringAttribute{Computed: true, MarkdownDescription: "Creation time (RFC 3339)."},
			"modified_on": schema.StringAttribute{Computed: true, MarkdownDescription: "Last modification time (RFC 3339)."},
			"modified_by": schema.StringAttribute{Computed: true, MarkdownDescription: "User of the last modification."},
		},
	}
}

func (d *groupDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

func (d *groupDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg groupDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	groupID := cfg.GroupID.ValueString()
	g, err := d.client.GetGroup(ctx, groupID)
	if err != nil {
		if client.IsNotFound(err) {
			resp.Diagnostics.AddError("Apicurio Group Not Found", fmt.Sprintf("No group with id %q exists in the registry at %s.", groupID, d.client.ServerURL()))
			return
		}
		resp.Diagnostics.AddError("Unable to Read Apicurio Group", err.Error())
		return
	}
	labels, diags := labelsFromServer(ctx, types.MapNull(types.StringType), g.Labels)
	resp.Diagnostics.Append(diags...)
	if labels.IsNull() {
		labels = types.MapValueMust(types.StringType, map[string]attrValue{})
	}
	state := groupDataSourceModel{
		ID:          types.StringValue(g.GroupID),
		GroupID:     types.StringValue(g.GroupID),
		Description: types.StringValue(g.Description),
		Labels:      labels,
		Owner:       types.StringValue(g.Owner),
		CreatedOn:   types.StringValue(g.CreatedOn),
		ModifiedOn:  types.StringValue(g.ModifiedOn),
		ModifiedBy:  types.StringValue(g.ModifiedBy),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
