#!/usr/bin/env bash
# The measure entry point of cplieger/ci's perf-measure.yaml: every cell of <cells.json> (one
# shard of a plan.json set), PERF_REPS reps each on a fresh instance, plus one profiled rep for
# a cell marked `profile`, then <out>/results.json for perf_gate.py. Runs the harness's own
# tests first, and measures every cell before it fails on a failed rep.
# Usage: PERF_BUILD=<build.sh out> [PERF_PORT=19951] [PERF_REPS=3] [PERF_REP_TIMEOUT=900]
#        run.sh <cells.json> <out dir>
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
root=$(cd "$here/../.." && pwd)
cells=$(cd "$(dirname "${1:?cells.json}")" && pwd)/$(basename "$1")
out=${2:?out directory}
: "${PERF_BUILD:?PERF_BUILD: the directory build.sh wrote}"
reps=${PERF_REPS:-3}
rep_timeout=${PERF_REP_TIMEOUT:-900}
for n in "$reps" "$rep_timeout"; do
  [[ $n =~ ^[1-9][0-9]*$ ]] || {
    printf 'run.sh: PERF_REPS and PERF_REP_TIMEOUT must be positive integers, not %s\n' "$n" >&2
    exit 2
  }
done
mkdir -p "$out"
out=$(cd "$out" && pwd)
raw=$out/raw
mkdir -p "$raw"
python3 -m unittest discover -s "$here" -p '*_test.py'

work=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/perf-run.XXXXXX")
inst=$work/instance
# shellcheck disable=SC2329 # invoked by the EXIT trap
cleanup() {
  "$here/instance/stop.sh" "$inst"
  rm -rf "$work"
}
trap cleanup EXIT

units() {
  python3 -c '
import json, sys
for c in json.load(open(sys.argv[1])):
    w = ",".join(c["windows"]) if "windows" in c else "-"
    p = "1" if c.get("profile") else "0"
    print(c["scenario"], c["scale"], c["engine"], c["width"], w, p)
' "$cells"
}

# One fixture per scale, dated from one instant so every rep of the shard reads the same records.
now=$(date +%s)
for scale in $(units | cut -d' ' -f2 | sort -u); do
  python3 "$here/synth/fixtures.py" --out "$work/fixture-$scale" --scale "${scale#x}" \
    --seed 11 --now "$now" >/dev/null
done

failed=()
# rep <replay 0|1> <tag> <view.mjs args...>: one rep of the current unit on a fresh instance;
# with replay 1, servercpu.py replays the reads of a rep that passed.
rep() {
  local replay=$1 tag=$2
  shift 2
  local doc=$raw/$stem.$tag.json rc=0
  "$here/instance/seed-fixture.sh" "$work/fixture-$scale" "$inst" >/dev/null
  if ! "$here/instance/serve.sh" "$inst" >/dev/null; then
    failed+=("$stem $tag: the instance did not start")
    [ ! -f "$inst/logs/marotte.log" ] || tail -n 40 "$inst/logs/marotte.log" >&2
    return 0
  fi
  timeout -k 30 "$rep_timeout" node "$root/static-src/perf/view.mjs" --instance "$inst" \
    --plan "$here/plan.json" --scenario "$scenario" --engine "$engine" --width "$width" \
    --out "$doc" "$@" || rc=$?
  if [ "$rc" -ne 0 ]; then
    failed+=("$stem $tag: view.mjs exited $rc")
  elif [ "$replay" = 1 ]; then
    python3 "$here/measure/servercpu.py" --instance "$inst" --rep "$doc" \
      --out "$raw/$stem.$tag.servercpu.json" || failed+=("$stem $tag: servercpu.py failed")
  fi
  "$here/instance/stop.sh" "$inst"
}

while read -r scenario scale engine width windows profile; do
  stem=$scenario.$scale.$engine.$width
  args=()
  [ "$windows" = - ] || args+=(--windows "$windows")
  # Server CPU per request is held at x10 in Chromium desktop, so only those reps replay.
  replay=0
  [ "$scale.$engine.$width" != x10.chromium.desktop ] || replay=1
  start=$EPOCHREALTIME
  for n in $(seq 1 "$reps"); do
    rep "$replay" "r$n" --rep "$n" "${args[@]}"
  done
  if [ "$profile" = 1 ]; then
    rep 0 rp --rep p --profile "${args[@]}"
  fi
  python3 -c 'import sys; print(round(float(sys.argv[2]) - float(sys.argv[1]), 1))' \
    "$start" "$EPOCHREALTIME" >"$raw/$stem.seconds"
done < <(units)

if [ "${#failed[@]}" -gt 0 ]; then
  printf 'run.sh: %s\n' "${failed[@]}" >&2
  exit 1
fi
python3 "$here/measure/summarize.py" --cells "$cells" --raw "$raw" --out "$out/results.json"
printf 'wrote %s/results.json\n' "$out"
