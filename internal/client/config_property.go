package client

import (
	"context"
	"net/http"
)

// ConfigProperty is a dynamic configuration property of the registry
// (GET /admin/config/properties).
type ConfigProperty struct {
	Name        string `json:"name"`
	Value       string `json:"value"`
	Type        string `json:"type,omitempty"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
}

// The three deletion switches, off by default on Apicurio Registry 3.x.
const (
	PropertyGroupDeletion    = "apicurio.rest.deletion.group.enabled"
	PropertyArtifactDeletion = "apicurio.rest.deletion.artifact.enabled"
	PropertyVersionDeletion  = "apicurio.rest.deletion.artifact-version.enabled"
)

// ListConfigProperties returns every dynamic property with its current value.
func (c *Client) ListConfigProperties(ctx context.Context) ([]ConfigProperty, error) {
	var out []ConfigProperty
	if err := c.request(ctx, http.MethodGet, "/admin/config/properties", nil, nil, &out, "list configuration properties"); err != nil {
		return nil, err
	}
	return out, nil
}

// GetConfigProperty reads one dynamic property. A name the registry does not
// know, or knows but does not expose dynamically, is ErrNotFound
// (ConfigPropertyNotFoundException).
func (c *Client) GetConfigProperty(ctx context.Context, name string) (*ConfigProperty, error) {
	var out ConfigProperty
	if err := c.request(ctx, http.MethodGet, "/admin/config/properties/"+segment(name), nil, nil, &out, "read configuration property "+quote(name)); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetConfigProperty changes a dynamic property; the change applies at once.
// A value of the wrong type is 400 InvalidPropertyValueException.
func (c *Client) SetConfigProperty(ctx context.Context, name, value string) error {
	body := struct {
		Value string `json:"value"`
	}{Value: value}
	return c.request(ctx, http.MethodPut, "/admin/config/properties/"+segment(name), nil, body, nil, "set configuration property "+quote(name))
}

// ResetConfigProperty removes the stored value, so the property falls back
// to its configured default (environment variable or shipped default).
func (c *Client) ResetConfigProperty(ctx context.Context, name string) error {
	return c.request(ctx, http.MethodDelete, "/admin/config/properties/"+segment(name), nil, nil, nil, "reset configuration property "+quote(name))
}
