# Provider configuration from Vault

This example reads an existing KV v2 secret containing string fields `endpoint`
and `token`, then passes both values to the OnPager provider. Configure the Vault
provider with your deployment's authentication method and read permission for
that secret. It must be readable during planning and independent of resources
created by the same apply. Explicit OnPager values override `ONPAGER_*` variables.

Use Terraform >= 1.11.0 and < 2.0.0. Run `terraform init` to install the providers
after the OnPager release is published, then `terraform validate` and
`terraform plan`. Local development also needs the CLI override described in
[CONTRIBUTING.md](../../CONTRIBUTING.md). This example requires Vault and its
provider; the offline documentation gate excludes it from Terraform validation.
The example reads the secret without creating or modifying it.

The ordinary Vault data source persists its result in Terraform state. Sensitive
markings hide normal output; protect state and saved plans accordingly. Do not
commit real tokens or state files.
