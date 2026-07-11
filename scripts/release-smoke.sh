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
AI_ARCHIVE="$ARTIFACT_DIR/mow-ai-plugin-${TARGET}-${ARCH}.tar.gz"
test -f "$CLI_ARCHIVE"
test -f "$AI_ARCHIVE"

tar -xzf "$CLI_ARCHIVE" -C "$INSTALL"
tar -xzf "$AI_ARCHIVE" -C "$ROOT"
mv "$ROOT/mow-ai-plugin" "$INSTALL/plugins/ai"
chmod +x "$INSTALL/mow" "$INSTALL/plugins/ai"

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
echo "release smoke passed: target=$TARGET arch=$ARCH version=$VERSION_OUTPUT"

