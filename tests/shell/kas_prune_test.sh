#!/usr/bin/env bash
# prune_superseded_kas_runtimes(): reclaim the ~240 MB agent-server runtime each kiro-cli version
# unpacks under <data-dir>/kas/<version>-<hash>/, keeping only the pinned one. The entrypoint's only
# root `rm -rf`, its target derived from the environment, so the REFUSALS carry the weight. It warns
# and returns 0 either way, so every case asserts the on-disk tree plus the warning line. Lint:
#   SC2015 - `[ cond ] && ok || no` cannot mis-fire: lib.sh's ok/no always return 0.
#   SC2034 - these variables are inputs to entrypoint.sh code sourced at RUNTIME.
#   SC2329 - the realpath and rm stubs are called only by that runtime-sourced code.
# shellcheck disable=SC2015,SC2034,SC2329
set -u

# shellcheck source-path=SCRIPTDIR
. "$(dirname -- "$0")/lib.sh"
new_workdir >/dev/null

load_function prune_superseded_kas_runtimes

KIRO_CLI_VERSION="2.14.2"

# Fresh fake volume per scenario. XDG_DATA_HOME drives the resolution, matching how
# kiro-cli locates its own data dir.
setup() {
  ROOT=$(mktemp -d "$WORK/vol.XXXXXX")
  export XDG_DATA_HOME="$ROOT/share"
  KAS="$XDG_DATA_HOME/kiro-cli/kas"
  mkdir -p "$KAS"
}

# --- 1. ordinary prune: superseded versions go, the pinned one stays -------------
setup
mkdir -p "$KAS/2.14.2-abc" "$KAS/2.13.0-def" "$KAS/2.12.1-ghi"
: >"$KAS/2.13.0-def.lock"
prune_superseded_kas_runtimes >/dev/null 2>&1
[ -d "$KAS/2.14.2-abc" ] && ok "pinned runtime kept" || no "pinned runtime kept" "it was deleted"
[ ! -d "$KAS/2.13.0-def" ] && [ ! -d "$KAS/2.12.1-ghi" ] \
  && ok "superseded runtimes pruned" || no "superseded runtimes pruned" "still present"
[ ! -e "$KAS/2.13.0-def.lock" ] && ok "the superseded .lock sibling pruned too" \
  || no ".lock sibling" "still present"

# Both streams to files: refusals are on stderr, skip/prune narration on stdout, and several cases
# tell the firing guard apart only by reading one.
prune_quietly() {
  prune_superseded_kas_runtimes >"$WORK/out.log" 2>"$WORK/warn.log"
}
guard_said() {
  grep -q "$1" "$WORK/warn.log"
}
narrated() {
  grep -q "$1" "$WORK/out.log"
}

# --- 2. entries kiro-cli owns but this pruner has never seen ---------------------
setup
mkdir -p "$KAS/2.14.2-abc" "$KAS/unpack-scratch" "$KAS/index"
: >"$KAS/store.lock"
prune_quietly
if [ -d "$KAS/unpack-scratch" ] && [ -d "$KAS/index" ] && [ -e "$KAS/store.lock" ]; then
  ok "unrecognized (non version-keyed) entries left alone"
else
  no "unrecognized entries" "the pruner deleted another program's state"
fi

# An ANCHORED three-component version match: neither near-miss is prunable, and the skip is
# narrated so a kas/ layout change shows in the boot log.
setup
mkdir -p "$KAS/2.14.2-abc" "$KAS/2.13-def" "$KAS/v2.13.0-def"
prune_quietly
[ -d "$KAS/2.13-def" ] && [ -d "$KAS/v2.13.0-def" ] \
  && narrated 'v2.13.0-def' && narrated '2.13-def' \
  && ok "a two-component and a v-prefixed name are not version-keyed, kept and narrated" \
  || no "near-miss names" "deleted, or the skip was not narrated"

