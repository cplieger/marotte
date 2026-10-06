#!/usr/bin/env bash
# The boot's first decision: the `mkdir -p` of every directory the container needs, and its
# fail-CLOSED branch, taken inline with extract_range. The ONE place the entrypoint aborts on
# /config state (nothing to persist, nothing to serve); a healthy image never shows it. Lint:
#   SC2015 - `[ cond ] && ok || no` cannot mis-fire: lib.sh's ok/no always return 0.
#   SC2034 - these variables are inputs to entrypoint.sh code sourced at RUNTIME.
#   SC2329 - the mkdir/sleep stubs are called only by that runtime-sourced code.
#   SC1090 - the sourced path is produced by extract_range at runtime.
# shellcheck disable=SC2015,SC2034,SC2329,SC1090
set -u

# shellcheck source-path=SCRIPTDIR
. "$(dirname -- "$0")/lib.sh"
new_workdir >/dev/null

# Captured as a path, not sourced through a substitution, so a failed extraction stops this file.
BLOCK=$(extract_range '^mkdir -p' '^  }$') || exit 1
# The end anchor `^  }$` is not unique in entrypoint.sh: a lost closing brace would capture the
# legacy-/config/kiro migration below it. Name that fault rather than rely on it failing to parse.
if grep -q '/config/kiro' "$BLOCK"; then
  printf 'harness error: extract_range ran past the mkdir block; its closing-brace end anchor is not unique\n' >&2
  exit 1
fi

# --- 1. the success path creates every required directory ------------------------
ROOT="$WORK/ok"
(
  TOOLS="$ROOT/tools"
  HOME="$ROOT/home"
  KIRO_HOME="$ROOT/home/.kiro"
  . "$BLOCK"
) >"$WORK/out.log" 2>&1
rc=$?
missing=""
for d in tools/bin tools/kiro-cli-versions home/.local/share/kiro-cli home/.ssh home/.kiro \
  home/.cache/go-build home/.docker/cli-plugins; do
  [ -d "$ROOT/$d" ] || missing="$missing $d"
done
[ "$rc" -eq 0 ] && [ -z "$missing" ] \
  && ok "the boot creates all seven required directories and continues" \
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
  TOOLS="$ROOT/tools"
  HOME="$ROOT/home"
  KIRO_HOME="$ROOT/home/.kiro"
  mkdir() { return 1; }
  sleep() { return 0; }
  . "$BLOCK"
) >"$WORK/out.log" 2>&1
rc=$?
[ "$rc" -ne 0 ] && grep -Fq 'failed to create required directories (is /config mounted and writable?)' "$WORK/out.log" \
  && ok "a mkdir it cannot complete aborts the boot and says /config may be unmounted" \
  || no "unwritable /config" "rc=$rc, or the message lost its remediation hint: $(cat "$WORK/out.log")"

# --- 3. an unset KIRO_HOME aborts rather than being skipped ----------------------
# KIRO_HOME comes from the image's ENV, not from this script, so nothing here defaults
# it. An unset value reaches mkdir as an EMPTY operand, which mkdir refuses — so the
# coupling to the Dockerfile ENV fails the boot loudly instead of quietly leaving the
# kiro state directory uncreated. The subshell runs with `set -u` off because the
# shipped entrypoint does (`grep '^set ' entrypoint.sh` finds nothing); asserting this
# under the test file's own `set -u` would measure the harness, not the boot.
ROOT="$WORK/nokirohome"
(
  set +u
  TOOLS="$ROOT/tools"
  HOME="$ROOT/home"
  unset KIRO_HOME
  sleep() { return 0; }
  . "$BLOCK"
) >"$WORK/out.log" 2>&1
rc=$?
[ "$rc" -ne 0 ] && grep -q 'failed to create required directories' "$WORK/out.log" \
  && [ -d "$ROOT/tools/bin" ] \
  && ok "an unset KIRO_HOME aborts the boot, after the operands it could satisfy" \
  || no "unset KIRO_HOME" "rc=$rc: the boot continued with no kiro state directory"

report
