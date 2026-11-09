package client

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/jarcoal/httpmock"
)

func TestVersionUpdates(t *testing.T) {
	t.Parallel()
	c, mt := newTestClient(t)
	ctx := context.Background()
	var got capture
	base := testURL + APIPath + "/groups/g/artifacts/a/versions/1.0.0"

	mt.RegisterResponder(http.MethodPut, base, capturing(&got, 204, ""))
	if err := c.UpdateVersion(ctx, "g", "a", "1.0.0", VersionUpdate{Name: "n"}); err != nil {
		t.Fatalf("UpdateVersion: %v", err)
	}
	if got.body["name"] != "n" || got.body["description"] != "" {
		t.Errorf("body = %v, want name and an empty description", got.body)
	}
	if labels, ok := got.body["labels"].(map[string]any); !ok || len(labels) != 0 {
		t.Errorf("labels must be sent as {}: %v", got.body)
	}

	mt.RegisterResponder(http.MethodPut, base+"/state", capturing(&got, 204, ""))
	if err := c.UpdateVersionState(ctx, "g", "a", "1.0.0", StateDeprecated, true); err != nil {
		t.Fatalf("UpdateVersionState: %v", err)
	}
	if got.body["state"] != "DEPRECATED" || got.query != "dryRun=true" {
		t.Errorf("state body/query = %v %q", got.body, got.query)
	}

	mt.RegisterResponder(http.MethodPut, base+"/state", httpmock.NewStringResponder(400,
		problem(400, "BadRequestException", "Illegal state transition: cannot transition to DRAFT state.")))
	var ae *APIError
	if err := c.UpdateVersionState(ctx, "g", "a", "1.0.0", StateDraft, false); !errors.As(err, &ae) || ae.Status != 400 {
		t.Errorf("want a 400 APIError, got %v", err)
	}

	mt.RegisterResponder(http.MethodPut, base+"/content", capturing(&got, 204, ""))
	if err := c.UpdateVersionContent(ctx, "g", "a", "1.0.0", Content{Content: "{}", ContentType: "application/json"}); err != nil {
		t.Fatalf("UpdateVersionContent: %v", err)
	}
	if got.body["content"] != "{}" || got.body["contentType"] != "application/json" {
		t.Errorf("content body = %v", got.body)
	}

	mt.RegisterResponder(http.MethodPut, base+"/content", httpmock.NewStringResponder(405,
		problem(405, "NotAllowedException", "Artifact version content update operation is not enabled.")))
	var de *OperationDisabledError
	if err := c.UpdateVersionContent(ctx, "g", "a", "1.0.0", Content{Content: "{}"}); !errors.As(err, &de) || de.Property != PropertyContentMutability {
		t.Errorf("want OperationDisabledError for the content update, got %v", err)
	}

	mt.RegisterResponder(http.MethodPut, base+"/content", httpmock.NewStringResponder(409,
		problem(409, "ConflictException", "Requested artifact version is not in DRAFT state.  Update disallowed.")))
	if err := c.UpdateVersionContent(ctx, "g", "a", "1.0.0", Content{Content: "{}"}); !errors.As(err, &ae) || ae.Status != 409 || errors.Is(err, ErrAlreadyExists) {
		t.Errorf("want a plain 409 APIError, got %v", err)
	}
}

func TestBranches(t *testing.T) {
	t.Parallel()
	c, mt := newTestClient(t)
	ctx := context.Background()
	var got capture
	base := testURL + APIPath + "/groups/g/artifacts/a/branches"

	mt.RegisterResponder(http.MethodPost, base, capturing(&got, 200,
		`{"groupId":"g","artifactId":"a","branchId":"rel","description":"d","systemDefined":false,"createdOn":"2026-11-09T08:00:00Z"}`))
	b, err := c.CreateBranch(ctx, "g", "a", BranchCreate{BranchID: "rel", Description: "d", Versions: []string{"3", "1"}})
	if err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if b.BranchID != "rel" || b.SystemDefined {
		t.Errorf("branch = %+v", b)
	}
	if vs, _ := got.body["versions"].([]any); len(vs) != 2 || vs[0] != "3" {
		t.Errorf("versions sent = %v", got.body)
	}

	mt.RegisterResponder(http.MethodGet, base+"/rel", httpmock.NewStringResponder(200, `{"branchId":"rel","systemDefined":false}`))
	if b, err = c.GetBranch(ctx, "g", "a", "rel"); err != nil || b.BranchID != "rel" {
		t.Errorf("GetBranch (3.0 shape without group/artifact ids): %v %+v", err, b)
	}

	mt.RegisterResponder(http.MethodPut, base+"/rel", capturing(&got, 204, ""))
	if err := c.UpdateBranch(ctx, "g", "a", "rel", ""); err != nil || got.body["description"] != "" {
		t.Errorf("UpdateBranch: %v %v", err, got.body)
	}

	mt.RegisterResponder(http.MethodPut, base+"/rel/versions", capturing(&got, 204, ""))
	if err := c.ReplaceBranchVersions(ctx, "g", "a", "rel", nil); err != nil {
		t.Fatalf("ReplaceBranchVersions: %v", err)
	}
	if vs, ok := got.body["versions"].([]any); !ok || len(vs) != 0 {
		t.Errorf("an empty list must be sent as {\"versions\":[]}: %v", got.body)
	}

	mt.RegisterResponder(http.MethodGet, base+"/rel/versions", httpmock.NewStringResponder(200,
		`{"count":2,"versions":[{"version":"1","state":"ENABLED"},{"version":"3","state":"ENABLED"}]}`))
	vs, err := c.ListBranchVersions(ctx, "g", "a", "rel")
	if err != nil || len(vs) != 2 || vs[0].Version != "1" {
		t.Errorf("ListBranchVersions: %v %+v", err, vs)
	}

	mt.RegisterResponder(http.MethodGet, base, httpmock.NewStringResponder(200,
		`{"branches":[{"branchId":"latest","systemDefined":true},{"branchId":"rel","systemDefined":false}],"count":2}`))
	bs, err := c.ListBranches(ctx, "g", "a")
	if err != nil || len(bs) != 2 || !bs[0].SystemDefined {
		t.Errorf("ListBranches: %v %+v", err, bs)
	}

	mt.RegisterResponder(http.MethodDelete, base+"/latest", httpmock.NewStringResponder(409,
		problem(409, "NotAllowedException", "System generated branches cannot be deleted.")))
	err = c.DeleteBranch(ctx, "g", "a", "latest")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 409 || errors.Is(err, ErrAlreadyExists) {
		t.Errorf("want a plain 409 for a system branch, got %v", err)
	}

	mt.RegisterResponder(http.MethodGet, base+"/nope", httpmock.NewStringResponder(404,
		problem(404, "BranchNotFoundException", "No branch 'nope' was found in g/a.")))
	if _, err := c.GetBranch(ctx, "g", "a", "nope"); !IsNotFound(err) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}
