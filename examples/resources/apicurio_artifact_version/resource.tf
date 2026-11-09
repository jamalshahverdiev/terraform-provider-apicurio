# The artifact itself, without content: its versions are explicit resources.
resource "apicurio_artifact" "price" {
  group_id      = apicurio_group.payments.group_id
  artifact_id   = "price"
  artifact_type = "AVRO"
}

# Immutable versions, one resource each. A change of content replaces the
# version (needs apicurio.rest.deletion.artifact-version.enabled on the server).
resource "apicurio_artifact_version" "v1" {
  group_id    = apicurio_artifact.price.group_id
  artifact_id = apicurio_artifact.price.artifact_id
  version     = "1.0.0"
  content     = file("${path.module}/schemas/price-1.0.0.avsc")
  name        = "Price 1.0.0"
}

resource "apicurio_artifact_version" "v2" {
  group_id    = apicurio_artifact.price.group_id
  artifact_id = apicurio_artifact.price.artifact_id
  version     = "2.0.0"
  content     = file("${path.module}/schemas/price-2.0.0.avsc")
  depends_on  = [apicurio_artifact_version.v1] # keeps the version order
}

# Retire the old version once consumers moved on.
resource "apicurio_artifact_version" "v1_deprecated" {
  group_id    = apicurio_artifact.price.group_id
  artifact_id = apicurio_artifact.price.artifact_id
  version     = "0.9.0"
  content     = file("${path.module}/schemas/price-0.9.0.avsc")
  state       = "DEPRECATED"
}

# A draft: no rules are applied and the content can change in place
# (apicurio.rest.mutability.artifact-version-content.enabled on the server).
# Setting state = "ENABLED" later promotes it, applying the rules.
resource "apicurio_artifact_version" "next" {
  group_id    = apicurio_artifact.price.group_id
  artifact_id = apicurio_artifact.price.artifact_id
  version     = "3.0.0-draft"
  draft       = true
  content     = file("${path.module}/schemas/price-3.0.0.avsc")
}
