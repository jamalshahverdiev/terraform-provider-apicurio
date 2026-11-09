# One version by id.
data "apicurio_artifact_version" "v1" {
  group_id    = "payments"
  artifact_id = "price"
  version     = "1.0.0"
}

# The tip of a branch (default: branch=latest, the newest enabled version),
# with referenced schemas inlined.
data "apicurio_artifact_version" "stable" {
  group_id            = "payments"
  artifact_id         = "price"
  version             = "branch=stable"
  references_handling = "DEREFERENCE"
}

output "stable_schema" {
  value = data.apicurio_artifact_version.stable.content
}
