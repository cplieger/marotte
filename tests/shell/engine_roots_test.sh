#!/usr/bin/env bash
# Lint:
#   SC2015 - `[ cond ] && ok || no` cannot mis-fire: lib.sh's ok/no always return 0.
#   SC2016 - the call-site pattern matches the entrypoint's literal "$VAR" text.
# shellcheck disable=SC2015,SC2016
set -u

# shellcheck source-path=SCRIPTDIR
. "$(dirname -- "$0")/lib.sh"
new_workdir >/dev/null

load_function own_place
load_function tighten_dir
load_function tighten_engine_roots

CONFIG="$WORK/config"
TOOLS="$CONFIG/tools"
OUTSIDE="$WORK/outside"
JUDGED="bin opt npm npm/bin npm/lib npm/lib/node_modules python python/bin python/tools"
for d in $JUDGED kiro-cli-versions; do
  mkdir -p "$TOOLS/$d" || exit 1
done
mkdir -p "$OUTSIDE" || exit 1
ln -s "$OUTSIDE" "$TOOLS/python/interpreters" || exit 1
chmod 0777 "$CONFIG" "$TOOLS" "$TOOLS/kiro-cli-versions" "$OUTSIDE" || exit 1
for d in $JUDGED; do
  chmod 0775 "$TOOLS/$d" || exit 1
done
[ "$(stat -c %a "$TOOLS/npm/lib/node_modules")" = 775 ] || {
  printf 'harness error: fixture modes did not take\n' >&2
  exit 1
}

rc=0
tighten_engine_roots "$CONFIG" "$TOOLS" >"$WORK/out.log" 2>"$WORK/err.log" || rc=$?
[ "$rc" -eq 0 ] && [ ! -s "$WORK/out.log" ] \
  && ok "the pass returns 0 and prints nothing on stdout" \
  || no "status" "rc=$rc: $(cat "$WORK/out.log")"

warned() {
  grep -c "^level=warn msg=\"a folder the tools engine checks let other accounts write; removing that, but anything they wrote there stays\" dir=\"$1\" mode=$2 component=entrypoint\$" "$WORK/err.log"
}
[ "$(wc -l <"$WORK/err.log")" -eq 11 ] && [ "$(warned "$CONFIG" 777)" = 1 ] \
  && [ "$(warned "$TOOLS" 777)" = 1 ] && [ "$(warned "$TOOLS/npm/lib/node_modules" 775)" = 1 ] \
  && ok "each of the 11 writable judged dirs is named once with the mode it had" \
  || no "warnings" "$(cat "$WORK/err.log")"
! grep -q -e 'interpreters' -e 'kiro-cli-versions' "$WORK/err.log" \
  && ok "no warning names a directory left alone" \
  || no "spurious warning" "$(cat "$WORK/err.log")"

[ "$(stat -c %a "$CONFIG")" = 755 ] && [ "$(stat -c %a "$TOOLS")" = 755 ] \
  && ok "the config and tools dirs lose group and other write" \
  || no "roots" "config $(stat -c %a "$CONFIG"), tools $(stat -c %a "$TOOLS")"

for d in $JUDGED; do
  mode=$(stat -c %a "$TOOLS/$d")
  [ "$mode" = 755 ] \
    && ok "$d loses group write" \
    || no "$d" "mode $mode, want 755"
done

[ "$(stat -c %a "$OUTSIDE")" = 777 ] \
  && ok "a symlinked interpreters dir is not followed out of the tree" \
  || no "symlink" "its target became $(stat -c %a "$OUTSIDE")"

[ "$(stat -c %a "$TOOLS/kiro-cli-versions")" = 777 ] \
  && ok "a directory the engine does not judge is left alone" \
  || no "unjudged dir" "kiro-cli-versions became $(stat -c %a "$TOOLS/kiro-cli-versions")"

# A symlinked npm: chmod "$tools/npm/lib" would resolve through it to the outside tree.
C2="$WORK/c2"
T2="$C2/tools"
O2="$WORK/outside-npm"
mkdir -p "$T2/bin" "$T2/python" "$O2/bin" "$O2/lib/node_modules" || exit 1
ln -s "$O2" "$T2/npm" || exit 1
chmod 0775 "$T2/bin" "$T2/python" "$O2" "$O2/bin" "$O2/lib" "$O2/lib/node_modules" || exit 1
rc=0
tighten_engine_roots "$C2" "$T2" 2>"$WORK/err.log" || rc=$?
modes="$(stat -c %a "$O2" "$O2/bin" "$O2/lib" "$O2/lib/node_modules" | tr '\n' ' ')"
[ "$rc" -eq 0 ] && [ "$modes" = "775 775 775 775 " ] \
  && ok "nothing beneath a symlinked npm is chmodded through it" \
  || no "symlinked parent" "rc=$rc, outside modes: $modes"
[ "$(stat -c %a "$T2/bin")" = 755 ] && [ "$(stat -c %a "$T2/python")" = 755 ] \
  && ok "the real siblings of a symlinked npm are still tightened" \
  || no "siblings" "bin $(stat -c %a "$T2/bin"), python $(stat -c %a "$T2/python")"

# A symlinked tools dir: the engine refuses it, and every child would resolve outside.
C3="$WORK/c3"
O3="$WORK/outside-tools"
mkdir -p "$C3" "$O3/bin" "$O3/npm/lib" || exit 1
ln -s "$O3" "$C3/tools" || exit 1
chmod 0775 "$C3" "$O3" "$O3/bin" "$O3/npm" "$O3/npm/lib" || exit 1
rc=0
tighten_engine_roots "$C3" "$C3/tools" 2>"$WORK/err.log" || rc=$?
modes="$(stat -c %a "$O3" "$O3/bin" "$O3/npm" "$O3/npm/lib" | tr '\n' ' ')"
[ "$rc" -eq 0 ] && [ "$modes" = "775 775 775 775 " ] && [ "$(stat -c %a "$C3")" = 755 ] \
  && ok "a symlinked tools dir is not followed, and the config dir is still tightened" \
  || no "symlinked tools" "rc=$rc, config $(stat -c %a "$C3"), outside modes: $modes"

