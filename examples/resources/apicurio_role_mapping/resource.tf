# Roles on a registry with apicurio.auth.role-based-authorization=true and
# apicurio.auth.role-source=application. The principal is the preferred
# username of a user or the name of a service account.
resource "apicurio_role_mapping" "ci" {
  principal_id   = "service-account-ci-pipeline"
  role           = "DEVELOPER"
  principal_name = "CI pipeline"
}

resource "apicurio_role_mapping" "auditors" {
  principal_id = "auditor"
  role         = "READ_ONLY"
}

# Who the provider itself is, as the registry sees it.
data "apicurio_current_user" "me" {}

output "provider_is_admin" {
  value = data.apicurio_current_user.me.admin
}
