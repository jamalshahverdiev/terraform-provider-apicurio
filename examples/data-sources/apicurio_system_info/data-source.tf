data "apicurio_system_info" "this" {}

output "registry_version" {
  value = data.apicurio_system_info.this.version
}
