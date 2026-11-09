# How the registry sees the provider's credentials; useful to debug
# authentication and roles before managing anything.
data "apicurio_current_user" "me" {}

output "registry_user" {
  value = {
    username  = data.apicurio_current_user.me.username
    admin     = data.apicurio_current_user.me.admin
    developer = data.apicurio_current_user.me.developer
    viewer    = data.apicurio_current_user.me.viewer
  }
}
