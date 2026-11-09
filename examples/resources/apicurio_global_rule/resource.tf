# Registry-wide defaults: every artifact without a rule of the same type on
# its group or on itself gets these. Needs the admin role on a secured registry.
resource "apicurio_global_rule" "validity" {
  rule_type = "VALIDITY"
  config    = "FULL"
}

resource "apicurio_global_rule" "compatibility" {
  rule_type = "COMPATIBILITY"
  config    = "BACKWARD"
}

resource "apicurio_global_rule" "integrity" {
  rule_type = "INTEGRITY"
  config    = "REFS_EXIST,NO_DUPLICATES"
}
