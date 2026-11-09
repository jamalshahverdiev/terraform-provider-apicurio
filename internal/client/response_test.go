package client

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/jarcoal/httpmock"
)

// problem builds a ProblemDetails body the way the registry answers.
func problem(status int, name, title string) string {
	return `{"status":` + strconv.Itoa(status) + `,"name":"` + name + `","title":"` + title + `","detail":"` + name + `: ` + title + `"}`
}

func TestErrorMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		opts          []Option
		status        int
		body          string
		wantIs        error
		wantNotIs     error
		wantType      string // "disabled", "rule", "" for a plain *APIError
		wantKind      string
		wantProperty  string
		wantCauses    int
		wantInMessage []string
		wantNotInMsg  []string
	}{
		{
			name: "group not found", status: 404,
			body:   problem(404, "GroupNotFoundException", "No group 'g' was found."),
			wantIs: ErrNotFound, wantInMessage: []string{"HTTP 404", "No group 'g' was found."},
		},
		{
			name: "version not found via branch", status: 404,
			body:   problem(404, "VersionNotFoundException", "No version '<tip of the branch 'latest'>' found for artifact with ID 'a' in group 'g'."),
			wantIs: ErrNotFound,
		},
		{
			name: "role mapping not found", status: 404,
			body:   problem(404, "RoleMappingNotFoundException", "No role mapping for principal 'p' was found."),
			wantIs: ErrNotFound,
		},
		{
			name: "config property not found", status: 404,
			body:   problem(404, "ConfigPropertyNotFoundException", "No configuration property named 'x' was found."),
			wantIs: ErrNotFound,
		},
		{
			name: "bare NotFoundException is a wrong route", status: 404,
			body:      problem(404, "NotFoundException", "RESTEASY003210: Could not find resource for full path"),
			wantNotIs: ErrNotFound, wantInMessage: []string{"check the provider url"},
		},
		{
			name: "html 404 from a proxy", status: 404,
			body:      "<html><body>nginx 404</body></html>",
			wantNotIs: ErrNotFound, wantInMessage: []string{"nginx 404", "check the provider url"},
		},
		{
			name: "artifact deletion disabled", status: 405,
			body:     problem(405, "NotAllowedException", "Artifact deletion operation is not enabled."),
			wantType: "disabled", wantKind: "artifact deletion", wantProperty: "apicurio.rest.deletion.artifact.enabled",
			wantInMessage: []string{"apicurio_config_property", "APICURIO_REST_DELETION_ARTIFACT_ENABLED=true"},
		},
		{
			name: "version deletion disabled", status: 405,
			body:     problem(405, "NotAllowedException", "Artifact version deletion operation is not enabled."),
			wantType: "disabled", wantKind: "artifact version deletion", wantProperty: "apicurio.rest.deletion.artifact-version.enabled",
			wantInMessage: []string{"APICURIO_REST_DELETION_ARTIFACT_VERSION_ENABLED=true"},
		},
		{
			name: "group deletion disabled", status: 405,
			body:     problem(405, "NotAllowedException", "Group deletion operation is not enabled."),
			wantType: "disabled", wantKind: "group deletion", wantProperty: "apicurio.rest.deletion.group.enabled",
		},
		{
			name: "content update disabled", status: 405,
			body:     problem(405, "NotAllowedException", "Artifact version content update operation is not enabled."),
			wantType: "disabled", wantKind: "artifact version content update", wantProperty: "apicurio.rest.mutability.artifact-version-content.enabled",
			wantInMessage: []string{"APICURIO_REST_MUTABILITY_ARTIFACT_VERSION_CONTENT_ENABLED=true"},
		},
		{
			name: "other 405 stays a plain error", status: 405,
			body: problem(405, "NotAllowedException", "Something else is not allowed."),
		},
		{
			name: "already exists", status: 409,
			body:   problem(409, "ArtifactAlreadyExistsException", "An artifact with ID 'a' in group 'g' already exists."),
			wantIs: ErrAlreadyExists,
		},
		{
			name: "branch already exists", status: 409,
			body:   problem(409, "BranchAlreadyExistsException", "Branch 'rel' already exists."),
			wantIs: ErrAlreadyExists,
		},
		{
			name: "conflict is not already-exists", status: 409,
			body:      problem(409, "ConflictException", "Requested artifact version is not in DRAFT state.  Update disallowed."),
			wantNotIs: ErrAlreadyExists, wantInMessage: []string{"not in DRAFT state"},
		},
		{
			name: "system branch deletion 409 is not already-exists", status: 409,
			body:      problem(409, "NotAllowedException", "System generated branches cannot be deleted."),
			wantNotIs: ErrAlreadyExists,
		},
		{
			name: "rule violation 400 (3.3)", status: 400,
			body:     `{"causes":[{"description":"vat","context":"/fields/3"}],"detail":"Incompatible artifact: comp [AVRO]","title":"Incompatible artifact: comp [AVRO], num of incompatible diffs: {1}","status":400,"name":"RuleViolationException"}`,
			wantType: "rule", wantCauses: 1, wantInMessage: []string{"refused the content", "Incompatible artifact", "vat at /fields/3"},
		},
		{
			name: "rule violation 409 (3.0 to 3.2)", status: 409,
			body:     `{"causes":[],"detail":"Syntax violation for Avro artifact.","title":"Syntax violation for Avro artifact.","status":409,"name":"RuleViolationException"}`,
			wantType: "rule", wantCauses: 0, wantNotIs: ErrAlreadyExists, wantInMessage: []string{"Syntax violation"},
		},
		{
			name: "unprocessable schema 422", status: 422,
			body:     problem(422, "UnprocessableSchemaException", "Could not execute compatibility rule on invalid Avro schema"),
			wantType: "rule", wantInMessage: []string{"Could not execute compatibility rule"},
		},
		{
			name: "401 without credentials", status: 401,
			body:   problem(401, "UnauthorizedException", "User is not authenticated."),
			wantIs: ErrUnauthorized, wantInMessage: []string{"carried no credentials"},
		},
		{
			name: "401 with credentials", status: 401, opts: []Option{WithToken("bad")},
			body:   "",
			wantIs: ErrUnauthorized, wantInMessage: []string{"were rejected"},
		},
		{
			name: "403 with credentials", status: 403, opts: []Option{WithBasicAuth("u", "p")},
			body:   problem(403, "ForbiddenException", "User is not authorized to perform the requested operation."),
			wantIs: ErrForbidden, wantInMessage: []string{"User is not authorized", "lack the role"},
		},
		{
			name: "403 without credentials, empty body (3.0.15)", status: 403,
			body:   "",
			wantIs: ErrForbidden, wantInMessage: []string{"HTTP 403", "carried no credentials"},
		},
		{
			name: "403 RBAC not enabled keeps the server explanation only", status: 403, opts: []Option{WithToken("t")},
			body:   problem(403, "ForbiddenException", "Application RBAC not enabled."),
			wantIs: ErrForbidden, wantInMessage: []string{"Application RBAC not enabled."}, wantNotInMsg: []string{"lack the role", "carried no credentials"},
		},
		{
			name: "500 with garbage", status: 500,
			body:          "boom",
			wantInMessage: []string{"HTTP 500", "boom"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, mt := newTestClient(t, tc.opts...)
			mt.RegisterResponder(http.MethodGet, testURL+APIPath+"/probe", httpmock.NewStringResponder(tc.status, tc.body))
			var out map[string]any
			err := c.request(context.Background(), http.MethodGet, "/probe", nil, nil, &out, "probe")
			if err == nil {
				t.Fatal("want an error")
			}
			var ae *APIError
			if !errors.As(err, &ae) {
				t.Fatalf("error %T does not wrap *APIError: %v", err, err)
			}
			if ae.Status != tc.status {
				t.Errorf("Status = %d, want %d", ae.Status, tc.status)
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Errorf("errors.Is(%v) = false for %v", tc.wantIs, err)
			}
			if tc.wantNotIs != nil && errors.Is(err, tc.wantNotIs) {
				t.Errorf("errors.Is(%v) = true for %v", tc.wantNotIs, err)
			}
			switch tc.wantType {
			case "disabled":
				var de *OperationDisabledError
				if !errors.As(err, &de) {
					t.Fatalf("want *OperationDisabledError, got %T: %v", err, err)
				}
				if de.Operation != tc.wantKind || de.Property != tc.wantProperty {
					t.Errorf("Operation/Property = %q/%q, want %q/%q", de.Operation, de.Property, tc.wantKind, tc.wantProperty)
				}
			case "rule":
				var re *RuleViolationError
				if !errors.As(err, &re) {
					t.Fatalf("want *RuleViolationError, got %T: %v", err, err)
				}
				if len(re.Causes) != tc.wantCauses {
					t.Errorf("Causes = %d, want %d", len(re.Causes), tc.wantCauses)
				}
			default:
				var de *OperationDisabledError
				var re *RuleViolationError
				if errors.As(err, &de) || errors.As(err, &re) {
					t.Errorf("want a plain *APIError, got %T", err)
				}
			}
			for _, s := range tc.wantInMessage {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("message %q does not contain %q", err.Error(), s)
				}
			}
			for _, s := range tc.wantNotInMsg {
				if strings.Contains(err.Error(), s) {
					t.Errorf("message %q must not contain %q", err.Error(), s)
				}
			}
			if !strings.HasPrefix(err.Error(), "probe: ") {
				t.Errorf("message %q does not start with the operation", err.Error())
			}
		})
	}
}

