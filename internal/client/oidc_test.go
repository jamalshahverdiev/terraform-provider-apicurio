package client

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jarcoal/httpmock"
)

const tokenURL = "http://keycloak-host/realms/registry/protocol/openid-connect/token"

// tokenResponder answers the token endpoint with tok-1, tok-2, ... and
// records the form of the last request.
func tokenResponder(t *testing.T, calls *int32, lastForm *string, expiresIn int) httpmock.Responder {
	t.Helper()
	return func(req *http.Request) (*http.Response, error) {
		n := atomic.AddInt32(calls, 1)
		if err := req.ParseForm(); err != nil {
			t.Errorf("token request form: %v", err)
		}
		*lastForm = req.PostForm.Encode()
		if req.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("token request Content-Type = %q", req.Header.Get("Content-Type"))
		}
		body := `{"access_token":"tok-` + strconv.Itoa(int(n)) + `","expires_in":` + strconv.Itoa(expiresIn) + `,"token_type":"Bearer"}`
		return httpmock.NewStringResponse(200, body), nil
	}
}

// systemInfoRequiring answers /system/info only for the given bearer tokens.
func systemInfoRequiring(apiCalls *int32, accepted ...string) httpmock.Responder {
	return func(req *http.Request) (*http.Response, error) {
		atomic.AddInt32(apiCalls, 1)
		got := req.Header.Get("Authorization")
		for _, tok := range accepted {
			if got == "Bearer "+tok {
				return httpmock.NewStringResponse(200, `{"name":"r","version":"3.3.4"}`), nil
			}
		}
		return httpmock.NewStringResponse(401, problem(401, "UnauthorizedException", "User is not authenticated.")), nil
	}
}

func TestOIDCNoRequestOnConstruction(t *testing.T) {
	t.Parallel()
	var calls int32
	var form string
	c, mt := newTestClient(t, WithOIDCClientCredentials(tokenURL, "id", "secret", nil))
	mt.RegisterResponder(http.MethodPost, tokenURL, tokenResponder(t, &calls, &form, 600))
	if calls != 0 || c.AuthMethod() != AuthOIDC || !c.HasCredentials() {
		t.Fatalf("construction must not fetch a token: calls=%d auth=%s", calls, c.AuthMethod())
	}
}

func TestOIDCSendsCredentialsAndScopes(t *testing.T) {
	t.Parallel()
	var calls, api int32
	var form string
	c, mt := newTestClient(t, WithOIDCClientCredentials(tokenURL, "registry-api", "s3cret", []string{"openid", "profile"}))
	mt.RegisterResponder(http.MethodPost, tokenURL, tokenResponder(t, &calls, &form, 600))
	mt.RegisterResponder(http.MethodGet, testURL+APIPath+"/system/info", systemInfoRequiring(&api, "tok-1"))
	if _, err := c.GetSystemInfo(context.Background()); err != nil {
		t.Fatalf("GetSystemInfo: %v", err)
	}
	for _, want := range []string{"grant_type=client_credentials", "client_id=registry-api", "client_secret=s3cret", "scope=openid+profile"} {
		if !strings.Contains(form, want) {
			t.Errorf("token form %q lacks %q", form, want)
		}
	}
	if calls != 1 || api != 1 {
		t.Errorf("calls: token=%d api=%d, want 1/1", calls, api)
	}
}

func TestOIDCSharesOneTokenAcrossConcurrentRequests(t *testing.T) {
	t.Parallel()
	var calls, api int32
	var form string
	c, mt := newTestClient(t, WithOIDCClientCredentials(tokenURL, "id", "secret", nil))
	mt.RegisterResponder(http.MethodPost, tokenURL, tokenResponder(t, &calls, &form, 600))
	mt.RegisterResponder(http.MethodGet, testURL+APIPath+"/system/info", systemInfoRequiring(&api, "tok-1"))
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.GetSystemInfo(context.Background()); err != nil {
				t.Errorf("GetSystemInfo: %v", err)
			}
		}()
	}
	wg.Wait()
	if calls != 1 || api != 10 {
		t.Errorf("calls: token=%d api=%d, want 1/10", calls, api)
	}
}

