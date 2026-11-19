#!/usr/bin/env bash
# load_boot_paths() hands the server's own folders (`marotte paths`) to the boot; what the server
# prints is internal/composition's paths_test.go. Lint:
#   SC2015 - `[ cond ] && ok || no` cannot mis-fire: lib.sh's ok/no always return 0.
#   SC2016 - the grep patterns and the stub's script match literal "$VAR" text.
#   SC2034 - CONFIG_DIR and TOOLS are set for the extracted code to read or overwrite.
#   SC1090 - the sourced path is produced by extract_range at runtime.
#   SC2329 - the load_boot_paths/sleep stubs are called only by that runtime-sourced code.
#   SC2030, SC2031 - each $(...) case sets MAROTTE_LOGIN_PATH for its own subshell only.
# shellcheck disable=SC2015,SC2016,SC2034,SC1090,SC2329,SC2030,SC2031
set -u

# shellcheck source-path=SCRIPTDIR
. "$(dirname -- "$0")/lib.sh"
new_workdir >/dev/null

DOCKERFILE="${MAROTTE_DOCKERFILE:-$REPO_ROOT/Dockerfile}"
PATHS_GO="${MAROTTE_PATHS_GO:-$REPO_ROOT/internal/composition/paths.go}"
MAIN_GO="${MAROTTE_MAIN_GO:-$REPO_ROOT/main.go}"
STATIC_GO="${MAROTTE_STATIC_GO:-$REPO_ROOT/internal/steering/static.go}"
DROPIN="${MAROTTE_LOGIN_DROPIN:-$REPO_ROOT/login-path.sh}"
for f in "$DOCKERFILE" "$PATHS_GO" "$MAIN_GO" "$STATIC_GO" "$DROPIN"; do
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
want="$WORK/c|$odd|$WORK/k|/t/bin:/usr/bin:/bin|unset|/t/bin|/t/bin:/usr/bin:/bin"
got=$(
  GOPATH=/stale
  MAROTTE_LOGIN_PATH=/stale/bin
  export GOPATH MAROTTE_LOGIN_PATH
  load_boot_paths "$STUB" || exit 1
  printf '%s|%s|' "$CONFIG_DIR" "$TOOLS"
  /bin/sh -c 'printf "%s|%s|%s|%s|%s" "$KIRO_HOME" "$PATH" "${GOPATH-unset}" "$GOBIN" "${MAROTTE_LOGIN_PATH-unset}"'
)
[ "$got" = "$want" ] \
  && ok "the server's folders reach this script and its children byte for byte, an unset GOPATH stays unset, and children get the boot's PATH as MAROTTE_LOGIN_PATH" \
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

stub 0 "CONFIG_DIR='$WORK/c'
export PATH='/t/bin:/bin'
false
"
got=$(
  unset MAROTTE_LOGIN_PATH
  rc=0
  load_boot_paths "$STUB" 2>/dev/null || rc=$?
  printf '%s|%s' "$rc" "${MAROTTE_LOGIN_PATH-unset}"
)
[ "$got" = "1|unset" ] \
  && ok "a server answer whose last line fails fails the hand-off and hands no login PATH on" \
  || no "failed hand-off" "got '$got', want '1|unset'"

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
mkdir_at=$(grep -n '^if ! mkdir -p "\$CONFIG_DIR"' "$ENTRYPOINT" | cut -d: -f1)
[ -n "$call" ] && [ -n "$mkdir_at" ] && [ "$call" -lt "$mkdir_at" ] \
  && ok "the boot takes the server's folders before its first mkdir" \
  || no "hand-off call site" "call at '${call}', mkdir at '${mkdir_at}'"

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

