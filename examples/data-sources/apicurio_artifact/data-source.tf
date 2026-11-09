data "apicurio_artifact" "price" {
  group_id    = "payments"
  artifact_id = "price"
}

# The latest version's content, for example to feed a consumer configuration.
output "price_schema" {
  value = data.apicurio_artifact.price.content
}

output "price_version" {
  value = data.apicurio_artifact.price.version
}
