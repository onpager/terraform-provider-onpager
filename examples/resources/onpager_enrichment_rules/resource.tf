# This resource owns the entire tenant list. Import existing rules with ID tenant.
resource "onpager_enrichment_rules" "example" {
  rules = [
    {
      id         = "22222222-2222-4222-8222-222222222222"
      dataset_id = "11111111-1111-4111-8111-111111111111"
      lookup = [
        { service = "labels.service" },
        { service = "labels.fallback_service" },
      ]
      emit = { owner = "owner" }
    },
  ]
}
