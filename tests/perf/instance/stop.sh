#!/usr/bin/env bash
# Stop the PIDs <instance>/instance.json names (marotte and the fake Gitea), each only while it is
# still the process serve.sh spawned, checked by its command line; never by name or pattern.
# Usage: stop.sh <instance dir>
set -uo pipefail
inst=${1:?instance directory}
record=$inst/instance.json
[ -f "$record" ] || exit 0
read -r pid fpid bin < <(
  python3 - "$record" <<'EOF'
import json
import pathlib
import sys

d = json.loads(pathlib.Path(sys.argv[1]).read_text())
print(d.get('pid') or 0, d.get('forge_pid') or 0, d['binary'])
EOF
)
stop() {
  local p=$1 want=$2
  { [ "$p" -gt 0 ] && kill -0 "$p" 2>/dev/null; } || return 0
  tr '\0' ' ' <"/proc/$p/cmdline" | grep -qF -- "$want" || return 0
  kill "$p"
  for _ in $(seq 1 100); do
    kill -0 "$p" 2>/dev/null || return 0
    sleep 0.1
  done
  kill -9 "$p" 2>/dev/null
}
stop "$pid" "$bin"
stop "$fpid" "fake-gitea.py"
rm -f "$record"
exit 0
