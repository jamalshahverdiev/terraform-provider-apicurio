package provider

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/jarcoal/httpmock"
)

const mockTokenURL = "http://keycloak-host/realms/registry/protocol/openid-connect/token"

var oidcType = tftypes.Object{AttributeTypes: map[string]tftypes.Type{
	"token_url":     tftypes.String,
	"client_id":     tftypes.String,
	"client_secret": tftypes.String,
	"scopes":        tftypes.List{ElementType: tftypes.String},
}}

func oidcValue(tokenURL, clientID, clientSecret string, scopes ...string) tftypes.Value {
	vals := map[string]tftypes.Value{
		"token_url":     str(tokenURL),
		"client_id":     str(clientID),
		"client_secret": str(clientSecret),
		"scopes":        tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
	}
	if tokenURL == "" {
		vals["token_url"] = tftypes.NewValue(tftypes.String, nil)
	}
	if clientID == "" {
		vals["client_id"] = tftypes.NewValue(tftypes.String, nil)
	}
	if clientSecret == "" {
		vals["client_secret"] = tftypes.NewValue(tftypes.String, nil)
	}
	if len(scopes) > 0 {
		elems := make([]tftypes.Value, 0, len(scopes))
		for _, sc := range scopes {
			elems = append(elems, str(sc))
		}
		vals["scopes"] = tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, elems)
	}
	return tftypes.NewValue(oidcType, vals)
}

// registerTokenEndpoint answers the mock token endpoint with `token`; the
// registry mock then recognises only that bearer.
func registerTokenEndpoint(t *testing.T, token string, wantForm ...string) {
	t.Helper()
	httpmock.RegisterResponder(http.MethodPost, mockTokenURL, func(req *http.Request) (*http.Response, error) {
		if err := req.ParseForm(); err != nil {
			t.Errorf("token request form: %v", err)
		}
		form := req.PostForm.Encode()
		for _, w := range wantForm {
			if !strings.Contains(form, w) {
				t.Errorf("token form %q lacks %q", form, w)
			}
		}
		return httpmock.NewStringResponse(200, fmt.Sprintf(`{"access_token":%q,"expires_in":600,"token_type":"Bearer"}`, token)), nil
	})
	httpmock.RegisterResponder("GET", mockAPI+"/users/me", func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") == "Bearer "+token {
			return httpmock.NewStringResponse(200, `{"username":"service-account-registry-api","displayName":"service-account-registry-api","admin":true,"developer":false,"viewer":false}`), nil
		}
		return httpmock.NewStringResponse(200, `{"username":"","displayName":"","admin":false,"developer":false,"viewer":false}`), nil
	})
}

func tokenCalls() int {
	return httpmock.GetCallCountInfo()["POST "+mockTokenURL]
}

func TestConfigureOIDCFetchesTokenAndAuthenticates(t *testing.T) {
	setupMock(t)
	registerTokenEndpoint(t, "tok-1", "grant_type=client_credentials", "client_id=registry-api", "client_secret=s3cret", "scope=openid")
	requireNoError(t, configureProvider(t, map[string]tftypes.Value{
		"url":  str(mockURL),
		"oidc": oidcValue(mockTokenURL, "registry-api", "s3cret", "openid"),
	}))
	if n := tokenCalls(); n != 1 {
		t.Errorf("token endpoint called %d times, want 1 (the connection check sends two requests)", n)
	}
}

func TestConfigureOIDCFromEnvironment(t *testing.T) {
	setupMock(t)
	registerTokenEndpoint(t, "tok-env", "client_id=env-client", "client_secret=env-secret")
	t.Setenv(envOIDCTokenURL, mockTokenURL)
	t.Setenv(envOIDCClientID, "env-client")
	t.Setenv(envOIDCClientSecret, "env-secret")
	requireNoError(t, configureProvider(t, map[string]tftypes.Value{"url": str(mockURL)}))
	if n := tokenCalls(); n != 1 {
		t.Errorf("token endpoint called %d times, want 1", n)
	}
}

