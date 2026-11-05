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
STATIC_GO="${MAROTTE_STATIC_GO:-$REPO_ROOT/internal/steering/static.go}"
for f in "$DOCKERFILE" "$PATHS_GO" "$MAIN_GO" "$STATIC_GO"; do
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
mkdir_at=$(grep -n '^if ! mkdir -p "\$CONFIG_DIR"' "$ENTRYPOINT" | cut -d: -f1)
dropin=$(grep -n '^offer_login_path "\$EUID"$' "$ENTRYPOINT" | cut -d: -f1)
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

# install_path_dropin against a scratch profile.d. install_case <mktemp fails: 0|1> <rm fails: 0|1>
# prints "<rc>|<the drop-in's text, or absent>|<stderr>".
load_function install_path_dropin
pd="$WORK/profile.d"
target="$pd/10-marotte-path.sh"
install_case() {
  (
    # shellcheck disable=SC2329 # the stubs shadow the commands install_path_dropin runs
    if [ "$1" -ne 0 ]; then mktemp() { return 1; }; fi
    # shellcheck disable=SC2329
    if [ "$2" -ne 0 ]; then rm() { return 1; }; fi
    rc=0
    install_path_dropin "$pd" 2>"$WORK/install.err" || rc=$?
    text=absent
    [ ! -e "$target" ] || text=$(<"$target")
    printf '%s|%s|%s' "$rc" "$text" "$(cat "$WORK/install.err")"
  )
}
current=$(emit_path_dropin)
[ -n "$current" ] || {
  printf 'harness error: emit_path_dropin printed nothing\n' >&2
  exit 1
}
mkdir -p "$pd"
printf 'PATH=stale\n' >"$target"
got=$(install_case 0 0)
[ "$got" = "0|$current|" ] && [ "$(stat -c %a "$target")" = 644 ] && [ "$(find "$pd" -mindepth 1 | wc -l)" -eq 1 ] \
  && ok "the drop-in replaces an earlier one with this boot's PATH, mode 644, leaving no temp file" \
  || no "drop-in write" "got '$got', mode $(stat -c %a "$target"), entries: $(ls -A "$pd")"
printf 'PATH=stale\n' >"$target"
got=$(install_case 1 0)
[ "$got" = "1|absent|" ] \
  && ok "a failed write removes an earlier boot's drop-in, so no login shell sources its PATH" \
  || no "failed write, earlier drop-in" "got '$got', want '1|absent|'"
emit_path_dropin >"$target"
got=$(install_case 1 0)
[ "$got" = "0|$current|" ] \
  && ok "a failed write over a drop-in already holding this boot's PATH keeps it and succeeds" \
  || no "failed write, current drop-in" "got '$got'"
printf 'PATH=stale\n' >"$target"
got=$(install_case 1 1)
case "$got" in
  "1|PATH=stale|WARNING: could not remove $target, written by an earlier boot; "*)
    ok "an earlier drop-in that cannot be removed is named in a warning"
    ;;
  *) no "failed write, earlier drop-in kept" "got '$got'" ;;
esac
rm -rf "$pd"
got=$(install_case 0 0)
[ "$got" = "1|absent|" ] \
  && ok "with no profile.d folder the drop-in reports failure and writes nothing" \
  || no "no profile.d" "got '$got', want '1|absent|'"

# Only root may write /etc/profile.d. Another uid tries nothing and names the full-path fallback.
load_function logfmt_value
load_function offer_login_path
TOOLS="$WORK/t"
# offer <install rc> <uid>: "<install calls>|<MAROTTE_PATH_DROPIN as a child sees it>|<stderr>" from
# one offer_login_path run that starts with a stale MAROTTE_PATH_DROPIN=1 exported.
offer() {
  (
    calls=0
    rc=$1
    install_path_dropin() {
      calls=$((calls + 1))
      [ "$#" -eq 1 ] && [ "$1" = /etc/profile.d ] || return 97
      return "$rc"
    }
    export MAROTTE_PATH_DROPIN=1
    offer_login_path "$2" 2>"$WORK/offer.err"
    printf '%s|%s|%s' "$calls" "$(/bin/sh -c 'printf %s "${MAROTTE_PATH_DROPIN-unset}"')" "$(cat "$WORK/offer.err")"
  )
}
got=$(offer 0 1000)
case "$got" in
  "0|unset|WARNING: running as uid 1000, not root, "*"does not search $TOOLS/bin, so run an engine-installed tool there by its full path ($TOOLS/bin/<tool>)")
    ok "a non-root boot writes no drop-in, tells the server so, and names the full-path fallback"
    ;;
  *) no "non-root boot" "got '$got'" ;;
