package provider

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/jamalshahverdiev/terraform-provider-apicurio/internal/client"
)

var (
	_ datasource.DataSource              = &artifactsDataSource{}
	_ datasource.DataSourceWithConfigure = &artifactsDataSource{}
)

// NewArtifactsDataSource returns the apicurio_artifacts data source.
func NewArtifactsDataSource() datasource.DataSource { return &artifactsDataSource{} }

type artifactsDataSource struct {
	client *client.Client
}

type artifactsDataSourceModel struct {
	ID           types.String `tfsdk:"id"`
	GroupID      types.String `tfsdk:"group_id"`
	ArtifactID   types.String `tfsdk:"artifact_id"`
	Name         types.String `tfsdk:"name"`
	Description  types.String `tfsdk:"description"`
	ArtifactType types.String `tfsdk:"artifact_type"`
	Labels       types.Map    `tfsdk:"labels"`
	OrderBy      types.String `tfsdk:"order_by"`
	Order        types.String `tfsdk:"order"`
	Artifacts    types.List   `tfsdk:"artifacts"`
}

// sortKeys maps the attribute values of order_by to the server's keys.
var sortKeys = map[string]string{
	"name":          client.SortByName,
	"created_on":    client.SortByCreatedOn,
	"modified_on":   client.SortByModifiedOn,
	"group_id":      client.SortByGroupID,
	"artifact_id":   client.SortByArtifactID,
	"artifact_type": client.SortByArtifactType,
}

var searchResultAttrTypes = map[string]attr.Type{
	"group_id":      types.StringType,
	"artifact_id":   types.StringType,
	"name":          types.StringType,
	"description":   types.StringType,
	"artifact_type": types.StringType,
	"owner":         types.StringType,
	"labels":        types.MapType{ElemType: types.StringType},
	"created_on":    types.StringType,
	"modified_on":   types.StringType,
	"modified_by":   types.StringType,
}

var searchResultObjectType = types.ObjectType{AttrTypes: searchResultAttrTypes}

func (d *artifactsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_artifacts"
}

func (d *artifactsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	keys := make([]string, 0, len(sortKeys))
	for k := range sortKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	resp.Schema = schema.Schema{
		MarkdownDescription: "Searches artifacts across the registry (`GET /search/artifacts`). Every filter is evaluated by the " +
			"server and all filters must match: `group_id`, `artifact_id` and `artifact_type` match exactly; `description` is a " +
			"substring match; `name` is an exact, case-sensitive match from Apicurio Registry 3.1 (a case-sensitive substring " +
			"match on 3.0); each label is required, with an empty value meaning \"any value\". All pages are fetched.",
		Attributes: map[string]schema.Attribute{
			"id":            schema.StringAttribute{Computed: true, MarkdownDescription: "The search URL."},
			"group_id":      schema.StringAttribute{Optional: true, MarkdownDescription: "Only artifacts of this group."},
			"artifact_id":   schema.StringAttribute{Optional: true, MarkdownDescription: "Only the artifact with this id (exact)."},
			"name":          schema.StringAttribute{Optional: true, MarkdownDescription: "Name filter (exact and case-sensitive from 3.1, substring on 3.0)."},
			"description":   schema.StringAttribute{Optional: true, MarkdownDescription: "Substring of the description."},
			"artifact_type": schema.StringAttribute{Optional: true, MarkdownDescription: "Artifact type, for example `AVRO`."},
			"labels": schema.MapAttribute{
				Optional:    true,
				ElementType: types.StringType,
				MarkdownDescription: "Labels the artifacts must carry (all of them). An empty value matches any value of the key. " +
					"Keys and values of this filter cannot contain `:`, because the registry splits each `key:value` on a colon " +
					"(3.0 to 3.2 on the first one, 3.3 on the last one).",
			},
			"order_by": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Sort key: " + backquoted(keys) + ". Default: the server's order (by name).",
				Validators:          []validator.String{stringvalidator.OneOf(keys...)},
			},
			"order": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "`asc` or `desc`.",
				Validators:          []validator.String{stringvalidator.OneOf("asc", "desc")},
			},
			"artifacts": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Matching artifacts, in the server's order.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"group_id":      schema.StringAttribute{Computed: true},
						"artifact_id":   schema.StringAttribute{Computed: true},
						"name":          schema.StringAttribute{Computed: true},
						"description":   schema.StringAttribute{Computed: true},
						"artifact_type": schema.StringAttribute{Computed: true},
						"owner":         schema.StringAttribute{Computed: true},
						"labels":        schema.MapAttribute{Computed: true, ElementType: types.StringType},
						"created_on":    schema.StringAttribute{Computed: true},
						"modified_on":   schema.StringAttribute{Computed: true},
						"modified_by":   schema.StringAttribute{Computed: true},
					},
				},
			},
		},
	}
}

