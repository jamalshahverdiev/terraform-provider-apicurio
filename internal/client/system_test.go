package client

import (
	"context"
	"net/http"
	"testing"

	"github.com/jarcoal/httpmock"
)

func TestGetSystemInfoRecordsVersion(t *testing.T) {
	t.Parallel()
	c, mt := newTestClient(t)
	mt.RegisterResponder(http.MethodGet, testURL+APIPath+"/system/info", httpmock.NewStringResponder(200,
		`{"name":"Apicurio Registry (SQL)","description":"High performance, runtime registry for schemas and API designs.","version":"3.3.4","builtOn":"2026-11-03T19:25:03Z"}`))
	if c.ServerVersion() != "" || c.VersionAtLeast(3, 0) {
		t.Fatal("version must be unknown before the first call")
	}
	info, err := c.GetSystemInfo(context.Background())
	if err != nil {
		t.Fatalf("GetSystemInfo: %v", err)
	}
	if info.Name != "Apicurio Registry (SQL)" || info.Version != "3.3.4" || info.BuiltOn != "2026-11-03T19:25:03Z" {
		t.Errorf("info = %+v", info)
	}
	if c.ServerVersion() != "3.3.4" {
		t.Errorf("ServerVersion = %q", c.ServerVersion())
	}
	if !c.VersionAtLeast(3, 3) || !c.VersionAtLeast(3, 0) || c.VersionAtLeast(3, 4) || c.VersionAtLeast(4, 0) {
		t.Errorf("VersionAtLeast wrong for 3.3.4")
	}
}

func TestGetCurrentUser(t *testing.T) {
	t.Parallel()
	c, mt := newTestClient(t, WithBasicAuth("registry-api", "wrong"))
	mt.RegisterResponder(http.MethodGet, testURL+APIPath+"/users/me", httpmock.NewStringResponder(200,
		`{"username":"","displayName":"","admin":false,"developer":false,"viewer":false}`))
	me, err := c.GetCurrentUser(context.Background())
	if err != nil {
		t.Fatalf("GetCurrentUser: %v", err)
	}
	if me.Username != "" || me.Admin {
		t.Errorf("me = %+v, want the anonymous user", me)
	}
}

func TestParseVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in           string
		major, minor int
		ok           bool
	}{
		{"3.3.4", 3, 3, true},
		{"3.0.15", 3, 0, true},
		{"3.4.0-SNAPSHOT", 3, 4, true},
		{"v3.2.6", 3, 2, true},
		{"3.1", 3, 1, true},
		{"3", 0, 0, false},
		{"", 0, 0, false},
		{"x.y", 0, 0, false},
	}
	for _, tc := range tests {
		maj, mnr, ok := parseVersion(tc.in)
		if maj != tc.major || mnr != tc.minor || ok != tc.ok {
			t.Errorf("parseVersion(%q) = %d.%d %v, want %d.%d %v", tc.in, maj, mnr, ok, tc.major, tc.minor, tc.ok)
		}
	}
}
