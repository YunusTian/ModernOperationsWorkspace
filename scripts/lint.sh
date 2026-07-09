#!/usr/bin/env bash
# lint.sh -- run golangci-lint across all modules.
#
# CI installs golangci-lint via the official action. When absent locally the
# script exits 0 with a SKIP message so developers without the tool are not
# blocked.

set -euo pipefail

if ! command -v golangci-lint >/dev/null 2>&1; then
  echo "[lint] SKIP: golangci-lint not on PATH."
  echo "        install: https://golangci-lint.run/usage/install/"
  exit 0
fi

repo="$(cd "$(dirname "$0")/.." && pwd)"
modules=(
  "core"
  "apps/cli"
  "apps/desktop"
  "plugins/ssh"
  "sdk"
  "tests/e2e"
)

for m in "${modules[@]}"; do
  echo "[lint] --- $m ---"
  (
    cd "$repo/$m"
    golangci-lint run --config "$repo/.golangci.yml" ./...
  )
done

echo "[lint] OK"
