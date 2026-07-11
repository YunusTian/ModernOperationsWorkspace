#!/usr/bin/env bash
# lint.sh -- run deterministic formatting and vet checks across all modules.

set -euo pipefail

repo="$(cd "$(dirname "$0")/.." && pwd)"
modules=(
  "core"
  "apps/cli"
  "apps/desktop"
  "plugins/ssh"
  "plugins/docker"
  "plugins/ai"
  "sdk"
  "tests/e2e"
)

unformatted="$(git -C "$repo" ls-files '*.go' | xargs gofmt -l)"
if [[ -n "$unformatted" ]]; then
  echo "[lint] gofmt required:" >&2
  echo "$unformatted" >&2
  exit 1
fi

for m in "${modules[@]}"; do
  echo "[lint] --- $m ---"
  (
    cd "$repo/$m"
    go vet ./...
  )
done

echo "[lint] OK"
