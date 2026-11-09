terraform {
  required_providers {
    apicurio = {
      source  = "jamalshahverdiev/apicurio"
      version = "~> 1.0"
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

# The same client, with the provider fetching the token from Keycloak itself.
#
# provider "apicurio" {
#   url = "https://registry.example.com"
#   oidc = {
#     token_url     = "https://keycloak.example.com/realms/registry/protocol/openid-connect/token"
#     client_id     = "registry-api"
#     client_secret = var.registry_client_secret
#   }
#   ca_certificate = file("${path.module}/ca.pem") # a private CA, if any
# }
