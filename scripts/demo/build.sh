#!/bin/sh
# Builds the static demo of the console into $1 (default dist-demo): the
# web UI as shipped, every language, the recording in scripts/demo/data.json
# and demo.js, which answers the console's requests from it. Serve the
# directory as plain files (GitHub Pages).
set -e
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
out=${1:-$root/dist-demo}
rm -rf "$out"; mkdir -p "$out/demo"
cp -R "$root/web/ui/." "$out/"
cp "$here/demo.js" "$out/demo/demo.js"
python3 - "$here/data.json" "$root/internal/i18n/locales" "$out/demo/data.json" <<'PY'
import json, os, sys
d = json.load(open(sys.argv[1]))
for f in os.listdir(sys.argv[2]):
    if f.endswith('.json'):
        d['responses']['/i18n/' + f[:-5]] = json.load(open(os.path.join(sys.argv[2], f)))
json.dump(d, open(sys.argv[3], 'w'), ensure_ascii=False)
PY
for page in index.html wall.html summary.html; do
  [ -f "$out/$page" ] || continue
  sed -i.bak 's#<link rel="stylesheet" href="app.css">#<link rel="stylesheet" href="app.css">\n<script src="demo/demo.js"></script>\n<style>.demo-ribbon{position:sticky;top:0;z-index:100;padding:6px 12px;text-align:center;font:13px system-ui,sans-serif;background:\#1f7a4d;color:\#fff}.demo-ribbon a{color:\#fff;text-decoration:underline}</style>#' "$out/$page"
  rm -f "$out/$page.bak"
done
touch "$out/.nojekyll"
echo "demo built in $out"
