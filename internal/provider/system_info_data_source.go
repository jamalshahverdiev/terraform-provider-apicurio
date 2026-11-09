package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/jamalshahverdiev/terraform-provider-apicurio/internal/client"
)

var (
	_ datasource.DataSource              = &systemInfoDataSource{}
	_ datasource.DataSourceWithConfigure = &systemInfoDataSource{}
)

// NewSystemInfoDataSource returns the apicurio_system_info data source.
func NewSystemInfoDataSource() datasource.DataSource {
	return &systemInfoDataSource{}
}

type systemInfoDataSource struct {
	client *client.Client
}

type systemInfoModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Version     types.String `tfsdk:"version"`
	BuiltOn     types.String `tfsdk:"built_on"`
}

func (d *systemInfoDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_system_info"
}

func (d *systemInfoDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Information about the Apicurio Registry the provider is connected to (`GET /system/info`). " +
			"Useful as a smoke test of the connection and to gate configuration on the server version.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The registry URL.",
			},
			"name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Server name, for example `Apicurio Registry (SQL)`.",
			},
			"description": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Server description.",
			},
			"version": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Server version, for example `3.3.4`.",
			},
			"built_on": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Build timestamp of the server (RFC 3339).",
			},
		},
	}
}

func (d *systemInfoDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

func (d *systemInfoDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.client == nil {
		resp.Diagnostics.AddError("Provider Not Configured", "The apicurio provider was not configured before this data source was read. This is a bug in the provider.")
		return
	}
	info, err := d.client.GetSystemInfo(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read the Apicurio Registry System Information", err.Error())
		return
	}
	tflog.Debug(ctx, "read system info", map[string]any{"version": info.Version})

	state := systemInfoModel{
		ID:          types.StringValue(d.client.ServerURL()),
		Name:        types.StringValue(info.Name),
		Description: types.StringValue(info.Description),
		Version:     types.StringValue(info.Version),
		BuiltOn:     types.StringValue(info.BuiltOn),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
