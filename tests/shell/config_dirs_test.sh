#!/usr/bin/env bash
# The `mkdir -p` of every directory the container needs, with its fail-CLOSED branch, taken inline
# with extract_range. The ONE place the entrypoint aborts on
# /config state (nothing to persist, nothing to serve); a healthy image never shows it. Lint:
#   SC2015 - `[ cond ] && ok || no` cannot mis-fire: lib.sh's ok/no always return 0.
#   SC2016 - the range pattern matches the entrypoint's literal "$CONFIG_DIR" text.
#   SC2034 - these variables are inputs to entrypoint.sh code sourced at RUNTIME.
#   SC2329 - the mkdir/sleep stubs are called only by that runtime-sourced code.
#   SC1090 - the sourced path is produced by extract_range at runtime.
# shellcheck disable=SC2015,SC2016,SC2034,SC2329,SC1090
set -u

# shellcheck source-path=SCRIPTDIR
. "$(dirname -- "$0")/lib.sh"
new_workdir >/dev/null

# Captured as a path, not sourced through a substitution, so a failed extraction stops this file.
BLOCK=$(extract_range '^mkdir -p "\$CONFIG_DIR"' '^  }$') || exit 1
# The end anchor `^  }$` is not unique in entrypoint.sh: a lost closing brace would capture the
# legacy-/config/kiro migration below it. Name that fault rather than rely on it failing to parse.
if grep -q '/config/kiro' "$BLOCK"; then
  printf 'harness error: extract_range ran past the mkdir block; its closing-brace end anchor is not unique\n' >&2
  exit 1
fi

# --- 1. the success path creates every required directory ------------------------
ROOT="$WORK/ok"
(
  CONFIG_DIR="$ROOT/config"
  TOOLS="$ROOT/tools"
  HOME="$ROOT/home"
  KIRO_HOME="$ROOT/home/.kiro"
  . "$BLOCK"
) >"$WORK/out.log" 2>&1
rc=$?
missing=""
for d in config tools/bin tools/kiro-cli-versions home/.local/share/kiro-cli home/.ssh home/.kiro \
  home/.cache/go-build home/.docker/cli-plugins; do
  [ -d "$ROOT/$d" ] || missing="$missing $d"
done
[ "$rc" -eq 0 ] && [ -z "$missing" ] \
  && ok "the boot creates all eight required directories and continues" \
  || no "required directories" "rc=$rc missing:$missing"

# Its own assertion: the only entry the SERVER writes into, and a missing parent fails the first
# install on a fresh volume, inside the server, invisible in the boot log.
[ -d "$ROOT/tools/kiro-cli-versions" ] \
  && ok "the kiro-cli version-install root exists before the server can write into it" \
  || no "kiro-cli install root" "$ROOT/tools/kiro-cli-versions was not created by the boot's mkdir"

# A SIBLING of the toolbelt engine's trees: its per-tool prune deletes every non-current version
# under opt/<tool>, for any manifest name, so an entry named `kiro-cli` would take the install.
[ ! -e "$ROOT/tools/opt/kiro-cli" ] \
  && ok "the boot creates no kiro-cli tree under the toolbelt engine's opt/, so the engine's prune cannot reach the install" \
  || no "install root under opt/" "$ROOT/tools/opt/kiro-cli exists again; the engine's per-tool prune deletes every non-current version directory under opt/<tool>"

# --- 2. a directory it cannot create aborts the boot -----------------------------
# Root cannot be denied a mkdir it owns, so an unwritable /config is provoked by shadowing mkdir in
# the subshell. `sleep` too: the shipped 10s delay proves nothing the exit status does not.
ROOT="$WORK/fail"
(
  CONFIG_DIR="$ROOT/config"
  TOOLS="$ROOT/tools"
  HOME="$ROOT/home"
  KIRO_HOME="$ROOT/home/.kiro"
  mkdir() { return 1; }
  sleep() { return 0; }
  . "$BLOCK"
) >"$WORK/out.log" 2>&1
rc=$?
[ "$rc" -ne 0 ] && grep -Fq "failed to create required directories (is $ROOT/config mounted and writable?)" "$WORK/out.log" \
  && ok "a mkdir it cannot complete aborts the boot and says the config dir may be unmounted" \
  || no "unwritable config dir" "rc=$rc, or the message lost its remediation hint: $(cat "$WORK/out.log")"

report