# Debian's /etc/profile assigns PATH, root's system folders or another uid's, and then sources the
# image's drop-in. login_path <sh> <assigned PATH> [NAME=value...] prints the PATH a login shell
# ends with from an environment holding only the given variables. stdin is /dev/null because bash
# sources ~/.bashrc when stdin is a socket.
login_path() {
  local sh=$1 assigned=$2
  shift 2
  /usr/bin/env -i "$@" "$sh" -c 'PATH=$1; export PATH; . "$2"; printf %s "$PATH"' login "$assigned" "$DROPIN" \
    </dev/null 2>"$WORK/login.err"
}
image_path=$(sed -n 's/^ENV PATH="\(.*\)"$/\1/p' "$DOCKERFILE" | tail -n 1) # the shipped stage's
[ -n "$image_path" ] || {
  printf 'harness error: no ENV PATH="..." line in %s\n' "$DOCKERFILE" >&2
  exit 1
}
boot_path="$odd/bin:$odd/go/bin:/usr/bin:/bin"
for sh in /bin/sh /bin/bash; do
  for profile in "root|/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin" \
    "uid 1000|/usr/local/bin:/usr/bin:/bin:/usr/local/games:/usr/games"; do
    who=${profile%%|*}
    got=$(login_path "$sh" "${profile#*|}" MAROTTE_LOGIN_PATH="$boot_path")
    [ "$got" = "$boot_path" ] && [ ! -s "$WORK/login.err" ] \
      && ok "a $who login shell under $sh gets the boot's PATH from MAROTTE_LOGIN_PATH, a tools folder with a space and a quote included" \
      || no "$who $sh login shell, boot PATH" "PATH '$got', want '$boot_path'; stderr: $(cat "$WORK/login.err")"
    got=$(login_path "$sh" "${profile#*|}")
    [ "$got" = "$image_path" ] && [ ! -s "$WORK/login.err" ] \
      && ok "a $who login shell under $sh started outside the server's tree gets the image's PATH" \
      || no "$who $sh login shell, no hand-off" "PATH '$got', want '$image_path'; stderr: $(cat "$WORK/login.err")"
  done
  got=$(/usr/bin/env -i "$sh" -c '. "$1"; /bin/sh -c "printf %s \"\${KIRO_NO_AUTO_UPDATE-unset}\""' dropin "$DROPIN" </dev/null 2>"$WORK/login.err")
  [ "$got" = 1 ] && [ ! -s "$WORK/login.err" ] \
    && ok "$sh sources the login-shell drop-in to an exported KIRO_NO_AUTO_UPDATE=1 from an empty environment" \
    || no "$sh drop-in self-update switch" "a child saw KIRO_NO_AUTO_UPDATE '$got', want 1; stderr: $(cat "$WORK/login.err")"
done

# Debian's /etc/profile sources only the profile.d names its run-parts filter matches.
installed=$(sed -n 's|^COPY login-path\.sh /etc/profile\.d/\([^/]*\)$|\1|p' "$DOCKERFILE")
[[ "$installed" =~ ^[a-zA-Z0-9_][a-zA-Z0-9._-]*\.sh$ ]] \
  && ok "the image installs the drop-in under a profile.d name /etc/profile sources" \
  || no "drop-in install" "the Dockerfile installs login-path.sh as '/etc/profile.d/$installed'"
grep -qx 'const loginPathEnv = "MAROTTE_LOGIN_PATH"' "$STATIC_GO" \
  && ok "the variable load_boot_paths exports is the one the server's steering reads" \
  || no "login PATH hand-off" "internal/steering/static.go no longer reads MAROTTE_LOGIN_PATH"

# Every boot line naming a folder from the environment or a store entry goes through logfmt_value,
# in the top-level blocks no case above runs too.
raw=$(awk '/\\$/ { printf "%s ", $0; next } { print }' "$ENTRYPOINT" | grep -E '^[[:space:]]*printf ' \
  | grep -P '(?<!logfmt_value )"\$\{?(HOME|KIRO_HOME|TOOLS|kas_dir|kas_real|name)\b')
[ -z "$raw" ] \
  && ok "no boot log line prints a folder or store entry name raw" \
  || no "raw value in a boot log line" "$raw"

report
