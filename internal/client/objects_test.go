package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/jarcoal/httpmock"
)

// capture records the last request a responder saw.
type capture struct {
	method, path, query, contentType string
	body                             map[string]any
}

func capturing(c *capture, status int, body string) httpmock.Responder {
	return func(r *http.Request) (*http.Response, error) {
		c.method, c.path, c.query, c.contentType = r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Content-Type")
		c.body = nil
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			if len(b) > 0 {
				_ = json.Unmarshal(b, &c.body)
			}
		}
		return httpmock.NewStringResponse(status, body), nil
	}
}

func TestGroupCRUD(t *testing.T) {
	t.Parallel()
	c, mt := newTestClient(t)
	ctx := context.Background()
	var got capture

	mt.RegisterResponder(http.MethodPost, testURL+APIPath+"/groups", capturing(&got, 200,
		`{"groupId":"payments","description":"d","labels":{"env":"prod"},"owner":"","createdOn":"2026-11-09T08:00:00Z","modifiedOn":"2026-11-09T08:00:00Z"}`))
	g, err := c.CreateGroup(ctx, GroupCreate{GroupID: "payments", Description: "d", Labels: map[string]string{"env": "prod"}})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if g.GroupID != "payments" || g.Labels["env"] != "prod" || got.body["groupId"] != "payments" {
		t.Errorf("create: got %+v, sent %v", g, got.body)
	}

	mt.RegisterResponder(http.MethodGet, testURL+APIPath+"/groups/pay%20ments", httpmock.NewStringResponder(200, `{"groupId":"pay ments"}`))
	if g, err = c.GetGroup(ctx, "pay ments"); err != nil || g.GroupID != "pay ments" {
		t.Errorf("GetGroup with a space: %v %+v", err, g)
	}

	mt.RegisterResponder(http.MethodPut, testURL+APIPath+"/groups/payments", capturing(&got, 204, ""))
	if err := c.UpdateGroup(ctx, "payments", GroupUpdate{Description: ""}); err != nil {
		t.Fatalf("UpdateGroup: %v", err)
	}
	// description is always sent (even empty) and nil labels become {} so the
	// server clears them instead of ignoring an absent property.
	if v, ok := got.body["description"]; !ok || v != "" {
		t.Errorf("description not sent as empty string: %v", got.body)
	}
	if labels, ok := got.body["labels"].(map[string]any); !ok || len(labels) != 0 {
		t.Errorf("labels not sent as an empty object: %v", got.body)
	}
	if _, ok := got.body["owner"]; ok {
		t.Errorf("empty owner must be omitted (the server refuses it): %v", got.body)
	}

	mt.RegisterResponder(http.MethodDelete, testURL+APIPath+"/groups/payments", httpmock.NewStringResponder(204, ""))
	if err := c.DeleteGroup(ctx, "payments"); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}

	mt.RegisterResponder(http.MethodPost, testURL+APIPath+"/groups", httpmock.NewStringResponder(409,
		problem(409, "GroupAlreadyExistsException", "Group 'payments' already exists.")))
	if _, err := c.CreateGroup(ctx, GroupCreate{GroupID: "payments"}); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("want ErrAlreadyExists, got %v", err)
	}
	mt.RegisterResponder(http.MethodDelete, testURL+APIPath+"/groups/locked", httpmock.NewStringResponder(405,
		problem(405, "NotAllowedException", "Group deletion operation is not enabled.")))
	var de *DeletionDisabledError
	if err := c.DeleteGroup(ctx, "locked"); !errors.As(err, &de) || de.Kind != "group" {
		t.Errorf("want DeletionDisabledError for group, got %v", err)
	}
}

