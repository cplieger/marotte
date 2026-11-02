#!/usr/bin/env bash
# refuse_held_roots(): a container started on folders a running marotte holds must stop before the
# entrypoint's deletions (the legacy migration, the KAS runtime prune) touch the live tree. The
# holder is played by this shell: an fd on the folder or its marotte.lock with its flock taken,
# which conflicts with the probe's own flock(1) process. Lint:
#   SC2015 - `[ cond ] && ok || no` cannot mis-fire: lib.sh's ok/no always return 0.
#   SC2016 - the call-site pattern matches the entrypoint's literal "$VAR" text.
#   SC2329 - the flock stub is called only by the runtime-sourced function.
# shellcheck disable=SC2015,SC2016,SC2329
set -u

# shellcheck source-path=SCRIPTDIR
. "$(dirname -- "$0")/lib.sh"
new_workdir >/dev/null

load_function logfmt_value
load_function refuse_held_roots

FREE="$WORK/free"
HELD="$WORK/held"
mkdir -p "$FREE" "$HELD" || exit 1
printf '4242\n' >"$HELD/marotte.lock" || exit 1
exec 9<"$HELD"
flock -n 9 || {
  printf 'harness error: could not take the holder flock on %s\n' "$HELD" >&2
  exit 1
}

probe() {
  rc=0
  refuse_held_roots "$@" >"$WORK/out.log" 2>"$WORK/err.log" || rc=$?
}

probe "$FREE" "$HELD"
[ "$rc" -eq 1 ] \
  && grep -Fq 'level=error msg="another marotte instance holds this folder, so this one is refusing to start"' "$WORK/err.log" \
  && grep -Fq "path=\"$HELD\" holder_pid=4242 component=entrypoint" "$WORK/err.log" \
  && ok "a held folder returns 1 with the refusal line naming the folder and pid 4242" \
  || no "held folder" "rc=$rc: $(cat "$WORK/err.log")"

probe "$FREE" "$WORK"
[ "$rc" -eq 0 ] && [ ! -s "$WORK/err.log" ] && [ ! -s "$WORK/out.log" ] \
  && ok "free folders return 0 and print nothing" \
  || no "free folders" "rc=$rc: $(cat "$WORK/err.log")"

rm -f "$HELD/marotte.lock"
probe "$HELD"
[ "$rc" -eq 1 ] && grep -Fq 'holder_pid=0 ' "$WORK/err.log" \
  && ok "a held folder without a lock file still refuses, with holder_pid=0" \
  || no "no lock file" "rc=$rc: $(cat "$WORK/err.log")"

ODD="$WORK/odd\" forged=1"
mkdir -p "$ODD" || exit 1
exec 8<"$ODD"
flock -n 8 || exit 1
probe "$ODD"
[ "$rc" -eq 1 ] && ! grep -Fq '" forged=1' "$WORK/err.log" \
  && ok "a quote in the folder name is neutralized in the path field" \
  || no "crafted folder name" "rc=$rc: $(cat "$WORK/err.log")"

(
  flock() { return 127; }
  refuse_held_roots "$HELD" >"$WORK/out.log" 2>"$WORK/err.log"
)
rc=$?
[ "$rc" -eq 0 ] && [ ! -s "$WORK/err.log" ] \
  && ok "a missing flock binary (127) is not read as contention" \
  || no "flock missing" "rc=$rc: $(cat "$WORK/err.log")"

call=$(grep -n '^refuse_held_roots "$CONFIG_DIR" "$KIRO_HOME" "$TOOLS" || exit 1$' "$ENTRYPOINT" | cut -d: -f1)
migrate=$(grep -n '^if \[ -d /config/kiro \]' "$ENTRYPOINT" | cut -d: -f1)
[ -n "$call" ] && [ -n "$migrate" ] && [ "$call" -lt "$migrate" ] \
  && ok "the boot refuses on all three roots before the legacy migration's deletions" \
  || no "call site" "refuse_held_roots call at line '${call}', migration at '${migrate}'"

FILEONLY="$WORK/fileonly"
mkdir -p "$FILEONLY" || exit 1
printf '5151\n' >"$FILEONLY/marotte.lock" || exit 1
exec 7<"$FILEONLY/marotte.lock"
flock -n 7 || exit 1
probe "$FILEONLY"
[ "$rc" -eq 1 ] && grep -Fq "path=\"$FILEONLY\" holder_pid=5151 " "$WORK/err.log" \
  && ok "a holder of only marotte.lock refuses, naming the folder and pid 5151" \
  || no "file-only holder" "rc=$rc: $(cat "$WORK/err.log")"

GO_SRC="$REPO_ROOT/internal/composition/instance_lock.go"
grep -Fq 'slog.Error("another marotte instance holds this folder, so this one is refusing to start"' "$GO_SRC" \
  && ok "the refusal message matches marotte's ERROR line verbatim" \
  || no "message parity" "$GO_SRC no longer logs the entrypoint's refusal message"

report
