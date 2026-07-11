#!/usr/bin/env bash
set -euo pipefail

ARTIFACT_DIR="${1:?usage: release-smoke.sh <artifact-dir> [target] [arch]}"
TARGET="${2:-$(uname -s | tr '[:upper:]' '[:lower:]')}"
ARCH="${3:-amd64}"
case "$TARGET" in
  darwin|linux) ;;
  *) echo "unsupported target: $TARGET" >&2; exit 2 ;;
esac

ROOT="$(mktemp -d)"
trap 'rm -rf "$ROOT"' EXIT
INSTALL="$ROOT/install"
DATA="$ROOT/data"
mkdir -p "$INSTALL/plugins" "$DATA"

CLI_ARCHIVE="$ARTIFACT_DIR/mow-${TARGET}-${ARCH}.tar.gz"
test -f "$CLI_ARCHIVE"

tar -xzf "$CLI_ARCHIVE" -C "$INSTALL"
chmod +x "$INSTALL/mow"

for id in ssh docker ai; do
  archive="$ARTIFACT_DIR/mow-${id}-plugin-${TARGET}-${ARCH}.tar.gz"
  test -f "$archive"
  package="$INSTALL/plugins/$id"
  mkdir -p "$package"
  tar -xzf "$archive" -C "$package"
  "$INSTALL/mow" plugin validate "$package"
done

cat > "$ROOT/config.json" <<JSON
{
  "version": 1,
  "app": {"data_dir": "$DATA", "plugins_dir": "$INSTALL/plugins"},
  "plugins": {"ai": {"enabled": true, "settings": {"providers": [{"name": "mock", "kind": "mock"}]}}}
}
JSON

VERSION_OUTPUT="$($INSTALL/mow version)"
test -n "$VERSION_OUTPUT"
PROVIDERS_OUTPUT="$($INSTALL/mow --config "$ROOT/config.json" ai providers)"
echo "$PROVIDERS_OUTPUT" | grep -q 'mock'
echo "release package + runtime smoke passed: target=$TARGET arch=$ARCH version=$VERSION_OUTPUT"
