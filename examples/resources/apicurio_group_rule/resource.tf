# A rule for every artifact of a group that has no rule of that type itself;
# it takes precedence over the global rule of the same type.
resource "apicurio_group_rule" "payments_compatibility" {
  group_id  = apicurio_group.payments.group_id
  rule_type = "COMPATIBILITY"
  config    = "FULL_TRANSITIVE"
}