func TestArtifactCRUDAndVersions(t *testing.T) {
	t.Parallel()
	c, mt := newTestClient(t)
	ctx := context.Background()
	var got capture
	base := testURL + APIPath + "/groups/g/artifacts"

	mt.RegisterResponder(http.MethodPost, base, capturing(&got, 200,
		`{"artifact":{"groupId":"g","artifactId":"a","artifactType":"AVRO"},"version":{"version":"1","globalId":7,"contentId":3,"state":"ENABLED","artifactType":"AVRO"}}`))
	resp, err := c.CreateArtifact(ctx, "g", ArtifactCreate{
		ArtifactID: "a", Name: "n",
		FirstVersion: &VersionCreate{Content: Content{Content: `{"type":"string"}`, ContentType: "application/json",
			References: []Reference{{GroupID: "g", ArtifactID: "b", Version: "1", Name: "x.B"}}}},
	}, CreateArtifactOptions{})
	if err != nil {
		t.Fatalf("CreateArtifact: %v", err)
	}
	if got.query != "ifExists=FAIL" {
		t.Errorf("query = %q, want ifExists=FAIL", got.query)
	}
	fv, _ := got.body["firstVersion"].(map[string]any)
	content, _ := fv["content"].(map[string]any)
	if content["contentType"] != "application/json" || content["content"] != `{"type":"string"}` {
		t.Errorf("firstVersion content not sent: %v", got.body)
	}
	if refs, _ := content["references"].([]any); len(refs) != 1 {
		t.Errorf("references not sent: %v", content)
	}
	if _, ok := got.body["artifactType"]; ok {
		t.Errorf("empty artifactType must be omitted so the server detects it: %v", got.body)
	}
	if resp.Version == nil || resp.Version.GlobalID != 7 || resp.Artifact.ArtifactType != "AVRO" {
		t.Errorf("response = %+v", resp)
	}

	mt.RegisterResponder(http.MethodPost, base, capturing(&got, 200, `{"artifact":{"groupId":"g","artifactId":"a"}}`))
	resp, err = c.CreateArtifact(ctx, "g", ArtifactCreate{ArtifactID: "a", ArtifactType: "AVRO"}, CreateArtifactOptions{IfExists: IfExistsCreateVersion, DryRun: true})
	if err != nil || resp.Version != nil {
		t.Errorf("create without first version: %v %+v", err, resp)
	}
	if got.query != "dryRun=true&ifExists=CREATE_VERSION" {
		t.Errorf("query = %q", got.query)
	}

	mt.RegisterResponder(http.MethodPut, base+"/a", capturing(&got, 204, ""))
	if err := c.UpdateArtifact(ctx, "g", "a", ArtifactUpdate{Name: "", Description: "d", Owner: "me"}); err != nil {
		t.Fatalf("UpdateArtifact: %v", err)
	}
	if got.body["name"] != "" || got.body["description"] != "d" || got.body["owner"] != "me" {
		t.Errorf("update body = %v", got.body)
	}
	if labels, ok := got.body["labels"].(map[string]any); !ok || len(labels) != 0 {
		t.Errorf("labels must be sent as {}: %v", got.body)
	}

	mt.RegisterResponder(http.MethodGet, base+"/a/versions/branch=latest", httpmock.NewStringResponder(200,
		`{"version":"2","globalId":9,"contentId":4,"state":"ENABLED","artifactType":"AVRO","groupId":"g","artifactId":"a"}`))
	v, err := c.GetVersion(ctx, "g", "a", BranchLatest)
	if err != nil || v.Version != "2" || v.GlobalID != 9 {
		t.Errorf("GetVersion latest: %v %+v", err, v)
	}

	mt.RegisterResponder(http.MethodGet, base+"/a/versions/2/content", func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Accept") != "*/*" {
			t.Errorf("content request Accept = %q, want */*", r.Header.Get("Accept"))
		}
		if r.URL.RawQuery != "references=DEREFERENCE" {
			t.Errorf("content query = %q", r.URL.RawQuery)
		}
		resp := httpmock.NewStringResponse(200, "syntax = \"proto3\";\r\n")
		resp.Header.Set("Content-Type", "application/x-protobuf")
		return resp, nil
	})
	vc, err := c.GetVersionContent(ctx, "g", "a", "2", ReferencesDereference)
	if err != nil || string(vc.Content) != "syntax = \"proto3\";\r\n" || vc.ContentType != "application/x-protobuf" {
		t.Errorf("GetVersionContent: %v %+v", err, vc)
	}

	mt.RegisterResponder(http.MethodGet, base+"/a/versions/2/references", httpmock.NewStringResponder(200,
		`[{"groupId":"g","artifactId":"b","version":"1","name":"x.B"}]`))
	refs, err := c.GetVersionReferences(ctx, "g", "a", "2")
	if err != nil || len(refs) != 1 || refs[0].Name != "x.B" {
		t.Errorf("GetVersionReferences: %v %+v", err, refs)
	}

	mt.RegisterResponder(http.MethodPost, base+"/a/versions", httpmock.NewStringResponder(400,
		`{"causes":[{"description":"vat","context":"/fields/3"}],"detail":"Incompatible artifact","title":"Incompatible artifact: a [AVRO]","status":400,"name":"RuleViolationException"}`))
	var rv *RuleViolationError
	_, err = c.CreateVersion(ctx, "g", "a", VersionCreate{Content: Content{Content: "{}", ContentType: "application/json"}}, false)
	if !errors.As(err, &rv) || len(rv.Causes) != 1 || rv.Causes[0].Context != "/fields/3" {
		t.Errorf("want RuleViolationError with a cause, got %v", err)
	}

	mt.RegisterResponder(http.MethodGet, base+"/missing", httpmock.NewStringResponder(404,
		problem(404, "ArtifactNotFoundException", "No artifact with ID 'missing' in group 'g' was found.")))
	if _, err := c.GetArtifact(ctx, "g", "missing"); !IsNotFound(err) {
		t.Errorf("want ErrNotFound, got %v", err)
	}

	mt.RegisterResponder(http.MethodGet, base+"/a/versions/branch=latest", httpmock.NewStringResponder(404,
		problem(404, "VersionNotFoundException", "No version '<tip of the branch 'latest'>' found for artifact with ID 'a' in group 'g'.")))
	if _, err := c.GetVersion(ctx, "g", "a", BranchLatest); !IsNotFound(err) {
		t.Errorf("artifact without versions must be ErrNotFound, got %v", err)
	}

	mt.RegisterResponder(http.MethodDelete, base+"/a", httpmock.NewStringResponder(405,
		problem(405, "NotAllowedException", "Artifact deletion operation is not enabled.")))
	var de *DeletionDisabledError
	if err := c.DeleteArtifact(ctx, "g", "a"); !errors.As(err, &de) || de.Property != PropertyArtifactDeletion {
		t.Errorf("want DeletionDisabledError for artifact, got %v", err)
	}
}

