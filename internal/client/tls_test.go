package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTLSOptions(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == APIPath+"/system/info" {
			_, _ = w.Write([]byte(`{"name":"r","version":"3.3.4"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	ctx := context.Background()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetSystemInfo(ctx); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("a self-signed server must be refused by default, got %v", err)
	}

	c, _ = New(srv.URL, WithTLSConfig(&tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12})) //nolint:gosec // the test exercises the opt-in
	if _, err := c.GetSystemInfo(ctx); err != nil {
		t.Errorf("insecure_skip_verify must accept it: %v", err)
	}

	c, _ = New(srv.URL, WithTLSConfig(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}))
	if _, err := c.GetSystemInfo(ctx); err != nil {
		t.Errorf("a trusted CA must accept it: %v", err)
	}

	c, _ = New(srv.URL, WithTLSConfig(nil))
	if _, err := c.GetSystemInfo(ctx); err == nil {
		t.Error("a nil TLS config must keep the default verification")
	}
}
