# A branch is an ordered list of versions; `latest` and `drafts` are
# maintained by the registry itself and cannot be managed.
resource "apicurio_branch" "stable" {
  group_id    = apicurio_artifact.price.group_id
  artifact_id = apicurio_artifact.price.artifact_id
  branch_id   = "stable"
  description = "Versions approved for production"

  versions = [
    apicurio_artifact_version.v1.version,
    apicurio_artifact_version.v2.version,
  ]
}

# Without `versions` the branch is created empty and its list is left to
# other tooling (for example the `branches` of new versions).
resource "apicurio_branch" "nightly" {
  group_id    = apicurio_artifact.price.group_id
  artifact_id = apicurio_artifact.price.artifact_id
  branch_id   = "nightly"
}
