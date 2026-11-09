package client

import (
	"context"
	"net/http"
	"net/url"
)

// Values of the ifExists parameter of CreateArtifact.
const (
	IfExistsFail                = "FAIL"
	IfExistsCreateVersion       = "CREATE_VERSION"
	IfExistsFindOrCreateVersion = "FIND_OR_CREATE_VERSION"
)

// Values of the references parameter of GetVersionContent.
const (
	ReferencesPreserve    = "PRESERVE"
	ReferencesDereference = "DEREFERENCE"
	ReferencesRewrite     = "REWRITE"
)

// BranchLatest is the version expression of the newest version of an artifact.
const BranchLatest = "branch=latest"

// Artifact is the metadata of an artifact (ArtifactMetaData).
type Artifact struct {
	GroupID      string            `json:"groupId"`
	ArtifactID   string            `json:"artifactId"`
	ArtifactType string            `json:"artifactType,omitempty"`
	Name         string            `json:"name,omitempty"`
	Description  string            `json:"description,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	Owner        string            `json:"owner,omitempty"`
	CreatedOn    string            `json:"createdOn,omitempty"`
	ModifiedBy   string            `json:"modifiedBy,omitempty"`
	ModifiedOn   string            `json:"modifiedOn,omitempty"`
}

// Version is the metadata of an artifact version (VersionMetaData).
type Version struct {
	GroupID      string            `json:"groupId"`
	ArtifactID   string            `json:"artifactId"`
	Version      string            `json:"version"`
	GlobalID     int64             `json:"globalId"`
	ContentID    int64             `json:"contentId"`
	State        string            `json:"state,omitempty"`
	ArtifactType string            `json:"artifactType,omitempty"`
	Name         string            `json:"name,omitempty"`
	Description  string            `json:"description,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	Owner        string            `json:"owner,omitempty"`
	CreatedOn    string            `json:"createdOn,omitempty"`
	ModifiedBy   string            `json:"modifiedBy,omitempty"`
	ModifiedOn   string            `json:"modifiedOn,omitempty"`
}

// Reference points a version at another artifact version by name.
type Reference struct {
	GroupID    string `json:"groupId"`
	ArtifactID string `json:"artifactId"`
	Version    string `json:"version"`
	Name       string `json:"name"`
}

// Content is the content of a version as sent to the registry.
type Content struct {
	Content     string      `json:"content"`
	ContentType string      `json:"contentType"`
	References  []Reference `json:"references,omitempty"`
}

// VersionCreate is the body of POST .../versions and the firstVersion of
// ArtifactCreate.
type VersionCreate struct {
	Version     string            `json:"version,omitempty"`
	Content     Content           `json:"content"`
	Name        string            `json:"name,omitempty"`
	Description string            `json:"description,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	IsDraft     bool              `json:"isDraft,omitempty"`
	Branches    []string          `json:"branches,omitempty"`
}

// ArtifactCreate is the body of POST /groups/{g}/artifacts.
type ArtifactCreate struct {
	ArtifactID   string            `json:"artifactId"`
	ArtifactType string            `json:"artifactType,omitempty"`
	Name         string            `json:"name,omitempty"`
	Description  string            `json:"description,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	FirstVersion *VersionCreate    `json:"firstVersion,omitempty"`
}

// ArtifactCreateResponse is the answer of POST /groups/{g}/artifacts; Version
// is nil when no first version was sent.
type ArtifactCreateResponse struct {
	Artifact Artifact `json:"artifact"`
	Version  *Version `json:"version,omitempty"`
}

// ArtifactUpdate is the body of PUT /groups/{g}/artifacts/{a}
// (EditableArtifactMetaData). See GroupUpdate for the semantics.
type ArtifactUpdate struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Labels      map[string]string `json:"labels"`
	Owner       string            `json:"owner,omitempty"`
}

// VersionContent is the stored content of a version.
type VersionContent struct {
	Content     []byte
	ContentType string
}

// CreateArtifactOptions are the query parameters of CreateArtifact.
type CreateArtifactOptions struct {
	IfExists string // IfExistsFail when empty
	DryRun   bool
}

