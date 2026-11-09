resource "apicurio_group" "payments" {
  group_id    = "payments"
  description = "Schemas of the payments domain"
  labels = {
    team = "payments"
    env  = "prod"
  }

  # Deleting the group deletes its artifacts; refuse that unless asked for.
  force_destroy = false
}
