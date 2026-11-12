# Terraform examples

All examples require Terraform >= 1.11.0 and < 2.0.0. Configure a reachable
OnPager Control API and bearer credentials before planning or applying.

| Directory | Purpose |
| --- | --- |
| [`provider/`](provider/provider.tf) | Provider configuration with optional explicit values and environment fallbacks. |
| [`provider-vault/`](provider-vault/README.md) | Read provider configuration from an existing Vault KV v2 secret. |
| [`team-configuration/`](team-configuration/README.md) | Complete root configuration connecting a team, service, role assignments and on-call resources. |
| [`resources/`](resources/README.md) | Standalone creation and import snippets for individual resources. |
| [`data-sources/`](data-sources/README.md) | Standalone read-only identity lookup examples. |

For installed releases, run `terraform init` in the selected example directory.
Local provider development uses the CLI override described in
[CONTRIBUTING.md](../CONTRIBUTING.md). Documentation checks validate examples
without applying them or contacting OnPager; the Vault example requires its
separate provider and is excluded from offline validation.
