#!/usr/bin/env bash
# Lint:
#   SC2015 - `[ cond ] && ok || no` cannot mis-fire: lib.sh's ok/no always return 0.
#   SC2016 - the grep patterns match the entrypoint's and Dockerfile's literal "$VAR" text.
#   SC1090 - the sourced path is produced by extract_range at runtime.
# shellcheck disable=SC2015,SC2016,SC1090
set -u

# shellcheck source-path=SCRIPTDIR
. "$(dirname -- "$0")/lib.sh"
new_workdir >/dev/null

DOCKERFILE="${MAROTTE_DOCKERFILE:-$REPO_ROOT/Dockerfile}"
[ -r "$DOCKERFILE" ] || {
  printf 'harness error: Dockerfile is not readable at %s\n' "$DOCKERFILE" >&2
  exit 1
}

ROOTS=$(extract_range '^CONFIG_DIR=' '^TOOLS=') || exit 1
[ "$(grep -c . "$ROOTS")" -eq 2 ] || {
  printf 'harness error: the CONFIG_DIR and TOOLS lines are no longer adjacent:\n%s\n' "$(cat "$ROOTS")" >&2
  exit 1
}

roots() {
  local dir=$1 kv
  shift
  (
    cd -- "$dir" || exit 1
    unset KIRO_CONFIG_DIR MAROTTE_TOOLS_DIR
    for kv in "$@"; do
      export "${kv?}"
    done
    . "$ROOTS"
    printf '%s|%s\n' "$CONFIG_DIR" "$TOOLS"
  )
}

# The expected values are ConfigFromEnv's: cmp.Or treats empty as unset, and filepath.Abs joins
# the cwd and cleans lexically without resolving a symlink.
CWD="$WORK/cwd"
mkdir -p "$CWD/real" || exit 1
ln -s "$CWD/real" "$CWD/link" || exit 1
check_roots() {
  local want=$1 got
  shift
  got=$(roots "$CWD" "$@")
  [ "$got" = "$want" ] \
    && ok "roots for [$*] resolve to $want" \
    || no "roots [$*]" "got '$got', want '$want'"
}
check_roots "/config|/config/tools"
check_roots "/config|/config/tools" KIRO_CONFIG_DIR= MAROTTE_TOOLS_DIR=
check_roots "/srv/m|/srv/m/tools" KIRO_CONFIG_DIR=/srv/m
check_roots "/srv/m|/opt/t" KIRO_CONFIG_DIR=/srv/m MAROTTE_TOOLS_DIR=/opt/t
check_roots "/config|/opt/t" MAROTTE_TOOLS_DIR=/opt/t/
check_roots "/srv/n|/srv/n/tools" KIRO_CONFIG_DIR=/srv/m/../n/
check_roots "$CWD/data|$CWD/t" KIRO_CONFIG_DIR=data/ MAROTTE_TOOLS_DIR=./t
check_roots "$CWD/link|$CWD/link/tools" KIRO_CONFIG_DIR=link

# A custom root whose host stamped every folder 0770.
load_function logfmt_value
load_function own_place
load_function at_place
load_function tighten_dir
load_function tighten_engine_roots
mkdir -p "$WORK/custom/config" "$WORK/custom/tools/npm/lib" || exit 1
chmod 0770 "$WORK/custom/config" "$WORK/custom/tools" "$WORK/custom/tools/npm" "$WORK/custom/tools/npm/lib" || exit 1
(
  export KIRO_CONFIG_DIR="$WORK/custom/config" MAROTTE_TOOLS_DIR="$WORK/custom/tools"
  . "$ROOTS"
  tighten_engine_roots "$CONFIG_DIR" "$TOOLS"
) 2>"$WORK/tighten.log"
modes=$(stat -c %a "$WORK/custom/config" "$WORK/custom/tools" "$WORK/custom/tools/npm" "$WORK/custom/tools/npm/lib" | tr '\n' ' ')
[ "$modes" = "750 750 750 750 " ] \
  && ok "the configured config and tools dirs are the ones the boot tightens" \
  || no "custom roots tightened" "modes: $modes"

load_function rebase_tools_env

