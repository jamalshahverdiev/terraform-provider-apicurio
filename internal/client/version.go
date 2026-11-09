package client

import (
	"context"
	"net/http"
	"net/url"
)

// Version states. DRAFT can only be entered at creation; SUNSET exists since
// Apicurio Registry 3.3.
const (
	StateEnabled    = "ENABLED"
	StateDisabled   = "DISABLED"
	StateDeprecated = "DEPRECATED"
	StateDraft      = "DRAFT"
	StateSunset     = "SUNSET"
)

// PropertyContentMutability lets `PUT .../versions/{v}/content` replace the
// content of a DRAFT version (off by default).
const PropertyContentMutability = "apicurio.rest.mutability.artifact-version-content.enabled"

// VersionUpdate is the body of PUT .../versions/{v} (EditableVersionMetaData).
// Properties present are written (an empty string or map clears), null and
// absent properties are left alone, so the client always sends all three.
type VersionUpdate struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Labels      map[string]string `json:"labels"`
}

// UpdateVersion changes the editable metadata of a version.
func (c *Client) UpdateVersion(ctx context.Context, groupID, artifactID, version string, in VersionUpdate) error {
	if in.Labels == nil {
		in.Labels = map[string]string{}
	}
	op := "update version " + quote(version) + " of artifact " + quote(groupID+"/"+artifactID)
	return c.request(ctx, http.MethodPut, versionPath(groupID, artifactID, version), nil, in, nil, op)
}

// UpdateVersionState moves a version to another state. Any state except
// DRAFT can be entered; leaving DRAFT applies the rules against the newest
// version (a *RuleViolationError when they fail). SUNSET needs 3.3; older
// servers answer 400 "Not able to deserialize data provided.".
func (c *Client) UpdateVersionState(ctx context.Context, groupID, artifactID, version, state string, dryRun bool) error {
	var q url.Values
	if dryRun {
		q = url.Values{"dryRun": {"true"}}
	}
	body := struct {
		State string `json:"state"`
	}{State: state}
	op := "set state " + state + " on version " + quote(version) + " of artifact " + quote(groupID+"/"+artifactID)
	return c.request(ctx, http.MethodPut, versionPath(groupID, artifactID, version)+"/state", q, body, nil, op)
}

// UpdateVersionContent replaces the content of a DRAFT version. A version in
// another state answers 409 ConflictException; a registry with
// PropertyContentMutability off answers 405 (*OperationDisabledError).
func (c *Client) UpdateVersionContent(ctx context.Context, groupID, artifactID, version string, content Content) error {
	op := "update content of version " + quote(version) + " of artifact " + quote(groupID+"/"+artifactID)
	return c.request(ctx, http.MethodPut, versionPath(groupID, artifactID, version)+"/content", nil, content, nil, op)
}
