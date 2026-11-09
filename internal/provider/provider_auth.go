package provider

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/url"
	"os"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/jamalshahverdiev/terraform-provider-apicurio/internal/client"
)

// Environment variables of the OIDC block and the TLS options.
const (
	envOIDCTokenURL      = "APICURIO_OIDC_TOKEN_URL"
	envOIDCClientID      = "APICURIO_OIDC_CLIENT_ID"
	envOIDCClientSecret  = "APICURIO_OIDC_CLIENT_SECRET"
	envInsecureSkipVerif = "APICURIO_INSECURE_SKIP_VERIFY"
	envCACertificate     = "APICURIO_CA_CERTIFICATE"
)

type oidcModel struct {
	TokenURL     types.String `tfsdk:"token_url"`
	ClientID     types.String `tfsdk:"client_id"`
	ClientSecret types.String `tfsdk:"client_secret"`
	Scopes       types.List   `tfsdk:"scopes"`
}

// providerSettings is the provider configuration after the environment
// fallback and the consistency checks.
type providerSettings struct {
	url                string
	token              string
	username, password string
	oidc               *oidcSettings
	tlsConfig          *tls.Config
	validate           bool
}

type oidcSettings struct {
	tokenURL, clientID, clientSecret string
	scopes                           []string
}

// clientOptions turns the settings into client options.
func (s providerSettings) clientOptions(version string) []client.Option {
	opts := []client.Option{client.WithUserAgent("terraform-provider-apicurio/" + version)}
	switch {
	case s.token != "":
		opts = append(opts, client.WithToken(s.token))
	case s.username != "":
		opts = append(opts, client.WithBasicAuth(s.username, s.password))
	case s.oidc != nil:
		opts = append(opts, client.WithOIDCClientCredentials(s.oidc.tokenURL, s.oidc.clientID, s.oidc.clientSecret, s.oidc.scopes))
	}
	if s.tlsConfig != nil {
		opts = append(opts, client.WithTLSConfig(s.tlsConfig))
	}
	return opts
}