func TestConfigureOIDCBadClientSecret(t *testing.T) {
	setupMock(t)
	httpmock.RegisterResponder(http.MethodPost, mockTokenURL, httpmock.NewStringResponder(401,
		`{"error":"unauthorized_client","error_description":"Invalid client or Invalid client credentials"}`))
	diags := configureProvider(t, map[string]tftypes.Value{
		"url":  str(mockURL),
		"oidc": oidcValue(mockTokenURL, "registry-api", "wrong"),
	})
	requireError(t, diags, "Unable to Authenticate to the Apicurio Registry")
	if msgs := strings.Join(errorsOf(diags), "\n"); !strings.Contains(msgs, "unauthorized_client") || !strings.Contains(msgs, "Invalid client credentials") {
		t.Errorf("the identity provider's reason must be shown, got %q", msgs)
	}
}

func TestConfigureOIDCTokenRejectedByRegistry(t *testing.T) {
	setupMock(t)
	registerTokenEndpoint(t, "tok-1")
	// the registry does not trust the issuer: anonymous answer
	httpmock.RegisterResponder("GET", mockAPI+"/users/me", httpmock.NewStringResponder(200,
		`{"username":"","displayName":"","admin":false,"developer":false,"viewer":false}`))
	diags := configureProvider(t, map[string]tftypes.Value{
		"url":  str(mockURL),
		"oidc": oidcValue(mockTokenURL, "registry-api", "s3cret"),
	})
	requireError(t, diags, "Apicurio Registry Rejected the Credentials")
	if msgs := strings.Join(errorsOf(diags), "\n"); !strings.Contains(msgs, "oidc.token_url") {
		t.Errorf("the OIDC hint must be used, got %q", msgs)
	}
}

func TestConfigureOIDCConflictsAndCompleteness(t *testing.T) {
	setupMock(t)
	registerTokenEndpoint(t, "tok-1")
	requireError(t, configureProvider(t, map[string]tftypes.Value{
		"url":   str(mockURL),
		"token": str("static"),
		"oidc":  oidcValue(mockTokenURL, "registry-api", "s3cret"),
	}), "Conflicting Authentication Settings")
	requireError(t, configureProvider(t, map[string]tftypes.Value{
		"url":  str(mockURL),
		"oidc": oidcValue(mockTokenURL, "", ""),
	}), "Incomplete OIDC Configuration")
	requireError(t, configureProvider(t, map[string]tftypes.Value{
		"url":  str(mockURL),
		"oidc": oidcValue("keycloak-host/token", "registry-api", "s3cret"),
	}), "Invalid OIDC Token URL")

	// an oidc block in the configuration hides APICURIO_TOKEN from the environment
	t.Setenv(envToken, "env-token")
	requireNoError(t, configureProvider(t, map[string]tftypes.Value{
		"url":  str(mockURL),
		"oidc": oidcValue(mockTokenURL, "registry-api", "s3cret"),
	}))
	// and a configured token hides the OIDC environment variables
	t.Setenv(envToken, "")
	t.Setenv(envOIDCTokenURL, mockTokenURL)
	t.Setenv(envOIDCClientID, "registry-api")
	t.Setenv(envOIDCClientSecret, "s3cret")
	httpmock.RegisterResponder("GET", mockAPI+"/users/me", func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") == "Bearer static" {
			return httpmock.NewStringResponse(200, `{"username":"static-user","admin":true}`), nil
		}
		return httpmock.NewStringResponse(200, `{"username":""}`), nil
	})
	requireNoError(t, configureProvider(t, map[string]tftypes.Value{"url": str(mockURL), "token": str("static")}))
}

func TestConfigureOIDCUnknownNestedValue(t *testing.T) {
	setupMock(t)
	diags := configureProvider(t, map[string]tftypes.Value{
		"url": str(mockURL),
		"oidc": tftypes.NewValue(oidcType, map[string]tftypes.Value{
			"token_url":     str(mockTokenURL),
			"client_id":     str("registry-api"),
			"client_secret": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
			"scopes":        tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		}),
	})
	requireError(t, diags, "Unknown Apicurio Registry oidc.client_secret")

	// an unknown element inside a known scopes list is reported too, before any token request
	diags = configureProvider(t, map[string]tftypes.Value{
		"url": str(mockURL),
		"oidc": tftypes.NewValue(oidcType, map[string]tftypes.Value{
			"token_url":     str(mockTokenURL),
			"client_id":     str("registry-api"),
			"client_secret": str("s3cret"),
			"scopes":        tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{tftypes.NewValue(tftypes.String, tftypes.UnknownValue)}),
		}),
	})
	requireError(t, diags, "Unknown Apicurio Registry oidc.scopes")
	if n := tokenCalls(); n != 0 {
		t.Errorf("no token request must be made with an unknown scope, got %d", n)
	}
}

