#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."

files=()
while IFS= read -r -d '' file; do
  files+=("$file")
done < <(find . \( -path './.git' -o -path './.harness' -o -path './vendor' \) -prune -o -type f -name '*.go' -print0)

if ((${#files[@]} == 0)); then
  exit 0
fi

case "${1:-check}" in
  write) gofmt -w "${files[@]}" ;;
  check)
    unformatted=$(gofmt -l "${files[@]}")
    if [[ -n "$unformatted" ]]; then
      printf 'Go files need formatting; run task fmt:\n%s\n' "$unformatted" >&2
      exit 1
    fi
    ;;
  *) printf 'Usage: %s [write|check]\n' "$0" >&2; exit 2 ;;
esac
