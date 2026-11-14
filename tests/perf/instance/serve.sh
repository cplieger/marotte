#!/usr/bin/env bash
# Start marotte's test build on 127.0.0.1:PERF_PORT against a seeded instance, signed in to the
# fake Gitea on PERF_PORT + 9, and record both PIDs in <instance>/instance.json. Stops the PIDs a
# previous instance.json names first (stop.sh).
# Usage: PERF_BUILD=<dir holding marotte> [PERF_PORT=19951] serve.sh <instance dir>
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
inst=${1:?instance directory}
port=${PERF_PORT:-19951}
live=9847
if ! [[ $port =~ ^[0-9]+$ ]] || ((port < 1024 || port > 65526)); then
  printf 'serve.sh: PERF_PORT=%s is not a port from 1024 to 65526\n' "$port" >&2
  exit 2
fi
fport=$((port + 9))
for p in "$port" "$fport"; do
  [ "$p" != "$live" ] || {
    printf 'serve.sh: PERF_PORT=%s would bind %s, the live marotte\n' "$port" "$live" >&2
    exit 2
  }
done
bin=${PERF_BUILD:?PERF_BUILD: the build directory}/marotte
[ -x "$bin" ] || {
  printf 'serve.sh: no marotte binary at %s (run tests/perf/build.sh)\n' "$bin" >&2
  exit 2
}
inst=$(cd "$inst" && pwd)
[ -f "$inst/seeded.json" ] || {
  printf 'serve.sh: %s is not seeded (run seed-fixture.sh)\n' "$inst" >&2
  exit 2
}
"$here/stop.sh" "$inst"
probe() {
  curl -s -o /dev/null --max-time 2 "$1"
}
if probe "http://127.0.0.1:$port/" || probe "http://127.0.0.1:$fport/"; then
  printf 'serve.sh: port %s or %s already answers; pick another PERF_PORT\n' "$port" "$fport" >&2
  exit 2
fi
python=$(command -v python3)
real_git=
for g in /usr/bin/git /bin/git; do
  [ -x "$g" ] && real_git=$g && break
done
[ -n "$real_git" ] || {
  printf 'serve.sh: no git in /usr/bin or /bin, where marotte resolves it\n' >&2
  exit 2
}
logs=$inst/logs
mkdir -p "$logs"
setsid "$python" "$here/../synth/fake-gitea.py" --data "$inst/forge/forge.json" --port "$fport" \
  --log "$logs/forge.jsonl" --churn "$inst/trigger/forge-churn.json" \
  >"$logs/forge.log" 2>&1 </dev/null &
fpid=$!
# KIRO_API_KEY is a placeholder so whoami reads signed in and the sign-in modal stays shut.
env -i PATH="$inst/bin:$(dirname "$python"):/usr/local/bin:/usr/bin:/bin" HOME="$inst/home" \
  KIRO_HOME="$inst/home/.kiro" KIRO_CONFIG_DIR="$inst/config" KIRO_WORK_DIR="$inst/work" \
  LISTEN_ADDR="127.0.0.1:$port" TZ=Europe/Paris LANG=C.UTF-8 \
  KIRO_API_KEY=perf-not-a-key MAROTTE_ALLOW_BRIDGE_ENV=KIRO_API_KEY \
  MAROTTE_TEST_GIT="$inst/bin/git" PERF_GIT_REAL="$real_git" PERF_GIT_LOG="$logs/git.jsonl" \
  FAKE_KIRO_LOG="$logs/fake-kiro.jsonl" FAKE_KIRO_TRIGGER_DIR="$inst/trigger" \
  FAKE_KIRO_RUNS="$inst/engine/fake-runs.json" FAKE_KIRO_INSPECT_DIR="$inst/engine/inspect" \
  FAKE_KIRO_SESSIONS="$inst/engine/sessions.json" \
  setsid "$bin" >"$logs/marotte.log" 2>&1 </dev/null &
pid=$!
"$python" - "$inst" "$pid" "$port" "$bin" "$fpid" "$fport" <<'EOF'
import json
import pathlib
import sys
import time

inst, pid, port, binary, fpid, fport = sys.argv[1:7]
seeded = json.loads(pathlib.Path(inst, 'seeded.json').read_text())
record = {
    'pid': int(pid),
    'port': int(port),
    'binary': binary,
    'forge_pid': int(fpid),
    'forge_port': int(fport),
    'config_dir': inst + '/config',
    'seeded': seeded,
    'started': time.strftime('%Y-%m-%dT%H:%M:%S%z'),
}
pathlib.Path(inst, 'instance.json').write_text(json.dumps(record, indent=1))
EOF
for _ in $(seq 1 300); do
  kill -0 "$pid" 2>/dev/null || {
    printf 'serve.sh: marotte exited; see %s/marotte.log\n' "$logs" >&2
    exit 1
  }
  # /api/test/sse exists only in the marotte_test build, so a release binary never reads ready.
  if curl -sf --max-time 2 "http://127.0.0.1:$port/api/test/sse" 2>/dev/null | grep -q '"clients"'; then
    start=$inst/trigger/trigger-0000-start.json
    printf '{"op":"start"}\n' >"$start"
    for _ in $(seq 1 50); do
      [ -f "$start.done" ] && break
      sleep 0.1
    done
    # Signed in to the fake Gitea, as a configured install is, once its first inventory is read.
    "$python" - "$port" "$fport" <<'EOF' || exit 1
import json
import sys
import time
import urllib.request

port, fport = sys.argv[1:3]
base = f'http://127.0.0.1:{port}'
forge = f'127.0.0.1:{fport}'
login = {
    'kind': 'gitea',
    'host': forge,
    'token': 'fixture-token',
    'web_base_url': f'http://{forge}',
    'plaintext_http': True,
    'private_addresses': True,
}
req = urllib.request.Request(
    base + '/api/forges/login',
    data=json.dumps(login).encode(),
    headers={'Content-Type': 'application/json'},
    method='POST',
)
with urllib.request.urlopen(req, timeout=60) as r:
    r.read()
for _ in range(180):
    with urllib.request.urlopen(base + '/api/forges/inventory', timeout=60) as r:
        entries = json.loads(r.read()).get('entries') or []
    if entries and all(e.get('state') == 'ready' for e in entries):
        sys.exit(0)
    time.sleep(0.5)
sys.exit('serve.sh: the fake forge inventory never read ready')
EOF
    printf 'pid %s ready on %s (forge pid %s on %s)\n' "$pid" "$port" "$fpid" "$fport"
    exit 0
  fi
  sleep 0.1
done
printf 'serve.sh: not ready after 30 s; see %s/marotte.log\n' "$logs" >&2
exit 1
