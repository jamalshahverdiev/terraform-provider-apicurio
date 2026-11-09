package client

import (
	"context"
	"net/http"
)

// Group is the metadata of a group (GroupMetaData).
type Group struct {
	GroupID     string            `json:"groupId"`
	Description string            `json:"description,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Owner       string            `json:"owner,omitempty"`
	CreatedOn   string            `json:"createdOn,omitempty"`
	ModifiedBy  string            `json:"modifiedBy,omitempty"`
	ModifiedOn  string            `json:"modifiedOn,omitempty"`
}

// GroupCreate is the body of POST /groups.
type GroupCreate struct {
	GroupID     string            `json:"groupId"`
	Description string            `json:"description,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

// GroupUpdate is the body of PUT /groups/{g} (EditableGroupMetaData).
//
// The registry changes only the properties present in the body and treats
// null as absent, except on 3.0.15 where the group update replaces the whole
// editable set. The client therefore always sends description and labels
// (an empty string and an empty map clear them) and sends owner only when it
// is set, because the server refuses an empty owner.
type GroupUpdate struct {
	Description string            `json:"description"`
	Labels      map[string]string `json:"labels"`
	Owner       string            `json:"owner,omitempty"`
}

// CreateGroup creates a group. The id `default` is reserved (400) and an
// existing id answers 409 (ErrAlreadyExists).
func (c *Client) CreateGroup(ctx context.Context, in GroupCreate) (*Group, error) {
	var out Group
	if err := c.request(ctx, http.MethodPost, "/groups", nil, in, &out, "create group "+quote(in.GroupID)); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetGroup reads a group; a missing group is ErrNotFound.
func (c *Client) GetGroup(ctx context.Context, groupID string) (*Group, error) {
	var out Group
	if err := c.request(ctx, http.MethodGet, "/groups/"+segment(groupID), nil, nil, &out, "read group "+quote(groupID)); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateGroup changes the editable metadata of a group.
func (c *Client) UpdateGroup(ctx context.Context, groupID string, in GroupUpdate) error {
	if in.Labels == nil {
		in.Labels = map[string]string{}
	}
	return c.request(ctx, http.MethodPut, "/groups/"+segment(groupID), nil, in, nil, "update group "+quote(groupID))
}

// DeleteGroup deletes a group and every artifact in it. A registry with
// `apicurio.rest.deletion.group.enabled=false` answers 405
// (*DeletionDisabledError); a missing group is ErrNotFound.
func (c *Client) DeleteGroup(ctx context.Context, groupID string) error {
	return c.request(ctx, http.MethodDelete, "/groups/"+segment(groupID), nil, nil, nil, "delete group "+quote(groupID))
}

// ListGroups returns every group.
func (c *Client) ListGroups(ctx context.Context) ([]Group, error) {
	return listAll[Group](ctx, c, "/groups", nil, "groups", "list groups")
}

func quote(s string) string { return `"` + s + `"` }