C5="$WORK/c5"
mkdir -p "$C5/tools" || exit 1
: >"$C5/tools/opt" || exit 1
chmod 0666 "$C5/tools/opt" || exit 1
tighten_engine_roots "$C5" "$C5/tools" 2>"$WORK/err.log"
[ "$(stat -c %a "$C5/tools/opt")" = 666 ] \
  && ok "a regular file where a judged directory belongs is left for the engine to name" \
  || no "file in a judged slot" "opt became $(stat -c %a "$C5/tools/opt")"

C6="$WORK/c6"
O6="$WORK/outside-config"
mkdir -p "$O6" || exit 1
ln -s "$O6" "$C6" || exit 1
chmod 0775 "$O6" || exit 1
tighten_engine_roots "$C6" "$C6/tools" 2>"$WORK/err.log"
[ "$(stat -c %a "$O6")" = 775 ] \
  && ok "a symlinked config dir is not followed" \
  || no "symlinked config" "its target became $(stat -c %a "$O6")"

# An empty tools path would otherwise turn "$tools/bin" into /bin. The stub records argv
# and the directory it ran in instead of touching the host's root directories.
C4="$WORK/c4"
mkdir -p "$C4" || exit 1
chmod 0775 "$C4" || exit 1
: >"$WORK/chmod.log"
# shellcheck disable=SC2329 # called by the loaded tighten_dir
chmod() { printf '%s %s\n' "$(pwd -P)" "$*" >>"$WORK/chmod.log"; }
rc=0
tighten_engine_roots "$C4" "" 2>"$WORK/err.log" || rc=$?
unset -f chmod
[ "$rc" -eq 0 ] && [ "$(cat "$WORK/chmod.log")" = "$(realpath "$C4") go-w ." ] \
  && ok "an empty tools path touches only the config dir" \
  || no "empty tools path" "rc=$rc, chmod calls: $(tr '\n' '|' <"$WORK/chmod.log")"

# The config dir becomes a symlink to an outside tree right before chmod runs.
C7="$WORK/c7"
O7="$WORK/outside-swap"
mkdir -p "$C7" "$O7" || exit 1
chmod 0775 "$C7" "$O7" || exit 1
# shellcheck disable=SC2329 # called by the loaded tighten_dir
chmod() {
  if [ ! -L "$C7" ]; then
    mv -- "$C7" "$C7.moved" && ln -s -- "$O7" "$C7" || return 1
  fi
  command chmod "$@"
}
tighten_engine_roots "$C7" "$C7/tools" 2>"$WORK/err.log"
unset -f chmod
[ -L "$C7" ] && [ "$(stat -c %a "$O7")" = 775 ] && [ "$(stat -c %a "$C7.moved")" = 755 ] \
  && ok "a swap after the place check cannot redirect chmod; the checked directory is tightened" \
  || no "swap race" "swapped $([ -L "$C7" ] && echo yes || echo no), outside $(stat -c %a "$O7"), checked dir $(stat -c %a "$C7.moved" 2>&1)"

C8="$WORK/c8"
mkdir -p "$C8" || exit 1
chmod 0775 "$C8" || exit 1
# shellcheck disable=SC2329 # called by the loaded tighten_dir
chmod() { return 1; }
rc=0
tighten_engine_roots "$C8" "$C8/tools" 2>"$WORK/err.log" || rc=$?
unset -f chmod
[ "$rc" -eq 0 ] && grep -qx "level=warn msg=\"could not remove group and other write; the tools engine will refuse this folder\" dir=\"$C8\" component=entrypoint" "$WORK/err.log" \
  && ok "a failed chmod is warned about and the boot goes on" \
  || no "chmod failure" "rc=$rc, $(cat "$WORK/err.log")"

tighten_engine_roots "$CONFIG" "$TOOLS" >"$WORK/out.log" 2>"$WORK/err.log"
[ ! -s "$WORK/out.log" ] && [ ! -s "$WORK/err.log" ] \
  && ok "a second pass over tightened dirs is silent" \
  || no "second pass" "$(cat "$WORK/out.log" "$WORK/err.log")"

EMPTY="$WORK/empty"
mkdir -p "$EMPTY" || exit 1
tighten_engine_roots "$WORK/missing-config" "$EMPTY/tools" 2>"$WORK/err.log"
rc=$?
[ "$rc" -eq 0 ] && [ -z "$(ls -A "$EMPTY")" ] && [ ! -e "$WORK/missing-config" ] && [ ! -s "$WORK/err.log" ] \
  && ok "missing roots are neither created nor reported" \
  || no "missing roots" "rc=$rc, created: $(ls -A "$EMPTY"), err: $(cat "$WORK/err.log")"

call=$(grep -n '^tighten_engine_roots "$CONFIG_DIR" "$TOOLS"$' "$ENTRYPOINT" | cut -d: -f1)
start=$(grep -n '^exec ' "$ENTRYPOINT" | tail -1 | cut -d: -f1)
[ -n "$call" ] && [ -n "$start" ] && [ "$call" -lt "$start" ] \
  && ok "the boot tightens the engine's roots before it starts the server" \
  || no "call site" "tighten_engine_roots call at line '${call}', exec at '${start}'"

report
