#!/usr/bin/env bash
# Record static-src/perf/canned/ from an x1 instance: every browser scenario of plan.json once,
# Chromium desktop, all its windows, each answer the page read and each window's stream frames
# (measure/canned.py). Re-record after a wire change fails static-src/perf/canned.test.ts.
# Usage: PERF_BUILD=<build.sh out> [PERF_PORT=19951] record-canned.sh
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
root=$(cd "$here/../.." && pwd)
: "${PERF_BUILD:?PERF_BUILD: the directory build.sh wrote}"
work=$(mktemp -d "${TMPDIR:-/tmp}/perf-canned.XXXXXX")
inst=$work/instance
rec=$work/recorded
mkdir -p "$rec"
# shellcheck disable=SC2329 # invoked by the EXIT trap
cleanup() {
  "$here/instance/stop.sh" "$inst"
  rm -rf "$work"
}
trap cleanup EXIT

python3 "$here/synth/fixtures.py" --out "$work/fixture" --scale 1 --seed 11 --now "$(date +%s)" \
  >/dev/null
scenarios=$(cd "$root/static-src" \
  && node -e 'import("./perf/views.ts").then((m) => console.log(Object.keys(m.VIEWS).join(" ")))')
for s in $scenarios; do
  "$here/instance/seed-fixture.sh" "$work/fixture" "$inst" >/dev/null
  "$here/instance/serve.sh" "$inst" >/dev/null
  node "$root/static-src/perf/view.mjs" --instance "$inst" --plan "$here/plan.json" \
    --scenario "$s" --engine chromium --width desktop --out "$rec/$s.json" \
    --record "$rec/$s.answers.jsonl"
  "$here/instance/stop.sh" "$inst"
done
python3 "$here/measure/canned.py" --recorded "$rec" --instance "$inst" --plan "$here/plan.json" \
  --routes "$here/canned-routes.json" --out "$root/static-src/perf/canned"
