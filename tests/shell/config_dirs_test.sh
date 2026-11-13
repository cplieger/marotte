#!/usr/bin/env bash
# The entrypoint's `mkdir -p` of every directory the container needs and its fatal branch, taken
# inline with extract_range; a healthy image never takes that branch. Lint:
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
BLOCK=$(extract_range '^if ! mkdir -p "\$CONFIG_DIR"' '^fi$') || exit 1
# The end anchor `^fi$` is not unique in entrypoint.sh: a lost `fi` would capture the
# legacy-/config/kiro migration below it. Name that fault rather than rely on it failing to parse.
if grep -q '/config/kiro' "$BLOCK"; then
  printf 'harness error: extract_range ran past the mkdir block; its fi end anchor is not unique\n' >&2
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

# The server holds HOME/.ssh at 700 and warns on each change, so one born wider warns on every fresh volume.
mode=$(stat -c %a "$ROOT/home/.ssh" 2>/dev/null)
[ "$mode" = 700 ] && [ "$(stat -c %a "$ROOT/home")" = 755 ] \
  && ok "HOME/.ssh is created 700 and HOME keeps the umask's 755" \
  || no "HOME/.ssh mode" "HOME/.ssh $mode, HOME $(stat -c %a "$ROOT/home")"

# internal/composition/boot_modes.go owns the modes of marotte's own folders. The boot creates
# ~/.ssh at 700 when it is missing and restores the system's /tmp to 1777 (tmp_mode_test.sh), and
# sets no other mode; the drop-in's temp file is a file.
stray=$(grep -vE '^[[:space:]]*#' "$ENTRYPOINT" | grep -nE 'chmod|mkdir[^#]* -m|--mode|install [^#]*-m' \
  | grep -vF 'chmod 644 "$tmp"' | grep -vF 'mkdir -m 700 "$HOME/.ssh"' \
  | grep -vF 'install -d -m 1777 "$dir"')
[ -z "$stray" ] \
  && ok "the boot script sets no folder mode beyond creating ~/.ssh at 700 and restoring /tmp to 1777" \
  || no "folder mode set in entrypoint.sh" "$stray"

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

# --- 2. a rejected tools folder (empty TOOLS) gets no folder ---------------------
ROOT="$WORK/rejected"
(
  CONFIG_DIR="$ROOT/config"
  TOOLS=""
  HOME="$ROOT/home"
  KIRO_HOME="$ROOT/home/.kiro"
  # Records every argument and creates only those under ROOT, so a stray "/bin" stays a record.
  mkdir() {
    local a
    for a; do
      printf '%s\n' "$a" >>"$WORK/mkdir.args"
      case $a in "$ROOT"/*) command mkdir -p "$a" || return ;; esac
    done
  }
  . "$BLOCK"
) >"$WORK/out.log" 2>&1
rc=$?
stray=$(grep -E '(^|/)(bin|kiro-cli-versions)$' "$WORK/mkdir.args")
[ "$rc" -eq 0 ] && [ -z "$stray" ] && [ -d "$ROOT/config" ] && [ -d "$ROOT/home/.kiro" ] \
  && ok "with no tools folder the boot creates its other folders and no bin/ or kiro-cli-versions/" \
  || no "rejected tools folder" "rc=$rc, created:$stray"

# --- 3. a directory it cannot create aborts the boot -----------------------------
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
