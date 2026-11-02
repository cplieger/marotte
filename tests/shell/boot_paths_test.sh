#!/usr/bin/env bash
# load_boot_paths() hands the server's own folders (`marotte paths`) to the boot; what the server
# prints is internal/composition's paths_test.go. Lint:
#   SC2015 - `[ cond ] && ok || no` cannot mis-fire: lib.sh's ok/no always return 0.
#   SC2016 - the grep patterns and the stub's script match literal "$VAR" text.
#   SC2034 - CONFIG_DIR and TOOLS are set for the extracted code to read or overwrite.
#   SC1090 - the sourced path is produced by extract_range at runtime.
#   SC2329 - the load_boot_paths/sleep stubs are called only by that runtime-sourced code.
# shellcheck disable=SC2015,SC2016,SC2034,SC1090,SC2329
set -u

# shellcheck source-path=SCRIPTDIR
. "$(dirname -- "$0")/lib.sh"
new_workdir >/dev/null

DOCKERFILE="${MAROTTE_DOCKERFILE:-$REPO_ROOT/Dockerfile}"
PATHS_GO="${MAROTTE_PATHS_GO:-$REPO_ROOT/internal/composition/paths.go}"
MAIN_GO="${MAROTTE_MAIN_GO:-$REPO_ROOT/main.go}"
for f in "$DOCKERFILE" "$PATHS_GO" "$MAIN_GO"; do
  [ -r "$f" ] || {
    printf 'harness error: %s is not readable\n' "$f" >&2
    exit 1
  }
done

load_function load_boot_paths

# stub <rc> <output>: a fake server binary that prints <output> for `paths` and refuses any
# other argv.
STUB="$WORK/marotte"
stub() {
  printf '%s' "$2" >"$WORK/stub.out"
  printf '#!/bin/sh\n[ "$#" -eq 1 ] && [ "$1" = paths ] || exit 64\ncat %s\nexit %s\n' \
    "$WORK/stub.out" "$1" >"$STUB"
  chmod +x "$STUB"
}

# quote <value>: <value> single-quoted, as the server prints it.
quote() {
  local q=${1//\'/\'\\\'\'}
  printf "'%s'" "$q"
}

odd="$WORK/my tools'x \$(false)"
stub 0 "CONFIG_DIR='$WORK/c'
TOOLS=$(quote "$odd")
export KIRO_HOME='$WORK/k'
export PATH='/t/bin:/usr/bin:/bin'
unset GOPATH
export GOBIN='/t/bin'
"
want="$WORK/c|$odd|$WORK/k|/t/bin:/usr/bin:/bin|unset|/t/bin"
got=$(
  GOPATH=/stale
  export GOPATH
  load_boot_paths "$STUB" || exit 1
  printf '%s|%s|' "$CONFIG_DIR" "$TOOLS"
  /bin/sh -c 'printf "%s|%s|%s|%s" "$KIRO_HOME" "$PATH" "${GOPATH-unset}" "$GOBIN"'
)
[ "$got" = "$want" ] \
  && ok "the server's folders reach this script and its children byte for byte, and an unset GOPATH stays unset" \
  || no "folders handed over" "got '$got', want '$want'"

stub 1 "CONFIG_DIR=/should-not-be-read
"
got=$(
  CONFIG_DIR=/before
  rc=0
  load_boot_paths "$STUB" 2>/dev/null || rc=$?
  printf '%s|%s' "$rc" "$CONFIG_DIR"
)
[ "$got" = "1|/before" ] \
  && ok "a refusal from the server fails the hand-off and sets nothing" \
  || no "refused hand-off" "got '$got', want '1|/before'"

# The boot stops on that refusal, before anything acts on a folder.
BLOCK=$(extract_range '^if ! load_boot_paths \/app\/marotte; then$' '^fi$') || exit 1
[ "$(grep -c . "$BLOCK")" -eq 4 ] || {
  printf 'harness error: the hand-off block is not the four lines expected:\n%s\n' "$(cat "$BLOCK")" >&2
  exit 1
}
rc=0
(
  load_boot_paths() { return 1; }
  sleep() { return 0; }
  . "$BLOCK"
  exit 0
) >/dev/null 2>&1 || rc=$?
[ "$rc" -eq 1 ] \
  && ok "the boot exits 1 when the server refuses its configuration" \
  || no "refused boot" "rc=$rc, want 1"

call=$(grep -n '^if ! load_boot_paths /app/marotte; then$' "$ENTRYPOINT" | cut -d: -f1)
mkdir_at=$(grep -n '^mkdir -p "\$CONFIG_DIR"' "$ENTRYPOINT" | cut -d: -f1)
dropin=$(grep -n '^if ! install_path_dropin; then$' "$ENTRYPOINT" | cut -d: -f1)
[ -n "$call" ] && [ -n "$mkdir_at" ] && [ -n "$dropin" ] && [ "$call" -lt "$mkdir_at" ] && [ "$mkdir_at" -lt "$dropin" ] \
  && ok "the boot takes the server's folders before its first mkdir and the login-shell drop-in" \
  || no "hand-off call site" "call at '${call}', mkdir at '${mkdir_at}', drop-in at '${dropin}'"

grep -qF '"$1" paths' "$ENTRYPOINT" \
  && grep -qx 'const PathsCommand = "paths"' "$PATHS_GO" \
  && grep -qF 'os.Args[1] == composition.PathsCommand' "$MAIN_GO" \
  && ok "the subcommand the boot runs is the one the server dispatches" \
  || no "paths subcommand" "entrypoint.sh, paths.go and main.go no longer agree on the 'paths' subcommand"

# The image's ENV is what the server rebases from.
grep -q '^ENV PATH="/config/tools/bin:/config/tools/go/bin:' "$DOCKERFILE" \
  && grep -qx 'ENV GOPATH="/config/tools/go"' "$DOCKERFILE" \
  && grep -qx 'ENV GOBIN="/config/tools/bin"' "$DOCKERFILE" \
  && grep -qx 'const imageToolsDir = "/config/tools"' "$PATHS_GO" \
  && ok "the Dockerfile's PATH, GOPATH and GOBIN name the tools tree the server rebases from" \
  || no "Dockerfile ENV" "the image's /config/tools entries and paths.go's imageToolsDir disagree"

# A tools dir with a space and a single quote must survive /etc/profile.d in sh and bash. stdin is
# /dev/null because bash sources ~/.bashrc when stdin is a socket.
load_function emit_path_dropin
dropin="$WORK/dropin.sh"
want="$odd/bin:/usr/bin:/bin"
PATH="$want" emit_path_dropin >"$dropin" || exit 1
[ -s "$dropin" ] || {
  printf 'harness error: emit_path_dropin wrote nothing\n' >&2
  exit 1
}
for sh in /bin/sh /bin/bash; do
  err="$WORK/dropin.err"
  got=$(/usr/bin/env -i "$sh" -c 'PATH=/usr/bin:/bin; . "$1"; printf %s "$PATH"' dropin "$dropin" </dev/null 2>"$err")
  [ "$got" = "$want" ] && [ ! -s "$err" ] \
    && ok "$sh sources the login-shell drop-in to the boot's PATH" \
    || no "$sh drop-in" "PATH '$got', want '$want'; stderr: $(cat "$err")"
done

report
