package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/jamalshahverdiev/terraform-provider-apicurio/internal/client"
)

var (
	_ datasource.DataSource              = &currentUserDataSource{}
	_ datasource.DataSourceWithConfigure = &currentUserDataSource{}
)

// NewCurrentUserDataSource returns the apicurio_current_user data source.
func NewCurrentUserDataSource() datasource.DataSource { return &currentUserDataSource{} }

type currentUserDataSource struct {
	client *client.Client
}

type currentUserModel struct {
	ID          types.String `tfsdk:"id"`
	Username    types.String `tfsdk:"username"`
	DisplayName types.String `tfsdk:"display_name"`
	Admin       types.Bool   `tfsdk:"admin"`
	Developer   types.Bool   `tfsdk:"developer"`
	Viewer      types.Bool   `tfsdk:"viewer"`
}

func (d *currentUserDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_current_user"
}

func (d *currentUserDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "How the registry sees the provider's credentials: the principal and its roles. On a registry without " +
			"authorization the anonymous caller is an admin with an empty `username`. On a secured registry an empty `username` " +
			"means the credentials were not accepted and the registry fell back to the anonymous user; all three role flags " +
			"`false` means the principal has no role mapping yet.",
		Attributes: map[string]schema.Attribute{
			"id":           schema.StringAttribute{Computed: true, MarkdownDescription: "The registry URL."},
			"username":     schema.StringAttribute{Computed: true, MarkdownDescription: "Principal id (the `preferred_username` claim or the service account name); empty for the anonymous user."},
			"display_name": schema.StringAttribute{Computed: true, MarkdownDescription: "Display name of the principal."},
			"admin":        schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the principal has the admin role."},
			"developer":    schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the principal has the developer role."},
			"viewer":       schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the principal has the read-only role."},
		},
	}
}

func (d *currentUserDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

func (d *currentUserDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	me, err := d.client.GetCurrentUser(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read the Current Apicurio Registry User", err.Error())
		return
	}
	state := currentUserModel{
		ID:          types.StringValue(d.client.ServerURL()),
		Username:    types.StringValue(me.Username),
		DisplayName: types.StringValue(me.DisplayName),
		Admin:       types.BoolValue(me.Admin),
		Developer:   types.BoolValue(me.Developer),
		Viewer:      types.BoolValue(me.Viewer),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
