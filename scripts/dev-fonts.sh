#!/usr/bin/env bash
# Fetch the terminal's web fonts into static/vendor/fonts/ for local development
# (static/vendor/ is gitignored and filled by the Dockerfile in the image build).
#
# Every URL, destination name and digest is read from the Dockerfile's `# repin:`
# markers, so the pins have one copy. The cache key is the digest set, and
# `.complete` is written last: only it makes a cache dir usable.
set -euo pipefail

cd -- "$(dirname -- "$0")/.."

dockerfile=Dockerfile
dest_dir=static/vendor/fonts

# One row per marker: <dep> <version-arg> <dest> <sha-arg> <url-template>; `dest=`
# overrides the URL basename where two projects would both land a LICENSE.
rows=$(awk '
	/^#[[:space:]]*repin:/ {
		dep = ""; url = ""; dest = ""
		for (i = 1; i <= NF; i++) {
			if ($i ~ /^dep=/)  { dep  = substr($i, 5) }
			if ($i ~ /^url=/)  { url  = substr($i, 5) }
			if ($i ~ /^dest=/) { dest = substr($i, 6) }
		}
		if (dep == "" || url == "") { next }
		pending_dep = dep; pending_url = url; pending_dest = dest
		next
	}
	pending_dep != "" && /^ARG[[:space:]]+[A-Z0-9_]+=/ {
		split($0, a, "=")
		name = a[1]; sub(/^ARG[[:space:]]+/, "", name)
		if (pending_dest == "") {
			n = split(pending_url, seg, "/")
			pending_dest = seg[n]
		}
		printf "%s\t%s\t%s\t%s\n", pending_dep, pending_dest, name, pending_url
		pending_dep = ""; next
	}
	pending_dep != "" { pending_dep = "" }
' "$dockerfile")

[ -n "$rows" ] || {
  printf 'dev-fonts: no repin markers in %s\n' "$dockerfile" >&2
  exit 1
}

arg_value() {
  sed -n -E "s/^ARG $1=([^[:space:]#]+).*/\\1/p" "$dockerfile" | head -1
}

version_arg_for() {
  case $1 in
    githubnext/monaspace) printf 'MONASPACE_VERSION\n' ;;
    cplieger/web-terminal-glyphs) printf 'WEB_TERMINAL_GLYPHS_VERSION\n' ;;
    *) return 1 ;;
  esac
}

# Only the font rows: the Dockerfile's other repin markers pin npm tarballs the
# frontend build fetches itself.
font_rows=""
while IFS=$'\t' read -r dep dest sha_arg url; do
  version_arg_for "$dep" >/dev/null 2>&1 || continue
  font_rows="${font_rows}${dep}	${dest}	${sha_arg}	${url}
"
done <<EOF
$rows
EOF

[ -n "$font_rows" ] || {
  printf 'dev-fonts: no font pins found in %s\n' "$dockerfile" >&2
  exit 1
}

key=$(printf '%s' "$font_rows" | while IFS=$'\t' read -r _ _ sha_arg _; do arg_value "$sha_arg"; done | sha256sum | cut -c1-16)
cache="${XDG_CACHE_HOME:-$HOME/.cache}/marotte-fonts/$key"

# The digest list (`sha256sum -c` form) admits a cached tree and verifies a fresh
# fetch, so a rotted cached file is re-fetched.
manifest=$(
  while IFS=$'\t' read -r dep dest sha_arg _; do
    [ -n "$dep" ] || continue
    sha=$(arg_value "$sha_arg")
    [ -n "$sha" ] || {
      printf 'dev-fonts: %s: no digest at ARG %s\n' "$dest" "$sha_arg" >&2
      exit 1
    }
    printf '%s  %s/%s\n' "$sha" "$cache" "$dest"
  done <<EOF
$font_rows
EOF
)

verified() {
  [ -f "$cache/.complete" ] || return 1
  printf '%s\n' "$manifest" | sha256sum -c --status - 2>/dev/null
}

if ! verified; then
  rm -rf -- "$cache"
  mkdir -p -- "$cache"
  while IFS=$'\t' read -r dep dest sha_arg url; do
    [ -n "$dep" ] || continue
    version=$(arg_value "$(version_arg_for "$dep")")
    [ -n "$version" ] || {
      printf 'dev-fonts: %s: no version for %s\n' "$dest" "$dep" >&2
      exit 1
    }
    printf 'dev-fonts: %s\n' "$dest"
    curl --proto '=https' --proto-redir '=https' --tlsv1.2 \
      --connect-timeout 20 --max-time 300 --retry 3 --retry-delay 5 -fsSL \
      -o "$cache/$dest" "${url//\{version\}/$version}"
  done <<EOF
$font_rows
EOF
  printf '%s\n' "$manifest" | sha256sum -c - >/dev/null
  : >"$cache/.complete"
fi

mkdir -p -- "$dest_dir"
while IFS=$'\t' read -r _ dest _ _; do
  [ -n "$dest" ] || continue
  cp -- "$cache/$dest" "$dest_dir/$dest"
done <<EOF
$font_rows
EOF

printf 'dev-fonts: %s ready\n' "$dest_dir"