# The dash in "$KIRO_CLI_VERSION"-* is load-bearing: without it a LONGER version sharing the pin's
# digits would survive forever.
setup
mkdir -p "$KAS/2.14.2-abc" "$KAS/2.14.20-xyz"
prune_quietly
[ -d "$KAS/2.14.2-abc" ] && [ ! -d "$KAS/2.14.20-xyz" ] \
  && ok "a version merely sharing the pin's prefix is pruned, not mistaken for the pin" \
  || no "prefix-sharing version" "2.14.20-xyz survived as though it were the pin"

# --- 3. THE SECURITY CASE: a symlinked store must not redirect a root rm -rf ----
#
# The bait is DELIBERATELY version-keyed and non-pinned ("2.13.0-victim"), the shape the pruner
# deletes, and planted where the resolved $kas_dir points, past the `[ -d "$kas_dir" ]` return.
# Verified by neutralising both guards in a /tmp copy and watching each bait go.
plant_victim() {
  mkdir -p "$1/2.13.0-victim" && : >"$1/2.13.0-victim/data"
}

# realpath resolves symlinks, so every redirect -L catches also fails containment; outcome cannot
# isolate -L. Each case asserts its own refusal LINE instead.
setup
VICTIM="$ROOT/victim"
plant_victim "$VICTIM"
rm -rf "$KAS"
mkdir -p "$XDG_DATA_HOME/kiro-cli"
ln -s "$VICTIM" "$KAS" # kas -> an arbitrary tree
prune_quietly
[ -f "$VICTIM/2.13.0-victim/data" ] && guard_said 'is a symlink' \
  && ok "symlinked kas store refused BY the symlink guard; the victim tree survived" \
  || no "symlinked kas store" "the pruner deleted through the symlink, or a different guard caught it"

setup
VICTIM="$ROOT/victim2"
# A real `kas` child under the symlinked `kiro-cli`, or the `-d` check returns first.
plant_victim "$VICTIM/kas"
rm -rf "$XDG_DATA_HOME/kiro-cli"
ln -s "$VICTIM" "$XDG_DATA_HOME/kiro-cli" # the data dir itself is the symlink
prune_quietly
[ -f "$VICTIM/kas/2.13.0-victim/data" ] && guard_said 'is a symlink' \
  && ok "symlinked data dir refused BY the symlink guard; the victim tree survived" \
  || no "symlinked data dir" "the pruner deleted through the symlink, or a different guard caught it"

# The containment guard alone, with no symlink: a ".." in XDG_DATA_HOME makes realpath disagree
# with the literal path, and the pruner refuses.
setup
mkdir -p "$ROOT/real-share"
XDG_DATA_HOME="$ROOT/real-share/../real-share"
KAS="$XDG_DATA_HOME/kiro-cli/kas"
mkdir -p "$KAS"
plant_victim "$KAS"
prune_quietly
[ -f "$KAS/2.13.0-victim/data" ] && guard_said 'does not resolve inside the data dir' \
  && ok "non-canonical data-dir path refused BY the containment guard (no symlink involved)" \
  || no "containment check" "pruned against a path realpath could not confirm, or a different guard caught it"

# realpath itself failing: an empty answer must refuse and say "unknown". Stubbed inside a SUBSHELL
# so it cannot shadow this file's own realpath.
setup
plant_victim "$KAS"
(
  realpath() { return 1; }
  prune_superseded_kas_runtimes
) >"$WORK/out.log" 2>"$WORK/warn.log"
[ -f "$KAS/2.13.0-victim/data" ] && guard_said 'does not resolve inside the data dir' \
  && guard_said 'unknown' \
  && ok "an unresolvable kas path refuses and names its target unknown" \
  || no "unresolvable kas path" "pruned anyway, or reported an empty target"