func TestDecodeResponseMalformedJSON(t *testing.T) {
	t.Parallel()
	c, mt := newTestClient(t)
	mt.RegisterResponder(http.MethodGet, testURL+APIPath+"/system/info", httpmock.NewStringResponder(200, `{"version": `))
	_, err := c.GetSystemInfo(context.Background())
	if err == nil || !strings.Contains(err.Error(), "decode the 200 response") {
		t.Fatalf("want a decode error, got %v", err)
	}
}

func TestDecodeResponseTransportError(t *testing.T) {
	t.Parallel()
	c, mt := newTestClient(t)
	mt.RegisterResponder(http.MethodGet, testURL+APIPath+"/system/info", httpmock.NewErrorResponder(errors.New("connection refused")))
	_, err := c.GetSystemInfo(context.Background())
	if err == nil || !strings.Contains(err.Error(), "connection refused") || !strings.Contains(err.Error(), "read the registry system information") {
		t.Fatalf("want a wrapped transport error, got %v", err)
	}
}

func TestEnvName(t *testing.T) {
	t.Parallel()
	if got := envName("apicurio.rest.deletion.artifact-version.enabled"); got != "APICURIO_REST_DELETION_ARTIFACT_VERSION_ENABLED" {
		t.Errorf("envName = %q", got)
	}
}
