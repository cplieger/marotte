#!/usr/bin/env bash
# Lint:
#   SC2015 - `[ cond ] && ok || no` cannot mis-fire: lib.sh's ok/no always return 0.
#   SC2016 - the call-site pattern matches the entrypoint's literal "$VAR" text.
# shellcheck disable=SC2015,SC2016
set -u

# shellcheck source-path=SCRIPTDIR
. "$(dirname -- "$0")/lib.sh"
new_workdir >/dev/null

load_function logfmt_value
load_function own_place
load_function at_place
load_function chmod_at
load_function normalize_modes

HOME_SUBDIRS=".local .local/share .local/share/kiro-cli .cache .cache/go-build .docker .docker/cli-plugins"

H="$WORK/home"
T="$WORK/tools"
for d in $HOME_SUBDIRS .ssh .kiro; do
  mkdir -p "$H/$d" || exit 1
done
mkdir -p "$T/kiro-cli-versions" || exit 1
chmod 0777 "$H" "$T/kiro-cli-versions" || exit 1
for d in $HOME_SUBDIRS .kiro; do
  chmod 0775 "$H/$d" || exit 1
done
chmod 0755 "$H/.ssh" || exit 1
[ "$(stat -c %a "$H/.docker/cli-plugins")" = 775 ] || {
  printf 'harness error: fixture modes did not take\n' >&2
  exit 1
}

rc=0
normalize_modes "$H" "$H/.kiro" "$T" >"$WORK/out.log" 2>"$WORK/err.log" || rc=$?
[ "$rc" -eq 0 ] && [ ! -s "$WORK/out.log" ] && [ ! -s "$WORK/err.log" ] \
  && ok "the pass returns 0 and prints nothing" \
  || no "status" "rc=$rc: $(cat "$WORK/out.log" "$WORK/err.log")"
[ "$(stat -c %a "$H")" = 755 ] && [ "$(stat -c %a "$H/.ssh")" = 700 ] \
  && ok "HOME becomes 755 and ~/.ssh 700" \
  || no "home and ssh" "home $(stat -c %a "$H"), .ssh $(stat -c %a "$H/.ssh")"
for d in $HOME_SUBDIRS .kiro; do
  mode=$(stat -c %a "$H/$d")
  [ "$mode" = 755 ] \
    && ok "HOME/$d loses group write" \
    || no "HOME/$d" "mode $mode, want 755"
done
[ "$(stat -c %a "$T/kiro-cli-versions")" = 755 ] \
  && ok "kiro-cli-versions loses group and other write" \
  || no "kiro-cli-versions" "mode $(stat -c %a "$T/kiro-cli-versions")"

# Planted symlinks: each chmod by path would land on the outside tree.
H2="$WORK/home2"
T2="$WORK/tools2"
O="$WORK/outside"
mkdir -p "$H2/.local/share/kiro-cli" "$H2/.cache/go-build" "$T2" \
  "$O/docker/cli-plugins" "$O/ssh" "$O/kiro" "$O/versions" || exit 1
ln -s -- "$O/docker" "$H2/.docker" || exit 1
ln -s -- "$O/ssh" "$H2/.ssh" || exit 1
ln -s -- "$O/kiro" "$H2/.kiro" || exit 1
ln -s -- "$O/versions" "$T2/kiro-cli-versions" || exit 1
chmod 0775 "$O/docker" "$O/docker/cli-plugins" "$O/ssh" "$O/kiro" "$O/versions" "$H2/.cache" || exit 1
normalize_modes "$H2" "$H2/.kiro" "$T2" 2>"$WORK/err.log"
modes="$(stat -c %a "$O/docker" "$O/docker/cli-plugins" "$O/ssh" "$O/kiro" "$O/versions" | tr '\n' ' ')"
[ "$modes" = "775 775 775 775 775 " ] \
  && ok "no chmod follows a symlink planted in HOME or the tools dir" \
  || no "planted symlinks" "outside modes: $modes"
[ "$(stat -c %a "$H2/.cache")" = 755 ] \
  && ok "the real siblings of planted symlinks are still tightened" \
  || no "real sibling" ".cache $(stat -c %a "$H2/.cache")"

# HOME and a KIRO_HOME outside it are the operator's values: a symlink there is followed.
H3="$WORK/home3-link"
O3="$WORK/home3"
K3="$WORK/kiro3-link"
OK3="$WORK/kiro3"
mkdir -p "$O3/.ssh" "$OK3" || exit 1
ln -s -- "$O3" "$H3" || exit 1
ln -s -- "$OK3" "$K3" || exit 1
chmod 0777 "$O3" "$O3/.ssh" "$OK3" || exit 1
normalize_modes "$H3" "$K3" "$WORK/tools3" 2>"$WORK/err.log"
[ "$(stat -c %a "$O3")" = 755 ] && [ "$(stat -c %a "$O3/.ssh")" = 700 ] && [ "$(stat -c %a "$OK3")" = 755 ] \
  && ok "a symlinked HOME and an outside KIRO_HOME are set at the folders they name" \
  || no "operator links" "home $(stat -c %a "$O3"), .ssh $(stat -c %a "$O3/.ssh"), kiro $(stat -c %a "$OK3")"