esac
got=$(offer 0 0)
[ "$got" = "1|1|" ] \
  && ok "a root boot writes the drop-in, tells the server so, and warns about nothing" \
  || no "root boot" "got '$got', want '1|1|'"
got=$(offer 1 0)
case "$got" in
  "1|unset|WARNING: could not write /etc/profile.d/10-marotte-path.sh; "*"may not see $TOOLS/bin, so an engine-installed tool may not resolve by name there")
    ok "a root boot whose drop-in fails says so and does not tell the server it was written"
    ;;
  *) no "failed drop-in" "got '$got'" ;;
esac
# A rejected tools folder leaves TOOLS empty: no tools bin is named, and only a failed drop-in warns.
TOOLS=
got=$(offer 0 1000)
[ "$got" = "0|unset|" ] \
  && ok "a non-root boot with no tools folder warns about nothing" \
  || no "non-root boot, no tools folder" "got '$got', want '0|unset|'"
got=$(offer 1 0)
[ "$got" = "1|unset|WARNING: could not write /etc/profile.d/10-marotte-path.sh; a login shell (the browser shell runs bash --login) may not get the boot's PATH" ] \
  && ok "a failed drop-in with no tools folder warns without naming a tools bin" \
  || no "failed drop-in, no tools folder" "got '$got'"
TOOLS="$WORK/t"
grep -qx 'const dropInEnv = "MAROTTE_PATH_DROPIN"' "$STATIC_GO" \
  && ok "the variable offer_login_path exports is the one the server's steering reads" \
  || no "drop-in hand-off" "internal/steering/static.go no longer reads MAROTTE_PATH_DROPIN"
# The server rejects a tools folder holding a line break, leaving TOOLS empty; the warning does not
# rely on that.
TOOLS="$WORK/t"$'\n'"level=error msg=forged"
safe="$WORK/t?level=error msg=forged/bin"
for args in "0 1000" "1 0"; do
  # shellcheck disable=SC2086 # two words on purpose: the install rc and the uid
  got=$(offer $args)
  [ "$(printf '%s\n' "$got" | wc -l)" -eq 1 ] && case "$got" in *"$safe"*) true ;; *) false ;; esac \
    && ok "the warning for install rc and uid '$args' names a tools folder with a line break on one line" \
    || no "line break in the tools folder, '$args'" "got '$got'"
done
# The full-path fallback names a long tools folder whole: a truncated path would be a wrong one.
TOOLS="$WORK/$(printf 'd%.0s' {1..250})"
got=$(offer 0 1000)
case "$got" in
  *"($TOOLS/bin/<tool>)") ok "the fallback names a tools folder longer than 200 bytes in full" ;;
  *) no "long tools folder" "got '$got'" ;;
esac

# Every boot line naming a folder from the environment or a store entry goes through logfmt_value,
# in the top-level blocks no case above runs too.
raw=$(awk '/\\$/ { printf "%s ", $0; next } { print }' "$ENTRYPOINT" | grep -E '^[[:space:]]*printf ' \
  | grep -P '(?<!logfmt_value )"\$\{?(HOME|KIRO_HOME|TOOLS|kas_dir|kas_real|name)\b')
[ -z "$raw" ] \
  && ok "no boot log line prints a folder or store entry name raw" \
  || no "raw value in a boot log line" "$raw"

report
