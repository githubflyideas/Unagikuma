#!/bin/bash
# upgrade-fixture.sh VERSION BINARY WORKDIR PORT: run a release's binary on
# a fresh data directory, leave behind what such an installation has
# (accounts, an encrypted channel secret, an open incident, a push token,
# a node token, history) and pack it as
# internal/app/testdata/upgrade/VERSION.tar.gz for the upgrade tests.
# Run from the repository root with each release's binary; WORKDIR must
# not exist yet.
set -eu
v=$1; bin=$2; out=$3; port=$4
repo=$(pwd)
mkdir "$out"
cat >"$out/monitors.conf" <<B
@ IN SOA fixture. admin.localhost. ( 2026102801 1s 500ms 300ms 2 )
\$SET engine.first-round-delay=0s
self     IN TCP 127.0.0.1:$port title="This server"
down     IN TCP 127.0.0.1:1 title="Closed port"
backup   IN PUSH period=1h
B
"$bin" server --data "$out" --listen "127.0.0.1:$port" >"$out.log" 2>&1 &
pid=$!
base=http://127.0.0.1:$port
for _ in $(seq 1 50); do curl -fsS "$base/healthz" >/dev/null 2>&1 && break; sleep 0.2; done
jar=$out.jar
csrf=$(curl -fsS -c "$jar" -H 'Content-Type: application/json' -d '{"username":"admin","password":"fixture-pass-1"}' "$base/api/v1/setup" |
	sed -n 's/.*"csrf":"\([^"]*\)".*/\1/p')
h=(-b "$jar" -H "X-Unagikuma-CSRF: $csrf" -H 'Content-Type: application/json')
curl -fsS "${h[@]}" -d '{"name":"ops","kind":"feishu","opts":{"url":"https://open.feishu.cn/open-apis/bot/v2/hook/fixture-secret"}}' "$base/api/v1/channels" >/dev/null
curl -fsS "${h[@]}" -d '{"name":"bj"}' "$base/api/v1/nodes" >/dev/null
tok=$(curl -fsS "${h[@]}" "$base/api/v1/monitors/backup/push" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
curl -fsS "$base/api/push/$tok?status=up" >/dev/null
sleep 12
kill "$pid"
wait "$pid" || true
printf '{"version":"%s","push_token":"%s"}\n' "$v" "$tok" >"$out/fixture.json"
# No PEM keys in the repository: the test does not need the cluster CA.
tar czf "$repo/internal/app/testdata/upgrade/$v.tar.gz" --owner=0 --group=0 \
	--exclude='./cluster*' --exclude='*.db-wal' --exclude='*.db-shm' -C "$out" .
echo "$v: internal/app/testdata/upgrade/$v.tar.gz"
