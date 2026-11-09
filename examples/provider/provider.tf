terraform {
  required_providers {
    apicurio = {
      source  = "jamalshahverdiev/apicurio"
      version = "~> 0.1"
    }
  }
}

# A registry without authentication.
provider "apicurio" {
  url = "http://localhost:8090"
}

# A registry secured with Keycloak, using the client credentials of a
# confidential OIDC client (apicurio.authn.basic-client-credentials.enabled).
#
# provider "apicurio" {
#   url      = "http://localhost:8093"
#   username = "registry-api"
#   password = var.registry_client_secret
# }
