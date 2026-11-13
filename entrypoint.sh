#!/bin/bash

# Make an untrusted value safe in a logfmt line: it cannot split the line nor close a quoted field.
# Truncate to $2 (default 200) before doubling backslashes, or the doubling could split a `\\` pair.
logfmt_value() {
  local raw=$1 safe
  safe=${raw:0:${2:-200}}
  safe=${safe//\\/\\\\}
  safe=${safe//[![:print:]]/?}
  safe=${safe//\"/\'}
  printf '%s' "$safe"
}

# A process that recreates /tmp under umask 022 leaves it 0755; a symlinked /tmp is the operator's.
# 01777 needs its five digits: GNU chmod keeps a directory's setgid bit for a four-digit mode.
restore_tmp_mode() {
  local dir=$1 was now msg="the temp folder had the wrong mode; restored it to 1777"
  [ -L "$dir" ] && return 0
  was=$(stat -c %a "$dir" 2>/dev/null) || was=missing
  [ -d "$dir" ] && [ "$was" = 1777 ] && return 0
  [ "$was" = missing ] && msg="the temp folder was missing; created it at mode 1777"
  mkdir -p "$dir" 2>/dev/null && chmod 01777 "$dir" 2>/dev/null
  now=$(stat -c %a "$dir" 2>/dev/null) || now=missing
  if [ -d "$dir" ] && [ "$now" = 1777 ]; then
    printf 'level=warn msg="%s" path="%s" was=%s component=entrypoint\n' "$msg" "$(logfmt_value "$dir")" "$was" >&2
  else
    printf 'level=warn msg="could not restore the temp folder to mode 1777; a process running as another uid may fail to write temporary files" path="%s" was=%s now=%s component=entrypoint\n' \
      "$(logfmt_value "$dir")" "$was" "$now" >&2
  fi
  return 0
}

restore_tmp_mode /tmp

# Sets CONFIG_DIR, TOOLS, KIRO_HOME, PATH, GOPATH and GOBIN from `$1 paths`, the server's own
# derivation (internal/composition/paths.go), so this script acts on the folders the server uses.
# TOOLS is empty when the server rejects the tools folder, which this script then leaves alone.
load_boot_paths() {
  local out
  out=$("$1" paths) || return 1
  eval "$out"
}

# `paths` refuses only a config folder the server would refuse too, and has printed why.
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
KIRO_CLI_VERSION="3.2.1"
KIRO_CLI_SHA256="8338ea19e7065333e25e6f722c8d96c942d426bbc5335b01158ea72b6c4b978a"
# The `# kiro-cli <version>` trailer is Renovate's anchor for this digest.
# renovate: datasource=custom.kiro-cli-arm64 depName=kiro-cli-arm64
KIRO_CLI_SHA256_ARM64="5756c82a88346843b28a9b79a38cabfebea797f10167dd95cc61382438f45020" # kiro-cli 3.2.1

# Without the export the server sees no pins and turns its readiness gate off,
# reporting healthy while installing nothing.
export KIRO_CLI_VERSION KIRO_CLI_SHA256 KIRO_CLI_SHA256_ARM64

# bin/ is made here so PATH never races the engine, which makes its other subtrees, and
# kiro-cli-versions (the server's install root) so this failure branch covers it. That root
# must stay a sibling of opt/: the engine prunes every non-current directory under opt/<tool>.
# The config dir is made even when the tools dir lies elsewhere, or flock would create a file.
# ~/.ssh is born 700, the mode the server holds it at, so a fresh volume logs no change.
# shellcheck disable=SC2153 # load_boot_paths sets TOOLS and KIRO_HOME through eval
if ! mkdir -p "$CONFIG_DIR" ${TOOLS:+"$TOOLS/bin" "$TOOLS/kiro-cli-versions"} \
  "$HOME/.local/share/kiro-cli" \
  "$KIRO_HOME" "$HOME/.cache/go-build" "$HOME/.docker/cli-plugins" \
  || ! { [ -d "$HOME/.ssh" ] || mkdir -m 700 "$HOME/.ssh"; }; then
  printf "ERROR: failed to create required directories (is %s mounted and writable?)\n" "$CONFIG_DIR"
  sleep 10
  exit 1
fi

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

refuse_held_roots "$CONFIG_DIR" "$KIRO_HOME" ${TOOLS:+"$TOOLS"} || exit 1

# Give login shells (the browser shell runs `bash --login`) the inherited PATH:
# Debian's /etc/profile assigns uid 0 only the system directories, dropping the
# /config entries. profile.d is sourced after that assignment. The value is the image's
# ENV as load_boot_paths set it.
emit_path_dropin() {
  printf '%s\n' \
    '# Generated by marotte entrypoint.sh on every boot -- edits are overwritten.' \
    '#' \
    '# WHY THIS IS NOT REDUNDANT WITH THE IMAGE ENV PATH: Debian /etc/profile' \
    '# ASSIGNS PATH rather than appending, and its uid-0 branch (this file is' \
    '# written only when the container runs as root)' \
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

# $1 is the profile.d folder. Through a temp file so a login shell never sources a half-written
# assignment; the mktemp suffix keeps run-parts from matching it. A failed write still succeeds
# when the file already holds this boot's text; a different file is removed, or named in a warning
# when it cannot be, since a login shell would source a previous boot's PATH from it.
install_path_dropin() {
  local dir=$1 target tmp
  target="$dir/10-marotte-path.sh"
  [ -d "$dir" ] || return 1
  if tmp=$(mktemp "$target.XXXXXX"); then
    if emit_path_dropin >"$tmp" && chmod 644 "$tmp" && mv -f "$tmp" "$target"; then
      return 0
    fi
    rm -f "$tmp"
  fi
  [ -e "$target" ] || return 1
  [ -f "$target" ] && [ "$(<"$target")" = "$(emit_path_dropin)" ] && return 0
  rm -f "$target" 2>/dev/null
  [ -e "$target" ] \
    && printf 'WARNING: could not remove %s, written by an earlier boot; a login shell sources its PATH, which may not be this boot'\''s\n' "$target" >&2
  return 1
}

# $1 is the effective uid. Only root may write /etc/profile.d, and Debian's /etc/profile resets
# PATH for every other uid too, so elsewhere a login shell searches no tools folder. Warn only: a
# login shell without the tools PATH is no reason to stop the boot, and with no tools folder only a
# failed drop-in is worth a warning. MAROTTE_PATH_DROPIN=1 tells the server (internal/steering's
# dropInEnv) the drop-in holds this start's PATH.
offer_login_path() {
  unset MAROTTE_PATH_DROPIN
  if [ "$1" -ne 0 ]; then
    [ -z "$TOOLS" ] || printf "WARNING: running as uid %s, not root, so /etc/profile.d/10-marotte-path.sh is not written; a login shell (the browser shell runs bash --login) does not search %s, so run an engine-installed tool there by its full path (%s/<tool>)\n" "$1" "$(logfmt_value "$TOOLS/bin" 4096)" "$(logfmt_value "$TOOLS/bin" 4096)" >&2
  elif install_path_dropin /etc/profile.d; then
    export MAROTTE_PATH_DROPIN=1
  elif [ -n "$TOOLS" ]; then
    printf "WARNING: could not write /etc/profile.d/10-marotte-path.sh; a login shell (the browser shell runs bash --login) may not see %s, so an engine-installed tool may not resolve by name there\n" "$(logfmt_value "$TOOLS/bin" 4096)" >&2
  else
    printf "WARNING: could not write /etc/profile.d/10-marotte-path.sh; a login shell (the browser shell runs bash --login) may not get the boot's PATH\n" >&2
  fi
}

offer_login_path "$EUID"

# One-time migration from the legacy KIRO_HOME (/config/kiro) to the current one;
# only the settings and custom steering doc carry over.
if [ -d /config/kiro ] && [ "$KIRO_HOME" != /config/kiro ]; then
  printf "Migrating legacy kiro state /config/kiro -> %s\n" "$(logfmt_value "$KIRO_HOME")"
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

# Older images left kiro-cli dispatchers in $HOME/.local/bin, which is on PATH and
# on the volume, so a bare `kiro-cli` could resolve to an unpinned copy. It lives
# here because it is outside $TOOLS, the server's tree. An unmatched glob returns 0,
# so a failure is real.
if ! rm -rf "$HOME/.local/bin"/kiro-cli*; then
  printf "WARNING: failed to remove legacy kiro-cli residue from %s/.local/bin; an unpinned copy may stay resolvable by bare name\n" "$(logfmt_value "$HOME")" >&2
fi

# Repair an interrupted dpkg transaction before the listener binds: apt installs are the tools
# engine's, and it cannot repair a database it does not own. Gate on the audit OUTPUT as well as
# its status, because `dpkg --audit` exits 0 while reporting unconfigured packages; a non-empty
# updates journal is a third trigger.
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
# and binds its listener first, so a first-boot download shows as /healthz
# unhealthy with a reason.
exec /app/marotte
