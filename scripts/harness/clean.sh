#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."

# Go module directories are read-only. Let Go remove its own local module cache
# before deleting ordinary build outputs. Never act on a global GOMODCACHE.
GOMODCACHE="$PWD/.harness/go-mod" go clean -modcache
rm -rf -- bin .harness
