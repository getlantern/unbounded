#!/usr/bin/env bash
# Assemble the deployable demo site into ./dist.
#
# The same zip this produces is what gets submitted to the WordPress Plugin
# Directory, so the archive layout is not arbitrary: the top-level directory
# name becomes the plugin's permanent slug on wordpress.org. It must stay
# "unbounded".
#
# The zip is built from source rather than committed. The hand-uploaded copy on
# S3 sat at the Feb 2024 build for two and a half years while the source moved
# on, so a demo created from it installed a plugin that silently ignored half
# its own settings. Generating it here means the demo and the submission can
# only ever ship what is actually in the tree.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
plugin_dir="$(dirname "$here")"
out="$here/dist"
pkg="$here/package"
slug="unbounded"

command -v python3 >/dev/null || {
  echo "build.sh needs python3, which is not on PATH in this build image" >&2
  exit 1
}

rm -rf "$out" "$pkg"
mkdir -p "$out" "$pkg/$slug"

# Only what a WordPress install needs. README.md, docker-compose.yml and the
# demo site itself are development files and stay out of the distributed plugin.
for f in unbounded.php readme.txt uninstall.php; do
  cp "$plugin_dir/$f" "$pkg/$slug/$f"
done

# zipfile rather than the zip(1) binary: Cloudflare's Pages build image ships
# python3 but not zip, and this keeps the local and CI builds on one code path.
# The staged directory is kept: publish-wp-plugin.yml hands it to the SVN
# deploy as BUILD_DIR, so the directory and the zip can never differ.
python3 - "$pkg" "$out/$slug.zip" <<'PY'
import os, sys, zipfile

stage, target = sys.argv[1], sys.argv[2]
with zipfile.ZipFile(target, "w", zipfile.ZIP_DEFLATED) as z:
    for root, dirs, files in sorted(os.walk(stage)):
        dirs.sort()
        rel = os.path.relpath(root, stage)
        if rel != ".":
            z.writestr(rel + "/", "")
        for name in sorted(files):
            path = os.path.join(root, name)
            z.write(path, os.path.relpath(path, stage))
PY

cp "$here/index.html" "$here/blueprint.json" "$here/_headers" "$out/"

version=$(sed -n 's/^ \* Version: *//p' "$plugin_dir/unbounded.php" | head -1)
stable=$(sed -n 's/^Stable tag: *//p' "$plugin_dir/readme.txt" | head -1)
if [ "$version" != "$stable" ]; then
  echo "version mismatch: unbounded.php says '$version', readme.txt Stable tag says '$stable'" >&2
  echo "wordpress.org serves whatever Stable tag points at, so these must agree" >&2
  exit 1
fi

echo "built demo site -> $out (plugin v$version, $slug.zip $(wc -c < "$out/$slug.zip") bytes)"
