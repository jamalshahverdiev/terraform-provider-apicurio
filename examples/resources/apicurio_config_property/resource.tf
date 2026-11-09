# Turn the deletion switches on from Terraform; the resources that must be
# deletable depend on them so that the switch is set first.
resource "apicurio_config_property" "delete_groups" {
  name  = "apicurio.rest.deletion.group.enabled"
  value = "true"
}

resource "apicurio_config_property" "delete_artifacts" {
  name  = "apicurio.rest.deletion.artifact.enabled"
  value = "true"
}

resource "apicurio_config_property" "delete_versions" {
  name  = "apicurio.rest.deletion.artifact-version.enabled"
  value = "true"
}

resource "apicurio_group" "payments" {
  group_id   = "payments"
  depends_on = [apicurio_config_property.delete_groups]
}

# Any other dynamic property, as GET /admin/config/properties lists them.
resource "apicurio_config_property" "max_subjects" {
  name  = "apicurio.ccompat.max-subjects"
  value = "5000"
}
