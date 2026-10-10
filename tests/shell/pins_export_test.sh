#!/usr/bin/env bash
# The shell/Go boundary: entrypoint.sh declares the kiro-cli pins and the Go server installs from
# them, agreeing only by name, and every break is SILENT (a dropped export installs nothing behind a
# healthy check; a renamed literal stops Renovate bumps; a moved install root is never created).
# So each assertion reads BOTH sides: the shipped entrypoint and the Go file consuming it.
# Lint directives:
#   SC2015 - `[ cond ] && ok || no` cannot mis-fire: lib.sh's ok/no always return 0.
#   SC2016 - the grep patterns match LITERAL text and stay single-quoted.
# shellcheck disable=SC2015,SC2016
set -u

# shellcheck source-path=SCRIPTDIR
. "$(dirname -- "$0")/lib.sh"
new_workdir >/dev/null

REPO=$(cd -- "$(dirname -- "$0")/../.." && pwd)
# Overridable like lib.sh's $ENTRYPOINT, so a maintainer can red-check the Go-side assertions
# against a mutated /tmp copy.
CONFIG_GO="${MAROTTE_CONFIG_GO:-$REPO/internal/composition/config.go}"
KIROCLI_GO="${MAROTTE_KIROCLI_GO:-$REPO/internal/composition/kirocli.go}"
GO_SOURCE_ROOT="${MAROTTE_GO_SOURCE_ROOT:-$REPO}"
README="${MAROTTE_README:-$REPO/README.md}"
# Fatal for the section: an unreadable config.go would fail every cross-file assertion as drift.
if [ ! -r "$CONFIG_GO" ]; then
  printf 'harness error: internal/composition/config.go is not readable at %s\n' "$CONFIG_GO" >&2
  exit 1
fi
if [ ! -r "$KIROCLI_GO" ]; then
  printf 'harness error: internal/composition/kirocli.go is not readable at %s\n' "$KIROCLI_GO" >&2
  exit 1
fi
if [ ! -r "$README" ] || [ ! -d "$GO_SOURCE_ROOT" ]; then
  printf 'harness error: README (%s) or Go source root (%s) is unreadable\n' "$README" "$GO_SOURCE_ROOT" >&2
  exit 1
fi

# --- the Renovate literals still look the way the datasource expects ----------
# cplieger/.github's customDatasources match these exact anchors: version + amd64 sha are rewritten
# as a pair, and the arm64 sha's `# kiro-cli <version>` trailer is its own anchor.
grep -q '^# renovate: datasource=custom.kiro-cli depName=kiro-cli$' "$ENTRYPOINT" \
  && ok "the amd64 Renovate anchor comment is intact" \
  || no "amd64 Renovate anchor" "the '# renovate: datasource=custom.kiro-cli depName=kiro-cli' line is gone; the version + sha pair will stop being bumped"

grep -q '^# renovate: datasource=custom.kiro-cli-arm64 depName=kiro-cli-arm64$' "$ENTRYPOINT" \
  && ok "the arm64 Renovate anchor comment is intact" \
  || no "arm64 Renovate anchor" "the '# renovate: datasource=custom.kiro-cli-arm64 depName=kiro-cli-arm64' line is gone"

PIN_VERSION=$(sed -n 's/^KIRO_CLI_VERSION="\([^"]*\)"$/\1/p' "$ENTRYPOINT")
[ -n "$PIN_VERSION" ] \
  && ok "KIRO_CLI_VERSION is a bare double-quoted literal ($PIN_VERSION)" \
  || no "KIRO_CLI_VERSION literal" "no 'KIRO_CLI_VERSION=\"...\"' line; the datasource's rewrite target is gone"

grep -qE '^KIRO_CLI_SHA256="[0-9a-f]{64}"$' "$ENTRYPOINT" \
  && ok "KIRO_CLI_SHA256 is a bare 64-hex literal" \
  || no "KIRO_CLI_SHA256 literal" "not a bare 64-hex double-quoted literal"

# A stale arm64 trailer sends the datasource to the wrong release's digest.
grep -qE "^KIRO_CLI_SHA256_ARM64=\"[0-9a-f]{64}\" # kiro-cli ${PIN_VERSION}\$" "$ENTRYPOINT" \
  && ok "KIRO_CLI_SHA256_ARM64 carries a 64-hex literal and a trailer naming the pinned version" \
  || no "KIRO_CLI_SHA256_ARM64 literal" "missing the 64-hex value or the '# kiro-cli $PIN_VERSION' trailer"

# --- every pin the server needs is exported, and read under the same name -----
# Sourcing is not an option (it creates /config and execs the server), so the export is read from
# the text, paired with the Go read.
for var in KIRO_CLI_VERSION KIRO_CLI_SHA256 KIRO_CLI_SHA256_ARM64; do
  if grep -qE "^export ([A-Z0-9_]+ )*${var}( |$)" "$ENTRYPOINT"; then
    ok "$var is exported to the server"
  else
    no "$var export" "entrypoint.sh never exports it, so the server falls back to bare-name kiro-cli with its readiness gate off"
  fi
  if grep -q "\"$var\"" "$CONFIG_GO"; then
    ok "$var is read by config.go under that exact name"
  else
    no "$var read" "internal/composition/config.go does not mention \"$var\"; the exported pin reaches nothing"
  fi
done

# --- KIRO_CLI_PATH is GONE, on both surfaces --------------------------------
# KIRO_CLI_PATH made marotte run a binary verbatim and stop reporting kiro-cli readiness; re-adding
# the read would bring that back SILENTLY, so its absence is asserted in the Go sources and the
# README table. Dot-directories are skipped: not source, and a stale scratch copy would fail this.
if grep -rq --include='*.go' --exclude-dir='.*' 'KIRO_CLI_PATH' "$GO_SOURCE_ROOT"; then
  no "KIRO_CLI_PATH in Go sources" "a Go source still mentions KIRO_CLI_PATH; reading it stands the install manager down and takes kiro-cli readiness off /api/health"
