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
PLUGINS="$INSTALL/plugins"
mkdir -p "$PLUGINS" "$DATA"

CLI_ARCHIVE="$ARTIFACT_DIR/mow-${TARGET}-${ARCH}.tar.gz"
test -f "$CLI_ARCHIVE"

tar -xzf "$CLI_ARCHIVE" -C "$INSTALL"
chmod +x "$INSTALL/mow"

# ---------------------------------------------------------------------------
# Phase 1: plugin validate 冒烟（保持原有语义，用于覆盖 legacy path）
# ---------------------------------------------------------------------------
for id in ssh docker ai pve; do
  archive="$ARTIFACT_DIR/mow-${id}-plugin-${TARGET}-${ARCH}.tar.gz"
  test -f "$archive"
  package="$PLUGINS/$id"
  mkdir -p "$package"
  tar -xzf "$archive" -C "$package"
  "$INSTALL/mow" plugin validate "$package"
done

cat > "$ROOT/config.json" <<JSON
{
  "version": 1,
  "app": {"data_dir": "$DATA", "plugins_dir": "$PLUGINS"},
  "plugins": {"ai": {"enabled": true, "settings": {"providers": [{"name": "mock", "kind": "mock"}]}}}
}
JSON

VERSION_OUTPUT="$($INSTALL/mow version)"
test -n "$VERSION_OUTPUT"
PROVIDERS_OUTPUT="$($INSTALL/mow --config "$ROOT/config.json" ai providers)"
echo "$PROVIDERS_OUTPUT" | grep -q 'mock'
echo "release package + runtime smoke passed: target=$TARGET arch=$ARCH version=$VERSION_OUTPUT"

# ---------------------------------------------------------------------------
# Phase 2: 通过 catalog 走一次 install → list → update → uninstall 全链路
# ---------------------------------------------------------------------------
CAT_JSON="$ARTIFACT_DIR/catalog.json"
if [ ! -f "$CAT_JSON" ]; then
  echo "catalog.json not present; skipping catalog smoke" >&2
  exit 0
fi

# 用 file:// 指向 artifact 目录：catalog 里的 URL 若指向 github release，本地
# smoke 时用 python 起一个 http server 复用同一份内容更实际，但为了不引入依赖，
# 这里直接跑一个 catalog 派生副本：把 URL 全部改写成 file://<ARTIFACT_DIR>/...
DERIVED_CAT="$ROOT/catalog.json"
python3 - "$CAT_JSON" "$ARTIFACT_DIR" "$DERIVED_CAT" <<'PY'
import json, os, sys, urllib.parse
src, art, dst = sys.argv[1], sys.argv[2], sys.argv[3]
with open(src) as f: c = json.load(f)
for e in c.get("entries", []):
    for r in e.get("versions", []):
        for p in r.get("platforms", []):
            fname = p["url"].rsplit("/", 1)[-1]
            local = os.path.abspath(os.path.join(art, fname))
            p["url"] = "file://" + urllib.parse.quote(local)
with open(dst, "w") as f: json.dump(c, f)
PY

# 二级 config：使用派生 catalog，plugins_dir 换到干净目录避免 phase1 残留
CATALOG_PLUGINS="$ROOT/plugins-catalog"
mkdir -p "$CATALOG_PLUGINS"
cat > "$ROOT/config-catalog.json" <<JSON
{
  "version": 1,
  "app": {
    "data_dir": "$DATA",
    "plugins_dir": "$CATALOG_PLUGINS",
    "catalog": {
      "cache_dir": "$ROOT/catalog-cache",
      "sources": [{"name": "local", "url": "file://$DERIVED_CAT"}]
    }
  }
}
JSON

# refresh + search 冒烟
"$INSTALL/mow" --config "$ROOT/config-catalog.json" plugin catalog refresh
"$INSTALL/mow" --config "$ROOT/config-catalog.json" plugin search | grep -Eq 'ssh|docker|ai|pve'
# 从 catalog 装一个插件（挑 ssh，它编译最快、依赖最简单）
"$INSTALL/mow" --config "$ROOT/config-catalog.json" plugin install ssh
"$INSTALL/mow" --config "$ROOT/config-catalog.json" plugin list | grep -q 'ssh'
# uninstall + 校验目录消失
"$INSTALL/mow" --config "$ROOT/config-catalog.json" plugin uninstall ssh --purge
test ! -d "$CATALOG_PLUGINS/ssh"
echo "catalog install smoke passed"
