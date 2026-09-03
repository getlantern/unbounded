#!/usr/bin/env bash
# Assemble the deployable demo site into ./dist.
#
# wp-plugin.zip is built from the plugin source next door rather than committed.
# The hand-uploaded copy on S3 sat at the Feb 2024 build for two and a half years
# while the source moved on, so a demo created from it installed a plugin that
# silently ignored half its own settings. Generating the zip here means the demo
# can only ever ship what is actually in the tree.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
plugin_dir="$(dirname "$here")"
out="$here/dist"

command -v zip >/dev/null || {
  echo "build.sh needs 'zip', which is not on PATH in this build image" >&2
  exit 1
}

rm -rf "$out"
mkdir -p "$out/stage/wp-plugin"

# Mirror the layout of the published archive: a single top-level wp-plugin/ dir.
for f in browsers-unbounded-plugin.php README.md; do
  cp "$plugin_dir/$f" "$out/stage/wp-plugin/$f"
done

( cd "$out/stage" && zip -r -q "$out/wp-plugin.zip" wp-plugin )
rm -rf "$out/stage"

cp "$here/index.html" "$here/blueprint.json" "$here/_headers" "$out/"

version=$(sed -n 's/^ \* Version: *//p' "$plugin_dir/browsers-unbounded-plugin.php" | head -1)
echo "built demo site -> $out (plugin v${version:-unknown}, zip $(wc -c < "$out/wp-plugin.zip") bytes)"
