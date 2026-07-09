#!/usr/bin/env bash
# race.sh -- run `go test -race` across all modules when CGO is available.
#
# Usage:
#   ./scripts/race.sh
#
# Preconditions:
#   - CGO_ENABLED=1
#   - a C compiler (gcc or clang) on PATH
#
# When no compiler is present the script exits 0 with a SKIP message, so it
# can be dropped into a CI matrix that includes hosts without a compiler.

set -euo pipefail

if ! command -v gcc >/dev/null 2>&1 && ! command -v clang >/dev/null 2>&1; then
  echo "[race] SKIP: no C compiler (gcc/clang) in PATH; install one to enable -race."
  exit 0
fi

export CGO_ENABLED=1

repo="$(cd "$(dirname "$0")/.." && pwd)"
modules=(
  "core"
  "plugins/ssh"
  "tests/e2e"
)

for m in "${modules[@]}"; do
  echo "[race] --- $m ---"
  (
    cd "$repo/$m"
    go test -race -count=1 ./...
  )
done

echo "[race] OK"