func TestOIDCRefreshesOnceOn401(t *testing.T) {
	t.Parallel()
	var calls, api int32
	var form string
	c, mt := newTestClient(t, WithOIDCClientCredentials(tokenURL, "id", "secret", nil))
	mt.RegisterResponder(http.MethodPost, tokenURL, tokenResponder(t, &calls, &form, 600))
	// tok-1 is refused (revoked on the server), tok-2 accepted
	mt.RegisterResponder(http.MethodGet, testURL+APIPath+"/system/info", systemInfoRequiring(&api, "tok-2"))
	if _, err := c.GetSystemInfo(context.Background()); err != nil {
		t.Fatalf("GetSystemInfo: %v", err)
	}
	if calls != 2 || api != 2 {
		t.Errorf("calls: token=%d api=%d, want 2/2 (one refresh, one retry)", calls, api)
	}
}

func TestOIDCDoesNotRetryTwice(t *testing.T) {
	t.Parallel()
	var calls, api int32
	var form string
	c, mt := newTestClient(t, WithOIDCClientCredentials(tokenURL, "id", "secret", nil))
	mt.RegisterResponder(http.MethodPost, tokenURL, tokenResponder(t, &calls, &form, 600))
	mt.RegisterResponder(http.MethodGet, testURL+APIPath+"/system/info", systemInfoRequiring(&api)) // never accepted
	_, err := c.GetSystemInfo(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
	if calls != 2 || api != 2 {
		t.Errorf("calls: token=%d api=%d, want 2/2 (a single refresh and retry)", calls, api)
	}
}

func TestOIDCRenewsBeforeExpiry(t *testing.T) {
	t.Parallel()
	var calls, api int32
	var form string
	c, mt := newTestClient(t, WithOIDCClientCredentials(tokenURL, "id", "secret", nil))
	mt.RegisterResponder(http.MethodPost, tokenURL, tokenResponder(t, &calls, &form, 100)) // lifetime 70 s after the skew
	mt.RegisterResponder(http.MethodGet, testURL+APIPath+"/system/info", systemInfoRequiring(&api, "tok-1", "tok-2"))
	a := c.auth.(*oidcAuth)
	now := time.Now()
	a.now = func() time.Time { return now }
	ctx := context.Background()
	if _, err := c.GetSystemInfo(ctx); err != nil {
		t.Fatal(err)
	}
	now = now.Add(60 * time.Second)
	if _, err := c.GetSystemInfo(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("a token with 40 s left must be reused, got %d token requests", calls)
	}
	now = now.Add(15 * time.Second) // 75 s: past the 70 s lifetime
	if _, err := c.GetSystemInfo(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("an expiring token must be renewed, got %d token requests", calls)
	}
}

func TestOIDCShortLivedTokenKeepsHalfItsLifetime(t *testing.T) {
	t.Parallel()
	var calls, api int32
	var form string
	c, mt := newTestClient(t, WithOIDCClientCredentials(tokenURL, "id", "secret", nil))
	mt.RegisterResponder(http.MethodPost, tokenURL, tokenResponder(t, &calls, &form, 20)) // shorter than the skew
	mt.RegisterResponder(http.MethodGet, testURL+APIPath+"/system/info", systemInfoRequiring(&api, "tok-1", "tok-2"))
	a := c.auth.(*oidcAuth)
	now := time.Now()
	a.now = func() time.Time { return now }
	ctx := context.Background()
	_, _ = c.GetSystemInfo(ctx)
	now = now.Add(5 * time.Second)
	_, _ = c.GetSystemInfo(ctx)
	if calls != 1 {
		t.Fatalf("half of 20 s is 10 s; 5 s later the token must be reused, got %d requests", calls)
	}
	now = now.Add(6 * time.Second)
	_, _ = c.GetSystemInfo(ctx)
	if calls != 2 {
		t.Errorf("after 11 s the token must be renewed, got %d requests", calls)
	}
}

func TestOIDCAcceptsQuotedExpiresIn(t *testing.T) {
	t.Parallel()
	var api int32
	c, mt := newTestClient(t, WithOIDCClientCredentials(tokenURL, "id", "secret", nil))
	mt.RegisterResponder(http.MethodPost, tokenURL, httpmock.NewStringResponder(200, `{"access_token":"q","expires_in":"300","token_type":"bearer"}`))
	mt.RegisterResponder(http.MethodGet, testURL+APIPath+"/system/info", systemInfoRequiring(&api, "q"))
	a := c.auth.(*oidcAuth)
	now := time.Now()
	a.now = func() time.Time { return now }
	if _, err := c.GetSystemInfo(context.Background()); err != nil {
		t.Fatalf("a quoted expires_in must be accepted: %v", err)
	}
	if want := now.Add(270 * time.Second); !a.expiry.Equal(want) {
		t.Errorf("expiry = %v, want %v (300 s minus the skew)", a.expiry, want)
	}
}

func TestOIDCConcurrent401sRefreshOnce(t *testing.T) {
	t.Parallel()
	var calls, api int32
	var form string
	c, mt := newTestClient(t, WithOIDCClientCredentials(tokenURL, "id", "secret", nil))
	mt.RegisterResponder(http.MethodPost, tokenURL, tokenResponder(t, &calls, &form, 600))
	// every goroutine starts with tok-1, which the registry has since revoked
	if _, err := c.auth.(*oidcAuth).current(context.Background()); err != nil {
		t.Fatal(err)
	}
	mt.RegisterResponder(http.MethodGet, testURL+APIPath+"/system/info", systemInfoRequiring(&api, "tok-2"))
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.GetSystemInfo(context.Background()); err != nil {
				t.Errorf("GetSystemInfo: %v", err)
			}
		}()
	}
	wg.Wait()
	if calls != 2 {
		t.Errorf("ten concurrent 401s must cause one refresh, got %d token requests", calls)
	}
}

