#!/bin/bash

# Make an untrusted value (a name off the /config volume, dpkg output) safe inside
# a quoted logfmt field, so a crafted name cannot close the field and append keys.
# Truncate the raw value first, or the doubling could split a `\\` pair; then
# double backslashes, replace non-printables, and neutralize the quote. $2 is the
# input bound (default 200).
logfmt_value() {
  local raw=$1 safe
  safe=${raw:0:${2:-200}}
  safe=${safe//\\/\\\\}
  safe=${safe//[![:print:]]/?}
  safe=${safe//\"/\'}
  printf '%s' "$safe"
}

# Sets CONFIG_DIR, TOOLS, KIRO_HOME, PATH, GOPATH and GOBIN from `$1 paths`, the server's own
# derivation (internal/composition/paths.go), so this script acts on the folders the server uses.
load_boot_paths() {
  local out
  out=$("$1" paths) || return 1
  eval "$out"
}

# An unusable config folder is the one setting the server refuses to start on; paths named it.
if ! load_boot_paths /app/marotte; then
  sleep 10
  exit 1
fi

# kiro-cli pins, tracked by Renovate against
# https://desktop-release.q.us-east-1.amazonaws.com/index.json. The server installs
# from them (internal/composition/kirocli.go) and enforces both per-arch sha256s.
# Keep them bare double-quoted literals under their `# renovate:` anchors: the
# shared custom datasource matches exactly this shape (tests/shell/pins_export_test.sh).
# renovate: datasource=custom.kiro-cli depName=kiro-cli
KIRO_CLI_VERSION="2.37.1"
KIRO_CLI_SHA256="b4049000d5a7b684947306eb9f9a480cab3a3ab2ff42584c293d05bde4d314a1"
# The `# kiro-cli <version>` trailer is Renovate's anchor for this digest.
# renovate: datasource=custom.kiro-cli-arm64 depName=kiro-cli-arm64
KIRO_CLI_SHA256_ARM64="e589bd8d8b845bb193b9f36f2ee263fe8aef7dbb19aff03ce44c5076ce6ab0c1" # kiro-cli 2.37.1

# Without the export the server sees no pins and turns its readiness gate off,
# reporting healthy while installing nothing.
export KIRO_CLI_VERSION KIRO_CLI_SHA256 KIRO_CLI_SHA256_ARM64

# bin/ is made here so PATH never races the engine, which makes its other subtrees, and
# kiro-cli-versions (the server's install root) so this failure branch covers it. That root
# must stay a sibling of opt/: the engine prunes every non-current directory under opt/<tool>.
# The config dir is made even when the tools dir lies elsewhere, or flock would create a file.
# shellcheck disable=SC2153 # load_boot_paths sets TOOLS and KIRO_HOME through eval
mkdir -p "$CONFIG_DIR" "$TOOLS/bin" "$TOOLS/kiro-cli-versions" \
  "$HOME/.local/share/kiro-cli" "$HOME/.ssh" \
  "$KIRO_HOME" "$HOME/.cache/go-build" "$HOME/.docker/cli-plugins" \
  || {
    printf "ERROR: failed to create required directories (is %s mounted and writable?)\n" "$CONFIG_DIR"
    sleep 10
    exit 1
  }

# Returns 1, with marotte's own refusal line, when a running marotte holds one of the
# folders: this script's deletions below would otherwise run against its live tree
# before marotte's claim (internal/composition/instance_lock.go) refuses. It probes
# both locks that claim takes, the folder and its marotte.lock. The probe keeps no
# lock, and any failure other than contention leaves the refusal to marotte.
refuse_held_roots() {
  local root rc pid
  for root in "$@"; do
    rc=0
    flock -n -E 75 "$root" true 2>/dev/null || rc=$?
    if [ "$rc" -ne 75 ] && [ -f "$root/marotte.lock" ]; then
      rc=0
      flock -n -E 75 "$root/marotte.lock" true 2>/dev/null || rc=$?
    fi
    [ "$rc" -eq 75 ] || continue
    pid=$(head -c 32 "$root/marotte.lock" 2>/dev/null | tr -dc '0-9')
    printf 'level=error msg="another marotte instance holds this folder, so this one is refusing to start" path="%s" holder_pid=%s component=entrypoint\n' \
      "$(logfmt_value "$root")" "${pid:-0}" >&2
    return 1
  done
  return 0
}

refuse_held_roots "$CONFIG_DIR" "$KIRO_HOME" "$TOOLS" || exit 1

# Prints the physical path of the entry $1 names: its parent resolved, its own name kept,
# so a symlink there leads somewhere else.
own_place() {
  local parent
  parent=$(realpath -e -- "$(dirname -- "$1")") || return 1
  printf '%s/%s\n' "${parent%/}" "$(basename -- "$1")"
}

# Succeeds when the cwd lies at $1. realpath asks getcwd, where bash's pwd -P re-resolves
# the logical $PWD, which a swap after cd can point back at the expected place.
at_place() { [ "$(realpath -e -- .)" = "$1" ]; }

# Removes group and other write from directory $1 when it lies at $2, with a warning,
# since what another account wrote there meanwhile stays. The subshell holds the directory
# as its cwd, so chmod acts on the inode at_place placed and a later swap cannot redirect it.
tighten_dir() {
  local dir=$1 want=$2 mode
  (
    if ! cd -- "$dir" 2>/dev/null || ! at_place "$want"; then
      exit 0
    fi
    mode=$(stat -c %a .) || exit 0
    [ $((8#$mode & 0022)) -ne 0 ] || exit 0
    printf 'level=warn msg="a folder the tools engine checks let other accounts write; removing that, but anything they wrote there stays" dir="%s" mode=%s component=entrypoint\n' "$(logfmt_value "$dir")" "$mode" >&2
    chmod go-w . \
      || printf 'level=warn msg="could not remove group and other write; the tools engine will refuse this folder" dir="%s" component=entrypoint\n' "$(logfmt_value "$dir")" >&2
  )
}

# Runs chmod $1 on directory $2 from inside it when it lies at $3, as tighten_dir does.
chmod_at() {
  local mode=$1 dir=$2 want=$3
  (
    if ! cd -- "$dir" 2>/dev/null || ! at_place "$want"; then
      exit 0
    fi
    chmod "$mode" . 2>/dev/null \
      || printf 'level=warn msg="could not set the mode of a folder" dir="%s" mode=%s component=entrypoint\n' "$(logfmt_value "$dir")" "$mode" >&2
  )
}

# Tightens every directory the tools engine's root-integrity check judges, since one left
# writable boots marotte with no tools engine. The list is held to toolbelt's documented
# one by TestEntrypointTightensEveryRootTheEngineJudges. A child counts only at its own
# place under the tools dir, so a symlink at any depth is left for the engine to name.
tighten_engine_roots() {
  local config=$1 tools=$2 real dir
  real=$(own_place "$config") && tighten_dir "$config" "$real"
  real=$(own_place "$tools") || return 0
  tighten_dir "$tools" "$real"
  for dir in "$tools/bin" "$tools/opt" \
    "$tools/npm" "$tools/npm/bin" "$tools/npm/lib" "$tools/npm/lib/node_modules" \
    "$tools/python" "$tools/python/bin" "$tools/python/tools" "$tools/python/interpreters"; do
    tighten_dir "$dir" "$real/${dir#"$tools"/}"
  done
}

# HOME, and a KIRO_HOME outside it, are the operator's values, so each is resolved; the
# folders below them, and kiro-cli-versions below the tools dir, count only at their own place.
normalize_modes() {
  local home=$1 kiro_home=$2 tools=$3 place d
  if place=$(realpath -e -- "$home" 2>/dev/null); then
    chmod_at 755 "$home" "$place"
    for d in .local .local/share .local/share/kiro-cli .cache .cache/go-build .docker .docker/cli-plugins; do
      chmod_at go-w "$home/$d" "$place/$d"
    done
    chmod_at 700 "$home/.ssh" "$place/.ssh"
  fi
  case "$kiro_home" in
    "$home"/*) chmod_at go-w "$kiro_home" "$place/${kiro_home#"$home"/}" ;;
    *) place=$(realpath -e -- "$kiro_home" 2>/dev/null) && chmod_at go-w "$kiro_home" "$place" ;;
  esac
  place=$(own_place "$tools") && chmod_at go-w "$tools/kiro-cli-versions" "$place/kiro-cli-versions"
}

# Normalize modes on every boot: an inheritable ACL or umask on the host volume
# can stamp any mode, the tools engine refuses a group- or other-writable root, and
# ssh refuses a lax ~/.ssh.
tighten_engine_roots "$CONFIG_DIR" "$TOOLS"
normalize_modes "$HOME" "$KIRO_HOME" "$TOOLS"

# Give login shells (the browser shell runs `bash --login`) the inherited PATH:
# Debian's /etc/profile assigns uid 0 only the system directories, dropping the
# /config entries. profile.d is sourced after that assignment. The value is the image's
# ENV as load_boot_paths set it.
emit_path_dropin() {
  printf '%s\n' \
    '# Generated by marotte entrypoint.sh on every boot -- edits are overwritten.' \
    '#' \
    '# WHY THIS IS NOT REDUNDANT WITH THE IMAGE ENV PATH: Debian /etc/profile' \
    '# ASSIGNS PATH rather than appending, and its uid-0 branch (this container)' \
    '# assigns only the six system directories -- so every login shell discards the' \
    '# /config entries the image exported and an engine-installed tool such as gh' \
    '# stops resolving by name. /etc/profile sources this directory through' \
    '# run-parts AFTER that assignment, so restoring the value here puts them back.' \
    '#' \
    '# The assignment below is lossless rather than additive on purpose: the' \
    '# exported PATH already ends with exactly the six directories /etc/profile'\''s' \
    '# uid-0 branch sets, so it is a superset and nothing is dropped.'
  # Single-quoted, so a tools dir with a space or a quote stays one word.
  local quoted=${PATH//\'/\'\\\'\'}
  printf "PATH='%s'\nexport PATH\n" "$quoted"
}

# Through a temp file so a login shell never sources a half-written assignment;
# the mktemp suffix keeps run-parts from matching it.
install_path_dropin() {
  local dir=/etc/profile.d target tmp
  target="$dir/10-marotte-path.sh"
  [ -d "$dir" ] || return 1
  tmp=$(mktemp "$target.XXXXXX") || return 1
  if emit_path_dropin >"$tmp" && chmod 644 "$tmp" && mv -f "$tmp" "$target"; then
    return 0
  fi
  rm -f "$tmp"
  return 1
}

# Warn only: the mkdir above is the one place this script fails the boot.
if ! install_path_dropin; then
  printf "WARNING: could not write /etc/profile.d/10-marotte-path.sh; a login shell (the browser shell runs bash --login) will not see %s, so an engine-installed tool will not resolve by name there\n" "$TOOLS/bin" >&2
fi

# One-time migration from the legacy KIRO_HOME (/config/kiro) to the current one;
# only the settings and custom steering doc carry over.
if [ -d /config/kiro ] && [ "$KIRO_HOME" != /config/kiro ]; then
  printf "Migrating legacy kiro state /config/kiro -> %s\n" "$KIRO_HOME"
  mkdir -p "$KIRO_HOME/settings" "$KIRO_HOME/steering" "$KIRO_HOME/agents"
  # Legacy copies are newer than anything at the destination.
  [ -f /config/kiro/settings/cli.json ] \
    && cp -f /config/kiro/settings/cli.json "$KIRO_HOME/settings/cli.json"
  [ -f /config/kiro/steering/custom.md ] \
    && cp -f /config/kiro/steering/custom.md "$KIRO_HOME/steering/custom.md"
  if [ -d /config/kiro/agents ]; then
    cp -rn /config/kiro/agents/. "$KIRO_HOME/agents/" 2>/dev/null || true
  fi
  # A stale environment.md would load before marotte regenerates it.
  rm -f "$KIRO_HOME/steering/environment.md"
  rm -rf /config/kiro
fi

# Reclaim superseded kiro-cli agent-server runtimes: each version unpacks a
# ~240 MB tree under <data-dir>/kas/<version>-<hash>/ at the engine's first launch,
# and nothing removes the old ones. Unrelated to KAS session state. Data-dir
# resolution mirrors kiro-cli's (XDG_DATA_HOME, else $HOME/.local/share). Warn,
# never fail the boot. It stays here because it must run before the server can
# unpack a new tree.
prune_superseded_kas_runtimes() {
  local data_home kas_dir kas_real entry name
  data_home="${XDG_DATA_HOME:-}"
  if [ -z "$data_home" ]; then
    [ -n "${HOME:-}" ] || return 0
    data_home="$HOME/.local/share"
  fi
  kas_dir="$data_home/kiro-cli/kas"
  [ -d "$kas_dir" ] || return 0
  # `-d` follows symlinks and the rm runs as root, so a planted symlink could
  # redirect the sweep at an arbitrary tree. Refuse unless the store is a real
  # directory resolving where it is named.
  if [ -L "$data_home/kiro-cli" ] || [ -L "$kas_dir" ]; then
    printf "WARNING: kiro-cli data dir or its kas store is a symlink; refusing to prune through it (%s)\n" "$kas_dir" >&2
    return 0
  fi
  kas_real=$(realpath "$kas_dir" 2>/dev/null) || kas_real=""
  case "$kas_real" in
    "$data_home"/kiro-cli/kas) ;;
    *)
      printf "WARNING: kiro-cli kas store does not resolve inside the data dir; refusing to prune (%s -> %s)\n" \
        "$kas_dir" "${kas_real:-unknown}" >&2
      return 0
      ;;
  esac
  for entry in "$kas_dir"/*; do
    # An empty store leaves the glob unexpanded.
    [ -e "$entry" ] || continue
    name="${entry##*/}"
    # Covers the tree and its sibling .lock; quoting keeps the version literal.
    case "$name" in
      "$KIRO_CLI_VERSION"-*) continue ;;
    esac
    # Only version-keyed entries are superseded runtimes: kas/ is kiro-cli's
    # directory, so anything else is left in place and logged.
    if [[ ! "$name" =~ ^[0-9]+\.[0-9]+\.[0-9]+- ]]; then
      printf "Leaving unrecognized (non version-keyed) entry in the kiro-cli agent runtime store: %s\n" "$name"
      continue
    fi
    if rm -rf "$entry"; then
      printf "Pruned superseded kiro-cli agent runtime: %s (pin %s)\n" "$name" "$KIRO_CLI_VERSION"
    else
      printf "WARNING: failed to prune superseded kiro-cli agent runtime %s\n" "$name" >&2
    fi
  done
  return 0
}

# Before the server starts, so a bump boot drops the old tree before the new one
# unpacks and peak usage stays at one tree.
prune_superseded_kas_runtimes

# Older images left kiro-cli dispatchers in $HOME/.local/bin, which is on PATH and
# on the volume, so a bare `kiro-cli` could resolve to an unpinned copy. It lives
# here because it is outside $TOOLS, the server's tree. An unmatched glob returns 0,
# so a failure is real.
if ! rm -rf "$HOME/.local/bin"/kiro-cli*; then
  printf "WARNING: failed to remove legacy kiro-cli residue from %s/.local/bin; an unpinned copy may stay resolvable by bare name\n" "$HOME" >&2
fi

# Repair an interrupted dpkg transaction before the listener binds: apt installs
# are the tools engine's, and it cannot repair a database it does not own. Gate on
# the audit OUTPUT as well as its status, because `dpkg --audit` exits 0 while
# reporting unconfigured packages; a non-empty updates journal is a third trigger.
# Warn only.
dpkg_audit_rc=0
dpkg_audit_out=$(timeout --signal=TERM --kill-after=30s 300s dpkg --audit 2>/dev/null) || dpkg_audit_rc=$?
# A truncated first line is enough to name the state.
dpkg_audit_summary=$(printf '%s' "${dpkg_audit_out:0:400}" | tr '\n' ' ')
if [ "$dpkg_audit_rc" -ne 0 ] || [ -n "$dpkg_audit_out" ] \
  || [ -n "$(ls -A /var/lib/dpkg/updates 2>/dev/null)" ]; then
  printf 'level=warn msg="dpkg is in an interrupted state (an install was killed mid-transaction); reconfiguring" audit_rc=%d audit="%s" component=entrypoint\n' \
    "$dpkg_audit_rc" "$(logfmt_value "$dpkg_audit_summary" 400)" >&2
  dpkg_fix_rc=0
  timeout --signal=TERM --kill-after=30s 300s dpkg --configure -a || dpkg_fix_rc=$?
  if [ "$dpkg_fix_rc" -ne 0 ]; then
    printf 'level=warn msg="dpkg --configure -a failed; apt installs will keep failing until the container is recreated" rc=%d component=entrypoint\n' "$dpkg_fix_rc" >&2
  fi
fi

# The server owns the rest of kiro-cli (download, verify, install, select, prune)
# and binds its listener first, so a first-boot download shows as /api/health
# unready with a reason.
exec /app/marotte
