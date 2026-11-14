#!/usr/bin/env bash
# The build entry point of cplieger/ci's perf-ci.yaml: the client bundled with the terminal's
# fonts, as the release image ships it, and marotte built with -tags marotte_test (the test
# routes serve.sh reads ready from) into <out>/marotte, which run.sh takes as PERF_BUILD.
# Usage: build.sh <out dir>
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
out=${1:?out directory}
mkdir -p "$out"
out=$(cd "$out" && pwd)
cd "$root"
(cd static-src && npm ci --no-audit --no-fund)
scripts/dev-fonts.sh
go run ./cmd/bundle
CGO_ENABLED=0 go build -tags marotte_test -o "$out/marotte" .
printf 'built %s/marotte\n' "$out"
