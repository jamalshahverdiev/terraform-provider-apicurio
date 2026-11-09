package provider

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/jarcoal/httpmock"
)

var errConnRefused = errors.New("dial tcp: connection refused")

// mockURL is the registry address the fake server answers at. The
// acceptance tests run against it unless APICURIO_URL points at a real one.
const (
	mockURL     = "http://apicurio-host"
	mockAPI     = mockURL + "/apis/registry/v3"
	mockVersion = "3.3.4"
)

var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"apicurio": providerserver.NewProtocol6WithError(New("test")()),
}

// liveServer reports whether APICURIO_URL points at a real registry, in
// which case the fake server stays off and the checks only look at shapes.
func liveServer() bool {
	u := os.Getenv("APICURIO_URL")
	return u != "" && u != mockURL
}

// setupMock points the provider at the fake registry and activates httpmock
// on the default transport, unless a live server is configured.
func setupMock(t *testing.T) {
	t.Helper()
	if liveServer() {
		return
	}
	clearEnv(t)
	t.Setenv("APICURIO_URL", mockURL)
	activateMock(t)
	registerSystemResponders(`{"username":"","displayName":"","admin":true,"developer":false,"viewer":false}`)
}

func activateMock(t *testing.T) {
	t.Helper()
	httpmock.Activate()
	t.Cleanup(httpmock.DeactivateAndReset)
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, e := range []string{envURL, envToken, envUsername, envPassword, envOIDCTokenURL, envOIDCClientID, envOIDCClientSecret, envInsecureSkipVerif, envCACertificate} {
		t.Setenv(e, "")
	}
}

func registerSystemResponders(usersMe string) {
	httpmock.RegisterResponder("GET", mockAPI+"/system/info", httpmock.NewStringResponder(200,
		`{"name":"Apicurio Registry (SQL)","description":"High performance, runtime registry for schemas and API designs.","version":"`+mockVersion+`","builtOn":"2026-11-03T19:25:03Z"}`))
	httpmock.RegisterResponder("GET", mockAPI+"/users/me", httpmock.NewStringResponder(200, usersMe))
}

func testAccPreCheck(t *testing.T) {
	t.Helper()
	if os.Getenv("APICURIO_URL") == "" {
		t.Fatal("APICURIO_URL must be set for acceptance tests (setupMock sets it when no live server is configured)")
	}
}

// configureProvider drives ConfigureProvider through the protocol with the
// given attribute values (missing attributes are null) and returns the
// diagnostics.
func configureProvider(t *testing.T, attrs map[string]tftypes.Value) []*tfprotov6.Diagnostic {
	t.Helper()
	ctx := context.Background()
	srv, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatalf("provider server: %v", err)
	}
	schemaResp, err := srv.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatalf("GetProviderSchema: %v", err)
	}
	objType, ok := schemaResp.Provider.ValueType().(tftypes.Object)
	if !ok {
		t.Fatalf("provider schema type %T is not an object", schemaResp.Provider.ValueType())
	}
	vals := map[string]tftypes.Value{}
	for name, at := range objType.AttributeTypes {
		if v, ok := attrs[name]; ok {
			vals[name] = v
		} else {
			vals[name] = tftypes.NewValue(at, nil)
		}
	}
	for name := range attrs {
		if _, ok := objType.AttributeTypes[name]; !ok {
			t.Fatalf("test sets unknown attribute %q", name)
		}
	}
	dv, err := tfprotov6.NewDynamicValue(objType, tftypes.NewValue(objType, vals))
	if err != nil {
		t.Fatalf("NewDynamicValue: %v", err)
	}
	resp, err := srv.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{Config: &dv})
	if err != nil {
		t.Fatalf("ConfigureProvider: %v", err)
	}
	return resp.Diagnostics
}

func str(s string) tftypes.Value { return tftypes.NewValue(tftypes.String, s) }

func errorsOf(diags []*tfprotov6.Diagnostic) []string {
	var out []string
	for _, d := range diags {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			out = append(out, d.Summary+": "+d.Detail)
		}
	}
	return out
}

func requireError(t *testing.T, diags []*tfprotov6.Diagnostic, want string) {
	t.Helper()
	for _, e := range errorsOf(diags) {
		if strings.Contains(e, want) {
			return
		}
	}
	t.Fatalf("want an error containing %q, got %q", want, errorsOf(diags))
}

func requireNoError(t *testing.T, diags []*tfprotov6.Diagnostic) {
	t.Helper()
	if errs := errorsOf(diags); len(errs) > 0 {
		t.Fatalf("unexpected errors: %q", errs)
	}
}

func TestConfigureMissingURL(t *testing.T) {
	clearEnv(t)
	requireError(t, configureProvider(t, nil), "Missing Apicurio Registry URL")
}

func TestConfigureInvalidURL(t *testing.T) {
	clearEnv(t)
	requireError(t, configureProvider(t, map[string]tftypes.Value{"url": str("localhost:8090")}), "Invalid Apicurio Registry URL")
}

