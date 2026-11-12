#!/usr/bin/env bash
set -euo pipefail

for tool in go gofmt task terraform find python3 git; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    printf 'Missing tool: %s. See CONTRIBUTING.md for setup.\n' "$tool" >&2
    exit 1
  fi
done

python3 -c 'import sys; sys.exit(0 if sys.version_info >= (3, 9) else "Python >= 3.9 is required for documentation checks")'

go_version=$(go env GOVERSION)
if [[ ! "$go_version" =~ ^go1\.([0-9]+)\.([0-9]+)$ ]] || ((BASH_REMATCH[1] < 27)); then
  printf 'Go >= 1.27.0 is required; found %s.\n' "$go_version" >&2
  exit 1
fi

terraform_version=$(terraform version | sed -n '1s/^Terraform v//p')
if [[ ! "$terraform_version" =~ ^1\.([0-9]+)\.[0-9]+$ ]] || ((BASH_REMATCH[1] < 11)); then
  printf 'Terraform >= 1.11.0 and < 2.0.0 is required; found %s.\n' "$terraform_version" >&2
  exit 1
fi

printf 'Go: %s\nTask: %s\nTerraform: %s\nGit: %s\n' "$go_version" "$(task --version)" "$terraform_version" "$(git --version)"
