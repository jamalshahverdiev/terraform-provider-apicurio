# A rule for one artifact; it takes precedence over the group and global rules.
resource "apicurio_artifact_rule" "price_compatibility" {
  group_id    = apicurio_artifact.price.group_id
  artifact_id = apicurio_artifact.price.artifact_id
  rule_type   = "COMPATIBILITY"
  config      = "BACKWARD"
}

# NONE switches a rule type off for one artifact even when a global or group
# rule is configured. Versions created later are not checked.
resource "apicurio_artifact_rule" "legacy_no_validity" {
  group_id    = apicurio_artifact.legacy.group_id
  artifact_id = apicurio_artifact.legacy.artifact_id
  rule_type   = "VALIDITY"
  config      = "NONE"
}

# Rules are applied to versions created after them: make the versions depend
# on the rule so that Terraform orders the operations.
resource "apicurio_artifact_version" "price_v2" {
  group_id    = apicurio_artifact.price.group_id
  artifact_id = apicurio_artifact.price.artifact_id
  version     = "2.0.0"
  content     = file("${path.module}/schemas/price-2.0.0.avsc")
  depends_on  = [apicurio_artifact_rule.price_compatibility]
}
