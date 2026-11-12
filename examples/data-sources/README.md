# Data source examples

Each `onpager_*` directory is a standalone configuration containing
`data-source.tf` and `terraform.tf`. Copy both files, replace the example
selector with an existing object's identity, and configure the provider with
`ONPAGER_ENDPOINT` and `ONPAGER_TOKEN` or explicit provider arguments.

| Example | Selector |
| --- | --- |
| [`onpager_delivery_channel`](onpager_delivery_channel/data-source.tf) | Known delivery channel UUID supplied through `channel_id`; distinct from a catalog binding UUID. |
| [`onpager_group`](onpager_group/data-source.tf) | Exact immutable group key. |
| [`onpager_rbac_role`](onpager_rbac_role/data-source.tf) | Exact immutable role key. |
| [`onpager_service`](onpager_service/data-source.tf) | Exact service name and optional expected owner team UUID, resolved by the team data source in this example. |
| [`onpager_team`](onpager_team/data-source.tf) | Exact team name. |
| [`onpager_user`](onpager_user/data-source.tf) | Local login, distinct from an OIDC subject or display name. |

These lookups read existing objects without modifying them. The token needs the
corresponding read grants. Named selectors use exact server-side filters; zero,
multiple or incomplete matches are errors. Team, service, role and group lookups
also accept a known UUID. Data sources can read inactive objects, builtin roles
and system groups where access permits. Delivery channel output excludes
credentials, configuration and target addresses.

Use `terraform init`, `terraform validate` and `terraform plan` in the selected
directory after configuring credentials and selectors. There are no import
examples because data sources do not adopt managed resources. `task docs` and
`task docs:check` validate these configurations without contacting OnPager.