// resolveProviderSettings applies the environment fallback (a non-empty
// configuration value wins; an authentication method set in the
// configuration hides the environment variables of the other methods) and
// checks the result.
func resolveProviderSettings(ctx context.Context, cfg providerModel) (providerSettings, diag.Diagnostics) {
	var diags diag.Diagnostics
	var s providerSettings

	unknown := func(p path.Path, env string, v interface{ IsUnknown() bool }) {
		if !v.IsUnknown() {
			return
		}
		hint := "Use a static value, or plan the configuration that produces it first."
		if env != "" {
			hint = fmt.Sprintf("Use a static value or the %s environment variable, or plan the configuration that produces it first.", env)
		}
		diags.AddAttributeError(p, fmt.Sprintf("Unknown Apicurio Registry %s", p),
			fmt.Sprintf("The provider cannot create its API client because `%s` is unknown at plan time. %s", p, hint))
	}
	unknown(path.Root("url"), envURL, cfg.URL)
	unknown(path.Root("token"), envToken, cfg.Token)
	unknown(path.Root("username"), envUsername, cfg.Username)
	unknown(path.Root("password"), envPassword, cfg.Password)
	unknown(path.Root("validate_connection"), "", cfg.ValidateConnection)
	unknown(path.Root("insecure_skip_verify"), envInsecureSkipVerif, cfg.InsecureSkipVerify)
	unknown(path.Root("ca_certificate"), envCACertificate, cfg.CACertificate)
	unknown(path.Root("oidc"), "", cfg.OIDC)
	if diags.HasError() {
		return s, diags
	}
	var oidc oidcModel
	if !cfg.OIDC.IsNull() {
		diags.Append(cfg.OIDC.As(ctx, &oidc, basetypes.ObjectAsOptions{})...)
		if diags.HasError() {
			return s, diags
		}
		unknown(path.Root("oidc").AtName("token_url"), envOIDCTokenURL, oidc.TokenURL)
		unknown(path.Root("oidc").AtName("client_id"), envOIDCClientID, oidc.ClientID)
		unknown(path.Root("oidc").AtName("client_secret"), envOIDCClientSecret, oidc.ClientSecret)
		unknown(path.Root("oidc").AtName("scopes"), "", oidc.Scopes)
		if !oidc.Scopes.IsNull() && !oidc.Scopes.IsUnknown() {
			for _, e := range oidc.Scopes.Elements() {
				if e.IsUnknown() {
					unknown(path.Root("oidc").AtName("scopes"), "", e)
					break
				}
			}
		}
		if diags.HasError() {
			return s, diags
		}
	}

	s.url = valueOrEnv(cfg.URL, envURL)
	s.token = valueOrEnv(cfg.Token, envToken)
	s.username = valueOrEnv(cfg.Username, envUsername)
	s.password = valueOrEnv(cfg.Password, envPassword)
	tokenURL := valueOrEnv(oidc.TokenURL, envOIDCTokenURL)
	clientID := valueOrEnv(oidc.ClientID, envOIDCClientID)
	clientSecret := valueOrEnv(oidc.ClientSecret, envOIDCClientSecret)
	oidcConfigured := isSet(oidc.TokenURL) || isSet(oidc.ClientID) || isSet(oidc.ClientSecret)

	// Two methods in the provider block itself are a conflict (the config
	// validators report it too); otherwise the configured method hides the
	// environment variables of the other methods.
	configured := 0
	if isSet(cfg.Token) {
		configured++
	}
	if isSet(cfg.Username) || isSet(cfg.Password) {
		configured++
	}
	if oidcConfigured {
		configured++
	}
	if configured > 1 {
		diags.AddError("Conflicting Authentication Settings",
			"Use only one of `token`, `username`/`password` or the `oidc` block in the provider configuration.")
		return s, diags
	}
	switch {
	case isSet(cfg.Token):
		s.username, s.password = "", ""
		tokenURL, clientID, clientSecret = "", "", ""
	case isSet(cfg.Username) || isSet(cfg.Password):
		s.token = ""
		tokenURL, clientID, clientSecret = "", "", ""
	case oidcConfigured:
		s.token = ""
		s.username, s.password = "", ""
	}

	if s.url == "" {
		diags.AddAttributeError(path.Root("url"), "Missing Apicurio Registry URL",
			"Set `url` in the provider block or the APICURIO_URL environment variable, for example http://localhost:8090.")
	}
	methods := 0
	if s.token != "" {
		methods++
	}
	if s.username != "" || s.password != "" {
		methods++
	}
	oidcAny := tokenURL != "" || clientID != "" || clientSecret != ""
	if oidcAny {
		methods++
	}
	if methods > 1 {
		diags.AddError("Conflicting Authentication Settings",
			"Use only one of `token` (APICURIO_TOKEN), `username`/`password` (APICURIO_USERNAME/APICURIO_PASSWORD) or the `oidc` block "+
				"(APICURIO_OIDC_TOKEN_URL, APICURIO_OIDC_CLIENT_ID, APICURIO_OIDC_CLIENT_SECRET).")
	}
	if (s.username == "") != (s.password == "") {
		diags.AddError("Incomplete Basic Authentication",
			"`username` and `password` (APICURIO_USERNAME and APICURIO_PASSWORD) must be set together.")
	}
	if oidcAny {
		if tokenURL == "" || clientID == "" || clientSecret == "" {
			diags.AddAttributeError(path.Root("oidc"), "Incomplete OIDC Configuration",
				"`oidc.token_url`, `oidc.client_id` and `oidc.client_secret` (APICURIO_OIDC_TOKEN_URL, APICURIO_OIDC_CLIENT_ID, "+
					"APICURIO_OIDC_CLIENT_SECRET) must be set together.")
		} else if u, err := url.Parse(tokenURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			diags.AddAttributeError(path.Root("oidc").AtName("token_url"), "Invalid OIDC Token URL",
				fmt.Sprintf("%q is not an absolute http(s) URL; the token endpoint of a Keycloak realm is "+
					"https://<host>/realms/<realm>/protocol/openid-connect/token.", tokenURL))
		} else {
			var scopes []string
			for _, sc := range listToStrings(oidc.Scopes) {
				if sc != "" {
					scopes = append(scopes, sc)
				}
			}
			s.oidc = &oidcSettings{tokenURL: tokenURL, clientID: clientID, clientSecret: clientSecret, scopes: scopes}
		}
	}

	insecure := false
	switch {
	case !cfg.InsecureSkipVerify.IsNull():
		insecure = cfg.InsecureSkipVerify.ValueBool()
	case os.Getenv(envInsecureSkipVerif) != "":
		v, err := strconv.ParseBool(os.Getenv(envInsecureSkipVerif))
		if err != nil {
			diags.AddAttributeError(path.Root("insecure_skip_verify"), "Invalid "+envInsecureSkipVerif,
				fmt.Sprintf("%q is not a boolean; use true or false.", os.Getenv(envInsecureSkipVerif)))
		}
		insecure = v
	}
	var pool *x509.CertPool
	if pem := valueOrEnv(cfg.CACertificate, envCACertificate); pem != "" {
		// added to the system roots, as documented (SystemCertPool returns a copy)
		if pool, _ = x509.SystemCertPool(); pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(pem)) {
			diags.AddAttributeError(path.Root("ca_certificate"), "Invalid CA Certificate",
				"`ca_certificate` (APICURIO_CA_CERTIFICATE) must hold one or more PEM-encoded certificates, for example file(\"ca.pem\").")
		}
	}
	if insecure || pool != nil {
		s.tlsConfig = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: insecure, //nolint:gosec // an explicit opt-in of the user, documented as testing-only
			RootCAs:            pool,
		}
	}
	s.validate = cfg.ValidateConnection.IsNull() || cfg.ValidateConnection.ValueBool()
	return s, diags
}
