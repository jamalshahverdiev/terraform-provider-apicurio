package provider

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"

	"github.com/jamalshahverdiev/terraform-provider-apicurio/internal/client"
)

// fake is the in-memory registry of the current test when no live server
// is configured (nil otherwise).
var fake *fakeRegistry

// setupRegistry points the provider at the fake registry (or at the live one
// named by APICURIO_URL) and returns a client for direct API calls.
func setupRegistry(t *testing.T) *client.Client {
	t.Helper()
	fake = nil
	if !liveServer() {
		clearEnv(t)
		t.Setenv("APICURIO_URL", mockURL)
		activateMock(t)
		fake = newFakeRegistry()
		fake.install()
	}
	return testClient(t)
}

// testClient builds a client from the APICURIO_* environment (the same the
// provider under test reads).
func testClient(t *testing.T) *client.Client {
	t.Helper()
	var opts []client.Option
	switch {
	case os.Getenv(envToken) != "":
		opts = append(opts, client.WithToken(os.Getenv(envToken)))
	case os.Getenv(envUsername) != "":
		opts = append(opts, client.WithBasicAuth(os.Getenv(envUsername), os.Getenv(envPassword)))
	case os.Getenv(envOIDCTokenURL) != "":
		opts = append(opts, client.WithOIDCClientCredentials(os.Getenv(envOIDCTokenURL), os.Getenv(envOIDCClientID), os.Getenv(envOIDCClientSecret), nil))
	}
	if ca, insecure := os.Getenv(envCACertificate), os.Getenv(envInsecureSkipVerif); ca != "" || insecure == "true" {
		cfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: insecure == "true"} //nolint:gosec // test harness option
		if ca != "" {
			cfg.RootCAs = x509.NewCertPool()
			if !cfg.RootCAs.AppendCertsFromPEM([]byte(ca)) {
				t.Fatalf("%s is not a PEM certificate", envCACertificate)
			}
		}
		opts = append(opts, client.WithTLSConfig(cfg))
	}
	c, err := client.New(os.Getenv(envURL), opts...)
	if err != nil {
		t.Fatalf("test client: %v", err)
	}
	return c
}

// randName returns a unique object name for live registries (and a stable
// one for the fake, which starts empty).
func randName(prefix string) string {
	return acctest.RandomWithPrefix(prefix)
}

// setDeletion toggles one of the three deletion properties and restores it
// to true when the test ends.
func setDeletion(t *testing.T, c *client.Client, property, value string) {
	t.Helper()
	ctx := context.Background()
	if err := c.SetConfigProperty(ctx, property, value); err != nil {
		t.Fatalf("set %s=%s: %v", property, value, err)
	}
	t.Cleanup(func() {
		_ = c.SetConfigProperty(ctx, property, "true")
	})
}

// serverAtLeast reports whether the registry under test is at least the
// given version (the fake reports mockVersion).
func serverAtLeast(t *testing.T, c *client.Client, major, minor int) bool {
	t.Helper()
	if _, err := c.GetSystemInfo(context.Background()); err != nil {
		t.Fatalf("system info: %v", err)
	}
	return c.VersionAtLeast(major, minor)
}

// Avro fixtures: v2 adds a field with a default (BACKWARD compatible), the
// incompatible one adds a field without a default.
const (
	avroPriceV1    = `{"type":"record","name":"Price","namespace":"tfacc","fields":[{"name":"id","type":"string"},{"name":"price","type":"double"}]}`
	avroPriceV2    = `{"type":"record","name":"Price","namespace":"tfacc","fields":[{"name":"id","type":"string"},{"name":"price","type":"double"},{"name":"currency","type":"string","default":"EUR"}]}`
	avroPriceV2Fmt = "{\n  \"type\": \"record\",\n  \"name\": \"Price\",\n  \"namespace\": \"tfacc\",\n  \"fields\": [\n    {\"name\": \"id\", \"type\": \"string\"},\n    {\"name\": \"price\", \"type\": \"double\"},\n    {\"name\": \"currency\", \"type\": \"string\", \"default\": \"EUR\"}\n  ]\n}\n"
	avroPriceBad   = `{"type":"record","name":"Price","namespace":"tfacc","fields":[{"name":"id","type":"string"},{"name":"price","type":"double"},{"name":"currency","type":"string","default":"EUR"},{"name":"vat","type":"double"}]}`
	avroOrder      = `{"type":"record","name":"Order","namespace":"tfacc","fields":[{"name":"orderId","type":"string"},{"name":"price","type":"tfacc.Price"}]}`
	avroTax        = `{"type":"record","name":"Tax","namespace":"tfacc","fields":[{"name":"rate","type":"double"}]}`
	avroOrder2     = `{"type":"record","name":"Order","namespace":"tfacc","fields":[{"name":"orderId","type":"string"},{"name":"price","type":"tfacc.Price"},{"name":"tax","type":"tfacc.Tax"}]}`
	avroOrder2V2   = `{"type":"record","name":"Order","namespace":"tfacc","fields":[{"name":"orderId","type":"string"},{"name":"price","type":"tfacc.Price"},{"name":"tax","type":"tfacc.Tax"},{"name":"note","type":"string","default":""}]}`
	protoSchema    = "syntax = \"proto3\";\npackage tfacc;\nmessage Probe { string id = 1; }\n"
)
