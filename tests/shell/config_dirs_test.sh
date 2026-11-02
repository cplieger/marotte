#!/usr/bin/env bash
# The boot's KIRO_HOME hand-off and the `mkdir -p` of every directory the container needs, with
# its fail-CLOSED branch, taken inline with extract_range. The ONE place the entrypoint aborts on
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

# From the resolver's definition, so a lost hand-off between it and the mkdir fails a case below
# rather than the extraction. Captured as a path, not sourced through a substitution, so a failed
# extraction stops this file.
BLOCK=$(extract_range '^effective_kiro_home() {$' '^  }$') || exit 1
grep -q '^mkdir -p' "$BLOCK" || {
  printf 'harness error: the extracted block does not reach the mkdir; the resolver moved below it\n' >&2
  exit 1
}
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

# --- 3. the directories land in the KIRO_HOME the resolver hands every child ---------
# The image's ENV sets KIRO_HOME, so the export matters only where nothing did: a child must
# still see the value the boot created. The subshells run with `set -u` off because the shipped
# entrypoint does (`grep '^set ' entrypoint.sh` finds nothing).
ROOT="$WORK/nokirohome"
(
  set +u
  TOOLS="$ROOT/tools"
  HOME="$ROOT/home"
  unset KIRO_HOME
  sleep() { return 0; }
  . "$BLOCK"
  bash -c 'printf "%s\n" "${KIRO_HOME-unset}"' >"$WORK/child.txt"
) >"$WORK/out.log" 2>&1
rc=$?
child=$(cat "$WORK/child.txt" 2>/dev/null)
[ "$rc" -eq 0 ] && [ -d "$ROOT/home/.kiro" ] && [ "$child" = "$ROOT/home/.kiro" ] \
  && ok "an unset KIRO_HOME creates HOME/.kiro and exports it to children" \
  || no "unset KIRO_HOME" "rc=$rc, child saw '$child': $(cat "$WORK/out.log")"

# A relative value would create the tree under the boot's cwd, which neither KAS nor the server reads.
ROOT="$WORK/relative"
mkdir -p "$ROOT" || exit 1
(
  set +u
  cd "$ROOT" || exit 1
  TOOLS="$ROOT/tools"
  HOME="$ROOT/home"
  KIRO_HOME="state/kiro"
  sleep() { return 0; }
  . "$BLOCK"
) >"$WORK/out.log" 2>&1
rc=$?
[ "$rc" -eq 0 ] && [ -d "$ROOT/home/.kiro" ] && [ ! -e "$ROOT/state" ] \
  && grep -Fq 'WARNING: ignoring KIRO_HOME=state/kiro (relative)' "$WORK/out.log" \
  && ok "a rejected KIRO_HOME creates the fallback tree, not the rejected path, and warns" \
  || no "rejected KIRO_HOME" "rc=$rc, created: $(ls -A "$ROOT"): $(cat "$WORK/out.log")"

report
