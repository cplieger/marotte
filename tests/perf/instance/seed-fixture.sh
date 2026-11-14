#!/usr/bin/env bash
# Seed an instance directory from one generated fixture (synth/fixtures.py): the fixture's trees,
# the fake kiro-cli and the git shim on the instance's bin/, the files tab and the repositories'
# remotes re-pointed at this instance's work tree and fake forge (PERF_PORT + 9).
# Usage: [PERF_PORT=19951] seed-fixture.sh <fixture dir> <instance dir>
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
fx=$(cd "${1:?fixture directory}" && pwd)
inst=${2:?instance directory}
port=${PERF_PORT:-19951}
[[ $port =~ ^[0-9]+$ ]] || {
  echo "seed-fixture.sh: PERF_PORT=$port is not a port number" >&2
  exit 2
}
[ -f "$fx/manifest.json" ] || {
  echo "seed-fixture.sh: no fixture at $fx (run synth/fixtures.py)" >&2
  exit 2
}
mkdir -p "$inst"
inst=$(cd "$inst" && pwd)
# The marker is what lets a re-seed empty the directory: never one this script did not seed.
if [ -n "$(ls -A "$inst")" ] && [ ! -f "$inst/.perf-instance" ]; then
  echo "seed-fixture.sh: $inst is not empty and was not seeded by this script" >&2
  exit 2
fi
"$here/stop.sh" "$inst"
find "$inst" -mindepth 1 -maxdepth 1 -exec rm -rf {} +
touch "$inst/.perf-instance"
mkdir -p "$inst"/{bin,trigger,logs}
cp -a "$fx/config" "$fx/home" "$fx/work" "$fx/engine" "$fx/forge" "$inst/"
chmod 0600 "$inst/config/mcp.json"
python=$(command -v python3)
{
  printf '#!%s\n' "$python"
  tail -n +2 "$here/../synth/fake-kiro-cli.py"
} >"$inst/bin/kiro-cli"
cp "$here/git" "$inst/bin/git"
chmod 0755 "$inst/bin/kiro-cli" "$inst/bin/git"
"$python" - "$fx" "$inst" "http://127.0.0.1:$((port + 9))" <<'EOF'
import json
import pathlib
import sys

fx, inst, forge_base = sys.argv[1:4]
inst = pathlib.Path(inst)
m = json.loads(pathlib.Path(fx, 'manifest.json').read_text())
work = str(inst / 'work')
tabs_path = inst / 'config' / 'tabs.json'
tabs = json.loads(tabs_path.read_text())
for t in tabs['tabs']:
    if t['ref'].startswith(m['work_path'] + '/'):
        t['ref'] = work + t['ref'][len(m['work_path']) :]
tabs_path.write_text(json.dumps(tabs, separators=(',', ':')))
for repo in m['git']['repos']:
    cfg = inst / 'work' / repo / '.git' / 'config'
    cfg.write_text(cfg.read_text().replace(m['forge_base'] + '/', forge_base + '/'))
sessions = {}
for header in sorted((inst / 'config' / 'chats').glob('*/chat.json')):
    sessions[header.parent.name] = json.loads(header.read_text())['acp_session_id']
seeded = {
    'fixture': fx,
    'scale': m['scale'],
    'run': m['run']['workflowId'],
    'long_chat': m['chats']['long']['id'],
    'stream_chat': m['chats']['stream']['id'],
    'stream_session': m['chats']['stream']['session'],
    'run_parent': m['chats']['run_parent']['id'],
    'tabs': m['tabs']['ids'],
    'sessions': sessions,
    'shell_lines': m['shell']['lines'],
    'work': work,
    'forge_base': forge_base,
}
(inst / 'seeded.json').write_text(json.dumps(seeded, indent=1))
EOF
echo "seeded $inst from $fx"
