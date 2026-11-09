#!/bin/sh
# Builds release archives for every supported platform into dist/:
#   unagikuma-<version>-<os>-<arch>.tar.gz (.zip for Windows) and SHA256SUMS.
# Usage: scripts/dist.sh <version>
set -eu
cd "$(dirname "$0")/.."
version=${1:?usage: scripts/dist.sh <version>}
platforms="linux/amd64 linux/arm64 linux/arm linux/386 linux/riscv64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64 freebsd/amd64"

rm -rf dist
mkdir -p dist/stage
scripts/licenses.sh >dist/THIRD_PARTY_LICENSES.txt

for p in $platforms; do
	os=${p%/*}
	arch=${p#*/}
	name="unagikuma-$version-$os-$arch"
	dir="dist/stage/$name"
	exe=unagikuma
	[ "$os" = windows ] && exe=unagikuma.exe
	echo "building $p"
	mkdir -p "$dir"
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch GOARM=7 go build -trimpath \
		-ldflags "-s -w -X main.version=$version" -o "$dir/$exe" ./cmd/unagikuma
	cp LICENSE README*.md dist/THIRD_PARTY_LICENSES.txt "$dir/"
	mkdir -p "$dir/docs/spec" && cp docs/manual.md docs/per-site-upgrade.md "$dir/docs/" && cp docs/spec/monitors-conf.md "$dir/docs/spec/"
	if [ "$os" = windows ]; then
		(cd dist/stage && zip -qr "../$name.zip" "$name")
	else
		tar -C dist/stage -czf "dist/$name.tar.gz" "$name"
	fi
done

rm -rf dist/stage dist/THIRD_PARTY_LICENSES.txt
(cd dist && sha256sum unagikuma-* >SHA256SUMS)
ls -l dist
