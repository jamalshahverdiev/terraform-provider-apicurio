// Package provider implements the Terraform provider for Apicurio Registry 3.x.
package provider

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/hashicorp/terraform-plugin-framework-validators/providervalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/jamalshahverdiev/terraform-provider-apicurio/internal/client"
)

// Environment variables read when the matching attribute is unset or empty.
const (
	envURL      = "APICURIO_URL"
	envToken    = "APICURIO_TOKEN"
	envUsername = "APICURIO_USERNAME"
	envPassword = "APICURIO_PASSWORD"
)

var (
	_ provider.Provider                     = &apicurioProvider{}
	_ provider.ProviderWithConfigValidators = &apicurioProvider{}
)

type apicurioProvider struct {
	version string
}

// New returns the provider constructor used by main and by the tests.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &apicurioProvider{version: version}
	}
}

type providerModel struct {
	URL                types.String `tfsdk:"url"`
	Token              types.String `tfsdk:"token"`
	Username           types.String `tfsdk:"username"`
	Password           types.String `tfsdk:"password"`
	ValidateConnection types.Bool   `tfsdk:"validate_connection"`
}

func (p *apicurioProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "apicurio"
	resp.Version = p.version
}

func (p *apicurioProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manage the content and the administration of an Apicurio Registry 3.x instance " +
			"(groups, artifacts, versions, branches, rules, role mappings and configuration properties) " +
			"through its Core Registry API v3.",
		Attributes: map[string]schema.Attribute{
			"url": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Base URL of the registry, for example `http://localhost:8090`. The provider appends " +
					"`/apis/registry/v3`; a URL that already ends with it is accepted. Env: `APICURIO_URL`.",
			},
			"token": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				MarkdownDescription: "Static bearer token sent as `Authorization: Bearer`. Conflicts with `username`/`password`. " +
					"Env: `APICURIO_TOKEN`.",
			},
			"username": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "HTTP basic user name. On a registry with `apicurio.authn.basic-client-credentials.enabled=true` " +
					"this is an OIDC client id and `password` its secret; the registry exchanges them for a token itself. " +
					"Requires `password`. Env: `APICURIO_USERNAME`.",
			},
			"password": schema.StringAttribute{
				Optional:            true,
				Sensitive:           true,
				MarkdownDescription: "HTTP basic password (or OIDC client secret, see `username`). Env: `APICURIO_PASSWORD`.",
			},
			"validate_connection": schema.BoolAttribute{
				Optional: true,
				MarkdownDescription: "Check the registry while configuring the provider: `GET /system/info` (records the server " +
					"version) and, when credentials are configured, `GET /users/me`, so a wrong URL or rejected credentials " +
					"fail the plan right away. Defaults to `true`. Set it to `false` for configurations where the registry " +
					"is not deployed yet.",
			},
		},
	}
}

// ConfigValidators refuses mixed authentication settings at validation time.
func (p *apicurioProvider) ConfigValidators(context.Context) []provider.ConfigValidator {
	return []provider.ConfigValidator{
		providervalidator.Conflicting(path.MatchRoot("token"), path.MatchRoot("username")),
		providervalidator.Conflicting(path.MatchRoot("token"), path.MatchRoot("password")),
		providervalidator.RequiredTogether(path.MatchRoot("username"), path.MatchRoot("password")),
	}
}

