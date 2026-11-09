package client

import (
	"context"
	"errors"
	"net/http"
)

// Roles of a role mapping.
const (
	RoleReadOnly  = "READ_ONLY"
	RoleDeveloper = "DEVELOPER"
	RoleAdmin     = "ADMIN"
)

// RoleMapping assigns a registry role to a principal (the preferred username
// of a user or the name of a service account). Role mappings exist only on a
// registry with apicurio.auth.role-based-authorization=true and
// apicurio.auth.role-source=application; otherwise every call is a 403.
type RoleMapping struct {
	PrincipalID   string `json:"principalId"`
	Role          string `json:"role"`
	PrincipalName string `json:"principalName,omitempty"`
}

// CreateRoleMapping maps a principal; 409 RoleMappingAlreadyExistsException
// (ErrAlreadyExists) when the principal is mapped already.
func (c *Client) CreateRoleMapping(ctx context.Context, in RoleMapping) error {
	return c.request(ctx, http.MethodPost, "/admin/roleMappings", nil, in, nil, "create role mapping for "+quote(in.PrincipalID))
}

// GetRoleMapping reads the mapping of a principal; ErrNotFound when it has none.
func (c *Client) GetRoleMapping(ctx context.Context, principalID string) (*RoleMapping, error) {
	var out RoleMapping
	if err := c.request(ctx, http.MethodGet, "/admin/roleMappings/"+segment(principalID), nil, nil, &out, "read role mapping of "+quote(principalID)); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateRoleMapping changes the role of a mapped principal (the name cannot
// be changed); ErrNotFound when the principal is not mapped.
func (c *Client) UpdateRoleMapping(ctx context.Context, principalID, role string) error {
	body := struct {
		Role string `json:"role"`
	}{Role: role}
	return c.request(ctx, http.MethodPut, "/admin/roleMappings/"+segment(principalID), nil, body, nil, "update role mapping of "+quote(principalID))
}

// DeleteRoleMapping removes the mapping of a principal.
func (c *Client) DeleteRoleMapping(ctx context.Context, principalID string) error {
	return c.request(ctx, http.MethodDelete, "/admin/roleMappings/"+segment(principalID), nil, nil, nil, "delete role mapping of "+quote(principalID))
}

// ListRoleMappings returns every mapping.
func (c *Client) ListRoleMappings(ctx context.Context) ([]RoleMapping, error) {
	return listAll[RoleMapping](ctx, c, "/admin/roleMappings", nil, "roleMappings", "list role mappings")
}

// IsForbidden reports a 403: the registry refuses the caller (missing role),
// or, for role mappings, application RBAC is not enabled.
func IsForbidden(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusForbidden
}
