// Command terraform-provider-apicurio is the Terraform provider for Apicurio
// Registry 3.x.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/jamalshahverdiev/terraform-provider-apicurio/internal/provider"
)

// Format the examples and regenerate docs/ from the schema and the examples.
//
//go:generate terraform fmt -recursive ./examples/
//go:generate go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@v0.25.0 generate --provider-name apicurio

// version is set by goreleaser at release build time (ldflags).
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run with support for debuggers")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/jamalshahverdiev/apicurio",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
