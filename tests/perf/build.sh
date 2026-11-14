#!/usr/bin/env bash
# The build entry point of cplieger/ci's perf-ci.yaml: the client bundled with the terminal's
# fonts, as the release image ships it, and marotte built with -tags marotte_test (the test
# routes serve.sh reads ready from) into <out>/marotte, which run.sh takes as PERF_BUILD. The
# build's git hook is tested first, since CI's go test builds no tags and a silent hook reads
# every git_fetch cell as 0.
# Usage: build.sh <out dir>
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
out=${1:?out directory}
mkdir -p "$out"
out=$(cd "$out" && pwd)
cd "$root"
go test -count=1 -tags marotte_test -run '^TestMarotteTestGit_' ./internal/git
(cd static-src && npm ci --no-audit --no-fund)
scripts/dev-fonts.sh
go run ./cmd/bundle
CGO_ENABLED=0 go build -tags marotte_test -o "$out/marotte" .
printf 'built %s/marotte\n' "$out"
