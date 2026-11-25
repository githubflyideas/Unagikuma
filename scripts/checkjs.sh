#!/bin/sh
# Checks the web UI's ES modules (needs Node). The UI has no build step,
# so a stray token, a name used but never imported or an unused import
# would otherwise only show up in the browser. The lint step installs
# ESLint into a temporary directory; it is skipped offline unless
# CHECKJS_LINT=1 (as in CI) makes it required.
set -eu
cd "$(dirname "$0")/.."
fail=0
for f in web/ui/js/*.js web/ui/js/views/*.js; do
	if ! node --input-type=module --check <"$f" 2>/tmp/checkjs.$$; then
		echo "$f:"; cat /tmp/checkjs.$$; fail=1
	fi
done
rm -f /tmp/checkjs.$$
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
install() {
	npm install --no-save --no-package-lock --no-audit --no-fund --prefix "$tmp" eslint@9.17.0 globals@15.14.0 >"$tmp/npm.log" 2>&1
}
if command -v npm >/dev/null 2>&1 && { install || { sleep 5; install; }; }; then
	cp web/eslint.config.mjs "$tmp/"
	(cd web && "$tmp/node_modules/.bin/eslint" -c "$tmp/eslint.config.mjs" 'ui/js/**/*.js') || fail=1
elif [ "${CHECKJS_LINT:-}" = 1 ]; then
	echo "cannot install ESLint:" >&2
	cat "$tmp/npm.log" >&2 2>/dev/null || true
	fail=1
else
	echo "ESLint not available: lint skipped" >&2
fi
exit $fail
