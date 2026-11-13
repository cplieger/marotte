#!/usr/bin/env bash
# restore_tmp_mode(): a /tmp some process recreated 0755 is put back to 1777 at boot, in place, so
# its files survive; a correct or symlinked /tmp is not touched. Lint:
#   SC2015 - `[ cond ] && ok || no` cannot mis-fire: lib.sh's ok/no always return 0.
#   SC2329 - the mkdir and chmod stubs are called only by the runtime-sourced function.
# shellcheck disable=SC2015,SC2329
set -u

# shellcheck source-path=SCRIPTDIR
. "$(dirname -- "$0")/lib.sh"
new_workdir >/dev/null

load_function logfmt_value
load_function restore_tmp_mode

FAILED_MSG='could not restore the temp folder to mode 1777; a process running as another uid may fail to write temporary files'

# fixture <name> <mode>: a folder at <mode> holding a file and a subfolder, the contents a fix must keep.
fixture() {
  DIR="$WORK/$1"
  mkdir -p "$DIR/sub" && printf 'kept\n' >"$DIR/sub/file" && chmod "$2" "$DIR" || {
    printf 'harness error: could not build the %s fixture\n' "$1" >&2
    exit 1
  }
  [ "$(stat -c %a "$DIR")" = "$2" ] || {
    printf 'harness error: the %s fixture reads mode %s, want %s\n' "$1" "$(stat -c %a "$DIR")" "$2" >&2
    exit 1
  }
}

kept() { [ "$(cat "$DIR/sub/file" 2>/dev/null)" = kept ]; }

probe() {
  rc=0
  restore_tmp_mode "$1" >"$WORK/out.log" 2>"$WORK/err.log" || rc=$?
}

fixture wrong 755
probe "$DIR"
[ "$rc" -eq 0 ] && [ "$(stat -c %a "$DIR")" = 1777 ] && kept && [ ! -s "$WORK/out.log" ] \
  && grep -Fqx "level=warn msg=\"the temp folder had the wrong mode; restored it to 1777\" path=\"$DIR\" was=755 component=entrypoint" "$WORK/err.log" \
  && ok "a 755 folder is restored to 1777 with its contents kept, warning was=755" \
  || no "wrong mode" "rc=$rc mode=$(stat -c %a "$DIR"): $(cat "$WORK/err.log")"

fixture setgid 3777
probe "$DIR"
[ "$rc" -eq 0 ] && [ "$(stat -c %a "$DIR")" = 1777 ] && kept \
  && grep -Fqx "level=warn msg=\"the temp folder had the wrong mode; restored it to 1777\" path=\"$DIR\" was=3777 component=entrypoint" "$WORK/err.log" \
  && ok "a setgid 3777 folder loses its setgid bit and reads 1777, warning was=3777" \
  || no "setgid folder" "rc=$rc mode=$(stat -c %a "$DIR"): $(cat "$WORK/err.log")"

fixture right 1777
(
  mkdir() {
    : >"$WORK/stub.called"
    return 1
  }
  chmod() {
    : >"$WORK/stub.called"
    return 1
  }
  restore_tmp_mode "$DIR"
) >"$WORK/out.log" 2>"$WORK/err.log"
rc=$?
[ "$rc" -eq 0 ] && [ ! -e "$WORK/stub.called" ] && [ "$(stat -c %a "$DIR")" = 1777 ] && kept \
  && [ ! -s "$WORK/out.log" ] && [ ! -s "$WORK/err.log" ] \
  && ok "a 1777 folder is left alone: no mkdir or chmod, no output, contents kept" \
  || no "correct mode" "rc=$rc stub called: $([ -e "$WORK/stub.called" ] && printf yes || printf no): $(cat "$WORK/err.log")"

DIR="$WORK/missing"
probe "$DIR"
[ "$rc" -eq 0 ] && [ -d "$DIR" ] && [ "$(stat -c %a "$DIR" 2>/dev/null)" = 1777 ] \
  && grep -Fqx "level=warn msg=\"the temp folder was missing; created it at mode 1777\" path=\"$DIR\" was=missing component=entrypoint" "$WORK/err.log" \
  && ok "a missing folder is created at 1777, warning that it was missing" \
  || no "missing folder" "rc=$rc: $(cat "$WORK/err.log")"

# A chmod that exits 0 yet leaves the mode: the verdict must come from the mode read back.
fixture unchanged 700
(
  chmod() {
    printf '%s\n' "$*" >"$WORK/chmod.argv"
    return 0
  }
  restore_tmp_mode "$DIR"
) >"$WORK/out.log" 2>"$WORK/err.log"
rc=$?
[ "$rc" -eq 0 ] && [ "$(cat "$WORK/chmod.argv" 2>/dev/null)" = "01777 $DIR" ] \
  && grep -Fqx "level=warn msg=\"$FAILED_MSG\" path=\"$DIR\" was=700 now=700 component=entrypoint" "$WORK/err.log" \
  && [ "$(stat -c %a "$DIR")" = 700 ] && kept \
  && ok "a chmod 01777 that leaves 700 warns the restore failed, was=700 now=700, and returns 0" \
  || no "failed restore" "rc=$rc argv=$(cat "$WORK/chmod.argv" 2>/dev/null): $(cat "$WORK/err.log")"

FILE="$WORK/file"
{ printf 'kept\n' >"$FILE" && chmod 1777 "$FILE" && [ "$(stat -c %a "$FILE")" = 1777 ]; } || {
  printf 'harness error: could not build a regular file at mode 1777\n' >&2
  exit 1
}
probe "$FILE"
[ "$rc" -eq 0 ] && [ -f "$FILE" ] && [ "$(cat "$FILE")" = kept ] \
  && grep -Fqx "level=warn msg=\"$FAILED_MSG\" path=\"$FILE\" was=1777 now=1777 component=entrypoint" "$WORK/err.log" \
  && ok "a regular file at 1777 warns the restore failed and stays a file" \
  || no "regular file" "rc=$rc: $(cat "$WORK/err.log")"

fixture target 755
LINK="$WORK/link"
ln -s "$DIR" "$LINK" || exit 1
probe "$LINK"
[ "$rc" -eq 0 ] && [ -L "$LINK" ] && [ "$(stat -c %a "$DIR")" = 755 ] && kept \
  && [ ! -s "$WORK/out.log" ] && [ ! -s "$WORK/err.log" ] \
  && ok "a symlinked folder is left alone, its 755 target unchanged" \
  || no "symlink" "rc=$rc target mode=$(stat -c %a "$DIR"): $(cat "$WORK/err.log")"

call=$(grep -n '^restore_tmp_mode /tmp$' "$ENTRYPOINT" | cut -d: -f1)
paths=$(grep -n '^if ! load_boot_paths /app/marotte; then$' "$ENTRYPOINT" | cut -d: -f1)
[ -n "$call" ] && [ -n "$paths" ] && [ "$call" -lt "$paths" ] \
  && ok "the boot restores /tmp before anything else runs" \
  || no "call site" "restore_tmp_mode /tmp at line '${call}', load_boot_paths at '${paths}'"

report
