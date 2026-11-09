# The artifact types this registry accepts (built-in and custom).
data "apicurio_artifact_types" "all" {}

output "supports_protobuf" {
  value = contains(data.apicurio_artifact_types.all.types, "PROTOBUF")
}
