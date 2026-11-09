package client

import (
	"context"
	"net/http"
)

// Rule types and their configurations. The registry accepts any config
// string, so the provider validates them itself.
const (
	RuleValidity      = "VALIDITY"
	RuleCompatibility = "COMPATIBILITY"
	RuleIntegrity     = "INTEGRITY"
)

// Rule is a configured rule (ruleType and config).
type Rule struct {
	RuleType string `json:"ruleType"`
	Config   string `json:"config"`
}

// RuleScope selects global rules (zero value), the rules of a group
// (GroupID set) or the rules of an artifact (both set).
type RuleScope struct {
	GroupID    string
	ArtifactID string
}

func (s RuleScope) path() string {
	switch {
	case s.ArtifactID != "":
		return artifactPath(s.GroupID, s.ArtifactID) + "/rules"
	case s.GroupID != "":
		return "/groups/" + segment(s.GroupID) + "/rules"
	}
	return "/admin/rules"
}

func (s RuleScope) String() string {
	switch {
	case s.ArtifactID != "":
		return "artifact " + quote(s.GroupID+"/"+s.ArtifactID)
	case s.GroupID != "":
		return "group " + quote(s.GroupID)
	}
	return "the registry"
}

// CreateRule configures a rule; 409 RuleAlreadyExistsException
// (ErrAlreadyExists) when the type is already configured in that scope.
func (c *Client) CreateRule(ctx context.Context, scope RuleScope, rule Rule) error {
	return c.request(ctx, http.MethodPost, scope.path(), nil, rule, nil, "create "+rule.RuleType+" rule of "+scope.String())
}

// GetRule reads a rule; ErrNotFound when the rule is not configured (or the
// group or artifact is missing).
func (c *Client) GetRule(ctx context.Context, scope RuleScope, ruleType string) (*Rule, error) {
	var out Rule
	if err := c.request(ctx, http.MethodGet, scope.path()+"/"+segment(ruleType), nil, nil, &out, "read "+ruleType+" rule of "+scope.String()); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateRule changes the config of a rule and returns the stored rule.
func (c *Client) UpdateRule(ctx context.Context, scope RuleScope, rule Rule) (*Rule, error) {
	var out Rule
	if err := c.request(ctx, http.MethodPut, scope.path()+"/"+segment(rule.RuleType), nil, rule, &out, "update "+rule.RuleType+" rule of "+scope.String()); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteRule removes a rule.
func (c *Client) DeleteRule(ctx context.Context, scope RuleScope, ruleType string) error {
	return c.request(ctx, http.MethodDelete, scope.path()+"/"+segment(ruleType), nil, nil, nil, "delete "+ruleType+" rule of "+scope.String())
}

// ListRules returns the types of the rules configured in a scope.
func (c *Client) ListRules(ctx context.Context, scope RuleScope) ([]string, error) {
	var out []string
	if err := c.request(ctx, http.MethodGet, scope.path(), nil, nil, &out, "list rules of "+scope.String()); err != nil {
		return nil, err
	}
	return out, nil
}
