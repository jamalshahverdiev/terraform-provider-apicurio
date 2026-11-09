# Every artifact of a group, newest first.
data "apicurio_artifacts" "payments" {
  group_id = "payments"
  order_by = "created_on"
  order    = "desc"
}

# Production Avro schemas of one team, anywhere in the registry: every label
# must match; an empty value accepts any value of that key.
data "apicurio_artifacts" "prod_avro" {
  artifact_type = "AVRO"
  labels = {
    env  = "prod"
    team = ""
  }
}

output "payments_artifact_ids" {
  value = [for a in data.apicurio_artifacts.payments.artifacts : a.artifact_id]
}
