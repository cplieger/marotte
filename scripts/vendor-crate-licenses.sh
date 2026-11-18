#!/bin/sh
# Commit the license text of everything ripgrep's release binaries link that their tarballs do
# not ship: every crate in ripgrep's Cargo.lock, the PCRE2 and jemalloc sources two crates bundle,
# the Rust standard library and the musl libc the amd64 build is static against.
# usage: sh scripts/vendor-crate-licenses.sh [--dockerfile FILE] [--out DIR], from the repo root.
# Re-run it on every ripgrep bump (Renovate runs it in the bump's commit): the Dockerfile's
# ripgrep stage refuses a MANIFEST whose first line names another version.
set -eu

DOCKERFILE=Dockerfile
OUT=licenses/crates
REGISTRY='registry+https://github.com/rust-lang/crates.io-index'

while [ $# -gt 0 ]; do
  case "$1" in
    --dockerfile)
      DOCKERFILE="${2:?--dockerfile needs a file}"
      shift 2
      ;;
    --out)
      OUT="${2:?--out needs a directory}"
      shift 2
      ;;
    *)
      printf 'vendor-crate-licenses: unknown option %s\n' "$1" >&2
      exit 2
      ;;
  esac
done

die() {
  printf 'vendor-crate-licenses: %s\n' "$*" >&2
  exit 1
}

arg() {
  value=$(awk -F= -v key="ARG $1" '$1 == key { print $2; exit }' "$DOCKERFILE")
  [ -n "$value" ] || die "no \"ARG $1=\" line in $DOCKERFILE"
  printf '%s\n' "$value"
}

version=$(arg RIPGREP_VERSION)

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM HUP
stage="$work/out"
mkdir -p "$stage"

fetch() {
  curl --proto '=https' --proto-redir '=https' -fsSL --connect-timeout 10 --max-time 120 --retry 3 --retry-delay 5 -o "$1" "$2"
}

# copy_text SRC DEST_DIR: SRC into DEST_DIR with LF line ends, so the committed tree is the same
# whether it is committed locally (through .gitattributes) or through the GitHub API.
copy_text() {
  mkdir -p "$2"
  tr -d '\r' <"$1" >"$2/${1##*/}"
}

# unpack NAME VERSION: the crate's source tree under $work/x, its root in $root.
unpack() {
  rm -rf "$work/x"
  mkdir -p "$work/x"
  fetch "$work/crate.tar.gz" "https://static.crates.io/crates/$1/$1-$2.crate"
  tar -xzf "$work/crate.tar.gz" -C "$work/x"
  root="$work/x/$1-$2"
  [ -d "$root" ] || die "$1-$2.crate does not unpack to $1-$2/"
}

fetch "$work/Cargo.lock" "https://raw.githubusercontent.com/BurntSushi/ripgrep/$version/Cargo.lock"

# Every [[package]] with a source, which is every crate outside ripgrep's own workspace; a crate
# only another platform links keeps its text, since an extra file costs bytes and a missing one
# is a breach.
awk '
BEGIN { RS = "" }
$1 != "[[package]]" { next }
{
  name = ""; ver = ""; src = ""
  for (i = 2; i < NF; i++) {
    if ($i != "=") { continue }
    key = $(i - 1); val = $(i + 1)
    gsub(/^"|",?$/, "", val)
    if (key == "name") { name = val }
    else if (key == "version") { ver = val }
    else if (key == "source") { src = val }
  }
  if (name != "" && ver != "" && src != "") { print name, ver, src }
}
' "$work/Cargo.lock" | sort >"$work/crates"
[ -s "$work/crates" ] || die "ripgrep $version's Cargo.lock lists no dependency crate"

