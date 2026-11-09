data "apicurio_group" "payments" {
  group_id = "payments"
}

output "payments_labels" {
  value = data.apicurio_group.payments.labels
}