else
  ok "no Go source mentions KIRO_CLI_PATH: the install manager is the only source of the binary path"
fi

grep -q 'KIRO_CLI_PATH' "$README" \
  && no "KIRO_CLI_PATH in the README" "the README still documents KIRO_CLI_PATH; an operator setting a variable marotte ignores gets no error and no managed install either" \
  || ok "the README config table does not offer KIRO_CLI_PATH"

# --- the tools tree both halves compute independently -------------------------
# The tools dir is NOT exported: the entrypoint derives it from CONFIG_DIR, the server from
# KIRO_CONFIG_DIR, so the two derivations are what this pins.
grep -q '^TOOLS="\$CONFIG_DIR/tools"$' "$ENTRYPOINT" \
  && ok "the entrypoint derives \$TOOLS as \$CONFIG_DIR/tools" \
  || no "entrypoint tools dir" "TOOLS is no longer \$CONFIG_DIR/tools, so it may not be the tree the server installs into"

# envx/v2's String() takes no fallback (composed with cmp.Or); loose about the wrapper, so it pins
# the <configDir>/tools DERIVATION.
grep -qF 'envx.String("MAROTTE_TOOLS_DIR")' "$CONFIG_GO" \
  && grep -qF 'filepath.Join(configDir, "tools")' "$CONFIG_GO" \
  && ok "the server derives the same tools dir from its config dir" \
  || no "server tools dir" "config.go no longer defaults ToolsDir to <configDir>/tools; the server may install outside the tree this script created"

# --- the install root is created here, before the server writes into it -------
# A SIBLING of the toolbelt engine's opt/ tree: its per-tool prune deletes every non-current
# version under opt/<tool>, for any manifest name.
grep -qF 'mkdir -p "$TOOLS/bin" "$TOOLS/kiro-cli-versions"' "$ENTRYPOINT" \
  && ok "the boot mkdir creates \$TOOLS/kiro-cli-versions, the version-install root" \
  || no "install root mkdir" "the install root is not created by the boot mkdir, so a fresh volume's first install has to create it inside the server, outside this script's /config failure branch"

grep -qE 'mkdir -p [^&|]*"\$TOOLS/(opt|npm|python|bin)/' "$ENTRYPOINT" \
  && no "install root under a toolbelt tree" "the boot creates a directory INSIDE one of the toolbelt engine's own trees (opt/npm/python/bin); the engine's per-tool prune and bin republish own everything under those" \
  || ok "the boot creates nothing inside a toolbelt-engine tree, so the engine's prune cannot reach the kiro-cli install"

# --- the shell installer is GONE, not merely unused --------------------------
# Two installers on one volume fight (the server PURGES $TOOLS/bin/kiro-cli*), so a dormant shell
# installer's absence is asserted.
for gone in install_kiro_cli needs_kiro_cli_install kiro_cli_version \
  is_self_contained_executable kiro_setting; do
  grep -q "^${gone}()" "$ENTRYPOINT" \
    && no "$gone removed" "entrypoint.sh still defines $gone(); the server owns the install now, and two installers write the same tree" \
    || ok "$gone() is gone from the entrypoint"
done

# The kiro-cli function that stays: it prunes the DATA dir's agent-runtime trees, before the server
# unpacks a new one.
grep -q '^prune_superseded_kas_runtimes()' "$ENTRYPOINT" \
  && ok "prune_superseded_kas_runtimes() stayed: it prunes the data dir, not the install" \
  || no "kas pruner" "the agent-runtime pruner is gone; nothing reclaims the ~240 MB tree each superseded version leaves on the volume"

# --- the taint flag: absent on BOTH sides, deliberately ----------------------
# KIRO_CLI_TOOLS_TAINTED: web-terminal-kiro exports it when hardening finds the tools tree
# writable by others. marotte has no hardening pass, so it neither exports nor reads it; either half
# alone is worse (a no-op guard, or every boot reported clean). Grow hardening, wire both ends.
if grep -q 'KIRO_CLI_TOOLS_TAINTED' "$ENTRYPOINT" || grep -q 'KIRO_CLI_TOOLS_TAINTED' "$CONFIG_GO"; then
  grep -q 'KIRO_CLI_TOOLS_TAINTED' "$ENTRYPOINT" && grep -q 'KIRO_CLI_TOOLS_TAINTED' "$CONFIG_GO" \
    && ok "the taint flag is exported by the entrypoint AND read by config.go" \
    || no "taint flag half-wired" "only one side mentions KIRO_CLI_TOOLS_TAINTED: an export nothing reads is a no-op that looks like a guard, and a read with no producer reports every boot as clean"
else
  ok "neither side claims a taint observation marotte cannot make (no hardening pass here)"
fi

# --- the LIBRARY half of the same non-claim: pinstall.Untrusted --------------
# The pinstall.Config field the env var would feed (Untrusted since pinstall v2; grepping the v1
# spelling Tainted would pass vacuously). Setting it without a hardening pass is a guard with no producer.
# kirocli.go CITES this assertion.
if grep -q 'Untrusted:' "$KIROCLI_GO"; then
  no "Untrusted claimed" "internal/composition/kirocli.go now sets pinstall.Untrusted, but marotte has no hardening pass to make that observation: the field then reports every boot as clean while looking like a check"
else
  ok "the Go side sets no pinstall.Untrusted (the observation has no producer here)"
fi

report