// CreateArtifact creates an artifact, with its first version when
// in.FirstVersion is set. Errors: 409 ErrAlreadyExists when the artifact
// exists and IfExists is FAIL; 404 GroupNotFoundException (ErrNotFound) when
// the group is missing and automatic group creation is off; a
// *RuleViolationError when a rule refuses the content; 400
// InvalidArtifactTypeException when the type cannot be discovered from the
// content (the content type hint matters: application/x-protobuf,
// application/x-yaml, application/graphql, application/xml).
func (c *Client) CreateArtifact(ctx context.Context, groupID string, in ArtifactCreate, opts CreateArtifactOptions) (*ArtifactCreateResponse, error) {
	q := url.Values{}
	if opts.IfExists == "" {
		opts.IfExists = IfExistsFail
	}
	q.Set("ifExists", opts.IfExists)
	if opts.DryRun {
		q.Set("dryRun", "true")
	}
	var out ArtifactCreateResponse
	op := "create artifact " + quote(groupID+"/"+in.ArtifactID)
	if err := c.request(ctx, http.MethodPost, "/groups/"+segment(groupID)+"/artifacts", q, in, &out, op); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetArtifact reads the metadata of an artifact; ErrNotFound when the
// artifact or its group is missing.
func (c *Client) GetArtifact(ctx context.Context, groupID, artifactID string) (*Artifact, error) {
	var out Artifact
	if err := c.request(ctx, http.MethodGet, artifactPath(groupID, artifactID), nil, nil, &out, "read artifact "+quote(groupID+"/"+artifactID)); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateArtifact changes the editable metadata of an artifact.
func (c *Client) UpdateArtifact(ctx context.Context, groupID, artifactID string, in ArtifactUpdate) error {
	if in.Labels == nil {
		in.Labels = map[string]string{}
	}
	return c.request(ctx, http.MethodPut, artifactPath(groupID, artifactID), nil, in, nil, "update artifact "+quote(groupID+"/"+artifactID))
}

// DeleteArtifact deletes an artifact and all its versions; 405 when
// `apicurio.rest.deletion.artifact.enabled` is false (*DeletionDisabledError).
func (c *Client) DeleteArtifact(ctx context.Context, groupID, artifactID string) error {
	return c.request(ctx, http.MethodDelete, artifactPath(groupID, artifactID), nil, nil, nil, "delete artifact "+quote(groupID+"/"+artifactID))
}

// ListArtifacts returns every artifact of a group (ErrNotFound for a missing
// group).
func (c *Client) ListArtifacts(ctx context.Context, groupID string) ([]Artifact, error) {
	return listAll[Artifact](ctx, c, "/groups/"+segment(groupID)+"/artifacts", nil, "artifacts", "list artifacts of group "+quote(groupID))
}

// ListArtifactTypes returns the artifact type names the registry supports
// (GET /admin/config/artifactTypes).
func (c *Client) ListArtifactTypes(ctx context.Context) ([]string, error) {
	var out []struct {
		Name string `json:"name"`
	}
	if err := c.request(ctx, http.MethodGet, "/admin/config/artifactTypes", nil, nil, &out, "list artifact types"); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(out))
	for _, t := range out {
		names = append(names, t.Name)
	}
	return names, nil
}

// CreateVersion adds a version to an artifact. With dryRun the registry runs
// the rules and returns the metadata it would create without storing it.
func (c *Client) CreateVersion(ctx context.Context, groupID, artifactID string, in VersionCreate, dryRun bool) (*Version, error) {
	var q url.Values
	if dryRun {
		q = url.Values{"dryRun": {"true"}}
	}
	var out Version
	op := "create version of artifact " + quote(groupID+"/"+artifactID)
	if err := c.request(ctx, http.MethodPost, artifactPath(groupID, artifactID)+"/versions", q, in, &out, op); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetVersion reads the metadata of a version. expression is a version id or
// a `branch=<id>` expression (BranchLatest for the newest version). An
// artifact without versions answers ErrNotFound for BranchLatest.
func (c *Client) GetVersion(ctx context.Context, groupID, artifactID, expression string) (*Version, error) {
	var out Version
	op := "read version " + quote(expression) + " of artifact " + quote(groupID+"/"+artifactID)
	if err := c.request(ctx, http.MethodGet, versionPath(groupID, artifactID, expression), nil, nil, &out, op); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetVersionContent reads the stored bytes of a version and their content
// type. references is one of the References* constants ("" for the server
// default, PRESERVE).
func (c *Client) GetVersionContent(ctx context.Context, groupID, artifactID, expression, references string) (*VersionContent, error) {
	var q url.Values
	if references != "" {
		q = url.Values{"references": {references}}
	}
	op := "read content of version " + quote(expression) + " of artifact " + quote(groupID+"/"+artifactID)
	body, ct, err := c.getRaw(ctx, versionPath(groupID, artifactID, expression)+"/content", q, op)
	if err != nil {
		return nil, err
	}
	return &VersionContent{Content: body, ContentType: ct}, nil
}

// GetVersionReferences lists the references of a version.
func (c *Client) GetVersionReferences(ctx context.Context, groupID, artifactID, expression string) ([]Reference, error) {
	var out []Reference
	op := "read references of version " + quote(expression) + " of artifact " + quote(groupID+"/"+artifactID)
	if err := c.request(ctx, http.MethodGet, versionPath(groupID, artifactID, expression)+"/references", nil, nil, &out, op); err != nil {
		return nil, err
	}
	return out, nil
}

// ListVersions returns every version of an artifact in creation order.
func (c *Client) ListVersions(ctx context.Context, groupID, artifactID string) ([]Version, error) {
	return listAll[Version](ctx, c, artifactPath(groupID, artifactID)+"/versions", nil, "versions", "list versions of artifact "+quote(groupID+"/"+artifactID))
}

// DeleteVersion deletes one version; 405 when
// `apicurio.rest.deletion.artifact-version.enabled` is false. The artifact
// survives the deletion of its last version.
func (c *Client) DeleteVersion(ctx context.Context, groupID, artifactID, version string) error {
	op := "delete version " + quote(version) + " of artifact " + quote(groupID+"/"+artifactID)
	return c.request(ctx, http.MethodDelete, versionPath(groupID, artifactID, version), nil, nil, nil, op)
}

func artifactPath(groupID, artifactID string) string {
	return "/groups/" + segment(groupID) + "/artifacts/" + segment(artifactID)
}

// versionPath keeps the `=` of a `branch=latest` expression unescaped the way
// the registry expects it.
func versionPath(groupID, artifactID, expression string) string {
	return artifactPath(groupID, artifactID) + "/versions/" + versionSegment(expression)
}

func versionSegment(expression string) string {
	if len(expression) > 7 && expression[:7] == "branch=" {
		return "branch=" + segment(expression[7:])
	}
	return segment(expression)
}