func TestConfigureUnknownValue(t *testing.T) {
	clearEnv(t)
	diags := configureProvider(t, map[string]tftypes.Value{"url": tftypes.NewValue(tftypes.String, tftypes.UnknownValue)})
	requireError(t, diags, "Unknown Apicurio Registry url")
}

func TestConfigureConflictingAuthFromEnvironment(t *testing.T) {
	clearEnv(t)
	activateMock(t)
	// Config-level conflicts are caught by the config validators; a mixed
	// environment with nothing configured is caught by Configure itself.
	t.Setenv(envToken, "tok")
	t.Setenv(envUsername, "u")
	t.Setenv(envPassword, "p")
	diags := configureProvider(t, map[string]tftypes.Value{"url": str(mockURL)})
	requireError(t, diags, "Conflicting Authentication Settings")
	if httpmock.GetTotalCallCount() != 0 {
		t.Errorf("no request may be sent before the settings are accepted, got %d", httpmock.GetTotalCallCount())
	}
}

func TestConfigureConfiguredTokenWinsOverEnvBasic(t *testing.T) {
	clearEnv(t)
	activateMock(t)
	registerSystemResponders(`{"username":"svc","admin":true}`)
	var gotAuth string
	httpmock.RegisterResponder("GET", mockAPI+"/users/me", func(r *http.Request) (*http.Response, error) {
		gotAuth = r.Header.Get("Authorization")
		return httpmock.NewStringResponse(200, `{"username":"svc","admin":true}`), nil
	})
	t.Setenv(envUsername, "stray")
	t.Setenv(envPassword, "stray")
	requireNoError(t, configureProvider(t, map[string]tftypes.Value{"url": str(mockURL), "token": str("tok")}))
	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q, want the configured token", gotAuth)
	}
}

func TestConfigureConfiguredBasicWinsOverEnvToken(t *testing.T) {
	clearEnv(t)
	activateMock(t)
	var gotAuth string
	registerSystemResponders(`{"username":"svc","admin":true}`)
	httpmock.RegisterResponder("GET", mockAPI+"/users/me", func(r *http.Request) (*http.Response, error) {
		gotAuth = r.Header.Get("Authorization")
		return httpmock.NewStringResponse(200, `{"username":"svc","admin":true}`), nil
	})
	t.Setenv(envToken, "stray")
	requireNoError(t, configureProvider(t, map[string]tftypes.Value{"url": str(mockURL), "username": str("u"), "password": str("p")}))
	if !strings.HasPrefix(gotAuth, "Basic ") {
		t.Errorf("Authorization = %q, want the configured basic credentials", gotAuth)
	}
}

func TestConfigureEmptyStringFallsBackToEnvironment(t *testing.T) {
	clearEnv(t)
	activateMock(t)
	registerSystemResponders(`{"username":"svc","admin":true}`)
	t.Setenv(envURL, mockURL)
	t.Setenv(envToken, "env-token")
	// `url = ""` and `token = ""` (a variable with an empty default) count as unset.
	requireNoError(t, configureProvider(t, map[string]tftypes.Value{"url": str(""), "token": str("")}))
	if httpmock.GetCallCountInfo()["GET "+mockAPI+"/users/me"] != 1 {
		t.Errorf("the token from the environment must be used: %v", httpmock.GetCallCountInfo())
	}
}

func TestConfigureUnknownValidateConnection(t *testing.T) {
	clearEnv(t)
	diags := configureProvider(t, map[string]tftypes.Value{
		"url":                 str(mockURL),
		"validate_connection": tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue),
	})
	requireError(t, diags, "Unknown Apicurio Registry validate_connection")
	for _, e := range errorsOf(diags) {
		if strings.Contains(e, "APICURIO_*") {
			t.Errorf("the hint names an environment variable that does not exist: %s", e)
		}
	}
}

func TestConfigureTokenAgainstOpenRegistry(t *testing.T) {
	clearEnv(t)
	activateMock(t)
	// An open registry ignores the bearer token and answers /users/me as the
	// anonymous user; the message must talk about the token, not about basic auth.
	registerSystemResponders(`{"username":"","displayName":"","admin":true,"developer":false,"viewer":false}`)
	t.Setenv(envToken, "stray")
	diags := configureProvider(t, map[string]tftypes.Value{"url": str(mockURL)})
	requireError(t, diags, "Apicurio Registry Rejected the Credentials")
	requireError(t, diags, "bearer token")
	for _, e := range errorsOf(diags) {
		if strings.Contains(e, "client id or secret") {
			t.Errorf("basic-auth wording for a token problem: %s", e)
		}
	}
}

func TestConfigureIncompleteBasicAuth(t *testing.T) {
	clearEnv(t)
	t.Setenv(envUsername, "u")
	requireError(t, configureProvider(t, map[string]tftypes.Value{"url": str(mockURL)}), "Incomplete Basic Authentication")
}

func TestConfigureValidateFalseSendsNothing(t *testing.T) {
	clearEnv(t)
	activateMock(t) // no responders: any request would fail
	diags := configureProvider(t, map[string]tftypes.Value{
		"url":                 str("http://registry.not.deployed.yet"),
		"validate_connection": tftypes.NewValue(tftypes.Bool, false),
	})
	requireNoError(t, diags)
	if httpmock.GetTotalCallCount() != 0 {
		t.Errorf("validate_connection=false must not send requests, got %d", httpmock.GetTotalCallCount())
	}
}

