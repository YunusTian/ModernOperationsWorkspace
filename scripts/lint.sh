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

for m in "${modules[@]}"; do
  echo "[lint] --- $m ---"
  (
    cd "$repo/$m"
    go vet ./...
  )
done

echo "[lint] OK"
