package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// tokenSkew is taken off the token lifetime so a token is renewed before the
// server considers it expired.
const tokenSkew = 30 * time.Second

// WithOIDCClientCredentials authenticates with the OAuth2 client credentials
// grant against tokenURL: nothing is sent at construction, the first request
// fetches a token, concurrent requests share it, it is renewed shortly before
// it expires and once more when the registry answers 401 (the request is then
// retried once).
func WithOIDCClientCredentials(tokenURL, clientID, clientSecret string, scopes []string) Option {
	return func(c *Client) {
		c.auth = &oidcAuth{client: c, tokenURL: tokenURL, clientID: clientID, clientSecret: clientSecret, scopes: scopes, now: time.Now}
	}
}

// TokenError is a failed request to the OIDC token endpoint.
type TokenError struct {
	URL         string
	Status      int    // 0 when the request did not get an HTTP answer
	Code        string // the OAuth2 `error` field, when the server sent one
	Description string // the OAuth2 `error_description`
	Err         error
}

func (e *TokenError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "OIDC token request to %s failed", e.URL)
	if e.Status != 0 {
		fmt.Fprintf(&b, ": HTTP %d", e.Status)
	}
	if e.Code != "" {
		b.WriteString(" " + e.Code)
	}
	if e.Description != "" {
		b.WriteString(": " + e.Description)
	}
	if e.Err != nil {
		b.WriteString(": " + e.Err.Error())
	}
	return b.String()
}

func (e *TokenError) Unwrap() error { return e.Err }

// IsTokenError reports whether err comes from the OIDC token endpoint (bad
// client credentials, unreachable identity provider).
func IsTokenError(err error) bool {
	var te *TokenError
	return errors.As(err, &te)
}

type oidcAuth struct {
	client                           *Client
	tokenURL, clientID, clientSecret string
	scopes                           []string
	now                              func() time.Time

	mu      sync.Mutex
	token   string
	expiry  time.Time
	fetches int // token requests made, for tests
}

func (a *oidcAuth) name() string { return AuthOIDC }

func (a *oidcAuth) apply(ctx context.Context, req *http.Request) error {
	tok, err := a.current(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	return nil
}

// current returns the cached token, fetching one when there is none or it
// is about to expire. The mutex makes concurrent first requests share one
// token request.
func (a *oidcAuth) current(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && a.now().Before(a.expiry) {
		return a.token, nil
	}
	return a.fetchLocked(ctx)
}

// refresh renews the token after a 401, unless another request already did
// (the failed request carried an older token); the caller retries once with
// the current token either way.
func (a *oidcAuth) refresh(ctx context.Context, failed *http.Request) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if failed.Header.Get("Authorization") != "Bearer "+a.token {
		return true, nil
	}
	if _, err := a.fetchLocked(ctx); err != nil {
		return false, err
	}
	return true, nil
}

type tokenResponse struct {
	AccessToken string          `json:"access_token"`
	TokenType   string          `json:"token_type"`
	ExpiresIn   json.RawMessage `json:"expires_in"` // a number, or a quoted number on some identity providers
}

// fetchLocked requests a token; the caller holds the mutex.
func (a *oidcAuth) fetchLocked(ctx context.Context) (string, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {a.clientID},
		"client_secret": {a.clientSecret},
	}
	if len(a.scopes) > 0 {
		form.Set("scope", strings.Join(a.scopes, " "))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", &TokenError{URL: a.tokenURL, Err: err}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", a.client.userAgent)
	a.fetches++
	resp, err := a.client.http.Do(req)
	if err != nil {
		return "", &TokenError{URL: a.tokenURL, Err: err}
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if readErr != nil {
		return "", &TokenError{URL: a.tokenURL, Status: resp.StatusCode, Err: fmt.Errorf("reading the response: %w", readErr)}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		te := &TokenError{URL: a.tokenURL, Status: resp.StatusCode}
		var oe struct {
			Code        string `json:"error"`
			Description string `json:"error_description"`
		}
		if json.Unmarshal(body, &oe) == nil {
			te.Code, te.Description = oe.Code, oe.Description
		}
		if te.Code == "" && len(body) > 0 && len(body) < 300 {
			te.Description = strings.TrimSpace(string(body))
		}
		// An endpoint that echoes the request (a proxy, a debug page) must not
		// put the secret into Terraform's diagnostics.
		if a.clientSecret != "" {
			te.Description = strings.NewReplacer(a.clientSecret, "***", url.QueryEscape(a.clientSecret), "***").Replace(te.Description)
		}
		return "", te
	}
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", &TokenError{URL: a.tokenURL, Status: resp.StatusCode, Err: fmt.Errorf("the response is not JSON: %w", err)}
	}
	if tr.AccessToken == "" {
		return "", &TokenError{URL: a.tokenURL, Status: resp.StatusCode, Err: errors.New("the response has no access_token")}
	}
	if tr.TokenType != "" && !strings.EqualFold(tr.TokenType, "Bearer") {
		return "", &TokenError{URL: a.tokenURL, Status: resp.StatusCode, Err: fmt.Errorf("token_type %q is not Bearer", tr.TokenType)}
	}
	var secs float64
	if raw := strings.Trim(strings.TrimSpace(string(tr.ExpiresIn)), `"`); raw != "" && raw != "null" {
		secs, _ = strconv.ParseFloat(raw, 64) // unparsable: 0, handled below
	}
	ttl := time.Duration(secs * float64(time.Second))
	if ttl <= 0 {
		ttl = 5 * time.Minute // no usable expiry: renew now and then
	}
	lifetime := ttl - tokenSkew
	if lifetime < ttl/2 {
		lifetime = ttl / 2
	}
	a.token = tr.AccessToken
	a.expiry = a.now().Add(lifetime)
	return a.token, nil
}
