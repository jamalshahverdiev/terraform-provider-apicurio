package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/jamalshahverdiev/terraform-provider-apicurio/internal/client"
)

var (
	_ datasource.DataSource              = &artifactTypesDataSource{}
	_ datasource.DataSourceWithConfigure = &artifactTypesDataSource{}
)

// NewArtifactTypesDataSource returns the apicurio_artifact_types data source.
func NewArtifactTypesDataSource() datasource.DataSource { return &artifactTypesDataSource{} }

type artifactTypesDataSource struct {
	client *client.Client
}

type artifactTypesModel struct {
	ID    types.String `tfsdk:"id"`
	Types types.List   `tfsdk:"types"`
}

func (d *artifactTypesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_artifact_types"
}

func (d *artifactTypesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The artifact types this registry supports (`GET /admin/config/artifactTypes`): the built-in ones " +
			"(`AVRO`, `PROTOBUF`, `JSON`, `OPENAPI`, `ASYNCAPI`, `GRAPHQL`, `KCONNECT`, `WSDL`, `XSD`, `XML`), the ones newer " +
			"versions add and any custom type the instance registers. `apicurio_artifact.artifact_type` is validated against this list.",
		Attributes: map[string]schema.Attribute{
			"id":    schema.StringAttribute{Computed: true, MarkdownDescription: "The registry URL."},
			"types": schema.ListAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Type names, in the registry's order."},
		},
	}
}

func (d *artifactTypesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

func (d *artifactTypesDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	names, err := d.client.ListArtifactTypes(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to List the Artifact Types of the Apicurio Registry", err.Error())
		return
	}
	list, diags := types.ListValueFrom(ctx, types.StringType, names)
	resp.Diagnostics.Append(diags...)
	state := artifactTypesModel{ID: types.StringValue(d.client.ServerURL()), Types: list}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
