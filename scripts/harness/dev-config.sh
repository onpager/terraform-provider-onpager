#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
mkdir -p .harness

# Terraform CLI config uses HCL quoted strings; escape workspace path characters.
binary_dir="$PWD/bin"
binary_dir=${binary_dir//\\/\\\\}
binary_dir=${binary_dir//\"/\\\"}
cat > .harness/terraform.tfrc <<EOF
provider_installation {
  dev_overrides {
    "registry.terraform.io/onpager/onpager" = "$binary_dir"
  }
  direct {}
}
EOF
printf 'Development override: %s/.harness/terraform.tfrc\n' "$PWD"
