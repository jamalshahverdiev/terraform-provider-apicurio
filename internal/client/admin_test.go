package client

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/jarcoal/httpmock"
)

func TestRoleMappings(t *testing.T) {
	t.Parallel()
	c, mt := newTestClient(t)
	ctx := context.Background()
	var got capture
	base := testURL + APIPath + "/admin/roleMappings"

	mt.RegisterResponder(http.MethodPost, base, capturing(&got, 204, ""))
	if err := c.CreateRoleMapping(ctx, RoleMapping{PrincipalID: "alice", Role: RoleDeveloper, PrincipalName: "Alice"}); err != nil {
		t.Fatalf("CreateRoleMapping: %v", err)
	}
	if got.body["principalId"] != "alice" || got.body["role"] != "DEVELOPER" || got.body["principalName"] != "Alice" {
		t.Errorf("body = %v", got.body)
	}
	mt.RegisterResponder(http.MethodPost, base, capturing(&got, 204, ""))
	_ = c.CreateRoleMapping(ctx, RoleMapping{PrincipalID: "bob", Role: RoleReadOnly})
	if _, ok := got.body["principalName"]; ok {
		t.Errorf("an empty principalName must be omitted: %v", got.body)
	}

	mt.RegisterResponder(http.MethodPost, base, httpmock.NewStringResponder(409,
		problem(409, "RoleMappingAlreadyExistsException", "A mapping for principal 'alice' and role 'READ_ONLY' already exists.")))
	if err := c.CreateRoleMapping(ctx, RoleMapping{PrincipalID: "alice", Role: RoleReadOnly}); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("want ErrAlreadyExists, got %v", err)
	}

	mt.RegisterResponder(http.MethodGet, base+"/alice", httpmock.NewStringResponder(200, `{"principalId":"alice","role":"DEVELOPER","principalName":"Alice"}`))
	m, err := c.GetRoleMapping(ctx, "alice")
	if err != nil || m.Role != RoleDeveloper || m.PrincipalName != "Alice" {
		t.Errorf("GetRoleMapping: %v %+v", err, m)
	}
	mt.RegisterResponder(http.MethodGet, base+"/nobody", httpmock.NewStringResponder(404,
		problem(404, "RoleMappingNotFoundException", "No role mapping for principal 'nobody' was found.")))
	if _, err := c.GetRoleMapping(ctx, "nobody"); !IsNotFound(err) {
		t.Errorf("want ErrNotFound, got %v", err)
	}

	mt.RegisterResponder(http.MethodPut, base+"/alice", capturing(&got, 204, ""))
	if err := c.UpdateRoleMapping(ctx, "alice", RoleAdmin); err != nil || got.body["role"] != "ADMIN" || len(got.body) != 1 {
		t.Errorf("UpdateRoleMapping: %v %v", err, got.body)
	}

	mt.RegisterResponder(http.MethodDelete, base+"/alice", httpmock.NewStringResponder(204, ""))
	if err := c.DeleteRoleMapping(ctx, "alice"); err != nil {
		t.Errorf("DeleteRoleMapping: %v", err)
	}

	mt.RegisterResponder(http.MethodGet, base, httpmock.NewStringResponder(200,
		`{"roleMappings":[{"principalId":"alice","role":"ADMIN"},{"principalId":"bob","role":"READ_ONLY"}],"count":2}`))
	list, err := c.ListRoleMappings(ctx)
	if err != nil || len(list) != 2 || list[1].PrincipalID != "bob" {
		t.Errorf("ListRoleMappings: %v %+v", err, list)
	}

	// the open registry: application RBAC off (3.0.15 sends an empty body)
	mt.RegisterResponder(http.MethodGet, base+"/x", httpmock.NewStringResponder(403, ""))
	_, err = c.GetRoleMapping(ctx, "x")
	if !IsForbidden(err) || IsNotFound(err) {
		t.Errorf("want a 403 recognised by IsForbidden, got %v", err)
	}
	mt.RegisterResponder(http.MethodGet, base+"/y", httpmock.NewStringResponder(403,
		problem(403, "ForbiddenException", "Application RBAC not enabled.")))
	if _, err := c.GetRoleMapping(ctx, "y"); !IsForbidden(err) {
		t.Errorf("want IsForbidden, got %v", err)
	}
	if IsForbidden(errors.New("plain")) {
		t.Error("IsForbidden must be false for a plain error")
	}
}