# HOME is a symlink when cd follows it and a real directory by the place check.
H4="$WORK/home4"
O4="$WORK/outside-cd"
T4="$WORK/tools4"
mkdir -p "$O4" "$T4/kiro-cli-versions" || exit 1
ln -s -- "$O4" "$T4/kiro-cli-versions.swap" || exit 1
chmod 0775 "$O4" || exit 1
mkdir -p "$H4" || exit 1
# shellcheck disable=SC2329 # called by the loaded chmod_at
cd() {
  if [ "$1" = -- ] && [ "$2" = "$T4/kiro-cli-versions" ] && [ -L "$T4/kiro-cli-versions.swap" ]; then
    rmdir -- "$T4/kiro-cli-versions" && mv -T -- "$T4/kiro-cli-versions.swap" "$T4/kiro-cli-versions" || return 1
    builtin cd "$@" || return
    rm -f -- "$T4/kiro-cli-versions" && mkdir -- "$T4/kiro-cli-versions" || return 1
    return 0
  fi
  builtin cd "$@" || return
}
normalize_modes "$H4" "$H4/.kiro" "$T4" 2>"$WORK/err.log"
unset -f cd
[ ! -L "$T4/kiro-cli-versions" ] && [ "$(stat -c %a "$O4")" = 775 ] \
  && ok "a swap between cd and the place check leaves the outside tree alone" \
  || no "cd swap" "link $([ -L "$T4/kiro-cli-versions" ] && echo left || echo replaced), outside $(stat -c %a "$O4")"

# ~/.cache becomes a symlink to an outside tree right before chmod runs.
H6="$WORK/home6"
O6="$WORK/outside-swap"
mkdir -p "$H6/.cache" "$O6" || exit 1
chmod 0775 "$H6/.cache" "$O6" || exit 1
# shellcheck disable=SC2329 # called by the loaded chmod_at
chmod() {
  if [ "$1" = go-w ] && [ -d "$H6/.cache" ] && [ ! -L "$H6/.cache" ]; then
    mv -- "$H6/.cache" "$H6/.cache.moved" && ln -s -- "$O6" "$H6/.cache" || return 1
  fi
  command chmod "$@"
}
normalize_modes "$H6" "$H6/.kiro" "$WORK/tools6" 2>"$WORK/err.log"
unset -f chmod
[ -L "$H6/.cache" ] && [ "$(stat -c %a "$O6")" = 775 ] && [ "$(stat -c %a "$H6/.cache.moved")" = 755 ] \
  && ok "a swap after the place check cannot redirect chmod; the checked folder is set" \
  || no "swap race" "outside $(stat -c %a "$O6"), checked dir $(stat -c %a "$H6/.cache.moved" 2>&1)"

H5="$WORK/home\"5"
mkdir -p "$H5" || exit 1
# shellcheck disable=SC2329 # called by the loaded chmod_at
chmod() { return 1; }
rc=0
normalize_modes "$H5" "$H5/.kiro" "$WORK/tools5" 2>"$WORK/err.log" || rc=$?
unset -f chmod
[ "$rc" -eq 0 ] && grep -qx "level=warn msg=\"could not set the mode of a folder\" dir=\"$WORK/home'5\" mode=755 component=entrypoint" "$WORK/err.log" \
  && ok "a failed chmod is warned about, with the folder name made safe, and the boot goes on" \
  || no "chmod failure" "rc=$rc, $(cat "$WORK/err.log")"

EMPTY="$WORK/empty"
mkdir -p "$EMPTY" || exit 1
rc=0
normalize_modes "$WORK/missing-home" "$WORK/missing-home/.kiro" "$EMPTY/tools" 2>"$WORK/err.log" || rc=$?
[ "$rc" -eq 0 ] && [ -z "$(ls -A "$EMPTY")" ] && [ ! -e "$WORK/missing-home" ] && [ ! -s "$WORK/err.log" ] \
  && ok "missing folders are neither created nor reported" \
  || no "missing folders" "rc=$rc, created: $(ls -A "$EMPTY"), err: $(cat "$WORK/err.log")"

call=$(grep -n '^normalize_modes "$HOME" "$KIRO_HOME" "$TOOLS"$' "$ENTRYPOINT" | cut -d: -f1)
start=$(grep -n '^exec ' "$ENTRYPOINT" | tail -1 | cut -d: -f1)
[ -n "$call" ] && [ -n "$start" ] && [ "$call" -lt "$start" ] \
  && ok "the boot normalizes HOME and the kiro-cli install root before it starts the server" \
  || no "call site" "normalize_modes call at line '${call}', exec at '${start}'"
! grep -qE '^chmod ' "$ENTRYPOINT" \
  && ok "no top-level chmod by path is left in the boot" \
  || no "top-level chmod" "$(grep -nE '^chmod ' "$ENTRYPOINT")"

report
