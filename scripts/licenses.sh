#!/bin/sh
# Prints the licenses of the third-party code built into unagikuma: the Go
# modules linked into the binary and the browser libraries embedded in the
# web UI. Release archives ship the output as THIRD_PARTY_LICENSES.txt.
set -eu
cd "$(dirname "$0")/.."

section() {
	printf '\n%s\n%s\n%s\n\n' "================================================================" "$1" "================================================================"
}

echo "unagikuma includes the following third-party software."
go mod download
go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}' ./cmd/unagikuma |
	sort -u | while read -r path version dir; do
		section "$path $version"
		found=no
		for f in "$dir"/LICENSE* "$dir"/LICENCE* "$dir"/COPYING*; do
			if [ -f "$f" ]; then
				cat "$f"
				found=yes
			fi
		done
		[ "$found" = yes ] || echo "(license file not found in module; see https://pkg.go.dev/$path)"
	done
for lib in preact htm uplot; do
	section "$lib (web UI, see web/ui/vendor/VERSIONS.txt)"
	cat "web/ui/vendor/$lib/LICENSE"
done