func TestListArtifactTypes(t *testing.T) {
	t.Parallel()
	c, mt := newTestClient(t)
	mt.RegisterResponder(http.MethodGet, testURL+APIPath+"/admin/config/artifactTypes", httpmock.NewStringResponder(200,
		`[{"name":"PROTOBUF"},{"name":"AVRO"}]`))
	got, err := c.ListArtifactTypes(context.Background())
	if err != nil || len(got) != 2 || got[0] != "PROTOBUF" || got[1] != "AVRO" {
		t.Fatalf("ListArtifactTypes: %v %v", err, got)
	}
}

func TestRules(t *testing.T) {
	t.Parallel()
	c, mt := newTestClient(t)
	ctx := context.Background()
	var got capture
	tests := []struct {
		scope RuleScope
		path  string
	}{
		{RuleScope{}, "/admin/rules"},
		{RuleScope{GroupID: "g"}, "/groups/g/rules"},
		{RuleScope{GroupID: "g", ArtifactID: "a/b"}, "/groups/g/artifacts/a%2Fb/rules"},
	}
	for _, tc := range tests {
		mt.RegisterResponder(http.MethodPost, testURL+APIPath+tc.path, capturing(&got, 204, ""))
		if err := c.CreateRule(ctx, tc.scope, Rule{RuleType: RuleCompatibility, Config: "BACKWARD"}); err != nil {
			t.Fatalf("CreateRule %s: %v", tc.path, err)
		}
		if got.body["ruleType"] != "COMPATIBILITY" || got.body["config"] != "BACKWARD" {
			t.Errorf("rule body = %v", got.body)
		}
		mt.RegisterResponder(http.MethodGet, testURL+APIPath+tc.path+"/COMPATIBILITY", httpmock.NewStringResponder(200, `{"ruleType":"COMPATIBILITY","config":"BACKWARD"}`))
		r, err := c.GetRule(ctx, tc.scope, RuleCompatibility)
		if err != nil || r.Config != "BACKWARD" {
			t.Errorf("GetRule %s: %v %+v", tc.path, err, r)
		}
		mt.RegisterResponder(http.MethodPut, testURL+APIPath+tc.path+"/COMPATIBILITY", httpmock.NewStringResponder(200, `{"ruleType":"COMPATIBILITY","config":"FULL"}`))
		if r, err = c.UpdateRule(ctx, tc.scope, Rule{RuleType: RuleCompatibility, Config: "FULL"}); err != nil || r.Config != "FULL" {
			t.Errorf("UpdateRule %s: %v %+v", tc.path, err, r)
		}
		mt.RegisterResponder(http.MethodGet, testURL+APIPath+tc.path, httpmock.NewStringResponder(200, `["COMPATIBILITY"]`))
		if list, err := c.ListRules(ctx, tc.scope); err != nil || len(list) != 1 {
			t.Errorf("ListRules %s: %v %v", tc.path, err, list)
		}
		mt.RegisterResponder(http.MethodDelete, testURL+APIPath+tc.path+"/COMPATIBILITY", httpmock.NewStringResponder(204, ""))
		if err := c.DeleteRule(ctx, tc.scope, RuleCompatibility); err != nil {
			t.Errorf("DeleteRule %s: %v", tc.path, err)
		}
	}
	mt.RegisterResponder(http.MethodGet, testURL+APIPath+"/admin/rules/VALIDITY", httpmock.NewStringResponder(404,
		problem(404, "RuleNotFoundException", "No rule named 'VALIDITY' was found.")))
	if _, err := c.GetRule(ctx, RuleScope{}, RuleValidity); !IsNotFound(err) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestConfigProperties(t *testing.T) {
	t.Parallel()
	c, mt := newTestClient(t)
	ctx := context.Background()
	var got capture
	mt.RegisterResponder(http.MethodGet, testURL+APIPath+"/admin/config/properties", httpmock.NewStringResponder(200,
		`[{"name":"apicurio.rest.deletion.group.enabled","value":"true","type":"java.lang.Boolean","label":"Delete group"}]`))
	list, err := c.ListConfigProperties(ctx)
	if err != nil || len(list) != 1 || list[0].Type != "java.lang.Boolean" {
		t.Fatalf("ListConfigProperties: %v %+v", err, list)
	}
	mt.RegisterResponder(http.MethodPut, testURL+APIPath+"/admin/config/properties/"+PropertyGroupDeletion, capturing(&got, 204, ""))
	if err := c.SetConfigProperty(ctx, PropertyGroupDeletion, "false"); err != nil || got.body["value"] != "false" {
		t.Errorf("SetConfigProperty: %v %v", err, got.body)
	}
	mt.RegisterResponder(http.MethodGet, testURL+APIPath+"/admin/config/properties/"+PropertyGroupDeletion, httpmock.NewStringResponder(200,
		`{"name":"apicurio.rest.deletion.group.enabled","value":"false","type":"java.lang.Boolean"}`))
	if p, err := c.GetConfigProperty(ctx, PropertyGroupDeletion); err != nil || p.Value != "false" {
		t.Errorf("GetConfigProperty: %v %+v", err, p)
	}
	mt.RegisterResponder(http.MethodDelete, testURL+APIPath+"/admin/config/properties/"+PropertyGroupDeletion, httpmock.NewStringResponder(204, ""))
	if err := c.ResetConfigProperty(ctx, PropertyGroupDeletion); err != nil {
		t.Errorf("ResetConfigProperty: %v", err)
	}
	mt.RegisterResponder(http.MethodGet, testURL+APIPath+"/admin/config/properties/no.such", httpmock.NewStringResponder(404,
		problem(404, "ConfigPropertyNotFoundException", "No configuration property named 'no.such' was found.")))
	if _, err := c.GetConfigProperty(ctx, "no.such"); !IsNotFound(err) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestVersionSegmentKeepsBranchExpression(t *testing.T) {
	t.Parallel()
	if got := versionSegment("branch=latest"); got != "branch=latest" {
		t.Errorf("versionSegment(branch=latest) = %q", got)
	}
	if got := versionSegment("branch=rel 1"); got != "branch=rel%201" {
		t.Errorf("versionSegment(branch=rel 1) = %q", got)
	}
	if got := versionSegment("1.0.0"); got != "1.0.0" {
		t.Errorf("versionSegment(1.0.0) = %q", got)
	}
}
