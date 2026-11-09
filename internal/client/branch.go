package client

import (
	"context"
	"net/http"
)

// System branches the registry maintains itself.
const (
	BranchIDLatest = "latest"
	BranchIDDrafts = "drafts"
)

// Branch is the metadata of a branch (BranchMetaData). GroupID and
// ArtifactID are absent on 3.0.x answers.
type Branch struct {
	GroupID       string `json:"groupId,omitempty"`
	ArtifactID    string `json:"artifactId,omitempty"`
	BranchID      string `json:"branchId"`
	Description   string `json:"description,omitempty"`
	SystemDefined bool   `json:"systemDefined"`
	Owner         string `json:"owner,omitempty"`
	CreatedOn     string `json:"createdOn,omitempty"`
	ModifiedBy    string `json:"modifiedBy,omitempty"`
	ModifiedOn    string `json:"modifiedOn,omitempty"`
}

// BranchCreate is the body of POST .../branches.
type BranchCreate struct {
	BranchID    string   `json:"branchId"`
	Description string   `json:"description,omitempty"`
	Versions    []string `json:"versions,omitempty"`
}

func branchPath(groupID, artifactID, branchID string) string {
	return artifactPath(groupID, artifactID) + "/branches/" + segment(branchID)
}

// CreateBranch creates a branch, with its initial versions in the given
// order. An existing id (the system branches included) is 409 ErrAlreadyExists;
// a version that does not exist is a 500 from the registry, so callers check
// the versions first.
func (c *Client) CreateBranch(ctx context.Context, groupID, artifactID string, in BranchCreate) (*Branch, error) {
	var out Branch
	op := "create branch " + quote(in.BranchID) + " of artifact " + quote(groupID+"/"+artifactID)
	if err := c.request(ctx, http.MethodPost, artifactPath(groupID, artifactID)+"/branches", nil, in, &out, op); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetBranch reads a branch; ErrNotFound when the branch, artifact or group is missing.
func (c *Client) GetBranch(ctx context.Context, groupID, artifactID, branchID string) (*Branch, error) {
	var out Branch
	op := "read branch " + quote(branchID) + " of artifact " + quote(groupID+"/"+artifactID)
	if err := c.request(ctx, http.MethodGet, branchPath(groupID, artifactID, branchID), nil, nil, &out, op); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateBranch changes the description of a branch.
func (c *Client) UpdateBranch(ctx context.Context, groupID, artifactID, branchID, description string) error {
	body := struct {
		Description string `json:"description"`
	}{Description: description}
	op := "update branch " + quote(branchID) + " of artifact " + quote(groupID+"/"+artifactID)
	return c.request(ctx, http.MethodPut, branchPath(groupID, artifactID, branchID), nil, body, nil, op)
}

// ReplaceBranchVersions replaces the whole version list of a branch (an
// empty list empties it). The registry lists the versions back newest-added
// first, i.e. in the reverse of this order.
func (c *Client) ReplaceBranchVersions(ctx context.Context, groupID, artifactID, branchID string, versions []string) error {
	if versions == nil {
		versions = []string{}
	}
	body := struct {
		Versions []string `json:"versions"`
	}{Versions: versions}
	op := "replace the versions of branch " + quote(branchID) + " of artifact " + quote(groupID+"/"+artifactID)
	return c.request(ctx, http.MethodPut, branchPath(groupID, artifactID, branchID)+"/versions", nil, body, nil, op)
}

// ListBranchVersions returns the versions of a branch as the registry lists
// them: newest-added first.
func (c *Client) ListBranchVersions(ctx context.Context, groupID, artifactID, branchID string) ([]Version, error) {
	op := "list the versions of branch " + quote(branchID) + " of artifact " + quote(groupID+"/"+artifactID)
	return listAll[Version](ctx, c, branchPath(groupID, artifactID, branchID)+"/versions", nil, "versions", op)
}

// ListBranches returns every branch of an artifact, system branches included.
func (c *Client) ListBranches(ctx context.Context, groupID, artifactID string) ([]Branch, error) {
	return listAll[Branch](ctx, c, artifactPath(groupID, artifactID)+"/branches", nil, "branches", "list branches of artifact "+quote(groupID+"/"+artifactID))
}

// DeleteBranch deletes a branch; a system branch answers 409
// NotAllowedException ("System generated branches cannot be deleted.").
func (c *Client) DeleteBranch(ctx context.Context, groupID, artifactID, branchID string) error {
	op := "delete branch " + quote(branchID) + " of artifact " + quote(groupID+"/"+artifactID)
	return c.request(ctx, http.MethodDelete, branchPath(groupID, artifactID, branchID), nil, nil, nil, op)
}
