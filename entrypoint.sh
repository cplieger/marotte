#!/bin/bash

CONFIG_DIR="/config"
TOOLS="$CONFIG_DIR/tools"

# kiro-cli pins, tracked by Renovate against
# https://desktop-release.q.us-east-1.amazonaws.com/index.json. The server installs
# from them (internal/composition/kirocli.go) and enforces both per-arch sha256s.
# Keep them bare double-quoted literals under their `# renovate:` anchors: the
# shared custom datasource matches exactly this shape (tests/shell/pins_export_test.sh).
# renovate: datasource=custom.kiro-cli depName=kiro-cli
KIRO_CLI_VERSION="2.32.0"
KIRO_CLI_SHA256="7cea2c90198edc60ccaee9e0d9c9de26902b3824f54a66aa3639a365f7242fbb"
# The `# kiro-cli <version>` trailer is Renovate's anchor for this digest.
# renovate: datasource=custom.kiro-cli-arm64 depName=kiro-cli-arm64
KIRO_CLI_SHA256_ARM64="f66faaa3fa202e7730979e10ad907c43f83220577609c97fdffbb07fc8ef1848" # kiro-cli 2.32.0

# Without the export the server sees no pins and turns its readiness gate off,
# reporting healthy while installing nothing.
export KIRO_CLI_VERSION KIRO_CLI_SHA256 KIRO_CLI_SHA256_ARM64

# Prints the KIRO_HOME kiro-cli honours (2.31.0 acp-server.js P8o/O9u): the trimmed
# value when absolute, not a filesystem root, and neither equal to HOME nor holding
# HOME inside it, else $HOME/.kiro. Symlinks are resolved, and "inside" is tested as
# kiro-cli tests it: a HOME whose relative path from the value starts with ".." (such
# as "..home" directly below it) is not inside. Exported back, so this script, the
# server and every child act on one tree; workspace.ResolveKiroHome is the Go twin.
effective_kiro_home() {
  local v why="" resolved home_resolved shown
  v=${KIRO_HOME:-}
  v=${v#"${v%%[![:space:]]*}"}
  v=${v%"${v##*[![:space:]]}"}
  case "$v" in
    "")
      printf '%s\n' "$HOME/.kiro"
      return 0
      ;;
    /*)
      # -s first: KAS cleans the path lexically, then resolves the longest existing prefix.
      resolved=$(realpath -m -- "$(realpath -sm -- "$v")")
      home_resolved=$(realpath -m -- "$(realpath -sm -- "$HOME")")
      if [ "$resolved" = / ]; then
        why="a filesystem root"
      else
        # O9u's startsWith("..") is a byte-prefix test, so a HOME whose relative path from
        # the candidate starts with ".." (such as "..home" directly below it) is outside it,
        # and kiro-cli honours the candidate.
        case "$home_resolved/" in
          "$resolved/"..*) ;;
          "$resolved/"*) why="it equals or contains HOME" ;;
        esac
      fi
      if [ -z "$why" ]; then
        realpath -sm -- "$v"
        return 0
      fi
      ;;
    *) why="relative" ;;
  esac
  # The value is untrusted: a control character would forge a boot-log line.
  shown=${v//[[:cntrl:]]/?}
  [ "${#shown}" -le 256 ] || shown="${shown:0:256}..."
  printf 'WARNING: ignoring KIRO_HOME=%s (%s), as kiro-cli does; using %s\n' "$shown" "$why" "$HOME/.kiro" >&2
  printf '%s\n' "$HOME/.kiro"
}

KIRO_HOME=$(effective_kiro_home)
export KIRO_HOME

# The tools engine creates its own subtrees; bin/ is created here so PATH never
# races it. kiro-cli-versions is the server's kiro-cli install root, created here so
# this script's /config failure branch covers it. It must stay a sibling of opt/:
# the engine prunes every non-current directory under opt/<tool>.
mkdir -p "$TOOLS/bin" "$TOOLS/kiro-cli-versions" \
  "$HOME/.local/share/kiro-cli" "$HOME/.ssh" \
  "$KIRO_HOME" "$HOME/.cache/go-build" "$HOME/.docker/cli-plugins" \
  || {
    printf "ERROR: failed to create required directories (is /config mounted and writable?)\n"
    sleep 10
    exit 1
  }

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

# Returns 1, with marotte's own refusal line, when a running marotte holds one of the
# folders: this script's deletions below would otherwise run against its live tree
# before marotte's claim (internal/composition/instance_lock.go) refuses. The probe
# keeps no lock, and any failure other than contention leaves the refusal to marotte.
refuse_held_roots() {
  local root rc pid
  for root in "$@"; do
    rc=0
    flock -n -E 75 "$root" true 2>/dev/null || rc=$?
    [ "$rc" -eq 75 ] || continue
    pid=$(head -c 32 "$root/marotte.lock" 2>/dev/null | tr -dc '0-9')
    printf 'level=error msg="another marotte instance holds this folder, so this one is refusing to start" path="%s" holder_pid=%s component=entrypoint\n' \
      "$(logfmt_value "$root")" "${pid:-0}" >&2
    return 1
  done
  return 0
}

refuse_held_roots "$CONFIG_DIR" "$KIRO_HOME" "$TOOLS" || exit 1

# Normalize modes on every boot: an inheritable ACL or umask on the host volume
# can stamp any mode, the tools engine refuses a group- or other-writable root, and
# ssh refuses a lax ~/.ssh.
chmod 755 "$HOME" || true
chmod go-w "$TOOLS" "$TOOLS/bin" "$TOOLS/kiro-cli-versions" \
  "$HOME/.local" "$HOME/.local/share" "$HOME/.local/share/kiro-cli" \
  "$KIRO_HOME" "$HOME/.cache" "$HOME/.cache/go-build" \
  "$HOME/.docker" "$HOME/.docker/cli-plugins" || true
chmod 700 "$HOME/.ssh" || true

# Give login shells (the browser shell runs `bash --login`) the inherited PATH:
# Debian's /etc/profile assigns uid 0 only the system directories, dropping the
# /config entries. profile.d is sourced after that assignment. The Dockerfile's ENV
# stays the source of the value.
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
  printf 'PATH=%s\nexport PATH\n' "$PATH"
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
