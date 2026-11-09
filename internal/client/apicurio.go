// Package client is a hand-written HTTP client for the Apicurio Registry
// Core API v3 (`/apis/registry/v3`). It knows the authentication modes of
// the registry, maps its ProblemDetails error bodies to typed errors and
// follows the offset/limit paging of its list endpoints.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// APIPath is the path of the Core Registry API v3 under the server URL.
const APIPath = "/apis/registry/v3"

const defaultTimeout = 60 * time.Second

// Client talks to one Apicurio Registry instance. It is safe for concurrent
// use; Terraform runs resource operations in parallel.
type Client struct {
	baseURL   string // server URL + APIPath, without a trailing slash
	http      *http.Client
	auth      authenticator
	userAgent string

	mu            sync.RWMutex
	serverVersion string
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient replaces the default HTTP client (60 s timeout, default
// transport). Tests pass a client with a mock transport; TLS options build
// one with a custom transport.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		if h != nil {
			c.http = h
		}
	}
}

// WithUserAgent sets the User-Agent header of every request.
func WithUserAgent(ua string) Option {
	return func(c *Client) {
		if ua != "" {
			c.userAgent = ua
		}
	}
}

// WithToken sends a static bearer token.
func WithToken(token string) Option {
	return func(c *Client) { c.auth = bearerAuth{token: token} }
}

// WithBasicAuth sends HTTP basic credentials. On a registry with
// `apicurio.authn.basic-client-credentials.enabled=true` these are an OIDC
// client id and secret that the server exchanges for a token itself.
func WithBasicAuth(username, password string) Option {
	return func(c *Client) { c.auth = basicAuth{username: username, password: password} }
}

// New builds a client for the registry at rawURL. The URL is the server
// address (`http://localhost:8090`); a URL that already ends with
// `/apis/registry/v3` is accepted and trimmed.
func New(rawURL string, opts ...Option) (*Client, error) {
	base, err := normalizeBaseURL(rawURL)
	if err != nil {
		return nil, err
	}
	c := &Client{
		baseURL:   base,
		http:      &http.Client{Timeout: defaultTimeout},
		auth:      noAuth{},
		userAgent: "terraform-provider-apicurio",
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

func normalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("the registry URL is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("the registry URL %q is invalid: %w", raw, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("the registry URL %q must be an absolute http(s) URL such as http://localhost:8090", raw)
	}
	p := strings.TrimRight(u.Path, "/")
	p = strings.TrimSuffix(p, APIPath)
	p = strings.TrimRight(p, "/")
	u.Path = p + APIPath
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// BaseURL returns the API base URL, ending with /apis/registry/v3.
func (c *Client) BaseURL() string { return c.baseURL }

// ServerURL returns the server URL without the API path.
func (c *Client) ServerURL() string { return strings.TrimSuffix(c.baseURL, APIPath) }

// AuthMethod names the configured authentication: "none", "token" or "basic".
func (c *Client) AuthMethod() string { return c.auth.name() }

// HasCredentials reports whether requests carry credentials.
func (c *Client) HasCredentials() bool { return c.auth.name() != AuthNone }

// ServerVersion returns the version reported by the last GetSystemInfo call,
// or "" when none was made.
func (c *Client) ServerVersion() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.serverVersion
}

func (c *Client) setServerVersion(v string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.serverVersion = v
}

// doJSON sends a request whose body, when not nil, is encoded as JSON. The
// caller must consume and close the response body; decodeResponse does.
func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, body any) (*http.Response, error) {
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request body: %w", err)
		}
	}
	return c.doRaw(ctx, method, path, query, payload, "application/json")
}

// request sends a JSON request and decodes a JSON answer into out (when out
// is not nil). It closes the response body and maps errors; op names the
// operation in error messages.
func (c *Client) request(ctx context.Context, method, path string, query url.Values, body, out any, op string) error {
	resp, err := c.doJSON(ctx, method, path, query, body)
	if err != nil {
		return wrapOp(op, err)
	}
	defer func() { _ = resp.Body.Close() }()
	return c.decodeResponse(resp, out, op)
}

// doRaw sends a request with an optional raw body of the given content type.
// A 401 is retried once when the authenticator can refresh its credentials.
func (c *Client) doRaw(ctx context.Context, method, path string, query url.Values, payload []byte, contentType string) (*http.Response, error) {
	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	build := func() (*http.Request, error) {
		var rdr io.Reader
		if payload != nil {
			rdr = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, target, rdr)
		if err != nil {
			return nil, fmt.Errorf("build %s %s: %w", method, target, err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", c.userAgent)
		if payload != nil {
			req.Header.Set("Content-Type", contentType)
		}
		if err := c.auth.apply(ctx, req); err != nil {
			return nil, fmt.Errorf("authenticate %s %s: %w", method, target, err)
		}
		return req, nil
	}

	req, err := build()
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, target, err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	r, ok := c.auth.(refresher)
	if !ok {
		return resp, nil
	}
	refreshed, rerr := r.refresh(ctx)
	if rerr != nil {
		drain(resp)
		return nil, fmt.Errorf("%s %s: refresh credentials after 401: %w", method, target, rerr)
	}
	if !refreshed {
		return resp, nil
	}
	drain(resp)
	req, err = build()
	if err != nil {
		return nil, err
	}
	resp, err = c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s (retry): %w", method, target, err)
	}
	return resp, nil
}

// drain reads the rest of a body and closes it so the connection is reused.
func drain(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
}

// segment escapes one path segment (group ids, artifact ids, versions, ...).
func segment(s string) string { return url.PathEscape(s) }

// ---------------------------------------------------------------- authentication

// Authentication method names returned by Client.AuthMethod.
const (
	AuthNone  = "none"
	AuthToken = "token"
	AuthBasic = "basic"
)

// authenticator adds credentials to a request.
type authenticator interface {
	name() string
	apply(ctx context.Context, req *http.Request) error
}

// refresher is implemented by authenticators that can obtain new credentials
// after a 401 (the OIDC client credentials grant). refresh returns false when
// nothing changed, so the 401 is final.
type refresher interface {
	refresh(ctx context.Context) (bool, error)
}

type noAuth struct{}

func (noAuth) name() string                               { return AuthNone }
func (noAuth) apply(context.Context, *http.Request) error { return nil }

type bearerAuth struct{ token string }

func (bearerAuth) name() string { return AuthToken }
func (a bearerAuth) apply(_ context.Context, req *http.Request) error {
	req.Header.Set("Authorization", "Bearer "+a.token)
	return nil
}

type basicAuth struct{ username, password string }

func (basicAuth) name() string { return AuthBasic }
func (a basicAuth) apply(_ context.Context, req *http.Request) error {
	req.SetBasicAuth(a.username, a.password)
	return nil
}
