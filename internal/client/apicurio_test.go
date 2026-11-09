package client

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"testing"

	"github.com/jarcoal/httpmock"
)

const testURL = "http://apicurio-host"

// newTestClient builds a client whose HTTP client uses a private mock
// transport, so tests can run in parallel.
func newTestClient(t *testing.T, opts ...Option) (*Client, *httpmock.MockTransport) {
	t.Helper()
	mt := httpmock.NewMockTransport()
	opts = append([]Option{WithHTTPClient(&http.Client{Transport: mt})}, opts...)
	c, err := New(testURL, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, mt
}

func TestNewNormalizesURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "server url", in: "http://localhost:8090", want: "http://localhost:8090/apis/registry/v3"},
		{name: "trailing slash", in: "http://localhost:8090/", want: "http://localhost:8090/apis/registry/v3"},
		{name: "api path given", in: "http://localhost:8090/apis/registry/v3", want: "http://localhost:8090/apis/registry/v3"},
		{name: "api path with slash", in: "http://localhost:8090/apis/registry/v3/", want: "http://localhost:8090/apis/registry/v3"},
		{name: "context path", in: "https://registry.example.com/registry/", want: "https://registry.example.com/registry/apis/registry/v3"},
		{name: "query dropped", in: "http://h:1?x=1#f", want: "http://h:1/apis/registry/v3"},
		{name: "spaces trimmed", in: "  http://h:1 ", want: "http://h:1/apis/registry/v3"},
		{name: "empty", in: "", wantErr: true},
		{name: "no scheme", in: "localhost:8090", wantErr: true},
		{name: "bad scheme", in: "ftp://h", wantErr: true},
		{name: "no host", in: "http://", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, err := New(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("New(%q): want error, got %q", tc.in, c.BaseURL())
				}
				return
			}
			if err != nil {
				t.Fatalf("New(%q): %v", tc.in, err)
			}
			if got := c.BaseURL(); got != tc.want {
				t.Errorf("BaseURL = %q, want %q", got, tc.want)
			}
			if got, want := c.ServerURL()+APIPath, tc.want; got != want {
				t.Errorf("ServerURL+APIPath = %q, want %q", got, want)
			}
		})
	}
}

func TestAuthenticators(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		opts       []Option
		method     string
		wantHeader string
	}{
		{name: "none", method: AuthNone, wantHeader: ""},
		{name: "token", opts: []Option{WithToken("t0k")}, method: AuthToken, wantHeader: "Bearer t0k"},
		{name: "basic", opts: []Option{WithBasicAuth("registry-api", "s3cret")}, method: AuthBasic,
			wantHeader: "Basic " + base64.StdEncoding.EncodeToString([]byte("registry-api:s3cret"))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, mt := newTestClient(t, tc.opts...)
			var got http.Header
			mt.RegisterResponder(http.MethodGet, testURL+APIPath+"/system/info", func(r *http.Request) (*http.Response, error) {
				got = r.Header.Clone()
				return httpmock.NewJsonResponse(200, SystemInfo{Version: "3.3.4"})
			})
			if _, err := c.GetSystemInfo(context.Background()); err != nil {
				t.Fatalf("GetSystemInfo: %v", err)
			}
			if c.AuthMethod() != tc.method {
				t.Errorf("AuthMethod = %q, want %q", c.AuthMethod(), tc.method)
			}
			if c.HasCredentials() != (tc.method != AuthNone) {
				t.Errorf("HasCredentials = %v", c.HasCredentials())
			}
			if h := got.Get("Authorization"); h != tc.wantHeader {
				t.Errorf("Authorization = %q, want %q", h, tc.wantHeader)
			}
			if h := got.Get("Accept"); h != "application/json" {
				t.Errorf("Accept = %q", h)
			}
			if h := got.Get("User-Agent"); h != "terraform-provider-apicurio" {
				t.Errorf("User-Agent = %q", h)
			}
		})
	}
}

func TestDoJSONSendsBodyAndQuery(t *testing.T) {
	t.Parallel()
	c, mt := newTestClient(t, WithUserAgent("tf-test/1"))
	var gotBody, gotCT, gotUA, gotQuery string
	mt.RegisterResponder(http.MethodPost, testURL+APIPath+"/groups", func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		gotBody, gotCT, gotUA, gotQuery = string(b), r.Header.Get("Content-Type"), r.Header.Get("User-Agent"), r.URL.RawQuery
		return httpmock.NewStringResponse(204, ""), nil
	})
	err := c.request(context.Background(), http.MethodPost, "/groups", map[string][]string{"dryRun": {"true"}}, map[string]string{"groupId": "g"}, nil, "create group")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if gotBody != `{"groupId":"g"}` {
		t.Errorf("body = %q", gotBody)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q", gotCT)
	}
	if gotUA != "tf-test/1" {
		t.Errorf("User-Agent = %q", gotUA)
	}
	if gotQuery != "dryRun=true" {
		t.Errorf("query = %q", gotQuery)
	}
}

func TestSegmentEscapes(t *testing.T) {
	t.Parallel()
	if got := segment("a b/c"); got != "a%20b%2Fc" {
		t.Errorf("segment = %q", got)
	}
}