# printenv, because a bash child would source BASH_ENV first. stderr goes to $REBASE_ERR.
REBASE_ERR="$WORK/rebase.err"
rebase() {
  (
    PATH=$3
    export GOPATH=$4 GOBIN=$5
    rebase_tools_env "$1" "$2" "${6-}"
    /usr/bin/printenv PATH GOPATH GOBIN | /usr/bin/paste -sd'|'
  ) 2>"$REBASE_ERR"
}

IMAGE_PATH="/config/tools/bin:/config/tools/go/bin:/config/home/.local/bin:/usr/bin:/bin"
got=$(rebase /config/tools /config/tools "$IMAGE_PATH" /config/tools/go /config/tools/bin)
[ "$got" = "$IMAGE_PATH|/config/tools/go|/config/tools/bin" ] && [ ! -s "$REBASE_ERR" ] \
  && ok "the default tools dir leaves the image's PATH, GOPATH and GOBIN as they are, silently" \
  || no "default tools dir" "child saw $got; stderr: $(cat "$REBASE_ERR")"

# PATH and GOPATH split on ':', so such a dir would become two entries, one of them relative.
colon_warn() {
  printf 'level=warn msg="the tools folder holds a colon, which PATH and GOPATH cannot carry, so a shell reaches what the tools engine installs only by its full path" dir="%s" env=%s component=entrypoint\n' "$1" "$2"
}
got=$(rebase /config/tools '/srv/a:b' "$IMAGE_PATH" /config/tools/go /config/tools/bin MAROTTE_TOOLS_DIR)
[ "$got" = "/config/home/.local/bin:/usr/bin:/bin|/srv/a:b/bin" ] \
  && [ "$(cat "$REBASE_ERR")" = "$(colon_warn '/srv/a:b' MAROTTE_TOOLS_DIR)" ] \
  && ok "a tools dir holding a colon is left off PATH and GOPATH, the image's entries go, and the warning names it" \
  || no "colon tools dir" "child saw $got; stderr: $(cat "$REBASE_ERR")"

got=$(rebase /config/tools '/m:x/tools' "/usr/bin:/config/tools/bin2:/bin" /home/go /home/go/bin)
[ "$got" = "/usr/bin:/config/tools/bin2:/bin|/home/go|/home/go/bin" ] \
  && [ "$(cat "$REBASE_ERR")" = "$(colon_warn '/m:x/tools' KIRO_CONFIG_DIR)" ] \
  && ok "a colon tools dir derived from KIRO_CONFIG_DIR is named so, gains no entries, and an operator GOPATH stays" \
  || no "colon tools dir from the config dir" "child saw $got; stderr: $(cat "$REBASE_ERR")"