func (d *artifactsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

func (d *artifactsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg artifactsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	search := client.ArtifactSearch{
		GroupID:      cfg.GroupID.ValueString(),
		ArtifactID:   cfg.ArtifactID.ValueString(),
		Name:         cfg.Name.ValueString(),
		Description:  cfg.Description.ValueString(),
		ArtifactType: cfg.ArtifactType.ValueString(),
		OrderBy:      sortKeys[cfg.OrderBy.ValueString()],
		Order:        cfg.Order.ValueString(),
	}
	if !cfg.Labels.IsNull() && !cfg.Labels.IsUnknown() {
		labels, diags := labelsToServer(ctx, cfg.Labels)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		keys := make([]string, 0, len(labels))
		for k := range labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := labels[k]
			if strings.Contains(k, ":") || strings.Contains(v, ":") {
				resp.Diagnostics.AddAttributeError(path.Root("labels").AtMapKey(k), "Invalid Label Filter",
					fmt.Sprintf("Label %q: the keys and values of the labels filter cannot contain ':'. The registry splits each "+
						"filter on a colon (3.3 on the last one, 3.0 to 3.2 on the first), so such a filter never matches.", k))
				return
			}
			if v == "" {
				search.Labels = append(search.Labels, k)
			} else {
				search.Labels = append(search.Labels, k+":"+v)
			}
		}
	}
	found, err := d.client.SearchArtifacts(ctx, search)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Search Apicurio Artifacts", err.Error())
		return
	}
	items := make([]attr.Value, 0, len(found))
	for _, a := range found {
		labels, diags := labelsFromServer(ctx, types.MapNull(types.StringType), a.Labels)
		resp.Diagnostics.Append(diags...)
		if labels.IsNull() {
			labels = types.MapValueMust(types.StringType, map[string]attr.Value{})
		}
		items = append(items, types.ObjectValueMust(searchResultAttrTypes, map[string]attr.Value{
			"group_id":      types.StringValue(a.GroupID),
			"artifact_id":   types.StringValue(a.ArtifactID),
			"name":          types.StringValue(a.Name),
			"description":   types.StringValue(a.Description),
			"artifact_type": types.StringValue(a.ArtifactType),
			"owner":         types.StringValue(a.Owner),
			"labels":        labels,
			"created_on":    types.StringValue(a.CreatedOn),
			"modified_on":   types.StringValue(a.ModifiedOn),
			"modified_by":   types.StringValue(a.ModifiedBy),
		}))
	}
	q := url.Values{}
	for _, kv := range []struct{ k, v string }{
		{"groupId", search.GroupID}, {"artifactId", search.ArtifactID}, {"name", search.Name}, {"description", search.Description},
		{"artifactType", search.ArtifactType}, {"orderby", search.OrderBy}, {"order", search.Order},
	} {
		if kv.v != "" {
			q.Set(kv.k, kv.v)
		}
	}
	for _, l := range search.Labels {
		q.Add("labels", l)
	}
	cfg.ID = types.StringValue(d.client.BaseURL() + "/search/artifacts?" + q.Encode())
	cfg.Artifacts = types.ListValueMust(searchResultObjectType, items)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