// tlsRegistry serves the two public endpoints over TLS with httptest's
// self-signed certificate.
func tlsRegistry(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apis/registry/v3/system/info":
			_, _ = w.Write([]byte(`{"name":"Apicurio Registry (SQL)","version":"3.3.4"}`))
		case "/apis/registry/v3/users/me":
			_, _ = w.Write([]byte(`{"username":"","displayName":"","admin":true,"developer":false,"viewer":false}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	return srv, ca
}

func TestConfigureTLS(t *testing.T) {
	clearEnv(t)
	srv, ca := tlsRegistry(t)

	diags := configureProvider(t, map[string]tftypes.Value{"url": str(srv.URL)})
	requireError(t, diags, "Unable to Reach the Apicurio Registry")
	if msgs := strings.Join(errorsOf(diags), "\n"); !strings.Contains(msgs, "certificate") {
		t.Errorf("the TLS failure must be visible, got %q", msgs)
	}

	requireNoError(t, configureProvider(t, map[string]tftypes.Value{
		"url":                  str(srv.URL),
		"insecure_skip_verify": tftypes.NewValue(tftypes.Bool, true),
	}))
	requireNoError(t, configureProvider(t, map[string]tftypes.Value{
		"url":            str(srv.URL),
		"ca_certificate": str(ca),
	}))
	requireError(t, configureProvider(t, map[string]tftypes.Value{
		"url":            str(srv.URL),
		"ca_certificate": str("not a certificate"),
	}), "Invalid CA Certificate")

	// the same from the environment
	t.Setenv(envCACertificate, ca)
	requireNoError(t, configureProvider(t, map[string]tftypes.Value{"url": str(srv.URL)}))
	t.Setenv(envCACertificate, "")
	t.Setenv(envInsecureSkipVerif, "true")
	requireNoError(t, configureProvider(t, map[string]tftypes.Value{"url": str(srv.URL)}))
	t.Setenv(envInsecureSkipVerif, "maybe")
	requireError(t, configureProvider(t, map[string]tftypes.Value{"url": str(srv.URL)}), "Invalid APICURIO_INSECURE_SKIP_VERIFY")

	// sanity: the PEM really is the server certificate
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(ca)) {
		t.Fatal("test CA PEM is invalid")
	}
}

// TestAccProviderOIDC runs a configuration through Terraform with the oidc
// block against the fake registry, which records the bearer it received.
func TestAccProviderOIDC(t *testing.T) {
	setupRegistry(t)
	if liveServer() {
		t.Skip("the fake registry records the bearer token; live OIDC runs use the APICURIO_OIDC_* environment variables")
	}
	registerTokenEndpoint(t, "fake-oidc-token")
	cfg := fmt.Sprintf(`
provider "apicurio" {
  url = %q
  oidc = {
    token_url     = %q
    client_id     = "registry-api"
    client_secret = "registry-api-secret"
  }
}

data "apicurio_current_user" "me" {}
`, mockURL, mockTokenURL)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.apicurio_current_user.me", "username", "service-account-registry-api"),
					func(_ *terraformState) error {
						if fake.lastAuth != "Bearer fake-oidc-token" {
							return fmt.Errorf("the registry saw %q, want the OIDC bearer", fake.lastAuth)
						}
						return nil
					},
				),
			},
			{
				Config: strings.Replace(cfg, `client_secret = "registry-api-secret"`, `client_secret = "wrong"`, 1),
				PreConfig: func() {
					httpmock.RegisterResponder(http.MethodPost, mockTokenURL, httpmock.NewStringResponder(401,
						`{"error":"unauthorized_client","error_description":"Invalid client or Invalid client credentials"}`))
				},
				ExpectError: regexp.MustCompile(`Unable to Authenticate[\s\S]*unauthorized_client`),
			},
			{
				// back to a working secret, so the test's own destroy can run
				PreConfig: func() { registerTokenEndpoint(t, "fake-oidc-token-2") },
				Config:    cfg,
				Check: func(_ *terraformState) error {
					if fake.lastAuth != "Bearer fake-oidc-token-2" {
						return fmt.Errorf("the registry saw %q, want the renewed OIDC bearer", fake.lastAuth)
					}
					return nil
				},
			},
		},
	})
}