func TestOIDCRefreshFailureIsATokenError(t *testing.T) {
	t.Parallel()
	var calls, api int32
	c, mt := newTestClient(t, WithOIDCClientCredentials(tokenURL, "id", "secret", nil))
	mt.RegisterResponder(http.MethodPost, tokenURL, func(*http.Request) (*http.Response, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return httpmock.NewStringResponse(200, `{"access_token":"tok-1","expires_in":600,"token_type":"Bearer"}`), nil
		}
		return httpmock.NewStringResponse(503, `{"error":"temporarily_unavailable","error_description":"maintenance"}`), nil
	})
	mt.RegisterResponder(http.MethodGet, testURL+APIPath+"/system/info", systemInfoRequiring(&api)) // tok-1 revoked
	_, err := c.GetSystemInfo(context.Background())
	if !IsTokenError(err) || !strings.Contains(err.Error(), "temporarily_unavailable") {
		t.Fatalf("a failed refresh must surface the identity provider's answer, got %v", err)
	}
	if calls != 2 || api != 1 {
		t.Errorf("calls: token=%d api=%d, want 2/1 (no retry without a new token)", calls, api)
	}
}

func TestOIDCTokenErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		status int
		body   string
		want   []string
	}{
		{"bad client credentials (Keycloak)", 401, `{"error":"unauthorized_client","error_description":"Invalid client or Invalid client credentials"}`,
			[]string{"OIDC token request to " + tokenURL + " failed: HTTP 401 unauthorized_client: Invalid client or Invalid client credentials"}},
		{"plain-text failure", 502, "bad gateway", []string{"HTTP 502", "bad gateway"}},
		{"no access_token", 200, `{"token_type":"Bearer"}`, []string{"has no access_token"}},
		{"not json", 200, `<html>`, []string{"is not JSON"}},
		{"wrong token type", 200, `{"access_token":"x","token_type":"MAC"}`, []string{`token_type "MAC" is not Bearer`}},
		{"echoing endpoint hides the secret", 400, "bad request: client_id=id&client_secret=s3cret-value&grant_type=client_credentials",
			[]string{"HTTP 400", "client_secret=***"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var api int32
			c, mt := newTestClient(t, WithOIDCClientCredentials(tokenURL, "id", "s3cret-value", nil))
			mt.RegisterResponder(http.MethodPost, tokenURL, httpmock.NewStringResponder(tc.status, tc.body))
			mt.RegisterResponder(http.MethodGet, testURL+APIPath+"/system/info", systemInfoRequiring(&api, "x"))
			_, err := c.GetSystemInfo(context.Background())
			if err == nil || !IsTokenError(err) {
				t.Fatalf("want a TokenError, got %v", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
			if strings.Contains(err.Error(), "s3cret-value") {
				t.Errorf("the client secret must never reach the error: %q", err)
			}
			if api != 0 {
				t.Errorf("no API request must be sent without a token, got %d", api)
			}
		})
	}
}