func (p *apicurioProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	unknown := func(attr string, v interface{ IsUnknown() bool }) {
		if !v.IsUnknown() {
			return
		}
		hint := "Use a static value, or plan the configuration that produces it first."
		if env := envFor(attr); env != "" {
			hint = fmt.Sprintf("Use a static value or the %s environment variable, or plan the configuration that produces it first.", env)
		}
		resp.Diagnostics.AddAttributeError(path.Root(attr), fmt.Sprintf("Unknown Apicurio Registry %s", attr),
			fmt.Sprintf("The provider cannot create its API client because `%s` is unknown at plan time. %s", attr, hint))
	}
	unknown("url", cfg.URL)
	unknown("token", cfg.Token)
	unknown("username", cfg.Username)
	unknown("password", cfg.Password)
	unknown("validate_connection", cfg.ValidateConnection)
	if resp.Diagnostics.HasError() {
		return
	}

	rawURL := valueOrEnv(cfg.URL, envURL)
	token := valueOrEnv(cfg.Token, envToken)
	username := valueOrEnv(cfg.Username, envUsername)
	password := valueOrEnv(cfg.Password, envPassword)

	// Configuration wins per authentication method: an authentication
	// attribute set in the provider block hides the environment variables of
	// the other method, so an exported APICURIO_USERNAME does not break a
	// block that configures `token` (and vice versa). With nothing configured
	// a mixed environment is still reported as a conflict below.
	switch {
	case isSet(cfg.Token):
		username, password = "", ""
	case isSet(cfg.Username) || isSet(cfg.Password):
		token = ""
	}

	if rawURL == "" {
		resp.Diagnostics.AddAttributeError(path.Root("url"), "Missing Apicurio Registry URL",
			"Set `url` in the provider block or the APICURIO_URL environment variable, for example http://localhost:8090.")
	}
	if token != "" && (username != "" || password != "") {
		resp.Diagnostics.AddError("Conflicting Authentication Settings",
			"Use either `token` (APICURIO_TOKEN) or `username`/`password` (APICURIO_USERNAME/APICURIO_PASSWORD), not both.")
	}
	if (username == "") != (password == "") {
		resp.Diagnostics.AddError("Incomplete Basic Authentication",
			"`username` and `password` (APICURIO_USERNAME and APICURIO_PASSWORD) must be set together.")
	}
	if resp.Diagnostics.HasError() {
		return
	}

	opts := []client.Option{client.WithUserAgent("terraform-provider-apicurio/" + p.version)}
	switch {
	case token != "":
		opts = append(opts, client.WithToken(token))
	case username != "":
		opts = append(opts, client.WithBasicAuth(username, password))
	}
	c, err := client.New(rawURL, opts...)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("url"), "Invalid Apicurio Registry URL", err.Error())
		return
	}

	validate := cfg.ValidateConnection.IsNull() || cfg.ValidateConnection.ValueBool()
	if validate {
		resp.Diagnostics.Append(validateConnection(ctx, c)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	resp.DataSourceData = c
	resp.ResourceData = c
}

// validateConnection checks the server and, with credentials, the identity
// the registry gives them. GET /system/info and GET /users/me are public even
// on a secured registry, and a failed HTTP basic login is answered as the
// anonymous user rather than with a 401, so the identity check looks at the
// username, not at the status.
func validateConnection(ctx context.Context, c *client.Client) diag.Diagnostics {
	var diags diag.Diagnostics
	info, err := c.GetSystemInfo(ctx)
	if err != nil {
		// /system/info is public, so a 401 here means the credentials
		// themselves were refused (a malformed or expired bearer token).
		if errors.Is(err, client.ErrUnauthorized) || errors.Is(err, client.ErrForbidden) {
			diags.AddError("Unable to Authenticate to the Apicurio Registry", err.Error())
			return diags
		}
		diags.AddError("Unable to Reach the Apicurio Registry",
			err.Error()+"\n\nSet validate_connection = false to skip this check for a registry that is not deployed yet.")
		return diags
	}
	tflog.Debug(ctx, "connected to Apicurio Registry", map[string]any{
		"url":     c.ServerURL(),
		"name":    info.Name,
		"version": info.Version,
		"auth":    c.AuthMethod(),
	})
	if !c.HasCredentials() {
		return diags
	}
	me, err := c.GetCurrentUser(ctx)
	if err != nil {
		diags.AddError("Unable to Authenticate to the Apicurio Registry", err.Error())
		return diags
	}
	if me.Username == "" {
		var detail string
		switch c.AuthMethod() {
		case client.AuthToken:
			detail = fmt.Sprintf("GET %s/users/me answered as the anonymous user although a bearer token was sent. "+
				"Either this registry has no authentication enabled (then drop `token`/APICURIO_TOKEN) or it did not "+
				"accept the token.", c.BaseURL())
		default:
			detail = fmt.Sprintf("GET %s/users/me answered as the anonymous user. The registry does that when the HTTP basic "+
				"client id or secret cannot be exchanged for a token. Check `username`/`password` "+
				"(APICURIO_USERNAME/APICURIO_PASSWORD), or the server's apicurio.authn.basic-client-credentials.enabled setting.",
				c.BaseURL())
		}
		diags.AddError("Apicurio Registry Rejected the Credentials", detail)
		return diags
	}
	tflog.Debug(ctx, "authenticated to Apicurio Registry", map[string]any{
		"username":  me.Username,
		"admin":     me.Admin,
		"developer": me.Developer,
		"viewer":    me.Viewer,
	})
	return diags
}

func (p *apicurioProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewGroupResource,
		NewArtifactResource,
	}
}

func (p *apicurioProvider) DataSources(context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewSystemInfoDataSource,
		NewGroupDataSource,
		NewArtifactDataSource,
	}
}

// valueOrEnv returns the configured value, or the environment variable when
// the attribute is null or an empty string (a variable with an empty default).
func valueOrEnv(v types.String, env string) string {
	if isSet(v) {
		return v.ValueString()
	}
	return os.Getenv(env)
}

func envFor(attr string) string {
	switch attr {
	case "url":
		return envURL
	case "token":
		return envToken
	case "username":
		return envUsername
	case "password":
		return envPassword
	}
	return ""
}

// clientFromProviderData extracts the configured client in the Configure
// method of resources and data sources. It returns nil (and adds an error
// when the type is wrong) when the provider has not been configured yet.
func clientFromProviderData(data any, diags *diag.Diagnostics) *client.Client {
	if data == nil {
		return nil
	}
	c, ok := data.(*client.Client)
	if !ok {
		diags.AddError("Unexpected Provider Data Type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a bug in the provider.", data))
		return nil
	}
	return c
}
