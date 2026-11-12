# Terraform Provider for OnPager

The OnPager provider manages configuration through the OnPager Control API.
Provider source: `onpager/onpager`.

## Requirements

- Terraform >= 1.11.0 and < 2.0.0.
- An accessible OnPager Control endpoint.
- A user personal access token or service-account bearer token with the native
  permissions required by the selected resources and data sources.

## Configuration

```hcl
terraform {
  required_version = ">= 1.11.0, < 2.0.0"
  required_providers {
    onpager = {
      source = "onpager/onpager"
    }
  }
}

variable "onpager_endpoint" {
  type = string
}

variable "onpager_token" {
  type      = string
  sensitive = true
}

provider "onpager" {
  endpoint = var.onpager_endpoint
  token    = var.onpager_token
  timezone = "Europe/Moscow"
}
```

Declare `onpager_token` as a sensitive input variable. Omitted or `null` provider
arguments use `ONPAGER_ENDPOINT` and `ONPAGER_TOKEN`. Explicit arguments take
precedence. The endpoint is the Control base URL, including any reverse-proxy
prefix; resource requests append `/v1`.

Optional `timezone` sets the `Onpager-Timezone` request header. The API interprets
local datetime input; explicit timestamp offsets continue to identify absolute
instants. See [authentication](docs/guides/authentication.md) for environment
configuration, Vault integration and timezone semantics.

## Documentation

- [Provider reference](docs/index.md)
- [Getting started](docs/guides/getting-started.md)
- [Authentication and permissions](docs/guides/authentication.md)
- [Lifecycle and state](docs/guides/lifecycle.md)
- [Suppression and enrichment](docs/guides/suppression-enrichment.md)
- [Glossary](docs/guides/glossary.md)
- [Resource reference](docs/resources)
- [Data source reference](docs/data-sources)
- [Examples](examples)

The provider includes 34 resources and seven data sources. Reference schemas are
built from the provider with `tfplugindocs`; templates contain lifecycle,
permissions and import contracts.

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md) for tool requirements, local provider
installation, test commands and documentation generation.

```sh
task setup
task verify
```

[GitHub Actions](.github/workflows/ci.yml) runs the verification gate and release
packaging checks for pull requests and main-branch pushes. Version tags build
signed draft releases; see [release setup](CONTRIBUTING.md#releases).
