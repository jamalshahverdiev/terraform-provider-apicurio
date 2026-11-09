# An artifact whose latest version follows a schema file: every change of
# the file creates a new version, numbered by the registry.
resource "apicurio_artifact" "price" {
  group_id    = apicurio_group.payments.group_id
  artifact_id = "price"
  name        = "Price"
  description = "Price of an item, published on the prices topic"
  labels = {
    topic = "prices"
  }

  # The registry detects AVRO from the content; set artifact_type and
  # content_type explicitly for content it cannot sniff.
  content = file("${path.module}/schemas/price.avsc")
}

# An artifact that references another one by its Avro type name.
resource "apicurio_artifact" "order" {
  group_id    = apicurio_group.payments.group_id
  artifact_id = "order"
  content     = file("${path.module}/schemas/order.avsc")

  references = [{
    group_id    = apicurio_artifact.price.group_id
    artifact_id = apicurio_artifact.price.artifact_id
    version     = apicurio_artifact.price.version
    name        = "com.example.Price"
  }]
}

# Metadata only: the versions are managed elsewhere (or by
# apicurio_artifact_version resources).
resource "apicurio_artifact" "events" {
  group_id      = apicurio_group.payments.group_id
  artifact_id   = "events"
  artifact_type = "PROTOBUF"
  name          = "Payment events"
}