# --- 4. a failed delete warns; it never fails the boot ---------------------------
# An rm failure (immutable attribute, EPERM) is provoked by shadowing rm in a subshell. The contract
# is warn-and-continue: hygiene must not brick the container.
setup
mkdir -p "$KAS/2.13.0-def"
(
  rm() { return 1; }
  prune_superseded_kas_runtimes
) >"$WORK/out.log" 2>"$WORK/warn.log"
rc=$?
[ "$rc" -eq 0 ] && [ -d "$KAS/2.13.0-def" ] \
  && guard_said 'failed to prune superseded kiro-cli agent runtime 2.13.0-def' \
  && ok "a failed delete warns, names the entry, and still returns 0" \
  || no "failed delete" "returned $rc, or the warning lost the entry name: $(cat "$WORK/warn.log")"

# --- 5. degenerate inputs must not fail the boot --------------------------------
setup
prune_superseded_kas_runtimes >/dev/null 2>&1
[ $? -eq 0 ] && ok "empty store returns 0" || no "empty store" "non-zero return"

setup
rm -rf "$XDG_DATA_HOME"
prune_superseded_kas_runtimes >/dev/null 2>&1
[ $? -eq 0 ] && ok "absent data dir returns 0" || no "absent data dir" "non-zero return"

setup
unset XDG_DATA_HOME
HOME_SAVED="${HOME:-}"
unset HOME
# The EXECUTION must be observable: with the HOME guard deleted the function derives
# data_home=/.local/share and still returns 0, a false green before a root rm -rf. The xtrace log
# shows whether the guard returned before any data_home assignment.
_trace="$WORK/unset-home-trace"
{
  BASH_XTRACEFD=7
  set -x
  prune_superseded_kas_runtimes >/dev/null 2>&1
  rc=$?
  set +x
  unset BASH_XTRACEFD
} 7>"$_trace"
export HOME="$HOME_SAVED"
# The empty XDG assignment legitimately runs; the guard must prevent the HOME-fallback DERIVATION
# (its trace carries .local/share). The `data_home=` anchor proves the capture is LIVE, or a lost
# BASH_XTRACEFD would pass vacuously.
grep -q 'data_home=' "$_trace" && [ "$rc" -eq 0 ] && ! grep -q '\.local/share' "$_trace" \
  && ok "neither XDG_DATA_HOME nor HOME set returns 0 before the HOME fallback is derived" \
  || no "unset HOME" "rc=$rc, trace lines=$(wc -l <"$_trace"), derivations: $(grep '\.local/share' "$_trace" | head -2 | tr '\n' ';')"

# --- 6. data-dir resolution must match kiro-cli's own ---------------------------
# Pruning a directory the CLI does not use is a silent no-op. A prunable tree sits on BOTH candidate
# paths, so a swapped precedence deletes the wrong one visibly.
setup
HOME_SAVED="${HOME:-}"
export HOME="$ROOT/home"
HOME_KAS="$HOME/.local/share/kiro-cli/kas"
mkdir -p "$HOME_KAS/2.13.0-home" "$KAS/2.13.0-xdg"
prune_quietly
export HOME="$HOME_SAVED"
[ ! -d "$KAS/2.13.0-xdg" ] && [ -d "$HOME_KAS/2.13.0-home" ] \
  && ok "XDG_DATA_HOME wins over HOME; the HOME tree is not touched" \
  || no "XDG precedence" "pruned the HOME tree, or left the XDG one"

setup
unset XDG_DATA_HOME
HOME_SAVED="${HOME:-}"
export HOME="$ROOT/home2"
HOME_KAS="$HOME/.local/share/kiro-cli/kas"
mkdir -p "$HOME_KAS/2.14.2-keep" "$HOME_KAS/2.13.0-home"
prune_quietly
export HOME="$HOME_SAVED"
[ ! -d "$HOME_KAS/2.13.0-home" ] && [ -d "$HOME_KAS/2.14.2-keep" ] \
  && ok "with XDG_DATA_HOME unset the store resolves under \$HOME/.local/share" \
  || no "HOME fallback" "the fallback path was not the one pruned"

report