printf 'ripgrep %s\n' "$version" >"$stage/MANIFEST"
missing=""
pcre2=""
jemalloc=""
while read -r name ver src; do
  [ "$src" = "$REGISTRY" ] || die "$name $ver does not come from crates.io ($src)"
  case "$name$ver" in
    *[!A-Za-z0-9._+-]*) die "\"$name $ver\" is not a single path component" ;;
  esac
  unpack "$name" "$ver"
  copied=0
  for f in "$root"/*; do
    [ -f "$f" ] || continue
    case "$(printf '%s\n' "${f##*/}" | tr '[:lower:]' '[:upper:]')" in
      LICENSE* | LICENCE* | COPYING* | NOTICE* | UNLICENSE*) ;;
      *) continue ;;
    esac
    copy_text "$f" "$stage/$name"
    copied=$((copied + 1))
  done
  # r-efi, for one, carries its license texts in AUTHORS and ships no LICENSE file.
  if [ "$copied" -eq 0 ] && [ -f "$root/AUTHORS" ]; then
    copy_text "$root/AUTHORS" "$stage/$name"
    copied=1
  fi
  if [ "$copied" -eq 0 ]; then
    missing="$missing
  $name $ver"
    continue
  fi
  case "$name" in
    pcre2-sys)
      major=$(awk '$2 == "PCRE2_MAJOR" { print $3 }' "$root/upstream/include/pcre2.h")
      minor=$(awk '$2 == "PCRE2_MINOR" { print $3 }' "$root/upstream/include/pcre2.h")
      pcre2="$major.$minor"
      ;;
    tikv-jemalloc-sys)
      jemalloc=${ver#*+}
      jemalloc=${jemalloc%%-*}
      copy_text "$root/jemalloc/COPYING" "$stage/jemalloc"
      printf 'jemalloc %s\n' "$jemalloc" >>"$stage/MANIFEST.extra"
      ;;
  esac
  printf '%s %s\n' "$name" "$ver" >>"$stage/MANIFEST"
done <"$work/crates"
[ -z "$missing" ] || die "no LICENSE*, LICENCE*, COPYING*, NOTICE* or UNLICENSE* file at the crate root of:$missing"
[ -n "$pcre2" ] || die "ripgrep $version links no pcre2-sys, so the PCRE2 step below is stale"
[ -n "$jemalloc" ] || die "ripgrep $version links no tikv-jemalloc-sys, so the jemalloc step above is stale"

# PCRE2's own license, which the pcre2-sys crate bundles the source of but not the text of.
fetch "$work/LICENCE.md" "https://raw.githubusercontent.com/PCRE2Project/pcre2/pcre2-$pcre2/LICENCE.md" \
  || fetch "$work/LICENCE.md" "https://raw.githubusercontent.com/PCRE2Project/pcre2/pcre2-$pcre2/LICENCE"
copy_text "$work/LICENCE.md" "$stage/libpcre2"
printf 'libpcre2 %s\n' "$pcre2" >>"$stage/MANIFEST.extra"

# The Rust standard library is linked into each binary, built by the rustc commit the binary
# names; both architectures' commits must carry the same license text.
rust=""
for target in x86_64-unknown-linux-musl:RIPGREP_AMD64_SHA256 aarch64-unknown-linux-gnu:RIPGREP_ARM64_SHA256; do
  name="ripgrep-$version-${target%%:*}"
  fetch "$work/rg.tar.gz" "https://github.com/BurntSushi/ripgrep/releases/download/$version/$name.tar.gz"
  printf '%s  %s\n' "$(arg "${target#*:}")" "$work/rg.tar.gz" | sha256sum -c - >/dev/null \
    || die "$name.tar.gz does not match ${target#*:}; run sh scripts/repin-sha.sh BurntSushi/ripgrep $version first"
  tar -xzf "$work/rg.tar.gz" -C "$work"
  commit=$(grep -a -o 'rustc/[0-9a-f]\{40\}' "$work/$name/rg" | sort -u)
  case "$commit" in
    rustc/*[!0-9a-f/]* | *"
"* | "") die "$name/rg names no single rustc commit: $commit" ;;
  esac
  commit=${commit#rustc/}
  mkdir -p "$work/rust-$commit"
  for f in COPYRIGHT LICENSE-APACHE LICENSE-MIT; do
    fetch "$work/rust-$commit/$f" "https://raw.githubusercontent.com/rust-lang/rust/$commit/$f"
  done
  if [ -z "$rust" ]; then
    rust=$commit
    for f in "$work/rust-$commit"/*; do copy_text "$f" "$stage/rust-std"; done
    # The amd64 build is static against the musl its rustc ships (dist-x86_64-musl).
    fetch "$work/musl-toolchain.sh" "https://raw.githubusercontent.com/rust-lang/rust/$commit/src/ci/docker/scripts/musl-toolchain.sh"
    musl=$(grep -o 'MUSL_VER=[0-9][0-9.]*' "$work/musl-toolchain.sh" | sort -u)
    case "$musl" in
      *"
"* | "") die "rust-lang/rust $commit musl-toolchain.sh names no single MUSL_VER: $musl" ;;
    esac
    musl=${musl#MUSL_VER=}
    fetch "$work/COPYRIGHT" "https://git.musl-libc.org/cgit/musl/plain/COPYRIGHT?h=v$musl"
    copy_text "$work/COPYRIGHT" "$stage/musl"
    printf 'musl %s\n' "$musl" >>"$stage/MANIFEST.extra"
  elif ! diff -r "$work/rust-$rust" "$work/rust-$commit" >/dev/null; then
    die "rust-lang/rust $rust and $commit differ in COPYRIGHT or LICENSE-*; vendor both by hand"
  fi
  rust_commits="${rust_commits:+$rust_commits,}$commit"
done
printf 'rust-std %s\n' "$rust_commits" >>"$stage/MANIFEST.extra"

sort "$stage/MANIFEST.extra" >>"$stage/MANIFEST"
rm "$stage/MANIFEST.extra"
rm -rf "$OUT"
mkdir -p "$OUT"
cp -R "$stage/." "$OUT/"
printf 'vendor-crate-licenses: %s entries under %s (ripgrep %s)\n' "$(($(wc -l <"$OUT/MANIFEST") - 1))" "$OUT" "$version"
