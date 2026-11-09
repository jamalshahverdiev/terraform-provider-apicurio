package client

import (
	"context"
	"net/url"
)

// Sort keys of SearchArtifacts (the server's ArtifactSortBy).
const (
	SortByName         = "name"
	SortByCreatedOn    = "createdOn"
	SortByModifiedOn   = "modifiedOn"
	SortByGroupID      = "groupId"
	SortByArtifactID   = "artifactId"
	SortByArtifactType = "artifactType"
)

// ArtifactSearch holds the filters of GET /search/artifacts; empty fields
// are not sent. GroupID, ArtifactID and ArtifactType match exactly;
// Description is a substring match; Name is an exact, case-sensitive match
// from Apicurio Registry 3.1 (a substring match on 3.0); Labels are "key" or
// "key:value" entries that are all required (ANDed).
type ArtifactSearch struct {
	GroupID      string
	ArtifactID   string
	Name         string
	Description  string
	ArtifactType string
	Labels       []string
	OrderBy      string // a SortBy* value
	Order        string // "asc" or "desc"
}

// SearchArtifacts returns every artifact matching the filters, following the
// pages in the server's order.
func (c *Client) SearchArtifacts(ctx context.Context, s ArtifactSearch) ([]Artifact, error) {
	q := url.Values{}
	set := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}
	set("groupId", s.GroupID)
	set("artifactId", s.ArtifactID)
	set("name", s.Name)
	set("description", s.Description)
	set("artifactType", s.ArtifactType)
	for _, l := range s.Labels {
		q.Add("labels", l)
	}
	set("orderby", s.OrderBy)
	set("order", s.Order)
	return listAll[Artifact](ctx, c, "/search/artifacts", q, "artifacts", "search artifacts")
}
