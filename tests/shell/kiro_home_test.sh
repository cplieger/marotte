#!/usr/bin/env bash
# effective_kiro_home(): the KIRO_HOME the entrypoint exports, which must be the tree kiro-cli
# honours, or the entrypoint creates and migrates into a directory neither the server nor the agent
# reads. The cases mirror internal/workspace TestResolveKiroHome_MatchesTheAgentsRule. Lint:
#   SC2015 - `[ cond ] && ok || no` cannot mis-fire: lib.sh's ok/no always return 0.
# shellcheck disable=SC2015
set -u

# shellcheck source-path=SCRIPTDIR
. "$(dirname -- "$0")/lib.sh"
new_workdir >/dev/null

load_function effective_kiro_home

HOME="$WORK/home"
OUTSIDE="$WORK/outside"
mkdir -p "$HOME" "$OUTSIDE" || exit 1
ln -s "$HOME" "$OUTSIDE/home-link" || exit 1
FALLBACK="$HOME/.kiro"

# check <name> <KIRO_HOME> <want> <warns: yes|no>
check() {
  local got warned=no
  got=$(KIRO_HOME=$2 effective_kiro_home 2>"$WORK/warn.log")
  [ -s "$WORK/warn.log" ] && warned=yes
  if [ "$got" = "$3" ] && [ "$warned" = "$4" ]; then
    ok "$1"
  else
    no "$1" "got '$got' (warned: $warned), want '$3' (warned: $4)"
  fi
}

check "unset answers the default" "" "$FALLBACK" no
check "blank answers the default" "  " "$FALLBACK" no
check "relative is ignored with a warning" "state/kiro" "$FALLBACK" yes
check "a filesystem root is ignored with a warning" "/" "$FALLBACK" yes
check "HOME itself is ignored with a warning" "$HOME" "$FALLBACK" yes
check "a parent of HOME is ignored with a warning" "$WORK" "$FALLBACK" yes
check "a symlink to HOME is ignored with a warning" "$OUTSIDE/home-link" "$FALLBACK" yes
check "the default spelled out is honoured" "$FALLBACK" "$FALLBACK" no
check "a path inside HOME is honoured" "$HOME/kiro-state" "$HOME/kiro-state" no
check "a not-yet-created path outside HOME is honoured" "$OUTSIDE/kiro/x" "$OUTSIDE/kiro/x" no
check "an uncleaned path is honoured cleaned" "$OUTSIDE/kiro/../kiro2/" "$OUTSIDE/kiro2" no
check "surrounding blanks are trimmed" " $OUTSIDE/kiro " "$OUTSIDE/kiro" no
check "only the ends are trimmed, as kiro-cli's trim does" $' '"$OUTSIDE"$'/a \n b ' "$OUTSIDE"$'/a \n b' no

# check_one_warning <name> <KIRO_HOME>: a rejected value never adds a boot-log line of its own.
check_one_warning() {
  local got lines
  got=$(KIRO_HOME=$2 effective_kiro_home 2>"$WORK/warn.log")
  lines=$(wc -l <"$WORK/warn.log")
  if [ "$got" = "$FALLBACK" ] && [ "$lines" -eq 1 ] && ! grep -q '^ERROR' "$WORK/warn.log" \
    && [ "$(wc -c <"$WORK/warn.log")" -le 512 ]; then
    ok "$1"
  else
    no "$1" "got '$got', $lines warning line(s): $(cat "$WORK/warn.log")"
  fi
}

check_one_warning "a relative value cannot forge a log line" $'state\nERROR: forged'
check_one_warning "a rejected absolute value cannot forge a log line" $'/\nERROR: forged/..'
check_one_warning "a rejected value's warning is bounded" "state/$(printf '%*s' 5000 '' | tr ' ' x)"

# TestResolveKiroHome_KeepsTheMissingTailInOrder: reversed, link/kiro/x would resolve to HOME.
mkdir -p "$WORK/real/x/kiro" || exit 1
ln -s "$WORK/real" "$WORK/link" || exit 1
HOME="$WORK/real/x/kiro"
check "a missing tail behind a symlink keeps its order" "$WORK/link/kiro/x" "$WORK/link/kiro/x" no

# TestResolveKiroHome_DotDotNamedComponentIsOutside: kiro-cli's startsWith("..") reads a HOME of
# base/..home as outside base, so base is honoured.
mkdir -p "$WORK/base/..home/..user" || exit 1
HOME="$WORK/base/..home"
check "a HOME component named ..home is outside the candidate" "$WORK/base" "$WORK/base" no
HOME="$WORK/base/..home/..user"
check "a deeper ..-named HOME component is outside the candidate" "$WORK/base" "$WORK/base" no

report
