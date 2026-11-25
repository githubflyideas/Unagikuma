#!/bin/sh
# End-to-end smoke test of a built binary: start a server on a scratch
# data directory, create the admin, add a monitor and see it checked.
# Usage: scripts/smoke.sh [path/to/unagikuma]
set -eu
bin=${1:-bin/unagikuma}
dir=$(mktemp -d)
port=${SMOKE_PORT:-18977}
base=http://127.0.0.1:$port
jar=$dir/jar
trap 'kill "$pid" 2>/dev/null || true; rm -rf "$dir"' EXIT
printf '@ IN SOA smoke. admin.localhost. ( 2026102801 2s 1s 1s 1 )\n$SET engine.first-round-delay=0s\n' >"$dir/monitors.conf"
"$bin" server --data "$dir" --listen "127.0.0.1:$port" >"$dir/log" 2>&1 &
pid=$!
fail() { echo "smoke: $*" >&2; cat "$dir/log" >&2; exit 1; }
for _ in $(seq 1 50); do curl -fsS "$base/healthz" >/dev/null 2>&1 && break; sleep 0.2; done
curl -fsS "$base/healthz" | grep -q '"status":"ok"' || fail "healthz"
csrf=$(curl -fsS -c "$jar" -H 'Content-Type: application/json' -d '{"username":"admin","password":"smoke-pass-1"}' "$base/api/v1/setup" |
	sed -n 's/.*"csrf":"\([^"]*\)".*/\1/p')
[ -n "$csrf" ] || fail "setup"
curl -fsS -b "$jar" -H "X-Unagikuma-CSRF: $csrf" -H 'Content-Type: application/json' \
	-d "{\"record\":{\"name\":\"self\",\"type\":\"TCP\",\"target\":\"127.0.0.1:$port\"}}" "$base/api/v1/monitors" >/dev/null || fail "create monitor"
for _ in $(seq 1 50); do
	curl -fsS -b "$jar" "$base/api/v1/monitors" | grep -q '"status":"ok"' && break
	sleep 0.2
done
curl -fsS -b "$jar" "$base/api/v1/monitors" | grep -q '"status":"ok"' || fail "monitor never OK"
code=$(curl -s -o /dev/null -w '%{http_code}' "$base/metrics")
[ "$code" = 401 ] || fail "metrics without a token answered $code"
echo "smoke: ok"