got=$(rebase /config/tools "$(printf '/srv/a:"b\tc')" "$IMAGE_PATH" "" "" MAROTTE_TOOLS_DIR)
grep -qF "dir=\"/srv/a:'b?c\" env=MAROTTE_TOOLS_DIR" "$REBASE_ERR" && [ "$(grep -c . "$REBASE_ERR")" -eq 1 ] \
  && ok "the colon warning makes the dir safe inside its logfmt field" \
  || no "colon warning logfmt" "stderr: $(cat "$REBASE_ERR")"

got=$(rebase /config/tools /srv/t "$IMAGE_PATH" /config/tools/go /config/tools/bin)
[ "$got" = "/srv/t/bin:/srv/t/go/bin:/config/home/.local/bin:/usr/bin:/bin|/srv/t/go|/srv/t/bin" ] \
  && ok "a configured tools dir takes over the image's tools entries, in place, and children see them" \
  || no "configured tools dir" "child saw $got"

got=$(rebase /config/tools /srv/t "$IMAGE_PATH" /home/go /home/go/bin)
[ "$got" = "/srv/t/bin:/srv/t/go/bin:/config/home/.local/bin:/usr/bin:/bin|/home/go|/home/go/bin" ] \
  && ok "a GOPATH and GOBIN the operator set elsewhere are kept" \
  || no "operator GOPATH" "child saw $got"

got=$(rebase /config/tools /srv/t ":/config/tools/bin2:/config/tools/bin::/config/tools/opt/x/bin:" /g /g/bin)
[ "$got" = ":/config/tools/bin2:/srv/t/bin:/srv/t/go/bin::/config/tools/opt/x/bin:|/g|/g/bin" ] \
  && ok "only whole matching entries move, and empty PATH entries survive" \
  || no "entry matching" "child saw $got"

got=$(rebase /config/tools /srv/t "/srv/t/bin2:/usr/local/bin:/usr/bin:/bin" "" "")
[ "$got" = "/srv/t/bin:/srv/t/go/bin:/srv/t/bin2:/usr/local/bin:/usr/bin:/bin|/srv/t/go|/srv/t/bin" ] \
  && ok "a PATH without the tools entries gains them in front, and an empty GOPATH and GOBIN follow the tools dir" \
  || no "PATH without the tools entries" "child saw $got"

got=$(rebase /config/tools /config/tools "/usr/bin:/config/tools/bin2:/config/tools/bin:/bin" /config/tools/go /config/tools/bin)
[ "$got" = "/usr/bin:/config/tools/bin2:/config/tools/bin:/config/tools/go/bin:/bin|/config/tools/go|/config/tools/bin" ] \
  && ok "a missing go/bin is added right after the tools bin, which keeps its place" \
  || no "missing go/bin" "child saw $got"

got=$(rebase /config/tools /srv/t "" /g /g/bin)
[ "$got" = "/srv/t/bin:/srv/t/go/bin|/g|/g/bin" ] \
  && ok "an empty PATH becomes the two tools entries, with no empty entry" \
  || no "empty PATH" "child saw $got"

got=$(
  PATH=/usr/bin:/bin
  unset GOPATH GOBIN
  rebase_tools_env /config/tools /srv/t
  /usr/bin/printenv GOPATH GOBIN | /usr/bin/paste -sd'|'
)
[ "$got" = "/srv/t/go|/srv/t/bin" ] \
  && ok "an unset GOPATH and GOBIN are set to the tools dir and exported" \
  || no "unset GOPATH and GOBIN" "child saw '$got'"

# A tools dir with a space and a single quote must survive /etc/profile.d in sh and bash. stdin is
# /dev/null because bash sources ~/.bashrc when stdin is a socket.
load_function emit_path_dropin
odd="$WORK/my tools'x"
dropin="$WORK/dropin.sh"
want=$(
  PATH="$IMAGE_PATH"
  rebase_tools_env /config/tools "$odd"
  emit_path_dropin >"$dropin" || exit 1
  printf '%s' "$PATH"
) || exit 1
[ -s "$dropin" ] || {
  printf 'harness error: emit_path_dropin wrote nothing\n' >&2
  exit 1
}
for sh in /bin/sh /bin/bash; do
  err="$WORK/dropin.err"
  got=$(/usr/bin/env -i "$sh" -c 'PATH=/usr/bin:/bin; . "$1"; printf %s "$PATH"' dropin "$dropin" </dev/null 2>"$err")
  [ "$got" = "$want" ] && [ ! -s "$err" ] \
    && ok "$sh sources the login-shell drop-in to the rebased PATH" \
    || no "$sh drop-in" "PATH '$got', want '$want'; stderr: $(cat "$err")"
done

# The image's ENV is what the call rebases from.
grep -q '^ENV PATH="/config/tools/bin:/config/tools/go/bin:' "$DOCKERFILE" \
  && grep -qx 'ENV GOPATH="/config/tools/go"' "$DOCKERFILE" \
  && grep -qx 'ENV GOBIN="/config/tools/bin"' "$DOCKERFILE" \
  && ok "the Dockerfile's PATH, GOPATH and GOBIN name the default tools tree the boot rebases from" \
  || no "Dockerfile ENV" "the image no longer exports the /config/tools entries rebase_tools_env replaces"

call=$(grep -n '^rebase_tools_env /config/tools "$TOOLS" "${MAROTTE_TOOLS_DIR:+MAROTTE_TOOLS_DIR}"$' "$ENTRYPOINT" | cut -d: -f1)
roots_at=$(grep -n '^TOOLS=' "$ENTRYPOINT" | cut -d: -f1)
dropin=$(grep -n '^if ! install_path_dropin; then$' "$ENTRYPOINT" | cut -d: -f1)
[ -n "$call" ] && [ -n "$roots_at" ] && [ -n "$dropin" ] && [ "$roots_at" -lt "$call" ] && [ "$call" -lt "$dropin" ] \
  && ok "the boot rebases PATH after resolving the tools dir and before writing the login-shell drop-in" \
  || no "rebase call site" "TOOLS at '${roots_at}', call at '${call}', drop-in at '${dropin}'"

report
