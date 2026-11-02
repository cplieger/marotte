#!/usr/bin/env bash
# tighten_engine_roots(): a host ACL or umask can stamp the tools engine's roots group- or
# other-writable, and the engine then refuses to start. The boot drops those bits from every real
# directory it judges, leaves a symlink for the engine to name and creates nothing. Lint:
#   SC2015 - `[ cond ] && ok || no` cannot mis-fire: lib.sh's ok/no always return 0.
#   SC2016 - the call-site pattern matches the entrypoint's literal "$VAR" text.
# shellcheck disable=SC2015,SC2016
set -u

# shellcheck source-path=SCRIPTDIR
. "$(dirname -- "$0")/lib.sh"
new_workdir >/dev/null

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
[ "$rc" -eq 0 ] && [ ! -s "$WORK/err.log" ] \
  && ok "the pass returns 0 and prints nothing" \
  || no "status" "rc=$rc: $(cat "$WORK/err.log")"

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