func TestConfigureValidatesWithoutCredentials(t *testing.T) {
	clearEnv(t)
	activateMock(t)
	registerSystemResponders(`{"username":"","admin":true}`)
	requireNoError(t, configureProvider(t, map[string]tftypes.Value{"url": str(mockURL)}))
	info := httpmock.GetCallCountInfo()
	if info["GET "+mockAPI+"/system/info"] != 1 || info["GET "+mockAPI+"/users/me"] != 0 {
		t.Errorf("calls = %v, want one GET /system/info and no GET /users/me", info)
	}
}

func TestConfigureValidatesCredentials(t *testing.T) {
	clearEnv(t)
	activateMock(t)
	registerSystemResponders(`{"username":"service-account-registry-api","admin":true}`)
	var gotAuth string
	httpmock.RegisterResponder("GET", mockAPI+"/users/me", func(r *http.Request) (*http.Response, error) {
		gotAuth = r.Header.Get("Authorization")
		return httpmock.NewStringResponse(200, `{"username":"service-account-registry-api","admin":true}`), nil
	})
	requireNoError(t, configureProvider(t, map[string]tftypes.Value{"url": str(mockURL), "token": str("abc")}))
	if gotAuth != "Bearer abc" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	info := httpmock.GetCallCountInfo()
	if info["GET "+mockAPI+"/system/info"] != 1 || info["GET "+mockAPI+"/users/me"] != 1 {
		t.Errorf("calls = %v", info)
	}
}

func TestConfigureRejectedBasicCredentials(t *testing.T) {
	clearEnv(t)
	activateMock(t)
	// A wrong client secret is not a 401: the registry answers as the anonymous user.
	registerSystemResponders(`{"username":"","displayName":"","admin":false,"developer":false,"viewer":false}`)
	t.Setenv(envUsername, "registry-api")
	t.Setenv(envPassword, "wrong")
	diags := configureProvider(t, map[string]tftypes.Value{"url": str(mockURL)})
	requireError(t, diags, "Apicurio Registry Rejected the Credentials")
}

func TestConfigureRejectedToken(t *testing.T) {
	clearEnv(t)
	activateMock(t)
	registerSystemResponders("")
	httpmock.RegisterResponder("GET", mockAPI+"/users/me", httpmock.NewStringResponder(401, ""))
	diags := configureProvider(t, map[string]tftypes.Value{"url": str(mockURL), "token": str("expired")})
	requireError(t, diags, "Unable to Authenticate to the Apicurio Registry")
	requireError(t, diags, "credentials were rejected")
}

func TestConfigureUnauthorizedToken(t *testing.T) {
	clearEnv(t)
	activateMock(t)
	// A malformed or expired bearer token is refused even on the public
	// /system/info endpoint; the error must say so instead of "unreachable".
	httpmock.RegisterResponder("GET", mockAPI+"/system/info", httpmock.NewStringResponder(401, ""))
	diags := configureProvider(t, map[string]tftypes.Value{"url": str(mockURL), "token": str("garbage")})
	requireError(t, diags, "Unable to Authenticate to the Apicurio Registry")
	requireError(t, diags, "credentials were rejected")
	for _, e := range errorsOf(diags) {
		if strings.Contains(e, "Unable to Reach") {
			t.Errorf("a 401 must not be reported as unreachable: %s", e)
		}
	}
}

func TestConfigureUnreachable(t *testing.T) {
	clearEnv(t)
	activateMock(t)
	httpmock.RegisterResponder("GET", mockAPI+"/system/info", httpmock.NewErrorResponder(errConnRefused))
	diags := configureProvider(t, map[string]tftypes.Value{"url": str(mockURL)})
	requireError(t, diags, "Unable to Reach the Apicurio Registry")
	requireError(t, diags, "validate_connection = false")
}

func TestConfigureWrongURLIsNotNotFound(t *testing.T) {
	clearEnv(t)
	activateMock(t)
	httpmock.RegisterResponder("GET", mockAPI+"/system/info", httpmock.NewStringResponder(404,
		`{"detail":"NotFoundException: RESTEASY003210: Could not find resource for full path","title":"RESTEASY003210: Could not find resource for full path","status":404,"name":"NotFoundException"}`))
	diags := configureProvider(t, map[string]tftypes.Value{"url": str(mockURL)})
	requireError(t, diags, "check the provider url")
}

func TestConfigureFromEnvironment(t *testing.T) {
	clearEnv(t)
	activateMock(t)
	registerSystemResponders(`{"username":"admin","admin":true}`)
	t.Setenv(envURL, mockURL)
	t.Setenv(envToken, "from-env")
	requireNoError(t, configureProvider(t, nil))
	if httpmock.GetCallCountInfo()["GET "+mockAPI+"/users/me"] != 1 {
		t.Errorf("token from the environment must trigger the identity check: %v", httpmock.GetCallCountInfo())
	}
}
