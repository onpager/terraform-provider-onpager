resource "onpager_enrichment_dataset" "example" {
  name        = "service_owners"
  description = "Explicit passive inventory of service ownership."
  enabled     = false
  rows = [
    { service = "checkout", owner = "commerce" },
    { service = "billing", owner = "payments" },
  ]
}
