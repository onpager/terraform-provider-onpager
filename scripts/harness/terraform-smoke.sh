#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
export TF_CLI_CONFIG_FILE="$PWD/.harness/terraform.tfrc"
export TF_IN_AUTOMATION=1 CHECKPOINT_DISABLE=1

# dev_overrides loads the local binary directly; terraform init is unnecessary.
terraform -chdir=examples/provider providers schema -json > .harness/provider-schema.json
terraform -chdir=examples/provider validate -no-color
printf 'Terraform plugin schema and example validation: OK\n'
