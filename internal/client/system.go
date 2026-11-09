package client

import (
	"context"
	"net/http"
	"strconv"
	"strings"
)

// SystemInfo is the answer of GET /system/info.
type SystemInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	BuiltOn     string `json:"builtOn"`
}

// CurrentUser is the answer of GET /users/me. On a registry with
// authentication, an anonymous caller (no credentials, or HTTP basic
// credentials the registry could not exchange for a token) gets a 200 with an
// empty Username and every role false.
type CurrentUser struct {
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Admin       bool   `json:"admin"`
	Developer   bool   `json:"developer"`
	Viewer      bool   `json:"viewer"`
}

// GetSystemInfo reads the server information and records the server version
// for ServerVersion / VersionAtLeast. The endpoint is public even on a
// secured registry.
func (c *Client) GetSystemInfo(ctx context.Context) (*SystemInfo, error) {
	var info SystemInfo
	if err := c.request(ctx, http.MethodGet, "/system/info", nil, nil, &info, "read the registry system information"); err != nil {
		return nil, err
	}
	c.setServerVersion(info.Version)
	return &info, nil
}

// GetCurrentUser reads who the registry thinks the caller is.
func (c *Client) GetCurrentUser(ctx context.Context) (*CurrentUser, error) {
	var me CurrentUser
	if err := c.request(ctx, http.MethodGet, "/users/me", nil, nil, &me, "read the current user"); err != nil {
		return nil, err
	}
	return &me, nil
}

// VersionAtLeast reports whether the recorded server version is at least
// major.minor. It returns false when no version was recorded.
func (c *Client) VersionAtLeast(major, minor int) bool {
	maj, mnr, ok := parseVersion(c.ServerVersion())
	if !ok {
		return false
	}
	return maj > major || (maj == major && mnr >= minor)
}

// parseVersion reads "3.3.4", "3.3.4-SNAPSHOT" or "3.3" into its major and
// minor numbers.
func parseVersion(v string) (major, minor int, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+ "); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return 0, 0, false
	}
	maj, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	mnr, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, false
	}
	return maj, mnr, true
}
