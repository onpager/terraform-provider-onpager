# Team configuration

This root configuration connects a team and service to an Alertmanager
integration, alert filters, a work calendar, a primary on-call schedule and an
escalation chain. It also creates an observer role, explicit role assignments,
a group and a service account. It is an executable example, not a reusable module.

Use Terraform >= 1.11.0 and < 2.0.0, a reachable OnPager Control API, and a bearer
token with read and write grants for the declared resources. Set
`ONPAGER_ENDPOINT` to the Control base URL, for example
`https://onpager.example.test`, and obtain `ONPAGER_TOKEN` from your secret manager
or shell environment. The operator must already be an active user. Team and
service names and custom role/group keys must be available in the tenant.

`terraform.tf` declares requirements, `providers.tf` configures OnPager,
`variables.tf` declares inputs, `main.tf` declares resources, and `outputs.tf`
declares outputs. Terraform loads these files together.

## Required inputs

| Input | Type | Meaning |
| --- | --- | --- |
| `iam_key_prefix` | `string` | Immutable prefix for custom role/group keys: 1–52 lowercase letters, digits, underscores or hyphens, starting with a letter or digit. |
| `operator_user_id` | `string` | UUID of the existing active user for explicit memberships and on-call coverage. |
| `rotation_starts_at` | `string` | Fixed whole-second RFC3339 rotation start and effective boundary. |
| `service_name` | `string` | Case-insensitively unique service name in the tenant, including inactive services. |
| `team_name` | `string` | Case-insensitively unique team name in the tenant, including inactive teams. |

For example, create an uncommitted `terraform.tfvars` and replace the user UUID
with an existing user's ID:

```hcl
iam_key_prefix     = "team_config"
operator_user_id   = "11111111-1111-4111-8111-111111111111"
rotation_starts_at = "2027-01-04T00:00:00Z"
service_name       = "API"
team_name          = "Operations"
```

## Optional inputs

Every optional input defaults to `null`.

| Input | Object fields or type | Effect when set |
| --- | --- | --- |
| `notification_target` | `delivery_channel_id`, `target_id` strings | Creates explicit team alert, service alert and team on-call catalog bindings, enables schedule handover notifications, and adds a channel step to the chain. Supply an existing conversation-capable channel and target; the provider performs no discovery. |
| `oidc_group` | `provider_slug`, `issuer`, `client_id`, `external_key` strings | Creates a local OIDC group mapping for the declared group. The identity provider must already be configured. |
| `onpager_timezone` | `string` | Sends an explicit IANA request timezone. Null sends no override; the backend uses the user profile or UTC for a service account. |
| `team_alert_template` | `delivery_provider`, `language`, `format`, `body` strings | Creates a team `alert_direct` notification-template override for explicit `ru` or `en`. Each language inherits separately. Zvonok requires `plain` format. Null leaves inheritance unchanged. |
| `temporary_override` | `starts_at`, `ends_at` strings | Creates a one-off override for the operator using whole-second RFC3339 timestamps with explicit offsets. |

The work calendar uses a fixed 2024 UTC source week for recurring weekly
intervals. Choose `rotation_starts_at` separately for the intended coverage start.

## Ownership and outputs

This configuration owns only its declared resources and explicit bindings. The
calendar and schedule do not adopt unrelated entries, rotations or overrides.
The service account receives the declared observer assignment; creating it does
not issue a key. The custom role grants only `teams.read`, `services.read` and
`schedules.read` within the declared team. The group receives the same role
through its own assignment.

| Output | Value |
| --- | --- |
| `automation_service_account_id` | Created service account UUID. |
| `escalation_chain_id` | Created escalation chain UUID. |
| `integration_token` | One-time integration token, marked sensitive. |
| `integration_webhook_url` | Integration webhook URL. |
| `observer_role_id` | Created custom role UUID. |
| `service_id` | Created service UUID. |
| `team_id` | Created team UUID. |

Terraform state contains the integration token. Sensitive marking hides ordinary
CLI output; protect state and saved plans. Destroy uses each resource's documented
retirement behavior, including disable, archive or deletion where supported;
historical objects can remain in OnPager. See the individual
[resource examples](../resources/README.md) for import configurations.

## Run

From this directory, after configuring credentials and input values:

```sh
terraform init
terraform validate
terraform plan -out=tfplan
terraform apply tfplan
```

Provider installation requires a published OnPager release. For local provider
development, run `task dev` from the repository root and use the generated CLI
override as described in [CONTRIBUTING.md](../../CONTRIBUTING.md). The repository
documentation checks validate this configuration without applying it or
contacting OnPager.
